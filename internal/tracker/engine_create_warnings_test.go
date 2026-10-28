package tracker

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// warningCreateTracker models an adapter whose create only partly succeeds:
// the remote issue exists (and is fetchable afterwards) but is still open,
// and CreateIssue reports that through TrackerIssue.Warnings, as the GitHub
// adapter does when its post-create close fails (gastownhall/beads#5486).
type warningCreateTracker struct {
	*mockTracker
	warning string
}

func (m *warningCreateTracker) CreateIssue(ctx context.Context, issue *types.Issue) (*TrackerIssue, error) {
	created, err := m.mockTracker.CreateIssue(ctx, issue)
	if err != nil {
		return nil, err
	}
	created.State = "open"
	m.issues = append(m.issues, *created)
	if m.warning != "" {
		created.Warnings = []string{m.warning}
	}
	return created, nil
}

// A create that returns Warnings did not leave the remote matching local
// content, so the engine must not cache its push hash: otherwise the next push
// takes the stored-hash fast path and never repairs the drift.
func TestEnginePushCreateWarningSkipsPushHash(t *testing.T) {
	for _, tc := range []struct {
		name          string
		warning       string
		wantFetches   int
		wantRepairing bool
	}{
		{name: "create with warning is re-checked", warning: "created issue but failed to close it (left open)", wantFetches: 1, wantRepairing: true},
		{name: "clean create stays cached", wantFetches: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			issue := &types.Issue{
				ID: "bd-w1", Title: "Done already",
				Status: types.StatusClosed, IssueType: types.TypeTask, Priority: 2,
			}
			store := newPureTestStore(issue)
			tracker := &warningCreateTracker{mockTracker: newMockTracker("test"), warning: tc.warning}
			engine := NewEngine(tracker, store, "test-actor")
			engine.OnWarning = func(string) {}
			engine.PushHooks = &PushHooks{
				ContentHash: func(local *types.Issue) string { return "h:" + local.Title + ":" + string(local.Status) },
				ContentEqual: func(local *types.Issue, remote *TrackerIssue) bool {
					return local.Title == remote.Title && (local.Status == types.StatusClosed) == (remote.State == "closed")
				},
			}
			hashKey := "test.pushhash." + issue.ID

			result, err := engine.Sync(ctx, SyncOptions{Push: true})
			if err != nil {
				t.Fatalf("first Sync() error: %v", err)
			}
			if result.Stats.Created != 1 {
				t.Errorf("first sync Stats.Created = %d, want 1", result.Stats.Created)
			}
			if issue.ExternalRef == nil || *issue.ExternalRef == "" {
				t.Fatalf("external_ref not stored after create")
			}
			if tc.warning != "" {
				found := false
				for _, w := range result.Warnings {
					if strings.Contains(w, tc.warning) {
						found = true
					}
				}
				if !found {
					t.Errorf("first sync Warnings = %v, want the create warning", result.Warnings)
				}
				if got := store.localMetadata[hashKey]; got != "" {
					t.Errorf("push hash recorded after a create with warnings: %q", got)
				}
			} else if store.localMetadata[hashKey] == "" {
				t.Errorf("push hash not recorded after a clean create")
			}

			if _, err := engine.Sync(ctx, SyncOptions{Push: true}); err != nil {
				t.Fatalf("second Sync() error: %v", err)
			}
			if tracker.fetchCalls != tc.wantFetches {
				t.Errorf("second sync FetchIssue calls = %d, want %d", tracker.fetchCalls, tc.wantFetches)
			}
			_, updated := tracker.updated["EXT-"+issue.ID]
			if updated != tc.wantRepairing {
				t.Errorf("second sync updated EXT-%s = %v, want %v", issue.ID, updated, tc.wantRepairing)
			}
			if store.localMetadata[hashKey] == "" {
				t.Errorf("push hash not recorded after the second sync")
			}
		})
	}
}
