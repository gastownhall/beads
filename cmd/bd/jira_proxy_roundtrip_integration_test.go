//go:build cgo && unix

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/jira"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// TestJiraPullPushSQLServerFrontdoor exercises the public Jira commands over
// a child process and a dedicated direct SQL database.
func TestJiraPullPushSQLServerFrontdoor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt SQL test server not available")
	}
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "issues.db")
	raw := newTestStoreIsolatedDB(t, dbPath, "jirapullpush")
	if err := os.WriteFile(filepath.Join(dir, ".beads", "config.yaml"), []byte("# Beads Config\n"), 0o600); err != nil {
		t.Fatalf("create direct SQL config.yaml: %v", err)
	}
	result := runJiraPullPushFixture(t, bd, dir, append(bdEnv(dir), "BEADS_DIR="+filepath.Join(dir, ".beads"), "BEADS_TEST_SERVER=1", "JIRA_API_TOKEN=test-api-token"), tracker.NewStore(raw), func(id string) { jiraSetIssueUpdatedAt(t, raw.DB(), id) })
	assertJiraPullPushSemantics(t, result)
}

// TestManagedLocalProxiedJiraPullPushParity runs the identical fixture through
// the managed proxy UOW store, keeping the tracker engine and REST double
// shared with the direct front door.
func TestManagedLocalProxiedJiraPullPushParity(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildBDForInitTests(t)
	project := bdManagedLocalInit(t, bd, "jirapullpush", 5*time.Minute)
	provider, err := newProxiedServerUOWProvider(t.Context(), project.beadsDir, "")
	if err != nil {
		t.Fatalf("open managed-local proxy UOW provider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close(t.Context()) })
	proxyDB := openProxiedDB(t, project)
	result := runJiraPullPushFixture(t, bd, project.dir, append(bdProxiedEnv(project.dir), "JIRA_API_TOKEN=test-api-token"), tracker.NewUOWStore(provider), func(id string) { jiraSetIssueUpdatedAt(t, proxyDB, id) })
	assertJiraPullPushSemantics(t, result)
}

type jiraPullPushResult struct {
	issues             []*types.Issue
	linkedRef          string
	parentRef          string
	blockerRef         string
	dependencies       string
	remoteUpdatedTitle string
	remoteIssueCount   int
}

func runJiraPullPushFixture(t *testing.T, bd, dir string, env []string, store tracker.Store, setUpdatedAt func(string)) jiraPullPushResult {
	t.Helper()
	mock := newMockJiraServer()
	server := httptest.NewServer(mock)
	t.Cleanup(server.Close)
	mock.seedGraph(server.URL, time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir, cmd.Env = dir, append([]string(nil), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	runJSON := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir, cmd.Env = dir, append([]string(nil), env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	for _, entry := range [][2]string{{"jira.url", server.URL}, {"jira.project", "GC"}} {
		run("config", "set", entry[0], entry[1])
	}
	previousSync := "2020-01-01T00:00:00Z"
	pull := parseJiraSyncResult(t, runJSON("--json", "jira", "sync", "--pull", "--relations"))
	if pull.Stats.Pulled != 3 || pull.LastSync == "" {
		t.Fatalf("pull result = %+v", pull)
	}
	persisted, err := store.GetLocalMetadata(t.Context(), "jira.last_sync")
	if err != nil || persisted != pull.LastSync {
		t.Fatalf("persisted Jira cursor = %q, %v; want %q", persisted, err, pull.LastSync)
	}
	issues := jiraListIssues(t, runJSON("list", "--all", "--json"))
	linked := jiraFindIssue(t, issues, server.URL+"/browse/GC-2")
	blocker := jiraFindIssue(t, issues, server.URL+"/browse/GC-3")
	blockerReady := false
	for _, ready := range jiraListIssues(t, runJSON("ready", "--json")) {
		if ready.ID == linked.ID {
			t.Fatalf("linked child %s is ready despite Jira blocking link", linked.ID)
		}
		if ready.ID == blocker.ID {
			blockerReady = true
		}
	}
	if !blockerReady {
		t.Fatalf("Jira blocker %s is not ready", blocker.ID)
	}
	if blocker.Status != types.StatusOpen {
		t.Fatalf("Jira blocker status = %s, want open", blocker.Status)
	}
	selectivePull := parseJiraSyncResult(t, runJSON("--json", "jira", "pull", "GC-2", "--relations"))
	if !selectivePull.Success {
		t.Fatalf("selective Jira pull result = %+v", selectivePull)
	}
	setUpdatedAt(linked.ID)
	if err := store.SetLocalMetadata(t.Context(), "jira.last_sync", previousSync); err != nil {
		t.Fatalf("seed Jira cursor: %v", err)
	}
	run("update", linked.ID, "--title", "local title pushed through Jira")
	selectivePush := parseJiraSyncResult(t, runJSON("--json", "jira", "push", linked.ID))
	if !selectivePush.Success {
		t.Fatalf("selective Jira push result = %+v", selectivePush)
	}
	created := parseIssueJSON(t, runJSON("create", "--json", "new local issue to push"))
	if result := parseJiraSyncResult(t, runJSON("--json", "jira", "push", created.ID)); !result.Success {
		t.Fatalf("selective Jira create push result = %+v", result)
	}
	issues = jiraListIssues(t, runJSON("list", "--all", "--json"))
	return jiraPullPushResult{issues: issues, linkedRef: server.URL + "/browse/GC-2", parentRef: server.URL + "/browse/GC-1", blockerRef: server.URL + "/browse/GC-3", dependencies: string(run("dep", "list", linked.ID)), remoteUpdatedTitle: mock.title("GC-2"), remoteIssueCount: mock.count()}
}

func jiraSetIssueUpdatedAt(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), "UPDATE issues SET updated_at = ? WHERE id = ?", time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC), id); err != nil {
		t.Fatalf("seed local issue %s updated_at: %v", id, err)
	}
}

func parseJiraSyncResult(t *testing.T, out []byte) tracker.SyncResult {
	t.Helper()
	var result tracker.SyncResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("decode Jira sync JSON: %v\n%s", err, out)
	}
	return result
}

func jiraListIssues(t *testing.T, out []byte) []*types.Issue {
	t.Helper()
	var issues []*types.Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		t.Fatalf("decode list JSON: %v\n%s", err, out)
	}
	return issues
}

