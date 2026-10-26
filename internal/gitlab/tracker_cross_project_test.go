package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// crossProjectStore is a minimal in-memory tracker.Store for pull tests.
type crossProjectStore struct {
	issues map[string]*types.Issue
	next   int
}

func (s *crossProjectStore) GetConfig(context.Context, string) (string, error) { return "", nil }
func (s *crossProjectStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (s *crossProjectStore) GetLocalMetadata(context.Context, string) (string, error) {
	return "", nil
}
func (s *crossProjectStore) SetLocalMetadata(context.Context, string, string) error { return nil }
func (s *crossProjectStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	out := make([]*types.Issue, 0, len(s.issues))
	for _, iss := range s.issues {
		out = append(out, iss)
	}
	return out, nil
}
func (s *crossProjectStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, iss := range s.issues {
		if iss.ExternalRef != nil && *iss.ExternalRef == ref {
			return iss, nil
		}
	}
	return nil, nil
}
func (s *crossProjectStore) GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (s *crossProjectStore) GetDependenciesWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (s *crossProjectStore) CreateIssue(_ context.Context, iss *types.Issue, _ string) error {
	s.next++
	if iss.ID == "" {
		iss.ID = "bd-new" + string(rune('0'+s.next))
	}
	s.issues[iss.ID] = iss
	return nil
}
func (s *crossProjectStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
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
func (s *crossProjectStore) ApplyIssueUpdate(ctx context.Context, id string, updates map[string]interface{}, _ []string, actor string) error {
	return s.UpdateIssue(ctx, id, updates, actor)
}
func (s *crossProjectStore) AddDependency(context.Context, *types.Dependency, string) error {
	return nil
}

// TestPull_SameIIDInOtherProjectCreatesNewBead reproduces gastownhall/beads#7795:
// a bead linked to project A's issue 7 must not be matched (and overwritten)
// when pulling issue 7 from project B into the same database.
func TestPull_SameIIDInOtherProjectCreatesNewBead(t *testing.T) {
	const projectBRef = "https://gitlab.com/group-b/project-b/-/issues/7"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/api/v4") != "/projects/200/issues/7" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":9007,"iid":7,"project_id":200,"title":"Project B issue 7","state":"opened",` +
			`"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z","web_url":"` + projectBRef + `"}`))
	}))
	t.Cleanup(srv.Close)

	tr := &Tracker{client: NewClient("token", srv.URL, "200"), config: DefaultMappingConfig()}

	projectARef := "https://gitlab.com/group-a/project-a/-/issues/7"
	store := &crossProjectStore{issues: map[string]*types.Issue{
		"bd-a7": {
			ID:          "bd-a7",
			Title:       "Project A issue 7",
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			ExternalRef: &projectARef,
		},
	}}

	engine := tracker.NewEngine(tr, store, "test")
	result, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true, IssueIDs: []string{"7"}})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	a := store.issues["bd-a7"]
	if a.Title != "Project A issue 7" || a.ExternalRef == nil || *a.ExternalRef != projectARef {
		t.Errorf("project A bead overwritten by project B pull: title=%q external_ref=%v", a.Title, derefRef(a.ExternalRef))
	}
	if result.PullStats.Created != 1 || result.PullStats.Updated != 0 {
		t.Errorf("pull of project B issue 7: created=%d updated=%d, want created=1 updated=0",
			result.PullStats.Created, result.PullStats.Updated)
	}
}

func derefRef(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// newProjectBServer serves one issue (IID 7) of project B over the GitLab
// REST API, with the canonical issues-spelled web_url.
func newProjectBServer(t *testing.T) *httptest.Server {
	t.Helper()
	const projectBRef = "https://gitlab.com/group-b/project-b/-/issues/7"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/api/v4") != "/projects/200/issues/7" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":9007,"iid":7,"project_id":200,"title":"Project B issue 7","state":"opened",` +
			`"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z","web_url":"` + projectBRef + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pullProjectB7 runs a selective pull of issue 7 against a fresh project-B
// tracker backed by one existing bead carrying ref, and returns the result
// and the store.
func pullProjectB7(t *testing.T, ref string) (*tracker.SyncResult, *crossProjectStore) {
	t.Helper()
	srv := newProjectBServer(t)
	tr := &Tracker{client: NewClient("token", srv.URL, "200"), config: DefaultMappingConfig()}
	store := &crossProjectStore{issues: map[string]*types.Issue{
		"bd-b7": {
			ID:          "bd-b7",
			Title:       "Stale title",
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			ExternalRef: &ref,
		},
	}}
	engine := tracker.NewEngine(tr, store, "test")
	result, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true, IssueIDs: []string{"7"}})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return result, store
}

// TestPull_SameProjectRefVariantsUpdateInPlace pins the healing half of the
// project-qualified fallback key: a bead linked to the configured project's
// issue by any spelling GitLab has emitted must still be found on re-pull —
// updated in place with its external_ref rewritten to the canonical web_url —
// instead of duplicated.
func TestPull_SameProjectRefVariantsUpdateInPlace(t *testing.T) {
	const projectBRef = "https://gitlab.com/group-b/project-b/-/issues/7"
	variants := []struct {
		name string
		ref  string
	}{
		{"work items spelling", "https://gitlab.com/group-b/project-b/-/work_items/7"},
		{"legacy url without separator", "https://gitlab.com/group-b/project-b/issues/7"},
		{"path case", "https://gitlab.com/Group-B/Project-B/-/issues/7"},
		{"explicit default port", "https://gitlab.com:443/group-b/project-b/-/issues/7"},
		{"trailing slash", "https://gitlab.com/group-b/project-b/-/issues/7/"},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			result, store := pullProjectB7(t, v.ref)
			if result.PullStats.Updated != 1 || result.PullStats.Created != 0 {
				t.Fatalf("pull stats: created=%d updated=%d, want created=0 updated=1",
					result.PullStats.Created, result.PullStats.Updated)
			}
			bead := store.issues["bd-b7"]
			if len(store.issues) != 1 {
				t.Fatalf("store holds %d issues, want the single existing bead", len(store.issues))
			}
			if bead.Title != "Project B issue 7" {
				t.Errorf("title = %q, want updated from pull", bead.Title)
			}
			if bead.ExternalRef == nil || *bead.ExternalRef != projectBRef {
				t.Errorf("external_ref = %v, want canonical web_url %q", derefRef(bead.ExternalRef), projectBRef)
			}
		})
	}
}
