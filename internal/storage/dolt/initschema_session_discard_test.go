package dolt

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// stubMigrateUpWithLock replaces the migration pass for one test.
func stubMigrateUpWithLock(t *testing.T, fn func(ctx context.Context, conn *sql.Conn, database string, opts ...schema.MigrateLockOption) (int, error)) {
	t.Helper()
	prev := migrateUpWithLock
	migrateUpWithLock = fn
	t.Cleanup(func() { migrateUpWithLock = prev })
}

// TestInitSchemaOnDBDiscardsSessionOnFailure is the DoltStore sibling of
// uow's gastownhall/beads#6504 regression: initSchemaOnDBWithBootstrapHeal
// pins a session and runs under initSchemaOnDBWithRetryAndGateBootstrapHeal's
// retry on the same pool. A failed attempt may leave that session mid-script
// (FOREIGN_KEY_CHECKS cleared) or catalog-pinned by its failing statement
// (be-bv7x), so it must be discarded, not pooled for the next attempt — while
// the attempt still reports its applied count and its wrapped error. A
// successful attempt keeps its session.
func TestInitSchemaOnDBDiscardsSessionOnFailure(t *testing.T) {
	lockWait := &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}
	sentinel := errors.New("migration 0041 failed between its SETs")

	for _, tt := range []struct {
		name        string
		script      func(t *testing.T, mock sqlmock.Sqlmock)
		wantErr     error
		wantPrefix  string
		wantApplied int
		wantOpen    int
	}{
		{
			name: "database name read fails",
			script: func(t *testing.T, mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).WillReturnError(lockWait)
			},
			wantErr:    lockWait,
			wantPrefix: "schema: read database name:",
		},
		{
			name: "migration fails after the lock is taken",
			script: func(t *testing.T, mock sqlmock.Sqlmock) {
				lockName := schema.MigrationLockName("beads")
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("beads"))
				// The convergence probe: on the target, but the cursor is
				// behind, so the locked path runs.
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("beads"))
				expectCursorTable(mock)
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")).
					WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
					WithArgs(lockName, 5).
					WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
				// MigrateUp's first statement (the dolt_ignore seed) fails; the lock
				// is still released.
				mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
					WithArgs(sqlmock.AnyArg()).
					WillReturnError(lockWait)
				mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
					WithArgs(lockName).
					WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
			},
			wantErr:    lockWait,
			wantPrefix: "schema migration:",
		},
		{
			name: "migration fails part-way through the pass",
			script: func(t *testing.T, mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("beads"))
				stubMigrateUpWithLock(t, func(context.Context, *sql.Conn, string, ...schema.MigrateLockOption) (int, error) {
					return 3, sentinel
				})
			},
			wantErr:     sentinel,
			wantPrefix:  "schema migration:",
			wantApplied: 3,
		},
		{
			name: "successful pass keeps its session",
			script: func(t *testing.T, mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("beads"))
				stubMigrateUpWithLock(t, func(context.Context, *sql.Conn, string, ...schema.MigrateLockOption) (int, error) {
					return 5, nil
				})
			},
			wantApplied: 5,
			wantOpen:    1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			tt.script(t, mock)

			applied, err := initSchemaOnDBWithBootstrapHeal(context.Background(), db, nil, "")
			if applied != tt.wantApplied {
				t.Errorf("applied = %d, want %d", applied, tt.wantApplied)
			}
			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("initSchemaOnDBWithBootstrapHeal() error = %v, want nil", err)
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Fatalf("initSchemaOnDBWithBootstrapHeal() error = %v, want it to wrap %v", err, tt.wantErr)
			case tt.wantErr != nil && !strings.HasPrefix(err.Error(), tt.wantPrefix):
				t.Fatalf("initSchemaOnDBWithBootstrapHeal() error = %q, want prefix %q", err, tt.wantPrefix)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("attempt SQL expectations: %v", err)
			}
			if stats := db.Stats(); stats.OpenConnections != tt.wantOpen || stats.Idle != tt.wantOpen {
				t.Fatalf("after the attempt the pool holds %d open / %d idle sessions, want %d / %d",
					stats.OpenConnections, stats.Idle, tt.wantOpen, tt.wantOpen)
			}
		})
	}
}

// expectCursorTable mocks migrationSource.currentVersion's existence probe
// for the schema_migrations cursor table, which exists.
func expectCursorTable(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("^SHOW TABLES$").
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow("issues").AddRow("schema_migrations"))
}
