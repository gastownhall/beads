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
