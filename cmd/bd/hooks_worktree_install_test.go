package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeHooksDir pins the gating matrix for the linked-worktree install
// target. The rewrite fires only for a hooks directory inside the primary
// checkout, outside the common Git directory, that the invoking worktree
// tracks; everything else keeps the directory the mode branch resolved.
func TestWorktreeHooksDir(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(root, "main")
	worktree := filepath.Join(root, "wt")
	commonDir := filepath.Join(primary, ".git")
	bare := filepath.Join(root, "repo.git")
	mkdirs(t,
		filepath.Join(primary, ".beads"),
		commonDir,
		filepath.Join(worktree, ".beads"),
		bare,
		filepath.Join(root, "elsewhere"),
	)

	tracked := func(string) bool { return true }
	untracked := func(string) bool { return false }

	tests := []struct {
		name      string
		hooksDir  string
		wtRoot    string
		mainRoot  string
		commonDir string
		tracked   func(string) bool
		want      string
	}{
		{
			name:      "tracked .beads/hooks moves to the worktree",
			hooksDir:  filepath.Join(primary, ".beads", "hooks"),
			wtRoot:    worktree,
			mainRoot:  primary,
			commonDir: commonDir,
			tracked:   tracked,
			want:      filepath.Join(worktree, ".beads", "hooks"),
		},
		{
			name:      "tracked .beads-hooks moves to the worktree",
			hooksDir:  filepath.Join(primary, ".beads-hooks"),
			wtRoot:    worktree,
			mainRoot:  primary,
			commonDir: commonDir,
			tracked:   tracked,
			want:      filepath.Join(worktree, ".beads-hooks"),
		},
		{
			// An ignored or primary-only layout has one copy, the one Git runs.
			name:      "untracked primary-only hooks stay",
			hooksDir:  filepath.Join(primary, ".beads", "hooks"),
			wtRoot:    worktree,
			mainRoot:  primary,
			commonDir: commonDir,
			tracked:   untracked,
			want:      filepath.Join(primary, ".beads", "hooks"),
		},
		{
			name:      "common .git/hooks is shared by Git and stays",
			hooksDir:  filepath.Join(commonDir, "hooks"),
			wtRoot:    worktree,
			mainRoot:  primary,
			commonDir: commonDir,
			tracked:   tracked,
			want:      filepath.Join(commonDir, "hooks"),
		},
		{
			// A bare repository's primary root is the parent of the common
			// directory, so its hooks directory is inside that root; the
			// common-directory check is what keeps it in place.
			name:      "bare repository hooks stay",
			hooksDir:  filepath.Join(bare, "hooks"),
			wtRoot:    worktree,
			mainRoot:  root,
			commonDir: bare,
			tracked:   tracked,
			want:      filepath.Join(bare, "hooks"),
		},
		{
			name:      "a directory outside the primary checkout stays",
			hooksDir:  filepath.Join(root, "elsewhere", "hooks"),
			wtRoot:    worktree,
			mainRoot:  primary,
			commonDir: commonDir,
			tracked:   tracked,
			want:      filepath.Join(root, "elsewhere", "hooks"),
		},
		{
			name:      "a directory already inside the worktree stays",
			hooksDir:  filepath.Join(worktree, ".beads", "hooks"),
			wtRoot:    worktree,
			mainRoot:  primary,
			commonDir: commonDir,
			tracked:   tracked,
			want:      filepath.Join(worktree, ".beads", "hooks"),
		},
		{
			name:      "the primary checkout itself stays",
			hooksDir:  filepath.Join(primary, ".beads", "hooks"),
			wtRoot:    primary,
			mainRoot:  primary,
			commonDir: commonDir,
			tracked:   tracked,
			want:      filepath.Join(primary, ".beads", "hooks"),
		},
		{
			name:     "unknown roots stay",
			hooksDir: filepath.Join(primary, ".beads", "hooks"),
			tracked:  tracked,
			want:     filepath.Join(primary, ".beads", "hooks"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := worktreeHooksDir(tc.hooksDir, tc.wtRoot, tc.mainRoot, tc.commonDir, tc.tracked)
			if got != tc.want {
				t.Errorf("worktreeHooksDir(%q, wt=%q, main=%q, common=%q) = %q, want %q",
					tc.hooksDir, tc.wtRoot, tc.mainRoot, tc.commonDir, got, tc.want)
			}
		})
	}

	// The tracked probe is asked about the path the worktree would track.
	var asked string
	worktreeHooksDir(filepath.Join(primary, ".beads", "hooks"), worktree, primary, commonDir, func(rel string) bool {
		asked = rel
		return true
	})
	if want := filepath.Join(".beads", "hooks"); asked != want {
		t.Errorf("tracked probe asked about %q, want %q", asked, want)
	}
}

