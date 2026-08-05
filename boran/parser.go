package main

import "fmt"

// ParseError records a single recoverable syntax error.
type ParseError struct {
	Line, Col int
	Message   string
}

func (e ParseError) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Line, e.Col, e.Message)
}

// parsePanic is the internal signal used for panic/recover-based error
// propagation inside a single statement; it is always caught before it
// escapes parseStmt, so callers of the exported API never see it.
type parsePanic struct{ err ParseError }

type Parser struct {
	tokens  []Token
	pos     int
	Symbols *SymbolTable
	Errors  []ParseError
}

func NewParser(tokens []Token) *Parser {
	return &Parser{tokens: tokens, pos: 0, Symbols: NewSymbolTable()}
}

// ---- token stream helpers --------------------------------------------

func (p *Parser) current() Token {
	if p.pos >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1] // EOF
	}
	return p.tokens[p.pos]
}

func (p *Parser) peek() Token {
	if p.pos+1 >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[p.pos+1]
}

func (p *Parser) advance() Token {
	tok := p.current()
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
	return tok
}

func (p *Parser) atEOF() bool {
	return p.pos >= len(p.tokens) || p.current().Type == TOKEN_EOF
}

// consume verifies the current token has the expected type, advances past
// it, and returns it. On mismatch it raises a parsePanic.
func (p *Parser) consume(t TokenType) Token {
	tok := p.current()
	if tok.Type == TOKEN_ERROR {
		p.fail(tok, "lexical error: %s", tok.Literal)
	}
	if tok.Type != t {
		p.fail(tok, "expected %s but found %s (%q)", t, tok.Type, tok.Literal)
	}
	return p.advance()
}

// consumeKeyword verifies the current token is TOKEN_KEYWORD with the given
// literal spelling (e.g. "const", "struct", "fn") and advances past it.
func (p *Parser) consumeKeyword(lit string) Token {
	tok := p.current()
	if tok.Type != TOKEN_KEYWORD || tok.Literal != lit {
		p.fail(tok, "expected keyword %q but found %s (%q)", lit, tok.Type, tok.Literal)
	}
	return p.advance()
}

func (p *Parser) isKeyword(lit string) bool {
	tok := p.current()
	return tok.Type == TOKEN_KEYWORD && tok.Literal == lit
}

func (p *Parser) peekIsKeyword(lit string) bool {
	tok := p.peek()
	return tok.Type == TOKEN_KEYWORD && tok.Literal == lit
}

func (p *Parser) fail(tok Token, format string, args ...interface{}) {
	panic(parsePanic{ParseError{Line: tok.Line, Col: tok.Col, Message: fmt.Sprintf(format, args...)}})
}

// synchronize implements simple panic-mode error recovery: it skips tokens
// until it finds a statement boundary (';' or '}') or EOF, so parsing of
// subsequent top-level statements can continue after an error.
func (p *Parser) synchronize() {
	for !p.atEOF() {
		tok := p.advance()
		if tok.Type == TOKEN_SEMICOLON || tok.Type == TOKEN_RBRACE {
			return
		}
	}
}

// builtinTypeNames are the reserved words that name primitive / structural
// datatypes, as opposed to user-defined struct/enum type identifiers.
var builtinTypeNames = map[string]bool{
	"int": true, "float": true, "char": true, "string": true, "bool": true,
	"fn": true, "struct": true, "enum": true,
}

// ============================================================================
// Entry point
// ============================================================================

// ParseProgram parses the entire token stream into a *Program, collecting
// (rather than stopping at) syntax errors so the caller gets as complete an
// AST and error list as possible.
func (p *Parser) ParseProgram() *Program {
	prog := &Program{pos: pos{1, 1}}
	for !p.atEOF() {
		stmt := p.parseStmtRecover()
		if stmt != nil {
			prog.Statements = append(prog.Statements, stmt)
		}
	}
	return prog
}

func (p *Parser) parseStmtRecover() (stmt Stmt) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parsePanic); ok {
				p.Errors = append(p.Errors, pe.err)
				p.synchronize()
				stmt = nil
				return
			}
			panic(r)
		}
	}()
	return p.parseStmt()
}

// ============================================================================
// III.1 / III.5  Statements
// ============================================================================

