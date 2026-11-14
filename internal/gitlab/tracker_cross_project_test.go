package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// crossProjectStore is a minimal in-memory tracker.Store for pull tests.
// SearchIssues lists issues lowest ID first, so a resolver that keeps the
// first bead per key is deterministic, and AddDependency records the edge.
// GetIssueByExternalRef reports a miss as a wrapped storage.ErrNotFound, as
// DoltStore does.
type crossProjectStore struct {
	issues map[string]*types.Issue
	deps   []types.Dependency
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
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *crossProjectStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, iss := range s.issues {
		if iss.ExternalRef != nil && *iss.ExternalRef == ref {
			return iss, nil
		}
	}
	return nil, fmt.Errorf("%w: external_ref %s", storage.ErrNotFound, ref)
}
func (s *crossProjectStore) GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (s *crossProjectStore) GetDependenciesWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var out []*types.IssueWithDependencyMetadata
	for _, dep := range s.deps {
		if target := s.issues[dep.DependsOnID]; dep.IssueID == issueID && target != nil {
			out = append(out, &types.IssueWithDependencyMetadata{Issue: *target, DependencyType: dep.Type})
		}
	}
	return out, nil
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
func (s *crossProjectStore) AddDependency(_ context.Context, dep *types.Dependency, _ string) error {
	s.deps = append(s.deps, *dep)
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

// TestPull_GroupModeSameIIDStableAcrossProjects pins group mode under the
// project-qualified fallback key: one bulk pull spanning two group projects
// that both have issue 7 must update each project's bead in place, and no
// bead may be rewritten to the other project's URL. Beads are linked with
// work_items-spelled web_urls while the pull returns issues-spelled ones, the
// spelling drift the fallback exists to heal — and the drift that, keyed by
// bare IID alone, made one bead capture both projects' updates.
func TestPull_GroupModeSameIIDStableAcrossProjects(t *testing.T) {
	const (
		projectAIssuesRef = "https://gitlab.com/group-a/project-a/-/issues/7"
		projectBIssuesRef = "https://gitlab.com/group-b/project-b/-/issues/7"
	)
	projectAWorkItemRef := "https://gitlab.com/group-a/project-a/-/work_items/7"
	projectBWorkItemRef := "https://gitlab.com/group-b/project-b/-/work_items/7"
	srv := newIssueListServer(t, "/groups/30/issues",
		gitLabIssueJSON(8007, 7, "Project A issue 7", projectAIssuesRef),
		gitLabIssueJSON(9007, 7, "Project B issue 7", projectBIssuesRef))

	tr := &Tracker{client: NewClient("token", srv.URL, "").WithGroupID("30"), config: DefaultMappingConfig()}
	store := &crossProjectStore{issues: map[string]*types.Issue{
		"bd-a7": {
			ID:          "bd-a7",
			Title:       "Project A issue 7 stale",
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			ExternalRef: &projectAWorkItemRef,
		},
		"bd-b7": {
			ID:          "bd-b7",
			Title:       "Project B issue 7 stale",
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			ExternalRef: &projectBWorkItemRef,
		},
	}}
	engine := tracker.NewEngine(tr, store, "test")

	first, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if first.PullStats.Created != 0 || first.PullStats.Updated != 2 {
		t.Fatalf("first pull stats: created=%d updated=%d, want created=0 updated=2 (each bead in place)",
			first.PullStats.Created, first.PullStats.Updated)
	}
	// Assert per-bead state right after the first pull: keyed by bare IID,
	// one bead absorbs both projects' updates (ending on whichever issue is
	// processed last) and the other is never touched, so at least one bead
	// holds neither its own issues url nor an updated title.
	if len(store.issues) != 2 {
		t.Fatalf("store holds %d issues, want the original two", len(store.issues))
	}
	for id, wantRef := range map[string]string{
		"bd-a7": projectAIssuesRef,
		"bd-b7": projectBIssuesRef,
	} {
		bead := store.issues[id]
		if bead.ExternalRef == nil || *bead.ExternalRef != wantRef {
			t.Errorf("%s external_ref = %v after first pull, want its own project's issues url %q", id, derefRef(bead.ExternalRef), wantRef)
		}
	}

	second, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if second.PullStats.Created != 0 {
		t.Fatalf("second pull created=%d, want 0", second.PullStats.Created)
	}
}

// TestPull_ProjectlessShorthandBead pins the legacy "gitlab:{iid}" ref, which
// names no project: with no project path configured, a URL pull of another
// project's issue 7 must not fall back onto it; once the configured project's
// path is known, its issue 7 is the one that relinks the shorthand bead.
func TestPull_ProjectlessShorthandBead(t *testing.T) {
	tests := []struct {
		name        string
		projectID   string
		projectPath string
		wantCreated int
		wantUpdated int
	}{
		{name: "numeric project id, no path", projectID: "200", wantCreated: 1},
		{name: "project path matches the pulled project", projectID: "200", projectPath: "group-b/project-b", wantUpdated: 1},
		{name: "path-form project id matches the pulled project", projectID: "group-b/project-b", wantUpdated: 1},
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
				_, _ = w.Write([]byte(gitLabIssueJSON(9007, 7, "Project B issue 7", webURL)))
			}))
			t.Cleanup(srv.Close)
			webURL = srv.URL + "/group-b/project-b/-/issues/7"
			tr := &Tracker{client: NewClient("token", srv.URL, tt.projectID), config: DefaultMappingConfig(), projectPath: tt.projectPath}

			shorthand := "gitlab:7"
			store := &crossProjectStore{issues: map[string]*types.Issue{
				"bd-s7": {ID: "bd-s7", Title: "Shorthand issue 7", Status: types.StatusOpen, IssueType: types.TypeTask, ExternalRef: &shorthand},
			}}
			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true, IssueIDs: []string{"7"}})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Created != tt.wantCreated || result.PullStats.Updated != tt.wantUpdated {
				t.Errorf("created=%d updated=%d, want created=%d updated=%d",
					result.PullStats.Created, result.PullStats.Updated, tt.wantCreated, tt.wantUpdated)
			}
			s := store.issues["bd-s7"]
			wantTitle, wantRef := "Shorthand issue 7", shorthand
			if tt.wantUpdated == 1 {
				wantTitle, wantRef = "Project B issue 7", webURL
			}
			if s.Title != wantTitle || derefRef(s.ExternalRef) != wantRef {
				t.Errorf("shorthand bead = {title:%q external_ref:%q}, want {title:%q external_ref:%q}",
					s.Title, derefRef(s.ExternalRef), wantTitle, wantRef)
			}
		})
	}
	t.Run("unqualified shorthand key cannot equal a URL key", func(t *testing.T) {
		tr := &Tracker{client: NewClient("token", "https://gitlab.com", "200")}
		if got := tr.MatchKey("gitlab:7"); got != "gitlab:7" {
			t.Errorf(`MatchKey("gitlab:7") = %q, want "gitlab:7"`, got)
		}
	})
}

