package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
// issues lowest ID first, or fails with searchErr when it is set;
// GetIssueByExternalRef reports a miss as a wrapped storage.ErrNotFound, as
// DoltStore does; every write is counted per issue.
type crossRepoStore struct {
	issues    map[string]*types.Issue
	config    map[string]string
	meta      map[string]string
	writes    map[string]int
	next      int
	searchErr error
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
	if s.searchErr != nil {
		return nil, s.searchErr
	}
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
	// renamedFrom, when set, is the old /repos/{owner}/{repo} path of the
	// repository; see newRenamedRepoStub.
	renamedFrom string
	// fail maps a request path to the HTTP status the stub answers it with,
	// or to dropConnection.
	fail map[string]int
}

// dropConnection, as a githubAPIStub.fail status, fails the request the way
// a dropped connection does: the transport returns an error and no response.
const dropConnection = -1

func newGitHubAPIStub(t *testing.T) (*githubAPIStub, *Tracker) {
	t.Helper()
	stub := &githubAPIStub{issues: map[int]string{}}
	srv := httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(srv.Close)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if stub.drops(req) {
			return nil, errors.New("connection reset by peer")
		}
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

// newRenamedRepoStub is newGitHubAPIStub for a config that still names
// owner-b/repo-b by its old name, old-owner/repo-b. Like GitHub, the stub
// redirects every request under the old name to the repository's ID path
// (301 for GET, 307 for other methods), which it serves as owner-b/repo-b.
func newRenamedRepoStub(t *testing.T) (*githubAPIStub, *Tracker) {
	t.Helper()
	stub, tr := newGitHubAPIStub(t)
	stub.renamedFrom = "/repos/old-owner/repo-b"
	tr.client.Owner = "old-owner"
	return stub, tr
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// drops records req and reports whether the stub fails it as a dropped
// connection (see dropConnection).
func (s *githubAPIStub) drops(req *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail[req.URL.Path] != dropConnection {
		return false
	}
	s.calls = append(s.calls, req.Method+" "+req.URL.Path)
	return true
}

func (s *githubAPIStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if status := s.fail[r.URL.Path]; status > 0 {
		http.Error(w, `{"message":"Server Error"}`, status)
		return
	}

	const repoPath, idPath = "/repos/owner-b/repo-b", "/repositories/4242"
	if s.renamedFrom != "" {
		if rest, ok := cutPathPrefix(r.URL.Path, s.renamedFrom); ok {
			status := http.StatusMovedPermanently
			if r.Method != http.MethodGet {
				status = http.StatusTemporaryRedirect
			}
			w.Header().Set("Location", (&url.URL{Path: idPath + rest, RawQuery: r.URL.RawQuery}).String())
			w.WriteHeader(status)
			return
		}
	}
	path := r.URL.Path
	if rest, ok := cutPathPrefix(path, idPath); ok {
		path = repoPath + rest
	}

	const prefix = repoPath + "/issues"
	switch {
	case path == repoPath && r.Method == http.MethodGet:
		_, _ = w.Write([]byte(`{"id":4242,"name":"repo-b","full_name":"owner-b/repo-b",` +
			`"html_url":"https://github.com/owner-b/repo-b"}`))
	case path == "/repos/owner-a/repo-a" && r.Method == http.MethodGet:
		// Repository A exists, as another repository.
		_, _ = w.Write([]byte(`{"id":1111,"name":"repo-a","full_name":"owner-a/repo-a",` +
			`"html_url":"https://github.com/owner-a/repo-a"}`))
	case path == prefix && r.Method == http.MethodGet:
		objs := make([]string, 0, len(s.list))
		for _, n := range s.list {
			objs = append(objs, s.issues[n])
		}
		_, _ = w.Write([]byte("[" + strings.Join(objs, ",") + "]"))
	case path == prefix && r.Method == http.MethodPost:
		_, _ = w.Write([]byte(gitHubIssueJSON(8, "Created", crossRepoRemote)))
	case strings.HasPrefix(path, prefix+"/"):
		var n int
		if _, err := fmt.Sscanf(strings.TrimPrefix(path, prefix+"/"), "%d", &n); err != nil || s.issues[n] == "" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(s.issues[n]))
	default:
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}
}

// cutPathPrefix reports whether path is prefix or lies under it, and returns
// the part of path after prefix.
func cutPathPrefix(path, prefix string) (string, bool) {
	if path == prefix {
		return "", true
	}
	if strings.HasPrefix(path, prefix+"/") {
		return path[len(prefix):], true
	}
	return "", false
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

// requestLog returns every recorded request, in order.
func (s *githubAPIStub) requestLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
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

// TestPull_SameRepoRespellingUpdatesExistingBead: a bead linked to B's issue
// through another spelling RefScope accepts still matches the canonical ref
// on pull and is canonicalized, while A's same-numbered bead is ignored. A
// sorts after B's bead, so an index keyed by bare number would pick A. The
// canonical-ref case is the exact-match control: a fix that disables all
// matching fails it.
func TestPull_SameRepoRespellingUpdatesExistingBead(t *testing.T) {
	for _, mode := range []struct {
		name string
		opts tracker.SyncOptions
	}{
		{"bulk", tracker.SyncOptions{Pull: true}},
		{"selective number", tracker.SyncOptions{Pull: true, IssueIDs: []string{"1313"}}},
	} {
		for _, tc := range []struct {
			name string
			ref  string
		}{
			{"canonical exact ref", crossRepoBRef},
			{"owner and repo case", "https://github.com/OWNER-B/Repo-B/issues/1313"},
			{"REST URL", "https://api.github.com/repos/owner-b/repo-b/issues/1313"},
			{"shorthand", "github:1313"},
		} {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				stub, tr := newGitHubAPIStub(t)
				stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
				stub.list = []int{1313}
				ref := tc.ref
				const bID = "bd-1313b"
				store := newCrossRepoStore(repoABead(time.Time{}), &types.Issue{
					ID:          bID,
					Title:       "stale B title",
					Status:      types.StatusOpen,
					IssueType:   types.TypeTask,
					ExternalRef: &ref,
				})

				result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(), mode.opts)
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}

				store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
				if result.PullStats.Created != 0 || result.PullStats.Updated != 1 {
					t.Errorf("created=%d updated=%d, want created=0 updated=1",
						result.PullStats.Created, result.PullStats.Updated)
				}
				b := store.issues[bID]
				if b.Title != "Repo B issue 1313" || b.ExternalRef == nil || *b.ExternalRef != crossRepoBRef {
					t.Errorf("B bead not updated and canonicalized: title=%q external_ref=%v", b.Title, derefRef(b.ExternalRef))
				}
				if len(store.issues) != 2 {
					t.Errorf("store holds %d beads, want 2", len(store.issues))
				}
			})
		}
	}
}

