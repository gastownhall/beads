package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// gitLabIssueJSON renders one GitLab REST issue object for pull fixtures.
func gitLabIssueJSON(id, iid int, title, webURL string) string {
	return `{"id":` + strconv.Itoa(id) + `,"iid":` + strconv.Itoa(iid) + `,"title":"` + title + `","state":"opened",` +
		`"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z","web_url":"` + webURL + `"}`
}

// newIssueListServer serves listPath (a project or group issues listing) with
// the given issue objects and answers every other request 404, which the
// tracker treats as "no links" for per-issue link enrichment.
func newIssueListServer(t *testing.T, listPath string, issues ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/api/v4") != listPath {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[" + strings.Join(issues, ",") + "]"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPull_BulkSameIIDInOtherProjectCreatesNewBead is the bulk-pull form of
// gastownhall/beads#7795: a full pull of project B must not fall back onto a
// bead linked to project A's issue with the same IID.
func TestPull_BulkSameIIDInOtherProjectCreatesNewBead(t *testing.T) {
	const projectBRef = "https://gitlab.com/group-b/project-b/-/issues/7"
	srv := newIssueListServer(t, "/projects/200/issues", gitLabIssueJSON(9007, 7, "Project B issue 7", projectBRef))
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

	result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	a := store.issues["bd-a7"]
	if a.Title != "Project A issue 7" || a.ExternalRef == nil || *a.ExternalRef != projectARef {
		t.Errorf("project A bead overwritten by project B pull: title=%q external_ref=%v", a.Title, derefRef(a.ExternalRef))
	}
	if result.PullStats.Created != 1 || result.PullStats.Updated != 0 {
		t.Errorf("bulk pull of project B: created=%d updated=%d, want created=1 updated=0",
			result.PullStats.Created, result.PullStats.Updated)
	}
	var newRef string
	for id, iss := range store.issues {
		if id != "bd-a7" && iss.ExternalRef != nil {
			newRef = *iss.ExternalRef
		}
	}
	if newRef != projectBRef {
		t.Errorf("new bead external_ref = %q, want %q", newRef, projectBRef)
	}
}