// linkedWorktreeHooksFixture creates a primary checkout with one commit and a
// linked worktree on its own branch, both under a symlink-resolved root, and
// returns their paths. hooks, when non-nil, is called before the worktree is
// added so the fixture can commit or leave untracked a hook set.
func linkedWorktreeHooksFixture(t *testing.T, hooks func(primary string)) (primary, worktree string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	primary = filepath.Join(root, "main")
	worktree = filepath.Join(root, "wt")
	mkdirs(t, primary)
	runGitForWorktreeTest(t, primary, "init", "-q", "-b", "main")
	runGitForWorktreeTest(t, primary, "config", "user.email", "test@example.com")
	runGitForWorktreeTest(t, primary, "config", "user.name", "test")
	runGitForWorktreeTest(t, primary, "commit", "-q", "--allow-empty", "-m", "init")
	if hooks != nil {
		hooks(primary)
	}
	runGitForWorktreeTest(t, primary, "worktree", "add", "-q", worktree, "-b", "wt")
	return primary, worktree
}

// oldHookMarker is the begin marker of a hook installed by an earlier bd: still
// bd-owned, so the install must upgrade it rather than refuse it.
const oldHookMarker = hookSectionBeginPrefix + " v0.0.1 ---"

// writeOldHooks writes every managed hook into dir with an outdated section.
func writeOldHooks(t *testing.T, dir string) {
	t.Helper()
	mkdirs(t, dir)
	for _, name := range managedHookNames {
		section := strings.Replace(generateHookSection(name), hookSectionBeginLine(), oldHookMarker, 1)
		writeFileForWorktreeTest(t, filepath.Join(dir, name), []byte("#!/usr/bin/env sh\n"+section))
	}
}

func assertHooksCurrent(t *testing.T, dir string) {
	t.Helper()
	for _, name := range managedHookNames {
		if content := readFileForWorktreeTest(t, filepath.Join(dir, name)); !strings.Contains(content, hookSectionBeginLine()) {
			t.Errorf("%s was not upgraded:\n%s", filepath.Join(dir, name), content)
		}
	}
}

func assertHooksOld(t *testing.T, dir string) {
	t.Helper()
	for _, name := range managedHookNames {
		if content := readFileForWorktreeTest(t, filepath.Join(dir, name)); !strings.Contains(content, oldHookMarker) {
			t.Errorf("%s was rewritten:\n%s", filepath.Join(dir, name), content)
		}
	}
}

func gitOutputForWorktreeTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitCommand(args...)
	cmd.Dir = dir
	cmd.Env = scrubGitHookEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestInstallHooksFromLinkedWorktreeWritesTrackedCopy is the default bd init
