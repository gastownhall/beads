package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectPendingMigrations_Hooks(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepoInDir(t, tmpDir)
	forceRepoHooksPath(t, tmpDir)

	_, hooksDir, err := resolveGitHooksDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveGitHooksDir failed: %v", err)
	}

	writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookFile(t, filepath.Join(hooksDir, "pre-commit.old"), "#!/bin/sh\necho old\n")

	pending := DetectPendingMigrations(tmpDir)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending migration, got %d", len(pending))
	}

	m := pending[0]
	if m.Name != "hooks" {
		t.Fatalf("expected migration name 'hooks', got %q", m.Name)
	}
	if m.Command != "bd doctor --fix" {
		t.Fatalf("expected command 'bd doctor --fix', got %q", m.Command)
	}
	if m.Priority != 2 {
		t.Fatalf("expected recommended priority 2, got %d", m.Priority)
	}
}

func TestDetectPendingMigrations_HooksBrokenMarkerIsCritical(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepoInDir(t, tmpDir)
	forceRepoHooksPath(t, tmpDir)

	_, hooksDir, err := resolveGitHooksDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveGitHooksDir failed: %v", err)
	}

	writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n")

	pending := DetectPendingMigrations(tmpDir)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending migration, got %d", len(pending))
	}
	if pending[0].Priority != 1 {
		t.Fatalf("expected critical priority 1 for broken marker, got %d", pending[0].Priority)
	}
	if pending[0].Command != "bd doctor --fix" {
		t.Fatalf("expected command 'bd doctor --fix', got %q", pending[0].Command)
	}

	check := CheckPendingMigrations(tmpDir)
	if check.Status != StatusError {
		t.Fatalf("expected error status for broken marker migration, got %q", check.Status)
	}
}

func TestDetectPendingMigrations_HooksWorktreeFallback(t *testing.T) {
	clearResolveBeadsDirCache()
	t.Cleanup(clearResolveBeadsDirCache)

	mainRepoDir, worktreeDir := setupWorktreeRepo(t)
	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0755); err != nil {
		t.Fatalf("failed to create shared .beads: %v", err)
	}

	_, hooksDir, err := resolveGitHooksDir(worktreeDir)
	if err != nil {
		t.Fatalf("resolveGitHooksDir failed: %v", err)
	}

	writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookFile(t, filepath.Join(hooksDir, "pre-commit.old"), "#!/bin/sh\necho old\n")

	pending := DetectPendingMigrations(worktreeDir)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending migration from shared worktree, got %d", len(pending))
	}
	if pending[0].Name != "hooks" {
		t.Fatalf("expected hooks migration, got %q", pending[0].Name)
	}
}
