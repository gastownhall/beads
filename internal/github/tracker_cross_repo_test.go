package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// crossRepoStore is a minimal in-memory tracker.Store for cross-repository
// sync tests, adapted from the GitLab one. SearchIssues lists issues lowest ID
// first, so the fallback index's last write per key wins deterministically.
// GetIssueByExternalRef reports a miss as a wrapped storage.ErrNotFound, as
// DoltStore does; the metadata map backs github.last_sync and the push hash
// cache; GetConfig reports the bd issue prefix.
type crossRepoStore struct {
	issues   map[string]*types.Issue
	deps     []types.Dependency
	metadata map[string]string
	next     int
}

func newCrossRepoStore(issues map[string]*types.Issue) *crossRepoStore {
	return &crossRepoStore{issues: issues, metadata: map[string]string{}}
}

func (s *crossRepoStore) GetConfig(_ context.Context, key string) (string, error) {
	if key == "issue_prefix" {
		return "bd", nil
	}
	return "", nil
}

func (s *crossRepoStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s *crossRepoStore) GetLocalMetadata(_ context.Context, key string) (string, error) {
	return s.metadata[key], nil
}

func (s *crossRepoStore) SetLocalMetadata(_ context.Context, key, value string) error {
	s.metadata[key] = value
	return nil
}

func (s *crossRepoStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	out := make([]*types.Issue, 0, len(s.issues))
	for _, iss := range s.issues {
		out = append(out, iss)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *crossRepoStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, iss := range s.issues {
		if iss.ExternalRef != nil && *iss.ExternalRef == ref {
			return iss, nil
		}
	}
	return nil, fmt.Errorf("%w: external_ref %s", storage.ErrNotFound, ref)
}

func (s *crossRepoStore) GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}

func (s *crossRepoStore) GetDependenciesWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}

func (s *crossRepoStore) CreateIssue(_ context.Context, iss *types.Issue, _ string) error {
	s.next++
	if iss.ID == "" {
		iss.ID = "bd-new" + strconv.Itoa(s.next)
	}
	s.issues[iss.ID] = iss
	return nil
}

func (s *crossRepoStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	iss := s.issues[id]
	if iss == nil {
		return nil
	}
	if v, ok := updates["title"].(string); ok {
		iss.Title = v
	}
	if v, ok := updates["status"].(string); ok {
		iss.Status = types.Status(v)
	}
	if v, ok := updates["assignee"].(string); ok {
		iss.Assignee = v
	}
	if v, ok := updates["external_ref"].(string); ok {
		iss.ExternalRef = &v
	}
	return nil
}

func (s *crossRepoStore) ApplyIssueUpdate(ctx context.Context, id string, updates map[string]interface{}, _ []string, actor string) error {
	return s.UpdateIssue(ctx, id, updates, actor)
}

func (s *crossRepoStore) AddDependency(_ context.Context, dep *types.Dependency, _ string) error {
	s.deps = append(s.deps, *dep)
	return nil
}

// beadByRef returns the ID of the bead linked to ref, or "".
func (s *crossRepoStore) beadByRef(ref string) string {
	for id, iss := range s.issues {
		if iss.ExternalRef != nil && *iss.ExternalRef == ref {
			return id
		}
	}
	return ""
}

// ghTransport serves GitHub REST traffic in-process through a fixed handler
// while recording every request, so tests keep BaseURL at
// https://api.github.com — the host a repository-scoped ref must name —
// without touching the network.
type ghTransport struct {
	handler  http.Handler
	requests []string
}

func (t *ghTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, req.Method+" "+req.URL.Path)
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// requestPaths returns the recorded requests as method/path pairs.
func (t *ghTransport) requestPaths() []string {
	return append([]string(nil), t.requests...)
}

// newCrossRepoTracker returns an initialized GitHub tracker for
// owner-b/repo-b whose REST traffic handler serves in-process.
func newCrossRepoTracker(handler http.Handler) (*Tracker, *ghTransport) {
	tp := &ghTransport{handler: handler}
	tr := &Tracker{
		client: NewClient("token", "owner-b", "repo-b").WithHTTPClient(&http.Client{Transport: tp}),
		config: DefaultMappingConfig(),
	}
	return tr, tp
}

