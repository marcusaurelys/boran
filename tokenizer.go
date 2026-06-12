package main

import (
	"fmt"
	"io"
	"os"
	"time"
)

type TokenType string

const (
	TOKEN_EOF   TokenType = "EOF"
	TOKEN_ERROR TokenType = "ERROR"

	TOKEN_IDENTIFIER TokenType = "IDENTIFIER"
	TOKEN_KEYWORD    TokenType = "KEYWORD"

	TOKEN_INT_LIT    TokenType = "INT_LIT"
	TOKEN_FLOAT_LIT  TokenType = "FLOAT_LIT"
	TOKEN_CHAR_LIT   TokenType = "CHAR_LIT"
	TOKEN_STRING_LIT TokenType = "STRING_LIT"
	TOKEN_BOOL_LIT   TokenType = "BOOL_LIT"

	TOKEN_OP_ASSIGN TokenType = "ASSIGN"
	TOKEN_OP_EQUAL  TokenType = "EQUAL"
	TOKEN_OP_NOT_EQ TokenType = "NOT_EQ"
	TOKEN_OP_NOT    TokenType = "NOT"
	TOKEN_OP_ADD    TokenType = "ADD"
	TOKEN_OP_SUB    TokenType = "SUB"
	TOKEN_OP_MUL    TokenType = "MUL"
	TOKEN_OP_DIV    TokenType = "DIV"
	TOKEN_OP_MOD    TokenType = "MOD"
	TOKEN_OP_INC    TokenType = "INC"
	TOKEN_OP_DEC    TokenType = "DEC"
	TOKEN_OP_LT     TokenType = "LT"
	TOKEN_OP_GT     TokenType = "GT"
	TOKEN_OP_LE     TokenType = "LE"
	TOKEN_OP_GE     TokenType = "GE"
	TOKEN_OP_AND    TokenType = "AND"
	TOKEN_OP_OR     TokenType = "OR"
	TOKEN_OP_DOT    TokenType = "DOT"

	TOKEN_LBRACE    TokenType = "LBRACE"
	TOKEN_RBRACE    TokenType = "RBRACE"
	TOKEN_LPAREN    TokenType = "LPAREN"
	TOKEN_RPAREN    TokenType = "RPAREN"
	TOKEN_LBRACKET  TokenType = "LBRACKET"
	TOKEN_RBRACKET  TokenType = "RBRACKET"
	TOKEN_COMMA     TokenType = "COMMA"
	TOKEN_COLON     TokenType = "COLON"
	TOKEN_SEMICOLON TokenType = "SEMICOLON"
)

type Token struct {
	Type    TokenType
	Literal string
	Line    int
	Col     int
}

var keywords = map[string]TokenType{
	"const": TOKEN_KEYWORD, "let": TOKEN_KEYWORD,
	"int": TOKEN_KEYWORD, "float": TOKEN_KEYWORD, "char": TOKEN_KEYWORD,
	"string": TOKEN_KEYWORD, "bool": TOKEN_KEYWORD, "fn": TOKEN_KEYWORD,
	"struct": TOKEN_KEYWORD, "enum": TOKEN_KEYWORD, "ptr": TOKEN_KEYWORD,
	"if": TOKEN_KEYWORD, "else": TOKEN_KEYWORD, "for": TOKEN_KEYWORD,
	"in": TOKEN_KEYWORD, "return": TOKEN_KEYWORD,
	"print": TOKEN_KEYWORD, "input": TOKEN_KEYWORD,
	"true": TOKEN_BOOL_LIT, "false": TOKEN_BOOL_LIT,
	"null": TOKEN_KEYWORD, "this": TOKEN_KEYWORD,
}

type Scanner struct {
	input        string
	position     int
	readPosition int
	ch           byte
	line         int
	lineStart    int
}

func NewScanner(input string) *Scanner {
	s := &Scanner{input: input, line: 1, lineStart: 0}
	s.readChar()
	return s
}

func (s *Scanner) col() int {
	return s.position - s.lineStart + 1
}

// Consumes a character and increments the scanner's position
func (s *Scanner) readChar() {
	if s.readPosition >= len(s.input) {
		s.ch = 0
	} else {
		s.ch = s.input[s.readPosition]
	}
	s.position = s.readPosition
	s.readPosition++
}

