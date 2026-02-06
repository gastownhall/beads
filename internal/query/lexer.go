// Package query implements a simple query language for filtering beads.
//
// The query language supports:
//   - Field comparisons: status=open, priority>1, updated>7d
//   - Boolean operators: AND, OR, NOT
//   - Parentheses for grouping: (status=open OR status=blocked) AND priority<2
//   - Date-relative expressions: updated>7d, created<30d
//
// Example queries:
//   - status=open AND priority>1
//   - (status=open OR status=blocked) AND updated>7d
//   - NOT status=closed
//   - type=bug AND priority=0
package query

import (
	"fmt"
	"strings"
	"unicode"
)

// TokenType represents the type of a lexer token.
type TokenType int

const (
	TokenEOF TokenType = iota
	TokenIdent       // field names, values
	TokenString      // quoted strings
	TokenNumber      // numeric values
	TokenDuration    // duration values like 7d, 24h
	TokenEquals      // =
	TokenNotEquals   // !=
	TokenLess        // <
	TokenLessEq      // <=
	TokenGreater     // >
	TokenGreaterEq   // >=
	TokenAnd         // AND
	TokenOr          // OR
	TokenNot         // NOT
	TokenLParen      // (
	TokenRParen      // )
	TokenComma       // , (for lists)
)

// String returns the string representation of a TokenType.
func (t TokenType) String() string {
	switch t {
	case TokenEOF:
		return "EOF"
	case TokenIdent:
		return "IDENT"
	case TokenString:
		return "STRING"
	case TokenNumber:
		return "NUMBER"
	case TokenDuration:
		return "DURATION"
	case TokenEquals:
		return "="
	case TokenNotEquals:
		return "!="
	case TokenLess:
		return "<"
	case TokenLessEq:
		return "<="
	case TokenGreater:
		return ">"
	case TokenGreaterEq:
		return ">="
	case TokenAnd:
		return "AND"
	case TokenOr:
		return "OR"
	case TokenNot:
		return "NOT"
	case TokenLParen:
		return "("
	case TokenRParen:
		return ")"
	case TokenComma:
		return ","
	default:
		return fmt.Sprintf("UNKNOWN(%d)", t)
	}
}

// Token represents a single token from the lexer.
type Token struct {
	Type  TokenType
	Value string
	Pos   int // Position in input string
}

// Lexer tokenizes a query string.
type Lexer struct {
	input string
	pos   int
	width int // width of last rune read
}

// NewLexer creates a new Lexer for the given input string.
func NewLexer(input string) *Lexer {
	return &Lexer{input: input}
}

// next returns the next rune and advances position.
func (l *Lexer) next() rune {
	if l.pos >= len(l.input) {
		l.width = 0
		return 0
	}
	r := rune(l.input[l.pos])
	l.width = 1
	l.pos += l.width
	return r
}

// peek returns the next rune without advancing.
func (l *Lexer) peek() rune {
	if l.pos >= len(l.input) {
		return 0
	}
	return rune(l.input[l.pos])
}

// backup steps back one rune.
func (l *Lexer) backup() {
	l.pos -= l.width
}

// skipWhitespace skips whitespace characters.
func (l *Lexer) skipWhitespace() {
	for {
		r := l.next()
		if r == 0 || !unicode.IsSpace(r) {
			l.backup()
			return
		}
	}
}

// NextToken returns the next token from the input.
func (l *Lexer) NextToken() (Token, error) {
	l.skipWhitespace()

	startPos := l.pos
	r := l.next()

	if r == 0 {
		return Token{Type: TokenEOF, Pos: startPos}, nil
	}

	switch r {
	case '(':
		return Token{Type: TokenLParen, Value: "(", Pos: startPos}, nil
	case ')':
		return Token{Type: TokenRParen, Value: ")", Pos: startPos}, nil
	case ',':
		return Token{Type: TokenComma, Value: ",", Pos: startPos}, nil
	case '=':
		return Token{Type: TokenEquals, Value: "=", Pos: startPos}, nil
	case '!':
		if l.peek() == '=' {
			l.next()
			return Token{Type: TokenNotEquals, Value: "!=", Pos: startPos}, nil
		}
		return Token{}, fmt.Errorf("unexpected character '!' at position %d (did you mean '!=' or 'NOT'?)", startPos)
	case '<':
		if l.peek() == '=' {
			l.next()
			return Token{Type: TokenLessEq, Value: "<=", Pos: startPos}, nil
		}
		return Token{Type: TokenLess, Value: "<", Pos: startPos}, nil
	case '>':
		if l.peek() == '=' {
			l.next()
			return Token{Type: TokenGreaterEq, Value: ">=", Pos: startPos}, nil
		}
		return Token{Type: TokenGreater, Value: ">", Pos: startPos}, nil
	case '"', '\'':
		return l.readString(r, startPos)
	default:
		if unicode.IsDigit(r) || r == '-' || r == '+' {
			l.backup()
			return l.readNumberOrDuration(startPos)
		}
		if isIdentStart(r) {
			l.backup()
			return l.readIdent(startPos)
		}
		return Token{}, fmt.Errorf("unexpected character %q at position %d", r, startPos)
	}
}

