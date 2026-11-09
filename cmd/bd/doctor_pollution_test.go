package main

import (
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/types"
)

// TestPollutionTitlePredicateKeepsPrefixSemantics pins the title predicate
// bd create (test-issue warning, description requirement) and bd export
// --scrub share with doctor: moving it into doctor.IsTestTitle must not change
// which titles it accepts (GH#5025 tightened only the pollution classifier).
func TestPollutionTitlePredicateKeepsPrefixSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		title string
		want  bool
	}{
		{"test-something", true},
		{"Test-quality architecture", true},
		{"TEST issue", true},
		{"benchmark-perf test", true},
		{"sample_data", true},
		{"tmp-throwaway", true},
		{"temp\tnote", true},
		{"Debug_log", true},
		{"dummy x", true},
		{"testing harness", false},
		{"test", false},
		{" test-leading-space", false},
		{"Fix test-driver.sh", false},
		{"Real feature request", false},
		{"Test Issue 1", true},
		{"issue for testing", false},
	}

	var issues []*types.Issue
	var kept []string
	for _, tt := range tests {
		if got := isTestIssue(tt.title); got != tt.want {
			t.Errorf("isTestIssue(%q) = %v, want %v", tt.title, got, tt.want)
		}
		if got := doctor.IsTestTitle(tt.title); got != tt.want {
			t.Errorf("doctor.IsTestTitle(%q) = %v, want %v", tt.title, got, tt.want)
		}
		issues = append(issues, &types.Issue{ID: tt.title, Title: tt.title})
		if !tt.want {
			kept = append(kept, tt.title)
		}
	}

	var got []string
	for _, issue := range filterOutPollution(issues) {
		got = append(got, issue.Title)
	}
	if len(got) != len(kept) {
		t.Fatalf("filterOutPollution kept %q, want %q", got, kept)
	}
	for i := range kept {
		if got[i] != kept[i] {
			t.Fatalf("filterOutPollution kept %q, want %q", got, kept)
		}
	}
}
