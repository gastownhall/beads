//go:build cgo && unix

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

const linearProxyTestTeamID = "12345678-1234-1234-1234-123456789abc"

// TestLinearPullPushSQLServerFrontdoor exercises the direct SQL-server
// topology. It uses a dedicated database because a child bd process cannot
// inherit a connection-scoped shared-store branch.
func TestLinearPullPushSQLServerFrontdoor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt SQL test server not available")
	}
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "issues.db")
	raw := newTestStoreIsolatedDB(t, dbPath, "linpullpush")
	if err := os.WriteFile(filepath.Join(dir, ".beads", "config.yaml"), []byte("# Beads Config\n"), 0o600); err != nil {
		t.Fatalf("create direct SQL config.yaml: %v", err)
	}
	result := runLinearPullPushFixture(t, bd, dir, append(bdEnv(dir),
		"BEADS_DIR="+filepath.Join(dir, ".beads"),
		"BEADS_TEST_SERVER=1",
		"LINEAR_API_KEY=test-api-key",
	), tracker.NewStore(raw), func(id string) { linearSetIssueUpdatedAt(t, raw.DB(), id) })
	assertLinearPullPushSemantics(t, result)
}

// TestManagedLocalProxiedLinearPullPushParity exercises the same CLI sequence
// through the managed loopback proxy. The direct SQL test above runs in its own
// environment because this lane deliberately suppresses testcontainer startup.
func TestManagedLocalProxiedLinearPullPushParity(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildBDForInitTests(t)
	project := bdManagedLocalInit(t, bd, "linpullpush", 5*time.Minute)
	provider, err := newProxiedServerUOWProvider(t.Context(), project.beadsDir, "")
	if err != nil {
		t.Fatalf("open managed-local proxy UOW provider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close(t.Context()) })
	proxyDB := openProxiedDB(t, project)
	result := runLinearPullPushFixture(t, bd, project.dir, append(bdProxiedEnv(project.dir), "LINEAR_API_KEY=test-api-key"), tracker.NewUOWStore(provider), func(id string) { linearSetIssueUpdatedAt(t, proxyDB, id) })
	assertLinearPullPushSemantics(t, result)
}

type linearPullPushResult struct {
	issues             []*types.Issue
	remoteUpdatedTitle string
	remoteIssueCount   int
	childDependencies  string
}

func runLinearPullPushFixture(t *testing.T, bd, dir string, env []string, store tracker.Store, setUpdatedAt func(string)) linearPullPushResult {
	t.Helper()
	mock := newMockLinearServer(linearProxyTestTeamID, "MOCK")
	server := httptest.NewServer(mock)
	t.Cleanup(server.Close)
	seedLinearPullGraph(mock)

	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = append([]string(nil), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	for _, entry := range [][2]string{
		{"linear.team_id", linearProxyTestTeamID},
		{"linear.api_endpoint", server.URL},
		{"linear.state_map.unstarted", "open"},
		{"linear.state_map.started", "in_progress"},
		{"linear.state_map.completed", "closed"},
	} {
		run("config", "set", entry[0], entry[1])
	}

	childURL := "https://linear.app/mock/issue/MOCK-2"
	stale := parseIssueJSON(t, run("create", "--json", "stale local child", "--external-ref", childURL))
	// The local issue predates the recorded sync point while the remote closed
	// issue is newer, so pull must update rather than report a conflict.
	const previousSync = "2020-01-01T00:00:00Z"
	setUpdatedAt(stale.ID)
	if err := store.SetLocalMetadata(t.Context(), "linear.last_sync", previousSync); err != nil {
		t.Fatalf("seed linear.last_sync local metadata: %v", err)
	}
	pull := parseLinearSyncResult(t, run("--json", "linear", "sync", "--pull", "--relations"))
	assertLinearPullMetadata(t, store, previousSync, pull)

	issues := linearListIssues(t, run("list", "--all", "--json"))
	child := findLinearIssue(t, issues, childURL)
	if child.ID != stale.ID {
		t.Fatalf("pull created a duplicate child: got %s, want %s", child.ID, stale.ID)
	}
	run("update", child.ID, "--title", "local title pushed through Linear")
	run("linear", "push", child.ID)
	created := parseIssueJSON(t, run("create", "--json", "new local issue to push"))
	run("linear", "push", created.ID)

	issues = linearListIssues(t, run("list", "--all", "--json"))
	return linearPullPushResult{
		issues:             issues,
		remoteUpdatedTitle: linearMockIssueTitle(t, mock, "MOCK-2"),
		remoteIssueCount:   mock.issueCount(),
		childDependencies:  string(run("dep", "list", child.ID)),
	}
}

func linearSetIssueUpdatedAt(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), "UPDATE issues SET updated_at = ? WHERE id = ?", time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC), id); err != nil {
		t.Fatalf("seed local issue %s updated_at before last_sync: %v", id, err)
	}
}

