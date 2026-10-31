package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/testutil"
)

// GH#4402: implicit discovery passes over the user-level <home>/.beads, so a
// command run outside a project refuses instead of writing to the global
// database, and the refusal names the skipped directory. The skip reads the
// OS account home, which a bd subprocess's environment cannot repoint, so
// these run the command tree in-process with beads.SetUserHomeDirForTesting
// pointing it at a fixture home, as the bd init home-guard tests stub
// resolveUserHomeDir.

// userGlobalFixture is <tmp>/home holding an embedded user-level workspace
// (metadata.json plus an empty embeddeddolt/), with notes/ beside it outside
// any repository.
type userGlobalFixture struct {
	home, beadsDir, notes string
}

const userGlobalFixtureMetadata = "{\"database\": \"beads.db\"}\n"

func newUserGlobalFixture(t *testing.T) userGlobalFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := userGlobalFixture{
		home:     filepath.Join(root, "home"),
		beadsDir: filepath.Join(root, "home", ".beads"),
		notes:    filepath.Join(root, "home", "notes"),
	}
	if err := os.MkdirAll(f.notes, 0o700); err != nil {
		t.Fatal(err)
	}
	newFixtureWorkspace(t, f.beadsDir)
	return f
}

// newFixtureWorkspace creates beadsDir as an embedded workspace holding
// metadata.json and an empty embeddeddolt/.
func newFixtureWorkspace(t *testing.T, beadsDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(userGlobalFixtureMetadata), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertUntouched checks that nothing under <home>/.beads was created or
// modified: the refusal runs before any store is opened.
func (f userGlobalFixture) assertUntouched(t *testing.T) {
	t.Helper()
	assertFixtureWorkspaceUntouched(t, f.beadsDir)
}

// assertFixtureWorkspaceUntouched checks that nothing under beadsDir, which
// newFixtureWorkspace created, was created or modified: no command opened it.
func assertFixtureWorkspaceUntouched(t *testing.T, beadsDir string) {
	t.Helper()
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "embeddeddolt,metadata.json"; got != want {
		t.Errorf("%s holds %q, want exactly %q: the command must not touch it", beadsDir, got, want)
	}
	if data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json")); err != nil || string(data) != userGlobalFixtureMetadata {
		t.Errorf("%s/metadata.json was modified: %q, %v", beadsDir, data, err)
	}
	if sub, err := os.ReadDir(filepath.Join(beadsDir, "embeddeddolt")); err != nil || len(sub) != 0 {
		t.Errorf("%s/embeddeddolt holds %d entries (%v), want none: the command must not open or create its database", beadsDir, len(sub), err)
	}
}

