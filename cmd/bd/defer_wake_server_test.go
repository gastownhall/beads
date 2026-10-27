//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/types"
)

// TestServerModeDeferAutoWake documents the same defer/wake contract as
// TestEmbeddedDeferAutoWake (defer_wake_embedded_test.go) and
// TestProxiedServerDeferAutoWake (defer_wake_proxied_integration_test.go), but
// against a real dolt sql-server (be-vbhpf): a DATED defer is a snooze, and
// once defer_until passes, the next ready-front read on `bd ready` or
// `bd list --ready` must return the issue to open. In server mode this sweep
// (DoltStore.wakeExpiredDefers) is silently skipped today because the store is
// opened read-only purely from command classification (bd ready/bd list are
// on the read-only allowlist for the CURRENT project, GH#804) — indistinguishable,
// before this fix, from a strict --readonly or foreign-project open that must
// never sweep.
func TestServerModeDeferAutoWake(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "dws")

	t.Run("expired_dated_defer_wakes_on_ready", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Expired snooze (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		status, _ := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Fatalf("precondition: expected status=deferred, got %q", status)
		}

		ids := wakeReadyIDs(t, bd, p.dir)
		if !ids[issue.ID] {
			t.Errorf("expected %s in bd ready after its defer date passed (server mode)", issue.ID)
		}
		status, deferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "open" {
			t.Errorf("expected status=open after wake, got %q", status)
		}
		if deferUntil != nil {
			t.Errorf("expected defer_until cleared after wake (undefer's shape), got %v", deferUntil)
		}
	})

	t.Run("expired_dated_defer_wakes_on_list_ready", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Expired snooze via list (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")

		issues := bdListJSON(t, bd, p.dir, "--ready")
		var found *types.IssueWithCounts
		for _, iss := range issues {
			if iss.ID == issue.ID {
				found = iss
				break
			}
		}
		if found == nil {
			t.Fatalf("expected %s in bd list --ready after its defer date passed (server mode)", issue.ID)
		}
		if found.Status != types.StatusOpen {
			t.Errorf("expected status=open in bd list --ready, got %q", found.Status)
		}
		if found.DeferUntil != nil {
			t.Errorf("expected defer_until cleared after wake, got %v", found.DeferUntil)
		}
	})

	t.Run("dateless_defer_never_wakes", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Indefinite icebox (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID)

		ids := wakeReadyIDs(t, bd, p.dir)
		if ids[issue.ID] {
			t.Errorf("dateless defer %s must not appear in bd ready", issue.ID)
		}
		status, _ := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Errorf("dateless defer must stay deferred, got %q", status)
		}
	})

	t.Run("future_dated_defer_stays_hidden", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Future snooze (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "+8760h")

		ids := wakeReadyIDs(t, bd, p.dir)
		if ids[issue.ID] {
			t.Errorf("future defer %s must not appear in bd ready", issue.ID)
		}
		status, _ := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Errorf("future defer must stay deferred, got %q", status)
		}
	})

	t.Run("expired_dated_defer_stays_hidden_under_strict_readonly", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Expired snooze under strict --readonly (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		status, _ := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Fatalf("precondition: expected status=deferred, got %q", status)
		}

		ids := wakeReadyIDs(t, bd, p.dir, "--readonly")
		if ids[issue.ID] {
			t.Errorf("expired defer %s must not appear in bd --readonly ready — strict --readonly must never sweep", issue.ID)
		}
		status, _ = showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Errorf("expired defer must stay deferred under strict --readonly, got %q", status)
		}
	})

	// A migration freeze makes the open a classified read too
	// (classifiedReadOpen's frozen term at the main.go call site), so the
	// sweep — a write — must not run while the marker is up. The predicate is
	// unit-tested on its own; this pins the wiring: dropping the frozen term
	// from the call site reddens only this case.
	t.Run("frozen_ready_does_not_wake", func(t *testing.T) {
		issue := bdCreate(t, bd, p.dir, "Expired snooze under migration freeze (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		status, _ := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Fatalf("precondition: expected status=deferred, got %q", status)
		}

		// The marker lives outside the project and is reached only through
		// the env override, so no other bd call in this test sees it.
		marker := writeFreezeMarker(t, t.TempDir(), "migrator", "dolt v2 migration")
		frozenEnv := append(bdEnv(p.dir), migration.EnvFreezeFile+"="+marker)

		ids := frozenRunJSON(t, bd, p.dir, frozenEnv, "ready", "--json")
		if ids[issue.ID] != nil {
			t.Errorf("expired defer %s must not appear in bd ready under a migration freeze — a frozen read must never sweep", issue.ID)
		}
		row := frozenRunJSON(t, bd, p.dir, frozenEnv, "show", issue.ID, "--json")[issue.ID]
		if row == nil {
			t.Fatalf("bd show %s under the freeze returned no row", issue.ID)
		}
		if row["status"] != "deferred" {
			t.Errorf("expired defer must stay deferred under a migration freeze, got %v", row["status"])
		}
		if row["defer_until"] == nil {
			t.Errorf("defer_until must survive a frozen bd ready, got nil")
		}

		// Thaw: the same project now wakes the issue, so the frozen
		// assertions above are not vacuous.
		if err := os.Remove(marker); err != nil {
			t.Fatalf("removing freeze marker: %v", err)
		}
		if !wakeReadyIDs(t, bd, p.dir)[issue.ID] {
			t.Errorf("expected %s in bd ready once the freeze lifted", issue.ID)
		}
		status, _ = showDeferState(t, bd, p.dir, issue.ID)
		if status != "open" {
			t.Errorf("expected status=open after wake once the freeze lifted, got %q", status)
		}
	})
}

// frozenRunJSON runs bd with env, requires exit 0, and returns the rows of its
// JSON array output keyed by id — enough for both bd ready --json and
// bd show --json, which the env-less helpers above cannot run under a freeze.
func frozenRunJSON(t *testing.T, bd, dir string, env []string, args ...string) map[string]map[string]interface{} {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd %s failed under freeze: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	rows := map[string]map[string]interface{}{}
	start := strings.Index(s, "[")
	if start < 0 {
		return rows
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &arr); err != nil {
		t.Fatalf("parse bd %s JSON: %v\n%s", strings.Join(args, " "), err, s)
	}
	for _, row := range arr {
		if id, ok := row["id"].(string); ok {
			rows[id] = row
		}
	}
	return rows
}
