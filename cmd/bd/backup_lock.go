package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/lockfile"
)

// The backup lock serializes the two writers of a workspace's Dolt backup:
// post-command auto-backup and an explicit `bd backup sync`. Both run under the
// command's SHARED workspace gate, so the gate alone lets them overlap, and the
// auto-backup body does remove → add → sync on one fixed remote name — two
// concurrent runs race each other's registration and full-sync the database
// twice. Restore needs no part in it: it holds the workspace gate EXCLUSIVELY,
// which already excludes every command that could take this lock.
//
// The two callers wait differently, and deliberately:
//   - auto-backup does not wait. Another backup running means one is already
//     happening; it skips, and the throttle picks it up on a later command.
//   - `bd backup sync` waits up to backupLockWait — the same bound an exclusive
//     workspace-gate acquisition (restore) uses — and then fails with an error
//     naming the busy lock, so an operator is never told it synced when it
//     did not.
const backupLockFileName = "backup.lock"

// backupLockWait bounds how long `bd backup sync` waits for another backup.
// A var so tests can shorten it.
var backupLockWait = exclusiveGateWait

// errBackupBusy is returned when another backup holds the lock past the wait.
var errBackupBusy = errors.New("another backup is running for this workspace")

const backupLockPoll = 50 * time.Millisecond

// acquireBackupLock takes the workspace's backup lock, polling up to wait
// (zero means one non-blocking attempt). The returned release must be called.
func acquireBackupLock(wait time.Duration) (release func(), err error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return nil, fmt.Errorf("backup lock: %s", activeWorkspaceNotFoundError())
	}
	path := filepath.Join(beadsDir, backupLockFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path is the workspace's own lock file
	if err != nil {
		return nil, fmt.Errorf("backup lock: open %s: %w", path, err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := lockfile.FlockExclusiveNonBlocking(f)
		if err == nil {
			return func() {
				_ = lockfile.FlockUnlock(f)
				_ = f.Close()
			}, nil
		}
		if !lockfile.IsLocked(err) {
			_ = f.Close()
			return nil, fmt.Errorf("backup lock: %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, errBackupBusy
		}
		time.Sleep(backupLockPoll)
	}
}
