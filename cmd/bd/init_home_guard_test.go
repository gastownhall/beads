package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsUserHomeDir covers the guard decision used to refuse `bd init` directly
// in the user's home directory (GH#4635). The resolver is stubbed so the test
// does not depend on (or touch) the real home directory.
func TestIsUserHomeDir(t *testing.T) {
	home := t.TempDir()
	stubHomeDir(t, home)

	if !isUserHomeDir(home) {
		t.Errorf("isUserHomeDir(home) = false, want true for %q", home)
	}
	if sub := filepath.Join(home, "project"); isUserHomeDir(sub) {
		t.Errorf("isUserHomeDir(%q) = true, want false (subdir of home)", sub)
	}
	if parent := filepath.Dir(home); isUserHomeDir(parent) {
		t.Errorf("isUserHomeDir(%q) = true, want false (parent of home)", parent)
	}
}

// TestIsUserHomeDir_UnresolvableHome verifies the guard never fires when the
// home directory cannot be resolved (fail-open: better to allow init than to
// wrongly refuse every directory).
func TestIsUserHomeDir_UnresolvableHome(t *testing.T) {
	stubHomeDir(t, "")

	if isUserHomeDir("/some/dir") {
		t.Error("isUserHomeDir should be false when home is unresolvable")
	}
}

// TestGuardInitInHomeDir covers the conditions under which the guard declines
// to fire: an explicit BEADS_DIR, an already-tracked home, and any directory
// that simply is not home.
func TestGuardInitInHomeDir(t *testing.T) {
	t.Run("refuses_in_untracked_home", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		chdir(t, home)
		stubCallerBeadsDir(t, "")

		err := guardInitInHomeDir()
		if err == nil {
			t.Fatal("guardInitInHomeDir() = nil, want a refusal in an untracked home")
		}
		assertHomeDirRefusal(t, err)
	})

	t.Run("allows_when_the_caller_supplied_beads_dir", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		chdir(t, home)
		stubCallerBeadsDir(t, filepath.Join(home, "elsewhere", ".beads"))

		if err := guardInitInHomeDir(); err != nil {
			t.Errorf("guardInitInHomeDir() = %v, want nil when the caller named a BEADS_DIR", err)
		}
	})

	// The bypass this guard shipped with: BEADS_DIR in the live environment is
	// not evidence the caller asked for anything. bd exports one itself for
	// every no-DB command, and beads.FindBeadsDir() accepts an ancestor
	// .beads/ holding nothing but a config.yaml — which is where this
	// project's own legacy user-level config lives, at ~/.beads/config.yaml.
	// So the users most likely to have scaffolding in $HOME worth protecting
	// were exactly the ones the guard failed open for.
	t.Run("refuses_when_bd_exported_beads_dir_for_itself", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		chdir(t, home)
		stubCallerBeadsDir(t, "")
		// What the root pre-run does, and what `bd -C <dir>` does: set the
		// live env without the caller having named anything.
		t.Setenv("BEADS_DIR", filepath.Join(home, ".beads"))

		err := guardInitInHomeDir()
		if err == nil {
			t.Fatal("guardInitInHomeDir() = nil; a bd-exported BEADS_DIR is not caller opt-in")
		}
		assertHomeDirRefusal(t, err)
	})

	t.Run("allows_when_home_is_already_a_git_repo", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		chdir(t, home)
		stubCallerBeadsDir(t, "")
		gitInitAt(t, home)

		if err := guardInitInHomeDir(); err != nil {
			t.Errorf("guardInitInHomeDir() = %v, want nil when home is already tracked", err)
		}
	})

	t.Run("allows_in_an_ordinary_project_dir", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		project := filepath.Join(home, "project")
		if err := os.MkdirAll(project, 0o750); err != nil {
			t.Fatal(err)
		}
		chdir(t, project)
		stubCallerBeadsDir(t, "")

		if err := guardInitInHomeDir(); err != nil {
			t.Errorf("guardInitInHomeDir() = %v, want nil for a project dir under home", err)
		}
	})
}

