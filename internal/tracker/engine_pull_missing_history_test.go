package tracker

// Regression tests for #7294 (bd-nnkvz2): a history-capable store that holds
// no row for a linked issue at last_sync (compaction or flatten removed the
// baseline) must not make the pull hydrate that issue, because hydration
// exempts it from the conflict and overlap guards that keep unpushed local
// edits.

import (
	"context"
	"fmt"
	"reflect"
	"strings"
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
		// The warning must not claim the issue kept its local content,
		// which conflict resolution just replaced.
		warnings := missingHistoryWarnings(result.Warnings)
		if len(warnings) != 1 || !strings.Contains(warnings[0], "the conflict checks decided their content") || strings.Contains(warnings[0], "keep their local content") {
			t.Errorf("missing-history warnings = %q, want one saying the conflict checks decided the content", warnings)
		}
	})
}

// TestEnginePullWispsPlaneIssuesWithoutHistory pins how a pull treats linked
// wisps-plane issues (#7294): Dolt does not version the wisps table, so an
// ephemeral or no-history issue never has a history row. One created
// before last_sync and edited since is neither fetched for hydration nor
// written, and no missing-history warning names it, while a stub created
// after last_sync is still hydrated.
func TestEnginePullWispsPlaneIssuesWithoutHistory(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		name := "real"
		if dryRun {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			old := overlapLastSync.Add(-24 * time.Hour)
			wisp := linkedIssue("bd-wisp", "EXT-1", "local wisp edit", old, overlapLastSync.Add(10*time.Minute))
			wisp.Ephemeral = true
			noHistory := linkedIssue("bd-nh", "EXT-2", "local no-history edit", old, overlapLastSync.Add(10*time.Minute))
			noHistory.NoHistory = true
			stub := linkedIssue("bd-wisp-stub", "EXT-3", "new wisp stub", overlapLastSync.Add(time.Minute), overlapLastSync.Add(time.Minute))
			stub.Ephemeral = true
			store := newMissingHistoryStore(wisp, noHistory, stub)
			before := snapshotIssues(wisp, noHistory)
			// Every remote was last updated before the overlap window, so
			// only hydration can bring one into the pull.
			mock, _ := newOverlapTracker(
				staleRemote("EXT-1", overlapLastSync.Add(-2*time.Hour)),
				staleRemote("EXT-2", overlapLastSync.Add(-2*time.Hour)),
				staleRemote("EXT-3", overlapLastSync.Add(-2*time.Hour)),
			)

			result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: dryRun})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if mock.fetchCalls != 1 {
				t.Errorf("FetchIssue calls = %d, want 1 (the stub)", mock.fetchCalls)
			}
			if result.PullStats.Candidates != 1 || result.PullStats.Updated != 1 {
				t.Errorf("PullStats = %+v, want the stub as the only update", result.PullStats)
			}
			if warnings := missingHistoryWarnings(result.Warnings); len(warnings) != 0 {
				t.Errorf("missing-history warnings = %q, want none: a wisps-plane issue never has history", warnings)
			}
			if store.updates[wisp.ID] != 0 || store.updates[noHistory.ID] != 0 {
				t.Errorf("transaction writes = %v, want none to the older wisps-plane issues", store.updates)
			}
			if !dryRun && stub.Title != "stale remote title EXT-3" {
				t.Errorf("stub title = %q, want it hydrated from its remote", stub.Title)
			}
			assertIssuesUnchanged(t, before, wisp, noHistory)
		})
	}
}

