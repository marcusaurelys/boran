package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ============================================================================
// Runtime errors
// ============================================================================

// RuntimeError is raised via panic/recover for conditions the static type
// checker can't (or doesn't) fully guarantee away -- out-of-bounds array
// access, division by zero, dereferencing a freed pointer, calling a
// non-function value, etc. Interpreter.Run recovers these at the top level
// so one bad statement reports cleanly instead of crashing the process.
type RuntimeError struct {
	Line, Col int
	Message   string
}

func (e *RuntimeError) Error() string {
	return fmt.Sprintf("%d:%d: runtime error: %s", e.Line, e.Col, e.Message)
}

func rtPanic(line, col int, format string, args ...interface{}) {
	panic(&RuntimeError{Line: line, Col: col, Message: fmt.Sprintf(format, args...)})
}

// ============================================================================
// Control-flow signals
//
// execStmt returns one of these instead of using panic/recover for
// break/continue/return -- they're expected, frequent control flow (every
// loop iteration and every function call), not exceptional conditions, so
// a plain return value keeps Step() cheap and keeps stack traces clean for
// genuine RuntimeErrors.
// ============================================================================

type ctrlKind int

const (
	ctrlNone ctrlKind = iota
	ctrlBreak
	ctrlContinue
	ctrlReturn
)

type ctrlSignal struct {
	kind  ctrlKind
	value RTValue // populated for ctrlReturn
}

var sigNone = ctrlSignal{kind: ctrlNone}

// ============================================================================
// Interpreter
// ============================================================================

// StepHook is called after each statement executes. It's how main.go drives
// line-by-line execution: pass a hook that prints i.Global/i.Stack/i.Heap
// and blocks (e.g. on stdin) before returning. For all-at-once mode, leave
// OnStep nil -- the same Run/execStmt path is used either way, so the two
// execution modes can never silently diverge in behavior.
type StepHook func(i *Interpreter, node Node, env *Environment, line, col int)

type Interpreter struct {
	Heap   *Heap
	Global *Environment
	Stack  *CallStack

	structDefs map[string]*StructLiteral
	enumDefs   map[string]*EnumBody

	Output io.Writer
	Input  *bufio.Reader

	OnStep     StepHook
	BeforeInput func(prompt string) // fired right before input() blocks on ReadString, if set
	AfterInput func() // fired right after input() reads a line, if set
	Errors     []*RuntimeError
}

func NewInterpreter(out io.Writer, in io.Reader) *Interpreter {
	return &Interpreter{
		Heap:       NewHeap(),
		Global:     NewEnvironment(nil),
		Stack:      NewCallStack(),
		structDefs: make(map[string]*StructLiteral),
		enumDefs:   make(map[string]*EnumBody),
		Output:     out,
		Input:      bufio.NewReader(in),
	}
}

// NewInterpreterWithReader is identical to NewInterpreter except it takes
// an already-constructed *bufio.Reader and uses it as-is, rather than
// wrapping a fresh io.Reader. Use this whenever the reader is shared with
// something else reading the same underlying stream (e.g. a step
// controller reading "press Enter" prompts from the same stdin) --
// wrapping the same fd in two independent bufio.Readers risks one of them
// silently swallowing bytes the other needed.
func NewInterpreterWithReader(out io.Writer, in *bufio.Reader) *Interpreter {
	return &Interpreter{
		Heap:       NewHeap(),
		Global:     NewEnvironment(nil),
		Stack:      NewCallStack(),
		structDefs: make(map[string]*StructLiteral),
		enumDefs:   make(map[string]*EnumBody),
		Output:     out,
		Input:      in,
	}
}

// Run executes the whole program top to bottom (or one Step() at a time, if
// OnStep is set), recovering any RuntimeError so execution halts cleanly
// with a recorded diagnostic rather than a Go panic reaching main.
func (i *Interpreter) Run(prog *Program) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if re, ok := r.(*RuntimeError); ok {
				i.Errors = append(i.Errors, re)
				err = re
				return
			}
			panic(r) // not one of ours -- a real bug, don't swallow it
		}
	}()

	for _, s := range prog.Statements {
		sig := i.execStmt(s, i.Global)
		if sig.kind == ctrlReturn {
			break // bare top-level 'return' just ends the program
		}
	}
	return nil
}

// ---- Statements -----------------------------------------------------------

