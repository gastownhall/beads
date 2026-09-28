//go:build cgo

package main

// Black-box regression for the LONELY stamp on `bd unclaim`:
// `--if-updated-at` WITHOUT `--if-assignee` (critic round 2, finding 1).
//
// The lonely stamp is a legal, storage-ready fence — ReleaseRequest carries
// ExpectedUpdatedAt on its own and ReleaseIssueInTx checks the generation even
// when ExpectedAssignee is nil — but both CLI routes used to choose their
// release path solely on the presence of --if-assignee, so a lonely stamp fell
// into the unconditional release: exit 0 on the very row the caller fenced on,
// inverting the flag's own documented CAS contract. These tests pin the routed
// behavior: a stale lonely stamp refuses with exit 1 (unclaim's SilentExit
// taxonomy, identical on the embedded route) and writes NOTHING — full-row
// read-back — while naming the current stamp; a matching lonely stamp releases.
//
// Harness conventions are if_updated_at_conformance_test.go's: embedded binary
// via ifupGate, fresh store per test, NO_COLOR=1, process exit codes asserted
// alongside stderr text.

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestIfUpdatedAtUnclaimLonelyStampStaleRefuses: the stale lonely stamp is a
// refusal, never a silent unconditional release. Exit 1 per the verb's
// SilentExit contract; the live claim survives byte-for-byte; the refusal
// names the sentinel, the CURRENT stamp and the expected one.
func TestIfUpdatedAtUnclaimLonelyStampStaleRefuses(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yl1")

	id, current, stale, baseline := ifupSeedClaimed(t, bd, dir, "Lonely stamp stale")

	stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id,
		"--actor", "supervisor-x", "--if-updated-at", stale)
	// Per-verb taxonomy (bead y30h.3.30.2): unclaim rides the SilentExit
	// release contract — stamp-mismatch exits 1.
	if code != 1 {
		t.Fatalf("lonely stale-stamp unclaim exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	ifupAssertUnchanged(t, bd, dir, id, baseline, "lonely stale-stamp unclaim refusal")
	combined := stdout + stderr
	if !strings.Contains(combined, "Error unclaiming") {
		t.Errorf("refusal must keep the family single-line shape, got:\n%s", combined)
	}
	if !strings.Contains(combined, "mismatch") || !strings.Contains(combined, current) || !strings.Contains(combined, stale) {
		t.Errorf("refusal must carry sentinel + current %s + expected %s, got:\n%s", current, stale, combined)
	}
	// The claim is not merely "not updated_at": the holder and status survive.
	held := bdShow(t, bd, dir, id)
	if held.Assignee != "worker-a" || held.Status != types.StatusInProgress {
		t.Fatalf("the live claim did not survive the refusal: assignee=%q status=%q, want worker-a/in_progress",
			held.Assignee, held.Status)
	}
}

// TestIfUpdatedAtUnclaimLonelyStampMatchReleases: a matching lonely stamp
// authorizes the release FOR THE HOLDER — assignee cleared, status back to
// open, generation advanced (the fence is one-shot per second).
//
// OWNERSHIP STANDS: unlike --if-assignee, whose match REPLACES the ownership
// fence (naming the holder demonstrates the view), a generation stamp alone
// names the row's state, not its owner — so the lonely stamp fences the
// release without weakening ErrNotOwner. A foreign actor holding a MATCHING
// stamp is still refused (pinned below), exactly as an unguarded foreign
// release is.
func TestIfUpdatedAtUnclaimLonelyStampMatchReleases(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yl2")

	id, current, _, _ := ifupSeedClaimed(t, bd, dir, "Lonely stamp match")
	// Cross a second boundary so the release's own write lands in a LATER
	// second than the stamp being fenced on — DATETIME(0) keeps same-second
	// writes on one generation, and this test also asserts the advance.
	waitPastStampBoundary(t)

	stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id,
		"--actor", "worker-a", "--if-updated-at", current)
	if code != 0 {
		t.Fatalf("lonely matching-stamp unclaim by the holder exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	released := bdShow(t, bd, dir, id)
	if released.Assignee != "" || released.Status != types.StatusOpen {
		t.Fatalf("match did not release: assignee=%q status=%q, want released/open",
			released.Assignee, released.Status)
	}
	if got := released.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"); got == current {
		t.Fatalf("released row still carries the fenced stamp %s; the release must advance the generation", got)
	}
}

// TestIfUpdatedAtUnclaimLonelyStampDoesNotReplaceOwnership: a MATCHING lonely
// stamp in foreign hands is ErrNotOwner, not a release — the stamp names the
// row's generation, not its owner, so it cannot stand in for --if-assignee's
// demonstrated-view authorization. Pinning this keeps the lonely stamp a
// fence rather than an accidental ownership bypass.
func TestIfUpdatedAtUnclaimLonelyStampDoesNotReplaceOwnership(t *testing.T) {
	bd := ifupGate(t)
	t.Parallel()
	dir, _, _ := bdInit(t, bd, "--prefix", "yl3")

	id, current, _, baseline := ifupSeedClaimed(t, bd, dir, "Lonely stamp ownership")

	stdout, stderr, code := ifupRun(t, bd, dir, "unclaim", id,
		"--actor", "supervisor-x", "--if-updated-at", current)
	if code != 1 {
		t.Fatalf("foreign lonely-stamp unclaim exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	ifupAssertUnchanged(t, bd, dir, id, baseline, "foreign lonely-stamp ownership refusal")
	combined := stdout + stderr
	if !strings.Contains(combined, "claimed by a different actor") {
		t.Errorf("refusal must be the ownership sentinel, got:\n%s", combined)
	}
}
