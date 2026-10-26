package tracker

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// matchKeyStore is a minimal in-memory Store + IssueUpdater for pull tests.
type matchKeyStore struct {
	issues map[string]*types.Issue
	next   int
}

func (s *matchKeyStore) GetConfig(context.Context, string) (string, error) { return "", nil }
func (s *matchKeyStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (s *matchKeyStore) GetLocalMetadata(context.Context, string) (string, error) {
	return "", nil
}
func (s *matchKeyStore) SetLocalMetadata(context.Context, string, string) error { return nil }
func (s *matchKeyStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	out := make([]*types.Issue, 0, len(s.issues))
	for _, iss := range s.issues {
		out = append(out, iss)
	}
	return out, nil
}
func (s *matchKeyStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, iss := range s.issues {
		if iss.ExternalRef != nil && *iss.ExternalRef == ref {
			return iss, nil
		}
	}
	return nil, nil
}
func (s *matchKeyStore) GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (s *matchKeyStore) GetDependenciesWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (s *matchKeyStore) CreateIssue(_ context.Context, iss *types.Issue, _ string) error {
	s.next++
	if iss.ID == "" {
		iss.ID = "bd-new" + string(rune('0'+s.next))
	}
	s.issues[iss.ID] = iss
	return nil
}
func (s *matchKeyStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	iss := s.issues[id]
	if iss == nil {
		return nil
	}
	if v, ok := updates["title"].(string); ok {
		iss.Title = v
	}
	if v, ok := updates["external_ref"].(string); ok {
		iss.ExternalRef = &v
	}
	return nil
}
func (s *matchKeyStore) ApplyIssueUpdate(ctx context.Context, id string, updates map[string]interface{}, _ []string, actor string) error {
	return s.UpdateIssue(ctx, id, updates, actor)
}
func (s *matchKeyStore) AddDependency(context.Context, *types.Dependency, string) error {
	return nil
}

// matchKeyTracker is a mockTracker whose refs carry a repo scope the bare
// ExtractIdentifier drops: URLs look like https://mk.test/{repo}/-/{kind}/{n},
// ExtractIdentifier returns {n} (the mock's last path segment), and MatchKey
// returns {repo}#{n} so same-numbered issues of different repos cannot
// fallback-match each other.
type matchKeyTracker struct {
	*mockTracker
}

func (m *matchKeyTracker) MatchKey(ref string) string {
	segs := strings.Split(strings.Trim(ref, "/"), "/")
	if len(segs) < 3 {
		return ""
	}
	repo := segs[len(segs)-3]
	if repo == "-" {
		if len(segs) < 4 {
			return ""
		}
		repo = segs[len(segs)-4]
	}
	if repo == "" || repo == "-" {
		return ""
	}
	return repo + "#" + segs[len(segs)-1]
}

// TestEnginePullPrefersMatchKeyer pins the engine half of tracker.MatchKeyer:
// the pull fallback map is keyed by MatchKey when the tracker implements it,
// so an issue whose bare identifier collides with another repo's bead creates
// instead of updating, while a ref sharing the key under a different spelling
// still updates in place.
func TestEnginePullPrefersMatchKeyer(t *testing.T) {
	newStore := func() *matchKeyStore {
		ref := "https://mk.test/repo-a/-/issues/7"
		return &matchKeyStore{issues: map[string]*types.Issue{
			"bd-a7": {
				ID:          "bd-a7",
				Title:       "Repo A issue 7",
				Status:      types.StatusOpen,
				IssueType:   types.TypeTask,
				ExternalRef: &ref,
			},
		}}
	}

	t.Run("same bare identifier in another scope creates", func(t *testing.T) {
		store := newStore()
		mock := newMockTracker("mk")
		mock.issues = []TrackerIssue{{
			ID: "b7", Identifier: "7", URL: "https://mk.test/repo-b/-/issues/7",
			Title: "Repo B issue 7",
		}}
		result, err := NewEngine(&matchKeyTracker{mockTracker: mock}, store, "test").
			Sync(context.Background(), SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Created != 1 || result.PullStats.Updated != 0 {
			t.Fatalf("pull stats: created=%d updated=%d, want created=1 updated=0",
				result.PullStats.Created, result.PullStats.Updated)
		}
		bead := store.issues["bd-a7"]
		if bead.Title != "Repo A issue 7" || bead.ExternalRef == nil || *bead.ExternalRef != "https://mk.test/repo-a/-/issues/7" {
			t.Errorf("repo-a bead overwritten by repo-b pull: title=%q external_ref=%q", bead.Title, derefMatchKeyRef(bead.ExternalRef))
		}
	})

	t.Run("same key under another spelling updates in place", func(t *testing.T) {
		store := newStore()
		mock := newMockTracker("mk")
		mock.issues = []TrackerIssue{{
			ID: "a7", Identifier: "7", URL: "https://mk.test/repo-a/-/work_items/7",
			Title: "Repo A issue 7 updated",
		}}
		result, err := NewEngine(&matchKeyTracker{mockTracker: mock}, store, "test").
			Sync(context.Background(), SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Updated != 1 || result.PullStats.Created != 0 {
			t.Fatalf("pull stats: created=%d updated=%d, want created=0 updated=1",
				result.PullStats.Created, result.PullStats.Updated)
		}
		bead := store.issues["bd-a7"]
		if bead.Title != "Repo A issue 7 updated" || bead.ExternalRef == nil || *bead.ExternalRef != "https://mk.test/repo-a/-/work_items/7" {
			t.Errorf("same-key bead not healed: title=%q external_ref=%q", bead.Title, derefMatchKeyRef(bead.ExternalRef))
		}
	})
}

func derefMatchKeyRef(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
