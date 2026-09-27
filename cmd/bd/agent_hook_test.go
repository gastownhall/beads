package main

import (
	"context"
	"errors"
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
func TestResolvePrimeExecutableRefusesTestBinary(t *testing.T) {
	path, err := resolvePrimeExecutable()
	if !errors.Is(err, errPrimeTestBinary) {
		t.Fatalf("want errPrimeTestBinary under go test, got path=%q err=%v", path, err)
	}
	if path != "" {
		t.Fatalf("refusal must not return a path, got %q", path)
	}
}