// Peeks at the next character
func (s *Scanner) peekChar() byte {
	if s.readPosition >= len(s.input) {
		return 0
	}
	return s.input[s.readPosition]
}

func (s *Scanner) skipWhitespaceAndComments() {
	for {
		switch s.ch {
		case ' ', '\t', '\r':
			s.readChar()
		case '\n':
			s.line++
			s.lineStart = s.readPosition
			s.readChar()
		case '#':
			for s.ch != '\n' && s.ch != 0 {
				s.readChar()
			}
		default:
			return
		}
	}
}

// Skips to the next token
func (s *Scanner) NextToken() Token {
	s.skipWhitespaceAndComments()

	line := s.line
	col := s.col()

	tok := func(t TokenType, lit string) Token { return Token{t, lit, line, col} }
	err := func(msg string) Token { return Token{TOKEN_ERROR, msg, line, col} }

	switch s.ch {
	case '=':
		if s.peekChar() == '=' {
			s.readChar()
			s.readChar()
			return tok(TOKEN_OP_EQUAL, "==")
		}
		s.readChar()
		return tok(TOKEN_OP_ASSIGN, "=")

	case '!':
		if s.peekChar() == '=' {
			s.readChar()
			s.readChar()
			return tok(TOKEN_OP_NOT_EQ, "!=")
		}
		s.readChar()
		return tok(TOKEN_OP_NOT, "!")

	case '+':
		if s.peekChar() == '+' {
			s.readChar()
			s.readChar()
			return tok(TOKEN_OP_INC, "++")
		}
		s.readChar()
		return tok(TOKEN_OP_ADD, "+")

	case '-':
		if s.peekChar() == '-' {
			s.readChar()
			s.readChar()
			return tok(TOKEN_OP_DEC, "--")
		}
		s.readChar()
		return tok(TOKEN_OP_SUB, "-")

	case '*':
		s.readChar()
		return tok(TOKEN_OP_MUL, "*")
	case '/':
		s.readChar()
		return tok(TOKEN_OP_DIV, "/")
	case '%':
		s.readChar()
		return tok(TOKEN_OP_MOD, "%")

	case '<':
		if s.peekChar() == '=' {
			s.readChar()
			s.readChar()
			return tok(TOKEN_OP_LE, "<=")
		}
		s.readChar()
		return tok(TOKEN_OP_LT, "<")

	case '>':
		if s.peekChar() == '=' {
			s.readChar()
			s.readChar()
			return tok(TOKEN_OP_GE, ">=")
		}
		s.readChar()
		return tok(TOKEN_OP_GT, ">")

	case '&':
		if s.peekChar() == '&' {
			s.readChar()
			s.readChar()
			return tok(TOKEN_OP_AND, "&&")
		}
		s.readChar()
		return err("unexpected character '&' (did you mean '&&'?)")

	case '|':
		if s.peekChar() == '|' {
			s.readChar()
			s.readChar()
			return tok(TOKEN_OP_OR, "||")
		}
		s.readChar()
		return err("unexpected character '|' (did you mean '||'?)")

	case '.':
		if isDigit(s.peekChar()) {
			return s.readFloatStartingWithDot(line, col)
		}
		s.readChar()
		return tok(TOKEN_OP_DOT, ".")

	case '{':
		s.readChar()
		return tok(TOKEN_LBRACE, "{")
	case '}':
		s.readChar()
		return tok(TOKEN_RBRACE, "}")
	case '(':
		s.readChar()
		return tok(TOKEN_LPAREN, "(")
	case ')':
		s.readChar()
		return tok(TOKEN_RPAREN, ")")
	case '[':
		s.readChar()
		return tok(TOKEN_LBRACKET, "[")
	case ']':
		s.readChar()
		return tok(TOKEN_RBRACKET, "]")
	case ',':
		s.readChar()
		return tok(TOKEN_COMMA, ",")
	case ':':
		s.readChar()
		return tok(TOKEN_COLON, ":")
	case ';':
		s.readChar()
		return tok(TOKEN_SEMICOLON, ";")

	case '"':
		return s.readStringLiteral(line, col)
	case '\'':
		return s.readCharLiteral(line, col)
	case 0:
		return tok(TOKEN_EOF, "")

	default:
		if isLetter(s.ch) {
			lit := s.readIdentifier()
			tt, isKw := keywords[lit]
			if !isKw {
				tt = TOKEN_IDENTIFIER
			}
			return Token{tt, lit, line, col}
		}
		if isDigit(s.ch) {
			return s.readNumberLiteral(line, col)
		}
		bad := s.ch
		s.readChar()
		return err(fmt.Sprintf("unexpected character %q", bad))
	}
}

