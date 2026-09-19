package tracker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

type pullTestTransaction struct {
	storage.Transaction
	issue       *types.Issue
	addLabelErr error
}

func (t *pullTestTransaction) UpdateIssue(_ context.Context, _ string, updates map[string]interface{}, _ string) error {
	if title, ok := updates["title"].(string); ok {
		t.issue.Title = title
	}
	if status, ok := updates["status"]; ok {
		switch value := status.(type) {
		case types.Status:
			t.issue.Status = value
		case string:
			t.issue.Status = types.Status(value)
		}
	}
	return nil
}

func (t *pullTestTransaction) ReopenIssueWithResult(_ context.Context, _ string, _ string, _ string) (bool, error) {
	t.issue.Status = types.StatusOpen
	t.issue.ClosedAt = nil
	return true, nil
}

func (t *pullTestTransaction) GetIssue(_ context.Context, _ string) (*types.Issue, error) {
	return t.issue, nil
}

func (t *pullTestTransaction) GetLabels(context.Context, string) ([]string, error) {
	return append([]string(nil), t.issue.Labels...), nil
}
func (t *pullTestTransaction) AddLabel(_ context.Context, _ string, label string, _ string) error {
	if t.addLabelErr != nil {
		return t.addLabelErr
	}
	for _, existing := range t.issue.Labels {
		if existing == label {
			return nil
		}
	}
	t.issue.Labels = append(t.issue.Labels, label)
	return nil
}
func (t *pullTestTransaction) RemoveLabel(_ context.Context, _ string, label string, _ string) error {
	for i, existing := range t.issue.Labels {
		if existing == label {
			t.issue.Labels = append(t.issue.Labels[:i], t.issue.Labels[i+1:]...)
			break
		}
	}
	return nil
}

type pullFailureStore struct {
	*pureTestStore
	tx      *pullTestTransaction
	commits int
}

func (s *pullFailureStore) RunInTransaction(_ context.Context, _ string, fn func(storage.Transaction) error) error {
	before := *s.tx.issue
	if err := fn(s.tx); err != nil {
		*s.tx.issue = before
		return err
	}
	s.commits++
	return nil
}

func (s *pullFailureStore) RunInIssueLifecycleTransaction(_ context.Context, _ string, fn func(storage.IssueLifecycleTransaction) error) error {
	before := *s.tx.issue
	if err := fn(s.tx); err != nil {
		*s.tx.issue = before
		return err
	}
	s.commits++
	return nil
}

func (s *pullFailureStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, issue := range s.issues {
		if issue.ExternalRef != nil && *issue.ExternalRef == ref {
			return issue, nil
		}
	}
	return nil, nil
}

func TestEngineSyncFailedPullIsNotPushed(t *testing.T) {
	remoteUpdated := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name         string
		localID      string
		identifier   string
		externalRef  string
		localUpdated time.Time
	}{
		{
			name:         "create-eligible",
			localID:      "pull-failure-create",
			identifier:   "EXT-CREATE",
			localUpdated: remoteUpdated,
		},
		{
			name:         "update-eligible",
			localID:      "pull-failure-update",
			identifier:   "EXT-UPDATE",
			externalRef:  "https://test.test/EXT-UPDATE",
			localUpdated: remoteUpdated.Add(time.Hour),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			local := &types.Issue{
				ID:        test.localID,
				Title:     "local",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				UpdatedAt: test.localUpdated,
			}
			if test.externalRef != "" {
				ref := test.externalRef
				local.ExternalRef = &ref
			}
			store := &pullFailureStore{
				pureTestStore: newPureTestStore(local),
				tx: &pullTestTransaction{
					issue: local, addLabelErr: errors.New("label write failed"),
				},
			}
			mock := newMockTracker("test")
			mock.issues = []TrackerIssue{{
				ID: test.identifier, Identifier: test.identifier, Title: "remote", UpdatedAt: remoteUpdated,
			}}
			mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
				return &IssueConversion{Issue: &types.Issue{
					ID: test.localID, Title: "remote", Status: types.StatusClosed,
					Priority: 2, IssueType: types.TypeTask, Labels: []string{"remote"},
				}}
			}}

			result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, Push: true})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Errors != 1 || result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
				t.Fatalf("PullStats = %+v, want one error and no create/update", result.PullStats)
			}
			if result.PushStats.Created != 0 || result.PushStats.Updated != 0 || len(mock.created) != 0 || len(mock.updated) != 0 {
				t.Fatalf("%s failed pull was pushed: PushStats=%+v create calls=%d update calls=%d",
					test.name, result.PushStats, len(mock.created), len(mock.updated))
			}
			if store.commits != 0 || local.Status != types.StatusOpen ||
				local.Title != "local" || len(local.Labels) != 0 || !local.UpdatedAt.Equal(test.localUpdated) {
				t.Fatalf("%s failed pull committed state: commits=%d issue=%+v", test.name, store.commits, local)
			}
			if test.externalRef == "" {
				if local.ExternalRef != nil {
					t.Fatalf("create-eligible failed pull left external_ref %q", *local.ExternalRef)
				}
			} else if local.ExternalRef == nil || *local.ExternalRef != test.externalRef {
				t.Fatalf("update-eligible failed pull changed external_ref to %v", local.ExternalRef)
			}
		})
	}
}

func TestReimportIssuePreservesLabels(t *testing.T) {
	ctx := context.Background()
	local := &types.Issue{
		ID:        "reimport-labels",
		Title:     "local",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{"keep-local-label"},
	}
	store := &pullFailureStore{
		pureTestStore: newPureTestStore(local),
		tx:            &pullTestTransaction{issue: local},
	}
	mock := newMockTracker("test")
	mock.issues = []TrackerIssue{{ID: "EXT-2", Identifier: "EXT-2", Title: "remote"}}
	mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			Title: "remote", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask,
		}}
	}}

	engine := NewEngine(mock, store, "sync")
	engine.reimportIssue(ctx, Conflict{IssueID: local.ID, ExternalIdentifier: "EXT-2"})

	if store.commits != 1 {
		t.Fatalf("reimport transaction commits = %d, want 1", store.commits)
	}
	if local.Status != types.StatusClosed || local.Title != "remote" {
		t.Fatalf("reimport result = %+v", local)
	}
	if len(local.Labels) != 1 || local.Labels[0] != "keep-local-label" {
		t.Fatalf("reimport changed local labels: %v", local.Labels)
	}
}
