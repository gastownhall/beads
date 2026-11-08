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
	"github.com/steveyegge/beads/internal/beads"
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
func TestSelectedWorkspaceWhereProvenance(t *testing.T) {
	fixture := newRedirectedCallerFixture(t)
	targetDir := t.TempDir()
	targetBeads := filepath.Join(targetDir, ".beads")
	writeSelectedServerTarget(t, targetBeads, "target_db", "targetpfx")

	assertWhereDescribesTarget := func(t *testing.T) {
		t.Helper()
		result, err := runSelectedWhereJSON(t)
		if err != nil {
			t.Fatalf("where --json: %v", err)
		}
		assertSelectedDirEqual(t, result.Path, targetBeads, "where path")
		assertSelectedDirEqual(t, result.DatabasePath, filepath.Join(targetBeads, "dolt"), "where database_path")
		if result.Prefix != "targetpfx" {
			t.Errorf("where prefix = %q, want %q", result.Prefix, "targetpfx")
		}
		if result.RedirectedFrom != "" {
			t.Errorf("where redirected_from = %q, want none: the selection did not come through that redirect", result.RedirectedFrom)
		}
	}

	t.Run("foreign direct target via dash-C", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, whereCmd, selectViaChangeDir(targetDir)); err != nil {
			t.Fatalf("PersistentPreRunE(where): %v", err)
		}
		assertNoCallerDatabaseLeak(t)
		assertWhereDescribesTarget(t)
	})

	t.Run("foreign direct target via BEADS_DIR", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, whereCmd, selectViaBeadsDir(targetBeads)); err != nil {
			t.Fatalf("PersistentPreRunE(where): %v", err)
		}
		assertNoCallerDatabaseLeak(t)
		assertWhereDescribesTarget(t)
	})

	// The caller's own workspace still reports its redirect (be-xil /
	// bd-wayc3): without this row the fix could pass by never preserving.
	t.Run("caller's own redirect keeps provenance", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, whereCmd, nil); err != nil {
			t.Fatalf("PersistentPreRunE(where): %v", err)
		}
		result, err := runSelectedWhereJSON(t)
		if err != nil {
			t.Fatalf("where --json: %v", err)
		}
		assertSelectedDirEqual(t, result.Path, fixture.sharedBeads, "where path")
		assertSelectedDirEqual(t, result.DatabasePath, filepath.Join(fixture.sharedBeads, "dolt"), "where database_path")
		if result.Prefix != "sharedpfx" {
			t.Errorf("where prefix = %q, want %q", result.Prefix, "sharedpfx")
		}
		assertSelectedDirEqual(t, result.RedirectedFrom, fixture.callerBeads, "where redirected_from")
	})
}

