// Regression tests for #7294: when a store's commit history is rewritten
// (bd compact, bd dolt flatten, an import), the rows a tracker's stored
// last_sync cursor would compare an issue's external_ref against are gone.
// The pre-link detector must not read that missing row as a re-link: it
// granted the issue a hydration exemption, which bypassed the pull's
// conflict and overlap guards, so a stale remote could overwrite unpushed
// local edits. Missing history is unknown evidence, not a change.
//
// This file MUST NOT carry a `//go:build cgo` tag (see test_helpers_pure_test.go):
// the stores below are pure Go fakes.

package tracker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// missingHistoryStore serves several local issues like pullFailureStore
// does, but its update transactions dispatch to the issue being updated, so
// one pull can record which of several issues each write touched.
type missingHistoryStore struct {
	*pureTestStore
	previousRefs map[string]string // issue ID -> ref at last_sync; absent entry = no history row (#7294)
	commits      int
	updatedIDs   []string
}

func newMissingHistoryStore(issues ...*types.Issue) *missingHistoryStore {
	return &missingHistoryStore{
		pureTestStore: newPureTestStore(issues...),
		previousRefs:  make(map[string]string),
	}
}

// PreviousExternalRef mirrors a history-capable store whose history no
// longer reaches back to the cursor: an issue with no entry has no
// comparable row at or before last_sync (found=false), an issue with an
// entry answers the recorded ref.
func (s *missingHistoryStore) PreviousExternalRef(_ context.Context, id string, _ time.Time) (string, bool, error) {
	ref, ok := s.previousRefs[id]
	return ref, ok, nil
}

func (s *missingHistoryStore) issue(id string) *types.Issue {
	for _, issue := range s.issues {
		if issue.ID == id {
			return issue
		}
	}
	return nil
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
	before := s.snapshot()
	if err := fn(&missingHistoryTransaction{store: s}); err != nil {
		s.restore(before)
		return err
	}
	s.commits++
	return nil
}

func (s *missingHistoryStore) RunInIssueLifecycleTransaction(_ context.Context, _ string, fn func(storage.IssueLifecycleTransaction) error) error {
	before := s.snapshot()
	if err := fn(&missingHistoryTransaction{store: s}); err != nil {
		s.restore(before)
		return err
	}
	s.commits++
	return nil
}

func (s *missingHistoryStore) snapshot() map[string]types.Issue {
	out := make(map[string]types.Issue, len(s.issues))
	for _, issue := range s.issues {
		out[issue.ID] = *issue
	}
	return out
}

func (s *missingHistoryStore) restore(before map[string]types.Issue) {
	for _, issue := range s.issues {
		if saved, ok := before[issue.ID]; ok {
			*issue = saved
		}
	}
}

func (s *missingHistoryStore) appliedUpdate(id string) {
	s.updatedIDs = append(s.updatedIDs, id)
}

// missingHistoryTransaction applies the pull's writes to the store's issues,
// dispatching by issue ID like the real transaction does.
type missingHistoryTransaction struct {
	storage.Transaction
	store *missingHistoryStore
}

func (t *missingHistoryTransaction) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	issue := t.store.issue(id)
	if issue == nil {
		return storage.ErrNotFound
	}
	if title, ok := updates["title"].(string); ok {
		issue.Title = title
	}
	if description, ok := updates["description"].(string); ok {
		issue.Description = description
	}
	if assignee, ok := updates["assignee"].(string); ok {
		issue.Assignee = assignee
	}
	if issueType, ok := updates["issue_type"].(string); ok {
		issue.IssueType = types.IssueType(issueType)
	}
	if ref, ok := updates["external_ref"].(string); ok {
		issue.ExternalRef = &ref
	}
	if status, ok := updates["status"]; ok {
		switch value := status.(type) {
		case types.Status:
			issue.Status = value
		case string:
			issue.Status = types.Status(value)
		}
	}
	t.store.appliedUpdate(id)
	return nil
}

func (t *missingHistoryTransaction) ReopenIssueWithResult(_ context.Context, id string, _, _ string) (bool, error) {
	issue := t.store.issue(id)
	if issue == nil {
		return false, storage.ErrNotFound
	}
	issue.Status = types.StatusOpen
	issue.ClosedAt = nil
	return true, nil
}

func (t *missingHistoryTransaction) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if issue := t.store.issue(id); issue != nil {
		return issue, nil
	}
	return nil, storage.ErrNotFound
}

