package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// The tests in this file reproduce gastownhall/beads#7781: a bead linked to
// one repository's issue must not be matched, overwritten, re-fetched, or
// pushed onto another repository's issue that happens to carry the same
// number. They exercise the real sync engine against a GitHub tracker and a
// recording transport; the client keeps the default api.github.com base URL
// so RefScope resolves github.com and api.github.com refs as in production.

const (
	repoAIssue42Ref = "https://github.com/acme/repo-a/issues/42"
	repoBIssue42Ref = "https://github.com/acme/repo-b/issues/42"
	repoBIssue7Ref  = "https://github.com/acme/repo-b/issues/7"
)

// ghIssueJSON renders one GitHub REST issue object for pull and push
// fixtures. Its updated_at (2026-10-02) predates every locally edited bead
// these tests seed, so the push timestamp check wants to update them.
func ghIssueJSON(id, number int, title, body, htmlURL string) string {
	return fmt.Sprintf(`{"id":%d,"number":%d,"title":%s,"body":%s,"state":"open","html_url":%s,`+
		`"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z"}`,
		id, number, strconv.Quote(title), strconv.Quote(body), strconv.Quote(htmlURL))
}

// ghFixtureTransport serves canned JSON bodies keyed by "METHOD path" (the
// query is ignored) and records every request it sees, so tests can assert
// that no request reached an endpoint. An un-routed request answers 404.
type ghFixtureTransport struct {
	mu       sync.Mutex
	routes   map[string]string
	requests []string
}

func (t *ghFixtureTransport) route(method, path, body string) {
	if t.routes == nil {
		t.routes = map[string]string{}
	}
	t.routes[method+" "+path] = body
}

func (t *ghFixtureTransport) respond(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func (t *ghFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req.Method+" "+req.URL.Path)
	body, ok := t.routes[req.Method+" "+req.URL.Path]
	t.mu.Unlock()
	if !ok {
		return t.respond(http.StatusNotFound, `{"message":"Not Found"}`), nil
	}
	return t.respond(http.StatusOK, body), nil
}

func (t *ghFixtureTransport) seen(method, path string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	want := method + " " + path
	for _, r := range t.requests {
		if r == want {
			return true
		}
	}
	return false
}

func (t *ghFixtureTransport) requestLog() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.requests...)
}

// newGHTestTracker returns a GitHub tracker for acme/repo-b whose client
// talks to rt instead of the network. The base URL stays the default
// https://api.github.com so RefScope keeps resolving github.com and
// api.github.com refs.
func newGHTestTracker(rt *ghFixtureTransport) *Tracker {
	return &Tracker{
		client: NewClient("token", "acme", "repo-b").WithHTTPClient(&http.Client{Transport: rt}),
		config: DefaultMappingConfig(),
	}
}

// ghCrossRepoStore is a minimal in-memory tracker.Store for the cross-repo
// tests, modeled on internal/gitlab's crossProjectStore. It answers
// issue_prefix "bd" so a selective pull can name beads by ID, keeps local
// metadata (github.last_sync), reports an external-ref miss as a wrapped
// storage.ErrNotFound as DoltStore does, and applies the update map the pull
// path builds.
type ghCrossRepoStore struct {
	issues map[string]*types.Issue
	config map[string]string
	local  map[string]string
	next   int
}

func newGHCrossRepoStore(beads ...*types.Issue) *ghCrossRepoStore {
	s := &ghCrossRepoStore{
		issues: map[string]*types.Issue{},
		config: map[string]string{"issue_prefix": "bd"},
		local:  map[string]string{},
	}
	for _, b := range beads {
		s.issues[b.ID] = b
	}
	return s
}

func (s *ghCrossRepoStore) GetConfig(_ context.Context, key string) (string, error) {
	return s.config[key], nil
}

func (s *ghCrossRepoStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s *ghCrossRepoStore) GetLocalMetadata(_ context.Context, key string) (string, error) {
	return s.local[key], nil
}