// ghRoutes answers each API path with its canned JSON body and every other
// request 404.
func ghRoutes(routes map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// ghIssueJSON renders one GitHub REST issue object for sync fixtures.
func ghIssueJSON(id, number int, title, htmlURL, updatedAt string) string {
	return `{"id":` + strconv.Itoa(id) + `,"number":` + strconv.Itoa(number) +
		`,"title":"` + title + `","state":"open",` +
		`"created_at":"2026-10-01T00:00:00Z","updated_at":"` + updatedAt +
		`","html_url":"` + htmlURL + `"}`
}

// ghSyncTime parses one of the fixed fixture timestamps.
func ghSyncTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

// TestPull_SameNumberInOtherRepoCreatesNewBead reproduces
// gastownhall/beads#7781: a bead linked to repository A's issue 1313 must not
// be matched — and overwritten — when repository B's issue 1313 is pulled
// into the same database, in a bulk pull and in a selective pull by number.
func TestPull_SameNumberInOtherRepoCreatesNewBead(t *testing.T) {
	const (
		repoAIssue = "https://github.com/owner-a/repo-a/issues/1313"
		repoBIssue = "https://github.com/owner-b/repo-b/issues/1313"
	)
	issueJSON := ghIssueJSON(5001, 1313, "Repo B issue 1313", repoBIssue, "2026-10-02T00:00:00Z")

	for _, tt := range []struct {
		name  string
		routes map[string]string
		issueIDs []string
	}{
		{name: "bulk pull", routes: map[string]string{
			"/repos/owner-b/repo-b/issues": "[" + issueJSON + "]",
		}},
		{name: "selective pull by issue number", routes: map[string]string{
			"/repos/owner-b/repo-b/issues/1313": issueJSON,
		}, issueIDs: []string{"1313"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tr, _ := newCrossRepoTracker(ghRoutes(tt.routes))
			aRef := repoAIssue
			store := newCrossRepoStore(map[string]*types.Issue{
				"bd-a1313": {ID: "bd-a1313", Title: "Repo A issue 1313", Status: types.StatusOpen,
					IssueType: types.TypeTask, ExternalRef: &aRef},
			})

			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(),
				tracker.SyncOptions{Pull: true, IssueIDs: tt.issueIDs})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Created != 1 || result.PullStats.Updated != 0 {
				t.Errorf("pull of repository B's issue 1313: created=%d updated=%d, want created=1 updated=0",
					result.PullStats.Created, result.PullStats.Updated)
			}
			a := store.issues["bd-a1313"]
			if a.Title != "Repo A issue 1313" || a.ExternalRef == nil || *a.ExternalRef != repoAIssue {
				t.Errorf("repository A's bead overwritten: title=%q external_ref=%q",
					a.Title, derefExtRef(a.ExternalRef))
			}
			if newBead := store.beadByRef(repoBIssue); newBead == "" || newBead == "bd-a1313" {
				t.Errorf("bead linked to repository B's issue = %q, want a new bead", newBead)
			}
		})
	}
}

// TestPull_SelectiveBeadIDLinkedToOtherRepoIsSkipped pins the selective pull
// of a bead ID: a bead linked to another repository's issue has no resolvable
// identifier for this tracker, so the pull skips it without any request
// instead of fetching this repository's same-numbered issue and relinking
// the bead to it.
func TestPull_SelectiveBeadIDLinkedToOtherRepoIsSkipped(t *testing.T) {
	const repoAIssue = "https://github.com/owner-a/repo-a/issues/1313"
	tr, tp := newCrossRepoTracker(ghRoutes(map[string]string{
		"/repos/owner-b/repo-b/issues/1313": ghIssueJSON(5001, 1313, "Repo B issue 1313",
			"https://github.com/owner-b/repo-b/issues/1313", "2026-10-02T00:00:00Z"),
	}))
	aRef := repoAIssue
	store := newCrossRepoStore(map[string]*types.Issue{
		"bd-a1313": {ID: "bd-a1313", Title: "Repo A issue 1313", Status: types.StatusOpen,
			IssueType: types.TypeTask, ExternalRef: &aRef},
	})

	result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(),
		tracker.SyncOptions{Pull: true, IssueIDs: []string{"bd-a1313"}})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Skipped != 1 || result.PullStats.Errors != 0 {
		t.Errorf("selective pull of other-repo bead: skipped=%d errors=%d, want skipped=1 errors=0",
			result.PullStats.Skipped, result.PullStats.Errors)
	}
	if got := tp.requestPaths(); len(got) != 0 {
		t.Errorf("pull issued requests %q, want none", got)
	}
	a := store.issues["bd-a1313"]
	if a.Title != "Repo A issue 1313" || a.ExternalRef == nil || *a.ExternalRef != repoAIssue {
		t.Errorf("repository A's bead overwritten: title=%q external_ref=%q",
			a.Title, derefExtRef(a.ExternalRef))
	}
}

// TestPull_PrelinkedOtherRepoBeadNotHydrated pins pre-linked hydration: a
// bead linked to another repository's issue, locally edited after the last
// sync, must not be hydrated from this repository's same-numbered issue. The
// store has no ref-history capability, so changed-after is the timestamp
// heuristic.
func TestPull_PrelinkedOtherRepoBeadNotHydrated(t *testing.T) {
	const repoAIssue = "https://github.com/owner-a/repo-a/issues/1313"
	tr, tp := newCrossRepoTracker(ghRoutes(map[string]string{
		"/repos/owner-b/repo-b/issues":      "[]",
		"/repos/owner-b/repo-b/issues/1313": ghIssueJSON(5001, 1313, "Repo B issue 1313",
			"https://github.com/owner-b/repo-b/issues/1313", "2026-10-02T00:00:00Z"),
	}))
	aRef := repoAIssue
	store := newCrossRepoStore(map[string]*types.Issue{
		"bd-a1313": {ID: "bd-a1313", Title: "Repo A issue 1313", Status: types.StatusOpen,
			IssueType: types.TypeTask, ExternalRef: &aRef,
			CreatedAt: ghSyncTime(t, "2026-10-01T00:00:00Z"),
			UpdatedAt: ghSyncTime(t, "2026-10-03T00:00:00Z")},
	})
	store.metadata["github.last_sync"] = "2026-10-02T00:00:00Z"

	result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(),
		tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
		t.Errorf("incremental pull: created=%d updated=%d, want 0 and 0",
			result.PullStats.Created, result.PullStats.Updated)
	}
	if got := tp.requestPaths(); len(got) != 1 || got[0] != "GET /repos/owner-b/repo-b/issues" {
		t.Errorf("pull issued requests %q, want only the bulk listing", got)
	}
	a := store.issues["bd-a1313"]
	if a.Title != "Repo A issue 1313" || a.ExternalRef == nil || *a.ExternalRef != repoAIssue {
		t.Errorf("repository A's bead overwritten: title=%q external_ref=%q",
			a.Title, derefExtRef(a.ExternalRef))
	}
}

// derefExtRef returns the issue's external_ref, or "<nil>".
func derefExtRef(ref *string) string {
	if ref == nil {
		return "<nil>"
	}
	return *ref
}