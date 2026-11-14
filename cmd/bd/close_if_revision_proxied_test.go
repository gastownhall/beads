//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerIfRevisionCloseReplaysMoleculeAutoClose is
// TestEmbeddedIfRevisionCloseReplaysMoleculeAutoClose's proxied-server twin, pinning
// mc-zndi7.75 item 1 on the route `bd serve` actually runs:
// runCloseProxiedIfRevision bypasses the batch entirely (A8, beads#4682), so
// it must re-drive molecule auto-close itself via its own post-close unit of
// work, exactly as closeProxiedRunPostClose does for the unguarded batch
// route.
func TestProxiedServerIfRevisionCloseReplaysMoleculeAutoClose(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ivc")

	root := bdProxiedCreate(t, bd, p.dir, "Guarded proxied molecule root", "-t", "epic", "--labels", "template")
	step1 := bdProxiedCreate(t, bd, p.dir, "Guarded proxied molecule step one", "--parent", root.ID)
	step2 := bdProxiedCreate(t, bd, p.dir, "Guarded proxied molecule step two", "--parent", root.ID)

	bdProxiedClose(t, bd, p.dir, step1.ID, "--reason", "one")
	bdProxiedClose(t, bd, p.dir, step2.ID, "--reason", "two")
	db := openProxiedDB(t, p)
	if got := readStatus(t, db, root.ID); got != types.StatusClosed {
		t.Fatalf("precondition: expected molecule root %s auto-closed after final step, got %q", root.ID, got)
	}

	bdProxiedReopen(t, bd, p.dir, root.ID)
	if got := readStatus(t, db, root.ID); got != types.StatusOpen {
		t.Fatalf("precondition: expected molecule root %s reopened, got %q", root.ID, got)
	}

	// Re-close the already-closed final step, this time GUARDED. The
	// idempotent guarded re-close must replay molecule auto-close and heal
	// the stranded-open root exactly as the unguarded path does, and its
	// --json report names the root it closed.
	rev := bdProxiedShowRevision(t, bd, p.dir, step2.ID)
	out := bdProxiedClose(t, bd, p.dir, step2.ID, "--if-revision", proxiedRevStr(rev), "--reason", "retry", "--json")
	requireCloseJSONReportsAutoClosedMolecule(t, out, []string{step2.ID}, root.ID)

	if got := readStatus(t, db, root.ID); got != types.StatusClosed {
		t.Errorf("expected stranded-open molecule root %s healed by a guarded proxied re-close of the final step, got %q",
			root.ID, got)
	}
}

// TestProxiedServerIfRevisionCloseReportsAlreadyClosedNoOp is
// TestEmbeddedIfRevisionCloseReportsAlreadyClosedNoOp's proxied-server twin:
// the guarded proxied re-close of an already-closed issue reports the no-op
// the way every other close route does (bd-th8229 / #4816), in text and in
// --json, with the stored first reason.
func TestProxiedServerIfRevisionCloseReportsAlreadyClosedNoOp(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ivn")

	issue := bdProxiedCreate(t, bd, p.dir, "Guarded proxied already closed")
	bdProxiedClose(t, bd, p.dir, issue.ID, "--reason", "FIRST")

	rev := bdProxiedShowRevision(t, bd, p.dir, issue.ID)

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "close", issue.ID, "--if-revision", proxiedRevStr(rev), "--reason", "SECOND")
	if err != nil {
		t.Fatalf("guarded re-close of an already-closed issue is an idempotent no-op and must exit 0: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Already closed ") {
		t.Errorf("guarded proxied re-close must report the no-op as already closed, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "(close reason unchanged: FIRST)") {
		t.Errorf("guarded proxied re-close must show the stored first reason, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "SECOND") {
		t.Errorf("guarded proxied re-close must not present the dropped reason as applied, got:\n%s", stdout)
	}
	db := openProxiedDB(t, p)
	if got := readCloseReason(t, db, issue.ID); got != "FIRST" {
		t.Errorf("close_reason = %q after guarded proxied re-close, want %q", got, "FIRST")
	}

	// The --json twin of the same no-op; the row did not move, so the same
	// revision still guards it.
	stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "close", issue.ID, "--if-revision", proxiedRevStr(rev), "--json", "--reason", "THIRD")
	if err != nil {
		t.Fatalf("guarded proxied re-close --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	entry := decodeCloseJSON(t, stdout, []string{issue.ID})[0]
	if entry.AlreadyClosed == nil || !*entry.AlreadyClosed {
		t.Errorf("guarded proxied re-close --json entry must carry already_closed: true, got %+v", entry.AlreadyClosed)
	}
	if entry.CloseReason != "FIRST" {
		t.Errorf("guarded proxied re-close --json close_reason = %q, want the stored %q", entry.CloseReason, "FIRST")
	}
}

// TestProxiedServerIfRevisionCloseWarnsOnForcedOpenChildren is
// TestEmbeddedIfRevisionCloseWarnsOnForcedOpenChildren's proxied-server twin, pinning
// mc-zndi7.75 item 3 on the proxied route.
func TestProxiedServerIfRevisionCloseWarnsOnForcedOpenChildren(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ivw")

	parent := bdProxiedCreate(t, bd, p.dir, "Guarded proxied parent with open child")
	_ = bdProxiedCreate(t, bd, p.dir, "Open proxied child", "--parent", parent.ID)

	rev := bdProxiedShowRevision(t, bd, p.dir, parent.ID)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "close", parent.ID, "--if-revision", proxiedRevStr(rev), "--force")
	if err != nil {
		t.Fatalf("bd close --force --if-revision failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "open child issue(s) still active") {
		t.Errorf("guarded proxied forced close with an open child did not warn:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	db := openProxiedDB(t, p)
	if got := readStatus(t, db, parent.ID); got != types.StatusClosed {
		t.Errorf("guarded proxied forced close did not apply: status = %q, want closed", got)
	}
}
