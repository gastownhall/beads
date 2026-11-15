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

// overlapTitleEqual is a GitHub-style PushFieldsEqual: it compares only the
// fields a push carries, so a remote copy without the local assignee still
// counts as holding what the push sent.
func overlapTitleEqual(local *types.Issue, remote *TrackerIssue) bool {
	return local.Title == remote.Title && local.Description == remote.Description
}

// overlapTitleHooks are GitHub-shaped hooks: one content comparison serves
// as both ContentEqual and PushFieldsEqual.
var overlapTitleHooks = &PushHooks{ContentEqual: overlapTitleEqual, PushFieldsEqual: overlapTitleEqual}

// overlapTimestampHooks are GitLab-shaped hooks for an issue: ContentEqual
// keeps the push's timestamp skip, so it calls any remote at least as new as
// the local issue equal, and the hooks offer no PushFieldsEqual.
var overlapTimestampHooks = &PushHooks{ContentEqual: func(local *types.Issue, remote *TrackerIssue) bool {
	return !remote.UpdatedAt.Before(local.UpdatedAt)
}}

// TestEnginePullOverlapPullsOnlyRemoteChanges pins that the overlap window
// re-fetches the previous sync's own pushes without letting them rewrite
// local issues: a linked issue is written only when the remote changed what a
// push carries, and never by a remote copy older than the local issue.
func TestEnginePullOverlapPullsOnlyRemoteChanges(t *testing.T) {
	const ref = "https://test.test/EXT-1"
	for _, test := range []struct {
		name          string
		hooks         *PushHooks
		push          bool
		localTitle    string
		localUpdated  time.Duration // relative to last_sync
		remoteTitle   string
		remoteUpdated time.Duration // relative to last_sync
		wantUpdated   int
		wantTitle     string
		wantAssignee  string
		wantPushed    string // title pushed to EXT-1, "" for none
	}{
		{
			// Echo of the previous push: GitHub never pushes assignee, so
			// the pulled conversion clears it, yet the remote holds what the
			// push sent.
			name: "echo", hooks: overlapTitleHooks,
			localTitle: "same title", localUpdated: -10 * time.Minute,
			remoteTitle: "same title", remoteUpdated: -30 * time.Second,
			wantTitle: "same title", wantAssignee: "alice",
		},
		{
			name: "remote_edit", hooks: overlapTitleHooks,
			localTitle: "same title", localUpdated: -10 * time.Minute,
			remoteTitle: "renamed remotely", remoteUpdated: -30 * time.Second,
			wantUpdated: 1, wantTitle: "renamed remotely",
		},
		{
			// A push-side ContentEqual never decides the pull: GitLab's
			// calls this newer, retitled remote equal.
			name: "timestamp_content_equal", hooks: overlapTimestampHooks,
			localTitle: "same title", localUpdated: -10 * time.Minute,
			remoteTitle: "renamed remotely", remoteUpdated: -30 * time.Second,
			wantUpdated: 1, wantTitle: "renamed remotely",
		},
		{
			name:       "no_hook",
			localTitle: "same title", localUpdated: -10 * time.Minute,
			remoteTitle: "renamed remotely", remoteUpdated: -30 * time.Second,
			wantUpdated: 1, wantTitle: "renamed remotely",
		},
		{
			// Pushed at T-3m, edited locally at T-60s, then a sync that did
			// not push it advanced last_sync past the edit. The overlap
			// re-fetches the older push; it must not replace the edit.
			name: "unpushed_local_edit", hooks: overlapTitleHooks, push: true,
			localTitle: "edited locally", localUpdated: -60 * time.Second,
			remoteTitle: "pushed title", remoteUpdated: -3 * time.Minute,
			wantTitle: "edited locally", wantAssignee: "alice", wantPushed: "edited locally",
		},
		{
			name: "local_edit_after_T", hooks: overlapTitleHooks, push: true,
			localTitle: "edited locally", localUpdated: 10 * time.Second,
			remoteTitle: "pushed title", remoteUpdated: -30 * time.Second,
			wantTitle: "edited locally", wantAssignee: "alice", wantPushed: "edited locally",
		},
		{
			// A row at last_sync was returned before the overlap too, so it
			// still goes through pullIssueEqual as it always has.
			name: "at_T", hooks: overlapTitleHooks,
			localTitle: "same title", localUpdated: -10 * time.Minute,
			remoteTitle: "same title", remoteUpdated: 0,
			wantUpdated: 1, wantTitle: "same title",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			linkedRef := ref
			local := &types.Issue{
				ID:          "bd-1",
				Title:       test.localTitle,
				Description: "body",
				Assignee:    "alice",
				Status:      types.StatusOpen,
				Priority:    2,
				IssueType:   types.TypeTask,
				ExternalRef: &linkedRef,
				CreatedAt:   overlapLastSync.Add(-time.Hour),
				UpdatedAt:   overlapLastSync.Add(test.localUpdated),
			}
			store := newOverlapStore(local)
			store.previousRefs[local.ID] = ref
			mock, _ := newOverlapTracker(TrackerIssue{
				ID:          "EXT-1",
				Identifier:  "EXT-1",
				Title:       test.remoteTitle,
				Description: "body",
				UpdatedAt:   overlapLastSync.Add(test.remoteUpdated),
			})
			engine := NewEngine(mock, store, "sync")
			engine.PushHooks = test.hooks

			result, err := engine.Sync(ctx, SyncOptions{Pull: true, Push: test.push})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Updated != test.wantUpdated || store.commits != test.wantUpdated {
				t.Fatalf("PullStats = %+v, commits = %d, want %d update(s)", result.PullStats, store.commits, test.wantUpdated)
			}
			if local.Title != test.wantTitle || local.Assignee != test.wantAssignee {
				t.Fatalf("local title/assignee = %q/%q, want %q/%q", local.Title, local.Assignee, test.wantTitle, test.wantAssignee)
			}
			pushed := ""
			if issue := mock.updated["EXT-1"]; issue != nil {
				pushed = issue.Title
			}
			if pushed != test.wantPushed || len(mock.created) != 0 {
				t.Fatalf("pushed title = %q (creates %d), want %q", pushed, len(mock.created), test.wantPushed)
			}
		})
	}
}

