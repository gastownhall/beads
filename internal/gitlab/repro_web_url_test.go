package gitlab

import (
	"context"
	"encoding/json"
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

// TestReproPushTwiceWithWebURLOnOtherHostCreatesOneIssue (bd-0nikt4, PR #8633):
// GitLab reached at gitlab.url but writing web_url on another host (its
// external_url, set as gitlab.web_url) must not get a new issue on every push.
func TestReproPushTwiceWithWebURLOnOtherHostCreatesOneIssue(t *testing.T) {
	const webHost = "https://git.example.org"
	var mu sync.Mutex
	creates := 0
	issues := map[int]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/api/v4")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && path == "/projects/42/issues":
			creates++
			body, _ := io.ReadAll(r.Body)
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			iid := creates
			iss := map[string]any{
				"id": 1000 + iid, "iid": iid, "project_id": 42, "title": in["title"], "description": in["description"],
				"state": "opened", "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-01T00:00:00Z",
				"web_url": webHost + "/group/proj/-/issues/" + strconv.Itoa(iid),
			}
			issues[iid] = iss
			_ = json.NewEncoder(w).Encode(iss)
		case strings.HasPrefix(path, "/projects/42/issues/"):
			iid, _ := strconv.Atoi(strings.TrimPrefix(path, "/projects/42/issues/"))
			iss, ok := issues[iid]
			if !ok {
				http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(iss)
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("BEADS_DIR", "")
	t.Setenv("GITLAB_URL", srv.URL)
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("GITLAB_PROJECT_ID", "42")
	t.Setenv("GITLAB_PROJECT_PATH", "group/proj")
	t.Setenv("GITLAB_WEB_URL", webHost)

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
	ref := ""
	if r := store.issues["bd-1"].ExternalRef; r != nil {
		ref = *r
	}
	if creates != 1 {
		t.Fatalf("two pushes created %d GitLab issues, want 1 (external_ref now %q)", creates, ref)
	}
}
