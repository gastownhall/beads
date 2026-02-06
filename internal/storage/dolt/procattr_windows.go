//go:build windows

package dolt

import "os/exec"

// Windows does not support Setpgid; leave default process attributes.
func setDoltServerSysProcAttr(cmd *exec.Cmd) {
	// no-op
	_ = cmd
}
