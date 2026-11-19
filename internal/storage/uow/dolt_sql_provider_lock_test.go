package uow

import (
	"context"
	"database/sql"
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
	// The shared-store migration gate, between preparation and MigrateUp. This
	// init just created the database, so the cursor table does not exist yet,
	// the gate reads version 0, and it allows: creating a database is consent
	// for its schema.
	expectCursorProbe(mock, "schema_migrations", false)
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
	schema.SetSharedMigrateConsent(true)
	defer schema.SetSharedMigrateConsent(false)
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("^SHOW TABLES$").
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow("schema_migrations"))
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
	// The shared-store migration gate, between preparation and MigrateUp. This
	// database is pre-existing and behind, so the gate would refuse — the
	// consent below keeps this test about the lock/probe ordering it exists to
	// pin. See TestInitSchemaSharedStoreGate for the gate's own behavior.
	expectSharedGateProbe(mock, 1, 0)
	// The gate passed, so it asks forward drift too: behind, so allowed.
	expectVersionQuery(mock, 1)
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

// expectFreshBootstrapThroughFirstMigration scripts an initSchemaAttempt on a
// fresh database from the pre-lock probe through the first migration
// statement (the dolt_ignore seed), which answers firstErr, and then the
// migration-lock release.
func expectFreshBootstrapThroughFirstMigration(mock sqlmock.Sqlmock, database string, firstErr error) {
	expectFreshBootstrapThroughFirstMigrationStatement(mock, database, firstErr)
	expectLockRelease(mock, schema.MigrationLockName(database))
}

