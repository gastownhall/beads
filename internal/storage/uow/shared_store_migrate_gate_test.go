package uow

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// expectSharedGateProbe mocks the shared-store gate's read sequence on an
// existing database: CurrentVersion, PendingVersions, and the remote count.
func expectSharedGateProbe(mock sqlmock.Sqlmock, current, remotes int) {
	for i := 0; i < 2; i++ { // CurrentVersion, then PendingVersions
		expectCursorProbe(mock, "schema_migrations", true)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")).
			WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(current))
	}
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(remotes))
}

// expectBehindDatabaseThroughPreparation walks every statement of a proxied
// open against a pre-existing database that is one migration behind, up to and
// including the locked preparation — the point at which the migration gate
// runs. Returns the lock name so callers can expect its release.
func expectBehindDatabaseThroughPreparation(mock sqlmock.Sqlmock, database string, current int) string {
	// Pre-lock convergence probe: the database exists, the session is put on
	// it, and the cursor turns out to be behind — so the probe declines and
	// the locked path runs.
	expectNoSessionDatabase(mock)
	expectDatabaseExistsProbe(mock, database, true)
	mock.ExpectExec(regexp.QuoteMeta("USE `" + database + "`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(current))

	lockName := schema.MigrationLockName(database)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 5).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	// Locked preparation on a pre-existing database: the bare CREATE loses, so
	// this init captures no fresh-bootstrap heal authority, and the USE is all
	// it contributes.
	mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `" + database + "`")).
		WillReturnError(&mysql.MySQLError{Number: 1007, Message: "database exists"})
	mock.ExpectExec(regexp.QuoteMeta("USE `" + database + "`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	return lockName
}

func expectLockRelease(mock sqlmock.Sqlmock, lockName string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
}

// TestInitSchemaSharedStoreGate covers the proxied/`bd serve` open path's half
// of gastownhall/beads#5920 — and closes the gate leg of #5043, where this
// path ran MigrateUpWithLock with no CheckRemoteMigrateGate variant at all and
// so migrated a shared database on every open, silently, since v1.2.2.
func TestInitSchemaSharedStoreGate(t *testing.T) {
	resetConsent := func(t *testing.T) {
		t.Helper()
		t.Cleanup(func() {
			schema.SetSharedMigrateConsent(false)
			schema.SetForceAllowRemoteMigrate(false)
		})
	}

	t.Run("behind database is refused, permanently", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(schema.AllowRemoteMigrateEnv, "0")
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		lockName := expectBehindDatabaseThroughPreparation(mock, "beads", 1)
		expectSharedGateProbe(mock, 1, 0)
		// No migration statement between the gate and the release: the refusal
		// must land before MigrateUp issues its first one.
		expectLockRelease(mock, lockName)

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		err = p.initSchema(context.Background(), "beads")
		var gateErr *schema.RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("initSchema() error = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if gateErr.Decision != "shared-no-remote" {
			t.Errorf("Decision = %q, want %q", gateErr.Decision, "shared-no-remote")
		}
		// The backoff budget is 60s; the mock scripts exactly one attempt.
		// Reaching here at all proves the refusal was not retried — a retry
		// would have hit unexpected-query errors and a different failure.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("read-only open warns and serves the current schema", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(schema.AllowRemoteMigrateEnv, "0")
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		lockName := expectBehindDatabaseThroughPreparation(mock, "beads", 1)
		expectSharedGateProbe(mock, 1, 0)
		expectLockRelease(mock, lockName)
		// The read-through: attach at the current schema and carry on. Still
		// no migration statement anywhere.
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306", readOnly: true}
		var initErr error
		stderr := captureStderr(t, func() {
			initErr = p.initSchema(context.Background(), "beads")
		})
		if initErr != nil {
			t.Fatalf("initSchema() error = %v, want nil — reads must keep working on the old schema", initErr)
		}
		for _, want := range []string{"Read-only command", "without migrating", "bd migrate schema"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("warning missing %q:\n%s", want, stderr)
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("fresh database is created and migrated — creation is consent", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(schema.AllowRemoteMigrateEnv, "0")
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		lockName := schema.MigrationLockName("beads")
		expectNoSessionDatabase(mock)
		expectDatabaseExistsProbe(mock, "beads", false)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
			WithArgs(lockName, 5).
			WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
		mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid, DOLT_HASHOF('HEAD')")).
			WillReturnRows(sqlmock.NewRows([]string{"database", "server_uuid", "head"}).AddRow("beads", "server-uuid", "initial-head"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status")).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		// The gate on a database this init just created: no cursor table, so
		// version 0, so allow — and MigrateUp proceeds to its first statement.
		expectCursorProbe(mock, "schema_migrations", false)
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(sqlmock.AnyArg()).
			WillReturnError(errors.New("reached the first migration statement"))
		expectLockRelease(mock, lockName)

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		err = p.initSchema(context.Background(), "beads")
		if err == nil || !strings.Contains(err.Error(), "reached the first migration statement") {
			t.Fatalf("initSchema() error = %v, want the fresh bootstrap to migrate", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("env consent migrates", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(schema.AllowRemoteMigrateEnv, "1")
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		lockName := expectBehindDatabaseThroughPreparation(mock, "beads", 1)
		expectSharedGateProbe(mock, 1, 0)
		mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
			WithArgs(sqlmock.AnyArg()).
			WillReturnError(errors.New("reached the first migration statement"))
		expectLockRelease(mock, lockName)

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		var initErr error
		_ = captureStderr(t, func() {
			initErr = p.initSchema(context.Background(), "beads")
		})
		if initErr == nil || !strings.Contains(initErr.Error(), "reached the first migration statement") {
			t.Fatalf("initSchema() error = %v, want the consented open to migrate", initErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// Preview never reaches MigrateUpWithLock at all, so it cannot reach the
	// gate either — pinned so a future refactor cannot route preview through
	// the migrating path and start refusing --dry-run on a behind workspace.
	t.Run("preview open is unchanged", func(t *testing.T) {
		resetConsent(t)
		t.Setenv(schema.AllowRemoteMigrateEnv, "0")
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306", preview: true}
		if err := p.initSchema(context.Background(), "beads"); err != nil {
			t.Fatalf("initSchema() error = %v, want the attach-only preview open", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}
