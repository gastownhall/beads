package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// This file covers bd-0nikt4 (PR #8633): the GitLab tracker must recognize
// the address GitLab writes into the web_url values it returns (its
// external_url), which need not be the gitlab.url the API is reached at.

// TestPushTwiceCreatesOneIssue (AC-1): GitLab writes its own external_url
// into the web_url of every issue it returns, whatever address the API was
// reached at. Two pushes that each Init a fresh Tracker, the way two bd runs
// behave, must create exactly one issue, and the bead's external_ref must
// stay the first create's web_url.
func TestPushTwiceCreatesOneIssue(t *testing.T) {
	tests := []struct {
		name          string
		projectWebURL string // GET /projects/42 answer; %s is the stub's own root
		issueWebBase  string // root the created issues' web_urls are spelled under
	}{
		{name: "web_url under gitlab.url", projectWebURL: "%s/g/p", issueWebBase: "%s"},
		{name: "web_url on the project's web host", projectWebURL: "https://git.example.org/g/p", issueWebBase: "https://git.example.org"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			creates := 0
			webURLs := map[int]string{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/api/v4")
				w.Header().Set("Content-Type", "application/json")
				mu.Lock()
				defer mu.Unlock()
				root := func(tmpl string) string { return strings.ReplaceAll(tmpl, "%s", "http://"+r.Host) }
				switch {
				case r.Method == http.MethodGet && path == "/projects/42":
					_, _ = w.Write([]byte(`{"id":42,"path_with_namespace":"g/p","web_url":"` + root(tt.projectWebURL) + `"}`))
				case r.Method == http.MethodPost && path == "/projects/42/issues":
					creates++
					ref := root(tt.issueWebBase) + "/g/p/-/issues/" + strconv.Itoa(creates)
					webURLs[creates] = ref
					_, _ = w.Write([]byte(`{"id":` + strconv.Itoa(1000+creates) + `,"iid":` + strconv.Itoa(creates) +
						`,"project_id":42,"title":"Local bead","state":"opened",` +
						`"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z","web_url":"` + ref + `"}`))
				case r.Method == http.MethodGet && strings.HasPrefix(path, "/projects/42/issues/"):
					iid, _ := strconv.Atoi(strings.TrimPrefix(path, "/projects/42/issues/"))
					if ref := webURLs[iid]; ref != "" {
						_, _ = w.Write([]byte(`{"iid":` + strconv.Itoa(iid) + `,"title":"Local bead","state":"opened",` +
							`"updated_at":"2026-10-01T00:00:00Z","web_url":"` + ref + `"}`))
						return
					}
					http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
				default:
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			t.Cleanup(srv.Close)

			t.Setenv("BEADS_DIR", "")
			t.Setenv("GITLAB_URL", srv.URL)
			t.Setenv("GITLAB_TOKEN", "token")
			t.Setenv("GITLAB_PROJECT_ID", "42")
			t.Setenv("GITLAB_PROJECT_PATH", "g/p")

			store := &crossProjectStore{issues: map[string]*types.Issue{
				"bd-1": {ID: "bd-1", Title: "Local bead", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			}}
			for run := 1; run <= 2; run++ {
				tr := &Tracker{}
				if err := tr.Init(context.Background(), store); err != nil {
					t.Fatalf("run %d Init: %v", run, err)
				}
				if _, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Push: true}); err != nil {
					t.Fatalf("run %d Sync: %v", run, err)
				}
			}
			if creates != 1 {
				t.Fatalf("two pushes created %d GitLab issues, want 1", creates)
			}
			if ref := store.issues["bd-1"].ExternalRef; ref == nil || *ref != webURLs[1] {
				t.Fatalf("bead external_ref = %s, want the first create's web_url %q", derefRef(ref), webURLs[1])
			}
		})
	}
}

