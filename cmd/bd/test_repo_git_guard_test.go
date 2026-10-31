package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests drive the TestMain git-state guard in
// test_repo_beads_guard_test.go against throwaway clones, never this checkout.
// Most fixtures are a main checkout plus one linked worktree, the layout the
// r880 clobber broke (wy-qlvlmf): the guard reads the clone's shared common
// dir through the linked worktree, as it does when the suite runs from one.
// A clone with no linked worktree, such as a CI checkout, gets the stricter
// check.

// guardFixtureHost appears in every remote URL the fixtures write. No guard
// report may contain it: configs carry remote URLs and can carry credentials.
const guardFixtureHost = "guard-fixture.invalid"

// newGitGuardClone returns a checkout with an origin remote and an origin/main
// tracking ref, and no linked worktree.
func newGitGuardClone(t *testing.T) string {
	t.Helper()
	mainDir := filepath.Join(t.TempDir(), "main")
	if err := os.Mkdir(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, mainDir)
	runGitForBootstrapTest(t, mainDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, mainDir, "remote", "add", "origin", "https://"+guardFixtureHost+"/upstream.git")
	runGitForBootstrapTest(t, mainDir, "update-ref", "refs/remotes/origin/main", "HEAD")
	return mainDir
}

// newGitGuardFixture returns a newGitGuardClone checkout and a detached linked
// worktree of it.
func newGitGuardFixture(t *testing.T) (mainDir, linkedDir string) {
	t.Helper()
	mainDir = newGitGuardClone(t)
	linkedDir = filepath.Join(filepath.Dir(mainDir), "linked")
	runGitForBootstrapTest(t, mainDir, "worktree", "add", "--detach", linkedDir)
	return mainDir, linkedDir
}

func locateGitGuardFixture(t *testing.T, dir string) repoGitDirs {
	t.Helper()
	dirs, err := locateRepoGitDirs(dir)
	if err != nil {
		t.Fatalf("locateRepoGitDirs(%s): %v", dir, err)
	}
	if dirs.common == "" {
		t.Fatalf("locateRepoGitDirs(%s) found no common git dir", dir)
	}
	return dirs
}

func evalRealPath(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return real
}

func TestLocateRepoGitDirs(t *testing.T) {
	mainDir, linkedDir := newGitGuardFixture(t)
	wantCommon := evalRealPath(t, filepath.Join(mainDir, ".git"))

	t.Run("main checkout", func(t *testing.T) {
		dirs := locateGitGuardFixture(t, mainDir)
		if got := evalRealPath(t, dirs.common); got != wantCommon {
			t.Errorf("common = %s, want %s", got, wantCommon)
		}
		if got := evalRealPath(t, dirs.gitDir); got != wantCommon {
			t.Errorf("gitDir = %s, want the common dir %s", got, wantCommon)
		}
		// A main checkout has a config.worktree too, beside the shared config.
		if got, want := dirs.worktreeConfig(), filepath.Join(dirs.gitDir, "config.worktree"); got != want {
			t.Errorf("worktreeConfig() = %q, want %q", got, want)
		}
		if !dirs.shared {
			t.Error("shared = false for a main checkout with a linked worktree registered")
		}
	})

	t.Run("linked worktree", func(t *testing.T) {
		dirs := locateGitGuardFixture(t, linkedDir)
		if got := evalRealPath(t, dirs.common); got != wantCommon {
			t.Errorf("common = %s, want %s", got, wantCommon)
		}
		if got, want := evalRealPath(t, dirs.gitDir), filepath.Join(wantCommon, "worktrees", "linked"); got != want {
			t.Errorf("gitDir = %s, want %s", got, want)
		}
		if got, want := dirs.worktreeConfig(), filepath.Join(dirs.gitDir, "config.worktree"); got != want {
			t.Errorf("worktreeConfig() = %q, want %q", got, want)
		}
		if !dirs.shared {
			t.Error("shared = false for a linked worktree")
		}
	})

	t.Run("clone with no linked worktree", func(t *testing.T) {
		dirs := locateGitGuardFixture(t, newGitGuardClone(t))
		if dirs.shared {
			t.Error("shared = true for a clone no other checkout uses; the guard would let a test's identity or role write through")
		}
	})

	t.Run("not a checkout", func(t *testing.T) {
		dir := t.TempDir()
		// Keep git from walking up to a repository above the temp dir.
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		dirs, err := locateRepoGitDirs(dir)
		if err != nil || dirs != (repoGitDirs{}) {
			t.Fatalf("got (%+v, %v); a tree with no .git (Bazel runfiles, a tarball) has nothing to guard and is not an error", dirs, err)
		}
	})

	t.Run("broken checkout", func(t *testing.T) {
		dir := t.TempDir()
		gitFile := "gitdir: " + filepath.Join(dir, "missing") + "\n"
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(gitFile), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := locateRepoGitDirs(dir); err == nil {
			t.Fatal("no error for a .git that points nowhere; the guard would drop its git check without a warning")
		}
	})
}

