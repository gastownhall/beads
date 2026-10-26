package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// hookProcessGoBlockerEnv turns this test binary into a stand-in for a bd that
// never returns: a Go process, so it inherits the Go runtime's SIGALRM
// handling exactly as the real bd does (bd-bnsguq, GH#7753). init runs before
// TestMain, so the blocker never reaches the test harness.
const hookProcessGoBlockerEnv = "BD_TEST_HOOK_GO_BLOCKER"

func init() {
	if os.Getenv(hookProcessGoBlockerEnv) != "1" {
		return
	}
	fmt.Println("go-bd-started")
	time.Sleep(12 * time.Second)
	os.Exit(97)
}

// TestGeneratedHookPerlTimeoutStopsGoBD asserts that the Perl backend's
// BEADS_HOOK_TIMEOUT stops a Go bd, not only a shell stub: the shim must report
// the timeout and exit 0, as it does with the coreutils backend.
func TestGeneratedHookPerlTimeoutStopsGoBD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Perl alarm backend is not the Git Bash path")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	perlDir := findHookProcessPerl(t)
	timeout := "1"
	bdBody := "#!/bin/sh\n" + hookProcessGoBlockerEnv + "=1\nexport " + hookProcessGoBlockerEnv +
		"\nexec " + hookProcessShellQuote(hookProcessShellPath(t, self)) + " </dev/null\n"
	result := runGeneratedHookProcess(t, hookProcessCase{
		fixtures: []hookProcessFixture{
			{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
			{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
		},
		bdBody:   bdBody,
		timeout:  &timeout,
		pathTail: perlDir,
	})
	if !strings.Contains(result.output, "go-bd-started") {
		t.Fatalf("Go bd stand-in never started\n%s", result.output)
	}
	if result.exitCode != 0 || !strings.Contains(result.output, "timed out after 1s") {
		t.Fatalf("Perl backend did not stop a Go bd at BEADS_HOOK_TIMEOUT=1: exit=%d after %s, want exit 0 with the timed-out message\n%s",
			result.exitCode, result.elapsed, result.output)
	}
	if result.elapsed > 9*time.Second {
		t.Errorf("Perl expiry of a Go bd took %s, want at most 9s", result.elapsed)
	}
}
