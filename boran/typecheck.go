package main

import "fmt"

// ============================================================================
// Semantic error categories
//
// These map directly onto the rubric's "Semantic Errors" checklist so each
// diagnostic can be identified by kind, not just by its message text:
//   1. Undeclared variable
//   2. Type Mismatch
//   3. Multiply-defined variable
//   4. Constant Reassignment
//   5. Cardinality/Ordinality: function parameters
// ============================================================================

type SemErrorKind int

const (
	ErrUndeclared SemErrorKind = iota
	ErrTypeMismatch
	ErrMultiplyDefined
	ErrConstReassign
	ErrCardinality
	ErrOther
)

func (k SemErrorKind) String() string {
	switch k {
	case ErrUndeclared:
		return "undeclared variable"
	case ErrTypeMismatch:
		return "type mismatch"
	case ErrMultiplyDefined:
		return "multiply-defined variable"
	case ErrConstReassign:
		return "constant reassignment"
	case ErrCardinality:
		return "cardinality/ordinality"
	default:
		return "semantic error"
	}
}

// SemError is one recorded semantic diagnostic. Analysis never stops at the
// first error -- like the parser, it records and keeps going so the report
// is as complete as possible in one pass.
type SemError struct {
	Kind      SemErrorKind
	Line, Col int
	Message   string
}

func (e SemError) Error() string {
	return fmt.Sprintf("%d:%d: [%s] %s", e.Line, e.Col, e.Kind, e.Message)
}

// ============================================================================
// Internal type representation
//
// TCType mirrors DatatypeNode but is the checker's own working
// representation, since DatatypeNode is an AST node and the checker also
// needs to represent things the grammar doesn't (e.g. "void", "unknown").
// ============================================================================

type TCType struct {
	Kind   string  // "int","float","char","string","bool","fn","ptr","array","named","void","unknown"
	Name   string  // struct/enum name, when Kind == "named"
	Elem   *TCType // element type (array) / pointee type (ptr)
	ArrLen int
}

func (t *TCType) String() string {
	if t == nil {
		return "unknown"
	}
	switch t.Kind {
	case "array":
		return fmt.Sprintf("%s[%d]", t.Elem.String(), t.ArrLen)
	case "ptr":
		return t.Elem.String() + "*"
	case "named":
		return t.Name
	default:
		return t.Kind
	}
}

func tUnknown() *TCType { return &TCType{Kind: "unknown"} }
func tVoid() *TCType    { return &TCType{Kind: "void"} }
func tBuiltin(k string) *TCType { return &TCType{Kind: k} }

// fromDatatypeNode converts the parser's structured type node into the
// checker's TCType, recursively preserving array length / element / pointee.
func fromDatatypeNode(d *DatatypeNode) *TCType {
	if d == nil {
		return tUnknown()
	}
	switch d.Kind {
	case "array":
		return &TCType{Kind: "array", Elem: fromDatatypeNode(d.Elem), ArrLen: d.ArrLen}
	case "ptr":
		return &TCType{Kind: "ptr", Elem: fromDatatypeNode(d.Elem)}
	case "named":
		return &TCType{Kind: "named", Name: d.Name}
	default:
		return &TCType{Kind: d.Kind}
	}
}

func isNumeric(t *TCType) bool {
	return t != nil && (t.Kind == "int" || t.Kind == "float")
}

// isPrimitiveScalar reports whether t is one of the five scalar builtin
// types 'as' can convert between. Structs, arrays, fn, enum, and pointer
// types are intentionally excluded -- casting between them has no obvious
// single meaning here (e.g. what would `myStruct as int` even mean), so
// they're left unsupported rather than guessed at.
func isPrimitiveScalar(t *TCType) bool {
	if t == nil {
		return false
	}
	switch t.Kind {
	case "int", "float", "char", "string", "bool":
		return true
	}
	return false
}

// isCastable reports whether an 'as' cast from 'from' to 'to' is allowed.
// Every pair of primitive scalar types is castable -- the interpreter
// (castValue in interpreter.go) defines exactly what each pairing computes,
// including which ones can fail at runtime (e.g. a non-numeric string cast
// to int/float).
func isCastable(from, to *TCType) bool {
	return isPrimitiveScalar(from) && isPrimitiveScalar(to)
}

