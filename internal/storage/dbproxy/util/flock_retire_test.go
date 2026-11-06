package util

import (
	"os"
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

func TestRetireRefusesOrdinaryLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "life.lock")
	lease, err := TryLock(path)
	require.NoError(t, err)
	defer lease.Unlock()

	require.Error(t, lease.Retire(), "an ordinary lease is not retirable")
	require.FileExists(t, path)
}

func TestRetireToleratesMissingPathAndRefusesReleasedLease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "life.lock")
	lease, err := TryRetirableLock(path)
	require.NoError(t, err)
	moved := filepath.Join(dir, "moved.lock")
	require.NoError(t, os.Rename(path, moved))

	require.NoError(t, lease.Retire(), "a pathname that is already gone counts as retired")
	require.FileExists(t, moved, "retirement deletes by pathname, never the renamed file")
	lease.Unlock()

	released, err := TryRetirableLock(path)
	require.NoError(t, err)
	released.Unlock()
	require.Error(t, released.Retire(), "a released lease cannot be retired")
	require.FileExists(t, path)
}
