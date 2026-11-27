package gitlab

import (
	"context"
	"encoding/json"
	"errors"
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

// gitLabStub is an httptest GitLab for project 42. GET /projects/42 answers
// projectJSON, or projectStatus when that is set; POST /projects/42/issues
// creates issue N with web_url issueWebRoot+"/-/issues/N", which a later
// GET /projects/42/issues/N returns.
type gitLabStub struct {
	srv *httptest.Server

	mu            sync.Mutex
	projectStatus int
	projectJSON   string
	issueWebRoot  string
	projectGets   int
	creates       int
	requests      []string
	issues        map[int]map[string]any
	linkPosts     []string
	graphql       func(query string) string
}

func newGitLabStub(t *testing.T) *gitLabStub {
	t.Helper()
	s := &gitLabStub{issues: map[int]map[string]any{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	s.issueWebRoot = s.srv.URL + "/g/p"
	s.projectJSON = `{"id":42,"path_with_namespace":"g/p","web_url":"` + s.srv.URL + `/g/p"}`
	return s
}

func (s *gitLabStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/api/v4")
	s.requests = append(s.requests, r.Method+" "+path)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && path == "/projects/42":
		s.projectGets++
		if s.projectStatus != 0 {
			http.Error(w, `{"message":"`+http.StatusText(s.projectStatus)+`"}`, s.projectStatus)
			return
		}
		_, _ = io.WriteString(w, s.projectJSON)
	case r.Method == http.MethodPost && r.URL.Path == "/api/graphql":
		var in struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &in)
		if s.graphql == nil {
			http.Error(w, `{"message":"no graphql"}`, http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, s.graphql(in.Query))
	case r.Method == http.MethodPost && path == "/projects/42/issues":
		s.creates++
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		iid := s.creates
		iss := map[string]any{
			"id": 1000 + iid, "iid": iid, "project_id": 42, "title": in["title"], "description": in["description"],
			"state": "opened", "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-01T00:00:00Z",
			"web_url": s.issueWebRoot + "/-/issues/" + strconv.Itoa(iid),
		}
		s.issues[iid] = iss
		_ = json.NewEncoder(w).Encode(iss)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/links"):
		_, _ = io.WriteString(w, `[]`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/links"):
		s.linkPosts = append(s.linkPosts, path+" "+string(body))
		_, _ = io.WriteString(w, `{}`)
	case strings.HasPrefix(path, "/projects/42/issues/"):
		iid, _ := strconv.Atoi(strings.TrimPrefix(path, "/projects/42/issues/"))
		iss, ok := s.issues[iid]
		if !ok {
			http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(iss)
	case r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `[]`)
	default:
		_, _ = io.WriteString(w, `{}`)
	}
}

func (s *gitLabStub) counts() (projectGets, creates int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projectGets, s.creates
}

// initStubTracker runs Init the way `bd gitlab sync` does, configured by
// environment for project 42 at the stub (projectPath "" leaves
// gitlab.project_path unset). store nil means an empty store.
func initStubTracker(t *testing.T, stub *gitLabStub, projectPath string, store *crossProjectStore) (*Tracker, error) {
	t.Helper()
	setStubEnv(t, stub, "42", projectPath)
	if store == nil {
		store = &crossProjectStore{issues: map[string]*types.Issue{}}
	}
	tr := &Tracker{}
	return tr, tr.Init(context.Background(), store)
}

func setStubEnv(t *testing.T, stub *gitLabStub, projectID, projectPath string) {
	t.Helper()
	t.Setenv("BEADS_DIR", "")
	t.Setenv("GITLAB_URL", stub.srv.URL)
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("GITLAB_PROJECT_ID", projectID)
	t.Setenv("GITLAB_PROJECT_PATH", projectPath)
	t.Setenv("GITLAB_GROUP_ID", "")
	t.Setenv("GITLAB_DEFAULT_PROJECT_ID", "")
	for _, k := range []string{"GITLAB_FILTER_LABELS", "GITLAB_FILTER_PROJECT", "GITLAB_FILTER_MILESTONE", "GITLAB_FILTER_ASSIGNEE"} {
		t.Setenv(k, "")
	}
}

// TestInitFailsWhenTheProjectCannotBeRead pins AC-4: a project read that
// fails for any reason but 404 stops Init, since a push without the address
// GitLab writes into its links could copy every issue linked under it.
func TestInitFailsWhenTheProjectCannotBeRead(t *testing.T) {
	stub := newGitLabStub(t)
	stub.projectStatus = http.StatusUnauthorized
	_, err := initStubTracker(t, stub, "g/p", nil)
	if err == nil {
		t.Fatal("Init succeeded although GET /projects/42 answered 401")
	}
	for _, want := range []string{"project 42", "address GitLab writes into issue links"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Init error %q does not say %q", err, want)
		}
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("Init error %v does not wrap the 401 *APIError", err)
	}
}

// TestAPIErrorTextUnchanged pins the text of a typed API error to the untyped
// error it replaces, which isGitLabLicenseError still matches.
func TestAPIErrorTextUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Blocked issues not available for current license"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	_, err := NewClient("token", srv.URL, "42").CreateIssueLink(context.Background(), 1, 2, "blocks")
	want := `API error: {"message":"Blocked issues not available for current license"}` + "\n (status 403)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("CreateIssueLink error = %v, want it to contain %q", err, want)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden || apiErr.Error() != want {
		t.Fatalf("error %v: *APIError = %+v, want status 403 and text %q", err, apiErr, want)
	}
	if !isGitLabLicenseError(err) {
		t.Errorf("isGitLabLicenseError(%v) = false, want true", err)
	}
	if IsNotFound(err) || !IsNotFound(&APIError{StatusCode: http.StatusNotFound}) {
		t.Errorf("IsNotFound misclassifies 403 or 404")
	}
}

