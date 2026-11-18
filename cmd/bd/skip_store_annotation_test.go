package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
)

// annotate returns a command carrying the skip-store annotation set to v.
func annotate(use, v string) *cobra.Command {
	return &cobra.Command{Use: use, Annotations: map[string]string{skipStoreAnnotation: v}}
}

func TestCommandOptsOutOfStore(t *testing.T) {
	// Build a small tree: root -> parent -> child, so ancestor-walking is exercised.
	newTree := func(root, parent, child *cobra.Command) *cobra.Command {
		if parent != nil {
			if child != nil {
				parent.AddCommand(child)
			}
			root.AddCommand(parent)
		}
		return child
	}

	tests := []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{
			name: "self annotated 1 opts out",
			cmd:  annotate("login", "1"),
			want: true,
		},
		{
			name: "no annotations map does not opt out",
			cmd:  &cobra.Command{Use: "add"},
			want: false,
		},
		{
			name: "annotation present but not 1 does not opt out",
			cmd:  annotate("add", "0"),
			want: false,
		},
		{
			name: "empty annotation value does not opt out",
			cmd:  annotate("add", ""),
			want: false,
		},
		{
			name: "child inherits annotated parent",
			cmd:  newTree(&cobra.Command{Use: "bd"}, annotate("remote", "1"), &cobra.Command{Use: "use"}),
			want: true,
		},
		{
			name: "grandchild inherits annotated root ancestor",
			cmd:  newTree(annotate("bd", "1"), &cobra.Command{Use: "remote"}, &cobra.Command{Use: "use"}),
			want: true,
		},
		{
			name: "child of unannotated parent does not opt out",
			cmd:  newTree(&cobra.Command{Use: "bd"}, &cobra.Command{Use: "remote"}, &cobra.Command{Use: "use"}),
			want: false,
		},
		{
			name: "annotated child under unannotated parent opts out",
			cmd:  newTree(&cobra.Command{Use: "bd"}, &cobra.Command{Use: "remote"}, annotate("use", "1")),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandOptsOutOfStore(tt.cmd); got != tt.want {
				t.Errorf("commandOptsOutOfStore(%q) = %v, want %v", tt.cmd.Name(), got, tt.want)
			}
		})
	}
}

// TestCommandOptsOutOfStore_NilSafe guards the nil-command edge (the loop guard
// must terminate rather than panic).
func TestCommandOptsOutOfStore_NilSafe(t *testing.T) {
	if commandOptsOutOfStore(nil) {
		t.Fatal("nil command must not opt out of store init")
	}
}

// TestPersistentPreRunHonorsSkipStoreAnnotation exercises the behavioral change
// end-to-end: PersistentPreRunE must route a command carrying skipStoreAnnotation
// down the no-store early-return path instead of opening the database. Without
// this, a refactor that drops the commandOptsOutOfStore call from the store-skip
// gate would silently break every annotation-based command (doctor, and any
// build-tagged command that opts out via the annotation) while the pure-helper
// tests above stay green.
//
// The test is self-validating via a control: a non-annotated command run in the
// same environment must reach store init and fail to open the configured
// (server-mode, absent) database. That control proves the environment really does
// attempt a store open for a non-skipped command — so the annotated command
// returning nil without assigning the global store is a meaningful signal that
// the annotation short-circuited the open, not that the open happened to succeed.
// If the store open ever stops failing here, the control fails loudly rather than
// letting a dropped gate pass silently. serverMode is deliberately not asserted:
// it is set on both the skip and full-store paths, so it does not discriminate.
func TestPersistentPreRunHonorsSkipStoreAnnotation(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	writeMetadataConfig(t, beadsDir, configfile.DoltModeServer, "skip_annotation_test")

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	// If the store IS opened (a broken gate), fail fast: never auto-start a server.
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	t.Cleanup(func() { store = oldStore })

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}

	// Return the mutable globals a PersistentPreRunE drive touches to zero so the
	// two independent drives below cannot cross-contaminate.
	resetPreRunGlobals := func() {
		serverMode = false
		cmdCtx = nil
		dbPath = ""
		readonlyMode = false
		doltAutoCommit = ""
		store = nil
	}

	// Subject: opts out of store init purely via the annotation (it is NOT in
	// noDbCommands), attached to rootCmd so it classifies as a top-level command.
	// It must take the no-store early return — PersistentPreRunE returns nil and
	// never assigns the global store.
	resetPreRunGlobals()
	annotated := &cobra.Command{
		Use:         "skipstoreprobe",
		Annotations: map[string]string{skipStoreAnnotation: "1"},
		RunE:        func(*cobra.Command, []string) error { return nil },
	}
	rootCmd.AddCommand(annotated)
	t.Cleanup(func() { rootCmd.RemoveCommand(annotated) })
	if err := rootCmd.PersistentPreRunE(annotated, nil); err != nil {
		t.Fatalf("annotated command must skip store init and return nil, got: %v", err)
	}
	if store != nil {
		t.Fatal("annotated command must not open the store")
	}

	// Control: the same shape WITHOUT the annotation (and not in noDbCommands)
	// must reach store init and fail to open the absent server-mode database.
	resetPreRunGlobals()
	control := &cobra.Command{
		Use:  "storeprobe-control",
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	rootCmd.AddCommand(control)
	t.Cleanup(func() { rootCmd.RemoveCommand(control) })
	if err := rootCmd.PersistentPreRunE(control, nil); err == nil {
		t.Fatal("control (non-annotated) command should attempt store init and fail to open the absent database; test-environment precondition broken")
	}
}

