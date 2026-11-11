package uow

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// stubConvergedMigrateUp stands in for schema.MigrateUpWithLock's converged
// fast path: it reports success without taking the migration lock, running
// the migration gate, or issuing a single statement — exactly what the real
// function does once its pre-lock convergence probe answers "converged". The
// probe deliberately answers that for an AHEAD database too
// (schema.TestAlreadyConvergedOnForwardSkew), so whatever refuses forward
// drift on this path has to run after the call. Returns the call count. Not
// safe for t.Parallel: it swaps a package variable.
func stubConvergedMigrateUp(t *testing.T) *int {
	t.Helper()
	prev := migrateUpWithLock
	calls := 0
	migrateUpWithLock = func(context.Context, *sql.Conn, string, ...schema.MigrateLockOption) (int, error) {
		calls++
		return 0, nil
	}
	t.Cleanup(func() { migrateUpWithLock = prev })
	return &calls
}

// countMigrateUpWithLock wraps the real schema.MigrateUpWithLock with a call
// counter, so a test can prove a refusal was not retried. Not safe for
// t.Parallel: it swaps a package variable.
func countMigrateUpWithLock(t *testing.T) *int {
	t.Helper()
	prev := migrateUpWithLock
	calls := 0
	migrateUpWithLock = func(ctx context.Context, conn *sql.Conn, database string, opts ...schema.MigrateLockOption) (int, error) {
		calls++
		return prev(ctx, conn, database, opts...)
	}
	t.Cleanup(func() { migrateUpWithLock = prev })
	return &calls
}

