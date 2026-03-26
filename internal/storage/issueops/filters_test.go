package issueops

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestLooksLikeIssueID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		want  bool
	}{
		{name: "standard issue ID", query: "bd-abc123", want: true},
		{name: "dotted child ID", query: "bd-abc123.1", want: true},
		{name: "wisp ID", query: "bd-wisp-xyz", want: true},
		{name: "numeric suffix only", query: "proj-42", want: true},
		{name: "plain word", query: "hello", want: false},
		{name: "has spaces", query: "bd abc", want: false},
		{name: "empty string", query: "", want: false},
		{name: "just a dash", query: "-", want: false},
		{name: "leading dash", query: "-abc", want: false},
		{name: "trailing dash", query: "abc-", want: false},
		{name: "special characters", query: "bd-abc@123", want: false},
		{name: "uppercase letters", query: "BD-ABC123", want: true},
		{name: "single char prefix", query: "a-1", want: true},
		{name: "multiple dashes", query: "a-b-c", want: true},
		{name: "unicode characters", query: "bd-café", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := LooksLikeIssueID(tt.query)
			if got != tt.want {
				t.Errorf("LooksLikeIssueID(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestBuildIssueFilterClauses_EmptyFilter(t *testing.T) {
	t.Parallel()

	clauses, args, err := BuildIssueFilterClauses("", types.IssueFilter{}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clauses) != 0 {
		t.Errorf("expected no clauses for empty filter, got %d: %v", len(clauses), clauses)
	}
	if len(args) != 0 {
		t.Errorf("expected no args for empty filter, got %d: %v", len(args), args)
	}
}

func TestBuildIssueFilterClauses_QueryAsIssueID(t *testing.T) {
	t.Parallel()

	clauses, args, err := BuildIssueFilterClauses("bd-abc123", types.IssueFilter{}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clauses) != 1 {
		t.Fatalf("expected 1 clause, got %d", len(clauses))
	}
	// ID-like query produces 4 args: exact match, prefix, title LIKE, external_ref LIKE
	if len(args) != 4 {
		t.Errorf("expected 4 args for ID-like query, got %d: %v", len(args), args)
	}
}

func TestBuildIssueFilterClauses_QueryAsText(t *testing.T) {
	t.Parallel()

	clauses, args, err := BuildIssueFilterClauses("fix the bug", types.IssueFilter{}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clauses) != 1 {
		t.Fatalf("expected 1 clause, got %d", len(clauses))
	}
	// Text query produces 2 args: title LIKE, id LIKE
	if len(args) != 2 {
		t.Errorf("expected 2 args for text query, got %d: %v", len(args), args)
	}
}

func TestBuildIssueFilterClauses_StatusFilter(t *testing.T) {
	t.Parallel()

	status := types.StatusOpen
	clauses, args, err := BuildIssueFilterClauses("", types.IssueFilter{Status: &status}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clauses) != 1 {
		t.Fatalf("expected 1 clause, got %d", len(clauses))
	}
	if clauses[0] != "status = ?" {
		t.Errorf("unexpected clause: %s", clauses[0])
	}
	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(args))
	}
}

func TestBuildIssueFilterClauses_ExcludeStatus(t *testing.T) {
	t.Parallel()

	filter := types.IssueFilter{
		ExcludeStatus: []types.Status{types.StatusClosed, types.StatusOpen},
	}
	clauses, args, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clauses) != 1 {
		t.Fatalf("expected 1 clause, got %d", len(clauses))
	}
	if len(args) != 2 {
		t.Errorf("expected 2 args for 2 excluded statuses, got %d", len(args))
	}
}

func TestBuildIssueFilterClauses_PriorityRange(t *testing.T) {
	t.Parallel()

	min, max := 1, 3
	filter := types.IssueFilter{PriorityMin: &min, PriorityMax: &max}
	clauses, args, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clauses) != 2 {
		t.Fatalf("expected 2 clauses, got %d", len(clauses))
	}
	if len(args) != 2 {
		t.Errorf("expected 2 args, got %d", len(args))
	}
}

func TestBuildIssueFilterClauses_Labels(t *testing.T) {
	t.Parallel()

	filter := types.IssueFilter{Labels: []string{"bug", "urgent"}}
	clauses, args, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each AND label produces a separate IN subquery clause
	if len(clauses) != 2 {
		t.Fatalf("expected 2 clauses for 2 AND labels, got %d", len(clauses))
	}
	if len(args) != 2 {
		t.Errorf("expected 2 args, got %d", len(args))
	}
}

