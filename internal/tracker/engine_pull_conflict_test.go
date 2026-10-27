package tracker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// GetConfig/GetAllConfig stubs: doPull's selective path reads issue_prefix,
// which pureTestStore would otherwise forward to its nil embedded
// storage.Storage.
func (s *pullFailureStore) GetConfig(context.Context, string) (string, error)       { return "", nil }
func (s *pullFailureStore) GetAllConfig(context.Context) (map[string]string, error) { return nil, nil }

// pullConflictLastSync is the fixture's last_sync. The local bd-1 was edited
// 10 minutes after it, so the pull guard always fires.
var pullConflictLastSync = time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)

// pullConflictFixture is one linked local issue (bd-1, open) edited after
// last_sync, plus a mock tracker whose EXT-1 maps back to bd-1 as
// in_progress: the pull guard sees a locally-modified bead whose external
// version carries a status change.
type pullConflictFixture struct {
	store    *pullFailureStore
	local    *types.Issue
	mock     *mockTracker
	engine   *Engine
	messages []string
}

func newPullConflictFixture(t *testing.T, remoteUpdated time.Time) *pullConflictFixture {
	t.Helper()
	local := &types.Issue{
		ID:          "bd-1",
		Title:       "t",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: strPtr("https://test.test/EXT-1"),
		UpdatedAt:   pullConflictLastSync.Add(10 * time.Minute),
	}
	store := &pullFailureStore{
		pureTestStore: newPureTestStore(local),
		tx:            &pullTestTransaction{issue: local},
	}
	store.localMetadata["test.last_sync"] = pullConflictLastSync.Format(time.RFC3339)
	mock := newMockTracker("test")
	mock.issues = []TrackerIssue{{
		ID: "EXT-1", Identifier: "EXT-1", Title: "t", UpdatedAt: remoteUpdated,
	}}
	mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			ID: "bd-1", Title: "t", Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeTask,
		}}
	}}
	fx := &pullConflictFixture{store: store, local: local, mock: mock, engine: NewEngine(mock, store, "sync")}
	fx.engine.OnMessage = func(msg string) { fx.messages = append(fx.messages, msg) }
	return fx
}

// conflictMessages returns the "Conflict on bd-1:" lines the sync emitted.
func conflictMessages(msgs []string) []string {
	var out []string
	for _, m := range msgs {
		if strings.HasPrefix(m, "Conflict on bd-1:") {
			out = append(out, m)
		}
	}
	return out
}

func TestEnginePullOnlyConflictStrategy(t *testing.T) {
	for _, row := range []struct {
		name          string
		strategy      ConflictResolution
		remoteUpdated time.Time
		wantStatus    types.Status
		wantUpdated   int
		wantSkipped   int
		wantMsg       string
	}{
		{
			name:          "external strategy applies the external version",
			strategy:      ConflictExternal,
			remoteUpdated: pullConflictLastSync.Add(20 * time.Minute),
			wantStatus:    types.StatusInProgress,
			wantUpdated:   1,
			wantSkipped:   0,
			wantMsg:       "keeping external version",
		},
		{
			name:          "external strategy wins even when the local edit is newer",
			strategy:      ConflictExternal,
			remoteUpdated: pullConflictLastSync.Add(5 * time.Minute),
			wantStatus:    types.StatusInProgress,
			wantUpdated:   1,
			wantSkipped:   0,
			wantMsg:       "keeping external version",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			fx := newPullConflictFixture(t, row.remoteUpdated)
			result, err := fx.engine.Sync(context.Background(), SyncOptions{
				Pull:              true,
				ConflictResolution: row.strategy,
			})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if fx.local.Status != row.wantStatus {
				t.Errorf("status = %q, want %q", fx.local.Status, row.wantStatus)
			}
			if result.PullStats.Updated != row.wantUpdated || result.PullStats.Skipped != row.wantSkipped {
				t.Errorf("PullStats.Updated/Skipped = %d/%d, want %d/%d",
					result.PullStats.Updated, result.PullStats.Skipped, row.wantUpdated, row.wantSkipped)
			}
			if result.Stats.Conflicts != 1 {
				t.Errorf("Stats.Conflicts = %d, want 1", result.Stats.Conflicts)
			}
			msgs := conflictMessages(fx.messages)
			if len(msgs) != 1 || !strings.Contains(msgs[0], row.wantMsg) {
				t.Errorf("conflict messages = %q, want one containing %q", msgs, row.wantMsg)
			}
			if fx.mock.fetchCalls != 0 {
				t.Errorf("FetchIssue calls = %d, want 0 (bulk pull must not probe)", fx.mock.fetchCalls)
			}
		})
	}

	t.Run("dry run reports the conflict without applying it", func(t *testing.T) {
		fx := newPullConflictFixture(t, pullConflictLastSync.Add(20*time.Minute))
		result, err := fx.engine.Sync(context.Background(), SyncOptions{
			Pull:              true,
			DryRun:            true,
			ConflictResolution: ConflictExternal,
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		msgs := conflictMessages(fx.messages)
		if len(msgs) != 1 || !strings.Contains(msgs[0], "keeping external version") {
			t.Errorf("conflict messages = %q, want one naming the external version", msgs)
		}
		if result.PullStats.Updated != 1 {
			t.Errorf("PullStats.Updated = %d, want 1", result.PullStats.Updated)
		}
		if fx.store.commits != 0 {
			t.Errorf("commits = %d, want 0", fx.store.commits)
		}
		if fx.local.Status != types.StatusOpen {
			t.Errorf("status = %q, want open (dry run must not write)", fx.local.Status)
		}
		if fx.mock.fetchCalls != 0 {
			t.Errorf("FetchIssue calls = %d, want 0", fx.mock.fetchCalls)
		}
	})
}
