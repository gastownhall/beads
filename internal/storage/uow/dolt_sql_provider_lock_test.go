package uow

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/cenkalti/backoff/v4"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/schema"
)

func TestClassifyInitSchemaErrorKeepsBootstrapPreparationErrorsDistinct(t *testing.T) {
	t.Run("permanent preparation remains permanent when cleanup fails", func(t *testing.T) {
		preparationErr := errors.New("create database failed")
		err := classifyInitSchemaError(errors.Join(
			&bootstrapPreparationError{err: preparationErr},
			schema.ErrMigrationLockRelease,
		))

		var permanentErr *backoff.PermanentError
		if !errors.As(err, &permanentErr) {
			t.Fatalf("classifyInitSchemaError() error = %T %v, want permanent", err, err)
		}
		if !errors.Is(err, preparationErr) || !errors.Is(err, schema.ErrMigrationLockRelease) {
			t.Fatalf("classifyInitSchemaError() error = %v, want preparation and release errors", err)
		}
	})

	for _, tt := range []struct {
		name      string
		retryable bool
		permanent bool
	}{
		{name: "initial bare create serialization retries", retryable: true},
		{name: "use serialization remains permanent", permanent: true},
		{name: "capture serialization remains permanent", permanent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			serializationErr := &mysql.MySQLError{Number: 1213}
			err := classifyInitSchemaError(&bootstrapPreparationError{
				err:       serializationErr,
				retryable: tt.retryable,
			})

			var permanentErr *backoff.PermanentError
			if got := errors.As(err, &permanentErr); got != tt.permanent {
				t.Fatalf("classifyInitSchemaError() permanent = %t, want %t (error = %v)", got, tt.permanent, err)
			}
			if !errors.Is(err, serializationErr) {
				t.Fatalf("classifyInitSchemaError() error = %v, want serialization error", err)
			}
			if !tt.permanent && (!strings.Contains(err.Error(), "bootstrap preparation") || strings.Contains(err.Error(), "uow: migrate:")) {
				t.Fatalf("classifyInitSchemaError() error = %v, want retryable bootstrap preparation error", err)
			}
		})
	}
}

// expectNoSessionDatabase mocks the opening question of the pre-lock
// convergence probe on the shape every production seat presents: initSchema
// pins its connection from a pool opened with an EMPTY DSN database (see
// openAndInitSchema), so DATABASE() is NULL until something issues USE.
func expectNoSessionDatabase(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(nil))
}

// expectDatabaseExistsProbe mocks the always-succeeding existence probe the
// convergence probe must issue before any USE.
func expectDatabaseExistsProbe(mock sqlmock.Sqlmock, database string, exists bool) {
	n := 0
	if exists {
		n = 1
	}
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.schemata`).
		WithArgs(database).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
}

// TestInitSchemaAcquiresMigrationLockBeforeBootstrapDDL is the fresh-bootstrap
// ordering guard: nothing that creates or touches schema may run before
// GET_LOCK.
//
// The probe expectations at the top are load-bearing, not scenery. Without
// them the probe's statements were simply unexpected — sqlmock refuses an
// unexpected query WITHOUT consuming the next expectation, so the probe erred,
// failed closed, and GET_LOCK matched expectation #1 anyway. The test passed
// no matter what the probe did or did not do on this path, which is exactly
// how a fast path that could never fire on the proxied CLI open reached
// review. Now the probe is walked explicitly: it finds no database to converge
// on (this init is about to create it), declines, and the lock is taken first.
func TestInitSchemaAcquiresMigrationLockBeforeBootstrapDDL(t *testing.T) {
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
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(errors.New("first migration statement failed"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	p := &doltSQLProvider{
		defaultBranch:  defaultBranch,
		db:             db,
		serverEndpoint: "tcp:127.0.0.1:3306",
	}
	err = p.initSchema(context.Background(), "beads")
	if err == nil || !strings.Contains(err.Error(), "first migration statement failed") {
		t.Fatalf("initSchema() error = %v, want first migration sentinel", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ordered bootstrap SQL expectations: %v", err)
	}
}

// TestInitSchemaConvergenceProbeRunsWithNoSessionDatabase is the caller-side
// regression for the fast path that could not fire. openAndInitSchema opens
// its schema-init pool with an EMPTY DSN database and the USE happens only
// inside the locked bootstrap preparation, so when the pre-lock convergence
// probe asked DATABASE() it read NULL on every production seat's open, gave
// up on its first statement, and took the server-wide GET_LOCK exactly as
// before — the change measured on the shared rig was a no-op on the only path
// that mattered.
//
// From this caller, with the database already present, the probe must reach
// the schema predicates: prove the database exists with a query that cannot
// fail, USE it, and read the cursor. Here that cursor is far behind, so the
// probe correctly declines and the ordinary locked pass follows — GET_LOCK
// first, bootstrap DDL after.
func TestInitSchemaConvergenceProbeRunsWithNoSessionDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	lockName := schema.MigrationLockName("beads")
	expectNoSessionDatabase(mock)
	expectDatabaseExistsProbe(mock, "beads", true)
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// migrationWorkNeeded's first question, reached only because the probe put
	// the session on the database itself. The cursor is far behind this
	// binary, so there is real work and the fast path declines.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.tables`).
		WithArgs("schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(1))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 5).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	// The bare CREATE DATABASE loses to the database the probe just proved
	// exists, so this init captures no fresh-bootstrap heal authority.
	mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
		WillReturnError(&mysql.MySQLError{Number: 1007, Message: "database exists"})
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(errors.New("first migration statement failed"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	p := &doltSQLProvider{
		defaultBranch:  defaultBranch,
		db:             db,
		serverEndpoint: "tcp:127.0.0.1:3306",
	}
	err = p.initSchema(context.Background(), "beads")
	if err == nil || !strings.Contains(err.Error(), "first migration statement failed") {
		t.Fatalf("initSchema() error = %v, want first migration sentinel", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the convergence probe must reach the schema predicates on a session with no database selected: %v", err)
	}
}

// TestSelectProbeDatabaseIssuesPreparationsUse pins the two properties the
// convergence probe borrows from this injection site and cannot check for
// itself: the statement is the DDL repository's own UseDatabase (identical to
// what prepareBootstrap issues, which is why a converged probe may skip locked
// preparation), and the name is validated and quoted by that same repository
// BEFORE any statement goes out.
func TestSelectProbeDatabaseIssuesPreparationsUse(t *testing.T) {
	t.Run("valid name", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		quoted, err := selectProbeDatabase(context.Background(), db, "beads")
		if err != nil {
			t.Fatalf("selectProbeDatabase() error = %v", err)
		}
		if quoted != "`beads`" {
			t.Fatalf("selectProbeDatabase() quoted = %q, want %q", quoted, "`beads`")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("invalid name", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		defer db.Close()

		quoted, err := selectProbeDatabase(context.Background(), db, "bad`name")
		if err == nil {
			t.Fatal("selectProbeDatabase() error = nil, want the identifier rejection")
		}
		if quoted != "" {
			t.Fatalf("selectProbeDatabase() quoted = %q, want empty", quoted)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations (an invalid name must be refused before any statement): %v", err)
		}
	})
}
