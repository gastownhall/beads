//go:build cgo

package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// bdProxiedUpdateFailCode runs a proxied "bd update" expecting failure and
// returns combined output plus the exit code, for asserting the
// ExitGuardMismatch contract on the proxied path.
func bdProxiedUpdateFailCode(t *testing.T, bd, dir string, args ...string) (string, int) {
	t.Helper()
	stdout, stderr, err := bdProxiedUpdateRaw(t, bd, dir, args...)
	if err == nil {
		t.Fatalf("bd update %s should have failed; got:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("bd update %s failed without an exit code: %v", strings.Join(args, " "), err)
	}
	return stdout + stderr, ee.ExitCode()
}

// TestProxiedServerUpdateIfGuards proves the bd-wsqvw conditional-update
// guards hold on the proxied-server path: the guards ride domain.UpdateSpec
// into ApplyUpdate, where the guard read shares the unit of work's
// transaction. A stale guard is a terminal per-issue failure (loud, non-zero,
// nothing written — the finding-#10 exit contract), a matching guard applies,
// and `--if-assignee ""` guards on unassigned.
func TestProxiedServerUpdateIfGuards(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("guarded_reassign_wins_and_then_loses", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ugp")
		issue := bdProxiedCreate(t, bd, p.dir, "Park via proxy")
		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--assignee", "worker", "--status", "in_progress"); err != nil {
			t.Fatalf("seed assign failed: %v\n%s", err, out)
		}

		// Matching guard applies.
		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--if-assignee", "worker", "--assignee", "mayor"); err != nil {
			t.Fatalf("guarded reassign failed: %v\n%s", err, out)
		}
		got := bdProxiedShow(t, bd, p.dir, issue.ID)
		if got.Assignee != "mayor" {
			t.Fatalf("assignee = %q after guarded reassign, want mayor", got.Assignee)
		}

		// The same guard now loses with the distinct guard-mismatch exit code,
		// names the actual holder, and writes nothing.
		out, code := bdProxiedUpdateFailCode(t, bd, p.dir, issue.ID, "--if-assignee", "worker", "--assignee", "thief")
		if code != ExitGuardMismatch {
			t.Errorf("stale guard exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "mayor") {
			t.Errorf("mismatch error should name the current holder mayor, got:\n%s", out)
		}
		got = bdProxiedShow(t, bd, p.dir, issue.ID)
		if got.Assignee != "mayor" {
			t.Errorf("lost guard clobbered the row: assignee = %q, want mayor", got.Assignee)
		}
	})

	t.Run("claim_on_behalf_guards_on_unassigned", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ugr")
		issue := bdProxiedCreate(t, bd, p.dir, "Restore via proxy")

		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID,
			"--if-assignee", "", "--if-status", "open", "--assignee", "owner", "--status", "in_progress"); err != nil {
			t.Fatalf("claim-on-behalf failed: %v\n%s", err, out)
		}
		got := bdProxiedShow(t, bd, p.dir, issue.ID)
		if got.Assignee != "owner" {
			t.Fatalf("assignee = %q after claim-on-behalf, want owner", got.Assignee)
		}

		// Second restore loses: no longer unassigned.
		out, code := bdProxiedUpdateFailCode(t, bd, p.dir, issue.ID,
			"--if-assignee", "", "--if-status", "open", "--assignee", "other", "--status", "in_progress")
		if code != ExitGuardMismatch {
			t.Errorf("lost restore exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "owner") {
			t.Errorf("mismatch error should name the holder owner, got:\n%s", out)
		}
	})

	t.Run("guards_with_claim_rejected", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ugx")
		issue := bdProxiedCreate(t, bd, p.dir, "Flag combo via proxy")
		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "--if-assignee", "", "--claim")
		if !strings.Contains(out, "--claim") {
			t.Errorf("expected the --claim exclusion in the error, got:\n%s", out)
		}
	})
}
