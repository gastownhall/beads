//go:build cgo

package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestFederationSyncRefusedInReadonly is the cgo-only sibling of
// TestDoltPublishingCommandsRefusedInReadonly: federationSyncCmd exists only
// in the cgo build (federation_nocgo.go defines federationCmd alone).
func TestFederationSyncRefusedInReadonly(t *testing.T) {
	originalMode := readonlyMode
	t.Cleanup(func() { readonlyMode = originalMode })
	readonlyMode = true

	assertReadonlyRefusal(t, federationSyncCmd, nil, "operation 'federation sync' is not allowed in read-only mode")
}

// TestFederationSyncRefusedWhenNoPushSet is the cgo-only sibling of
// TestDoltPublishingCommandsRefusedWhenNoPushSet.
func TestFederationSyncRefusedWhenNoPushSet(t *testing.T) {
	t.Setenv("BD_NO_PUSH", "true")
	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	assertNoPushRefusal(t, federationSyncCmd, nil)
}
