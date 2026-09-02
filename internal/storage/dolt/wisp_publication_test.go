package dolt

import (
	"context"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestWispDurableChangesFiltersIgnoredTables(t *testing.T) {
	var changes wispDurableChanges
	changes.Merge(map[string]bool{
		"issues":            true,
		"dependencies":      true,
		"wisps":             true,
		"wisp_dependencies": true,
		"events":            true,
		"bd_events_journal": true,
		"local_metadata":    true,
		"comments":          false,
	})
	if got, want := changes.Tables(), []string{"dependencies", "issues"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("durable change tables = %v, want %v", got, want)
	}
}

func TestDoltStoreDirectWispStatusMutationsPublishDurableDependents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, tc := range []struct {
		name   string
		mutate func(string) error
	}{
		{name: "update", mutate: func(id string) error {
			return store.UpdateIssue(ctx, id, map[string]interface{}{"status": types.StatusClosed}, "tester")
		}},
		{name: "checked-update", mutate: func(id string) error {
			return store.UpdateIssueChecked(ctx, id, map[string]interface{}{"status": types.StatusClosed}, "tester", storage.UpdateIssueOptions{})
		}},
		{name: "close", mutate: func(id string) error {
			return store.CloseIssue(ctx, id, "done", "tester", "")
		}},
		{name: "checked-close", mutate: func(id string) error {
			_, err := store.CloseIssueChecked(ctx, id, "tester", storage.CloseIssueOptions{Reason: "done"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dependerID := "direct-wisp-" + tc.name + "-depender"
			wispID := "direct-wisp-" + tc.name + "-blocker"
			seedDirectWispBlocker(t, ctx, store, dependerID, wispID)
			if err := tc.mutate(wispID); err != nil {
				t.Fatalf("mutate wisp: %v", err)
			}
			assertBlockedWorkingAndHead(ctx, t, store, dependerID, false)
		})
	}
}

func TestDoltStoreDirectWispDependencyMutationsPublishDurableDescendant(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	parent := crossTierWispIssue("direct-wisp-dep-parent", "wisp parent")
	child := crossTierRegularIssue("direct-wisp-dep-child", "durable child")
	blocker := crossTierRegularIssue("direct-wisp-dep-blocker", "durable blocker")
	for _, issue := range []*types.Issue{parent, child, blocker} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("seed durable child -> wisp parent: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, child.ID, false)

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: parent.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency wisp blocker: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, child.ID, true)

	if err := store.RemoveDependency(ctx, parent.ID, blocker.ID, "tester"); err != nil {
		t.Fatalf("RemoveDependency wisp blocker: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, child.ID, false)
}

func TestDoltStoreDirectDurableDependencyMutationsPublishReadiness(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const (
		dependerID = "direct-durable-depender"
		blockerID  = "direct-durable-blocker"
	)
	createPerm(t, ctx, store, dependerID)
	createPerm(t, ctx, store, blockerID)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: dependerID, DependsOnID: blockerID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, dependerID, true)
	if err := store.RemoveDependency(ctx, dependerID, blockerID, "tester"); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, dependerID, false)
}

func TestDoltStoreDirectWispDeletesPublishDurableRows(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, tc := range []struct {
		name   string
		delete func(string) error
	}{
		{name: "single", delete: func(id string) error { return store.DeleteIssue(ctx, id) }},
		{name: "batch", delete: func(id string) error {
			_, err := store.DeleteIssues(ctx, []string{id}, false, true, false)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dependerID := "direct-wisp-delete-" + tc.name + "-depender"
			wispID := "direct-wisp-delete-" + tc.name + "-blocker"
			seedDirectWispBlocker(t, ctx, store, dependerID, wispID)
			if err := tc.delete(wispID); err != nil {
				t.Fatalf("delete wisp: %v", err)
			}
			assertBlockedWorkingAndHead(ctx, t, store, dependerID, false)
			var workingWisp, headEdge int
			if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", wispID).Scan(&workingWisp); err != nil {
				t.Fatalf("read working wisp: %v", err)
			}
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM dependencies AS OF 'HEAD' WHERE issue_id = ? AND depends_on_wisp_id = ?",
				dependerID, wispID,
			).Scan(&headEdge); err != nil {
				t.Fatalf("read durable edge AS OF HEAD: %v", err)
			}
			if workingWisp != 0 || headEdge != 0 {
				t.Fatalf("post-delete rows = wisp:%d HEAD-edge:%d, want 0/0", workingWisp, headEdge)
			}
		})
	}
}