func (t *missingHistoryTransaction) GetLabels(_ context.Context, id string) ([]string, error) {
	if issue := t.store.issue(id); issue != nil {
		return append([]string(nil), issue.Labels...), nil
	}
	return nil, storage.ErrNotFound
}

func (t *missingHistoryTransaction) AddLabel(_ context.Context, id string, label string, _ string) error {
	issue := t.store.issue(id)
	if issue == nil {
		return storage.ErrNotFound
	}
	for _, existing := range issue.Labels {
		if existing == label {
			return nil
		}
	}
	issue.Labels = append(issue.Labels, label)
	return nil
}

func (t *missingHistoryTransaction) RemoveLabel(_ context.Context, id string, label string, _ string) error {
	issue := t.store.issue(id)
	if issue == nil {
		return storage.ErrNotFound
	}
	for i, existing := range issue.Labels {
		if existing == label {
			issue.Labels = append(issue.Labels[:i], issue.Labels[i+1:]...)
			break
		}
	}
	return nil
}

// missingHistoryWarning returns the one warning a pull with unverified older
// links must produce, and whether it was found.
func missingHistoryWarning(result *SyncResult) (string, bool) {
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "cannot verify whether issues linked before the last sync") {
			return warning, true
		}
	}
	return "", false
}

// TestEnginePullMissingHistoryPreservesLocalEdits pins #7294: a store whose
// history was rewritten has no row at or before the stored cursor for an
// issue that predates it, so the pre-link detector must not read the
// missing row as a re-link and hydrate the issue — the hydration exemption
// is what let a stale remote overwrite the unpushed local edit.
func TestEnginePullMissingHistoryPreservesLocalEdits(t *testing.T) {
	const ref = "https://test.test/EXT-1"
	newLocal := func() *types.Issue {
		linked := ref
		return &types.Issue{
			ID:          "bd-1",
			Title:       "unpushed local edit",
			Description: "local body",
			Assignee:    "alice",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			ExternalRef: &linked,
			// Existed before the cursor, edited after it: the exact row the
			// old found=false exemption handed to the remote.
			CreatedAt: overlapLastSync.Add(-time.Hour),
			UpdatedAt: overlapLastSync.Add(10 * time.Second),
		}
	}
	remote := TrackerIssue{
		ID:          "EXT-1",
		Identifier:  "EXT-1",
		Title:       "stale remote title",
		Description: "stale remote body",
		UpdatedAt:   overlapLastSync.Add(-time.Minute),
	}

	for _, dryRun := range []bool{false, true} {
		name := "real pull"
		if dryRun {
			name = "dry run"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			local := newLocal()
			store := newOverlapStore(local) // no previousRefs entry: the row at the cursor is gone (#7294)
			// The bulk fetch returns nothing; the stale remote exists only
			// per-issue, so the sole route to the local issue is the
			// pre-link hydration fetch.
			mock := newMockTracker("test")
			mock.issues = []TrackerIssue{remote}
			var fetchSince *time.Time
			mock.fetchIssues = func(_ context.Context, opts FetchOptions) ([]TrackerIssue, error) {
				fetchSince = opts.Since
				return nil, nil
			}

			result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: dryRun})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
				t.Fatalf("PullStats = %+v, want no creates and no updates", result.PullStats)
			}
			if result.PullStats.Candidates != 0 {
				t.Fatalf("PullStats.Candidates = %d, want zero hydration candidates", result.PullStats.Candidates)
			}
			if mock.fetchCalls != 0 {
				t.Fatalf("FetchIssue calls = %d, want 0 (no per-issue hydration fetch)", mock.fetchCalls)
			}
			if fetchSince == nil || !fetchSince.Equal(overlapLastSync.Add(-pullFetchOverlap)) {
				t.Fatalf("FetchOptions.Since = %v, want the original cursor %s", fetchSince, overlapLastSync.Add(-pullFetchOverlap).Format(time.RFC3339))
			}
			if local.Title != "unpushed local edit" || local.Description != "local body" || local.Assignee != "alice" {
				t.Fatalf("local issue = %q/%q/%q, want every local field preserved",
					local.Title, local.Description, local.Assignee)
			}
			if store.commits != 0 {
				t.Fatalf("update transactions = %d, want 0", store.commits)
			}
			warning, found := missingHistoryWarning(result)
			if !found {
				t.Fatalf("warnings = %v, want one about unverified links from the rewritten history", result.Warnings)
			}
			if !strings.Contains(warning, "test") {
				t.Errorf("warning %q does not name the tracker", warning)
			}
			if !strings.Contains(warning, overlapLastSync.Format(time.RFC3339)) {
				t.Errorf("warning %q does not name the original sync time %s", warning, overlapLastSync.Format(time.RFC3339))
			}
			if !strings.Contains(warning, "review") {
				t.Errorf("warning %q does not ask for review before pushing", warning)
			}
		})
	}
}

