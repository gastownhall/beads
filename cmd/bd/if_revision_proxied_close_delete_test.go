//go:build cgo

package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerIfRevisionCloseMismatchRefuses pins mc-zndi7.76 (gap 4 / mutant
// MD2): close_if_revision.go's proxied route wires ExpectedVersion into both
// of its SingleIssueUpdate calls (the plain close and the --force one), but
// until now nothing on the proxied route ever gave it a STALE token --
// TestProxiedServerIfRevisionCloseReplaysMoleculeAutoClose and
// TestProxiedServerIfRevisionCloseWarnsOnForcedOpenChildren only ever close with the
// current revision. A stale guard must refuse the close outright, before any
// write, on the real shared Dolt server.
func TestProxiedServerIfRevisionCloseMismatchRefuses(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ivcm")

	issue := bdProxiedCreate(t, bd, p.dir, "Guarded proxied close vs stale revision")
	rev0 := bdProxiedShowRevision(t, bd, p.dir, issue.ID)
	bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--notes", "bump the revision out from under the guard")

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "close", issue.ID, "--if-revision", proxiedRevStr(rev0))
	if err == nil {
		t.Fatalf("stale --if-revision close should have failed, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("stale --if-revision close failed without an exit code: %v", err)
	}
	out := stdout + stderr
	if ee.ExitCode() != ExitGuardMismatch {
		t.Errorf("stale --if-revision close exit code = %d, want %d\n%s", ee.ExitCode(), ExitGuardMismatch, out)
	}
	if !strings.Contains(out, "revision mismatch") {
		t.Errorf("stale --if-revision close error should say \"revision mismatch\", got:\n%s", out)
	}
	db := openProxiedDB(t, p)
	if got := readStatus(t, db, issue.ID); got == types.StatusClosed {
		t.Errorf("stale --if-revision close must not have applied: status = %q", got)
	}
}

// TestProxiedServerIfRevisionDeleteMatchAndMismatch pins mc-zndi7.76 (gap 4 / mutant
// MD4): delete_proxied_server.go wires ExpectedVersion: ifRevision into the
// issueops.DeleteRequest it sends to the real Deleter, but no proxied test
// gave --if-revision to delete at all before now. A matching guard deletes
// the row; a stale guard refuses, and the row survives.
func TestProxiedServerIfRevisionDeleteMatchAndMismatch(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ivdm")
	db := openProxiedDB(t, p)

	t.Run("mismatch_refuses", func(t *testing.T) {
		t.Parallel()
		issue := bdProxiedCreate(t, bd, p.dir, "Guarded proxied delete vs stale revision")
		rev0 := bdProxiedShowRevision(t, bd, p.dir, issue.ID)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--notes", "bump the revision out from under the guard")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "delete", issue.ID, "--force", "--if-revision", proxiedRevStr(rev0))
		if err == nil {
			t.Fatalf("stale --if-revision delete should have failed, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("stale --if-revision delete failed without an exit code: %v", err)
		}
		out := stdout + stderr
		if ee.ExitCode() != ExitGuardMismatch {
			t.Errorf("stale --if-revision delete exit code = %d, want %d\n%s", ee.ExitCode(), ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("stale --if-revision delete error should say \"revision mismatch\", got:\n%s", out)
		}
		row := readStatus(t, db, issue.ID)
		if row == "" {
			t.Errorf("stale --if-revision delete must not have applied: row %s is gone", issue.ID)
		}
	})

	t.Run("match_applies", func(t *testing.T) {
		t.Parallel()
		issue := bdProxiedCreate(t, bd, p.dir, "Guarded proxied delete vs current revision")
		rev := bdProxiedShowRevision(t, bd, p.dir, issue.ID)

		bdProxiedDelete(t, bd, p.dir, issue.ID, "--force", "--if-revision", proxiedRevStr(rev))
		assertRowAbsent(t, db, "issues", issue.ID)
	})
}

// TestProxiedIfRevisionTargetGoneIsPreconditionFailed is the proxied-route
// twin of TestIfRevisionPreflightGoneIsPreconditionFailedOnEveryVerb
// (ga-vnycm2.10): a guarded close, update or assign whose row a concurrent
// `bd delete` already removed must report precondition_failed /
// ExitGuardMismatch, whichever step (the advisory pre-read or the guarded
// write) first observes the row gone — never an unclassified "not found"
// exit 1. TestProxiedDeleteIfRevisionSingleWinner's delete_vs_close and
// delete_vs_update race exactly this; here the winning delete runs first, so
// the outcome is deterministic.
func TestProxiedIfRevisionTargetGoneIsPreconditionFailed(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ivtg")

	for _, tc := range []struct {
		verb string
		args func(id, rev string) []string
	}{
		{"close", func(id, rev string) []string { return []string{"close", id, "--if-revision", rev, "--reason", "gone"} }},
		{"update", func(id, rev string) []string {
			return []string{"update", id, "--if-revision", rev, "--spec-id", "gone"}
		}},
		{"assign", func(id, rev string) []string { return []string{"assign", id, "someone", "--if-revision", rev} }},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			t.Parallel()
			issue := bdProxiedCreate(t, bd, p.dir, "Proxied guarded "+tc.verb+" vs deleted row")
			rev0 := bdProxiedShowRevision(t, bd, p.dir, issue.ID)
			bdProxiedDelete(t, bd, p.dir, issue.ID, "--force")

			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, tc.args(issue.ID, proxiedRevStr(rev0))...)
			if err == nil {
				t.Fatalf("--if-revision %s of a deleted row should have failed, got:\nstdout:\n%s\nstderr:\n%s", tc.verb, stdout, stderr)
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("--if-revision %s of a deleted row failed without an exit code: %v", tc.verb, err)
			}
			out := stdout + stderr
			if ee.ExitCode() != ExitGuardMismatch {
				t.Errorf("--if-revision %s of a deleted row exit code = %d, want %d\n%s", tc.verb, ee.ExitCode(), ExitGuardMismatch, out)
			}
			if !strings.Contains(out, "precondition failed: issue no longer exists") {
				t.Errorf("--if-revision %s of a deleted row should say \"precondition failed: issue no longer exists\", got:\n%s", tc.verb, out)
			}
		})
	}
}