func TestDoltStoreDirectWispCreatePathsPublishOnlyConcreteDurableChanges(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	parent := crossTierRegularIssue("direct-wisp-create-parent", "durable parent")
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}
	child := crossTierWispIssue(parent.ID+".7", "wisp child")
	if err := store.CreateIssue(ctx, child, "tester"); err != nil {
		t.Fatalf("CreateIssue hierarchical wisp: %v", err)
	}
	assertChildCounterWorkingAndHead(t, ctx, store, parent.ID, 7)

	batchChild := crossTierWispIssue(parent.ID+".9", "batch wisp child")
	if err := store.CreateIssues(ctx, []*types.Issue{batchChild}, "tester"); err != nil {
		t.Fatalf("CreateIssues hierarchical wisp: %v", err)
	}
	assertChildCounterWorkingAndHead(t, ctx, store, parent.ID, 9)

	isolated := crossTierWispIssue("direct-wisp-create-isolated", "isolated wisp")
	headBefore, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit before isolated create: %v", err)
	}
	if err := store.CreateIssue(ctx, isolated, "tester"); err != nil {
		t.Fatalf("CreateIssue isolated wisp: %v", err)
	}
	if err := store.UpdateIssue(ctx, isolated.ID, map[string]interface{}{"title": "isolated wisp updated"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue isolated wisp: %v", err)
	}
	headAfter, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit after isolated mutations: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("ignored-only wisp create/update changed HEAD from %s to %s", headBefore, headAfter)
	}
}

// CreateIssues is also the public import/upsert path. An all-wisp batch can
// therefore add an edge to an existing wisp and change a durable descendant's
// derived blocked state even though the issue row itself is Dolt-ignored.
func TestDoltStoreDirectWispBatchUpsertPublishesDurableDescendant(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	parent := crossTierWispIssue("direct-wisp-upsert-parent", "wisp parent")
	child := crossTierRegularIssue("direct-wisp-upsert-child", "durable child")
	blocker := crossTierRegularIssue("direct-wisp-upsert-blocker", "durable blocker")
	for _, issue := range []*types.Issue{parent, child, blocker} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("seed durable child -> wisp parent: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, child.ID, false)

	upsert := crossTierWispIssue(parent.ID, "wisp parent with blocker")
	upsert.Dependencies = []*types.Dependency{{
		IssueID: parent.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}}
	if err := store.CreateIssues(ctx, []*types.Issue{upsert}, "tester"); err != nil {
		t.Fatalf("CreateIssues wisp upsert: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, child.ID, true)
}

func seedDirectWispBlocker(t *testing.T, ctx context.Context, store *DoltStore, dependerID, wispID string) {
	t.Helper()
	createPerm(t, ctx, store, dependerID)
	createWisp(t, ctx, store, wispID)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: dependerID, DependsOnID: wispID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed durable depender -> wisp blocker: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, dependerID, true)
}

func assertChildCounterWorkingAndHead(t *testing.T, ctx context.Context, store *DoltStore, parentID string, want int) {
	t.Helper()
	var working, head int
	if err := store.db.QueryRowContext(ctx, "SELECT last_child FROM child_counters WHERE parent_id = ?", parentID).Scan(&working); err != nil {
		t.Fatalf("read child counter working set: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT last_child FROM child_counters AS OF 'HEAD' WHERE parent_id = ?", parentID).Scan(&head); err != nil {
		t.Fatalf("read child counter AS OF HEAD: %v", err)
	}
	if working != want || head != want {
		t.Fatalf("child counter = working:%d HEAD:%d, want %d/%d", working, head, want, want)
	}
}
