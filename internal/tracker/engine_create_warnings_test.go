// Pure-Go tests for the push loop's handling of partial-success creates
// (TrackerIssue.Warnings). This file MUST NOT carry a `//go:build cgo` tag so
// it runs in the --config=ci lane; engine_test.go is Dolt-backed and skipped
// there.
package tracker

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// warningCreateTracker wraps mockTracker so every successful CreateIssue
// returns a partial-success Warning, like the GitLab and (post-#5486) GitHub
// trackers do when a follow-up state change fails.
type warningCreateTracker struct {
	*mockTracker
	warning string
}

func (m *warningCreateTracker) CreateIssue(ctx context.Context, issue *types.Issue) (*TrackerIssue, error) {
	ti, err := m.mockTracker.CreateIssue(ctx, issue)
	if err != nil {
		return nil, err
	}
	ti.Warnings = []string{m.warning}
	return ti, nil
}

// TestEnginePushCreateWarningSkipsPushHash covers the partial-success create
// path of gastownhall/beads#5486: a create whose TrackerIssue carries
// Warnings means the remote does not match local content (e.g. the bead is
// closed but the created issue is open), so the engine must NOT record a
// push hash for it. Recording one would make every later push short-circuit
// on the stored-hash fast path and silently skip the needed update.
func TestEnginePushCreateWarningSkipsPushHash(t *testing.T) {
	ctx := context.Background()

	pushHooks := func() *PushHooks {
		return &PushHooks{
			ContentHash: func(local *types.Issue) string { return "h:" + local.Title },
			ContentEqual: func(local *types.Issue, remote *TrackerIssue) bool {
				return local.Title == remote.Title
			},
		}
	}

	t.Run("warning create is not cached and is updated next push", func(t *testing.T) {
		issue := &types.Issue{
			ID: "bd-warn", Title: "Warned create", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 2,
		}
		store := newPureTestStore(issue)
		tr := &warningCreateTracker{
			mockTracker: newMockTracker("test"),
			warning:     "created the issue but failed to close it (left open)",
		}
		engine := NewEngine(tr, store, "test-actor")
		engine.PushHooks = pushHooks()

		// Sync 1: the create succeeds with a warning. The external_ref is
		// stored and the warning surfaced, but no push hash may be cached.
		result, err := engine.Sync(ctx, SyncOptions{Push: true})
		if err != nil {
			t.Fatalf("Sync #1: %v", err)
		}
		if result.Stats.Created != 1 {
			t.Errorf("Sync #1: Stats.Created = %d, want 1", result.Stats.Created)
		}
		found := false
		for _, w := range result.Warnings {
			if strings.Contains(w, "failed to close it") && strings.Contains(w, "bd-warn") {
				found = true
			}
		}
		if !found {
			t.Errorf("Sync #1: expected the create warning in result.Warnings, got %v", result.Warnings)
		}
		if issue.ExternalRef == nil || *issue.ExternalRef == "" {
			t.Fatalf("Sync #1: external_ref not stored on the bead")
		}
		if stored := store.localMetadata["test.pushhash.bd-warn"]; stored != "" {
			t.Fatalf("Sync #1: push hash recorded for a warned create (%q), want none so the next push re-fetches", stored)
		}

		// Sync 2: the fast path must miss (no stored hash), so the issue is
		// fetched and updated, and the hash is recorded only after that.
		result, err = engine.Sync(ctx, SyncOptions{Push: true})
		if err != nil {
			t.Fatalf("Sync #2: %v", err)
		}
		if tr.fetchCalls != 1 {
			t.Errorf("Sync #2: fetchCalls = %d, want 1 (the warned create must be fetched, not skipped)", tr.fetchCalls)
		}
		if updated := tr.updated["EXT-bd-warn"]; updated == nil {
			t.Errorf("Sync #2: expected an update for EXT-bd-warn, got updated=%v", tr.updated)
		}
		if result.Stats.Updated != 1 {
			t.Errorf("Sync #2: Stats.Updated = %d, want 1", result.Stats.Updated)
		}
		if stored := store.localMetadata["test.pushhash.bd-warn"]; stored == "" {
			t.Error("Sync #2: push hash should be recorded after a clean update")
		}
	})

	t.Run("clean create is still cached", func(t *testing.T) {
		// Control: without warnings the create-time cache stays intact, so
		// a no-op second push does one less fetch (gastownhall/beads#4214).
		issue := &types.Issue{
			ID: "bd-clean", Title: "Clean create", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 2,
		}
		store := newPureTestStore(issue)
		tr := newMockTracker("test")
		engine := NewEngine(tr, store, "test-actor")
		engine.PushHooks = pushHooks()

		if _, err := engine.Sync(ctx, SyncOptions{Push: true}); err != nil {
			t.Fatalf("Sync #1: %v", err)
		}
		if stored := store.localMetadata["test.pushhash.bd-clean"]; stored == "" {
			t.Fatal("Sync #1: push hash missing after a clean create")
		}

		result, err := engine.Sync(ctx, SyncOptions{Push: true})
		if err != nil {
			t.Fatalf("Sync #2: %v", err)
		}
		if tr.fetchCalls != 0 {
			t.Errorf("Sync #2: fetchCalls = %d, want 0 (unchanged issue, stored hash matches)", tr.fetchCalls)
		}
		if result.Stats.Skipped != 1 {
			t.Errorf("Sync #2: Stats.Skipped = %d, want 1", result.Stats.Skipped)
		}
	})
}
