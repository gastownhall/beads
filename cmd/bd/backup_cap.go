package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
)

// effectiveSizeCapMB returns the configured backup.size-cap-mb, or 0 if the
// cap is disabled. internal/config/config.go registers a viper default of
// 2048 for this key, so GetInt only returns <= 0 when the operator has
// explicitly set 0 (or an unparseable value, which viper's GetInt also
// resolves to 0) — per the ga-y6gjv PR #6071 review, that means "no cap",
// not "use the 2048 default instead" (an operator with a legitimately
// larger destination needs an off switch).
func effectiveSizeCapMB() int {
	capMB := config.GetInt("backup.size-cap-mb")
	if capMB <= 0 {
		return 0
	}
	return capMB
}

// backupSizeCapExceeded reports whether dir's on-disk size has crossed
// backup.size-cap-mb (default 2048MB / 2GB; 0 disables the cap).
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
	capMB := effectiveSizeCapMB()
	if capMB == 0 {
		return false, 0, nil
	}
	size, err = getDirSize(dir)
	if err != nil {
		return false, 0, err
	}
	return size >= int64(capMB)*1024*1024, size, nil
}

// showSizeCapStatus prints size-cap info as part of `bd backup status`
// (ga-y6gjv PR #6071 review: status previously said nothing about the cap,
// so an agent/CI caller watching a paused destination saw only a
// reassuring "Last backup" line).
func showSizeCapStatus(dir string) {
	capMB := effectiveSizeCapMB()
	if capMB == 0 {
		fmt.Println("  Size cap: disabled (backup.size-cap-mb=0)")
		return
	}
	size, err := getDirSize(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			debug.Logf("backup status: size cap check failed (non-fatal): %v\n", err)
			return
		}
		size = 0
	}
	capBytes := int64(capMB) * 1024 * 1024
	if size >= capBytes {
		fmt.Printf("  Size cap: PAUSED (cap exceeded) — %s / %s. Raise backup.size-cap-mb "+
			"or run `bd backup init <new-path>` to switch destinations.\n",
			formatBytes(size), formatBytes(capBytes))
		return
	}
	fmt.Printf("  Size cap: %s / %s\n", formatBytes(size), formatBytes(capBytes))
}

// showSizeCapStatusJSON returns size-cap info for `bd backup status --json`
// — see showSizeCapStatus.
func showSizeCapStatusJSON(dir string) map[string]interface{} {
	capMB := effectiveSizeCapMB()
	if capMB == 0 {
		return map[string]interface{}{"enabled": false}
	}
	size, err := getDirSize(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return map[string]interface{}{"enabled": true, "cap_mb": capMB, "error": err.Error()}
		}
		size = 0
	}
	capBytes := int64(capMB) * 1024 * 1024
	return map[string]interface{}{
		"enabled":       true,
		"cap_mb":        capMB,
		"current_bytes": size,
		"exceeded":      size >= capBytes,
	}
}

// capWarnFallback caches, per backup directory, the in-process time of the
// last size-cap warning. It exists for the case maybeWarnBackupSizeCap
// itself creates: the destination directory is full, so persisting
// backup_state.json into it also fails. A short-lived bd CLI invocation
// doesn't need this (the process exits either way), but a long-lived bd
// process that keeps calling maybeAutoBackup for the same destination
// (e.g. internal/storage/dbproxy's server mode) would otherwise re-warn on
// every single call instead of respecting backup.size-warn-interval,
// because the on-disk throttle state can never stick while the write
// that's supposed to record it is itself failing for the same reason as
// the backup. Keyed by dir (not a single scalar) so unrelated destinations
// — and unrelated tests — never share a throttle.
var (
	capWarnFallbackMu sync.Mutex
	capWarnFallback   = map[string]time.Time{}
)

func capWarnFallbackFor(dir string) time.Time {
	capWarnFallbackMu.Lock()
	defer capWarnFallbackMu.Unlock()
	return capWarnFallback[dir]
}

func recordCapWarnFallback(dir string, at time.Time) {
	capWarnFallbackMu.Lock()
	defer capWarnFallbackMu.Unlock()
	capWarnFallback[dir] = at
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

	lastWarn := state.LastCapWarnAt
	if fb := capWarnFallbackFor(dir); fb.After(lastWarn) {
		lastWarn = fb
	}
	if !lastWarn.IsZero() && time.Since(lastWarn) < warnInterval {
		debug.Logf("backup: size cap exceeded (%s), auto-backup paused (warning throttled)\n", formatBytes(size))
		return
	}

	now := time.Now().UTC()
	state.LastCapWarnAt = now
	recordCapWarnFallback(dir, now)
	if err := saveBackupState(dir, state); err != nil {
		debug.Logf("backup: failed to persist size-cap warning state: %v\n", err)
	}
	if !isQuiet() && !jsonOutput {
		fmt.Fprintf(os.Stderr,
			"Warning: auto-backup PAUSED — destination %s has reached %s. "+
				"No further syncs will run until you raise backup.size-cap-mb "+
				"or switch to a fresh destination with `bd backup init <new-path>`.\n",
			dir, formatBytes(size))
	}
	debug.Logf("backup: size cap exceeded (%s), auto-backup paused\n", formatBytes(size))
}
