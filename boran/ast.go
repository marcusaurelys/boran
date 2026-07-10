package main

import "strings"

type Node interface {
	Pos() (line, col int)
}

type pos struct {
	Line, Col int
}

func (p pos) Pos() (int, int) { return p.Line, p.Col }

// ---- Program / statements -------------------------------------------------

type Program struct {
	pos
	Statements []Stmt
}

// Stmt is the interface implemented by every statement node.
type Stmt interface {
	Node
	stmtNode()
}

type Block struct {
	pos
	Statements []Stmt
}

func (b *Block) stmtNode() {}

type ConstDecl struct {
	pos
	Name     string
	TypeName string // either a builtin datatype keyword or a user-defined type identifier
	Value    Value
}

func (d *ConstDecl) stmtNode() {}

type LetDecl struct {
	pos
	Name     string
	TypeName string
	Value    Value
}

func (d *LetDecl) stmtNode() {}

// AssignStmt covers all four assignment forms:
//
//	ident = expr;
//	ident[expr] = expr;
//	ident.ident = expr;
//	this.ident = expr;
//
// AssignStmt covers assignment to any lvalue reachable via <lvalue_tail>:
//
//	ident = value;
//	this = value;
//	ident.field1.field2 = value;
//	ident[expr].field = value;
//	this.field[expr] = value;
//	... any chain of '.' ident and '[' expr ']' suffixes, in any order.
type AssignStmt struct {
	pos
	Target AssignTarget
	Value  Value
}

func (s *AssignStmt) stmtNode() {}

type AssignTargetKind int

const (
	TargetIdent AssignTargetKind = iota // ident <suffixes>
	TargetThis                          // this <suffixes>
)

type LvalueSuffixKind int

const (
	SuffixField LvalueSuffixKind = iota // '.' identifier
	SuffixIndex                         // '[' expr ']'
)

// LvalueSuffix is one segment of a chained assignment target, applied in
// source order (leftmost suffix first).
type LvalueSuffix struct {
	pos
	Kind  LvalueSuffixKind
	Field string // populated when Kind == SuffixField
	Index Expr   // populated when Kind == SuffixIndex
}

type AssignTarget struct {
	pos
	Kind     AssignTargetKind
	Name     string // identifier name, populated when Kind == TargetIdent
	Suffixes []LvalueSuffix
	Deref    int
}

// String renders a target like "a[i].b.c" for diagnostics and printing.
func (t AssignTarget) String() string {
	var sb strings.Builder
	for i := 0; i < t.Deref; i++ {
		sb.WriteString("*")
	}
	switch t.Kind {
	case TargetIdent:
		sb.WriteString(t.Name)
	case TargetThis:
		sb.WriteString("this")
	}
	for _, suf := range t.Suffixes {
		switch suf.Kind {
		case SuffixField:
			sb.WriteString(".")
			sb.WriteString(suf.Field)
		case SuffixIndex:
			sb.WriteString("[")
			sb.WriteString(strings.TrimSpace(printNode(suf.Index, 0)))
			sb.WriteString("]")
		}
	}
	return sb.String()
}

type IfStmt struct {
	pos
	Cond Expr
	Then *Block
	// Exactly one of Else / ElseIf is populated, or neither.
	Else   *Block
	ElseIf *IfStmt
}

func (s *IfStmt) stmtNode() {}

type ForIterStmt struct {
	pos
	VarName string
	Iter    Expr
	Body    *Block
}

func (s *ForIterStmt) stmtNode() {}

type ForWhileStmt struct {
	pos
	Cond Expr
	Body *Block
}

func (s *ForWhileStmt) stmtNode() {}

type ForRepeatStmt struct {
	pos
	Body *Block
	Cond Expr
}

func (s *ForRepeatStmt) stmtNode() {}

type PrintStmt struct {
	pos
	Args []Expr
}

func (s *PrintStmt) stmtNode() {}

type ReturnStmt struct {
	pos
	Value Expr // nil for bare `return;`
}

func (s *ReturnStmt) stmtNode() {}

type BreakStmt struct {
	pos
}

func (s *BreakStmt) stmtNode() {}

type ContinueStmt struct {
	pos
}

func (s *ContinueStmt) stmtNode() {}

// ExprStmt wraps a bare function/method call used as a statement.
type ExprStmt struct {
	pos
	Call Expr
}

func (s *ExprStmt) stmtNode() {}

