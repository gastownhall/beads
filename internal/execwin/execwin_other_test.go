//go:build !windows

package execwin

import (
	"bytes"
	"os/exec"
	"syscall"
	"testing"
)

// TestHideLeavesSysProcAttrNil pins the no-op guarantee: Hide is called from
// shared (non-build-tagged) code, so it must not perturb process creation off
// Windows. A nil SysProcAttr is exactly what those call sites had before.
func TestHideLeavesSysProcAttrNil(t *testing.T) {
	cmd := Hide(exec.Command("true"))
	if cmd.SysProcAttr != nil {
		t.Fatalf("Hide set SysProcAttr = %+v off windows, want nil", cmd.SysProcAttr)
	}
}

// TestHideDoesNotOverwriteExistingAttr: several spawn sites set their own
// SysProcAttr (Setsid/Setpgid on Unix). Off Windows Hide must leave the very
// same structure in place, with its values, and the rest of the command as
// the caller configured it.
func TestHideDoesNotOverwriteExistingAttr(t *testing.T) {
	existing := &syscall.SysProcAttr{Setpgid: true, Setsid: true}
	var stdout bytes.Buffer
	cmd := exec.Command("dolt", "version")
	cmd.SysProcAttr = existing
	cmd.Dir = "/nonexistent-dir"
	cmd.Env = []string{"A=1"}
	cmd.Stdout = &stdout
	path, args := cmd.Path, append([]string(nil), cmd.Args...)

	if got := Hide(cmd); got != cmd {
		t.Fatalf("Hide returned %p, want the command it was given (%p)", got, cmd)
	}
	if cmd.SysProcAttr != existing {
		t.Fatalf("Hide replaced a caller-set SysProcAttr (%p) with %p", existing, cmd.SysProcAttr)
	}
	if !existing.Setpgid || !existing.Setsid {
		t.Fatalf("Hide changed the caller's SysProcAttr values: %+v", *existing)
	}
	if cmd.Path != path || len(cmd.Args) != len(args) || cmd.Args[0] != args[0] || cmd.Args[1] != args[1] ||
		cmd.Dir != "/nonexistent-dir" || len(cmd.Env) != 1 || cmd.Env[0] != "A=1" || cmd.Stdout != &stdout {
		t.Fatalf("Hide changed the command's configuration: path=%q args=%q dir=%q env=%q",
			cmd.Path, cmd.Args, cmd.Dir, cmd.Env)
	}
}