// TestEnginePullOverlapHydratesPrelinkedStub pins that a local issue linked
// after last_sync to a remote issue last updated before it is still hydrated
// from the remote when the overlap window returns that remote in the bulk
// fetch, so the push does not overwrite the remote with the local stub.
func TestEnginePullOverlapHydratesPrelinkedStub(t *testing.T) {
	ctx := context.Background()
	ref := "https://test.test/EXT-1"
	stub := &types.Issue{
		ID:          "bd-stub",
		Title:       "stub",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
		CreatedAt:   overlapLastSync.Add(-time.Hour),
		UpdatedAt:   overlapLastSync.Add(10 * time.Second),
	}
	store := newOverlapStore(stub) // created before last_sync, no ref at it: linked after it
	mock, _ := newOverlapTracker(TrackerIssue{
		ID:          "EXT-1",
		Identifier:  "EXT-1",
		Title:       "remote title",
		Description: "remote body",
		UpdatedAt:   overlapLastSync.Add(-30 * time.Second),
	})
	engine := NewEngine(mock, store, "sync")
	engine.PushHooks = overlapTitleHooks

	result, err := engine.Sync(ctx, SyncOptions{Pull: true, Push: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Updated != 1 || stub.Title != "remote title" || stub.Description != "remote body" {
		t.Fatalf("PullStats = %+v, stub = %q/%q, want the stub hydrated from the remote", result.PullStats, stub.Title, stub.Description)
	}
	if len(mock.updated) != 0 || len(mock.created) != 0 {
		t.Fatalf("push wrote the remote: updates %v, creates %d, want it left unchanged", mock.updated, len(mock.created))
	}
	// The only single-issue fetch is DetectConflicts' probe of the locally
	// modified stub: hydration reuses the overlap-only bulk row.
	if mock.fetchCalls != 1 {
		t.Fatalf("FetchIssue calls = %d, want 1 (DetectConflicts probe only; hydration reused the bulk row)", mock.fetchCalls)
	}
}

// TestEnginePullOverlapLinksUnlinkedLocalMatchedByID pins that the overlap
// skip protects only linked issues: an overlap-only row whose conversion names
// an unlinked local issue's ID (as Notion's carries the bead it was pushed
// from) links that issue even when the row is older than it, so the push does
// not create a second remote issue for it.
func TestEnginePullOverlapLinksUnlinkedLocalMatchedByID(t *testing.T) {
	ctx := context.Background()
	local := &types.Issue{
		ID:        "bd-1",
		Title:     "local title",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: overlapLastSync.Add(-time.Hour),
		UpdatedAt: overlapLastSync.Add(-time.Minute),
	}
	store := newOverlapStore(local)
	mock, _ := newOverlapTracker(TrackerIssue{
		ID:         "EXT-1",
		Identifier: "EXT-1",
		Title:      "remote title",
		UpdatedAt:  overlapLastSync.Add(-3 * time.Minute),
	})
	mock.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			ID:        "bd-1",
			Title:     ti.Title,
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}}
	}}

	result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, Push: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Updated != 1 || local.ExternalRef == nil || *local.ExternalRef != "https://test.test/EXT-1" {
		t.Fatalf("PullStats = %+v, external_ref = %v, want bd-1 linked to EXT-1 by the overlap row", result.PullStats, local.ExternalRef)
	}
	if len(mock.created) != 0 || len(mock.updated) != 0 {
		t.Fatalf("push wrote the tracker: creates %d, updates %v, want neither", len(mock.created), mock.updated)
	}
}

