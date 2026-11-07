package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/types"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]int
	}{
		{
			name:  "simple words",
			input: "fix authentication bug",
			expected: map[string]int{
				"fix":            1,
				"authentication": 1,
				"bug":            1,
			},
		},
		{
			name:  "duplicate words",
			input: "fix the bug fix",
			expected: map[string]int{
				"fix": 2,
				"the": 1,
				"bug": 1,
			},
		},
		{
			name:  "mixed case and punctuation",
			input: "Fix: Authentication BUG!",
			expected: map[string]int{
				"fix":            1,
				"authentication": 1,
				"bug":            1,
			},
		},
		{
			name:  "hyphenated words preserved",
			input: "auto-import feature",
			expected: map[string]int{
				"auto-import": 1,
				"feature":     1,
			},
		},
		{
			name:     "single chars filtered",
			input:    "a b c hello",
			expected: map[string]int{"hello": 1},
		},
		{
			name:     "empty string",
			input:    "",
			expected: map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tokenize(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("tokenize(%q) got %d tokens, want %d: %v", tt.input, len(result), len(tt.expected), result)
				return
			}
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("tokenize(%q)[%q] = %d, want %d", tt.input, k, result[k], v)
				}
			}
		})
	}
}

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     map[string]int
		expected float64
		epsilon  float64
	}{
		{
			name:     "identical sets",
			a:        map[string]int{"fix": 1, "bug": 1},
			b:        map[string]int{"fix": 1, "bug": 1},
			expected: 1.0,
			epsilon:  0.001,
		},
		{
			name:     "disjoint sets",
			a:        map[string]int{"fix": 1, "bug": 1},
			b:        map[string]int{"add": 1, "feature": 1},
			expected: 0.0,
			epsilon:  0.001,
		},
		{
			name:     "partial overlap",
			a:        map[string]int{"fix": 1, "bug": 1, "auth": 1},
			b:        map[string]int{"fix": 1, "auth": 1, "login": 1},
			expected: 0.5, // intersection=2, union=4
			epsilon:  0.001,
		},
		{
			name:     "both empty",
			a:        map[string]int{},
			b:        map[string]int{},
			expected: 0.0,
			epsilon:  0.001,
		},
		{
			name:     "one empty",
			a:        map[string]int{"fix": 1},
			b:        map[string]int{},
			expected: 0.0,
			epsilon:  0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := jaccardSimilarity(tt.a, tt.b)
			diff := result - tt.expected
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.epsilon {
				t.Errorf("jaccardSimilarity() = %f, want %f (diff %f)", result, tt.expected, diff)
			}
		})
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     map[string]int
		expected float64
		epsilon  float64
	}{
		{
			name:     "identical",
			a:        map[string]int{"fix": 1, "bug": 1},
			b:        map[string]int{"fix": 1, "bug": 1},
			expected: 1.0,
			epsilon:  0.001,
		},
		{
			name:     "orthogonal",
			a:        map[string]int{"fix": 1},
			b:        map[string]int{"add": 1},
			expected: 0.0,
			epsilon:  0.001,
		},
		{
			name:     "empty sets",
			a:        map[string]int{},
			b:        map[string]int{},
			expected: 0.0,
			epsilon:  0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cosineSimilarity(tt.a, tt.b)
			diff := result - tt.expected
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.epsilon {
				t.Errorf("cosineSimilarity() = %f, want %f", result, tt.expected)
			}
		})
	}
}

