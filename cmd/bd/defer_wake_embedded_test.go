//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
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
	return bdReadIDsWithEnv(t, bd, dir, nil, append([]string{"ready", "--json"}, args...)...)
}

// bdReadIDsWithEnv runs a bd read that prints a JSON array of issues (bd ready
// --json, bd list --json) with extraEnv layered over bdEnv, and returns the
// listed ids.
func bdReadIDsWithEnv(t *testing.T, bd, dir string, extraEnv []string, args ...string) map[string]bool {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), extraEnv...)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.Index(s, "[")
	if start < 0 {
		return map[string]bool{}
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &arr); err != nil {
		t.Fatalf("parse %s JSON: %v\n%s", args[0], err, s)
	}
	ids := map[string]bool{}
	for _, row := range arr {
		if id, ok := row["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

// deferWakeFreezeEnv returns the environment entries that freeze (frozen) or
// thaw a bd invocation through the authoritative BD_MIGRATION_FREEZE_FILE
// override, so a freeze never leaks into another invocation in the same
// workspace. The marker lives outside the workspace and is created once.
func deferWakeFreezeEnv(t *testing.T, markerDir string, frozen bool) []string {
	t.Helper()
	marker := filepath.Join(markerDir, migration.FileName)
	if !frozen {
		return []string{migration.EnvFreezeFile + "=" + filepath.Join(markerDir, "no-such-freeze-marker")}
	}
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		if err := os.WriteFile(marker, []byte("operator: defer-wake-test\n"), 0o644); err != nil {
			t.Fatalf("write freeze marker: %v", err)
		}
	}
	return []string{migration.EnvFreezeFile + "=" + marker}
}

// embeddedDeferWakeMessagesSince returns the workspace's Dolt HEAD and the
// messages of the commits HEAD has that since does not (none when since is
// empty), read without writing.
func embeddedDeferWakeMessagesSince(t *testing.T, beadsDir, since string) (head string, messages []string) {
	t.Helper()
	database := ""
	if cfg, _ := configfile.Load(beadsDir); cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	if err := db.QueryRowContext(t.Context(), "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if since == "" {
		return head, nil
	}
	rows, err := db.QueryContext(t.Context(), "SELECT message FROM DOLT_LOG('HEAD', '--not', ?)", since)
	if err != nil {
		t.Fatalf("read commits since %s: %v", since, err)
	}
	defer rows.Close()
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			t.Fatalf("scan commit message: %v", err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read commits since %s: %v", since, err)
	}
	return head, messages
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
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dw")
	markerDir := t.TempDir()

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

	// A MIGRATION-FREEZE makes the workspace read-only for the duration of a
	// migration: a ready read still answers, but the sweep must not wake
	// anything or mint a commit (#7634).
	t.Run("frozen_ready_leaves_expired_defer_asleep", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Frozen snooze (ready)", "--type", "task")
		bdDefer(t, bd, dir, issue.ID, "--until", "2020-01-01")
		before, _ := embeddedDeferWakeMessagesSince(t, beadsDir, "")

		ids := bdReadIDsWithEnv(t, bd, dir, deferWakeFreezeEnv(t, markerDir, true), "ready", "--json")
		if ids[issue.ID] {
			t.Errorf("frozen bd ready listed %s: the sweep woke it during a freeze", issue.ID)
		}
		if after, commits := embeddedDeferWakeMessagesSince(t, beadsDir, before); after != before {
			t.Errorf("frozen bd ready moved HEAD %s -> %s (commits %q)", before, after, commits)
		}
		status, deferUntil := showDeferState(t, bd, dir, issue.ID)
		if status != "deferred" || deferUntil == nil {
			t.Errorf("frozen bd ready changed %s to status=%q defer_until=%v, want deferred with its date", issue.ID, status, deferUntil)
		}
	})

	t.Run("frozen_list_ready_leaves_expired_defer_asleep", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Frozen snooze (list --ready)", "--type", "task")
		bdDefer(t, bd, dir, issue.ID, "--until", "2020-01-01")
		before, _ := embeddedDeferWakeMessagesSince(t, beadsDir, "")

		ids := bdReadIDsWithEnv(t, bd, dir, deferWakeFreezeEnv(t, markerDir, true), "list", "--ready", "--json")
		if ids[issue.ID] {
			t.Errorf("frozen bd list --ready listed %s: the sweep woke it during a freeze", issue.ID)
		}
		if after, commits := embeddedDeferWakeMessagesSince(t, beadsDir, before); after != before {
			t.Errorf("frozen bd list --ready moved HEAD %s -> %s (commits %q)", before, after, commits)
		}
		status, deferUntil := showDeferState(t, bd, dir, issue.ID)
		if status != "deferred" || deferUntil == nil {
			t.Errorf("frozen bd list --ready changed %s to status=%q defer_until=%v, want deferred with its date", issue.ID, status, deferUntil)
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
