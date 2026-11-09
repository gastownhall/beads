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
	// The locked preparation probes visibility from inside the lock too
	// (gastownhall/beads#5079), and only then CREATEs what it cannot see.
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
	// The locked preparation probes visibility from inside the lock
	// (gastownhall/beads#5079): the database is visible there too, so no
	// CREATE form runs at all and this init captures no fresh-bootstrap heal
	// authority.
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

// newPreparerMock pins a fresh sqlmock connection and returns a preparer bound
// to it, so a test can drive bootstrapPreparer.prepare directly through the
// same statement stream MigrateUpWithLock would.
func newPreparerMock(t *testing.T) (*bootstrapPreparer, *sql.Conn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		t.Fatalf("pin mock connection: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = db.Close()
	})
	p := &doltSQLProvider{
		defaultBranch:  defaultBranch,
		serverEndpoint: "tcp:127.0.0.1:3306",
	}
	return &bootstrapPreparer{provider: p, database: "beads"}, conn, mock
}

// expectPreparationCapture mocks the fresh-bootstrap identity capture a
// successful bare CREATE triggers: the identity read, then the pristine-
// history and working-set checks.
func expectPreparationCapture(mock sqlmock.Sqlmock, database string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid, DOLT_HASHOF('HEAD')")).
		WillReturnRows(sqlmock.NewRows([]string{"database", "server_uuid", "head"}).AddRow(database, "server-uuid", "initial-head"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
}

// expectPreparationUse mocks the one statement the visible branch contributes.
func expectPreparationUse(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// assertPermanentClassification fails unless classifyInitSchemaError wraps err
// in backoff.Permanent, the contract every non-retryable preparation failure
// keeps.
func assertPermanentClassification(t *testing.T, err error) {
	t.Helper()
	var permanentErr *backoff.PermanentError
	if !errors.As(classifyInitSchemaError(err), &permanentErr) {
		t.Fatalf("classifyInitSchemaError(%v) is not permanent", err)
	}
}

// TestBootstrapPreparerProbesBeforeCreate is the red-to-green regression for
// gastownhall/beads#5079: locked bootstrap preparation must look before it
// leaps. A preparer that issues CREATE DATABASE unconditionally needs the
// global CREATE privilege on every open — even for a read of a database that
// already exists and is visible to the credential — so a least-privilege
// account is refused before the command it opened for ever runs.
//
// The probe-first contract, AC-1: a database the credential can see gets the
// visibility probe and the USE, never either CREATE form; the preparer keeps
// whatever creation state and heal capability it already carried; a probe
// that fails must not fall through to CREATE; an invalid name is rejected
// before anything reaches the wire.
func TestBootstrapPreparerProbesBeforeCreate(t *testing.T) {
	t.Run("visible database on the initial attempt issues no CREATE", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		expectDatabaseExistsProbe(mock, "beads", true)
		expectPreparationUse(mock)

		capability, err := prep.prepare(context.Background(), conn)
		if err != nil {
			t.Fatalf("prepare() error = %v, want nil: a visible database needs no CREATE (gastownhall/beads#5079)", err)
		}
		if capability != nil {
			t.Fatalf("prepare() capability = %v, want nil: a database this preparer did not create carries no fresh-bootstrap authority", capability)
		}
		if prep.created {
			t.Error("created = true; a visible database must not be recorded as created by this preparer")
		}
		if prep.heal != nil {
			t.Error("heal captured on a visible database; visibility must never establish ownership")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("ordered SQL expectations (no CREATE form may be sent): %v", err)
		}
	})

	t.Run("visible database on a retry preserves prior creation state and capability", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		// A previous attempt created the database and captured authority.
		expectDatabaseExistsProbe(mock, "beads", false)
		mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		expectPreparationUse(mock)
		expectPreparationCapture(mock, "beads")
		first, err := prep.prepare(context.Background(), conn)
		if err != nil {
			t.Fatalf("first prepare() error = %v, want nil", err)
		}
		if first == nil {
			t.Fatal("first prepare() capability = nil, want the captured authority")
		}

		// The retry finds the database visible: no sticky CREATE IF NOT
		// EXISTS either, and the original capability comes back unchanged.
		expectDatabaseExistsProbe(mock, "beads", true)
		expectPreparationUse(mock)
		second, err := prep.prepare(context.Background(), conn)
		if err != nil {
			t.Fatalf("retry prepare() error = %v, want nil", err)
		}
		if second != first {
			t.Fatalf("retry prepare() capability = %v, want the original %v unchanged", second, first)
		}
		if !prep.created {
			t.Error("created = false after the retry; the prior creation state must be preserved, not reset")
		}
		if prep.heal != first {
			t.Error("heal replaced on the retry; a visible database must not recapture authority")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("ordered SQL expectations (no CREATE form may be sent on the retry): %v", err)
		}
	})

	t.Run("failed probe does not fall through to CREATE, and retries on serialization", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.schemata`).
			WithArgs("beads").
			WillReturnError(&mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"})

		_, err := prep.prepare(context.Background(), conn)
		if err == nil {
			t.Fatal("prepare() error = nil, want the probe failure surfaced")
		}
		var serialization *mysql.MySQLError
		if !errors.As(err, &serialization) || serialization.Number != 1213 {
			t.Fatalf("prepare() error = %v, want the probe's serialization failure preserved", err)
		}
		var permanentErr *backoff.PermanentError
		if errors.As(classifyInitSchemaError(err), &permanentErr) {
			t.Fatalf("classifyInitSchemaError(%v) is permanent; a serialization failure must stay retryable", err)
		}
		// No CREATE was primed: a fall-through would surface as sqlmock's
		// unexpected-statement error, not the 1213 the assertions above hold.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
		if prep.created {
			t.Error("created = true after a failed probe; the probe must not mark anything created")
		}
	})

	t.Run("failed probe is permanent when the failure is not serialization", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.schemata`).
			WithArgs("beads").
			WillReturnError(errors.New("probe transport broke"))

		_, err := prep.prepare(context.Background(), conn)
		if err == nil || !strings.Contains(err.Error(), "probe transport broke") {
			t.Fatalf("prepare() error = %v, want the probe failure surfaced", err)
		}
		assertPermanentClassification(t, err)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations (no CREATE may follow a failed probe): %v", err)
		}
	})

	t.Run("invalid identifier is rejected before any statement", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		prep.database = "bad`name"

		_, err := prep.prepare(context.Background(), conn)
		if err == nil || !strings.Contains(err.Error(), "invalid identifier") {
			t.Fatalf("prepare() error = %v, want the identifier rejection", err)
		}
		assertPermanentClassification(t, err)
		// Nothing was primed, so any statement would have been unexpected and
		// would have replaced this error with sqlmock's own.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations (an invalid name must issue no SQL): %v", err)
		}
	})

	t.Run("a database that disappears after a positive probe fails the USE, permanently", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		expectDatabaseExistsProbe(mock, "beads", true)
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnError(&mysql.MySQLError{Number: 1049, Message: "Unknown database 'beads'"})

		_, err := prep.prepare(context.Background(), conn)
		if err == nil || !strings.Contains(err.Error(), "switching to database") {
			t.Fatalf("prepare() error = %v, want the USE failure surfaced", err)
		}
		assertPermanentClassification(t, err)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
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

// TestInitSchemaReadOnlyOpenOfExistingDatabaseIssuesNoCreateDatabase
// reproduces gastownhall/beads#5079: a read command over the proxied open path
// must not require global CREATE on a database that already exists. Here the
// pre-lock convergence probe proves the database exists (and puts the session
// on it), but declines because the cursor is behind this binary, so the locked
// path runs. A least-privilege (SELECT-only) credential cannot CREATE DATABASE,
// so any bare CREATE sent here fails the whole read before it starts; the
// correct behavior is to look before leaping (the direct stack's
// CreateIfMissing guard) and read through on the old schema.
//
// Adapted from the triage repro at the pipeline base: the locked preparation's
// own visibility probe — the statement this fix adds — is primed between
// GET_LOCK and the USE. The recorded-CREATE matcher and the zero-CREATE
// assertion are the repro's, unchanged.
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
	// Locked preparation, probe-first: existence is established from inside
	// the lock too, so no CREATE DATABASE is expected on this path — only the
	// probe and the USE.
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

// TestBootstrapPreparerBareCreateArbitrationUnchanged pins that the probe did
// not disturb the #5012 fresh-bootstrap ownership proof on an invisible
// database (AC-2): the bare CREATE still arbitrates creation atomically, only
// its success captures authority, an already-exists race proceeds without
// authority, and the creation-error classifications are exactly what they
// were.
func TestBootstrapPreparerBareCreateArbitrationUnchanged(t *testing.T) {
	t.Run("successful bare CREATE captures authority", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		expectDatabaseExistsProbe(mock, "beads", false)
		mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		expectPreparationUse(mock)
		expectPreparationCapture(mock, "beads")

		capability, err := prep.prepare(context.Background(), conn)
		if err != nil {
			t.Fatalf("prepare() error = %v, want nil", err)
		}
		if capability == nil {
			t.Fatal("prepare() capability = nil, want the captured authority: this attempt won the bare CREATE")
		}
		if !prep.created {
			t.Error("created = false after a successful bare CREATE")
		}
		if prep.heal != capability {
			t.Error("heal does not match the captured capability")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("already-exists race proceeds without authority", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		expectDatabaseExistsProbe(mock, "beads", false)
		mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
			WillReturnError(&mysql.MySQLError{Number: 1007, Message: "Can't create database 'beads'; database exists"})
		expectPreparationUse(mock)

		capability, err := prep.prepare(context.Background(), conn)
		if err != nil {
			t.Fatalf("prepare() error = %v, want nil: 1007 is the lost create race, not a failure", err)
		}
		if capability != nil {
			t.Fatal("prepare() capability = non-nil; a database the bare CREATE did not win carries no authority")
		}
		if prep.created {
			t.Error("created = true after losing the create race")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("bare CREATE serialization keeps its retryable classification", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		expectDatabaseExistsProbe(mock, "beads", false)
		mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
			WillReturnError(&mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"})

		_, err := prep.prepare(context.Background(), conn)
		if err == nil {
			t.Fatal("prepare() error = nil, want the CREATE failure surfaced")
		}
		var permanentErr *backoff.PermanentError
		if errors.As(classifyInitSchemaError(err), &permanentErr) {
			t.Fatalf("classifyInitSchemaError(%v) is permanent; only the bare CREATE keeps the serialization retry", err)
		}
		if !prep.created {
			// Not an assertion of the contract, just a sanity check that the
			// failed CREATE did not record creation.
			t.Log("created stays false after the failed CREATE")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("other bare CREATE failures stay permanent", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		expectDatabaseExistsProbe(mock, "beads", false)
		mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
			WillReturnError(errors.New("Error 1105 (HY000): the server is read-only"))

		_, err := prep.prepare(context.Background(), conn)
		if err == nil || !strings.Contains(err.Error(), "the server is read-only") {
			t.Fatalf("prepare() error = %v, want the CREATE failure surfaced", err)
		}
		assertPermanentClassification(t, err)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("a failed capture stays failed: no recapture on a later visible attempt", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		// The bare CREATE won, but the identity capture failed.
		expectDatabaseExistsProbe(mock, "beads", false)
		mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		expectPreparationUse(mock)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid, DOLT_HASHOF('HEAD')")).
			WillReturnError(errors.New("capture transport broke"))
		_, err := prep.prepare(context.Background(), conn)
		if err == nil || !strings.Contains(err.Error(), "capture") {
			t.Fatalf("prepare() error = %v, want the capture failure surfaced", err)
		}
		assertPermanentClassification(t, err)

		// The retry finds the database its own CREATE left behind: visible, so
		// no CREATE and no capture. Authority stays unissued, and the open can
		// still complete without it.
		expectDatabaseExistsProbe(mock, "beads", true)
		expectPreparationUse(mock)
		capability, err := prep.prepare(context.Background(), conn)
		if err != nil {
			t.Fatalf("retry prepare() error = %v, want nil: a visible database must not need a capture to open", err)
		}
		if capability != nil {
			t.Fatal("retry prepare() capability = non-nil; a failed capture must never be retried on a later attempt")
		}
		if !prep.created {
			t.Error("created = false; the prior CREATE's creation state must survive the retry")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations (the retry must not re-run the capture): %v", err)
		}
	})
}

// TestBootstrapPreparerStickyRecreationReturnsOriginalCapability pins AC-3: a
// preparer that previously won the bare CREATE and captured a capability, on a
// later locked attempt that finds the database no longer visible, re-asserts
// availability with CREATE DATABASE IF NOT EXISTS and returns the ORIGINAL
// capability — never capturing authority over a replacement incarnation.
func TestBootstrapPreparerStickyRecreationReturnsOriginalCapability(t *testing.T) {
	prep, conn, mock := newPreparerMock(t)
	// Attempt 1: create and capture.
	expectDatabaseExistsProbe(mock, "beads", false)
	mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectPreparationUse(mock)
	expectPreparationCapture(mock, "beads")
	original, err := prep.prepare(context.Background(), conn)
	if err != nil {
		t.Fatalf("first prepare() error = %v, want nil", err)
	}
	if original == nil {
		t.Fatal("first prepare() capability = nil, want the captured authority")
	}

	// Attempt 2: the database was dropped in between (a concurrent
	// clean-databases), so the probe cannot see it. Availability is restored
	// with the sticky CREATE IF NOT EXISTS — which is not the bare CREATE and
	// must not capture anything.
	expectDatabaseExistsProbe(mock, "beads", false)
	mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE IF NOT EXISTS `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectPreparationUse(mock)
	returned, err := prep.prepare(context.Background(), conn)
	if err != nil {
		t.Fatalf("sticky prepare() error = %v, want nil", err)
	}
	if returned != original {
		t.Fatalf("sticky prepare() capability = %v, want the original %v: authority over a replacement incarnation must never be inferred from CREATE IF NOT EXISTS", returned, original)
	}
	if prep.heal != original {
		t.Error("heal replaced on the sticky re-creation")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (the sticky re-creation must not capture): %v", err)
	}
}

// TestBootstrapPreparerStickyCreateFailuresStayPermanent pins the unchanged
// classifications of the sticky branch: its CREATE IF NOT EXISTS failure and
// the USE after it are permanent, exactly as before the probe.
func TestBootstrapPreparerStickyCreateFailuresStayPermanent(t *testing.T) {
	t.Run("sticky CREATE failure is permanent", func(t *testing.T) {
		prep, conn, mock := newPreparerMock(t)
		prep.created = true
		expectDatabaseExistsProbe(mock, "beads", false)
		mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE IF NOT EXISTS `beads`")).
			WillReturnError(&mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"})

		_, err := prep.prepare(context.Background(), conn)
		if err == nil {
			t.Fatal("prepare() error = nil, want the sticky CREATE failure")
		}
		assertPermanentClassification(t, err)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}

// TestBootstrapPreparerDeniedCreateReportsBothPossibilities pins AC-4. After a
// negative probe, a denied CREATE cannot distinguish a database that is not
// provisioned from one this credential has not been granted, so the refusal
// must name both possibilities and the administrator remedy — not the old
// blanket claim that the server provisions databases server-side, and not a
// re-run-init instruction an existing-database reader cannot act on.
func TestBootstrapPreparerDeniedCreateReportsBothPossibilities(t *testing.T) {
	for _, tc := range []struct {
		name string
		deny error
	}{
		{name: "database-scoped denial", deny: &mysql.MySQLError{Number: 1044, Message: "Access denied for user 'reader'@'%' to database 'beads'"}},
		{name: "command denial", deny: &mysql.MySQLError{Number: 1142, Message: "CREATE command denied to user 'reader'@'%'"}},
		{name: "dolt generic 1105 command denial", deny: &mysql.MySQLError{Number: 1105, Message: "command denied to user 'reader'@'%'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prep, conn, mock := newPreparerMock(t)
			expectDatabaseExistsProbe(mock, "beads", false)
			mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
				WillReturnError(tc.deny)

			_, err := prep.prepare(context.Background(), conn)
			if err == nil {
				t.Fatal("prepare() error = nil, want the refusal")
			}
			for _, want := range []string{
				`database "beads" is not visible to this credential and creating it was denied`,
				"either the database is not provisioned on the server or this credential has not been granted access to it",
				"ask the server administrator to provision the database and grant access",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal missing %q:\n%s", want, err)
				}
			}
			for _, forbidden := range []string{"provisions databases server-side", "re-run init"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("refusal still claims %q:\n%s", forbidden, err)
				}
			}
			var mysqlErr *mysql.MySQLError
			if !errors.As(err, &mysqlErr) || mysqlErr != tc.deny {
				t.Fatalf("prepare() error = %v, want the underlying driver error preserved", err)
			}
			assertPermanentClassification(t, err)
			// Only the probe and the CREATE were primed: no USE and no
			// CREATE IF NOT EXISTS fallback may follow the refusal.
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations (a denial must not fall back): %v", err)
			}
		})
	}
}
