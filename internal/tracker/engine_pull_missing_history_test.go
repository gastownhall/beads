package tracker

// Regression tests for #7294 (bd-nnkvz2): a history-capable store that holds
// no row for a linked issue at last_sync (compaction or flatten removed the
// baseline) must not make the pull hydrate that issue, because hydration
// exempts it from the conflict and overlap guards that keep unpushed local
// edits.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// missingHistoryStore is a pure store holding several issues. Its
// transactions route each write to the issue it names and count it, and its
// PreviousExternalRef answers from previousRefs: an ID absent from it has no
// history row at the asked time (found=false), as after a compaction whose
// squashed base postdates last_sync.
type missingHistoryStore struct {
	*pureTestStore
	previousRefs map[string]string
	updates      map[string]int
}

func newMissingHistoryStore(issues ...*types.Issue) *missingHistoryStore {
	pure := newPureTestStore(issues...)
	pure.localMetadata["test.last_sync"] = overlapLastSync.Format(time.RFC3339)
	return &missingHistoryStore{
		pureTestStore: pure,
		previousRefs:  make(map[string]string),
		updates:       make(map[string]int),
	}
}

func (s *missingHistoryStore) PreviousExternalRef(_ context.Context, id string, _ time.Time) (string, bool, error) {
	ref, ok := s.previousRefs[id]
	return ref, ok, nil
}

func (s *missingHistoryStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, issue := range s.issues {
		if issue.ExternalRef != nil && *issue.ExternalRef == ref {
			return issue, nil
		}
	}
	return nil, nil
}

func (s *missingHistoryStore) RunInTransaction(_ context.Context, _ string, fn func(storage.Transaction) error) error {
	return fn(&missingHistoryTx{store: s})
}

func (s *missingHistoryStore) RunInIssueLifecycleTransaction(_ context.Context, _ string, fn func(storage.IssueLifecycleTransaction) error) error {
	return fn(&missingHistoryTx{store: s})
}

// missingHistoryTx routes each call to a pullTestTransaction over the issue
// it names.
type missingHistoryTx struct {
	storage.Transaction
	store *missingHistoryStore
}

func (t *missingHistoryTx) on(id string) *pullTestTransaction {
	for _, issue := range t.store.issues {
		if issue.ID == id {
			return &pullTestTransaction{issue: issue}
		}
	}
	return &pullTestTransaction{issue: &types.Issue{}}
}

func (t *missingHistoryTx) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	t.store.updates[id]++
	return t.on(id).UpdateIssue(ctx, id, updates, actor)
}

func (t *missingHistoryTx) ReopenIssueWithResult(ctx context.Context, id, reason, actor string) (bool, error) {
	t.store.updates[id]++
	return t.on(id).ReopenIssueWithResult(ctx, id, reason, actor)
}

func (t *missingHistoryTx) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return t.on(id).GetIssue(ctx, id)
}

func (t *missingHistoryTx) GetLabels(ctx context.Context, id string) ([]string, error) {
	return t.on(id).GetLabels(ctx, id)
}

func (t *missingHistoryTx) AddLabel(ctx context.Context, id, label, actor string) error {
	t.store.updates[id]++
	return t.on(id).AddLabel(ctx, id, label, actor)
}

func (t *missingHistoryTx) RemoveLabel(ctx context.Context, id, label, actor string) error {
	t.store.updates[id]++
	return t.on(id).RemoveLabel(ctx, id, label, actor)
}

// linkedIssue returns a local issue linked to the test tracker's EXT-<n>.
func linkedIssue(id, ext, title string, created, updated time.Time) *types.Issue {
	ref := "https://test.test/" + ext
	return &types.Issue{
		ID:          id,
		Title:       title,
		Description: title + " body",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
		CreatedAt:   created,
		UpdatedAt:   updated,
	}
}

