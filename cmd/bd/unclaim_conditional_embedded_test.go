//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestUnclaimIfAssigneeCLI drives `bd unclaim --if-assignee` end-to-end against
// the embedded Dolt backend: the conditional release must be a compare-and-swap,
// not a read-then-clobber. A stale expectation exits nonzero, names the current
// holder, and leaves the claim untouched; the matching expectation releases the
// claim; and releasing an already-released issue is a distinct failure, not a
// silent success (the exactly-once property a release-if-current supervisor
// depends on).
func TestUnclaimIfAssigneeCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ur")

	issue := bdCreate(t, bd, dir, "Conditional release", "--type", "task")
	bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")

	// Stale expectation: distinct failure that names the current holder, claim intact.
	out := bdUnclaimFail(t, bd, dir, issue.ID, "--if-assignee", "bob")
	if !strings.Contains(out, "alice") {
		t.Errorf("mismatch error should name the current holder alice, got:\n%s", out)
	}
	got := bdShow(t, bd, dir, issue.ID)
	if got.Assignee != "alice" {
		t.Errorf("stale --if-assignee clobbered the claim: assignee = %q, want alice", got.Assignee)
	}
	if got.Status != types.StatusInProgress {
		t.Errorf("stale --if-assignee changed status to %q, want in_progress", got.Status)
	}

	// Matching expectation: releases the claim.
	bdUnclaim(t, bd, dir, issue.ID, "--if-assignee", "alice")
	got = bdShow(t, bd, dir, issue.ID)
	if got.Assignee != "" {
		t.Errorf("after matching --if-assignee: assignee = %q, want empty", got.Assignee)
	}
	if got.Status != types.StatusOpen {
		t.Errorf("after matching --if-assignee: status = %q, want open", got.Status)
	}

	// Releasing an already-released issue with --if-assignee is also a distinct
	// failure (exactly-once), not a silent success.
	_ = bdUnclaimFail(t, bd, dir, issue.ID, "--if-assignee", "alice")
}

// TestUnclaimIfAssigneeFlagValidation drives the CLI guards that keep the
// conditional-release contract unambiguous. A conditional unclaim is selected by
// the *presence* of --if-assignee, so an explicitly empty --if-assignee "" (an
// unset variable that expanded into the flag) must be rejected rather than
// silently downgraded to an unconditional, --force-capable release; and --force
// cannot be combined with --if-assignee. Both cases must exit nonzero and leave
// the claim untouched. Each subtest runs as the holder so the pre-fix behavior
// (fall through to UnclaimIssue / let one flag win) would actually have released
// the claim, making these true regression guards.
func TestUnclaimIfAssigneeFlagValidation(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "uv")

	t.Run("empty_if_assignee_rejected", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Empty expectation", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")

		// As the holder (alice), a pre-fix empty --if-assignee "" would have
		// fallen through to an unconditional owner release and cleared the claim.
		out := bdUnclaimFail(t, bd, dir, issue.ID, "--if-assignee", "", "--actor", "alice")
		if !strings.Contains(out, "if-assignee requires a non-empty assignee") {
			t.Errorf("empty --if-assignee should be rejected with a clear error, got:\n%s", out)
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "alice" {
			t.Errorf("empty --if-assignee released the claim: assignee = %q, want alice", got.Assignee)
		}
		if got.Status != types.StatusInProgress {
			t.Errorf("empty --if-assignee changed status to %q, want in_progress", got.Status)
		}
	})

	t.Run("force_and_if_assignee_mutually_exclusive", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Conflicting flags", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")

		// --force + a matching --if-assignee would, pre-fix, take the CAS path and
		// release the claim. The combination must instead be rejected outright.
		out := bdUnclaimFail(t, bd, dir, issue.ID, "--force", "--if-assignee", "alice")
		if !strings.Contains(out, "force") || !strings.Contains(out, "if-assignee") {
			t.Errorf("--force with --if-assignee should be rejected as mutually exclusive, got:\n%s", out)
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "alice" {
			t.Errorf("--force --if-assignee released the claim: assignee = %q, want alice", got.Assignee)
		}
		if got.Status != types.StatusInProgress {
			t.Errorf("--force --if-assignee changed status to %q, want in_progress", got.Status)
		}
	})
}
