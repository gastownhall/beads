package main

import (
	"fmt"
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
//
// It only sees tests that ran before it in this process. TestMain repeats the
// check after m.Run (checkStdioAfterRun), which covers every test in the
// process, including each Bazel shard.
//
// Where TestAAAStdioBaseline did not run in this process (a -run filter, or a
// Bazel shard that holds this test but not that one), the baseline is the
// streams TestMain saw before m.Run, with checkStdioAfterRun's one allowance:
// the framework's own -json swap, os.Stderr = os.Stdout.
func TestZZStdioNotLeaked(t *testing.T) {
	wantStdout, wantStderr := baselineStdout, baselineStderr
	frameworkSwapAllowed := false
	if wantStdout == nil || wantStderr == nil {
		if preRunStdout == nil || preRunStderr == nil {
			t.Fatal("neither TestAAAStdioBaseline nor TestMain recorded the streams; TestMain must run the suite through runTestsAndSweep")
		}
		wantStdout, wantStderr = preRunStdout, preRunStderr
		frameworkSwapAllowed = true
	}
	if os.Stdout != wantStdout {
		t.Errorf("os.Stdout was leaked by an earlier test (now fd=%d name=%q); "+
			"a capture helper restored it on the happy path only - move the restore into a defer",
			os.Stdout.Fd(), os.Stdout.Name())
		os.Stdout = wantStdout
	}
	if os.Stderr != wantStderr && (!frameworkSwapAllowed || os.Stderr != wantStdout) {
		t.Errorf("os.Stderr was leaked by an earlier test (now fd=%d name=%q); "+
			"a capture helper restored it on the happy path only - move the restore into a defer",
			os.Stderr.Fd(), os.Stderr.Name())
		os.Stderr = wantStderr
	}
}

// checkStdioAfterRun fails the process (returning a non-zero code) when a test
// left os.Stdout or os.Stderr reassigned. stdout/stderr are the streams from
// before m.Run. The one reassignment allowed is the framework's own under
// -json, os.Stderr = os.Stdout (see TestZZStdioNotLeaked).
func checkStdioAfterRun(code int, stdout, stderr *os.File) int {
	var leaked []string
	if os.Stdout != stdout {
		leaked = append(leaked, fmt.Sprintf("os.Stdout (now fd=%d name=%q)", os.Stdout.Fd(), os.Stdout.Name()))
	}
	if os.Stderr != stderr && os.Stderr != stdout {
		leaked = append(leaked, fmt.Sprintf("os.Stderr (now fd=%d name=%q)", os.Stderr.Fd(), os.Stderr.Name()))
	}
	if len(leaked) == 0 {
		return code
	}
	os.Stdout, os.Stderr = stdout, stderr
	for _, l := range leaked {
		fmt.Fprintf(stderr, "FAIL: %s was leaked by a test in this process; "+
			"a capture helper restored it on the happy path only - move the restore into a defer\n", l)
	}
	if code == 0 {
		code = 1
	}
	return code
}
