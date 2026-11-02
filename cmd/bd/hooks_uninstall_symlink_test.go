package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUninstallHooksRefusesSymlinkedHooksDir covers AC-3: uninstall refuses a
// hooks directory that is itself a symlink, before reading or removing any
// entry in it and before invoking the reset callback.
func TestUninstallHooksRefusesSymlinkedHooksDir(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside-hooks")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// A bd hook in the link target, which an unguarded uninstall would remove.
	targetHook := filepath.Join(outside, "pre-commit")
	writeFileForWorktreeTest(t, targetHook, []byte("#!/bin/sh\n# bd-hooks-version: 1.0.0\n"))
	linkedDir := filepath.Join(root, "linked-hooks")
	if err := os.Symlink(outside, linkedDir); err != nil {
		t.Fatal(err)
	}

	resetCalled := false
	err := uninstallHooksAt(linkedDir, func() error { resetCalled = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected uninstall to refuse symlinked hooks directory, got %v", err)
	}
	if resetCalled {
		t.Errorf("reset callback invoked despite the refusal")
	}
	if got := readFileForWorktreeTest(t, targetHook); got != "#!/bin/sh\n# bd-hooks-version: 1.0.0\n" {
		t.Errorf("hook in the symlink target modified by uninstall: %q", got)
	}
}

// TestUninstallHooksSkipsSymlinkedHookLeavesTarget covers AC-4: in a real
// hooks directory a symlinked hook is left, with its target untouched, while
// ordinary bd hooks are still processed and the reset callback still runs.
func TestUninstallHooksSkipsSymlinkedHookLeavesTarget(t *testing.T) {
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlinked hook pointing at a foreign script outside the hooks dir.
	target := filepath.Join(root, "foreign", "pre-commit")
	writeFileForWorktreeTest(t, target, []byte("#!/bin/sh\necho keep\n"))
	if err := os.Symlink(target, filepath.Join(hooksDir, "pre-commit")); err != nil {
		t.Fatal(err)
	}
	// An ordinary legacy bd hook next to it.
	writeFileForWorktreeTest(t, filepath.Join(hooksDir, "post-merge"), []byte("#!/bin/sh\n# bd-hooks-version: 1.0.0\n"))

	resetCalled := false
	var err error
	captureHookStderr(t, func() {
		err = uninstallHooksAt(hooksDir, func() error { resetCalled = true; return nil })
	})
	if err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}
	fi, statErr := os.Lstat(filepath.Join(hooksDir, "pre-commit"))
	if statErr != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlinked hook entry was removed or replaced: lstat=%v", statErr)
	}
	if got := readFileForWorktreeTest(t, target); got != "#!/bin/sh\necho keep\n" {
		t.Errorf("symlinked hook's target modified: %q", got)
	}
	if _, statErr := os.Lstat(filepath.Join(hooksDir, "post-merge")); statErr == nil {
		t.Errorf("ordinary bd hook left behind despite a successful uninstall")
	}
	if !resetCalled {
		t.Errorf("reset callback not invoked for an otherwise successful uninstall")
	}
}

// TestUninstallHooksLeavesNonRegularBackupInPlace covers AC-4: a legacy
// backup that is not a regular file (a symlink) is left in place with a
// warning instead of being renamed over the hook path.
func TestUninstallHooksLeavesNonRegularBackupInPlace(t *testing.T) {
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	writeFileForWorktreeTest(t, hookPath, []byte("#!/bin/sh\n# bd-hooks-version: 1.0.0\n"))
	backupTarget := filepath.Join(root, "backup-target")
	writeFileForWorktreeTest(t, backupTarget, []byte("#!/bin/sh\necho original\n"))
	if err := os.Symlink(backupTarget, hookPath+".backup"); err != nil {
		t.Fatal(err)
	}

	var err error
	captureHookStderr(t, func() {
		err = uninstallHooksAt(hooksDir, func() error { return nil })
	})
	if err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}
	if _, statErr := os.Lstat(hookPath); statErr == nil {
		t.Errorf("legacy bd hook left behind despite a successful uninstall")
	}
	bfi, statErr := os.Lstat(hookPath + ".backup")
	if statErr != nil || bfi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("non-regular backup entry was renamed away or replaced: lstat=%v", statErr)
	}
	if got := readFileForWorktreeTest(t, backupTarget); got != "#!/bin/sh\necho original\n" {
		t.Errorf("non-regular backup's target modified: %q", got)
	}
}

// TestUninstallHooksRestoresRegularBackup covers AC-4's allowed half: a
// regular legacy backup is restored over the removed hook as before.
func TestUninstallHooksRestoresRegularBackup(t *testing.T) {
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	writeFileForWorktreeTest(t, hookPath, []byte("#!/bin/sh\n# bd-hooks-version: 1.0.0\n"))
	writeFileForWorktreeTest(t, hookPath+".backup", []byte("#!/bin/sh\necho original\n"))

	if err := uninstallHooksAt(hooksDir, func() error { return nil }); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}
	if got := readFileForWorktreeTest(t, hookPath); got != "#!/bin/sh\necho original\n" {
		t.Errorf("regular backup not restored over removed hook: %q", got)
	}
	if _, statErr := os.Lstat(hookPath + ".backup"); statErr == nil {
		t.Errorf("backup sidecar left behind after a successful restore")
	}
}