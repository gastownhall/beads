package main

import (
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
)

// backupSizeCapExceeded reports whether dir's on-disk size has crossed
// backup.size-cap-mb (default 2048MB / 2GB).
//
// A hard cap exists because BackupSync/CALL DOLT_BACKUP('sync', ...) only
// ever transfers new chunks into dir — it never prunes ones that became
// unreachable on the source DB (history rewrites, superseded data) — and
// Dolt exposes no supported way to GC a backup destination in place: it is
// a bare chunk-store directory with no .dolt repo-root marker (confirmed
// against dolt_backup.go's SQL procedures — add/sync/restore only, no gc;
// and empirically, `dolt gc` run with its working directory set to a real
// backup destination fails "not a valid dolt repository" because there is
// no .dolt for the CLI to find). Absent a cap, the destination can only
// grow forever until disk fills (ga-y6gjv, the 2026-06-19 outage: 43GB
// backup dir from a 1.7GB store). getDirSize/formatBytes are shared with
// runCompactDolt (compact.go).
func backupSizeCapExceeded(dir string) (exceeded bool, size int64, err error) {
	capMB := config.GetInt("backup.size-cap-mb")
	if capMB <= 0 {
		capMB = 2048
	}
	size, err = getDirSize(dir)
	if err != nil {
		return false, 0, err
	}
	return size >= int64(capMB)*1024*1024, size, nil
}

// maybeWarnBackupSizeCap announces (to stderr, throttled) that auto-backup
// is paused because the destination exceeds its size cap, and persists the
// throttle so the warning isn't repeated on every subsequent bd command —
// only once per backup.size-warn-interval (default 24h). Mirrors the
// "persist the attempt time even on a skip" pattern already used for the
// backup interval throttle itself (runBackupExport's failure path,
// wy-zrmqr) so a caller that's paused for days doesn't get spammed.
//
// Never returns an error: a failure to persist the warning state must not
// block the (already-decided) skip of the backup attempt itself.
func maybeWarnBackupSizeCap(dir string, state *backupState, size int64) {
	warnInterval := config.GetDuration("backup.size-warn-interval")
	if warnInterval == 0 {
		warnInterval = 24 * time.Hour
	}
	if !state.LastCapWarnAt.IsZero() && time.Since(state.LastCapWarnAt) < warnInterval {
		debug.Logf("backup: size cap exceeded (%s), auto-backup paused (warning throttled)\n", formatBytes(size))
		return
	}
	state.LastCapWarnAt = time.Now().UTC()
	if err := saveBackupState(dir, state); err != nil {
		debug.Logf("backup: failed to persist size-cap warning state: %v\n", err)
	}
	if !isQuiet() && !jsonOutput {
		fmt.Fprintf(os.Stderr,
			"Warning: auto-backup PAUSED — destination %s has reached %s. "+
				"No further syncs will run until you free space (e.g. delete "+
				"the backup directory and let the next sync recreate it from "+
				"scratch) or raise backup.size-cap-mb.\n", dir, formatBytes(size))
	}
	debug.Logf("backup: size cap exceeded (%s), auto-backup paused\n", formatBytes(size))
}