// gitLabProjectIssueJSON renders a GitLab REST issue object that carries its
// project_id, as the issues API returns it.
func gitLabProjectIssueJSON(id, iid, projectID int, title, webURL string) string {
	return `{"id":` + strconv.Itoa(id) + `,"iid":` + strconv.Itoa(iid) + `,"project_id":` + strconv.Itoa(projectID) +
		`,"title":"` + title + `","state":"opened","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z","web_url":"` + webURL + `"}`
}

// gitLabLinkJSON renders one entry of the issue-links list API: the linked
// issue itself, with the link type seen from the listed issue.
func gitLabLinkJSON(iid, projectID int, webURL, linkType string) string {
	return `{"iid":` + strconv.Itoa(iid) + `,"project_id":` + strconv.Itoa(projectID) +
		`,"web_url":"` + webURL + `","link_type":"` + linkType + `"}`
}

// newGitLabServer answers each API path in routes with a JSON array of its
// entries, and every other request 404.
func newGitLabServer(t *testing.T, routes map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries, ok := routes[strings.TrimPrefix(r.URL.Path, "/api/v4")]
		if !ok {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[" + strings.Join(entries, ",") + "]"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// beadIDByRef returns the ID of the bead linked to ref, or "".
func beadIDByRef(store *crossProjectStore, ref string) string {
	for id, iss := range store.issues {
		if iss.ExternalRef != nil && *iss.ExternalRef == ref {
			return id
		}
	}
	return ""
}

// dependencyEdges renders the store's recorded dependencies, sorted.
func dependencyEdges(store *crossProjectStore) []string {
	edges := make([]string, 0, len(store.deps))
	for _, dep := range store.deps {
		edges = append(edges, dep.IssueID+" -> "+dep.DependsOnID+" "+string(dep.Type))
	}
	sort.Strings(edges)
	return edges
}

// TestPull_DependencyEndpointsResolveByProject is the dependency-pass form of
// gastownhall/beads#7795: with a bead linked to project A's issue 7 in the
// database, pulling project B, whose issue 7 blocks its issue 8, must put the
// edge on B's beads. Named by bare IID, the endpoint "7" resolved to whichever
// bead claimed IID 7 first, here project A's.
func TestPull_DependencyEndpointsResolveByProject(t *testing.T) {
	const (
		projectARef7 = "https://gitlab.com/group-a/project-a/-/issues/7"
		projectBRef7 = "https://gitlab.com/group-b/project-b/-/issues/7"
		projectBRef8 = "https://gitlab.com/group-b/project-b/-/issues/8"
	)
	srv := newGitLabServer(t, map[string][]string{
		"/projects/200/issues": {
			gitLabProjectIssueJSON(9007, 7, 200, "Project B issue 7", projectBRef7),
			gitLabProjectIssueJSON(9008, 8, 200, "Project B issue 8", projectBRef8),
		},
		"/projects/200/issues/7/links": {gitLabLinkJSON(8, 200, projectBRef8, gitLabLinkBlocks)},
	})
	tr := &Tracker{client: NewClient("token", srv.URL, "200"), config: DefaultMappingConfig()}
	aRef := projectARef7
	store := &crossProjectStore{issues: map[string]*types.Issue{
		"bd-a7": {ID: "bd-a7", Title: "Project A issue 7", Status: types.StatusOpen, IssueType: types.TypeTask, ExternalRef: &aRef},
	}}

	if _, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	b7, b8 := beadIDByRef(store, projectBRef7), beadIDByRef(store, projectBRef8)
	if b7 == "" || b8 == "" {
		t.Fatalf("pull did not import project B's issues: issue 7 bead %q, issue 8 bead %q", b7, b8)
	}
	want := []string{b8 + " -> " + b7 + " blocks"}
	if got := dependencyEdges(store); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("dependencies = %q, want %q (project A's bead bd-a7 must carry none)", got, want)
	}
}

// TestPull_GroupModeDependenciesStayInTheirProject pins link enrichment and
// endpoints in group mode, where one pull spans project A (id 100) and project
// B (id 200), which both have an issue 7. Each issue's links are listed in its
// own project, so B's "7 blocks 8" lands on B's beads and not on A's issue 7,
// and A#7's link to B#7, two issues with one IID, is an edge between them
// rather than a dropped self-link.
func TestPull_GroupModeDependenciesStayInTheirProject(t *testing.T) {
	const (
		projectARef7 = "https://gitlab.com/group-a/project-a/-/issues/7"
		projectBRef7 = "https://gitlab.com/group-b/project-b/-/issues/7"
		projectBRef8 = "https://gitlab.com/group-b/project-b/-/issues/8"
	)
	srv := newGitLabServer(t, map[string][]string{
		"/groups/30/issues": {
			gitLabProjectIssueJSON(8007, 7, 100, "Project A issue 7", projectARef7),
			gitLabProjectIssueJSON(9007, 7, 200, "Project B issue 7", projectBRef7),
			gitLabProjectIssueJSON(9008, 8, 200, "Project B issue 8", projectBRef8),
		},
		"/projects/100/issues/7/links": {gitLabLinkJSON(7, 200, projectBRef7, gitLabLinkRelatesTo)},
		"/projects/200/issues/7/links": {gitLabLinkJSON(8, 200, projectBRef8, gitLabLinkBlocks)},
	})
	// Project B is the group's default project (gitlab.default_project_id),
	// the one a configured-project link listing would read for every issue 7.
	tr := &Tracker{client: NewClient("token", srv.URL, "200").WithGroupID("30"), config: DefaultMappingConfig()}
	aRef, bRef := projectARef7, projectBRef7
	store := &crossProjectStore{issues: map[string]*types.Issue{
		"bd-a7": {ID: "bd-a7", Title: "Project A issue 7", Status: types.StatusOpen, IssueType: types.TypeTask, ExternalRef: &aRef},
		"bd-b7": {ID: "bd-b7", Title: "Project B issue 7", Status: types.StatusOpen, IssueType: types.TypeTask, ExternalRef: &bRef},
	}}

	if _, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	b8 := beadIDByRef(store, projectBRef8)
	if b8 == "" {
		t.Fatalf("pull did not import project B's issue 8")
	}
	want := []string{b8 + " -> bd-b7 blocks", "bd-a7 -> bd-b7 related"}
	sort.Strings(want)
	if got := dependencyEdges(store); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("dependencies = %q, want %q", got, want)
	}
}

