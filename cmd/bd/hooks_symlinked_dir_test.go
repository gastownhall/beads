package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/stretchr/testify/require"
)

const symlinkedDirSentinel = "#!/bin/sh\necho keep\n"

// setupSymlinkedHooksDirRepo creates root/repo with a .beads workspace (also
// selected through BEADS_DIR) and chdirs into it. root holds the fixture's
// outside directories.
func setupSymlinkedHooksDirRepo(t *testing.T) (root, repo string) {
	t.Helper()
	isolateBeadsDirForTest(t)
	root = t.TempDir()
	repo = filepath.Join(root, "repo")
	beadsDir := filepath.Join(repo, ".beads")
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"))
	runGitForWorktreeTest(t, repo, "init", "-q")
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	t.Setenv("BEADS_DIR", beadsDir)
	return root, repo
}

// newOutsideHooksDir creates an outside directory holding a foreign pre-commit
// hook, the content a write through a symlinked hooks directory would reach.
func newOutsideHooksDir(t *testing.T, root string) string {
	t.Helper()
	outside := filepath.Join(root, "outside-hooks")
	writeFileForWorktreeTest(t, filepath.Join(outside, "pre-commit"), []byte(symlinkedDirSentinel))
	return outside
}

// symlinkOrSkip creates link -> target, skipping where symlinks are unavailable.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
}

// dirSnapshot maps each entry name in dir to its content ("<dir>" for a
// directory, "<link> target" for a symlink); a missing dir is nil.
func dirSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	snap := map[string]string{}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}
			snap[entry.Name()] = "<link> " + target
		case info.IsDir():
			snap[entry.Name()] = "<dir>"
		default:
			snap[entry.Name()] = readFileForWorktreeTest(t, path)
		}
	}
	return snap
}

// localHooksPath returns repo's local core.hooksPath, "" when unset.
func localHooksPath(t *testing.T, repo string) string {
	t.Helper()
	cmd := gitCommand("config", "--local", "--get", "core.hooksPath")
	cmd.Dir = repo
	cmd.Env = scrubGitHookEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestInstallHooksRefusesSymlinkedHooksDirInEveryMode: whichever mode selects
// the hooks directory, a destination that is itself a symlink (dangling
// included) is refused before preservation copies, hook writes, or Git
// configuration reach the link target.
func TestInstallHooksRefusesSymlinkedHooksDirInEveryMode(t *testing.T) {
	tests := []struct {
		name           string
		link           string // repo-relative hooks destination made a symlink
		hooksPath      string // core.hooksPath to configure ("" leaves it unset)
		shared, beads  bool
		dangling       bool
		preserveSource bool // seed .git/hooks/post-rewrite for the preservation copy
	}{
		{name: "default git hooks", link: filepath.Join(".git", "hooks")},
		{name: "absolute core.hooksPath", link: "team-hooks", hooksPath: "<abs>"},
		{name: "absolute core.hooksPath with trailing separator", link: "team-hooks", hooksPath: "<abs>/"},
		{name: "relative core.hooksPath", link: "team-hooks", hooksPath: "team-hooks"},
		{name: "shared", link: ".beads-hooks", shared: true, preserveSource: true},
		{name: "beads", link: filepath.Join(".beads", "hooks"), beads: true, preserveSource: true},
		{name: "beads dangling", link: filepath.Join(".beads", "hooks"), beads: true, dangling: true, preserveSource: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, repo := setupSymlinkedHooksDirRepo(t)
			link := filepath.Join(repo, tt.link)
			if tt.link == filepath.Join(".git", "hooks") {
				require.NoError(t, os.RemoveAll(link))
			}
			if tt.preserveSource {
				writeFileForWorktreeTest(t, filepath.Join(repo, ".git", "hooks", "post-rewrite"), []byte("#!/bin/sh\necho foreign\n"))
			}
			if tt.hooksPath != "" {
				value := tt.hooksPath
				switch value {
				case "<abs>":
					value = link
				case "<abs>/":
					value = link + string(os.PathSeparator)
				}
				runGitForWorktreeTest(t, repo, "config", "--local", "core.hooksPath", value)
				git.ResetCaches()
			}
			outside := filepath.Join(root, "missing-target")
			if !tt.dangling {
				outside = newOutsideHooksDir(t, root)
			}
			symlinkOrSkip(t, outside, link)
			before := dirSnapshot(t, outside)
			configBefore := localHooksPath(t, repo)

			err := installHooksWithOptions(managedHookNames, false, tt.shared, false, tt.beads)
			require.Error(t, err)
			require.Contains(t, err.Error(), "symlink")
			// The directory check refuses, not the per-hook guard that runs
			// after the directory is created.
			require.Contains(t, err.Error(), "refusing to install hooks: hooks directory")
			require.Equal(t, before, dirSnapshot(t, outside), "link target contents changed")
			require.Equal(t, configBefore, localHooksPath(t, repo), "core.hooksPath changed")
			info, lerr := os.Lstat(link)
			require.NoError(t, lerr)
			require.NotZero(t, info.Mode()&os.ModeSymlink, "hooks destination link replaced")
		})
	}
}

