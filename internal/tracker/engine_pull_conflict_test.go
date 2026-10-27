package tracker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// pullConflictStore answers the config read a selective pull makes for
// issue_prefix; the embedded pureTestStore leaves it to a nil storage.Storage.
type pullConflictStore struct {
	*pullFailureStore
}

func (s *pullConflictStore) GetConfig(context.Context, string) (string, error) {
	return "", nil
}

var pullConflictLastSync = time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)

type pullConflictFixture struct {
	local    *types.Issue
	store    *pullConflictStore
	mock     *mockTracker
	engine   *Engine
	messages []string
}

// newPullConflictFixture links local issue bd-1 (status open, edited 10m after
// last_sync) to EXT-1, whose remote UpdatedAt is remoteUpdated and which maps
// to status in_progress.
func newPullConflictFixture(remoteUpdated time.Time) *pullConflictFixture {
	ref := "https://test.test/EXT-1"
	local := &types.Issue{
		ID:          "bd-1",
		Title:       "t",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
		UpdatedAt:   pullConflictLastSync.Add(10 * time.Minute),
	}
	store := &pullConflictStore{pullFailureStore: &pullFailureStore{
		pureTestStore: newPureTestStore(local),
		tx:            &pullTestTransaction{issue: local},
	}}
	store.localMetadata["test.last_sync"] = pullConflictLastSync.Format(time.RFC3339)

	mock := newMockTracker("test")
	mock.issues = []TrackerIssue{{ID: "EXT-1", Identifier: "EXT-1", Title: "t", UpdatedAt: remoteUpdated}}
	mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			ID: "bd-1", Title: "t", Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeTask,
		}}
	}}

	f := &pullConflictFixture{local: local, store: store, mock: mock}
	f.engine = NewEngine(mock, store, "sync")
	f.engine.OnMessage = func(msg string) { f.messages = append(f.messages, msg) }
	return f
}

func (f *pullConflictFixture) conflictMessages() []string {
	var out []string
	for _, msg := range f.messages {
		if strings.HasPrefix(msg, "Conflict on bd-1:") {
			out = append(out, msg)
		}
	}
	return out
}

func TestEnginePullOnlyConflictStrategy(t *testing.T) {
	for _, test := range []struct {
		name       string
		strategy   ConflictResolution
		remote     time.Duration // remote UpdatedAt relative to last_sync; local edit is at +10m
		wantStatus types.Status
		wantMsg    string
	}{
		{"external newer", ConflictExternal, 20 * time.Minute, types.StatusInProgress, "keeping external version"},
		{"external older than local edit", ConflictExternal, 5 * time.Minute, types.StatusInProgress, "keeping external version"},
		{"local", ConflictLocal, 20 * time.Minute, types.StatusOpen, "keeping local version"},
		{"timestamp external newer", ConflictTimestamp, 20 * time.Minute, types.StatusInProgress, "external is newer, importing"},
		{"unset external newer", "", 20 * time.Minute, types.StatusInProgress, "external is newer, importing"},
		{"timestamp local newer", ConflictTimestamp, 5 * time.Minute, types.StatusOpen, "local is newer, keeping local version"},
		{"timestamp tie", ConflictTimestamp, 10 * time.Minute, types.StatusInProgress, "external is newer, importing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPullConflictFixture(pullConflictLastSync.Add(test.remote))

			result, err := f.engine.Sync(context.Background(), SyncOptions{Pull: true, ConflictResolution: test.strategy})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}

			if f.local.Status != test.wantStatus {
				t.Errorf("local status = %q, want %q", f.local.Status, test.wantStatus)
			}
			wantUpdated, wantSkipped := 0, 1
			if test.wantStatus == types.StatusInProgress {
				wantUpdated, wantSkipped = 1, 0
			}
			if result.PullStats.Updated != wantUpdated || result.PullStats.Skipped != wantSkipped {
				t.Errorf("PullStats = %+v, want Updated=%d Skipped=%d", result.PullStats, wantUpdated, wantSkipped)
			}
			if result.PullStats.Conflicts != 1 || result.Stats.Conflicts != 1 {
				t.Errorf("conflicts: PullStats=%d Stats=%d, want 1 and 1", result.PullStats.Conflicts, result.Stats.Conflicts)
			}
			if msgs := f.conflictMessages(); len(msgs) != 1 || !strings.Contains(msgs[0], test.wantMsg) {
				t.Errorf("conflict messages = %q, want one containing %q", msgs, test.wantMsg)
			}
			if f.mock.fetchCalls != 0 {
				t.Errorf("FetchIssue calls = %d, want 0 (pull-only must not run conflict detection)", f.mock.fetchCalls)
			}
		})
	}

	t.Run("dry run external", func(t *testing.T) {
		f := newPullConflictFixture(pullConflictLastSync.Add(20 * time.Minute))

		result, err := f.engine.Sync(context.Background(), SyncOptions{Pull: true, DryRun: true, ConflictResolution: ConflictExternal})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Updated != 1 || f.store.commits != 0 || f.local.Status != types.StatusOpen {
			t.Errorf("dry run: PullStats=%+v commits=%d status=%q, want Updated=1, no commits, status open",
				result.PullStats, f.store.commits, f.local.Status)
		}
		if msgs := f.conflictMessages(); len(msgs) != 1 || !strings.Contains(msgs[0], "keeping external version") {
			t.Errorf("conflict messages = %q, want one keeping external version", msgs)
		}
	})
}

