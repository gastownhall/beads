package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// expectFreshScriptThroughFirstMigrationStatement scripts
// initSchemaOnDBWithBootstrapHeal on a session whose database exists but has
// no schema at all: the function's own DATABASE() read, the pre-lock
// convergence probe (session already on the target, main cursor table absent
// so there is migration work), the migration lock, and MigrateUp's first
// statement — the dolt_ignore seed INSERT — as the failure point. Returns the
// lock name so callers can script its release.
func expectFreshScriptThroughFirstMigrationStatement(mock sqlmock.Sqlmock, database string, firstErr error) string {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(database))
	// Convergence probe: selectTargetDatabase reads the session database
	// (already on target, no USE), then migrationWorkNeeded finds the main
	// cursor table absent — there is work, so the probe declines.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(database))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("^SHOW TABLES$").
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_beads"}))
	lockName := schema.MigrationLockName(database)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 5).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(firstErr)
	return lockName
}

// assertDiscardedPool asserts a FAILED init's session never reached the pool:
// the discard must have closed the driver connection, and no connection may
// remain open or idle.
func assertDiscardedPool(t *testing.T, db *sql.DB) {
	t.Helper()
	if stats := db.Stats(); stats.Idle != 0 || stats.OpenConnections != 0 {
		t.Fatalf("after a failed init the pool holds %d open / %d idle sessions, want 0: "+
			"the failed init's session went back to the pool the store serves queries from",
			stats.OpenConnections, stats.Idle)
	}
}

// TestInitSchemaOnDBDiscardsSessionOnFailure is the internal-store half of
// gastownhall/beads#6504: initSchemaOnDBWithBootstrapHeal pins one session from
// the very pool the DoltStore serves queries from, and a failed statement can
// leave that session mid-script or pinned to its pre-statement catalog
// snapshot (be-bv7x). A FAILED init must discard the session — not hand it
// back — while preserving its applied count and wrapped error exactly.
func TestInitSchemaOnDBDiscardsSessionOnFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("database name read failure discards the session", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)

		mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
			WillReturnError(errors.New("connection reset before DATABASE()"))

		applied, err := initSchemaOnDB(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "schema: read database name") {
			t.Fatalf("initSchemaOnDB() error = %v, want the wrapped database-name read failure", err)
		}
		if applied != 0 {
			t.Fatalf("initSchemaOnDB() applied = %d, want 0", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("init SQL expectations: %v", err)
		}
		assertDiscardedPool(t, db)
	})

	t.Run("migration failure discards the session and preserves the verdict", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)

		lockName := expectFreshScriptThroughFirstMigrationStatement(mock, "beads",
			errors.New("first migration statement failed"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
			WithArgs(lockName).
			WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
		mock.ExpectClose()

		applied, err := initSchemaOnDB(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "schema migration:") ||
			!strings.Contains(err.Error(), "first migration statement failed") {
			t.Fatalf("initSchemaOnDB() error = %v, want the wrapped migration failure preserved", err)
		}
		if applied != 0 {
			t.Fatalf("initSchemaOnDB() applied = %d, want 0 preserved by the discard", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("init SQL expectations: %v", err)
		}
		assertDiscardedPool(t, db)
	})

	t.Run("lock release failure does not double-close or mask the verdict", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)

		lockName := expectFreshScriptThroughFirstMigrationStatement(mock, "beads",
			&mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"})
		// The release fails; schema's own cleanup discards the session FIRST
		// (schema.discardConn), so the init's deferred Raw/Close answer
		// sql.ErrConnDone and must be ignored. Exactly one driver close may
		// happen.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
			WithArgs(lockName).
			WillReturnError(errors.New("connection reset during release"))
		mock.ExpectClose()

		applied, err := initSchemaOnDB(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "schema migration:") {
			t.Fatalf("initSchemaOnDB() error = %v, want the wrapped migration failure preserved", err)
		}
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1205 {
			t.Fatalf("initSchemaOnDB() error = %v, want the 1205 preserved", err)
		}
		if !errors.Is(err, schema.ErrMigrationLockRelease) {
			t.Fatalf("initSchemaOnDB() error = %v, want the lock-release failure kept reachable", err)
		}
		if applied != 0 {
			t.Fatalf("initSchemaOnDB() applied = %d, want 0", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("init SQL expectations: %v", err)
		}
		assertDiscardedPool(t, db)
	})
}

// TestInitSchemaOnDBKeepsSessionOnSuccess is the preservation half of
// gastownhall/beads#6504: the failed-init discard must never fire on a
// SUCCESSFUL init. On a real server, a single-connection pool must run a full
// fresh migration and still serve the very same session afterward — the
// converged steady state pays for one session per open, and a session that
// survives a migration must carry FOREIGN_KEY_CHECKS at its default.
func TestInitSchemaOnDBKeepsSessionOnSuccess(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dbName := uniqueTestDBName(t)
	initDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	admin, err := sql.Open("mysql", initDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	pool, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName,
	}.String())
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	// One session for the whole store: a successful init must run on it and
	// hand it back, not force a second connection (which the #6504 discard
	// would, by physically closing the first).
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)

	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("pin session before init: %v", err)
	}
	var beforeID int64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&beforeID); err != nil {
		t.Fatalf("read session identity: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("release session: %v", err)
	}

	applied, err := initSchemaOnDB(ctx, pool)
	if err != nil {
		t.Fatalf("initSchemaOnDB() on a fresh database: %v", err)
	}
	if applied == 0 {
		t.Fatalf("initSchemaOnDB() applied = 0, want the full fresh migration applied")
	}

	conn, err = pool.Conn(ctx)
	if err != nil {
		t.Fatalf("pin session after init: %v", err)
	}
	var afterID int64
	var fk int
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID(), @@FOREIGN_KEY_CHECKS").Scan(&afterID, &fk); err != nil {
		t.Fatalf("read session identity after init: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("release session: %v", err)
	}
	if afterID != beforeID {
		t.Fatalf("after a successful init the pool serves session %d, want the kept session %d: "+
			"the discard fired on success or forced an extra connection", afterID, beforeID)
	}
	if fk != 1 {
		t.Fatalf("the kept session carries FOREIGN_KEY_CHECKS = %d, want the server default 1", fk)
	}
}