// TestPull_SelectiveBeadIDLinkedToOtherRepoIsSkipped: selecting A's bead by
// ID gives B's tracker no identifier to fetch, so the pull skips it instead
// of fetching B's 1313 into it, and says the bead's link is not one the
// tracker can address rather than that it has none. An unlinked bead is the
// control: it still gets the no-link warning.
func TestPull_SelectiveBeadIDLinkedToOtherRepoIsSkipped(t *testing.T) {
	stub, tr := newGitHubAPIStub(t)
	stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
	store := newCrossRepoStore(repoABead(time.Time{}), &types.Issue{
		ID: "bd-unlinked", Title: "Unlinked", Status: types.StatusOpen, IssueType: types.TypeTask,
	})

	result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(),
		tracker.SyncOptions{Pull: true, IssueIDs: []string{crossRepoAID, "bd-unlinked"}})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
	if got := stub.requestLog(); len(got) != 0 {
		t.Errorf("selective pull of %s and bd-unlinked issued requests %q, want none", crossRepoAID, got)
	}
	if result.PullStats.Skipped != 2 || result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
		t.Errorf("skipped=%d created=%d updated=%d, want skipped=2 created=0 updated=0",
			result.PullStats.Skipped, result.PullStats.Created, result.PullStats.Updated)
	}
	want := []string{
		`External ref "` + crossRepoARef + `" of local issue ` + crossRepoAID +
			` is not an issue GitHub can address with its current configuration (another repository?), skipping pull`,
		"No external ref found for local issue bd-unlinked, skipping pull",
	}
	if !slices.Equal(result.Warnings, want) {
		t.Errorf("warnings = %q, want %q", result.Warnings, want)
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

// TestPush_OtherRepoBeadLeavesConfiguredRepoIssueAlone: pushing to B must
// neither fetch nor update B's issue 1313 on behalf of A's locally edited
// bead, in a real run or a preview, nor relink the bead. Own-repository
// updates and unlinked creates still go through.
func TestPush_OtherRepoBeadLeavesConfiguredRepoIssueAlone(t *testing.T) {
	ownRef := "https://github.com/owner-b/repo-b/issues/7"
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("isolated/dry-run=%v", dryRun), func(t *testing.T) {
			stub, tr := newGitHubAPIStub(t)
			stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
			store := newCrossRepoStore(repoABead(crossRepoLocal))

			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(),
				tracker.SyncOptions{Push: true, DryRun: dryRun})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}

			store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
			if got := stub.requestLog(); len(got) != 0 {
				t.Errorf("push issued requests %q, want none", got)
			}
			if got := result.PushStats; got.Created != 0 || got.Updated != 0 || got.Skipped != 1 {
				t.Errorf("push stats created=%d updated=%d skipped=%d, want 0, 0, 1",
					got.Created, got.Updated, got.Skipped)
			}
		})

		t.Run(fmt.Sprintf("mixed/dry-run=%v", dryRun), func(t *testing.T) {
			stub, tr := newGitHubAPIStub(t)
			stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
			stub.issues[7] = gitHubIssueJSON(7, "Own issue 7", crossRepoRemote)
			store := newCrossRepoStore(
				repoABead(crossRepoLocal),
				&types.Issue{ID: "bd-own7", Title: "Own issue 7 edited", Status: types.StatusOpen,
					IssueType: types.TypeTask, ExternalRef: &ownRef, UpdatedAt: crossRepoLocal},
				&types.Issue{ID: "bd-unlinked", Title: "Unlinked", Status: types.StatusOpen,
					IssueType: types.TypeTask, UpdatedAt: crossRepoLocal},
			)

			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(),
				tracker.SyncOptions{Push: true, DryRun: dryRun})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}

			store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
			if got := stub.requests(1313); len(got) != 0 {
				t.Errorf("push sent requests for B's issue 1313 on behalf of %s: %v", crossRepoAID, got)
			}
			if got := result.PushStats; got.Created != 1 || got.Updated != 1 || got.Skipped != 1 {
				t.Errorf("push stats created=%d updated=%d skipped=%d, want created=1 (unlinked) updated=1 (own) skipped=1 (repo A)",
					got.Created, got.Updated, got.Skipped)
			}
			var want []string
			if !dryRun {
				want = []string{
					"GET /repos/owner-b/repo-b/issues/7",
					"PATCH /repos/owner-b/repo-b/issues/7",
					"POST /repos/owner-b/repo-b/issues",
				}
				if linked := store.issues["bd-unlinked"].ExternalRef; linked == nil ||
					*linked != "https://github.com/owner-b/repo-b/issues/8" {
					t.Errorf("unlinked bead external_ref = %s, want B's created issue 8", derefRef(linked))
				}
			}
			if got := stub.requestLog(); !slices.Equal(got, want) {
				t.Errorf("push issued requests %q, want %q", got, want)
			}
		})

		// A conflict resolved as "keep local" forces a push. A's bead must not
		// become a conflict against B's issue, so nothing is forced onto it.
		t.Run(fmt.Sprintf("forced/dry-run=%v", dryRun), func(t *testing.T) {
			stub, tr := newGitHubAPIStub(t)
			stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
			store := newCrossRepoStore(repoABead(crossRepoLocal))
			store.meta["github.last_sync"] = crossRepoSync.Format(time.RFC3339)

			result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(),
				tracker.SyncOptions{Pull: true, Push: true, DryRun: dryRun, ConflictResolution: tracker.ConflictLocal})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}

			store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
			if got := stub.requests(1313); len(got) != 0 {
				t.Errorf("sync sent requests for B's issue 1313 on behalf of %s: %v", crossRepoAID, got)
			}
			if got := result.PushStats; got.Created != 0 || got.Updated != 0 || got.Skipped != 1 {
				t.Errorf("push stats created=%d updated=%d skipped=%d, want 0, 0, 1",
					got.Created, got.Updated, got.Skipped)
			}
		})
	}

	// B's own issue 99, edited on both sides since the last sync, is a real
	// conflict that "keep local" resolves by forcing its update: the forced
	// push still skips A's bead and never addresses issue 1313.
	t.Run("forced own-repo conflict", func(t *testing.T) {
		stub, tr := newGitHubAPIStub(t)
		stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
		stub.issues[99] = gitHubIssueJSON(99, "Repo B issue 99 (remote)", crossRepoRemote)
		stub.list = []int{99}
		ref99 := "https://github.com/owner-b/repo-b/issues/99"
		store := newCrossRepoStore(
			repoABead(crossRepoLocal),
			&types.Issue{ID: "bd-b99", Title: "Local edit on B's bead", Status: types.StatusOpen,
				IssueType: types.TypeTask, ExternalRef: &ref99, UpdatedAt: crossRepoLocal},
		)
		store.meta["github.last_sync"] = crossRepoSync.Format(time.RFC3339)

		result, err := tracker.NewEngine(tr, store, "test").Sync(context.Background(),
			tracker.SyncOptions{Pull: true, Push: true, ConflictResolution: tracker.ConflictLocal})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}

		store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
		if result.Stats.Conflicts != 1 {
			t.Errorf("conflicts=%d, want 1 (B's issue 99 only)", result.Stats.Conflicts)
		}
		if got := result.PushStats; got.Created != 0 || got.Updated != 1 || got.Skipped != 1 {
			t.Errorf("forced push stats created=%d updated=%d skipped=%d, want created=0 updated=1 (issue 99) skipped=1 (repo A)",
				got.Created, got.Updated, got.Skipped)
		}
		if got := stub.requests(1313); len(got) != 0 {
			t.Errorf("forced push sent requests for B's issue 1313 on behalf of %s: %v", crossRepoAID, got)
		}
		if !slices.Contains(stub.requestLog(), "PATCH /repos/owner-b/repo-b/issues/99") {
			t.Errorf("forced push did not update B's issue 99: requests %q", stub.requestLog())
		}
	})
}

