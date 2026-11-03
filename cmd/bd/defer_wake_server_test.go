//go:build cgo

package main

import (
	"context"
	"database/sql"
	"os/exec"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/types"
)

// serverModeDoltLogCount reads the project database's history length straight
// from the shared test server. Counting through a bd invocation of its own
// would blur the measurement: any unfrozen bd run's post-command maintenance
// may commit on its own, so the counts around a frozen read must come from
// SQL the invoked binary never touches (bd-6zf0q5).
func serverModeDoltLogCount(t *testing.T, p serverModeProject) int {
	t.Helper()
	port := requireSharedProxiedServer(t)
	db, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: p.database}.String())
	if err != nil {
		t.Fatalf("connecting to the shared test server: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
		t.Fatalf("reading dolt_log in %s: %v", p.database, err)
	}
	return n
}

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

	t.Run("expired_dated_defer_stays_deferred_under_frozen_ready", func(t *testing.T) {
		// bd-6zf0q5 (AC-1): the migration freeze deliberately lets a
		// classified read-only command past the write gate, but the lazy
		// defer-wake sweep such an open enables is itself a write. A frozen
		// `bd ready` must answer, keep the workspace's other ready work
		// visible, leave the expired defer exactly as it is, and mint no
		// Dolt history for it.
		issue := bdCreate(t, bd, p.dir, "Expired snooze under a migration freeze (bd ready, server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		status, savedDeferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Fatalf("precondition: expected status=deferred, got %q", status)
		}
		control := bdCreate(t, bd, p.dir, "Open control during a migration freeze (server mode)", "--type", "task")

		marker := writeFreezeMarker(t, t.TempDir(), "migrator", "bd-6zf0q5 frozen bd ready")
		freezeEnv := []string{migration.EnvFreezeFile + "=" + marker}

		before := serverModeDoltLogCount(t, p)
		ids := wakeReadyIDsWithEnv(t, bd, p.dir, freezeEnv)
		after := serverModeDoltLogCount(t, p)
		if after != before {
			t.Errorf("frozen bd ready minted Dolt history (%d -> %d commits): the defer-wake sweep wrote past the freeze marker", before, after)
		}
		if ids[issue.ID] {
			t.Errorf("expired defer %s woke into bd ready under a migration freeze", issue.ID)
		}
		if !ids[control.ID] {
			t.Errorf("open control issue %s must stay visible in bd ready during a freeze", control.ID)
		}
		status, deferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Errorf("expired defer must stay deferred under a migration freeze, got status=%q (server-mode bd ready woke it)", status)
		}
		if deferUntil != savedDeferUntil {
			t.Errorf("defer_until must stay %v during a freeze, got %v", savedDeferUntil, deferUntil)
		}
	})

	t.Run("expired_dated_defer_stays_deferred_under_frozen_list_ready", func(t *testing.T) {
		// bd-6zf0q5 (AC-2): the same freeze suppression for bd list --ready,
		// the other ready-front read whose store open carries the
		// classified-read permission.
		issue := bdCreate(t, bd, p.dir, "Expired snooze under a migration freeze (bd list --ready, server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
		status, savedDeferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Fatalf("precondition: expected status=deferred, got %q", status)
		}
		control := bdCreate(t, bd, p.dir, "Open list control during a migration freeze (server mode)", "--type", "task")

		marker := writeFreezeMarker(t, t.TempDir(), "migrator", "bd-6zf0q5 frozen bd list --ready")
		freezeEnv := []string{migration.EnvFreezeFile + "=" + marker}

		before := serverModeDoltLogCount(t, p)
		issues := bdListJSONWithEnv(t, bd, p.dir, freezeEnv, "--ready")
		after := serverModeDoltLogCount(t, p)
		if after != before {
			t.Errorf("frozen bd list --ready minted Dolt history (%d -> %d commits): the defer-wake sweep wrote past the freeze marker", before, after)
		}
		for _, iss := range issues {
			if iss.ID == issue.ID {
				t.Errorf("expired defer %s woke into bd list --ready under a migration freeze", issue.ID)
			}
		}
		var controlVisible bool
		for _, iss := range issues {
			if iss.ID == control.ID {
				controlVisible = true
			}
		}
		if !controlVisible {
			t.Errorf("open control issue %s must stay visible in bd list --ready during a freeze", control.ID)
		}
		status, deferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "deferred" {
			t.Errorf("expired defer must stay deferred under a migration freeze, got status=%q (server-mode bd list --ready woke it)", status)
		}
		if deferUntil != savedDeferUntil {
			t.Errorf("defer_until must stay %v during a freeze, got %v", savedDeferUntil, deferUntil)
		}
	})

	t.Run("suppressed_defer_wakes_on_next_unfrozen_ready", func(t *testing.T) {
		// bd-6zf0q5 (AC-3): suppression is per-invocation, not a lasting
		// state change: once the freeze is gone, the next ready read wakes
		// the expired defer exactly as before.
		issue := bdCreate(t, bd, p.dir, "Suppressed snooze wakes after the freeze (server mode)", "--type", "task")
		bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")

		marker := writeFreezeMarker(t, t.TempDir(), "migrator", "bd-6zf0q5 thaw wake")
		freezeEnv := []string{migration.EnvFreezeFile + "=" + marker}

		ids := wakeReadyIDsWithEnv(t, bd, p.dir, freezeEnv)
		if ids[issue.ID] {
			t.Fatalf("expired defer %s must stay suppressed during the frozen read", issue.ID)
		}
		if status, _ := showDeferState(t, bd, p.dir, issue.ID); status != "deferred" {
			t.Fatalf("precondition: expected the frozen read to leave status=deferred, got %q", status)
		}

		ids = wakeReadyIDs(t, bd, p.dir)
		if !ids[issue.ID] {
			t.Errorf("expected %s in bd ready on the first read after the freeze lifted", issue.ID)
		}
		status, deferUntil := showDeferState(t, bd, p.dir, issue.ID)
		if status != "open" {
			t.Errorf("expected status=open after the post-freeze wake, got %q", status)
		}
		if deferUntil != nil {
			t.Errorf("expected defer_until cleared after the post-freeze wake (undefer's shape), got %v", deferUntil)
		}
	})
}