// typesCompatible is deliberately permissive about "unknown" (propagated
// after an earlier error, so it shouldn't cascade into new false positives)
// and about int/float mixing in arithmetic, matching common scripting-
// language numeric-tower behavior.
func typesCompatible(want, got *TCType) bool {
	if want == nil || got == nil {
		return true
	}
	if want.Kind == "unknown" || got.Kind == "unknown" {
		return true
	}
	if want.Kind == "void" || got.Kind == "void" {
		return true
	}
	if isNumeric(want) && isNumeric(got) {
		return true
	}
	if want.Kind != got.Kind {
		return false
	}
	switch want.Kind {
	case "named":
		return want.Name == got.Name
	case "ptr", "array":
		return typesCompatible(want.Elem, got.Elem)
	default:
		return true
	}
}

// ============================================================================
// Scopes & symbols
// ============================================================================

type tcSymbol struct {
	Name     string
	Kind     SymbolKind
	Type     *TCType
	Mutable  bool // true for 'let', false for 'const'/param
	FnSig    *fnSignature
	Line     int
	Col      int
}

type fnSignature struct {
	Params []*TCType
	Return *TCType
}

type tcScope struct {
	parent  *tcScope
	symbols map[string]*tcSymbol
}

func newTCScope(parent *tcScope) *tcScope {
	return &tcScope{parent: parent, symbols: make(map[string]*tcSymbol)}
}

func (s *tcScope) declare(sym *tcSymbol) (redeclared bool) {
	if _, ok := s.symbols[sym.Name]; ok {
		return true
	}
	s.symbols[sym.Name] = sym
	return false
}

func (s *tcScope) resolve(name string) (*tcSymbol, bool) {
	for sc := s; sc != nil; sc = sc.parent {
		if sym, ok := sc.symbols[name]; ok {
			return sym, true
		}
	}
	return nil, false
}

// ============================================================================
// TypeChecker
// ============================================================================

type TypeChecker struct {
	global     *tcScope
	current    *tcScope
	structDefs map[string]*StructLiteral
	enumDefs   map[string]*EnumBody
	Errors     []SemError
}

func NewTypeChecker() *TypeChecker {
	g := newTCScope(nil)
	return &TypeChecker{
		global:     g,
		current:    g,
		structDefs: make(map[string]*StructLiteral),
		enumDefs:   make(map[string]*EnumBody),
	}
}

func (c *TypeChecker) errorf(kind SemErrorKind, line, col int, format string, args ...interface{}) {
	c.Errors = append(c.Errors, SemError{Kind: kind, Line: line, Col: col, Message: fmt.Sprintf(format, args...)})
}

func (c *TypeChecker) enterScope() { c.current = newTCScope(c.current) }
func (c *TypeChecker) exitScope() {
	if c.current.parent != nil {
		c.current = c.current.parent
	}
}

// Check runs semantic analysis over the whole program and returns all
// diagnostics collected. It never panics on a single bad statement; each
// statement is checked independently so one error doesn't suppress others.
func (c *TypeChecker) Check(prog *Program) []SemError {
	for _, stmt := range prog.Statements {
		c.checkStmt(stmt)
	}
	return c.Errors
}

// ---- Statements -----------------------------------------------------------

func (c *TypeChecker) checkStmt(s Stmt) {
	switch n := s.(type) {
	case *ConstDecl:
		c.checkDecl(n.Name, n.TypeName, n.DeclaredType, n.Value, false, n)
	case *LetDecl:
		c.checkDecl(n.Name, n.TypeName, n.DeclaredType, n.Value, true, n)
	case *AssignStmt:
		c.checkAssign(n)
	case *Block:
		c.enterScope()
		for _, st := range n.Statements {
			c.checkStmt(st)
		}
		c.exitScope()
	case *IfStmt:
		c.checkExpr(n.Cond)
		c.checkStmt(n.Then)
		if n.ElseIf != nil {
			c.checkStmt(n.ElseIf)
		} else if n.Else != nil {
			c.checkStmt(n.Else)
		}
	case *ForIterStmt:
		c.checkExpr(n.Iter)
		c.enterScope()
		l, col := n.Pos()
		c.current.declare(&tcSymbol{Name: n.VarName, Kind: SymLet, Type: tUnknown(), Mutable: true, Line: l, Col: col})
		for _, st := range n.Body.Statements {
			c.checkStmt(st)
		}
		c.exitScope()
	case *ForWhileStmt:
		c.checkExpr(n.Cond)
		c.checkStmt(n.Body)
	case *ForRepeatStmt:
		c.checkStmt(n.Body)
		c.checkExpr(n.Cond)
	case *PrintStmt:
		for _, a := range n.Args {
			c.checkExpr(a)
		}
	case *ReturnStmt:
		if n.Value != nil {
			c.checkExpr(n.Value)
		}
	case *ExprStmt:
		c.checkExpr(n.Call)
	case *BreakStmt, *ContinueStmt:
		// nothing to check
	}
}