// TestDiffRepoGitStateClassifiesGitWrites runs the git commands that other
// worktrees of a shared clone run routinely, and the writes the r880 clobber
// made, and checks which ones the guard lets through, in a shared clone and
// in one no other checkout uses.
func TestDiffRepoGitStateClassifiesGitWrites(t *testing.T) {
	type fixture struct {
		mainDir, linkedDir string
		dirs               repoGitDirs
	}
	git := func(dir func(fixture) string, args ...string) func(*testing.T, fixture) {
		return func(t *testing.T, f fixture) {
			t.Helper()
			runGitForBootstrapTest(t, dir(f), args...)
		}
	}
	inMain := func(f fixture) string { return f.mainDir }
	inLinked := func(f fixture) string { return f.linkedDir }
	writeHook := func(name, content string) func(*testing.T, fixture) {
		return func(t *testing.T, f fixture) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(f.dirs.common, "hooks", name), []byte(content), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	both := func(steps ...func(*testing.T, fixture)) func(*testing.T, fixture) {
		return func(t *testing.T, f fixture) {
			t.Helper()
			for _, step := range steps {
				step(t, f)
			}
		}
	}
	forkURL := "https://" + guardFixtureHost + "/fork.git"

	tests := []struct {
		name      string
		guardMain bool                      // guard the main checkout instead of the linked worktree
		soleClone bool                      // a clone with no linked worktree; guard its main checkout
		setup     func(*testing.T, fixture) // before the first snapshot
		act       func(*testing.T, fixture) // between the snapshots
		routine   bool
		want      []string // fragments of the one change's detail; nil means no change at all
	}{
		{
			name: "read-only git commands",
			act:  both(git(inMain, "rev-parse", "HEAD"), git(inLinked, "config", "--get", "user.email")),
		},
		{
			name:    "branch tracking set up by another worktree",
			act:     git(inMain, "branch", "--track", "feature", "origin/main"),
			routine: true,
			want:    []string{"branch.feature.remote added", "branch.feature.merge added"},
		},
		{
			name:    "tracked branch with a dotted name deleted",
			setup:   git(inMain, "branch", "--track", "release/v1.2", "origin/main"),
			act:     git(inMain, "branch", "-D", "release/v1.2"),
			routine: true,
			want:    []string{"branch.release/v1.2.remote removed", "branch.release/v1.2.merge removed"},
		},
		{
			name:    "remote added",
			act:     git(inMain, "remote", "add", "fork", forkURL),
			routine: true,
			want:    []string{"remote.fork.url added", "remote.fork.fetch added"},
		},
		{
			name:    "remote removed",
			setup:   git(inMain, "remote", "add", "fork", forkURL),
			act:     git(inMain, "remote", "remove", "fork"),
			routine: true,
			want:    []string{"remote.fork.url removed", "remote.fork.fetch removed"},
		},
		{
			name: "existing remote repointed",
			act:  git(inLinked, "remote", "set-url", "origin", "https://"+guardFixtureHost+"/test-fixture.git"),
			want: []string{"remote.origin.url changed"},
		},
		{
			// Unlike other remotes, the clone's origin is not added or
			// removed whole by its worktrees.
			name: "origin removed",
			act:  git(inLinked, "remote", "remove", "origin"),
			want: []string{"remote.origin.url removed", "remote.origin.fetch removed"},
		},
		{
			name:  "origin added",
			setup: git(inMain, "remote", "remove", "origin"),
			act:   git(inLinked, "remote", "add", "origin", "https://"+guardFixtureHost+"/test-fixture.git"),
			want:  []string{"remote.origin.url added", "remote.origin.fetch added"},
		},
		{
			name: "core.bare flipped",
			act:  git(inLinked, "config", "core.bare", "true"),
			want: []string{"core.bare changed"},
		},
		{
			name: "worktree config extension enabled",
			act:  git(inLinked, "config", "extensions.worktreeConfig", "true"),
			want: []string{"extensions.worktreeconfig added"},
		},
		{
			name: "worktree path set",
			act:  git(inLinked, "config", "core.worktree", "elsewhere"),
			want: []string{"core.worktree added"},
		},
		{
			// Other worktrees of a shared clone set these while the suite
			// runs; the guard cannot tell their writes from a test's.
			name:    "identity rewritten by another worktree",
			act:     git(inMain, "config", "user.email", "fixture@"+guardFixtureHost),
			routine: true,
			want:    []string{"user.email changed"},
		},
		{
			name:    "bd role set by another worktree",
			act:     git(inMain, "config", "beads.role", "maintainer"),
			routine: true,
			want:    []string{"beads.role added"},
		},
		{
			// With no other checkout to blame, a top-level key change
			// is a test running git against this one.
			name:      "identity rewritten in a clone no other checkout uses",
			soleClone: true,
			act:       git(inMain, "config", "user.email", "fixture@"+guardFixtureHost),
			want:      []string{"user.email changed"},
		},
		{
			name:      "bd role set in a clone no other checkout uses",
			soleClone: true,
			act:       git(inMain, "config", "beads.role", "maintainer"),
			want:      []string{"beads.role added"},
		},
		{
			name:      "branch tracking set up in a clone no other checkout uses",
			soleClone: true,
			act:       git(inMain, "branch", "--track", "feature", "origin/main"),
			routine:   true,
			want:      []string{"branch.feature.remote added", "branch.feature.merge added"},
		},
		{
			name:      "origin removed in a clone no other checkout uses",
			soleClone: true,
			act:       git(inMain, "remote", "remove", "origin"),
			want:      []string{"remote.origin.url removed", "remote.origin.fetch removed"},
		},
		{
			name: "hooks path rewritten",
			act:  git(inLinked, "config", "core.hooksPath", "/dev/null"),
			want: []string{"core.hookspath changed"},
		},
		{
			name: "clobber next to routine branch tracking",
			act: both(
				git(inMain, "branch", "--track", "feature", "origin/main"),
				git(inLinked, "config", "core.bare", "true"),
			),
			want: []string{"core.bare changed", "also 2 routine key change(s)"},
		},
		{
			name: "clobber next to another worktree's identity change",
			act: both(
				git(inMain, "config", "user.email", "fixture@"+guardFixtureHost),
				git(inLinked, "config", "extensions.worktreeConfig", "true"),
			),
			want: []string{"extensions.worktreeconfig added", "also 1 routine key change(s)"},
		},
		{
			name: "hook written",
			act:  writeHook("pre-commit", "#!/bin/sh\nexit 0\n"),
			want: []string{"new file (17 bytes)"},
		},
		{
			name:  "hook rewritten",
			setup: writeHook("pre-push", "#!/bin/sh\n"),
			act:   writeHook("pre-push", "#!/bin/sh\nexit 1\n"),
			want:  []string{"changed (10 → 17 bytes)"},
		},
		{
			name:  "hook deleted",
			setup: writeHook("pre-push", "#!/bin/sh\n"),
			act: func(t *testing.T, f fixture) {
				if err := os.Remove(filepath.Join(f.dirs.common, "hooks", "pre-push")); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"deleted (was 10 bytes)"},
		},
		{
			// Only the common config gets the shared-clone allowance: nothing
			// but this worktree writes its config.worktree.
			name: "branch key written to config.worktree",
			setup: both(
				git(inMain, "config", "extensions.worktreeConfig", "true"),
				git(inLinked, "config", "--worktree", "core.sparseCheckout", "false"),
			),
			act:  git(inLinked, "config", "--worktree", "branch.feature.remote", "origin"),
			want: []string{"branch.feature.remote added"},
		},
		{
			// A main checkout's config.worktree lives in the common dir, and
			// with the extension on its keys override the shared config.
			name:      "main checkout's config.worktree written",
			guardMain: true,
			setup: both(
				git(inMain, "config", "extensions.worktreeConfig", "true"),
				git(inMain, "config", "--worktree", "core.sparseCheckout", "false"),
			),
			act:  git(inMain, "config", "--worktree", "core.hooksPath", "/dev/null"),
			want: []string{"core.hookspath added"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mainDir, linkedDir string
			if tt.soleClone {
				mainDir = newGitGuardClone(t)
			} else {
				mainDir, linkedDir = newGitGuardFixture(t)
			}
			guarded := linkedDir
			if tt.guardMain || tt.soleClone {
				guarded = mainDir
			}
			f := fixture{mainDir: mainDir, linkedDir: linkedDir, dirs: locateGitGuardFixture(t, guarded)}
			if f.dirs.shared == tt.soleClone {
				t.Fatalf("fixture shared = %v, want %v", f.dirs.shared, !tt.soleClone)
			}
			if tt.setup != nil {
				tt.setup(t, f)
			}
			before := readRepoGitState(f.dirs)
			tt.act(t, f)
			changes := diffRepoGitState(before, readRepoGitState(f.dirs))

			if tt.want == nil {
				if len(changes) != 0 {
					t.Fatalf("got changes %+v, want none", changes)
				}
				return
			}
			if len(changes) != 1 {
				t.Fatalf("got %d changes %+v, want 1", len(changes), changes)
			}
			c := changes[0]
			if c.routine != tt.routine {
				t.Errorf("routine = %v, want %v (detail %q)", c.routine, tt.routine, c.detail)
			}
			for _, w := range tt.want {
				if !strings.Contains(c.detail, w) {
					t.Errorf("detail %q does not mention %q", c.detail, w)
				}
			}
			if strings.Contains(c.detail, guardFixtureHost) {
				t.Errorf("detail %q echoes a config value", c.detail)
			}
		})
	}
}

func TestDiffGitConfigText(t *testing.T) {
	const base = "[core]\n\tbare = false\n" +
		"[remote \"origin\"]\n\turl = https://" + guardFixtureHost + "/upstream.git\n" +
		"\tfetch = +refs/heads/*:refs/remotes/origin/*\n"

	tests := []struct {
		name    string
		after   string
		scope   gitConfigScope
		routine bool
		want    string
	}{
		{
			name: "comments and layout only",
			after: "# reformatted\n[core]\nbare=false\n[remote \"origin\"]\n" +
				"url = https://" + guardFixtureHost + "/upstream.git\nfetch = +refs/heads/*:refs/remotes/origin/*\n",
			scope:   soleCloneConfig,
			routine: true,
			want:    "rewritten with no key or value change",
		},
		{
			name:    "empty section left behind by a removal",
			after:   base + "[branch \"gone\"]\n",
			scope:   soleCloneConfig,
			routine: true,
			want:    "rewritten with no key or value change",
		},
		{
			name:  "not parseable",
			after: "[core\n\tbare = true\n",
			scope: sharedCloneConfig,
			want:  "not parseable as git config",
		},
		{
			name:  "existing remote gains a fetch refspec",
			after: base + "[remote \"origin\"]\n\tfetch = +refs/pull/*/head:refs/remotes/origin/pr/*\n",
			scope: sharedCloneConfig,
			want:  "remote.origin.fetch changed",
		},
		{
			name:    "remote with a dotted name added",
			after:   base + "[remote \"my.fork\"]\n\turl = https://" + guardFixtureHost + "/fork.git\n",
			scope:   soleCloneConfig,
			routine: true,
			want:    "remote.my.fork.url added",
		},
		{
			name:    "top-level key in a shared clone's config",
			after:   base + "[merge]\n\tconflictStyle = zdiff3\n",
			scope:   sharedCloneConfig,
			routine: true,
			want:    "merge.conflictstyle added",
		},
		{
			name:  "top-level key in a sole clone's config",
			after: base + "[merge]\n\tconflictStyle = zdiff3\n",
			scope: soleCloneConfig,
			want:  "merge.conflictstyle added",
		},
		{
			name:  "branch key outside the common config",
			after: base + "[branch \"feature\"]\n\tremote = origin\n",
			scope: checkoutConfig,
			want:  "branch.feature.remote added",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := diffGitConfig("config", base, tt.after, tt.scope)
			if c.routine != tt.routine {
				t.Errorf("routine = %v, want %v (detail %q)", c.routine, tt.routine, c.detail)
			}
			if !strings.Contains(c.detail, tt.want) {
				t.Errorf("detail %q does not mention %q", c.detail, tt.want)
			}
			if strings.Contains(c.detail, guardFixtureHost) {
				t.Errorf("detail %q echoes a config value", c.detail)
			}
		})
	}

	t.Run("long key lists are capped", func(t *testing.T) {
		var after strings.Builder
		after.WriteString(base)
		for i := range 20 {
			fmt.Fprintf(&after, "[branch \"b%02d\"]\n\tremote = origin\n", i)
		}
		c := diffGitConfig("config", base, after.String(), soleCloneConfig)
		if !c.routine || !strings.HasSuffix(c.detail, ", and 8 more") {
			t.Errorf("got routine=%v detail %q, want a routine change listing 12 keys and 8 more", c.routine, c.detail)
		}
	})
}

