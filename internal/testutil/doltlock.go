package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/lockfile"
)

// HoldDoltDatabaseLock takes the lock a dolt process holds on the Dolt
// database in dir for as long as it has the database open, an exclusive lock
// on dir/.dolt/noms/LOCK, and keeps it until the test ends, as a running dolt
// sql-server that serves the database does. A dolt CLI command run in dir
// while the lock is held hands itself to the server the database's
// sql-server.info names instead of working on the files, so a test runs such
// commands before it takes the lock.
func HoldDoltDatabaseLock(t testing.TB, dir string) {
	t.Helper()
	noms := filepath.Join(dir, ".dolt", "noms")
	if err := os.MkdirAll(noms, 0o750); err != nil {
		t.Fatalf("create %s: %v", noms, err)
	}
	f, err := os.OpenFile(filepath.Join(noms, "LOCK"), os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: the test's own database directory
	if err != nil {
		t.Fatalf("open the database lock in %s: %v", noms, err)
	}
	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		t.Fatalf("take the database lock in %s: %v", noms, err)
	}
	t.Cleanup(func() {
		_ = lockfile.FlockUnlock(f)
		_ = f.Close()
	})
}