func (p *Parser) parseStmt() Stmt {
	tok := p.current()

	// 1. Handle Keywords that start specific statements
	if tok.Type == TOKEN_KEYWORD {
		switch tok.Literal {
		case "const":
			return p.parseConstDecl()
		case "let":
			return p.parseLetDecl()
		case "if":
			return p.parseIfStmt()
		case "for":
			return p.parseForStmt()
		case "print":
			return p.parsePrintStmt()
		case "return":
			return p.parseReturnStmt()
		case "break":
			return p.parseBreakStmt()
		case "continue":
			return p.parseContinueStmt()
		}
	}

	// 2. Blocks
	if tok.Type == TOKEN_LBRACE {
		return p.parseBlock()
	}

	// 3. Identifier / 'this' led statements: could be an assignment
	//    (possibly through a chain: a.start.x = 1;) or an expression
	//    statement (possibly through a chain: a.start.normalize();).
	//    A single token of lookahead can't tell these apart once chaining
	//    is involved, so parse the full postfix expression first and then
	//    decide based on what follows it.
	if tok.Type == TOKEN_IDENTIFIER || (tok.Type == TOKEN_KEYWORD && tok.Literal == "this") || tok.Type == TOKEN_OP_MUL {
		return p.parseIdentOrThisStmt()
	}

	// 4. Expression Statements (e.g. ++i;, myFunc();)
	// This covers the BNF: <stmt> ::= <expr> ';'
	expr := p.parseExpr()
	p.consume(TOKEN_SEMICOLON)
	return &ExprStmt{pos: pos{tok.Line, tok.Col}, Call: expr}
}

func (p *Parser) parseBreakStmt() *BreakStmt {
	start := p.consumeKeyword("break")
	p.consume(TOKEN_SEMICOLON)
	return &BreakStmt{pos: pos{start.Line, start.Col}}
}

func (p *Parser) parseContinueStmt() *ContinueStmt {
	start := p.consumeKeyword("continue")
	p.consume(TOKEN_SEMICOLON)
	return &ContinueStmt{pos: pos{start.Line, start.Col}}
}

// parseIdentOrThisStmt parses a statement that begins with an identifier or
// 'this'. It first parses the full chained expression (a, a.start,
// a.start.x, matrix[0][1], a.start.normalize(), this.x, ...); if that is
// immediately followed by '=' it's an assignment (and the parsed expression
// must reduce to a valid lvalue per the grammar: ident, ident[expr],
// ident.field, or this.field), otherwise it's a bare expression statement.
func (p *Parser) parseIdentOrThisStmt() Stmt {
	tok := p.current()
	startPos := pos{tok.Line, tok.Col}
	expr := p.parseExpr()

	if p.current().Type == TOKEN_OP_ASSIGN {
		target := p.exprToAssignTarget(expr, startPos)
		p.advance() // consume '='
		val := p.parseValue("", nil)
		p.consume(TOKEN_SEMICOLON)
		return &AssignStmt{pos: startPos, Target: target, Value: val}
	}

	p.consume(TOKEN_SEMICOLON)
	return &ExprStmt{pos: startPos, Call: expr}
}

func (p *Parser) exprToAssignTarget(e Expr, at pos) AssignTarget {
	cur := e
	derefCount := 0

	for {
		u, ok := cur.(*UnaryExpr)
		if !ok || u.Op != "*" || u.Postfix {
			break
		}
		derefCount++
		cur = u.Operand
	}

	var suffixes []LvalueSuffix
loop:
	for {
		switch v := cur.(type) {
		case *MemberAccess:
			l, c := v.Pos()
			suffixes = append(suffixes, LvalueSuffix{pos: pos{l, c}, Kind: SuffixField, Field: v.Field})
			cur = v.Base
		case *IndexExpr:
			l, c := v.Pos()
			suffixes = append(suffixes, LvalueSuffix{pos: pos{l, c}, Kind: SuffixIndex, Index: v.Index})
			cur = v.Base
		default:
			break loop
		}
	}

	for i, j := 0, len(suffixes)-1; i < j; i, j = i+1, j-1 {
		suffixes[i], suffixes[j] = suffixes[j], suffixes[i]
	}

	switch base := cur.(type) {
	case *Identifier:
		return AssignTarget{pos: at, Kind: TargetIdent, Name: base.Name, Suffixes: suffixes, Deref: derefCount}
	case *ThisExpr:
		return AssignTarget{pos: at, Kind: TargetThis, Suffixes: suffixes, Deref: derefCount}
	}

	p.fail(p.current(), "invalid assignment target")
	return AssignTarget{pos: at}
}

