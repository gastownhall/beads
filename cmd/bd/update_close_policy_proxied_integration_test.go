//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// countAttemptRefusals counts the refusals printed for one issue, not the run's
// failure summary. proxiedUpdateFailure prints the policy error flush left;
// reportUpdateFailures reprints the same text indented behind the issue ID, so
// "unindented" separates the two.
func countAttemptRefusals(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "cannot close ") && !strings.HasPrefix(line, " ") {
			n++
		}
	}
	return n
}

// TestProxiedServerUpdateClosePolicy pins close policy on the PROXIED update
// path — `bd update --status closed` against a dolt sql-server.
//
// It exists because the two halves of this behavior were written against each
// other's stale shape. #5206 added the policy and its two proxied refusal arms
// while this path still ran a bespoke read-merge-write of its own, and the only
// proxied coverage it shipped was a unit test on the spec that path built —
// which passes whether or not the refusal ever reaches a user. These assertions
// are the only proxied close-policy coverage that watches a user-visible
// boundary rather than an internal request.
//
// So the assertions here are end-to-end and deliberately narrow:
//
//   - the sentinels still match through the contract's wrapping, so the
//     boundary prints the close-path copy rather than the generic
//     "Error updating" arm;
//   - the refusal is TERMINAL, printed exactly once. A policy error is not a
//     serialization failure, so the contract's retry loop wraps it Permanent:
//     no commit, no retry. If a later change let it propagate as a retryable
//     error instead, the backoff would redo the attempt for its whole budget
//     and this count would climb;
//   - it exits 1, never ExitGuardMismatch/13, which is reserved for a stale
//     --if-assignee/--if-status precondition;
//   - nothing is written; and --force overrides, on the same path.
func TestProxiedServerUpdateClosePolicy(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("live_blocker_refuses_once", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "upcb")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker")
		blocked := bdProxiedCreate(t, bd, p.dir, "Blocked", "--deps", "depends-on:"+blocker.ID)

		out, code := bdProxiedUpdateFailCode(t, bd, p.dir, blocked.ID, "--status", "closed")
		if code != 1 {
			t.Errorf("exit code = %d, want 1 (a policy refusal is not a guard mismatch); output:\n%s", code, out)
		}
		if !strings.Contains(out, "cannot close blocked issue") {
			t.Errorf("refusal did not use the close-path copy; output:\n%s", out)
		}
		if !strings.Contains(out, "(use --force to override)") {
			t.Errorf("refusal did not name the override; output:\n%s", out)
		}
		if n := countAttemptRefusals(out); n != 1 {
			t.Errorf("attempt printed the refusal %d times, want exactly 1 — a policy refusal must not be retried; output:\n%s", n, out)
		}
		db := openProxiedDB(t, p)
		if got := readStatus(t, db, blocked.ID); got == types.StatusClosed {
			t.Error("a refused status update must write nothing")
		}
	})

	t.Run("live_blocker_forced_crosses", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "upcf")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker force")
		blocked := bdProxiedCreate(t, bd, p.dir, "Blocked force", "--deps", "depends-on:"+blocker.ID)

		bdProxiedUpdateOne(t, bd, p.dir, blocked.ID, "--status", "closed", "--force")
		db := openProxiedDB(t, p)
		if got := readStatus(t, db, blocked.ID); got != types.StatusClosed {
			t.Errorf("status = %q, want closed: --force must override close policy here too", got)
		}
	})

	t.Run("open_children_refuse_once", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "upcc")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent")
		child := bdProxiedCreate(t, bd, p.dir, "Child")
		bdProxiedDep(t, bd, p.dir, "add", child.ID, parent.ID, "--type", "parent-child")

		out, code := bdProxiedUpdateFailCode(t, bd, p.dir, parent.ID, "--status", "closed")
		if code != 1 {
			t.Errorf("exit code = %d, want 1; output:\n%s", code, out)
		}
		if !strings.Contains(out, "open child issue(s)") {
			t.Errorf("refusal did not use the close-path copy; output:\n%s", out)
		}
		if n := countAttemptRefusals(out); n != 1 {
			t.Errorf("attempt printed the refusal %d times, want exactly 1; output:\n%s", n, out)
		}
		db := openProxiedDB(t, p)
		if got := readStatus(t, db, parent.ID); got == types.StatusClosed {
			t.Error("a refused status update must write nothing")
		}
	})

	t.Run("open_children_forced_crosses", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "upcg")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent force")
		child := bdProxiedCreate(t, bd, p.dir, "Child force")
		bdProxiedDep(t, bd, p.dir, "add", child.ID, parent.ID, "--type", "parent-child")

		bdProxiedUpdateOne(t, bd, p.dir, parent.ID, "--status", "closed", "--force")
		db := openProxiedDB(t, p)
		if got := readStatus(t, db, parent.ID); got != types.StatusClosed {
			t.Errorf("status = %q, want closed", got)
		}
	})
}
