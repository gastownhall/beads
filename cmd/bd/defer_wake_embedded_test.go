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
	return wakeReadyIDsWithEnv(t, bd, dir, nil, args...)
}

// wakeReadyIDsWithEnv is wakeReadyIDs with extra environment variables for
// this one invocation.
func wakeReadyIDsWithEnv(t *testing.T, bd, dir string, extraEnv []string, args ...string) map[string]bool {
	t.Helper()
	fullArgs := append([]string{"ready", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), extraEnv...)
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

// embeddedDoltHistory is a workspace's Dolt history at one moment: the HEAD
// commit hash, the number of commits, and HEAD's message.
type embeddedDoltHistory struct {
	head    string
	count   int
	message string
}

// readEmbeddedDoltHistory opens the workspace's embedded database directly and
// reads its history. The handle is closed before returning, so a bd
// subprocess run afterwards never shares the data directory with it.
func readEmbeddedDoltHistory(t *testing.T, beadsDir string) embeddedDoltHistory {
	t.Helper()
	database := ""
	if cfg, _ := configfile.Load(beadsDir); cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("closing OpenSQL: %v", err)
		}
	}()
	var h embeddedDoltHistory
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&h.count); err != nil {
		t.Fatalf("counting dolt_log: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT commit_hash, message FROM dolt_log WHERE commit_hash = HASHOF('HEAD')").Scan(&h.head, &h.message); err != nil {
		t.Fatalf("reading HEAD from dolt_log: %v", err)
	}
	return h
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
	// GH#7634: the marker lives outside the workspace and reaches only the
	// invocations given frozenEnv, so every other subtest here runs unfrozen.
	marker := writeFreezeMarker(t, t.TempDir(), "migrator", "dolt v2 migration")
	frozenEnv := []string{migration.EnvFreezeFile + "=" + marker}

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

	// GH#7634: a migration freeze lets bd ready through as a classified read,
	// but the defer-wake sweep is a write, so a frozen ready must answer from
	// current state and leave both the issue and the Dolt history untouched.
	t.Run("frozen_ready_does_not_wake", func(t *testing.T) {
		control := bdCreate(t, bd, dir, "Open control under a freeze", "--type", "task")
		issue := bdCreate(t, bd, dir, "Expired snooze under a freeze", "--type", "task")
		bdDefer(t, bd, dir, issue.ID, "--until", "2020-01-01")
		wisp := bdCreate(t, bd, dir, "Expired wisp snooze under a freeze", "--type", "task", "--ephemeral", "--wisp-type", "heartbeat")
		bdDefer(t, bd, dir, wisp.ID, "--until", "2020-01-01")
		status, deferUntil := showDeferState(t, bd, dir, issue.ID)
		if status != "deferred" || deferUntil == nil {
			t.Fatalf("precondition: expected a dated deferred issue, got status=%q defer_until=%v", status, deferUntil)
		}
		_, wispDeferUntil := showDeferState(t, bd, dir, wisp.ID)

		before := readEmbeddedDoltHistory(t, beadsDir)
		ids := wakeReadyIDsWithEnv(t, bd, dir, frozenEnv)
		after := readEmbeddedDoltHistory(t, beadsDir)

		if !ids[control.ID] {
			t.Errorf("open control %s missing from frozen bd ready: the read must still answer", control.ID)
		}
		if ids[issue.ID] {
			t.Errorf("expired defer %s listed by frozen bd ready: the wake sweep wrote past the freeze", issue.ID)
		}
		if after.head != before.head || after.count != before.count {
			t.Errorf("frozen bd ready moved Dolt history: HEAD %s -> %s, commits %d -> %d (HEAD message %q); a frozen read must not commit",
				before.head, after.head, before.count, after.count, after.message)
		}
		assertDeferUnchanged(t, bd, dir, issue.ID, deferUntil)
		// Wisp wakes mint no version commit, so only their state can show
		// the sweep ran.
		assertDeferUnchanged(t, bd, dir, wisp.ID, wispDeferUntil)
	})

	// GH#7634: the freeze only postpones the sweep. Once the marker is gone,
	// the next bd ready wakes what the frozen read left deferred and records
	// the normal wake commit.
	t.Run("thawed_ready_wakes_after_freeze", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Expired snooze across a thaw", "--type", "task")
		bdDefer(t, bd, dir, issue.ID, "--until", "2020-01-01")
		_ = wakeReadyIDsWithEnv(t, bd, dir, frozenEnv)

		thawed := readEmbeddedDoltHistory(t, beadsDir)
		ids := wakeReadyIDs(t, bd, dir)
		after := readEmbeddedDoltHistory(t, beadsDir)

		if !ids[issue.ID] {
			t.Errorf("expected %s in bd ready once the freeze lifted", issue.ID)
		}
		status, deferUntil := showDeferState(t, bd, dir, issue.ID)
		if status != "open" {
			t.Errorf("expected status=open after the thawed wake, got %q", status)
		}
		if deferUntil != nil {
			t.Errorf("expected defer_until cleared after the thawed wake, got %v", deferUntil)
		}
		if after.count != thawed.count+1 || after.head == thawed.head {
			t.Errorf("thawed bd ready: commits %d -> %d, HEAD %s -> %s; want exactly one new wake commit",
				thawed.count, after.count, thawed.head, after.head)
		}
		if !strings.HasPrefix(after.message, "bd: wake ") || !strings.HasSuffix(after.message, " expired defer(s)") {
			t.Errorf("HEAD message after the thawed wake = %q, want the normal wake commit (%q)",
				after.message, "bd: wake N expired defer(s)")
		}
	})
}