func (p *Parser) parseForStmt() Stmt {
	start := p.consumeKeyword("for")

	// 1. for <ident> in <expr> <block>
	if p.current().Type == TOKEN_IDENTIFIER && p.peekIsKeyword("in") {
		name := p.advance()
		p.consumeKeyword("in")
		iter := p.parseExpr()
		body := p.parseBlock()
		return &ForIterStmt{pos: pos{start.Line, start.Col}, VarName: name.Literal, Iter: iter, Body: body}
	}

	// 2. for { <block> } <bool_expr> [';']
	//
	// The trailing ';' is accepted but no longer required. Every other
	// block-terminated statement form (if, for-iter, for-while) needs no
	// semicolon, since the closing '}' (or, here, the condition that
	// follows it) already marks the statement's end unambiguously; making
	// this one form demand a ';' while the rest don't was an inconsistency
	// in the surface syntax, not a parsing necessity. Consuming it when
	// present (rather than removing support outright) keeps existing
	// source files with a trailing ';' here still parsing correctly.
	if p.current().Type == TOKEN_LBRACE {
		body := p.parseBlock()
		cond := p.parseExpr()
		if p.current().Type == TOKEN_SEMICOLON {
			p.advance()
		}
		return &ForRepeatStmt{pos: pos{start.Line, start.Col}, Body: body, Cond: cond}
	}

	// 3. for <bool_expr> <block>
	cond := p.parseExpr()
	body := p.parseBlock()
	return &ForWhileStmt{pos: pos{start.Line, start.Col}, Cond: cond, Body: body}
}

func (p *Parser) parseBlock() *Block {
	start := p.consume(TOKEN_LBRACE)
	b := &Block{pos: pos{start.Line, start.Col}}
	p.Symbols.EnterScope()
	for !p.atEOF() && p.current().Type != TOKEN_RBRACE {
		stmt := p.parseStmtRecover()
		if stmt != nil {
			b.Statements = append(b.Statements, stmt)
		}
	}
	p.Symbols.ExitScope()
	p.consume(TOKEN_RBRACE)
	return b
}

// ---- Declarations -------------------------------------------------------

func (p *Parser) parseConstDecl() *ConstDecl {
	start := p.consumeKeyword("const")
	name := p.consume(TOKEN_IDENTIFIER)
	p.consume(TOKEN_COLON)
	dtype, typeName := p.parseTypeAnnotation()
	p.consume(TOKEN_OP_ASSIGN)
	val := p.parseValue(typeName, dtype)
	p.consume(TOKEN_SEMICOLON)

	kind := symbolKindForType(typeName)
	p.Symbols.Declare(name.Literal, kind, typeName, dtype, name.Line, name.Col)

	return &ConstDecl{pos: pos{start.Line, start.Col}, Name: name.Literal, TypeName: typeName, DeclaredType: dtype, Value: val}
}

func (p *Parser) parseLetDecl() *LetDecl {
	start := p.consumeKeyword("let")
	name := p.consume(TOKEN_IDENTIFIER)
	p.consume(TOKEN_COLON)
	dtype, typeName := p.parseTypeAnnotation()
	p.consume(TOKEN_OP_ASSIGN)
	val := p.parseValue(typeName, dtype)
	p.consume(TOKEN_SEMICOLON)

	p.Symbols.Declare(name.Literal, SymLet, typeName, dtype, name.Line, name.Col)

	return &LetDecl{pos: pos{start.Line, start.Col}, Name: name.Literal, TypeName: typeName, DeclaredType: dtype, Value: val}
}

func symbolKindForType(typeName string) SymbolKind {
	switch typeName {
	case "fn":
		return SymFunction
	case "struct":
		return SymStructType
	case "enum":
		return SymEnumType
	default:
		return SymConst
	}
}

// ---- Types ----------------------------------------------------------------

// parseTypeAnnotation parses <datatype> (or a user-defined type identifier,
// per the const/let second form) and returns both a structured DatatypeNode
// and a short textual tag used to disambiguate <value> parsing:
// one of "int","float","char","string","bool","fn","struct","enum","ptr",
// or the literal identifier of a user-defined type.
func (p *Parser) parseTypeAnnotation() (*DatatypeNode, string) {
	tok := p.current()
	var base *DatatypeNode
	var tag string

	switch {
	case tok.Type == TOKEN_KEYWORD && builtinTypeNames[tok.Literal]:
		p.advance()
		switch tok.Literal {
		case "fn":
			base = &DatatypeNode{pos: pos{tok.Line, tok.Col}, Kind: "fn"}
			tag = "fn"
		default:
			base = &DatatypeNode{pos: pos{tok.Line, tok.Col}, Kind: tok.Literal}
			tag = tok.Literal
		}
	case tok.Type == TOKEN_IDENTIFIER:
		p.advance()
		base = &DatatypeNode{pos: pos{tok.Line, tok.Col}, Kind: "named", Name: tok.Literal}
		tag = tok.Literal
	default:
		p.fail(tok, "expected a type (builtin datatype or type name) but found %s (%q)", tok.Type, tok.Literal)
	}

	// Postfix modifiers, may combine in either order:
	//   <ptr_type> ::= <datatype> '*'
	//   <arr_type> ::= <datatype> '[' INT_LIT ']'
	for {
		switch p.current().Type {
		case TOKEN_OP_MUL:
			p.advance()
			base = &DatatypeNode{pos: base.pos, Kind: "ptr", Elem: base}
			tag = "ptr"
		case TOKEN_LBRACKET:
			p.advance()
			lenTok := p.consume(TOKEN_INT_LIT)
			p.consume(TOKEN_RBRACKET)
			n := 0
			fmt.Sscanf(lenTok.Literal, "%d", &n)
			base = &DatatypeNode{pos: base.pos, Kind: "array", Elem: base, ArrLen: n}
			tag = "array"
		default:
			return base, tag
		}
	}
}

