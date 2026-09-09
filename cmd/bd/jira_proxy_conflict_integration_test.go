//go:build cgo && unix

package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func TestJiraConflictJSONSQLServerFrontdoor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt SQL test server not available")
	}
	runJiraDirectConflictFixture(t, "--prefer-local", "local title")
}

func TestJiraPreferExternalConflictSQLServerFrontdoor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt SQL test server not available")
	}
	runJiraDirectConflictFixture(t, "--prefer-jira", "remote title")
}

func TestJiraDefaultConflictSQLServerFrontdoor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt SQL test server not available")
	}
	runJiraDirectConflictFixture(t, "", "local title")
}

func runJiraDirectConflictFixture(t *testing.T, preference, wantRemoteTitle string) {
	t.Helper()
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "issues.db")
	raw := newTestStoreIsolatedDB(t, dbPath, "jiraconflict")
	if err := os.WriteFile(filepath.Join(dir, ".beads", "config.yaml"), []byte("# Beads Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runJiraConflictJSONFixture(t, bd, dir, append(bdEnv(dir), "BEADS_DIR="+filepath.Join(dir, ".beads"), "BEADS_TEST_SERVER=1", "JIRA_API_TOKEN=test-api-token"), tracker.NewStore(raw), raw.DB(), preference, wantRemoteTitle)
}

// TestManagedLocalProxiedJiraConflictJSONParity proves that the managed proxy
// produces one JSON value even when the shared engine emits conflict messages.
func TestManagedLocalProxiedJiraConflictJSONParity(t *testing.T) {
	runJiraManagedConflictFixture(t, "--prefer-local", "local title")
}

func TestManagedLocalProxiedJiraPreferExternalConflictParity(t *testing.T) {
	runJiraManagedConflictFixture(t, "--prefer-jira", "remote title")
}

func TestManagedLocalProxiedJiraDefaultConflictParity(t *testing.T) {
	runJiraManagedConflictFixture(t, "", "local title")
}

func runJiraManagedConflictFixture(t *testing.T, preference, wantRemoteTitle string) {
	t.Helper()
	requireManagedLocalProxiedEnv(t)
	bd := buildBDForInitTests(t)
	project := bdManagedLocalInit(t, bd, "jiraconflict", 5*time.Minute)
	provider, err := newProxiedServerUOWProvider(t.Context(), project.beadsDir, "")
	if err != nil {
		t.Fatalf("open managed-local proxy UOW provider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close(t.Context()) })
	runJiraConflictJSONFixture(t, bd, project.dir, append(bdProxiedEnv(project.dir), "JIRA_API_TOKEN=test-api-token"), tracker.NewUOWStore(provider), openProxiedDB(t, project), preference, wantRemoteTitle)
}

func runJiraConflictJSONFixture(t *testing.T, bd, dir string, env []string, store tracker.Store, db *sql.DB, preference, wantRemoteTitle string) {
	t.Helper()
	mock := newMockJiraServer()
	server := httptest.NewServer(mock)
	t.Cleanup(server.Close)
	mock.seed(server.URL, "GC-1", "remote title", time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
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
	for _, entry := range [][2]string{{"jira.url", server.URL}, {"jira.project", "GC"}} {
		run("config", "set", entry[0], entry[1])
	}
	local := parseIssueJSON(t, run("create", "--json", "local title", "--external-ref", server.URL+"/browse/GC-1"))
	if _, err := db.ExecContext(t.Context(), "UPDATE issues SET updated_at = ? WHERE id = ?", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), local.ID); err != nil {
		t.Fatalf("seed local conflict timestamp: %v", err)
	}
	if err := store.SetLocalMetadata(t.Context(), "jira.last_sync", "2020-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed Jira cursor: %v", err)
	}
	args := []string{"--json", "jira", "sync"}
	if preference != "" {
		args = append(args, preference)
	}
	cmd := exec.Command(bd, args...)
	cmd.Dir, cmd.Env = dir, append([]string(nil), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("JSON Jira conflict sync: %v\n%s", err, out)
	}
	var result tracker.SyncResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Jira conflict stdout must be one JSON value: %v\n%s", err, out)
	}
	if !result.Success || result.LastSync == "" || result.Stats.Conflicts != 1 || mock.title("GC-1") != wantRemoteTitle {
		t.Fatalf("conflict result=%+v remote title=%q", result, mock.title("GC-1"))
	}
	wantLocalTitle := "local title"
	if preference == "--prefer-jira" {
		wantLocalTitle = "remote title"
	}
	issues, err := store.SearchIssues(t.Context(), "", types.IssueFilter{})
	var gotLocal *types.Issue
	if err == nil {
		for _, issue := range issues {
			if issue.ID == local.ID {
				gotLocal = issue
				break
			}
		}
	}
	if err != nil || gotLocal == nil || gotLocal.Title != wantLocalTitle {
		t.Fatalf("resolved local conflict = %#v, err=%v; want title %q", gotLocal, err, wantLocalTitle)
	}
	persisted, err := store.GetLocalMetadata(t.Context(), "jira.last_sync")
	if err != nil || persisted != result.LastSync {
		t.Fatalf("persisted Jira conflict cursor = %q, err=%v; want %q", persisted, err, result.LastSync)
	}
}