// coarseOverlapTracker is an overlap tracker whose FetchIssues matches Since
// to its granularity: Jira's JQL and Notion's minute-precision page times
// match to the minute, Azure DevOps' date-only WIQL to the UTC day.
type coarseOverlapTracker struct {
	*mockTracker
	granularity time.Duration
}

func (c coarseOverlapTracker) SinceGranularity() time.Duration { return c.granularity }

func newCoarseOverlapTracker(granularity time.Duration, remote ...TrackerIssue) coarseOverlapTracker {
	mock, _ := newOverlapTracker(remote...)
	exact := mock.fetchIssues
	mock.fetchIssues = func(ctx context.Context, opts FetchOptions) ([]TrackerIssue, error) {
		if opts.Since != nil {
			floor := opts.Since.UTC().Truncate(granularity)
			opts.Since = &floor
		}
		return exact(ctx, opts)
	}
	return coarseOverlapTracker{mockTracker: mock, granularity: granularity}
}

// TestEnginePullOverlapCoarseTrackerKeepsCursorMinuteRows pins that on a
// tracker matching Since to the minute, the rows a fetch from the exact
// last_sync already returned (the rest of the cursor's minute) keep the
// treatment they had before the overlap, and only older rows are overlap-only.
func TestEnginePullOverlapCoarseTrackerKeepsCursorMinuteRows(t *testing.T) {
	const ref = "https://test.test/EXT-1"
	for _, test := range []struct {
		name          string
		history       bool // the store answers external_ref history
		push          bool
		localTitle    string
		localUpdated  time.Duration // relative to last_sync (03:04:05)
		remoteTitle   string
		remoteUpdated time.Duration // relative to last_sync
		wantUpdated   int
		wantTitle     string
		wantPushed    string // title pushed to EXT-1, "" for none
	}{
		{
			// The previous sync pushed at T-3s, inside the cursor's minute;
			// the issue was edited locally after T. Without external_ref
			// history the edit looks like a relink, but the row was fetched
			// before the overlap too, so the conflict guard keeps the edit
			// and the push sends it.
			name: "local_edit_without_history", push: true,
			localTitle: "edited locally", localUpdated: 10 * time.Second,
			remoteTitle: "pushed title", remoteUpdated: -3 * time.Second,
			wantTitle: "edited locally", wantPushed: "edited locally",
		},
		{
			// A remote edit made after T, stamped with the minute it was
			// made in (03:04:00, as Notion stamps pages), is older than the
			// local issue's last write but still pulled.
			name: "minute_stamped_remote_edit", history: true,
			localTitle: "same title", localUpdated: -2 * time.Second,
			remoteTitle: "renamed remotely", remoteUpdated: -5 * time.Second,
			wantUpdated: 1, wantTitle: "renamed remotely",
		},
		{
			// Before the cursor's minute the overlap rules apply: an older
			// remote copy does not replace a local edit.
			name: "older_row_before_the_minute", history: true, push: true,
			localTitle: "edited locally", localUpdated: -60 * time.Second,
			remoteTitle: "pushed title", remoteUpdated: -3 * time.Minute,
			wantTitle: "edited locally", wantPushed: "edited locally",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			linkedRef := ref
			local := &types.Issue{
				ID:          "bd-1",
				Title:       test.localTitle,
				Status:      types.StatusOpen,
				Priority:    2,
				IssueType:   types.TypeTask,
				ExternalRef: &linkedRef,
				CreatedAt:   overlapLastSync.Add(-time.Hour),
				UpdatedAt:   overlapLastSync.Add(test.localUpdated),
			}
			overlap := newOverlapStore(local)
			overlap.previousRefs[local.ID] = ref
			var store Store = overlap.pullFailureStore
			if test.history {
				store = overlap
			}
			tr := newCoarseOverlapTracker(time.Minute, TrackerIssue{
				ID:         "EXT-1",
				Identifier: "EXT-1",
				Title:      test.remoteTitle,
				UpdatedAt:  overlapLastSync.Add(test.remoteUpdated),
			})

			result, err := NewEngine(tr, store, "sync").Sync(ctx, SyncOptions{Pull: true, Push: test.push})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Updated != test.wantUpdated || local.Title != test.wantTitle {
				t.Fatalf("PullStats = %+v, local title = %q, want %d update(s) and %q", result.PullStats, local.Title, test.wantUpdated, test.wantTitle)
			}
			pushed := ""
			if issue := tr.updated["EXT-1"]; issue != nil {
				pushed = issue.Title
			}
			if pushed != test.wantPushed {
				t.Fatalf("pushed title = %q, want %q", pushed, test.wantPushed)
			}
		})
	}
}

