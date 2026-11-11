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