func TestFindMechanicalDuplicates(t *testing.T) {
	issues := []*types.Issue{
		{
			ID:          "bd-001",
			Title:       "Fix authentication bug in login flow",
			Description: "The login page fails when using SSO authentication",
		},
		{
			ID:          "bd-002",
			Title:       "Authentication login bug fix",
			Description: "SSO authentication fails on the login page",
		},
		{
			ID:          "bd-003",
			Title:       "Add dark mode support",
			Description: "Implement dark mode theme for the application",
		},
	}

	pairs := findMechanicalDuplicates(issues, 0.3)

	// Should find bd-001 and bd-002 as similar
	found := false
	for _, p := range pairs {
		if (p.IssueA.ID == "bd-001" && p.IssueB.ID == "bd-002") ||
			(p.IssueA.ID == "bd-002" && p.IssueB.ID == "bd-001") {
			found = true
			if p.Similarity < 0.3 {
				t.Errorf("expected similarity >= 0.3 for auth bug pair, got %f", p.Similarity)
			}
		}
	}
	if !found {
		t.Error("expected to find auth bug pair as similar")
	}

	// bd-003 should NOT match with bd-001 or bd-002
	for _, p := range pairs {
		if p.IssueA.ID == "bd-003" || p.IssueB.ID == "bd-003" {
			t.Errorf("dark mode issue should not match auth issues, got similarity %f", p.Similarity)
		}
	}
}

func TestFindMechanicalDuplicatesHighThreshold(t *testing.T) {
	issues := []*types.Issue{
		{ID: "bd-001", Title: "Fix bug A", Description: "Something completely different"},
		{ID: "bd-002", Title: "Add feature B", Description: "Totally unrelated content"},
	}

	pairs := findMechanicalDuplicates(issues, 0.9)
	if len(pairs) != 0 {
		t.Errorf("expected no pairs at threshold 0.9 for different issues, got %d", len(pairs))
	}
}

func TestFindMechanicalDuplicatesMinIssues(t *testing.T) {
	// Single issue - no pairs possible
	issues := []*types.Issue{
		{ID: "bd-001", Title: "Only issue"},
	}
	pairs := findMechanicalDuplicates(issues, 0.1)
	if len(pairs) != 0 {
		t.Errorf("expected no pairs for single issue, got %d", len(pairs))
	}
}

func TestIssueText(t *testing.T) {
	issue := &types.Issue{
		Title:       "Fix bug",
		Description: "detailed description",
	}
	text := issueText(issue)
	if text != "Fix bug detailed description" {
		t.Errorf("issueText() = %q, want %q", text, "Fix bug detailed description")
	}

	// No description
	issue2 := &types.Issue{Title: "Just title"}
	text2 := issueText(issue2)
	if text2 != "Just title" {
		t.Errorf("issueText() = %q, want %q", text2, "Just title")
	}
}

// TestFindDuplicatesReachesProxiedRouteUnderPositiveMaxRows pins that a
// positive cap from either source no longer stops `bd find-duplicates` before
// its proxied route. The assertion is two-sided: no refusal, AND the run
// reached the proxied route, which with a nil uowProvider fails opening its
// unit of work rather than dialing a server.
func TestFindDuplicatesReachesProxiedRouteUnderPositiveMaxRows(t *testing.T) {
	pinProxiedServerMode(t)
	pinJSONOutput(t, false)

	if uowProvider != nil {
		t.Fatal("precondition: uowProvider must be nil so the route cannot open a real proxied connection")
	}

	for _, source := range []string{"flag", "env"} {
		t.Run(source, func(t *testing.T) {
			cmd := &cobra.Command{Use: "find-duplicates"}
			cmd.Flags().String("method", "mechanical", "")
			cmd.Flags().Float64("threshold", 0.5, "")
			cmd.Flags().String("status", "", "")
			cmd.Flags().Int("limit", 50, "")
			cmd.Flags().String("model", "unused", "")
			addRoutedMaxRowsFlag(cmd)
			if source == "flag" {
				t.Setenv(maxRowsEnvVar, "")
				if err := cmd.Flags().Set(maxRowsFlagName, "5"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv(maxRowsEnvVar, "5")
			}

			var err error
			stderr := captureStderr(t, func() { err = runFindDuplicates(cmd, nil) })

			if strings.Contains(stderr, "not supported in proxied-server mode") {
				t.Fatalf("proxied `bd find-duplicates` refused a cap the proxied route enforces: %q", stderr)
			}
			if !strings.Contains(stderr, "proxied-server UOW provider not initialized") {
				t.Fatalf("proxied `bd find-duplicates` did not reach the proxied route: err=%v stderr=%q", err, stderr)
			}
		})
	}
}