func (i *Interpreter) execStmt(s Stmt, env *Environment) ctrlSignal {
	line, col := s.Pos()
	if i.OnStep != nil {
		i.OnStep(i, s, env, line, col)
	}

	switch n := s.(type) {
	case *ConstDecl:
		i.execDecl(n.Name, n.TypeName, n.DeclaredType, n.Value, env)
		return sigNone

	case *LetDecl:
		i.execDecl(n.Name, n.TypeName, n.DeclaredType, n.Value, env)
		return sigNone

	case *AssignStmt:
		addr := i.addressOfTarget(n.Target, env)
		val := i.evalValue(n.Value, env, i.declaredTypeAtAddr(addr))
		i.Heap.Set(addr, val)
		return sigNone

	case *Block:
		child := NewEnvironment(env)
		for _, st := range n.Statements {
			sig := i.execStmt(st, child)
			if sig.kind != ctrlNone {
				return sig
			}
		}
		return sigNone

	case *IfStmt:
		cond := i.evalExpr(n.Cond, env)
		b, ok := cond.(*BoolVal)
		if !ok {
			rtPanic(line, col, "if condition is not a bool (got %s)", cond.TypeTag())
		}
		if b.Val {
			return i.execStmt(n.Then, env)
		} else if n.ElseIf != nil {
			return i.execStmt(n.ElseIf, env)
		} else if n.Else != nil {
			return i.execStmt(n.Else, env)
		}
		return sigNone

	case *ForIterStmt:
		iterVal := i.evalExpr(n.Iter, env)
		arr, ok := iterVal.(*ArrayVal)
		if !ok {
			rtPanic(line, col, "'for %s in ...' requires an array, got %s", n.VarName, iterVal.TypeTag())
		}
		for _, elemAddr := range arr.Elems {
			child := NewEnvironment(env)
			box, ok := i.Heap.Get(elemAddr)
			if !ok {
				rtPanic(line, col, "dangling array element reference")
			}
			child.Define(n.VarName, i.Heap.Alloc(box.Value))
			sig := i.execStmt(n.Body, child)
			if sig.kind == ctrlBreak {
				break
			}
			if sig.kind == ctrlReturn {
				return sig
			}
		}
		return sigNone

	case *ForWhileStmt:
		for {
			cond := i.evalExpr(n.Cond, env)
			b, ok := cond.(*BoolVal)
			if !ok {
				rtPanic(line, col, "for-while condition is not a bool (got %s)", cond.TypeTag())
			}
			if !b.Val {
				break
			}
			sig := i.execStmt(n.Body, env)
			if sig.kind == ctrlBreak {
				break
			}
			if sig.kind == ctrlReturn {
				return sig
			}
		}
		return sigNone

	case *ForRepeatStmt:
		for {
			sig := i.execStmt(n.Body, env)
			if sig.kind == ctrlBreak {
				break
			}
			if sig.kind == ctrlReturn {
				return sig
			}
			cond := i.evalExpr(n.Cond, env)
			b, ok := cond.(*BoolVal)
			if !ok {
				rtPanic(line, col, "repeat-until condition is not a bool (got %s)", cond.TypeTag())
			}
			if b.Val {
				break // "UNTIL (cond)" -- stop once cond becomes true
			}
		}
		return sigNone

	case *PrintStmt:
		parts := make([]string, len(n.Args))
		for idx, a := range n.Args {
			parts[idx] = i.evalExpr(a, env).String()
		}
		fmt.Fprintln(i.Output, strings.Join(parts, " "))
		return sigNone

	case *ReturnStmt:
		if n.Value == nil {
			return ctrlSignal{kind: ctrlReturn, value: &NullVal{}}
		}
		return ctrlSignal{kind: ctrlReturn, value: i.evalExpr(n.Value, env)}

	case *BreakStmt:
		return ctrlSignal{kind: ctrlBreak}

	case *ContinueStmt:
		return ctrlSignal{kind: ctrlContinue}

	case *ExprStmt:
		i.evalExpr(n.Call, env)
		return sigNone
	}

	return sigNone
}

func (i *Interpreter) execDecl(name, typeTag string, dtype *DatatypeNode, val Value, env *Environment) {
	if sl, ok := val.(*StructLiteral); ok && typeTag == "struct" {
		i.structDefs[name] = sl
	}
	if eb, ok := val.(*EnumBody); ok && typeTag == "enum" {
		i.enumDefs[name] = eb
	}
	rv := i.evalValue(val, env, dtype)
	if td, ok := rv.(*TypeDefVal); ok {
		td.Name = name
	}
	addr := i.Heap.Alloc(rv)
	env.Define(name, addr)
}

// declaredTypeAtAddr recovers a struct's declared type from whatever's
// already stored at addr, so `p.field = {...}` (assigning a fresh struct
// instance into an existing struct-typed field) can resolve its field
// defaults against the right definition without a separate static type
// table at the interpreter level.
func (i *Interpreter) declaredTypeAtAddr(addr int) *DatatypeNode {
	box, ok := i.Heap.Get(addr)
	if !ok {
		return nil
	}
	if sv, ok := box.Value.(*StructVal); ok && sv.TypeName != "" {
		return &DatatypeNode{Kind: "named", Name: sv.TypeName}
	}
	return nil
}

