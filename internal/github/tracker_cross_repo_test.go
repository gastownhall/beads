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

// Cross-repository fixtures for gastownhall/beads#7781: one beads database
// holds a bead linked to issue 42 of acme/repo-a while the tracker is
// configured for acme/repo-b, which has its own issue 42.
const (
	repoA42Ref = "https://github.com/acme/repo-a/issues/42"
	repoB42Ref = "https://github.com/acme/repo-b/issues/42"
	repoB7Ref  = "https://github.com/acme/repo-b/issues/7"

	repoATitle = "Repo A issue 42"
	repoABody  = "repo-a body"
)

// crossRepoStore is a minimal in-memory tracker.Store. SearchIssues lists
// issues lowest ID first so fallback matching is deterministic, and
// GetIssueByExternalRef reports a miss as a wrapped storage.ErrNotFound, as
// DoltStore does. issue_prefix is "bd" so bead IDs are recognized by a
// selective pull.
type crossRepoStore struct {
	mu       sync.Mutex
	issues   map[string]*types.Issue
	metadata map[string]string
	next     int
}

func newCrossRepoStore(issues ...*types.Issue) *crossRepoStore {
	s := &crossRepoStore{issues: map[string]*types.Issue{}, metadata: map[string]string{}}
	for _, iss := range issues {
		s.issues[iss.ID] = iss
	}
	return s
}

func (s *crossRepoStore) GetConfig(_ context.Context, key string) (string, error) {
	if key == "issue_prefix" {
		return "bd", nil
	}
	return "", nil
}
func (s *crossRepoStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{"issue_prefix": "bd"}, nil
}
func (s *crossRepoStore) GetLocalMetadata(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata[key], nil
}
func (s *crossRepoStore) SetLocalMetadata(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadata[key] = value
	return nil
}
func (s *crossRepoStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*types.Issue, 0, len(s.issues))
	for _, iss := range s.issues {
		out = append(out, iss)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *crossRepoStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	if iss.ID == "" {
		iss.ID = "bd-new" + strconv.Itoa(s.next)
	}
	s.issues[iss.ID] = iss
	return nil
}
func (s *crossRepoStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return nil
}
func (s *crossRepoStore) ApplyIssueUpdate(ctx context.Context, id string, updates map[string]interface{}, _ []string, actor string) error {
	return s.UpdateIssue(ctx, id, updates, actor)
}
func (s *crossRepoStore) AddDependency(context.Context, *types.Dependency, string) error {
	return nil
}

func (s *crossRepoStore) issue(id string) types.Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if iss := s.issues[id]; iss != nil {
		return *iss
	}
	return types.Issue{}
}