// layout: .beads/hooks committed and core.hooksPath absolute into the primary
// checkout (GH#2414). An install run from a linked worktree must upgrade the
// worktree's own tracked copy, leave the primary checkout's working tree
// untouched, and keep core.hooksPath pointing at the primary.
func TestInstallHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	primary, worktree := linkedWorktreeHooksFixture(t, func(primary string) {
		writeOldHooks(t, filepath.Join(primary, ".beads", "hooks"))
		runGitForWorktreeTest(t, primary, "add", ".beads/hooks")
		runGitForWorktreeTest(t, primary, "commit", "-q", "-m", "track hooks")
		runGitForWorktreeTest(t, primary, "config", "core.hooksPath", filepath.Join(primary, ".beads", "hooks"))
	})

	t.Chdir(worktree)
	resetRepoCachesForTest(t)
	if err := installStandaloneHooks(managedHookNames, false, false, false, false); err != nil {
		t.Fatalf("install from linked worktree: %v", err)
	}

	assertHooksCurrent(t, filepath.Join(worktree, ".beads", "hooks"))
	assertHooksOld(t, filepath.Join(primary, ".beads", "hooks"))
	if status := gitOutputForWorktreeTest(t, primary, "status", "--porcelain"); status != "" {
		t.Errorf("primary checkout dirtied by a worktree install:\n%s", status)
	}
	if got, want := gitOutputForWorktreeTest(t, worktree, "config", "--get", "core.hooksPath"), filepath.Join(primary, ".beads", "hooks"); got != want {
		t.Errorf("core.hooksPath = %q, want the primary's %q", got, want)
	}
}

// TestInstallSharedHooksFromLinkedWorktreeConfiguresPrimary covers --shared,
// whose .beads-hooks exists to be committed: the worktree's copy is written,
// but the recorded core.hooksPath still names the primary checkout's copy,
// which is the one Git resolves from every worktree.
func TestInstallSharedHooksFromLinkedWorktreeConfiguresPrimary(t *testing.T) {
	primary, worktree := linkedWorktreeHooksFixture(t, func(primary string) {
		writeOldHooks(t, filepath.Join(primary, ".beads-hooks"))
		runGitForWorktreeTest(t, primary, "add", ".beads-hooks")
		runGitForWorktreeTest(t, primary, "commit", "-q", "-m", "track shared hooks")
	})

	t.Chdir(worktree)
	resetRepoCachesForTest(t)
	if err := installStandaloneHooks(managedHookNames, false, true, false, false); err != nil {
		t.Fatalf("shared install from linked worktree: %v", err)
	}

	assertHooksCurrent(t, filepath.Join(worktree, ".beads-hooks"))
	assertHooksOld(t, filepath.Join(primary, ".beads-hooks"))
	if status := gitOutputForWorktreeTest(t, primary, "status", "--porcelain"); status != "" {
		t.Errorf("primary checkout dirtied by a worktree install:\n%s", status)
	}
	if got, want := gitOutputForWorktreeTest(t, worktree, "config", "--get", "core.hooksPath"), filepath.Join(primary, ".beads-hooks"); got != want {
		t.Errorf("core.hooksPath = %q, want the primary's %q", got, want)
	}
}

// TestInstallHooksFromLinkedWorktreeKeepsUntrackedPrimaryCopy: when only the
// primary checkout has a .beads/hooks and nothing tracks it, that copy is the
// one Git runs and the install must keep upgrading it rather than drop an
// unused copy into the worktree.
func TestInstallHooksFromLinkedWorktreeKeepsUntrackedPrimaryCopy(t *testing.T) {
	primary, worktree := linkedWorktreeHooksFixture(t, func(primary string) {
		writeOldHooks(t, filepath.Join(primary, ".beads", "hooks"))
		runGitForWorktreeTest(t, primary, "config", "core.hooksPath", filepath.Join(primary, ".beads", "hooks"))
	})

	t.Chdir(worktree)
	resetRepoCachesForTest(t)
	if err := installStandaloneHooks(managedHookNames, false, false, false, false); err != nil {
		t.Fatalf("install from linked worktree: %v", err)
	}

	assertHooksCurrent(t, filepath.Join(primary, ".beads", "hooks"))
	if _, err := os.Stat(filepath.Join(worktree, ".beads")); !os.IsNotExist(err) {
		t.Errorf("worktree gained a .beads it does not track (stat err=%v)", err)
	}
}
