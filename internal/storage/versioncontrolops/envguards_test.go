package versioncontrolops

import (
	"errors"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/doltinfoenv"
)

// TestWithRemoteEnvGuardsDisablesRemoteInfoBranch pins the info-branch guard
// (GH#6484): an in-process remote statement must see DOLT_REMOTE_INFO_BRANCH
// set to empty — Dolt's opt-out from publishing the visible info branch —
// while it runs, the variable must be absent again once the outermost
// guarded call returns, and a value the user set beforehand must never be
// changed.
func TestWithRemoteEnvGuardsDisablesRemoteInfoBranch(t *testing.T) {
	if err := os.Unsetenv(doltinfoenv.EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(doltinfoenv.EnvVar) })

	var inside string
	var insideOK bool
	if err := withRemoteEnvGuards(func() error {
		inside, insideOK = os.LookupEnv(doltinfoenv.EnvVar)
		return nil
	}); err != nil {
		t.Fatalf("withRemoteEnvGuards: %v", err)
	}
	if !insideOK || inside != "" {
		t.Errorf("inside the guard %s = (%q, %v); want set to empty", doltinfoenv.EnvVar, inside, insideOK)
	}
	if _, ok := os.LookupEnv(doltinfoenv.EnvVar); ok {
		t.Errorf("%s still set after the guard returned; want absent again", doltinfoenv.EnvVar)
	}

	// The user's opt-in survives the guard untouched.
	t.Setenv(doltinfoenv.EnvVar, "dolt-info")
	if err := withRemoteEnvGuards(func() error {
		if got, ok := os.LookupEnv(doltinfoenv.EnvVar); !ok || got != "dolt-info" {
			t.Errorf("inside the guard %s = (%q, %v); want the user's %q", doltinfoenv.EnvVar, got, ok, "dolt-info")
		}
		return nil
	}); err != nil {
		t.Fatalf("withRemoteEnvGuards: %v", err)
	}
	if got := os.Getenv(doltinfoenv.EnvVar); got != "dolt-info" {
		t.Errorf("after the guard %s = %q, want the user's %q", doltinfoenv.EnvVar, got, "dolt-info")
	}
}

func TestWithRemoteEnvGuardsReturnsFnError(t *testing.T) {
	sentinel := errors.New("CALL DOLT_PUSH failed")
	if err := withRemoteEnvGuards(func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("withRemoteEnvGuards returned %v, want %v", err, sentinel)
	}
}
