package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/tracker/conformance"
	"github.com/steveyegge/beads/internal/types"
)

// pushTwice pushes one unlinked bead twice, each time with a tracker
// initialized afresh as a second bd run would, against a GitLab that writes
// webHost into every web_url (its own address when webHost is ""). It returns
// how many issues GitLab was asked to create, the bead's external_ref after
// each push, and the warnings of each push.
func pushTwice(t *testing.T, webHost, webURLConfig string) (creates int, refs [2]string, warnings [2][]string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GITLAB_TOKEN", "test-token")
	t.Setenv("GITLAB_PROJECT_ID", "42")
	t.Setenv("GITLAB_WEB_URL", "")

	var mu sync.Mutex
	issues := map[int]*Issue{}
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	const issuesPath = "/api/v4/projects/42/issues"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == issuesPath:
			var in struct {
				Title       string `json:"title"`
				Description string `json:"description"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			creates++
			host := webHost
			if host == "" {
				host = srv.URL
			}
			created := &Issue{
				ID: 1000 + creates, IID: creates, ProjectID: 42, Title: in.Title, Description: in.Description,
				State: "opened", CreatedAt: &now, UpdatedAt: &now, Labels: []string{},
				WebURL: fmt.Sprintf("%s/acme/widgets/-/issues/%d", host, creates),
			}
			issues[created.IID] = created
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(created)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, issuesPath+"/"):
			var iid int
			_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, issuesPath+"/"), "%d", &iid)
			if found, ok := issues[iid]; ok && !strings.Contains(strings.TrimPrefix(r.URL.Path, issuesPath+"/"), "/") {
				_ = json.NewEncoder(w).Encode(found)
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("GITLAB_URL", srv.URL)

	store := conformance.NewStore()
	store.Config["gitlab.url"] = srv.URL
	store.Config["gitlab.project_id"] = "42"
	if webURLConfig != "" {
		store.Config["gitlab.web_url"] = webURLConfig
	}
	store.Issues["bd-1"] = &types.Issue{
		ID: "bd-1", Title: "local only", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2,
		UpdatedAt: now,
	}

	for i := range refs {
		tr := &Tracker{}
		if err := tr.Init(context.Background(), store); err != nil {
			t.Fatalf("Init before push %d: %v", i+1, err)
		}
		res, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Push: true})
		if err != nil {
			t.Fatalf("push %d: %v", i+1, err)
		}
		warnings[i] = res.Warnings
		if ref := store.Issues["bd-1"].ExternalRef; ref != nil {
			refs[i] = *ref
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return creates, refs, warnings
}

// A second push of a bead the first push created must find the bead linked.
// GitLab writes its external_url into web_url, so when that is not the address
// in gitlab.url the link is recognized only through gitlab.web_url.
func TestPushTwiceCreatesOneIssue(t *testing.T) {
	tests := []struct {
		name         string
		webHost      string
		webURLConfig string
	}{
		{name: "web_url under gitlab.url"},
		{name: "web_url on the host gitlab.web_url names", webHost: "https://git.example.org", webURLConfig: "https://git.example.org"},
		{name: "gitlab.web_url spells the default port", webHost: "https://git.example.org", webURLConfig: "https://git.example.org:443/"},
		{name: "gitlab.web_url is a sub-path", webHost: "https://corp.example.org/git", webURLConfig: "https://corp.example.org/git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creates, refs, warnings := pushTwice(t, tt.webHost, tt.webURLConfig)
			if creates != 1 {
				t.Errorf("two pushes of one bead created %d issues, want 1", creates)
			}
			if refs[0] == "" || refs[1] != refs[0] {
				t.Errorf("external_ref after push 1 = %q, after push 2 = %q; want the same issue", refs[0], refs[1])
			}
			for i, w := range warnings {
				if len(w) != 0 {
					t.Errorf("push %d warned: %v", i+1, w)
				}
			}
		})
	}
}

// With web_url on a host nothing configured names, the tracker cannot
// recognize the ref it is about to store. The create must say so and name the
// key that cures it, because the next push will create the issue again.
func TestCreateWarnsWhenWebURLIsNotRecognized(t *testing.T) {
	_, refs, warnings := pushTwice(t, "https://git.example.org", "")
	if !strings.HasPrefix(refs[0], "https://git.example.org/") {
		t.Fatalf("external_ref after push 1 = %q, want the web_url GitLab returned", refs[0])
	}
	for i, w := range warnings {
		joined := strings.Join(w, "\n")
		if !strings.Contains(joined, "gitlab.web_url") || !strings.Contains(joined, "https://git.example.org") {
			t.Errorf("push %d warnings = %q, want one naming gitlab.web_url and https://git.example.org", i+1, joined)
		}
	}
}

func TestInitRejectsWebURLWithoutHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GITLAB_TOKEN", "test-token")
	t.Setenv("GITLAB_PROJECT_ID", "42")
	t.Setenv("GITLAB_URL", "https://gitlab.example.com")
	t.Setenv("GITLAB_WEB_URL", "")

	store := conformance.NewStore()
	store.Config["gitlab.url"] = "https://gitlab.example.com"
	store.Config["gitlab.project_id"] = "42"
	store.Config["gitlab.web_url"] = "git.example.org"
	err := (&Tracker{}).Init(context.Background(), store)
	if err == nil || !strings.Contains(err.Error(), "gitlab.web_url") {
		t.Fatalf("Init with a host-less gitlab.web_url = %v, want an error naming gitlab.web_url", err)
	}
}

func TestIsExternalRefWebURLAndDefaultPort(t *testing.T) {
	// gitlab.url spelled with its default port claims the web URLs GitLab
	// writes without it, and the other way around.
	port := &Tracker{client: NewClient("tok", "https://gl.corp:443", "42")}
	if !port.IsExternalRef("https://gl.corp/g/p/-/issues/3") {
		t.Error("gitlab.url with an explicit default port should claim a ref that omits it")
	}
	bare := &Tracker{client: NewClient("tok", "https://gl.corp", "42")}
	if !bare.IsExternalRef("https://gl.corp:443/g/p/-/issues/3") {
		t.Error("gitlab.url without a port should claim a ref that spells the default port")
	}
	// Only the default port of a URL's own scheme is left out: HTTPS on port
	// 80 and HTTP on port 443 are other endpoints.
	for _, tt := range []struct {
		base, ref string
		want      bool
	}{
		{"http://code.example.org:80", "http://code.example.org/g/p/-/issues/3", true},
		{"https://code.example.org", "https://code.example.org:80/g/p/-/issues/3", false},
		{"http://code.example.org", "http://code.example.org:443/g/p/-/issues/3", false},
		{"https://code.example.org:80", "https://code.example.org/g/p/-/issues/3", false},
	} {
		if got := (&Tracker{client: NewClient("tok", tt.base, "42")}).IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("gitlab.url %s: IsExternalRef(%q) = %v, want %v", tt.base, tt.ref, got, tt.want)
		}
	}

	web := &Tracker{client: NewClient("tok", "http://10.0.0.5:8080", "42"), webURL: "https://git.example.org"}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://git.example.org/g/p/-/issues/3", true},
		{"https://git.example.org/g/p/-/work_items/3", true},
		{"https://GIT.example.org/g/p/-/issues/3", true},
		// The API address stays claimed beside the web address.
		{"http://10.0.0.5:8080/g/p/-/issues/3", true},
		// A host neither key names stays rejected.
		{"https://other.example.org/g/p/-/issues/3", false},
		{"https://git.example.org/g/p/-/merge_requests/3", false},
	}
	for _, tt := range tests {
		if got := web.IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}

	// The project-less shorthand is keyed on the web address, where the
	// issue's web_url is, so a pull still matches the two.
	web.projectPath = "g/p"
	if got, want := web.MatchKey("gitlab:3"), web.MatchKey("https://git.example.org/g/p/-/issues/3"); got != want {
		t.Errorf(`MatchKey("gitlab:3") = %q, want the web URL's key %q`, got, want)
	}
}

