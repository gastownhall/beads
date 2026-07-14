//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRepairWorktreeBeadsPermissions is the regression for GH#3593: after worktree
// creation, permissive .beads/ (e.g. 0755 from checkout + umask) must be repaired to 0700.
func TestRepairWorktreeBeadsPermissions(t *testing.T) {
	tmp := t.TempDir()
	worktreePath := filepath.Join(tmp, "demo-wt")
	beadsDir := filepath.Join(worktreePath, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.Chmod(beadsDir, 0o755); err != nil {
		t.Fatalf("chmod .beads: %v", err)
	}

	saveJSON := jsonOutput
	jsonOutput = true // suppress stderr from repair helper
	t.Cleanup(func() { jsonOutput = saveJSON })

	repairWorktreeBeadsPermissions(worktreePath)

	info, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatalf("stat .beads: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf(".beads permissions = %04o, want 0700", got)
	}
}

func TestRepairWorktreeBeadsPermissionsRejectsSymlink(t *testing.T) {
	tmp := t.TempDir()
	worktreePath := filepath.Join(tmp, "demo-wt")
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	target := filepath.Join(tmp, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("chmod target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(worktreePath, ".beads")); err != nil {
		t.Fatalf("symlink .beads: %v", err)
	}

	suppressRepairOutput(t)
	repairWorktreeBeadsPermissions(worktreePath)

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("symlink target permissions = %04o, want unchanged 0755", got)
	}
}

func TestRepairWorktreeBeadsPermissionsRejectsNonDirectory(t *testing.T) {
	tmp := t.TempDir()
	worktreePath := filepath.Join(tmp, "demo-wt")
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	beadsPath := filepath.Join(worktreePath, ".beads")
	if err := os.WriteFile(beadsPath, []byte("not a directory"), 0o755); err != nil {
		t.Fatalf("write .beads: %v", err)
	}
	if err := os.Chmod(beadsPath, 0o755); err != nil {
		t.Fatalf("chmod .beads: %v", err)
	}

	suppressRepairOutput(t)
	repairWorktreeBeadsPermissions(worktreePath)

	info, err := os.Stat(beadsPath)
	if err != nil {
		t.Fatalf("stat .beads: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("non-directory .beads permissions = %04o, want unchanged 0755", got)
	}
}

func suppressRepairOutput(t *testing.T) {
	t.Helper()
	saveJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = saveJSON })
}
