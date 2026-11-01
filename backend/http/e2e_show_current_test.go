//go:build cgo

package bdhttp_test

import (
	"strings"
	"testing"
)

// connectE2EWorkspace starts an in-process server and returns a workspace
// `bd connect`ed to it, plus a runner that drives the built binary there as
// the given actor.
func connectE2EWorkspace(t *testing.T, actor string) func(args ...string) bdResult {
	t.Helper()
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)
	workspace := t.TempDir()
	env := []string{"BEADS_ACTOR=" + actor}
	if r := runBD(t, bin, workspace, env, "connect", "http://"+addr, "--expect-project-id", e2eProjectID, "--json"); r.code != 0 {
		t.Fatalf("bd connect failed (exit %d): stdout=%s stderr=%s", r.code, r.stdout, r.stderr)
	}
	return func(args ...string) bdResult {
		t.Helper()
		return runBD(t, bin, workspace, env, args...)
	}
}

func mustRun(t *testing.T, bd func(args ...string) bdResult, args ...string) bdResult {
	t.Helper()
	r := bd(args...)
	if r.code != 0 {
		t.Fatalf("bd %s failed (exit %d): stdout=%s stderr=%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

func createE2EIssue(t *testing.T, bd func(args ...string) bdResult, title string) string {
	t.Helper()
	id := firstJSONField(t, mustRun(t, bd, "create", title, "--json").stdout, "id")
	if id == "" {
		t.Fatalf("bd create %q --json produced no id", title)
	}
	return id
}

// TestE2E_ShowCurrentOverHTTP is S6e. `bd show --current` used to read the
// actor's in-progress work through the raw SearchIssues method, which the http
// backend refuses; the refusal was swallowed and the command printed the
// LAST-TOUCHED issue instead, exit 0. It now reads through the Querier role
// (queryIssues), so the in-progress issue wins over a more recently touched
// one, and a hooked issue is found when nothing is in progress.
func TestE2E_ShowCurrentOverHTTP(t *testing.T) {
	bd := connectE2EWorkspace(t, "worker")

	current := createE2EIssue(t, bd, "the work in progress")
	hooked := createE2EIssue(t, bd, "hooked work")
	touched := createE2EIssue(t, bd, "touched last")

	mustRun(t, bd, "update", current, "--status", "in_progress", "--assignee", "worker", "--json")
	// Touch another issue AFTER claiming, so last-touched is not the current
	// issue: the old swallowed refusal answered with exactly this one.
	mustRun(t, bd, "update", touched, "--priority", "1", "--json")

	showCurrent := func() string {
		t.Helper()
		r := mustRun(t, bd, "show", "--current", "--json")
		return firstJSONField(t, r.stdout, "id")
	}
	if got := showCurrent(); got != current {
		t.Fatalf("bd show --current = %s, want the in-progress %s (last touched was %s)", got, current, touched)
	}

	// Nothing in progress: the hooked issue assigned to the actor is current.
	mustRun(t, bd, "update", current, "--status", "open", "--assignee", "", "--json")
	mustRun(t, bd, "update", hooked, "--status", "hooked", "--assignee", "worker", "--json")
	mustRun(t, bd, "update", touched, "--priority", "2", "--json")
	if got := showCurrent(); got != hooked {
		t.Fatalf("bd show --current = %s, want the hooked %s (last touched was %s)", got, hooked, touched)
	}

	// Neither: last-touched is the answer, and only now.
	mustRun(t, bd, "update", hooked, "--status", "open", "--assignee", "", "--json")
	mustRun(t, bd, "update", touched, "--priority", "3", "--json")
	if got := showCurrent(); got != touched {
		t.Fatalf("bd show --current = %s, want the last-touched %s when nothing is in progress or hooked", got, touched)
	}
}
