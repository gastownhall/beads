//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// ===== Close-specific test helpers =====

// bdClose runs "bd close" with the given args and returns stdout.
// Retries on flock contention.
func bdClose(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"close"}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd close %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdCloseFail runs "bd close" expecting failure.
func bdCloseFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"close"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd close %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdDepAdd runs "bd dep add" with the given args.
// Retries on flock contention.
func bdDepAdd(t *testing.T, bd, dir string, args ...string) {
	t.Helper()
	fullArgs := append([]string{"dep", "add"}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd dep add %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// querySessionSQL queries closed_by_session via raw SQL since it's not in IssueSelectColumns.
func querySessionSQL(t *testing.T, beadsDir, id string) string {
	t.Helper()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var session string
	// Check both tables.
	err = db.QueryRowContext(t.Context(),
		"SELECT COALESCE(closed_by_session, '') FROM issues WHERE id = ?", id).Scan(&session)
	if err != nil {
		// Try wisps table.
		err = db.QueryRowContext(t.Context(),
			"SELECT COALESCE(closed_by_session, '') FROM wisps WHERE id = ?", id).Scan(&session)
		if err != nil {
			t.Fatalf("query closed_by_session: %v", err)
		}
	}
	return session
}

// ===== Close tests =====

func TestEmbeddedClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tc")

	// ===== Basic Close Behavior =====

	t.Run("basic_close", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Close me", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected status closed, got %s", got.Status)
		}
		if got.ClosedAt == nil {
			t.Error("expected closed_at to be set")
		}
	})

	t.Run("close_default_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Default reason", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "Closed" {
			t.Errorf("expected default close_reason 'Closed', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reason test", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--reason", "done")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "done" {
			t.Errorf("expected close_reason 'done', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_reason_short", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Short reason", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "-r", "fixed")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "fixed" {
			t.Errorf("expected close_reason 'fixed', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_message_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Message alias", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "-m", "via message")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "via message" {
			t.Errorf("expected close_reason 'via message', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_resolution_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Resolution alias", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--resolution", "wontfix")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "wontfix" {
			t.Errorf("expected close_reason 'wontfix', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_comment_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Comment alias", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--comment", "duplicate")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "duplicate" {
			t.Errorf("expected close_reason 'duplicate', got %q", got.CloseReason)
		}
	})

	t.Run("close_multiple_ids", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Multi close 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Multi close 2", "--type", "task")
		bdClose(t, bd, dir, issue1.ID, issue2.ID)
		got1 := bdShow(t, bd, dir, issue1.ID)
		got2 := bdShow(t, bd, dir, issue2.ID)
		if got1.Status != types.StatusClosed {
			t.Errorf("issue1: expected closed, got %s", got1.Status)
		}
		if got2.Status != types.StatusClosed {
			t.Errorf("issue2: expected closed, got %s", got2.Status)
		}
	})

	t.Run("close_multiple_ids_with_per_id_reasons", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Multi close reason 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Multi close reason 2", "--type", "task")

		bdClose(t, bd, dir, issue1.ID, "--reason", "fixed A", issue2.ID, "--reason", "fixed B")

		got1 := bdShow(t, bd, dir, issue1.ID)
		got2 := bdShow(t, bd, dir, issue2.ID)
		if got1.CloseReason != "fixed A" {
			t.Errorf("issue1 close_reason = %q, want %q", got1.CloseReason, "fixed A")
		}
		if got2.CloseReason != "fixed B" {
			t.Errorf("issue2 close_reason = %q, want %q", got2.CloseReason, "fixed B")
		}
	})

	t.Run("close_already_closed", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Double close", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		// Closing again should not panic.
		cmd := exec.Command(bd, "close", issue.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.CombinedOutput() // Don't check error — behavior varies.
	})

	// The delegated close (CloseIssueChecked) reports Unchanged for an already-
	// closed issue. Re-closing must stay an idempotent success (exit 0, issue
	// stays closed) — matching the old CloseIssue path, which returned nil for an
	// already-closed issue. bdClose t.Fatalf's on non-zero exit, so this asserts
	// the exit code.
	t.Run("close_already_closed_is_idempotent_success", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Idempotent close", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		bdClose(t, bd, dir, issue.ID) // second close: idempotent no-op, exit 0
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected issue to remain closed after idempotent re-close, got %s", got.Status)
		}
	})

	// Output parity: `bd close --json` on an already-closed bead must still emit
	// the issue in the JSON array (the old CloseIssue path re-fetched and reported
	// it). The Unchanged branch skips the real-close side effects but keeps the
	// display, so the shape is unchanged.
	t.Run("close_json_already_closed_emits_issue", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON idempotent", "--type", "task")
		bdClose(t, bd, dir, issue.ID) // first close
		cmd := exec.Command(bd, "close", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd close --json (already closed) failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := stdout.String()
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("expected a JSON array for already-closed --json re-close, got: %s", s)
		}
		var issues []json.RawMessage
		if jsonErr := json.Unmarshal([]byte(s[start:]), &issues); jsonErr != nil {
			t.Fatalf("expected valid JSON array, got: %s (%v)", s[start:], jsonErr)
		}
		if len(issues) != 1 {
			t.Fatalf("expected 1 issue in JSON for already-closed re-close (parity), got %d: %s", len(issues), s[start:])
		}
		if !strings.Contains(s, issue.ID) {
			t.Errorf("expected already-closed issue %s in JSON output, got: %s", issue.ID, s)
		}
	})

	// Mixed batch: one already-closed bead + one live bead. Both must appear in
	// the JSON array — the already-closed one for output parity, the live one as a
	// real close.
	t.Run("close_json_mixed_batch_includes_already_closed", func(t *testing.T) {
		already := bdCreate(t, bd, dir, "Mixed already", "--type", "task")
		fresh := bdCreate(t, bd, dir, "Mixed fresh", "--type", "task")
		bdClose(t, bd, dir, already.ID) // pre-close one

		cmd := exec.Command(bd, "close", already.ID, fresh.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd close --json (mixed batch) failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := stdout.String()
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("expected a JSON array for mixed-batch --json close, got: %s", s)
		}
		var issues []json.RawMessage
		if jsonErr := json.Unmarshal([]byte(s[start:]), &issues); jsonErr != nil {
			t.Fatalf("expected valid JSON array, got: %s (%v)", s[start:], jsonErr)
		}
		if len(issues) != 2 {
			t.Fatalf("expected both issues in JSON (real close + already-closed parity), got %d: %s", len(issues), s[start:])
		}
		if !strings.Contains(s, already.ID) || !strings.Contains(s, fresh.ID) {
			t.Errorf("expected both %s and %s in JSON output, got: %s", already.ID, fresh.ID, s)
		}
	})

	t.Run("close_nonexistent_id", func(t *testing.T) {
		bdCloseFail(t, bd, dir, "tc-nonexistent999")
	})

	// ===== Force Flag and Close Guards =====

	t.Run("close_blocked_refuses_without_force", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Blocker guard", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked guard", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		// Without --force, should fail (exit non-zero).
		bdCloseFail(t, bd, dir, blocked.ID)
		got := bdShow(t, bd, dir, blocked.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected blocked issue to remain open without --force")
		}
	})

	t.Run("close_blocked_with_force", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Blocker force", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked force", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		bdClose(t, bd, dir, blocked.ID, "--force")
		got := bdShow(t, bd, dir, blocked.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed with --force, got %s", got.Status)
		}
	})

	// Proves the S7 delegation: `bd close` on a blocked issue now surfaces the
	// engine's atomic guard (storage.ErrCloseBlocked) rather than a duplicated
	// CLI pre-check. The refusal must be atomic — the issue stays open because the
	// guard and the close share one transaction — and the message must name the
	// blocker and the --force hint. --force then bypasses the engine guard.
	t.Run("close_blocked_delegated_guard", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Deleg blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Deleg blocked", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		out := bdCloseFail(t, bd, dir, blocked.ID)
		if !strings.Contains(out, "cannot close") {
			t.Errorf("expected engine guard message ('cannot close'), got: %s", out)
		}
		if !strings.Contains(out, blocker.ID) {
			t.Errorf("expected guard message to name blocker %s, got: %s", blocker.ID, out)
		}
		if !strings.Contains(out, "--force") {
			t.Errorf("expected guard message to mention --force, got: %s", out)
		}

		// Atomic refuse: the guard ran in-transaction, so the issue must remain open.
		got := bdShow(t, bd, dir, blocked.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected blocked issue to remain open after the guard refused (atomic)")
		}

		// --force bypasses the engine guard.
		bdClose(t, bd, dir, blocked.ID, "--force")
		got = bdShow(t, bd, dir, blocked.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed with --force, got %s", got.Status)
		}
	})

	// The delegated guard refuses only on a LIVE direct blocker, matching the
	// historical `bd close` predicate. A transitively-blocked child (parent-child
	// of a blocked parent) has is_blocked=1 but no direct blocker of its own, so it
	// must close WITHOUT --force — the historical behavior.
	t.Run("close_transitively_blocked_closes_without_force", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Trans blocker", "--type", "task")
		parent := bdCreate(t, bd, dir, "Trans parent", "--type", "task")
		child := bdCreate(t, bd, dir, "Trans child", "--type", "task")
		// parent is blocked by an open blocker; child is a parent-child of parent,
		// so child inherits is_blocked=1 transitively with no direct blocker.
		bdDepAdd(t, bd, dir, parent.ID, blocker.ID)
		bdDepAdd(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")

		bdClose(t, bd, dir, child.ID) // no --force
		got := bdShow(t, bd, dir, child.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected transitively-blocked child to close without --force, got %s", got.Status)
		}
	})

	t.Run("close_pinned_refuses_without_force", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Pinned guard", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "pinned")
		bdCloseFail(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected pinned issue to remain pinned without --force")
		}
	})

	t.Run("close_pinned_with_force", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Pinned force", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "pinned")
		bdClose(t, bd, dir, issue.ID, "--force")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed with --force, got %s", got.Status)
		}
	})

	// be-035: silent-data-loss bug. Without an authority check, actor A could
	// close a bead claimed by actor B and bd would print "✓ Closed" with no
	// indication the actor mismatched. The fix refuses the close (non-zero
	// exit, stderr message) unless --force is set.
	t.Run("close_assignee_mismatch_refuses_without_force", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Mismatch guard", "--type", "task")
		// Bob claims the bead.
		bdUpdate(t, bd, dir, issue.ID, "--actor", "bob", "--claim")

		// Alice tries to close it — must fail loudly, not silently succeed.
		out := bdCloseFail(t, bd, dir, issue.ID, "--actor", "alice")
		if !strings.Contains(out, "assignee is") {
			t.Errorf("expected stderr to mention assignee mismatch, got: %s", out)
		}
		if !strings.Contains(out, "bob") || !strings.Contains(out, "alice") {
			t.Errorf("expected stderr to name both assignee and actor, got: %s", out)
		}

		// Bead must remain open.
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected bead to remain open after refused close")
		}
	})

	t.Run("close_assignee_mismatch_with_force", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Mismatch force", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--actor", "bob", "--claim")

		// --force overrides the authority check.
		bdClose(t, bd, dir, issue.ID, "--actor", "alice", "--force")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed with --force despite mismatch, got %s", got.Status)
		}
	})

	t.Run("close_same_actor_succeeds", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Same actor", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--actor", "alice", "--claim")

		// Same actor — no authority issue.
		bdClose(t, bd, dir, issue.ID, "--actor", "alice")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed when actor matches assignee, got %s", got.Status)
		}
	})

	t.Run("close_unassigned_bead_succeeds", func(t *testing.T) {
		// Lots of bd's normal flow involves closing unclaimed beads;
		// the authority check must not break this.
		issue := bdCreate(t, bd, dir, "Unassigned", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--actor", "carol")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected unassigned bead to close, got %s", got.Status)
		}
	})

	t.Run("close_epic_open_children_refuses", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Epic guard", "--type", "epic")
		child := bdCreate(t, bd, dir, "Epic child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, epic.ID, "--type", "parent-child")

		bdCloseFail(t, bd, dir, epic.ID)
		got := bdShow(t, bd, dir, epic.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected epic with open children to remain open without --force")
		}
	})

	t.Run("close_epic_open_children_force", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Epic force", "--type", "epic")
		child := bdCreate(t, bd, dir, "Epic child force", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, epic.ID, "--type", "parent-child")

		cmd := exec.Command(bd, "close", epic.ID, "--force")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd close --force failed: %v\n%s", err, out)
		}
		got := bdShow(t, bd, dir, epic.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected epic closed with --force, got %s", got.Status)
		}
		if !strings.Contains(string(out), "warning:") || !strings.Contains(string(out), "open child") {
			t.Errorf("expected warning about open children on --force, got: %s", out)
		}
		_ = child
	})

	t.Run("close_non_epic_parent_open_children_refuses", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Task parent guard", "--type", "task")
		child := bdCreate(t, bd, dir, "Task child guard", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")

		bdCloseFail(t, bd, dir, parent.ID)
		got := bdShow(t, bd, dir, parent.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected non-epic parent with open children to remain open without --force")
		}
		_ = child
	})

	t.Run("close_last_child_keeps_regular_epic_open", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Epic stays open", "--type", "epic")
		child := bdCreate(t, bd, dir, "Epic closing child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, epic.ID, "--type", "parent-child")

		bdClose(t, bd, dir, child.ID)

		got := bdShow(t, bd, dir, epic.ID)
		if got.Status != types.StatusOpen {
			t.Errorf("expected regular epic to stay open after its last child closes, got %s", got.Status)
		}
	})

	// ===== Blocker and Suggest-Next Behavior =====

	t.Run("close_unblocks_dependent", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Unblock blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Unblock blocked", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		bdClose(t, bd, dir, blocker.ID)
		got := bdShow(t, bd, dir, blocker.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected blocker closed, got %s", got.Status)
		}
		gotBlocked := bdShow(t, bd, dir, blocked.ID)
		if gotBlocked.Status != types.StatusOpen {
			t.Errorf("expected dependent still open, got %s", gotBlocked.Status)
		}
	})

	t.Run("close_suggest_next", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Suggest blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Suggest blocked", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		out := bdClose(t, bd, dir, blocker.ID, "--suggest-next")
		if !strings.Contains(out, "unblocked") && !strings.Contains(out, blocked.ID) {
			t.Logf("suggest-next output did not mention unblocked issue: %s", out)
		}
	})

	t.Run("close_suggest_next_json", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Suggest JSON blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Suggest JSON blocked", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		cmd := exec.Command(bd, "close", blocker.ID, "--suggest-next", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd close --suggest-next --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := stdout.String()
		if !strings.Contains(s, "unblocked") {
			t.Logf("JSON output did not contain 'unblocked' key: %s", s)
		}
	})

	// ===== Claim-Next Flag =====

	t.Run("close_claim_next", func(t *testing.T) {
		toClose := bdCreate(t, bd, dir, "Claim next close", "--type", "task")
		nextIssue := bdCreate(t, bd, dir, "Claim next target", "--type", "task")

		out := bdClose(t, bd, dir, toClose.ID, "--claim-next")
		got := bdShow(t, bd, dir, nextIssue.ID)
		if got.Status == types.StatusInProgress && got.Assignee != "" {
			_ = out
		} else {
			t.Logf("claim-next: next issue status=%s assignee=%q (may not have been claimed)", got.Status, got.Assignee)
		}
	})

	t.Run("close_claim_next_no_ready", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Only issue", "--type", "task")
		out := bdClose(t, bd, dir, issue.ID, "--claim-next")
		if !strings.Contains(out, "No ready issues") && !strings.Contains(out, "claimed") {
			t.Logf("claim-next with no ready issues: %s", out)
		}
	})

	t.Run("close_claim_next_json", func(t *testing.T) {
		toClose := bdCreate(t, bd, dir, "Claim JSON close", "--type", "task")
		_ = bdCreate(t, bd, dir, "Claim JSON target", "--type", "task")

		cmd := exec.Command(bd, "close", toClose.ID, "--claim-next", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd close --claim-next --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := stdout.String()
		start := strings.Index(s, "{")
		if start < 0 {
			start = strings.Index(s, "[")
		}
		if start >= 0 && !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON, got: %s", s[start:])
		}
	})

	// ===== Session Flag =====

	t.Run("close_with_session", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Session test", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--session", "sess-456")
		session := querySessionSQL(t, beadsDir, issue.ID)
		if session != "sess-456" {
			t.Errorf("expected closed_by_session 'sess-456', got %q", session)
		}
	})

	t.Run("close_session_from_env", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Env session test", "--type", "task")
		cmd := exec.Command(bd, "close", issue.ID)
		cmd.Dir = dir
		env := bdEnv(dir)
		env = append(env, "CLAUDE_SESSION_ID=env-sess")
		cmd.Env = env
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd close with env session failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		session := querySessionSQL(t, beadsDir, issue.ID)
		if session != "env-sess" {
			t.Errorf("expected closed_by_session 'env-sess', got %q", session)
		}
	})

	// ===== JSON Output and Done Alias =====

	t.Run("close_json_output", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON close test", "--type", "task")
		cmd := exec.Command(bd, "close", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd close --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := stdout.String()
		start := strings.Index(s, "[")
		if start < 0 {
			start = strings.Index(s, "{")
		}
		if start < 0 {
			t.Fatalf("no JSON in output: %s", s)
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON, got: %s", s[start:])
		}
	})

	t.Run("done_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Done alias test", "--type", "task")
		cmd := exec.Command(bd, "done", issue.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd done failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed via done alias, got %s", got.Status)
		}
	})

	t.Run("done_positional_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Done reason test", "--type", "task")
		cmd := exec.Command(bd, "done", issue.ID, "the reason")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd done with reason failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "the reason" {
			t.Errorf("expected close_reason 'the reason', got %q", got.CloseReason)
		}
	})

	// ===== Dolt Commit and Edge Cases =====

	t.Run("close_dolt_commit", func(t *testing.T) {
		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		cfg, _ := configfile.Load(beadsDir)
		database := ""
		if cfg != nil {
			database = cfg.GetDoltDatabase()
		}

		countCommits := func() int {
			db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer cleanup()
			var count int
			if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&count); err != nil {
				t.Fatalf("query dolt_log: %v", err)
			}
			return count
		}

		before := countCommits()
		issue := bdCreate(t, bd, dir, "Dolt commit test", "--type", "task")
		_ = issue
		afterCreate := countCommits()
		bdClose(t, bd, dir, issue.ID)
		afterClose := countCommits()

		if afterClose <= afterCreate {
			t.Errorf("expected Dolt commit count to increase after close: before=%d afterCreate=%d afterClose=%d", before, afterCreate, afterClose)
		}
	})

	t.Run("close_continue_multiple_ids_fails", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Continue multi 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Continue multi 2", "--type", "task")
		bdCloseFail(t, bd, dir, issue1.ID, issue2.ID, "--continue")
	})

	// Reproduces gastownhall/beads#3769: --continue auto-advances + claims
	// the next molecule step inside AdvanceToNextStep, but only --claim-next
	// was calling SetLastTouchedID. Without the fix, .beads/last-touched
	// stayed pointed at the just-closed step.
	t.Run("close_continue_updates_last_touched", func(t *testing.T) {
		// Template-shaped epic so AdvanceToNextStep recognizes it as a molecule.
		root := bdCreate(t, bd, dir, "Continue last-touched root", "--type", "epic", "--labels", "template")
		step1 := bdCreate(t, bd, dir, "Step one", "--type", "task", "--parent", root.ID)
		step2 := bdCreate(t, bd, dir, "Step two", "--type", "task", "--parent", root.ID)
		// step2 blocks on step1, so step1 closes first and step2 becomes ready.
		bdDepAdd(t, bd, dir, step2.ID, step1.ID)

		// Claim step1 first (mirrors the natural workflow); this seeds last-touched
		// with step1's ID via the update --claim path, isolating the close-flow's
		// responsibility for advancing it.
		_, err := bdRunWithFlockRetry(t, bd, dir, "update", step1.ID, "--claim")
		if err != nil {
			t.Fatalf("seed claim failed: %v", err)
		}

		_ = bdClose(t, bd, dir, step1.ID, "--reason", "test", "--continue")

		got, err := os.ReadFile(filepath.Join(beadsDir, "last-touched"))
		if err != nil {
			t.Fatalf("read .beads/last-touched: %v", err)
		}
		gotID := strings.TrimSpace(string(got))
		if gotID != step2.ID {
			t.Errorf(".beads/last-touched = %q after `bd close %s --continue`, want %q (the auto-advanced step)",
				gotID, step1.ID, step2.ID)
		}
	})

	// Regression for the delegated-close change: routing an already-closed issue
	// through alreadyClosed instead of closedCount must NOT drop the retry-safe
	// post-close command contracts. A re-close is an idempotent no-op on stored
	// state, but it is still a successful `bd close` and must honor last-touched,
	// --continue, and --claim-next so a crash/retry after the status flip can still
	// re-drive workflow advancement. Real mutation side effects stay suppressed.
	t.Run("close_already_closed_updates_last_touched", func(t *testing.T) {
		target := bdCreate(t, bd, dir, "Reclose last-touched target", "--type", "task")
		bdClose(t, bd, dir, target.ID) // real close: last-touched = target
		other := bdCreate(t, bd, dir, "Reclose last-touched other", "--type", "task")
		bdClose(t, bd, dir, other.ID) // real close moves last-touched to `other`

		// Re-close the already-closed target: idempotent no-op, but it must re-touch
		// the target so subsequent default-target commands point back at it.
		bdClose(t, bd, dir, target.ID)

		got, err := os.ReadFile(filepath.Join(beadsDir, "last-touched"))
		if err != nil {
			t.Fatalf("read .beads/last-touched: %v", err)
		}
		if gotID := strings.TrimSpace(string(got)); gotID != target.ID {
			t.Errorf(".beads/last-touched = %q after re-closing already-closed %s, want %q",
				gotID, target.ID, target.ID)
		}
	})

	t.Run("close_already_closed_continue_advances", func(t *testing.T) {
		// Isolated store so molecule progress and the Dolt commit count are
		// deterministic, mirroring close_already_closed_claim_next.
		cdir, cbeads, _ := bdInit(t, bd, "--prefix", "rk")
		countCommits := func() int {
			dataDir := filepath.Join(cbeads, "embeddeddolt")
			cfg, _ := configfile.Load(cbeads)
			database := ""
			if cfg != nil {
				database = cfg.GetDoltDatabase()
			}
			db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer cleanup()
			var count int
			if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&count); err != nil {
				t.Fatalf("query dolt_log: %v", err)
			}
			return count
		}

		root := bdCreate(t, bd, cdir, "Reclose continue root", "--type", "epic", "--labels", "template")
		step1 := bdCreate(t, bd, cdir, "Reclose continue step one", "--type", "task", "--parent", root.ID)
		step2 := bdCreate(t, bd, cdir, "Reclose continue step two", "--type", "task", "--parent", root.ID)
		// step2 blocks on step1, so closing step1 makes step2 the next ready step.
		bdDepAdd(t, bd, cdir, step2.ID, step1.ID)

		if _, err := bdRunWithFlockRetry(t, bd, cdir, "update", step1.ID, "--claim"); err != nil {
			t.Fatalf("seed claim failed: %v", err)
		}

		// Close step1 for real WITHOUT --continue — the advancement trigger never ran
		// (models a crash/retry between the status flip and the advance).
		bdClose(t, bd, cdir, step1.ID, "--reason", "first")

		// Retry the close WITH --continue against the now already-closed step. The
		// idempotent re-close must advance the molecule AND persist the advance, not
		// just mutate the in-memory working set.
		beforeCommits := countCommits()
		_ = bdClose(t, bd, cdir, step1.ID, "--reason", "retry", "--continue")

		got, err := os.ReadFile(filepath.Join(cbeads, "last-touched"))
		if err != nil {
			t.Fatalf("read .beads/last-touched: %v", err)
		}
		if gotID := strings.TrimSpace(string(got)); gotID != step2.ID {
			t.Errorf(".beads/last-touched = %q after re-closing already-closed %s --continue, want %q (auto-advanced step)",
				gotID, step1.ID, step2.ID)
		}

		// Persisted-advancement assertions — the retry-safety property the fix
		// guarantees, and the gap the reviewer flagged: last-touched alone proves
		// AdvanceToNextStep ran in the working set, not that the advance was
		// committed. step2 must be persisted as in_progress, and the already-closed
		// re-close (closedCount==0) must still produce a Dolt commit for the advance.
		// The auto-advance moves the step to in_progress via UpdateIssue but, unlike
		// --claim-next's ClaimIssue, does not set an assignee, so we assert status +
		// commit rather than assignee.
		if s2 := bdShow(t, bd, cdir, step2.ID); s2.Status != types.StatusInProgress {
			t.Errorf("expected step2 %s persisted as in_progress after already-closed --continue, got status=%s",
				step2.ID, s2.Status)
		}
		if afterCommits := countCommits(); afterCommits <= beforeCommits {
			t.Errorf("expected a Dolt commit for the --continue advance on an already-closed re-close: before=%d after=%d",
				beforeCommits, afterCommits)
		}
	})

	t.Run("close_already_closed_claim_next", func(t *testing.T) {
		// Isolated store so the ready set is deterministic — the shared store carries
		// open issues from sibling subtests, and --claim-next claims the global
		// highest-priority ready issue.
		cdir, cbeads, _ := bdInit(t, bd, "--prefix", "rc")
		countCommits := func() int {
			dataDir := filepath.Join(cbeads, "embeddeddolt")
			cfg, _ := configfile.Load(cbeads)
			database := ""
			if cfg != nil {
				database = cfg.GetDoltDatabase()
			}
			db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer cleanup()
			var count int
			if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&count); err != nil {
				t.Fatalf("query dolt_log: %v", err)
			}
			return count
		}

		target := bdCreate(t, bd, cdir, "Reclose claim target", "--type", "task")
		next := bdCreate(t, bd, cdir, "Reclose claim next", "--type", "task")
		bdClose(t, bd, cdir, target.ID) // real close; `next` is now the only ready issue

		// Re-close the already-closed target with --claim-next: the retry-safe claim
		// must still fire and claim the next ready issue.
		beforeCommits := countCommits()
		_ = bdClose(t, bd, cdir, target.ID, "--claim-next")

		got := bdShow(t, bd, cdir, next.ID)
		if got.Status != types.StatusInProgress || got.Assignee == "" {
			t.Errorf("expected next issue %s claimed (in_progress, assigned) after already-closed --claim-next, got status=%s assignee=%q",
				next.ID, got.Status, got.Assignee)
		}
		// The claim is a real mutation, so the already-closed re-close must still
		// persist it with a Dolt commit — not leave it dangling in the working set
		// (the close itself is a no-op, so only the claim drives the commit).
		if afterCommits := countCommits(); afterCommits <= beforeCommits {
			t.Errorf("expected a Dolt commit for the --claim-next claim on an already-closed re-close: before=%d after=%d",
				beforeCommits, afterCommits)
		}
	})

	// Regression for the delegated-close change: molecule root auto-close is a
	// state-derived post-close contract, so an already-closed re-close of the final
	// step must re-drive it. Models the crash where the final step's close persisted
	// but its molecule-root auto-close did not — the idempotent retry heals the
	// stranded-open root instead of leaving it open forever.
	t.Run("close_already_closed_replays_molecule_auto_close", func(t *testing.T) {
		// Isolated store so molecule progress is deterministic.
		mdir, _, _ := bdInit(t, bd, "--prefix", "rm")
		root := bdCreate(t, bd, mdir, "Reclose molecule root", "--type", "epic", "--labels", "template")
		step1 := bdCreate(t, bd, mdir, "Reclose molecule step one", "--type", "task", "--parent", root.ID)
		step2 := bdCreate(t, bd, mdir, "Reclose molecule step two", "--type", "task", "--parent", root.ID)

		// Close both steps for real. Closing the final step auto-closes the root.
		bdClose(t, bd, mdir, step1.ID, "--reason", "one")
		bdClose(t, bd, mdir, step2.ID, "--reason", "two")
		if got := bdShow(t, bd, mdir, root.ID); got.Status != types.StatusClosed {
			t.Fatalf("precondition: expected molecule root %s auto-closed after final step, got %s", root.ID, got.Status)
		}

		// Strand the molecule: reopen ONLY the root, leaving both steps closed — the
		// exact state left when a final step's close commits but its root auto-close
		// does not.
		bdReopen(t, bd, mdir, root.ID)
		if got := bdShow(t, bd, mdir, root.ID); got.Status != types.StatusOpen {
			t.Fatalf("precondition: expected molecule root %s reopened, got %s", root.ID, got.Status)
		}
		if got := bdShow(t, bd, mdir, step2.ID); got.Status != types.StatusClosed {
			t.Fatalf("precondition: expected step2 %s to stay closed after reopening only the root, got %s", step2.ID, got.Status)
		}

		// Re-close the already-closed final step. The idempotent re-close must replay
		// molecule auto-close and re-close the stranded-open root.
		_ = bdClose(t, bd, mdir, step2.ID, "--reason", "retry")

		if got := bdShow(t, bd, mdir, root.ID); got.Status != types.StatusClosed {
			t.Errorf("expected stranded-open molecule root %s re-closed by an already-closed re-close of the final step, got %s",
				root.ID, got.Status)
		}
	})

	t.Run("close_suggest_next_multiple_ids_fails", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Suggest multi 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Suggest multi 2", "--type", "task")
		bdCloseFail(t, bd, dir, issue1.ID, issue2.ID, "--suggest-next")
	})
}