// TestDetectConflicts_OtherRepoBeadNotFetched: A's bead edited since the last
// sync must not be compared against B's same-numbered issue, which was also
// edited since then.
func TestDetectConflicts_OtherRepoBeadNotFetched(t *testing.T) {
	stub, tr := newGitHubAPIStub(t)
	stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
	store := newCrossRepoStore(repoABead(crossRepoLocal))
	store.meta["github.last_sync"] = crossRepoSync.Format(time.RFC3339)

	conflicts, err := tracker.NewEngine(tr, store, "test").DetectConflicts(context.Background())
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if got := stub.requestLog(); len(got) != 0 {
		t.Errorf("conflict detection for %s issued requests %q, want none", crossRepoAID, got)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %+v, want none", conflicts)
	}
	store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
}

// TestRenamedRepository_PulledBeadStillPushesAndConflicts: a config that still
// names a renamed or transferred repository by its old name keeps syncing,
// because GitHub serves that name by redirect, but every issue it returns
// names the current repository, and the pull links beads to those URLs. The
// name Init resolves makes those refs addressable, so a pulled bead edited
// locally is still pushed (and previewed as an update) and still reaches
// conflict detection, rather than being skipped until a remote edit
// overwrites the local one.
func TestRenamedRepository_PulledBeadStillPushesAndConflicts(t *testing.T) {
	ctx := context.Background()
	pullAndEdit := func(t *testing.T) (*githubAPIStub, *Tracker, *crossRepoStore, *types.Issue) {
		t.Helper()
		stub, tr := newRenamedRepoStub(t)
		stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
		stub.list = []int{1313}
		if err := tr.resolveRepositoryNames(ctx); err != nil { // as Init does
			t.Fatalf("resolveRepositoryNames: %v", err)
		}
		if tr.currentName != "owner-b/repo-b" {
			t.Fatalf("resolved current name %q, want owner-b/repo-b (requests %q)", tr.currentName, stub.requestLog())
		}
		store := newCrossRepoStore()
		if _, err := tracker.NewEngine(tr, store, "test").Sync(ctx, tracker.SyncOptions{Pull: true}); err != nil {
			t.Fatalf("pull: %v", err)
		}
		bead, err := store.GetIssueByExternalRef(ctx, crossRepoBRef)
		if err != nil {
			t.Fatalf("pull linked no bead to %s: %v", crossRepoBRef, err)
		}
		bead.Title = "Local edit"
		bead.UpdatedAt = crossRepoLocal
		return stub, tr, store, bead
	}

	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("push/dry-run=%v", dryRun), func(t *testing.T) {
			stub, tr, store, _ := pullAndEdit(t)

			result, err := tracker.NewEngine(tr, store, "test").Sync(ctx, tracker.SyncOptions{Push: true, DryRun: dryRun})
			if err != nil {
				t.Fatalf("push: %v", err)
			}
			if got := result.PushStats; got.Updated != 1 || got.Skipped != 0 {
				t.Errorf("push stats updated=%d skipped=%d, want updated=1 skipped=0", got.Updated, got.Skipped)
			}
			var want []string
			if !dryRun {
				want = []string{
					"GET /repos/old-owner/repo-b/issues/1313",
					"GET /repositories/4242/issues/1313",
					"PATCH /repos/old-owner/repo-b/issues/1313",
					"PATCH /repositories/4242/issues/1313",
				}
			}
			if got := stub.requests(1313); !slices.Equal(got, want) {
				t.Errorf("push requests for issue 1313 = %q, want %q", got, want)
			}
		})
	}

	// Both sides edited since the last sync: conflict detection compares the
	// bead with its issue, keeps the newer local edit and pushes it.
	t.Run("conflict", func(t *testing.T) {
		stub, tr, store, bead := pullAndEdit(t)
		store.meta["github.last_sync"] = crossRepoSync.Format(time.RFC3339)

		result, err := tracker.NewEngine(tr, store, "test").Sync(ctx,
			tracker.SyncOptions{Pull: true, Push: true, ConflictResolution: tracker.ConflictTimestamp})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.Stats.Conflicts != 1 {
			t.Errorf("conflicts=%d, want 1", result.Stats.Conflicts)
		}
		if got := result.PushStats; got.Updated != 1 {
			t.Errorf("push stats updated=%d, want 1 (the local edit)", got.Updated)
		}
		if !slices.Contains(stub.requests(1313), "PATCH /repositories/4242/issues/1313") {
			t.Errorf("sync did not push the local edit: requests %q", stub.requests(1313))
		}
		if bead.Title != "Local edit" {
			t.Errorf("bead title = %q, want the local edit kept", bead.Title)
		}
	})
}

