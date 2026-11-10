//go:build !windows

package execwin

import "os/exec"

// hide does nothing off Windows: there is no console window to suppress, and
// leaving cmd untouched keeps process creation there byte-identical to not
// calling Hide at all.
func hide(*exec.Cmd) {}