func (s *ghCrossRepoStore) SetLocalMetadata(_ context.Context, key, value string) error {
	s.local[key] = value
	return nil
}

func (s *ghCrossRepoStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	out := make([]*types.Issue, 0, len(s.issues))
	for _, iss := range s.issues {
		out = append(out, iss)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *ghCrossRepoStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, iss := range s.issues {
		if iss.ExternalRef != nil && *iss.ExternalRef == ref {
			return iss, nil
		}
	}
	return nil, fmt.Errorf("%w: external_ref %s", storage.ErrNotFound, ref)
}

func (s *ghCrossRepoStore) GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}

func (s *ghCrossRepoStore) GetDependenciesWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}

func (s *ghCrossRepoStore) CreateIssue(_ context.Context, iss *types.Issue, _ string) error {
	s.next++
	if iss.ID == "" {
		iss.ID = fmt.Sprintf("bd-new%d", s.next)
	}
	s.issues[iss.ID] = iss
	return nil
}

func (s *ghCrossRepoStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	return s.applyUpdates(id, updates)
}

func (s *ghCrossRepoStore) ApplyIssueUpdate(_ context.Context, id string, updates map[string]interface{}, _ []string, _ string) error {
	return s.applyUpdates(id, updates)
}

func (s *ghCrossRepoStore) applyUpdates(id string, updates map[string]interface{}) error {
	iss := s.issues[id]
	if iss == nil {
		return nil
	}
	if v, ok := updates["title"].(string); ok {
		iss.Title = v
	}
	if v, ok := updates["description"].(string); ok {
		iss.Description = v
	}
	if v, ok := updates["external_ref"].(string); ok {
		iss.ExternalRef = &v
	}
	if v, ok := updates["status"].(string); ok {
		iss.Status = types.Status(v)
	}
	if v, ok := updates["issue_type"].(string); ok {
		iss.IssueType = types.IssueType(v)
	}
	if v, ok := updates["priority"].(int); ok {
		iss.Priority = v
	}
	return nil
}

func (s *ghCrossRepoStore) AddDependency(_ context.Context, dep *types.Dependency, _ string) error {
	return nil
}

// newRepoBead builds a local bead linked to ref, edited after 2026-10-02 so
// a push sees it as newer than the fixtures.
func newRepoBead(id, title, ref string) *types.Issue {
	return &types.Issue{
		ID:          id,
		Title:       title,
		Description: title + " (local)",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
		UpdatedAt:   time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
	}
}