// TestPull_RenamedProjectMatchesBySourceSystem pins the rename-stable pull
// match. A bead a pull created records its issue's project ID and IID as its
// source_system; a project rename or transfer changes the issue's URL, which
// is what MatchKey reads, but neither of those, so the bead is relinked by
// them. Another project's issue with the same IID is not matched, and neither
// is a bead whose ref no longer names its source issue on this instance. A
// bead whose ref is at the path the pull shows the pulled issue's project at,
// but whose source_system names another project, follows its ref: a pull
// rewrites the ref and never the source_system, so the issue at that path
// takes the bead by its path as it would by the exact ref.
func TestPull_RenamedProjectMatchesBySourceSystem(t *testing.T) {
	tests := []struct {
		name         string
		sourceSystem string
		stored       string // %s is the configured host:port
		wantRelinked bool
	}{
		{name: "renamed project", sourceSystem: "gitlab:200:7", stored: "http://%s/old-group/project-b/-/issues/7", wantRelinked: true},
		{name: "transferred nested project", sourceSystem: "gitlab:200:7", stored: "http://%s/old-group/sub/project-b/-/work_items/7", wantRelinked: true},
		{name: "project-less shorthand", sourceSystem: "gitlab:200:7", stored: "gitlab:7", wantRelinked: true},
		{name: "other project with the same IID", sourceSystem: "gitlab:100:7", stored: "http://%s/group-a/project-a/-/issues/7"},
		{name: "other project formerly at the pulled path", sourceSystem: "gitlab:100:7", stored: "http://%s/new-group/project-b/-/work_items/7", wantRelinked: true},
		{name: "bead relinked to another IID", sourceSystem: "gitlab:200:7", stored: "http://%s/new-group/project-b/-/issues/9"},
		{name: "bead on another GitLab instance", sourceSystem: "gitlab:200:7", stored: "https://gitlab.other.example/old-group/project-b/-/issues/7"},
		{name: "renamed project, bead without a source_system", stored: "http://%s/old-group/project-b/-/issues/7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var webURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.TrimPrefix(r.URL.Path, "/api/v4") != "/projects/200/issues/7" {
					http.Error(w, "unexpected", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(gitLabProjectIssueJSON(9007, 7, 200, "Project B issue 7 (remote)", webURL)))
			}))
			t.Cleanup(srv.Close)
			host := "localhost:" + srv.URL[strings.LastIndex(srv.URL, ":")+1:]
			webURL = "http://" + host + "/new-group/project-b/-/issues/7"
			tr := &Tracker{client: NewClient("token", "http://"+host, "200"), config: DefaultMappingConfig()}

			stored := strings.ReplaceAll(tt.stored, "%s", host)
			store := &crossProjectStore{issues: map[string]*types.Issue{
				"bd-b7": {ID: "bd-b7", Title: "Local issue 7", Status: types.StatusOpen, IssueType: types.TypeTask,
					ExternalRef: &stored, SourceSystem: tt.sourceSystem},
			}}
			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true, IssueIDs: []string{"7"}})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}

			wantCreated, wantUpdated, wantTitle, wantRef, wantBeads := 1, 0, "Local issue 7", stored, 2
			if tt.wantRelinked {
				wantCreated, wantUpdated, wantTitle, wantRef, wantBeads = 0, 1, "Project B issue 7 (remote)", webURL, 1
			}
			if result.PullStats.Created != wantCreated || result.PullStats.Updated != wantUpdated {
				t.Errorf("created=%d updated=%d, want created=%d updated=%d",
					result.PullStats.Created, result.PullStats.Updated, wantCreated, wantUpdated)
			}
			b := store.issues["bd-b7"]
			if b.Title != wantTitle || derefRef(b.ExternalRef) != wantRef {
				t.Errorf("bd-b7 = {title:%q external_ref:%q}, want {title:%q external_ref:%q}",
					b.Title, derefRef(b.ExternalRef), wantTitle, wantRef)
			}
			if len(store.issues) != wantBeads {
				t.Errorf("store holds %d beads, want %d", len(store.issues), wantBeads)
			}
		})
	}
}

