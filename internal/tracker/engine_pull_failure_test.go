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
	created []*types.Issue
}

func (s *pullFailureStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	s.created = append(s.created, issue)
	return nil
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
	// Only externally-linked beads appear here: since embedded-ID adoption of
	// unlinked beads was removed (the footer is attacker-writable and is now a
	// consistency hint only), a pull failure can no longer touch a local bead
	// that lacks an external_ref, so no push-create-eligible scenario exists.
	for _, test := range []struct {
		name         string
		localID      string
		identifier   string
		externalRef  string
		localUpdated time.Time
	}{
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
			if local.ExternalRef == nil || *local.ExternalRef != test.externalRef {
				t.Fatalf("update-eligible failed pull changed external_ref to %v", local.ExternalRef)
			}
		})
	}
}

// TestPullTreatsEmbeddedIDAsConsistencyHint verifies that a bead ID embedded
// in an external issue body (e.g. the GitHub sync footer) is only a
// consistency hint, never an identity source: issue authors control the body
// and the footer format is public, so a fabricated footer must neither adopt
// a bead that is not already linked to this exact external issue (unlinked or
// linked elsewhere) nor mint a new bead with the attacker-chosen ID. Only a
// bead whose external_ref already names this external issue is confirmed.
func TestPullTreatsEmbeddedIDAsConsistencyHint(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name        string
		victimRef   string // external_ref already on the local bead ("" = unlinked)
		wantCreated int
		wantUpdated int
	}{
		{name: "different external_ref refuses adoption", victimRef: "https://test.test/EXT-OTHER", wantCreated: 1, wantUpdated: 0},
		{name: "fabricated footer on unlinked bead refuses adoption", victimRef: "", wantCreated: 1, wantUpdated: 0},
		{name: "matching external_ref confirms the link", victimRef: "https://test.test/EXT-IMPOSTER", wantCreated: 0, wantUpdated: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			victim := &types.Issue{
				ID:        "bd-victim",
				Title:     "victim",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if test.victimRef != "" {
				ref := test.victimRef
				victim.ExternalRef = &ref
			}
			store := &pullFailureStore{
				pureTestStore: newPureTestStore(victim),
				tx:            &pullTestTransaction{issue: victim},
			}
			mock := newMockTracker("test")
			mock.issues = []TrackerIssue{{ID: "EXT-IMPOSTER", Identifier: "EXT-IMPOSTER", Title: "imposter"}}
			mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
				return &IssueConversion{Issue: &types.Issue{
					ID: "bd-victim", Title: "imposter", Status: types.StatusOpen,
					Priority: 2, IssueType: types.TypeTask,
				}}
			}}

			result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: true})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Created != test.wantCreated || result.PullStats.Updated != test.wantUpdated {
				t.Fatalf("PullStats = %+v, want Created=%d Updated=%d",
					result.PullStats, test.wantCreated, test.wantUpdated)
			}
			// The victim bead must be untouched in every refusal case.
			if test.victimRef == "" {
				if victim.ExternalRef != nil {
					t.Fatalf("pull linked the unlinked victim to %q", *victim.ExternalRef)
				}
			} else if victim.ExternalRef == nil || *victim.ExternalRef != test.victimRef {
				t.Fatalf("pull changed victim external_ref to %v", victim.ExternalRef)
			}
			if victim.Title != "victim" {
				t.Fatalf("pull mutated victim title to %q", victim.Title)
			}
		})
	}
}