// ---- lvalue address resolution --------------------------------------------

// addressOfTarget resolves an AssignStmt's AssignTarget (the specialized
// lvalue-chain type, distinct from a general Expr) down to the heap address
// that should actually be overwritten.
func (i *Interpreter) addressOfTarget(t AssignTarget, env *Environment) int {
	line, col := t.Pos()
	var addr int
	var ok bool
	switch t.Kind {
	case TargetIdent:
		addr, ok = env.Resolve(t.Name)
		if !ok {
			rtPanic(line, col, "assignment to undeclared variable %q", t.Name)
		}
	case TargetThis:
		addr, ok = env.Resolve("this")
		if !ok {
			rtPanic(line, col, "'this' used outside of a method")
		}
	}

	for d := 0; d < t.Deref; d++ {
		box, ok := i.Heap.Get(addr)
		if !ok {
			rtPanic(line, col, "dereference of invalid/freed pointer")
		}
		ptr, ok := box.Value.(*PtrVal)
		if !ok {
			rtPanic(line, col, "cannot dereference non-pointer value")
		}
		addr = ptr.Addr
	}

	for _, suf := range t.Suffixes {
		box, ok := i.Heap.Get(addr)
		if !ok {
			rtPanic(line, col, "invalid/freed reference in assignment target")
		}
		switch suf.Kind {
		case SuffixField:
			sv, ok := box.Value.(*StructVal)
			if !ok {
				rtPanic(line, col, "'.%s' used on a non-struct value", suf.Field)
			}
			fieldAddr, ok := sv.Fields[suf.Field]
			if !ok {
				rtPanic(line, col, "%q is not a field of struct %q", suf.Field, sv.TypeName)
			}
			addr = fieldAddr
		case SuffixIndex:
			arr, ok := box.Value.(*ArrayVal)
			if !ok {
				rtPanic(line, col, "'[...]' used on a non-array value")
			}
			idxVal := i.evalExpr(suf.Index, env)
			idx, ok := idxVal.(*IntVal)
			if !ok {
				rtPanic(line, col, "array index must be int, got %s", idxVal.TypeTag())
			}
			if idx.Val < 0 || int(idx.Val) >= len(arr.Elems) {
				rtPanic(line, col, "array index %d out of bounds (length %d)", idx.Val, len(arr.Elems))
			}
			addr = arr.Elems[idx.Val]
		}
	}
	return addr
}

// addressOfExpr is the general-expression counterpart to addressOfTarget,
// used wherever an lvalue is needed from ordinary expression syntax
// (unary '&' / prefix-postfix '++'/'--' / MethodCall receivers).
func (i *Interpreter) addressOfExpr(e Expr, env *Environment) int {
	line, col := e.Pos()
	switch n := e.(type) {
	case *Identifier:
		addr, ok := env.Resolve(n.Name)
		if !ok {
			rtPanic(line, col, "undeclared variable %q", n.Name)
		}
		return addr

	case *ThisExpr:
		addr, ok := env.Resolve("this")
		if !ok {
			rtPanic(line, col, "'this' used outside of a method")
		}
		return addr

	case *GroupExpr:
		return i.addressOfExpr(n.Inner, env)

	case *UnaryExpr:
		if n.Op == "*" {
			ptrVal := i.evalExpr(n.Operand, env)
			ptr, ok := ptrVal.(*PtrVal)
			if !ok {
				rtPanic(line, col, "cannot dereference non-pointer value")
			}
			if _, ok := i.Heap.Get(ptr.Addr); !ok {
				rtPanic(line, col, "dereference of invalid/freed pointer")
			}
			return ptr.Addr
		}

	case *MemberAccess:
		baseAddr := i.addressOfExpr(n.Base, env)
		box, ok := i.Heap.Get(baseAddr)
		if !ok {
			rtPanic(line, col, "invalid/freed reference")
		}
		sv, ok := box.Value.(*StructVal)
		if !ok {
			rtPanic(line, col, "'.%s' used on a non-struct value", n.Field)
		}
		fieldAddr, ok := sv.Fields[n.Field]
		if !ok {
			rtPanic(line, col, "%q is not a field of struct %q", n.Field, sv.TypeName)
		}
		return fieldAddr

	case *IndexExpr:
		baseAddr := i.addressOfExpr(n.Base, env)
		box, ok := i.Heap.Get(baseAddr)
		if !ok {
			rtPanic(line, col, "invalid/freed reference")
		}
		arr, ok := box.Value.(*ArrayVal)
		if !ok {
			rtPanic(line, col, "'[...]' used on a non-array value")
		}
		idxVal := i.evalExpr(n.Index, env)
		idx, ok := idxVal.(*IntVal)
		if !ok {
			rtPanic(line, col, "array index must be int, got %s", idxVal.TypeTag())
		}
		if idx.Val < 0 || int(idx.Val) >= len(arr.Elems) {
			rtPanic(line, col, "array index %d out of bounds (length %d)", idx.Val, len(arr.Elems))
		}
		return arr.Elems[idx.Val]
	}
	rtPanic(line, col, "expression is not addressable")
	return 0
}

