package flatfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func seedIssues(t *testing.T, s *FlatFileStore) {
	t.Helper()
	ctx := context.Background()
	p0, p1, p2 := 0, 1, 2
	issues := []*types.Issue{
		{ID: "s-1", Title: "Bug in auth", Description: "Login fails", Status: types.StatusOpen, Priority: p0, IssueType: "bug", Assignee: "alice", Labels: []string{"security", "urgent"}},
		{ID: "s-2", Title: "Add dark mode", Description: "User request", Status: types.StatusOpen, Priority: p2, IssueType: "feature", Labels: []string{"ui"}},
		{ID: "s-3", Title: "Refactor DB layer", Status: types.StatusInProgress, Priority: p1, IssueType: "task", Assignee: "bob"},
		{ID: "s-4", Title: "Closed old bug", Status: types.StatusClosed, Priority: p1, IssueType: "bug"},
		{ID: "s-5", Title: "Epic: Q3 roadmap", Status: types.StatusOpen, Priority: p0, IssueType: "epic"},
	}
	for _, issue := range issues {
		if err := s.CreateIssue(ctx, issue, "seeder"); err != nil {
			t.Fatalf("seed CreateIssue(%s): %v", issue.ID, err)
		}
	}
	_ = p0
}

func TestSearchIssuesNoFilter(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	results, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("len = %d, want 5", len(results))
	}
}

func TestSearchIssuesTextQuery(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	results, err := s.SearchIssues(ctx, "auth", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("query 'auth': len = %d, want 1", len(results))
	}
}

func TestSearchIssuesStatusFilter(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	open := types.StatusOpen
	results, err := s.SearchIssues(ctx, "", types.IssueFilter{Status: &open})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("status=open: len = %d, want 3", len(results))
	}
}

func TestSearchIssuesMultiStatus(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	results, err := s.SearchIssues(ctx, "", types.IssueFilter{
		Statuses: []types.Status{types.StatusOpen, types.StatusInProgress},
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("multi-status: len = %d, want 4", len(results))
	}
}

func TestSearchIssuesPriorityFilter(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	p := 0
	results, err := s.SearchIssues(ctx, "", types.IssueFilter{Priority: &p})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("priority=0: len = %d, want 2", len(results))
	}
}

func TestSearchIssuesTypeFilter(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	bug := types.IssueType("bug")
	results, err := s.SearchIssues(ctx, "", types.IssueFilter{IssueType: &bug})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("type=bug: len = %d, want 2", len(results))
	}
}

func TestSearchIssuesLabelFilter(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	results, err := s.SearchIssues(ctx, "", types.IssueFilter{Labels: []string{"security"}})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("label=security: len = %d, want 1", len(results))
	}
}

func TestSearchIssuesAssigneeFilter(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	a := "alice"
	results, err := s.SearchIssues(ctx, "", types.IssueFilter{Assignee: &a})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("assignee=alice: len = %d, want 1", len(results))
	}
}

func TestSearchIssuesNoAssignee(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	results, err := s.SearchIssues(ctx, "", types.IssueFilter{NoAssignee: true})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("no assignee: len = %d, want 3", len(results))
	}
}

func TestSearchIssuesLimit(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	results, err := s.SearchIssues(ctx, "", types.IssueFilter{Limit: 2})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("limit=2: len = %d, want 2", len(results))
	}
}

func TestSearchIssuesExcludeStatus(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	results, err := s.SearchIssues(ctx, "", types.IssueFilter{
		ExcludeStatus: []types.Status{types.StatusClosed},
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("exclude closed: len = %d, want 4", len(results))
	}
}

func TestCountIssues(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	count, err := s.CountIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}
	if count != 5 {
		t.Errorf("count = %d, want 5", count)
	}
}

func TestCountIssuesByGroup(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	counts, err := s.CountIssuesByGroup(ctx, types.IssueFilter{}, "status")
	if err != nil {
		t.Fatalf("CountIssuesByGroup: %v", err)
	}
	if counts["open"] != 3 {
		t.Errorf("open count = %d, want 3", counts["open"])
	}
	if counts["closed"] != 1 {
		t.Errorf("closed count = %d, want 1", counts["closed"])
	}
}

func TestSearchIssuesWithCounts(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	results, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{Limit: 2})
	if err != nil {
		t.Fatalf("SearchIssuesWithCounts: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("len = %d, want 2", len(results))
	}
}

