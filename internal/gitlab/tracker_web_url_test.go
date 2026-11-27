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
