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
