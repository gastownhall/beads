//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigrate0053AddsMissingWispsColumns_6080 pins
// gastownhall/beads#6080 on the engine the reporter ran (embeddeddolt).
// Migrations 0023 and 0027 add wisps.no_history and wisps.started_at only
// when the clone-local wisps table already exists when they run, and wisps
// never syncs between clones, so a v32-lineage store that built wisps later
// from an older shape reaches ledger v52 without them. Migration 0053's
// frozen INSERT ... SELECT names both columns, so the upgrade died at
// PREPARE with Error 1105 ("column \"started_at\" could not be found in any
// table in scope") after the ledger had already advanced 32 -> 52, leaving
// the store readable but unwritable with both documented recovery paths
// dead-ended.
//
// The fix (already merged as #8389 when this regression was written) is the
// pre-0053 repair ensureWispsLateColumns: probe the table, probe each late
// column independently, and add exactly what is missing with its original
// 0023/0027 definition. Every supported drift shape runs against the real
// migration runner on a store whose main schema is genuinely at v52 — not a
// lowered cursor — modeling the drift by dropping the columns the drifted
// clones never got.
func TestEmbeddedMigrate0053AddsMissingWispsColumns_6080(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	cases := []struct {
		name string
		drop []string
	}{
		// The reported shape: no_history present, started_at absent.
		{name: "started_at_only", drop: []string{"started_at"}},
		// The mirror shape: a live started_at value, no_history absent.
		{name: "no_history_only", drop: []string{"no_history"}},
		// A wisps table predating both late columns.
		{name: "both_missing", drop: []string{"no_history", "started_at"}},
		// The same drift with no wisp rows at all: 0053 failed at PREPARE
		// even with nothing to promote, so convergence itself is the fix.
		{name: "started_at_only_empty_wisps", drop: []string{"started_at"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := seedMainSchemaAt(t, ctx, 52)

			setup, closeSetup := openPinnedConn(t, ctx, dataDir)
			// The freshly migrated shape is the reference the repaired
			// columns must come back with: their original 0023/0027
			// definitions, not a lookalike, captured before the drift is
			// modeled.
			freshShape := map[string]string{}
			for _, col := range tc.drop {
				freshShape[col] = wispsColumnShape6080(t, ctx, setup, col)
			}
			for _, col := range tc.drop {
				if _, err := setup.ExecContext(ctx, "ALTER TABLE wisps DROP COLUMN "+col); err != nil {
					t.Fatalf("model drift: drop wisps.%s: %v", col, err)
				}
			}
			closeSetup()

			// The reporter's recovery command on a fresh pinned session:
			// this is the MigrateUp that stranded their store at v52.
			conn, closeConn := openPinnedConn(t, ctx, dataDir)
			defer closeConn()
			if _, err := schema.MigrateUp(ctx, conn); err != nil {
				t.Fatalf("MigrateUp from v52 with wisps lacking %v: %v", tc.drop, err)
			}
			if v := currentMainVersion(t, ctx, conn); v != schema.LatestVersion() {
				t.Fatalf("main schema version = %d, want latest %d", v, schema.LatestVersion())
			}
			for _, col := range tc.drop {
				if got, want := wispsColumnShape6080(t, ctx, conn, col), freshShape[col]; got != want {
					t.Errorf("wisps.%s shape after repair = %q, want the fresh-store %q", col, got, want)
				}
			}

			// Normal re-entry: a second pass finds nothing to apply.
			applied, err := schema.MigrateUp(ctx, conn)
			if err != nil {
				t.Fatalf("re-run MigrateUp: %v", err)
			}
			if applied != 0 {
				t.Errorf("re-run MigrateUp applied = %d, want 0", applied)
			}
		})
	}
}

// wispsColumnShape6080 describes wisps.col as the freshly migrated chain
// defines it, so a repaired column can be compared against its original
// definition without the test restating the DDL.
func wispsColumnShape6080(t *testing.T, ctx context.Context, conn *sql.Conn, col string) string {
	t.Helper()
	var colType, nullable, def string
	if err := conn.QueryRowContext(ctx, `
		SELECT COLUMN_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, '<NULL>')
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = ?
	`, col).Scan(&colType, &nullable, &def); err != nil {
		t.Fatalf("read wisps.%s shape: %v", col, err)
	}
	return fmt.Sprintf("type=%s nullable=%s default=%s", colType, nullable, def)
}