// ---- Values (<value>) ------------------------------------------------------

// parseValue implements <value>, disambiguated using the declared-type
// context (typeTag/dtype) established by the enclosing const/let decl or
// struct instance field, since '{' and '(' are each shared by two
// grammar alternatives that are not distinguishable by a single token of
// lookahead alone.

// looksLikeFnLiteral performs bounded lookahead from the current '(' to
// decide whether this is a fn_literal signature — '(' <param_list> ')'
// (':' <datatype>)? '=>' — versus a plain parenthesized expression. It only
// reads tokens (via saved/restored p.pos); it never builds AST nodes or
// reports errors, so it's safe to call speculatively.
func (p *Parser) looksLikeFnLiteral() bool {
	saved := p.pos

	if p.current().Type != TOKEN_LPAREN {
		p.pos = saved
		return false
	}
	p.advance() // consume '('

	depth := 1
	for depth > 0 {
		if p.atEOF() {
			p.pos = saved
			return false
		}
		switch p.current().Type {
		case TOKEN_LPAREN:
			depth++
		case TOKEN_RPAREN:
			depth--
		}
		p.advance()
	}
	// p is now positioned just past the matching ')'

	if p.current().Type == TOKEN_COLON {
		// skip an optional ': <datatype>' — datatype may itself contain
		// '[' INT_LIT ']' for array types, so skip balanced brackets too.
		p.advance()
		if p.current().Type != TOKEN_KEYWORD && p.current().Type != TOKEN_IDENTIFIER {
			p.pos = saved
			return false
		}
		p.advance()
		if p.current().Type == TOKEN_LBRACKET {
			bdepth := 1
			p.advance()
			for bdepth > 0 {
				if p.atEOF() {
					p.pos = saved
					return false
				}
				switch p.current().Type {
				case TOKEN_LBRACKET:
					bdepth++
				case TOKEN_RBRACKET:
					bdepth--
				}
				p.advance()
			}
		}
	}

	isFn := p.current().Type == TOKEN_OP_ARROW
	p.pos = saved
	return isFn
}

func (p *Parser) parseValue(typeTag string, dtype *DatatypeNode) Value {
	tok := p.current()

	switch tok.Type {
	case TOKEN_LBRACKET:
		return p.parseArrLiteral(dtype)

	case TOKEN_LBRACE:
		switch typeTag {
		case "struct":
			return p.parseStructLiteral()
		case "enum":
			return p.parseEnumBody()
		default:
			// Named user-defined type (or unknown context): treat as an
			// instance of a previously-declared struct type.
			return p.parseStructInstance()
		}

	case TOKEN_LPAREN:
		if typeTag == "fn" || p.looksLikeFnLiteral() {
			return p.parseFnLiteral()
		}
		// Parenthesized expression used as a value.
		e := p.parseExpr()
		return &ExprValue{pos: pos{tok.Line, tok.Col}, Expr: e}

	default:
		e := p.parseExpr()
		return &ExprValue{pos: pos{tok.Line, tok.Col}, Expr: e}
	}
}

func (p *Parser) parseArrLiteral(dtype *DatatypeNode) *ArrLiteral {
	start := p.consume(TOKEN_LBRACKET)
	lit := &ArrLiteral{pos: pos{start.Line, start.Col}}
	var elemTag string
	var elemType *DatatypeNode
	if dtype != nil && dtype.Kind == "array" {
		elemType = dtype.Elem
		if elemType != nil {
			elemTag = elemType.Kind
			if elemTag == "named" {
				elemTag = elemType.Name
			}
		}
	}
	if p.current().Type != TOKEN_RBRACKET {
		lit.Elements = append(lit.Elements, p.parseValue(elemTag, elemType))
		for p.current().Type == TOKEN_COMMA {
			p.advance()
			lit.Elements = append(lit.Elements, p.parseValue(elemTag, elemType))
		}
	}
	p.consume(TOKEN_RBRACKET)
	return lit
}

