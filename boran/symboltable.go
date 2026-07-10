package main

import "fmt"

// SymbolKind classifies what a symbol table entry represents.
type SymbolKind int

const (
	SymConst SymbolKind = iota
	SymLet
	SymParam
	SymStructType
	SymEnumType
	SymFunction
)

func (k SymbolKind) String() string {
	switch k {
	case SymConst:
		return "const"
	case SymLet:
		return "let"
	case SymParam:
		return "param"
	case SymStructType:
		return "struct"
	case SymEnumType:
		return "enum"
	case SymFunction:
		return "fn"
	default:
		return "unknown"
	}
}

// Symbol is a single entry registered in a scope.
type Symbol struct {
	Name     string
	Kind     SymbolKind
	TypeName string        // tag form, e.g. "array", "ptr", "int", or a named type
	Type     *DatatypeNode // full structured type — used to recover array length / pointer element type
	Line     int
	Col      int
}

// Scope is one lexical block. Scopes are linked to their parent, forming a
// tree that mirrors block nesting ({ ... } inside if/for/fn bodies).
type Scope struct {
	Parent   *Scope
	Children []*Scope
	Symbols  map[string]*Symbol
}

func NewScope(parent *Scope) *Scope {
	s := &Scope{Parent: parent, Symbols: make(map[string]*Symbol)}
	if parent != nil {
		parent.Children = append(parent.Children, s)
	}
	return s
}

// Declare adds a new symbol to this scope. It returns an error if the name
// is already declared *in this same scope* (shadowing an outer scope is
// allowed, matching typical block-scoped language semantics).
func (s *Scope) Declare(sym *Symbol) error {
	if existing, ok := s.Symbols[sym.Name]; ok {
		return fmt.Errorf("redeclaration of %q (%s) at line %d:%d; previously declared as %s at line %d:%d",
			sym.Name, sym.Kind, sym.Line, sym.Col, existing.Kind, existing.Line, existing.Col)
	}
	s.Symbols[sym.Name] = sym
	return nil
}

// Resolve looks up a name in this scope, then walks up through parent
// scopes until it is found or the chain is exhausted.
func (s *Scope) Resolve(name string) (*Symbol, bool) {
	for scope := s; scope != nil; scope = scope.Parent {
		if sym, ok := scope.Symbols[name]; ok {
			return sym, true
		}
	}
	return nil, false
}

// SymbolTable owns the root (global) scope and tracks the parser's current
// scope as it descends into and returns from nested blocks.
type SymbolTable struct {
	Global  *Scope
	Current *Scope
	Errors  []string
}

func NewSymbolTable() *SymbolTable {
	global := NewScope(nil)
	return &SymbolTable{Global: global, Current: global}
}

// EnterScope pushes a new child scope and makes it current — call this
// whenever the parser opens a `{`.
func (t *SymbolTable) EnterScope() {
	t.Current = NewScope(t.Current)
}

// ExitScope pops back to the parent scope — call this on a matching `}`.
func (t *SymbolTable) ExitScope() {
	if t.Current.Parent != nil {
		t.Current = t.Current.Parent
	}
}

// Declare registers a symbol in the current scope, recording (rather than
// panicking on) any redeclaration error so parsing can continue.
func (t *SymbolTable) Declare(name string, kind SymbolKind, typeName string, dtype *DatatypeNode, line, col int) {
	err := t.Current.Declare(&Symbol{Name: name, Kind: kind, TypeName: typeName, Type: dtype, Line: line, Col: col})
	if err != nil {
		t.Errors = append(t.Errors, err.Error())
	}
}

// Resolve looks up a name starting at the current scope.
func (t *SymbolTable) Resolve(name string) (*Symbol, bool) {
	return t.Current.Resolve(name)
}