// TestEnginePullOverlapDayTrackerAddsOnlyTheWindow pins that a tracker
// matching Since to the UTC day (Azure DevOps) does not turn a whole day into
// overlap rows: a last_sync at 00:03 widens Since into the previous day, so
// the fetch returns all of it, but only rows in the five minutes before
// last_sync, or on its own day as before the overlap, may update a linked
// issue.
func TestEnginePullOverlapDayTrackerAddsOnlyTheWindow(t *testing.T) {
	const ref = "https://test.test/EXT-1"
	lastSync := time.Date(2026, time.January, 2, 0, 3, 0, 0, time.UTC)
	for _, test := range []struct {
		name          string
		remoteUpdated time.Duration // relative to last_sync
		wantUpdated   int
	}{
		{name: "previous_afternoon", remoteUpdated: -9 * time.Hour},
		{name: "in_the_window", remoteUpdated: -4 * time.Minute, wantUpdated: 1},
		{name: "cursor_day", remoteUpdated: -2 * time.Minute, wantUpdated: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			linkedRef := ref
			local := &types.Issue{
				ID:          "bd-1",
				Title:       "local title",
				Status:      types.StatusOpen,
				Priority:    2,
				IssueType:   types.TypeTask,
				ExternalRef: &linkedRef,
				CreatedAt:   lastSync.Add(-48 * time.Hour),
				UpdatedAt:   lastSync.Add(-24 * time.Hour),
			}
			store := newOverlapStore(local)
			store.localMetadata["test.last_sync"] = lastSync.Format(time.RFC3339)
			store.previousRefs[local.ID] = ref
			tr := newCoarseOverlapTracker(24*time.Hour, TrackerIssue{
				ID:         "EXT-1",
				Identifier: "EXT-1",
				Title:      "renamed remotely",
				UpdatedAt:  lastSync.Add(test.remoteUpdated),
			})

			result, err := NewEngine(tr, store, "sync").Sync(ctx, SyncOptions{Pull: true})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			wantTitle := "local title"
			if test.wantUpdated > 0 {
				wantTitle = "renamed remotely"
			}
			if result.PullStats.Updated != test.wantUpdated || local.Title != wantTitle {
				t.Fatalf("PullStats = %+v, local title = %q, want %d update(s) and %q", result.PullStats, local.Title, test.wantUpdated, wantTitle)
			}
		})
	}
}
