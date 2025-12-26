// Package formula provides range expression evaluation for computed loops.
//
// Range expressions enable loops with computed bounds (gt-8tmz.27):
//
//	range: "1..10"           // Simple integer range
//	range: "1..2^{disks}"    // Expression with variable
//	range: "{start}..{end}"  // Variable bounds
//
// Supports: + - * / ^ (power) and parentheses.
// Variables use {name} syntax and are substituted from the vars map.
package formula

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// RangeSpec represents a parsed range expression.
type RangeSpec struct {
	Start int // Evaluated start value (inclusive)
	End   int // Evaluated end value (inclusive)
}

// rangePattern matches "start..end" format.
var rangePattern = regexp.MustCompile(`^(.+)\.\.(.+)$`)

// rangeVarPattern matches {varname} placeholders in range expressions.
var rangeVarPattern = regexp.MustCompile(`\{(\w+)\}`)

// ParseRange parses a range expression and evaluates it using the given variables.
// Returns the start and end values of the range.
//
// Examples:
//
//	ParseRange("1..10", nil)           -> {1, 10}
//	ParseRange("1..2^3", nil)          -> {1, 8}
//	ParseRange("1..2^{n}", {"n":"3"})  -> {1, 8}
func ParseRange(expr string, vars map[string]string) (*RangeSpec, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty range expression")
	}

	// Parse start..end format
	m := rangePattern.FindStringSubmatch(expr)
	if m == nil {
		return nil, fmt.Errorf("invalid range format %q: expected start..end", expr)
	}

	startExpr := strings.TrimSpace(m[1])
	endExpr := strings.TrimSpace(m[2])

	// Evaluate start expression
	start, err := EvaluateExpr(startExpr, vars)
	if err != nil {
		return nil, fmt.Errorf("evaluating range start %q: %w", startExpr, err)
	}

	// Evaluate end expression
	end, err := EvaluateExpr(endExpr, vars)
	if err != nil {
		return nil, fmt.Errorf("evaluating range end %q: %w", endExpr, err)
	}

	return &RangeSpec{Start: start, End: end}, nil
}

// EvaluateExpr evaluates a mathematical expression with variable substitution.
// Supports: + - * / ^ (power) and parentheses.
// Variables use {name} syntax.
func EvaluateExpr(expr string, vars map[string]string) (int, error) {
	// Substitute variables first
	expr = substituteVars(expr, vars)

	// Tokenize and parse
	tokens, err := tokenize(expr)
	if err != nil {
		return 0, err
	}

	result, err := parseExpr(tokens)
	if err != nil {
		return 0, err
	}

	return int(result), nil
}

// substituteVars replaces {varname} with values from vars map.
func substituteVars(expr string, vars map[string]string) string {
	if vars == nil {
		return expr
	}
	return rangeVarPattern.ReplaceAllStringFunc(expr, func(match string) string {
		name := match[1 : len(match)-1] // Remove { and }
		if val, ok := vars[name]; ok {
			return val
		}
		return match // Leave unresolved
	})
}

// Token types for expression parsing.
type tokenType int

const (
	tokNumber tokenType = iota
	tokPlus
	tokMinus
	tokMul
	tokDiv
	tokPow
	tokLParen
	tokRParen
	tokEOF
)

type token struct {
	typ tokenType
	val float64
}

// tokenize converts expression string to tokens.
func tokenize(expr string) ([]token, error) {
	var tokens []token
	i := 0

	for i < len(expr) {
		ch := expr[i]

		// Skip whitespace
		if unicode.IsSpace(rune(ch)) {
			i++
			continue
		}

		// Number
		if unicode.IsDigit(rune(ch)) {
			j := i
			for j < len(expr) && (unicode.IsDigit(rune(expr[j])) || expr[j] == '.') {
				j++
			}
			val, err := strconv.ParseFloat(expr[i:j], 64)
			if err != nil {
				return nil, fmt.Errorf("invalid number %q", expr[i:j])
			}
			tokens = append(tokens, token{tokNumber, val})
			i = j
			continue
		}

		// Operators
		switch ch {
		case '+':
			tokens = append(tokens, token{tokPlus, 0})
		case '-':
			// Could be unary minus or subtraction
			// If previous token is not a number or right paren, it's unary
			if len(tokens) == 0 || (tokens[len(tokens)-1].typ != tokNumber && tokens[len(tokens)-1].typ != tokRParen) {
				// Unary minus: parse the number with the minus
				j := i + 1
				for j < len(expr) && (unicode.IsDigit(rune(expr[j])) || expr[j] == '.') {
					j++
				}
				if j > i+1 {
					val, err := strconv.ParseFloat(expr[i:j], 64)
					if err != nil {
						return nil, fmt.Errorf("invalid number %q", expr[i:j])
					}
					tokens = append(tokens, token{tokNumber, val})
					i = j
					continue
				}
			}
			tokens = append(tokens, token{tokMinus, 0})
		case '*':
			tokens = append(tokens, token{tokMul, 0})
		case '/':
			tokens = append(tokens, token{tokDiv, 0})
		case '^':
			tokens = append(tokens, token{tokPow, 0})
		case '(':
			tokens = append(tokens, token{tokLParen, 0})
		case ')':
			tokens = append(tokens, token{tokRParen, 0})
		default:
			return nil, fmt.Errorf("unexpected character %q in expression", ch)
		}
		i++
	}

	tokens = append(tokens, token{tokEOF, 0})
	return tokens, nil
}

