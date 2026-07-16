package main

import "fmt"

// IRGen walks the AST and mechanically emits TAC. One IRGen lowers one
// function body (or the top-level statement list, treated the same way).
type IRGen struct {
	Instrs      []Instr
	Unsupported []string // human-readable notes on skipped statements

	tempN, labelN int
	loopStack     []loopLabels
}

type loopLabels struct{ breakL, continueL string }

func NewIRGen() *IRGen { return &IRGen{} }

func (g *IRGen) newTemp() TempOperand {
	t := TempOperand{ID: g.tempN}
	g.tempN++
	return t
}

func (g *IRGen) newLabel(prefix string) string {
	l := fmt.Sprintf("%s%d", prefix, g.labelN)
	g.labelN++
	return l
}

func (g *IRGen) emit(in Instr) { g.Instrs = append(g.Instrs, in) }

func (g *IRGen) skip(line, col int, why string) {
	g.Unsupported = append(g.Unsupported, fmt.Sprintf("%d:%d: not lowered to IR (%s) -- runs only in the tree-walking interpreter", line, col, why))
}

// LowerStatements lowers a top-level or function-body statement list.
func (g *IRGen) LowerStatements(stmts []Stmt) {
	for _, s := range stmts {
		g.lowerStmt(s)
	}
}

// ---- Statements -------------------------------------------------------

func (g *IRGen) lowerStmt(s Stmt) {
	line, col := s.Pos()
	switch n := s.(type) {
	case *ConstDecl:
		g.lowerSimpleDecl(n.Name, n.Value, line, col)

	case *LetDecl:
		g.lowerSimpleDecl(n.Name, n.Value, line, col)

	case *AssignStmt:
		if n.Target.Kind != TargetIdent || len(n.Target.Suffixes) > 0 || n.Target.Deref > 0 {
			g.skip(line, col, "assignment target is not a plain identifier (struct/array/pointer lvalue)")
			return
		}
		ev, ok := n.Value.(*ExprValue)
		if !ok {
			g.skip(line, col, "assigned value is not a plain expression")
			return
		}
		src := g.lowerExpr(ev.Expr)
		if src == nil {
			return
		}
		g.emit(Instr{Op: OpStoreVar, Dst: VarOperand{Name: n.Target.Name}, Src1: src})

	case *Block:
		g.LowerStatements(n.Statements)

	case *IfStmt:
		g.lowerIf(n)

	case *ForWhileStmt:
		g.lowerForWhile(n)

	case *BreakStmt:
		if len(g.loopStack) == 0 {
			g.skip(line, col, "'break' outside a lowered loop")
			return
		}
		g.emit(Instr{Op: OpGoto, Label: g.loopStack[len(g.loopStack)-1].breakL})

	case *ContinueStmt:
		if len(g.loopStack) == 0 {
			g.skip(line, col, "'continue' outside a lowered loop")
			return
		}
		g.emit(Instr{Op: OpGoto, Label: g.loopStack[len(g.loopStack)-1].continueL})

	case *PrintStmt:
		args := make([]Operand, 0, len(n.Args))
		for _, a := range n.Args {
			op := g.lowerExpr(a)
			if op == nil {
				g.skip(line, col, "print argument uses an unsupported expression form")
				return
			}
			args = append(args, op)
		}
		g.emit(Instr{Op: OpPrint, Args: args})

	case *ReturnStmt:
		if n.Value == nil {
			g.emit(Instr{Op: OpReturn})
			return
		}
		v := g.lowerExpr(n.Value)
		if v == nil {
			g.skip(line, col, "return value uses an unsupported expression form")
			return
		}
		g.emit(Instr{Op: OpReturn, Src1: v})

	default:
		g.skip(line, col, fmt.Sprintf("statement type %T is outside the IR's arithmetic/control-flow subset", s))
	}
}

func (g *IRGen) lowerSimpleDecl(name string, val Value, line, col int) {
	ev, ok := val.(*ExprValue)
	if !ok {
		g.skip(line, col, fmt.Sprintf("%q's initializer is not a plain expression (array/struct/enum/fn literal)", name))
		return
	}
	src := g.lowerExpr(ev.Expr)
	if src == nil {
		return
	}
	g.emit(Instr{Op: OpStoreVar, Dst: VarOperand{Name: name}, Src1: src})
}

