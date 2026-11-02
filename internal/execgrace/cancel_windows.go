//go:build windows

package execgrace

import "os/exec"

// setGracefulCancel keeps os/exec's default cancellation (Process.Kill):
// Windows has no SIGTERM to deliver to a console child.
func setGracefulCancel(*exec.Cmd) {}