// With gitlab.web_url set, the configured project's refs are under two
// addresses: gitlab.url, where beads linked before the key was set keep
// theirs, and gitlab.web_url, where GitLab's links are. Both are in the
// project scope, so the dependency links between them are still pushed.
// Another project or instance on either address stays outside, and a project
// configured by path needs no lookup.
func TestProjectScopeUnderGitLabURLAndWebURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	tests := []struct {
		name    string
		apiPath string // gitlab.url is the test server's address and this path
		webURL  string
		ref     string // "{api}" stands for gitlab.url
		want    bool
	}{
		{name: "issue under gitlab.web_url", webURL: "https://git.example.org", ref: "https://git.example.org/g/p/-/issues/3", want: true},
		{name: "issue linked before gitlab.web_url was set", webURL: "https://git.example.org", ref: "{api}/g/p/-/issues/3", want: true},
		{name: "task work item under gitlab.url", webURL: "https://git.example.org", ref: "{api}/g/p/-/work_items/3", want: true},
		{name: "another project under gitlab.web_url", webURL: "https://git.example.org", ref: "https://git.example.org/g/other/-/issues/3"},
		{name: "sibling project under gitlab.url", webURL: "https://git.example.org", ref: "{api}/g/p-other/-/issues/3"},
		{name: "another instance", webURL: "https://git.example.org", ref: "https://other.example.org/g/p/-/issues/3"},
		{name: "issue under a sub-path gitlab.web_url", apiPath: "/git", webURL: "https://corp.example.org/git", ref: "https://corp.example.org/git/g/p/-/issues/3", want: true},
		{name: "task work item under a sub-path gitlab.url", apiPath: "/git", webURL: "https://corp.example.org/git", ref: "{api}/g/p/-/work_items/3", want: true},
		{name: "web host outside the sub-path", apiPath: "/git", webURL: "https://corp.example.org/git", ref: "https://corp.example.org/g/p/-/issues/3"},
	}
	for _, tt := range tests {
		api := srv.URL + tt.apiPath
		scope := (&Tracker{client: NewClient("tok", api, "42"), projectPath: "g/p", webURL: tt.webURL}).ProjectScope()
		ref := strings.ReplaceAll(tt.ref, "{api}", api)
		if got := scope.Contains(context.Background(), &types.Issue{ID: "bd-1", ExternalRef: &ref}); got != tt.want {
			t.Errorf("%s: Contains(%s) = %v, want %v", tt.name, ref, got, tt.want)
		}
		if err := scope.Err(); err != nil {
			t.Errorf("%s: project lookup: %v", tt.name, err)
		}
	}
}

