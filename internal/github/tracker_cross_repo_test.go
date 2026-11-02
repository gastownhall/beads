package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// The tracker under test syncs repository B (owner-b/repo-b). The local
// database also holds a bead linked to repository A's issue with the same
// number, which no B sync may read, fetch, update or relink
// (gastownhall/beads#7781).
const (
	crossRepoARef = "https://github.com/owner-a/repo-a/issues/1313"
	crossRepoBRef = "https://github.com/owner-b/repo-b/issues/1313"
	crossRepoAID  = "bd-a1313"
	crossRepoAT   = "Repo A issue 1313"
)

var (
	crossRepoSync   = time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	crossRepoLocal  = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	crossRepoRemote = time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
)

// crossRepoStore is a minimal in-memory tracker.Store. SearchIssues lists
// issues lowest ID first; GetIssueByExternalRef reports a miss as a wrapped
// storage.ErrNotFound, as DoltStore does; every write is counted per issue.
type crossRepoStore struct {
	issues map[string]*types.Issue
	config map[string]string
	meta   map[string]string
	writes map[string]int
	next   int
}

func newCrossRepoStore(issues ...*types.Issue) *crossRepoStore {
	s := &crossRepoStore{
		issues: make(map[string]*types.Issue, len(issues)),
		config: map[string]string{"issue_prefix": "bd"},
		meta:   map[string]string{},
		writes: map[string]int{},
	}
	for _, iss := range issues {
		s.issues[iss.ID] = iss
	}
	return s
}

func (s *crossRepoStore) GetConfig(_ context.Context, key string) (string, error) {
	return s.config[key], nil
}
func (s *crossRepoStore) GetAllConfig(context.Context) (map[string]string, error) {
	return s.config, nil
}
func (s *crossRepoStore) GetLocalMetadata(_ context.Context, key string) (string, error) {
	return s.meta[key], nil
}
func (s *crossRepoStore) SetLocalMetadata(_ context.Context, key, value string) error {
	s.meta[key] = value
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
		iss.ID = fmt.Sprintf("bd-new%d", s.next)
	}
	s.issues[iss.ID] = iss
	s.writes[iss.ID]++
	return nil
}
func (s *crossRepoStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	iss := s.issues[id]
	if iss == nil {
		return fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	s.writes[id]++
	for key, value := range updates {
		switch key {
		case "title":
			iss.Title = value.(string)
		case "description":
			iss.Description = value.(string)
		case "status":
			iss.Status = types.Status(value.(string))
		case "issue_type":
			iss.IssueType = types.IssueType(value.(string))
		case "priority":
			iss.Priority = value.(int)
		case "assignee":
			iss.Assignee = value.(string)
		case "external_ref":
			ref := value.(string)
			iss.ExternalRef = &ref
		}
	}
	return nil
}
func (s *crossRepoStore) ApplyIssueUpdate(ctx context.Context, id string, updates map[string]interface{}, _ []string, actor string) error {
	return s.UpdateIssue(ctx, id, updates, actor)
}
func (s *crossRepoStore) AddDependency(context.Context, *types.Dependency, string) error {
	return nil
}

// assertUntouched fails when the store wrote to the bead with id, or when its
// title or external_ref no longer hold the given values.
func (s *crossRepoStore) assertUntouched(t *testing.T, id, title, ref string) {
	t.Helper()
	iss := s.issues[id]
	if iss == nil {
		t.Fatalf("bead %s missing", id)
	}
	if s.writes[id] != 0 || iss.Title != title || iss.ExternalRef == nil || *iss.ExternalRef != ref {
		t.Errorf("bead %s written %d time(s): title=%q external_ref=%v, want title=%q external_ref=%q",
			id, s.writes[id], iss.Title, derefRef(iss.ExternalRef), title, ref)
	}
}

func derefRef(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func repoABead(updated time.Time) *types.Issue {
	ref := crossRepoARef
	return &types.Issue{
		ID:          crossRepoAID,
		Title:       crossRepoAT,
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
		UpdatedAt:   updated,
	}
}

// githubAPIStub answers GitHub REST requests for owner-b/repo-b and records
// them as "METHOD path". The tracker's client keeps BaseURL
// https://api.github.com (so RefScope sees the real hosts) while its
// transport sends every request to this server.
type githubAPIStub struct {
	mu     sync.Mutex
	calls  []string
	issues map[int]string // issue number -> JSON object
	list   []int          // numbers GET .../issues answers
}

func newGitHubAPIStub(t *testing.T) (*githubAPIStub, *Tracker) {
	t.Helper()
	stub := &githubAPIStub{issues: map[int]string{}}
	srv := httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(srv.Close)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.URL.Scheme = "http"
		req.URL.Host = srv.Listener.Addr().String()
		req.Host = req.URL.Host
		return http.DefaultTransport.RoundTrip(req)
	})
	client := NewClient("tok", "owner-b", "repo-b").WithHTTPClient(&http.Client{Transport: transport})
	client.Retry = RetryConfig{}
	return stub, &Tracker{client: client, config: DefaultMappingConfig()}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func (s *githubAPIStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")

	const prefix = "/repos/owner-b/repo-b/issues"
	switch {
	case r.URL.Path == prefix && r.Method == http.MethodGet:
		objs := make([]string, 0, len(s.list))
		for _, n := range s.list {
			objs = append(objs, s.issues[n])
		}
		_, _ = w.Write([]byte("[" + strings.Join(objs, ",") + "]"))
	case r.URL.Path == prefix && r.Method == http.MethodPost:
		_, _ = w.Write([]byte(gitHubIssueJSON(8, "Created", crossRepoRemote)))
	case strings.HasPrefix(r.URL.Path, prefix+"/"):
		var n int
		if _, err := fmt.Sscanf(strings.TrimPrefix(r.URL.Path, prefix+"/"), "%d", &n); err != nil || s.issues[n] == "" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(s.issues[n]))
	default:
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}
}