// parseStructLiteral parses a struct *type definition*:
//
//	{ field: datatype, field: fn_literal, ... }
func (p *Parser) parseStructLiteral() *StructLiteral {
	start := p.consume(TOKEN_LBRACE)
	lit := &StructLiteral{pos: pos{start.Line, start.Col}}
	lit.Fields = append(lit.Fields, p.parseStructFieldInit())
	for p.current().Type == TOKEN_COMMA {
		p.advance()
		lit.Fields = append(lit.Fields, p.parseStructFieldInit())
	}
	p.consume(TOKEN_RBRACE)
	return lit
}

func (p *Parser) parseStructFieldInit() StructFieldInit {
	var mutable bool
	switch {
	case p.isKeyword("const"):
		p.advance()
	case p.isKeyword("let"):
		p.advance()
		mutable = true
	default:
		p.fail(p.current(), "expected 'const' or 'let' to start a struct field, found %s (%q)", p.current().Type, p.current().Literal)
	}

	name := p.consume(TOKEN_IDENTIFIER)
	p.consume(TOKEN_COLON)
	dtype, tag := p.parseTypeAnnotation()

	f := StructFieldInit{pos: pos{name.Line, name.Col}, Name: name.Literal, Mutable: mutable, TypeName: tag, DeclaredType: dtype}

	if p.current().Type == TOKEN_OP_ASSIGN {
		p.advance()
		f.Default = p.parseValue(tag, dtype)
	} else if !mutable {
		p.fail(p.current(), "const struct field %q requires a value", name.Literal)
	}

	return f
}

// parseStructInstance parses a struct *value*: { field: value, ... }
func (p *Parser) parseStructInstance() *StructInstance {
	start := p.consume(TOKEN_LBRACE)
	inst := &StructInstance{pos: pos{start.Line, start.Col}}
	if p.current().Type != TOKEN_RBRACE {
		inst.Fields = append(inst.Fields, p.parseInstanceFieldInit())
		for p.current().Type == TOKEN_COMMA {
			p.advance()
			inst.Fields = append(inst.Fields, p.parseInstanceFieldInit())
		}
	}
	p.consume(TOKEN_RBRACE)
	return inst
}

func (p *Parser) parseInstanceFieldInit() InstanceFieldInit {
	name := p.consume(TOKEN_IDENTIFIER)
	p.consume(TOKEN_COLON)
	val := p.parseValue("", nil)
	return InstanceFieldInit{pos: pos{name.Line, name.Col}, Name: name.Literal, Value: val}
}

func (p *Parser) parseEnumBody() *EnumBody {
	start := p.consume(TOKEN_LBRACE)
	body := &EnumBody{pos: pos{start.Line, start.Col}}
	first := p.consume(TOKEN_IDENTIFIER)
	body.Variants = append(body.Variants, first.Literal)
	for p.current().Type == TOKEN_COMMA {
		p.advance()
		v := p.consume(TOKEN_IDENTIFIER)
		body.Variants = append(body.Variants, v.Literal)
	}
	p.consume(TOKEN_RBRACE)
	return body
}

func (p *Parser) parseFnLiteral() *FnLiteral {
	start := p.consume(TOKEN_LPAREN)
	fn := &FnLiteral{pos: pos{start.Line, start.Col}}

	p.Symbols.EnterScope() // params + body share one function scope
	if p.current().Type != TOKEN_RPAREN {
		fn.Params = append(fn.Params, p.parseParam())
		for p.current().Type == TOKEN_COMMA {
			p.advance()
			fn.Params = append(fn.Params, p.parseParam())
		}
	}
	p.consume(TOKEN_RPAREN)

	if p.current().Type == TOKEN_COLON {
		p.advance()
		rt, _ := p.parseTypeAnnotation()
		fn.ReturnType = rt
	}

	p.consume(TOKEN_OP_ARROW)

	braceTok := p.consume(TOKEN_LBRACE)
	body := &Block{pos: pos{braceTok.Line, braceTok.Col}}
	for !p.atEOF() && p.current().Type != TOKEN_RBRACE {
		stmt := p.parseStmtRecover()
		if stmt != nil {
			body.Statements = append(body.Statements, stmt)
		}
	}
	p.consume(TOKEN_RBRACE)
	fn.Body = body
	p.Symbols.ExitScope()

	return fn
}

func (p *Parser) parseParam() Param {
	name := p.consume(TOKEN_IDENTIFIER)
	p.consume(TOKEN_COLON)
	dtype, tag := p.parseTypeAnnotation()
	p.Symbols.Declare(name.Literal, SymParam, tag, dtype, name.Line, name.Col)
	return Param{pos: pos{name.Line, name.Col}, Name: name.Literal, Type: *dtype}
}

// ============================================================================
// Identifier-led / this-led statements
// (assign_stmt, fn_call-as-stmt, method_call-as-stmt, io_stmt's input form)
// ============================================================================

