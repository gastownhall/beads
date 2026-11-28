//go:build !linux && !windows

package testutil

import "syscall"

// localServerSysProcAttr keeps the server in the test's process group (no
// Pdeathsig outside Linux: a server whose owner dies without stopping it runs
// on until the next process's sweepDeadLocalServerRoots kills it).
func localServerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