// initWebBaseTracker Inits a fresh Tracker, the way a bd run does, against a
// stub GitLab whose project 42 answers GET with projectJSON (404 when empty),
// with the standard gitlab.url/token/project env pointed at it and env set on
// top.
func initWebBaseTracker(t *testing.T, projectJSON string, env map[string]string) *Tracker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if projectJSON != "" && r.Method == http.MethodGet && strings.TrimPrefix(r.URL.Path, "/api/v4") == "/projects/42" {
			_, _ = w.Write([]byte(projectJSON))
			return
		}
		http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("BEADS_DIR", "")
	t.Setenv("GITLAB_URL", srv.URL)
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("GITLAB_PROJECT_ID", "42")
	t.Setenv("GITLAB_PROJECT_PATH", "g/p")
	for k, v := range env {
		t.Setenv(k, v)
	}

	tr := &Tracker{}
	if err := tr.Init(context.Background(), &crossProjectStore{issues: map[string]*types.Issue{}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return tr
}

// TestInitLearnsWebBase (AC-2): the learned web base is the project's web_url
// minus its path with namespace, so a GitLab served from a relative root is
// learned with that root in front.
func TestInitLearnsWebBase(t *testing.T) {
	tests := []struct {
		name        string
		projectJSON string
		want        string
	}{
		{
			name:        "project at the web root",
			projectJSON: `{"id":42,"path_with_namespace":"g/p","web_url":"https://git.example.org/g/p"}`,
			want:        "https://git.example.org",
		},
		{
			name:        "project under a relative root",
			projectJSON: `{"id":42,"path_with_namespace":"g/p","web_url":"https://code.example.org/scm/g/p"}`,
			want:        "https://code.example.org/scm",
		},
		// AC-3: no address is learned — a 404 for the project, or a web_url
		// that does not end in the project's path with namespace, or none at
		// all — and recognition and keys keep exactly today's behavior.
		{
			name:        "project answers 404",
			projectJSON: ``,
			want:        "",
		},
		{
			name:        "web_url does not end in path_with_namespace",
			projectJSON: `{"id":42,"path_with_namespace":"g/p","web_url":"https://git.example.org/elsewhere"}`,
			want:        "",
		},
		{
			name:        "no web_url reported",
			projectJSON: `{"id":42,"path_with_namespace":"g/p"}`,
			want:        "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := initWebBaseTracker(t, tt.projectJSON, nil)
			if tr.webBase != tt.want {
				t.Fatalf("learned web base = %q, want %q", tr.webBase, tt.want)
			}
			if tt.want != "" {
				return
			}
			// Nothing was learned, so recognition and keys are exactly
			// today's: a web-host ref stays unrecognized, and the shorthand
			// still keys on the gitlab.url host.
			if tr.IsExternalRef("https://git.example.org/g/p/-/issues/5") {
				t.Error(`IsExternalRef("https://git.example.org/g/p/-/issues/5") = true, want false`)
			}
			wantKey := matchKeyFromURL(tr.client.BaseURL + "/g/p/-/issues/7")
			if got := tr.MatchKey("gitlab:7"); got != wantKey {
				t.Errorf(`MatchKey("gitlab:7") = %q, want today's %q`, got, wantKey)
			}
		})
	}
}

// TestInitGroupOnlySendsNoProjectRequest (AC-3): a group-mode config without
// a project (no gitlab.default_project_id) cannot create issues, so Init
// sends no request and learns no web address.
func TestInitGroupOnlySendsNoProjectRequest(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("BEADS_DIR", "")
	t.Setenv("GITLAB_URL", srv.URL)
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("GITLAB_PROJECT_ID", "")
	t.Setenv("GITLAB_PROJECT_PATH", "")
	t.Setenv("GITLAB_GROUP_ID", "30")
	t.Setenv("GITLAB_DEFAULT_PROJECT_ID", "")

	tr := &Tracker{}
	if err := tr.Init(context.Background(), &crossProjectStore{issues: map[string]*types.Issue{}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if requests != 0 {
		t.Fatalf("Init sent %d API requests, want 0", requests)
	}
	if tr.webBase != "" || tr.configuredWebURL != "" {
		t.Fatalf("webBase = %q configuredWebURL = %q, want neither learned nor configured", tr.webBase, tr.configuredWebURL)
	}
}

// TestInitFailsWhenProjectReadFails (AC-4): only a 404 leaves today's
// behavior. Any other failure of the project read — a refused token, a 5xx
// after the client's retries, a network error — stops Init: a push without
// the web address would create a second copy of every linked issue, and a
// warning would not stop the next run from moving the link to the copy.
func TestInitFailsWhenProjectReadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("BEADS_DIR", "")
	t.Setenv("GITLAB_URL", srv.URL)
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("GITLAB_PROJECT_ID", "42")
	t.Setenv("GITLAB_PROJECT_PATH", "g/p")

	tr := &Tracker{}
	err := tr.Init(context.Background(), &crossProjectStore{issues: map[string]*types.Issue{}})
	if err == nil {
		t.Fatal("Init succeeded with the project read answering 401, want an error")
	}
	if msg := err.Error(); !strings.Contains(msg, "42") || !strings.Contains(msg, "could not be read") {
		t.Errorf("error = %q, want it to name project 42 and say the address could not be read", msg)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error does not wrap *APIError: %v", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrapped APIError status = %d, want 401", apiErr.StatusCode)
	}
}