// TestPull_GroupModeRelinkedBeadTakesOneIssue pins a bead an older pull
// relinked (gastownhall/beads#7795): its source_system still names project A's
// issue 7 while its external_ref is project B's issue 7. A group pull of both
// issues, in either order, leaves the bead to project B's issue, which its ref
// names, and gives project A's issue a new bead: no pull lets two issues
// write one bead.
func TestPull_GroupModeRelinkedBeadTakesOneIssue(t *testing.T) {
	for _, bFirst := range []bool{true, false} {
		name := "project A's issue listed first"
		if bFirst {
			name = "project B's issue listed first"
		}
		t.Run(name, func(t *testing.T) {
			routes := map[string][]string{}
			srv := newGitLabServer(t, routes)
			refA, refB := srv.URL+"/group-a/project-a/-/issues/7", srv.URL+"/group-b/project-b/-/issues/7"
			issueA := gitLabProjectIssueJSON(8007, 7, 100, "Project A issue 7", refA)
			issueB := gitLabProjectIssueJSON(9007, 7, 200, "Project B issue 7", refB)
			routes["/groups/30/issues"] = []string{issueA, issueB}
			if bFirst {
				routes["/groups/30/issues"] = []string{issueB, issueA}
			}
			tr := &Tracker{client: NewClient("token", srv.URL, "").WithGroupID("30"), config: DefaultMappingConfig()}
			stored := refB
			store := &crossProjectStore{issues: map[string]*types.Issue{
				"bd-x": {ID: "bd-x", Title: "Project B issue 7 (stale)", Status: types.StatusOpen, IssueType: types.TypeTask,
					ExternalRef: &stored, SourceSystem: "gitlab:100:7"},
			}}

			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Created != 1 || result.PullStats.Updated != 1 {
				t.Errorf("created=%d updated=%d, want created=1 updated=1",
					result.PullStats.Created, result.PullStats.Updated)
			}
			if x := store.issues["bd-x"]; x.Title != "Project B issue 7" || derefRef(x.ExternalRef) != refB {
				t.Errorf("bd-x = {title:%q external_ref:%q}, want project B's issue 7", x.Title, derefRef(x.ExternalRef))
			}
			a := beadIDByRef(store, refA)
			if a == "" || a == "bd-x" || store.issues[a].Title != "Project A issue 7" {
				t.Errorf("project A's issue 7 bead = %q, want a new bead with its title", a)
			}
			if len(store.issues) != 2 {
				t.Errorf("store holds %d beads, want 2", len(store.issues))
			}
		})
	}
}

