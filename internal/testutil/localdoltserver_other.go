//go:build !linux && !windows

package testutil

import "syscall"

// localServerSysProcAttr keeps the server in the test's process group (no
// Pdeathsig outside Linux).
func localServerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
