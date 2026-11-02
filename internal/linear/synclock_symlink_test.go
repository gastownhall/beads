package linear

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSyncLockRejectsSymlink: a symlink at the primary lock path is refused
// before any truncate or write, so the lock can neither overwrite nor create
// the link's target (bd-1tmym4 / #7888).
func TestSyncLockRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}

	t.Run("live link leaves target unchanged", func(t *testing.T) {
		dir := t.TempDir()
		victim := filepath.Join(dir, "victim.lock")
		if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(dir, syncLockFilename)
		if err := os.Symlink(victim, lockPath); err != nil {
			t.Fatal(err)
		}

		lock, err := AcquireSyncLock(dir, false)
		if err == nil {
			_ = lock.Release()
			t.Fatal("AcquireSyncLock accepted a symlinked lock path")
		}
		if !strings.Contains(err.Error(), lockPath) {
			t.Errorf("refusal does not name the lock path %s: %v", lockPath, err)
		}
		if got, readErr := os.ReadFile(victim); readErr != nil || string(got) != "original\n" {
			t.Errorf("lock acquisition wrote through the symlink: %q (%v)", got, readErr)
		}
	})

	t.Run("dangling link is not materialized", func(t *testing.T) {
		dir := t.TempDir()
		lockPath := filepath.Join(dir, syncLockFilename)
		target := filepath.Join(dir, "no-such-target")
		if err := os.Symlink(target, lockPath); err != nil {
			t.Fatal(err)
		}

		lock, err := AcquireSyncLock(dir, false)
		if err == nil {
			_ = lock.Release()
			t.Fatal("AcquireSyncLock accepted a symlinked lock path")
		}
		if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
			t.Errorf("acquisition materialized the dangling link's target: %v", statErr)
		}
		if fi, lstatErr := os.Lstat(lockPath); lstatErr != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("dangling lock link was replaced: %v (%v)", fi, lstatErr)
		}
	})
}