// TestSelectedWorkspaceRedirectDatabase pins AC-4: two clones of one shared
// database have distinct redirect-source names; selecting clone2 from
// clone1's checkout (or from outside any repository) reports clone2's
// identity — its redirect-source database for context, its own stub for
// where provenance — never clone1's.
func TestSelectedWorkspaceRedirectDatabase(t *testing.T) {
	root := t.TempDir()
	sharedBeads := filepath.Join(root, "shared", ".beads")
	writeSelectedServerTarget(t, sharedBeads, "shared_db", "sharedpfx")

	newClone := func(name, database string) selectedWorkspace {
		dir := filepath.Join(root, name)
		initGitRepoForContextTest(t, dir)
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			t.Fatalf("mkdir %s beads dir: %v", name, err)
		}
		if err := (&configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			DoltDatabase: database,
		}).Save(beadsDir); err != nil {
			t.Fatalf("save %s stub metadata: %v", name, err)
		}
		writeRedirectForTest(t, beadsDir, sharedBeads)
		return selectedWorkspace{dir: dir, beadsDir: beadsDir}
	}
	clone1 := newClone("clone1", "clone1_db")
	clone2 := newClone("clone2", "clone2_db")

	assertContextNamesClone2 := func(t *testing.T) {
		t.Helper()
		info := runSelectedContextJSON(t)
		assertSelectedDirEqual(t, info.BeadsDir, sharedBeads, "context beads_dir")
		if info.Database != "clone2_db" {
			t.Errorf("context database = %q, want clone2's redirect-source %q", info.Database, "clone2_db")
		}
		if info.Database == "clone1_db" {
			t.Errorf("context database = clone1_db: the caller's redirect-source database leaked into the selection")
		}
	}

	t.Run("dash-C clone2 from clone1", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, clone1.dir, contextCmd, selectViaChangeDir(clone2.dir)); err != nil {
			t.Fatalf("PersistentPreRunE(context): %v", err)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "clone2_db" {
			t.Errorf("BEADS_DOLT_SERVER_DATABASE = %q, want clone2's %q", got, "clone2_db")
		}
		assertContextNamesClone2(t)
	})

	t.Run("dash-C clone2 from outside any repository", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, t.TempDir(), contextCmd, selectViaChangeDir(clone2.dir)); err != nil {
			t.Fatalf("PersistentPreRunE(context): %v", err)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "clone2_db" {
			t.Errorf("BEADS_DOLT_SERVER_DATABASE = %q, want clone2's %q", got, "clone2_db")
		}
		assertContextNamesClone2(t)
	})

	t.Run("where reports clone2 provenance", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, clone1.dir, whereCmd, selectViaChangeDir(clone2.dir)); err != nil {
			t.Fatalf("PersistentPreRunE(where): %v", err)
		}
		result, err := runSelectedWhereJSON(t)
		if err != nil {
			t.Fatalf("where --json: %v", err)
		}
		assertSelectedDirEqual(t, result.Path, sharedBeads, "where path")
		assertSelectedDirEqual(t, result.RedirectedFrom, clone2.beadsDir, "where redirected_from")
		if sameDirPath(result.RedirectedFrom, clone1.beadsDir) {
			t.Errorf("where redirected_from = clone1's stub: the caller's redirect was reported as the selection's provenance")
		}
	})
}

