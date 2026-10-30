package tracker

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// overlapLastSync is the stored test.last_sync cursor (T) in the overlap tests.
var overlapLastSync = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

// overlapStore is a pullFailureStore that also answers external_ref history,
// which is what tells a pre-linked stub (no ref at last_sync) apart from an
// ordinarily edited linked issue (same ref at last_sync).
type overlapStore struct {
	*pullFailureStore
	previousRefs map[string]string
}

func (s *overlapStore) PreviousExternalRef(_ context.Context, id string, _ time.Time) (string, bool, error) {
	ref, ok := s.previousRefs[id]
	return ref, ok, nil
}

// newOverlapStore returns a store holding local (when non-nil) with
// test.last_sync set to overlapLastSync.
func newOverlapStore(local *types.Issue) *overlapStore {
	pure := newPureTestStore()
	tx := &pullTestTransaction{issue: &types.Issue{}}
	if local != nil {
		pure = newPureTestStore(local)
		tx.issue = local
	}
	pure.localMetadata["test.last_sync"] = overlapLastSync.Format(time.RFC3339)
	return &overlapStore{
		pullFailureStore: &pullFailureStore{pureTestStore: pure, tx: tx},
		previousRefs:     make(map[string]string),
	}
}

// newOverlapTracker returns a mock tracker serving remote, whose FetchIssues
// honours Since the way GitHub's ?since= list does and records the Since it
// was given.
func newOverlapTracker(remote ...TrackerIssue) (*mockTracker, **time.Time) {
	mock := newMockTracker("test")
	mock.issues = remote
	since := new(*time.Time)
	mock.fetchIssues = func(_ context.Context, opts FetchOptions) ([]TrackerIssue, error) {
		*since = opts.Since
		var out []TrackerIssue
		for _, issue := range mock.issues {
			if opts.Since == nil || !issue.UpdatedAt.Before(*opts.Since) {
				out = append(out, issue)
			}
		}
		return out, nil
	}
	return mock, since
}

// TestEnginePullOverlapImportsIssueMissedBeforeLastSync pins #6806: tracker
// list endpoints lag behind writes, so an incremental pull must re-fetch a
// window before the stored last_sync. A remote issue whose updated_at is
// slightly older than last_sync is still pulled on the next sync.
func TestEnginePullOverlapImportsIssueMissedBeforeLastSync(t *testing.T) {
	ctx := context.Background()
	store := newOverlapStore(nil)
	mock, since := newOverlapTracker(TrackerIssue{
		ID:         "ext-late",
		Identifier: "EXT-LATE",
		Title:      "written just before the previous sync finished",
		State:      types.StatusOpen,
		Priority:   2,
		UpdatedAt:  overlapLastSync.Add(-30 * time.Second),
	})

	result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	want := overlapLastSync.Add(-5 * time.Minute)
	if *since == nil || !(*since).Equal(want) {
		t.Fatalf("FetchOptions.Since = %v, want %s (last_sync minus 5m)", *since, want.Format(time.RFC3339))
	}
	if result.PullStats.Created != 1 {
		t.Fatalf("PullStats = %+v, want the remote issue updated 30s before last_sync created", result.PullStats)
	}
	if got := store.localMetadata["test.last_sync"]; got != overlapLastSync.Format(time.RFC3339) {
		t.Fatalf("stored last_sync = %q, want it unchanged at %s", got, overlapLastSync.Format(time.RFC3339))
	}
}
