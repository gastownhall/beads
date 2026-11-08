package main

// GH#5756: a workspace selected with -C or an exported BEADS_DIR must be
// described by its own database identity, not the caller's. Before the fix,
// the redirect-source database preservation keyed off the ambient cwd repo
// (beads.GetRedirectInfo), so `bd -C <foreign target>` run from inside a
// redirected clone exported the caller's redirect-source dolt_database into
// BEADS_DOLT_SERVER_DATABASE, `bd context` then reported the caller's
// database for the target, and `bd where` reported the caller's redirect as
// the target's provenance. These tests pin the selected workspace's own
// identity for both selection routes (-C and raw BEADS_DIR), keep the
// caller's own redirect working (be-xil / bd-wayc3), and keep explicit
// database targets and user-exported overrides on top.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// selectedWorkspace is a .beads workspace the tests select from a caller.
type selectedWorkspace struct {
	dir      string // the -C target (workspace root)
	beadsDir string // its .beads directory
}

// writeSelectedServerTarget writes a server-mode .beads whose metadata
// names database, with a dolt data directory and a config.yaml issue prefix,
// so context/where can report it without any test opening a store.
func writeSelectedServerTarget(t *testing.T, beadsDir, database, prefix string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o750); err != nil {
		t.Fatalf("mkdir dolt dir: %v", err)
	}
	if err := (&configfile.Config{
		Backend:      configfile.BackendDolt,
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: database,
		Database:     "dolt",
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	writeTestConfigYAML(t, beadsDir, "issue-prefix: "+prefix+"\n")
}

// selectedCallerFixture is the GH#5756 reporter's shape: a git-repo caller
// whose .beads holds a redirect to a shared workspace plus a metadata.json
// naming the caller's own redirect-source database caller_db. Selecting the
// caller's own workspace must preserve caller_db; selecting a foreign
// workspace must not.
type selectedCallerFixture struct {
	callerDir   string
	callerBeads string
	sharedBeads string
}

func newRedirectedCallerFixture(t *testing.T) selectedCallerFixture {
	t.Helper()
	root := t.TempDir()
	sharedBeads := filepath.Join(root, "shared", ".beads")
	writeSelectedServerTarget(t, sharedBeads, "shared_db", "sharedpfx")

	callerDir := filepath.Join(root, "caller")
	initGitRepoForContextTest(t, callerDir)
	callerBeads := filepath.Join(callerDir, ".beads")
	if err := os.MkdirAll(callerBeads, 0o700); err != nil {
		t.Fatalf("mkdir caller beads dir: %v", err)
	}
	if err := (&configfile.Config{
		Backend:      configfile.BackendDolt,
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "caller_db",
	}).Save(callerBeads); err != nil {
		t.Fatalf("save caller stub metadata: %v", err)
	}
	writeRedirectForTest(t, callerBeads, sharedBeads)

	return selectedCallerFixture{
		callerDir:   callerDir,
		callerBeads: callerBeads,
		sharedBeads: sharedBeads,
	}
}

// runSelectedWorkspacePreRun runs rootCmd.PersistentPreRunE for cmd with cwd
// inside the caller's redirected repo, after applying the row's workspace
// selector. It returns the PreRun error so refusal rows can assert on it.
func runSelectedWorkspacePreRun(t *testing.T, cwd string, cmd *cobra.Command, selector func(*testing.T)) error {
	t.Helper()
	t.Chdir(cwd)
	resetRepoContextCachesForTest(t)

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	// The selector runs after the ambient state is reset — rows like the
	// explicit --db target and the user-exported override must survive it.
	if selector != nil {
		selector(t)
	}
	return rootCmd.PersistentPreRunE(cmd, nil)
}

// selectViaChangeDir simulates `bd -C <workspace dir>`.
func selectViaChangeDir(dir string) func(*testing.T) {
	return func(t *testing.T) {
		setChangeDirForTest(t, dir)
	}
}

// selectViaBeadsDir simulates a caller who exported BEADS_DIR before bd ran.
func selectViaBeadsDir(beadsDir string) func(*testing.T) {
	return func(t *testing.T) {
		setBeadsDirStartupProvenanceForTest(t, true)
		t.Setenv("BEADS_DIR", beadsDir)
	}
}

// runSelectedContextJSON captures `bd context --json` for the selected
// workspace. jsonOutput is set here, after PersistentPreRunE, because the
// hook rebinds it from config.
func runSelectedContextJSON(t *testing.T) ContextInfo {
	t.Helper()
	originalRootCtx := rootCtx
	t.Cleanup(func() { rootCtx = originalRootCtx })

	jsonOutput = true
	rootCtx = context.Background()
	output := captureStdout(t, func() error {
		return contextCmd.RunE(contextCmd, nil)
	})

	var info ContextInfo
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		t.Fatalf("context --json did not parse: %v\noutput:\n%s", err, output)
	}
	return info
}