// ---- Values (<value> productions) ------------------------------------------

// typeTagOf reduces a DatatypeNode down to the string key used to look up
// struct/enum definitions and to label runtime values: for a user-defined
// type ("named") that's the type's own name (e.g. "Vector2"); for anything
// else it's just the builtin keyword ("int", "array", "struct", ...).
func typeTagOf(dtype *DatatypeNode) string {
	if dtype == nil {
		return ""
	}
	if dtype.Kind == "named" {
		return dtype.Name
	}
	return dtype.Kind
}

func (i *Interpreter) evalValue(v Value, env *Environment, dtype *DatatypeNode) RTValue {
	typeTag := typeTagOf(dtype)
	switch val := v.(type) {
	case *ExprValue:
		return i.evalExpr(val.Expr, env)

	case *ArrLiteral:
		var elemDtype *DatatypeNode
		if dtype != nil && dtype.Kind == "array" {
			elemDtype = dtype.Elem
		}
		elemType := typeTagOf(elemDtype)
		elems := make([]int, len(val.Elements))
		for idx, el := range val.Elements {
			ev := i.evalValue(el, env, elemDtype)
			if idx == 0 && elemType == "" {
				elemType = ev.TypeTag()
			}
			elems[idx] = i.Heap.Alloc(ev)
		}
		return &ArrayVal{Elems: elems, ElemType: elemType}

	case *StructLiteral:
		return &TypeDefVal{Kind: "struct", Name: typeTag}

	case *EnumBody:
		return &TypeDefVal{Kind: "enum", Name: typeTag}

	case *StructInstance:
		sv := NewStructVal(typeTag)
		if def, ok := i.structDefs[typeTag]; ok {
			// Seed every declared field with its default first (so fields
			// the instance literal omits still exist), then overlay
			// whatever the instance literal actually provides.
			for _, f := range def.Fields {
				var fv RTValue = &NullVal{}
				if f.Default != nil {
					fv = i.evalValue(f.Default, env, f.DeclaredType)
				}
				sv.SetField(f.Name, i.Heap.Alloc(fv))
			}
			for _, fi := range val.Fields {
				var fieldDtype *DatatypeNode
				for _, f := range def.Fields {
					if f.Name == fi.Name {
						fieldDtype = f.DeclaredType
					}
				}
				fv := i.evalValue(fi.Value, env, fieldDtype)
				if addr, exists := sv.Fields[fi.Name]; exists {
					i.Heap.Set(addr, fv)
				} else {
					sv.SetField(fi.Name, i.Heap.Alloc(fv))
				}
			}
			return sv
		}
		// Unknown/unregistered type name: still build something usable
		// rather than failing outright, matching the type checker's
		// best-effort stance on unresolved names.
		for _, fi := range val.Fields {
			fv := i.evalValue(fi.Value, env, nil)
			sv.SetField(fi.Name, i.Heap.Alloc(fv))
		}
		return sv

	case *FnLiteral:
		return &FnVal{Lit: val, Closure: env}
	}
	return &NullVal{}
}

// ---- Expressions ------------------------------------------------------

