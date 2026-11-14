//go:build cgo

package main

import (
	"fmt"
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

// TestEmbeddedIfRevisionCloseAlreadyClosedReportsNoOp pins bd-th8229 (GH#4816)
// on the guarded direct route: a re-close at the issue's current revision with
// a new reason exits 0, keeps the first reason, and reports the no-op as the
// batch routes do, in text and in --json.
func TestEmbeddedIfRevisionCloseAlreadyClosedReportsNoOp(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rgn")

	issue := bdCreate(t, bd, dir, "Guarded reclose", "--type", "task")
	bdClose(t, bd, dir, issue.ID, "--reason", "FIRST")

	out := bdClose(t, bd, dir, issue.ID, "--if-revision", revStr(bdShowRevision(t, bd, dir, issue.ID)), "--reason", "SECOND")
	want := fmt.Sprintf("Already closed %s — Guarded reclose (close reason unchanged: FIRST)", issue.ID)
	if !strings.Contains(out, want) {
		t.Errorf("guarded re-close should report %q, got:\n%s", want, out)
	}
	if strings.Contains(out, "✓ Closed") || strings.Contains(out, "SECOND") {
		t.Errorf("guarded re-close reports a close with the dropped reason:\n%s", out)
	}

	out = bdClose(t, bd, dir, issue.ID, "--if-revision", revStr(bdShowRevision(t, bd, dir, issue.ID)), "--reason", "THIRD", "--json")
	entries := decodeCloseJSON(t, out, []string{issue.ID})
	requireCloseJSONMarksAlreadyClosed(t, entries, map[string]bool{issue.ID: true})
	if got := entries[0].CloseReason; got != "FIRST" {
		t.Errorf("guarded re-close --json close_reason = %q, want %q", got, "FIRST")
	}

	if got := bdShow(t, bd, dir, issue.ID).CloseReason; got != "FIRST" {
		t.Errorf("close_reason = %q after guarded re-closes, want %q (first close wins)", got, "FIRST")
	}
}
