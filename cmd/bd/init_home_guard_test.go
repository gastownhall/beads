package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
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

// TestGuardInitInHomeDir covers the guard's three answers: refuse (an
// untracked home, no BEADS_DIR from the caller), proceed but keep the home
// clean (the same home, with one), and stand aside (a home that is itself a
// git repository, or any directory that simply is not home).
func TestGuardInitInHomeDir(t *testing.T) {
	t.Run("refuses_in_untracked_home", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		t.Chdir(home)
		stubCallerBeadsDir(t, "")

		_, err := guardInitInHomeDir()
		if err == nil {
			t.Fatal("guardInitInHomeDir() = nil, want a refusal in an untracked home")
		}
		assertHomeDirRefusal(t, err)
	})

	// The opt-out the refusal offers is a different init, not this one with
	// the guard lifted: it may proceed, but it must not set the home up as a
	// project (TestInitInUntrackedHomeWithCallerBeadsDir_SetsUpOnlyTheWorkspace
	// runs it).
	t.Run("caller_beads_dir_proceeds_but_keeps_home_clean", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		t.Chdir(home)
		stubCallerBeadsDir(t, filepath.Join(home, "elsewhere", ".beads"))

		keepHomeClean, err := guardInitInHomeDir()
		if err != nil {
			t.Fatalf("guardInitInHomeDir() = %v, want nil when the caller named a BEADS_DIR", err)
		}
		if !keepHomeClean {
			t.Error("keepHomeClean = false, want true: init would install agent files into the untracked home")
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
		t.Chdir(home)
		stubCallerBeadsDir(t, "")
		// What the root pre-run does, and what `bd -C <dir>` does: set the
		// live env without the caller having named anything.
		t.Setenv("BEADS_DIR", filepath.Join(home, ".beads"))

		_, err := guardInitInHomeDir()
		if err == nil {
			t.Fatal("guardInitInHomeDir() = nil; a bd-exported BEADS_DIR is not caller opt-in")
		}
		assertHomeDirRefusal(t, err)
	})

	// "Is this home tracked?" has to be asked the way init's own `git init`
	// decision asks it, with git's routing environment scrubbed. A plain probe
	// believes an inherited GIT_DIR, while init does not: it would find the
	// home untracked, `git init` it and install agent files there.
	t.Run("refuses_when_git_dir_is_inherited", func(t *testing.T) {
		other := t.TempDir()
		gitInitAt(t, other)
		home := t.TempDir()
		stubHomeDir(t, home)
		t.Chdir(home)
		stubCallerBeadsDir(t, "")
		t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
		if !isGitRepo() {
			t.Fatal("precondition: isGitRepo() = false under an inherited GIT_DIR, so this no longer reproduces the plain probe's false positive")
		}

		_, err := guardInitInHomeDir()
		if err == nil {
			t.Fatal("guardInitInHomeDir() = nil; an inherited GIT_DIR does not make the home a repository")
		}
		assertHomeDirRefusal(t, err)
	})

	// Only a repository rooted at the home itself means someone set the home
	// up on purpose. A home that merely sits inside an enclosing work tree
	// still gets init's agent files, so it is refused like any untracked one,
	// and a caller BEADS_DIR there is the same opt-out: keepHomeClean, which
	// also keeps init out of the enclosing repository
	// (TestInitInUntrackedHomeWithCallerBeadsDir_SetsUpOnlyTheWorkspace).
	t.Run("refuses_when_home_only_sits_inside_an_enclosing_repo", func(t *testing.T) {
		root := t.TempDir()
		gitInitAt(t, root)
		home := filepath.Join(root, "home")
		if err := os.Mkdir(home, 0o750); err != nil {
			t.Fatal(err)
		}
		stubHomeDir(t, home)
		t.Chdir(home)
		stubCallerBeadsDir(t, "")
		if !isGitRepo() {
			t.Fatal("precondition: isGitRepo() = false inside the enclosing repository, so this no longer reproduces the plain probe's false positive")
		}

		_, err := guardInitInHomeDir()
		if err == nil {
			t.Fatal("guardInitInHomeDir() = nil; an enclosing repository does not make the home itself tracked")
		}
		assertHomeDirRefusal(t, err)

		stubCallerBeadsDir(t, filepath.Join(t.TempDir(), ".beads"))
		if keepHomeClean, err := guardInitInHomeDir(); err != nil || !keepHomeClean {
			t.Errorf("with a caller BEADS_DIR: guardInitInHomeDir() = (%v, %v), want (true, nil)", keepHomeClean, err)
		}
	})

	// A home that is itself a repository was set up as a project on purpose,
	// so init runs there exactly as anywhere else, BEADS_DIR or not.
	t.Run("stands_aside_when_home_is_itself_a_git_repo", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		t.Chdir(home)
		gitInitAt(t, home)

		for _, callerBeadsDir := range []string{"", filepath.Join(home, "notes", ".beads")} {
			stubCallerBeadsDir(t, callerBeadsDir)
			if keepHomeClean, err := guardInitInHomeDir(); err != nil || keepHomeClean {
				t.Errorf("caller BEADS_DIR %q: guardInitInHomeDir() = (%v, %v), want (false, nil) in a tracked home", callerBeadsDir, keepHomeClean, err)
			}
		}
	})

	t.Run("stands_aside_in_an_ordinary_project_dir", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		project := filepath.Join(home, "project")
		if err := os.MkdirAll(project, 0o750); err != nil {
			t.Fatal(err)
		}
		t.Chdir(project)

		for _, callerBeadsDir := range []string{"", filepath.Join(home, "notes", ".beads")} {
			stubCallerBeadsDir(t, callerBeadsDir)
			if keepHomeClean, err := guardInitInHomeDir(); err != nil || keepHomeClean {
				t.Errorf("caller BEADS_DIR %q: guardInitInHomeDir() = (%v, %v), want (false, nil) for a project dir under home", callerBeadsDir, keepHomeClean, err)
			}
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
// So the assertion is not merely "it errors": the home must still be empty.
//
// Every case also names a backend that cannot exist, as a circuit breaker: if
// the guard ever fails open, init stops at the unknown-backend check, ahead of
// every effect, and the failure below names the guard instead of a store being
// built in the temp home.
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
			t.Chdir(home)
			stubCallerBeadsDir(t, "")
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BD_NON_INTERACTIVE", "1")
			setInitFlag(t, "backend", homeGuardSentinelBackend)
			for name, value := range tc.flags {
				setInitFlag(t, name, value)
			}

			err := initCmd.RunE(initCmd, nil)
			if err == nil {
				t.Fatal("bd init in an untracked home returned nil, want a refusal")
			}
			if strings.Contains(err.Error(), homeGuardSentinelBackend) {
				t.Fatalf("init ran past the home guard and got as far as backend selection: %v", err)
			}
			assertHomeDirRefusal(t, err)

			// A refusal that still left something behind is not a refusal.
			entries, readErr := os.ReadDir(home)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, entry := range entries {
				t.Errorf("init created %s in the home directory despite refusing", entry.Name())
			}
		})
	}
}

