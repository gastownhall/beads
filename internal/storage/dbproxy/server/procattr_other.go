//go:build !windows

package server

import "syscall"

// managedServerSysProcAttr returns nil off Windows, which is exactly what this
// spawn site used before the Windows console-allocation fix. There is no
// console to detach from, and the server is a managed child that should stay in
// the caller's process group so existing signal and cleanup behavior is
// unchanged.
func managedServerSysProcAttr() *syscall.SysProcAttr {
	return nil
}
