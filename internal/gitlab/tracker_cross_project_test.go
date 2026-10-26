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

func TestTrackerMatchKey(t *testing.T) {
	const keyB7 = "gl://gitlab.com/group-b/project-b#7"
	tests := []struct {
		name        string
		baseURL     string
		projectID   string
		projectPath string
		ref         string
		want        string
	}{
		{name: "issue URL", ref: "https://gitlab.com/group-b/project-b/-/issues/7", want: keyB7},
		{name: "other project same IID", ref: "https://gitlab.com/group-a/project-a/-/issues/7", want: "gl://gitlab.com/group-a/project-a#7"},
		{name: "work item shares the issue IID space", ref: "https://gitlab.com/group-b/project-b/-/work_items/7", want: keyB7},
		{name: "legacy issue path", ref: "https://gitlab.com/group-b/project-b/issues/7", want: keyB7},
		{name: "legacy work item path", ref: "https://gitlab.com/group-b/project-b/work_items/7", want: keyB7},
		{name: "host and path case", ref: "https://GitLab.COM/Group-B/Project-B/-/Issues/7", want: keyB7},
		{name: "explicit https port", ref: "https://gitlab.com:443/group-b/project-b/-/issues/7", want: keyB7},
		{name: "explicit http port", ref: "http://gitlab.com:80/group-b/project-b/-/issues/7", want: keyB7},
		{name: "non-default port kept", ref: "https://gitlab.com:8443/group-b/project-b/-/issues/7", want: "gl://gitlab.com:8443/group-b/project-b#7"},
		{name: "trailing and repeated slashes", ref: "https://gitlab.com//group-b/project-b/-/issues/7/", want: keyB7},
		{name: "query and fragment ignored", ref: "https://gitlab.com/group-b/project-b/-/issues/7?x=1#note_3", want: keyB7},
		{name: "sibling prefix project", ref: "https://gitlab.com/group-b/project-b-other/-/issues/7", want: "gl://gitlab.com/group-b/project-b-other#7"},
		{name: "nested group", ref: "https://gitlab.com/group-b/sub/project-b/-/issues/7", want: "gl://gitlab.com/group-b/sub/project-b#7"},
		{name: "nested group legacy", ref: "https://gitlab.com/group-b/sub/project-b/issues/7", want: "gl://gitlab.com/group-b/sub/project-b#7"},
		{name: "self-hosted sub-path", ref: "https://example.com/gitlab/group-b/project-b/-/issues/7", want: "gl://example.com/gitlab/group-b/project-b#7"},
		{name: "project milestone", ref: "https://gitlab.com/group-b/project-b/-/milestones/7", want: "gl://gitlab.com/group-b/project-b#milestone:7"},
		{name: "group milestone", ref: "https://gitlab.com/groups/group-b/-/milestones/7", want: "gl://gitlab.com/groups/group-b#milestone:7"},
		{name: "shorthand with project path", baseURL: "https://GitLab.com/", projectID: "200", projectPath: "Group-B/Project-B", ref: "gitlab:7", want: keyB7},
		{name: "shorthand with path-form project id", baseURL: "https://gitlab.com", projectID: "group-b/project-b", ref: "gitlab:7", want: keyB7},
		{name: "shorthand under sub-path", baseURL: "https://example.com/gitlab", projectPath: "group-b/project-b", ref: "gitlab:7", want: "gl://example.com/gitlab/group-b/project-b#7"},
		{name: "IID zero", ref: "https://gitlab.com/group-b/project-b/-/issues/0", want: ""},
		{name: "no project", ref: "https://gitlab.com/-/issues/7", want: ""},
		{name: "not a resource", ref: "https://gitlab.com/group-b/project-b/-/issues/7/notes", want: ""},
		{name: "no host", ref: "/group-b/project-b/-/issues/7", want: ""},
		{name: "garbage", ref: "not a ref", want: ""},
		{name: "empty", ref: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL := tt.baseURL
			if baseURL == "" {
				baseURL = "https://gitlab.com"
			}
			projectID := tt.projectID
			if projectID == "" {
				projectID = "200"
			}
			tr := &Tracker{client: NewClient("token", baseURL, projectID), projectPath: tt.projectPath}
			if got := tr.MatchKey(tt.ref); got != tt.want {
				t.Errorf("MatchKey(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

// TestPull_SameProjectRespellingUpdatesExistingBead pins that project-scoped
// matching still finds a bead linked to the pulled issue under another
// spelling of its URL (or the shorthand, when the project is configured by
// path), and relinks it to GitLab's web_url instead of creating a duplicate.
func TestPull_SameProjectRespellingUpdatesExistingBead(t *testing.T) {
	tests := []struct {
		name        string
		stored      string // %s and %S are the configured host:port, lower- and upper-cased
		webURL      string
		projectID   string
		projectPath string
	}{
		{name: "work item URL", stored: "https://gitlab.com/group-b/project-b/-/work_items/7", webURL: "https://gitlab.com/group-b/project-b/-/issues/7"},
		{name: "issue URL under work item web_url", stored: "https://gitlab.com/group-b/project-b/-/issues/7", webURL: "https://gitlab.com/group-b/project-b/-/work_items/7"},
		{name: "legacy issue URL", stored: "https://gitlab.com/group-b/project-b/issues/7", webURL: "https://gitlab.com/group-b/project-b/-/issues/7"},
		{name: "path case", stored: "https://gitlab.com/Group-B/Project-B/-/issues/7", webURL: "https://gitlab.com/group-b/project-b/-/issues/7"},
		{name: "self-hosted host and path case", stored: "http://%S/Group-B/Project-B/-/issues/7", webURL: "http://%s/group-b/project-b/-/issues/7"},
		{name: "explicit default port", stored: "https://gitlab.com:443/group-b/project-b/-/issues/7", webURL: "https://gitlab.com/group-b/project-b/-/issues/7"},
		{name: "shorthand with project path", stored: "gitlab:7", webURL: "http://%s/group-b/project-b/-/issues/7", projectPath: "group-b/project-b"},
		{name: "shorthand with path-form project id", stored: "gitlab:7", webURL: "http://%s/group-b/project-b/-/issues/7", projectID: "group-b/project-b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var webURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/issues/7") {
					http.Error(w, "unexpected", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(gitLabIssueJSON(9007, 7, "Project B issue 7 (remote)", webURL)))
			}))
			t.Cleanup(srv.Close)
			// Configure the server by name so the self-hosted host has a case
			// to differ in; onConfiguredHost recognizes it case-insensitively.
			host := "localhost:" + srv.URL[strings.LastIndex(srv.URL, ":")+1:]
			expand := strings.NewReplacer("%s", host, "%S", strings.ToUpper(host)).Replace
			webURL = expand(tt.webURL)

			projectID := tt.projectID
			if projectID == "" {
				projectID = "200"
			}
			tr := &Tracker{client: NewClient("token", "http://"+host, projectID), config: DefaultMappingConfig(), projectPath: tt.projectPath}

			stored := expand(tt.stored)
			store := &crossProjectStore{issues: map[string]*types.Issue{
				"bd-b7": {ID: "bd-b7", Title: "Project B issue 7", Status: types.StatusOpen, IssueType: types.TypeTask, ExternalRef: &stored},
			}}
			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true, IssueIDs: []string{"7"}})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Created != 0 || result.PullStats.Updated != 1 {
				t.Errorf("re-pull with stored ref %q: created=%d updated=%d, want created=0 updated=1",
					stored, result.PullStats.Created, result.PullStats.Updated)
			}
			b := store.issues["bd-b7"]
			if b.ExternalRef == nil || *b.ExternalRef != webURL {
				t.Errorf("bead external_ref = %v, want web_url %q", derefRef(b.ExternalRef), webURL)
			}
			if len(store.issues) != 1 {
				t.Errorf("store holds %d beads, want 1", len(store.issues))
			}
		})
	}
}