func (i *Interpreter) evalExpr(e Expr, env *Environment) RTValue {
	line, col := e.Pos()
	switch n := e.(type) {
	case *Literal:
		return evalLiteral(n)

	case *Identifier:
		addr, ok := env.Resolve(n.Name)
		if !ok {
			rtPanic(line, col, "undeclared variable %q", n.Name)
		}
		box, ok := i.Heap.Get(addr)
		if !ok {
			rtPanic(line, col, "invalid/freed reference for %q", n.Name)
		}
		return box.Value

	case *ThisExpr:
		addr, ok := env.Resolve("this")
		if !ok {
			rtPanic(line, col, "'this' used outside of a method")
		}
		box, _ := i.Heap.Get(addr)
		return box.Value

	case *InputExpr:
		promptVal := i.evalExpr(n.Prompt, env)
		fmt.Fprint(i.Output, promptVal.String())
		if i.BeforeInput != nil {
			i.BeforeInput(promptVal.String())
		}
		line, err := i.Input.ReadString('\n')
		if i.AfterInput != nil {
			i.AfterInput()
		}
		if err != nil && line == "" {
			return &StringVal{Val: ""}
		}
		return &StringVal{Val: strings.TrimRight(line, "\r\n")}

	case *GroupExpr:
		return i.evalExpr(n.Inner, env)

	case *CastExpr:
		v := i.evalExpr(n.Operand, env)
		return castValue(v, n.Target, line, col)

	case *UnaryExpr:
		return i.evalUnary(n, env)

	case *BinaryExpr:
		return i.evalBinary(n, env)

	case *FnCall:
		addr, ok := env.Resolve(n.Callee)
		if !ok {
			rtPanic(line, col, "call to undeclared function %q", n.Callee)
		}
		box, ok := i.Heap.Get(addr)
		if !ok {
			rtPanic(line, col, "invalid/freed reference for %q", n.Callee)
		}
		fn, ok := box.Value.(*FnVal)
		if !ok {
			rtPanic(line, col, "%q is not callable (got %s)", n.Callee, box.Value.TypeTag())
		}
		args := i.evalArgs(n.Args, env)
		return i.callFunction(fn, args, n.Callee, line, col)

	case *MethodCall:
		baseAddr := i.addressOfExpr(n.Base, env)
		box, ok := i.Heap.Get(baseAddr)
		if !ok {
			rtPanic(line, col, "invalid/freed reference")
		}
		sv, ok := box.Value.(*StructVal)
		if !ok {
			rtPanic(line, col, "method call '.%s(...)' on a non-struct value", n.MethodName)
		}
		fieldAddr, ok := sv.Fields[n.MethodName]
		if !ok {
			rtPanic(line, col, "%q is not a field/method of struct %q", n.MethodName, sv.TypeName)
		}
		fbox, ok := i.Heap.Get(fieldAddr)
		if !ok {
			rtPanic(line, col, "invalid/freed reference")
		}
		fn, ok := fbox.Value.(*FnVal)
		if !ok {
			rtPanic(line, col, "%q is not callable (got %s)", n.MethodName, fbox.Value.TypeTag())
		}
		args := i.evalArgs(n.Args, env)
		return i.callMethod(fn, args, baseAddr, sv.TypeName+"."+n.MethodName, line, col)

	case *MemberAccess:
		// Enum variant access, e.g. Direction.north, is resolved against
		// the type registry directly rather than through a struct field,
		// since the "base" here is a type name, not a struct instance.
		if ident, ok := n.Base.(*Identifier); ok {
			if eb, ok := i.enumDefs[ident.Name]; ok {
				for _, variant := range eb.Variants {
					if variant == n.Field {
						return &EnumVal{TypeName: ident.Name, Variant: variant}
					}
				}
				rtPanic(line, col, "%q is not a variant of enum %q", n.Field, ident.Name)
			}
		}
		addr := i.addressOfExpr(n, env)
		box, _ := i.Heap.Get(addr)
		return box.Value

	case *IndexExpr:
		addr := i.addressOfExpr(n, env)
		box, _ := i.Heap.Get(addr)
		return box.Value
	}
	rtPanic(line, col, "cannot evaluate expression of type %T", e)
	return &NullVal{}
}

func (i *Interpreter) evalArgs(argExprs []Expr, env *Environment) []RTValue {
	args := make([]RTValue, len(argExprs))
	for idx, a := range argExprs {
		args[idx] = i.evalExpr(a, env)
	}
	return args
}

func (i *Interpreter) callFunction(fn *FnVal, args []RTValue, label string, line, col int) RTValue {
	return i.invoke(fn, args, nil, label, line, col)
}

func (i *Interpreter) callMethod(fn *FnVal, args []RTValue, thisAddr int, label string, line, col int) RTValue {
	return i.invoke(fn, args, &thisAddr, label, line, col)
}

// invoke is the shared machinery behind plain calls and method calls: bind
// params (and 'this', if present) in a fresh environment chained to the
// function's closure (not the caller's environment -- that's what makes
// these real closures), push a call-stack frame, run the body, pop.
func (i *Interpreter) invoke(fn *FnVal, args []RTValue, thisAddr *int, label string, line, col int) RTValue {
	if len(args) != len(fn.Lit.Params) {
		rtPanic(line, col, "%q expects %d argument(s) but got %d", label, len(fn.Lit.Params), len(args))
	}
	callEnv := NewEnvironment(fn.Closure)
	if thisAddr != nil {
		callEnv.Define("this", *thisAddr)
	}
	for idx, p := range fn.Lit.Params {
		callEnv.Define(p.Name, i.Heap.Alloc(args[idx]))
	}

	i.Stack.Push(&CallFrame{FnName: label, Env: callEnv, CallLine: line, CallCol: col})
	defer i.Stack.Pop()

	for _, st := range fn.Lit.Body.Statements {
		sig := i.execStmt(st, callEnv)
		if sig.kind == ctrlReturn {
			return sig.value
		}
		if sig.kind == ctrlBreak || sig.kind == ctrlContinue {
			rtPanic(line, col, "'break'/'continue' used outside of a loop")
		}
	}
	return &NullVal{}
}

