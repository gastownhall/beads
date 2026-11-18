//go:build !windows

package server

import "syscall"

// managedServerSysProcAttr returns nil off Windows, which is exactly what this
// spawn site used before the Windows console fix: the server is a managed
// child that stays in the caller's process group, so signal and cleanup
// behavior there is unchanged.
func managedServerSysProcAttr() *syscall.SysProcAttr {
	return nil
}
