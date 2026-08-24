package main

import (
	"os"
	"testing"
)

// realStdout/realStderr are captured at package-variable initialization, before
// any test runs, so they are the process's genuine streams.
var (
	realStdout = os.Stdout
	realStderr = os.Stderr
)

// TestZZStdioNotLeaked fails if any test in this package reassigned os.Stdout or
// os.Stderr and did not restore it. A capture helper that restores in a defer
// cannot trip this; one that restores on the happy path only will trip it as soon
// as its callback calls t.Fatal. See be-gh02.
func TestZZStdioNotLeaked(t *testing.T) {
	if os.Stdout != realStdout {
		t.Errorf("os.Stdout was leaked by an earlier test (now fd=%d name=%q); "+
			"a capture helper restored it on the happy path only - move the restore into a defer",
			os.Stdout.Fd(), os.Stdout.Name())
		os.Stdout = realStdout
	}
	if os.Stderr != realStderr {
		t.Errorf("os.Stderr was leaked by an earlier test (now fd=%d name=%q); "+
			"a capture helper restored it on the happy path only - move the restore into a defer",
			os.Stderr.Fd(), os.Stderr.Name())
		os.Stderr = realStderr
	}
}
