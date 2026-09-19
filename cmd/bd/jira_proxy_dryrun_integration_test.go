//go:build cgo && unix

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
)

func TestJiraDryRunSQLServerFrontdoor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt SQL test server not available")
	}
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "issues.db")
	raw := newTestStoreIsolatedDB(t, dbPath, "jiradryrun")
	if err := os.WriteFile(filepath.Join(dir, ".beads", "config.yaml"), []byte("# Beads Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runJiraDryRunFixture(t, bd, dir, append(bdEnv(dir), "BEADS_DIR="+filepath.Join(dir, ".beads"), "BEADS_TEST_SERVER=1", "JIRA_API_TOKEN=test-api-token"), tracker.NewStore(raw), raw.DB(), false)
}

func TestManagedLocalProxiedJiraDryRunParity(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildBDForInitTests(t)
	project := bdManagedLocalInit(t, bd, "jiradryrun", 5*time.Minute)
	provider, err := newProxiedServerUOWProvider(t.Context(), project.beadsDir, "")
	if err != nil {
		t.Fatalf("open managed-local proxy UOW provider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close(t.Context()) })
	runJiraDryRunFixture(t, bd, project.dir, append(bdProxiedEnv(project.dir), "JIRA_API_TOKEN=test-api-token"), tracker.NewUOWStore(provider), openProxiedDB(t, project), true)
}

func runJiraDryRunFixture(t *testing.T, bd, dir string, env []string, store tracker.Store, db *sql.DB, proxied bool) {
	t.Helper()
	mock := newMockJiraServer()
	server := httptest.NewServer(mock)
	t.Cleanup(server.Close)
	mock.seed(server.URL, "GC-1", "remote dry-run title", time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
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
	linked := parseIssueJSON(t, run("create", "--json", "local dry-run title", "--external-ref", server.URL+"/browse/GC-1"))
	run("create", "local issue that dry-run would create")
	if _, err := db.ExecContext(t.Context(), "UPDATE issues SET updated_at = ? WHERE id = ?", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), linked.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLocalMetadata(t.Context(), "jira.last_sync", "2020-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	before, err := proxyTrackerSnapshot(t.Context(), store, "jira.last_sync")
	if err != nil {
		t.Fatalf("snapshot before dry-run: %v", err)
	}
	remoteBefore := mock.snapshot()
	// Each front-door refusal is a single JSON document and must occur before
	// either local or remote mutation.
	for _, args := range [][]string{{"--json", "jira", "push"}, {"--json", "jira", "pull"}, {"--json", "jira", "sync", "--prefer-local", "--prefer-jira"}} {
		refusal := exec.Command(bd, args...)
		refusal.Dir, refusal.Env = dir, append([]string(nil), env...)
		refusalOut, refusalErr := refusal.Output()
		if refusalErr == nil {
			t.Fatalf("Jira refusal %s unexpectedly succeeded", strings.Join(args, " "))
		}
		var refusalJSON map[string]any
		if err := json.Unmarshal(refusalOut, &refusalJSON); err != nil {
			t.Fatalf("Jira refusal %s stdout is not JSON: %v\n%s", strings.Join(args, " "), err, refusalOut)
		}
		afterRefusal, err := proxyTrackerSnapshot(t.Context(), store, "jira.last_sync")
		if err != nil || !reflect.DeepEqual(before, afterRefusal) || !reflect.DeepEqual(remoteBefore, mock.snapshot()) {
			t.Fatalf("Jira refusal %s mutated state: err=%v", strings.Join(args, " "), err)
		}
	}
	readonly := exec.Command(bd, "--readonly", "--json", "jira", "sync", "--dry-run", "--pull")
	readonly.Dir, readonly.Env = dir, append([]string(nil), env...)
	readonlyOut, readonlyErr := readonly.Output()
	if proxied {
		if readonlyErr == nil {
			t.Fatal("proxied strict-readonly Jira dry-run unexpectedly succeeded")
		}
		var response map[string]any
		if err := json.Unmarshal(readonlyOut, &response); err != nil || response["code"] != "proxy.readonly.unsupported" || response["mutates"] != false {
			t.Fatalf("proxied readonly refusal = %#v, decode err=%v", response, err)
		}
	} else {
		if readonlyErr != nil {
			t.Fatalf("direct readonly Jira dry-run: %v\n%s", readonlyErr, readonlyOut)
		}
		var result tracker.SyncResult
		if err := json.Unmarshal(readonlyOut, &result); err != nil || !result.Success {
			t.Fatalf("direct readonly dry-run result=%+v decode err=%v", result, err)
		}
	}
	mutatingReadonly := exec.Command(bd, "--readonly", "--json", "jira", "sync", "--pull")
	mutatingReadonly.Dir, mutatingReadonly.Env = dir, append([]string(nil), env...)
	mutatingOut, mutatingErr := mutatingReadonly.Output()
	if mutatingErr == nil {
		t.Fatal("mutating readonly Jira sync unexpectedly succeeded")
	}
	if proxied {
		var response map[string]any
		if err := json.Unmarshal(mutatingOut, &response); err != nil || response["code"] != "proxy.readonly.unsupported" || response["mutates"] != false {
			t.Fatalf("proxied mutating readonly refusal = %#v, decode err=%v", response, err)
		}
	}
	afterReadonly, err := proxyTrackerSnapshot(t.Context(), store, "jira.last_sync")
	if err != nil || !reflect.DeepEqual(before, afterReadonly) || !reflect.DeepEqual(remoteBefore, mock.snapshot()) {
		t.Fatalf("readonly Jira commands mutated state: err=%v", err)
	}
	for _, args := range [][]string{{"--json", "jira", "sync", "--dry-run", "--pull"}, {"--json", "jira", "sync", "--dry-run", "--push"}, {"--json", "jira", "sync", "--dry-run"}} {
		t.Run(strings.Join(args[3:], " "), func(t *testing.T) {
			cmd := exec.Command(bd, args...)
			cmd.Dir, cmd.Env = dir, append([]string(nil), env...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("dry run %s: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, out, stderr.String())
			}
			var result tracker.SyncResult
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("dry-run stdout is not one JSON value: %v\n%s", err, out)
			}
			if !result.Success || result.LastSync != "" {
				t.Fatalf("dry-run result = %+v", result)
			}
			after, err := proxyTrackerSnapshot(t.Context(), store, "jira.last_sync")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("%s changed local state:\nbefore=%#v\nafter=%#v", strings.Join(args, " "), before, after)
			}
			if !reflect.DeepEqual(remoteBefore, mock.snapshot()) {
				t.Fatalf("%s changed remote state", strings.Join(args, " "))
			}
		})
	}
}
