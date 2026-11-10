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
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
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

// expectIssuesProjection mocks the read-through's schema.CheckReadableDrift on
// a database at version, behind the binary: the cursor read, then the listing
// of issues, which carries every column the shared projection selects except
// omit (none when omit is empty).
func expectIssuesProjection(mock sqlmock.Sqlmock, version int, omit string) {
	expectVersionQuery(mock, version)
	expectCursorProbe(mock, "issues", true)
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	for _, column := range sqlbuild.ColumnNames(sqlbuild.IssueBaseColumns) {
		if column != omit {
			rows.AddRow(column, "text", "YES", "", nil, "")
		}
	}
	mock.ExpectQuery(`^SHOW COLUMNS FROM issues$`).WillReturnRows(rows)
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
		// The read-through: attach at the current schema, which the reads can
		// still query, and carry on. Still no migration statement anywhere.
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		expectIssuesProjection(mock, 1, "")

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

// TestReadThroughRefusedMigrationWarning pins the composed stderr of the
// proxied read-through (bd-mfsky, the uow leg of #6660). Every refusal the
// smart gate shaped — a non-empty Decision, or the #6575 data-behind stop —
// must lead with the gate's own UserMessage body, as embedded's
// lenientGateWarningBody and server mode's warnLenientOpenRefusal render it.
// Only the blunt fallback keeps the one-line summary plus the consent
// sentence. It drives the real function, so it pins what an operator reads.
func TestReadThroughRefusedMigrationWarning(t *testing.T) {
	const modeLine = "  Read-only command: continuing on schema v65 without migrating.\n"
	for _, tc := range []struct {
		name    string
		gateErr *schema.RemoteMigrateGateError
		// marker is text only this refusal's own body produces, so a
		// Decision literal that drifts from the schema constant lands on a
		// different body and fails here instead of passing vacuously.
		marker string
		// shaped rows must render UserMessage; the blunt fallback keeps
		// Error() and the consent sentence.
		shaped bool
		// consentVerbOK marks the rows whose text legitimately names the
		// bare consent verb: shared-no-remote has no remote, so that verb is
		// the consent there, and the blunt fallback's sentence is unchanged.
		consentVerbOK bool
		// forceOK marks data-behind, whose "Do NOT use `bd migrate --force`"
		// paragraph names the command it forbids.
		forceOK bool
	}{
		{
			name: "fork-skew",
			gateErr: &schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 70, Pending: 5,
				Decision: "fork-skew", SkewVersions: []int{66}, Shared: true, Proxied: true,
			},
			marker: "cannot un-fork it",
			shaped: true,
		},
		{
			name: "adopt",
			gateErr: &schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 70, Pending: 5,
				Decision: "adopt", Shared: true, Proxied: true,
			},
			marker: "adopt the remote's migrated database instead",
			shaped: true,
		},
		{
			name: "shared-no-remote",
			gateErr: &schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 70, Pending: 5,
				Decision: "shared-no-remote", Shared: true, Proxied: true,
			},
			marker:        "Upgrade bd on every client of this server first",
			shaped:        true,
			consentVerbOK: true,
		},
		{
			name: "data-behind",
			gateErr: &schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 70, Pending: 5,
				FallbackReason: "data-behind", DataDiverged: true, Shared: true, Proxied: true,
			},
			marker:  "Pull first",
			shaped:  true,
			forceOK: true,
		},
		{
			name: "blunt shared-store fallback",
			gateErr: &schema.RemoteMigrateGateError{
				CurrentVersion: 65, LatestVersion: 70, Pending: 5,
				FallbackReason: "shared-store", Shared: true, Proxied: true,
			},
			marker:        "Run '" + schema.SharedConsentCommand + "'",
			consentVerbOK: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if want := tc.name == "data-behind"; tc.gateErr.IsDataBehind() != want {
				t.Fatalf("IsDataBehind() = %v, want %v — the fixture's FallbackReason literal drifted", !want, want)
			}
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()
			ctx := context.Background()
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatalf("pin connection: %v", err)
			}
			defer conn.Close()
			mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
				WillReturnResult(sqlmock.NewResult(0, 0))
			expectIssuesProjection(mock, tc.gateErr.CurrentVersion, "")

			var readErr error
			stderr := captureStderr(t, func() {
				readErr = readThroughRefusedMigration(ctx, conn, "beads", tc.gateErr)
			})
			if readErr != nil {
				t.Fatalf("readThroughRefusedMigration() = %v, want nil — the read proceeds on the old schema", readErr)
			}
			if !strings.Contains(stderr, tc.marker) {
				t.Errorf("warning missing %q:\n%s", tc.marker, stderr)
			}
			// Body first, then the mode line, as on embedded: the shaped body
			// leads the warning rather than being replaced by the summary.
			lead := "Warning: " + tc.gateErr.Error() + "\n" + modeLine
			if tc.shaped {
				lead = "Warning: " + tc.gateErr.UserMessage() + modeLine
			}
			if !strings.HasPrefix(stderr, lead) {
				t.Errorf("warning does not open with\n%s\ngot:\n%s", lead, stderr)
			}
			if !strings.HasSuffix(stderr, "\n") {
				t.Errorf("warning is unterminated:\n%q", stderr)
			}
			if !tc.forceOK && strings.Contains(stderr, "bd migrate --force") {
				t.Errorf("warning prescribes `bd migrate --force`:\n%s", stderr)
			}
			// adopt and fork-skew are remote-backed, where the bare verb's
			// consent is never read (schema.SharedConsentCommandForced), and
			// their bodies say not to migrate at all. Scrub the forced forms
			// first: each contains the bare verb.
			scrubbed := strings.ReplaceAll(stderr, schema.SharedConsentCommandForcedGlobal, "")
			scrubbed = strings.ReplaceAll(scrubbed, schema.SharedConsentCommandForced, "")
			if !tc.consentVerbOK && strings.Contains(scrubbed, schema.SharedConsentCommand) {
				t.Errorf("warning prescribes the bare consent verb %q:\n%s", schema.SharedConsentCommand, stderr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestReadThroughRefusesASchemaItsReadsCannotQuery pins the read-through's
// limit. It serves reads at the current schema only while this binary's issue
// reads can query it; on a database that lacks a column the shared projection
// selects (arrival_seq, on one the previous release migrated) every read would
// fail on the unknown column. So the open refuses, permanently, with
// SchemaBehindError, and never prints that the read carries on.
func TestReadThroughRefusesASchemaItsReadsCannotQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()
	behind := schema.LatestVersion() - 1
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectIssuesProjection(mock, behind, "arrival_seq")

	gateErr := &schema.RemoteMigrateGateError{
		CurrentVersion: behind, LatestVersion: schema.LatestVersion(), Pending: 1,
		Decision: "shared-no-remote", Shared: true, Proxied: true,
	}
	var readErr error
	stderr := captureStderr(t, func() {
		readErr = readThroughRefusedMigration(ctx, conn, "beads", gateErr)
	})
	var permanent *backoff.PermanentError
	if !errors.As(readErr, &permanent) {
		t.Fatalf("readThroughRefusedMigration() = %T (%v), want a permanent refusal: a retry reads the same schema", readErr, readErr)
	}
	var behindErr *schema.SchemaBehindError
	if !errors.As(readErr, &behindErr) || behindErr.DBVersion != behind {
		t.Fatalf("readThroughRefusedMigration() = %v, want *schema.SchemaBehindError at v%d", readErr, behind)
	}
	if strings.Contains(stderr, "continuing on schema") {
		t.Errorf("the refused read-through still announced that the read carries on:\n%s", stderr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
