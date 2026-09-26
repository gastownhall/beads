package main

import (
	"context"
	"errors"
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