// replaceUserGlobalDir replaces the fixture's <home>/.beads workspace with a
// directory holding only files (config.yaml gets an issue prefix, any other
// file is empty), or with nothing at all when files is nil.
func replaceUserGlobalDir(t *testing.T, f userGlobalFixture, files []string) {
	t.Helper()
	if err := os.RemoveAll(f.beadsDir); err != nil {
		t.Fatal(err)
	}
	if files == nil {
		return
	}
	if err := os.MkdirAll(f.beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		var data []byte
		if name == "config.yaml" {
			data = []byte("issue-prefix: gl\n")
		}
		if err := os.WriteFile(filepath.Join(f.beadsDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// writeUserGlobalImportInput writes a one-issue JSONL file for bd import and
// returns its path.
func writeUserGlobalImportInput(t *testing.T) string {
	t.Helper()
	input := filepath.Join(t.TempDir(), "in.jsonl")
	if err := os.WriteFile(input, []byte(`{"id":"gl-1","title":"probe","status":"open","priority":2,"issue_type":"task"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return input
}

// addUserGlobalLinkedWorktree makes mainRepo a git repository with one commit
// and adds a linked worktree of it at worktree.
func addUserGlobalLinkedWorktree(t *testing.T, mainRepo, worktree string) {
	t.Helper()
	if err := os.MkdirAll(mainRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, mainRepo)
	for _, args := range [][]string{
		{"commit", "--allow-empty", "-m", "init"},
		{"worktree", "add", worktree},
	} {
		cmd := gitCommand(args...)
		cmd.Dir = mainRepo
		cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// runInUserGlobalHome runs bd args in-process from dir, with the user-global
// skip and HOME pointed at home and env set on top, and returns the exit code
// and what the command printed.
func runInUserGlobalHome(t *testing.T, home, dir string, env map[string]string, args ...string) (exit int, stdout, stderr string) {
	t.Helper()
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)
	isolateBeadsDirForTest(t)
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for k, v := range env {
		t.Setenv(k, v)
	}
	t.Cleanup(beads.SetUserHomeDirForTesting(home))
	t.Chdir(dir)
	// Discovery caches the cwd's repository and workspace process-wide, and
	// the skip record lives with them: start from answers about dir, and
	// drop them again before the cwd is restored.
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})
	store, dbPath = nil, ""
	resetCommandFlags(rootCmd)
	t.Cleanup(func() { resetCommandFlags(rootCmd) })

	oldStdout, oldStderr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var outBuf, errBuf bytes.Buffer
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(&outBuf, rOut); done <- struct{}{} }()
	go func() { _, _ = io.Copy(&errBuf, rErr); done <- struct{}{} }()
	os.Stdout, os.Stderr = wOut, wErr

	rootCmd.SetArgs(args)
	execErr := rootCmd.Execute()
	rootCmd.SetArgs(nil)

	os.Stdout, os.Stderr = oldStdout, oldStderr
	_ = wOut.Close()
	_ = wErr.Close()
	<-done
	<-done

	if execErr != nil {
		code, ok := exitCodeFromError(execErr)
		if !ok {
			t.Fatalf("bd %s: %v (not an exit code)\nstderr:\n%s", strings.Join(args, " "), execErr, errBuf.String())
		}
		exit = code
	}
	return exit, outBuf.String(), errBuf.String()
}

// noDatabaseRefusal is what printNoDatabaseRefusal prints with no directory
// to name.
func noDatabaseRefusal() string {
	return "Error: no beads database found\n" +
		"Hint: " + diagHint() + "\n" +
		"      or set BEADS_DIR to point to your .beads directory\n"
}

// TestUserGlobalBeadsRefusalNamesTheSkippedDir pins the GH#4402 contract: a
// command run outside any project under the home directory refuses instead
// of binding the user-level workspace, names the directory and the
// per-command opt-in, and leaves the directory alone.
func TestUserGlobalBeadsRefusalNamesTheSkippedDir(t *testing.T) {
	isolateBeadsDirForTest(t)

	refusal := func(f userGlobalFixture) string {
		return noDatabaseRefusal() + userGlobalBeadsNote(runtime.GOOS, f.beadsDir) + "\n"
	}

	t.Run("remember", func(t *testing.T) {
		// The reporter's command: a write from outside any project must not
		// land in the global database.
		f := newUserGlobalFixture(t)
		exit, stdout, stderr := runInUserGlobalHome(t, f.home, f.notes, nil, "remember", "some note")
		if exit != 1 || stdout != "" || stderr != refusal(f) {
			t.Errorf("bd remember: exit %d, stdout %q\nstderr: %q\nwant exit 1, no stdout, stderr %q", exit, stdout, stderr, refusal(f))
		}
		f.assertUntouched(t)
	})

	t.Run("list --json", func(t *testing.T) {
		// --json does not turn the refusal into a JSON document.
		f := newUserGlobalFixture(t)
		exit, stdout, stderr := runInUserGlobalHome(t, f.home, f.notes, nil, "list", "--json")
		if exit != 1 || stdout != "" || stderr != refusal(f) {
			t.Errorf("bd list --json: exit %d, stdout %q\nstderr: %q\nwant exit 1, no stdout, stderr %q", exit, stdout, stderr, refusal(f))
		}
		f.assertUntouched(t)
	})

	t.Run("import from the home directory", func(t *testing.T) {
		// bd import falls back to the working directory's own .beads when
		// discovery finds none. In the home directory that is the
		// user-global one, which it must not write into.
		f := newUserGlobalFixture(t)
		input := filepath.Join(t.TempDir(), "in.jsonl")
		if err := os.WriteFile(input, []byte(`{"id":"gl-1","title":"probe from home","status":"open","priority":2,"issue_type":"task"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		exit, stdout, stderr := runInUserGlobalHome(t, f.home, f.home, nil, "import", input)
		if exit != 1 || stdout != "" || stderr != refusal(f) {
			t.Errorf("bd import: exit %d, stdout %q\nstderr: %q\nwant exit 1, no stdout, stderr %q", exit, stdout, stderr, refusal(f))
		}
		f.assertUntouched(t)
	})

	t.Run("import from a subdirectory of home", func(t *testing.T) {
		// Away from the home directory the fallback is notes/.beads, which
		// does not exist yet. Binding it must not climb to the user-global
		// .beads above it, and with a user-level workspace there the import
		// refuses with the note like every other command run from notes/.
		f := newUserGlobalFixture(t)
		input := filepath.Join(t.TempDir(), "in.jsonl")
		if err := os.WriteFile(input, []byte(`{"id":"gl-1","title":"probe from notes","status":"open","priority":2,"issue_type":"task"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		exit, stdout, stderr := runInUserGlobalHome(t, f.home, f.notes, nil, "import", input)
		if exit != 1 || stdout != "" || stderr != refusal(f) {
			t.Errorf("bd import: exit %d, stdout %q\nstderr: %q\nwant exit 1, no stdout, stderr %q", exit, stdout, stderr, refusal(f))
		}
		f.assertUntouched(t)
		if _, err := os.Stat(filepath.Join(f.notes, ".beads")); !os.IsNotExist(err) {
			t.Errorf("bd import left %s (stat: %v), want no workspace set up beside the refused one", filepath.Join(f.notes, ".beads"), err)
		}
	})

	t.Run("where", func(t *testing.T) {
		f := newUserGlobalFixture(t)
		exit, stdout, stderr := runInUserGlobalHome(t, f.home, f.notes, nil, "where")
		want := "Error: " + activeWorkspaceNotFoundMessage() + "\n" +
			"Hint: " + whereDiagHint() + "\n" + userGlobalBeadsNote(runtime.GOOS, f.beadsDir) + "\n"
		if exit != 1 || stdout != "" || stderr != want {
			t.Errorf("bd where: exit %d, stdout %q\nstderr: %q\nwant exit 1, no stdout, stderr %q", exit, stdout, stderr, want)
		}
	})

	t.Run("where --json", func(t *testing.T) {
		// The note joins the existing hint string: no new JSON keys.
		f := newUserGlobalFixture(t)
		exit, stdout, stderr := runInUserGlobalHome(t, f.home, f.notes, nil, "where", "--json")
		if exit != 1 || stderr != "" {
			t.Errorf("bd where --json: exit %d, stderr %q, want exit 1 and no stderr", exit, stderr)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("bd where --json: stdout is not JSON: %v\n%s", err, stdout)
		}
		want := map[string]any{
			"error":          "no_beads_directory",
			"hint":           whereDiagHint() + "\n" + userGlobalBeadsNote(runtime.GOOS, f.beadsDir),
			"message":        activeWorkspaceNotFoundMessage(),
			"schema_version": float64(JSONSchemaVersion),
		}
		if len(got) != len(want) {
			t.Errorf("bd where --json keys = %v, want exactly %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("bd where --json %q = %#v, want %#v", k, got[k], v)
			}
		}
	})
}

// TestResolveCommandBeadsDirStopsAtTheBeadsDirItNames pins the binding half of
// the bd import fix: the fallback's dbPath, notes/.beads/<db> before
// notes/.beads exists, maps to notes/.beads and not to the nearest .beads
// above it, here the user-global one. A config.yaml-only ~/.beads gets no
// refusal, so without this the import would create a database inside the
// user's configuration directory.
func TestResolveCommandBeadsDirStopsAtTheBeadsDirItNames(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configOnly bool
	}{
		{name: "user-level workspace above"},
		{name: "config.yaml only above", configOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUserGlobalFixture(t)
			if tc.configOnly {
				replaceUserGlobalDir(t, f, []string{"config.yaml"})
			}
			isolateBeadsDirForTest(t)
			t.Cleanup(beads.SetUserHomeDirForTesting(f.home))
			t.Chdir(f.notes)
			beads.ResetCaches()
			git.ResetCaches()
			t.Cleanup(func() {
				beads.ResetCaches()
				git.ResetCaches()
			})

			want := filepath.Join(f.notes, ".beads")
			if got := resolveCommandBeadsDir(filepath.Join(want, beads.CanonicalDatabaseName)); got != want {
				t.Errorf("resolveCommandBeadsDir(<notes>/.beads/%s) = %q, want %q", beads.CanonicalDatabaseName, got, want)
			}
		})
	}
}

// TestResolveCommandBeadsDirClimbEndsAtTheAccountHome pins the climb
// resolveCommandBeadsDir makes from a dbPath whose directory is not named
// .beads: it ends at the account home without binding the user-global
// .beads (GH#4402), unless the dbPath lies inside that directory or the home
// directory is a repository root, and it never binds a .beads above the home
// directory.
func TestResolveCommandBeadsDirClimbEndsAtTheAccountHome(t *testing.T) {
	enter := func(t *testing.T, f userGlobalFixture) {
		t.Helper()
		isolateBeadsDirForTest(t)
		t.Cleanup(beads.SetUserHomeDirForTesting(f.home))
		t.Chdir(f.notes)
		beads.ResetCaches()
		git.ResetCaches()
		t.Cleanup(func() {
			beads.ResetCaches()
			git.ResetCaches()
		})
	}

	t.Run("a new directory under home", func(t *testing.T) {
		f := newUserGlobalFixture(t)
		enter(t, f)
		want := filepath.Join(f.home, "store")
		if got := resolveCommandBeadsDir(filepath.Join(want, beads.CanonicalDatabaseName)); got != want {
			t.Errorf("resolveCommandBeadsDir(<home>/store/%s) = %q, want %q", beads.CanonicalDatabaseName, got, want)
		}
	})

	t.Run("a path inside the user-global directory", func(t *testing.T) {
		// Naming the user-level directory explicitly still selects it.
		f := newUserGlobalFixture(t)
		enter(t, f)
		dbPath := filepath.Join(f.beadsDir, "data", "beads")
		if got := resolveCommandBeadsDir(dbPath); got != f.beadsDir {
			t.Errorf("resolveCommandBeadsDir(%q) = %q, want %q", dbPath, got, f.beadsDir)
		}
	})

	t.Run("the user-global directory itself", func(t *testing.T) {
		// The directory form of --db or BEADS_DB (GH#2548) naming ~/.beads
		// selects it whatever it holds. Without a metadata.json nothing
		// validates it, and a climb from it would start at the home
		// directory and end there, binding the home directory.
		for _, layout := range []struct {
			name  string
			files []string // what ~/.beads holds; nil: it does not exist
		}{
			{name: "config.yaml only", files: []string{"config.yaml"}},
			{name: "a bare database file", files: []string{beads.CanonicalDatabaseName}},
			{name: "empty", files: []string{}},
			{name: "not created yet"},
		} {
			t.Run(layout.name, func(t *testing.T) {
				f := newUserGlobalFixture(t)
				replaceUserGlobalDir(t, f, layout.files)
				enter(t, f)
				if got := resolveCommandBeadsDir(f.beadsDir); got != f.beadsDir {
					t.Errorf("resolveCommandBeadsDir(%q) = %q, want that directory", f.beadsDir, got)
				}
			})
		}
	})

	t.Run("home is a repository root", func(t *testing.T) {
		// A dotfiles repository's .beads is its own, as discovery has it.
		f := newUserGlobalFixture(t)
		initGitRepoAt(t, f.home)
		enter(t, f)
		if got := resolveCommandBeadsDir(filepath.Join(f.home, "store", beads.CanonicalDatabaseName)); got != f.beadsDir {
			t.Errorf("resolveCommandBeadsDir(<home>/store/%s) = %q, want the home repository's %q", beads.CanonicalDatabaseName, got, f.beadsDir)
		}
	})

	t.Run("a .beads above home", func(t *testing.T) {
		f := newUserGlobalFixture(t)
		if err := os.RemoveAll(f.beadsDir); err != nil {
			t.Fatal(err)
		}
		newFixtureWorkspace(t, filepath.Join(filepath.Dir(f.home), ".beads"))
		enter(t, f)
		want := filepath.Join(f.home, "typo")
		if got := resolveCommandBeadsDir(filepath.Join(want, beads.CanonicalDatabaseName)); got != want {
			t.Errorf("resolveCommandBeadsDir(<home>/typo/%s) = %q, want %q", beads.CanonicalDatabaseName, got, want)
		}
	})
}

// TestExplicitTargetUnderHomeBindsTheNamedDir pins GH#4402 for explicit
// targets: --db, a file-form BEADS_DB and the BEADS_DIR bd import sets up,
// naming a directory under the home directory that does not exist yet and is
// not called .beads. resolveCommandBeadsDir climbed from it to the nearest
// .beads above, the user-global one, so the command opened and wrote the
// global database instead of its target. Each now opens its store in the
// directory it names, and <home>/.beads is left alone.
func TestExplicitTargetUnderHomeBindsTheNamedDir(t *testing.T) {
	isolateBeadsDirForTest(t)
	dbIn := func(dir string) string { return filepath.Join(dir, beads.CanonicalDatabaseName) }
	for _, tc := range []struct {
		name   string
		target string // relative to the home directory
		env    func(target string) map[string]string
		args   func(target string) []string
	}{
		{
			name:   "BEADS_DIR for bd import",
			target: "store",
			env:    func(target string) map[string]string { return map[string]string{"BEADS_DIR": target} },
			args:   func(string) []string { return []string{"import"} },
		},
		{
			// The import binds the directory it chose, not the nearest .beads
			// above it, here a project's own workspace.
			name:   "BEADS_DIR for bd import under a project workspace",
			target: filepath.Join("proj", "store"),
			env:    func(target string) map[string]string { return map[string]string{"BEADS_DIR": target} },
			args:   func(string) []string { return []string{"import"} },
		},
		{
			name:   "--db",
			target: "typo",
			args:   func(target string) []string { return []string{"--db", dbIn(target), "create", "probe"} },
		},
		{
			name:   "BEADS_DB",
			target: "typo",
			env:    func(target string) map[string]string { return map[string]string{"BEADS_DB": dbIn(target)} },
			args:   func(string) []string { return []string{"create", "probe"} },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUserGlobalFixture(t)
			target := filepath.Join(f.home, tc.target)
			projBeads := filepath.Join(f.home, "proj", ".beads")
			newFixtureWorkspace(t, projBeads)
			var env map[string]string
			if tc.env != nil {
				env = tc.env(target)
			}
			args := tc.args(target)
			if args[0] == "import" {
				args = append(args, writeUserGlobalImportInput(t))
			}
			_, _, stderr := runInUserGlobalHome(t, f.home, f.notes, env, args...)
			f.assertUntouched(t)
			assertFixtureWorkspaceUntouched(t, projBeads)
			if entries, err := os.ReadDir(target); err != nil || len(entries) == 0 {
				t.Errorf("%s holds %d entries (%v), want the store bd %s opened there\nstderr:\n%s", target, len(entries), err, strings.Join(args, " "), stderr)
			}
		})
	}
}

// TestNamingTheUserGlobalDirSelectsIt pins the directory form of --db and
// BEADS_DB (GH#2548) naming <home>/.beads itself when it holds no
// metadata.json to validate it, here only config.yaml: the command still
// selects that directory, so its store opens there and bd where reports it,
// and no workspace is set up in the home directory (GH#4402).
func TestNamingTheUserGlobalDirSelectsIt(t *testing.T) {
	isolateBeadsDirForTest(t)
	for _, tc := range []struct {
		name string
		env  func(beadsDir string) map[string]string
		args func(beadsDir string) []string
	}{
		{
			name: "--db",
			args: func(beadsDir string) []string { return []string{"--db", beadsDir, "create", "probe"} },
		},
		{
			name: "BEADS_DB",
			env:  func(beadsDir string) map[string]string { return map[string]string{"BEADS_DB": beadsDir} },
			args: func(string) []string { return []string{"create", "probe"} },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUserGlobalFixture(t)
			replaceUserGlobalDir(t, f, []string{"config.yaml"})
			var env map[string]string
			if tc.env != nil {
				env = tc.env(f.beadsDir)
			}
			args := tc.args(f.beadsDir)
			_, _, stderr := runInUserGlobalHome(t, f.home, f.notes, env, args...)
			for _, name := range []string{"embeddeddolt", "embeddeddolt.gate.lock"} {
				if _, err := os.Lstat(filepath.Join(f.home, name)); !os.IsNotExist(err) {
					t.Errorf("bd %s created %s in the home directory (stat: %v), want it in %s\nstderr:\n%s", strings.Join(args, " "), name, err, f.beadsDir, stderr)
				}
				if _, err := os.Lstat(filepath.Join(f.beadsDir, name)); err != nil {
					t.Errorf("bd %s: %s/%s: %v, want the store it opened there\nstderr:\n%s", strings.Join(args, " "), f.beadsDir, name, err, stderr)
				}
			}
		})
	}

	t.Run("where", func(t *testing.T) {
		f := newUserGlobalFixture(t)
		replaceUserGlobalDir(t, f, []string{"config.yaml"})
		exit, stdout, stderr := runInUserGlobalHome(t, f.home, f.notes, map[string]string{"BEADS_DB": f.beadsDir}, "where", "--json")
		var got WhereResult
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("bd where --json: exit %d, stdout is not JSON: %v\n%s\nstderr:\n%s", exit, err, stdout, stderr)
		}
		if exit != 0 || got.Path != f.beadsDir {
			t.Errorf("bd where --json: exit %d, path %q, want exit 0 and path %q\nstderr:\n%s", exit, got.Path, f.beadsDir, stderr)
		}
	})
}

// TestUserGlobalBeadsRefusalNamesOnlyARealSkip pins the refusals that must
// not name the user-level directory: discovery never passed over it, or
// passed over it inside a repository, whose own .beads is the remedy there.
func TestUserGlobalBeadsRefusalNamesOnlyARealSkip(t *testing.T) {
	isolateBeadsDirForTest(t)

	t.Run("git repository under home", func(t *testing.T) {
		// Selection stops at the repository root, below the home directory.
		f := newUserGlobalFixture(t)
		proj := filepath.Join(f.home, "proj")
		if err := os.MkdirAll(proj, 0o700); err != nil {
			t.Fatal(err)
		}
		initGitRepoAt(t, proj)
		exit, _, stderr := runInUserGlobalHome(t, f.home, proj, nil, "create", "a project issue")
		if exit != 1 || stderr != noDatabaseRefusal() {
			t.Errorf("bd create: exit %d\nstderr: %q\nwant exit 1, stderr %q", exit, stderr, noDatabaseRefusal())
		}
		f.assertUntouched(t)
	})

	t.Run("BEADS_DIR names a missing directory", func(t *testing.T) {
		// An explicit selection runs no walk: a mistyped one is not steered
		// into the global directory.
		f := newUserGlobalFixture(t)
		exit, _, stderr := runInUserGlobalHome(t, f.home, f.notes,
			map[string]string{"BEADS_DIR": filepath.Join(f.home, "work", ".beads")}, "list")
		if exit != 1 || stderr != noDatabaseRefusal() {
			t.Errorf("bd list: exit %d\nstderr: %q\nwant exit 1, stderr %q", exit, stderr, noDatabaseRefusal())
		}
		f.assertUntouched(t)
	})

	// The usual `git worktree add ../feature` layout: the main checkout lies
	// beside the worktree, not above it, so discovery meets the home
	// directory on its way there. Inside a repository that names nothing.
	t.Run("linked worktree under home", func(t *testing.T) {
		f := newUserGlobalFixture(t)
		worktree := filepath.Join(f.home, "feature")
		addUserGlobalLinkedWorktree(t, filepath.Join(f.home, "proj"), worktree)
		exit, _, stderr := runInUserGlobalHome(t, f.home, worktree, nil, "create", "a project issue")
		if exit != 1 || stderr != noDatabaseRefusal() {
			t.Errorf("bd create: exit %d\nstderr: %q\nwant exit 1, stderr %q", exit, stderr, noDatabaseRefusal())
		}
		f.assertUntouched(t)
	})

	t.Run("import in a linked worktree under home", func(t *testing.T) {
		// Not refused: bd import sets up the worktree's own .beads, as it
		// does in any repository with no workspace yet.
		f := newUserGlobalFixture(t)
		worktree := filepath.Join(f.home, "feature")
		addUserGlobalLinkedWorktree(t, filepath.Join(f.home, "proj"), worktree)
		_, _, stderr := runInUserGlobalHome(t, f.home, worktree, nil, "import", writeUserGlobalImportInput(t))
		if strings.Contains(stderr, "no beads database found") || strings.Contains(stderr, f.beadsDir) {
			t.Errorf("bd import refused or named %s in the worktree\nstderr:\n%s", f.beadsDir, stderr)
		}
		wtBeads := filepath.Join(worktree, ".beads")
		if entries, err := os.ReadDir(wtBeads); err != nil || len(entries) == 0 {
			t.Errorf("%s holds %d entries (%v), want the workspace bd import set up\nstderr:\n%s", wtBeads, len(entries), err, stderr)
		}
		f.assertUntouched(t)
	})
}

// TestUserGlobalBeadsNote pins the note's text: BEADS_DIR named per command,
// never exported, with the directory quoted for the shell the command is
// pasted into, and no paste-ready form on Windows, whose shells have none.
func TestUserGlobalBeadsNote(t *testing.T) {
	cases := []struct {
		name, goos, dir, want string
	}{
		{
			name: "nothing skipped", goos: "linux", dir: "", want: "",
		},
		{
			name: "plain path", goos: "linux", dir: "/home/u/.beads",
			want: "Note: /home/u/.beads is your user-level beads directory; bd uses it outside a project\n" +
				"      only when BEADS_DIR names it. Name it for each command that should use it:\n" +
				"      BEADS_DIR=/home/u/.beads bd <command>\n" +
				"      (an exported BEADS_DIR overrides every project's own .beads)",
		},
		{
			name: "path with a space", goos: "darwin", dir: "/Users/Jane Doe/.beads",
			want: "Note: /Users/Jane Doe/.beads is your user-level beads directory; bd uses it outside a project\n" +
				"      only when BEADS_DIR names it. Name it for each command that should use it:\n" +
				"      BEADS_DIR='/Users/Jane Doe/.beads' bd <command>\n" +
				"      (an exported BEADS_DIR overrides every project's own .beads)",
		},
		{
			name: "path no quoting passes", goos: "linux", dir: "/home/x\x1b[2K/.beads",
			want: "Note: \"/home/x\\x1b[2K/.beads\" is your user-level beads directory; bd uses it outside a project\n" +
				"      only when BEADS_DIR names it. Name it for each command that should use it:\n" +
				"      BEADS_DIR=<dir> bd <command>  (<dir>: \"/home/x\\x1b[2K/.beads\")\n" +
				"      (an exported BEADS_DIR overrides every project's own .beads)",
		},
		{
			name: "windows", goos: "windows", dir: `C:\Users\Jane Doe\.beads`,
			want: `Note: C:\Users\Jane Doe\.beads is your user-level beads directory; bd uses it outside a project` + "\n" +
				"      only when BEADS_DIR names it. Name it for each command that should use it.\n" +
				"      (an exported BEADS_DIR overrides every project's own .beads)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := userGlobalBeadsNote(tc.goos, tc.dir); got != tc.want {
				t.Errorf("userGlobalBeadsNote(%q, %q) =\n%s\nwant\n%s", tc.goos, tc.dir, got, tc.want)
			}
		})
	}
}