// TestPull_SourceSystemStaleOnAnotherProjectsPath pins when a bead's
// source_system stops naming it: once a pull sees another project at the
// bead's ref path, here project B through its issue 8 or through a link to
// its issue 7, the bead is project B's issue 7's, which its ref names, even
// with that issue absent from the pull. Project A's issue 7, whose project
// and IID the bead's source_system still records, gets a new bead.
func TestPull_SourceSystemStaleOnAnotherProjectsPath(t *testing.T) {
	tests := []struct {
		name      string
		withB8    bool // list project B's issue 8
		linkToB7  bool // link project A's issue 7 to project B's issue 7
		wantCount int  // beads the pull creates
	}{
		{name: "project B seen through its issue 8", withB8: true, wantCount: 2},
		{name: "project B seen through a link to its issue 7", linkToB7: true, wantCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := map[string][]string{}
			srv := newGitLabServer(t, routes)
			refA7 := srv.URL + "/group-a/project-a/-/issues/7"
			refB7 := srv.URL + "/group-b/project-b/-/issues/7"
			routes["/groups/30/issues"] = []string{gitLabProjectIssueJSON(8007, 7, 100, "Project A issue 7", refA7)}
			if tt.withB8 {
				routes["/groups/30/issues"] = append(routes["/groups/30/issues"],
					gitLabProjectIssueJSON(9008, 8, 200, "Project B issue 8", srv.URL+"/group-b/project-b/-/issues/8"))
			}
			if tt.linkToB7 {
				routes["/projects/100/issues/7/links"] = []string{gitLabLinkJSON(7, 200, refB7, gitLabLinkRelatesTo)}
			}
			tr := &Tracker{client: NewClient("token", srv.URL, "").WithGroupID("30"), config: DefaultMappingConfig()}
			stored := refB7
			store := &crossProjectStore{issues: map[string]*types.Issue{
				"bd-x": {ID: "bd-x", Title: "Project B issue 7", Status: types.StatusOpen, IssueType: types.TypeTask,
					ExternalRef: &stored, SourceSystem: "gitlab:100:7"},
			}}

			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Created != tt.wantCount || result.PullStats.Updated != 0 {
				t.Errorf("created=%d updated=%d, want created=%d updated=0",
					result.PullStats.Created, result.PullStats.Updated, tt.wantCount)
			}
			if x := store.issues["bd-x"]; x.Title != "Project B issue 7" || derefRef(x.ExternalRef) != refB7 {
				t.Errorf("bd-x = {title:%q external_ref:%q}, want it left to project B's issue 7", x.Title, derefRef(x.ExternalRef))
			}
			a := beadIDByRef(store, refA7)
			if a == "" || a == "bd-x" {
				t.Fatalf("project A's issue 7 bead = %q, want a new bead", a)
			}
			var want []string
			if tt.linkToB7 {
				want = []string{a + " -> bd-x related"}
			}
			if got := dependencyEdges(store); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("dependencies = %q, want %q", got, want)
			}
		})
	}
}

