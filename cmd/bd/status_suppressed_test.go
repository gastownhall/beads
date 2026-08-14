package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestSuppressedTypeSummary(t *testing.T) {
	tests := []struct {
		name  string
		stats *types.Statistics
		want  string
	}{
		{
			name:  "nothing suppressed",
			stats: &types.Statistics{TotalIssues: 3},
			want:  "",
		},
		{
			name:  "one gate",
			stats: &types.Statistics{TotalIssues: 2, GateIssues: 1},
			want:  "1 gate (--include-gates)",
		},
		{
			name:  "gates are pluralized",
			stats: &types.Statistics{TotalIssues: 4, GateIssues: 3},
			want:  "3 gates (--include-gates)",
		},
		{
			name:  "gates and templates",
			stats: &types.Statistics{TotalIssues: 5, GateIssues: 1, TemplateIssues: 2},
			want:  "1 gate (--include-gates), 2 templates (--include-templates)",
		},
		{
			name:  "templates only",
			stats: &types.Statistics{TotalIssues: 5, TemplateIssues: 1},
			want:  "1 template (--include-templates)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := suppressedTypeSummary(tt.stats); got != tt.want {
				t.Errorf("suppressedTypeSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The disclosure exists so the operator can account for a total that no listing
// reproduces; it must name the flag that reveals the rows.
func TestSuppressedTypeSummaryNamesTheRevealingFlag(t *testing.T) {
	got := suppressedTypeSummary(&types.Statistics{TotalIssues: 2, GateIssues: 1})
	if !strings.Contains(got, "--include-gates") {
		t.Errorf("suppressedTypeSummary() = %q, want it to name --include-gates", got)
	}
}