// Parser state
type exprParser struct {
	tokens []token
	pos    int
}

func (p *exprParser) current() token {
	if p.pos >= len(p.tokens) {
		return token{tokEOF, 0}
	}
	return p.tokens[p.pos]
}

func (p *exprParser) advance() {
	p.pos++
}

// parseExpr parses an expression using recursive descent.
// Handles operator precedence: + - < * / < ^
func parseExpr(tokens []token) (float64, error) {
	p := &exprParser{tokens: tokens}
	result, err := p.parseAddSub()
	if err != nil {
		return 0, err
	}
	if p.current().typ != tokEOF {
		return 0, fmt.Errorf("unexpected token after expression")
	}
	return result, nil
}

// parseAddSub handles + and - (lowest precedence)
func (p *exprParser) parseAddSub() (float64, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return 0, err
	}

	for {
		switch p.current().typ {
		case tokPlus:
			p.advance()
			right, err := p.parseMulDiv()
			if err != nil {
				return 0, err
			}
			left += right
		case tokMinus:
			p.advance()
			right, err := p.parseMulDiv()
			if err != nil {
				return 0, err
			}
			left -= right
		default:
			return left, nil
		}
	}
}

// parseMulDiv handles * and /
func (p *exprParser) parseMulDiv() (float64, error) {
	left, err := p.parsePow()
	if err != nil {
		return 0, err
	}

	for {
		switch p.current().typ {
		case tokMul:
			p.advance()
			right, err := p.parsePow()
			if err != nil {
				return 0, err
			}
			left *= right
		case tokDiv:
			p.advance()
			right, err := p.parsePow()
			if err != nil {
				return 0, err
			}
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		default:
			return left, nil
		}
	}
}

// parsePow handles ^ (power, highest binary precedence, right-associative)
func (p *exprParser) parsePow() (float64, error) {
	base, err := p.parseUnary()
	if err != nil {
		return 0, err
	}

	if p.current().typ == tokPow {
		p.advance()
		exp, err := p.parsePow() // Right-associative
		if err != nil {
			return 0, err
		}
		return math.Pow(base, exp), nil
	}

	return base, nil
}

// parseUnary handles unary minus
func (p *exprParser) parseUnary() (float64, error) {
	if p.current().typ == tokMinus {
		p.advance()
		val, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return -val, nil
	}
	return p.parsePrimary()
}

// parsePrimary handles numbers and parentheses
func (p *exprParser) parsePrimary() (float64, error) {
	switch p.current().typ {
	case tokNumber:
		val := p.current().val
		p.advance()
		return val, nil
	case tokLParen:
		p.advance()
		val, err := p.parseAddSub()
		if err != nil {
			return 0, err
		}
		if p.current().typ != tokRParen {
			return 0, fmt.Errorf("expected closing parenthesis")
		}
		p.advance()
		return val, nil
	default:
		return 0, fmt.Errorf("unexpected token in expression")
	}
}

// ValidateRange validates a range expression without evaluating it.
// Useful for syntax checking during formula validation.
func ValidateRange(expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return fmt.Errorf("empty range expression")
	}

	m := rangePattern.FindStringSubmatch(expr)
	if m == nil {
		return fmt.Errorf("invalid range format: expected start..end")
	}

	// Check that expressions parse (with placeholder vars)
	placeholderVars := make(map[string]string)
	rangeVarPattern.ReplaceAllStringFunc(expr, func(match string) string {
		name := match[1 : len(match)-1]
		placeholderVars[name] = "1" // Use 1 as placeholder
		return "1"
	})

	startExpr := strings.TrimSpace(m[1])
	startExpr = substituteVars(startExpr, placeholderVars)
	if _, err := tokenize(startExpr); err != nil {
		return fmt.Errorf("invalid start expression: %w", err)
	}

	endExpr := strings.TrimSpace(m[2])
	endExpr = substituteVars(endExpr, placeholderVars)
	if _, err := tokenize(endExpr); err != nil {
		return fmt.Errorf("invalid end expression: %w", err)
	}

	return nil
}