// TestEmbeddedCloseConcurrent exercises create, close, and list operations
// concurrently to verify EmbeddedDoltStore handles concurrent CLI invocations
// without panics, data corruption, or deadlocks.
func TestEmbeddedCloseConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cx")

	const (
		numWorkers      = 10
		issuesPerWorker = 5
	)

	type workerResult struct {
		worker     int
		ids        []string
		listCounts []int
		err        error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			for i := 0; i < issuesPerWorker; i++ {
				// Create an issue.
				title := fmt.Sprintf("w%d-close-%d", worker, i)
				out, err := bdRunWithFlockRetry(t, bd, dir, "create", "--silent", title)
				if err != nil {
					r.err = fmt.Errorf("create %d: %v\n%s", i, err, out)
					results[worker] = r
					return
				}
				id := strings.TrimSpace(string(out))
				if id == "" {
					r.err = fmt.Errorf("create %d: empty ID", i)
					results[worker] = r
					return
				}
				r.ids = append(r.ids, id)

				// Close with a reason.
				reason := fmt.Sprintf("done-by-worker-%d", worker)
				cCmd := exec.Command(bd, "close", id, "--reason", reason)
				cCmd.Dir = dir
				cCmd.Env = bdEnv(dir)
				cOut, err := cCmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("close %d: %v\n%s", i, err, cOut)
					results[worker] = r
					return
				}

				// List to verify consistency (interleaved with writes).
				listCmd := exec.Command(bd, "list", "--json", "--limit", "0", "--all")
				listCmd.Dir = dir
				listCmd.Env = bdEnv(dir)
				listStdout, listStderr, err := runCommandBuffers(t, listCmd)
				if err != nil {
					r.err = fmt.Errorf("list after close %d: %v\nstdout:\n%s\nstderr:\n%s", i, err, listStdout.String(), listStderr.String())
					results[worker] = r
					return
				}
				s := listStdout.String()
				start := strings.Index(s, "[")
				if start < 0 {
					r.listCounts = append(r.listCounts, 0)
					continue
				}
				var issues []json.RawMessage
				if jsonErr := json.Unmarshal([]byte(s[start:]), &issues); jsonErr != nil {
					r.err = fmt.Errorf("list parse %d: %v\nstdout:\n%s\nstderr:\n%s", i, jsonErr, s, listStderr.String())
					results[worker] = r
					return
				}
				r.listCounts = append(r.listCounts, len(issues))
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	// Check for errors and collect IDs.
	allIDs := make(map[string]bool)
	var failures int
	for _, r := range results {
		if r.err != nil {
			if !strings.Contains(r.err.Error(), "one writer at a time") {
				t.Errorf("worker %d failed: %v", r.worker, r.err)
			}
			failures++
			continue
		}
		for _, id := range r.ids {
			if allIDs[id] {
				t.Errorf("duplicate ID %q from worker %d", id, r.worker)
			}
			allIDs[id] = true
		}
	}

	successes := numWorkers - failures
	if successes == 0 {
		t.Fatalf("all %d workers failed; expected at least 1 success", numWorkers)
	}
	t.Logf("%d/%d workers succeeded (flock contention expected)", successes, numWorkers)

	if len(allIDs) == 0 {
		t.Fatal("no IDs collected from successful workers")
	}

	// Verify issues from successful workers exist and are closed.
	store := openStore(t, beadsDir, "cx")
	for id := range allIDs {
		issue, err := store.GetIssue(t.Context(), id)
		if err != nil {
			t.Errorf("GetIssue(%s): %v", id, err)
			continue
		}
		if issue.Status != types.StatusClosed {
			t.Errorf("issue %s: expected status closed, got %s", id, issue.Status)
		}
		if issue.ClosedAt == nil {
			t.Errorf("issue %s: expected closed_at to be set", id)
		}
	}

	// Verify list counts were monotonically non-decreasing per worker.
	for _, r := range results {
		if r.err != nil {
			continue
		}
		for i := 1; i < len(r.listCounts); i++ {
			if r.listCounts[i] < r.listCounts[i-1] {
				t.Errorf("worker %d: list count decreased from %d to %d at step %d",
					r.worker, r.listCounts[i-1], r.listCounts[i], i)
			}
		}
	}

	stats, err := store.GetStatistics(t.Context())
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}

	t.Logf("created and closed %d issues across %d concurrent workers, %d in DB",
		len(allIDs), numWorkers, stats.TotalIssues)
}