// A task created as a work item under a linked story gets a ref built from the
// project path and IID under gitlab.web_url when that is set, gitlab.url
// otherwise. The ref is recognized, it is in the project scope, and it keys
// like the link GitLab shows for the same IID, so a pull matches the two.
func TestCreateTaskWorkItemRefIsUnderTheWebAddress(t *testing.T) {
	tests := []struct {
		name    string
		webURL  string // gitlab.web_url
		webBase string // where GitLab's links are; "" is the API's address
		want    string // "{api}" stands for gitlab.url
	}{
		{name: "gitlab.web_url set", webURL: "https://git.example.org", webBase: "https://git.example.org", want: "https://git.example.org/g/p/-/work_items/7"},
		{name: "gitlab.web_url on a sub-path, with a trailing slash", webURL: "https://corp.example.org/git/", webBase: "https://corp.example.org/git", want: "https://corp.example.org/git/g/p/-/work_items/7"},
		{name: "gitlab.web_url unset", want: "{api}/g/p/-/work_items/7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Query string `json:"query"`
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/graphql" || json.NewDecoder(r.Body).Decode(&in) != nil {
					http.NotFound(w, r)
					return
				}
				base := tt.webBase
				if base == "" {
					base = srv.URL
				}
				switch {
				case strings.Contains(in.Query, "workItemCreate"):
					_, _ = fmt.Fprintf(w, `{"data":{"workItemCreate":{"errors":[],"workItem":{"id":"gid://gitlab/WorkItem/907","iid":"7","title":"task","workItemType":{"name":"Task"},"webUrl":"%s/g/p/-/work_items/7"}}}}`, base)
				case strings.Contains(in.Query, "workItemTypes"):
					_, _ = w.Write([]byte(`{"data":{"project":{"workItemTypes":{"nodes":[{"id":"gid://gitlab/WorkItems::Type/5"}]}}}}`))
				case strings.Contains(in.Query, "workItems("):
					_, _ = w.Write([]byte(`{"data":{"project":{"workItems":{"nodes":[{"id":"gid://gitlab/WorkItem/905"}]}}}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			story := gitLabIssue("bd-story", "https://git.example.org/g/p/-/issues/5", types.TypeFeature)
			tr := &Tracker{
				client:      NewClient("tok", srv.URL, "42"),
				projectPath: "g/p",
				webURL:      tt.webURL,
				store: &gitLabLinkFakeStore{deps: map[string][]*types.IssueWithDependencyMetadata{
					"bd-task": {{Issue: *story, DependencyType: types.DepParentChild}},
				}},
			}
			created, err := tr.CreateIssue(context.Background(), &types.Issue{ID: "bd-task", Title: "task", IssueType: types.TypeTask, Status: types.StatusOpen})
			if err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}
			want := strings.ReplaceAll(tt.want, "{api}", srv.URL)
			if created.URL != want {
				t.Fatalf("work item ref = %q, want %q", created.URL, want)
			}
			if len(created.Warnings) != 0 {
				t.Errorf("warnings = %q, want none", created.Warnings)
			}
			if !tr.IsExternalRef(created.URL) {
				t.Errorf("IsExternalRef(%q) = false, want true", created.URL)
			}
			scope := tr.ProjectScope()
			if !scope.Contains(context.Background(), &types.Issue{ID: "bd-task", ExternalRef: &created.URL}) || scope.Err() != nil {
				t.Errorf("ProjectScope.Contains(%q) = false (lookup error %v), want true", created.URL, scope.Err())
			}
			base := tt.webBase
			if base == "" {
				base = srv.URL
			}
			if got, link := tr.MatchKey(created.URL), tr.MatchKey(base+"/g/p/-/issues/7"); got != link {
				t.Errorf("MatchKey(%q) = %q, want the key of GitLab's link %q", created.URL, got, link)
			}
		})
	}
}

// The create warning names the gitlab.web_url that cures it: the web URL's
// scheme and host, followed by the relative URL root in front of the project's
// path, which the project scope keys on. Set to that value, the tracker
// recognizes the ref, holds it in the project, and stops warning. A
// gitlab.web_url already set is named in the warning.
func TestUnrecognizedRefWarningNamesTheWebAddress(t *testing.T) {
	tests := []struct {
		name        string
		apiURL      string
		projectPath string
		webURL      string
		ref         string
		wantAddress string
	}{
		{name: "GitLab at the host root", apiURL: "http://10.0.0.5:8080", projectPath: "g/p", ref: "https://corp.example.org/g/p/-/issues/1", wantAddress: "https://corp.example.org"},
		{name: "GitLab under a relative URL root", apiURL: "http://10.0.0.5:8080/git", projectPath: "g/p", ref: "https://corp.example.org/git/g/p/-/issues/1", wantAddress: "https://corp.example.org/git"},
		{name: "nested group, spelled in another case", apiURL: "http://10.0.0.5:8080/git", projectPath: "Grp/Sub/P", ref: "https://corp.example.org/Git/grp/sub/p/-/issues/1", wantAddress: "https://corp.example.org/git"},
		{name: "project known only by ID", apiURL: "http://10.0.0.5:8080/git", ref: "https://corp.example.org/git/acme/widgets/-/issues/1", wantAddress: "https://corp.example.org"},
		{name: "gitlab.web_url names another address", apiURL: "http://10.0.0.5:8080", projectPath: "g/p", webURL: "https://old.example.org", ref: "https://corp.example.org/g/p/-/issues/1", wantAddress: "https://corp.example.org"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &Tracker{client: NewClient("tok", tt.apiURL, "42"), projectPath: tt.projectPath, webURL: tt.webURL}
			warnings := tr.unrecognizedRefWarnings(tt.ref)
			if len(warnings) != 1 || !strings.Contains(warnings[0], "(e.g. "+tt.wantAddress+")") {
				t.Fatalf("warnings = %q, want one suggesting %s", warnings, tt.wantAddress)
			}
			where := "not under gitlab.url,"
			if tt.webURL != "" {
				where = "under neither gitlab.url nor gitlab.web_url (" + tt.webURL + "),"
			}
			if !strings.Contains(warnings[0], where) {
				t.Errorf("warning = %q, want it to say the ref is %s", warnings[0], where)
			}

			tr.webURL = tt.wantAddress
			if !tr.IsExternalRef(tt.ref) {
				t.Errorf("with gitlab.web_url %s, IsExternalRef(%q) = false", tt.wantAddress, tt.ref)
			}
			if got := tr.unrecognizedRefWarnings(tt.ref); len(got) != 0 {
				t.Errorf("with gitlab.web_url %s, warnings = %q, want none", tt.wantAddress, got)
			}
			if tt.projectPath != "" && !tr.ProjectScope().Contains(context.Background(), &types.Issue{ID: "bd-1", ExternalRef: &tt.ref}) {
				t.Errorf("with gitlab.web_url %s, ProjectScope.Contains(%q) = false", tt.wantAddress, tt.ref)
			}
		})
	}
}