func ghDerefRef(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func ghBeadIDByRef(store *ghCrossRepoStore, ref string) string {
	for id, iss := range store.issues {
		if iss.ExternalRef != nil && *iss.ExternalRef == ref {
			return id
		}
	}
	return ""
}

// TestPull_SameNumberInOtherRepoCreatesNewBead covers #7781's pull half:
// pulling acme/repo-b's issue 42 into a database holding acme/repo-a's
// issue 42 must create a second bead, not overwrite the repo-A one.
func TestPull_SameNumberInOtherRepoCreatesNewBead(t *testing.T) {
	assertRepoABeadUntouched := func(t *testing.T, store *ghCrossRepoStore) {
		t.Helper()
		a := store.issues["bd-a42"]
		if a == nil {
			t.Fatalf("repo-A bead bd-a42 vanished from the store")
		}
		if a.Title != "Repo A issue 42" || a.Description != "Repo A issue 42 (local)" ||
			a.ExternalRef == nil || *a.ExternalRef != repoAIssue42Ref {
			t.Errorf("repo-A bead overwritten by repo-B pull: title=%q description=%q external_ref=%v",
				a.Title, a.Description, ghDerefRef(a.ExternalRef))
		}
	}

	t.Run("bulk pull", func(t *testing.T) {
		rt := &ghFixtureTransport{}
		rt.route(http.MethodGet, "/repos/acme/repo-b/issues",
			"["+ghIssueJSON(9001, 42, "Repo B issue 42 (remote)", "Repo B body", repoBIssue42Ref)+"]")
		store := newGHCrossRepoStore(newRepoBead("bd-a42", "Repo A issue 42", repoAIssue42Ref))

		result, err := tracker.NewEngine(newGHTestTracker(rt), store, "test").
			Sync(context.Background(), tracker.SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Created != 1 || result.PullStats.Updated != 0 {
			t.Errorf("bulk pull of repo-B issue 42: created=%d updated=%d, want created=1 updated=0 (a second bead)",
				result.PullStats.Created, result.PullStats.Updated)
		}
		assertRepoABeadUntouched(t, store)
		if id := ghBeadIDByRef(store, repoBIssue42Ref); id == "" || id == "bd-a42" {
			t.Errorf("repo-B issue 42 landed on bead %q, want a distinct new bead linked to %s", id, repoBIssue42Ref)
		}
	})

	t.Run("selective pull by bead id", func(t *testing.T) {
		rt := &ghFixtureTransport{}
		rt.route(http.MethodGet, "/repos/acme/repo-b/issues/42",
			ghIssueJSON(9001, 42, "Repo B issue 42 (remote)", "Repo B body", repoBIssue42Ref))
		store := newGHCrossRepoStore(newRepoBead("bd-a42", "Repo A issue 42", repoAIssue42Ref))
		var warnings []string
		engine := tracker.NewEngine(newGHTestTracker(rt), store, "test")
		engine.OnWarning = func(msg string) { warnings = append(warnings, msg) }

		result, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true, IssueIDs: []string{"bd-a42"}})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if rt.seen(http.MethodGet, "/repos/acme/repo-b/issues/42") {
			t.Errorf("pull of bd-a42 fetched repo-B issue 42, though the bead is linked to repo-A")
		}
		if result.PullStats.Created != 0 || result.PullStats.Updated != 0 || result.PullStats.Skipped != 1 {
			t.Errorf("pull of bd-a42: created=%d updated=%d skipped=%d, want created=0 updated=0 skipped=1",
				result.PullStats.Created, result.PullStats.Updated, result.PullStats.Skipped)
		}
		var beadNamed bool
		for _, w := range warnings {
			if !strings.Contains(w, "bd-a42") {
				continue
			}
			beadNamed = true
			if strings.Contains(w, "No external ref found") {
				t.Errorf("warning %q claims bd-a42 has no external ref; it is linked outside the configured repository", w)
			}
		}
		if !beadNamed {
			t.Errorf("no warning named bd-a42: %q", warnings)
		}
		assertRepoABeadUntouched(t, store)
	})

	t.Run("pre-linked hydration", func(t *testing.T) {
		rt := &ghFixtureTransport{}
		rt.route(http.MethodGet, "/repos/acme/repo-b/issues", "[]") // since-list: nothing changed
		rt.route(http.MethodGet, "/repos/acme/repo-b/issues/42",
			ghIssueJSON(9001, 42, "Repo B issue 42 (remote)", "Repo B body", repoBIssue42Ref))
		store := newGHCrossRepoStore(newRepoBead("bd-a42", "Repo A issue 42", repoAIssue42Ref))
		store.local["github.last_sync"] = "2026-10-01T00:00:00Z"

		result, err := tracker.NewEngine(newGHTestTracker(rt), store, "test").
			Sync(context.Background(), tracker.SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if rt.seen(http.MethodGet, "/repos/acme/repo-b/issues/42") {
			t.Errorf("hydration fetched repo-B issue 42 for a bead linked to repo-A")
		}
		if result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
			t.Errorf("pull since last_sync: created=%d updated=%d, want 0 and 0 (repo-B reports nothing since last_sync)",
				result.PullStats.Created, result.PullStats.Updated)
		}
		assertRepoABeadUntouched(t, store)
	})
}