// TestRenamedRepository_ConfigMovedToCurrentName: the reverse of the case
// above. The config names the repository's current name, as it does once the
// user updates github.owner/github.repo after a rename or transfer, or when
// GITHUB_REPOSITORY follows the repository, but beads pulled before the
// rename are still linked under the old name. GitHub still resolves that name
// to the repository, so those beads keep syncing: a pull updates and relinks
// the bead instead of importing its issue again as a second bead, a local
// edit is pushed (and previewed as an update) instead of being skipped
// without a warning, and conflict detection still compares the bead with its
// issue. Repository A, which GitHub serves as another repository, stays
// foreign: its same-numbered bead is never read, fetched, updated or relinked.
func TestRenamedRepository_ConfigMovedToCurrentName(t *testing.T) {
	ctx := context.Background()
	const oldRef = "https://github.com/old-owner/repo-b/issues/1313"
	const oldID = "bd-old1313"
	setup := func(t *testing.T, edited bool) (*githubAPIStub, *Tracker, *crossRepoStore) {
		t.Helper()
		stub, tr := newGitHubAPIStub(t)
		stub.renamedFrom = "/repos/old-owner/repo-b" // GitHub redirects the old name
		stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
		stub.list = []int{1313}
		ref := oldRef
		bead := &types.Issue{ID: oldID, Title: "stale B title", Status: types.StatusOpen,
			IssueType: types.TypeTask, ExternalRef: &ref}
		var aUpdated time.Time
		if edited {
			bead.Title = "Local edit"
			bead.UpdatedAt = crossRepoLocal
			aUpdated = crossRepoLocal
		}
		store := newCrossRepoStore(repoABead(aUpdated), bead)
		tr.store = store
		if err := tr.resolveRepositoryNames(ctx); err != nil { // as Init does
			t.Fatalf("resolveRepositoryNames: %v", err)
		}
		want := []string{
			"GET /repos/owner-b/repo-b",
			"GET /repos/old-owner/repo-b",
			"GET /repositories/4242",
			"GET /repos/owner-a/repo-a",
		}
		if got := stub.requestLog(); !slices.Equal(got, want) {
			t.Fatalf("name resolution requests = %q, want %q", got, want)
		}
		if !slices.Equal(tr.formerNames, []string{"old-owner/repo-b"}) {
			t.Fatalf("former names = %q, want [old-owner/repo-b]", tr.formerNames)
		}
		stub.mu.Lock()
		stub.calls = nil
		stub.mu.Unlock()
		return stub, tr, store
	}

	t.Run("pull", func(t *testing.T) {
		_, tr, store := setup(t, false)

		result, err := tracker.NewEngine(tr, store, "test").Sync(ctx, tracker.SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		if got := result.PullStats; got.Created != 0 || got.Updated != 1 {
			t.Errorf("pull stats created=%d updated=%d, want created=0 updated=1", got.Created, got.Updated)
		}
		if len(store.issues) != 2 {
			t.Errorf("store holds %d beads, want 2 (no duplicate)", len(store.issues))
		}
		b := store.issues[oldID]
		if b.Title != "Repo B issue 1313" || b.ExternalRef == nil || *b.ExternalRef != crossRepoBRef {
			t.Errorf("old-name bead not updated and relinked: title=%q external_ref=%v", b.Title, derefRef(b.ExternalRef))
		}
		store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
	})

	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("push/dry-run=%v", dryRun), func(t *testing.T) {
			stub, tr, store := setup(t, true)

			result, err := tracker.NewEngine(tr, store, "test").Sync(ctx, tracker.SyncOptions{Push: true, DryRun: dryRun})
			if err != nil {
				t.Fatalf("push: %v", err)
			}
			if got := result.PushStats; got.Created != 0 || got.Updated != 1 || got.Skipped != 1 {
				t.Errorf("push stats created=%d updated=%d skipped=%d, want created=0 updated=1 (old-name bead) skipped=1 (repo A)",
					got.Created, got.Updated, got.Skipped)
			}
			var want []string
			if !dryRun {
				want = []string{
					"GET /repos/owner-b/repo-b/issues/1313",
					"PATCH /repos/owner-b/repo-b/issues/1313",
				}
			}
			if got := stub.requestLog(); !slices.Equal(got, want) {
				t.Errorf("push requests = %q, want %q", got, want)
			}
			store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
		})
	}

	t.Run("conflict", func(t *testing.T) {
		stub, tr, store := setup(t, true)
		store.meta["github.last_sync"] = crossRepoSync.Format(time.RFC3339)

		conflicts, err := tracker.NewEngine(tr, store, "test").DetectConflicts(ctx)
		if err != nil {
			t.Fatalf("DetectConflicts: %v", err)
		}
		if len(conflicts) != 1 || conflicts[0].IssueID != oldID {
			t.Errorf("conflicts = %+v, want one, for %s", conflicts, oldID)
		}
		if got, want := stub.requestLog(), []string{"GET /repos/owner-b/repo-b/issues/1313"}; !slices.Equal(got, want) {
			t.Errorf("conflict detection requests = %q, want %q", got, want)
		}
		store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
	})
}