// SQL parity: invalid label regex/glob must surface as an error, not silently
// match everything (nil regex) or nothing (glob ErrBadPattern swallowed).
func TestSearchIssuesInvalidLabelRegexErrors(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	_, err := s.SearchIssues(ctx, "", types.IssueFilter{LabelRegex: "tech-(debt"})
	if err == nil {
		t.Fatal("SearchIssues with unbalanced regex: want error, got nil")
	}
}

func TestSearchIssuesInvalidLabelPatternErrors(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	_, err := s.SearchIssues(ctx, "", types.IssueFilter{LabelPattern: "[a-"})
	if err == nil {
		t.Fatal("SearchIssues with malformed glob: want error, got nil")
	}
}

func TestSearchIssuesValidLabelRegexAndPattern(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	got, err := s.SearchIssues(ctx, "", types.IssueFilter{LabelRegex: "^sec"})
	if err != nil {
		t.Fatalf("SearchIssues LabelRegex: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s-1" {
		t.Errorf("LabelRegex ^sec: got %d issues, want [s-1]", len(got))
	}

	got, err = s.SearchIssues(ctx, "", types.IssueFilter{LabelPattern: "u*"})
	if err != nil {
		t.Fatalf("SearchIssues LabelPattern: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("LabelPattern u*: got %d issues, want 2 (urgent, ui)", len(got))
	}
}

// SQL parity (sqlbuild/filter.go ParentID clause): --parent matches explicit
// parent-child deps AND implicit dotted-ID children that have no parent-child
// dep at all; a dotted-ID issue with a parent-child dep to a DIFFERENT parent
// must not match via the prefix fallback.
func TestSearchIssuesParentIDImplicitDottedChildren(t *testing.T) {
	s := newTestStore(t)

	for _, id := range []string{"p-1", "p-2", "p-1.1", "p-1.2", "p-1.3", "p-10"} {
		if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: id}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	// p-1.1: explicit child of p-1. p-1.2: no deps (implicit child).
	// p-1.3: dotted ID but explicitly parented under p-2 — must NOT match p-1.
	for _, d := range []struct{ child, parent string }{
		{"p-1.1", "p-1"}, {"p-1.3", "p-2"},
	} {
		dep := &types.Dependency{IssueID: d.child, DependsOnID: d.parent, Type: types.DepParentChild}
		if err := s.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency(%s->%s): %v", d.child, d.parent, err)
		}
	}

	parent := "p-1"
	got, err := s.SearchIssues(ctx, "", types.IssueFilter{ParentID: &parent})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	ids := make(map[string]bool, len(got))
	for _, is := range got {
		ids[is.ID] = true
	}
	want := []string{"p-1.1", "p-1.2"}
	if len(got) != len(want) {
		t.Errorf("ParentID=p-1: got %d issues %v, want %v", len(got), ids, want)
	}
	for _, w := range want {
		if !ids[w] {
			t.Errorf("ParentID=p-1: missing %s", w)
		}
	}
	// p-10 shares the "p-1" string prefix but not the "p-1." dotted prefix.
	if ids["p-10"] || ids["p-1.3"] {
		t.Errorf("ParentID=p-1: matched wrong issues: %v", ids)
	}
}

// SQL parity (issueops countGroupForTablesInTx): unknown groupBy — including
// the SQL column name "issue_type" — errors instead of returning a bucket.
func TestCountIssuesByGroupUnsupportedErrors(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	for _, bad := range []string{"prio", "issue_type", ""} {
		if _, err := s.CountIssuesByGroup(ctx, types.IssueFilter{}, bad); err == nil {
			t.Errorf("CountIssuesByGroup(%q): want error, got nil", bad)
		}
	}

	// Errors on an empty store too (SQL errors regardless of row count).
	empty := newTestStore(t)
	if _, err := empty.CountIssuesByGroup(ctx, types.IssueFilter{}, "prio"); err == nil {
		t.Error("CountIssuesByGroup on empty store: want error, got nil")
	}

	// "type" remains the supported spelling.
	counts, err := s.CountIssuesByGroup(ctx, types.IssueFilter{}, "type")
	if err != nil {
		t.Fatalf("CountIssuesByGroup(type): %v", err)
	}
	if counts["bug"] != 2 {
		t.Errorf("type=bug count = %d, want 2", counts["bug"])
	}
}

// LIKE-wildcard characters in text queries and *Contains/IDPrefix filters
// have no special meaning: matching is literal. This is the canonical
// contract; SQL backends conform by LIKE-escaping needles
// (sqlbuild.EscapeLikePattern + ESCAPE clause).
func TestSearchIssuesWildcardNeedlesLiteral(t *testing.T) {
	s := newTestStore(t)
	seed := []*types.Issue{
		{ID: "w-1", Title: "release 10_ branch"},
		{ID: "w-2", Title: "release 105 branch"},
		{ID: "w-3", Title: "100% done"},
		{ID: "w-4", Title: "1005 done"},
	}
	for _, is := range seed {
		if err := s.CreateIssue(ctx, is, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", is.ID, err)
		}
	}

	// Free-text query: "_" is not a single-character wildcard.
	got, err := s.SearchIssues(ctx, "10_", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues(10_): %v", err)
	}
	if len(got) != 1 || got[0].ID != "w-1" {
		t.Errorf("query 10_ matched %d issues, want only w-1", len(got))
	}

	// TitleContains: "%" is not a multi-character wildcard.
	got, err = s.SearchIssues(ctx, "", types.IssueFilter{TitleContains: "100%"})
	if err != nil {
		t.Fatalf("SearchIssues(TitleContains 100%%): %v", err)
	}
	if len(got) != 1 || got[0].ID != "w-3" {
		t.Errorf("TitleContains 100%% matched %d issues, want only w-3", len(got))
	}

	// IDPrefix: literal prefix, no wildcards.
	got, err = s.SearchIssues(ctx, "", types.IssueFilter{IDPrefix: "w_"})
	if err != nil {
		t.Fatalf("SearchIssues(IDPrefix w_): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("IDPrefix w_ matched %d issues, want 0", len(got))
	}
}

// SQL parity (sqlbuild.AppendMetadataClauses): metadata filters compare
// JSON_UNQUOTE(JSON_EXTRACT(...)) text — JSON source text for non-string
// scalars, unquoted contents for strings, MySQL's spaced serialization for
// objects/arrays (go-mysql-server writeMarshalledValue: ", "/": " separators,
// keys ordered by length then bytewise, integral floats without fraction) —
// and invalid keys are an error.
func TestSearchIssuesMetadataJSONTextComparison(t *testing.T) {
	s := newTestStore(t)
	meta := []byte(`{"count": 1.0, "env": "prod", "flag": true, "obj": {"a": 1},` +
		` "arr": [1, 2.5, "x"], "deep": {"bb": {"c": 1.0}, "a": [null, false]}}`)
	if err := s.CreateIssue(ctx, &types.Issue{ID: "m-1", Title: "with meta", Metadata: meta}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.CreateIssue(ctx, &types.Issue{ID: "m-2", Title: "no meta"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	cases := []struct {
		name  string
		key   string
		value string
		want  int
	}{
		{"number JSON text", "count", "1.0", 1},
		{"number fmt %%v text does not match", "count", "1", 0},
		{"string unquoted", "env", "prod", 1},
		{"string quoted does not match", "env", `"prod"`, 0},
		{"bool JSON text", "flag", "true", 1},
		{"object MySQL spaced text", "obj", `{"a": 1}`, 1},
		{"object compact JSON text does not match", "obj", `{"a":1}`, 0},
		{"array MySQL spaced text", "arr", `[1, 2.5, "x"]`, 1},
		{"array compact JSON text does not match", "arr", `[1,2.5,"x"]`, 0},
		{"nested keys length-then-lex, integral float as int", "deep", `{"a": [null, false], "bb": {"c": 1}}`, 1},
		{"nested source key order does not match", "deep", `{"bb": {"c": 1.0}, "a": [null, false]}`, 0},
		{"missing key", "nope", "x", 0},
	}
	for _, tc := range cases {
		got, err := s.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{tc.key: tc.value},
		})
		if err != nil {
			t.Fatalf("%s: SearchIssues: %v", tc.name, err)
		}
		if len(got) != tc.want {
			t.Errorf("%s: got %d issues, want %d", tc.name, len(got), tc.want)
		}
	}
}

func TestSearchIssuesMetadataInvalidKeyErrors(t *testing.T) {
	s := newTestStore(t)
	seedIssues(t, s)

	_, err := s.SearchIssues(ctx, "", types.IssueFilter{
		MetadataFields: map[string]string{"bad key!": "x"},
	})
	if err == nil {
		t.Error("MetadataFields with invalid key: want error, got nil")
	}

	_, err = s.SearchIssues(ctx, "", types.IssueFilter{HasMetadataKey: "1leading-digit"})
	if err == nil {
		t.Error("HasMetadataKey with invalid key: want error, got nil")
	}
}

// Comment-count I/O failures must surface as an error from
// SearchIssuesWithCounts, not silently render as CommentCount=0 (SQL backends
// propagate count-query failures).
func TestSearchIssuesWithCountsCommentDirUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	s := newTestStore(t)
	if err := s.CreateIssue(ctx, &types.Issue{ID: "c-1", Title: "has comment"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if _, err := s.AddIssueComment(ctx, "c-1", "tester", "hello"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}

	// Sanity: count works while readable.
	got, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssuesWithCounts: %v", err)
	}
	if len(got) != 1 || got[0].CommentCount != 1 {
		t.Fatalf("CommentCount = %d, want 1", got[0].CommentCount)
	}

	dir := filepath.Join(s.commentsDir, "c-1")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if _, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{}); err == nil {
		t.Error("SearchIssuesWithCounts with unreadable comments dir: want error, got nil")
	}
}

// SQL parity (sqlbuild.SearchCountsSQL dep_count/rdep_count subqueries, both
// WHERE type = 'blocks'): DependencyCount and DependentCount include only
// 'blocks' edges — related/discovered-from edges must not count. Also pins
// consistency with GetReadyWorkWithCounts on the same store.
func TestSearchIssuesWithCountsBlocksOnly(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"n-1", "n-2", "n-3"} {
		if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "n-1", DependsOnID: "n-2", Type: types.DepRelated},
		{IssueID: "n-1", DependsOnID: "n-3", Type: types.DepDiscoveredFrom},
		{IssueID: "n-2", DependsOnID: "n-1", Type: types.DepBlocks},
	} {
		if err := s.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	got, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssuesWithCounts: %v", err)
	}
	byID := make(map[string]*types.IssueWithCounts, len(got))
	for _, wc := range got {
		byID[wc.ID] = wc
	}
	// n-1: two non-blocks deps count as 0; one incoming blocks edge counts as 1.
	if byID["n-1"].DependencyCount != 0 || byID["n-1"].DependentCount != 1 {
		t.Errorf("n-1 counts = (dep %d, rdep %d), want (0, 1)",
			byID["n-1"].DependencyCount, byID["n-1"].DependentCount)
	}
	// n-2: one outgoing blocks edge; the related edge pointing at it must not count.
	if byID["n-2"].DependencyCount != 1 || byID["n-2"].DependentCount != 0 {
		t.Errorf("n-2 counts = (dep %d, rdep %d), want (1, 0)",
			byID["n-2"].DependencyCount, byID["n-2"].DependentCount)
	}
	// n-3: discovered-from edge pointing at it must not count.
	if byID["n-3"].DependencyCount != 0 || byID["n-3"].DependentCount != 0 {
		t.Errorf("n-3 counts = (dep %d, rdep %d), want (0, 0)",
			byID["n-3"].DependencyCount, byID["n-3"].DependentCount)
	}

	// bd ready --counts and bd list --counts must agree on the same store.
	ready, err := s.GetReadyWorkWithCounts(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWorkWithCounts: %v", err)
	}
	for _, rc := range ready {
		sc, ok := byID[rc.ID]
		if !ok {
			t.Fatalf("ready issue %s missing from search results", rc.ID)
		}
		if rc.DependencyCount != sc.DependencyCount || rc.DependentCount != sc.DependentCount {
			t.Errorf("%s: ready counts (dep %d, rdep %d) != search counts (dep %d, rdep %d)",
				rc.ID, rc.DependencyCount, rc.DependentCount, sc.DependencyCount, sc.DependentCount)
		}
	}
}

// DependentCount must be computed over the whole store, not just the filtered
// result set (guards the single-snapshot refactor of SearchIssuesWithCounts).
func TestSearchIssuesWithCountsDependentsOutsideFilter(t *testing.T) {
	s := newTestStore(t)
	// d-2 (closed) blocks on d-1 (open): filtering to open excludes d-2 from
	// the results but its dependency must still count toward d-1.
	if err := s.CreateIssue(ctx, &types.Issue{ID: "d-1", Title: "dep target", Status: types.StatusOpen}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.CreateIssue(ctx, &types.Issue{ID: "d-2", Title: "dependent", Status: types.StatusClosed}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	dep := &types.Dependency{IssueID: "d-2", DependsOnID: "d-1", Type: "blocks"}
	if err := s.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	open := types.StatusOpen
	got, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{Status: &open})
	if err != nil {
		t.Fatalf("SearchIssuesWithCounts: %v", err)
	}
	if len(got) != 1 || got[0].ID != "d-1" {
		t.Fatalf("got %d issues, want [d-1]", len(got))
	}
	if got[0].DependentCount != 1 {
		t.Errorf("DependentCount = %d, want 1 (dependent excluded by filter must still count)", got[0].DependentCount)
	}
}