// readString reads a quoted string.
func (l *Lexer) readString(quote rune, startPos int) (Token, error) {
	var sb strings.Builder
	for {
		r := l.next()
		if r == 0 {
			return Token{}, fmt.Errorf("unterminated string starting at position %d", startPos)
		}
		if r == quote {
			return Token{Type: TokenString, Value: sb.String(), Pos: startPos}, nil
		}
		if r == '\\' {
			// Handle escape sequences
			escaped := l.next()
			switch escaped {
			case 'n':
				sb.WriteRune('\n')
			case 't':
				sb.WriteRune('\t')
			case '\\':
				sb.WriteRune('\\')
			case '"':
				sb.WriteRune('"')
			case '\'':
				sb.WriteRune('\'')
			case 0:
				return Token{}, fmt.Errorf("unterminated escape sequence at position %d", l.pos-1)
			default:
				sb.WriteRune(escaped)
			}
		} else {
			sb.WriteRune(r)
		}
	}
}

// readNumberOrDuration reads a number or duration (e.g., 7d, 24h).
func (l *Lexer) readNumberOrDuration(startPos int) (Token, error) {
	var sb strings.Builder

	// Handle optional sign
	r := l.next()
	if r == '-' || r == '+' {
		sb.WriteRune(r)
		r = l.next()
	}

	// Must have at least one digit
	if !unicode.IsDigit(r) {
		l.backup()
		// This might be a minus in front of an identifier, which is invalid
		return Token{}, fmt.Errorf("expected digit at position %d", l.pos)
	}
	sb.WriteRune(r)

	// Read remaining digits
	for {
		r = l.next()
		if !unicode.IsDigit(r) {
			break
		}
		sb.WriteRune(r)
	}

	// Check for duration suffix
	if r != 0 && isDurationSuffix(r) {
		sb.WriteRune(r)
		return Token{Type: TokenDuration, Value: sb.String(), Pos: startPos}, nil
	}

	// Not a duration suffix, back up
	if r != 0 {
		l.backup()
	}

	return Token{Type: TokenNumber, Value: sb.String(), Pos: startPos}, nil
}

// readIdent reads an identifier or keyword.
func (l *Lexer) readIdent(startPos int) (Token, error) {
	var sb strings.Builder

	for {
		r := l.next()
		if r == 0 || !isIdentChar(r) {
			l.backup()
			break
		}
		sb.WriteRune(r)
	}

	value := sb.String()
	upper := strings.ToUpper(value)

	// Check for keywords
	switch upper {
	case "AND":
		return Token{Type: TokenAnd, Value: value, Pos: startPos}, nil
	case "OR":
		return Token{Type: TokenOr, Value: value, Pos: startPos}, nil
	case "NOT":
		return Token{Type: TokenNot, Value: value, Pos: startPos}, nil
	default:
		return Token{Type: TokenIdent, Value: value, Pos: startPos}, nil
	}
}

// Tokenize returns all tokens from the input.
func (l *Lexer) Tokenize() ([]Token, error) {
	var tokens []Token
	for {
		tok, err := l.NextToken()
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
		if tok.Type == TokenEOF {
			break
		}
	}
	return tokens, nil
}

// isIdentStart returns true if r can start an identifier.
func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

// isIdentChar returns true if r can be part of an identifier.
func isIdentChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.'
}

// isDurationSuffix returns true if r is a valid duration suffix.
func isDurationSuffix(r rune) bool {
	switch r {
	case 'h', 'd', 'w', 'm', 'y', 'H', 'D', 'W', 'M', 'Y':
		return true
	default:
		return false
	}
}
