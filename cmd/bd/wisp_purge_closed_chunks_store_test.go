//go:build cgo

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// wispPurgeRecordingStore is a real store whose deletion role records each
// request and, while failOn is set, refuses the request that carries it
// before the request reaches the store.
type wispPurgeRecordingStore struct {
	*dolt.DoltStore
	requests [][]string
	failOn   string
}

func (s *wispPurgeRecordingStore) Deleter() (issueops.Deleter, error) {
	inner, err := s.DoltStore.Deleter()
	if err != nil {
		return nil, err
	}
	return wispPurgeRecordingDeleter{s: s, inner: inner}, nil
}

type wispPurgeRecordingDeleter struct {
	s     *wispPurgeRecordingStore
	inner issueops.Deleter
}

func (d wispPurgeRecordingDeleter) Delete(ctx context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	d.s.requests = append(d.s.requests, slices.Clone(req.IDs))
	if d.s.failOn != "" && slices.Contains(req.IDs, d.s.failOn) {
		return issueops.DeleteResult{}, context.DeadlineExceeded
	}
	return d.inner.Delete(ctx, req)
}

// TestWispPurgeClosedPersistsCompletedChunksOnRealStore runs a forced closed
// purge of 450 wisps against a real store, with the dependents, labels and
// text references of a molecule spanning the 200-id chunk boundaries
// (bd-d2zqqc, GH#4684).
//
// First the request carrying the last selected wisp is refused: the two
// chunks before it must stay deleted (400 gone, 50 left), not roll back with
// it. A selected wisp whose dependency sat in chunk 1 was orphaned by chunk 1
// and deleted by chunk 2, so it must not be in the orphan report; the live
// step depending on a chunk-1 wisp survives and is reported. Then the rerun,
// with nothing refused, finishes the rest: the durable issue depending on a
// chunk-3 wisp survives, is reported orphaned, and its text citing that wisp
// is rewritten, as a plain uncascaded forced delete does.
func TestWispPurgeClosedPersistsCompletedChunksOnRealStore(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))

	closedAt := time.Now()
	wisps := make([]*types.Issue, 450)
	for i := range wisps {
		wisps[i] = &types.Issue{
			Title:     fmt.Sprintf("closed patrol step %d", i),
			Status:    types.StatusClosed,
			ClosedAt:  &closedAt,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
	}
	if err := s.CreateIssues(ctx, wisps, "test"); err != nil {
		t.Fatalf("creating closed wisps: %v", err)
	}

	statusClosed, ephemeral := types.StatusClosed, true
	selection := func() []string {
		t.Helper()
		found, err := s.SearchIssues(ctx, "", types.IssueFilter{Status: &statusClosed, Ephemeral: &ephemeral, Limit: 5000})
		if err != nil {
			t.Fatalf("listing closed wisps: %v", err)
		}
		ids := make([]string, len(found))
		for i, issue := range found {
			ids[i] = issue.ID
		}
		return ids
	}
	order := selection()
	if len(order) != 450 {
		t.Fatalf("selection holds %d closed wisps, want 450", len(order))
	}
	chunk1, chunk2, chunk3 := order[10], order[300], order[420]

	live := &types.Issue{Title: "live patrol step", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := s.CreateIssue(ctx, live, "test"); err != nil {
		t.Fatalf("creating live wisp: %v", err)
	}
	durable := &types.Issue{
		Title:       "durable follow-up",
		Description: "continues the work of " + chunk3,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := s.CreateIssue(ctx, durable, "test"); err != nil {
		t.Fatalf("creating durable issue: %v", err)
	}
	for _, dep := range []*types.Dependency{
		{IssueID: chunk2, DependsOnID: chunk1, Type: types.DepBlocks},
		{IssueID: live.ID, DependsOnID: chunk1, Type: types.DepBlocks},
		{IssueID: durable.ID, DependsOnID: chunk3, Type: types.DepRelated},
	} {
		if err := s.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("adding %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	if err := s.AddLabel(ctx, chunk1, "patrol", "test"); err != nil {
		t.Fatalf("labeling %s: %v", chunk1, err)
	}
	if got := selection(); !slices.Equal(got, order) {
		t.Fatal("adding dependents and labels changed the selection order the chunk positions were chosen from")
	}

	rec := &wispPurgeRecordingStore{DoltStore: s, failOn: order[449]}
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldQuiet, oldProxied, oldActor := rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor
	oldDidWrite := commandDidWrite.Load()
	t.Cleanup(func() {
		rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = oldCtx, oldJSON, oldQuiet, oldProxied, oldActor
		commandDidWrite.Store(oldDidWrite)
		commandDeletedIssueIDs.reset()
	})
	store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = rec, ctx, true, false, false, "test"

	stdout, stderr, err := captureWispGC(t, func() error {
		return runWispPurgeClosed(ctx, false, true, nil)
	})
	if err == nil {
		t.Fatalf("first run succeeded, want the injected failure\nstdout:\n%s", stdout)
	}
	if got, want := len(rec.requests), 3; got != want {
		t.Fatalf("first run sent %d requests, want %d", got, want)
	}
	for i, req := range rec.requests {
		if len(req) > wispPurgeChunkSize {
			t.Errorf("request %d carries %d ids, want at most %d", i, len(req), wispPurgeChunkSize)
		}
	}
	if left := selection(); len(left) != 50 {
		t.Fatalf("%d closed wisps remain after the first run, want the 50 of the refused chunk\nstderr:\n%s", len(left), stderr)
	}
	obj := decodeSingleJSONObject(t, stdout)
	if got := obj["deleted_count"]; got != float64(400) {
		t.Errorf("first run deleted_count = %v, want 400", got)
	}
	if got := obj["labels_removed"]; got != float64(1) {
		t.Errorf("first run labels_removed = %v, want 1", got)
	}
	orphans, _ := obj["orphaned_issues"].([]interface{})
	if slices.Contains(orphans, interface{}(chunk2)) {
		t.Errorf("orphaned_issues %v names %s, which a later chunk of the same run deleted", orphans, chunk2)
	}
	if !slices.Contains(orphans, interface{}(live.ID)) {
		t.Errorf("orphaned_issues %v does not name the live step %s that lost its dependency", orphans, live.ID)
	}
	if _, err := s.GetIssue(ctx, live.ID); err != nil {
		t.Errorf("live step %s did not survive: %v", live.ID, err)
	}
	if !commandDeletedIssueIDs.has(chunk1) || commandDeletedIssueIDs.has(chunk3) {
		t.Errorf("deletion bookkeeping: has(%s)=%v has(%s)=%v, want only committed chunks recorded",
			chunk1, commandDeletedIssueIDs.has(chunk1), chunk3, commandDeletedIssueIDs.has(chunk3))
	}

	rec.failOn, rec.requests = "", nil
	stdout, stderr, err = captureWispGC(t, func() error {
		return runWispPurgeClosed(ctx, false, true, nil)
	})
	if err != nil {
		t.Fatalf("rerun: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if left := selection(); len(left) != 0 {
		t.Fatalf("%d closed wisps remain after the rerun, want 0", len(left))
	}
	obj = decodeSingleJSONObject(t, stdout)
	if got := obj["deleted_count"]; got != float64(50) {
		t.Errorf("rerun deleted_count = %v, want 50", got)
	}
	if got := obj["references_updated"]; got != float64(1) {
		t.Errorf("rerun references_updated = %v, want 1", got)
	}
	orphans, _ = obj["orphaned_issues"].([]interface{})
	if !slices.Equal(orphans, []interface{}{durable.ID}) {
		t.Errorf("rerun orphaned_issues = %v, want [%s]", orphans, durable.ID)
	}
	got, err := s.GetIssue(ctx, durable.ID)
	if err != nil {
		t.Fatalf("durable issue %s did not survive: %v", durable.ID, err)
	}
	if want := "[deleted:" + chunk3 + "]"; !strings.Contains(got.Description, want) {
		t.Errorf("durable description = %q, want its citation rewritten to %s", got.Description, want)
	}
	if _, err := s.GetIssue(ctx, live.ID); err != nil {
		t.Errorf("live step %s did not survive the rerun: %v", live.ID, err)
	}
}
