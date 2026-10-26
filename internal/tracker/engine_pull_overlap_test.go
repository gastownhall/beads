package tracker

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// overlapTestStore is a minimal in-memory Store that records every write the
// engine makes, so tests can assert that a pull touched nothing.
type overlapTestStore struct {
	issues   []*types.Issue
	metadata map[string]string
	creates  int
	updates  int
	deps     int
}

func (s *overlapTestStore) GetConfig(context.Context, string) (string, error) { return "", nil }
func (s *overlapTestStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (s *overlapTestStore) GetLocalMetadata(_ context.Context, key string) (string, error) {
	return s.metadata[key], nil
}
func (s *overlapTestStore) SetLocalMetadata(_ context.Context, key, value string) error {
	s.metadata[key] = value
	return nil
}
func (s *overlapTestStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return append([]*types.Issue(nil), s.issues...), nil
}
func (s *overlapTestStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, issue := range s.issues {
		if issue.ExternalRef != nil && *issue.ExternalRef == ref {
			return issue, nil
		}
	}
	return nil, nil
}
func (s *overlapTestStore) GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (s *overlapTestStore) GetDependenciesWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (s *overlapTestStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	s.creates++
	s.issues = append(s.issues, issue)
	return nil
}
func (s *overlapTestStore) UpdateIssue(context.Context, string, map[string]interface{}, string) error {
	s.updates++
	return nil
}
func (s *overlapTestStore) ApplyIssueUpdate(context.Context, string, map[string]interface{}, []string, string) error {
	s.updates++
	return nil
}
func (s *overlapTestStore) AddDependency(context.Context, *types.Dependency, string) error {
	s.deps++
	return nil
}

// TestEnginePullOverlapsPreviousSync covers a remote issue that became
// visible to the tracker's list endpoint only after the previous sync ended,
// so its updated_at is slightly before the stored last_sync. The incremental
// fetch window must still include it, while an unchanged issue re-fetched by
// the same overlap must not be written or counted as updated.
func TestEnginePullOverlapsPreviousSync(t *testing.T) {
	ctx := context.Background()
	lastSync := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)

	unchangedRef := "https://github.test/1"
	store := &overlapTestStore{
		metadata: map[string]string{"github.last_sync": lastSync.Format(time.RFC3339Nano)},
		issues: []*types.Issue{{
			ID:          "bd-1",
			Title:       "Already synced",
			Description: "same",
			Priority:    2,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			ExternalRef: &unchangedRef,
			CreatedAt:   lastSync.Add(-time.Hour),
			UpdatedAt:   lastSync.Add(-30 * time.Second),
		}},
	}

	tr := newMockTracker("github")
	tr.issues = []TrackerIssue{
		{ID: "1", Identifier: "1", URL: unchangedRef, Title: "Already synced", Description: "same", UpdatedAt: lastSync.Add(-30 * time.Second)},
		{ID: "2", Identifier: "2", URL: "https://github.test/2", Title: "Created just before sync ended", UpdatedAt: lastSync.Add(-5 * time.Second)},
	}
	var gotSince *time.Time
	tr.fetchIssues = func(_ context.Context, opts FetchOptions) ([]TrackerIssue, error) {
		gotSince = opts.Since
		var out []TrackerIssue
		for _, issue := range tr.issues {
			if opts.Since == nil || !issue.UpdatedAt.Before(*opts.Since) {
				out = append(out, issue)
			}
		}
		return out, nil
	}

	engine := NewEngine(tr, store, "test-actor")
	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync error: %v", err)
	}

	if !result.PullStats.Incremental {
		t.Fatalf("PullStats.Incremental = false, want true")
	}
	if result.PullStats.Created != 1 || store.creates != 1 {
		t.Fatalf("created = %d (store %d), want the late-visible issue imported once", result.PullStats.Created, store.creates)
	}
	if got := store.issues[len(store.issues)-1]; got.Title != "Created just before sync ended" {
		t.Fatalf("created issue title = %q, want the late-visible remote issue", got.Title)
	}
	if result.PullStats.Updated != 0 || store.updates != 0 {
		t.Fatalf("updated = %d (store writes %d), want re-fetched unchanged issue to be a no-op", result.PullStats.Updated, store.updates)
	}
	if store.deps != 0 {
		t.Fatalf("dependency writes = %d, want 0", store.deps)
	}
	if gotSince == nil {
		t.Fatal("FetchIssues Since = nil, want an incremental fetch")
	}
	if want := lastSync.Add(-PullFetchOverlap); !gotSince.Equal(want) {
		t.Fatalf("FetchIssues Since = %v, want last_sync - PullFetchOverlap = %v", gotSince, want)
	}
}

// TestEnginePullOverlapKeepsLocalEditMadeBeforeLastSync covers an unpushed
// local edit made before last_sync on an issue the remote also changed inside
// the overlap window. The guard's last_sync threshold does not protect it, so
// the remote's updated_at must: the local copy changed after the remote did
// and pull must not overwrite it.
func TestEnginePullOverlapKeepsLocalEditMadeBeforeLastSync(t *testing.T) {
	ctx := context.Background()
	lastSync := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)

	ref := "https://github.test/1"
	store := &overlapTestStore{
		metadata: map[string]string{"github.last_sync": lastSync.Format(time.RFC3339Nano)},
		issues: []*types.Issue{{
			ID:          "bd-1",
			Title:       "Edited locally",
			Description: "local content",
			Priority:    2,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			ExternalRef: &ref,
			CreatedAt:   lastSync.Add(-time.Hour),
			UpdatedAt:   lastSync.Add(-10 * time.Second),
		}},
	}

	tr := newMockTracker("github")
	tr.issues = []TrackerIssue{
		{ID: "1", Identifier: "1", URL: ref, Title: "Edited remotely", Description: "remote content", UpdatedAt: lastSync.Add(-2 * time.Minute)},
	}

	engine := NewEngine(tr, store, "test-actor")
	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync error: %v", err)
	}

	if store.updates != 0 || result.PullStats.Updated != 0 {
		t.Fatalf("updated = %d (store writes %d), want the local edit kept", result.PullStats.Updated, store.updates)
	}
	if result.PullStats.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 for the protected local edit", result.PullStats.Skipped)
	}
}

// TestEnginePullOverlapAppliesRemoteChangeOlderThanLocalCopy is the other
// side of the same guard: when the remote changed after the local copy did,
// the re-fetched remote content still wins.
func TestEnginePullOverlapAppliesRemoteChangeOlderThanLocalCopy(t *testing.T) {
	ctx := context.Background()
	lastSync := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)

	ref := "https://github.test/1"
	store := &overlapTestStore{
		metadata: map[string]string{"github.last_sync": lastSync.Format(time.RFC3339Nano)},
		issues: []*types.Issue{{
			ID:          "bd-1",
			Title:       "Stale",
			Description: "old",
			Priority:    2,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			ExternalRef: &ref,
			CreatedAt:   lastSync.Add(-time.Hour),
			UpdatedAt:   lastSync.Add(-3 * time.Minute),
		}},
	}

	tr := newMockTracker("github")
	tr.issues = []TrackerIssue{
		{ID: "1", Identifier: "1", URL: ref, Title: "Changed remotely", Description: "new", UpdatedAt: lastSync.Add(-10 * time.Second)},
	}

	engine := NewEngine(tr, store, "test-actor")
	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync error: %v", err)
	}

	if store.updates != 1 || result.PullStats.Updated != 1 {
		t.Fatalf("updated = %d (store writes %d), want the late-visible remote change applied", result.PullStats.Updated, store.updates)
	}
}
