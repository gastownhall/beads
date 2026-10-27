//go:build windows

package server

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// managedServerSysProcAttr returns the creation flags for the long-lived
// `dolt sql-server` this package manages.
//
// CREATE_NO_WINDOW gives the server a console of its own that is never
// displayed. Without it, a parent that has no console to share (an MCP server,
// an agent harness with redirected stdio) makes the system allocate a visible
// one for dolt, which Windows Terminal shows as a real window when it is the
// registered default terminal application, and may then persist and replay
// from C:\Windows\System32 on later launches.
//
// DETACHED_PROCESS is NOT a substitute, even though the server's stdout/stderr
// already go to a log file. It leaves dolt with no console at all, and dolt
// runs `uname -s`, `uname -r` and `uname -m` on every start; with Git for
// Windows on PATH each of those is a console program that then gets a fresh,
// visible console — three Windows Terminal windows per server start. A hidden
// console is inherited by those children instead.
//
// CREATE_NEW_PROCESS_GROUP additionally keeps a Ctrl+C aimed at an ancestor
// from reaching the server mid-write. This mirrors the sibling spawner at
// internal/storage/dbproxy/proxy/endpoint_windows.go.
//
// None of this changes lifetime management here: the server is still a child
// this package Waits on and kills by handle via the managed context.
func managedServerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
}