// homeGuardSentinelBackend is a --backend no bd can have: the home-guard
// tests' circuit breaker. Init rejects it before any effect, so a guard that
// fails open ends at that error, which names it, instead of at a real store.
const homeGuardSentinelBackend = "review4795-home-guard-sentinel"

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

// gitInitAt makes dir a git repository, with git's routing environment
// scrubbed so an inherited GIT_DIR cannot send the init somewhere else.
func gitInitAt(t *testing.T, dir string) {
	t.Helper()
	cmd := initArtifactGitCommand(dir, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
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
// prepareSelectedNoDBContext). That resolution accepted an ancestor .beads/
// holding nothing but a config.yaml, which is precisely the shape of this
// project's legacy user-level ~/.beads/config.yaml. A guard reading the LIVE
// BEADS_DIR therefore saw a value it had set for itself and stood down.
//
// Discovery now passes over the account home's own .beads (GH#4402;
// TestInitThroughRootCmd_HomeDirDiscoveryExportsNoUserGlobalBeadsDir), so
// only the guard's home is stubbed here: discovery's stays the real account
// home, and to it the fixture's config.yaml-only .beads is an ordinary
// ancestor workspace. That keeps the bypass reproducible, so the guard's own
// defense, reading the caller's BEADS_DIR rather than the live one, stays
// pinned even if some discovery route adopts ~/.beads again.
//
// The half that made this worth blocking on: the `git init` is separately
// gated on the same env, so it stayed skipped — but the agent scaffolding is
// not. CLAUDE.md, AGENTS.md, .claude/, .codex/, .cursor/ and .agents/ are
// written relative to the working directory whatever BEADS_DIR says, so the
// bypass still installed into $HOME the user-wide agent settings the guard
// exists to keep out of it.
//
// --backend names a backend that cannot exist (homeGuardSentinelBackend), as
// a circuit breaker: if the guard ever fails open again, init stops at the
// unknown-backend error instead of building a real store in someone's home
// directory, and the assertion below reports that error rather than hanging
// on Dolt.
func TestInitThroughRootCmd_RefusesInHomeDirWithDiscoveredBeadsDir(t *testing.T) {
	home := t.TempDir()
	stubHomeDir(t, home)
	t.Chdir(home)
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

	rootCmd.SetArgs([]string{"init", "--backend", homeGuardSentinelBackend, "--prefix", "hg"})
	err := rootCmd.Execute()

	if err == nil {
		t.Fatal("bd init in an untracked home returned nil, want a refusal")
	}
	if strings.Contains(err.Error(), homeGuardSentinelBackend) {
		t.Fatalf("init ran past the home guard and got as far as backend selection: %v", err)
	}
	assertHomeDirRefusal(t, err)

	// The files the bypass could still install, none of which is gated on
	// BEADS_DIR: scaffolding is written relative to the working directory.
	for _, name := range []string{".git", "CLAUDE.md", "AGENTS.md", ".gitignore", ".agents", ".codex", ".claude", ".cursor"} {
		if _, statErr := os.Stat(filepath.Join(home, name)); statErr == nil {
			t.Errorf("init created %s in the home directory despite refusing", name)
		}
	}
}

// TestInitThroughRootCmd_HomeDirDiscoveryExportsNoUserGlobalBeadsDir is the
// bypass above with discovery's home pointed at the fixture too, as it is in
// production, where both resolve the account home: PreRun discovery passes
// over the home directory's own .beads (GH#4402), so it exports no BEADS_DIR
// for the guard to mistake for the caller's, and init refuses.
func TestInitThroughRootCmd_HomeDirDiscoveryExportsNoUserGlobalBeadsDir(t *testing.T) {
	home := t.TempDir()
	stubHomeDir(t, home)
	t.Cleanup(beads.SetUserHomeDirForTesting(home))
	t.Chdir(home)
	stubCallerBeadsDir(t, "")
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	t.Setenv("BEADS_DIR", "")
	t.Setenv("HOME", home)
	t.Setenv("BD_NON_INTERACTIVE", "1")

	beadsDir := filepath.Join(home, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("actor: someone\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := selectedNoDBBeadsDir(initCmd); got != "" {
		t.Fatalf("selectedNoDBBeadsDir = %q, want \"\": discovery from the home directory must pass over %s", got, beadsDir)
	}

	origStore, origDBPath := store, dbPath
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		store, dbPath = origStore, origDBPath
		_ = initCmd.Flags().Set("backend", "")
		_ = initCmd.Flags().Set("prefix", "")
	})
	store, dbPath = nil, ""

	rootCmd.SetArgs([]string{"init", "--backend", homeGuardSentinelBackend, "--prefix", "hg"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("bd init in an untracked home returned nil, want a refusal")
	}
	if strings.Contains(err.Error(), homeGuardSentinelBackend) {
		t.Fatalf("init ran past the home guard and got as far as backend selection: %v", err)
	}
	assertHomeDirRefusal(t, err)
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
