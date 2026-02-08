package query

import (
	"fmt"
	"strings"
)

// Node represents a node in the query AST.
type Node interface {
	node() // marker method
	String() string
}

// ComparisonOp represents a comparison operator.
type ComparisonOp int

const (
	OpEquals ComparisonOp = iota
	OpNotEquals
	OpLess
	OpLessEq
	OpGreater
	OpGreaterEq
)

// String returns the string representation of a ComparisonOp.
func (op ComparisonOp) String() string {
	switch op {
	case OpEquals:
		return "="
	case OpNotEquals:
		return "!="
	case OpLess:
		return "<"
	case OpLessEq:
		return "<="
	case OpGreater:
		return ">"
	case OpGreaterEq:
		return ">="
	default:
		return "?"
	}
}

// ComparisonNode represents a field comparison (e.g., status=open).
type ComparisonNode struct {
	Field     string
	Op        ComparisonOp
	Value     string
	ValueType TokenType // TokenIdent, TokenString, TokenNumber, or TokenDuration
}

func (n *ComparisonNode) node() {}
func (n *ComparisonNode) String() string {
	return fmt.Sprintf("%s%s%s", n.Field, n.Op.String(), n.Value)
}

// AndNode represents a logical AND operation.
type AndNode struct {
	Left  Node
	Right Node
}

func (n *AndNode) node() {}
func (n *AndNode) String() string {
	return fmt.Sprintf("(%s AND %s)", n.Left.String(), n.Right.String())
}

// OrNode represents a logical OR operation.
type OrNode struct {
	Left  Node
	Right Node
}

func (n *OrNode) node() {}
func (n *OrNode) String() string {
	return fmt.Sprintf("(%s OR %s)", n.Left.String(), n.Right.String())
}

// NotNode represents a logical NOT operation.
type NotNode struct {
	Operand Node
}

func (n *NotNode) node() {}
func (n *NotNode) String() string {
	return fmt.Sprintf("NOT %s", n.Operand.String())
}

// Parser parses a query string into an AST.
type Parser struct {
	lexer   *Lexer
	current Token
	peeked  *Token
}

// NewParser creates a new Parser for the given input.
func NewParser(input string) *Parser {
	return &Parser{lexer: NewLexer(input)}
}

// Parse parses the query string and returns the root AST node.
func (p *Parser) Parse() (Node, error) {
	if err := p.advance(); err != nil {
		return nil, err
	}

	if p.current.Type == TokenEOF {
		return nil, fmt.Errorf("empty query")
	}

	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}

	if p.current.Type != TokenEOF {
		return nil, fmt.Errorf("unexpected token %q at position %d (expected end of query)", p.current.Value, p.current.Pos)
	}

	return node, nil
}

// advance moves to the next token.
func (p *Parser) advance() error {
	if p.peeked != nil {
		p.current = *p.peeked
		p.peeked = nil
		return nil
	}
	tok, err := p.lexer.NextToken()
	if err != nil {
		return err
	}
	p.current = tok
	return nil
}

// peek returns the next token without consuming it.
func (p *Parser) peek() (Token, error) {
	if p.peeked != nil {
		return *p.peeked, nil
	}
	tok, err := p.lexer.NextToken()
	if err != nil {
		return Token{}, err
	}
	p.peeked = &tok
	return tok, nil
}

// parseOr parses OR expressions (lowest precedence).
func (p *Parser) parseOr() (Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}

	for p.current.Type == TokenOr {
		if err := p.advance(); err != nil {
			return nil, err
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &OrNode{Left: left, Right: right}
	}

	return left, nil
}

// parseAnd parses AND expressions.
func (p *Parser) parseAnd() (Node, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}

	for p.current.Type == TokenAnd {
		if err := p.advance(); err != nil {
			return nil, err
		}
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &AndNode{Left: left, Right: right}
	}

	return left, nil
}

// parseNot parses NOT expressions.
func (p *Parser) parseNot() (Node, error) {
	if p.current.Type == TokenNot {
		if err := p.advance(); err != nil {
			return nil, err
		}
		operand, err := p.parseNot() // NOT is right-associative
		if err != nil {
			return nil, err
		}
		return &NotNode{Operand: operand}, nil
	}

	return p.parsePrimary()
}

// parsePrimary parses primary expressions (comparisons and parenthesized expressions).
func (p *Parser) parsePrimary() (Node, error) {
	if p.current.Type == TokenLParen {
		if err := p.advance(); err != nil {
			return nil, err
		}
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.current.Type != TokenRParen {
			return nil, fmt.Errorf("expected ')' at position %d, got %s", p.current.Pos, p.current.Type.String())
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
		return node, nil
	}

	return p.parseComparison()
}

// parseComparison parses a field comparison.
func (p *Parser) parseComparison() (Node, error) {
	if p.current.Type != TokenIdent {
		return nil, fmt.Errorf("expected field name at position %d, got %s", p.current.Pos, p.current.Type.String())
	}

	field := strings.ToLower(p.current.Value)
	if err := p.advance(); err != nil {
		return nil, err
	}

	var op ComparisonOp
	switch p.current.Type {
	case TokenEquals:
		op = OpEquals
	case TokenNotEquals:
		op = OpNotEquals
	case TokenLess:
		op = OpLess
	case TokenLessEq:
		op = OpLessEq
	case TokenGreater:
		op = OpGreater
	case TokenGreaterEq:
		op = OpGreaterEq
	default:
		return nil, fmt.Errorf("expected comparison operator at position %d, got %s", p.current.Pos, p.current.Type.String())
	}

	if err := p.advance(); err != nil {
		return nil, err
	}

	// Value can be identifier, string, number, or duration
	var value string
	var valueType TokenType
	switch p.current.Type {
	case TokenIdent:
		value = p.current.Value
		valueType = TokenIdent
	case TokenString:
		value = p.current.Value
		valueType = TokenString
	case TokenNumber:
		value = p.current.Value
		valueType = TokenNumber
	case TokenDuration:
		value = p.current.Value
		valueType = TokenDuration
	default:
		return nil, fmt.Errorf("expected value at position %d, got %s", p.current.Pos, p.current.Type.String())
	}

	if err := p.advance(); err != nil {
		return nil, err
	}

	return &ComparisonNode{
		Field:     field,
		Op:        op,
		Value:     value,
		ValueType: valueType,
	}, nil
}

// Parse is a convenience function that parses a query string.
func Parse(input string) (Node, error) {
	p := NewParser(input)
	return p.Parse()
}

// KnownFields lists fields that can be queried.
var KnownFields = map[string]bool{
	// Core fields
	"id":          true,
	"title":       true,
	"description": true,
	"desc":        true, // alias
	"status":      true,
	"priority":    true,
	"type":        true,
	"assignee":    true,
	"owner":       true,

	// Timestamps
	"created":    true,
	"updated":    true,
	"closed":     true,
	"created_at": true, // alias
	"updated_at": true, // alias
	"closed_at":  true, // alias

	// Labels
	"label":  true,
	"labels": true, // alias

	// Flags
	"pinned":    true,
	"ephemeral": true,
	"template":  true,

	// Other
	"spec":     true,
	"spec_id":  true, // alias
	"parent":   true,
	"mol_type": true,
	"notes":    true,
}
