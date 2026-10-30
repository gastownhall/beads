package tracker

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestEnginePullFetchWindowOverlapsLastSync pins #6806: an incremental pull
// must re-fetch a window before the stored last_sync, because tracker list
// endpoints lag behind writes. A remote issue whose updated_at is slightly
// older than last_sync must still be pulled on the next sync.
func TestEnginePullFetchWindowOverlapsLastSync(t *testing.T) {
	ctx := context.Background()
	lastSync := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	store := newPureTestStore()
	if err := store.SetLocalMetadata(ctx, "test.last_sync", lastSync.Format(time.RFC3339)); err != nil {
		t.Fatalf("SetLocalMetadata error: %v", err)
	}

	tr := newMockTracker("test")
	late := TrackerIssue{
		ID:         "ext-late",
		Identifier: "EXT-LATE",
		URL:        "https://test.test/EXT-LATE",
		Title:      "written just before the previous sync finished",
		State:      types.StatusOpen,
		Priority:   2,
		UpdatedAt:  lastSync.Add(-30 * time.Second),
	}
	var since *time.Time
	tr.fetchIssues = func(_ context.Context, opts FetchOptions) ([]TrackerIssue, error) {
		since = opts.Since
		// Honour Since the way GitHub's ?since= list does.
		if opts.Since != nil && !late.UpdatedAt.After(*opts.Since) {
			return nil, nil
		}
		return []TrackerIssue{late}, nil
	}

	engine := NewEngine(tr, store, "test-actor")
	result, err := engine.Sync(ctx, SyncOptions{Pull: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync error: %v", err)
	}
	if since == nil {
		t.Fatalf("FetchOptions.Since = nil, want an incremental fetch")
	}
	if !since.Before(lastSync) {
		t.Errorf("FetchOptions.Since = %s, want a window starting before last_sync %s", since.Format(time.RFC3339), lastSync.Format(time.RFC3339))
	}
	if result.PullStats.Created != 1 {
		t.Errorf("PullStats = %+v, want the late remote issue (updated_at %s, 30s before last_sync) pulled", result.PullStats, late.UpdatedAt.Format(time.RFC3339))
	}
	// The overlap widens only the fetch: the stored cursor must stay the
	// exact last_sync, because it is the local-edit threshold the next
	// sync's conflict guard compares against.
	if got := store.localMetadata["test.last_sync"]; got != lastSync.Format(time.RFC3339) {
		t.Errorf("stored last_sync = %q, want it unchanged at %q (dry run)", got, lastSync.Format(time.RFC3339))
	}
}
