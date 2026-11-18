//go:build !windows

package execwin

import "os/exec"

// Hide returns cmd unchanged. There is no console window to suppress off
// Windows, so cmd.SysProcAttr is left exactly as the caller set it (a nil
// SysProcAttr stays nil) and process creation is byte-identical to not
// calling Hide at all.
func Hide(cmd *exec.Cmd) *exec.Cmd {
	return cmd
}
