//go:build cgo

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
)

// TestPersistentPreRunLogsAndSkipsIdentityCheckForFreshEmbeddedWorkspace is
// the MINOR-criterion regression test for be-0gfcs round 2 (be-3bt2e): a
// brand-new workspace -- metadata.json present, but no embedded database on
// disk yet -- is the one previewErr case the identity check has always
// tolerated. It must still emit a debug-visible log line explaining why it
// was skipped, so the skip is diagnosable under BD_DEBUG/--verbose instead of
// looking identical to "the check silently ran and found nothing wrong."
//
// This exercises a real embedded-Dolt bootstrap (schema init on first open),
// so it is opt-in like the other embedded-dolt integration tests in this
// package: set BEADS_TEST_EMBEDDED_DOLT=1 to run it.
func TestPersistentPreRunLogsAndSkipsIdentityCheckForFreshEmbeddedWorkspace(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	// No .beads/embeddeddolt data dir: newPreviewStoreFromConfig's embedded
	// open fails with a wrapped os.ErrNotExist -- the legitimate first-run
	// condition this check must keep skipping (silently, save for the debug
	// log line under test here).
	writeMetadataConfig(t, beadsDir, configfile.DoltModeEmbedded, "identity_gate_bootstrap_test")

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_SKIP_IDENTITY_CHECK", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	t.Cleanup(func() { store = oldStore })

	debug.SetVerbose(true)
	t.Cleanup(func() { debug.SetVerbose(false) })

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}

	probe := &cobra.Command{
		Use:  "identity-gate-bootstrap-probe",
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	rootCmd.AddCommand(probe)
	t.Cleanup(func() { rootCmd.RemoveCommand(probe) })

	oldStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	os.Stderr = w

	err := rootCmd.PersistentPreRunE(probe, nil)

	w.Close()
	os.Stderr = oldStderr
	var captured bytes.Buffer
	io.Copy(&captured, r) //nolint:errcheck // best-effort drain of a test pipe

	if err != nil {
		t.Fatalf("PersistentPreRunE on a fresh embedded workspace: %v", err)
	}

	const wantMarker = "workspace identity check: skipping"
	if !strings.Contains(captured.String(), wantMarker) {
		t.Errorf("expected a debug log line containing %q noting the identity check was skipped for a fresh workspace, got stderr:\n%s", wantMarker, captured.String())
	}
}