func seedLinearPullGraph(mock *mockLinearServer) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	parent := &linear.Issue{
		ID: "remote-parent", Identifier: "MOCK-1", Title: "remote parent", Description: "parent from Linear",
		URL: "https://linear.app/mock/issue/MOCK-1", Priority: 2,
		State:     &linear.State{ID: "state-unstarted", Name: "Todo", Type: "unstarted"},
		CreatedAt: "2024-12-31T00:00:00Z", UpdatedAt: "2025-01-02T00:00:00Z",
	}
	blocker := &linear.Issue{
		ID: "remote-blocker", Identifier: "MOCK-3", Title: "remote blocker", Description: "blocks the remote child",
		URL: "https://linear.app/mock/issue/MOCK-3", Priority: 3,
		State:     &linear.State{ID: "state-unstarted", Name: "Todo", Type: "unstarted"},
		CreatedAt: "2024-12-31T00:00:00Z", UpdatedAt: "2025-01-02T00:00:00Z",
	}
	relation := linear.Relation{ID: "blocked-by-blocker", Type: "blockedBy"}
	relation.RelatedIssue.ID = blocker.ID
	relation.RelatedIssue.Identifier = blocker.Identifier
	child := &linear.Issue{
		ID: "remote-child", Identifier: "MOCK-2", Title: "remote closed child", Description: "closed remotely",
		URL: "https://linear.app/mock/issue/MOCK-2", Priority: 1,
		State:     &linear.State{ID: "state-completed", Name: "Done", Type: "completed"},
		Labels:    &linear.Labels{Nodes: []linear.Label{{ID: "label-remote-a", Name: " remote-label "}, {ID: "label-remote-b", Name: "remote-label"}, {ID: "label-remote-empty", Name: ""}}},
		Parent:    &linear.Parent{ID: parent.ID, Identifier: parent.Identifier},
		Relations: &linear.Relations{Nodes: []linear.Relation{relation}},
		CreatedAt: "2024-12-31T00:00:00Z", UpdatedAt: "2025-01-02T00:00:00Z", CompletedAt: "2025-01-02T00:00:00Z",
	}
	mock.issues[parent.ID] = parent
	mock.issues[child.ID] = child
	mock.issues[blocker.ID] = blocker
	mock.nextSeq = 3
}

func linearListIssues(t *testing.T, out []byte) []*types.Issue {
	t.Helper()
	var issues []*types.Issue
	start := strings.Index(string(out), "[")
	if start < 0 {
		t.Fatalf("bd list --json emitted no array:\n%s", out)
	}
	if err := json.Unmarshal(out[start:], &issues); err != nil {
		t.Fatalf("decode bd list --json: %v\n%s", err, out)
	}
	return issues
}

func parseLinearSyncResult(t *testing.T, out []byte) tracker.SyncResult {
	t.Helper()
	start := strings.Index(string(out), "{")
	if start < 0 {
		t.Fatalf("bd linear sync --json emitted no object:\n%s", out)
	}
	var result tracker.SyncResult
	if err := json.Unmarshal(out[start:], &result); err != nil {
		t.Fatalf("decode bd linear sync --json: %v\n%s", err, out)
	}
	return result
}