// ---- Unary / binary operators ----------------------------------------

func (i *Interpreter) evalUnary(n *UnaryExpr, env *Environment) RTValue {
	line, col := n.Pos()
	switch n.Op {
	case "-":
		v := i.evalExpr(n.Operand, env)
		switch val := v.(type) {
		case *IntVal:
			return &IntVal{Val: -val.Val}
		case *FloatVal:
			return &FloatVal{Val: -val.Val}
		}
		rtPanic(line, col, "unary '-' requires a numeric operand, got %s", v.TypeTag())

	case "!":
		v := i.evalExpr(n.Operand, env)
		b, ok := v.(*BoolVal)
		if !ok {
			rtPanic(line, col, "'!' requires a bool operand, got %s", v.TypeTag())
		}
		return &BoolVal{Val: !b.Val}

	case "&":
		addr := i.addressOfExpr(n.Operand, env)
		return &PtrVal{Addr: addr}

	case "*":
		v := i.evalExpr(n.Operand, env)
		ptr, ok := v.(*PtrVal)
		if !ok {
			rtPanic(line, col, "cannot dereference non-pointer value (got %s)", v.TypeTag())
		}
		box, ok := i.Heap.Get(ptr.Addr)
		if !ok {
			rtPanic(line, col, "dereference of invalid/freed pointer")
		}
		return box.Value

	case "++", "--":
		addr := i.addressOfExpr(n.Operand, env)
		box, ok := i.Heap.Get(addr)
		if !ok {
			rtPanic(line, col, "invalid/freed reference")
		}
		var oldVal, newVal RTValue
		switch cur := box.Value.(type) {
		case *IntVal:
			oldVal = &IntVal{Val: cur.Val}
			if n.Op == "++" {
				newVal = &IntVal{Val: cur.Val + 1}
			} else {
				newVal = &IntVal{Val: cur.Val - 1}
			}
		case *FloatVal:
			oldVal = &FloatVal{Val: cur.Val}
			if n.Op == "++" {
				newVal = &FloatVal{Val: cur.Val + 1}
			} else {
				newVal = &FloatVal{Val: cur.Val - 1}
			}
		default:
			rtPanic(line, col, "'%s' requires a numeric operand, got %s", n.Op, box.Value.TypeTag())
		}
		i.Heap.Set(addr, newVal)
		if n.Postfix {
			return oldVal
		}
		return newVal
	}
	rtPanic(line, col, "unknown unary operator %q", n.Op)
	return &NullVal{}
}

func (i *Interpreter) evalBinary(n *BinaryExpr, env *Environment) RTValue {
	line, col := n.Pos()

	// Logical operators short-circuit, so the right operand is evaluated
	// lazily -- evaluated here, not up front with Left/Right together.
	if n.Op == "&&" || n.Op == "||" {
		l := i.evalExpr(n.Left, env)
		lb, ok := l.(*BoolVal)
		if !ok {
			rtPanic(line, col, "operator %q requires bool operands, got %s on the left", n.Op, l.TypeTag())
		}
		if n.Op == "&&" && !lb.Val {
			return &BoolVal{Val: false}
		}
		if n.Op == "||" && lb.Val {
			return &BoolVal{Val: true}
		}
		r := i.evalExpr(n.Right, env)
		rb, ok := r.(*BoolVal)
		if !ok {
			rtPanic(line, col, "operator %q requires bool operands, got %s on the right", n.Op, r.TypeTag())
		}
		return &BoolVal{Val: rb.Val}
	}

	l := i.evalExpr(n.Left, env)
	r := i.evalExpr(n.Right, env)

	switch n.Op {
	case "+", "-", "*", "/", "%":
		if n.Op == "+" {
			ls, lok := l.(*StringVal)
			rs, rok := r.(*StringVal)
			if lok && rok {
				return &StringVal{Val: ls.Val + rs.Val}
			}
		}
		return i.arith(n.Op, l, r, line, col)

	case "==", "!=":
		eq := valuesEqual(l, r)
		if n.Op == "!=" {
			eq = !eq
		}
		return &BoolVal{Val: eq}

	case "<", ">", "<=", ">=":
		return &BoolVal{Val: i.compare(n.Op, l, r, line, col)}
	}
	rtPanic(line, col, "unknown binary operator %q", n.Op)
	return &NullVal{}
}