// TestAPIErrorTextUnchanged pins that the typed API error keeps the exact
// message of the untyped error it replaces, which the license-403 matcher and
// its tests match on.
func TestAPIErrorTextUnchanged(t *testing.T) {
	apiErr := &APIError{StatusCode: 403, Body: `{"message":"Blocked issues not available for current license"}`}
	want := `API error: {"message":"Blocked issues not available for current license"} (status 403)`
	if got := apiErr.Error(); got != want {
		t.Fatalf("APIError.Error() = %q, want %q", got, want)
	}
	if !isGitLabLicenseError(fmt.Errorf("failed to create issue link: %w", apiErr)) {
		t.Error("license matcher no longer recognizes the typed error")
	}
	if IsNotFound(apiErr) {
		t.Error("IsNotFound(403) = true, want false")
	}
	if !IsNotFound(fmt.Errorf("failed to get project: %w", &APIError{StatusCode: 404, Body: `{"message":"404 Not found"}`})) {
		t.Error("IsNotFound(wrapped 404) = false, want true")
	}
}

// TestMatchKeyAndLocalIdentityKeyUnderWebBase (AC-5): with a web base
// learned, the "gitlab:N" shorthand keys on it — the address GitLab's own
// links are under — so it still matches the issue's web_url, and a bead
// whose ref is under the web base keeps its source_system identity.
func TestMatchKeyAndLocalIdentityKeyUnderWebBase(t *testing.T) {
	tr := initWebBaseTracker(t, `{"id":42,"path_with_namespace":"g/p","web_url":"https://git.example.org/g/p"}`, nil)

	want := tr.MatchKey("https://git.example.org/g/p/-/issues/7")
	if want != "gl://git.example.org/g/p#7" {
		t.Fatalf(`MatchKey(web issue URL) = %q, want "gl://git.example.org/g/p#7"`, want)
	}
	if got := tr.MatchKey("gitlab:7"); got != want {
		t.Errorf(`MatchKey("gitlab:7") = %q, want %q (the web_url's key)`, got, want)
	}

	ref := "https://git.example.org/g/p/-/issues/7"
	issue := &types.Issue{ID: "bd-7", ExternalRef: &ref, SourceSystem: "gitlab:42:7"}
	if got := tr.LocalIdentityKey(issue); got != "gitlab:42:7" {
		t.Errorf("LocalIdentityKey = %q, want %q", got, "gitlab:42:7")
	}
}

