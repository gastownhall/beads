package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestIsBootstrapPreviewErr pins the classifier that decides whether the
// identity check's preview-open failure is a legitimate first run (skip
// silently) or something else (refuse). It must recognize the exact wrapping
// shape embeddeddolt produces for "no database on disk yet" and nothing else,
// including an error that merely happens to also be wrapped.
func TestIsBootstrapPreviewErr(t *testing.T) {
	// The bootstrap shape, built the way embeddeddolt's read-only open builds
	// it around its failed stat of the data dir.
	dataDir := filepath.Join(t.TempDir(), "embeddeddolt")
	_, statErr := os.Stat(dataDir)
	noEmbeddedDB := fmt.Errorf("%w at %s: %w", embeddeddolt.ErrNoEmbeddedDatabase, dataDir, statErr)
	unreadableDataDir := fmt.Errorf("%w at %s: %w", embeddeddolt.ErrNoEmbeddedDatabase, dataDir, os.ErrPermission)
	wrappedOther := fmt.Errorf("dial tcp 127.0.0.1:3306: %w", errors.New("connect: connection refused"))

	// Real ENOENTs from sources that are not "no local database yet" (bd-wlygc).
	// A hand-built errors.New can never satisfy errors.Is(err, os.ErrNotExist),
	// so only a real syscall error tells a narrow classifier from one that
	// matches every ENOENT. The dial is what dolt server mode wraps for a
	// configured socket nothing listens on; it is relative to a fresh cwd so
	// the path stays under the unix socket path limit wherever t.TempDir lives.
	t.Chdir(t.TempDir())
	_, dialErr := net.Dial("unix", "absent.sock")
	absentSocket := fmt.Errorf("Dolt server unreachable at %s: %w", "absent.sock", dialErr)
	_, manifestErr := os.Stat(filepath.Join(t.TempDir(), "beads", ".dolt", "noms", "manifest"))
	guttedDataDir := fmt.Errorf("embeddeddolt: open db: %w", manifestErr)
	for _, err := range []error{absentSocket, guttedDataDir} {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("precondition: %v is not an ENOENT, so it cannot tell a narrow classifier from a broad one", err)
		}
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"embedded store's no-database error", noEmbeddedDB, true},
		{"bare os.ErrNotExist", os.ErrNotExist, false},
		{"embedded store's data dir unreadable", unreadableDataDir, false},
		{"real ENOENT dialing an absent unix socket", absentSocket, false},
		{"real ENOENT from a file missing inside the data dir", guttedDataDir, false},
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
	// Zero it too: the assertion at the end of this test is that the gate did
	// NOT open the real store, which a leftover non-nil global from an earlier
	// test would satisfy vacuously (or fail spuriously).
	store = nil
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

	if err == nil {
		t.Fatal("expected PersistentPreRunE to fail against an unreachable server-mode database; test-environment precondition broken")
	}

	// HandleError's returned error carries only "exit code N" in its own
	// Error() string -- the real message goes to stderr (see HandleError in
	// errors.go) -- so the marker must be asserted against captured stderr,
	// not err.Error().
	const wantMarker = "could not verify workspace identity"
	if !strings.Contains(captured.String(), wantMarker) {
		t.Errorf("PersistentPreRunE stderr = %q, want it to contain %q -- a preview-open failure for a reason other than first-time bootstrap must refuse before the real, mutating store open runs (be-0gfcs: the guard cannot prevent what it is guarding against if it silently no-ops here)", captured.String(), wantMarker)
	}
	if store != nil {
		t.Error("PersistentPreRunE must not have opened the real store after refusing on an unverifiable identity preview")
	}
}
