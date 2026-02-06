package query

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestLexer(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []TokenType
		values   []string
	}{
		{
			name:     "simple equality",
			input:    "status=open",
			expected: []TokenType{TokenIdent, TokenEquals, TokenIdent, TokenEOF},
			values:   []string{"status", "=", "open", ""},
		},
		{
			name:     "not equals",
			input:    "status!=closed",
			expected: []TokenType{TokenIdent, TokenNotEquals, TokenIdent, TokenEOF},
			values:   []string{"status", "!=", "closed", ""},
		},
		{
			name:     "greater than",
			input:    "priority>1",
			expected: []TokenType{TokenIdent, TokenGreater, TokenNumber, TokenEOF},
			values:   []string{"priority", ">", "1", ""},
		},
		{
			name:     "less than or equal",
			input:    "priority<=3",
			expected: []TokenType{TokenIdent, TokenLessEq, TokenNumber, TokenEOF},
			values:   []string{"priority", "<=", "3", ""},
		},
		{
			name:     "duration value",
			input:    "updated>7d",
			expected: []TokenType{TokenIdent, TokenGreater, TokenDuration, TokenEOF},
			values:   []string{"updated", ">", "7d", ""},
		},
		{
			name:     "AND expression",
			input:    "status=open AND priority>1",
			expected: []TokenType{TokenIdent, TokenEquals, TokenIdent, TokenAnd, TokenIdent, TokenGreater, TokenNumber, TokenEOF},
			values:   []string{"status", "=", "open", "AND", "priority", ">", "1", ""},
		},
		{
			name:     "OR expression",
			input:    "status=open OR status=blocked",
			expected: []TokenType{TokenIdent, TokenEquals, TokenIdent, TokenOr, TokenIdent, TokenEquals, TokenIdent, TokenEOF},
			values:   []string{"status", "=", "open", "OR", "status", "=", "blocked", ""},
		},
		{
			name:     "NOT expression",
			input:    "NOT status=closed",
			expected: []TokenType{TokenNot, TokenIdent, TokenEquals, TokenIdent, TokenEOF},
			values:   []string{"NOT", "status", "=", "closed", ""},
		},
		{
			name:     "parentheses",
			input:    "(status=open)",
			expected: []TokenType{TokenLParen, TokenIdent, TokenEquals, TokenIdent, TokenRParen, TokenEOF},
			values:   []string{"(", "status", "=", "open", ")", ""},
		},
		{
			name:     "quoted string",
			input:    `title="hello world"`,
			expected: []TokenType{TokenIdent, TokenEquals, TokenString, TokenEOF},
			values:   []string{"title", "=", "hello world", ""},
		},
		{
			name:     "case insensitive keywords",
			input:    "status=open and priority>1 or type=bug",
			expected: []TokenType{TokenIdent, TokenEquals, TokenIdent, TokenAnd, TokenIdent, TokenGreater, TokenNumber, TokenOr, TokenIdent, TokenEquals, TokenIdent, TokenEOF},
		},
		{
			name:     "negative number",
			input:    "priority>-1",
			expected: []TokenType{TokenIdent, TokenGreater, TokenNumber, TokenEOF},
			values:   []string{"priority", ">", "-1", ""},
		},
		{
			name:     "identifier with hyphen",
			input:    "id=bd-abc123",
			expected: []TokenType{TokenIdent, TokenEquals, TokenIdent, TokenEOF},
			values:   []string{"id", "=", "bd-abc123", ""},
		},
		{
			name:     "identifier with underscore",
			input:    "mol_type=swarm",
			expected: []TokenType{TokenIdent, TokenEquals, TokenIdent, TokenEOF},
			values:   []string{"mol_type", "=", "swarm", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lexer := NewLexer(tt.input)
			tokens, err := lexer.Tokenize()
			if err != nil {
				t.Fatalf("Tokenize() error = %v", err)
			}

			if len(tokens) != len(tt.expected) {
				t.Fatalf("got %d tokens, want %d", len(tokens), len(tt.expected))
			}

			for i, tok := range tokens {
				if tok.Type != tt.expected[i] {
					t.Errorf("token %d: got type %v, want %v", i, tok.Type, tt.expected[i])
				}
				if tt.values != nil && tok.Value != tt.values[i] {
					t.Errorf("token %d: got value %q, want %q", i, tok.Value, tt.values[i])
				}
			}
		})
	}
}

func TestLexerErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"unterminated string", `title="hello`},
		{"invalid character", "status@open"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lexer := NewLexer(tt.input)
			_, err := lexer.Tokenize()
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestParser(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple comparison",
			input:    "status=open",
			expected: "status=open",
		},
		{
			name:     "AND expression",
			input:    "status=open AND priority>1",
			expected: "(status=open AND priority>1)",
		},
		{
			name:     "OR expression",
			input:    "status=open OR status=blocked",
			expected: "(status=open OR status=blocked)",
		},
		{
			name:     "NOT expression",
			input:    "NOT status=closed",
			expected: "NOT status=closed",
		},
		{
			name:     "parentheses",
			input:    "(status=open OR status=blocked) AND priority<2",
			expected: "((status=open OR status=blocked) AND priority<2)",
		},
		{
			name:     "chained AND",
			input:    "status=open AND priority>1 AND type=bug",
			expected: "((status=open AND priority>1) AND type=bug)",
		},
		{
			name:     "AND has higher precedence than OR",
			input:    "status=open OR priority>1 AND type=bug",
			expected: "(status=open OR (priority>1 AND type=bug))",
		},
		{
			name:     "NOT with parentheses",
			input:    "NOT (status=closed OR status=deferred)",
			expected: "NOT (status=closed OR status=deferred)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			got := node.String()
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestParserErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty query", ""},
		{"missing value", "status="},
		{"missing operator", "status open"},
		{"unclosed paren", "(status=open"},
		{"extra paren", "status=open)"},
		{"missing operand after AND", "status=open AND"},
		{"invalid operator", "status~open"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestEvaluatorSimpleQueries(t *testing.T) {
	now := time.Date(2025, 2, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name                string
		query               string
		expectFilter        func(*types.IssueFilter) bool
		requiresPredicate   bool
	}{
		{
			name:  "status equals",
			query: "status=open",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.Status != nil && *f.Status == types.StatusOpen
			},
		},
		{
			name:  "status not equals",
			query: "status!=closed",
			expectFilter: func(f *types.IssueFilter) bool {
				return len(f.ExcludeStatus) == 1 && f.ExcludeStatus[0] == types.StatusClosed
			},
		},
		{
			name:  "priority equals",
			query: "priority=2",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.Priority != nil && *f.Priority == 2
			},
		},
		{
			name:  "priority greater than",
			query: "priority>1",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.PriorityMin != nil && *f.PriorityMin == 2
			},
		},
		{
			name:  "priority less than",
			query: "priority<3",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.PriorityMax != nil && *f.PriorityMax == 2
			},
		},
		{
			name:  "type equals",
			query: "type=bug",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.IssueType != nil && *f.IssueType == types.TypeBug
			},
		},
		{
			name:  "assignee equals",
			query: "assignee=alice",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.Assignee != nil && *f.Assignee == "alice"
			},
		},
		{
			name:  "assignee none",
			query: "assignee=none",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.NoAssignee
			},
		},
		{
			name:  "label equals",
			query: "label=urgent",
			expectFilter: func(f *types.IssueFilter) bool {
				return len(f.Labels) == 1 && f.Labels[0] == "urgent"
			},
		},
		{
			name:  "label none",
			query: "label=none",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.NoLabels
			},
		},
		{
			name:  "title contains",
			query: "title=authentication",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.TitleContains == "authentication"
			},
		},
		{
			name:  "description empty",
			query: "description=none",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.EmptyDescription
			},
		},
		{
			name:  "pinned equals true",
			query: "pinned=true",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.Pinned != nil && *f.Pinned == true
			},
		},
		{
			name:  "updated greater than duration",
			query: "updated>7d",
			expectFilter: func(f *types.IssueFilter) bool {
				// 7d ago from now
				expected := now.AddDate(0, 0, -7)
				return f.UpdatedAfter != nil && f.UpdatedAfter.Year() == expected.Year() &&
					f.UpdatedAfter.Month() == expected.Month() && f.UpdatedAfter.Day() == expected.Day()
			},
		},
		{
			name:  "created less than duration",
			query: "created<30d",
			expectFilter: func(f *types.IssueFilter) bool {
				expected := now.AddDate(0, 0, -30)
				return f.CreatedBefore != nil && f.CreatedBefore.Year() == expected.Year() &&
					f.CreatedBefore.Month() == expected.Month() && f.CreatedBefore.Day() == expected.Day()
			},
		},
		{
			name:  "AND expression",
			query: "status=open AND priority>1",
			expectFilter: func(f *types.IssueFilter) bool {
				return f.Status != nil && *f.Status == types.StatusOpen &&
					f.PriorityMin != nil && *f.PriorityMin == 2
			},
		},
		{
			name:  "multiple labels AND",
			query: "label=frontend AND label=urgent",
			expectFilter: func(f *types.IssueFilter) bool {
				return len(f.Labels) == 2 &&
					(f.Labels[0] == "frontend" || f.Labels[0] == "urgent") &&
					(f.Labels[1] == "frontend" || f.Labels[1] == "urgent")
			},
		},
		{
			name:  "NOT status",
			query: "NOT status=closed",
			expectFilter: func(f *types.IssueFilter) bool {
				return len(f.ExcludeStatus) == 1 && f.ExcludeStatus[0] == types.StatusClosed
			},
		},
		{
			name:  "NOT type",
			query: "NOT type=epic",
			expectFilter: func(f *types.IssueFilter) bool {
				return len(f.ExcludeTypes) == 1 && f.ExcludeTypes[0] == types.TypeEpic
			},
		},
		{
			name:  "labels OR uses LabelsAny",
			query: "label=frontend OR label=backend",
			expectFilter: func(f *types.IssueFilter) bool {
				return len(f.LabelsAny) == 2
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateAt(tt.query, now)
			if err != nil {
				t.Fatalf("EvaluateAt() error = %v", err)
			}

			if tt.expectFilter != nil && !tt.expectFilter(&result.Filter) {
				t.Errorf("filter check failed for %q", tt.query)
			}

			if result.RequiresPredicate != tt.requiresPredicate {
				t.Errorf("RequiresPredicate = %v, want %v", result.RequiresPredicate, tt.requiresPredicate)
			}
		})
	}
}

