//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// showDeferState returns (status, defer_until) for an issue via bd show --json.
func showDeferState(t *testing.T, bd, dir, id string) (string, interface{}) {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\nstdout:\n%s\nstderr:\n%s", id, err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		t.Fatalf("no JSON in show output: %s", s)
	}
	var m map[string]interface{}
	if s[start] == '[' {
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &arr); err != nil {
			t.Fatalf("parse show JSON array: %v\n%s", err, s)
		}
		if len(arr) == 0 {
			t.Fatalf("empty JSON array in show output")
		}
		m = arr[0]
	} else {
		if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
			t.Fatalf("parse show JSON: %v\n%s", err, s)
		}
	}
	status, _ := m["status"].(string)
	return status, m["defer_until"]
}

// wakeReadyIDs runs bd ready --json (plus extra args) and returns the listed ids.
func wakeReadyIDs(t *testing.T, bd, dir string, args ...string) map[string]bool {
	t.Helper()
	fullArgs := append([]string{"ready", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd ready --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.Index(s, "[")
	if start < 0 {
		return map[string]bool{}
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &arr); err != nil {
		t.Fatalf("parse ready JSON: %v\n%s", err, s)
	}
	ids := map[string]bool{}
	for _, row := range arr {
		if id, ok := row["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

// TestEmbeddedDeferAutoWake documents the defer contract: a DATED defer is a
// snooze — once defer_until passes, the next ready-front read returns the
// issue to open (status open, defer_until cleared, same shape bd undefer
// writes). A DATELESS defer is the indefinite icebox and never auto-wakes.
func TestEmbeddedDeferAutoWake(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dw")

	t.Run("expired_dated_defer_wakes_on_ready", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Expired snooze", "--type", "task")
		// defer.go warns about a past date but proceeds: status=deferred with
		// an already-expired defer_until — exactly the tomb state a defer that
		// expired while nobody was looking leaves behind.
		bdDefer(t, bd, dir, issue.ID, "--until", "2020-01-01")
		status, _ := showDeferState(t, bd, dir, issue.ID)
		if status != "deferred" {
			t.Fatalf("precondition: expected status=deferred, got %q", status)
		}

		ids := wakeReadyIDs(t, bd, dir)
		if !ids[issue.ID] {
			t.Errorf("expected %s in bd ready after its defer date passed", issue.ID)
		}
		status, deferUntil := showDeferState(t, bd, dir, issue.ID)
		if status != "open" {
			t.Errorf("expected status=open after wake, got %q", status)
		}
		if deferUntil != nil {
			t.Errorf("expected defer_until cleared after wake (undefer's shape), got %v", deferUntil)
		}
	})

	t.Run("dateless_defer_never_wakes", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Indefinite icebox", "--type", "task")
		bdDefer(t, bd, dir, issue.ID)

		ids := wakeReadyIDs(t, bd, dir)
		if ids[issue.ID] {
			t.Errorf("dateless defer %s must not appear in bd ready", issue.ID)
		}
		status, _ := showDeferState(t, bd, dir, issue.ID)
		if status != "deferred" {
			t.Errorf("dateless defer must stay deferred, got %q", status)
		}
	})

	t.Run("future_dated_defer_stays_hidden", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Future snooze", "--type", "task")
		bdDefer(t, bd, dir, issue.ID, "--until", "+8760h")

		ids := wakeReadyIDs(t, bd, dir)
		if ids[issue.ID] {
			t.Errorf("future defer %s must not appear in bd ready", issue.ID)
		}
		status, _ := showDeferState(t, bd, dir, issue.ID)
		if status != "deferred" {
			t.Errorf("future defer must stay deferred, got %q", status)
		}
	})

	t.Run("expired_dated_defer_wakes_wisp", func(t *testing.T) {
		// Wisps ride the same defer/wake contract through dolt_ignored tables,
		// which take a different persistence path (SQL commit, no version
		// commit) — the seam where the uow stack once rolled wisp wakes back.
		issue := bdCreate(t, bd, dir, "Wisp snooze", "--type", "task", "--ephemeral", "--wisp-type", "heartbeat")
		bdDefer(t, bd, dir, issue.ID, "--until", "2020-01-01")
		status, _ := showDeferState(t, bd, dir, issue.ID)
		if status != "deferred" {
			t.Fatalf("precondition: expected status=deferred, got %q", status)
		}

		_ = wakeReadyIDs(t, bd, dir) // trigger the sweep

		status, deferUntil := showDeferState(t, bd, dir, issue.ID)
		if status != "open" {
			t.Errorf("expected wisp status=open after wake, got %q", status)
		}
		if deferUntil != nil {
			t.Errorf("expected wisp defer_until cleared after wake, got %v", deferUntil)
		}
	})

	t.Run("expired_dated_defer_claimable", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Claimable after snooze", "--type", "task", "--labels", "wake-claim-test")
		bdDefer(t, bd, dir, issue.ID, "--until", "2020-01-01")

		// The claim path wakes before selecting, so the freshly-expired bead
		// is claimable without any listing having run first.
		cmd := exec.Command(bd, "ready", "--claim", "--json", "-l", "wake-claim-test")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready --claim failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), issue.ID) {
			t.Errorf("expected claim to win %s, got:\n%s", issue.ID, stdout.String())
		}
		status, _ := showDeferState(t, bd, dir, issue.ID)
		if status != "in_progress" {
			t.Errorf("expected status=in_progress after claim, got %q", status)
		}
	})
}