func (s *Scanner) readIdentifier() string {
	start := s.position
	for isLetter(s.ch) || isDigit(s.ch) {
		s.readChar()
	}
	return s.input[start:s.position]
}

func (s *Scanner) consumeJunkSuffix() {
	for isLetter(s.ch) || isDigit(s.ch) || s.ch == '_' || s.ch == '.' {
		s.readChar()
	}
}

func (s *Scanner) readNumberLiteral(line, col int) Token {
	start := s.position

	for isDigit(s.ch) {
		s.readChar()
	}

	isFloat := false

	if s.ch == '.' {
		peek := s.peekChar()
		if peek == '.' {
			s.consumeJunkSuffix()
			return Token{TOKEN_ERROR,
				fmt.Sprintf("invalid numeric literal %q: consecutive dots", s.input[start:s.position]),
				line, col}
		}
		if isDigit(peek) {
			// consume the fractional part
			isFloat = true
			s.readChar() // consume '.'
			for isDigit(s.ch) {
				s.readChar()
			}
			if s.ch == '.' {
				s.consumeJunkSuffix()
				return Token{TOKEN_ERROR,
					fmt.Sprintf("invalid float literal %q: unexpected '.' after fractional part", s.input[start:s.position]),
					line, col}
			}
		}
	}

	if isLetter(s.ch) || s.ch == '_' {
		s.consumeJunkSuffix()
		return Token{TOKEN_ERROR,
			fmt.Sprintf("invalid numeric literal %q", s.input[start:s.position]),
			line, col}
	}

	lit := s.input[start:s.position]

	if !isFloat && len(lit) > 1 && lit[0] == '0' {
		return Token{TOKEN_ERROR,
			fmt.Sprintf("invalid integer literal %q: leading zeros are not allowed", lit),
			line, col}
	}

	tt := TOKEN_INT_LIT
	if isFloat {
		tt = TOKEN_FLOAT_LIT
	}
	return Token{tt, lit, line, col}
}

func (s *Scanner) readFloatStartingWithDot(line, col int) Token {
	// Called only when ch=='.' and peekChar() is a digit, so ".5" form.
	start := s.position
	s.readChar() // consume '.'
	for isDigit(s.ch) {
		s.readChar()
	}

	if s.ch == '.' {
		s.consumeJunkSuffix()
		return Token{TOKEN_ERROR,
			fmt.Sprintf("invalid float literal %q: unexpected '.' after fractional part", s.input[start:s.position]),
			line, col}
	}
	if isLetter(s.ch) || s.ch == '_' {
		s.consumeJunkSuffix()
		return Token{TOKEN_ERROR,
			fmt.Sprintf("invalid numeric literal %q", s.input[start:s.position]),
			line, col}
	}
	return Token{TOKEN_FLOAT_LIT, s.input[start:s.position], line, col}
}

func (s *Scanner) readStringLiteral(line, col int) Token {
	s.readChar() // consume opening '"'
	start := s.position

	for {
		switch s.ch {
		case 0:
			return Token{TOKEN_ERROR,
				fmt.Sprintf("unterminated string literal starting at line %d col %d", line, col),
				line, col}
		case '\n':
			s.line++
			s.lineStart = s.readPosition
			return Token{TOKEN_ERROR,
				fmt.Sprintf("unterminated string literal (unexpected newline) at line %d col %d", line, col),
				line, col}
		case '\\':
			s.readChar()
			if s.ch == 0 || s.ch == '\n' {
				return Token{TOKEN_ERROR,
					fmt.Sprintf("unterminated escape sequence in string at line %d col %d", line, col),
					line, col}
			}
			if !isValidEscape(s.ch) {
				bad := s.ch
				s.readChar()
				return Token{TOKEN_ERROR,
					fmt.Sprintf("invalid escape sequence '\\%c' in string at line %d col %d", bad, line, col),
					line, col}
			}
			s.readChar()
		case '"':
			content := s.input[start:s.position]
			s.readChar() // consume closing '"'
			return Token{TOKEN_STRING_LIT, `"` + content + `"`, line, col}
		default:
			s.readChar()
		}
	}
}