// TestProjectScopeUnderGitLabURLAndWebBase (AC-6): with a web base learned,
// the configured project's issues under either address — gitlab.url, where
// beads linked before the address was known keep their refs, and the web
// base, where GitLab's links are — are in the dependency-link push scope,
// another project on the web host and another instance are not, and the run
// reads the project once (Init's read).
func TestProjectScopeUnderGitLabURLAndWebBase(t *testing.T) {
	var projectGets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.TrimPrefix(r.URL.Path, "/api/v4") == "/projects/42" {
			projectGets++
			_, _ = w.Write([]byte(`{"id":42,"path_with_namespace":"g/p","web_url":"https://git.example.org/g/p"}`))
			return
		}
		http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("BEADS_DIR", "")
	t.Setenv("GITLAB_URL", srv.URL)
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("GITLAB_PROJECT_ID", "42")
	t.Setenv("GITLAB_PROJECT_PATH", "g/p")

	tr := &Tracker{}
	if err := tr.Init(context.Background(), &crossProjectStore{issues: map[string]*types.Issue{}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	scope := tr.ProjectScope()
	tests := []struct {
		name string
		ref  string
		want bool
	}{
		{"issue under gitlab.url", srv.URL + "/g/p/-/issues/1", true},
		{"issue under the web base", "https://git.example.org/g/p/-/issues/2", true},
		{"work item under gitlab.url", srv.URL + "/g/p/-/work_items/3", true},
		{"another project on the web host", "https://git.example.org/other/q/-/issues/4", false},
		{"another instance", "https://gitlab.other.example/g/p/-/issues/5", false},
	}
	for _, tt := range tests {
		ref := tt.ref
		issue := &types.Issue{ID: "bd-x", ExternalRef: &ref}
		if got := scope.Contains(context.Background(), issue); got != tt.want {
			t.Errorf("%s: Contains = %v, want %v", tt.name, got, tt.want)
		}
	}
	if projectGets != 1 {
		t.Fatalf("GET /projects/42 answered %d times, want 1", projectGets)
	}
}

// TestCreateTaskWorkItemRefUnderWebBase (AC-7): a task created under a story
// parent becomes a GitLab Task work item whose stored ref is composed under
// the address GitLab writes its links under — the learned web base, like the
// web_url GitLab itself returns — not the gitlab.url the API was reached at.
func TestCreateTaskWorkItemRefUnderWebBase(t *testing.T) {
	const webBase = "https://git.example.org"
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.TrimPrefix(r.URL.Path, "/api/v4") == "/projects/42":
			_, _ = w.Write([]byte(`{"id":42,"path_with_namespace":"g/p","web_url":"` + webBase + `/g/p"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/graphql":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(body, &req)
			switch {
			case strings.Contains(req.Query, "workItems(iid:"):
				_, _ = w.Write([]byte(`{"data":{"project":{"workItems":{"nodes":[{"id":"gid://gitlab/WorkItem/5"}]}}}}`))
			case strings.Contains(req.Query, "workItemCreate"):
				_, _ = w.Write([]byte(`{"data":{"workItemCreate":{"errors":[],"workItem":{"id":"gid://gitlab/WorkItem/9001","iid":"9","title":"Do it","workItemType":{"name":"Task"},"webUrl":"` + webBase + `/g/p/-/work_items/9"}}}}`))
			default:
				_, _ = w.Write([]byte(`{"data":{"project":{"workItemTypes":{"nodes":[{"id":"gid://gitlab/WorkItems::Type/5"}]}}}}`))
			}
		default:
			http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("BEADS_DIR", "")
	t.Setenv("GITLAB_URL", srv.URL)
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("GITLAB_PROJECT_ID", "42")
	t.Setenv("GITLAB_PROJECT_PATH", "g/p")

	parentRef := webBase + "/g/p/-/issues/5"
	store := &crossProjectStore{
		issues: map[string]*types.Issue{
			"bd-story": {ID: "bd-story", IssueType: types.TypeFeature, Status: types.StatusOpen, ExternalRef: &parentRef},
			"bd-task":  {ID: "bd-task", IssueType: types.TypeTask, Status: types.StatusOpen, Title: "Do it"},
		},
		deps: []types.Dependency{{IssueID: "bd-task", DependsOnID: "bd-story", Type: types.DepParentChild}},
	}
	tr := &Tracker{}
	if err := tr.Init(context.Background(), store); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ti, err := tr.CreateIssue(context.Background(), store.issues["bd-task"])
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	want := webBase + "/g/p/-/work_items/9"
	if ti.URL != want {
		t.Fatalf("created work-item ref = %q, want %q (the web base, not %q)", ti.URL, want, srv.URL)
	}
	if !tr.IsExternalRef(ti.URL) {
		t.Error("IsExternalRef(created ref) = false, want true")
	}
	if got, shorthand := tr.MatchKey(ti.URL), tr.MatchKey("gitlab:9"); got != shorthand {
		t.Errorf("MatchKey(created ref) = %q, want the shorthand's %q", got, shorthand)
	}
}

// TestIsExternalRefLearnedWebBase (AC-2): with a relative-root web base
// learned, issue and work-item URLs under it are recognized, refs under
// gitlab.url stay recognized, and a path outside the root, an unrelated host
// and a non-issue URL are not.
func TestIsExternalRefLearnedWebBase(t *testing.T) {
	tr := initWebBaseTracker(t, `{"id":42,"path_with_namespace":"g/p","web_url":"https://code.example.org/scm/g/p"}`, nil)
	gitlabURLRef := tr.client.BaseURL + "/g/p/-/issues/1"
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://code.example.org/scm/g/p/-/issues/5", true},
		{"https://code.example.org/scm/g/p/-/work_items/6", true},
		{"https://CODE.EXAMPLE.ORG/scm/g/p/-/issues/5", true},
		{gitlabURLRef, true},
		{"https://code.example.org/other/-/issues/5", false},
		{"https://other.example.net/g/p/-/issues/5", false},
		{"https://code.example.org/scm/g/p/-/merge_requests/5", false},
	}
	for _, tt := range tests {
		if got := tr.IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}
