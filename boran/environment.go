package main

import (
	"fmt"
	"sort"
	"strings"
)

// Slot is a binding's storage. Most locals never need heap identity, so a
// Slot starts out holding its RTValue inline (the "stack" case) and is
// only promoted to a real heap address the moment something takes '&' on
// it, indexes/derefs through it, etc. -- see Interpreter.promote.
type Slot struct {
	Heap  bool
	Value RTValue // valid when !Heap
	Addr  int     // valid when Heap
}

// Environment is the runtime counterpart to the parser's static Scope
// (symboltable.go): same parent-chain shape, but it binds names to heap
// addresses rather than to declaration metadata. Every block, function
// call, and loop iteration that needs its own scope gets a fresh
// Environment chained to its enclosing one.
type Environment struct {
	Parent *Environment
	vars   map[string]*Slot
	owned  []int
}

func NewEnvironment(parent *Environment) *Environment {
	return &Environment{Parent: parent, vars: make(map[string]*Slot)}
}

// Define binds a new name in *this* scope to an inline value -- no heap
// allocation. Returns false if the name is already bound here (the
// interpreter shouldn't normally hit this, since the type checker already
// caught multiply-defined names -- but it stays defensive in case the
// interpreter is ever run standalone without a prior checker pass).
func (e *Environment) Define(name string, val RTValue) bool {
	if _, ok := e.vars[name]; ok {
		return false
	}
	e.vars[name] = &Slot{Heap: false, Value: val}
	return true
}

// DefineAddr binds name directly to an existing heap address, skipping the
// inline slot entirely. Used for bindings that are already heap-identity
// values by construction -- currently just 'this', which always refers to
// an existing StructVal already living on the heap.
func (e *Environment) DefineAddr(name string, addr int) bool {
	if _, ok := e.vars[name]; ok {
		return false
	}
	e.vars[name] = &Slot{Heap: true, Addr: addr}
	return true
}

// Resolve looks up which heap address a name is currently bound to,
// walking outward through enclosing scopes.
func (e *Environment) Resolve(name string) (*Slot, bool) {
	for env := e; env != nil; env = env.Parent {
		if slot, ok := env.vars[name]; ok {
			return slot, true
		}
	}
	return nil, false
}

// ResolveOwner is like Resolve but also returns the specific Environment
// in the chain that actually owns the binding -- needed by promote(),
// which must mutate the Slot in place wherever it really lives, and by
// plain assignment, which writes into the owning scope rather than always
// the innermost one.
func (e *Environment) ResolveOwner(name string) (*Environment, *Slot, bool) {
	for env := e; env != nil; env = env.Parent {
		if slot, ok := env.vars[name]; ok {
			return env, slot, true
		}
	}
	return nil, nil, false
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
				slot := env.vars[n]
				loc := "(stack)"
				valStr := "<invalid>"
				if slot.Heap {
					loc = fmt.Sprintf("@0x%012x", h.DisplayAddr(slot.Addr))
					if b, ok := h.Get(slot.Addr); ok {
						valStr = b.Value.String()
					}
				} else {
					valStr = slot.Value.String()
				}
				sb.WriteString(fmt.Sprintf("    %-12s %-20s = %s\n", n, loc, valStr))

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
// easy to eyeball during line-by-line execution. Each frame shows its
// depth and function name (both always known -- FnName is set from the
// call-site label at every invoke(), so there's no case where a frame's
// identity is actually unknown) plus that frame's own locals (params and
// top-of-function let/const bindings; nested block scopes inside the
// function body aren't walked here, same way the frame itself only ever
// pointed at the function's own environment).
func (cs *CallStack) String(h *Heap) string {
	if len(cs.frames) == 0 {
		return "  (empty — top level)\n"
	}
	var sb strings.Builder
	for i := len(cs.frames) - 1; i >= 0; i-- {
		f := cs.frames[i]
		sb.WriteString(fmt.Sprintf("  #%d  %s()  called at %d:%d\n", i, f.FnName, f.CallLine, f.CallCol))

		names := make([]string, 0, len(f.Env.vars))
		for n := range f.Env.vars {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) == 0 {
			sb.WriteString("        (no locals)\n")
			continue
		}
		for _, n := range names {
			slot := f.Env.vars[n]
			loc := "(stack)"
			valStr := "<invalid>"
			if slot.Heap {
				loc = fmt.Sprintf("@0x%012x", h.DisplayAddr(slot.Addr))
				if b, ok := h.Get(slot.Addr); ok {
					valStr = b.Value.String()
				}
			} else {
				valStr = slot.Value.String()
			}
			sb.WriteString(fmt.Sprintf("        %-12s %-20s = %s\n", n, loc, valStr))
		}
	}
	return sb.String()
}