// TestEnginePullMissingHistoryHydratesNewStub pins AC-3 of #7294: a pre-linked
// stub created at or after last_sync with no history row, and an older stub
// whose historical ref was NULL or another issue's, keep their hydration,
// through a single FetchIssue or by reusing an overlap-only bulk row, without
// the missing-history warning. A stub created within half a second after the
// last_sync stamp stores a created_at equal to it.
func TestEnginePullMissingHistoryHydratesNewStub(t *testing.T) {
	for _, stub := range []struct {
		name    string
		created time.Time
		// history is the stub's historical ref at last_sync; nil means no row.
		history *string
	}{
		{name: "new stub without history", created: overlapLastSync.Add(time.Minute)},
		{name: "new stub created at last_sync without history", created: overlapLastSync},
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

// TestEnginePullMissingHistoryWarnsOnce pins AC-4 of #7294: several older
// issues with missing history, mixed with an older issue whose historical ref
// shows it was linked after last_sync and a new stub, produce exactly one
// warning naming the tracker, the cursor, the missing-history issues changed
// since last_sync (not the untouched one) and, for a pull-only run, a check
// before a sync that pushes, while the known changed ref and the new stub are
// still hydrated and the bulk fetch keeps the cursor minus the overlap
// window. Both issue orders run, so a missing row never stands for the
// issues after it.
func TestEnginePullMissingHistoryWarnsOnce(t *testing.T) {
	for _, order := range []string{"missing first", "missing last"} {
		for _, dryRun := range []bool{true, false} {
			name := order + "/real"
			if dryRun {
				name = order + "/dry-run"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				old := overlapLastSync.Add(-24 * time.Hour)
				missing := []*types.Issue{
					linkedIssue("bd-old-1", "EXT-1", "old one", old, overlapLastSync.Add(10*time.Minute)),
					linkedIssue("bd-old-2", "EXT-2", "old two", old, overlapLastSync.Add(-time.Hour)),
					linkedIssue("bd-old-3", "EXT-3", "old three", old, overlapLastSync.Add(20*time.Minute)),
				}
				relinked := linkedIssue("bd-relinked", "EXT-4", "relinked", old, overlapLastSync.Add(10*time.Minute))
				stub := linkedIssue("bd-new", "EXT-5", "new stub", overlapLastSync.Add(time.Minute), overlapLastSync.Add(time.Minute))
				issues := append(append([]*types.Issue{}, missing...), relinked, stub)
				if order == "missing last" {
					issues = append([]*types.Issue{relinked, stub}, missing...)
				}
				store := newMissingHistoryStore(issues...)
				store.previousRefs[relinked.ID] = "" // unlinked at last_sync
				before := snapshotIssues(missing...)
				var remotes []TrackerIssue
				for _, ext := range []string{"EXT-1", "EXT-2", "EXT-3", "EXT-4", "EXT-5"} {
					remotes = append(remotes, staleRemote(ext, overlapLastSync.Add(-2*time.Hour)))
				}
				mock, since := newOverlapTracker(remotes...)

				result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: dryRun})
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
				wantSince := overlapLastSync.Add(-pullFetchOverlap)
				if *since == nil || !(*since).Equal(wantSince) {
					t.Errorf("FetchOptions.Since = %v, want %s", *since, wantSince)
				}
				if mock.fetchCalls != 2 {
					t.Errorf("FetchIssue calls = %d, want 2 (the relinked issue and the new stub)", mock.fetchCalls)
				}
				if result.PullStats.Candidates != 2 || result.PullStats.Updated != 2 {
					t.Errorf("PullStats = %+v, want the relinked issue and the new stub as the only updates", result.PullStats)
				}
				missingWarnings := missingHistoryWarnings(result.Warnings)
				if len(missingWarnings) != 1 {
					t.Fatalf("missing-history warnings = %q (all warnings %q), want exactly one", missingWarnings, result.Warnings)
				}
				for _, want := range []string{"test: ", overlapLastSync.Format(time.RFC3339), "2 issue(s)", "bd-old-1, bd-old-3", "before a sync that pushes"} {
					if !strings.Contains(missingWarnings[0], want) {
						t.Errorf("warning %q does not mention %q", missingWarnings[0], want)
					}
				}
				if strings.Contains(missingWarnings[0], "bd-old-2") {
					t.Errorf("warning %q names bd-old-2, which is untouched since last_sync", missingWarnings[0])
				}
				if !dryRun {
					if relinked.Title != "stale remote title EXT-4" || stub.Title != "stale remote title EXT-5" {
						t.Errorf("relinked = %q, stub = %q, want both hydrated from their remotes", relinked.Title, stub.Title)
					}
				}
				assertIssuesUnchanged(t, before, missing...)
			})
		}
	}
}