// TestInit_ResolvesRenamedRepositoryName: Init asks GitHub for the configured
// repository. When the configured name redirects, as a renamed or transferred
// repository's does, refs under the current name address the repository too.
// When GitHub answers 404 for it, Init still succeeds and only the configured
// name does, as before. Any other failure fails Init, for the configured name
// and for another name local refs use alike (a 403 here, a 5xx or a dropped
// connection in TestResolveRepositoryNames_FailedLookups), rather than
// narrowing the scope.
func TestInit_ResolvesRenamedRepositoryName(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v3/repos/old-owner/repo-b":
			http.Redirect(w, r, "/api/v3/repositories/4242", http.StatusMovedPermanently)
		case "/api/v3/repositories/4242", "/api/v3/repos/owner-b/repo-b":
			_, _ = w.Write([]byte(`{"id":4242,"name":"repo-b","full_name":"owner-b/repo-b"}`))
		case "/api/v3/repos/owner-a/repo-a":
			_, _ = w.Write([]byte(`{"id":1111,"name":"repo-a","full_name":"owner-a/repo-a"}`))
		case "/api/v3/repos/locked-owner/repo-b":
			// An organization that refuses the token, as SAML enforcement
			// does: a 403 that is no rate limit, which the client does not
			// retry.
			http.Error(w, `{"message":"Resource protected by organization SAML enforcement."}`, http.StatusForbidden)
		default:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	// A GitHub Enterprise host serves issue pages and the API at /api/v3.
	web := srv.URL

	for _, tc := range []struct {
		name      string
		owner     string
		refs      []string // external_refs of the local beads
		wantCalls []string
		wantErr   string            // part of the error Init returns
		want      map[string]string // ref -> ExtractIdentifier
	}{
		{
			// The config names the current name; local refs name the old one
			// (in two spellings and the REST form), repository A, a
			// repository GitHub does not know, a malformed name, the
			// configured repository, another host and the shorthand. Each
			// other repository on the configured host is read once, in name
			// order, and only the one GitHub resolves to the configured
			// repository's ID is accepted.
			name:  "former name in local refs",
			owner: "owner-b",
			refs: []string{
				web + "/owner-a/repo-a/issues/5",
				web + "/old-owner/repo-b/issues/5",
				web + "/OLD-OWNER/Repo-B/issues/6",
				web + "/api/v3/repos/old-owner/repo-b/issues/7",
				web + "/gone/repo-x/issues/5",
				web + "/bad~owner/repo-b/issues/5",
				web + "/owner-b/repo-b/issues/9",
				"https://github.com/elsewhere/repo-b/issues/5",
				"github:5",
			},
			wantCalls: []string{
				"GET /api/v3/repos/owner-b/repo-b",
				"GET /api/v3/repos/gone/repo-x",
				"GET /api/v3/repos/old-owner/repo-b",
				"GET /api/v3/repositories/4242",
				"GET /api/v3/repos/owner-a/repo-a",
			},
			want: map[string]string{
				web + "/owner-b/repo-b/issues/5":                "5",
				web + "/old-owner/repo-b/issues/5":              "5",
				web + "/Old-Owner/repo-b/issues/6":              "6",
				web + "/api/v3/repos/old-owner/repo-b/issues/7": "7",
				web + "/owner-a/repo-a/issues/5":                "",
				web + "/gone/repo-x/issues/5":                   "",
				web + "/bad~owner/repo-b/issues/5":              "",
			},
		},
		{
			name:      "renamed",
			owner:     "old-owner",
			wantCalls: []string{"GET /api/v3/repos/old-owner/repo-b", "GET /api/v3/repositories/4242"},
			want: map[string]string{
				web + "/old-owner/repo-b/issues/5": "5",
				web + "/owner-b/repo-b/issues/5":   "5",
				web + "/owner-a/repo-a/issues/5":   "",
			},
		},
		{
			name:      "configured name not found",
			owner:     "gone-owner",
			wantCalls: []string{"GET /api/v3/repos/gone-owner/repo-b"},
			want: map[string]string{
				web + "/gone-owner/repo-b/issues/5": "5",
				web + "/owner-b/repo-b/issues/5":    "",
			},
		},
		{
			// GitHub refuses the configured name, which answers nothing about
			// the repository's names, so Init fails rather than going on
			// without them.
			name:      "configured lookup refused",
			owner:     "locked-owner",
			wantCalls: []string{"GET /api/v3/repos/locked-owner/repo-b"},
			wantErr:   "looking up repository locked-owner/repo-b on GitHub",
		},
		{
			// GitHub refuses another name local refs use. It may be a former
			// name of the configured repository, so Init fails at once,
			// before it reads repository A's name, which sorts after it.
			name:  "other-name lookup refused",
			owner: "owner-b",
			refs: []string{
				web + "/locked-owner/repo-b/issues/5",
				web + "/owner-a/repo-a/issues/5",
			},
			wantCalls: []string{
				"GET /api/v3/repos/owner-b/repo-b",
				"GET /api/v3/repos/locked-owner/repo-b",
			},
			wantErr: "looking up repository locked-owner/repo-b on GitHub",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The store-config github.url is shared configuration: the
			// endpoint trust gate needs a workspace to key its pin on and a
			// private confirmation of the endpoint before the token goes out.
			setupEndpointTrustWorkspace(t)
			t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")
			mu.Lock()
			calls = nil
			mu.Unlock()
			store := newCrossRepoStore()
			for i, ref := range tc.refs {
				store.issues[fmt.Sprintf("bd-%d", i)] = &types.Issue{ID: fmt.Sprintf("bd-%d", i), ExternalRef: &ref}
			}
			store.config["github.owner"] = tc.owner
			store.config["github.repo"] = "repo-b"
			store.config["github.url"] = srv.URL + "/api/v3"

			tr := &Tracker{}
			err := tr.Init(context.Background(), store)
			mu.Lock()
			gotCalls := append([]string(nil), calls...)
			mu.Unlock()
			if !slices.Equal(gotCalls, tc.wantCalls) {
				t.Errorf("Init requests = %q, want %q", gotCalls, tc.wantCalls)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("Init() = %v, want an error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			for ref, want := range tc.want {
				if got := tr.ExtractIdentifier(ref); got != want {
					t.Errorf("ExtractIdentifier(%q) = %q, want %q", ref, got, want)
				}
				if n, ok := tr.LinkResolver().Scope().IssueNumberFromRef(ref); (want != "") != ok || (ok && fmt.Sprint(n) != want) {
					t.Errorf("LinkResolver scope resolves %q to %d, %v; want %q", ref, n, ok, want)
				}
			}
		})
	}
}

