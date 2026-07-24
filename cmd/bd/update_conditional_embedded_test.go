//go:build cgo

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdUpdateFailCode runs "bd update" expecting failure and returns the combined
// output plus the process exit code, so guard tests can assert the
// ExitGuardMismatch contract (13 = stale guard, 1 = everything else).
func bdUpdateFailCode(t *testing.T, bd, dir string, args ...string) (string, int) {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd update %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("bd update %s failed without an exit code: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), ee.ExitCode()
}

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

		// Stale guard: exits ExitGuardMismatch (so scripts can tell "a racer
		// won" from infra failure), names the actual holder, writes nothing.
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-assignee", "bob", "--assignee", "mayor")
		if code != ExitGuardMismatch {
			t.Errorf("stale guard exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
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
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-assignee", "", "--if-status", "open",
			"--assignee", "other", "--status", "in_progress")
		if code != ExitGuardMismatch {
			t.Errorf("lost restore exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
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

		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-status", "open", "--priority", "1")
		if code != ExitGuardMismatch {
			t.Errorf("status guard exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "in_progress") {
			t.Errorf("mismatch error should name the actual status in_progress, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Priority != issue.Priority {
			t.Errorf("refused guard changed priority to %d, want unchanged %d", got.Priority, issue.Priority)
		}
	})

	t.Run("mixed_batch_failure_exits_1_not_13", func(t *testing.T) {
		// ExitGuardMismatch means "every failure was a stale guard — a racer
		// won, don't retry". A batch that ALSO hit a non-guard failure (here a
		// nonexistent ID) must keep the conservative exit 1 so callers still
		// treat the run as retry-worthy.
		issue := bdCreate(t, bd, dir, "Mixed batch", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "worker")

		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "ug-nope99",
			"--if-assignee", "bob", "--priority", "1")
		if code != 1 {
			t.Errorf("mixed guard+lookup failure exit code = %d, want 1\n%s", code, out)
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
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-assignee", "", "--claim")
		if code != 1 {
			t.Errorf("flag-validation exit code = %d, want 1 (13 is reserved for real guard mismatches)\n%s", code, out)
		}
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