// upgradeInspectionFixture is an isolated server-mode workspace whose database
// is absent and whose server may not be auto-started: any command that
// reaches store init fails to open it. It returns the .beads directory.
func upgradeInspectionFixture(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	writeMetadataConfig(t, beadsDir, configfile.DoltModeServer, "upgrade_inspection_test")

	t.Chdir(repoDir)
	t.Setenv("HOME", repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BD_DISABLE_METRICS", "1")
	t.Setenv("BD_ALLOW_MIGRATE", "")
	t.Setenv("BD_ALLOW_REMOTE_MIGRATE", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	oldUpgradeDetected := versionUpgradeDetected
	oldPreviousVersion := previousVersion
	oldUpgradeAcknowledged := upgradeAcknowledged
	t.Cleanup(func() {
		store = oldStore
		versionUpgradeDetected = oldUpgradeDetected
		previousVersion = oldPreviousVersion
		upgradeAcknowledged = oldUpgradeAcknowledged
	})
	return beadsDir
}

// resetUpgradeInspectionGlobals returns the globals one bd invocation would
// start with, so successive drives in one test behave like separate processes.
func resetUpgradeInspectionGlobals() {
	serverMode = false
	cmdCtx = nil
	dbPath = ""
	readonlyMode = false
	doltAutoCommit = ""
	jsonOutput = false
	store = nil
	versionUpgradeDetected = false
	previousVersion = ""
	upgradeAcknowledged = false
}

// driveUpgradeInspection runs the root hook and then the leaf's RunE, the way
// cobra does for `bd upgrade <leaf>`, and returns the leaf's stdout. The hook
// must succeed without assigning the global store.
func driveUpgradeInspection(t *testing.T, leaf *cobra.Command, asJSON bool) string {
	t.Helper()
	resetUpgradeInspectionGlobals()
	if err := rootCmd.PersistentPreRunE(leaf, nil); err != nil {
		t.Fatalf("bd upgrade %s: root hook must skip store init, got: %v", leaf.Name(), err)
	}
	if store != nil {
		t.Fatalf("bd upgrade %s opened the store", leaf.Name())
	}
	// The hook rebinds jsonOutput from config when --json was not passed;
	// set it afterwards, as the flag would have.
	jsonOutput = asJSON
	return captureStdout(t, func() error { return leaf.RunE(leaf, nil) })
}

// TestUpgradeReadOnlySubcommandsSkipStoreInit pins GH#6669 at the hook: `bd
// upgrade status` and `bd upgrade review` must succeed on a workspace whose
// database cannot be opened, because they never need it. `bd upgrade ack` is
// the control: it keeps the store-backed path, so in the same environment it
// must still reach store init and fail — which also proves the fixture really
// does refuse a store open.
func TestUpgradeReadOnlySubcommandsSkipStoreInit(t *testing.T) {
	beadsDir := upgradeInspectionFixture(t)
	localVersionPath := filepath.Join(beadsDir, localVersionFile)

	for _, leaf := range []*cobra.Command{upgradeStatusCmd, upgradeReviewCmd} {
		for _, asJSON := range []bool{false, true} {
			out := driveUpgradeInspection(t, leaf, asJSON)
			if strings.TrimSpace(out) == "" {
				t.Errorf("bd upgrade %s (json=%v) printed nothing", leaf.Name(), asJSON)
			}
			if asJSON && leaf == upgradeStatusCmd {
				var got map[string]any
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Errorf("bd upgrade status --json is not JSON: %v\n%s", err, out)
				} else if got["upgraded"] != false || got["current_version"] != Version {
					t.Errorf("bd upgrade status --json = %v, want no upgrade at %s", got, Version)
				}
			}
		}
	}
	// An absent marker is reported as such, never created by an inspection.
	if _, err := os.Stat(localVersionPath); !os.IsNotExist(err) {
		t.Errorf("upgrade inspection created %s (stat err=%v)", localVersionFile, err)
	}

	// Control: ack still goes through store init.
	resetUpgradeInspectionGlobals()
	if err := rootCmd.PersistentPreRunE(upgradeAckCmd, nil); err == nil {
		t.Fatal("bd upgrade ack should still reach store init and fail to open the absent database; fixture precondition broken")
	}

	// Outside any workspace both inspections refuse as the store-backed path
	// did before they skipped it, and as ack still does: the same stderr,
	// nothing on stdout, exit 1. Answering "no upgrade" there would read a
	// misdirected probe as an up-to-date workspace. GH#4402's user-level
	// ~/.beads case is TestUserGlobalBeadsRefusalNamesTheSkippedDir's.
	t.Run("no workspace", func(t *testing.T) {
		// Isolate as the rows above do, then leave the workspace for an empty
		// directory.
		upgradeInspectionFixture(t)
		outside := t.TempDir()
		t.Chdir(outside)
		t.Setenv("HOME", outside)
		t.Setenv("BEADS_DIR", "")
		beads.ResetCaches()
		git.ResetCaches()
		t.Cleanup(func() {
			beads.ResetCaches()
			git.ResetCaches()
		})

		resetUpgradeInspectionGlobals()
		var ackErr error
		ackOut, want := captureUpgradeStdio(t, func() { ackErr = rootCmd.PersistentPreRunE(upgradeAckCmd, nil) })
		if code, ok := exitCodeFromError(ackErr); !ok || code != 1 || ackOut != "" ||
			!strings.HasPrefix(want, "Error: no beads database found\n") {
			t.Fatalf("control: bd upgrade ack = (%v, stdout %q, stderr %q), want the no-database refusal; fixture precondition broken", ackErr, ackOut, want)
		}

		for _, leaf := range []*cobra.Command{upgradeStatusCmd, upgradeReviewCmd} {
			for _, asJSON := range []bool{false, true} {
				stdout, stderr, err := driveRefusedUpgradeInspection(t, leaf, asJSON)
				if code, ok := exitCodeFromError(err); !ok || code != 1 {
					t.Errorf("bd upgrade %s (json=%v) outside a workspace: err = %v, want exit 1", leaf.Name(), asJSON, err)
				}
				if stdout != "" {
					t.Errorf("bd upgrade %s (json=%v) outside a workspace printed on stdout: %q", leaf.Name(), asJSON, stdout)
				}
				if stderr != want {
					t.Errorf("bd upgrade %s (json=%v) outside a workspace:\nstderr %q\nwant ack's %q", leaf.Name(), asJSON, stderr, want)
				}
			}
		}
	})
}

// driveRefusedUpgradeInspection runs the root hook and then the leaf's RunE
// like driveUpgradeInspection, for a run whose RunE refuses. The hook must
// still skip store init; it returns RunE's stdout, stderr and error.
func driveRefusedUpgradeInspection(t *testing.T, leaf *cobra.Command, asJSON bool) (stdout, stderr string, err error) {
	t.Helper()
	resetUpgradeInspectionGlobals()
	if hookErr := rootCmd.PersistentPreRunE(leaf, nil); hookErr != nil {
		t.Fatalf("bd upgrade %s: root hook must skip store init, got: %v", leaf.Name(), hookErr)
	}
	if store != nil {
		t.Fatalf("bd upgrade %s opened the store", leaf.Name())
	}
	jsonOutput = asJSON
	stdout, stderr = captureUpgradeStdio(t, func() { err = leaf.RunE(leaf, nil) })
	return stdout, stderr, err
}

// captureUpgradeStdio is captureStdio serialized against the package's other
// stdio redirections.
func captureUpgradeStdio(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	return captureStdio(t, fn)
}

// TestUpgradeReadOnlySubcommandsPreserveVersionMarker pins the marker half of
// GH#6669: status and review detect an upgrade without consuming
// .beads/.local_version, the one-shot signal the next store-backed command's
// version-bump reconciliation reads. Run in succession, both must report the
// same prior version and leave the marker's bytes alone; a downgrade marker is
// reported as no upgrade and is not rewritten either.
func TestUpgradeReadOnlySubcommandsPreserveVersionMarker(t *testing.T) {
	t.Run("older marker", func(t *testing.T) {
		beadsDir := upgradeInspectionFixture(t)
		localVersionPath := filepath.Join(beadsDir, localVersionFile)
		marker := []byte("0.9.0\n")
		if err := os.WriteFile(localVersionPath, marker, 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		out := driveUpgradeInspection(t, upgradeStatusCmd, true)
		var status map[string]any
		if err := json.Unmarshal([]byte(out), &status); err != nil {
			t.Fatalf("bd upgrade status --json is not JSON: %v\n%s", err, out)
		}
		if status["upgraded"] != true || status["previous_version"] != "0.9.0" {
			t.Errorf("bd upgrade status --json = %v, want upgraded from 0.9.0", status)
		}
		assertMarkerBytes(t, localVersionPath, marker, "status")

		out = driveUpgradeInspection(t, upgradeReviewCmd, true)
		var review map[string]any
		if err := json.Unmarshal([]byte(out), &review); err != nil {
			t.Fatalf("bd upgrade review --json after status is not JSON: %v\n%s", err, out)
		}
		if review["previous_version"] != "0.9.0" {
			t.Errorf("bd upgrade review --json after status = %v, want previous_version 0.9.0", review)
		}
		assertMarkerBytes(t, localVersionPath, marker, "review")

		out = driveUpgradeInspection(t, upgradeStatusCmd, false)
		if !strings.Contains(out, "upgraded from v0.9.0") {
			t.Errorf("text bd upgrade status after review = %q, want the 0.9.0 upgrade", out)
		}
		assertMarkerBytes(t, localVersionPath, marker, "status (text)")
	})

	t.Run("newer marker", func(t *testing.T) {
		beadsDir := upgradeInspectionFixture(t)
		localVersionPath := filepath.Join(beadsDir, localVersionFile)
		marker := []byte("999.0.0\n")
		if err := os.WriteFile(localVersionPath, marker, 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		out := driveUpgradeInspection(t, upgradeStatusCmd, true)
		var status map[string]any
		if err := json.Unmarshal([]byte(out), &status); err != nil {
			t.Fatalf("bd upgrade status --json is not JSON: %v\n%s", err, out)
		}
		if status["upgraded"] != false {
			t.Errorf("bd upgrade status --json on a downgrade = %v, want upgraded=false", status)
		}
		driveUpgradeInspection(t, upgradeReviewCmd, false)
		assertMarkerBytes(t, localVersionPath, marker, "status+review")
	})
}

func assertMarkerBytes(t *testing.T, path string, want []byte, after string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s after %s: %v", localVersionFile, after, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s rewrote %s: got %q, want %q", after, localVersionFile, got, want)
	}
}