// TestEnginePullMissingHistoryBulkRowsKeepGuards pins that a missing-history
// issue is never granted the hydration exemption a bulk row would apply:
// when the stale remote comes back from the overlap window, and when it
// comes back from the ordinary post-cursor fetch, the conflict and overlap
// guards keep the unpushed local edit. Only an explicit conflict resolution
// that authorizes the overwrite still applies the remote.
func TestEnginePullMissingHistoryBulkRowsKeepGuards(t *testing.T) {
	const ref = "https://test.test/EXT-1"
	newLocal := func() *types.Issue {
		linked := ref
		return &types.Issue{
			ID:          "bd-1",
			Title:       "unpushed local edit",
			Description: "local body",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			ExternalRef: &linked,
			CreatedAt:   overlapLastSync.Add(-time.Hour),
			UpdatedAt:   overlapLastSync.Add(10 * time.Second),
		}
	}
	remoteAt := func(updated time.Time) TrackerIssue {
		return TrackerIssue{
			ID:          "EXT-1",
			Identifier:  "EXT-1",
			Title:       "stale remote title",
			Description: "stale remote body",
			UpdatedAt:   updated,
		}
	}

	t.Run("overlap window row", func(t *testing.T) {
		ctx := context.Background()
		for _, dryRun := range []bool{false, true} {
			name := "real pull"
			if dryRun {
				name = "dry run"
			}
			t.Run(name, func(t *testing.T) {
				local := newLocal()
				store := newOverlapStore(local) // no previousRefs entry: missing history (#7294)
				mock, _ := newOverlapTracker(remoteAt(overlapLastSync.Add(-30 * time.Second)))

				result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: dryRun})
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
				if result.PullStats.Updated != 0 || store.commits != 0 {
					t.Fatalf("PullStats = %+v, commits = %d, want the local edit kept", result.PullStats, store.commits)
				}
				if local.Title != "unpushed local edit" {
					t.Fatalf("local title = %q, want the unpushed local edit kept", local.Title)
				}
				if mock.fetchCalls != 0 {
					t.Fatalf("FetchIssue calls = %d, want 0", mock.fetchCalls)
				}
				if _, found := missingHistoryWarning(result); !found {
					t.Fatalf("warnings = %v, want the unverified-links warning", result.Warnings)
				}
			})
		}
	})

	t.Run("post-cursor bulk row", func(t *testing.T) {
		ctx := context.Background()
		local := newLocal()
		store := newOverlapStore(local) // no previousRefs entry: missing history (#7294)
		mock, _ := newOverlapTracker(remoteAt(overlapLastSync.Add(30 * time.Second)))

		result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Updated != 0 || store.commits != 0 {
			t.Fatalf("PullStats = %+v, commits = %d, want the local edit kept", result.PullStats, store.commits)
		}
		if local.Title != "unpushed local edit" {
			t.Fatalf("local title = %q, want the unpushed local edit kept", local.Title)
		}
		if _, found := missingHistoryWarning(result); !found {
			t.Fatalf("warnings = %v, want the unverified-links warning", result.Warnings)
		}
	})

	// Control: an explicit resolution that keeps the external version still
	// authorizes the overwrite; missing history only removes the automatic
	// exemption, never the deliberate one.
	t.Run("overwrite authorized still applies", func(t *testing.T) {
		ctx := context.Background()
		local := newLocal()
		store := newOverlapStore(local) // no previousRefs entry: missing history (#7294)
		mock, _ := newOverlapTracker(remoteAt(overlapLastSync.Add(30 * time.Second)))

		result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{
			Pull:               true,
			Push:               true,
			ConflictResolution: ConflictExternal,
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Updated != 1 || local.Title != "stale remote title" {
			t.Fatalf("PullStats = %+v, local title = %q, want the authorized overwrite applied",
				result.PullStats, local.Title)
		}
	})
}

