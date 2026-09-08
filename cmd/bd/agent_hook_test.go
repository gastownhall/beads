package main

import (
	"context"
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
	err := validatePrimeArgs([]string{"--memories-only", "--config", "/tmp/evil"})
	if err == nil {
		t.Fatal("expected unknown flag to be rejected")
	}
	if !strings.Contains(err.Error(), "unsupported hook argument") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// runBdPrime must refuse non-allowlisted arguments before it resolves the
// executable or builds a subprocess, so a caller can never steer the
// re-executed command.
func TestRunBdPrimeRejectsUnknownArgsBeforeExec(t *testing.T) {
	_, err := runBdPrime(context.Background(), "--config", "/tmp/evil")
	if err == nil {
		t.Fatal("expected runBdPrime to reject unknown args")
	}
	if !strings.Contains(err.Error(), "unsupported hook argument") {
		t.Fatalf("unexpected error: %v", err)
	}
}