// staleRemote returns the remote counterpart of a linked issue, holding
// content the local issue no longer has.
func staleRemote(ext string, updated time.Time) TrackerIssue {
	return TrackerIssue{
		ID:          ext,
		Identifier:  ext,
		Title:       "stale remote title " + ext,
		Description: "stale remote body",
		UpdatedAt:   updated,
	}
}

// snapshotIssues copies every issue so a test can assert none changed.
func snapshotIssues(issues ...*types.Issue) []types.Issue {
	out := make([]types.Issue, len(issues))
	for i, issue := range issues {
		out[i] = *issue
		if issue.ExternalRef != nil {
			ref := *issue.ExternalRef
			out[i].ExternalRef = &ref
		}
	}
	return out
}

func assertIssuesUnchanged(t *testing.T, before []types.Issue, issues ...*types.Issue) {
	t.Helper()
	for i, issue := range issues {
		if !reflect.DeepEqual(*issue, before[i]) {
			t.Errorf("issue %s changed by the pull:\n got  %+v\n want %+v", issue.ID, *issue, before[i])
		}
	}
}

// TestEnginePullMissingHistoryPreservesLocalEdits pins AC-1 of #7294: older
// linked issues with no history row at last_sync, one edited locally after
// it, are neither fetched for hydration nor written, in a dry run or a real
// pull, when the incremental bulk fetch returns no remote change.
func TestEnginePullMissingHistoryPreservesLocalEdits(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		name := "real"
		if dryRun {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			edited := linkedIssue("bd-edited", "EXT-1", "local edit not yet pushed",
				overlapLastSync.Add(-24*time.Hour), overlapLastSync.Add(10*time.Minute))
			unchanged := linkedIssue("bd-unchanged", "EXT-2", "local unchanged",
				overlapLastSync.Add(-24*time.Hour), overlapLastSync.Add(-time.Hour))
			store := newMissingHistoryStore(edited, unchanged)
			before := snapshotIssues(edited, unchanged)
			// Both remotes were last updated well before the overlap window,
			// so the incremental bulk fetch returns neither.
			mock, _ := newOverlapTracker(
				staleRemote("EXT-1", overlapLastSync.Add(-2*time.Hour)),
				staleRemote("EXT-2", overlapLastSync.Add(-2*time.Hour)),
			)

			result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: dryRun})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if mock.fetchCalls != 0 {
				t.Errorf("FetchIssue calls = %d, want 0: missing history must not hydrate older issues", mock.fetchCalls)
			}
			if result.PullStats.Candidates != 0 || result.PullStats.Updated != 0 || result.PullStats.Created != 0 {
				t.Errorf("PullStats = %+v, want no candidates, updates or creates", result.PullStats)
			}
			if len(store.updates) != 0 {
				t.Errorf("transaction writes = %v, want none", store.updates)
			}
			assertIssuesUnchanged(t, before, edited, unchanged)
		})
	}
}

