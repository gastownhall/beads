//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func TestGetDescendantsCycles(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "dc")
	ctx := t.Context()
	database, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	r := db.NewIssueSQLRepository(database)
	deps := db.NewDependencySQLRepository(database)
	for _, id := range []string{"dc-r", "dc-a", "dc-b", "dc-c", "dc-w", "dc-r.1", "dc-a.1"} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := r.Insert(ctx, issue, "tester", domain.InsertIssueOpts{UseWispsTable: id == "dc-w" || id == "dc-r.1"}); err != nil {
			t.Fatal(err)
		}
	}
	// Bypass write-side validation to exercise a corrupted graph, including
	// a cycle through a wisp, a cycle back to the root, and a durable/wisp
	// cycle (w<->c) below the anchor level, which only path extension stops.
	for _, e := range []struct{ child, parent string }{
		{"dc-a", "dc-r"}, {"dc-b", "dc-a"}, {"dc-a", "dc-b"},
		{"dc-r", "dc-b"}, {"dc-w", "dc-a"}, {"dc-b", "dc-w"},
		{"dc-c", "dc-w"}, {"dc-w", "dc-c"},
	} {
		if err := deps.Insert(ctx, &types.Dependency{IssueID: e.child, DependsOnID: e.parent, Type: types.DepParentChild},
			"tester", domain.DepInsertOpts{UseWispsTable: e.child == "dc-w", CycleValidated: true, HierarchyValidated: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	status := types.StatusOpen
	got, err := te.store.GetDescendants(ctx, "dc-r", types.IssueFilter{Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, issue := range got {
		ids = append(ids, issue.ID)
	}
	sort.Strings(ids)
	want := []string{"dc-a", "dc-a.1", "dc-b", "dc-c", "dc-r.1", "dc-w"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("descendants = %v, want %v (each once, without root)", ids, want)
	}

	// The cap counts the deduplicated subtree, not each level.
	if _, err := te.store.GetDescendants(ctx, "dc-r", types.IssueFilter{Status: &status, MaxRows: len(want)}); err != nil {
		t.Fatalf("cap equal to subtree size: %v", err)
	}
	_, err = te.store.GetDescendants(ctx, "dc-r", types.IssueFilter{Status: &status, MaxRows: len(want) - 1, MaxRowsSource: "--max-rows"})
	var capErr *issueops.ErrTooManyRows
	if !errors.As(err, &capErr) || capErr.Found != len(want) || capErr.Cap != len(want)-1 {
		t.Fatalf("cap below subtree size: err = %v, want ErrTooManyRows{Found: %d, Cap: %d}", err, len(want), len(want)-1)
	}
}
