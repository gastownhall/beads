// Package credentialcmd provides shell-portable credential-command fixtures
// for tests that need to exercise the production shell boundary.
package credentialcmd

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	helperProcessEnv = "BEADS_TEST_CREDENTIAL_COMMAND_HELPER"
	helperSentinel   = "beads-credential-command-helper"
	helperTestRun    = "-test.run=NoTestsMatchCredentialCommandHelper"
	malformedExit    = 97
)

var (
	commandSequence atomic.Uint64
	fixtureOnce     sync.Once
	fixture         preparedFixture
)

type preparedFixture struct {
	root       string
	shellName  string
	shellDir   string
	shellPath  string
	helperPath string
	targetPath string
	targetEnv  string
	err        error
}

// Dispatch handles an opted-in credential helper invocation. TestMain callers
// must call it before ordinary package setup so the helper process does not
// allocate suite resources.
//
// Both gates must hold before this claims the process: the env var AND the argv
// sentinel. The env gate alone is not sufficient, because command() arms
// helperProcessEnv in the *parent* test process and BEADS_-prefixed variables
// reach every re-exec'd child through integration.FilterEnv's allowlist. A
// package that re-execs its own test binary for an unrelated reason —
// internal/storage/dolt does exactly that, and maintains helperSubprocessSentinels
// for the hazard — would otherwise have its child claimed here and killed with a
// wrong-subsystem exit 97 the moment the two compose. An env-armed process
// without the sentinel is simply not ours, so it falls through to its own
// TestMain rather than being refused.
func Dispatch() (int, bool) {
	if os.Getenv(helperProcessEnv) != "1" {
		return 0, false
	}

	for i, arg := range os.Args {
		if arg == helperSentinel {
			return runProtocol(os.Args[i+1:], os.Stdout, os.Stderr), true
		}
	}
	return 0, false
}

// Constructor contract — Emit, Exit23 and Marker all share it, and it is more
// than "returns a string":
//
//   - They replace PATH for the remainder of the calling test with the isolated
//     shell directory, and set two to three environment variables. Production
//     code invoked later in the same test resolves binaries under that narrowed
//     PATH, so any lookup it makes is affected.
//   - They use t.Setenv, which makes the calling test permanently incompatible
//     with t.Parallel.
//   - Each returns a *distinct* command string, which is load-bearing: the
//     production credential cache is keyed by command text, so identical
//     fixtures would otherwise hit a 60s cross-test cache.
//
// Cleanup removes the package process's suite-scoped transport artifacts after
// all tests have completed.
func Cleanup() error {
	if fixture.root == "" {
		return nil
	}
	return os.RemoveAll(fixture.root)
}

// Emit returns a unique shell command that writes value to stdout without a
// trailing newline. See the constructor contract above for the PATH/env side
// effects every constructor here shares.
//
// value must not be empty. The payload is appended as one word, and both sh and
// cmd word-split an empty argument away, so the helper would receive only
// "emit" and exit 97 "malformed protocol" — a confusing way to reach a case the
// production parser rejects anyway ("credential command produced no output").
// Use Marker for a command that runs but yields nothing usable.
func Emit(t *testing.T, value string) string {
	t.Helper()
	if value == "" {
		t.Fatalf("credentialcmd.Emit: empty value is unsupported; the shell drops the empty payload word " +
			"and the helper exits 97. Use Marker for a command that runs and produces no usable output.")
	}
	return command(t, "emit", []byte(value))
}

// Exit23 returns a unique shell command that exits with status 23. See the
// constructor contract above for the PATH/env side effects every constructor
// here shares.
func Exit23(t *testing.T) string {
	t.Helper()
	return command(t, "exit23", nil)
}

// Marker returns a unique shell command that writes an invocation marker. See
// the constructor contract above for the PATH/env side effects every
// constructor here shares.
func Marker(t *testing.T, path string) string {
	t.Helper()
	return command(t, "marker", []byte(path))
}

// AssertMarkerAbsent fails when a Marker command ran unexpectedly.
func AssertMarkerAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("credential command ran unexpectedly and wrote %q", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("inspect credential command marker %q: %v", path, err)
	}
}

func command(t *testing.T, operation string, payload []byte) string {
	t.Helper()

	fixtureOnce.Do(prepareFixture)
	if fixture.err != nil {
		t.Fatalf("prepare credential command fixture: %v", fixture.err)
	}
	if !strings.Contains(fixture.helperPath, " ") {
		t.Fatalf("credential helper path does not exercise quoting: %q", fixture.helperPath)
	}

	executableEnv := fmt.Sprintf("BEADS_TEST_CREDENTIAL_COMMAND_EXE_%d", commandSequence.Add(1))
	executable := configurePlatformCommand(t, executableEnv)
	t.Setenv(helperProcessEnv, "1")

	parts := []string{executable, helperTestRun, "--", helperSentinel, operation}
	if payload != nil {
		parts = append(parts, base64.RawURLEncoding.EncodeToString(payload))
	}
	return strings.Join(parts, " ")
}

func runProtocol(args []string, stdout, stderr io.Writer) int {
	malformed := func() int {
		fmt.Fprintln(stderr, "credential command helper: malformed protocol")
		return malformedExit
	}

	switch {
	case len(args) == 2 && args[0] == "emit":
		payload, err := base64.RawURLEncoding.DecodeString(args[1])
		if err != nil {
			return malformed()
		}
		if _, err := stdout.Write(payload); err != nil {
			fmt.Fprintf(stderr, "credential command helper: write stdout: %v\n", err)
			return malformedExit
		}
		return 0
	case len(args) == 1 && args[0] == "exit23":
		return 23
	case len(args) == 2 && args[0] == "marker":
		payload, err := base64.RawURLEncoding.DecodeString(args[1])
		if err != nil || len(payload) == 0 {
			return malformed()
		}
		if err := os.WriteFile(string(payload), []byte("invoked"), 0o600); err != nil {
			fmt.Fprintf(stderr, "credential command helper: write marker: %v\n", err)
			return malformedExit
		}
		return 0
	default:
		return malformed()
	}
}
