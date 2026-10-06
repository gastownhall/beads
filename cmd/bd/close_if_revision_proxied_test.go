//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedIfRevisionCloseReplaysMoleculeAutoClose is
// TestIfRevisionCloseReplaysMoleculeAutoClose's proxied-server twin, pinning
// mc-zndi7.75 item 1 on the route `bd serve` actually runs:
// runCloseProxiedIfRevision bypasses the batch entirely (A8, beads#4682), so
// it must re-drive molecule auto-close itself via its own post-close unit of
// work, exactly as closeProxiedRunPostClose does for the unguarded batch
// route.
func TestProxiedIfRevisionCloseReplaysMoleculeAutoClose(t *testing.T) {
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
	// the stranded-open root exactly as the unguarded path does.
	rev := bdProxiedShowRevision(t, bd, p.dir, step2.ID)
	bdProxiedClose(t, bd, p.dir, step2.ID, "--if-revision", proxiedRevStr(rev), "--reason", "retry")

	if got := readStatus(t, db, root.ID); got != types.StatusClosed {
		t.Errorf("expected stranded-open molecule root %s healed by a guarded proxied re-close of the final step, got %q",
			root.ID, got)
	}
}

// TestProxiedIfRevisionCloseWarnsOnForcedOpenChildren is
// TestIfRevisionCloseWarnsOnForcedOpenChildren's proxied-server twin, pinning
// mc-zndi7.75 item 3 on the proxied route.
func TestProxiedIfRevisionCloseWarnsOnForcedOpenChildren(t *testing.T) {
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