func jiraFindIssue(t *testing.T, issues []*types.Issue, ref string) *types.Issue {
	t.Helper()
	for _, issue := range issues {
		if issue.ExternalRef != nil && *issue.ExternalRef == ref {
			return issue
		}
	}
	t.Fatalf("no issue with Jira external ref %q", ref)
	return nil
}

func assertJiraPullPushSemantics(t *testing.T, result jiraPullPushResult) {
	t.Helper()
	linked := jiraFindIssue(t, result.issues, result.linkedRef)
	if linked.Status != types.StatusOpen || linked.Title != "local title pushed through Jira" {
		t.Fatalf("linked Jira issue = %#v", linked)
	}
	parent := jiraFindIssue(t, result.issues, result.parentRef)
	blocker := jiraFindIssue(t, result.issues, result.blockerRef)
	if !strings.Contains(result.dependencies, parent.ID) || !strings.Contains(result.dependencies, "parent-child") || !strings.Contains(result.dependencies, blocker.ID) || !strings.Contains(result.dependencies, "blocks") {
		t.Fatalf("Jira child dependencies = %q", result.dependencies)
	}
	if result.remoteUpdatedTitle != "local title pushed through Jira" || result.remoteIssueCount != 4 {
		t.Fatalf("remote state: title=%q count=%d", result.remoteUpdatedTitle, result.remoteIssueCount)
	}
	created := jiraFindIssueByTitle(t, result.issues, "new local issue to push")
	if created.ExternalRef == nil || !strings.Contains(*created.ExternalRef, "/browse/GC-") {
		t.Fatalf("created Jira issue lacks external ref: %#v", created)
	}
}

func jiraFindIssueByTitle(t *testing.T, issues []*types.Issue, title string) *types.Issue {
	t.Helper()
	for _, issue := range issues {
		if issue.Title == title {
			return issue
		}
	}
	t.Fatalf("no issue %q", title)
	return nil
}

