package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// LIKE-wildcard characters in text queries and *Contains/IDPrefix needles must
// match literally (flat-file parity contract): bd search "10_" must not match
// title "105", and --title-contains "100%" must not match "1005". This is the
// always-running SQL-engine check that sqlbuild's needle escaping plus the
// ESCAPE '!' clause behave as intended on a real engine (the Dolt twin,
// TestSearchIssues_WildcardNeedlesMatchLiterally, skips without a server).
func TestSearchIssuesWildcardNeedlesLiteral(t *testing.T) {
	ctx := context.Background()
	st, err := Provision(ctx, filepath.Join(t.TempDir(), "like.db"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetConfig(ctx, "issue_prefix", "si"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	seed := []*types.Issue{
		{ID: "si-w1", Title: "release 10_ branch", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "si-w2", Title: "release 105 branch", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "si-w3", Title: "100% done", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "si-w4", Title: "1005 done", Status: types.StatusOpen, IssueType: types.TypeTask},
	}
	for _, is := range seed {
		if err := st.CreateIssue(ctx, is, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", is.ID, err)
		}
	}

	assertOnly := func(label string, got []*types.Issue, err error, wantIDs ...string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		gotIDs := make(map[string]bool, len(got))
		for _, is := range got {
			gotIDs[is.ID] = true
		}
		if len(got) != len(wantIDs) {
			t.Errorf("%s: got %d issues %v, want %v", label, len(got), gotIDs, wantIDs)
			return
		}
		for _, w := range wantIDs {
			if !gotIDs[w] {
				t.Errorf("%s: missing %s (got %v)", label, w, gotIDs)
			}
		}
	}

	got, err := st.SearchIssues(ctx, "10_", types.IssueFilter{})
	assertOnly("query 10_", got, err, "si-w1")
	got, err = st.SearchIssues(ctx, "", types.IssueFilter{TitleContains: "100%"})
	assertOnly("TitleContains 100%", got, err, "si-w3")
	got, err = st.SearchIssues(ctx, "", types.IssueFilter{IDPrefix: "si_"})
	assertOnly("IDPrefix si_", got, err)
	got, err = st.SearchIssues(ctx, "", types.IssueFilter{IDPrefix: "si-w"})
	assertOnly("IDPrefix si-w", got, err, "si-w1", "si-w2", "si-w3", "si-w4")
}
