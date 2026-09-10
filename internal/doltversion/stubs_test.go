package doltversion

import (
	"runtime"
	"testing"
)

// writeExecStub writes a runnable stub binary for the current platform:
// the posix body (a /bin/sh script) on Unix, the batch body (a cmd.exe
// script, so the file gets a .cmd extension) on Windows. This is what
// lets the probe's behavioral tests actually execute on Windows instead
// of skipping wholesale — review of this PR flagged that every behavioral
// test skipped there while the package ships Windows-specific branches
// (ERROR_BAD_EXE_FORMAT, the exec-bit carve-out, PATHEXT completion).
func writeExecStub(t *testing.T, dir, name, posix, batch string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return writeStub(t, dir, name+".cmd", batch, true)
	}
	return writeStub(t, dir, name, posix, true)
}

// writeVersionEchoStub writes a stub that prints one line and exits zero —
// the shape of a healthy `dolt version` (or of garbage output, depending
// on line).
func writeVersionEchoStub(t *testing.T, dir, name, line string) string {
	t.Helper()
	return writeExecStub(t, dir, name,
		"#!/bin/sh\necho '"+line+"'\n",
		"@echo "+line+"\r\n")
}
