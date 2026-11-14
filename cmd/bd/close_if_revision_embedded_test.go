//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedIfRevisionCloseReplaysMoleculeAutoClose pins mc-zndi7.75 item 1 on the
// embedded/direct --if-revision close route: runCloseDirectIfRevision bypasses
// `bd close`'s batch architecture entirely (A8, beads#4682 — BatchCloseItem
// carries no per-item ExpectedVersion), so it must re-drive molecule auto-close
// itself instead of silently dropping it.
//
// The setup mirrors close_embedded_test.go's
// "close_already_closed_replays_molecule_auto_close": strand a molecule root
// open by reopening ONLY the root after both steps are genuinely closed (the
// state a crash between the final step's close and its root auto-close would
// leave), then re-close the final step — this time guarded — and require the
// stranded-open root to heal.
func TestEmbeddedIfRevisionCloseReplaysMoleculeAutoClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rmg")

	root := bdCreate(t, bd, dir, "Guarded molecule root", "--type", "epic", "--labels", "template")
	step1 := bdCreate(t, bd, dir, "Guarded molecule step one", "--type", "task", "--parent", root.ID)
	step2 := bdCreate(t, bd, dir, "Guarded molecule step two", "--type", "task", "--parent", root.ID)

	bdClose(t, bd, dir, step1.ID, "--reason", "one")
	bdClose(t, bd, dir, step2.ID, "--reason", "two")
	if got := bdShow(t, bd, dir, root.ID); got.Status != types.StatusClosed {
		t.Fatalf("precondition: expected molecule root %s auto-closed after final step, got %s", root.ID, got.Status)
	}

	bdReopen(t, bd, dir, root.ID)
	if got := bdShow(t, bd, dir, root.ID); got.Status != types.StatusOpen {
		t.Fatalf("precondition: expected molecule root %s reopened, got %s", root.ID, got.Status)
	}

	// Re-close the already-closed final step, this time GUARDED. The idempotent
	// guarded re-close must replay molecule auto-close and heal the
	// stranded-open root exactly as the unguarded path does, and its --json
	// report names the root it closed.
	rev := bdShowRevision(t, bd, dir, step2.ID)
	out := bdClose(t, bd, dir, step2.ID, "--if-revision", revStr(rev), "--reason", "retry", "--json")
	requireCloseJSONReportsAutoClosedMolecule(t, out, []string{step2.ID}, root.ID)

	if got := bdShow(t, bd, dir, root.ID); got.Status != types.StatusClosed {
		t.Errorf("expected stranded-open molecule root %s healed by a guarded re-close of the final step, got %s",
			root.ID, got.Status)
	}
}

// TestEmbeddedIfRevisionCloseReportsAlreadyClosedNoOp pins the guarded
// direct route's half of bd-th8229 / #4816: an already-closed issue
// re-closed under --if-revision with a different reason must be reported
// like the batch routes report it — as an already-closed no-op with the
// stored first reason, in text and in --json — while the stored reason
// stays put.
func TestEmbeddedIfRevisionCloseReportsAlreadyClosedNoOp(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rca")

	issue := bdCreate(t, bd, dir, "Guarded already closed", "--type", "task")
	bdClose(t, bd, dir, issue.ID, "--reason", "FIRST")

	// The revision the closed row answers with now; the no-op below must not
	// move it, so the same value guards the --json re-close too.
	rev := bdShowRevision(t, bd, dir, issue.ID)

	out := bdClose(t, bd, dir, issue.ID, "--if-revision", revStr(rev), "--reason", "SECOND")
	if !strings.Contains(out, "Already closed ") {
		t.Errorf("guarded re-close must report the no-op as already closed, got:\n%s", out)
	}
	if !strings.Contains(out, "(close reason unchanged: FIRST)") {
		t.Errorf("guarded re-close must show the stored first reason, got:\n%s", out)
	}
	if strings.Contains(out, "SECOND") {
		t.Errorf("guarded re-close must not present the dropped reason as applied, got:\n%s", out)
	}
	if got := bdShow(t, bd, dir, issue.ID).CloseReason; got != "FIRST" {
		t.Errorf("close_reason = %q after guarded re-close, want %q", got, "FIRST")
	}

	// The --json twin of the same no-op.
	outJSON := bdClose(t, bd, dir, issue.ID, "--if-revision", revStr(rev), "--json", "--reason", "THIRD")
	entry := decodeCloseJSON(t, outJSON, []string{issue.ID})[0]
	if entry.AlreadyClosed == nil || !*entry.AlreadyClosed {
		t.Errorf("guarded re-close --json entry must carry already_closed: true, got %+v", entry.AlreadyClosed)
	}
	if entry.CloseReason != "FIRST" {
		t.Errorf("guarded re-close --json close_reason = %q, want the stored %q", entry.CloseReason, "FIRST")
	}
}

// TestEmbeddedIfRevisionCloseWarnsOnForcedOpenChildren pins mc-zndi7.75 item 3: the
// "warning: closing X with N open child issue(s) still active" line
// (close.go:194-196) must still print on the guarded direct close route when
// --force waives the engine's open-children refusal, exactly as it does on
// the unguarded batch path.
func TestEmbeddedIfRevisionCloseWarnsOnForcedOpenChildren(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rmw")

	parent := bdCreate(t, bd, dir, "Guarded parent with open child", "--type", "task")
	_ = bdCreate(t, bd, dir, "Open child", "--type", "task", "--parent", parent.ID)

	rev := bdShowRevision(t, bd, dir, parent.ID)
	out := bdRunOK(t, bd, dir, "close", parent.ID, "--if-revision", revStr(rev), "--force")
	if !strings.Contains(out, "open child issue(s) still active") {
		t.Errorf("guarded forced close with an open child did not warn:\n%s", out)
	}

	if got := bdShow(t, bd, dir, parent.ID); got.Status != types.StatusClosed {
		t.Errorf("guarded forced close did not apply: status = %s, want closed", got.Status)
	}
}
