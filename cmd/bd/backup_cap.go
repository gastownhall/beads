package main

import (
	"fmt"
	"os"
	"path/filepath"
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
// backup dir from a 1.7GB store). formatBytes is shared with
// runCompactDolt (compact.go); getDirSize is defined below.
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

// walkBackupDestination is getDirSize, indirected so tests can inject the
// mid-walk ENOENT that a real filesystem produces only under a race.
var walkBackupDestination = getDirSize

// backupDestinationSize measures dir for the status readers. Only a
// destination that does not exist yet counts as empty; every walk error is
// returned, ENOENT included. getDirSize aborts on the first per-entry
// error, so a chunk file removed between listing its directory and
// stat-ing it would otherwise read as 0 bytes: a false all-clear on a
// destination that may be well over its cap.
func backupDestinationSize(dir string) (int64, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return 0, nil
	}
	return walkBackupDestination(dir)
}

// showSizeCapStatus prints size-cap info as part of `bd backup status`
// (ga-y6gjv PR #6071 review: status previously said nothing about the cap,
// so an agent/CI caller watching a paused destination saw only a
// reassuring "Last backup" line).
func showSizeCapStatus(dir string) {
	capMB := effectiveSizeCapMB()
	if capMB == 0 {
		// Echo the configured value rather than a literal 0: an
		// unparseable one ("2048MB") also disables the cap (see
		// effectiveSizeCapMB), and this line is where an operator
		// debugging that looks.
		fmt.Printf("  Size cap: disabled (backup.size-cap-mb=%s)\n", config.GetString("backup.size-cap-mb"))
		return
	}
	size, err := backupDestinationSize(dir)
	if err != nil {
		// Say so rather than dropping the line: a silent omission
		// looks identical to "no cap configured" to an operator
		// debugging a permission problem on the destination, and
		// --json already reports this case (showSizeCapStatusJSON).
		debug.Logf("backup status: size cap check failed (non-fatal): %v\n", err)
		fmt.Printf("  Size cap: unavailable (%v)\n", err)
		return
	}
	capBytes := int64(capMB) * 1024 * 1024
	if size >= capBytes {
		// Same two levers as pauseAutoBackupForSizeCap's warning — see
		// there for why `bd backup init` is not one of them.
		fmt.Printf("  Size cap: PAUSED (cap exceeded) — %s / %s. Raise backup.size-cap-mb "+
			"or point backup.git-repo at a different git repository to switch destinations.\n",
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
	size, err := backupDestinationSize(dir)
	if err != nil {
		// exceeded is explicitly null, not omitted: a consumer that
		// reads a missing key as false would see "not exceeded"
		// during exactly the failure that makes it unmeasurable.
		return map[string]interface{}{
			"enabled":  true,
			"cap_mb":   capMB,
			"error":    err.Error(),
			"exceeded": nil,
		}
	}
	capBytes := int64(capMB) * 1024 * 1024
	return map[string]interface{}{
		"enabled":       true,
		"cap_mb":        capMB,
		"current_bytes": size,
		"exceeded":      size >= capBytes,
	}
}

// warnBackupSizeCapUnavailable reports, on stderr, that the cap could not
// be measured and the backup is proceeding uncapped.
//
// getDirSize returns the first per-file error straight out of its
// filepath.Walk callback, so one unreadable entry — or a chunk file removed
// mid-walk — aborts the whole measurement. maybeAutoBackup deliberately
// fails OPEN there (blocking backups outright on an unreadable destination
// would be worse), but a debug-only line made that silent: the cap can
// disable itself and restore the unbounded growth this feature exists to
// prevent with nothing on the operator's terminal, while `bd backup status
// --json` reports the very same error (ga-y6gjv PR #6071 review).
//
// It is throttled by the backup interval, not by the PAUSED warning's
// LastCapWarnAt slot — spending that slot here would let a transient walk
// error suppress the more important "auto-backup PAUSED" notice for a full
// warn interval. maybeAutoBackup reaches the walk only after the interval
// throttle and change detection have both passed, and re-arms
// state.Timestamp itself after this warning rather than relying on the
// fall-through into runBackupExport: not every runBackupExport exit
// persists it (a failed post-sync GetCurrentCommit returns without saving
// state), and the next command would then walk and warn again. That bounds
// this warning to once per backup.interval whenever backup_state.json can
// be written.
func warnBackupSizeCapUnavailable(err error) {
	if !isQuiet() && !jsonOutput {
		fmt.Fprintf(os.Stderr,
			"Warning: backup size cap could not be measured (%v); "+
				"proceeding without the cap. Auto-backup is unbounded until this clears.\n", err)
	}
	debug.Logf("backup: size cap check failed (non-fatal): %v\n", err)
}

// pauseAutoBackupForSizeCap records the skip that happens when the
// destination is over backup.size-cap-mb, and announces it to stderr at
// most once per backup.size-warn-interval (default 24h).
//
// It re-arms the interval throttle (state.Timestamp) even though no backup
// ran, leaving LastDoltCommit untouched exactly as runBackupExport's own
// failure path does (backup_export.go, wy-zrmqr). Without that, the paused
// state is self-perpetuating and pathological: Timestamp is only ever
// advanced by a backup attempt, so a destination that is over cap — by
// definition the largest one — would pay the full getDirSize walk on every
// single bd command, forever, until an operator intervenes (ga-y6gjv
// PR #6071 review). LastDoltCommit is deliberately left alone so change
// detection still sees the pending work once the cap is raised.
//
// The warn timestamp, by contrast, is recorded only when the message was
// actually printed. --json/--quiet callers — i.e. every agent-driven
// command — would otherwise consume the 24h slot silently and the
// human-visible warning could effectively never fire; those callers have
// `bd backup status --json`'s size_cap field instead.
//
// The advice names only the levers that end the pause: raising the cap, or
// pointing backup.git-repo — the only setting backupDir() reads — at another
// repository, whose backup/ directory carries none of this one's size or
// throttle state. `bd backup init` registers the separate destination that
// manual `bd backup sync` pushes to; auto-backup still targets this
// directory afterwards, so it would stay paused (PR #6071 post-merge
// review).
//
// Never returns an error: a failure to persist must not block the
// (already-decided) skip of the backup attempt itself.
func pauseAutoBackupForSizeCap(dir string, state *backupState, size int64) {
	warnInterval := config.GetDuration("backup.size-warn-interval")
	if warnInterval == 0 {
		warnInterval = 24 * time.Hour
	}

	state.Timestamp = time.Now().UTC()

	lastWarn := state.LastCapWarnAt
	throttled := !lastWarn.IsZero() && time.Since(lastWarn) < warnInterval
	announce := !throttled && !isQuiet() && !jsonOutput
	if announce {
		state.LastCapWarnAt = time.Now().UTC()
	}

	if err := saveBackupState(dir, state); err != nil {
		debug.Logf("backup: failed to persist size-cap skip state: %v\n", err)
	}

	if announce {
		fmt.Fprintf(os.Stderr,
			"Warning: auto-backup PAUSED — destination %s has reached %s. "+
				"No further syncs will run until you raise backup.size-cap-mb "+
				"or point backup.git-repo at a different git repository "+
				"(auto-backup then syncs to a backup/ directory inside it).\n",
			dir, formatBytes(size))
		debug.Logf("backup: size cap exceeded (%s), auto-backup paused\n", formatBytes(size))
		return
	}
	debug.Logf("backup: size cap exceeded (%s), auto-backup paused (warning throttled)\n", formatBytes(size))
}

// getDirSize calculates the total size of a directory recursively.
func getDirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}
