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
				if value == "<abs>" {
					value = link
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
			require.Equal(t, before, dirSnapshot(t, outside), "link target contents changed")
			require.Equal(t, configBefore, localHooksPath(t, repo), "core.hooksPath changed")
			info, lerr := os.Lstat(link)
			require.NoError(t, lerr)
			require.NotZero(t, info.Mode()&os.ModeSymlink, "hooks destination link replaced")
		})
	}
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