// ---- Types ------------------------------------------------------------

// DatatypeNode represents <datatype>. For array types, Elem+ArrLen are set;
// for function types Kind=="fn"; for pointer types Elem is the pointee type.
type DatatypeNode struct {
	pos
	Kind   string        // "int","float","char","string","bool","fn","struct","enum","ptr","array","named"
	Name   string        // used when Kind == "named" (user-defined struct/enum type)
	Elem   *DatatypeNode // element type for arrays / pointee type for ptr
	ArrLen int           // array length, for Kind == "array"
}

// ---- Values (<value>) --------------------------------------------------

// Value is anything that can appear on the RHS of a const/let declaration
// or as an instance field initializer: a literal, a full expression, an
// array literal, a struct type-literal, a struct instance, an enum body,
// or a function literal.
type Value interface {
	Node
	valueNode()
}

// ExprValue wraps a general <expr> used as a <value>.
type ExprValue struct {
	pos
	Expr Expr
}

func (v *ExprValue) valueNode() {}

type ArrLiteral struct {
	pos
	Elements []Value
}

func (v *ArrLiteral) valueNode() {}

// StructLiteral is a *type definition*: { field: type, ... }
// e.g. const Point : struct = { x: int, y: int }
type StructLiteral struct {
	pos
	Fields []StructFieldInit
}

func (v *StructLiteral) valueNode() {}

type StructFieldInit struct {
	pos
	Name     string
	Mutable  bool   // true for 'let' fields, false for 'const' fields
	TypeName string // datatype keyword ("int", "fn", ...) or user-defined type name
	Default  Value  // nil only for a 'let' field with no '=' initializer
}

// StructInstance is a value of a previously declared struct type:
// { x: 1, y: 2 }
type StructInstance struct {
	pos
	Fields []InstanceFieldInit
}

func (v *StructInstance) valueNode() {}

type InstanceFieldInit struct {
	pos
	Name  string
	Value Value
}

// EnumBody: { north, east, west, south }
type EnumBody struct {
	pos
	Variants []string
}

func (v *EnumBody) valueNode() {}

type FnLiteral struct {
	pos
	Params     []Param
	ReturnType *DatatypeNode // nil for void functions
	Body       *Block
}

func (v *FnLiteral) valueNode() {}

type Param struct {
	pos
	Name string
	Type DatatypeNode
}

// ---- Expressions (<expr>) ----------------------------------------------

type Expr interface {
	Node
	exprNode()
}

type BinaryExpr struct {
	pos
	Op    string
	Left  Expr
	Right Expr
}

func (e *BinaryExpr) exprNode() {}

type UnaryExpr struct {
	pos
	Op      string // "-", "++", "--", "!", "*", "&"
	Operand Expr
	Postfix bool // true for `x++` / `x--`
}

type InputExpr struct {
	pos
	Prompt Expr
}

func (e *InputExpr) exprNode() {}

func (e *UnaryExpr) exprNode() {}

type Literal struct {
	pos
	Kind  TokenType // TOKEN_INT_LIT, TOKEN_FLOAT_LIT, TOKEN_CHAR_LIT, TOKEN_STRING_LIT, TOKEN_BOOL_LIT, or TOKEN_KEYWORD for `null`
	Value string
}

func (e *Literal) exprNode() {}

type Identifier struct {
	pos
	Name string
}

func (e *Identifier) exprNode() {}

type ThisExpr struct {
	pos
}

func (e *ThisExpr) exprNode() {}

type FnCall struct {
	pos
	Callee string
	Args   []Expr
}

func (e *FnCall) exprNode() {}

type MethodCall struct {
	pos
	Base       Expr // the receiver expression, e.g. Identifier("a"), ThisExpr, or a nested chain
	MethodName string
	Args       []Expr
}

func (e *MethodCall) exprNode() {}

type IndexExpr struct {
	pos
	Base  Expr // the expression being indexed (may itself be an IndexExpr/MemberAccess/FnCall for chaining)
	Index Expr
}

func (e *IndexExpr) exprNode() {}

type MemberAccess struct {
	pos
	Base  Expr // the expression the field is accessed on (Identifier, ThisExpr, or a nested chain)
	Field string
}

func (e *MemberAccess) exprNode() {}

// GroupExpr preserves an explicitly parenthesized expression, e.g. (a + b).
type GroupExpr struct {
	pos
	Inner Expr
}

func (e *GroupExpr) exprNode() {}
