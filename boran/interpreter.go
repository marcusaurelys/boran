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

// BoranThrow is raised via panic/recover for a *catchable* runtime fault:
// either an explicit 'throw' statement, or one of the handful of native
// errors try/catch can intercept (division by zero, a nonnumeric-string
// cast, a null-pointer dereference). Distinct from RuntimeError (which is
// always fatal) so execTryCatch's recover can tell the two apart --
// anything else that panics is re-raised unchanged and still terminates
// the program even inside a try block.
type BoranThrow struct {
	Line, Col int
	Message   string
}

func (e *BoranThrow) Error() string {
	return fmt.Sprintf("%d:%d: uncaught throw: %s", e.Line, e.Col, e.Message)
}

func rtThrow(line, col int, format string, args ...interface{}) {
	panic(&BoranThrow{Line: line, Col: col, Message: fmt.Sprintf(format, args...)})
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

	OnStep      StepHook
	BeforeInput func(prompt string) // fired right before input() blocks on ReadString, if set
	AfterInput  func()              // fired right after input() reads a line, if set
	Errors      []*RuntimeError
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
			if bt, ok := r.(*BoranThrow); ok {
				// A 'throw' (or native-error throw) that escaped every
				// enclosing try/catch -- same fatal treatment as an
				// uncaught RuntimeError, just reported as an uncaught
				// throw rather than a generic runtime error.
				re := &RuntimeError{Line: bt.Line, Col: bt.Col, Message: "uncaught throw: " + bt.Message}
				i.Errors = append(i.Errors, re)
				err = re
				return
			}
			panic(r)
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

func isEphemeralArraySource(e Expr) bool {
	switch e.(type) {
	case *RangeExpr:
		return true
	}
	return false
}

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
		if n.Target.Deref == 0 && len(n.Target.Suffixes) == 0 {
			// Plain 'x = value;' / 'this = value;' -- no lvalue chain, so
			// no heap address is needed unless x was already promoted
			// (its address taken elsewhere). Write straight into the slot.
			name := n.Target.Name
			if n.Target.Kind == TargetThis {
				name = "this"
			}
			_, slot, ok := env.ResolveOwner(name)
			if !ok {
				rtPanic(line, col, "assignment to undeclared variable %q", name)
			}
			var declType *DatatypeNode
			if slot.Heap {
				declType = i.declaredTypeAtAddr(slot.Addr)
			} else if _, isPtr := slot.Value.(*PtrVal); isPtr {
				// Inline pointer slot: declaredTypeAtAddr only recovers a
				// type from a heap-boxed struct, so a plain 'p = null;'
				// would otherwise lose the pointer-type context a fresh
				// 'let p : T* = null' declaration gets -- see the
				// *ExprValue/'null' case in evalValue.
				declType = &DatatypeNode{Kind: "ptr"}
			}
			val := i.evalValue(n.Value, env, declType)
			if newSv, isSv := val.(*StructVal); isSv && n.Target.Kind == TargetThis && isStructInstanceLiteral(n.Value) && slot.Heap {
				// 'this = { ...literal... };' -- special-cased ahead of the
				// general container-rebind branch below: 'this' aliases the
				// caller's own variable, so it needs the shared-struct-mutated-
				// in-place treatment, not the ordinary "point this local binding
				// somewhere new" rebind that's correct for a plain local variable.
				i.rebindThis(slot.Addr, newSv)
			} else if newAddr, isContainer := containerAddr(val); isContainer {
				// Container-typed reassignment rebinds the slot's own
				// identity rather than mutating the old box in place --
				// otherwise any OTHER alias of the old container (e.g. a
				// 'let other = x;' taken before this assignment) would
				// see its contents silently replaced too.
				if slot.Heap {
					i.decref(slot.Addr)
				} else {
					i.release(slot.Value)
				}
				slot.Heap = true
				slot.Addr = newAddr
				if needsIncref(n.Value) {
					i.incref(newAddr)
				}
			} else if pAddr, isPtr := ownedPtrAddr(val); isPtr {
				// Owning-pointer reassignment ('p = new(...)' or
				// 'p = someOtherPtr;'): release whatever this slot
				// previously owned before adopting the new target, or
				// the old target would never be freed -- symmetric with
				// the container-rebind branch above.
				if slot.Heap {
					if old, ok := i.Heap.Get(slot.Addr); ok {
						i.release(old.Value)
					}
					i.Heap.Set(slot.Addr, val)
				} else {
					i.release(slot.Value)
					slot.Value = val
				}
				if needsIncref(n.Value) {
					i.incref(pAddr)
				}
			} else if slot.Heap {
				if old, ok := i.Heap.Get(slot.Addr); ok {
					i.release(old.Value)
				}
				i.Heap.Set(slot.Addr, val)
			} else {
				i.release(slot.Value)
				slot.Value = val
			}
			return sigNone
		}
		addr := i.addressOfTarget(n.Target, env)
		val := i.evalValue(n.Value, env, i.declaredTypeAtAddr(addr))
		old, _ := i.Heap.Get(addr)
		var oldVal RTValue
		if old != nil {
			oldVal = old.Value
		}
		i.Heap.Set(addr, val)
		if oldVal != nil {
			i.release(oldVal)
		}
		if needsIncref(n.Value) {
			i.retain(val)
		}
		return sigNone

	case *Block:
		child := NewEnvironment(env)
		for _, st := range n.Statements {
			sig := i.execStmt(st, child)
			if sig.kind != ctrlNone {
				i.teardown(child)
				return sig
			}
		}
		i.teardown(child)
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

		ephemeral := isEphemeralArraySource(n.Iter)
		// Whatever index the loop stops at -- whether it walks off the end
		// normally, or exits early via break/return -- everything from that
		// point in arr.Elems onward was never visited, so it never got its
		// per-iteration free below. Track it here and sweep the remainder
		// (plus the array's own header) in one deferred cleanup covering
		// every exit path uniformly, rather than duplicating the same
		// free-the-rest logic at each of the three separate return points.
		visited := 0
		if ephemeral {
			defer func() {
				for _, elemAddr := range arr.Elems[visited:] {
					i.Heap.Free(elemAddr)
				}
				i.Heap.Free(arr.HeapAddr)
			}()
		}

		for idx, elemAddr := range arr.Elems {
			visited = idx + 1
			child := NewEnvironment(env)
			box, ok := i.Heap.Get(elemAddr)
			if !ok {
				rtPanic(line, col, "dangling array element reference")
			}
			if cAddr, isContainer := containerAddr(box.Value); isContainer {
				// Reading a container-typed element is an aliasing read
				// of an existing structural place (arr's own Elems
				// entry), same as reading any other variable/field --
				// needs its own real reference for the loop var's slot.
				child.DefineAddr(n.VarName, cAddr)
				i.incref(cAddr)
			} else if pAddr, isPtr := ownedPtrAddr(box.Value); isPtr {
				// Same aliasing-read logic, but ONLY when the array
				// survives the loop: in ephemeral mode the element's
				// wrapper box (elemAddr) is about to be force-freed below
				// without decref'ing what it owns, which already
				// transfers that one claim to the loop var untouched --
				// incref-ing here too would leak it (two claims, only
				// one release at teardown). In non-ephemeral mode the
				// array keeps its own claim, so the loop var genuinely
				// needs a fresh temporary one, released by teardown at
				// the end of this iteration.
				child.Define(n.VarName, box.Value)
				if !ephemeral {
					i.incref(pAddr)
				}
			} else {
				child.Define(n.VarName, box.Value)
			}
			if ephemeral {
				// value's already been copied into the loop var above;
				// this cell is now provably unreachable from anywhere.
				i.Heap.Free(elemAddr)
			}

			sig := i.execStmt(n.Body, child)
			i.teardown(child)
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
		fmt.Fprint(i.Output, strings.Join(parts, " "))
		return sigNone

	case *ReturnStmt:
		if n.Value == nil {
			return ctrlSignal{kind: ctrlReturn, value: &NullVal{}}
		}
		v := i.evalExpr(n.Value, env)
		// Protective retain: about to unwind through invoke's teardown,
		// which will remove whatever local/param references currently
		// keep v alive (possibly several, if it's been aliased through a
		// chain of locals). This +1 guarantees the count never dips to 0
		// mid-teardown; the caller's own binding (or discard) is what
		// ultimately accounts for -- and balances -- this reference.
		i.retain(v)
		return ctrlSignal{kind: ctrlReturn, value: v}

	case *BreakStmt:
		return ctrlSignal{kind: ctrlBreak}

	case *ContinueStmt:
		return ctrlSignal{kind: ctrlContinue}

	case *ExprStmt:
		i.evalExpr(n.Call, env)
		return sigNone

	case *ThrowStmt:
		v := i.evalExpr(n.Value, env)
		sv, ok := v.(*StringVal)
		if !ok {
			rtPanic(line, col, "'throw' requires a string value, got %s", v.TypeTag())
		}
		rtThrow(line, col, "%s", sv.Val)
		return sigNone // unreachable -- rtThrow always panics

	case *TryCatchStmt:
		return i.execTryCatch(n, env)
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
	if addr, ok := containerAddr(rv); ok {
		env.DefineAddr(name, addr)
		if needsIncref(val) {
			i.incref(addr)
		}
		return
	}
	if pAddr, ok := ownedPtrAddr(rv); ok {
		// Unlike a container, the pointer value itself still lives
		// inline in this slot (env.Define, not DefineAddr) -- only the
		// separate cell it points at is heap-tracked/refcounted.
		if needsIncref(val) {
			i.incref(pAddr)
		}
		env.Define(name, rv)
		return
	}
	env.Define(name, rv)
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

// readSlot returns the current value held by slot, resolving through the
// heap if the binding has been promoted, or reading it directly if it's
// still an inline "stack" value.
func (i *Interpreter) readSlot(slot *Slot, line, col int, name string) RTValue {
	if !slot.Heap {
		return slot.Value
	}
	box, ok := i.Heap.Get(slot.Addr)
	if !ok {
		rtPanic(line, col, "invalid/freed reference for %q", name)
	}
	return box.Value
}

// promote ensures name's binding (found by walking env's parent chain) is
// backed by a real heap address, allocating one now if it's still an
// inline slot, and returns that address. Idempotent -- promoting an
// already-promoted binding just returns its existing address. This is the
// only place a plain variable ever gains heap identity: '&', '++'/'--',
// and lvalue chains (a.b, a[i], ...) rooted at a name all funnel through
// here via addressOfExpr/addressOfTarget.
func (i *Interpreter) promote(env *Environment, name string) int {
	_, slot, ok := env.ResolveOwner(name)
	if !ok {
		panic(fmt.Sprintf("internal error: promote called on unresolvable name %q", name))
	}
	if !slot.Heap {
		slot.Addr = i.Heap.Alloc(slot.Value)
		slot.Value = nil
		slot.Heap = true
	}
	return slot.Addr
}

// ---- Reference counting ------------------------------------------------
//
// Only containers (*ArrayVal / *StructVal) participate in refcounting --
// they're the only values with a persistent heap identity (HeapAddr) that
// can legitimately be shared across more than one Environment slot. Plain
// scalars stay Phase 1's inline-or-promoted-once model (never shared, so
// teardown just frees them directly, no counting needed). Pointers ('&x')
// are deliberately *not* retained/released by anything below -- they're
// weak/non-owning references, matching C-like semantics: dereferencing a
// pointer whose target has since been freed is a detectable runtime error
// (via Heap.Get), not a crash, and that's the accepted tradeoff rather
// than pointers keeping their targets alive forever.

// containerAddr returns v's own persistent heap address if v is a
// container, or ok=false for anything else (scalars, pointers, etc.).
func containerAddr(v RTValue) (int, bool) {
	switch val := v.(type) {
	case *ArrayVal:
		return val.HeapAddr, true
	case *StructVal:
		return val.HeapAddr, true
	}
	return 0, false
}

// ownedPtrAddr returns the target address an *owning* pointer (one
// created via 'new(...)') is responsible for, so callers can incref/
// decref it the same way containerAddr's callers do for arrays/structs.
// Unlike containerAddr, the returned address is NOT this value's own
// identity -- a PtrVal is always stored inline (or in its own private
// wrapper box) at its binding site; ownedPtrAddr only ever answers "what
// heap cell does this pointer keep alive," never "what heap cell is this
// value."  A non-owning pointer ('&x') returns ok=false: it never frees
// anything, since it doesn't own what it points to.
func ownedPtrAddr(v RTValue) (int, bool) {
	if pv, ok := v.(*PtrVal); ok && pv.Owned {
		return pv.Addr, true
	}
	return 0, false
}

func (i *Interpreter) incref(addr int) {
	if box, ok := i.Heap.Get(addr); ok {
		box.RefCount++
	}
}

// decref removes one structural reference to addr. If that was the last
// one, it cascades: every address the freed container itself references
// (its Elems/Fields) gets decref'd too, recursively freeing anything that
// was only reachable through it.
func (i *Interpreter) decref(addr int) {

	box, ok := i.Heap.Get(addr)
	if !ok {
		return
	}
	box.RefCount--
	if box.RefCount > 0 {
		return
	}
	switch val := box.Value.(type) {
	case *ArrayVal:
		for _, elemAddr := range val.Elems {
			i.decref(elemAddr)
		}
	case *StructVal:
		for _, fieldAddr := range val.Fields {
			i.decref(fieldAddr)
		}
	case *PtrVal:
		// A wrapper box (a scalar array element / struct field) whose
		// content is an owning pointer: freeing this box must also
		// release the separate cell it owns, or that cell would leak --
		// nothing else will ever call decref on it once this box is gone.
		if val.Owned {
			i.decref(val.Addr)
		}
	}
	i.Heap.Free(addr)
}

// retain/release are the convenience entry points used at every binding
// site below: no-ops for anything without a container identity, so
// callers don't need their own type switch first.
func (i *Interpreter) retain(v RTValue) {
	if addr, ok := containerAddr(v); ok {
		i.incref(addr)
	} else if addr, ok := ownedPtrAddr(v); ok {
		i.incref(addr)
	}
}

func (i *Interpreter) release(v RTValue) {
	if addr, ok := containerAddr(v); ok {
		i.decref(addr)
	} else if addr, ok := ownedPtrAddr(v); ok {
		i.decref(addr)
	}
}

// needsIncref/needsIncrefExpr decide whether *binding* a value to a new
// structural place (a new 'let'/'const', a reassignment, a function
// parameter) should add a real reference, or whether the value already
// arrives holding the one reference this new binding needs:
//   - reading an EXISTING place (a variable, a field, an element) is
//     aliasing -- the old place keeps its reference, so the new one needs
//     a genuinely additional increment.
//   - a fresh literal, or a function call's result, is NOT aliasing
//     anything yet: a literal starts at refcount 0 (unclaimed) and this is
//     its first claim; a call result already survived its own frame's
//     teardown via ReturnStmt's protective retain (see below), so it's
//     already carrying exactly the reference this new binding needs.
//
// Getting this wrong in either direction either double-counts (permanent
// leak) or under-counts (premature free of a still-aliased value) -- both
// were tried and ruled out before landing on this rule.
func needsIncref(val Value) bool {
	ev, ok := val.(*ExprValue)
	if !ok {
		return true // ArrLiteral, StructInstance: fresh construction
	}
	return needsIncrefExpr(ev.Expr)
}

// isStructInstanceLiteral reports whether val is a bare struct-instance
// literal ('{ x: ..., y: ... }'), as opposed to an aliasing read (a plain
// variable, field, or element) that merely evaluates to a struct. Used to
// gate rebindThis: harvesting fields out of the RHS is only safe when the
// RHS is freshly constructed for this assignment and nothing else could
// possibly be holding a reference to it yet.
func isStructInstanceLiteral(val Value) bool {
	_, ok := val.(*StructInstance)
	return ok
}

// rebindThis implements 'this = { ...literal... };' by mutating the
// struct this already points at in place, field by field, rather than
// rebinding this's own callEnv slot to the literal's address. 'this' is
// an alias for the caller's own variable -- rebinding just the local
// slot would be invisible to the caller once callEnv is torn down at
// return, since the caller's own binding still points at the original
// address the whole time. Mutating the shared struct's Fields map keeps
// the caller-visible identity (thisAddr) unchanged, just with each
// field's content replaced.
//
// newSv's own outer shell (its self-box) becomes unreachable once its
// fields are harvested; since a fresh literal's shell is never itself
// referenced by anyone (0-baseline, same convention as everywhere else
// fresh containers are built), it's freed directly rather than via
// decref/release -- decref would incorrectly cascade into the fields
// that were just migrated into oldSv, re-freeing them out from under
// their new owner.
func (i *Interpreter) rebindThis(thisAddr int, newSv *StructVal) {
	oldBox, ok := i.Heap.Get(thisAddr)
	if !ok {
		return
	}
	oldSv, ok := oldBox.Value.(*StructVal)
	if !ok {
		return
	}
	for _, name := range newSv.order {
		newFieldAddr := newSv.Fields[name]
		if oldFieldAddr, exists := oldSv.Fields[name]; exists {
			if fbox, ok := i.Heap.Get(oldFieldAddr); ok {
				if _, isContainer := containerAddr(fbox.Value); isContainer {
					i.release(fbox.Value)
				} else {
					// Scalar field: no refcount tracks the wrapper box
					// itself, and we're abandoning this specific box (not
					// reusing it), so it needs an explicit direct free or
					// it leaks. But if it holds an *owning* pointer, that
					// pointer's separate target cell needs releasing too,
					// or freeing just this wrapper would leak the target.
					if pAddr, isPtr := ownedPtrAddr(fbox.Value); isPtr {
						i.decref(pAddr)
					}
					i.Heap.Free(oldFieldAddr)
				}
			}
		}
		oldSv.SetField(name, newFieldAddr)
	}
	i.Heap.Free(newSv.HeapAddr)
}

func needsIncrefExpr(e Expr) bool {
	for {
		g, ok := e.(*GroupExpr)
		if !ok {
			break
		}
		e = g.Inner
	}
	switch e.(type) {
	case *FnCall, *MethodCall:
		return false
	}
	return true
}

// teardown releases every container-typed local declared directly in env,
// and frees every promoted-scalar local outright (they're never shared,
// so no counting is needed for them -- see the section comment above).
// Call this exactly once per scope exit: Block, a ForIterStmt iteration's
// child env, and invoke's callEnv on both its exit paths.
func (i *Interpreter) teardown(env *Environment) {
	for _, slot := range env.vars {
		if !slot.Heap {
			// Never promoted onto the heap, so there's no wrapper box
			// here for a plain scalar to leak -- Go's own GC reclaims
			// the slot. The one exception is an *owning* pointer
			// ('new(...)'): its VALUE is inline here, but it's still
			// responsible for a real, separately-tracked heap cell that
			// nothing else will release unless we do it now.
			if pAddr, ok := ownedPtrAddr(slot.Value); ok {
				i.decref(pAddr)
			}
			continue
		}
		box, ok := i.Heap.Get(slot.Addr)
		if !ok {
			continue // already freed
		}
		switch v := box.Value.(type) {
		case *ArrayVal, *StructVal:
			i.decref(slot.Addr)
		case *PtrVal:
			// A promoted (heap-boxed) pointer local, e.g. one that had
			// '&' taken on it. Release whatever it owns before freeing
			// its own wrapper box.
			if v.Owned {
				i.decref(v.Addr)
			}
			i.Heap.Free(slot.Addr)
		default:
			i.Heap.Free(slot.Addr)
		}
	}
}

// execTryCatch implements 'try { ... } catch (e) { ... }'. The try block
// runs in its own child scope, same as an ordinary Block; if it completes
// normally (including via a return/break/continue that should keep
// propagating), that signal is returned unchanged and the catch block
// never runs. If it panics with a *BoranThrow (an explicit 'throw', or
// one of the native errors division-by-zero/bad-cast/null-deref raise),
// runTryBlock recovers it and this method runs the catch block instead,
// with the caught message bound to CatchVar as a plain string. Any other
// panic (a *RuntimeError, or a genuine Go runtime panic) is NOT ours to
// catch and propagates past this call unchanged, exactly like it would
// through an ordinary Block.
func (i *Interpreter) execTryCatch(n *TryCatchStmt, env *Environment) ctrlSignal {
	sig, threw, msg := i.runTryBlock(n.Try, env)
	if !threw {
		return sig
	}

	catchEnv := NewEnvironment(env)
	catchEnv.Define(n.CatchVar, &StringVal{Val: msg})
	for _, st := range n.Catch.Statements {
		csig := i.execStmt(st, catchEnv)
		if csig.kind != ctrlNone {
			i.teardown(catchEnv)
			return csig
		}
	}
	i.teardown(catchEnv)
	return sigNone
}

// runTryBlock executes one try-block's statements in their own child
// scope, recovering a *BoranThrow rather than letting it unwind further.
// Whatever locals the block managed to declare before the throw still get
// torn down (teardown runs unconditionally, before the recover check), so
// a throw partway through a try block doesn't leak whatever it already
// allocated. Anything other than a *BoranThrow is re-panicked so it keeps
// propagating exactly as if this try block weren't here.
func (i *Interpreter) runTryBlock(block *Block, env *Environment) (sig ctrlSignal, threw bool, msg string) {
	tryEnv := NewEnvironment(env)
	defer func() {
		i.teardown(tryEnv)
		if r := recover(); r != nil {
			if bt, ok := r.(*BoranThrow); ok {
				threw = true
				msg = bt.Message
				return
			}
			panic(r)
		}
	}()
	for _, st := range block.Statements {
		s := i.execStmt(st, tryEnv)
		if s.kind != ctrlNone {
			sig = s
			return
		}
	}
	return
}

// addressOfTarget resolves an AssignStmt's AssignTarget (the specialized
// lvalue-chain type, distinct from a general Expr) down to the heap address
// that should actually be overwritten.
func (i *Interpreter) addressOfTarget(t AssignTarget, env *Environment) int {
	line, col := t.Pos()

	// Resolve the root binding's *owning* environment and slot, but don't
	// promote yet -- indexing/field access into it doesn't need the root
	// itself to have a heap address (see addressOfExpr for the same fix).
	var ownerEnv *Environment
	var slot *Slot
	var ok bool
	var name string
	switch t.Kind {
	case TargetIdent:
		name = t.Name
		ownerEnv, slot, ok = env.ResolveOwner(t.Name)
		if !ok {
			rtPanic(line, col, "assignment to undeclared variable %q", t.Name)
		}
	case TargetThis:
		name = "this"
		ownerEnv, slot, ok = env.ResolveOwner("this")
		if !ok {
			rtPanic(line, col, "'this' used outside of a method")
		}
	}

	if t.Deref == 0 && len(t.Suffixes) == 0 {
		return i.promote(ownerEnv, name)
	}

	cur := i.readSlot(slot, line, col, name)

	var addr int
	haveAddr := false
	for d := 0; d < t.Deref; d++ {
		var ptr *PtrVal
		if !haveAddr {
			ptr, ok = cur.(*PtrVal)
		} else {
			box, gok := i.Heap.Get(addr)
			if !gok {
				rtThrow(line, col, "null pointer dereference")
			}
			ptr, ok = box.Value.(*PtrVal)
		}
		if !ok {
			rtPanic(line, col, "cannot dereference non-pointer value")
		}
		addr = ptr.Addr
		haveAddr = true
	}
	if haveAddr {
		box, gok := i.Heap.Get(addr)
		if !gok {
			rtPanic(line, col, "invalid/freed reference in assignment target")
		}
		cur = box.Value
	}

	for idx, suf := range t.Suffixes {
		if idx > 0 {
			box, gok := i.Heap.Get(addr)
			if !gok {
				rtPanic(line, col, "invalid/freed reference in assignment target")
			}
			cur = box.Value
		}
		switch suf.Kind {
		case SuffixField:
			sv, sok := cur.(*StructVal)
			if !sok {
				rtPanic(line, col, "'.%s' used on a non-struct value", suf.Field)
			}
			fieldAddr, fok := sv.Fields[suf.Field]
			if !fok {
				rtPanic(line, col, "%q is not a field of struct %q", suf.Field, sv.TypeName)
			}
			addr = fieldAddr
		case SuffixIndex:
			arr, aok := cur.(*ArrayVal)
			if !aok {
				rtPanic(line, col, "'[...]' used on a non-array value")
			}
			idxVal := i.evalExpr(suf.Index, env)
			iv, iok := idxVal.(*IntVal)
			if !iok {
				rtPanic(line, col, "array index must be int, got %s", idxVal.TypeTag())
			}
			if iv.Val < 0 || int(iv.Val) >= len(arr.Elems) {
				rtPanic(line, col, "array index %d out of bounds (length %d)", iv.Val, len(arr.Elems))
			}
			addr = arr.Elems[iv.Val]
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
		if _, _, ok := env.ResolveOwner(n.Name); !ok {
			rtPanic(line, col, "undeclared variable %q", n.Name)
		}
		return i.promote(env, n.Name)

	case *ThisExpr:
		if _, _, ok := env.ResolveOwner("this"); !ok {
			rtPanic(line, col, "'this' used outside of a method")
		}
		return i.promote(env, "this")

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
				rtThrow(line, col, "null pointer dereference")
			}
			return ptr.Addr
		}

	case *MemberAccess:
		baseVal := i.evalExpr(n.Base, env)
		sv, ok := baseVal.(*StructVal)
		if !ok {
			rtPanic(line, col, "'.%s' used on a non-struct value", n.Field)
		}
		fieldAddr, ok := sv.Fields[n.Field]
		if !ok {
			rtPanic(line, col, "%q is not a field of struct %q", n.Field, sv.TypeName)
		}
		return fieldAddr

	case *IndexExpr:
		baseVal := i.evalExpr(n.Base, env)
		arr, ok := baseVal.(*ArrayVal)
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

// zeroValueFor returns the default "empty" runtime value for a declared
// type: 0 for int, 0.0 for float, "" for string, false for bool, the NUL
// char for char. Arrays (including arrays-of-arrays) and any type we don't
// have a concrete zero for just default to int 0, per spec -- a missing
// slot in an array literal is filled with a plain scalar placeholder
// rather than a recursively-built nested structure. Used to pad short
// array literals up to their declared length and to seed struct fields
// that have neither an instance value nor a declared default.
func zeroValueFor(dtype *DatatypeNode) RTValue {
	if dtype == nil {
		return &IntVal{Val: 0}
	}
	switch typeTagOf(dtype) {
	case "float":
		return &FloatVal{Val: 0}
	case "string":
		return &StringVal{Val: ""}
	case "bool":
		return &BoolVal{Val: false}
	case "char":
		return &CharVal{Val: 0}
	case "ptr":
		return &PtrVal{Addr: 0} // null pointer, not owning -- nothing to free
	default: // int, array, struct, enum, fn, named/unknown
		return &IntVal{Val: 0}
	}
}

func (i *Interpreter) evalValue(v Value, env *Environment, dtype *DatatypeNode) RTValue {
	typeTag := typeTagOf(dtype)
	if v == nil {
		// 'let x : T' with no '=' initializer at all. For an array-typed
		// declaration this still has a fixed declared length, so build a
		// full zero-filled array rather than a bare scalar placeholder;
		// everything else just gets its scalar zero value.
		if dtype != nil && dtype.Kind == "array" {
			elems := make([]int, dtype.ArrLen)
			for idx := range elems {
				elems[idx] = i.Heap.Alloc(zeroValueFor(dtype.Elem))
			}
			av := &ArrayVal{Elems: elems, ElemType: typeTagOf(dtype.Elem)}
			av.HeapAddr = i.Heap.Alloc(av)
			return av
		}
		return zeroValueFor(dtype)
	}
	switch val := v.(type) {
	case *ExprValue:
		// A bare 'null' literal being placed into a pointer-typed slot:
		// evalExpr/evalLiteral has no type context of its own and would
		// otherwise hand back a generic NullVal, which isn't a *PtrVal
		// and so can never actually trigger the catchable "null pointer
		// dereference" throw on '*p' -- it would instead hit the
		// unrelated (and uncatchable) "not a pointer" type error. Give
		// it a real null pointer (address 0, non-owning -- nothing to
		// ever free) whenever we know the target type is a pointer.
		if lit, ok := val.Expr.(*Literal); ok && lit.Kind == TOKEN_KEYWORD && lit.Value == "null" && dtype != nil && dtype.Kind == "ptr" {
			return &PtrVal{Addr: 0}
		}
		return i.evalExpr(val.Expr, env)

	case *ArrLiteral:
		var elemDtype *DatatypeNode
		if dtype != nil && dtype.Kind == "array" {
			elemDtype = dtype.Elem
		}
		elemType := typeTagOf(elemDtype)
		// A declared array type carries its own fixed length. If the
		// literal supplies fewer elements than that (including the empty
		// '[]' literal), the remaining slots are filled with the type's
		// zero value (0 / 0.0 / "" / false) rather than left missing --
		// missing elements were previously silently dropped, leaving the
		// array shorter than its declared type and prone to spurious
		// out-of-bounds errors down the line.
		declaredLen := len(val.Elements)
		if dtype != nil && dtype.Kind == "array" && dtype.ArrLen > declaredLen {
			declaredLen = dtype.ArrLen
		}
		elems := make([]int, declaredLen)
		for idx := 0; idx < declaredLen; idx++ {
			var ev RTValue
			if idx < len(val.Elements) {
				ev = i.evalValue(val.Elements[idx], env, elemDtype)
				if idx == 0 && elemType == "" {
					elemType = ev.TypeTag()
				}
			} else {
				ev = zeroValueFor(elemDtype)
			}
			if cAddr, isContainer := containerAddr(ev); isContainer {
				// A container element is identified by its own HeapAddr --
				// store that directly rather than wrapping it in a second
				// box, and incref it here: this array's Elems entry is now
				// a real, permanent structural owner, independent of
				// whatever transient reference a loop variable or 'this'
				// binding might separately take on it later.
				elems[idx] = cAddr
				i.incref(cAddr)
			} else if pAddr, isPtr := ownedPtrAddr(ev); isPtr {
				// Same idea for a pointer element, except the pointer
				// value itself still needs its own private wrapper box
				// (its identity isn't its target's address) -- only the
				// target it owns gets the extra structural reference.
				elems[idx] = i.Heap.Alloc(ev)
				i.incref(pAddr)
			} else {
				elems[idx] = i.Heap.Alloc(ev)
			}
		}
		if elemType == "" {
			elemType = typeTagOf(elemDtype)
		}
		av := &ArrayVal{Elems: elems, ElemType: elemType}
		av.HeapAddr = i.Heap.Alloc(av)
		return av

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
				var fv RTValue
				if f.Default != nil {
					fv = i.evalValue(f.Default, env, f.DeclaredType)
				} else {
					// A 'let' field with no '=' initializer in the struct
					// definition: seed it with its type's zero value (0 /
					// 0.0 / "" / false) instead of a bare null, matching
					// how a short/empty array literal is padded.
					fv = zeroValueFor(f.DeclaredType)
				}
				if cAddr, isContainer := containerAddr(fv); isContainer {
					sv.SetField(f.Name, cAddr)
					i.incref(cAddr)
				} else if pAddr, isPtr := ownedPtrAddr(fv); isPtr {
					sv.SetField(f.Name, i.Heap.Alloc(fv))
					i.incref(pAddr)
				} else {
					sv.SetField(f.Name, i.Heap.Alloc(fv))
				}
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
					if cAddr, isContainer := containerAddr(fv); isContainer {
						i.decref(addr)
						sv.SetField(fi.Name, cAddr)
						if needsIncref(fi.Value) {
							i.incref(cAddr)
						}
					} else {
						old, _ := i.Heap.Get(addr)
						var oldVal RTValue
						if old != nil {
							oldVal = old.Value
						}
						i.Heap.Set(addr, fv)
						if oldVal != nil {
							i.release(oldVal)
						}
						if pAddr, isPtr := ownedPtrAddr(fv); isPtr && needsIncref(fi.Value) {
							i.incref(pAddr)
						}
					}
				} else {
					if cAddr, isContainer := containerAddr(fv); isContainer {
						sv.SetField(fi.Name, cAddr)
						i.incref(cAddr)
					} else if pAddr, isPtr := ownedPtrAddr(fv); isPtr {
						sv.SetField(fi.Name, i.Heap.Alloc(fv))
						i.incref(pAddr)
					} else {
						sv.SetField(fi.Name, i.Heap.Alloc(fv))
					}
				}
			}
			sv.HeapAddr = i.Heap.Alloc(sv)
			return sv
		}
		// Unknown/unregistered type name: still build something usable
		// rather than failing outright, matching the type checker's
		// best-effort stance on unresolved names.
		for _, fi := range val.Fields {
			fv := i.evalValue(fi.Value, env, nil)
			if cAddr, isContainer := containerAddr(fv); isContainer {
				sv.SetField(fi.Name, cAddr)
				i.incref(cAddr)
			} else if pAddr, isPtr := ownedPtrAddr(fv); isPtr {
				sv.SetField(fi.Name, i.Heap.Alloc(fv))
				i.incref(pAddr)
			} else {
				sv.SetField(fi.Name, i.Heap.Alloc(fv))
			}
		}
		sv.HeapAddr = i.Heap.Alloc(sv)
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
		slot, ok := env.Resolve(n.Name)
		if !ok {
			rtPanic(line, col, "undeclared variable %q", n.Name)
		}
		return i.readSlot(slot, line, col, n.Name)

	case *ThisExpr:
		slot, ok := env.Resolve("this")
		if !ok {
			rtPanic(line, col, "'this' used outside of a method")
		}
		return i.readSlot(slot, line, col, "this")

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

	case *RangeExpr:
		return i.evalRange(n, env, line, col)

	case *NewExpr:
		// Fresh allocation, no other owner yet -- starts at refcount 0,
		// same convention as ArrLiteral/StructInstance. The binding site
		// (execDecl, AssignStmt, array/struct construction, param
		// binding, ...) claims the first (and only) reference via
		// retain()/incref, gated by needsIncref exactly like every other
		// fresh-vs-aliased value in this interpreter.
		v := i.evalExpr(n.Arg, env)
		addr := i.Heap.Alloc(v)
		return &PtrVal{Addr: addr, Owned: true}

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
		slot, ok := env.Resolve(n.Callee)
		if !ok {
			rtPanic(line, col, "call to undeclared function %q", n.Callee)
		}
		calleeVal := i.readSlot(slot, line, col, n.Callee)
		fn, ok := calleeVal.(*FnVal)
		if !ok {
			rtPanic(line, col, "%q is not callable (got %s)", n.Callee, calleeVal.TypeTag())
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
		v := i.evalExpr(a, env)
		if needsIncrefExpr(a) {
			// Aliasing an existing variable/field/element into a call:
			// the callee's parameter binding is a genuinely additional,
			// temporary reference for the duration of the call (retain
			// here, matching invoke's unconditional teardown decref on
			// exit). Fresh literals/nested call results are left alone,
			// same reasoning as needsIncref for declarations.
			i.retain(v)
		}
		args[idx] = v
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
		// The receiver is always an alias of an existing struct instance
		// (never fresh construction), so this call's borrow of it always
		// needs its own real reference -- symmetric with the unconditional
		// decref every container-typed local gets in teardown below.
		callEnv.DefineAddr("this", *thisAddr)
		i.incref(*thisAddr)
	}
	for idx, p := range fn.Lit.Params {
		// evalArgs already retained container-typed args that needed it
		// (see needsIncrefExpr there); param binding here just claims
		// that same address, uniformly, regardless of whether it was
		// freshly retained or arrived pre-counted.
		if addr, ok := containerAddr(args[idx]); ok {
			callEnv.DefineAddr(p.Name, addr)
		} else {
			callEnv.Define(p.Name, args[idx])
		}
	}

	i.Stack.Push(&CallFrame{FnName: label, Env: callEnv, CallLine: line, CallCol: col})
	defer i.Stack.Pop()

	for _, st := range fn.Lit.Body.Statements {
		sig := i.execStmt(st, callEnv)
		if sig.kind == ctrlReturn {
			i.teardown(callEnv)
			return sig.value
		}
		if sig.kind == ctrlBreak || sig.kind == ctrlContinue {
			rtPanic(line, col, "'break'/'continue' used outside of a loop")
		}
	}
	i.teardown(callEnv)
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
			rtThrow(line, col, "null pointer dereference")
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
				rtThrow(line, col, "division by zero")
			}
			return &IntVal{Val: li / ri}
		case "%":
			if ri == 0 {
				rtThrow(line, col, "division by zero")
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
			rtThrow(line, col, "division by zero")
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

// evalRange implements range(...)'s three arities. Every arg is coerced to
// an int64 (floats truncate, same rule as an explicit 'as int' cast) so
// range(3.5) and friends behave predictably rather than erroring outright.
// The result is a perfectly ordinary heap-backed ArrayVal -- range is pure
// sugar for building one, so 'for x in range(5)' reuses the existing
// for-in machinery unchanged.
func (i *Interpreter) evalRange(n *RangeExpr, env *Environment, line, col int) RTValue {
	toInt64 := func(e Expr) int64 {
		v := i.evalExpr(e, env)
		switch val := v.(type) {
		case *IntVal:
			return val.Val
		case *FloatVal:
			return int64(val.Val)
		}
		rtPanic(line, col, "range() arguments must be int or float, got %s", v.TypeTag())
		return 0
	}

	var start, end, step int64
	switch len(n.Args) {
	case 1:
		start, end, step = 0, toInt64(n.Args[0]), 1
	case 2:
		start, end, step = toInt64(n.Args[0]), toInt64(n.Args[1]), 1
	case 3:
		start, end, step = toInt64(n.Args[0]), toInt64(n.Args[1]), toInt64(n.Args[2])
	default:
		rtPanic(line, col, "range() takes 1 to 3 arguments, got %d", len(n.Args))
	}
	if step == 0 {
		rtPanic(line, col, "range() step cannot be 0")
	}

	var elems []int
	if step > 0 {
		for v := start; v < end; v += step {
			elems = append(elems, i.Heap.Alloc(&IntVal{Val: v}))
		}
	} else {
		for v := start; v > end; v += step {
			elems = append(elems, i.Heap.Alloc(&IntVal{Val: v}))
		}
	}
	av := &ArrayVal{Elems: elems, ElemType: "int"}
	av.HeapAddr = i.Heap.Alloc(av)
	return av
}

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
			rtThrow(line, col, "cannot cast string %q to int: not a valid integer", val.Val)
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
			rtThrow(line, col, "cannot cast string %q to float: not a valid number", val.Val)
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
