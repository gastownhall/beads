package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestApplyReplacementsWordBoundary verifies that distill's variable
// substitution uses word boundaries to avoid corrupting unrelated text
// that contains the replacement value as a substring.
//
// Regression for: bd mol distill substituting "4" (from --var new_routes=4)
// previously turned "return 404" into "return {{new_routes}}0{{new_routes}}"
// because strings.ReplaceAll has no notion of token boundaries.
func TestApplyReplacementsWordBoundary(t *testing.T) {
	tests := []struct {
		name         string
		issueTitle   string
		replacements map[string]string // map[VALUE]VARNAME — matches subgraphToFormula's caller convention
		want         string
	}{
		{
			name:         "digit_substring_404_not_corrupted",
			issueTitle:   "return 404",
			replacements: map[string]string{"4": "new_routes"},
			want:         "return 404",
		},
		{
			name:         "date_component_not_corrupted",
			issueTitle:   "2026-04-08",
			replacements: map[string]string{"4": "new_routes"},
			want:         "2026-04-08",
		},
		{
			name:         "word_substring_cached_not_corrupted",
			issueTitle:   "cached content",
			replacements: map[string]string{"cache": "domain_name"},
			want:         "cached content",
		},
		{
			name:         "clean_whole_word_still_replaces",
			issueTitle:   "the cache domain",
			replacements: map[string]string{"cache": "domain_name"},
			want:         "the {{domain_name}} domain",
		},
		{
			name:         "clean_whole_number_still_replaces",
			issueTitle:   "process 4 items",
			replacements: map[string]string{"4": "new_routes"},
			want:         "process {{new_routes}} items",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subgraph := &TemplateSubgraph{
				Root: &types.Issue{ID: "root", Title: "Root"},
				Issues: []*types.Issue{
					{ID: "root", Title: "Root"},
					{ID: "step", Title: tt.issueTitle},
				},
			}
			result := subgraphToFormula(subgraph, "test-formula", tt.replacements)
			if result == nil {
				t.Fatal("subgraphToFormula returned nil")
			}
			if len(result.Steps) != 1 {
				t.Fatalf("expected 1 step, got %d", len(result.Steps))
			}
			if got := result.Steps[0].Title; got != tt.want {
				t.Errorf("Steps[0].Title = %q, want %q", got, tt.want)
			}
		})
	}
}