func TestBuildIssueFilterClauses_LabelsAny(t *testing.T) {
	t.Parallel()

	filter := types.IssueFilter{LabelsAny: []string{"bug", "feature", "docs"}}
	clauses, args, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// OR labels produce a single IN clause
	if len(clauses) != 1 {
		t.Fatalf("expected 1 clause for OR labels, got %d", len(clauses))
	}
	if len(args) != 3 {
		t.Errorf("expected 3 args for 3 OR labels, got %d", len(args))
	}
}

func TestBuildIssueFilterClauses_DateFilters(t *testing.T) {
	t.Parallel()

	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	filter := types.IssueFilter{
		CreatedAfter:  &yesterday,
		CreatedBefore: &now,
	}
	clauses, args, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clauses) != 2 {
		t.Fatalf("expected 2 clauses, got %d", len(clauses))
	}
	if len(args) != 2 {
		t.Errorf("expected 2 args, got %d", len(args))
	}
}

func TestBuildIssueFilterClauses_BooleanFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		filter types.IssueFilter
	}{
		{name: "empty description", filter: types.IssueFilter{EmptyDescription: true}},
		{name: "no assignee", filter: types.IssueFilter{NoAssignee: true}},
		{name: "no labels", filter: types.IssueFilter{NoLabels: true}},
		{name: "no parent", filter: types.IssueFilter{NoParent: true}},
		{name: "deferred", filter: types.IssueFilter{Deferred: true}},
		{name: "overdue", filter: types.IssueFilter{Overdue: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clauses, _, err := BuildIssueFilterClauses("", tt.filter, IssuesFilterTables)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(clauses) == 0 {
				t.Errorf("expected at least 1 clause for %s filter", tt.name)
			}
		})
	}
}

func TestBuildIssueFilterClauses_PinnedFilter(t *testing.T) {
	t.Parallel()

	pinTrue := true
	pinFalse := false

	tests := []struct {
		name     string
		pinned   *bool
		wantSQL  string
	}{
		{name: "pinned=true", pinned: &pinTrue, wantSQL: "pinned = 1"},
		{name: "pinned=false", pinned: &pinFalse, wantSQL: "(pinned = 0 OR pinned IS NULL)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clauses, _, err := BuildIssueFilterClauses("", types.IssueFilter{Pinned: tt.pinned}, IssuesFilterTables)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(clauses) != 1 || clauses[0] != tt.wantSQL {
				t.Errorf("got clause %v, want %q", clauses, tt.wantSQL)
			}
		})
	}
}

func TestBuildIssueFilterClauses_IDFilters(t *testing.T) {
	t.Parallel()

	filter := types.IssueFilter{
		IDs:      []string{"bd-1", "bd-2", "bd-3"},
		IDPrefix: "bd-",
	}
	clauses, args, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clauses) != 2 {
		t.Fatalf("expected 2 clauses (IDs + IDPrefix), got %d", len(clauses))
	}
	// 3 args for IDs IN clause + 1 for IDPrefix LIKE
	if len(args) != 4 {
		t.Errorf("expected 4 args, got %d", len(args))
	}
}

func TestBuildIssueFilterClauses_WispsTables(t *testing.T) {
	t.Parallel()

	// Verify that wisps tables produce different SQL than issues tables
	filter := types.IssueFilter{NoParent: true}

	issuesClauses, _, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wispsClauses, _, err := BuildIssueFilterClauses("", filter, WispsFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(issuesClauses) != 1 || len(wispsClauses) != 1 {
		t.Fatalf("expected 1 clause each, got issues=%d wisps=%d", len(issuesClauses), len(wispsClauses))
	}
	if issuesClauses[0] == wispsClauses[0] {
		t.Error("expected different table names in issues vs wisps clauses")
	}
}

func TestBuildIssueFilterClauses_CombinedFilters(t *testing.T) {
	t.Parallel()

	status := types.StatusOpen
	priority := 2
	now := time.Now()
	filter := types.IssueFilter{
		Status:       &status,
		Priority:     &priority,
		Labels:       []string{"bug"},
		CreatedAfter: &now,
		NoAssignee:   true,
	}
	clauses, args, err := BuildIssueFilterClauses("search term", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// query(1) + status(1) + priority(1) + labels(1) + created_after(1) + no_assignee(1) = 6
	if len(clauses) != 6 {
		t.Errorf("expected 6 clauses for combined filter, got %d: %v", len(clauses), clauses)
	}
	// query text(2) + status(1) + priority(1) + label(1) + created_after(1) = 6
	if len(args) != 6 {
		t.Errorf("expected 6 args, got %d", len(args))
	}
}
