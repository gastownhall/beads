package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestIsBootstrapPreviewErr pins the classifier that decides whether the
// identity check's preview-open failure is a legitimate first run (skip
// silently) or something else (refuse). It must recognize the exact wrapping
// shape embeddeddolt produces for "no database on disk yet" and nothing else,
// including an error that merely happens to also be wrapped.
func TestIsBootstrapPreviewErr(t *testing.T) {
	wrappedNotExist := fmt.Errorf("embeddeddolt: no embedded database at %s: %w", "/tmp/x/embeddeddolt", os.ErrNotExist)
	wrappedOther := fmt.Errorf("dial tcp 127.0.0.1:3306: %w", errors.New("connect: connection refused"))

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"direct os.ErrNotExist", os.ErrNotExist, true},
		{"wrapped os.ErrNotExist (embedded store shape)", wrappedNotExist, true},
		{"generic non-ENOENT error", errors.New("connection refused"), false},
		{"wrapped non-ENOENT error", wrappedOther, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBootstrapPreviewErr(tt.err); got != tt.want {
				t.Errorf("isBootstrapPreviewErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestPersistentPreRunRefusesWhenIdentityPreviewFailsForNonBootstrapReason is
// the MAJOR-criterion regression test for be-0gfcs round 2 (be-3bt2e): a
// preview-open failure that is NOT the legitimate first-run case must refuse
// before the real, mutating store open runs -- not silently no-op the way it
// did when the guard's own peek could fail for the same reason it exists to
// catch (an unreachable or misconfigured workspace), letting a pending schema
// migration auto-apply unexamined.
//
// Dolt server-mode with no server listening is the hermetic trigger: it is
// the same setup TestPersistentPreRunHonorsSkipStoreAnnotation's control case
// already relies on to prove PersistentPreRunE reaches, and fails, a real
// store open in this test environment -- and it fails for a connection
// reason, never an os.ErrNotExist, so it is unambiguously NOT bootstrap.
func TestPersistentPreRunRefusesWhenIdentityPreviewFailsForNonBootstrapReason(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	writeMetadataConfig(t, beadsDir, configfile.DoltModeServer, "identity_gate_test")

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	// If the real store open is ever reached without refusing first (the bug),
	// fail fast: never auto-start a server from a test.
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_SKIP_IDENTITY_CHECK", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	t.Cleanup(func() { store = oldStore })

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}

	probe := &cobra.Command{
		Use:  "identity-gate-refuse-probe",
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	rootCmd.AddCommand(probe)
	t.Cleanup(func() { rootCmd.RemoveCommand(probe) })

	err := rootCmd.PersistentPreRunE(probe, nil)
	if err == nil {
		t.Fatal("expected PersistentPreRunE to fail against an unreachable server-mode database; test-environment precondition broken")
	}

	const wantMarker = "could not verify workspace identity"
	if !strings.Contains(err.Error(), wantMarker) {
		t.Errorf("PersistentPreRunE error = %q, want it to contain %q -- a preview-open failure for a reason other than first-time bootstrap must refuse before the real, mutating store open runs (be-0gfcs: the guard cannot prevent what it is guarding against if it silently no-ops here)", err.Error(), wantMarker)
	}
	if store != nil {
		t.Error("PersistentPreRunE must not have opened the real store after refusing on an unverifiable identity preview")
	}
}
