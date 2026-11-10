//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// deferFreezeDatabase is the prefix/database the freeze tests init with.
const deferFreezeDatabase = "dwf"

// deferFreezeProbe reads the embedded store's HEAD and the number of defer-wake
// sweep commits in-process. Called between subprocess runs, when no bd process
// holds the store: HEAD moves only when a subprocess committed, and a sweep
// commit is the only thing that names itself WakeDefersCommitMessage.
func deferFreezeProbe(t *testing.T, beadsDir string) (head string, wakeCommits int) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), deferFreezeDatabase, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	if err := db.QueryRowContext(t.Context(), "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
		t.Fatalf("HASHOF('HEAD'): %v", err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log WHERE message = ?",
		issueops.WakeDefersCommitMessage(1)).Scan(&wakeCommits); err != nil {
		t.Fatalf("count wake commits: %v", err)
	}
	return head, wakeCommits
}

// TestEmbeddedDeferFreezeSkipsSweepOnReady pins #7634's embedded arm: while a
// MIGRATION-FREEZE marker holds, `bd ready` still answers (reads keep working)
// but the lazy defer-wake sweep stands down — no wake commit, no state change.
// The expired defer wakes on the first ready read after the thaw (AC-3).
func TestEmbeddedDeferFreezeSkipsSweepOnReady(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", deferFreezeDatabase)

	issue := bdCreate(t, bd, dir, "Frozen snooze", "--type", "task")
	bdDefer(t, bd, dir, issue.ID, "--until", "2020-01-01")
	if status, _ := showDeferState(t, bd, dir, issue.ID); status != "deferred" {
		t.Fatalf("precondition: expected status=deferred, got %q", status)
	}

	// Capture HEAD AFTER the marker is in place: unfrozen read commands may
	// commit tip metadata and move HEAD, so only a HEAD read under freeze
	// isolates the frozen read's own (must-be-absent) commit.
	marker := writeFreezeMarker(t, dir, "migrator", "dolt v2 migration")
	headBefore, wakesBefore := deferFreezeProbe(t, beadsDir)

	// bd ready under the freeze must still answer as a read.
	ids := wakeReadyIDs(t, bd, dir)
	if ids[issue.ID] {
		t.Errorf("frozen bd ready surfaced expired defer %s", issue.ID)
	}
	headAfter, wakesAfter := deferFreezeProbe(t, beadsDir)
	if headAfter != headBefore {
		t.Errorf("frozen bd ready moved HEAD (%s -> %s): the defer-wake sweep must stand down during a migration freeze", headBefore, headAfter)
	}
	if wakesAfter != wakesBefore {
		t.Errorf("frozen bd ready committed a defer-wake sweep (%d -> %d wake commits): the sweep must stand down during a migration freeze", wakesBefore, wakesAfter)
	}
	status, deferUntil := showDeferState(t, bd, dir, issue.ID)
	if status != "deferred" {
		t.Errorf("frozen bd ready woke %s: status = %q, want deferred", issue.ID, status)
	}
	// showDeferState returns defer_until as it appears in bd show --json:
	// still-set is non-nil. Under the freeze the sweep must not have run, so
	// the defer_until the bdDefer above wrote must still be there.
	if deferUntil == nil {
		t.Errorf("frozen bd ready cleared %s's defer_until, want untouched", issue.ID)
	}

	// Thaw: the first ready read afterwards wakes the expired defer exactly once.
	if err := os.Remove(marker); err != nil {
		t.Fatalf("remove freeze marker: %v", err)
	}
	ids = wakeReadyIDs(t, bd, dir)
	if !ids[issue.ID] {
		t.Errorf("thawed bd ready must surface expired defer %s", issue.ID)
	}
	_, wakesThawed := deferFreezeProbe(t, beadsDir)
	if wakesThawed != wakesBefore+1 {
		t.Errorf("thawed bd ready committed %d wake commits, want exactly 1", wakesThawed-wakesBefore)
	}
	status, deferUntil = showDeferState(t, bd, dir, issue.ID)
	if status != "open" {
		t.Errorf("thawed bd ready left %s status = %q, want open", issue.ID, status)
	}
	if deferUntil != nil {
		t.Errorf("thawed bd ready left %s defer_until = %v, want cleared", issue.ID, deferUntil)
	}
}

// TestEmbeddedDeferFreezeSkipsSweepOnListReady pins the same stand-down on the
// `bd list --ready` route (GetReadyWorkWithCounts behind a classified read).
func TestEmbeddedDeferFreezeSkipsSweepOnListReady(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", deferFreezeDatabase)

	issue := bdCreate(t, bd, dir, "Frozen list snooze", "--type", "task")
	bdDefer(t, bd, dir, issue.ID, "--until", "2020-01-01")
	if status, _ := showDeferState(t, bd, dir, issue.ID); status != "deferred" {
		t.Fatalf("precondition: expected status=deferred, got %q", status)
	}

	writeFreezeMarker(t, dir, "migrator", "dolt v2 migration")
	headBefore, wakesBefore := deferFreezeProbe(t, beadsDir)

	for _, listed := range bdListJSON(t, bd, dir, "--ready") {
		if listed.ID == issue.ID {
			t.Errorf("frozen bd list --ready surfaced expired defer %s", issue.ID)
		}
	}
	headAfter, wakesAfter := deferFreezeProbe(t, beadsDir)
	if headAfter != headBefore {
		t.Errorf("frozen bd list --ready moved HEAD (%s -> %s): the defer-wake sweep must stand down during a migration freeze", headBefore, headAfter)
	}
	if wakesAfter != wakesBefore {
		t.Errorf("frozen bd list --ready committed a defer-wake sweep (%d -> %d wake commits): the sweep must stand down during a migration freeze", wakesBefore, wakesAfter)
	}
	if status, _ := showDeferState(t, bd, dir, issue.ID); status != "deferred" {
		t.Errorf("frozen bd list --ready woke %s, want deferred", issue.ID)
	}
}
