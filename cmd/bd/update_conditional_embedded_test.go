//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateIfGuardsCLI drives the bd-wsqvw conditional-update guards
// (`bd update --if-assignee/--if-status`) end-to-end against the embedded Dolt
// backend. The guarded update must be a compare-and-set, not a
// read-then-clobber: a stale guard exits nonzero naming the actual state and
// writes nothing; a matching guard applies; and `--if-assignee ""` is a real
// "expected unassigned" guard, not "no guard". Covers the two wheelhouse
// transitions no other verb expresses: reassign X→Y (park) and claim-on-behalf
// with a status guard (restore).
func TestUpdateIfGuardsCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ug")

	t.Run("reassign_only_if_still_held", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Park transition", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "worker", "--status", "in_progress")

		// Stale guard: exits nonzero, names the actual holder, writes nothing.
		out := bdUpdateFail(t, bd, dir, issue.ID, "--if-assignee", "bob", "--assignee", "mayor")
		if !strings.Contains(out, "worker") {
			t.Errorf("mismatch error should name the current holder worker, got:\n%s", out)
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "worker" {
			t.Errorf("stale --if-assignee clobbered the row: assignee = %q, want worker", got.Assignee)
		}

		// Matching guard: the park reassign applies.
		bdUpdate(t, bd, dir, issue.ID, "--if-assignee", "worker", "--assignee", "mayor")
		got = bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "mayor" {
			t.Errorf("after matching --if-assignee: assignee = %q, want mayor", got.Assignee)
		}

		// The guard is exactly-once: re-running the same conditional reassign
		// now fails (worker no longer holds it), it does not silently succeed.
		_ = bdUpdateFail(t, bd, dir, issue.ID, "--if-assignee", "worker", "--assignee", "mayor")
	})

	t.Run("claim_on_behalf_only_while_open", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Restore transition", "--type", "task")

		// --if-assignee "" is a real guard: claim-on-behalf applies only while
		// unassigned and open.
		bdUpdate(t, bd, dir, issue.ID, "--if-assignee", "", "--if-status", "open",
			"--assignee", "owner", "--status", "in_progress")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "owner" || got.Status != types.StatusInProgress {
			t.Errorf("claim-on-behalf: got assignee=%q status=%q, want owner/in_progress", got.Assignee, got.Status)
		}

		// A second restore attempt must lose: the bead is no longer unassigned.
		out := bdUpdateFail(t, bd, dir, issue.ID, "--if-assignee", "", "--if-status", "open",
			"--assignee", "other", "--status", "in_progress")
		if !strings.Contains(out, "owner") {
			t.Errorf("mismatch error should name the current holder owner, got:\n%s", out)
		}
		got = bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "owner" {
			t.Errorf("lost restore clobbered the row: assignee = %q, want owner", got.Assignee)
		}
	})

	t.Run("status_guard_mismatch_is_loud", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Status guard", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress")

		out := bdUpdateFail(t, bd, dir, issue.ID, "--if-status", "open", "--priority", "1")
		if !strings.Contains(out, "in_progress") {
			t.Errorf("mismatch error should name the actual status in_progress, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Priority != issue.Priority {
			t.Errorf("refused guard changed priority to %d, want unchanged %d", got.Priority, issue.Priority)
		}
	})
}

// TestUpdateIfGuardsFlagValidation drives the CLI-surface rules that keep the
// guard contract unambiguous: guards cannot combine with --claim (which is its
// own compare-and-set), guards require a field update to ride on (label-only
// edits run outside the guarded transaction), and an --if-status typo is
// rejected up front instead of mismatching forever. Every rejection must write
// nothing.
func TestUpdateIfGuardsFlagValidation(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "uf")

	issue := bdCreate(t, bd, dir, "Flag validation", "--type", "task")

	t.Run("claim_and_guards_mutually_exclusive", func(t *testing.T) {
		out := bdUpdateFail(t, bd, dir, issue.ID, "--if-assignee", "", "--claim")
		if !strings.Contains(out, "--claim") {
			t.Errorf("expected the --claim exclusion in the error, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
			t.Errorf("rejected flag combo still claimed the issue: assignee = %q", got.Assignee)
		}
	})

	t.Run("guards_require_a_field_update", func(t *testing.T) {
		out := bdUpdateFail(t, bd, dir, issue.ID, "--if-assignee", "", "--add-label", "x")
		if !strings.Contains(out, "field update") {
			t.Errorf("expected the field-update requirement in the error, got:\n%s", out)
		}
	})

	t.Run("invalid_if_status_rejected_up_front", func(t *testing.T) {
		out := bdUpdateFail(t, bd, dir, issue.ID, "--if-status", "opne", "--priority", "1")
		if !strings.Contains(out, "opne") {
			t.Errorf("expected the invalid status named in the error, got:\n%s", out)
		}
	})
}
