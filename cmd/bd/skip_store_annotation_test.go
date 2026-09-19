package main

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
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