// TestInitCommand_RefusesInHomeDirBeforeAnySideEffect runs the actual init
// command, which is where the guard's *position* is under test rather than its
// decision. Both flag shapes below reach a side effect before the guard's
// original location did:
//
//   - --reinit-local / --force run countExistingIssues, which opens a real
//     store (creating the data directory, and able to run migrations);
//   - --proxied-server dispatches to runInitProxiedServer, which calls
//     EnsureGitRepo — a `git init` in the current directory.
//
// So the assertion is not merely "it errors": nothing may exist afterwards.
func TestInitCommand_RefusesInHomeDirBeforeAnySideEffect(t *testing.T) {
	cases := []struct {
		name  string
		flags map[string]string
	}{
		{name: "plain", flags: nil},
		{name: "reinit_local", flags: map[string]string{"reinit-local": "true"}},
		{name: "force", flags: map[string]string{"force": "true"}},
		{name: "proxied_server", flags: map[string]string{"proxied-server": "true"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			stubHomeDir(t, home)
			chdir(t, home)
			stubCallerBeadsDir(t, "")
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BD_NON_INTERACTIVE", "1")
			for name, value := range tc.flags {
				setInitFlag(t, name, value)
			}

			err := initCmd.RunE(initCmd, nil)
			if err == nil {
				t.Fatal("bd init in an untracked home returned nil, want a refusal")
			}
			assertHomeDirRefusal(t, err)

			// A refusal that still left something behind is not a refusal.
			for _, name := range []string{".git", ".beads", "CLAUDE.md", "AGENTS.md", ".gitignore", ".agents", ".codex", ".claude"} {
				if _, statErr := os.Stat(filepath.Join(home, name)); statErr == nil {
					t.Errorf("init created %s in the home directory despite refusing", name)
				}
			}
		})
	}
}

// assertHomeDirRefusal checks that err is the home-directory refusal, by its
// exit code rather than its message: the code is the scriptable contract
// (docs/recovery/init-safety.md#init-home-refused), the wording is not.
func assertHomeDirRefusal(t *testing.T, err error) {
	t.Helper()
	code, ok := exitCodeFromError(err)
	if !ok {
		t.Fatalf("refusal did not carry an exit code: %v", err)
	}
	if code != ExitHomeDirRefused {
		t.Fatalf("exit code = %d, want %d (ExitHomeDirRefused)", code, ExitHomeDirRefused)
	}
}

// stubCallerBeadsDir sets what the guard sees as the caller-supplied BEADS_DIR.
// The real value is captured at package init from the inherited environment, so
// a test cannot move it with t.Setenv — which is the whole point of the
// snapshot, and why it has to be stubbed here instead.
func stubCallerBeadsDir(t *testing.T, dir string) {
	t.Helper()
	orig := beadsDirFromCaller
	beadsDirFromCaller = dir
	t.Cleanup(func() { beadsDirFromCaller = orig })
}

// stubHomeDir points the guard's home resolver at dir for the duration of the
// test. Stubbing rather than setting $HOME is deliberate: the resolver prefers
// the OS account database, so $HOME alone would not move it.
func stubHomeDir(t *testing.T, dir string) {
	t.Helper()
	orig := resolveUserHomeDir
	resolveUserHomeDir = func() string { return dir }
	t.Cleanup(func() { resolveUserHomeDir = orig })
}

// gitInitAt makes dir a git repository.
func gitInitAt(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}

// chdir switches to dir for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// setInitFlag sets a flag on the shared initCmd and restores it afterwards —
// cobra commands are package-level singletons, so a leaked flag would bleed
// into every later test in this package.
func setInitFlag(t *testing.T, name, value string) {
	t.Helper()
	flag := initCmd.Flags().Lookup(name)
	if flag == nil {
		t.Fatalf("init has no --%s flag", name)
	}
	orig := flag.Value.String()
	origChanged := flag.Changed
	if err := initCmd.Flags().Set(name, value); err != nil {
		t.Fatalf("setting --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() {
		_ = initCmd.Flags().Set(name, orig)
		flag.Changed = origChanged
	})
}