// missingHistoryWarnings returns the missing-history warnings among warnings.
func missingHistoryWarnings(warnings []string) []string {
	var out []string
	for _, w := range warnings {
		if strings.Contains(w, "cannot verify the links of") {
			out = append(out, w)
		}
	}
	return out
}

// TestEnginePullMissingHistoryWarningScope pins whom the missing-history
// warning names and what it advises (#7294). Older issues untouched since
// last_sync cannot have been linked to another remote issue since then
// (every update stamps updated_at), so they alone produce no warning. A run
// that pushes after its pull says its own push may write their local
// content; a pull-only run or a dry run advises a check before a sync that
// pushes. At most maxListedUnverifiedLinks issue IDs are listed.
func TestEnginePullMissingHistoryWarningScope(t *testing.T) {
	old := overlapLastSync.Add(-24 * time.Hour)
	edited := func(n int) *types.Issue {
		return linkedIssue(fmt.Sprintf("bd-edited-%d", n), fmt.Sprintf("EXT-%d", n), "local edit",
			old, overlapLastSync.Add(10*time.Minute))
	}
	for _, test := range []struct {
		name   string
		issues []*types.Issue
		opts   SyncOptions
		// want lists substrings of the one expected warning; nil expects none.
		want, unwanted []string
	}{
		{
			name: "untouched issues do not warn",
			issues: []*types.Issue{
				linkedIssue("bd-untouched-1", "EXT-1", "untouched", old, overlapLastSync.Add(-time.Hour)),
				linkedIssue("bd-untouched-2", "EXT-2", "written by the last sync", old, overlapLastSync),
			},
			opts: SyncOptions{Pull: true},
		},
		{
			name:     "pull and push names this sync's push",
			issues:   []*types.Issue{edited(1)},
			opts:     SyncOptions{Pull: true, Push: true},
			want:     []string{"1 issue(s)", "bd-edited-1", "this sync's push may write"},
			unwanted: []string{"before a sync that pushes"},
		},
		{
			name:   "default sync pulls and pushes",
			issues: []*types.Issue{edited(1)},
			want:   []string{"this sync's push may write"},
		},
		{
			name:     "dry run checks before a sync that pushes",
			issues:   []*types.Issue{edited(1)},
			opts:     SyncOptions{Pull: true, Push: true, DryRun: true},
			want:     []string{"before a sync that pushes"},
			unwanted: []string{"this sync's push"},
		},
		{
			name:     "lists at most five issues",
			issues:   []*types.Issue{edited(1), edited(2), edited(3), edited(4), edited(5), edited(6), edited(7)},
			opts:     SyncOptions{Pull: true},
			want:     []string{"7 issue(s)", ": bd-edited-1, bd-edited-2, bd-edited-3, bd-edited-4, bd-edited-5 and 2 more."},
			unwanted: []string{"bd-edited-6", "bd-edited-7"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newMissingHistoryStore(test.issues...)
			var remotes []TrackerIssue
			for _, issue := range test.issues {
				ext := strings.TrimPrefix(*issue.ExternalRef, "https://test.test/")
				remotes = append(remotes, staleRemote(ext, overlapLastSync.Add(-2*time.Hour)))
			}
			mock, _ := newOverlapTracker(remotes...)

			result, err := NewEngine(mock, store, "sync").Sync(ctx, test.opts)
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			warnings := missingHistoryWarnings(result.Warnings)
			if test.want == nil {
				if len(warnings) != 0 {
					t.Fatalf("missing-history warnings = %q, want none", warnings)
				}
				return
			}
			if len(warnings) != 1 {
				t.Fatalf("missing-history warnings = %q (all warnings %q), want exactly one", warnings, result.Warnings)
			}
			for _, want := range test.want {
				if !strings.Contains(warnings[0], want) {
					t.Errorf("warning %q does not mention %q", warnings[0], want)
				}
			}
			for _, unwanted := range test.unwanted {
				if strings.Contains(warnings[0], unwanted) {
					t.Errorf("warning %q mentions %q", warnings[0], unwanted)
				}
			}
		})
	}
}
