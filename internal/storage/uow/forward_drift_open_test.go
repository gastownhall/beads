package uow

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestInitSchemaRefusesForwardDrift covers the proxied/`bd serve` open on a
// database whose schema is AHEAD of this binary — the state every store is in
// for an older bd (a stale proxy, a rollback binary) after a newer one has
// migrated it. MigrateUp reads "cursor >= latest" as nothing to do, so the
// open must ask the forward-drift question itself, exactly as the dolt store,
// embeddeddolt and team-server opens do.
func TestInitSchemaRefusesForwardDrift(t *testing.T) {
	ahead := schema.LatestVersion() + 1

	t.Run("converged fast path", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		// The lock-free convergence probe answers "converged" for an ahead
		// database (TestAlreadyConvergedOnForwardSkew pins that), so
		// MigrateUpWithLock returns (0, nil) without ever reaching the gate.
		calls := 0
		prev := migrateUpWithLock
		migrateUpWithLock = func(context.Context, *sql.Conn, string, ...schema.MigrateLockOption) (int, error) {
			calls++
			return 0, nil
		}
		t.Cleanup(func() { migrateUpWithLock = prev })

		expectVersionQuery(mock, ahead)

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		err = p.initSchema(context.Background(), "beads")
		if !schema.IsSchemaSkewError(err) {
			t.Fatalf("initSchema() error = %v (%T), want *schema.SchemaSkewError", err, err)
		}
		// One scripted attempt: a retry would call the stub again.
		if calls != 1 {
			t.Fatalf("migrateUpWithLock called %d times, want 1 (the refusal must not be retried)", calls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("locked path refuses before MigrateUp", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		// The pre-lock probe cannot read the cursor and fails closed onto the
		// locked path, which is where MigrateUp would seed and commit.
		expectNoSessionDatabase(mock)
		expectDatabaseExistsProbe(mock, "beads", true)
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
			WillReturnError(errors.New("probe unavailable"))

		lockName := schema.MigrationLockName("beads")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
			WithArgs(lockName, 5).
			WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
		mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
			WillReturnError(&mysql.MySQLError{Number: 1007, Message: "database exists"})
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		// The shared-store gate reads the cursor twice and, with nothing
		// pending on an ahead database, passes it on to the forward-drift
		// check. No migration statement follows the refusal.
		expectVersionQuery(mock, ahead)
		expectVersionQuery(mock, ahead)
		expectVersionQuery(mock, ahead)
		expectLockRelease(mock, lockName)

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		err = p.initSchema(context.Background(), "beads")
		if !schema.IsSchemaSkewError(err) {
			t.Fatalf("initSchema() error = %v (%T), want *schema.SchemaSkewError", err, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("BD_IGNORE_SCHEMA_SKEW lets the converged open through", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		prev := migrateUpWithLock
		migrateUpWithLock = func(context.Context, *sql.Conn, string, ...schema.MigrateLockOption) (int, error) {
			return 0, nil
		}
		t.Cleanup(func() { migrateUpWithLock = prev })

		expectVersionQuery(mock, ahead)

		p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
		if err := p.initSchema(context.Background(), "beads"); err != nil {
			t.Fatalf("initSchema() error = %v, want nil with BD_IGNORE_SCHEMA_SKEW=1", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})
}
