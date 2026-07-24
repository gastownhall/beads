package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// migrationPassCompleteKey is the local_metadata row recording that a COMPLETE
// MigrateUp pass — numbered migrations, backfills, dependency/aux rekeys,
// ignored source, final staging commit — has finished against this database,
// and the "main/ignored" latest versions the completing binary knew.
//
// It exists for MigrateUpWithLock's lock-free fast path. The probe's other
// inputs (seed patterns, cursors, content-hash columns, custom backfill) are
// all satisfied MID-PASS by a running migration's per-step commits — before
// its dependency rekey and final staging commit have run — so on a shared
// sql-server a concurrent prober could otherwise pass while another process's
// pass tail is still rewriting rows. The sentinel closes that window: migrateUp
// deletes it before the first pass mutation and MigrateUp restamps it only
// after a fully successful return, so "sentinel current" is unsatisfiable
// while any pass is in flight (or died mid-flight).
//
// local_metadata is dolt-ignored: database-local on a shared server (exactly
// the visibility scope the probe reads), never committed or synced. A fresh
// clone or a pre-sentinel database simply lacks the row and pays one locked
// no-op pass to earn it.
const migrationPassCompleteKey = "migration_pass_complete"

// migrationPassCompleteCurrent reports whether a complete migration pass has
// finished at or past this binary's latest main and ignored versions. Strictly
// read-only. A missing local_metadata table, absent row, or unparseable value
// all report false (not an error): the caller falls through to the locked
// pass, which re-proves currency and restamps.
func migrationPassCompleteCurrent(ctx context.Context, db DBConn) (bool, error) {
	var value string
	err := db.QueryRowContext(ctx,
		"SELECT value FROM local_metadata WHERE `key` = ?",
		migrationPassCompleteKey).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || dberrors.IsTableNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading migration pass sentinel: %w", err)
	}
	var mainV, ignoredV int
	if n, err := fmt.Sscanf(value, "%d/%d", &mainV, &ignoredV); err != nil || n != 2 {
		return false, nil
	}
	return mainV >= LatestVersion() && ignoredV >= LatestIgnoredVersion(), nil
}

// clearMigrationPassComplete revokes the fast path before a migration pass's
// first mutation. It deliberately does NOT create local_metadata: the pass's
// dirty-table guards grep pending migration SQL against the pre-pass dirty
// set, and materializing the (dolt-ignored, hence perpetually untracked) table
// before those guards run would surface it to them on databases that never had
// it (main 0029 / ignored 0001 both reference it).
func clearMigrationPassComplete(ctx context.Context, db DBConn) error {
	if _, err := db.ExecContext(ctx,
		"DELETE FROM local_metadata WHERE `key` = ?",
		migrationPassCompleteKey); err != nil && !dberrors.IsTableNotExist(err) {
		return fmt.Errorf("clearing migration pass sentinel: %w", err)
	}
	return nil
}

// ensureMigrationPassComplete stamps the sentinel with this binary's latest
// versions unless an equal-or-newer stamp is already present (a newer binary's
// completed pass must not be downgraded — its stamp already satisfies this
// binary's >= probe). Called only after a fully successful MigrateUp, under
// the migration lock in server mode.
func ensureMigrationPassComplete(ctx context.Context, db DBConn) error {
	current, err := migrationPassCompleteCurrent(ctx, db)
	if err != nil || current {
		return err
	}
	if err := ensureLocalMetadataTable(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx,
		"REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)",
		migrationPassCompleteKey,
		fmt.Sprintf("%d/%d", LatestVersion(), LatestIgnoredVersion())); err != nil {
		return fmt.Errorf("recording migration pass sentinel: %w", err)
	}
	return nil
}

// ensureLocalMetadataTable creates the clone-local metadata table on demand
// with 0029's DDL — its dolt_ignore pattern is committed history, so the rows
// stay clone-local. Shared by the pass sentinel and the aux-rekey crash
// sentinel.
func ensureLocalMetadataTable(ctx context.Context, db DBConn) error {
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS local_metadata (`key` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL DEFAULT '')"); err != nil {
		return fmt.Errorf("ensuring local_metadata: %w", err)
	}
	return nil
}
