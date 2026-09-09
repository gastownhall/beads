//go:build cgo && unix

package main

import (
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
)

// TestManagedLocalProxiedJiraStatusParity proves that status reads the same
// configuration, local sync cursor, and issue counts through the direct and
// managed-proxy tracker stores.
func TestManagedLocalProxiedJiraStatusParity(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildBDForInitTests(t)
	const lastSync = "2026-09-09T01:02:03Z"

	directDir, directBeadsDir, _ := bdInit(t, bd, "--prefix", "jirastatus", "--non-interactive", "--skip-hooks", "--skip-agents")
	directRaw, err := newDoltStoreFromConfig(t.Context(), directBeadsDir)
	if err != nil {
		t.Fatalf("open direct tracker store: %v", err)
	}
	t.Cleanup(func() { _ = directRaw.Close() })
	direct := jiraStatusFixture(t, bd, directDir, bdEnv(directDir), tracker.NewStore(directRaw), lastSync)

	project := bdManagedLocalInit(t, bd, "jirastatus", 5*time.Minute)
	provider, err := newProxiedServerUOWProvider(t.Context(), project.beadsDir, "")
	if err != nil {
		t.Fatalf("open managed-local proxy UOW provider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close(t.Context()) })
	proxied := jiraStatusFixture(t, bd, project.dir, bdProxiedEnv(project.dir), tracker.NewUOWStore(provider), lastSync)

	for _, key := range []string{"configured", "jira_url", "jira_project", "jira_projects", "last_sync", "total_issues", "with_jira_ref", "pending_push"} {
		if got, want := proxied[key], direct[key]; !jsonEqual(got, want) {
			t.Fatalf("status[%s]: proxied=%#v direct=%#v", key, got, want)
		}
	}
	if proxied["last_sync"] != lastSync || proxied["total_issues"] != float64(2) || proxied["with_jira_ref"] != float64(1) || proxied["pending_push"] != float64(1) {
		t.Fatalf("unexpected Jira status: %#v", proxied)
	}
}

func jiraStatusFixture(t *testing.T, bd, dir string, env []string, store tracker.Store, lastSync string) map[string]any {
	t.Helper()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = append([]string(nil), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %v: %v\n%s", args, err, out)
		}
		return out
	}
	for _, entry := range [][2]string{{"jira.url", "https://jira.example.test"}, {"jira.project", "GC"}} {
		run("config", "set", entry[0], entry[1])
	}
	run("create", "linked", "--external-ref", "https://jira.example.test/browse/GC-1")
	run("create", "local")
	if err := store.SetLocalMetadata(t.Context(), "jira.last_sync", lastSync); err != nil {
		t.Fatalf("seed local last_sync metadata: %v", err)
	}
	var status map[string]any
	if err := json.Unmarshal(run("--json", "jira", "status"), &status); err != nil {
		t.Fatalf("decode Jira status: %v", err)
	}
	return status
}
