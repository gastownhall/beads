//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// if_revision_reopen_embedded_test.go pins the bee-ghosttrack maintainer-review
// blocker on `bd reopen --if-revision`'s direct (non-proxied) route: reopen.go
// used to run its "issue.Status == types.StatusOpen" already-open short-circuit
// unconditionally, BEFORE ops.Reopen ever got a chance to evaluate
// ExpectedVersion (ExecuteReopen -> CheckVersionInTx). That meant a stale
// --if-revision guard against an already-open issue silently returned the
// "already open" no-op (exit 0) instead of the precondition_failed refusal
// (exit 13) every other guarded verb gives for a stale token -- see
// TestEmbeddedGCConditionalMatcherDecode's reopen_open row for the same
// refusal pinned from the gc-matcher angle.
//
// TestProxiedServerIfRevisionGuardReopen
// (if_revision_proxied_integration_test.go) is this file's proxied-server-leg
// twin. The TestEmbedded name is what puts this test in a CI lane
// (.github/scripts/embedded-test-shard.sh); under any other name it skips
// everywhere.
func TestEmbeddedIfRevisionReopenGuardDirect(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "irr")

	t.Run("already_open_mismatch_refuses", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded reopen (already open)", "--type", "task")
		rev := bdShowRevision(t, bd, dir, issue.ID)
		stale := rev + 1_000_000

		out, code := bdRunFailCode(t, bd, dir, "reopen", issue.ID, "--if-revision", revStr(stale), "--json")
		if code != ExitGuardMismatch {
			t.Fatalf("stale --if-revision reopen of an already-open issue exit = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Status != types.StatusOpen {
			t.Errorf("stale --if-revision reopen of an already-open issue changed status: %s", got.Status)
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev {
			t.Errorf("stale --if-revision reopen of an already-open issue advanced the revision: %d, want unchanged %d", got, rev)
		}
	})

	t.Run("already_open_match_is_noop", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded reopen (already open, matching)", "--type", "task")
		rev := bdShowRevision(t, bd, dir, issue.ID)

		out := bdRunOK(t, bd, dir, "reopen", issue.ID, "--if-revision", revStr(rev))
		if !strings.Contains(out, issue.ID+" is already open") {
			t.Errorf("matching --if-revision reopen of an already-open issue lacks the already-open no-op line:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Status != types.StatusOpen {
			t.Errorf("matching --if-revision reopen of an already-open issue changed status: %s\noutput:\n%s", got.Status, out)
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev {
			t.Errorf("matching --if-revision reopen of an already-open issue advanced the revision: %d, want unchanged %d", got, rev)
		}
	})

	t.Run("closed_mismatch_refuses", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded reopen (closed, mismatch)", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		rev := bdShowRevision(t, bd, dir, issue.ID)
		stale := rev + 1_000_000

		out, code := bdRunFailCode(t, bd, dir, "reopen", issue.ID, "--if-revision", revStr(stale), "--json")
		if code != ExitGuardMismatch {
			t.Fatalf("stale --if-revision reopen of a closed issue exit = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Status != types.StatusClosed {
			t.Errorf("stale --if-revision reopen of a closed issue changed status: %s", got.Status)
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev {
			t.Errorf("stale --if-revision reopen of a closed issue advanced the revision: %d, want unchanged %d", got, rev)
		}
	})

	t.Run("closed_match_reopens", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded reopen (closed, matching)", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		rev := bdShowRevision(t, bd, dir, issue.ID)

		bdRunOK(t, bd, dir, "reopen", issue.ID, "--if-revision", revStr(rev))
		if got := bdShow(t, bd, dir, issue.ID); got.Status != types.StatusOpen {
			t.Fatalf("matching --if-revision reopen of a closed issue did not reopen: status = %s", got.Status)
		}
	})
}