// recordingTransport serves fixture JSON keyed by "METHOD path" (the query is
// ignored), answers anything else 404, and logs every request it sees.
type recordingTransport struct {
	mu       sync.Mutex
	routes   map[string]string
	requests []string
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	key := r.Method + " " + r.URL.Path
	rt.mu.Lock()
	rt.requests = append(rt.requests, key)
	body, ok := rt.routes[key]
	rt.mu.Unlock()
	status := http.StatusOK
	if !ok {
		status, body = http.StatusNotFound, `{"message":"Not Found"}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (rt *recordingTransport) sent(key string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, req := range rt.requests {
		if req == key {
			return true
		}
	}
	return false
}

func (rt *recordingTransport) log() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.requests...)
}

// newCrossRepoTracker returns a tracker configured for acme/repo-b on the
// default api.github.com base URL. The base URL is deliberately not an
// httptest server: RefScope reads the client's host, so pointing it at
// 127.0.0.1 would make every github.com fixture ref foreign and let the
// cross-repository assertions pass vacuously.
func newCrossRepoTracker(routes map[string]string) (*Tracker, *recordingTransport) {
	rt := &recordingTransport{routes: routes}
	client := NewClient("token", "acme", "repo-b").WithHTTPClient(&http.Client{Transport: rt})
	return &Tracker{client: client, config: DefaultMappingConfig()}, rt
}

// gitHubIssueJSON renders one GitHub REST issue object.
func gitHubIssueJSON(number int, title, body, htmlURL string, updated time.Time) string {
	return fmt.Sprintf(`{"id":%d,"number":%d,"title":%q,"body":%q,"state":"open","html_url":%q,`+
		`"created_at":"2026-10-01T00:00:00Z","updated_at":%q}`,
		900000+number, number, title, body, htmlURL, updated.UTC().Format(time.RFC3339))
}

func linkedBead(id, title, body, ref string, updated time.Time) *types.Issue {
	return &types.Issue{
		ID:          id,
		Title:       title,
		Description: body,
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: &ref,
		CreatedAt:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   updated,
	}
}

func repoABead(updated time.Time) *types.Issue {
	return linkedBead("bd-a42", repoATitle, repoABody, repoA42Ref, updated)
}

// assertRepoABeadUnchanged fails when anything overwrote the repo-a bead.
func assertRepoABeadUnchanged(t *testing.T, store *crossRepoStore) {
	t.Helper()
	a := store.issue("bd-a42")
	if a.Title != repoATitle || a.Description != repoABody || a.ExternalRef == nil || *a.ExternalRef != repoA42Ref {
		t.Errorf("repo-a bead overwritten: title=%q description=%q external_ref=%v",
			a.Title, a.Description, derefRef(a.ExternalRef))
	}
}

func derefRef(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

var (
	fixtureUpdated = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	repoB42JSON    = gitHubIssueJSON(42, "Repo B issue 42", "repo-b body", repoB42Ref, fixtureUpdated)
)

// TestPull_SameNumberInOtherRepoCreatesNewBead reproduces gastownhall/beads#7781:
// pulling issue 42 of acme/repo-b must not match (and overwrite) the bead
// linked to issue 42 of acme/repo-a, by any pull path.
func TestPull_SameNumberInOtherRepoCreatesNewBead(t *testing.T) {
	ctx := context.Background()

	t.Run("selective by number", func(t *testing.T) {
		tr, _ := newCrossRepoTracker(map[string]string{"GET /repos/acme/repo-b/issues/42": repoB42JSON})
		store := newCrossRepoStore(repoABead(time.Time{}))

		result, err := tracker.NewEngine(tr, store, "test").Sync(ctx, tracker.SyncOptions{Pull: true, IssueIDs: []string{"42"}})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		assertRepoABeadUnchanged(t, store)
		if result.PullStats.Created != 1 || result.PullStats.Updated != 0 {
			t.Errorf("pull of repo-b #42: created=%d updated=%d, want created=1 updated=0",
				result.PullStats.Created, result.PullStats.Updated)
		}
	})

	t.Run("bulk", func(t *testing.T) {
		tr, _ := newCrossRepoTracker(map[string]string{"GET /repos/acme/repo-b/issues": "[" + repoB42JSON + "]"})
		store := newCrossRepoStore(repoABead(time.Time{}))

		result, err := tracker.NewEngine(tr, store, "test").Sync(ctx, tracker.SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		assertRepoABeadUnchanged(t, store)
		if result.PullStats.Created != 1 || result.PullStats.Updated != 0 {
			t.Errorf("bulk pull of repo-b: created=%d updated=%d, want created=1 updated=0",
				result.PullStats.Created, result.PullStats.Updated)
		}
	})

	t.Run("selective by bead ID", func(t *testing.T) {
		tr, rt := newCrossRepoTracker(map[string]string{"GET /repos/acme/repo-b/issues/42": repoB42JSON})
		store := newCrossRepoStore(repoABead(time.Time{}))
		engine := tracker.NewEngine(tr, store, "test")
		var warnings []string
		engine.OnWarning = func(msg string) { warnings = append(warnings, msg) }

		result, err := engine.Sync(ctx, tracker.SyncOptions{Pull: true, IssueIDs: []string{"bd-a42"}})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		assertRepoABeadUnchanged(t, store)
		if rt.sent("GET /repos/acme/repo-b/issues/42") {
			t.Errorf("pull of bd-a42 fetched repo-b #42; requests=%v", rt.log())
		}
		if result.PullStats.Skipped != 1 {
			t.Errorf("pull of bd-a42: skipped=%d, want 1", result.PullStats.Skipped)
		}
		joined := strings.Join(warnings, "\n")
		if !strings.Contains(joined, "bd-a42") || strings.Contains(joined, "No external ref found") {
			t.Errorf("warnings = %q, want one naming bd-a42 as linked outside the configured repository", joined)
		}
	})

	t.Run("prelinked hydration", func(t *testing.T) {
		lastSync := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
		tr, rt := newCrossRepoTracker(map[string]string{
			"GET /repos/acme/repo-b/issues":    "[]",
			"GET /repos/acme/repo-b/issues/42": repoB42JSON,
		})
		store := newCrossRepoStore(repoABead(lastSync.Add(time.Hour)))
		store.metadata["github.last_sync"] = lastSync.Format(time.RFC3339)

		if _, err := tracker.NewEngine(tr, store, "test").Sync(ctx, tracker.SyncOptions{Pull: true}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		assertRepoABeadUnchanged(t, store)
		if rt.sent("GET /repos/acme/repo-b/issues/42") {
			t.Errorf("pull hydrated repo-b #42 for the repo-a bead; requests=%v", rt.log())
		}
	})
}

// TestPull_SameRepoRespellingsUpdateInPlace guards the repository scoping
// against over-reach: a bead linked to the configured repository's issue by a
// ref spelling the exact lookup misses must still be matched and updated in
// place, its ref rewritten to the issue's HTML URL.
func TestPull_SameRepoRespellingsUpdateInPlace(t *testing.T) {
	for _, localRef := range []string{
		"https://github.com/ACME/Repo-B/issues/42",
		"https://api.github.com/repos/acme/repo-b/issues/42",
		"github:42",
	} {
		t.Run(localRef, func(t *testing.T) {
			ctx := context.Background()
			tr, _ := newCrossRepoTracker(map[string]string{"GET /repos/acme/repo-b/issues/42": repoB42JSON})
			store := newCrossRepoStore(linkedBead("bd-b42", "Old title", "old body", localRef, time.Time{}))
			engine := tracker.NewEngine(tr, store, "test")
			opts := tracker.SyncOptions{Pull: true, IssueIDs: []string{"42"}}

			result, err := engine.Sync(ctx, opts)
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Created != 0 || result.PullStats.Updated != 1 {
				t.Errorf("pull of repo-b #42: created=%d updated=%d, want created=0 updated=1",
					result.PullStats.Created, result.PullStats.Updated)
			}
			b := store.issue("bd-b42")
			if b.Title != "Repo B issue 42" || b.ExternalRef == nil || *b.ExternalRef != repoB42Ref {
				t.Errorf("bd-b42 after pull: title=%q external_ref=%v, want %q and %q",
					b.Title, derefRef(b.ExternalRef), "Repo B issue 42", repoB42Ref)
			}

			again, err := engine.Sync(ctx, opts)
			if err != nil {
				t.Fatalf("second Sync: %v", err)
			}
			if again.PullStats.Created != 0 {
				t.Errorf("second pull created %d beads, want 0", again.PullStats.Created)
			}
		})
	}
}

// pushRoutes serves repo-b's issues 7 and 42 as last changed before the local
// edits, and accepts any write a push might send, so a test sees every write
// the engine attempts rather than an error that hides it.
func pushRoutes() map[string]string {
	repoB7JSON := gitHubIssueJSON(7, "Repo B issue 7", "repo-b 7 body", repoB7Ref, fixtureUpdated)
	return map[string]string{
		"GET /repos/acme/repo-b/issues/7":    repoB7JSON,
		"GET /repos/acme/repo-b/issues/42":   repoB42JSON,
		"PATCH /repos/acme/repo-b/issues/7":  repoB7JSON,
		"PATCH /repos/acme/repo-b/issues/42": repoB42JSON,
		"POST /repos/acme/repo-b/issues":     gitHubIssueJSON(99, "Created", "", "https://github.com/acme/repo-b/issues/99", fixtureUpdated),
	}
}

// TestPush_SkipsBeadLinkedToOtherRepo: pushing to acme/repo-b updates the
// bead linked to repo-b's issue 7 but neither writes repo-b's issue 42 with
// the content of the bead linked to repo-a's issue 42 nor re-creates that
// bead as a new repo-b issue.
func TestPush_SkipsBeadLinkedToOtherRepo(t *testing.T) {
	ctx := context.Background()
	localEdit := fixtureUpdated.Add(72 * time.Hour)
	tr, rt := newCrossRepoTracker(pushRoutes())
	store := newCrossRepoStore(
		repoABead(localEdit),
		linkedBead("bd-b7", "Repo B issue 7 edited", "repo-b 7 body", repoB7Ref, localEdit),
	)
	engine := tracker.NewEngine(tr, store, "test")

	live, err := engine.Sync(ctx, tracker.SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !rt.sent("PATCH /repos/acme/repo-b/issues/7") {
		t.Errorf("push did not update repo-b #7; requests=%v", rt.log())
	}
	if rt.sent("PATCH /repos/acme/repo-b/issues/42") {
		t.Errorf("push wrote the repo-a bead onto repo-b #42; requests=%v", rt.log())
	}
	if rt.sent("POST /repos/acme/repo-b/issues") {
		t.Errorf("push created a repo-b issue for the repo-a bead; requests=%v", rt.log())
	}
	assertRepoABeadUnchanged(t, store)
	if got := live.PushStats; got.Created != 0 || got.Updated != 1 || got.Skipped != 1 {
		t.Errorf("push stats = {Created:%d Updated:%d Skipped:%d}, want {Created:0 Updated:1 Skipped:1}",
			got.Created, got.Updated, got.Skipped)
	}
}