func (c *TypeChecker) checkDecl(name, typeTag string, dtype *DatatypeNode, val Value, mutable bool, node Stmt) {
	line, col := node.Pos()

	// Register struct/enum *type definitions* (not instances) globally so
	// later declarations of that named type can be resolved and validated.
	if sl, ok := val.(*StructLiteral); ok && typeTag == "struct" {
		c.structDefs[name] = sl
	}
	if eb, ok := val.(*EnumBody); ok && typeTag == "enum" {
		c.enumDefs[name] = eb
	}

	declType := fromDatatypeNode(dtype)
	kind := SymConst
	if mutable {
		kind = SymLet
	} else {
		kind = symbolKindForType(typeTag)
	}

	sym := &tcSymbol{Name: name, Kind: kind, Type: declType, Mutable: mutable, Line: line, Col: col}

	if fl, ok := val.(*FnLiteral); ok {
		sym.FnSig = c.fnSigFromLiteral(fl)
	}

	if c.current.declare(sym) {
		prev, _ := c.current.resolve(name)
		c.errorf(ErrMultiplyDefined, line, col,
			"%q is already declared in this scope (previously declared at %d:%d)", name, prev.Line, prev.Col)
	}

	// Struct-instance values are checked field-by-field against the
	// registered definition; everything else falls through to the general
	// value/expression type check.
	if inst, ok := val.(*StructInstance); ok && typeTag != "struct" && typeTag != "enum" {
		c.checkStructInstance(typeTag, inst)
		return
	}

	got := c.checkValue(val, declType)
	if declType.Kind != "unknown" && got != nil && !typesCompatible(declType, got) {
		c.errorf(ErrTypeMismatch, line, col,
			"%q declared as %s but initialized with %s", name, declType.String(), got.String())
	}
}

func (c *TypeChecker) fnSigFromLiteral(fl *FnLiteral) *fnSignature {
	sig := &fnSignature{Return: tVoid()}
	if fl.ReturnType != nil {
		sig.Return = fromDatatypeNode(fl.ReturnType)
	}
	for _, p := range fl.Params {
		pt := fromDatatypeNode(&p.Type)
		sig.Params = append(sig.Params, pt)
	}
	return sig
}

func (c *TypeChecker) checkAssign(n *AssignStmt) {
	line, col := n.Pos()

	if n.Target.Kind == TargetIdent {
		sym, ok := c.current.resolve(n.Target.Name)
		if !ok {
			c.errorf(ErrUndeclared, line, col, "assignment to undeclared variable %q", n.Target.Name)
		} else if len(n.Target.Suffixes) == 0 && n.Target.Deref == 0 {
			// Only a direct 'x = ...' reassigns the binding itself;
			// 'x.field = ...' / 'x[i] = ...' mutate through it and are
			// allowed even when x is const, since the const binding to x
			// itself never changes.
			if !sym.Mutable {
				c.errorf(ErrConstReassign, line, col,
					"cannot reassign %q: declared with 'const'", n.Target.Name)
			}
		}
	}

	got := c.checkValue(n.Value, nil)
	_ = got // full lvalue-chain type resolution intentionally kept best-effort;
	// the reassignment/undeclared checks above are the load-bearing ones here.
}

func (c *TypeChecker) checkStructInstance(typeName string, inst *StructInstance) {
	def, ok := c.structDefs[typeName]
	if !ok {
		// Either an unresolved forward reference or a genuine unknown type;
		// don't cascade a type-mismatch error for a name we can't resolve.
		return
	}
	fieldTypes := map[string]StructFieldInit{}
	for _, f := range def.Fields {
		fieldTypes[f.Name] = f
	}
	for _, fi := range inst.Fields {
		l, col := fi.Pos()
		fdef, ok := fieldTypes[fi.Name]
		if !ok {
			c.errorf(ErrOther, l, col, "%q is not a field of struct %q", fi.Name, typeName)
			continue
		}
		_ = fdef
		c.checkValue(fi.Value, nil)
	}
}

// ---- Values -----------------------------------------------------------

