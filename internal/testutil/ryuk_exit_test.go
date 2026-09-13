//go:build !windows

package testutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ryukExitChildEnv selects the re-exec'd child behaviour for the subprocess
// tests below. Every child half lives in ryukExitChild, which all three
// parents re-exec by name.
const ryukExitChildEnv = "BEADS_TEST_RYUK_EXIT_CHILD"

const (
	// childSwallow runs the WARN-and-continue TestMain shape end to end.
	childSwallow = "swallow"
	// childGuard calls the guard directly and reports that it returned.
	childGuard = "guard"
)

// ryukGuardReturnedMarker is printed by childGuard only if the guard returned
// instead of exiting the process.
const ryukGuardReturnedMarker = "RYUK-GUARD-RETURNED"

// TestRyukExitChild is the child half of the three subprocess tests below. It
// is inert unless re-exec'd with ryukExitChildEnv set, so a normal `go test`
// run of this package just sees it pass.
func TestRyukExitChild(t *testing.T) {
	switch os.Getenv(ryukExitChildEnv) {
	case childSwallow:
		// The exact shape of the 11 TestMains in this repo: downgrade the
		// error to a warning and carry on. If the guard merely returned an
		// error, this would print the WARN and exit 0.
		if err := EnsureDoltContainerForTestMain(); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
		}
		os.Exit(0)
	case childGuard:
		checkRyukEnabled()
		fmt.Fprintln(os.Stderr, ryukGuardReturnedMarker)
		os.Exit(0)
	}
}

// TestRyukDisabled_SwallowingCallerStillDies pins the unmissable path
// end-to-end through the caller shape that actually dominates this repo: a
// TestMain that downgrades EnsureDoltContainerForTestMain's error to a WARN on
// stderr and carries on green.
//
// This is the regression the guard exists to prevent. Returning an error from
// checkRyukEnabled is not enough on its own — 13 of the 18 call sites that
// reach a container-start path swallow it (11 TestMains take it as
// "WARN: ..., skipping Dolt tests", and the two NewContainerProvider callers
// t.Skipf on any error), so a Ryuk-disabled box got a green run with a warning
// buried in the output. That is the same signal that went unnoticed for four
// months (be-ovg86). The guard therefore exits the process rather than
// returning, and this test asserts the exit survives a caller that tries its
// hardest to ignore it.
//
// Hermetic: a stub `docker` on PATH satisfies checkDolt() so the guard is
// reachable, and the guard fires before dolt.Run, so no container runtime is
// touched in either the passing or the failing case.
func TestRyukDisabled_SwallowingCallerStillDies(t *testing.T) {
	home := t.TempDir()
	stubDir := t.TempDir()
	writeDockerStub(t, stubDir)

	out, err := runRyukChild(t, childSwallow, map[string]string{
		// Ryuk off, and deliberately no opt-out: this is the box the guard
		// exists to stop.
		"TESTCONTAINERS_RYUK_DISABLED":        "true",
		"BEADS_ALLOW_UNREAPED_TESTCONTAINERS": "",
		// Neither of these may short-circuit checkDolt() before the guard.
		"BEADS_TEST_SKIP": "",
		"HOME":            home,
		"PATH":            stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	})

	assertGuardExit(t, out, err)
	if strings.Contains(out, "skipping Dolt tests") {
		t.Errorf("child reached its WARN-and-continue branch — the guard let a "+
			"swallowing caller through; output:\n%s", out)
	}
	if !strings.Contains(out, home) {
		t.Errorf("child output does not name the resolved $HOME %q; output:\n%s", home, out)
	}
}

// TestRyukGuard_ExitsWhenDisabled asserts the guard itself kills the process,
// with no caller involved. Fast and runtime-free: the guard never reaches
// checkDolt, so this needs no docker stub.
func TestRyukGuard_ExitsWhenDisabled(t *testing.T) {
	home := t.TempDir()

	out, err := runRyukChild(t, childGuard, map[string]string{
		"TESTCONTAINERS_RYUK_DISABLED":        "true",
		"BEADS_ALLOW_UNREAPED_TESTCONTAINERS": "",
		"HOME":                                home,
	})

	assertGuardExit(t, out, err)
	if strings.Contains(out, ryukGuardReturnedMarker) {
		t.Errorf("guard returned instead of exiting; output:\n%s", out)
	}
}

// TestRyukGuard_ReturnsWhenOptedOut is the counterweight to the two tests
// above: with the documented opt-out set, the guard must NOT kill the
// process. Without this, a guard that exited unconditionally would still
// satisfy both.
func TestRyukGuard_ReturnsWhenOptedOut(t *testing.T) {
	home := t.TempDir()

	out, err := runRyukChild(t, childGuard, map[string]string{
		"TESTCONTAINERS_RYUK_DISABLED":        "true",
		"BEADS_ALLOW_UNREAPED_TESTCONTAINERS": "1",
		"HOME":                                home,
	})

	if err != nil {
		t.Fatalf("child exited %v with the opt-out set, want a clean exit; output:\n%s", err, out)
	}
	if !strings.Contains(out, ryukGuardReturnedMarker) {
		t.Errorf("child did not report that the guard returned; output:\n%s", out)
	}
}

// runRyukChild re-execs TestRyukExitChild in this test binary with the given
// child mode and environment overrides, returning its combined output.
func runRyukChild(t *testing.T, mode string, overrides map[string]string) (string, error) {
	t.Helper()

	env := make(map[string]string, len(overrides)+1)
	for k, v := range overrides {
		env[k] = v
	}
	env[ryukExitChildEnv] = mode

	cmd := exec.Command(os.Args[0], "-test.run=^TestRyukExitChild$")
	cmd.Env = childEnv(env)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// assertGuardExit checks the child died with the guard's exit code and said
// why, naming both override sources and the documented opt-out.
func assertGuardExit(t *testing.T, out string, err error) {
	t.Helper()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child exited %v (want a non-zero exit); output:\n%s", err, out)
	}
	if code := exitErr.ExitCode(); code != ryukGuardExitCode {
		t.Errorf("child exit code = %d, want %d (the Ryuk guard's exit); output:\n%s",
			code, ryukGuardExitCode, out)
	}
	for _, want := range []string{
		"Ryuk",
		".testcontainers.properties",
		"TESTCONTAINERS_RYUK_DISABLED",
		"BEADS_ALLOW_UNREAPED_TESTCONTAINERS",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("child output does not mention %q; output:\n%s", want, out)
		}
	}
}

// childEnv returns os.Environ() with the given keys forced to the given
// values. Built by replacement rather than by appending overrides, so the
// result does not depend on os/exec's dedup keeping the last duplicate.
func childEnv(overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok {
			if _, replaced := overrides[k]; replaced {
				continue
			}
		}
		env = append(env, kv)
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

// writeDockerStub drops a `docker` on PATH that succeeds for every
// invocation, so checkDolt() reports doltReady and the guard downstream of it
// is reachable. The guard fires before any real container call, so the stub
// never has to emulate one.
func writeDockerStub(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing docker stub: %v", err)
	}
}
