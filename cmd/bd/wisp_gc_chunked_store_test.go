//go:build cgo

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestWispGCAgePersistsBoundedTransactionsOnRealStore runs a forced
// age-based GC of 450 abandoned wisps against a real store (GH#6246): the
// deletions go out as bounded transactions whose committed progress survives
// an injected timeout on a later chunk, and a pinned wisp that depended on a
// deleted one is left consistent — its dependency edge gone, its is_blocked
// cleared — instead of wedged on a dangling edge.
func TestWispGCAgePersistsBoundedTransactionsOnRealStore(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))

	wisps := make([]*types.Issue, 450)
	for i := range wisps {
		wisps[i] = &types.Issue{
			Title:     fmt.Sprintf("abandoned patrol step %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
	}
	if err := s.CreateIssues(ctx, wisps, "test"); err != nil {
		t.Fatalf("creating abandoned wisps: %v", err)
	}

	ephemeral := true
	selection := func() []string {
		t.Helper()
		found, err := s.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &ephemeral, Limit: 5000})
		if err != nil {
			t.Fatalf("listing ephemeral wisps: %v", err)
		}
		ids := make([]string, 0, len(found))
		for _, issue := range found {
			ids = append(ids, issue.ID)
		}
		return ids
	}
	// A pinned live wisp blocked on a selected wisp: age GC protects it both
	// by the pin and by the open dependency, so nothing selects it, and the
	// chunk that deletes its dependency must leave it unblocked.
	live := &types.Issue{Title: "live patrol step", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true, Pinned: true}
	if err := s.CreateIssue(ctx, live, "test"); err != nil {
		t.Fatalf("creating live wisp: %v", err)
	}
	blockedIDs := func() map[string]bool {
		t.Helper()
		blocked, err := s.GetBlockedIssues(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("listing blocked issues: %v", err)
		}
		out := make(map[string]bool, len(blocked))
		for _, b := range blocked {
			out[b.ID] = true
		}
		return out
	}

	// The GC's selection is the ephemeral scan minus protected rows: the
	// 450 abandoned wisps, in scan order, without the live wisp.
	all := selection()
	if len(all) != 451 {
		t.Fatalf("ephemeral selection holds %d wisps, want 451", len(all))
	}
	order := slices.DeleteFunc(slices.Clone(all), func(id string) bool { return id == live.ID })
	if len(order) != 450 {
		t.Fatalf("selection holds %d abandonable wisps beside the live one, want 450", len(order))
	}
	target := order[10]
	if err := s.AddDependency(ctx, &types.Dependency{IssueID: live.ID, DependsOnID: target, Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("adding %s -> %s: %v", live.ID, target, err)
	}
	if !blockedIDs()[live.ID] {
		t.Fatal("the live wisp is not blocked on its dependency before the GC; the fixture does not say what it must")
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

	// --age 0: every wisp counts as abandoned. The request carrying the last
	// selected wisp is refused, as a client read timeout refuses it.
	stdout, stderr, err := captureWispGC(t, func() error {
		return runWispGCAge(ctx, false, 0, false, nil)
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
	if got := len(rec.requests[0]); got != wispPurgeChunkSize {
		t.Errorf("request 1 carries %d ids, want a full %d-id chunk", got, wispPurgeChunkSize)
	}
	obj := decodeSingleJSONObject(t, stdout)
	if got := obj["deleted_count"]; got != float64(400) {
		t.Errorf("first run deleted_count = %v, want 400 (the refused chunk rolls back alone)", got)
	}
	if got := obj["unconfirmed_count"]; got != float64(50) {
		t.Errorf("first run unconfirmed_count = %v, want 50", got)
	}
	for _, id := range order[:400] {
		if _, err := s.GetIssue(ctx, id); err == nil {
			t.Fatalf("%s survived a chunk that committed", id)
		}
	}
	for _, id := range order[400:] {
		if _, err := s.GetIssue(ctx, id); err != nil {
			t.Errorf("the refused chunk's wisp %s is gone, want it still there: %v", id, err)
		}
	}

	// The pinned survivor lost its dependency to a deleted wisp: the edge is
	// gone, so it must not read as blocked on anything.
	if blockedIDs()[live.ID] {
		t.Errorf("the live wisp %s is still blocked after its dependency was deleted; is_blocked was left stale", live.ID)
	}
	if _, err := s.GetIssue(ctx, live.ID); err != nil {
		t.Errorf("live wisp %s did not survive: %v", live.ID, err)
	}
	if !commandDeletedIssueIDs.has(order[10]) || commandDeletedIssueIDs.has(order[449]) {
		t.Errorf("deletion bookkeeping: has(%s)=%v has(%s)=%v, want only committed chunks recorded",
			order[10], commandDeletedIssueIDs.has(order[10]), order[449], commandDeletedIssueIDs.has(order[449]))
	}

	// Rerun without the fault deletes the rest; the pinned survivor stays.
	rec.failOn, rec.requests = "", nil
	stdout, stderr, err = captureWispGC(t, func() error {
		return runWispGCAge(ctx, false, 0, false, nil)
	})
	if err != nil {
		t.Fatalf("rerun: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got := len(rec.requests); got != 1 {
		t.Errorf("rerun sent %d requests, want 1 for the 50 remaining", got)
	}
	if got := len(rec.requests[0]); got != 50 {
		t.Errorf("rerun request carries %d ids, want 50", got)
	}
	obj = decodeSingleJSONObject(t, stdout)
	if got := obj["deleted_count"]; got != float64(50) {
		t.Errorf("rerun deleted_count = %v, want 50", got)
	}
	if left := selection(); !slices.Equal(left, []string{live.ID}) {
		t.Errorf("ephemeral wisps after the rerun = %v, want only the pinned live wisp %s", left, live.ID)
	}
	if _, err := s.GetIssue(ctx, live.ID); err != nil {
		t.Errorf("live wisp %s did not survive the rerun: %v", live.ID, err)
	}
}