// TestEnginePullMissingHistoryHydratesNewStub pins the other side of #7294's
// fix: hydration is withheld only where history is missing AND the issue is
// older than the cursor. A genuinely new pre-linked stub (created after the
// last sync) still hydrates, and an older issue whose recorded history does
// answer — a NULL or a different external ref at the cursor — still hydrates
// from it, without any warning.
func TestEnginePullMissingHistoryHydratesNewStub(t *testing.T) {
	const ref = "https://test.test/EXT-1"
	newStub := func(created time.Time) *types.Issue {
		linked := ref
		return &types.Issue{
			ID:          "bd-stub",
			Title:       "stub",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			ExternalRef: &linked,
			CreatedAt:   created,
			UpdatedAt:   created.Add(time.Second),
		}
	}
	remote := TrackerIssue{
		ID:          "EXT-1",
		Identifier:  "EXT-1",
		Title:       "remote title",
		Description: "remote body",
		UpdatedAt:   overlapLastSync.Add(-30 * time.Second),
	}

	t.Run("new stub hydrates through a single fetch", func(t *testing.T) {
		ctx := context.Background()
		stub := newStub(overlapLastSync.Add(30 * time.Second)) // created after the cursor
		store := newOverlapStore(stub)                          // no previousRefs entry: the stub is new
		mock := newMockTracker("test")
		mock.issues = []TrackerIssue{remote}
		mock.fetchIssues = func(_ context.Context, _ FetchOptions) ([]TrackerIssue, error) {
			return nil, nil
		}

		result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Updated != 1 || stub.Title != "remote title" || stub.Description != "remote body" {
			t.Fatalf("PullStats = %+v, stub = %q/%q, want the new stub hydrated from the remote",
				result.PullStats, stub.Title, stub.Description)
		}
		if mock.fetchCalls != 1 {
			t.Fatalf("FetchIssue calls = %d, want 1", mock.fetchCalls)
		}
		if _, found := missingHistoryWarning(result); found {
			t.Fatalf("warnings = %v, want none for a new stub", result.Warnings)
		}
	})

	t.Run("new stub hydrates from an overlap-only row", func(t *testing.T) {
		ctx := context.Background()
		stub := newStub(overlapLastSync.Add(30 * time.Second)) // created after the cursor
		store := newOverlapStore(stub)                          // no previousRefs entry: the stub is new
		mock, _ := newOverlapTracker(remote)                    // overlap-only bulk row

		result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Updated != 1 || stub.Title != "remote title" {
			t.Fatalf("PullStats = %+v, stub title = %q, want the bulk row applied to the new stub",
				result.PullStats, stub.Title)
		}
		if mock.fetchCalls != 0 {
			t.Fatalf("FetchIssue calls = %d, want 0 (the overlap-only row is reused)", mock.fetchCalls)
		}
		if _, found := missingHistoryWarning(result); found {
			t.Fatalf("warnings = %v, want none for a new stub", result.Warnings)
		}
	})

	t.Run("older stub with a historical NULL ref hydrates without warning", func(t *testing.T) {
		ctx := context.Background()
		stub := newStub(overlapLastSync.Add(-time.Hour)) // existed before the cursor, unlinked at it
		store := newOverlapStore(stub)
		store.previousRefs[stub.ID] = "" // issueops answers ("", found=true)
		mock := newMockTracker("test")
		mock.issues = []TrackerIssue{remote}
		mock.fetchIssues = func(_ context.Context, _ FetchOptions) ([]TrackerIssue, error) {
			return nil, nil
		}

		result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Updated != 1 || stub.Title != "remote title" {
			t.Fatalf("PullStats = %+v, stub title = %q, want the known pre-existing unlinked row hydrated",
				result.PullStats, stub.Title)
		}
		if mock.fetchCalls != 1 {
			t.Fatalf("FetchIssue calls = %d, want 1", mock.fetchCalls)
		}
		if _, found := missingHistoryWarning(result); found {
			t.Fatalf("warnings = %v, want none when history answers NULL", result.Warnings)
		}
	})

	t.Run("older issue with a historical different ref hydrates without warning", func(t *testing.T) {
		ctx := context.Background()
		stub := newStub(overlapLastSync.Add(-time.Hour)) // existed before the cursor
		store := newOverlapStore(stub)
		store.previousRefs[stub.ID] = "https://test.test/EXT-OLD" // a different ref at the cursor: a re-link
		mock := newMockTracker("test")
		mock.issues = []TrackerIssue{remote}
		mock.fetchIssues = func(_ context.Context, _ FetchOptions) ([]TrackerIssue, error) {
			return nil, nil
		}

		result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Updated != 1 || stub.Title != "remote title" {
			t.Fatalf("PullStats = %+v, stub title = %q, want the known re-link hydrated",
				result.PullStats, stub.Title)
		}
		if _, found := missingHistoryWarning(result); found {
			t.Fatalf("warnings = %v, want none when history answers a different ref", result.Warnings)
		}
	})
}