// expectAheadDatabaseThroughDriftCheck walks the real locked path of a proxied
// open against a pre-existing database at version ahead: the convergence probe
// declines (its main cursor is ahead but the ignored-lane cursor table is
// missing, so there is migration work), GET_LOCK, the locked preparation (a
// losing bare CREATE, then USE), the shared-store gate's two version reads,
// which find nothing pending and pass, and finally the forward-drift read.
// No migration write is scripted: any statement MigrateUp issues after it is
// unexpected. Returns the lock name so callers can script its release.
func expectAheadDatabaseThroughDriftCheck(mock sqlmock.Sqlmock, database string, ahead int) string {
	expectNoSessionDatabase(mock)
	expectDatabaseExistsProbe(mock, database, true)
	mock.ExpectExec(regexp.QuoteMeta("USE `" + database + "`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectVersionQuery(mock, ahead)
	expectCursorProbe(mock, "ignored_schema_migrations", false)

	lockName := schema.MigrationLockName(database)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 5).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `" + database + "`")).
		WillReturnError(&mysql.MySQLError{Number: 1007, Message: "database exists"})
	mock.ExpectExec(regexp.QuoteMeta("USE `" + database + "`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Shared-store gate: CurrentVersion and PendingVersions; nothing pending.
	expectVersionQuery(mock, ahead)
	expectVersionQuery(mock, ahead)
	// Forward-drift check, inside the gate and before MigrateUp.
	expectVersionQuery(mock, ahead)
	return lockName
}

// TestInitSchemaRefusesForwardDrift pins the proxied-server / `bd serve` open
// (doltSQLProvider.initSchemaAttempt) to the forward-drift rule every other
// open path already enforces with schema.CheckForwardDrift: a database whose
// schema is AHEAD of this binary is refused with *schema.SchemaSkewError,
// unless BD_IGNORE_SCHEMA_SKEW=1.
func TestInitSchemaRefusesForwardDrift(t *testing.T) {
	ahead := schema.LatestVersion() + 1

	// AC-1: the converged fast path never reaches the migration gate, so the
	// refusal comes from the check after MigrateUpWithLock returns. One
	// attempt, no lock, no write: any statement beyond the version read would
	// be unexpected to sqlmock.
	for _, readOnly := range []bool{false, true} {
		name := "converged fast path refuses an ahead database"
		if readOnly {
			name = "converged fast path refuses an ahead database on a read-only open"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")
			calls := stubConvergedMigrateUp(t)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()

			expectVersionQuery(mock, ahead)

			p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306", readOnly: readOnly}
			err = p.initSchema(context.Background(), "beads")
			var skew *schema.SchemaSkewError
			if !errors.As(err, &skew) {
				t.Fatalf("initSchema() on a database at v%d (binary v%d) error = %T (%v), want *schema.SchemaSkewError",
					ahead, schema.LatestVersion(), err, err)
			}
			if skew.DBVersion != ahead || skew.BinaryVersion != schema.LatestVersion() {
				t.Errorf("SchemaSkewError = {DB: %d, Binary: %d}, want {DB: %d, Binary: %d}",
					skew.DBVersion, skew.BinaryVersion, ahead, schema.LatestVersion())
			}
			if *calls != 1 {
				t.Errorf("MigrateUpWithLock called %d times, want 1: a skew refusal must not be retried", *calls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}

	// AC-2: with convergence declined, the refusal must land inside the
	// migration gate — before MigrateUp seeds dolt_ignore or reconciles on the
	// newer database — and the lock must still be released.
	t.Run("locked path refuses before any MigrateUp write", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")
		calls := countMigrateUpWithLock(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		lockName := expectAheadDatabaseThroughDriftCheck(mock, "beads", ahead)
		expectLockRelease(mock, lockName)

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		err = p.initSchema(context.Background(), "beads")
		var skew *schema.SchemaSkewError
		if !errors.As(err, &skew) {
			t.Fatalf("initSchema() on a database at v%d (binary v%d) error = %T (%v), want *schema.SchemaSkewError",
				ahead, schema.LatestVersion(), err, err)
		}
		if skew.DBVersion != ahead || skew.BinaryVersion != schema.LatestVersion() {
			t.Errorf("SchemaSkewError = {DB: %d, Binary: %d}, want {DB: %d, Binary: %d}",
				skew.DBVersion, skew.BinaryVersion, ahead, schema.LatestVersion())
		}
		if *calls != 1 {
			t.Errorf("MigrateUpWithLock called %d times, want 1: a skew refusal must not be retried", *calls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// A lock-release failure wraps the gate's error with a migration-lock
	// error, which classifyInitSchemaError alone would retry. The skew refusal
	// must stay permanent, and both errors must stay reachable.
	t.Run("locked path refusal is not retried when the lock release also fails", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")
		calls := countMigrateUpWithLock(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		lockName := expectAheadDatabaseThroughDriftCheck(mock, "beads", ahead)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
			WithArgs(lockName).
			WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(0))

		// Bounded: a regression that retries fails fast here instead of
		// spending initSchema's whole 60s backoff budget.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		err = p.initSchema(ctx, "beads")
		var skew *schema.SchemaSkewError
		if !errors.As(err, &skew) {
			t.Fatalf("initSchema() error = %T (%v), want *schema.SchemaSkewError", err, err)
		}
		if !errors.Is(err, schema.ErrMigrationLockRelease) {
			t.Errorf("initSchema() error = %v, want the lock-release failure kept reachable", err)
		}
		if *calls != 1 {
			t.Errorf("MigrateUpWithLock called %d times, want 1: a skew refusal must not be retried", *calls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// A serialization failure on the added version read keeps the provider's
	// ordinary retry; the next attempt reads a valid version and opens.
	t.Run("serialization failure on the drift read is retried", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")
		calls := stubConvergedMigrateUp(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
			WillReturnError(&mysql.MySQLError{Number: 1213, Message: "serialization failure"})
		expectVersionQuery(mock, schema.LatestVersion())

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		if err := p.initSchema(context.Background(), "beads"); err != nil {
			t.Fatalf("initSchema() error = %v, want the retry to open", err)
		}
		if *calls != 2 {
			t.Errorf("MigrateUpWithLock called %d times, want 2 (one retry)", *calls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}