// TestEnginePullMissingHistoryBulkRowsKeepGuards pins AC-2 of #7294: when the
// incremental bulk fetch returns the stale remote of an older issue with
// missing history and unpushed local edits, as an overlap-only row or as a
// row updated after last_sync, missing history grants no hydration exemption
// and the conflict and overlap guards keep the local edits. Conflict
// resolution's explicit overwrite authorization still applies.
func TestEnginePullMissingHistoryBulkRowsKeepGuards(t *testing.T) {
	for _, row := range []struct {
		name    string
		updated time.Time
	}{
		{name: "overlap-only row", updated: overlapLastSync.Add(-30 * time.Second)},
		{name: "row updated after last_sync", updated: overlapLastSync.Add(5 * time.Minute)},
	} {
		for _, dryRun := range []bool{true, false} {
			name := row.name + "/real"
			if dryRun {
				name = row.name + "/dry-run"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				edited := linkedIssue("bd-edited", "EXT-1", "local edit not yet pushed",
					overlapLastSync.Add(-24*time.Hour), overlapLastSync.Add(10*time.Minute))
				store := newMissingHistoryStore(edited)
				before := snapshotIssues(edited)
				mock, _ := newOverlapTracker(staleRemote("EXT-1", row.updated))

				result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: dryRun})
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
				if mock.fetchCalls != 0 {
					t.Errorf("FetchIssue calls = %d, want 0", mock.fetchCalls)
				}
				if result.PullStats.Candidates != 1 || result.PullStats.Updated != 0 || result.PullStats.Skipped != 1 {
					t.Errorf("PullStats = %+v, want the bulk row a candidate the guards skip", result.PullStats)
				}
				if len(store.updates) != 0 {
					t.Errorf("transaction writes = %v, want none", store.updates)
				}
				assertIssuesUnchanged(t, before, edited)
			})
		}
	}

	// Control: conflict resolution that picks the external side still
	// authorizes the overwrite, history or not.
	t.Run("external conflict resolution still overwrites", func(t *testing.T) {
		ctx := context.Background()
		edited := linkedIssue("bd-edited", "EXT-1", "local edit not yet pushed",
			overlapLastSync.Add(-24*time.Hour), overlapLastSync.Add(10*time.Minute))
		store := newMissingHistoryStore(edited)
		mock, _ := newOverlapTracker(staleRemote("EXT-1", overlapLastSync.Add(5*time.Minute)))

		result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{
			Pull: true, Push: true, ConflictResolution: ConflictExternal,
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.Stats.Conflicts != 1 || result.PullStats.Updated != 1 || edited.Title != "stale remote title EXT-1" {
			t.Fatalf("Stats = %+v, PullStats = %+v, title = %q, want the external side applied by conflict resolution",
				result.Stats, result.PullStats, edited.Title)
		}
	})
}

// TestEnginePullMissingHistoryHydratesNewStub pins AC-3 of #7294: a pre-linked
// stub created after last_sync with no history row, and an older stub whose
// historical ref was NULL or another issue's, keep their hydration, through a
// single FetchIssue or by reusing an overlap-only bulk row, without the
// missing-history warning.
func TestEnginePullMissingHistoryHydratesNewStub(t *testing.T) {
	for _, stub := range []struct {
		name    string
		created time.Time
		// history is the stub's historical ref at last_sync; nil means no row.
		history *string
	}{
		{name: "new stub without history", created: overlapLastSync.Add(time.Minute)},
		{name: "older stub with NULL historical ref", created: overlapLastSync.Add(-24 * time.Hour), history: strPtr("")},
		{name: "older stub with another historical ref", created: overlapLastSync.Add(-24 * time.Hour), history: strPtr("https://test.test/EXT-OLD")},
	} {
		for _, source := range []struct {
			name        string
			updated     time.Time
			wantFetches int
		}{
			{name: "single fetch", updated: overlapLastSync.Add(-2 * time.Hour), wantFetches: 1},
			{name: "overlap-only row", updated: overlapLastSync.Add(-30 * time.Second), wantFetches: 0},
		} {
			t.Run(stub.name+"/"+source.name, func(t *testing.T) {
				ctx := context.Background()
				local := linkedIssue("bd-stub", "EXT-1", "stub", stub.created, overlapLastSync.Add(10*time.Minute))
				store := newMissingHistoryStore(local)
				if stub.history != nil {
					store.previousRefs[local.ID] = *stub.history
				}
				mock, _ := newOverlapTracker(staleRemote("EXT-1", source.updated))

				result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true})
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
				if mock.fetchCalls != source.wantFetches {
					t.Errorf("FetchIssue calls = %d, want %d", mock.fetchCalls, source.wantFetches)
				}
				if result.PullStats.Updated != 1 || local.Title != "stale remote title EXT-1" || local.Description != "stale remote body" {
					t.Errorf("PullStats = %+v, stub = %q/%q, want the stub hydrated from the remote",
						result.PullStats, local.Title, local.Description)
				}
				if len(result.Warnings) != 0 {
					t.Errorf("Warnings = %q, want none", result.Warnings)
				}
			})
		}
	}
}