// expectFreshBootstrapThroughFirstMigrationStatement is
// expectFreshBootstrapThroughFirstMigration without the lock release, for a
// caller that scripts the release itself.
func expectFreshBootstrapThroughFirstMigrationStatement(mock sqlmock.Sqlmock, database string, firstErr error) {
	lockName := schema.MigrationLockName(database)
	expectNoSessionDatabase(mock)
	expectDatabaseExistsProbe(mock, database, false)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 5).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `" + database + "`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("USE `" + database + "`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid, DOLT_HASHOF('HEAD')")).
		WillReturnRows(sqlmock.NewRows([]string{"database", "server_uuid", "head"}).AddRow(database, "server-uuid", "initial-head"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	expectCursorProbe(mock, "schema_migrations", false)
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(firstErr)
}

// TestInitSchemaAttemptRetryableFailureDiscardsPinnedSession is the bd-yui097
// (gastownhall/beads#6504) repro: a retryable initSchemaAttempt failure (here a
// 1205 on the first migration statement) must not hand its pinned session back
// to the pool, because since wy-s8ytnw the fast path reuses that pool for live
// queries and the session may be mid-script (FOREIGN_KEY_CHECKS cleared) or
// catalog-pinned by the failed statement (be-bv7x).
func TestInitSchemaAttemptRetryableFailureDiscardsPinnedSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	expectFreshBootstrapThroughFirstMigration(mock, "beads",
		&mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"})
	// The discard physically closes the failed attempt's driver connection.
	mock.ExpectClose()

	p := &doltSQLProvider{
		defaultBranch:  defaultBranch,
		db:             db,
		serverEndpoint: "tcp:127.0.0.1:3306",
	}
	preparer := &bootstrapPreparer{provider: p, database: "beads"}
	err = p.initSchemaAttempt(context.Background(), "beads", preparer)
	var permanentErr *backoff.PermanentError
	if err == nil || errors.As(err, &permanentErr) {
		t.Fatalf("initSchemaAttempt() error = %v, want a retryable 1205 failure", err)
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1205 {
		t.Fatalf("initSchemaAttempt() error = %v, want the original 1205 error preserved", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("attempt SQL expectations: %v", err)
	}
	if stats := db.Stats(); stats.Idle != 0 || stats.OpenConnections != 0 {
		t.Fatalf("after a retryable failed attempt the pool holds %d open / %d idle sessions, want 0: "+
			"the failed attempt's session went back to the pool the fast path reuses for live queries", stats.OpenConnections, stats.Idle)
	}
}

// TestInitSchemaAttemptDiscardsSessionOnFailure covers every way an attempt
// that pinned a session can fail (gastownhall/beads#6504): the migration
// (retryable and permanent), team-server verification, preview attachment,
// the shared-store gate refusal, and a failed read-through. Each must discard
// the session rather than pool it, and keep its retry verdict and error
// identity unchanged.
func TestInitSchemaAttemptDiscardsSessionOnFailure(t *testing.T) {
	serialization := &mysql.MySQLError{Number: 1213, Message: "serialization failure"}
	lockWait := &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}
	permanentMigration := errors.New("migration statement failed")

	for _, tt := range []struct {
		name          string
		provider      func(p *doltSQLProvider)
		script        func(mock sqlmock.Sqlmock)
		wantPermanent bool
		wantIs        error
		wantGate      bool
		// wantContains is text the classified error must keep.
		wantContains string
		// wantMySQL is a driver error number the error must still carry.
		wantMySQL uint16
		// wantLockRelease asserts the lock-release failure stays reachable.
		wantLockRelease bool
	}{
		{
			name:   "migration lock wait is retried",
			script: func(mock sqlmock.Sqlmock) { expectFreshBootstrapThroughFirstMigration(mock, "beads", lockWait) },
			wantIs: lockWait,
		},
		{
			name: "migration failure is permanent",
			script: func(mock sqlmock.Sqlmock) {
				expectFreshBootstrapThroughFirstMigration(mock, "beads", permanentMigration)
			},
			wantPermanent: true,
			wantIs:        permanentMigration,
		},
		{
			name: "lock release failure does not double-close or mask the retryable verdict",
			script: func(mock sqlmock.Sqlmock) {
				expectFreshBootstrapThroughFirstMigrationStatement(mock, "beads", lockWait)
				// The release fails; schema's own cleanup discards the
				// session FIRST, so the attempt's deferred Raw/Close answer
				// sql.ErrConnDone and must be ignored. Exactly one driver
				// close may happen.
				mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
					WithArgs(schema.MigrationLockName("beads")).
					WillReturnError(errors.New("connection reset during release"))
				mock.ExpectClose()
			},
			wantMySQL:       1205,
			wantLockRelease: true,
		},
		{
			name:     "team-server switch is retried",
			provider: func(p *doltSQLProvider) { p.teamServer = true },
			script: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).WillReturnError(serialization)
			},
			wantIs: serialization,
		},
		{
			name:     "team-server USE is denied",
			provider: func(p *doltSQLProvider) { p.teamServer = true },
			script: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
					WillReturnError(&mysql.MySQLError{Number: 1044, Message: "Access denied for user 'u'@'%' to database 'beads'"})
			},
			wantPermanent: true,
			wantContains:  "was denied",
		},
		{
			name:     "team-server schema verification refuses a v0 database",
			provider: func(p *doltSQLProvider) { p.teamServer = true },
			script: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
					WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("beads"))
				expectVersionQuery(mock, 0)
			},
			wantPermanent: true,
			wantContains:  "bts init",
		},
		{
			name:     "preview attach is retried",
			provider: func(p *doltSQLProvider) { p.preview = true },
			script: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).WillReturnError(serialization)
			},
			wantIs: serialization,
		},
		{
			name:     "preview attach finds no database",
			provider: func(p *doltSQLProvider) { p.preview = true },
			script: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
					WillReturnError(&mysql.MySQLError{Number: 1049, Message: "Unknown database 'beads'"})
			},
			wantPermanent: true,
			wantContains:  previewRemedyText,
		},
		{
			name: "shared-store gate refusal is permanent",
			script: func(mock sqlmock.Sqlmock) {
				lockName := expectBehindDatabaseThroughPreparation(mock, "beads", 1)
				expectSharedGateProbe(mock, 1, 0)
				expectLockRelease(mock, lockName)
			},
			wantPermanent: true,
			wantGate:      true,
		},
		{
			name:     "failed read-through is retried",
			provider: func(p *doltSQLProvider) { p.readOnly = true },
			script: func(mock sqlmock.Sqlmock) {
				lockName := expectBehindDatabaseThroughPreparation(mock, "beads", 1)
				expectSharedGateProbe(mock, 1, 0)
				expectLockRelease(mock, lockName)
				mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).WillReturnError(serialization)
			},
			wantIs: serialization,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				schema.SetSharedMigrateConsent(false)
				schema.SetForceAllowRemoteMigrate(false)
			})
			t.Setenv(schema.AllowRemoteMigrateEnv, "0")
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			tt.script(mock)

			p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
			if tt.provider != nil {
				tt.provider(p)
			}
			preparer := &bootstrapPreparer{provider: p, database: "beads"}
			var attemptErr error
			_ = captureStderr(t, func() {
				attemptErr = p.initSchemaAttempt(context.Background(), "beads", preparer)
			})
			if attemptErr == nil {
				t.Fatal("initSchemaAttempt() error = nil, want a failure")
			}
			var permanentErr *backoff.PermanentError
			if got := errors.As(attemptErr, &permanentErr); got != tt.wantPermanent {
				t.Fatalf("initSchemaAttempt() permanent = %t, want %t (error = %v)", got, tt.wantPermanent, attemptErr)
			}
			if tt.wantIs != nil && !errors.Is(attemptErr, tt.wantIs) {
				t.Fatalf("initSchemaAttempt() error = %v, want it to wrap %v", attemptErr, tt.wantIs)
			}
			if tt.wantContains != "" && !strings.Contains(attemptErr.Error(), tt.wantContains) {
				t.Fatalf("initSchemaAttempt() error = %v, want it to keep %q", attemptErr, tt.wantContains)
			}
			var mysqlErr *mysql.MySQLError
			if tt.wantMySQL != 0 && (!errors.As(attemptErr, &mysqlErr) || mysqlErr.Number != tt.wantMySQL) {
				t.Fatalf("initSchemaAttempt() error = %v, want MySQL error %d preserved", attemptErr, tt.wantMySQL)
			}
			if tt.wantLockRelease && !errors.Is(attemptErr, schema.ErrMigrationLockRelease) {
				t.Fatalf("initSchemaAttempt() error = %v, want the lock-release failure kept reachable", attemptErr)
			}
			var gateErr *schema.RemoteMigrateGateError
			if tt.wantGate && !errors.As(attemptErr, &gateErr) {
				t.Fatalf("initSchemaAttempt() error = %T (%v), want *schema.RemoteMigrateGateError", attemptErr, attemptErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("attempt SQL expectations: %v", err)
			}
			if stats := db.Stats(); stats.OpenConnections != 0 || stats.Idle != 0 {
				t.Fatalf("after a failed attempt the pool holds %d open / %d idle sessions, want 0: "+
					"the failed session was handed back for reuse", stats.OpenConnections, stats.Idle)
			}
		})
	}
}

