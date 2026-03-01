package main

import (
	"context"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestIsDoltAutoPushEnabled_ExplicitConfig(t *testing.T) {
	// Cannot be parallel: modifies global env vars and config.

	tests := []struct {
		name       string
		envVal     string // "true"/"false" = explicit config via env
		wantResult bool
	}{
		{
			name:       "explicit true → enabled",
			envVal:     "true",
			wantResult: true,
		},
		{
			name:       "explicit false → disabled",
			envVal:     "false",
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BD_DOLT_AUTO_PUSH", tt.envVal)

			config.ResetForTesting()
			t.Cleanup(func() { config.ResetForTesting() })
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			// With explicit config, store check is bypassed
			// (store is nil in this test, which would return false for auto-detection)
			got := isDoltAutoPushEnabled(context.Background())
			if got != tt.wantResult {
				t.Errorf("isDoltAutoPushEnabled() = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

func TestIsDoltAutoPushEnabled_DefaultNoStore(t *testing.T) {
	// When no explicit config and no store, should return false.
	os.Unsetenv("BD_DOLT_AUTO_PUSH")
	t.Cleanup(func() { os.Unsetenv("BD_DOLT_AUTO_PUSH") })

	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	// store is nil → auto-detection returns false
	got := isDoltAutoPushEnabled(context.Background())
	if got != false {
		t.Errorf("isDoltAutoPushEnabled() with nil store = %v, want false", got)
	}
}

func TestMaybeAutoPush_NilStore(t *testing.T) {
	// maybeAutoPush should be a no-op when store is nil (no panic).
	os.Unsetenv("BD_DOLT_AUTO_PUSH")
	t.Cleanup(func() { os.Unsetenv("BD_DOLT_AUTO_PUSH") })

	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	// Should not panic with nil store
	maybeAutoPush(context.Background())
}

func TestAutoPush_SkippedForReadOnlyCommands(t *testing.T) {
	// Read-only commands should not trigger auto-push (GH#2191).
	readOnly := []string{"list", "ready", "show", "stats", "blocked", "search", "graph"}
	for _, cmd := range readOnly {
		if !isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = false, want true", cmd)
		}
	}

	writeCmds := []string{"create", "update", "close", "import"}
	for _, cmd := range writeCmds {
		if isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = true, want false", cmd)
		}
	}
}

func TestMaybeAutoPush_DisabledByConfig(t *testing.T) {
	// When explicitly disabled, maybeAutoPush should be a no-op.
	t.Setenv("BD_DOLT_AUTO_PUSH", "false")

	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	// Should not panic or attempt push
	maybeAutoPush(context.Background())
}
