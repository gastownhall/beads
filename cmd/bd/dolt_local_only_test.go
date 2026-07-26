package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// Unit tests for isDoltLocalOnly() and related guards.
// These fail to compile until the builder adds isDoltLocalOnly() in dolt.go.
// Cannot be parallel: tests modify global config state.

func TestIsDoltLocalOnly_FalseByDefault(t *testing.T) {
	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	if isDoltLocalOnly() {
		t.Error("isDoltLocalOnly() = true, want false when dolt.local-only not set")
	}
}

func TestIsDoltLocalOnly_TrueWhenConfigSet(t *testing.T) {
	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("dolt.local-only", true)

	if !isDoltLocalOnly() {
		t.Error("isDoltLocalOnly() = false, want true when dolt.local-only=true")
	}
}

func TestIsDoltLocalOnly_FalseAfterUnset(t *testing.T) {
	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("dolt.local-only", true)
	if !isDoltLocalOnly() {
		t.Fatal("precondition: isDoltLocalOnly() should be true after set")
	}
	config.Set("dolt.local-only", false)
	if isDoltLocalOnly() {
		t.Error("isDoltLocalOnly() = true after unsetting, want false")
	}
}

func TestMaybeAutoPush_SkipsWhenLocalOnly(t *testing.T) {
	// When dolt.local-only=true, maybeAutoPush must return early (no push attempt).
	// Local-only guard must fire BEFORE getStore(); store is nil here.
	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("dolt.local-only", true)
	// Enable auto-push so the local-only guard, not the disabled-check, short-circuits.
	t.Setenv("BD_DOLT_AUTO_PUSH", "true")

	// Must not panic. The local-only guard fires before any store interaction.
	maybeAutoPush(context.Background())
}