func (i *Interpreter) arith(op string, l, r RTValue, line, col int) RTValue {
	lf, lIsFloat, lok := toNumeric(l)
	rf, rIsFloat, rok := toNumeric(r)
	if !lok || !rok {
		rtPanic(line, col, "operator %q requires numeric operands, got %s and %s", op, l.TypeTag(), r.TypeTag())
	}

	useFloat := lIsFloat || rIsFloat
	if !useFloat {
		li := l.(*IntVal).Val
		ri := r.(*IntVal).Val
		switch op {
		case "+":
			return &IntVal{Val: li + ri}
		case "-":
			return &IntVal{Val: li - ri}
		case "*":
			return &IntVal{Val: li * ri}
		case "/":
			if ri == 0 {
				rtPanic(line, col, "division by zero")
			}
			return &IntVal{Val: li / ri}
		case "%":
			if ri == 0 {
				rtPanic(line, col, "division by zero")
			}
			return &IntVal{Val: li % ri}
		}
	}
	switch op {
	case "+":
		return &FloatVal{Val: lf + rf}
	case "-":
		return &FloatVal{Val: lf - rf}
	case "*":
		return &FloatVal{Val: lf * rf}
	case "/":
		if rf == 0 {
			rtPanic(line, col, "division by zero")
		}
		return &FloatVal{Val: lf / rf}
	case "%":
		rtPanic(line, col, "'%%' requires int operands")
	}
	return &NullVal{}
}

func (i *Interpreter) compare(op string, l, r RTValue, line, col int) bool {
	lf, _, lok := toNumeric(l)
	rf, _, rok := toNumeric(r)
	if lok && rok {
		switch op {
		case "<":
			return lf < rf
		case ">":
			return lf > rf
		case "<=":
			return lf <= rf
		case ">=":
			return lf >= rf
		}
	}
	ls, lok := l.(*StringVal)
	rs, rok := r.(*StringVal)
	if lok && rok {
		switch op {
		case "<":
			return ls.Val < rs.Val
		case ">":
			return ls.Val > rs.Val
		case "<=":
			return ls.Val <= rs.Val
		case ">=":
			return ls.Val >= rs.Val
		}
	}
	rtPanic(line, col, "operator %q cannot compare %s with %s", op, l.TypeTag(), r.TypeTag())
	return false
}

func toNumeric(v RTValue) (f float64, isFloat bool, ok bool) {
	switch val := v.(type) {
	case *IntVal:
		return float64(val.Val), false, true
	case *FloatVal:
		return val.Val, true, true
	}
	return 0, false, false
}

func valuesEqual(l, r RTValue) bool {
	if lf, _, lok := toNumeric(l); lok {
		if rf, _, rok := toNumeric(r); rok {
			return lf == rf
		}
	}
	switch lv := l.(type) {
	case *StringVal:
		if rv, ok := r.(*StringVal); ok {
			return lv.Val == rv.Val
		}
	case *BoolVal:
		if rv, ok := r.(*BoolVal); ok {
			return lv.Val == rv.Val
		}
	case *CharVal:
		if rv, ok := r.(*CharVal); ok {
			return lv.Val == rv.Val
		}
	case *PtrVal:
		if rv, ok := r.(*PtrVal); ok {
			return lv.Addr == rv.Addr
		}
	case *EnumVal:
		if rv, ok := r.(*EnumVal); ok {
			return lv.TypeName == rv.TypeName && lv.Variant == rv.Variant
		}
	case *NullVal:
		_, ok := r.(*NullVal)
		return ok
	}
	return false
}

// ---- Type casting ----------------------------------------------------

// castValue implements the 'as' operator's runtime behavior. The type
// checker (isCastable in typecheck.go) already restricts this to the five
// scalar primitives, so target.Kind is always one of those here; any other
// DatatypeNode reaching this point is a checker bug, not a user error, and
// is treated as an unsupported cast rather than silently doing nothing.
func castValue(v RTValue, target *DatatypeNode, line, col int) RTValue {
	if target == nil {
		rtPanic(line, col, "cast has no target type")
	}
	switch target.Kind {
	case "int":
		return castToInt(v, line, col)
	case "float":
		return castToFloat(v, line, col)
	case "bool":
		return castToBool(v, line, col)
	case "char":
		return castToChar(v, line, col)
	case "string":
		return &StringVal{Val: v.String()}
	}
	rtPanic(line, col, "unsupported cast target type %q", target.Kind)
	return &NullVal{}
}