// TestInitThroughRootCmd_RefusesInHomeDirWithDiscoveredBeadsDir is the
// regression for the bypass, and it has to run through rootCmd rather than
// calling initCmd.RunE directly — the whole failure lives in a hook RunE-only
// tests skip.
//
// Cobra's root PersistentPreRunE runs first, and for a no-DB command like init
// it resolves a beads dir and exports it (main.go, selectedNoDBBeadsDir ->
// prepareSelectedNoDBContext). That resolution accepts an ancestor .beads/
// holding nothing but a config.yaml, which is precisely the shape of this
// project's legacy user-level ~/.beads/config.yaml. A guard reading the LIVE
// BEADS_DIR therefore saw a value it had set for itself and stood down.
//
// The half that made this worth blocking on: the `git init` is separately
// gated on the same env, so it stayed skipped — but the agent scaffolding is
// not. CLAUDE.md, AGENTS.md and .claude/ are written relative to the working
// directory with no BEADS_DIR gate at all, so the bypass still clobbered the
// files in $HOME the guard exists to protect.
//
// --backend names a backend that cannot exist, as a circuit breaker: if the
// guard ever fails open again, init stops at the unknown-backend error instead
// of building a real store in someone's home directory, and the assertion
// below reports that error rather than hanging on Dolt.
func TestInitThroughRootCmd_RefusesInHomeDirWithDiscoveredBeadsDir(t *testing.T) {
	home := t.TempDir()
	stubHomeDir(t, home)
	chdir(t, home)
	stubCallerBeadsDir(t, "")

	// The caller named nothing. Anything in BEADS_DIR from here on was put
	// there by bd itself.
	t.Setenv("BEADS_DIR", "")
	t.Setenv("HOME", home)
	t.Setenv("BD_NON_INTERACTIVE", "1")

	// The legacy user-level config, which is all beads.FindBeadsDir needs.
	beadsDir := filepath.Join(home, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("actor: someone\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Confirm the precondition rather than assuming it: if discovery stops
	// resolving this, the test would pass for the wrong reason. Compared
	// through EvalSymlinks because macOS reports the same temp dir as both
	// /var/... and /private/var/....
	if got, want := evalSymlinks(t, selectedNoDBBeadsDir(initCmd)), evalSymlinks(t, beadsDir); got != want {
		t.Fatalf("precondition: selectedNoDBBeadsDir = %q, want %q — pre-run no longer discovers a config.yaml-only .beads, so this test no longer reproduces the bypass", got, want)
	}

	origStore, origDBPath := store, dbPath
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		store, dbPath = origStore, origDBPath
		_ = initCmd.Flags().Set("backend", "")
		_ = initCmd.Flags().Set("prefix", "")
	})
	store, dbPath = nil, ""

	rootCmd.SetArgs([]string{"init", "--backend", "review4795-home-guard-sentinel", "--prefix", "hg"})
	err := rootCmd.Execute()

	if err == nil {
		t.Fatal("bd init in an untracked home returned nil, want a refusal")
	}
	if strings.Contains(err.Error(), "review4795-home-guard-sentinel") {
		t.Fatalf("init ran past the home guard and got as far as backend selection: %v", err)
	}
	assertHomeDirRefusal(t, err)

	// The files the bypass could still clobber, none of which is gated on
	// BEADS_DIR: scaffolding is written relative to the working directory.
	for _, name := range []string{".git", "CLAUDE.md", "AGENTS.md", ".gitignore", ".agents", ".codex", ".claude"} {
		if _, statErr := os.Stat(filepath.Join(home, name)); statErr == nil {
			t.Errorf("init created %s in the home directory despite refusing", name)
		}
	}
}

// evalSymlinks resolves path for comparison, tolerating a path that does not
// exist (nothing to resolve is not a test failure here).
func evalSymlinks(t *testing.T, path string) string {
	t.Helper()
	if path == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}