// TestPull_RenamedProjectDependencyResolvesByIdentity pins link endpoints after
// a project rename. Project B's beads still hold its old URLs, and the pull
// lists only issue 7, whose links say it blocks issue 8: the edge must still
// reach issue 8's bead, found by the project ID and IID it records, where the
// link's new URL alone misses it and the edge is dropped.
func TestPull_RenamedProjectDependencyResolvesByIdentity(t *testing.T) {
	routes := map[string][]string{}
	srv := newGitLabServer(t, routes)
	ref := func(group string, iid int) string {
		return srv.URL + "/" + group + "/project-b/-/issues/" + strconv.Itoa(iid)
	}
	routes["/projects/200/issues"] = []string{gitLabProjectIssueJSON(9007, 7, 200, "Project B issue 7", ref("new-group", 7))}
	routes["/projects/200/issues/7/links"] = []string{gitLabLinkJSON(8, 200, ref("new-group", 8), gitLabLinkBlocks)}
	tr := &Tracker{client: NewClient("token", srv.URL, "200"), config: DefaultMappingConfig()}
	old7, old8 := ref("old-group", 7), ref("old-group", 8)
	store := &crossProjectStore{issues: map[string]*types.Issue{
		"bd-b7": {ID: "bd-b7", Title: "Project B issue 7", Status: types.StatusOpen, IssueType: types.TypeTask,
			ExternalRef: &old7, SourceSystem: "gitlab:200:7"},
		"bd-b8": {ID: "bd-b8", Title: "Project B issue 8", Status: types.StatusOpen, IssueType: types.TypeTask,
			ExternalRef: &old8, SourceSystem: "gitlab:200:8"},
	}}

	result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Created != 0 || result.PullStats.Updated != 1 {
		t.Errorf("created=%d updated=%d, want created=0 updated=1 (bd-b7 relinked)",
			result.PullStats.Created, result.PullStats.Updated)
	}
	want := []string{"bd-b8 -> bd-b7 blocks"}
	if got := dependencyEdges(store); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("dependencies = %q, want %q", got, want)
	}
}

