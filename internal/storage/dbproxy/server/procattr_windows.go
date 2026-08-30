//go:build windows

package server

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// managedServerSysProcAttr returns the creation flags for the long-lived
// `dolt sql-server` this package manages.
//
// DETACHED_PROCESS is the point: the server's stdout/stderr are redirected to a
// log file, so it has no use for a console, and giving it none is stronger than
// merely hiding one. bd db-proxy-child (this process) is itself started with
// DETACHED_PROCESS and therefore has no console to share, so without this flag
// the system allocates a fresh console for dolt — which Windows Terminal shows
// as a real window when it is the registered default terminal application, and
// may then persist and replay from C:\Windows\System32 on later launches.
//
// CREATE_NEW_PROCESS_GROUP additionally keeps a Ctrl+C aimed at an ancestor
// from reaching the server mid-write. This mirrors the sibling spawner at
// internal/storage/dbproxy/proxy/endpoint_windows.go.
//
// Detaching does not change lifetime management here: the server is still a
// child this package Waits on and kills by handle via the managed context.
func managedServerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}
