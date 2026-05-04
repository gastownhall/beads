package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

type execOnlyDB struct {
	exec func(query string) error
}

func (db execOnlyDB) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	if db.exec == nil {
		return nil, nil
	}
	return nil, db.exec(query)
}

func (execOnlyDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("QueryContext should not be called")
}

func (execOnlyDB) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("QueryRowContext should not be called")
}

func TestMigration0032ToleratesMissingAppliedAtColumn(t *testing.T) {
	var recorded bool
	db := execOnlyDB{
		exec: func(query string) error {
			switch {
			case strings.Contains(query, "DROP COLUMN applied_at"):
				return fmt.Errorf(`Error 1105 (HY000): table "schema_migrations" does not have column "applied_at"`)
			case strings.Contains(query, "INSERT IGNORE INTO schema_migrations"):
				recorded = true
			}
			return nil
		},
	}

	applied, err := runMigrations(context.Background(), db, 31, false)
	if err != nil {
		t.Fatalf("runMigrations returned error for already-missing applied_at: %v", err)
	}
	// runMigrations returns len(pending), which is every embedded migration
	// with version > 31. Derive the expected count from LatestVersion() so
	// adding new migrations doesn't break this test (it's about 0032's
	// tolerance, not the total migration count).
	wantApplied := LatestVersion() - 31
	if applied != wantApplied {
		t.Fatalf("applied migrations = %d, want %d", applied, wantApplied)
	}
	if !recorded {
		t.Fatal("migration 0032 was not recorded after already-missing applied_at")
	}
}

func TestAllMigrationsSQLBootstrapsSchemaMigrationsBeforeDrop(t *testing.T) {
	sql := AllMigrationsSQL()

	bootstrap := strings.Index(sql, "CREATE TABLE IF NOT EXISTS schema_migrations")
	if bootstrap < 0 {
		t.Fatal("AllMigrationsSQL missing schema_migrations bootstrap")
	}

	dropAppliedAt := strings.Index(sql, "ALTER TABLE schema_migrations DROP COLUMN applied_at")
	if dropAppliedAt < 0 {
		t.Fatal("AllMigrationsSQL missing migration 0032 applied_at drop")
	}

	if bootstrap > dropAppliedAt {
		t.Fatal("schema_migrations bootstrap appears after migration 0032 applied_at drop")
	}
}