// TestServerModeDeferWakeSkippedUnderMigrationFreeze (bd-6zf0q5, follow-up to
// be-vbhpf/#6424): a migration freeze lets read-only commands such as
// `bd ready` past the write gate, but the lazy defer-wake sweep the
// classified-read open enables is a write. Under an active freeze the read
// must still answer, and the expired defer must stay deferred.
func TestServerModeDeferWakeSkippedUnderMigrationFreeze(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "dwf")

	issue := bdCreate(t, bd, p.dir, "Expired snooze under a migration freeze (server mode)", "--type", "task")
	bdDefer(t, bd, p.dir, issue.ID, "--until", "2020-01-01")
	if status, _ := showDeferState(t, bd, p.dir, issue.ID); status != "deferred" {
		t.Fatalf("precondition: expected status=deferred, got %q", status)
	}

	marker := writeFreezeMarker(t, t.TempDir(), "migrator", "bd-6zf0q5 repro")
	cmd := exec.Command(bd, "ready", "--json")
	cmd.Dir = p.dir
	cmd.Env = append(bdEnv(p.dir), migration.EnvFreezeFile+"="+marker)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd ready must still run under a freeze: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), `"`+issue.ID+`"`) {
		t.Errorf("expired defer %s appeared in bd ready under a migration freeze: the wake sweep wrote past the marker", issue.ID)
	}

	if status, _ := showDeferState(t, bd, p.dir, issue.ID); status != "deferred" {
		t.Errorf("expired defer must stay deferred under a migration freeze, got status=%q (server-mode bd ready woke it)", status)
	}
}
