package main

import (
	"os"
	"testing"
)

// TestZZStdioNotLeaked fails if any test in this package reassigned os.Stdout
// or os.Stderr and did not restore it. A capture helper that restores in a
// defer cannot trip this; one that restores on the happy path only will trip
// it as soon as its callback calls t.Fatal. See be-gh02.
//
// The baseline is what the FIRST test saw (aaa_stdio_baseline_test.go), not
// the var-init streams: under `go test -json` the testing framework swaps
// os.Stderr to os.Stdout inside M.Run (go.dev/issue/33419), after var-init
// and before any test, and that framework swap is not a leak (#5881).
func TestZZStdioNotLeaked(t *testing.T) {
	if baselineStdout == nil || baselineStderr == nil {
		t.Skip("stdio baseline not captured (TestAAAStdioBaseline filtered out); nothing to compare against")
	}
	if os.Stdout != baselineStdout {
		t.Errorf("os.Stdout was leaked by an earlier test (now fd=%d name=%q); "+
			"a capture helper restored it on the happy path only - move the restore into a defer",
			os.Stdout.Fd(), os.Stdout.Name())
		os.Stdout = baselineStdout
	}
	if os.Stderr != baselineStderr {
		t.Errorf("os.Stderr was leaked by an earlier test (now fd=%d name=%q); "+
			"a capture helper restored it on the happy path only - move the restore into a defer",
			os.Stderr.Fd(), os.Stderr.Name())
		os.Stderr = baselineStderr
	}
}
