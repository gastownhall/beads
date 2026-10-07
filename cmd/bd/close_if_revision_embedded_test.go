//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestIfRevisionCloseReplaysMoleculeAutoClose pins mc-zndi7.75 item 1 on the
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
func TestIfRevisionCloseReplaysMoleculeAutoClose(t *testing.T) {
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
	// stranded-open root exactly as the unguarded path does.
	rev := bdShowRevision(t, bd, dir, step2.ID)
	bdRunOK(t, bd, dir, "close", step2.ID, "--if-revision", revStr(rev), "--reason", "retry")

	if got := bdShow(t, bd, dir, root.ID); got.Status != types.StatusClosed {
		t.Errorf("expected stranded-open molecule root %s healed by a guarded re-close of the final step, got %s",
			root.ID, got.Status)
	}
}

// TestIfRevisionCloseWarnsOnForcedOpenChildren pins mc-zndi7.75 item 3: the
// "warning: closing X with N open child issue(s) still active" line
// (close.go:194-196) must still print on the guarded direct close route when
// --force waives the engine's open-children refusal, exactly as it does on
// the unguarded batch path.
func TestIfRevisionCloseWarnsOnForcedOpenChildren(t *testing.T) {
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
