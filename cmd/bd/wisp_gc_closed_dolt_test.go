//go:build cgo

package main

// Real-store regression for the chunked closed-wisp purge (GH#4684): the
// stub tests in wisp_gc_closed_test.go pin the request shape, but the
// property that matters on HQ data is that a chunk's commit SURVIVES a later
// chunk's failure against actual storage. This test drives the direct route
// over a real Dolt server, refuses the final chunk, and checks the first
// two chunks' deletions are what a rerun actually finds.

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// wispGCDoltRefusingStore wraps a real store and refuses, atomically, every
// deletion request containing refuseID — the real-store twin of the stub
// deleter's injected chunk failure. Clearing refuseID lets the same store
// run a clean rerun.
type wispGCDoltRefusingStore struct {
	storage.DoltStorage
	refuseID string
}

func (s *wispGCDoltRefusingStore) Deleter() (issueops.Deleter, error) {
	inner, err := s.DoltStorage.Deleter()
	if err != nil {
		return nil, err
	}
	return &wispGCDoltRefusingDeleter{inner: inner, store: s}, nil
}

type wispGCDoltRefusingDeleter struct {
	inner issueops.Deleter
	store *wispGCDoltRefusingStore
}

func (d *wispGCDoltRefusingDeleter) Delete(ctx context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	if d.store.refuseID != "" && slices.Contains(req.IDs, d.store.refuseID) {
		return issueops.DeleteResult{}, context.DeadlineExceeded
	}
	return d.inner.Delete(ctx, req)
}

// seedClosedWisps creates n closed ephemeral wisps, returning their ids in
// creation order.
func seedClosedWisps(t *testing.T, s storage.DoltStorage, n int) []string {
	t.Helper()
	ctx := t.Context()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("closed wisp %04d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create wisp %d: %v", i, err)
		}
		if err := s.CloseIssue(ctx, issue.ID, "done", "tester", ""); err != nil {
			t.Fatalf("close wisp %s: %v", issue.ID, err)
		}
		ids = append(ids, issue.ID)
	}
	return ids
}

// closedWispSelection returns the ids the purge route's selection scan
// returns, in the same order the route will chunk them.
func closedWispSelection(t *testing.T, s storage.DoltStorage) []string {
	t.Helper()
	closed := types.StatusClosed
	ephemeral := true
	found, err := s.SearchIssues(t.Context(), "", types.IssueFilter{
		Status:    &closed,
		Ephemeral: &ephemeral,
		Limit:     5000,
	})
	if err != nil {
		t.Fatalf("scan closed wisps: %v", err)
	}
	ids := make([]string, len(found))
	for i, issue := range found {
		ids[i] = issue.ID
	}
	return ids
}

// TestWispPurgeClosedPersistsCompletedChunksOnRealStore drives the direct
// route over a real Dolt server with 450 closed wisps and a durable live
// issue depending on the first selected wisp:
//
//   - the final chunk is refused (deadline exceeded); the run exits nonzero
//     reporting 400 confirmed deletions and the 50 unconfirmed ones;
//   - the store actually holds exactly the 50 refused wisps afterwards — the
//     two committed chunks survived the failure;
//   - the durable dependent survived, was orphaned, and is reported;
//   - a clean rerun reselects the remainder and deletes it.
func TestWispPurgeClosedPersistsCompletedChunksOnRealStore(t *testing.T) {
	real := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), ".beads", "beads.db"), "gcr")
	ctx := t.Context()

	seedClosedWisps(t, real, 450)
	selection := closedWispSelection(t, real)
	if len(selection) != 450 {
		t.Fatalf("selection scan found %d closed wisps; want the 450 seeded", len(selection))
	}

	// A durable live issue depending on the first selected wisp: the purge
	// must drop the edge, report the orphan, and leave the issue itself alone.
	durable := &types.Issue{
		Title:     "durable dependent of the first chunk",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := real.CreateIssue(ctx, durable, "tester"); err != nil {
		t.Fatalf("create durable dependent: %v", err)
	}
	if err := real.AddDependency(ctx, &types.Dependency{
		IssueID:     durable.ID,
		DependsOnID: selection[0],
		Type:        types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("add dependent edge: %v", err)
	}

	spy := &wispGCDoltRefusingStore{DoltStorage: real, refuseID: selection[len(selection)-1]}
	ensureCleanGlobalState(t)
	oldStore, oldCtx, oldJSON, oldQuiet, oldProxied, oldActor, oldAuto := store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor, doltAutoCommit
	t.Cleanup(func() {
		store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor, doltAutoCommit = oldStore, oldCtx, oldJSON, oldQuiet, oldProxied, oldActor, oldAuto
		commandDeletedIssueIDs.reset()
	})
	t.Setenv("BEADS_DIR", t.TempDir())
	store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor, doltAutoCommit = spy, ctx, true, false, false, "tester", ""

	t.Run("refused final chunk keeps the committed chunks", func(t *testing.T) {
		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err == nil {
			t.Fatalf("the purge whose final chunk was refused exited nil\nstdout:\n%s", stdout)
		}
		if code, ok := exitCodeFromError(err); !ok || code != 1 {
			t.Errorf("exit code = %v, %v; want nonzero", err, code)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 400 {
			t.Errorf("deleted_count = %d; want 400 confirmed by the two committed chunks", got)
		}
		if got := wispPurgeIntField(t, obj, "remaining_count"); got != 50 {
			t.Errorf("remaining_count = %d; want the 50 refused ids", got)
		}
		if !slices.Contains(wispPurgeIDsField(t, obj, "deleted_ids"), selection[0]) {
			t.Error("the first selected wisp is not among the confirmed deletions")
		}
		if !slices.Contains(wispPurgeIDsField(t, obj, "orphaned_issues"), durable.ID) {
			t.Errorf("orphaned_issues = %v; want the durable dependent %s", obj["orphaned_issues"], durable.ID)
		}

		remaining := closedWispSelection(t, real)
		if len(remaining) != 50 {
			t.Fatalf("the store holds %d closed wisps after the refused run; want exactly the 50 of the refused chunk", len(remaining))
		}
		want := selection[400:]
		if !slices.Equal(remaining, want) {
			t.Errorf("the survivors are not the refused chunk:\n got %v\nwant %v", remaining, want)
		}
		if _, err := real.GetIssue(ctx, selection[0]); err == nil {
			t.Error("the first selected wisp is still in the store; its chunk's commit did not survive the later failure")
		}
		if _, err := real.GetIssue(ctx, durable.ID); err != nil {
			t.Errorf("the durable dependent %s did not survive the purge: %v", durable.ID, err)
		}
		if got, err := real.GetIssue(ctx, durable.ID); err == nil && got.IsBlocked {
			t.Error("the durable dependent is still marked blocked after its blocker was deleted")
		}
	})

	t.Run("clean rerun deletes the remainder", func(t *testing.T) {
		spy.refuseID = ""
		stdout, _, err := runWispPurgeClosedCaptured(t, false, true)
		if err != nil {
			t.Fatalf("the clean rerun failed: %v\nstdout:\n%s", err, stdout)
		}
		obj := decodeWispPurgeJSON(t, stdout)
		if got := wispPurgeIntField(t, obj, "deleted_count"); got != 50 {
			t.Errorf("deleted_count = %d; want the 50 the rerun reselected", got)
		}
		if remaining := closedWispSelection(t, real); len(remaining) != 0 {
			t.Errorf("%d closed wisps remain after the rerun; want 0", len(remaining))
		}
	})
}