// failedLookupOldRef and failedLookupOldID are the bead of
// newFailedLookupFixture that is linked under the repository's old name.
const (
	failedLookupOldRef = "https://github.com/old-owner/repo-b/issues/1313"
	failedLookupOldID  = "bd-old1313"
)

// newFailedLookupFixture sets up the case of
// TestRenamedRepository_ConfigMovedToCurrentName with a bead edited locally
// since github.last_sync: the config names the repository's current name,
// owner-b/repo-b, GitHub redirects its old name, old-owner/repo-b, and the
// bead bd-old1313 ("Local edit") is linked under the old name, next to
// repository A's bead. The stub fails the requests fail names (see
// githubAPIStub.fail), each on its one attempt.
func newFailedLookupFixture(t *testing.T, fail map[string]int) (*githubAPIStub, *Tracker, *crossRepoStore) {
	t.Helper()
	stub, tr := newGitHubAPIStub(t)
	// One attempt, and a millisecond's backoff after a 5xx.
	tr.client.Retry = RetryConfig{BaseDelay: time.Millisecond}
	stub.renamedFrom = "/repos/old-owner/repo-b" // GitHub redirects the old name
	stub.issues[1313] = gitHubIssueJSON(1313, "Repo B issue 1313", crossRepoRemote)
	stub.list = []int{1313}
	stub.fail = fail
	ref := failedLookupOldRef
	store := newCrossRepoStore(repoABead(crossRepoLocal), &types.Issue{ID: failedLookupOldID, Title: "Local edit",
		Status: types.StatusOpen, IssueType: types.TypeTask, ExternalRef: &ref, UpdatedAt: crossRepoLocal})
	store.meta["github.last_sync"] = crossRepoSync.Format(time.RFC3339)
	tr.store = store
	return stub, tr, store
}

