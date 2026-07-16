package main

import (
	"fmt"
	"sort"
	"strings"
)

// Environment is the runtime counterpart to the parser's static Scope
// (symboltable.go): same parent-chain shape, but it binds names to heap
// addresses rather than to declaration metadata. Every block, function
// call, and loop iteration that needs its own scope gets a fresh
// Environment chained to its enclosing one.
type Environment struct {
	Parent *Environment
	vars   map[string]int // name -> heap address
}

func NewEnvironment(parent *Environment) *Environment {
	return &Environment{Parent: parent, vars: make(map[string]int)}
}

// Define binds a new name in *this* scope. Returns false if the name is
// already bound here (the interpreter shouldn't normally hit this, since
// the type checker already caught multiply-defined names -- but it stays
// defensive in case the interpreter is ever run standalone without a prior
// checker pass).
func (e *Environment) Define(name string, addr int) bool {
	if _, ok := e.vars[name]; ok {
		return false
	}
	e.vars[name] = addr
	return true
}

// Resolve looks up which heap address a name is currently bound to,
// walking outward through enclosing scopes.
func (e *Environment) Resolve(name string) (int, bool) {
	for env := e; env != nil; env = env.Parent {
		if addr, ok := env.vars[name]; ok {
			return addr, true
		}
	}
	return 0, false
}

// Rebind changes which address an existing name points to (used for plain
// `x = value;` on a 'let' binding). It writes into whichever scope in the
// chain actually owns the name, matching lexical scoping -- it does NOT
// always write into the innermost scope.
func (e *Environment) Rebind(name string, addr int) bool {
	for env := e; env != nil; env = env.Parent {
		if _, ok := env.vars[name]; ok {
			env.vars[name] = addr
			return true
		}
	}
	return false
}

// String renders the scope chain from innermost to outermost, resolving
// each address through h so the live symbol-table display shows actual
// current values, not just addresses.
func (e *Environment) String(h *Heap) string {
	var sb strings.Builder
	depth := 0
	for env := e; env != nil; env = env.Parent {
		names := make([]string, 0, len(env.vars))
		for n := range env.vars {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > 0 {
			sb.WriteString(fmt.Sprintf("  Scope %d:\n", depth))
			for _, n := range names {
				addr := env.vars[n]
				valStr := "<invalid>"
				if b, ok := h.Get(addr); ok {
					valStr = b.Value.String()
				}
				sb.WriteString(fmt.Sprintf("    %-12s @0x%04x = %s\n", n, addr, valStr))
			}
		}
		depth++
	}
	if sb.Len() == 0 {
		return "  (empty)\n"
	}
	return sb.String()
}

// ---- Call stack -------------------------------------------------------

// CallFrame is one active function invocation, tracked so the interpreter
// can display a live call stack (a separate rubric line item from the
// symbol table) and so recursion is visibly traceable rather than just
// silently working via Go's own call stack underneath.
type CallFrame struct {
	FnName   string
	Env      *Environment
	CallLine int
	CallCol  int
}

type CallStack struct {
	frames []*CallFrame
}

func NewCallStack() *CallStack { return &CallStack{} }

func (cs *CallStack) Push(f *CallFrame) { cs.frames = append(cs.frames, f) }

func (cs *CallStack) Pop() {
	if len(cs.frames) > 0 {
		cs.frames = cs.frames[:len(cs.frames)-1]
	}
}

func (cs *CallStack) Top() *CallFrame {
	if len(cs.frames) == 0 {
		return nil
	}
	return cs.frames[len(cs.frames)-1]
}

func (cs *CallStack) Depth() int { return len(cs.frames) }

// String renders the stack top-first (most recent call first), which is
// the conventional debugger presentation and also makes runaway recursion
// easy to eyeball during line-by-line execution.
func (cs *CallStack) String() string {
	if len(cs.frames) == 0 {
		return "  (empty — top level)\n"
	}
	var sb strings.Builder
	for i := len(cs.frames) - 1; i >= 0; i-- {
		f := cs.frames[i]
		sb.WriteString(fmt.Sprintf("  #%d  %s()  called at %d:%d\n", i, f.FnName, f.CallLine, f.CallCol))
	}
	return sb.String()
}