func assertLinearPullMetadata(t *testing.T, store tracker.Store, previousSync string, result tracker.SyncResult) {
	t.Helper()
	previous, err := time.Parse(time.RFC3339, previousSync)
	if err != nil {
		t.Fatalf("parse previous last_sync: %v", err)
	}
	actual, err := time.Parse(time.RFC3339Nano, result.LastSync)
	if err != nil {
		t.Fatalf("parse pull result last_sync %q: %v", result.LastSync, err)
	}
	if !actual.After(previous) {
		t.Fatalf("pull last_sync = %s, want after %s", actual, previous)
	}
	persisted, err := store.GetLocalMetadata(t.Context(), "linear.last_sync")
	if err != nil {
		t.Fatalf("read persisted linear.last_sync local metadata: %v", err)
	}
	if persisted != result.LastSync {
		t.Fatalf("persisted linear.last_sync = %q, want sync result %q", persisted, result.LastSync)
	}
}

func findLinearIssue(t *testing.T, issues []*types.Issue, externalRef string) *types.Issue {
	t.Helper()
	for _, issue := range issues {
		if issue.ExternalRef != nil && *issue.ExternalRef == externalRef {
			return issue
		}
	}
	t.Fatalf("no local issue with external_ref %q in %#v", externalRef, issues)
	return nil
}

func linearMockIssueTitle(t *testing.T, mock *mockLinearServer, identifier string) string {
	t.Helper()
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for _, issue := range mock.issues {
		if issue.Identifier == identifier {
			return issue.Title
		}
	}
	t.Fatalf("mock issue %s not found", identifier)
	return ""
}

func assertLinearPullPushSemantics(t *testing.T, result linearPullPushResult) {
	t.Helper()
	child := findLinearIssue(t, result.issues, "https://linear.app/mock/issue/MOCK-2")
	if child.Title != "local title pushed through Linear" || child.Status != types.StatusClosed || child.ExternalRef == nil || *child.ExternalRef != "https://linear.app/mock/issue/MOCK-2" {
		t.Fatalf("pulled child after push = %#v", child)
	}
	if !sameStrings(child.Labels, []string{"remote-label"}) {
		t.Fatalf("pulled child labels = %#v, want normalized remote labels", child.Labels)
	}
	parent := findLinearIssue(t, result.issues, "https://linear.app/mock/issue/MOCK-1")
	blocker := findLinearIssue(t, result.issues, "https://linear.app/mock/issue/MOCK-3")
	if !strings.Contains(result.childDependencies, parent.ID) || !strings.Contains(result.childDependencies, "parent-child") || !strings.Contains(result.childDependencies, blocker.ID) || !strings.Contains(result.childDependencies, "blocks") {
		t.Fatalf("child dependencies = %q; expected parent and blocks edges for %s and %s", result.childDependencies, parent.ID, blocker.ID)
	}
	if result.remoteUpdatedTitle != "local title pushed through Linear" {
		t.Fatalf("remote update title = %q", result.remoteUpdatedTitle)
	}
	created := findLinearIssueByTitle(t, result.issues, "new local issue to push")
	if created.ExternalRef == nil || !strings.Contains(*created.ExternalRef, "/MOCK-") {
		t.Fatalf("created local issue has no Linear external_ref: %#v", created)
	}
	if result.remoteIssueCount != 4 {
		t.Fatalf("remote issue count = %d, want 4 after local create", result.remoteIssueCount)
	}
}

func findLinearIssueByTitle(t *testing.T, issues []*types.Issue, title string) *types.Issue {
	t.Helper()
	for _, issue := range issues {
		if issue.Title == title {
			return issue
		}
	}
	t.Fatalf("no local issue titled %q", title)
	return nil
}

func sameStrings(got, want []string) bool {
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	return fmt.Sprint(got) == fmt.Sprint(want)
}
