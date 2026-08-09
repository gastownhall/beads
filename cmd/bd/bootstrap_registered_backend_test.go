package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestRequireBootstrapDoltBackendRejectsRegisteredBackend pins the bootstrap
// fail-closed guard: a registered extension backend is open/discover-only, but
// every bd bootstrap action provisions or imports Dolt, so the guard must
// reject it with open-existing-workspace-only guidance. The default and
// explicit Dolt configs — the only workspaces bootstrap actually provisions —
// must still pass. This is the unit-level twin of TestInitRejectsRegisteredBackend.
func TestRequireBootstrapDoltBackendRejectsRegisteredBackend(t *testing.T) {
	const name = "registry-bootstrap"
	registerContractBackend(t, name)

	err := requireBootstrapDoltBackend(&configfile.Config{Backend: name})
	if err == nil {
		t.Fatal("requireBootstrapDoltBackend accepted a registered backend; want fail-closed rejection")
	}
	if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "open an existing workspace") {
		t.Fatalf("reject error = %v, want open-existing-workspace-only guidance naming %q", err, name)
	}

	// The guard must not reject the cases bd bootstrap is meant to provision:
	// the default config, an unset backend (GetBackend normalizes "" to dolt),
	// and an explicit dolt selection.
	for _, tc := range []struct {
		label string
		cfg   *configfile.Config
	}{
		{"default", configfile.DefaultConfig()},
		{"empty backend", &configfile.Config{}},
		{"explicit dolt", &configfile.Config{Backend: configfile.BackendDolt}},
	} {
		if err := requireBootstrapDoltBackend(tc.cfg); err != nil {
			t.Errorf("requireBootstrapDoltBackend rejected the %s config: %v", tc.label, err)
		}
	}
}

// TestBootstrapRejectsRegisteredBackendBeforeWorkspaceWrites drives the guard
// through the real bd bootstrap command path: a workspace whose metadata.json
// selects a registered backend is rejected before any Dolt provisioning, the
// guidance reaches the user, and no local storage state is written. Registered
// backends are not compiled into the OSS binary, so this must run in-process —
// a subprocess bd would have no backend registered and could not reproduce the
// case (unlike the removed-backend guard tests, which use a subprocess).
func TestBootstrapRejectsRegisteredBackendBeforeWorkspaceWrites(t *testing.T) {
	const name = "registry-bootstrap-e2e"
	registerContractBackend(t, name)
	beadsDir := writeContractBackendConfig(t, name)
	metadataPath := filepath.Join(beadsDir, configfile.ConfigFileName)
	metadataBefore, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read seeded metadata.json: %v", err)
	}

	// bd bootstrap runs on the shared cobra command and reads package globals;
	// snapshot and restore what the run can touch so the registered backend and
	// flag state cannot leak into other tests sharing this state.
	origDBPath := dbPath
	origStore := store
	origJSON := jsonOutput
	t.Cleanup(func() {
		if store != nil && store != origStore {
			store.Close()
		}
		store = origStore
		dbPath = origDBPath
		jsonOutput = origJSON
		_ = bootstrapCmd.Flags().Set("yes", "false")
	})
	dbPath = ""
	store = nil
	jsonOutput = false

	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BD_NON_INTERACTIVE", "1")
	t.Setenv("BD_DISABLE_METRICS", "1")
	t.Setenv("BD_DISABLE_EVENT_FLUSH", "1")

	var execErr error
	stderr := captureBootstrapStderr(t, func() {
		rootCmd.SetArgs([]string{"bootstrap", "--yes"})
		execErr = rootCmd.Execute()
	})
	if execErr == nil {
		t.Fatalf("bd bootstrap accepted a registered backend; want fail-closed rejection\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, name) || !strings.Contains(stderr, "open an existing workspace") {
		t.Fatalf("bootstrap reject stderr missing open-existing-workspace-only guidance naming %q:\n%s", name, stderr)
	}

	// Failing closed means the metadata is untouched and no Dolt state was
	// provisioned for the unprovisionable backend.
	metadataAfter, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata.json after rejected bootstrap: %v", err)
	}
	if !bytes.Equal(metadataAfter, metadataBefore) {
		t.Errorf("rejected bootstrap rewrote metadata.json:\nbefore:\n%s\nafter:\n%s", metadataBefore, metadataAfter)
	}
	assertNoBootstrapStorageArtifacts(t, beadsDir)
}

// captureBootstrapStderr redirects os.Stderr for the duration of fn and returns
// what was written. bd bootstrap surfaces the guard message through HandleError,
// which writes to os.Stderr and returns an opaque exit error, so the message is
// only observable here — not on the error returned by rootCmd.Execute().
func captureBootstrapStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stderr = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}
