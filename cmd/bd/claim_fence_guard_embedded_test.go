//go:build cgo

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// fenceOf reads an issue's claim fence back through the same surface a caller
// would use to construct --if-fence: bd show --json.
func fenceOf(t *testing.T, bd, dir, id string) int64 {
	t.Helper()
	return bdShow(t, bd, dir, id).ClaimFence
}

// TestUpdateIfFenceCLI drives `bd update --if-fence` end-to-end against the
// embedded backend. The fence guard joins the bd-wsqvw guard family, so a stale
// fence writes nothing and exits ExitGuardMismatch; what it adds over
// --if-assignee is that it survives the holder's own content writes and still
// catches a release-and-re-claim by the SAME assignee.
func TestUpdateIfFenceCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "uif")

	t.Run("live_fence_applies_stale_fence_exits_13", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Fence guard", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")
		fence := fenceOf(t, bd, dir, issue.ID)
		if fence == 0 {
			t.Fatalf("claim_fence = 0 after an assignee change; --if-fence has nothing to guard on")
		}

		// The live fence: the update applies.
		bdUpdate(t, bd, dir, issue.ID, "--if-fence", fmt.Sprint(fence), "--notes", "still mine")
		if got := bdShow(t, bd, dir, issue.ID).Notes; got != "still mine" {
			t.Errorf("notes = %q after a matching --if-fence, want %q", got, "still mine")
		}

		// A content write does not move the fence, so the same guard still holds.
		if got := fenceOf(t, bd, dir, issue.ID); got != fence {
			t.Errorf("claim_fence = %d after a content write, want unchanged %d", got, fence)
		}

		// Ownership moves and comes back to the SAME assignee: --if-assignee
		// would still pass here, --if-fence must not.
		bdUnclaim(t, bd, dir, issue.ID, "--actor", "alice")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")

		out, code := bdUpdateFailCode(t, bd, dir, issue.ID,
			"--if-fence", fmt.Sprint(fence), "--notes", "should-not-apply")
		if code != ExitGuardMismatch {
			t.Errorf("stale fence exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "fence") {
			t.Errorf("mismatch error should name the fence, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Notes; got != "still mine" {
			t.Errorf("stale --if-fence wrote through: notes = %q, want unchanged", got)
		}
	})

	t.Run("fence_zero_is_a_real_assertion", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Never claimed", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--if-fence", "0", "--notes", "pristine")
		if got := bdShow(t, bd, dir, issue.ID).Notes; got != "pristine" {
			t.Errorf("notes = %q after --if-fence 0 on a never-claimed bead, want %q", got, "pristine")
		}

		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-fence", "0", "--notes", "should-not-apply")
		if code != ExitGuardMismatch {
			t.Errorf("--if-fence 0 on a claimed bead: exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
	})

	t.Run("mixed_batch_failure_exits_1_not_13", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Mixed fence batch", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")

		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "uif-nope99",
			"--if-fence", "99", "--priority", "1")
		if code != 1 {
			t.Errorf("mixed fence+lookup failure exit code = %d, want 1\n%s", code, out)
		}
	})
}

