package util

import (
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/stretchr/testify/require"
)

func TestRetirableLockRetire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "life.lock")
	lease, err := TryRetirableLock(path)
	require.NoError(t, err)

	require.NoError(t, lease.Retire())
	require.NoError(t, lease.Retire(), "retiring twice is a no-op")
	lease.Unlock()
	require.NoFileExists(t, path)

	fresh, err := TryRetirableLock(path)
	require.NoError(t, err, "a retired pathname is free for a fresh lease")
	fresh.Unlock()
	require.FileExists(t, path, "Unlock alone never removes the file")
}

// TestRetirableLockKeepsLockViolationForOthers pins that delete sharing does
// not turn contention into an open-time sharing violation: every holder and
// contender combination still reports lockfile.IsLocked.
func TestRetirableLockKeepsLockViolationForOthers(t *testing.T) {
	constructors := map[string]func(string) (*Lock, error){
		"TryLock":          TryLock,
		"TryRetirableLock": TryRetirableLock,
	}
	for holderName, holderLock := range constructors {
		for contenderName, contenderLock := range constructors {
			t.Run(holderName+"_held_"+contenderName+"_competes", func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "life.lock")
				held, err := holderLock(path)
				require.NoError(t, err)
				defer held.Unlock()

				contender, err := contenderLock(path)
				if err == nil {
					contender.Unlock()
					t.Fatal("contender acquired a held lease")
				}
				require.True(t, lockfile.IsLocked(err), "contention must satisfy lockfile.IsLocked, got %v", err)
			})
		}
	}
}
