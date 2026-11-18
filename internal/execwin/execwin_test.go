package execwin

import (
	"os/exec"
	"testing"
)

// hideFixtureTool names a binary that does not exist: these tests only build
// *exec.Cmd values for Hide to mutate and never start them.
const hideFixtureTool = "execwin-fixture-never-started"

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
	cmd := exec.Command(hideFixtureTool, "version")
	if got := Hide(cmd); got != cmd {
		t.Fatalf("Hide returned a different *exec.Cmd (%p) than it was given (%p)", got, cmd)
	}
}