// TestInstallHooksAllowsMissingHooksDir pins the admitted half of the
// directory check: a hooks directory that does not exist yet is created, not
// refused.
// Adapted from the GLM candidate's test of the same name.
func TestInstallHooksAllowsMissingHooksDir(t *testing.T) {
	_, repo := setupSymlinkedHooksDirRepo(t)
	hooksDir := filepath.Join(repo, ".beads", "hooks")
	_, err := os.Lstat(hooksDir)
	require.True(t, os.IsNotExist(err), "fixture must start without .beads/hooks: %v", err)

	require.NoError(t, installHooksWithOptions([]string{"pre-commit"}, false, false, false, true))
	require.Contains(t, readFileForWorktreeTest(t, filepath.Join(hooksDir, "pre-commit")), hookSectionBeginPrefix)
}

// TestInstallHooksAllowsSymlinkedBeadsWorkspaceAlias: a .beads workspace
// that is a symlink to external storage, selected explicitly through
// BEADS_DIR, still installs into the real hooks directory behind the alias;
// only the hooks directory entry itself is checked, not its ancestors.
// Adapted from the GLM candidate's test of the same name.
func TestInstallHooksAllowsSymlinkedBeadsWorkspaceAlias(t *testing.T) {
	isolateBeadsDirForTest(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	store := filepath.Join(root, "real-store")
	writeFileForWorktreeTest(t, filepath.Join(store, "metadata.json"), []byte("{}\n"))
	runGitForWorktreeTest(t, repo, "init", "-q")
	symlinkOrSkip(t, store, filepath.Join(repo, ".beads"))
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	t.Setenv("BEADS_DIR", filepath.Join(repo, ".beads"))

	require.NoError(t, installHooksWithOptions([]string{"pre-commit"}, false, false, false, true))
	require.Contains(t, readFileForWorktreeTest(t, filepath.Join(store, "hooks", "pre-commit")), hookSectionBeginPrefix)
}

// TestInitHooksRefusesSymlinkedStorageHooksDir covers the selected init
// context: the storage hooks directory is resolved from the selected
// workspace, and a symlink there is refused like the standalone modes.
func TestInitHooksRefusesSymlinkedStorageHooksDir(t *testing.T) {
	selected, _, storage, _ := newInitHooksFixture(t)
	outside := newOutsideHooksDir(t, t.TempDir())
	symlinkOrSkip(t, outside, filepath.Join(storage, "hooks"))
	before := dirSnapshot(t, outside)
	configBefore := localHooksPath(t, selected)

	fs, _, err := withInitHooks(nil, selected, storage)
	require.NoError(t, err)
	err = fs.InstallGitHooks(t.Context(), domain.HooksInstallParams{HookNames: managedHookNames, BeadsHooks: true})
	require.ErrorContains(t, err, "symlink")
	require.Equal(t, before, dirSnapshot(t, outside), "link target contents changed")
	require.Equal(t, configBefore, localHooksPath(t, selected), "core.hooksPath changed")
}

// TestHookWriteGuardRefusesSymlinkedParentDir: the shared write guard checks
// the hook's immediate parent before its early return for a missing leaf, so
// hook migration cannot create a hook inside a symlinked directory's target.
func TestHookWriteGuardRefusesSymlinkedParentDir(t *testing.T) {
	root, repo := setupSymlinkedHooksDirRepo(t)
	outside := filepath.Join(root, "outside-hooks")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	link := filepath.Join(repo, "linked-hooks")
	symlinkOrSkip(t, outside, link)
	hookPath := filepath.Join(link, "pre-commit")

	t.Run("guard", func(t *testing.T) {
		err := guardHookWritePathWithProbe(hookPath, false, func(string) string { return "" })
		require.ErrorContains(t, err, "symlink")
	})
	t.Run("migration", func(t *testing.T) {
		plan := hookMigrationExecutionPlan{WriteOps: []hookMigrationWriteOp{{
			HookName:   "pre-commit",
			HookPath:   hookPath,
			State:      "missing_no_artifacts",
			SourceKind: hookMigrationWriteFromTemplate,
		}}}
		_, err := applyHookMigrationExecution(plan)
		require.ErrorContains(t, err, "symlink")
		require.Empty(t, dirSnapshot(t, outside), "migration created a hook in the link target")
	})
}

// TestUninstallHooksRefusesSymlinkedHooksDir: uninstall refuses a hooks
// directory that is a symlink before editing any hook in its target or
// resetting Git configuration, also when the directory is spelled with a
// trailing separator, as an absolute core.hooksPath can be.
func TestUninstallHooksRefusesSymlinkedHooksDir(t *testing.T) {
	for _, tt := range []struct{ name, suffix string }{
		{name: "plain"},
		{name: "trailing separator", suffix: string(os.PathSeparator)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, repo := setupSymlinkedHooksDirRepo(t)
			outside := filepath.Join(root, "outside-hooks")
			marked := "#!/usr/bin/env sh\necho keep\n" + generateHookSection("pre-commit")
			writeFileForWorktreeTest(t, filepath.Join(outside, "pre-commit"), []byte(marked))
			writeFileForWorktreeTest(t, filepath.Join(outside, "post-merge"), []byte("#!/usr/bin/env sh\n"+generateHookSection("post-merge")))
			link := filepath.Join(repo, "linked-hooks")
			symlinkOrSkip(t, outside, link)
			before := dirSnapshot(t, outside)

			resetCalled := false
			err := uninstallHooksAt(link+tt.suffix, func() error { resetCalled = true; return nil })
			require.ErrorContains(t, err, "symlink")
			require.False(t, resetCalled, "reset ran despite the refusal")
			require.Equal(t, before, dirSnapshot(t, outside), "link target contents changed")
		})
	}
}

