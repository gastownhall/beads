//go:build cgo && unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestLinearStatusSQLServerFrontdoor covers the direct SQL-server leg. The
// managed-local test server is intentionally not started when the proxied lane
// is selected, so this is a separate CI-selected process-level scenario.
func TestLinearStatusSQLServerFrontdoor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt SQL test server not available")
	}
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "issues.db")
	// A dedicated database is required here: the shared test-store helper uses
	// a connection-scoped Dolt branch that a child bd process cannot inherit.
	store := newTestStoreIsolatedDB(t, dbPath, "linstatus")
	ctx := context.Background()
	if err := store.SetConfig(ctx, "linear.last_sync", "2026-09-09T01:02:03Z"); err != nil {
		t.Fatal(err)
	}
	ref := "https://linear.app/team/issue/ENG-1"
	if err := store.CreateIssue(ctx, &types.Issue{ID: "test-1", Title: "linked", Status: types.StatusOpen, IssueType: types.TypeTask, ExternalRef: &ref}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateIssue(ctx, &types.Issue{ID: "test-2", Title: "local", Status: types.StatusOpen, IssueType: types.TypeTask}, "test"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bd, "--json", "linear", "status")
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), "BEADS_DIR="+filepath.Join(dir, ".beads"), "BEADS_TEST_SERVER=1", "LINEAR_API_KEY=test-key", "LINEAR_TEAM_ID=12345678-1234-1234-1234-123456789abc")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("direct SQL linear status: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
	}
	var status map[string]any
	if err := json.Unmarshal(out, &status); err != nil {
		t.Fatalf("decode direct SQL status: %v", err)
	}
	assertLinearStatus(t, status)
}

// TestManagedLocalProxiedLinearStatusParity drives the public status command
// against fresh direct and managed-local workspaces. It keeps config, issue
// counts, linked references, authentication mode, team selection, and the
// sync timestamp on the same tracker.Store seam used by Linear sync.
func TestManagedLocalProxiedLinearStatusParity(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildBDForInitTests(t)
	team := "12345678-1234-1234-1234-123456789abc"
	lastSync := "2026-09-09T01:02:03Z"
	env := []string{"LINEAR_API_KEY=test-key", "LINEAR_TEAM_ID=" + team}

	directDir, _, _ := bdInit(t, bd, "--prefix", "linstatus", "--non-interactive", "--skip-hooks", "--skip-agents")
	direct := linearStatusFixture(t, bd, directDir, bdEnv(directDir), env, lastSync)

	proxy := bdManagedLocalInit(t, bd, "linstatus", 5*time.Minute)
	proxied := linearStatusFixture(t, bd, proxy.dir, bdProxiedEnv(proxy.dir), env, lastSync)

	for _, key := range []string{"configured", "has_api_key", "has_oauth", "auth_mode", "team_id", "team_ids", "last_sync", "total_issues", "with_linear_ref", "pending_push"} {
		if got, want := proxied[key], direct[key]; !jsonEqual(got, want) {
			t.Fatalf("status[%s]: proxied=%#v direct=%#v", key, got, want)
		}
	}
	assertLinearStatus(t, proxied)
}

func assertLinearStatus(t *testing.T, status map[string]any) {
	t.Helper()
	if status["last_sync"] != "2026-09-09T01:02:03Z" || status["auth_mode"] != "api_key" || status["total_issues"] != float64(2) || status["with_linear_ref"] != float64(1) || status["pending_push"] != float64(1) {
		t.Fatalf("unexpected linear status: %#v", status)
	}
}

func linearStatusFixture(t *testing.T, bd, dir string, baseEnv, extraEnv []string, lastSync string) map[string]any {
	t.Helper()
	run := func(args ...string) []byte {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = append(append([]string(nil), baseEnv...), extraEnv...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %v: %v\n%s", args, err, out)
		}
		return out
	}
	run("config", "set", "linear.last_sync", lastSync)
	run("create", "linked", "--external-ref", "https://linear.app/team/issue/ENG-1")
	run("create", "local")
	var status map[string]any
	if err := json.Unmarshal(run("--json", "linear", "status"), &status); err != nil {
		t.Fatalf("decode linear status: %v", err)
	}
	return status
}

func jsonEqual(a, b any) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return string(aJSON) == string(bJSON)
}
