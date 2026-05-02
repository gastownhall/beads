package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestSkipLabelsConflicts covers AD-02 Wireframe 5: every label-filter flag
// must report as a conflict, and no other input should.
func TestSkipLabelsConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		labels        []string
		labelsAny     []string
		labelPattern  string
		labelRegex    string
		excludeLabels []string
		noLabels      bool
		want          []string
	}{
		{name: "no conflict — no label filters set", want: nil},
		{name: "label", labels: []string{"needs-pm"}, want: []string{"--label"}},
		{name: "label-any", labelsAny: []string{"frontend"}, want: []string{"--label-any"}},
		{name: "label-pattern", labelPattern: "tech-*", want: []string{"--label-pattern"}},
		{name: "label-regex", labelRegex: "tech-.*", want: []string{"--label-regex"}},
		{name: "exclude-label", excludeLabels: []string{"urgent"}, want: []string{"--exclude-label"}},
		{name: "no-labels (the filter)", noLabels: true, want: []string{"--no-labels"}},
		{
			name:         "multiple at once preserves stable order",
			labels:       []string{"x"},
			labelsAny:    []string{"y"},
			labelPattern: "z*",
			noLabels:     true,
			want:         []string{"--label", "--label-any", "--label-pattern", "--no-labels"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := skipLabelsConflicts(tt.labels, tt.labelsAny, tt.labelPattern, tt.labelRegex, tt.excludeLabels, tt.noLabels)
			if len(got) != len(tt.want) {
				t.Fatalf("len mismatch: got %v, want %v", got, tt.want)
			}
			for i, c := range tt.want {
				if got[i] != c {
					t.Errorf("conflict[%d] = %q, want %q (full got: %v)", i, got[i], c, got)
				}
			}
		})
	}
}

// TestSkipLabelsIssueView_AlwaysEmitsLabelsArray locks in the AD-02 JSON
// contract: with --skip-labels, every issue's labels field is present and
// is an array, regardless of the omitempty tag on the embedded Issue.Labels.
func TestSkipLabelsIssueView_AlwaysEmitsLabelsArray(t *testing.T) {
	t.Parallel()

	view := skipLabelsIssueView{
		IssueWithCounts: &types.IssueWithCounts{
			Issue: &types.Issue{ID: "be-1", Title: "x"},
		},
		Labels: []string{},
	}
	out, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"labels":[]`) {
		t.Errorf("expected explicit labels:[] in JSON, got: %s", got)
	}
	if !strings.Contains(got, `"id":"be-1"`) {
		t.Errorf("expected id pass-through from embedded IssueWithCounts, got: %s", got)
	}
}

// TestFormatSkipLabelsConflictError covers Wireframe 5: the error must echo
// the conflicting flags via "got: --skip-labels <flags>", state the reason,
// and offer two distinct remediation paths.
func TestFormatSkipLabelsConflictError(t *testing.T) {
	t.Parallel()

	msg := formatSkipLabelsConflictError([]string{"--label", "--no-labels"})

	wantSubstrings := []string{
		"--skip-labels cannot be combined with",
		"got: --skip-labels --label --no-labels",
		"reason:",
		"To filter by labels: drop --skip-labels.",
		"To get a label-free result fast: drop --label flags.",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(msg, s) {
			t.Errorf("error message missing %q\nfull message:\n%s", s, msg)
		}
	}
}