// TestInitSchemaOnDBWithRetryAndGate_GateErrorClassification verifies the gate
// runs INSIDE the retry loop (bd-6dnrw.30): transient probe failures (a freshly
// auto-started server still warming its catalog) are retried like the migration
// itself, while a gate refusal and unexpected probe errors are permanent. No
// Dolt server is needed — the gate fails before the database is ever touched.
func TestInitSchemaOnDBWithRetryAndGate_GateErrorClassification(t *testing.T) {
	ctx := context.Background()
	// Never dialed: every gate below errors, so initSchemaOnDB never runs.
	db, err := sql.Open("mysql", "root@tcp(127.0.0.1:1)/never_dialed")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	t.Run("retryable probe error is retried until the gate answers", func(t *testing.T) {
		calls := 0
		gate := func(context.Context, *sql.DB) error {
			calls++
			if calls < 3 {
				return fmt.Errorf("remote-migrate gate: read current version: %w",
					errors.New("driver: bad connection"))
			}
			return &schema.RemoteMigrateGateError{CurrentVersion: 1, LatestVersion: 2, Pending: 1}
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if !schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %T (%v), want *schema.RemoteMigrateGateError after retries", err, err)
		}
		if calls != 3 {
			t.Fatalf("gate calls = %d, want 3 (two transient failures retried, then refusal)", calls)
		}
	})

	for _, tc := range []struct {
		name string
		err  *mysql.MySQLError
	}{
		{
			name: "typed unknown database",
			err:  &mysql.MySQLError{Number: 1049, Message: "Unknown database 'beads_test'"},
		},
		{
			name: "typed missing session root",
			err:  &mysql.MySQLError{Number: 1105, Message: "no root value found in session"},
		},
	} {
		t.Run(tc.name+" is retried during setup", func(t *testing.T) {
			calls := 0
			gate := func(context.Context, *sql.DB) error {
				calls++
				if calls < 3 {
					return fmt.Errorf("remote-migrate gate: probe schema: %w", tc.err)
				}
				return &schema.RemoteMigrateGateError{CurrentVersion: 1, LatestVersion: 2, Pending: 1}
			}
			_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
			if !schema.IsRemoteMigrateGateError(err) {
				t.Fatalf("err = %T (%v), want *schema.RemoteMigrateGateError after retries", err, err)
			}
			if calls != 3 {
				t.Fatalf("gate calls = %d, want 3", calls)
			}
		})
	}

	t.Run("gate refusal is permanent, not retried", func(t *testing.T) {
		calls := 0
		gate := func(context.Context, *sql.DB) error {
			calls++
			return &schema.RemoteMigrateGateError{CurrentVersion: 1, LatestVersion: 2, Pending: 1}
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if !schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if calls != 1 {
			t.Fatalf("gate calls = %d, want 1 (refusal must not be retried)", calls)
		}
	})

	t.Run("shared-store refusal is permanent, not retried", func(t *testing.T) {
		// The classification keys on the error TYPE, so the new #5920 decision
		// inherits it — pinned explicitly because retrying this particular
		// refusal would mean retrying it into the migration it just refused.
		calls := 0
		gate := func(context.Context, *sql.DB) error {
			calls++
			return &schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 66, Pending: 1,
				Decision: "shared-no-remote",
			}
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if !schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if calls != 1 {
			t.Fatalf("gate calls = %d, want 1 (a shared-store refusal must not be retried)", calls)
		}
	})

	t.Run("non-retryable probe error is permanent", func(t *testing.T) {
		calls := 0
		gate := func(context.Context, *sql.DB) error {
			calls++
			return errors.New("remote-migrate gate: read remotes: syntax error")
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if err == nil || schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %v, want plain permanent error", err)
		}
		if calls != 1 {
			t.Fatalf("gate calls = %d, want 1 (non-retryable must not be retried)", calls)
		}
	})

	t.Run("unrelated typed MySQL error is permanent", func(t *testing.T) {
		calls := 0
		gate := func(context.Context, *sql.DB) error {
			calls++
			return fmt.Errorf("remote-migrate gate: probe schema: %w",
				&mysql.MySQLError{Number: 1105, Message: "connection lost while validating commit"})
		}
		_, err := initSchemaOnDBWithRetryAndGate(ctx, db, gate)
		if err == nil || schema.IsRemoteMigrateGateError(err) {
			t.Fatalf("err = %v, want plain permanent error", err)
		}
		if calls != 1 {
			t.Fatalf("gate calls = %d, want 1", calls)
		}
	})
}