func castToInt(v RTValue, line, col int) RTValue {
	switch val := v.(type) {
	case *IntVal:
		return &IntVal{Val: val.Val}
	case *FloatVal:
		return &IntVal{Val: int64(val.Val)} // truncates toward zero
	case *BoolVal:
		if val.Val {
			return &IntVal{Val: 1}
		}
		return &IntVal{Val: 0}
	case *CharVal:
		return &IntVal{Val: int64(val.Val)}
	case *StringVal:
		n, err := strconv.ParseInt(strings.TrimSpace(val.Val), 10, 64)
		if err != nil {
			rtPanic(line, col, "cannot cast string %q to int: not a valid integer", val.Val)
		}
		return &IntVal{Val: n}
	}
	rtPanic(line, col, "cannot cast %s to int", v.TypeTag())
	return &NullVal{}
}

func castToFloat(v RTValue, line, col int) RTValue {
	switch val := v.(type) {
	case *IntVal:
		return &FloatVal{Val: float64(val.Val)}
	case *FloatVal:
		return &FloatVal{Val: val.Val}
	case *BoolVal:
		if val.Val {
			return &FloatVal{Val: 1}
		}
		return &FloatVal{Val: 0}
	case *CharVal:
		return &FloatVal{Val: float64(val.Val)}
	case *StringVal:
		f, err := strconv.ParseFloat(strings.TrimSpace(val.Val), 64)
		if err != nil {
			rtPanic(line, col, "cannot cast string %q to float: not a valid number", val.Val)
		}
		return &FloatVal{Val: f}
	}
	rtPanic(line, col, "cannot cast %s to float", v.TypeTag())
	return &NullVal{}
}

func castToBool(v RTValue, line, col int) RTValue {
	switch val := v.(type) {
	case *IntVal:
		return &BoolVal{Val: val.Val != 0}
	case *FloatVal:
		return &BoolVal{Val: val.Val != 0}
	case *BoolVal:
		return &BoolVal{Val: val.Val}
	case *CharVal:
		return &BoolVal{Val: val.Val != 0}
	case *StringVal:
		switch strings.TrimSpace(val.Val) {
		case "true":
			return &BoolVal{Val: true}
		case "false":
			return &BoolVal{Val: false}
		}
		rtPanic(line, col, "cannot cast string %q to bool: expected \"true\" or \"false\"", val.Val)
	}
	rtPanic(line, col, "cannot cast %s to bool", v.TypeTag())
	return &NullVal{}
}

func castToChar(v RTValue, line, col int) RTValue {
	switch val := v.(type) {
	case *IntVal:
		return &CharVal{Val: rune(val.Val)}
	case *FloatVal:
		return &CharVal{Val: rune(int64(val.Val))}
	case *BoolVal:
		if val.Val {
			return &CharVal{Val: '1'}
		}
		return &CharVal{Val: '0'}
	case *CharVal:
		return &CharVal{Val: val.Val}
	case *StringVal:
		runes := []rune(val.Val)
		if len(runes) != 1 {
			rtPanic(line, col, "cannot cast string %q to char: must be exactly one character", val.Val)
		}
		return &CharVal{Val: runes[0]}
	}
	rtPanic(line, col, "cannot cast %s to char", v.TypeTag())
	return &NullVal{}
}

// ---- Literals ------------------------------------------------------------

func evalLiteral(l *Literal) RTValue {
	switch l.Kind {
	case TOKEN_INT_LIT:
		n, _ := strconv.ParseInt(l.Value, 10, 64)
		return &IntVal{Val: n}
	case TOKEN_FLOAT_LIT:
		f, _ := strconv.ParseFloat(l.Value, 64)
		return &FloatVal{Val: f}
	case TOKEN_BOOL_LIT:
		return &BoolVal{Val: l.Value == "true"}
	case TOKEN_CHAR_LIT:
		s := unescapeLiteralBody(strings.Trim(l.Value, "'"))
		r := rune(0)
		for _, c := range s {
			r = c
			break
		}
		return &CharVal{Val: r}
	case TOKEN_STRING_LIT:
		return &StringVal{Val: unescapeLiteralBody(strings.Trim(l.Value, `"`))}
	default: // 'null'
		return &NullVal{}
	}
}

func unescapeLiteralBody(s string) string {
	var sb strings.Builder
	for idx := 0; idx < len(s); idx++ {
		c := s[idx]
		if c == '\\' && idx+1 < len(s) {
			idx++
			switch s[idx] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case '\\':
				sb.WriteByte('\\')
			case '\'':
				sb.WriteByte('\'')
			case '"':
				sb.WriteByte('"')
			case '0':
				sb.WriteByte(0)
			default:
				sb.WriteByte(s[idx])
			}
			continue
		}
		sb.WriteByte(c)
	}
	return sb.String()
}