func TestEvaluatorComplexQueries(t *testing.T) {
	now := time.Date(2025, 2, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name              string
		query             string
		requiresPredicate bool
	}{
		{
			name:              "OR with different fields requires predicate",
			query:             "status=open OR priority>1",
			requiresPredicate: true,
		},
		{
			name:              "nested OR requires predicate",
			query:             "(status=open OR status=blocked) AND priority<2",
			requiresPredicate: true,
		},
		{
			name:              "NOT with complex expression requires predicate",
			query:             "NOT (status=closed AND type=bug)",
			requiresPredicate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateAt(tt.query, now)
			if err != nil {
				t.Fatalf("EvaluateAt() error = %v", err)
			}

			if result.RequiresPredicate != tt.requiresPredicate {
				t.Errorf("RequiresPredicate = %v, want %v", result.RequiresPredicate, tt.requiresPredicate)
			}

			if tt.requiresPredicate && result.Predicate == nil {
				t.Error("expected Predicate to be set")
			}
		})
	}
}

func TestPredicateEvaluation(t *testing.T) {
	now := time.Date(2025, 2, 4, 12, 0, 0, 0, time.UTC)

	openBug := &types.Issue{
		ID:       "bd-1",
		Status:   types.StatusOpen,
		Priority: 1,
		IssueType: types.TypeBug,
		Labels:   []string{"urgent", "frontend"},
		CreatedAt: now.AddDate(0, 0, -5),
		UpdatedAt: now.AddDate(0, 0, -1),
	}

	closedTask := &types.Issue{
		ID:       "bd-2",
		Status:   types.StatusClosed,
		Priority: 2,
		IssueType: types.TypeTask,
		Labels:   []string{"backend"},
		CreatedAt: now.AddDate(0, 0, -30),
		UpdatedAt: now.AddDate(0, 0, -10),
	}

	blockedFeature := &types.Issue{
		ID:       "bd-3",
		Status:   types.StatusBlocked,
		Priority: 0,
		IssueType: types.TypeFeature,
		Labels:   []string{},
		CreatedAt: now.AddDate(0, 0, -2),
		UpdatedAt: now,
	}

	tests := []struct {
		name    string
		query   string
		issue   *types.Issue
		matches bool
	}{
		// Status tests
		{"status=open matches open bug", "status=open", openBug, true},
		{"status=open doesn't match closed task", "status=open", closedTask, false},
		{"status!=closed matches open bug", "status!=closed", openBug, true},
		{"status!=closed doesn't match closed task", "status!=closed", closedTask, false},

		// Priority tests
		{"priority>0 matches P1 bug", "priority>0", openBug, true},
		{"priority>0 doesn't match P0 feature", "priority>0", blockedFeature, false},
		{"priority<=1 matches P1 bug", "priority<=1", openBug, true},
		{"priority<=1 doesn't match P2 task", "priority<=1", closedTask, false},

		// Type tests
		{"type=bug matches bug", "type=bug", openBug, true},
		{"type=bug doesn't match task", "type=bug", closedTask, false},

		// Label tests
		{"label=urgent matches issue with urgent", "label=urgent", openBug, true},
		{"label=urgent doesn't match issue without", "label=urgent", closedTask, false},
		{"label=none matches unlabeled", "label=none", blockedFeature, true},
		{"label=none doesn't match labeled", "label=none", openBug, false},

		// OR tests
		{"status=open OR status=blocked matches open", "status=open OR status=blocked", openBug, true},
		{"status=open OR status=blocked matches blocked", "status=open OR status=blocked", blockedFeature, true},
		{"status=open OR status=blocked doesn't match closed", "status=open OR status=blocked", closedTask, false},

		// AND tests
		{"status=open AND type=bug matches", "status=open AND type=bug", openBug, true},
		{"status=open AND type=bug doesn't match blocked", "status=open AND type=bug", blockedFeature, false},

		// NOT tests
		{"NOT status=closed matches open", "NOT status=closed", openBug, true},
		{"NOT status=closed doesn't match closed", "NOT status=closed", closedTask, false},

		// Complex tests
		{"(status=open OR status=blocked) AND priority<2 matches P1 open", "(status=open OR status=blocked) AND priority<2", openBug, true},
		{"(status=open OR status=blocked) AND priority<2 matches P0 blocked", "(status=open OR status=blocked) AND priority<2", blockedFeature, true},
		{"(status=open OR status=blocked) AND priority<2 doesn't match P2 closed", "(status=open OR status=blocked) AND priority<2", closedTask, false},

		// Label OR tests
		{"label=urgent OR label=backend matches urgent", "label=urgent OR label=backend", openBug, true},
		{"label=urgent OR label=backend matches backend", "label=urgent OR label=backend", closedTask, true},
		{"label=urgent OR label=backend doesn't match unlabeled", "label=urgent OR label=backend", blockedFeature, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateAt(tt.query, now)
			if err != nil {
				t.Fatalf("EvaluateAt() error = %v", err)
			}

			// For simple queries, check filter
			if !result.RequiresPredicate {
				// Build predicate anyway for testing
				eval := NewEvaluator(now)
				node, _ := Parse(tt.query)
				pred, err := eval.buildPredicate(node)
				if err != nil {
					t.Fatalf("buildPredicate() error = %v", err)
				}
				got := pred(tt.issue)
				if got != tt.matches {
					t.Errorf("predicate(%s) = %v, want %v", tt.issue.ID, got, tt.matches)
				}
			} else {
				// Use the predicate from result
				got := result.Predicate(tt.issue)
				if got != tt.matches {
					t.Errorf("predicate(%s) = %v, want %v", tt.issue.ID, got, tt.matches)
				}
			}
		})
	}
}

func TestEvaluatorErrors(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"invalid status", "status=invalid"},
		{"invalid priority", "priority=abc"},
		{"priority out of range", "priority=5"},
		{"invalid boolean", "pinned=maybe"},
		{"unknown field", "unknown=value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Evaluate(tt.query)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestDurationParsing(t *testing.T) {
	now := time.Date(2025, 2, 4, 12, 0, 0, 0, time.UTC)
	eval := NewEvaluator(now)

	tests := []struct {
		duration string
		expected time.Time
	}{
		{"7d", now.AddDate(0, 0, -7)},
		{"24h", now.Add(-24 * time.Hour)},
		{"2w", now.AddDate(0, 0, -14)},
		{"1m", now.AddDate(0, -1, 0)},
		{"1y", now.AddDate(-1, 0, 0)},
	}

	for _, tt := range tests {
		t.Run(tt.duration, func(t *testing.T) {
			got, err := eval.parseDurationAgo(tt.duration)
			if err != nil {
				t.Fatalf("parseDurationAgo() error = %v", err)
			}

			// Compare dates (ignore time precision)
			if got.Year() != tt.expected.Year() || got.Month() != tt.expected.Month() || got.Day() != tt.expected.Day() {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}