func TestSplitConfigKey(t *testing.T) {
	for _, tt := range []struct{ key, section, subsection, name string }{
		{"core.bare", "core", "", "bare"},
		{"remote.origin.url", "remote", "origin", "url"},
		{"branch.release/v1.2.merge", "branch", "release/v1.2", "merge"},
		{"remote.my.fork.gh-resolved", "remote", "my.fork", "gh-resolved"},
	} {
		section, subsection, name := splitConfigKey(tt.key)
		if section != tt.section || subsection != tt.subsection || name != tt.name {
			t.Errorf("splitConfigKey(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tt.key, section, subsection, name, tt.section, tt.subsection, tt.name)
		}
	}
}

func TestReportGitStateChanges(t *testing.T) {
	common := t.TempDir()
	configPath := filepath.Join(common, "config")
	hookPath := filepath.Join(common, "hooks", "pre-commit")
	const configBefore = "[core]\n\tbare = false\n"
	before := repoGitState{
		dirs: repoGitDirs{gitDir: common, common: common},
		files: map[string]gitFileSnap{
			configPath: {present: true, data: configBefore},
			hookPath:   {present: false},
		},
	}
	routine := gitStateChange{path: configPath, detail: "branch.feature.remote added", routine: true}
	clobber := gitStateChange{path: configPath, detail: "core.bare changed"}
	newHook := gitStateChange{path: hookPath, detail: "new file (17 bytes)"}
	const savedPrefix = "pre-run copies of the changed files: "

	t.Run("no changes", func(t *testing.T) {
		if report, failed := reportGitStateChanges(nil, before, t.TempDir()); report != "" || failed {
			t.Errorf("got (%q, %v), want an empty passing report", report, failed)
		}
	})

	t.Run("routine changes only draw a note", func(t *testing.T) {
		saveDir := t.TempDir()
		report, failed := reportGitStateChanges([]gitStateChange{routine}, before, saveDir)
		if failed {
			t.Error("a routine shared-clone change failed the run")
		}
		// The likely writer of a routine change is another process, so the
		// note says that before it says this suite could have.
		if !strings.HasPrefix(report, "NOTE: another process") || !strings.Contains(report, routine.detail) || strings.Contains(report, "ERROR") {
			t.Errorf("report %q, want a NOTE that names another process first, names %q, and has no ERROR", report, routine.detail)
		}
		if entries, err := os.ReadDir(saveDir); err != nil || len(entries) != 0 {
			t.Errorf("save dir holds %v (err %v), want nothing saved for routine changes", entries, err)
		}
	})

	t.Run("clobber fails and saves the pre-run copies", func(t *testing.T) {
		saveDir := t.TempDir()
		report, failed := reportGitStateChanges([]gitStateChange{clobber, newHook}, before, saveDir)
		if !failed {
			t.Error("a clobber did not fail the run")
		}
		for _, want := range []string{"ERROR: ", "wy-qlvlmf", clobber.detail, newHook.detail} {
			if !strings.Contains(report, want) {
				t.Errorf("report %q does not mention %q", report, want)
			}
		}
		var savedDir string
		for _, line := range strings.Split(report, "\n") {
			if dir, ok := strings.CutPrefix(line, savedPrefix); ok {
				savedDir = dir
			}
		}
		if filepath.Dir(savedDir) != saveDir {
			t.Fatalf("report %q names no save dir under %s", report, saveDir)
		}
		entries, err := os.ReadDir(savedDir)
		if err != nil {
			t.Fatal(err)
		}
		// The hook did not exist before the run, so only the config is saved.
		if len(entries) != 1 {
			t.Fatalf("saved %v, want exactly the pre-run config", entries)
		}
		saved := filepath.Join(savedDir, entries[0].Name())
		if data, err := os.ReadFile(saved); err != nil || string(data) != configBefore {
			t.Errorf("saved copy = %q (err %v), want %q", data, err, configBefore)
		}
		if runtime.GOOS != "windows" {
			for path, want := range map[string]os.FileMode{savedDir: 0o700, saved: 0o600} {
				info, err := os.Stat(path)
				if err != nil {
					t.Error(err)
				} else if got := info.Mode().Perm(); got != want {
					t.Errorf("%s: mode %v, want %v: a config copy can hold credentials", path, got, want)
				}
			}
		}
	})

	t.Run("notes and errors together", func(t *testing.T) {
		report, failed := reportGitStateChanges([]gitStateChange{routine, newHook}, before, t.TempDir())
		note, errPart, found := strings.Cut(report, "ERROR: ")
		if !failed || !found || !strings.Contains(note, routine.detail) || !strings.Contains(errPart, newHook.detail) {
			t.Errorf("got (%q, %v), want the routine change under NOTE, the hook under ERROR, and a failed run", report, failed)
		}
	})

	t.Run("a failed save is reported and still fails", func(t *testing.T) {
		report, failed := reportGitStateChanges([]gitStateChange{clobber}, before, filepath.Join(t.TempDir(), "missing"))
		if !failed || !strings.Contains(report, "could not save pre-run copies") {
			t.Errorf("got (%q, %v), want a failed run that says the copies were not saved", report, failed)
		}
	})
}
