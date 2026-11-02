package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// TestUninstallHooksDoesNotRewriteMarkedTargetThroughSymlinkedHook retains
// #7889's sharpest uninstall case (PR #8070 carries it): the symlinked hook
// points at a file that itself carries bd section markers, so an unguarded
// uninstall reads through the link, finds the markers and rewrites the
// target in place. The Lstat skip leaves the entry, and the target, alone.
func TestUninstallHooksDoesNotRewriteMarkedTargetThroughSymlinkedHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := t.TempDir()
	realHooks := filepath.Join(root, "real-hooks")
	if err := os.MkdirAll(realHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	markedTarget := filepath.Join(root, "marked-pre-commit")
	markedContent := "#!/usr/bin/env sh\necho keep\n" + generateHookSection("pre-commit")
	writeFileForWorktreeTest(t, markedTarget, []byte(markedContent))
	if err := os.Symlink(markedTarget, filepath.Join(realHooks, "pre-commit")); err != nil {
		t.Fatal(err)
	}

	var err error
	captureHookStderr(t, func() {
		err = uninstallHooksAt(realHooks, func() error { return nil })
	})
	if err != nil {
		t.Fatalf("uninstallHooksAt failed: %v", err)
	}
	if got := readFileForWorktreeTest(t, markedTarget); got != markedContent {
		t.Errorf("marked target rewritten through the symlinked hook: %q", got)
	}
	fi, statErr := os.Lstat(filepath.Join(realHooks, "pre-commit"))
	if statErr != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlinked hook entry removed or replaced: lstat=%v", statErr)
	}
}

// TestInstallHooksAdmitsDiscoveredSymlinkedBeadsWorkspace retains #7889's
// discovery case with the expectation this delta deliberately inverts:
// .beads itself a symlink, reached through workspace discovery rather than
// BEADS_DIR, is a supported alias (engdocs/REPO_CONTEXT.md documents the
// layout), so installing into .beads/hooks inside the aliased store
// succeeds — only the hooks directory entry itself is refused.
func TestInstallHooksAdmitsDiscoveredSymlinkedBeadsWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	isolateBeadsDirForTest(t)
	root := t.TempDir()
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
	t.Cleanup(git.ResetCaches)

	if err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, true); err != nil {
		t.Fatalf("expected install to admit the discovered symlinked .beads workspace alias, got %v", err)
	}
	body := readFileForWorktreeTest(t, filepath.Join(outsideBeads, "hooks", "pre-commit"))
	if !strings.Contains(body, hookSectionBeginPrefix) {
		t.Errorf("pre-commit hook missing bd section inside the aliased store: %q", body)
	}
}