// TestSelectedWorkspaceSelectionEdges pins AC-6: the selection rule keeps the
// precedences the historical regressions fixed — the caller's own redirect
// (be-xil), BEADS_DIR naming the redirect target (bd-wayc3), BEADS_DIR naming
// the redirecting stub itself, explicit database targets (be-fyt), a
// user-exported BEADS_DOLT_SERVER_DATABASE, and a .beads/.env selector — and
// never inherits the caller's source into a foreign or nested selection.
func TestSelectedWorkspaceSelectionEdges(t *testing.T) {
	fixture := newRedirectedCallerFixture(t)
	targetDir := t.TempDir()
	targetBeads := filepath.Join(targetDir, ".beads")
	writeSelectedServerTarget(t, targetBeads, "target_db", "targetpfx")

	// bd-wayc3: BEADS_DIR pre-set to the redirect target still means the
	// caller's own redirect, so the caller's source database is preserved.
	t.Run("BEADS_DIR naming the redirect target keeps the caller's source", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, contextCmd, selectViaBeadsDir(fixture.sharedBeads)); err != nil {
			t.Fatalf("PersistentPreRunE(context): %v", err)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "caller_db" {
			t.Errorf("BEADS_DOLT_SERVER_DATABASE = %q, want the caller's own %q", got, "caller_db")
		}
		info := runSelectedContextJSON(t)
		assertSelectedDirEqual(t, info.BeadsDir, fixture.sharedBeads, "context beads_dir")
		if info.Database != "caller_db" {
			t.Errorf("context database = %q, want the caller's preserved %q", info.Database, "caller_db")
		}
	})

	// A user-set BEADS_DIR pointing at the redirecting stub itself keeps
	// its own redirect. At base the BEADS_DIR rebind to the post-redirect
	// target erased the stub before where's cwd walk, so the redirect was
	// reported nowhere; retention keeps it (ported from
	// TestFindOriginalBeadsDir_BeadsDirEnvWithRedirectReturnsEnv).
	t.Run("BEADS_DIR naming the redirecting stub keeps its own redirect", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, t.TempDir(), whereCmd, selectViaBeadsDir(fixture.callerBeads)); err != nil {
			t.Fatalf("PersistentPreRunE(where): %v", err)
		}
		result, err := runSelectedWhereJSON(t)
		if err != nil {
			t.Fatalf("where --json: %v", err)
		}
		assertSelectedDirEqual(t, result.Path, fixture.sharedBeads, "where path")
		assertSelectedDirEqual(t, result.RedirectedFrom, fixture.callerBeads, "where redirected_from")
	})

	// Ported from TestFindOriginalBeadsDir_BeadsDirEnvWithoutRedirectFallsThrough:
	// BEADS_DIR names the shared target, the cwd worktree holds the redirect,
	// and where still reports the worktree's redirect as provenance.
	t.Run("BEADS_DIR naming the target falls through to the cwd redirect", func(t *testing.T) {
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, whereCmd, selectViaBeadsDir(fixture.sharedBeads)); err != nil {
			t.Fatalf("PersistentPreRunE(where): %v", err)
		}
		result, err := runSelectedWhereJSON(t)
		if err != nil {
			t.Fatalf("where --json: %v", err)
		}
		assertSelectedDirEqual(t, result.Path, fixture.sharedBeads, "where path")
		assertSelectedDirEqual(t, result.RedirectedFrom, fixture.callerBeads, "where redirected_from")
	})

	// be-fyt: an explicit database target suppresses ambient preservation.
	t.Run("explicit dash-db target suppresses ambient preservation", func(t *testing.T) {
		targetDBPath := filepath.Join(targetBeads, "dolt")
		selector := func(t *testing.T) {
			dbPath = targetDBPath
			if flag := rootCmd.PersistentFlags().Lookup("db"); flag != nil {
				flag.Changed = true
			}
			t.Cleanup(func() {
				dbPath = ""
				if flag := rootCmd.PersistentFlags().Lookup("db"); flag != nil {
					flag.Changed = false
				}
			})
		}
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, doctorCmd, selector); err != nil {
			t.Fatalf("PersistentPreRunE(doctor): %v", err)
		}
		assertNoCallerDatabaseLeak(t)
	})

	// A user-exported BEADS_DOLT_SERVER_DATABASE keeps its precedence over
	// anything bd would preserve.
	t.Run("user-exported database override wins", func(t *testing.T) {
		selector := func(t *testing.T) {
			t.Setenv("BEADS_DOLT_SERVER_DATABASE", "userdb")
		}
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, doctorCmd, selector); err != nil {
			t.Fatalf("PersistentPreRunE(doctor): %v", err)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "userdb" {
			t.Errorf("BEADS_DOLT_SERVER_DATABASE = %q, want the user's exported %q", got, "userdb")
		}
	})

	// A .beads/.env BEADS_DIR selector is user-authored selection, same as
	// exporting the variable: the foreign target it names is described by
	// its own database, not the caller's.
	t.Run("dot-env selector routes to a foreign target", func(t *testing.T) {
		envFile := filepath.Join(fixture.sharedBeads, ".env")
		if err := os.WriteFile(envFile, []byte("BEADS_DIR="+targetBeads+"\n"), 0o600); err != nil {
			t.Fatalf("write .env selector: %v", err)
		}
		// The fixture's shared workspace outlives this row; later rows run
		// commands from the same caller and must not re-select this .env.
		t.Cleanup(func() { _ = os.Remove(envFile) })
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, contextCmd, nil); err != nil {
			t.Fatalf("PersistentPreRunE(context): %v", err)
		}
		assertNoCallerDatabaseLeak(t)
		info := runSelectedContextJSON(t)
		assertSelectedDirEqual(t, info.BeadsDir, targetBeads, "context beads_dir")
		if info.Database != "target_db" {
			t.Errorf("context database = %q, want the target's own %q", info.Database, "target_db")
		}
	})

	// A direct workspace nested inside the caller's redirecting repo is a
	// foreign selection: the ancestor repo's redirect is not its provenance.
	t.Run("nested direct workspace under an ancestor redirect", func(t *testing.T) {
		nestedDir := filepath.Join(fixture.callerDir, "nested")
		nestedBeads := filepath.Join(nestedDir, ".beads")
		writeSelectedServerTarget(t, nestedBeads, "nested_db", "nestedpfx")
		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, contextCmd, selectViaChangeDir(nestedDir)); err != nil {
			t.Fatalf("PersistentPreRunE(context): %v", err)
		}
		assertNoCallerDatabaseLeak(t)
		info := runSelectedContextJSON(t)
		assertSelectedDirEqual(t, info.BeadsDir, nestedBeads, "context beads_dir")
		if info.Database != "nested_db" {
			t.Errorf("context database = %q, want the nested workspace's own %q", info.Database, "nested_db")
		}
	})

	// The retained source is per-command: a second command in the same
	// process selects its own source, not the previous command's. A fresh
	// process starts with the environment unset; simulate that between runs.
	t.Run("retention does not survive into the next command", func(t *testing.T) {
		t.Chdir(fixture.callerDir)
		resetRepoContextCachesForTest(t)
		config.ResetForTesting()
		t.Cleanup(config.ResetForTesting)
		savePersistentPreRunState(t)
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
		t.Setenv("BEADS_DOLT_SERVER_HOST", "")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")

		// First command: the caller's own workspace, preservation active.
		if err := rootCmd.PersistentPreRunE(doctorCmd, nil); err != nil {
			t.Fatalf("PersistentPreRunE(doctor), first command: %v", err)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != "caller_db" {
			t.Fatalf("first command preserved %q, want the caller's own %q", got, "caller_db")
		}

		// Fresh process: the ambient environment variable is unset again.
		_ = os.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
		setChangeDirForTest(t, targetDir)
		if err := rootCmd.PersistentPreRunE(contextCmd, nil); err != nil {
			t.Fatalf("PersistentPreRunE(context), second command: %v", err)
		}
		assertNoCallerDatabaseLeak(t)
		info := runSelectedContextJSON(t)
		if info.Database != "target_db" {
			t.Errorf("second command context database = %q, want the target's own %q", info.Database, "target_db")
		}
	})

	// Ported from TestFindOriginalBeadsDirRespectsOSTempRootCeiling: plain
	// discovery still passes over a redirecting .beads above the temp root,
	// and where answers with the no-workspace refusal instead of reporting it.
	t.Run("plain discovery passes over the temp root", func(t *testing.T) {
		tempRoot := filepath.Join(t.TempDir(), "tmp")
		rootBeads := filepath.Join(tempRoot, ".beads")
		if err := os.MkdirAll(rootBeads, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rootBeads, beads.RedirectFileName), []byte(filepath.Join(t.TempDir(), ".beads")), 0o600); err != nil {
			t.Fatal(err)
		}
		child := filepath.Join(tempRoot, "fixture")
		if err := os.MkdirAll(child, 0o700); err != nil {
			t.Fatal(err)
		}

		t.Setenv("TMPDIR", tempRoot)
		// Pin HOME: with no workspace discovered, the no-store guard falls
		// back to undiscovered candidates including the user-global
		// ~/.beads, which belongs to whoever runs the test, not this row.
		t.Setenv("HOME", t.TempDir())
		if err := runSelectedWorkspacePreRun(t, child, whereCmd, nil); err != nil {
			t.Fatalf("PersistentPreRunE(where): %v", err)
		}

		jsonOutput = true
		rootCtx = context.Background()
		// RunE prints the no_beads_directory JSON, then exits silently.
		output, runErr := captureStdoutWithError(t, func() error {
			return whereCmd.RunE(whereCmd, nil)
		})
		var exitErr *exitError
		if !errors.As(runErr, &exitErr) {
			t.Fatalf("where RunE = %v, want the silent no-workspace exit", runErr)
		}
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(output), &payload); err != nil {
			t.Fatalf("where --json did not parse: %v\noutput:\n%s", err, output)
		}
		if payload.Error != "no_beads_directory" {
			t.Errorf("where error = %q, want no_beads_directory; output:\n%s", payload.Error, output)
		}
	})
}

