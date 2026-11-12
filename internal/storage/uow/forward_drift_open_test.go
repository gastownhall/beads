package uow

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
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

// skewIgnoredWarning is the leading text of the warning schema prints when
// BD_IGNORE_SCHEMA_SKEW=1 lets an ahead database through.
const skewIgnoredWarning = "Warning: schema skew ignored"

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

	// AC-3: BD_IGNORE_SCHEMA_SKEW=1 lets an ahead database through on either
	// path, with schema's existing warning printed once per attempt.
	t.Run("override opens an ahead database on the converged path with one warning", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
		calls := stubConvergedMigrateUp(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		expectVersionQuery(mock, ahead)

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		var initErr error
		stderr := captureStderr(t, func() {
			initErr = p.initSchema(context.Background(), "beads")
		})
		if initErr != nil {
			t.Fatalf("initSchema() error = %v, want BD_IGNORE_SCHEMA_SKEW=1 to open", initErr)
		}
		if got := strings.Count(stderr, skewIgnoredWarning); got != 1 {
			t.Errorf("stderr carries %d skew-ignored warnings, want 1:\n%s", got, stderr)
		}
		if *calls != 1 {
			t.Errorf("MigrateUpWithLock called %d times, want 1", *calls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// On the locked path the gate's check allows the open with its warning and
	// MigrateUp proceeds (here to its first statement, which the script fails
	// so the run ends); the check is not asked a second time.
	t.Run("override lets the locked path through to MigrateUp with one warning", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
		calls := countMigrateUpWithLock(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		lockName := expectAheadDatabaseThroughDriftCheck(mock, "beads", ahead)
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(sqlmock.AnyArg()).
			WillReturnError(errors.New("reached the first migration statement"))
		expectLockRelease(mock, lockName)

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		var initErr error
		stderr := captureStderr(t, func() {
			initErr = p.initSchema(context.Background(), "beads")
		})
		if initErr == nil || !strings.Contains(initErr.Error(), "reached the first migration statement") {
			t.Fatalf("initSchema() error = %v, want BD_IGNORE_SCHEMA_SKEW=1 to let MigrateUp run", initErr)
		}
		if schema.IsSchemaSkewError(initErr) {
			t.Errorf("initSchema() error = %v, want no SchemaSkewError under BD_IGNORE_SCHEMA_SKEW=1", initErr)
		}
		if got := strings.Count(stderr, skewIgnoredWarning); got != 1 {
			t.Errorf("stderr carries %d skew-ignored warnings, want 1:\n%s", got, stderr)
		}
		if *calls != 1 {
			t.Errorf("MigrateUpWithLock called %d times, want 1", *calls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// AC-4: the steady-state open — a database at this binary's latest schema
	// on the converged fast path — still opens, and pays exactly one added
	// CurrentVersion read (the cursor existence probe, SHOW TABLES, and the
	// MAX(version) read) on the pinned session: no lock, no write.
	t.Run("converged path at latest opens with exactly one added version read", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")
		calls := stubConvergedMigrateUp(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		expectVersionQuery(mock, schema.LatestVersion())

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		var initErr error
		stderr := captureStderr(t, func() {
			initErr = p.initSchema(context.Background(), "beads")
		})
		if initErr != nil {
			t.Fatalf("initSchema() error = %v, want the steady-state open to succeed", initErr)
		}
		if strings.Contains(stderr, skewIgnoredWarning) {
			t.Errorf("a database at latest printed the skew warning:\n%s", stderr)
		}
		if *calls != 1 {
			t.Errorf("MigrateUpWithLock called %d times, want 1", *calls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// AC-5: a BEHIND database the shared-store check refuses, or serves
	// through read-only, never reaches the forward-drift read: the statement
	// sequence is exactly the pre-existing one (no version read between the
	// gate's probe and the lock release), and the outcome is unchanged.
	for _, readOnly := range []bool{false, true} {
		name := "behind database refused by the shared-store gate adds no drift read"
		if readOnly {
			name = "behind database read through on a read-only open adds no drift read"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")
			t.Setenv(schema.AllowRemoteMigrateEnv, "0")
			t.Cleanup(func() {
				schema.SetSharedMigrateConsent(false)
				schema.SetForceAllowRemoteMigrate(false)
			})
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()

			lockName := expectBehindDatabaseThroughPreparation(mock, "beads", 1)
			expectSharedGateProbe(mock, 1, 0)
			expectLockRelease(mock, lockName)
			if readOnly {
				mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
					WillReturnResult(sqlmock.NewResult(0, 0))
				expectIssuesProjection(mock, 1, "")
			}

			p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306", readOnly: readOnly}
			var initErr error
			_ = captureStderr(t, func() {
				initErr = p.initSchema(context.Background(), "beads")
			})
			if readOnly {
				if initErr != nil {
					t.Fatalf("initSchema() error = %v, want the read-only read-through", initErr)
				}
			} else {
				var gateErr *schema.RemoteMigrateGateError
				if !errors.As(initErr, &gateErr) {
					t.Fatalf("initSchema() error = %T (%v), want *schema.RemoteMigrateGateError", initErr, initErr)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}

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
