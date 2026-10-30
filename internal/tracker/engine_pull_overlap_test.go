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

// overlapHistoryStore is a pullFailureStore that answers
// PreviousExternalRef, the way a Dolt-backed store does, so
// fetchPrelinkedIssues can tell a pre-linked stub (no previous ref) apart
// from an ordinarily edited linked issue (same previous ref).
type overlapHistoryStore struct {
	*pullFailureStore
	prevRef   string
	prevFound bool
}

func (s *overlapHistoryStore) PreviousExternalRef(_ context.Context, _ string, _ time.Time) (string, bool, error) {
	return s.prevRef, s.prevFound, nil
}

// overlapConversion converts a TrackerIssue the way a lossy pull conversion
// does: title, description and assignee survive, everything else maps to a
// default. The assignee round-trip gap is what makes an echo of a push fail
// pullIssueEqual.
func overlapConversion(ti *TrackerIssue) *IssueConversion {
	return &IssueConversion{Issue: &types.Issue{
		Title:       ti.Title,
		Description: ti.Description,
		Assignee:    ti.Assignee,
		Priority:    2,
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
	}}
}

// TestEnginePullOverlapPullsOnlyRemoteChanges covers what an incremental
// pull does with the rows the overlapped fetch window re-fetches: an echo of
// the previous push must not rewrite the local issue through the lossy pull
// conversion, a real remote edit must still land, and a row older than an
// unpushed local edit must not overwrite it.
func TestEnginePullOverlapPullsOnlyRemoteChanges(t *testing.T) {
	ctx := context.Background()
	lastSync := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	const ref = "https://test.test/EXT-1"

	for _, tt := range []struct {
		name string
		// remote row
		remoteTitle   string
		remoteUpdated time.Time
		// local issue
		localTitle    string
		localDesc     string
		localAssignee string
		localUpdated  time.Time
		withHook      bool // install PushHooks.ContentEqual (title+description)
		push          bool // pull+push instead of pull-only
		wantUpdated   int  // PullStats.Updated
		wantCommits   int  // store transactions committed
		wantTitle     string
		wantAssignee  string
		wantPushTitle string // title of the pushed issue, "" for no push call
	}{
		{
			// Echo of the previous push: content equal, but the pull
			// conversion loses the assignee, so pullIssueEqual alone would
			// rewrite the local issue.
			name:          "echo",
			remoteTitle:   "same title",
			remoteUpdated: lastSync.Add(-30 * time.Second),
			localTitle:    "same title",
			localDesc:     "same desc",
			localAssignee: "alice",
			localUpdated:  lastSync.Add(-10 * time.Minute),
			withHook:      true,
			wantUpdated:   0,
			wantCommits:   0,
			wantTitle:     "same title",
			wantAssignee:  "alice",
		},
		{
			// A real remote edit inside the overlap must still be pulled.
			name:          "remote_edit",
			remoteTitle:   "renamed remotely",
			remoteUpdated: lastSync.Add(-30 * time.Second),
			localTitle:    "original title",
			localDesc:     "same desc",
			localUpdated:  lastSync.Add(-10 * time.Minute),
			withHook:      true,
			wantUpdated:   1,
			wantCommits:   1,
			wantTitle:     "renamed remotely",
		},
		{
			// Trackers without ContentEqual keep pulling through
			// pullIssueEqual.
			name:          "no_hook",
			remoteTitle:   "renamed remotely",
			remoteUpdated: lastSync.Add(-30 * time.Second),
			localTitle:    "original title",
			localDesc:     "same desc",
			localUpdated:  lastSync.Add(-10 * time.Minute),
			wantUpdated:   1,
			wantCommits:   1,
			wantTitle:     "renamed remotely",
		},
		{
			// An unpushed local edit (E < T) that a later sync already
			// advanced the cursor past: the echo row (S < E) must not
			// overwrite it, and the push must send the local edit.
			name:          "unpushed_local_edit",
			remoteTitle:   "pushed title",
			remoteUpdated: lastSync.Add(-3 * time.Minute),
			localTitle:    "local edit",
			localDesc:     "same desc",
			localUpdated:  lastSync.Add(-60 * time.Second),
			withHook:      true,
			push:          true,
			wantUpdated:   0,
			wantCommits:   0,
			wantTitle:     "local edit",
			wantPushTitle: "local edit",
		},
		{
			// Edge: a local edit after T keeps today's behavior — the guard
			// skips the pull, the push sends the edit.
			name:          "local_edit_after_T",
			remoteTitle:   "same title",
			remoteUpdated: lastSync.Add(-30 * time.Second),
			localTitle:    "edited after T",
			localDesc:     "same desc",
			localUpdated:  lastSync.Add(10 * time.Second),
			withHook:      true,
			push:          true,
			wantUpdated:   0,
			wantCommits:   0,
			wantTitle:     "edited after T",
			wantPushTitle: "edited after T",
		},
		{
			// Edge: a row at exactly T is not overlap-only; it keeps going
			// through pullIssueEqual even when ContentEqual reports equal.
			name:          "at_T",
			remoteTitle:   "same title",
			remoteUpdated: lastSync,
			localTitle:    "same title",
			localDesc:     "same desc",
			localAssignee: "alice",
			localUpdated:  lastSync.Add(-10 * time.Minute),
			withHook:      true,
			wantUpdated:   1,
			wantCommits:   1,
			wantTitle:     "same title",
			wantAssignee:  "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			remote := TrackerIssue{
				ID:          "ext-1",
				Identifier:  "EXT-1",
				URL:         ref,
				Title:       tt.remoteTitle,
				Description: "same desc",
				State:       types.StatusOpen,
				Priority:    2,
				UpdatedAt:   tt.remoteUpdated,
			}
			local := &types.Issue{
				ID:          "bd-ov-1",
				Title:       tt.localTitle,
				Description: tt.localDesc,
				Assignee:    tt.localAssignee,
				Status:      types.StatusOpen,
				IssueType:   types.TypeTask,
				Priority:    2,
				UpdatedAt:   tt.localUpdated,
				ExternalRef: strPtr(ref),
			}
			store := &overlapHistoryStore{
				pullFailureStore: &pullFailureStore{
					pureTestStore: newPureTestStore(local),
					tx:            &pullTestTransaction{issue: local},
				},
				prevRef:   ref,
				prevFound: true,
			}
			store.localMetadata["test.last_sync"] = lastSync.Format(time.RFC3339)

			tr := newMockTracker("test")
			tr.issues = []TrackerIssue{remote}
			tr.fieldMapper = &mockMapper{issueToBeads: overlapConversion}
			tr.fetchIssues = func(_ context.Context, opts FetchOptions) ([]TrackerIssue, error) {
				// Honour Since the way a tracker's updated-after list does.
				if opts.Since != nil && !remote.UpdatedAt.After(*opts.Since) {
					return nil, nil
				}
				return []TrackerIssue{remote}, nil
			}

			engine := NewEngine(tr, store, "sync")
			if tt.withHook {
				engine.PushHooks = &PushHooks{ContentEqual: func(local *types.Issue, remote *TrackerIssue) bool {
					return local.Title == remote.Title && local.Description == remote.Description
				}}
			}

			result, err := engine.Sync(ctx, SyncOptions{Pull: true, Push: tt.push})
			if err != nil {
				t.Fatalf("Sync error: %v", err)
			}
			if result.PullStats.Updated != tt.wantUpdated {
				t.Errorf("PullStats.Updated = %d, want %d (stats %+v)", result.PullStats.Updated, tt.wantUpdated, result.PullStats)
			}
			if store.commits != tt.wantCommits {
				t.Errorf("store commits = %d, want %d", store.commits, tt.wantCommits)
			}
			if local.Title != tt.wantTitle {
				t.Errorf("local title = %q, want %q", local.Title, tt.wantTitle)
			}
			if local.Assignee != tt.wantAssignee {
				t.Errorf("local assignee = %q, want %q", local.Assignee, tt.wantAssignee)
			}
			if tt.push {
				pushed, ok := tr.updated["EXT-1"]
				switch {
				case tt.wantPushTitle == "":
					if ok {
						t.Errorf("push updated the remote to %q, want no update", pushed.Title)
					}
				case !ok:
					t.Errorf("push did not update the remote, want title %q", tt.wantPushTitle)
				case pushed.Title != tt.wantPushTitle:
					t.Errorf("pushed title = %q, want %q", pushed.Title, tt.wantPushTitle)
				}
			}
		})
	}
}