// assertTodaysRecognition checks the recognition and keys a tracker had
// before it learned GitLab's web address: refs under gitlab.url only.
func assertTodaysRecognition(t *testing.T, tr *Tracker, stub *gitLabStub) {
	t.Helper()
	under := stub.srv.URL + "/g/p/-/issues/7"
	if !tr.IsExternalRef(under) {
		t.Errorf("IsExternalRef(%q) = false, want true", under)
	}
	if other := "https://git.example.org/g/p/-/issues/7"; tr.IsExternalRef(other) {
		t.Errorf("IsExternalRef(%q) = true, want false", other)
	}
	if got, want := tr.MatchKey("gitlab:7"), tr.MatchKey(under); got != want || got == "" {
		t.Errorf("MatchKey(gitlab:7) = %q, want %q", got, want)
	}
}

// TestInitKeepsTodaysRecognitionWhenTheWebAddressIsUnknown pins AC-3: a 404
// for the project, a web_url that does not end in the project's path, and a
// group-only config (which sends no request) all leave Init succeeding with
// today's recognition and keys.
func TestInitKeepsTodaysRecognitionWhenTheWebAddressIsUnknown(t *testing.T) {
	t.Run("project 404", func(t *testing.T) {
		stub := newGitLabStub(t)
		stub.projectStatus = http.StatusNotFound
		tr, err := initStubTracker(t, stub, "g/p", nil)
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		assertTodaysRecognition(t, tr, stub)
	})
	t.Run("web_url does not end in path_with_namespace", func(t *testing.T) {
		stub := newGitLabStub(t)
		stub.projectJSON = `{"id":42,"path_with_namespace":"g/p","web_url":"https://git.example.org/x/y"}`
		tr, err := initStubTracker(t, stub, "g/p", nil)
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		assertTodaysRecognition(t, tr, stub)
	})
	t.Run("group-only config sends no request", func(t *testing.T) {
		stub := newGitLabStub(t)
		setStubEnv(t, stub, "", "g/p")
		t.Setenv("GITLAB_GROUP_ID", "30")
		tr := &Tracker{}
		if err := tr.Init(context.Background(), &crossProjectStore{issues: map[string]*types.Issue{}}); err != nil {
			t.Fatalf("Init: %v", err)
		}
		stub.mu.Lock()
		reqs := append([]string(nil), stub.requests...)
		stub.mu.Unlock()
		if len(reqs) != 0 {
			t.Errorf("group-only Init sent %q, want no request", reqs)
		}
		assertTodaysRecognition(t, tr, stub)
	})
}

// TestPushTwiceCreatesOneIssue pins AC-1: GitLab reached at gitlab.url but
// writing its links under another address (its external_url, as the
// project's web_url reports it) must not get a new issue on every push. Each
// run Inits a fresh tracker, as a second bd run would.
func TestPushTwiceCreatesOneIssue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		webRoot string // the project's web_url; "" = under gitlab.url
	}{
		{name: "web_url under gitlab.url"},
		{name: "web_url on another host", webRoot: "https://git.example.org/g/p"},
		{name: "web_url under a relative root", webRoot: "https://code.example.org/scm/g/p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGitLabStub(t)
			if tc.webRoot != "" {
				stub.issueWebRoot = tc.webRoot
				stub.projectJSON = `{"id":42,"path_with_namespace":"g/p","web_url":"` + tc.webRoot + `"}`
			}
			store := &crossProjectStore{issues: map[string]*types.Issue{
				"bd-1": {ID: "bd-1", Title: "Local bead", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			}}
			for run := 1; run <= 2; run++ {
				tr, err := initStubTracker(t, stub, "g/p", store)
				if err != nil {
					t.Fatalf("run %d Init: %v", run, err)
				}
				if _, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Push: true}); err != nil {
					t.Fatalf("run %d Sync: %v", run, err)
				}
			}
			ref := derefRef(store.issues["bd-1"].ExternalRef)
			if _, creates := stub.counts(); creates != 1 {
				t.Fatalf("two pushes made %d POST /projects/42/issues, want 1 (external_ref now %q)", creates, ref)
			}
			if want := stub.issueWebRoot + "/-/issues/1"; ref != want {
				t.Errorf("external_ref = %q, want the first create's web_url %q", ref, want)
			}
		})
	}
}