// TestUninstallHooksLeavesUnsafeEntries: in a real hooks directory, uninstall
// skips a symlinked hook and leaves a non-regular legacy backup in place, each
// with a warning, while ordinary hooks and a regular backup are processed as
// before and the reset still runs.
func TestUninstallHooksLeavesUnsafeEntries(t *testing.T) {
	root, repo := setupSymlinkedHooksDirRepo(t)
	hooksDir := filepath.Join(repo, "real-hooks")
	outside := filepath.Join(root, "outside")
	legacy := "#!/bin/sh\n" + hookVersionPrefix + "0.1.0\nbd hooks run pre-push\n"
	marked := "#!/usr/bin/env sh\necho keep\n" + generateHookSection("pre-commit")
	writeFileForWorktreeTest(t, filepath.Join(outside, "marked-pre-commit"), []byte(marked))
	writeFileForWorktreeTest(t, filepath.Join(outside, "backup-target"), []byte(symlinkedDirSentinel))
	// pre-commit: a symlink to a bd-marked file outside; neither is touched.
	symlinkOrSkip(t, filepath.Join(outside, "marked-pre-commit"), filepath.Join(hooksDir, "pre-commit"))
	// post-merge: an ordinary bd-only hook, removed as before.
	writeFileForWorktreeTest(t, filepath.Join(hooksDir, "post-merge"), []byte("#!/usr/bin/env sh\n"+generateHookSection("post-merge")))
	// pre-push: a legacy hook whose regular backup is restored.
	writeFileForWorktreeTest(t, filepath.Join(hooksDir, "pre-push"), []byte(legacy))
	writeFileForWorktreeTest(t, filepath.Join(hooksDir, "pre-push.backup"), []byte("#!/bin/sh\necho original pre-push\n"))
	// post-checkout: a legacy hook whose backup is a symlink.
	writeFileForWorktreeTest(t, filepath.Join(hooksDir, "post-checkout"), []byte(legacy))
	symlinkOrSkip(t, filepath.Join(outside, "backup-target"), filepath.Join(hooksDir, "post-checkout.backup"))
	// prepare-commit-msg: a legacy hook whose backup is a directory.
	writeFileForWorktreeTest(t, filepath.Join(hooksDir, "prepare-commit-msg"), []byte(legacy))
	require.NoError(t, os.Mkdir(filepath.Join(hooksDir, "prepare-commit-msg.backup"), 0o755))
	outsideBefore := dirSnapshot(t, outside)

	resetCalled := false
	var err error
	stderr := captureStderr(t, func() {
		err = uninstallHooksAt(hooksDir, func() error { resetCalled = true; return nil })
	})
	require.NoError(t, err)
	require.True(t, resetCalled, "reset must still run")
	require.Equal(t, outsideBefore, dirSnapshot(t, outside), "symlink targets changed")
	require.Equal(t, map[string]string{
		"pre-commit":                "<link> " + filepath.Join(outside, "marked-pre-commit"),
		"pre-push":                  "#!/bin/sh\necho original pre-push\n",
		"post-checkout.backup":      "<link> " + filepath.Join(outside, "backup-target"),
		"prepare-commit-msg.backup": "<dir>",
	}, dirSnapshot(t, hooksDir))
	require.Contains(t, stderr, filepath.Join(hooksDir, "pre-commit"))
	require.Contains(t, stderr, filepath.Join(hooksDir, "post-checkout.backup"))
	require.Contains(t, stderr, filepath.Join(hooksDir, "prepare-commit-msg.backup"))
}

