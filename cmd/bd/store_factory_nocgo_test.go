//go:build !cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestNocgoNewDoltStore_ErrorSuggestsCorrectFlag verifies that newDoltStore
// suggests "bd init --server" (not the old "--mode server") when ServerMode
// is false.
func TestNocgoNewDoltStore_ErrorSuggestsCorrectFlag(t *testing.T) {
	cfg := &dolt.Config{ServerMode: false}
	_, err := newDoltStore(t.Context(), cfg)
	if err == nil {
		t.Fatal("expected error when ServerMode is false")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bd init --server") {
		t.Errorf("error should suggest 'bd init --server', got: %s", msg)
	}
	if strings.Contains(msg, "--mode") {
		t.Errorf("error should NOT contain '--mode' (old flag), got: %s", msg)
	}
}

// TestNocgoNewDoltStoreFromConfig_ErrorSuggestsCorrectFlag verifies that
// newDoltStoreFromConfig suggests "bd init --server" when no server-mode
// config exists.
func TestNocgoNewDoltStoreFromConfig_ErrorSuggestsCorrectFlag(t *testing.T) {
	beadsDir := t.TempDir() // empty dir — no config.json
	_, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err == nil {
		t.Fatal("expected error for empty beads dir without server config")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bd init --server") {
		t.Errorf("error should suggest 'bd init --server', got: %s", msg)
	}
	if strings.Contains(msg, "--mode") {
		t.Errorf("error should NOT contain '--mode' (old flag), got: %s", msg)
	}
}

// TestNocgoNewReadOnlyStoreFromConfig_ErrorSuggestsCorrectFlag verifies that
// newReadOnlyStoreFromConfig suggests "bd init --server" when no server-mode
// config exists.
func TestNocgoNewReadOnlyStoreFromConfig_ErrorSuggestsCorrectFlag(t *testing.T) {
	beadsDir := t.TempDir() // empty dir — no config.json
	_, err := newReadOnlyStoreFromConfig(t.Context(), beadsDir)
	if err == nil {
		t.Fatal("expected error for empty beads dir without server config")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bd init --server") {
		t.Errorf("error should suggest 'bd init --server', got: %s", msg)
	}
	if strings.Contains(msg, "--mode") {
		t.Errorf("error should NOT contain '--mode' (old flag), got: %s", msg)
	}
}