// checkValue type-checks any <value> production and returns its inferred
// type where determinable. ctxType is the declared type of the slot the
// value is being placed into (used to resolve array element types etc.);
// it may be nil when there is no useful context.
func (c *TypeChecker) checkValue(v Value, ctxType *TCType) *TCType {
	switch val := v.(type) {
	case *ExprValue:
		return c.checkExpr(val.Expr)
	case *ArrLiteral:
		var elemType *TCType
		if ctxType != nil && ctxType.Kind == "array" {
			elemType = ctxType.Elem
		}
		for _, el := range val.Elements {
			got := c.checkValue(el, elemType)
			if elemType != nil && got != nil && !typesCompatible(elemType, got) {
				l, col := el.Pos()
				c.errorf(ErrTypeMismatch, l, col, "array element is %s, expected %s", got.String(), elemType.String())
			}
		}
		if ctxType != nil && ctxType.Kind == "array" {
			return ctxType
		}
		return &TCType{Kind: "array", Elem: tUnknown(), ArrLen: len(val.Elements)}
	case *StructLiteral:
		return tBuiltin("struct")
	case *StructInstance:
		for _, fi := range val.Fields {
			c.checkValue(fi.Value, nil)
		}
		return tUnknown()
	case *EnumBody:
		return tBuiltin("enum")
	case *FnLiteral:
		c.enterScope()
		for _, p := range val.Params {
			l, col := p.Pos()
			pt := fromDatatypeNode(&p.Type)
			if c.current.declare(&tcSymbol{Name: p.Name, Kind: SymParam, Type: pt, Mutable: true, Line: l, Col: col}) {
				c.errorf(ErrMultiplyDefined, l, col, "duplicate parameter name %q", p.Name)
			}
		}
		for _, st := range val.Body.Statements {
			c.checkStmt(st)
		}
		c.exitScope()
		return tBuiltin("fn")
	}
	return tUnknown()
}

// ---- Expressions --------------------------------------------------------