// requests returns the recorded requests whose path names issue number n.
func (s *githubAPIStub) requests(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	suffix := fmt.Sprintf("/issues/%d", n)
	var out []string
	for _, c := range s.calls {
		if strings.HasSuffix(c, suffix) {
			out = append(out, c)
		}
	}
	return out
}

// gitHubIssueJSON renders one owner-b/repo-b REST issue object.
func gitHubIssueJSON(number int, title string, updated time.Time) string {
	return fmt.Sprintf(`{"id":%d,"number":%d,"title":%q,"body":"","state":"open",`+
		`"html_url":"https://github.com/owner-b/repo-b/issues/%d",`+
		`"created_at":"2026-10-01T00:00:00Z","updated_at":%q}`,
		90000+number, number, title, number, updated.Format(time.RFC3339))
}

// TestPull_SameNumberInOtherRepoCreatesNewBead reproduces
// gastownhall/beads#7781: pulling repository B's issue 1313 must create a bead
// for it rather than fall back onto the bead linked to repository A's 1313.
func TestPull_SameNumberInOtherRepoCreatesNewBead(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts tracker.SyncOptions
	}{
		{"bulk", tracker.SyncOptions{Pull: true}},
		{"selective number", tracker.SyncOptions{Pull: true, IssueIDs: []string{"1313"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub, tr := newGitHubAPIStub(t)
			stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
			stub.list = []int{1313}
			store := newCrossRepoStore(repoABead(time.Time{}))

			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}

			store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
			if result.PullStats.Created != 1 || result.PullStats.Updated != 0 {
				t.Errorf("pull of repo B issue 1313: created=%d updated=%d, want created=1 updated=0",
					result.PullStats.Created, result.PullStats.Updated)
			}
			created, err := store.GetIssueByExternalRef(context.Background(), crossRepoBRef)
			if err != nil || created.ID == crossRepoAID || created.Title != "Repo B issue 1313" {
				t.Errorf("no new bead linked to %s: issue=%v err=%v", crossRepoBRef, created, err)
			}
		})
	}
}

// TestPull_SelectiveBeadIDLinkedToOtherRepoIsSkipped: selecting A's bead by
// ID gives B's tracker no identifier to fetch, so the pull skips it instead
// of fetching B's 1313 into it.
func TestPull_SelectiveBeadIDLinkedToOtherRepoIsSkipped(t *testing.T) {
	stub, tr := newGitHubAPIStub(t)
	stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
	store := newCrossRepoStore(repoABead(time.Time{}))

	result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(),
		tracker.SyncOptions{Pull: true, IssueIDs: []string{crossRepoAID}})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
	if got := stub.requests(1313); len(got) != 0 {
		t.Errorf("selective pull of %s requested B's issue: %v", crossRepoAID, got)
	}
	if result.PullStats.Skipped != 1 || result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
		t.Errorf("skipped=%d created=%d updated=%d, want skipped=1 created=0 updated=0",
			result.PullStats.Skipped, result.PullStats.Created, result.PullStats.Updated)
	}
}

// TestPull_PrelinkedOtherRepoBeadNotHydrated: an incremental pull hydrates
// beads linked since the last sync by fetching their issue; A's bead must not
// be hydrated from (and overwritten by) B's same-numbered issue.
func TestPull_PrelinkedOtherRepoBeadNotHydrated(t *testing.T) {
	stub, tr := newGitHubAPIStub(t)
	stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
	store := newCrossRepoStore(repoABead(crossRepoLocal))
	store.meta["github.last_sync"] = crossRepoSync.Format(time.RFC3339)

	result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
	if got := stub.requests(1313); len(got) != 0 {
		t.Errorf("pull hydrated %s from B's issue: %v", crossRepoAID, got)
	}
	if result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
		t.Errorf("created=%d updated=%d, want 0 and 0", result.PullStats.Created, result.PullStats.Updated)
	}
}
