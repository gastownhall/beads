package format

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestStatusIcon(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"open", "○"},
		{"in_progress", "◐"},
		{"blocked", "●"},
		{"closed", "✓"},
		{"deferred", "❄"},
		{"hooked", "◇"},
		{"pinned", "📌"},
		{"unknown", "○"},
		{"", "○"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := StatusIcon(tt.status); got != tt.want {
				t.Errorf("StatusIcon(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestDependencyInfo(t *testing.T) {
	tests := []struct {
		name      string
		blockedBy []string
		blocks    []string
		parent    string
		want      string
	}{
		{"empty", nil, nil, "", ""},
		{"parent only", nil, nil, "epic-1", "(parent: epic-1)"},
		{"blocked by", []string{"a", "b"}, nil, "", "(blocked by: a, b)"},
		{"blocks", nil, []string{"c"}, "", "(blocks: c)"},
		{"all", []string{"a"}, []string{"b"}, "p", "(parent: p, blocked by: a, blocks: b)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DependencyInfo(tt.blockedBy, tt.blocks, tt.parent); got != tt.want {
				t.Errorf("DependencyInfo() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrettyIssue(t *testing.T) {
	issue := &types.Issue{
		ID:        "gas-abc",
		Title:     "Fix the thing",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: "task",
	}
	got := PrettyIssue(issue)
	if !strings.Contains(got, "gas-abc") {
		t.Errorf("PrettyIssue missing ID: %q", got)
	}
	if !strings.Contains(got, "Fix the thing") {
		t.Errorf("PrettyIssue missing title: %q", got)
	}
}

func TestPrettyIssue_Epic(t *testing.T) {
	issue := &types.Issue{
		ID:        "gas-xyz",
		Title:     "Big project",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: "epic",
	}
	got := PrettyIssue(issue)
	if !strings.Contains(got, "[epic]") {
		t.Errorf("PrettyIssue missing epic badge: %q", got)
	}
}

func TestPrettyIssue_Closed(t *testing.T) {
	issue := &types.Issue{
		ID:        "gas-done",
		Title:     "Completed work",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: "task",
	}
	got := PrettyIssue(issue)
	if !strings.Contains(got, "gas-done") {
		t.Errorf("PrettyIssue closed missing ID: %q", got)
	}
}

func TestListSummary(t *testing.T) {
	got := ListSummary(0, nil)
	if got != "No issues found." {
		t.Errorf("ListSummary(0) = %q", got)
	}

	got = ListSummary(5, map[string]int{"open": 3, "closed": 2})
	if !strings.Contains(got, "Total: 5 issues") {
		t.Errorf("ListSummary(5) = %q", got)
	}
}

func TestLongIssue(t *testing.T) {
	issue := &types.Issue{
		ID:          "gas-abc",
		Title:       "Fix the thing",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   "task",
		Assignee:    "mayor",
		Description: "A longer description",
	}
	got := LongIssue(issue, []string{"gt:task"})
	if !strings.Contains(got, "gas-abc") {
		t.Errorf("LongIssue missing ID: %q", got)
	}
	if !strings.Contains(got, "mayor") {
		t.Errorf("LongIssue missing assignee: %q", got)
	}
	if !strings.Contains(got, "A longer description") {
		t.Errorf("LongIssue missing description: %q", got)
	}
	if !strings.Contains(got, "gt:task") {
		t.Errorf("LongIssue missing labels: %q", got)
	}
}