// syncLikeCLI runs what one bd github sync, push or pull runs: Init's
// repository-name resolution, then, only if that succeeds, the sync.
func syncLikeCLI(ctx context.Context, tr *Tracker, store *crossRepoStore, opts tracker.SyncOptions) (*tracker.SyncResult, error) {
	tr.store = store
	if err := tr.resolveRepositoryNames(ctx); err != nil {
		return nil, err
	}
	return tracker.NewEngine(tr, store, "test").Sync(ctx, opts)
}

// TestResolveRepositoryNames_FailedLookups: only a 404 or an answer with
// another repository's ID settles a name. A 5xx or a dropped connection
// answers nothing, so resolveRepositoryNames, and so Init, fails at the first
// lookup that fails instead of narrowing the scope: the configured
// repository's, or that of another name local refs use, which may be a former
// name of the repository (TestFailedNameLookup_NextSyncStillReportsTheConflict
// shows what going on without it would cost). A failed scan of the local
// beads fails it too.
func TestResolveRepositoryNames_FailedLookups(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []struct {
		name   string
		status int
	}{
		{"502", http.StatusBadGateway},
		{"503", http.StatusServiceUnavailable},
		{"dropped connection", dropConnection},
	} {
		for _, lookup := range []struct {
			name, path, wantErr string
			wantCalls           []string
		}{
			{"configured repository", "/repos/owner-b/repo-b",
				"looking up repository owner-b/repo-b on GitHub: ",
				[]string{"GET /repos/owner-b/repo-b"}},
			// The old name sorts before repository A's, which is never read.
			{"other name", "/repos/old-owner/repo-b",
				"looking up repository old-owner/repo-b on GitHub, which local beads link to: ",
				[]string{"GET /repos/owner-b/repo-b", "GET /repos/old-owner/repo-b"}},
		} {
			t.Run(lookup.name+"/"+failure.name, func(t *testing.T) {
				stub, tr, _ := newFailedLookupFixture(t, map[string]int{lookup.path: failure.status})

				err := tr.resolveRepositoryNames(ctx)
				if err == nil || !strings.Contains(err.Error(), lookup.wantErr) {
					t.Fatalf("resolveRepositoryNames() = %v, want an error containing %q", err, lookup.wantErr)
				}
				if got := stub.requestLog(); !slices.Equal(got, lookup.wantCalls) {
					t.Errorf("requests = %q, want %q", got, lookup.wantCalls)
				}
				if len(tr.formerNames) != 0 {
					t.Errorf("former names = %q, want none", tr.formerNames)
				}
			})
		}
	}

	// The error for another name gives each remedy with the case it fits:
	// run the command again after a temporary failure, give the token read
	// access when the name is another repository that refuses it, and relink
	// the beads when it is a former name of the repository. Relinking a bead
	// of another repository would point it at the configured repository's
	// issue with the same number (#7781), and rerunning does not clear a
	// refusal, so neither may be advised unconditionally.
	t.Run("other name/advice", func(t *testing.T) {
		_, tr, _ := newFailedLookupFixture(t, map[string]int{"/repos/old-owner/repo-b": http.StatusBadGateway})

		err := tr.resolveRepositoryNames(ctx)
		if err == nil {
			t.Fatal("resolveRepositoryNames() = nil, want an error")
		}
		for _, want := range []string{
			"cannot tell whether those beads belong to owner-b/repo-b",
			"If the failure is temporary, run the command again.",
			"If old-owner/repo-b is another repository and GitHub refuses this token for it (a 403), give the token read access to it.",
			"If it is a former name of owner-b/repo-b, relinking those beads to their issues' current URLs (bd update <id> --external-ref <url>)",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not say %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "owner-a") {
			t.Errorf("error %q names owner-a/repo-a, which it never looked up", err)
		}
	})

	t.Run("local scan", func(t *testing.T) {
		stub, tr, store := newFailedLookupFixture(t, nil)
		store.searchErr = errors.New("database is closed")

		err := tr.resolveRepositoryNames(ctx)
		if !errors.Is(err, store.searchErr) || !strings.Contains(err.Error(), "listing local beads") {
			t.Fatalf("resolveRepositoryNames() = %v, want the scan's error", err)
		}
		if got, want := stub.requestLog(), []string{"GET /repos/owner-b/repo-b"}; !slices.Equal(got, want) {
			t.Errorf("requests = %q, want only %q", got, want)
		}
	})
}

