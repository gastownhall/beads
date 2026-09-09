package execwin

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestHideNil guards the convenience contract: Hide is meant to be wrapped
// around an exec.Command call inline, so it must tolerate a nil command rather
// than panicking at the call site.
func TestHideNil(t *testing.T) {
	if got := Hide(nil); got != nil {
		t.Fatalf("Hide(nil) = %v, want nil", got)
	}
}

// TestHideReturnsSameCommand documents that Hide mutates in place and hands the
// command back, which is what makes execwin.Hide(exec.Command(...)).Output()
// legal.
func TestHideReturnsSameCommand(t *testing.T) {
	cmd := exec.Command("go", "version")
	if got := Hide(cmd); got != cmd {
		t.Fatalf("Hide returned a different *exec.Cmd (%p) than it was given (%p)", got, cmd)
	}
}

// TestHideDoesNotOverwriteExistingAttr is the important one. Several spawn
// sites deliberately set their own SysProcAttr (Setsid/Setpgid on Unix,
// DETACHED_PROCESS on Windows). Hide must never clobber those — doing so would
// silently un-detach a server that is supposed to outlive its parent.
func TestHideDoesNotOverwriteExistingAttr(t *testing.T) {
	existing := &syscall.SysProcAttr{}
	cmd := exec.Command("go", "version")
	cmd.SysProcAttr = existing

	Hide(cmd)

	if cmd.SysProcAttr != existing {
		t.Fatalf("Hide replaced a caller-set SysProcAttr (%p) with %p", existing, cmd.SysProcAttr)
	}
}
