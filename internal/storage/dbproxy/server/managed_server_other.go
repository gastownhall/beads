//go:build !windows

package server

import "syscall"

// managedServerSysProcAttr returns nil off Windows: the managed server's
// spawn site is unchanged there, with whatever platform attributes the
// caller's lifecycle relies on (the proxy supervises and reaps the child
// itself, so Unix needs none here).
func managedServerSysProcAttr() *syscall.SysProcAttr {
	return nil
}