// TestPull_LinkToUnimportedIssueIsNotAFailure pins the dependency pass for a
// link whose far end has no bead: project B's issue 9, which the pull did not
// list, and project C's issue 12. The store reports the endpoint's web_url not
// found, as DoltStore does, and the pass skips the link as one to an issue the
// pull did not import: no warning and no skip, in a dry run as well.
func TestPull_LinkToUnimportedIssueIsNotAFailure(t *testing.T) {
	routes := map[string][]string{}
	srv := newGitLabServer(t, routes)
	routes["/projects/200/issues"] = []string{gitLabProjectIssueJSON(9007, 7, 200, "Project B issue 7", srv.URL+"/group-b/project-b/-/issues/7")}
	routes["/projects/200/issues/7/links"] = []string{
		gitLabLinkJSON(9, 200, srv.URL+"/group-b/project-b/-/issues/9", gitLabLinkBlocks),
		gitLabLinkJSON(12, 300, srv.URL+"/group-c/project-c/-/issues/12", gitLabLinkRelatesTo),
	}
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry run %v", dryRun), func(t *testing.T) {
			tr := &Tracker{client: NewClient("token", srv.URL, "200"), config: DefaultMappingConfig()}
			store := &crossProjectStore{issues: map[string]*types.Issue{}}
			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true, DryRun: dryRun})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if len(result.Warnings) != 0 || result.PullStats.Skipped != 0 {
				t.Errorf("warnings=%q skipped=%d, want no warning and no skip", result.Warnings, result.PullStats.Skipped)
			}
			if got := dependencyEdges(store); len(got) != 0 {
				t.Errorf("dependencies = %q, want none", got)
			}
		})
	}
}

// TestPull_DryRunDependencyPreviewUsesMatchedBead pins the dry-run preview of
// a link to an issue whose bead stores another spelling of its ref, here the
// work_items URL of issue 7: the preview resolves the endpoint to the bead the
// pull matches, as the pull does, so it does not propose the edge that bead
// already has.
func TestPull_DryRunDependencyPreviewUsesMatchedBead(t *testing.T) {
	routes := map[string][]string{}
	srv := newGitLabServer(t, routes)
	ref := func(kind string, iid int) string {
		return srv.URL + "/group-b/project-b/-/" + kind + "/" + strconv.Itoa(iid)
	}
	routes["/projects/200/issues"] = []string{gitLabProjectIssueJSON(9007, 7, 200, "Project B issue 7", ref("issues", 7))}
	routes["/projects/200/issues/7/links"] = []string{gitLabLinkJSON(8, 200, ref("issues", 8), gitLabLinkBlocks)}
	tr := &Tracker{client: NewClient("token", srv.URL, "200"), config: DefaultMappingConfig()}
	stored7, stored8 := ref("work_items", 7), ref("issues", 8)
	store := &crossProjectStore{
		issues: map[string]*types.Issue{
			"bd-b7": {ID: "bd-b7", Title: "Project B issue 7", Status: types.StatusOpen, IssueType: types.TypeTask, ExternalRef: &stored7},
			"bd-b8": {ID: "bd-b8", Title: "Project B issue 8", Status: types.StatusOpen, IssueType: types.TypeTask, ExternalRef: &stored8},
		},
		deps: []types.Dependency{{IssueID: "bd-b8", DependsOnID: "bd-b7", Type: types.DepBlocks}},
	}
	engine := tracker.NewEngine(tr, store, "test")
	// bd gitlab's pull hooks give every pulled issue a fresh ID.
	engine.PullHooks = &tracker.PullHooks{GenerateID: func(_ context.Context, issue *types.Issue) error {
		issue.ID = "bd-gen1"
		return nil
	}}
	var messages []string
	engine.OnMessage = func(msg string) { messages = append(messages, msg) }

	if _, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true, DryRun: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for _, msg := range messages {
		if strings.Contains(msg, "Would create dependency") {
			t.Errorf("preview says %q, want no dependency: bd-b8 already depends on bd-b7", msg)
		}
	}
}
