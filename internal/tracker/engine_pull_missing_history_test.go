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
