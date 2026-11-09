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
	// Preparation looks again under the lock: still not visible, so create.
	expectDatabaseExistsProbe(mock, "beads", false)
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
	// Preparation finds the database visible, so it sends no CREATE
	// DATABASE and this init captures no fresh-bootstrap heal authority.
	expectDatabaseExistsProbe(mock, "beads", true)
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// The shared-store migration gate, between preparation and MigrateUp. This
	// database is pre-existing and behind, so the gate would refuse — the
	// consent below keeps this test about the lock/probe ordering it exists to
	// pin. See TestInitSchemaSharedStoreGate for the gate's own behavior.
	expectSharedGateProbe(mock, 1, 0)
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

// TestInitSchemaReadOnlyOpenOfExistingDatabaseIssuesNoCreateDatabase
// reproduces gastownhall/beads#5079 through initSchema: a read command over the
// proxied open path must not require global CREATE on a database that already
// exists. Here the pre-lock convergence probe proves the database exists (and
// puts the session on it), but declines because the cursor is behind this
// binary, so the locked path runs. A least-privilege (SELECT-only) credential
// cannot CREATE DATABASE, so any CREATE sent here fails the whole read before
// it starts; preparation must look before it leaps and the open read through
// on the old schema.
func TestInitSchemaReadOnlyOpenOfExistingDatabaseIssuesNoCreateDatabase(t *testing.T) {
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	t.Setenv(schema.SmartGateEnv, "1")
	t.Cleanup(func() {
		schema.SetSharedMigrateConsent(false)
		schema.SetForceAllowRemoteMigrate(false)
	})

	var sentCreate []string
	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if strings.Contains(strings.ToUpper(actualSQL), "CREATE DATABASE") {
			sentCreate = append(sentCreate, actualSQL)
		}
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	// Pre-lock convergence probe: the database exists, the session is put on
	// it, and the cursor is behind, so the probe declines.
	expectNoSessionDatabase(mock)
	expectDatabaseExistsProbe(mock, "beads", true)
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(dataBehindCurrent))

	lockName := schema.MigrationLockName("beads")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 5).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	// Preparation re-checks visibility under the lock and only selects the
	// database: no CREATE DATABASE is expected on this path.
	expectDatabaseExistsProbe(mock, "beads", true)
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectSharedGateProbe(mock, dataBehindCurrent, 1)
	expectSmartGateDataBehindRoute(mock, 1, 2)
	expectLockRelease(mock, lockName)
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectIssuesProjection(mock, dataBehindCurrent, "")

	p := &doltSQLProvider{
		defaultBranch:     defaultBranch,
		db:                db,
		serverEndpoint:    "tcp:127.0.0.1:3306",
		proxiedServerMode: true,
		readOnly:          true,
	}
	var initErr error
	_ = captureStderr(t, func() {
		initErr = p.initSchema(context.Background(), "beads")
	})
	if len(sentCreate) > 0 {
		t.Errorf("read-only open of an existing database sent %q; a SELECT-only credential is refused here (gastownhall/beads#5079)", sentCreate)
	}
	if initErr != nil {
		t.Fatalf("initSchema() error = %v, want nil: a read command on an existing database must not need global CREATE", initErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
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

// preparerConn pins one connection of a fresh ordered sqlmock pool, the shape
// MigrateUpWithLock hands its locked-preparation callback.
func preparerConn(t *testing.T) (*sql.Conn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, mock
}

// TestBootstrapPreparerProbesBeforeCreate is the gastownhall/beads#5079
// regression at its source: locked bootstrap preparation looks before it
// leaps. A database this credential can already see needs only the USE, so a
// SELECT-only account never sends (and is never refused) CREATE DATABASE —
// and visibility is never mistaken for ownership: the preparer's creation
// state and heal capability come out exactly as they went in.
func TestBootstrapPreparerProbesBeforeCreate(t *testing.T) {
	ctx := context.Background()

	t.Run("initial attempt on a visible database only selects it", func(t *testing.T) {
		conn, mock := preparerConn(t)
		expectDatabaseExistsProbe(mock, "beads", true)
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		b := &bootstrapPreparer{provider: &doltSQLProvider{serverEndpoint: "tcp:127.0.0.1:3306"}, database: "beads"}
		heal, err := b.prepare(ctx, conn)
		if err != nil {
			t.Fatalf("prepare() error = %v, want nil", err)
		}
		if heal != nil || b.heal != nil || b.created {
			t.Fatalf("prepare() heal = %p, b.heal = %p, created = %t; a visible database grants no authority", heal, b.heal, b.created)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("ordered SQL expectations (no CREATE DATABASE may be sent): %v", err)
		}
	})

	t.Run("retry on a visible database keeps the captured capability", func(t *testing.T) {
		conn, mock := preparerConn(t)
		expectDatabaseExistsProbe(mock, "beads", true)
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		prior := &schema.FreshBootstrapHealCapability{}
		b := &bootstrapPreparer{provider: &doltSQLProvider{serverEndpoint: "tcp:127.0.0.1:3306"}, database: "beads", created: true, heal: prior}
		heal, err := b.prepare(ctx, conn)
		if err != nil {
			t.Fatalf("prepare() error = %v, want nil", err)
		}
		if heal != prior || b.heal != prior || !b.created {
			t.Fatalf("prepare() heal = %p, b.heal = %p, created = %t; want the prior capability %p unchanged and created kept", heal, b.heal, b.created, prior)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("ordered SQL expectations (no CREATE DATABASE IF NOT EXISTS may be sent): %v", err)
		}
	})

	t.Run("invalid name is refused before any statement", func(t *testing.T) {
		conn, mock := preparerConn(t)
		b := &bootstrapPreparer{provider: &doltSQLProvider{}, database: "bad`name"}
		if _, err := b.prepare(ctx, conn); err == nil {
			t.Fatal("prepare() error = nil, want the identifier rejection")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	for _, tt := range []struct {
		name      string
		probeErr  error
		retryable bool
	}{
		{name: "probe serialization failure retries without creating", probeErr: &mysql.MySQLError{Number: 1213, Message: "deadlock"}, retryable: true},
		{name: "probe failure is permanent without creating", probeErr: errors.New("probe broke")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn, mock := preparerConn(t)
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.schemata`).
				WithArgs("beads").
				WillReturnError(tt.probeErr)

			b := &bootstrapPreparer{provider: &doltSQLProvider{}, database: "beads"}
			heal, err := b.prepare(ctx, conn)
			var prepErr *bootstrapPreparationError
			if !errors.As(err, &prepErr) {
				t.Fatalf("prepare() error = %T %v, want *bootstrapPreparationError", err, err)
			}
			if prepErr.retryable != tt.retryable {
				t.Fatalf("prepare() retryable = %t, want %t (error = %v)", prepErr.retryable, tt.retryable, err)
			}
			if !errors.Is(err, tt.probeErr) {
				t.Fatalf("prepare() error = %v, want it to wrap %v", err, tt.probeErr)
			}
			if heal != nil || b.created {
				t.Fatalf("prepare() heal = %p, created = %t after a failed probe", heal, b.created)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("ordered SQL expectations (a failed probe must not fall through to CREATE): %v", err)
			}
		})
	}

	t.Run("denied USE after a positive probe stays permanent", func(t *testing.T) {
		conn, mock := preparerConn(t)
		expectDatabaseExistsProbe(mock, "beads", true)
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnError(&mysql.MySQLError{Number: 1049, Message: "Unknown database 'beads'"})

		b := &bootstrapPreparer{provider: &doltSQLProvider{}, database: "beads"}
		_, err := b.prepare(ctx, conn)
		var prepErr *bootstrapPreparationError
		if !errors.As(err, &prepErr) || prepErr.retryable {
			t.Fatalf("prepare() error = %T %v, want a permanent *bootstrapPreparationError", err, err)
		}
		if !strings.Contains(err.Error(), "switching to database") {
			t.Fatalf("prepare() error = %v, want the USE failure", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}