// TestFailedNameLookup_NextSyncStillReportsTheConflict: a command whose
// lookup of the old name fails stops before it syncs, so it leaves
// github.last_sync and every bead as they were, and the next command, once
// GitHub answers, still compares the edited bead with its issue: it reports
// the conflict and resolves it by the strategy, as a sync without the failed
// command does. Had the failed command synced without the old name, it would
// have skipped the bead while moving the cursor past its local edit (and,
// when GitHub's issue was edited first, imported that edit as a duplicate
// bead); the next sync would then find no conflict, and pull the remote edit
// over the local one or push the stale bead over the remote edit.
func TestFailedNameLookup_NextSyncStillReportsTheConflict(t *testing.T) {
	ctx := context.Background()
	for _, strategy := range []struct {
		name      string
		resolve   tracker.ConflictResolution
		wantTitle string // the edited bead's title after the second command
		wantPatch bool   // whether the second command pushes the local edit
	}{
		// The remote edit is the newer one.
		{"timestamp", tracker.ConflictTimestamp, "Remote edit", false},
		{"local", tracker.ConflictLocal, "Local edit", true},
	} {
		for _, first := range []struct {
			name string
			opts tracker.SyncOptions
			// editedFirst: GitHub's issue is edited before the failed
			// command, not after it.
			editedFirst bool
		}{
			{"push, then a remote edit", tracker.SyncOptions{Push: true}, false},
			{"a remote edit, then sync", tracker.SyncOptions{Pull: true, Push: true, ConflictResolution: strategy.resolve}, true},
		} {
			t.Run(strategy.name+"/"+first.name, func(t *testing.T) {
				stub, tr, store := newFailedLookupFixture(t, map[string]int{"/repos/old-owner/repo-b": http.StatusBadGateway})
				editRemote := func(at time.Time) {
					stub.mu.Lock()
					defer stub.mu.Unlock()
					stub.issues[1313] = gitHubIssueJSON(1313, "Remote edit", at)
				}
				if first.editedFirst {
					editRemote(crossRepoLocal.Add(24 * time.Hour))
				}

				if _, err := syncLikeCLI(ctx, tr, store, first.opts); err == nil {
					t.Errorf("first command: no error, want the failed lookup of old-owner/repo-b")
				}
				if got, want := store.meta["github.last_sync"], crossRepoSync.Format(time.RFC3339); got != want {
					t.Errorf("first command moved github.last_sync to %s, want it left at %s", got, want)
				}
				if len(store.issues) != 2 {
					t.Errorf("first command left %d beads, want 2 (no duplicate)", len(store.issues))
				}
				store.assertUntouched(t, failedLookupOldID, "Local edit", failedLookupOldRef)
				if !first.editedFirst {
					// Edited after any cursor the first command could have set.
					editRemote(time.Now().UTC().Add(time.Hour))
				}

				stub.mu.Lock()
				stub.fail = nil
				stub.calls = nil
				stub.mu.Unlock()
				result, err := syncLikeCLI(ctx, &Tracker{client: tr.client, config: DefaultMappingConfig()}, store,
					tracker.SyncOptions{Pull: true, Push: true, ConflictResolution: strategy.resolve})
				if err != nil {
					t.Fatalf("second command: %v", err)
				}
				if result.Stats.Conflicts != 1 {
					t.Errorf("second command: conflicts = %d, want 1", result.Stats.Conflicts)
				}
				if got := store.issues[failedLookupOldID].Title; got != strategy.wantTitle {
					t.Errorf("bead title = %q after the second command, want %q", got, strategy.wantTitle)
				}
				patched := slices.Contains(stub.requests(1313), "PATCH /repos/owner-b/repo-b/issues/1313")
				if patched != strategy.wantPatch {
					t.Errorf("second command pushed the local edit: %v, want %v (requests %q)", patched, strategy.wantPatch, stub.requests(1313))
				}
				if len(store.issues) != 2 {
					t.Errorf("store holds %d beads, want 2 (no duplicate)", len(store.issues))
				}
				store.assertUntouched(t, crossRepoAID, crossRepoAT, crossRepoARef)
			})
		}
	}
}

// setupEndpointTrustWorkspace gives the endpoint trust gate a workspace to
// key its pin on and an isolated HOME/XDG config directory to write it into.
func setupEndpointTrustWorkspace(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("BEADS_DIR", beadsDir)
}
