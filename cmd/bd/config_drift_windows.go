//go:build windows

package main

import "golang.org/x/sys/windows"

// pidAlive reports whether a process with the given PID is still running.
//
// A bare OpenProcess / os.FindProcess success is NOT sufficient on Windows.
// The kernel process object of a terminated process lingers, and stays
// openable, for as long as any handle to it remains open anywhere on the
// system (a parent that never Wait()ed, an AV/EDR scanner, a debugger, Task
// Manager). So an existence-only probe reports a dead server as alive for a
// window after it exits.
//
// Instead, wait on the process handle with a zero timeout: a terminated
// process's handle is signaled (WaitForSingleObject returns WAIT_OBJECT_0),
// a running one times out (WAIT_TIMEOUT). This distinguishes live from dead
// even when OpenProcess still succeeds on a lingering handle.
func pidAlive(pid int) bool {
	processHandle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// No such process, or we may not open it.
		return false
	}
	defer func() { _ = windows.CloseHandle(processHandle) }()

	waitResult, err := windows.WaitForSingleObject(processHandle, 0)
	if err != nil {
		return false
	}
	return waitResult == uint32(windows.WAIT_TIMEOUT)
}