// TestEnginePullOnlySelectiveConflict covers `bd <tracker> pull <id>`, which
// fetches each issue with FetchIssue and passes no conflict strategy.
func TestEnginePullOnlySelectiveConflict(t *testing.T) {
	f := newPullConflictFixture(pullConflictLastSync.Add(20 * time.Minute))

	result, err := f.engine.Sync(context.Background(), SyncOptions{Pull: true, IssueIDs: []string{"EXT-1"}})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if f.local.Status != types.StatusInProgress || result.PullStats.Updated != 1 || result.PullStats.Conflicts != 1 {
		t.Errorf("status=%q PullStats=%+v, want in_progress with Updated=1 Conflicts=1", f.local.Status, result.PullStats)
	}
}

// TestEnginePullOnlyKeepsLocalOnlyEdit: an external issue not changed since
// last_sync is no conflict, even under ConflictExternal. Jira fetches with
// minute granularity, so a pull can return issues updated before last_sync.
func TestEnginePullOnlyKeepsLocalOnlyEdit(t *testing.T) {
	for _, test := range []struct {
		name   string
		remote time.Time
	}{
		{"remote before last_sync", pullConflictLastSync.Add(-30 * time.Second)},
		{"remote at last_sync", pullConflictLastSync},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPullConflictFixture(test.remote)

			result, err := f.engine.Sync(context.Background(), SyncOptions{Pull: true, ConflictResolution: ConflictExternal})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if f.local.Status != types.StatusOpen || result.PullStats.Updated != 0 || result.PullStats.Conflicts != 0 {
				t.Errorf("status=%q PullStats=%+v, want status open, Updated=0, Conflicts=0", f.local.Status, result.PullStats)
			}
			if msgs := f.conflictMessages(); len(msgs) != 0 {
				t.Errorf("conflict messages = %q, want none", msgs)
			}
		})
	}
}

// TestEngineBidirectionalConflictUnchanged: a bidirectional sync still
// resolves conflicts in Phase 1, and doPull counts none of its own.
func TestEngineBidirectionalConflictUnchanged(t *testing.T) {
	f := newPullConflictFixture(pullConflictLastSync.Add(20 * time.Minute))

	result, err := f.engine.Sync(context.Background(), SyncOptions{Pull: true, Push: true, ConflictResolution: ConflictLocal})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if f.local.Status != types.StatusOpen {
		t.Errorf("local status = %q, want open", f.local.Status)
	}
	if len(f.mock.updated) != 1 {
		t.Errorf("tracker update calls = %d, want 1 (local version pushed)", len(f.mock.updated))
	}
	if result.Stats.Conflicts != 1 || result.PullStats.Conflicts != 0 {
		t.Errorf("conflicts: Stats=%d PullStats=%d, want 1 and 0", result.Stats.Conflicts, result.PullStats.Conflicts)
	}
}
