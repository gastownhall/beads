package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// TestInstallHooksRefusesSymlinkedBeadsHooksDir pins the #7889 refusal that
// PR #8070 carries: a .beads/hooks directory that is a symlink to a directory
// outside the repo must not be written through.
func TestInstallHooksRefusesSymlinkedBeadsHooksDir(t *testing.T) {
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
	if err := os.Symlink(outsideHooks, filepath.Join(beadsDir, "hooks")); err != nil {
		t.Fatal(err)
	}

	err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, true)
	if got := readFileForWorktreeTest(t, sentinel); got != "#!/bin/sh\necho keep\n" {
		t.Errorf("hook outside the repo modified through symlinked .beads/hooks: %q", got)
	}
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected install to refuse symlinked .beads/hooks, got %v", err)
	}
}