// ============================================================================
// Control flow
// ============================================================================

func (p *Parser) parseIfStmt() *IfStmt {
	start := p.consumeKeyword("if")
	cond := p.parseExpr()
	then := p.parseBlock()
	stmt := &IfStmt{pos: pos{start.Line, start.Col}, Cond: cond, Then: then}

	if p.isKeyword("else") {
		p.advance()
		if p.isKeyword("if") {
			stmt.ElseIf = p.parseIfStmt()
		} else {
			stmt.Else = p.parseBlock()
		}
	}
	return stmt
}

// parseForStmt disambiguates Boran's three unified loop forms using
// bounded lookahead:
//
//	for <ident> in <expr> <block>   -- for_iter_stmt   (IDENT then "in")
//	for { ... } <bool_expr>         -- for_repeat_stmt (block comes first)
//	for <bool_expr> <block>         -- for_while_stmt  (default)

func (p *Parser) parsePrintStmt() *PrintStmt {
	start := p.consumeKeyword("print")
	p.consume(TOKEN_LPAREN)
	stmt := &PrintStmt{pos: pos{start.Line, start.Col}}
	stmt.Args = append(stmt.Args, p.parseExpr())
	for p.current().Type == TOKEN_COMMA {
		p.advance()
		stmt.Args = append(stmt.Args, p.parseExpr())
	}
	p.consume(TOKEN_RPAREN)
	p.consume(TOKEN_SEMICOLON)
	return stmt
}

func (p *Parser) parseReturnStmt() *ReturnStmt {
	start := p.consumeKeyword("return")
	if p.current().Type == TOKEN_SEMICOLON {
		p.advance()
		return &ReturnStmt{pos: pos{start.Line, start.Col}}
	}
	val := p.parseExpr()
	p.consume(TOKEN_SEMICOLON)
	return &ReturnStmt{pos: pos{start.Line, start.Col}, Value: val}
}

// ============================================================================
// III.4  Expressions
// ============================================================================

func (p *Parser) parseExpr() Expr {
	return p.parseBoolExpr()
}

// <bool_expr> → <bool_term> <bool_expr_tail>
// <bool_expr_tail> → '||' <bool_term> <bool_expr_tail> | ε
func (p *Parser) parseBoolExpr() Expr {
	left := p.parseBoolTerm()
	for p.current().Type == TOKEN_OP_OR {
		op := p.advance()
		right := p.parseBoolTerm()
		left = &BinaryExpr{pos: pos{op.Line, op.Col}, Op: "||", Left: left, Right: right}
	}
	return left
}

// <bool_term> → <bool_factor> <bool_term_tail>
// <bool_term_tail> → '&&' <bool_factor> <bool_term_tail> | ε
func (p *Parser) parseBoolTerm() Expr {
	left := p.parseBoolFactor()
	for p.current().Type == TOKEN_OP_AND && p.current().Literal == "&&" {
		op := p.advance()
		right := p.parseBoolFactor()
		left = &BinaryExpr{pos: pos{op.Line, op.Col}, Op: "&&", Left: left, Right: right}
	}
	return left
}

// <bool_factor> → '!' <bool_factor> | <arith_expr> <rel_op_tail>
// <rel_op_tail>  → <rel_op> <arith_expr> | ε
// (parenthesized boolean grouping is handled uniformly by <primary>'s
// '(' <expr> ')' alternative, so no separate alt is needed here.)
func (p *Parser) parseBoolFactor() Expr {
	if p.current().Type == TOKEN_OP_NOT {
		op := p.advance()
		operand := p.parseBoolFactor()
		return &UnaryExpr{pos: pos{op.Line, op.Col}, Op: "!", Operand: operand}
	}
	left := p.parseArithExpr()
	if isRelOp(p.current().Type) {
		op := p.advance()
		right := p.parseArithExpr()
		return &BinaryExpr{pos: pos{op.Line, op.Col}, Op: op.Literal, Left: left, Right: right}
	}
	return left
}

func isRelOp(t TokenType) bool {
	switch t {
	case TOKEN_OP_EQUAL, TOKEN_OP_NOT_EQ, TOKEN_OP_LT, TOKEN_OP_GT, TOKEN_OP_LE, TOKEN_OP_GE:
		return true
	}
	return false
}

// <arith_expr> → <arith_term> <arith_expr_tail>
// <arith_expr_tail> → ('+'|'-') <arith_term> <arith_expr_tail> | ε
func (p *Parser) parseArithExpr() Expr {
	left := p.parseArithTerm()
	for p.current().Type == TOKEN_OP_ADD || p.current().Type == TOKEN_OP_SUB {
		op := p.advance()
		right := p.parseArithTerm()
		left = &BinaryExpr{pos: pos{op.Line, op.Col}, Op: op.Literal, Left: left, Right: right}
	}
	return left
}

