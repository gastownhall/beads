package gitlab

import (
	"context"
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := initWebBaseTracker(t, tt.projectJSON, nil)
			if tr.webBase != tt.want {
				t.Fatalf("learned web base = %q, want %q", tr.webBase, tt.want)
			}
		})
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
