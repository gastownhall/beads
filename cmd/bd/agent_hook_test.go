package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValidatePrimeArgsAcceptsHookFlags(t *testing.T) {
	if err := validatePrimeArgs(nil); err != nil {
		t.Fatalf("no args should be allowed, got %v", err)
	}
	if err := validatePrimeArgs([]string{"--memories-only"}); err != nil {
		t.Fatalf("--memories-only should be allowed, got %v", err)
	}
}

func TestValidatePrimeArgsRejectsUnknownFlag(t *testing.T) {
	err := validatePrimeArgs([]string{"--config"})
	if err == nil {
		t.Fatal("expected unknown flag to be rejected")
	}
	if !strings.Contains(err.Error(), `"--config"`) {
		t.Fatalf("rejection should name the offending argument, got: %v", err)
	}
}

// runBdPrime must refuse non-allowlisted arguments before it resolves the
// executable or builds a subprocess. The resolver is stubbed so the test also
// proves validation runs first — if a future change drops or reorders the
// validatePrimeArgs call, the resolver (and with it the subprocess) would be
// reached and this test fails instead of live-exec'ing anything.
func TestRunBdPrimeRejectsUnknownArgsBeforeExec(t *testing.T) {
	called := false
	orig := primeExecutable
	primeExecutable = func() (string, error) {
		called = true
		return "", errors.New("resolver must not run for rejected args")
	}
	t.Cleanup(func() { primeExecutable = orig })

	_, err := runBdPrime(context.Background(), "--config")
	if err == nil {
		t.Fatal("expected runBdPrime to reject unknown args")
	}
	if !strings.Contains(err.Error(), `"--config"`) {
		t.Fatalf("rejection should name the offending argument, got: %v", err)
	}
	if called {
		t.Fatal("executable resolver ran before argument validation")
	}
}

// With allowlisted args, a resolver failure surfaces as the wrapped
// resolve-executable error and no subprocess is built.
func TestRunBdPrimeExecutableResolutionError(t *testing.T) {
	orig := primeExecutable
	primeExecutable = func() (string, error) {
		return "", errors.New("no executable")
	}
	t.Cleanup(func() { primeExecutable = orig })

	_, err := runBdPrime(context.Background())
	if err == nil || !strings.Contains(err.Error(), "resolve executable") {
		t.Fatalf("want resolve-executable error, got: %v", err)
	}
}

// The point of the seam: the command must be built from the *resolved*
// executable, not from os.Args[0]. primeCommand builds without running, so
// this asserts the exec target and the full argv directly. The sentinel
// deliberately contains a path separator — exec.Command only skips LookPath
// for names that do, so a bare name would leave cmd.Path host-PATH-dependent
// and set cmd.Err, making the assertion vacuous.
func TestPrimeCommandUsesResolvedExecutable(t *testing.T) {
	const sentinel = "/tmp/beads-prime-sentinel-bd"

	orig := primeExecutable
	primeExecutable = func() (string, error) { return sentinel, nil }
	t.Cleanup(func() { primeExecutable = orig })

	cmd, err := primeCommand(context.Background(), "--memories-only")
	if err != nil {
		t.Fatalf("primeCommand with allowlisted args: %v", err)
	}
	if cmd.Err != nil {
		t.Fatalf("separator-containing sentinel should build cleanly, got cmd.Err=%v", cmd.Err)
	}
	if cmd.Path != sentinel {
		t.Fatalf("exec target should be the resolved executable %q, got %q", sentinel, cmd.Path)
	}
	want := []string{sentinel, "prime", "--memories-only"}
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("argv should be %q, got %q", want, cmd.Args)
	}
}

// primeCommand refuses non-allowlisted args before resolving, and returns no
// command to run.
func TestPrimeCommandRejectsUnknownArgs(t *testing.T) {
	orig := primeExecutable
	primeExecutable = func() (string, error) {
		return "", errors.New("resolver must not run for rejected args")
	}
	t.Cleanup(func() { primeExecutable = orig })

	cmd, err := primeCommand(context.Background(), "--config")
	if err == nil {
		t.Fatal("expected primeCommand to reject unknown args")
	}
	if cmd != nil {
		t.Fatalf("rejected args must not yield a command, got %v", cmd.Args)
	}
}

// The production resolver refuses a test binary rather than handing back a
// path whose re-exec would fork-bomb the suite — the guard doctor/fix's
// getBdBinary pairs with os.Executable. Under `go test` the running binary is
// the package test binary, so this exercises the refusal directly, the same
// way cmd/bd/doctor/fix's own suites assert ErrTestBinary.
// runBdPrimeInDir's cwd validation must run before the executable resolver,
// the same ordering TestRunBdPrimeRejectsUnknownArgsBeforeExec proves for
// argument validation above — a rejected cwd must never reach a subprocess.
func stubPrimeExecutableMustNotRun(t *testing.T) {
	t.Helper()
	orig := primeExecutable
	primeExecutable = func() (string, error) {
		t.Fatal("executable resolver ran despite a rejected workspace cwd")
		return "", nil
	}
	t.Cleanup(func() { primeExecutable = orig })
}

// GH#7095: a payload cwd must be an absolute, existing directory before it is
// assigned to cmd.Dir. These four cases are the validation boundary;
// TestRunBdPrimeInDirAcceptsValidAndEmptyCWD covers the two cases that pass.
func TestRunBdPrimeInDirRejectsRelativeCWD(t *testing.T) {
	stubPrimeExecutableMustNotRun(t)

	_, err := runBdPrimeInDir(context.Background(), "relative/workspace")
	if err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("want absolute-path rejection, got: %v", err)
	}
}

func TestRunBdPrimeInDirRejectsMissingCWD(t *testing.T) {
	stubPrimeExecutableMustNotRun(t)

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := runBdPrimeInDir(context.Background(), missing)
	if err == nil || !strings.Contains(err.Error(), "inspect bd prime workspace cwd") {
		t.Fatalf("want inspect-cwd error for missing path, got: %v", err)
	}
}

func TestRunBdPrimeInDirRejectsNonDirectoryCWD(t *testing.T) {
	stubPrimeExecutableMustNotRun(t)

	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := runBdPrimeInDir(context.Background(), file)
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("want not-a-directory error, got: %v", err)
	}
}

// A valid absolute directory, and an empty cwd (meaning "no -C override, run
// in the process's own workspace" — not a rejection), must both clear
// validation and reach the executable resolver. The resolver is stubbed to
// fail so the error proves validation passed through rather than exercising
// a real subprocess.
func TestRunBdPrimeInDirAcceptsValidAndEmptyCWD(t *testing.T) {
	for _, tc := range []struct {
		name string
		cwd  string
	}{
		{name: "valid absolute directory", cwd: t.TempDir()},
		{name: "empty cwd", cwd: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := primeExecutable
			primeExecutable = func() (string, error) {
				return "", errors.New("resolver reached: cwd validation passed")
			}
			t.Cleanup(func() { primeExecutable = orig })

			_, err := runBdPrimeInDir(context.Background(), tc.cwd)
			if err == nil || !strings.Contains(err.Error(), "resolve executable") {
				t.Fatalf("want validation to pass through to resolve-executable error, got: %v", err)
			}
		})
	}
}

func TestResolvePrimeExecutableRefusesTestBinary(t *testing.T) {
	path, err := resolvePrimeExecutable()
	if !errors.Is(err, errPrimeTestBinary) {
		t.Fatalf("want errPrimeTestBinary under go test, got path=%q err=%v", path, err)
	}
	if path != "" {
		t.Fatalf("refusal must not return a path, got %q", path)
	}
}