// TestEnginePullOverlapHydratesPrelinkedStub covers a local issue linked to a
// remote after last_sync whose remote row only the overlapped fetch window
// returns: the pull must hydrate the stub from that row (so the push does not
// overwrite the remote with the stub's placeholder content) instead of
// treating the row as "remote already in this pull" and skipping hydration.
func TestEnginePullOverlapHydratesPrelinkedStub(t *testing.T) {
	ctx := context.Background()
	lastSync := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	const ref = "https://test.test/EXT-1"

	remote := TrackerIssue{
		ID:          "ext-1",
		Identifier:  "EXT-1",
		URL:         ref,
		Title:       "remote title",
		Description: "remote desc",
		State:       types.StatusOpen,
		Priority:    2,
		UpdatedAt:   lastSync.Add(-30 * time.Second), // overlap-only row
	}
	// The link was made after last_sync: no previous external ref at T.
	local := &types.Issue{
		ID:        "bd-ov-stub",
		Title:     "stub title",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
		CreatedAt: lastSync.Add(-time.Hour),
		UpdatedAt: lastSync.Add(10 * time.Second),
		ExternalRef: strPtr(ref),
	}
	store := &overlapHistoryStore{
		pullFailureStore: &pullFailureStore{
			pureTestStore: newPureTestStore(local),
			tx:            &pullTestTransaction{issue: local},
		},
		prevFound: false,
	}
	store.localMetadata["test.last_sync"] = lastSync.Format(time.RFC3339)

	tr := newMockTracker("test")
	tr.issues = []TrackerIssue{remote}
	tr.fieldMapper = &mockMapper{issueToBeads: overlapConversion}
	tr.fetchIssues = func(_ context.Context, opts FetchOptions) ([]TrackerIssue, error) {
		if opts.Since != nil && !remote.UpdatedAt.After(*opts.Since) {
			return nil, nil
		}
		return []TrackerIssue{remote}, nil
	}

	engine := NewEngine(tr, store, "sync")
	engine.PushHooks = &PushHooks{ContentEqual: func(local *types.Issue, remote *TrackerIssue) bool {
		return local.Title == remote.Title && local.Description == remote.Description
	}}

	result, err := engine.Sync(ctx, SyncOptions{Pull: true, Push: true})
	if err != nil {
		t.Fatalf("Sync error: %v", err)
	}

	// The stub is hydrated from the overlapped bulk row.
	if result.PullStats.Updated != 1 {
		t.Errorf("PullStats.Updated = %d, want 1 (stub hydrated, stats %+v)", result.PullStats.Updated, result.PullStats)
	}
	if store.commits != 1 {
		t.Errorf("store commits = %d, want 1 (the hydration write)", store.commits)
	}
	if local.Title != "remote title" || local.Description != "remote desc" {
		t.Errorf("hydrated stub = %q / %q, want %q / %q", local.Title, local.Description, "remote title", "remote desc")
	}
	// The push finds the remote already holds the hydrated content.
	if len(tr.updated) != 0 {
		t.Errorf("push updated the remote to %v, want no update (remote unchanged)", tr.updated)
	}
	// The only single-issue fetch is DetectConflicts' probe of the locally
	// modified stub: hydration reuses the overlapped bulk row instead of
	// fetching, and the push does not re-examine an issue this sync just
	// pulled.
	if tr.fetchCalls != 1 {
		t.Errorf("FetchIssue calls = %d, want 1 (DetectConflicts probe only; hydration reused the bulk row and the push echoed nothing back)", tr.fetchCalls)
	}
}
