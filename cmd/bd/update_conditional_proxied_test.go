//go:build cgo

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
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

// TestProxiedServerUpdateIfFence proves --if-fence threads through the proxied
// path the same way --if-assignee/--if-status do: the guard rides
// domain.UpdateSpec.ExpectedFence into ApplyUpdate, which re-reads the row
// inside the unit of work's transaction. A stale fence is a terminal per-issue
// failure with the guard-mismatch exit code and nothing written; and because
// the fence only tracks OWNERSHIP, it still refuses a claim that was released
// and re-taken by the same assignee, which --if-assignee cannot detect.
func TestProxiedServerUpdateIfFence(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("stale_fence_loses_with_exit_13", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ufp")
		issue := bdProxiedCreate(t, bd, p.dir, "Fence via proxy")
		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--assignee", "worker", "--status", "in_progress"); err != nil {
			t.Fatalf("seed assign failed: %v\n%s", err, out)
		}
		fence := bdProxiedShow(t, bd, p.dir, issue.ID).ClaimFence
		if fence == 0 {
			t.Fatalf("claim_fence = 0 after an assignee change on the proxied path")
		}

		// The live fence applies.
		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID,
			"--if-fence", fmt.Sprint(fence), "--notes", "still mine"); err != nil {
			t.Fatalf("guarded update with a live fence failed: %v\n%s", err, out)
		}
		if got := bdProxiedShow(t, bd, p.dir, issue.ID).Notes; got != "still mine" {
			t.Fatalf("notes = %q after a matching --if-fence, want %q", got, "still mine")
		}

		// Ownership cycles back to the same assignee; only the fence moved.
		if out, err := bdProxiedRun(t, bd, p.dir, "unclaim", issue.ID, "--actor", "worker"); err != nil {
			t.Fatalf("release failed: %v\n%s", err, out)
		}
		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--assignee", "worker", "--status", "in_progress"); err != nil {
			t.Fatalf("re-assign failed: %v\n%s", err, out)
		}

		out, code := bdProxiedUpdateFailCode(t, bd, p.dir, issue.ID,
			"--if-fence", fmt.Sprint(fence), "--notes", "should-not-apply")
		if code != ExitGuardMismatch {
			t.Errorf("stale fence exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "fence") {
			t.Errorf("mismatch error should name the fence, got:\n%s", out)
		}
		if got := bdProxiedShow(t, bd, p.dir, issue.ID).Notes; got != "still mine" {
			t.Errorf("stale --if-fence wrote through: notes = %q, want unchanged", got)
		}
	})

	t.Run("proxied_claim_bumps_the_fence", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ufb")
		issue := bdProxiedCreate(t, bd, p.dir, "Claim bumps via proxy")

		before := bdProxiedShow(t, bd, p.dir, issue.ID).ClaimFence
		if before != 0 {
			t.Fatalf("pristine claim_fence = %d, want 0", before)
		}

		// The proxied claim CAS lives in internal/storage/domain/db, a hand-
		// written dual of issueops.ClaimIssueInTx that composes the bump as a
		// FRAGMENT — invisible to the source-literal pairing scan. Nothing but a
		// behavioral pin here proves the proxied dispatch layer actually bumps.
		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--claim"); err != nil {
			t.Fatalf("proxied claim failed: %v\n%s", err, out)
		}
		after := bdProxiedShow(t, bd, p.dir, issue.ID).ClaimFence
		if after <= before {
			t.Errorf("claim_fence = %d after a proxied claim, want greater than %d", after, before)
		}
	})

	t.Run("negative_if_fence_is_a_usage_error", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ufn")
		issue := bdProxiedCreate(t, bd, p.dir, "Negative fence via proxy")

		// The proxied parse site is gatherUpdateInput, a separate reader from the
		// direct path's updateGuardsFromFlags; both must reject it as usage.
		out, code := bdProxiedUpdateFailCode(t, bd, p.dir, issue.ID, "--if-fence=-1", "--notes", "should-not-apply")
		if code != 1 {
			t.Errorf("negative --if-fence exit code = %d, want 1 (%d is reserved for real mismatches)\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "--if-fence must be >= 0") {
			t.Errorf("expected the negative-fence rejection, got:\n%s", out)
		}
		if got := bdProxiedShow(t, bd, p.dir, issue.ID).Notes; got == "should-not-apply" {
			t.Error("a rejected --if-fence still wrote through the proxied path")
		}
	})

	t.Run("close_and_unclaim_refuse_the_fence_guard_loudly", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ufr")
		issue := bdProxiedCreate(t, bd, p.dir, "Unsupported fence guard")
		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--assignee", "worker", "--status", "in_progress"); err != nil {
			t.Fatalf("seed assign failed: %v\n%s", err, out)
		}

		// Neither the proxied close nor the proxied release carries a
		// compare-and-set, so the guard must be refused rather than dropped.
		for _, args := range [][]string{
			{"close", issue.ID, "--if-fence", "1"},
			{"unclaim", issue.ID, "--actor", "worker", "--if-fence", "1"},
		} {
			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, args...)
			if err == nil {
				t.Errorf("bd %s should be refused in proxied-server mode; got:\n%s%s",
					strings.Join(args, " "), stdout, stderr)
				continue
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Errorf("bd %s failed without an exit code: %v", strings.Join(args, " "), err)
				continue
			}
			if ee.ExitCode() != 1 {
				t.Errorf("bd %s exit code = %d, want 1", strings.Join(args, " "), ee.ExitCode())
			}
			if !strings.Contains(stdout+stderr, "--if-fence is not supported in proxied-server mode") {
				t.Errorf("bd %s should name the unsupported flag, got:\n%s%s",
					strings.Join(args, " "), stdout, stderr)
			}
		}
		// The refusal must not have written anything.
		got := bdProxiedShow(t, bd, p.dir, issue.ID)
		if got.Assignee != "worker" || got.Status != types.StatusInProgress {
			t.Errorf("refused guard mutated the issue: assignee=%q status=%q", got.Assignee, got.Status)
		}
	})
}