// <arith_term> → <unary_expr> <arith_term_tail>
// <arith_term_tail> → ('*'|'/'|'%') <unary_expr> <arith_term_tail> | ε
func (p *Parser) parseArithTerm() Expr {
	left := p.parseUnaryExpr()
	for p.current().Type == TOKEN_OP_MUL || p.current().Type == TOKEN_OP_DIV || p.current().Type == TOKEN_OP_MOD {
		op := p.advance()
		right := p.parseUnaryExpr()
		left = &BinaryExpr{pos: pos{op.Line, op.Col}, Op: op.Literal, Left: left, Right: right}
	}
	return left
}

// <unary_expr> → '-' <unary_expr> | '++' <unary_expr> | '--' <unary_expr>
//
//	| '*' <unary_expr>   (pointer dereference)
//	| '&' <unary_expr>   (address-of)
//	| <postfix_expr>
func (p *Parser) parseUnaryExpr() Expr {
	tok := p.current()
	switch {
	case tok.Type == TOKEN_OP_SUB:
		p.advance()
		return &UnaryExpr{pos: pos{tok.Line, tok.Col}, Op: "-", Operand: p.parseUnaryExpr()}
	case tok.Type == TOKEN_OP_INC:
		p.advance()
		return &UnaryExpr{pos: pos{tok.Line, tok.Col}, Op: "++", Operand: p.parseUnaryExpr()}
	case tok.Type == TOKEN_OP_DEC:
		p.advance()
		return &UnaryExpr{pos: pos{tok.Line, tok.Col}, Op: "--", Operand: p.parseUnaryExpr()}
	case tok.Type == TOKEN_OP_MUL:
		p.advance()
		return &UnaryExpr{pos: pos{tok.Line, tok.Col}, Op: "*", Operand: p.parseUnaryExpr()}
	case tok.Type == TOKEN_OP_ADDRESS:
		p.advance()
		return &UnaryExpr{pos: pos{tok.Line, tok.Col}, Op: "&", Operand: p.parseUnaryExpr()}
	default:
		return p.parseCastExpr()
	}
}

// <cast_expr> → <postfix_expr> <cast_tail>
// <cast_tail> → 'as' <datatype> <cast_tail> | ε
//
// Binds tighter than arithmetic/comparison but looser than the prefix unary
// forms above, so `-x as float` casts first then negates is NOT how this
// parses -- '-' recurses into parseUnaryExpr, which reaches parseCastExpr
// for its operand, so `-x as float` actually parses as `-(x as float)`.
// Chains left-associatively: `x as float as string` casts x to float, then
// that result to string.
func (p *Parser) parseCastExpr() Expr {
	expr := p.parsePostfixExpr()
	for p.isKeyword("as") {
		tok := p.advance()
		target, _ := p.parseTypeAnnotation()
		expr = &CastExpr{pos: pos{tok.Line, tok.Col}, Operand: expr, Target: target}
	}
	return expr
}

// <postfix_expr> → <primary> <postfix_tail>
// <postfix_tail> → '++' | '--' | ε
func (p *Parser) parsePostfixExpr() Expr {
	operand := p.parsePrimary()
	if p.current().Type == TOKEN_OP_INC || p.current().Type == TOKEN_OP_DEC {
		op := p.advance()
		return &UnaryExpr{pos: pos{op.Line, op.Col}, Op: op.Literal, Operand: operand, Postfix: true}
	}
	return operand
}

// <primary> → <literal> | this <this_tail> | '(' <expr> ')' | <identifier> <primary_tail>
func (p *Parser) parsePrimary() Expr {
	tok := p.current()

	switch tok.Type {
	case TOKEN_INT_LIT, TOKEN_FLOAT_LIT, TOKEN_CHAR_LIT, TOKEN_STRING_LIT, TOKEN_BOOL_LIT:
		p.advance()
		return &Literal{pos: pos{tok.Line, tok.Col}, Kind: tok.Type, Value: tok.Literal}

	case TOKEN_KEYWORD:
		switch tok.Literal {
		case "null":
			p.advance()
			return &Literal{pos: pos{tok.Line, tok.Col}, Kind: TOKEN_KEYWORD, Value: "null"}
		case "this":
			p.advance()
			return p.parseThisTail(tok)
		case "input": // NEW: handle input() as expression
			p.advance()
			p.consume(TOKEN_LPAREN)
			prompt := p.parseExpr()
			p.consume(TOKEN_RPAREN)
			return &InputExpr{pos: pos{tok.Line, tok.Col}, Prompt: prompt}
		case "range":
			return p.parseRangeExpr(tok)
		}
		p.fail(tok, "unexpected keyword %q in expression", tok.Literal)

	case TOKEN_IDENTIFIER:
		p.advance()
		return p.parsePrimaryTail(tok)

	case TOKEN_LPAREN:
		p.advance()
		inner := p.parseExpr()
		p.consume(TOKEN_RPAREN)
		return &GroupExpr{pos: pos{tok.Line, tok.Col}, Inner: inner}
	}

	p.fail(tok, "unexpected token %s (%q) in expression", tok.Type, tok.Literal)
	return nil
}