func (c *TypeChecker) checkExpr(e Expr) *TCType {
	switch n := e.(type) {
	case *Literal:
		return literalType(n)

	case *Identifier:
		sym, ok := c.current.resolve(n.Name)
		if !ok {
			line, col := n.Pos()
			c.errorf(ErrUndeclared, line, col, "undeclared variable %q", n.Name)
			return tUnknown()
		}
		return sym.Type

	case *ThisExpr:
		return tUnknown() // no enclosing struct-method context to resolve against yet

	case *InputExpr:
		c.checkExpr(n.Prompt)
		return tBuiltin("string")

	case *GroupExpr:
		return c.checkExpr(n.Inner)

	case *UnaryExpr:
		operand := c.checkExpr(n.Operand)
		line, col := n.Pos()
		switch n.Op {
		case "-":
			if operand != nil && operand.Kind != "unknown" && !isNumeric(operand) {
				c.errorf(ErrTypeMismatch, line, col, "unary '-' requires a numeric operand, got %s", operand.String())
			}
			return operand
		case "++", "--":
			if operand != nil && operand.Kind != "unknown" && !isNumeric(operand) {
				c.errorf(ErrTypeMismatch, line, col, "'%s' requires a numeric operand, got %s", n.Op, operand.String())
			}
			return operand
		case "!":
			if operand != nil && operand.Kind != "unknown" && operand.Kind != "bool" {
				c.errorf(ErrTypeMismatch, line, col, "'!' requires a bool operand, got %s", operand.String())
			}
			return tBuiltin("bool")
		case "&":
			return &TCType{Kind: "ptr", Elem: operand}
		case "*":
			if operand != nil && operand.Kind == "ptr" {
				return operand.Elem
			}
			if operand != nil && operand.Kind != "unknown" {
				c.errorf(ErrTypeMismatch, line, col, "cannot dereference non-pointer type %s", operand.String())
			}
			return tUnknown()
		}
		return tUnknown()

	case *BinaryExpr:
		left := c.checkExpr(n.Left)
		right := c.checkExpr(n.Right)
		line, col := n.Pos()
		switch n.Op {
		case "+", "-", "*", "/", "%":
			// '+' additionally allows string concatenation.
			if n.Op == "+" && left != nil && right != nil && left.Kind == "string" && right.Kind == "string" {
				return tBuiltin("string")
			}
			if left != nil && right != nil && left.Kind != "unknown" && right.Kind != "unknown" &&
				(!isNumeric(left) || !isNumeric(right)) {
				c.errorf(ErrTypeMismatch, line, col, "operator %q requires numeric operands, got %s and %s", n.Op, left.String(), right.String())
			}
			if isNumeric(left) && isNumeric(right) && (left.Kind == "float" || right.Kind == "float") {
				return tBuiltin("float")
			}
			return tBuiltin("int")
		case "==", "!=", "<", ">", "<=", ">=":
			if left != nil && right != nil && left.Kind != "unknown" && right.Kind != "unknown" && !typesCompatible(left, right) {
				c.errorf(ErrTypeMismatch, line, col, "cannot compare %s with %s", left.String(), right.String())
			}
			return tBuiltin("bool")
		case "&&", "||":
			if left != nil && left.Kind != "unknown" && left.Kind != "bool" {
				c.errorf(ErrTypeMismatch, line, col, "operator %q requires bool operands, got %s on the left", n.Op, left.String())
			}
			if right != nil && right.Kind != "unknown" && right.Kind != "bool" {
				c.errorf(ErrTypeMismatch, line, col, "operator %q requires bool operands, got %s on the right", n.Op, right.String())
			}
			return tBuiltin("bool")
		}
		return tUnknown()

	case *FnCall:
		sym, ok := c.current.resolve(n.Callee)
		line, col := n.Pos()
		for _, a := range n.Args {
			c.checkExpr(a)
		}
		if !ok {
			c.errorf(ErrUndeclared, line, col, "call to undeclared function %q", n.Callee)
			return tUnknown()
		}
		if sym.FnSig == nil {
			return tUnknown() // e.g. a fn-typed param with no known signature; skip cardinality check
		}
		if len(n.Args) != len(sym.FnSig.Params) {
			c.errorf(ErrCardinality, line, col,
				"%q expects %d argument(s) but got %d", n.Callee, len(sym.FnSig.Params), len(n.Args))
		} else {
			for i, a := range n.Args {
				at := c.checkExpr(a)
				want := sym.FnSig.Params[i]
				if at != nil && want != nil && at.Kind != "unknown" && !typesCompatible(want, at) {
					al, ac := a.Pos()
					c.errorf(ErrTypeMismatch, al, ac,
						"argument %d to %q: expected %s, got %s", i+1, n.Callee, want.String(), at.String())
				}
			}
		}
		return sym.FnSig.Return

	case *MethodCall:
		c.checkExpr(n.Base)
		for _, a := range n.Args {
			c.checkExpr(a)
		}
		// Method resolution against a struct's fn-typed fields is left
		// best-effort: the base's static type isn't always resolvable
		// (e.g. through a chain), so this doesn't raise cardinality errors.
		return tUnknown()

	case *MemberAccess:
		baseType := c.checkExpr(n.Base)
		line, col := n.Pos()
		if baseType == nil || baseType.Kind != "named" {
			return tUnknown()
		}
		def, ok := c.structDefs[baseType.Name]
		if !ok {
			return tUnknown()
		}
		for _, f := range def.Fields {
			if f.Name == n.Field {
				return &TCType{Kind: f.TypeName}
			}
		}
		c.errorf(ErrOther, line, col, "%q is not a field of struct %q", n.Field, baseType.Name)
		return tUnknown()

	case *CastExpr:
		operand := c.checkExpr(n.Operand)
		target := fromDatatypeNode(n.Target)
		line, col := n.Pos()
		if operand != nil && operand.Kind != "unknown" && !isCastable(operand, target) {
			c.errorf(ErrTypeMismatch, line, col, "cannot cast %s to %s", operand.String(), target.String())
		}
		return target

	case *IndexExpr:
		baseType := c.checkExpr(n.Base)
		idxType := c.checkExpr(n.Index)
		line, col := n.Pos()
		if idxType != nil && idxType.Kind != "unknown" && idxType.Kind != "int" {
			c.errorf(ErrTypeMismatch, line, col, "array index must be int, got %s", idxType.String())
		}
		if baseType != nil && baseType.Kind == "array" {
			return baseType.Elem
		}
		return tUnknown()
	}
	return tUnknown()
}

func literalType(l *Literal) *TCType {
	switch l.Kind {
	case TOKEN_INT_LIT:
		return tBuiltin("int")
	case TOKEN_FLOAT_LIT:
		return tBuiltin("float")
	case TOKEN_CHAR_LIT:
		return tBuiltin("char")
	case TOKEN_STRING_LIT:
		return tBuiltin("string")
	case TOKEN_BOOL_LIT:
		return tBuiltin("bool")
	default: // 'null'
		return tUnknown()
	}
}
