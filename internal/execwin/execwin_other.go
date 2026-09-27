//go:build !windows

package execwin

import "syscall"

// NoWindowSysProcAttr returns nil off Windows. There is no console-window
// concept to suppress, and returning nil keeps Hide a strict no-op so that
// non-Windows process creation is byte-identical to not calling it.
func NoWindowSysProcAttr() *syscall.SysProcAttr {
	return nil
}