// <range_expr> → 'range' '(' <expr> <range_args_tail> ')'
// <range_args_tail> → ',' <expr> <range_args_tail> | ε   (capped at 3 total args)
//
//	range(end)               -- implicit start=0, step=1
//	range(start, end)        -- implicit step=1
//	range(start, end, step)
func (p *Parser) parseRangeExpr(start Token) Expr {
	p.advance() // consume 'range'
	p.consume(TOKEN_LPAREN)
	args := []Expr{p.parseExpr()}
	for p.current().Type == TOKEN_COMMA {
		p.advance()
		if len(args) >= 3 {
			p.fail(p.current(), "range() takes at most 3 arguments (start, end, step)")
		}
		args = append(args, p.parseExpr())
	}
	p.consume(TOKEN_RPAREN)
	return &RangeExpr{pos: pos{start.Line, start.Col}, Args: args}
}

// <primary_tail> → '(' <arg_list> ')' <chain_tail>      (fn_call, then further chaining)
//
//	| <chain_tail>                                    (bare identifier, then further chaining)
//
// <chain_tail> → ( '[' <expr> ']' | '.' <identifier> <method_tail> )*
//
// This loops so chained access like a.start.x, matrix[0][1], and
// makePoint().x all parse as a single nested expression instead of
// stopping after the first segment.
func (p *Parser) parsePrimaryTail(nameTok Token) Expr {
	var expr Expr
	if p.current().Type == TOKEN_LPAREN {
		p.advance()
		args := p.parseArgList()
		p.consume(TOKEN_RPAREN)
		expr = &FnCall{pos: pos{nameTok.Line, nameTok.Col}, Callee: nameTok.Literal, Args: args}
	} else {
		expr = &Identifier{pos: pos{nameTok.Line, nameTok.Col}, Name: nameTok.Literal}
	}
	return p.parseChainTail(expr)
}

// <this_tail> → '.' <identifier> <method_tail> <chain_tail> | ε
// (Grammar defines `this.ident(args)` as a method_call; reading a plain
// `this.field` as a value is a natural, symmetric extension of the
// assign_stmt form `this.ident = expr`, so it is supported here too.)
func (p *Parser) parseThisTail(thisTok Token) Expr {
	var expr Expr = &ThisExpr{pos: pos{thisTok.Line, thisTok.Col}}
	if p.current().Type != TOKEN_OP_DOT {
		return expr
	}
	return p.parseChainTail(expr)
}

// parseChainTail repeatedly consumes '[' <expr> ']' and '.' <identifier>
// (optionally followed by a call) suffixes, building up a nested chain of
// IndexExpr / MemberAccess / MethodCall around base. It stops as soon as
// none of those tokens follow, leaving the parser positioned there.
func (p *Parser) parseChainTail(base Expr) Expr {
	expr := base
	for {
		switch p.current().Type {
		case TOKEN_LBRACKET:
			tok := p.current()
			p.advance()
			idx := p.parseExpr()
			p.consume(TOKEN_RBRACKET)
			expr = &IndexExpr{pos: pos{tok.Line, tok.Col}, Base: expr, Index: idx}

		case TOKEN_OP_DOT:
			tok := p.current()
			p.advance()
			field := p.consume(TOKEN_IDENTIFIER)
			if p.current().Type == TOKEN_LPAREN {
				p.advance()
				args := p.parseArgList()
				p.consume(TOKEN_RPAREN)
				expr = &MethodCall{pos: pos{tok.Line, tok.Col}, Base: expr, MethodName: field.Literal, Args: args}
			} else {
				expr = &MemberAccess{pos: pos{tok.Line, tok.Col}, Base: expr, Field: field.Literal}
			}

		default:
			return expr
		}
	}
}

// <arg_list> → <args> | ε
// <args> → <expr> <args_tail>
// <args_tail> → ',' <expr> <args_tail> | ε
func (p *Parser) parseArgList() []Expr {
	var args []Expr
	if p.current().Type == TOKEN_RPAREN {
		return args
	}
	args = append(args, p.parseExpr())
	for p.current().Type == TOKEN_COMMA {
		p.advance()
		args = append(args, p.parseExpr())
	}
	return args
}