// runSelectedWhereJSON captures `bd where --json` for the selected workspace.
func runSelectedWhereJSON(t *testing.T) (WhereResult, error) {
	t.Helper()
	originalRootCtx := rootCtx
	t.Cleanup(func() { rootCtx = originalRootCtx })

	jsonOutput = true
	rootCtx = context.Background()
	output := captureStdout(t, func() error {
		return whereCmd.RunE(whereCmd, nil)
	})

	var result WhereResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return result, err
	}
	return result, nil
}

// assertSelectedDirEqual compares two directory strings the way production
// compares them (canonicalized, symlink-resolved).
func assertSelectedDirEqual(t *testing.T, got, want, what string) {
	t.Helper()
	if !sameDirPath(got, want) {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func assertNoCallerDatabaseLeak(t *testing.T) {
	t.Helper()
	if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "" {
		t.Errorf("selection leaked the caller's redirect-source database %q into BEADS_DOLT_SERVER_DATABASE", got)
	}
}

// TestSelectedWorkspaceDatabaseIsolation pins AC-1: a foreign server-mode
// workspace selected via -C or an exported BEADS_DIR reports the target's
// own dolt_database, and the caller's redirect-source database name is never
// exported on the caller's behalf.
func TestSelectedWorkspaceDatabaseIsolation(t *testing.T) {
	fixture := newRedirectedCallerFixture(t)
	targetDir := t.TempDir()
	targetBeads := filepath.Join(targetDir, ".beads")
	writeSelectedServerTarget(t, targetBeads, "target_db", "targetpfx")

	rows := []struct {
		name     string
		selector func(*testing.T)
	}{
		{name: "dash-C flag", selector: selectViaChangeDir(targetDir)},
		{name: "exported BEADS_DIR", selector: selectViaBeadsDir(targetBeads)},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if err := runSelectedWorkspacePreRun(t, fixture.callerDir, contextCmd, row.selector); err != nil {
				t.Fatalf("PersistentPreRunE(context): %v", err)
			}
			assertNoCallerDatabaseLeak(t)

			info := runSelectedContextJSON(t)
			assertSelectedDirEqual(t, info.BeadsDir, targetBeads, "context beads_dir")
			if info.Database != "target_db" {
				t.Errorf("context database = %q, want the target's own %q", info.Database, "target_db")
			}
		})
	}

	// doctor runs the same no-store startup path context does; its startup
	// must not export the caller's database either.
	t.Run("doctor startup", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, doctorCmd, selectViaChangeDir(targetDir)); err != nil {
			t.Fatalf("PersistentPreRunE(doctor): %v", err)
		}
		assertNoCallerDatabaseLeak(t)
	})
}

// TestSelectedWorkspaceStorelessIsolation pins AC-2: a config-only foreign
// workspace selected via -C gets the same no-beads-database refusal
// `cd <target> && bd list` gets, without the caller's database name copied
// into the environment along the way.
func TestSelectedWorkspaceStorelessIsolation(t *testing.T) {
	fixture := newRedirectedCallerFixture(t)
	targetDir := t.TempDir()
	writeTestConfigYAML(t, filepath.Join(targetDir, ".beads"), "issue-prefix: storeless\n")

	for _, cmd := range []*cobra.Command{listCmd, createCmd, updateCmd} {
		t.Run(cmd.Name(), func(t *testing.T) {
			var preRunErr error
			stderr := captureStderr(t, func() {
				preRunErr = runSelectedWorkspacePreRun(t, fixture.callerDir, cmd, selectViaChangeDir(targetDir))
			})

			if preRunErr == nil {
				t.Fatalf("PersistentPreRunE(%s) = nil, want the no-database refusal", cmd.Name())
			}
			var exitErr *exitError
			if !errors.As(preRunErr, &exitErr) {
				t.Fatalf("PersistentPreRunE(%s) = %v, want the silent no-database exit", cmd.Name(), preRunErr)
			}
			if !strings.Contains(stderr, "no beads database found") {
				t.Errorf("refusal stderr = %q, want the no-database refusal", stderr)
			}
			assertNoCallerDatabaseLeak(t)
		})
	}
}

// TestSelectedWorkspaceWhereProvenance pins AC-3: `bd where` describes the
// selected workspace — its path, database path, and prefix — and reports
// redirect provenance only for a selection that actually came through that
// redirect. A foreign selection made from inside a redirected caller reports
// no provenance; the caller's own selection keeps reporting its redirect.