type mockJiraServer struct {
	mu     sync.Mutex
	issues map[string]*jira.Issue
	next   int
	base   string
}

func newMockJiraServer() *mockJiraServer {
	return &mockJiraServer{issues: map[string]*jira.Issue{}, next: 1}
}

func (m *mockJiraServer) seed(base, key, title string, updated time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.base = base
	m.issues[key] = mockJiraIssue(base, key, title, updated)
	m.next = 2
}

func (m *mockJiraServer) seedGraph(base string, updated time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.base = base
	parent := mockJiraIssue(base, "GC-1", "remote parent", updated)
	parent.Fields.Status = &jira.StatusField{Name: "Done"}
	blocker := mockJiraIssue(base, "GC-3", "remote blocker", updated)
	child := mockJiraIssue(base, "GC-2", "remote issue", updated)
	child.Fields.Parent = &jira.IssueReference{ID: parent.ID, Key: parent.Key}
	child.Fields.IssueLinks = []jira.IssueLink{{Type: jira.IssueLinkType{Inward: "is blocked by", Outward: "blocks"}, InwardIssue: &jira.IssueReference{ID: blocker.ID, Key: blocker.Key}}}
	m.issues = map[string]*jira.Issue{parent.Key: parent, child.Key: child, blocker.Key: blocker}
	m.next = 4
}

func mockJiraIssue(base, key, title string, updated time.Time) *jira.Issue {
	stamp := updated.UTC().Format("2006-01-02T15:04:05.000-0700")
	return &jira.Issue{ID: key, Key: key, Self: base + "/rest/api/3/issue/" + key, Fields: jira.IssueFields{Summary: title, Description: json.RawMessage(`"remote description"`), Status: &jira.StatusField{Name: "Open"}, Priority: &jira.PriorityField{Name: "Medium"}, IssueType: &jira.IssueTypeField{Name: "Task"}, Project: &jira.ProjectField{Key: "GC"}, Created: stamp, Updated: stamp}}
}

func (m *mockJiraServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/rest/api/3/")
	if r.Method == http.MethodGet && path == "search/jql" {
		issues := make([]jira.Issue, 0, len(m.issues))
		for _, issue := range m.issues {
			issues = append(issues, *issue)
		}
		_ = json.NewEncoder(w).Encode(jira.SearchResult{Issues: issues, IsLast: true})
		return
	}
	key := strings.TrimPrefix(path, "issue/")
	if r.Method == http.MethodGet && key != "" {
		if issue := m.issues[key]; issue != nil {
			_ = json.NewEncoder(w).Encode(issue)
			return
		}
	}
	if r.Method == http.MethodPost && path == "issue" {
		var payload struct {
			Fields map[string]any `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		key = fmt.Sprintf("GC-%d", m.next)
		m.next++
		title, _ := payload.Fields["summary"].(string)
		m.issues[key] = mockJiraIssue(m.base, key, title, time.Now().UTC())
		_ = json.NewEncoder(w).Encode(map[string]string{"id": key, "key": key, "self": m.issues[key].Self})
		return
	}
	if r.Method == http.MethodPut && key != "" {
		var payload struct {
			Fields map[string]any `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if issue := m.issues[key]; issue != nil {
			if title, ok := payload.Fields["summary"].(string); ok {
				issue.Fields.Summary = title
			}
			issue.Fields.Updated = time.Now().UTC().Format("2006-01-02T15:04:05.000-0700")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	http.Error(w, "unhandled Jira request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
}

func (m *mockJiraServer) title(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.issues[key].Fields.Summary
}
func (m *mockJiraServer) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.issues) }

func (m *mockJiraServer) snapshot() map[string]jira.Issue {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]jira.Issue, len(m.issues))
	for key, issue := range m.issues {
		encoded, err := json.Marshal(issue)
		if err != nil {
			panic(err)
		}
		var copied jira.Issue
		if err := json.Unmarshal(encoded, &copied); err != nil {
			panic(err)
		}
		result[key] = copied
	}
	return result
}
