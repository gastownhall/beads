//go:build !windows

package execwin

import (
	"os/exec"
	"reflect"
	"syscall"
	"testing"
)

// TestHideIsANoOpOffWindows pins AC-2: off Windows, Hide hands back the same
// command and leaves cmd.SysProcAttr as the very pointer the caller set, with
// its contents untouched, and a nil SysProcAttr stays nil. Shared spawn code
// calls Hide on every platform, so anything else would perturb Unix process
// creation (Setpgid/Setsid/Pdeathsig at the long-lived spawn sites).
func TestHideIsANoOpOffWindows(t *testing.T) {
	t.Run("nil SysProcAttr stays nil", func(t *testing.T) {
		cmd := exec.Command(hideFixtureTool)
		if got := Hide(cmd); got != cmd {
			t.Fatalf("Hide returned %p, want the same *exec.Cmd %p", got, cmd)
		}
		if cmd.SysProcAttr != nil {
			t.Fatalf("Hide set SysProcAttr = %+v off windows, want nil", cmd.SysProcAttr)
		}
	})
	t.Run("non-nil SysProcAttr is the identical pointer, unchanged", func(t *testing.T) {
		attr := &syscall.SysProcAttr{}
		setFixtureField(attr)
		want := *attr
		cmd := exec.Command(hideFixtureTool)
		cmd.SysProcAttr = attr
		for i := 0; i < 2; i++ {
			if got := Hide(cmd); got != cmd {
				t.Fatalf("Hide call %d returned %p, want the same *exec.Cmd %p", i+1, got, cmd)
			}
		}
		if cmd.SysProcAttr != attr {
			t.Fatalf("Hide replaced SysProcAttr %p with %p", attr, cmd.SysProcAttr)
		}
		if !reflect.DeepEqual(*attr, want) {
			t.Fatalf("Hide changed SysProcAttr contents: got %+v, want %+v", *attr, want)
		}
	})
}

// setFixtureField sets the first settable bool field of attr, so the
// unchanged-contents check compares a non-zero value on every platform whose
// SysProcAttr has one (Setpgid on Unix) without naming platform fields here.
func setFixtureField(attr *syscall.SysProcAttr) {
	v := reflect.ValueOf(attr).Elem()
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.CanSet() && f.Kind() == reflect.Bool {
			f.SetBool(true)
			return
		}
	}
}
