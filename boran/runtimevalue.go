package main

import (
	"fmt"
	"strconv"
	"strings"
)

// RTValue is anything the interpreter can produce or store. Note the name:
// this is deliberately distinct from the AST's own `Value` interface
// (ast.go) to avoid confusing "a parsed <value> production" with "a runtime
// value the evaluator produced."
type RTValue interface {
	rtValueNode()
	TypeTag() string
	String() string
}

type IntVal struct{ Val int64 }

func (v *IntVal) rtValueNode()     {}
func (v *IntVal) TypeTag() string  { return "int" }
func (v *IntVal) String() string   { return strconv.FormatInt(v.Val, 10) }

type FloatVal struct{ Val float64 }

func (v *FloatVal) rtValueNode()    {}
func (v *FloatVal) TypeTag() string { return "float" }
func (v *FloatVal) String() string  { return strconv.FormatFloat(v.Val, 'g', -1, 64) }

type BoolVal struct{ Val bool }

func (v *BoolVal) rtValueNode()    {}
func (v *BoolVal) TypeTag() string { return "bool" }
func (v *BoolVal) String() string  { return strconv.FormatBool(v.Val) }

type CharVal struct{ Val rune }

func (v *CharVal) rtValueNode()    {}
func (v *CharVal) TypeTag() string { return "char" }
func (v *CharVal) String() string  { return string(v.Val) }

type StringVal struct{ Val string }

func (v *StringVal) rtValueNode()    {}
func (v *StringVal) TypeTag() string { return "string" }
func (v *StringVal) String() string  { return v.Val }

// NullVal is the runtime value of the 'null' literal, and the zero value
// for an uninitialized pointer.
type NullVal struct{}

func (v *NullVal) rtValueNode()    {}
func (v *NullVal) TypeTag() string { return "null" }
func (v *NullVal) String() string  { return "null" }

// ArrayVal holds heap addresses rather than values directly, so that
// `arr[i] = x` mutates the same cell any alias/pointer would see, and so
// elements are visible as their own rows in the heap dump.
type ArrayVal struct {
	Elems    []int // heap addresses, one per element
	ElemType string

	// HeapAddr is this array header's own permanent identity: the address
	// of the box that holds this exact *ArrayVal, set once at
	// construction (see evalValue). Every Environment slot that binds a
	// name to this array points at HeapAddr directly (never a separate
	// wrapper), so aliasing ('let b = a;') shares identity rather than
	// copying -- and refcounting (Interpreter.incref/decref) tracks
	// exactly this address.
	HeapAddr int
}

func (v *ArrayVal) rtValueNode()    {}
func (v *ArrayVal) TypeTag() string { return "array" }
func (v *ArrayVal) String() string {
	return fmt.Sprintf("array[%d]<%s> @%v", len(v.Elems), v.ElemType, v.Elems)
}

// StructVal likewise holds addresses per field, for the same aliasing
// reason -- and so that `&instance.field` is a meaningful pointer.
type StructVal struct {
	TypeName string
	Fields   map[string]int // field name -> heap address
	order    []string       // preserves declaration order for display

	// HeapAddr is this struct instance's own permanent identity -- see
	// ArrayVal.HeapAddr for the full rationale; same mechanism here.
	HeapAddr int
}

func NewStructVal(typeName string) *StructVal {
	return &StructVal{TypeName: typeName, Fields: make(map[string]int)}
}

func (v *StructVal) SetField(name string, addr int) {
	if _, exists := v.Fields[name]; !exists {
		v.order = append(v.order, name)
	}
	v.Fields[name] = addr
}

func (v *StructVal) rtValueNode()    {}
func (v *StructVal) TypeTag() string { return "struct:" + v.TypeName }
func (v *StructVal) String() string {
	parts := make([]string, 0, len(v.order))
	for _, name := range v.order {
		parts = append(parts, fmt.Sprintf("%s=@%d", name, v.Fields[name]))
	}
	return fmt.Sprintf("%s{%s}", v.TypeName, strings.Join(parts, ", "))
}

// FnVal is a closure: the literal plus the environment it was defined in.
type FnVal struct {
	Lit     *FnLiteral
	Closure *Environment
	Name    string // for display / call-stack labeling; "" for anonymous
}

func (v *FnVal) rtValueNode()    {}
func (v *FnVal) TypeTag() string { return "fn" }
func (v *FnVal) String() string {
	if v.Name != "" {
		return "fn " + v.Name
	}
	return "fn <anonymous>"
}

// PtrVal wraps a heap address. Dereferencing/assigning through a PtrVal
// goes through Heap.Get/Set on Addr, which is what gives '&'/'*' real
// aliasing rather than copy semantics.
type PtrVal struct {
	Addr int
	// Owned marks a pointer created by 'new(...)': this pointer's target
	// is a heap cell nobody else already owns, so it participates in the
	// interpreter's refcounting (incref on every additional binding,
	// decref on every unbinding) the same way arrays/structs do -- see
	// ownedPtrAddr, retain/release, and teardown in interpreter.go.
	// '&x' pointers leave this false: they merely reference memory some
	// other binding already owns, so they never free their target.
	Owned bool
}

func (v *PtrVal) rtValueNode()    {}
func (v *PtrVal) TypeTag() string { return "ptr" }
func (v *PtrVal) String() string  { return fmt.Sprintf("*0x%04x", v.Addr) }

// EnumVal is one variant of a declared enum type.
type EnumVal struct {
	TypeName string
	Variant  string
}

func (v *EnumVal) rtValueNode()    {}
func (v *EnumVal) TypeTag() string { return "enum:" + v.TypeName }
func (v *EnumVal) String() string  { return v.TypeName + "." + v.Variant }

// TypeDefVal is the runtime placeholder bound to a struct/enum *type
// definition* itself (e.g. the name "Point" in `const Point : struct =
// {...}`) -- it's not a usable value, just a marker so the name still
// resolves to something displayable in the symbol table.
type TypeDefVal struct {
	Kind string // "struct" or "enum"
	Name string
}

func (v *TypeDefVal) rtValueNode()    {}
func (v *TypeDefVal) TypeTag() string { return "typedef:" + v.Kind }
func (v *TypeDefVal) String() string  { return fmt.Sprintf("<%s %s>", v.Kind, v.Name) }
