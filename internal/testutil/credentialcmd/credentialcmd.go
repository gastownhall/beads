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
func Dispatch() (int, bool) {
	if os.Getenv(helperProcessEnv) != "1" {
		return 0, false
	}

	for i, arg := range os.Args {
		if arg == helperSentinel {
			return runProtocol(os.Args[i+1:], os.Stdout, os.Stderr), true
		}
	}
	fmt.Fprintln(os.Stderr, "credential command helper: missing protocol sentinel")
	return malformedExit, true
}

// Cleanup removes the package process's suite-scoped transport artifacts after
// all tests have completed.
func Cleanup() error {
	if fixture.root == "" {
		return nil
	}
	return os.RemoveAll(fixture.root)
}

// Emit returns a unique shell command that writes value to stdout without a
// trailing newline.
func Emit(t *testing.T, value string) string {
	t.Helper()
	return command(t, "emit", []byte(value))
}

// Exit23 returns a unique shell command that exits with status 23.
func Exit23(t *testing.T) string {
	t.Helper()
	return command(t, "exit23", nil)
}

// Marker returns a unique shell command that writes an invocation marker.
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
