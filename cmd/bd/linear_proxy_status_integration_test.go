//go:build cgo && unix

package main

import (
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

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
