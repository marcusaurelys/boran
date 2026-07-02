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
	"fn": true, "struct": true, "enum": true, "ptr": true,
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
		}
	}

	// 2. Blocks
	if tok.Type == TOKEN_LBRACE {
		return p.parseBlock()
	}

	// 3. Assignments (Lookahead for identifier or 'this' followed by =, [, or .)
	if p.isAssignmentStart() {
		return p.parseAssignStmt()
	}

	// 4. Expression Statements (e.g. ++i;, myFunc();)
	// This covers the BNF: <stmt> ::= <expr> ';'
	expr := p.parseExpr()
	p.consume(TOKEN_SEMICOLON)
	return &ExprStmt{pos: pos{tok.Line, tok.Col}, Call: expr}
}

func (p *Parser) isAssignmentStart() bool {
	tok := p.current()
	if tok.Type != TOKEN_IDENTIFIER && !(tok.Type == TOKEN_KEYWORD && tok.Literal == "this") {
		return false
	}
	// Look ahead for =, [, or .
	next := p.peek()
	return next.Type == TOKEN_OP_ASSIGN || next.Type == TOKEN_LBRACKET || next.Type == TOKEN_OP_DOT
}

func (p *Parser) parseAssignStmt() *AssignStmt {
	tok := p.advance() // Consume identifier or 'this'
	startPos := pos{tok.Line, tok.Col}
	target := AssignTarget{pos: startPos}

	if tok.Literal == "this" {
		p.consume(TOKEN_OP_DOT)
		field := p.consume(TOKEN_IDENTIFIER)
		target.Kind = TargetThisMember
		target.Field = field.Literal
	} else {
		target.Name = tok.Literal
		switch p.current().Type {
		case TOKEN_LBRACKET:
			p.advance()
			target.IndexExpr = p.parseExpr()
			p.consume(TOKEN_RBRACKET)
			target.Kind = TargetIndex
		case TOKEN_OP_DOT:
			p.advance()
			field := p.consume(TOKEN_IDENTIFIER)
			target.Field = field.Literal
			target.Kind = TargetMember
		default:
			target.Kind = TargetIdent
		}
	}

	p.consume(TOKEN_OP_ASSIGN)
	val := p.parseExpr()
	p.consume(TOKEN_SEMICOLON)
	return &AssignStmt{pos: startPos, Target: target, Value: val}
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

	// 2. for { <block> } <bool_expr> ;
	if p.current().Type == TOKEN_LBRACE {
		body := p.parseBlock()
		cond := p.parseExpr()
		p.consume(TOKEN_SEMICOLON) // Added per new BNF
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
	p.Symbols.Declare(name.Literal, kind, typeName, name.Line, name.Col)

	return &ConstDecl{pos: pos{start.Line, start.Col}, Name: name.Literal, TypeName: typeName, Value: val}
}

func (p *Parser) parseLetDecl() *LetDecl {
	start := p.consumeKeyword("let")
	name := p.consume(TOKEN_IDENTIFIER)
	p.consume(TOKEN_COLON)
	dtype, typeName := p.parseTypeAnnotation()
	p.consume(TOKEN_OP_ASSIGN)
	val := p.parseValue(typeName, dtype)
	p.consume(TOKEN_SEMICOLON)

	p.Symbols.Declare(name.Literal, SymLet, typeName, name.Line, name.Col)

	return &LetDecl{pos: pos{start.Line, start.Col}, Name: name.Literal, TypeName: typeName, Value: val}
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
		case "ptr":
			elem, _ := p.parseTypeAnnotation()
			base = &DatatypeNode{pos: pos{tok.Line, tok.Col}, Kind: "ptr", Elem: elem}
			tag = "ptr"
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

	// <arr_type> → <datatype> '[' INT_LIT ']'  (may wrap any base type)
	if p.current().Type == TOKEN_LBRACKET {
		p.advance()
		lenTok := p.consume(TOKEN_INT_LIT)
		p.consume(TOKEN_RBRACKET)
		n := 0
		fmt.Sscanf(lenTok.Literal, "%d", &n)
		base = &DatatypeNode{pos: base.pos, Kind: "array", Elem: base, ArrLen: n}
		tag = "array"
	}

	return base, tag
}

// ---- Values (<value>) ------------------------------------------------------

// parseValue implements <value>, disambiguated using the declared-type
// context (typeTag/dtype) established by the enclosing const/let decl or
// struct instance field, since '{' and '(' are each shared by two
// grammar alternatives that are not distinguishable by a single token of
// lookahead alone.
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
		if typeTag == "fn" {
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
	name := p.consume(TOKEN_IDENTIFIER)
	p.consume(TOKEN_COLON)
	f := StructFieldInit{pos: pos{name.Line, name.Col}, Name: name.Literal}
	if p.current().Type == TOKEN_LPAREN {
		f.FnValue = p.parseFnLiteral()
		f.TypeName = "fn"
	} else {
		_, tag := p.parseTypeAnnotation()
		f.TypeName = tag
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

	// parseBlock manages its own scope for statements; params were already
	// registered in the scope we just entered, so borrow that same scope
	// for the body by inlining block parsing rather than calling
	// parseBlock (which would open yet another nested scope).
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
	p.Symbols.Declare(name.Literal, SymParam, tag, name.Line, name.Col)
	return Param{pos: pos{name.Line, name.Col}, Name: name.Literal, Type: *dtype}
}

// ============================================================================
// Identifier-led / this-led statements
// (assign_stmt, fn_call-as-stmt, method_call-as-stmt, io_stmt's input form)
// ============================================================================

func (p *Parser) parseIdentifierStmt() Stmt {
	name := p.consume(TOKEN_IDENTIFIER)

	switch p.current().Type {
	case TOKEN_OP_ASSIGN:
		p.advance()
		// Logic removed: 'if p.isKeyword("input")' is no longer needed
		// because parseExpr() will handle input() automatically.
		val := p.parseExpr()
		p.consume(TOKEN_SEMICOLON)
		return &AssignStmt{
			pos:    pos{name.Line, name.Col},
			Target: AssignTarget{pos: pos{name.Line, name.Col}, Kind: TargetIdent, Name: name.Literal},
			Value:  val,
		}

	case TOKEN_OP_INC, TOKEN_OP_DEC: // ADDED: Handle postfix i++;
		p.pos-- // Step back to include identifier in the expression
		expr := p.parseExpr()
		p.consume(TOKEN_SEMICOLON)
		return &ExprStmt{pos: pos{name.Line, name.Col}, Call: expr}

	// ... (LBRACKET and OP_DOT cases remain similar) ...

	case TOKEN_LPAREN:
		p.advance()
		args := p.parseArgList()
		p.consume(TOKEN_RPAREN)
		p.consume(TOKEN_SEMICOLON)
		return &ExprStmt{pos: pos{name.Line, name.Col}, Call: &FnCall{
			pos:    pos{name.Line, name.Col},
			Callee: name.Literal,
			Args:   args,
		}}

	default:
		// If it's just a bare identifier like 'x;', it's still a valid ExprStmt
		p.consume(TOKEN_SEMICOLON)
		return &ExprStmt{pos: pos{name.Line, name.Col}, Call: &Identifier{
			pos:  pos{name.Line, name.Col},
			Name: name.Literal,
		}}
	}
}

// this . identifier ( arg_list ) ;   -- method call statement
// this . identifier = expr ;        -- field assignment
func (p *Parser) parseThisStmt() Stmt {
	start := p.consumeKeyword("this")
	p.consume(TOKEN_OP_DOT)
	field := p.consume(TOKEN_IDENTIFIER)

	if p.current().Type == TOKEN_LPAREN {
		p.advance()
		args := p.parseArgList()
		p.consume(TOKEN_RPAREN)
		p.consume(TOKEN_SEMICOLON)
		return &ExprStmt{pos: pos{start.Line, start.Col}, Call: &MethodCall{
			pos: pos{start.Line, start.Col}, IsThis: true, MethodName: field.Literal, Args: args,
		}}
	}

	p.consume(TOKEN_OP_ASSIGN)
	val := p.parseExpr()
	p.consume(TOKEN_SEMICOLON)
	return &AssignStmt{
		pos:    pos{start.Line, start.Col},
		Target: AssignTarget{pos: pos{start.Line, start.Col}, Kind: TargetThisMember, Field: field.Literal},
		Value:  val,
	}
}

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
	case tok.Type == TOKEN_OP_AND && tok.Literal == "&":
		p.advance()
		return &UnaryExpr{pos: pos{tok.Line, tok.Col}, Op: "&", Operand: p.parseUnaryExpr()}
	default:
		return p.parsePostfixExpr()
	}
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

// <primary_tail> → '(' <arg_list> ')'            (fn_call)
//
//	| '[' <expr> ']'                (index_expr)
//	| '.' <identifier> <method_tail> (member_access | method_call)
//	| ε                              (bare identifier)
//
// <method_tail> → '(' <arg_list> ')' | ε
func (p *Parser) parsePrimaryTail(nameTok Token) Expr {
	switch p.current().Type {
	case TOKEN_LPAREN:
		p.advance()
		args := p.parseArgList()
		p.consume(TOKEN_RPAREN)
		return &FnCall{pos: pos{nameTok.Line, nameTok.Col}, Callee: nameTok.Literal, Args: args}

	case TOKEN_LBRACKET:
		p.advance()
		idx := p.parseExpr()
		p.consume(TOKEN_RBRACKET)
		return &IndexExpr{pos: pos{nameTok.Line, nameTok.Col}, Array: nameTok.Literal, Index: idx}

	case TOKEN_OP_DOT:
		p.advance()
		field := p.consume(TOKEN_IDENTIFIER)
		if p.current().Type == TOKEN_LPAREN {
			p.advance()
			args := p.parseArgList()
			p.consume(TOKEN_RPAREN)
			return &MethodCall{pos: pos{nameTok.Line, nameTok.Col}, Receiver: nameTok.Literal, MethodName: field.Literal, Args: args}
		}
		return &MemberAccess{pos: pos{nameTok.Line, nameTok.Col}, Base: nameTok.Literal, Field: field.Literal}

	default:
		return &Identifier{pos: pos{nameTok.Line, nameTok.Col}, Name: nameTok.Literal}
	}
}

// <this_tail> → '.' <identifier> ('(' <arg_list> ')')?  | ε
// (Grammar defines `this.ident(args)` as a method_call; reading a plain
// `this.field` as a value is a natural, symmetric extension of the
// assign_stmt form `this.ident = expr`, so it is supported here too.)
func (p *Parser) parseThisTail(thisTok Token) Expr {
	if p.current().Type != TOKEN_OP_DOT {
		return &ThisExpr{pos: pos{thisTok.Line, thisTok.Col}}
	}
	p.advance()
	field := p.consume(TOKEN_IDENTIFIER)
	if p.current().Type == TOKEN_LPAREN {
		p.advance()
		args := p.parseArgList()
		p.consume(TOKEN_RPAREN)
		return &MethodCall{pos: pos{thisTok.Line, thisTok.Col}, IsThis: true, MethodName: field.Literal, Args: args}
	}
	return &MemberAccess{pos: pos{thisTok.Line, thisTok.Col}, Base: "this", Field: field.Literal}
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