// TestIsExternalRefLearnedWebBase pins AC-2: with the project's web_url under
// a relative root, refs under that root are recognized beside refs under
// gitlab.url, while another path on its host and an unrelated host are not.
func TestIsExternalRefLearnedWebBase(t *testing.T) {
	stub := newGitLabStub(t)
	stub.projectJSON = `{"id":42,"path_with_namespace":"g/p","web_url":"https://code.example.org/scm/g/p"}`
	tr, err := initStubTracker(t, stub, "g/p", nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"https://code.example.org/scm/g/p/-/issues/5", true},
		{"https://code.example.org/scm/g/p/-/work_items/5", true},
		{"https://code.example.org/scm/other/q/-/issues/5", true},
		{stub.srv.URL + "/g/p/-/issues/5", true},
		{"https://code.example.org/other/-/issues/5", false},
		{"https://tracker.example.net/g/p/-/issues/5", false},
	} {
		if got := tr.IsExternalRef(tc.ref); got != tc.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

// TestMatchKeyAndLocalIdentityKeyUnderWebBase pins AC-5: the project-less
// shorthand keys like GitLab's link for the same IID under the learned web
// address, and a pulled bead linked there keeps its source_system identity.
func TestMatchKeyAndLocalIdentityKeyUnderWebBase(t *testing.T) {
	stub := newGitLabStub(t)
	stub.projectJSON = `{"id":42,"path_with_namespace":"g/p","web_url":"https://git.example.org/g/p"}`
	tr, err := initStubTracker(t, stub, "g/p", nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	link := "https://git.example.org/g/p/-/issues/7"
	if got, want := tr.MatchKey("gitlab:7"), tr.MatchKey(link); got != want || want == "" {
		t.Errorf("MatchKey(gitlab:7) = %q, want %q (the key of %s)", got, want, link)
	}
	bead := &types.Issue{ID: "bd-7", SourceSystem: "gitlab:42:7", ExternalRef: &link}
	if got := tr.LocalIdentityKey(bead); got != "gitlab:42:7" {
		t.Errorf("LocalIdentityKey(%s at %s) = %q, want gitlab:42:7", bead.SourceSystem, link, got)
	}
}

// TestProjectScopeUnderGitLabURLAndWebBase pins AC-6 in this package: with a
// learned web address, the configured project's refs under gitlab.url and
// under that address are in the scope, another project or instance is not,
// and the run reads the project once, in Init, whether the project is
// configured by path or only by ID.
func TestProjectScopeUnderGitLabURLAndWebBase(t *testing.T) {
	for _, projectPath := range []string{"g/p", ""} {
		t.Run("project_path="+projectPath, func(t *testing.T) {
			stub := newGitLabStub(t)
			stub.projectJSON = `{"id":42,"path_with_namespace":"g/p","web_url":"https://git.example.org/g/p"}`
			tr, err := initStubTracker(t, stub, projectPath, nil)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			scope := tr.ProjectScope()
			for _, tc := range []struct {
				ref  string
				want bool
			}{
				{stub.srv.URL + "/g/p/-/issues/1", true},
				{"https://git.example.org/g/p/-/issues/2", true},
				{"https://git.example.org/other/q/-/issues/3", false},
				{"https://tracker.example.net/g/p/-/issues/4", false},
			} {
				ref := tc.ref
				if got := scope.Contains(context.Background(), &types.Issue{ID: "bd-x", ExternalRef: &ref}); got != tc.want {
					t.Errorf("Contains(%q) = %v, want %v", ref, got, tc.want)
				}
			}
			if err := scope.Err(); err != nil {
				t.Errorf("scope.Err() = %v", err)
			}
			if gets, _ := stub.counts(); gets != 1 {
				t.Errorf("GET /projects/42 requested %d times in the run, want 1", gets)
			}
		})
	}
}