// Ported from #7889 by @Wenjix. Its last step is inverted: main supports a
// symlinked .beads, so installing into the real hooks directory inside the
// link target succeeds.
func TestHooksRefuseSymlinkedHooksDirAndUninstallSymlinkedHook(t *testing.T) {
	isolateBeadsDirForTest(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	beadsDir := filepath.Join(repo, ".beads")
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"))
	runGitForWorktreeTest(t, repo, "init", "-q")
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	t.Setenv("BEADS_DIR", beadsDir)

	outsideHooks := filepath.Join(root, "outside-hooks")
	if err := os.MkdirAll(outsideHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outsideHooks, "pre-commit")
	writeFileForWorktreeTest(t, sentinel, []byte("#!/bin/sh\necho keep\n"))

	hooksLink := filepath.Join(beadsDir, "hooks")
	if err := os.Symlink(outsideHooks, hooksLink); err != nil {
		t.Fatal(err)
	}

	if err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, true); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected installHooksWithOptions to refuse symlinked .beads/hooks, got %v", err)
	}
	if got := readFileForWorktreeTest(t, sentinel); got != "#!/bin/sh\necho keep\n" {
		t.Fatalf("outside hook modified by install: %q", got)
	}

	if err := uninstallHooksAt(hooksLink, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected uninstallHooksAt to refuse symlinked hooksDir, got %v", err)
	}

	// Real hooksDir with a symlinked hook file pointing to a file with bd section markers must not modify or remove the target on uninstall.
	realHooks := filepath.Join(repo, "real-hooks")
	if err := os.MkdirAll(realHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	markedTarget := filepath.Join(outsideHooks, "marked-pre-commit")
	markedContent := "#!/usr/bin/env sh\necho keep\n" + generateHookSection("pre-commit")
	writeFileForWorktreeTest(t, markedTarget, []byte(markedContent))
	if err := os.Symlink(markedTarget, filepath.Join(realHooks, "pre-commit")); err != nil {
		t.Fatal(err)
	}
	if err := uninstallHooksAt(realHooks, func() error { return nil }); err != nil {
		t.Fatalf("uninstallHooksAt failed: %v", err)
	}
	if got := readFileForWorktreeTest(t, markedTarget); got != markedContent {
		t.Fatalf("symlinked hook target modified on uninstall: %q", got)
	}

	// .beads itself symlinked, reached through discovery rather than
	// BEADS_DIR: the hooks directory inside the link target is real, so bd
	// installs into it.
	outsideBeads := filepath.Join(root, "outside-beads")
	if err := os.MkdirAll(filepath.Join(outsideBeads, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileForWorktreeTest(t, filepath.Join(outsideBeads, "metadata.json"), []byte("{}\n"))
	linkedRepo := filepath.Join(root, "linked")
	if err := os.MkdirAll(linkedRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForWorktreeTest(t, linkedRepo, "init", "-q")
	if err := os.Symlink(outsideBeads, filepath.Join(linkedRepo, ".beads")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", "")
	t.Chdir(linkedRepo)
	git.ResetCaches()

	if err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, true); err != nil {
		t.Fatalf("expected install through a symlinked .beads to succeed, got %v", err)
	}
	if got := readFileForWorktreeTest(t, filepath.Join(outsideBeads, "hooks", "pre-commit")); !strings.Contains(got, generateHookSection("pre-commit")) {
		t.Fatalf("expected the bd hook section in the symlinked .beads hooks dir, got %q", got)
	}
}
