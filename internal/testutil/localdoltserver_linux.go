//go:build linux

package testutil

import "syscall"

// localServerSysProcAttr keeps the server in the test's process group and
// asks the kernel to kill it if the thread that started it dies. Pdeathsig
// is tied to that OS thread, not the process, so it is a safety net only;
// the owner's Stop/Terminate and Bazel's process-group kill are the real
// reapers.
func localServerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