func (s *Scanner) readCharLiteral(line, col int) Token {
	s.readChar() // consume opening '\''
	start := s.position

	if s.ch == '\'' {
		s.readChar()
		return Token{TOKEN_ERROR,
			fmt.Sprintf("empty char literal at line %d col %d", line, col),
			line, col}
	}
	if s.ch == 0 || s.ch == '\n' {
		if s.ch == '\n' {
			s.line++
			s.lineStart = s.readPosition
		}
		return Token{TOKEN_ERROR,
			fmt.Sprintf("unterminated char literal at line %d col %d", line, col),
			line, col}
	}

	if s.ch == '\\' {
		s.readChar()
		if s.ch == 0 || s.ch == '\n' {
			return Token{TOKEN_ERROR,
				fmt.Sprintf("unterminated escape sequence in char literal at line %d col %d", line, col),
				line, col}
		}
		if !isValidEscape(s.ch) {
			bad := s.ch
			s.readChar()
			return Token{TOKEN_ERROR,
				fmt.Sprintf("invalid escape sequence '\\%c' in char literal at line %d col %d", bad, line, col),
				line, col}
		}
	}
	s.readChar() // step past body

	// recovers and doesn't re-tokenise the overflow as separate tokens.
	if s.ch != '\'' {
		for s.ch != '\'' && s.ch != '\n' && s.ch != 0 {
			s.readChar()
		}
		if s.ch == '\'' {
			s.readChar()
		}
		return Token{TOKEN_ERROR,
			fmt.Sprintf("char literal contains more than one character at line %d col %d", line, col),
			line, col}
	}

	content := s.input[start:s.position]
	s.readChar() // consume closing '\''
	return Token{TOKEN_CHAR_LIT, "'" + content + "'", line, col}
}

func isLetter(ch byte) bool {
	return ('a' <= ch && ch <= 'z') || ('A' <= ch && ch <= 'Z') || ch == '_'
}

func isDigit(ch byte) bool {
	return '0' <= ch && ch <= '9'
}

func isValidEscape(ch byte) bool {
	switch ch {
	case 'n', 't', 'r', '\\', '\'', '"', '0':
		return true
	}
	return false
}

func main() {
	// 1. Enforce argument constraints
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: boran <source_file> [output_file_or_stdout]")
		os.Exit(1)
	}

	// 2. Read the Boran source file
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not read %q: %v\n", os.Args[1], err)
		os.Exit(1)
	}

	// --- TOKENS IN MEMORY FIRST ---
	timeStart := time.Now()
	scanner := NewScanner(string(data))

	var tokens []Token
	hadError := false

	for {
		tok := scanner.NextToken()
		if tok.Type == TOKEN_EOF {
			break
		}
		if tok.Type == TOKEN_ERROR {
			hadError = true
		}
		tokens = append(tokens, tok)
	}
	elapsed := time.Since(timeStart)
	// ------------------------------

	// 3. Determine output destination
	var outputWriter io.Writer = os.Stdout
	usingCustomFile := false

	if len(os.Args) == 3 {
		arg2 := os.Args[2]
		if arg2 != "stdout" {
			f, err := os.Create(arg2)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: could not create output file %q: %v\n", arg2, err)
				os.Exit(1)
			}
			defer f.Close()
			outputWriter = f
			usingCustomFile = true
		}
	}

	// 5. Final performance metrics
	if usingCustomFile {
		fmt.Printf("Scan complete. Time to run: %s\n", elapsed)
	}
	fmt.Fprintf(outputWriter, "Time to run %s\n", elapsed)

	// 4. Dump everything to the writer at once
	if usingCustomFile {
		fmt.Printf("Dumping lexical scan analysis of %s to %s...\n", os.Args[1], os.Args[2])
	} else {
		fmt.Fprintln(outputWriter, "--- Boran Lexical Scan ---")
	}

	for _, tok := range tokens {
		if tok.Type == TOKEN_ERROR {
			fmt.Fprintf(outputWriter, "[ERROR] %d:%d  %s\n", tok.Line, tok.Col, tok.Literal)
		} else {
			fmt.Fprintf(outputWriter, "%d:%-3d  %-15s  %q\n", tok.Line, tok.Col, tok.Type, tok.Literal)
		}
	}

	if hadError {
		os.Exit(1)
	}
}
