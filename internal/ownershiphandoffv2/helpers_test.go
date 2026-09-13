package ownershiphandoffv2

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/lockfile"
)

// canonicalTempDir is t.TempDir() with symlinks resolved. Every path this
// package compares — the strict launch's nonce-bound config, the journal's
// root, the data-dir binding — goes through EvalSymlinks in production, and on
// macOS t.TempDir() hands back /var/... for a directory whose real path is
// /private/var/.... A test that skips this compares two spellings of the same
// directory and fails for a reason that has nothing to do with the handoff.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(t.TempDir()): %v", err)
	}
	return dir
}

// holdLock takes the same advisory lock Dolt holds on a noms LOCK file and
// returns a function that releases it. It models a server that has not let go
// of the workspace's storage.
func holdLock(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		t.Fatalf("lock %s: %v", path, err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = lockfile.FlockUnlock(f)
			_ = f.Close()
		}
	})
	return func() {
		if released {
			return
		}
		released = true
		_ = lockfile.FlockUnlock(f)
		_ = f.Close()
	}
}
