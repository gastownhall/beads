package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitRejectsRegisteredBackend verifies bd init fails closed for a
// registered extension backend instead of silently provisioning Dolt and
// persisting backend: dolt. Registered backends can only open an existing
// workspace; provisioning is the downstream registrant's own contract.
func TestInitRejectsRegisteredBackend(t *testing.T) {
	const name = "registry-init"
	registerContractBackend(t, name)

	// bd init runs on the shared cobra command and mutates package globals;
	// snapshot and restore both so the registered backend and its flag value
	// cannot leak into other init tests that share this state.
	origDBPath := dbPath
	origStore := store
	t.Cleanup(func() {
		if store != nil && store != origStore {
			store.Close()
		}
		store = origStore
		dbPath = origDBPath
		_ = initCmd.Flags().Set("backend", "")
		_ = initCmd.Flags().Set("prefix", "")
		_ = initCmd.Flags().Set("quiet", "false")
	})
	dbPath = ""
	store = nil

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	rootCmd.SetArgs([]string{"init", "--backend", name, "--prefix", "test", "--quiet"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("bd init accepted a registered backend; want fail-closed rejection")
	}
	if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "open an existing workspace") {
		t.Fatalf("init reject error = %v, want open-existing-workspace-only guidance naming %q", err, name)
	}
	// Failing closed means no workspace is written for the unprovisionable backend.
	if _, statErr := os.Stat(filepath.Join(tmpDir, ".beads")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected init created a .beads workspace (stat error: %v)", statErr)
	}
}