// TestSelectedWorkspaceRemoteStartup pins AC-7: a remote workspace without
// local database files — proxied-server, a registered backend, or a
// host-inferred server — selected via -C from a redirected caller keeps its
// admission path and identity, without the caller's redirect-source database
// name injected.
func TestSelectedWorkspaceRemoteStartup(t *testing.T) {
	fixture := newRedirectedCallerFixture(t)

	// doctor on a proxied-server workspace answers with the typed
	// capability refusal, not a corrupted startup: admission stays
	// available and the caller's database stays out of the environment.
	t.Run("proxied-server target keeps admission", func(t *testing.T) {
		targetDir := t.TempDir()
		targetBeads := filepath.Join(targetDir, ".beads")
		if err := os.MkdirAll(targetBeads, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := (&configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeProxiedServer,
			DoltDatabase: "proxied_db",
		}).Save(targetBeads); err != nil {
			t.Fatalf("save metadata: %v", err)
		}

		var preRunErr error
		stderr := captureStderr(t, func() {
			preRunErr = runSelectedWorkspacePreRun(t, fixture.callerDir, doctorCmd, selectViaChangeDir(targetDir))
		})
		// HandleProxyCapabilityError prints the typed refusal and exits
		// with its exit code; admission answered is the refusal on stderr.
		var exitErr *exitError
		if !errors.As(preRunErr, &exitErr) {
			t.Fatalf("PersistentPreRunE(doctor) = %v, want the proxy refusal's exit", preRunErr)
		}
		if !strings.Contains(stderr, "doctor is not supported in proxied-server mode") {
			t.Errorf("doctor refusal stderr = %q, want the proxy.doctor.unsupported refusal", stderr)
		}
		assertNoCallerDatabaseLeak(t)
	})

	// A registered remote backend (http) keeps its backend identity.
	t.Run("registered http backend target keeps its identity", func(t *testing.T) {
		targetDir := t.TempDir()
		targetBeads := filepath.Join(targetDir, ".beads")
		if err := os.MkdirAll(targetBeads, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := (&configfile.Config{Backend: "http"}).Save(targetBeads); err != nil {
			t.Fatalf("save metadata: %v", err)
		}

		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, contextCmd, selectViaChangeDir(targetDir)); err != nil {
			t.Fatalf("PersistentPreRunE(context): %v", err)
		}
		assertNoCallerDatabaseLeak(t)
		info := runSelectedContextJSON(t)
		assertSelectedDirEqual(t, info.BeadsDir, targetBeads, "context beads_dir")
		if info.Backend != "http" {
			t.Errorf("context backend = %q, want %q", info.Backend, "http")
		}
	})

	// A metadata-less workspace whose config.yaml names a remote server host
	// is a server workspace (GH#3545); context reports that host.
	t.Run("host-inferred server target keeps its host", func(t *testing.T) {
		targetDir := t.TempDir()
		targetBeads := filepath.Join(targetDir, ".beads")
		writeTestConfigYAML(t, targetBeads, "dolt:\n  host: 10.9.9.9\n")

		if err := runSelectedWorkspacePreRun(t, fixture.callerDir, contextCmd, selectViaChangeDir(targetDir)); err != nil {
			t.Fatalf("PersistentPreRunE(context): %v", err)
		}
		assertNoCallerDatabaseLeak(t)
		info := runSelectedContextJSON(t)
		assertSelectedDirEqual(t, info.BeadsDir, targetBeads, "context beads_dir")
		if info.ServerHost != "10.9.9.9" {
			t.Errorf("context server_host = %q, want the target's %q", info.ServerHost, "10.9.9.9")
		}
	})
}