func (g *IRGen) lowerIf(n *IfStmt) {
	cond := g.lowerExpr(n.Cond)
	if cond == nil {
		line, col := n.Pos()
		g.skip(line, col, "if-condition uses an unsupported expression form")
		return
	}
	elseL := g.newLabel("L")
	g.emit(Instr{Op: OpIfFalseGoto, Src1: cond, Label: elseL})
	g.lowerStmt(n.Then)

	if n.ElseIf != nil || n.Else != nil {
		endL := g.newLabel("L")
		g.emit(Instr{Op: OpGoto, Label: endL})
		g.emit(Instr{Op: OpLabel, Label: elseL})
		if n.ElseIf != nil {
			g.lowerStmt(n.ElseIf)
		} else {
			g.lowerStmt(n.Else)
		}
		g.emit(Instr{Op: OpLabel, Label: endL})
	} else {
		g.emit(Instr{Op: OpLabel, Label: elseL})
	}
}

func (g *IRGen) lowerForWhile(n *ForWhileStmt) {
	startL := g.newLabel("L")
	endL := g.newLabel("L")

	g.emit(Instr{Op: OpLabel, Label: startL})
	cond := g.lowerExpr(n.Cond)
	if cond == nil {
		line, col := n.Pos()
		g.skip(line, col, "for-while condition uses an unsupported expression form")
		return
	}
	g.emit(Instr{Op: OpIfFalseGoto, Src1: cond, Label: endL})

	g.loopStack = append(g.loopStack, loopLabels{breakL: endL, continueL: startL})
	g.lowerStmt(n.Body)
	g.loopStack = g.loopStack[:len(g.loopStack)-1]

	g.emit(Instr{Op: OpGoto, Label: startL})
	g.emit(Instr{Op: OpLabel, Label: endL})
}

// ---- Expressions --------------------------------------------------------

// lowerExpr returns nil (and records nothing itself -- callers decide
// whether/how to report) when the expression falls outside the IR's
// scalar arithmetic/logic subset.
func (g *IRGen) lowerExpr(e Expr) Operand {
	switch n := e.(type) {
	case *Literal:
		return ConstOperand{Val: evalLiteral(n)}

	case *Identifier:
		return VarOperand{Name: n.Name}

	case *GroupExpr:
		return g.lowerExpr(n.Inner)

	case *UnaryExpr:
		if n.Op != "-" && n.Op != "!" {
			return nil // '++'/'--'/'&'/'*' need heap semantics, not lowered
		}
		operand := g.lowerExpr(n.Operand)
		if operand == nil {
			return nil
		}
		dst := g.newTemp()
		op := OpNeg
		if n.Op == "!" {
			op = OpNot
		}
		g.emit(Instr{Op: op, Dst: dst, Src1: operand})
		return dst

	case *BinaryExpr:
		left := g.lowerExpr(n.Left)
		right := g.lowerExpr(n.Right)
		if left == nil || right == nil {
			return nil
		}
		opc, ok := binOpCode(n.Op)
		if !ok {
			return nil
		}
		dst := g.newTemp()
		g.emit(Instr{Op: opc, Dst: dst, Src1: left, Src2: right})
		return dst
	}
	// FnCall, MethodCall, IndexExpr, MemberAccess, ThisExpr, InputExpr:
	// all require the heap/environment model and are out of scope for TAC.
	return nil
}

func binOpCode(op string) (OpCode, bool) {
	switch op {
	case "+":
		return OpAdd, true
	case "-":
		return OpSub, true
	case "*":
		return OpMul, true
	case "/":
		return OpDiv, true
	case "%":
		return OpMod, true
	case "<":
		return OpLt, true
	case ">":
		return OpGt, true
	case "<=":
		return OpLe, true
	case ">=":
		return OpGe, true
	case "==":
		return OpEq, true
	case "!=":
		return OpNe, true
	case "&&":
		return OpAnd, true
	case "||":
		return OpOr, true
	}
	return "", false
}