// TestInitSchemaAttemptKeepsSessionOnSuccess is the other half of the
// gastownhall/beads#6504 contract: only a FAILED attempt discards its session.
// Every success shape — a preview attach, the converged fast path, or a
// read-only open that reads through a refused migration — leaves its usable
// session idle in the pool, so initialization never forces a fresh connection.
func TestInitSchemaAttemptKeepsSessionOnSuccess(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, db *sql.DB, mock sqlmock.Sqlmock) *doltSQLProvider
	}{
		{
			name: "preview attach keeps its session",
			setup: func(t *testing.T, db *sql.DB, mock sqlmock.Sqlmock) *doltSQLProvider {
				mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
					WillReturnResult(sqlmock.NewResult(0, 0))
				return &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306", preview: true}
			},
		},
		{
			name: "converged fast path keeps its session",
			setup: func(t *testing.T, db *sql.DB, mock sqlmock.Sqlmock) *doltSQLProvider {
				calls := stubConvergedMigrateUp(t)
				t.Cleanup(func() {
					if *calls != 1 {
						t.Errorf("MigrateUpWithLock called %d times, want 1", *calls)
					}
				})
				// The drift check after the converged fast path: one version
				// read, at latest, on the pinned session.
				expectVersionQuery(mock, schema.LatestVersion())
				return &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306"}
			},
		},
		{
			name: "read-only read-through keeps its session",
			setup: func(t *testing.T, db *sql.DB, mock sqlmock.Sqlmock) *doltSQLProvider {
				t.Setenv(schema.AllowRemoteMigrateEnv, "0")
				behind := schema.LatestVersion() - 1
				lockName := expectBehindDatabaseThroughPreparation(mock, "beads", behind)
				expectSharedGateProbe(mock, behind, 0)
				expectLockRelease(mock, lockName)
				mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
					WillReturnResult(sqlmock.NewResult(0, 0))
				expectIssuesProjection(mock, behind, "")
				return &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:127.0.0.1:3306", readOnly: true}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema.SetSharedMigrateConsent(false)
			t.Cleanup(func() {
				schema.SetSharedMigrateConsent(false)
				schema.SetForceAllowRemoteMigrate(false)
			})
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)

			p := tc.setup(t, db, mock)
			var attemptErr error
			if p.readOnly {
				stderr := captureStderr(t, func() {
					attemptErr = p.initSchemaAttempt(context.Background(), "beads", &bootstrapPreparer{provider: p, database: "beads"})
				})
				for _, want := range []string{"Read-only command", "without migrating"} {
					if !strings.Contains(stderr, want) {
						t.Errorf("warning missing %q:\n%s", want, stderr)
					}
				}
			} else {
				attemptErr = p.initSchemaAttempt(context.Background(), "beads", &bootstrapPreparer{provider: p, database: "beads"})
			}
			if attemptErr != nil {
				t.Fatalf("initSchemaAttempt() error = %v, want nil", attemptErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("attempt SQL expectations: %v", err)
			}
			if stats := db.Stats(); stats.OpenConnections != 1 || stats.Idle != 1 {
				t.Fatalf("after a successful attempt the pool holds %d open / %d idle sessions, want 1 / 1: "+
					"a successful attempt must keep its session", stats.OpenConnections, stats.Idle)
			}
			// The kept session is usable: the next query runs on it without a
			// new driver connection (sqlmock would refuse one only after the
			// last connection closed, which this proves did not happen).
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1")).WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			var one int
			if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil {
				t.Fatalf("query on the kept session: %v", err)
			}
			if stats := db.Stats(); stats.OpenConnections != 1 {
				t.Fatalf("query after a successful attempt opened %d connections, want the kept 1", stats.OpenConnections)
			}
		})
	}
}
