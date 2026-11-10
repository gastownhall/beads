//go:build !windows

package execwin

import (
	"os/exec"
	"syscall"
)

// hide does nothing off Windows: there is no console window to suppress, and
// leaving cmd untouched keeps process creation there byte-identical to not
// calling Hide at all.
func hide(*exec.Cmd) {}

// BackgroundSysProcAttr returns nil off Windows. The long-lived children keep
// their platform's own detaching attributes there (Setsid, Setpgid,
// Pdeathsig), which their packages set; this helper models only Windows.
func BackgroundSysProcAttr() *syscall.SysProcAttr {
	return nil
}