// TestPullFabricatedFooterCreatesBeadWithGeneratedID verifies the import path
// for a fabricated footer: whether the embedded ID names an unlinked existing
// bead or no bead at all, the external issue is imported as a NEW bead whose
// ID comes from the GenerateID hook — never the attacker-chosen one — and an
// existing bead of that name stays untouched.
func TestPullFabricatedFooterCreatesBeadWithGeneratedID(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name       string
		embeddedID string // ID carried in the fabricated footer
	}{
		{name: "unknown embedded ID", embeddedID: "bd-nonexistent"},
		{name: "embedded ID of unlinked existing bead", embeddedID: "bd-victim"},
	} {
		t.Run(test.name, func(t *testing.T) {
			victim := &types.Issue{
				ID:        "bd-victim",
				Title:     "victim",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			store := &pullFailureStore{
				pureTestStore: newPureTestStore(victim),
				tx:            &pullTestTransaction{issue: victim},
			}
			mock := newMockTracker("test")
			mock.issues = []TrackerIssue{{ID: "EXT-IMPOSTER", Identifier: "EXT-IMPOSTER", Title: "imposter"}}
			mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
				return &IssueConversion{Issue: &types.Issue{
					ID: test.embeddedID, Title: "imposter", Status: types.StatusOpen,
					Priority: 2, IssueType: types.TypeTask,
				}}
			}}

			engine := NewEngine(mock, store, "sync")
			engine.PullHooks = &PullHooks{
				GenerateID: func(_ context.Context, issue *types.Issue) error {
					if issue.ID == "" {
						issue.ID = "bd-generated"
					}
					return nil
				},
			}
			result, err := engine.Sync(ctx, SyncOptions{Pull: true})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Created != 1 || result.PullStats.Updated != 0 {
				t.Fatalf("PullStats = %+v, want Created=1 Updated=0", result.PullStats)
			}
			if len(store.created) != 1 {
				t.Fatalf("created %d issues, want 1", len(store.created))
			}
			created := store.created[0]
			if created.ID != "bd-generated" {
				t.Fatalf("created bead ID = %q, want generated bd-generated (embedded %q must not be minted)",
					created.ID, test.embeddedID)
			}
			if created.ExternalRef == nil || *created.ExternalRef != "https://test.test/EXT-IMPOSTER" {
				t.Fatalf("created bead external_ref = %v, want the external issue's ref", created.ExternalRef)
			}
			// The existing bead keeps its identity: no link, no field changes.
			if victim.ExternalRef != nil || victim.Title != "victim" || store.commits != 0 {
				t.Fatalf("existing bead mutated: %+v (commits=%d)", victim, store.commits)
			}
		})
	}
}

// TestEnginePushSkipsIssueWhenFormatDescriptionFails verifies that a
// FormatDescription failure (e.g. a failed dependency-graph query while
// rendering the GitHub sync footer) skips that issue's push entirely: nothing
// is sent to the tracker and no push hash is recorded, so the next run
// retries instead of caching a body rendered from incomplete inputs. It also
// pins dry-run/real-run parity (gastownhall/beads#5065): a dry-run preview
// must report the same skip+error a real run would, not a create/update the
// real run would never actually perform.
func TestEnginePushSkipsIssueWhenFormatDescriptionFails(t *testing.T) {
	newFixture := func() (*pullFailureStore, *mockTracker) {
		ref := "https://test.test/EXT-1"
		local := &types.Issue{
			ID:          "bd-render-fail",
			Title:       "local",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			ExternalRef: &ref,
		}
		store := &pullFailureStore{
			pureTestStore: newPureTestStore(local),
			tx:            &pullTestTransaction{issue: local},
		}
		return store, newMockTracker("test")
	}
	formatHook := &PushHooks{
		FormatDescription: func(*types.Issue) (string, error) {
			return "", errors.New("dependency query failed")
		},
	}

	t.Run("real run", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newFixture()
		engine := NewEngine(mock, store, "sync")
		engine.PushHooks = formatHook
		result, err := engine.Sync(ctx, SyncOptions{Push: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PushStats.Errors != 1 || result.PushStats.Created != 0 || result.PushStats.Updated != 0 {
			t.Fatalf("PushStats = %+v, want one error and no create/update", result.PushStats)
		}
		if len(mock.created) != 0 || len(mock.updated) != 0 {
			t.Fatalf("tracker was called despite render failure: create=%d update=%d", len(mock.created), len(mock.updated))
		}
		if h := store.localMetadata["test.pushhash.bd-render-fail"]; h != "" {
			t.Fatalf("push hash recorded despite render failure: %q", h)
		}
	})

	t.Run("dry run parity", func(t *testing.T) {
		ctx := context.Background()
		store, mock := newFixture()
		engine := NewEngine(mock, store, "sync")
		engine.PushHooks = formatHook
		result, err := engine.Sync(ctx, SyncOptions{Push: true, DryRun: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PushStats.Errors != 1 || result.PushStats.Created != 0 || result.PushStats.Updated != 0 {
			t.Fatalf("dry-run PushStats = %+v, want one error and no create/update (must match real run)", result.PushStats)
		}
		if len(mock.created) != 0 || len(mock.updated) != 0 {
			t.Fatalf("tracker was called during dry-run: create=%d update=%d", len(mock.created), len(mock.updated))
		}
	})
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