// TestUpdateIfFenceFlagValidation pins the CLI-surface rules --if-fence shares
// with the rest of the guard family: no --claim (the claim CAS bumps the very
// fence a pre-claim guard would name), no --force, and a field update to ride
// on. Every rejection must exit 1 — 13 is reserved for real mismatches — and
// write nothing.
func TestUpdateIfFenceFlagValidation(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ufv")

	issue := bdCreate(t, bd, dir, "Fence flag validation", "--type", "task")

	t.Run("claim_and_if_fence_mutually_exclusive", func(t *testing.T) {
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-fence", "0", "--claim")
		if code != 1 {
			t.Errorf("flag-validation exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "--claim") {
			t.Errorf("expected the --claim exclusion in the error, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
			t.Errorf("rejected flag combo still claimed the issue: assignee = %q", got.Assignee)
		}
	})

	t.Run("force_and_if_fence_mutually_exclusive", func(t *testing.T) {
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-fence", "0", "--force", "--assignee", "bob")
		if code != 1 {
			t.Errorf("flag-validation exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "force") || !strings.Contains(out, "if-fence") {
			t.Errorf("--force with --if-fence should be rejected as mutually exclusive, got:\n%s", out)
		}
	})

	t.Run("if_fence_requires_a_field_update", func(t *testing.T) {
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-fence", "0", "--add-label", "x")
		if code != 1 {
			t.Errorf("flag-validation exit code = %d, want 1 (%d is reserved for real mismatches)\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "field update") {
			t.Errorf("expected the field-update requirement in the error, got:\n%s", out)
		}
	})

	t.Run("negative_if_fence_is_a_usage_error", func(t *testing.T) {
		// The fence starts at 0 and only increments, so a negative value can
		// never match. It is rejected as bad input (exit 1) rather than reported
		// as a lost race (exit 13), and in both spellings a caller might type.
		for _, args := range [][]string{
			{"--if-fence=-1", "--notes", "should-not-apply"},
			{"--if-fence", "-5", "--notes", "should-not-apply"},
		} {
			out, code := bdUpdateFailCode(t, bd, dir, append([]string{issue.ID}, args...)...)
			if code != 1 {
				t.Errorf("%v: exit code = %d, want 1 (%d is reserved for real mismatches)\n%s", args, code, ExitGuardMismatch, out)
			}
			if !strings.Contains(out, "--if-fence must be >= 0") {
				t.Errorf("%v: expected the negative-fence rejection, got:\n%s", args, out)
			}
		}
		if got := bdShow(t, bd, dir, issue.ID).Notes; got == "should-not-apply" {
			t.Error("a rejected --if-fence still wrote the update")
		}
	})
}

// TestCloseIfFenceCLI drives `bd close --if-fence`: the guard that stops a
// worker whose claim was retired from completing a bead it no longer owns.
// Close keeps its shipped contracts — partial success and an already-closed
// no-op both stay exit 0 — and adds exit 13 for the all-stale-guard case.
func TestCloseIfFenceCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cif")

	t.Run("stale_fence_refuses_with_13_live_fence_closes", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Zombie close", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")
		stale := fenceOf(t, bd, dir, issue.ID)

		// The claim is retired and re-taken; the zombie still holds `stale`.
		bdUnclaim(t, bd, dir, issue.ID, "--actor", "alice")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")

		// Run as the holder: bd close refuses another actor's live claim before
		// any guard runs (that refusal is a policy exit 1, not a guard verdict).
		out, code := bdRunFailCode(t, bd, dir, "close", issue.ID, "--actor", "alice", "--if-fence", fmt.Sprint(stale))
		if code != ExitGuardMismatch {
			t.Errorf("stale-fence close exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Status; got == types.StatusClosed {
			t.Errorf("zombie close landed: status = %q", got)
		}

		// Re-observe and close for real.
		live := fenceOf(t, bd, dir, issue.ID)
		bdClose(t, bd, dir, issue.ID, "--actor", "alice", "--if-fence", fmt.Sprint(live))
		if got := bdShow(t, bd, dir, issue.ID).Status; got != types.StatusClosed {
			t.Errorf("status = %q after a matching --if-fence close, want closed", got)
		}

		// Close is not an ownership transition, so the same fence still holds
		// and the idempotent re-close stays exit 0.
		bdClose(t, bd, dir, issue.ID, "--actor", "alice", "--if-fence", fmt.Sprint(live))

		// A stale fence on an already-closed bead is still a refusal: the guard
		// is evaluated against the row, not waived by idempotency.
		out, code = bdRunFailCode(t, bd, dir, "close", issue.ID, "--actor", "alice", "--if-fence", fmt.Sprint(live+1))
		if code != ExitGuardMismatch {
			t.Errorf("stale-fence re-close exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
	})

	t.Run("force_does_not_waive_the_fence", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Force plus fence", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")
		fence := fenceOf(t, bd, dir, issue.ID)

		out, code := bdRunFailCode(t, bd, dir, "close", issue.ID, "--force", "--if-fence", fmt.Sprint(fence+1))
		if code != ExitGuardMismatch {
			t.Errorf("--force with a stale fence: exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Status; got == types.StatusClosed {
			t.Errorf("--force bypassed the fence guard: status = %q", got)
		}
		// --force with a live fence is allowed: force waives only the is_blocked
		// and gate refusals.
		bdClose(t, bd, dir, issue.ID, "--force", "--if-fence", fmt.Sprint(fence))
		if got := bdShow(t, bd, dir, issue.ID).Status; got != types.StatusClosed {
			t.Errorf("status = %q after --force --if-fence with the live fence, want closed", got)
		}
	})

	t.Run("partial_success_still_exits_0", func(t *testing.T) {
		// Shipped contract: a multi-ID close that settles at least one issue
		// exits 0 even when another ID failed. The fence taxonomy only fires
		// when NOTHING settled.
		open := bdCreate(t, bd, dir, "Unclaimed target", "--type", "task")
		claimed := bdCreate(t, bd, dir, "Claimed target", "--type", "task")
		bdUpdate(t, bd, dir, claimed.ID, "--assignee", "alice", "--status", "in_progress")

		bdClose(t, bd, dir, open.ID, claimed.ID, "--actor", "alice", "--if-fence", "0")
		if got := bdShow(t, bd, dir, open.ID).Status; got != types.StatusClosed {
			t.Errorf("never-claimed target status = %q, want closed (its fence is 0)", got)
		}
		if got := bdShow(t, bd, dir, claimed.ID).Status; got == types.StatusClosed {
			t.Errorf("claimed target closed despite a fence-0 guard: status = %q", got)
		}
	})

	t.Run("all_stale_fences_exit_13_mixed_batch_exits_1", func(t *testing.T) {
		// The taxonomy switch is "nothing settled AND every attempted ID lost on
		// a guard". Two batches, both settling nothing, differing only in whether
		// a non-guard failure is mixed in.
		mine := bdCreate(t, bd, dir, "Stale fence A", "--type", "task")
		alsoMine := bdCreate(t, bd, dir, "Stale fence B", "--type", "task")
		theirs := bdCreate(t, bd, dir, "Held by bob", "--type", "task")
		for _, id := range []string{mine.ID, alsoMine.ID} {
			bdUpdate(t, bd, dir, id, "--assignee", "alice", "--status", "in_progress")
		}
		bdUpdate(t, bd, dir, theirs.ID, "--assignee", "bob", "--status", "in_progress")

		// Every failure is a stale fence: 13.
		out, code := bdRunFailCode(t, bd, dir, "close", mine.ID, alsoMine.ID,
			"--actor", "alice", "--if-fence", "9999")
		if code != ExitGuardMismatch {
			t.Errorf("all-stale-fence batch exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}

		// One stale fence plus one ownership refusal (a policy failure, not a
		// guard verdict): 1, because retrying the batch is NOT pointless.
		out, code = bdRunFailCode(t, bd, dir, "close", mine.ID, theirs.ID,
			"--actor", "alice", "--if-fence", "9999")
		if code != 1 {
			t.Errorf("mixed fence+ownership batch exit code = %d, want 1\n%s", code, out)
		}
		for _, id := range []string{mine.ID, alsoMine.ID, theirs.ID} {
			if got := bdShow(t, bd, dir, id).Status; got == types.StatusClosed {
				t.Errorf("%s closed despite a refused batch: status = %q", id, got)
			}
		}
	})

	t.Run("negative_if_fence_is_a_usage_error", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Negative close fence", "--type", "task")
		out, code := bdRunFailCode(t, bd, dir, "close", issue.ID, "--if-fence=-1")
		if code != 1 {
			t.Errorf("negative --if-fence exit code = %d, want 1 (%d is reserved for real mismatches)\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "--if-fence must be >= 0") {
			t.Errorf("expected the negative-fence rejection, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Status; got == types.StatusClosed {
			t.Errorf("a rejected --if-fence still closed the issue: status = %q", got)
		}
	})
}

// TestUnclaimIfFenceCLI drives `bd unclaim --if-fence`. Unclaim keeps its
// shipped exit-1 contract for guard failures (13 is an update/close taxonomy),
// the fence is an ADDITIONAL conjunct on both release forms, and it never
// authorizes a release the owner check would refuse.
func TestUnclaimIfFenceCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "nif")

	t.Run("stale_fence_refuses_with_1_live_fence_releases", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded release", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")
		stale := fenceOf(t, bd, dir, issue.ID)

		bdUnclaim(t, bd, dir, issue.ID, "--actor", "alice")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")

		out, code := bdRunFailCode(t, bd, dir, "unclaim", issue.ID,
			"--actor", "alice", "--if-fence", fmt.Sprint(stale))
		if code != 1 {
			t.Errorf("stale-fence unclaim exit code = %d, want 1 (unclaim's shipped guard contract)\n%s", code, out)
		}
		if !strings.Contains(out, "fence") {
			t.Errorf("mismatch error should name the fence, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "alice" {
			t.Errorf("stale --if-fence released the claim: assignee = %q", got)
		}

		live := fenceOf(t, bd, dir, issue.ID)
		bdUnclaim(t, bd, dir, issue.ID, "--actor", "alice", "--if-fence", fmt.Sprint(live))
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "" {
			t.Errorf("assignee = %q after a matching --if-fence release, want empty", got)
		}
	})

	t.Run("fence_never_authorizes_a_cross_actor_release", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Not a credential", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")
		live := fenceOf(t, bd, dir, issue.ID)

		// A stranger quoting the CORRECT fence is still not the owner.
		out, code := bdRunFailCode(t, bd, dir, "unclaim", issue.ID,
			"--actor", "stranger", "--if-fence", fmt.Sprint(live))
		if code != 1 {
			t.Errorf("cross-actor guarded release exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "held by") {
			t.Errorf("expected the ownership refusal, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "alice" {
			t.Errorf("cross-actor release landed: assignee = %q", got)
		}
	})

	t.Run("force_does_not_skip_a_supplied_fence", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Force plus fence", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")
		fence := fenceOf(t, bd, dir, issue.ID)

		out, code := bdRunFailCode(t, bd, dir, "unclaim", issue.ID,
			"--actor", "admin", "--force", "--if-fence", fmt.Sprint(fence+7))
		if code != 1 {
			t.Errorf("--force with a stale fence: exit code = %d, want 1\n%s", code, out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "alice" {
			t.Errorf("--force bypassed a failing fence guard: assignee = %q", got)
		}
		// --force with the live fence still performs the admin release.
		bdUnclaim(t, bd, dir, issue.ID, "--actor", "admin", "--force", "--if-fence", fmt.Sprint(fence))
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "" {
			t.Errorf("assignee = %q after a forced guarded release, want empty", got)
		}
	})

	t.Run("if_assignee_and_if_fence_compose", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Both guards", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")
		stale := fenceOf(t, bd, dir, issue.ID)

		bdUnclaim(t, bd, dir, issue.ID, "--actor", "alice")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")

		// The assignee guard holds and only the fence is stale: the whole
		// conjunction must still refuse.
		out := bdUnclaimFail(t, bd, dir, issue.ID, "--if-assignee", "alice", "--if-fence", fmt.Sprint(stale))
		if !strings.Contains(out, "fence") {
			t.Errorf("fence-only miss should name the fence, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "alice" {
			t.Errorf("refused conditional release clobbered the claim: assignee = %q", got)
		}

		live := fenceOf(t, bd, dir, issue.ID)
		bdUnclaim(t, bd, dir, issue.ID, "--if-assignee", "alice", "--if-fence", fmt.Sprint(live))
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "" {
			t.Errorf("assignee = %q after both guards held, want empty", got)
		}
	})

	t.Run("negative_if_fence_is_a_usage_error", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Negative release fence", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--status", "in_progress")

		out, code := bdRunFailCode(t, bd, dir, "unclaim", issue.ID, "--actor", "alice", "--if-fence=-1")
		if code != 1 {
			t.Errorf("negative --if-fence exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "--if-fence must be >= 0") {
			t.Errorf("expected the negative-fence rejection, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "alice" {
			t.Errorf("a rejected --if-fence still released the claim: assignee = %q", got)
		}
	})
}
