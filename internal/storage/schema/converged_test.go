package schema

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/debug"
)

// expectConvergedFastPathMiss walks the REAL pre-lock probe on the shape the
// proxied CLI actually presents — a pinned session with no database selected —
// and declines at the first schema predicate: the database exists, the session
// is put on it, and the main cursor turns out to be one migration behind, so
// the locked path runs exactly as it did before the probe existed.
//
// It deliberately does NOT decline at statement 1. An earlier version of this
// helper primed DATABASE() to NULL and stopped there, which meant every
// retrofitted locked-path test in lock_test.go skipped the probe entirely —
// and the probe's inability to fire on that very shape (uow's schema-init pool
// opens with an EMPTY DSN database) went unnoticed through review.
func expectConvergedFastPathMiss(mock sqlmock.Sqlmock, database string) {
	expectSessionPutOnDatabase(mock, database)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion()-1)
}

// testDatabaseSelector stands in for the selector uow injects: it issues the
// same USE through the same connection and returns the same quoted name, so
// the mocked statement stream matches production byte for byte.
var testDatabaseSelector DatabaseSelector = func(ctx context.Context, conn DBConn, database string) (string, error) {
	quoted := "`" + database + "`"
	if _, err := conn.ExecContext(ctx, "USE "+quoted); err != nil {
		return "", err
	}
	return quoted, nil
}

// qualifiedDoltIgnore is the table expression the probe reads after a selector
// put the session on database; unqualifiedDoltIgnore is what it reads when the
// session was already there and nothing had to be selected.
func qualifiedDoltIgnore(database string) string {
	return "`" + database + "`.dolt_ignore"
}

const unqualifiedDoltIgnore = "dolt_ignore"

// expectCurrentDatabase mocks the fast path's opening question: which database
// is this pinned session actually on?
func expectCurrentDatabase(mock sqlmock.Sqlmock, name any) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(name))
}

// expectDatabaseExistsProbe mocks the always-succeeding existence probe that
// must precede any USE (be-bv7x: a failing statement pins a Dolt session to
// its pre-statement catalog snapshot for the rest of its pooled life).
func expectDatabaseExistsProbe(mock sqlmock.Sqlmock, name string, exists bool) {
	n := 0
	if exists {
		n = 1
	}
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.schemata`).
		WithArgs(name).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
}

// expectSessionPutOnDatabase mocks the whole database-selection preamble on
// the hot-path shape: nothing selected, the database exists, USE it.
func expectSessionPutOnDatabase(mock sqlmock.Sqlmock, name string) {
	expectCurrentDatabase(mock, nil)
	expectDatabaseExistsProbe(mock, name, true)
	mock.ExpectExec(regexp.QuoteMeta("USE `" + name + "`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectNoMigrationWorkNeeded mocks migrationWorkNeeded on a fully upgraded
// database: both cursors at latest, both content_hash columns present, no
// custom-status/type backfill pending.
func expectNoMigrationWorkNeeded(mock sqlmock.Sqlmock) {
	expectNoMigrationWorkNeededAtVersion(mock, LatestVersion())
}

// expectNoMigrationWorkNeededAtVersion is the same, with the main cursor
// reporting mainVersion (which may be ahead of this binary's latest).
func expectNoMigrationWorkNeededAtVersion(mock sqlmock.Sqlmock, mainVersion int) {
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", mainVersion)
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	expectIgnoredSentinelProbes(mock, true)
	expectContentHashColumnExists(mock)
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
}

// expectDoltIgnoreRead mocks the read-only dolt_ignore probe, returning
// exactly the patterns given. The read is schema-qualified (dolt_ignore is a
// Dolt system table and is not listed in information_schema.tables, so it
// cannot carry the cursor tables' existence probe) and takes no cursor read of
// its own: migrationWorkNeeded has already proved the main cursor is at or
// past this binary's latest, which settles every version gate.
func expectDoltIgnoreRead(mock sqlmock.Sqlmock, table string, patterns []string) {
	rows := sqlmock.NewRows([]string{"pattern"})
	for _, pattern := range patterns {
		rows.AddRow(pattern)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM " + table)).
		WillReturnRows(rows)
}

// expectMigrationLockProbe mocks the fast path's LAST predicate: is anyone
// holding the database-scoped migration lock right now?
func expectMigrationLockProbe(mock sqlmock.Sqlmock, database string, free any) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT IS_FREE_LOCK(?)")).
		WithArgs(MigrationLockName(database)).
		WillReturnRows(sqlmock.NewRows([]string{"free"}).AddRow(free))
}

// seededIgnorePatterns is every pattern seedDoltIgnorePatterns would assert on
// a database whose main cursor is at mainVersion.
func seededIgnorePatterns(mainVersion int) []string {
	patterns := append([]string(nil), doltIgnorePatterns...)
	for _, gated := range versionGatedDoltIgnorePatterns {
		if mainVersion >= gated.minMainVersion {
			patterns = append(patterns, gated.pattern)
		}
	}
	return patterns
}

// expectConvergedProbe mocks the whole fast path on a converged database whose
// session is already on it: nothing is selected, so the dolt_ignore read runs
// unqualified against the session database DATABASE() just confirmed.
func expectConvergedProbe(mock sqlmock.Sqlmock, database string) {
	expectCurrentDatabase(mock, database)
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectMigrationLockProbe(mock, database, 1)
}

func newMockConn(t *testing.T) (*sql.Conn, sqlmock.Sqlmock, func()) {
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
	return conn, mock, func() {
		conn.Close()
		db.Close()
	}
}

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, mock
}

// TestMigrateUpWithLockSkipsLockWhenAlreadyConverged is the point of the whole
// change: on a database that needs nothing, no GET_LOCK is issued at all. The
// mock is primed with the probe and nothing else, so a GET_LOCK — or the
// caller's locked preparation, whose CREATE DATABASE the fast path also skips
// — would surface as an unexpected statement and fail the call.
func TestMigrateUpWithLockSkipsLockWhenAlreadyConverged(t *testing.T) {
	conn, mock, cleanup := newMockConn(t)
	defer cleanup()

	expectConvergedProbe(mock, "testdb")

	prepared := 0
	applied, err := MigrateUpWithLock(context.Background(), conn, "testdb",
		WithLockedPreparation("tcp:test", func(context.Context, *sql.Conn) (*FreshBootstrapHealCapability, error) {
			prepared++
			return nil, nil
		}))
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v, want nil (converged database must short-circuit before GET_LOCK)", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	if prepared != 0 {
		t.Fatalf("locked preparation ran %d times on a converged database, want 0", prepared)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpWithLockSkipsLockOnAnUnselectedSession is the regression test
// for the reason this fast path did not work: uow.openAndInitSchema opens the
// schema-init pool with an EMPTY DSN database and only issues USE after
// GET_LOCK, so on every production seat's path DATABASE() was NULL when the
// probe ran. A probe that merely ASKS whether the session is on the target
// database therefore declined every single time and the lock was taken exactly
// as before — the whole change was a no-op on the only path that mattered.
//
// The probe must instead establish the database through the injected
// selector: prove it exists with a query that cannot fail, USE it, and only
// then evaluate the schema predicates — reading dolt_ignore under the name the
// selector quoted.
func TestMigrateUpWithLockSkipsLockOnAnUnselectedSession(t *testing.T) {
	conn, mock, cleanup := newMockConn(t)
	defer cleanup()

	expectSessionPutOnDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, qualifiedDoltIgnore("testdb"), seededIgnorePatterns(LatestVersion()))
	expectMigrationLockProbe(mock, "testdb", 1)

	prepared := 0
	applied, err := MigrateUpWithLock(context.Background(), conn, "testdb",
		WithDatabaseSelector(testDatabaseSelector),
		WithLockedPreparation("tcp:test", func(context.Context, *sql.Conn) (*FreshBootstrapHealCapability, error) {
			prepared++
			return nil, nil
		}))
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v, want nil", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	if prepared != 0 {
		t.Fatalf("locked preparation ran %d times, want 0", prepared)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (an unselected session must reach the probe, not decline at DATABASE()): %v", err)
	}
}

// TestMigrateUpWithLockTakesLockWhenBehind pins the other half of the
// contract: a database one migration behind still goes through GET_LOCK and
// the full pass. Without this, a fast path that answered "converged"
// unconditionally would still pass the test above.
func TestMigrateUpWithLockTakesLockWhenBehind(t *testing.T) {
	conn, mock, cleanup := newMockConn(t)
	defer cleanup()

	// Fast path: right database, but the main cursor is behind -> work needed.
	expectCurrentDatabase(mock, "testdb")
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion()-1)

	lockName := MigrationLockName("testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	expectOnePendingMigration(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(context.Background(), conn, "testdb")
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpWithLockKeepsLockWithBootstrapHeal pins that a caller carrying
// fresh-bootstrap reset authority never enters the fast path: the very first
// statement it issues is GET_LOCK, so the #5012 bootstrap sequence is
// byte-identical to what it was before the probe existed.
func TestMigrateUpWithLockKeepsLockWithBootstrapHeal(t *testing.T) {
	conn, mock, cleanup := newMockConn(t)
	defer cleanup()

	lockName := MigrationLockName("testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	expectDirtyGuardRefusal(t, mock)
	expectFreshBootstrapIdentityMatch(mock)
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET('--hard')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	expectOnePendingMigration(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(context.Background(), conn, "testdb",
		WithFreshBootstrapHeal(testFreshBootstrapHealCapability(), testBootstrapEndpoint))
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestAlreadyConvergedFallsThroughOnAMissingDatabase is the fresh-bootstrap
// case. The database does not exist yet, so the probe must decline WITHOUT
// issuing the USE that would fail — a failing statement pins a Dolt session to
// its pre-statement catalog snapshot for the rest of its pooled life (be-bv7x)
// — and hand the open to the locked path, whose bare CREATE DATABASE
// arbitrates creation and issues the #5012 heal capability.
func TestAlreadyConvergedFallsThroughOnAMissingDatabase(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, nil)
	expectDatabaseExistsProbe(mock, "testdb", false)

	converged, err := alreadyConverged(context.Background(), db, "testdb", testDatabaseSelector)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v", err)
	}
	if converged {
		t.Fatal("alreadyConverged() = true on a database that does not exist, want false")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (a missing database must decline before USE, not after): %v", err)
	}
}

// TestAlreadyConvergedWithoutASelectorNeverSelects pins the default for
// callers that inject nothing. The probe has no legal way to issue USE on its
// own — selecting a database and quoting an identifier belong to the DDL
// repository, which this package cannot import without breaking that package's
// test build — so a session that is not already on the target is simply
// declined, exactly as it was before the selector existed. That is the whole
// behavior for internal/storage/dolt, whose pool DSN already names the
// database; the mock is primed with nothing after DATABASE(), so an existence
// probe or a USE would surface as an unexpected statement.
func TestAlreadyConvergedWithoutASelectorNeverSelects(t *testing.T) {
	for _, tt := range []struct {
		name    string
		current any
	}{
		{name: "no database selected", current: nil},
		{name: "different database", current: "otherdb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMockDB(t)

			expectCurrentDatabase(mock, tt.current)

			converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
			if err != nil {
				t.Fatalf("alreadyConverged() error = %v", err)
			}
			if converged {
				t.Fatal("alreadyConverged() = true without a selector and off the target database, want false")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations (an uninjected probe must not probe or select): %v", err)
			}
		})
	}
}

// TestAlreadyConvergedFailsClosedOnDatabaseSelection covers the ways putting
// the session on the target database can go wrong. All must surface as "not
// converged" so MigrateUpWithLock takes the lock.
func TestAlreadyConvergedFailsClosedOnDatabaseSelection(t *testing.T) {
	t.Run("empty database name", func(t *testing.T) {
		db, mock := newMockDB(t)

		converged, err := alreadyConverged(context.Background(), db, "", nil)
		if err != nil {
			t.Fatalf("alreadyConverged() error = %v", err)
		}
		if converged {
			t.Fatal("alreadyConverged() = true with no database name, want false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations (an empty name must issue no statements at all): %v", err)
		}
	})

	t.Run("current database unreadable", func(t *testing.T) {
		db, mock := newMockDB(t)

		mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
			WillReturnError(sql.ErrConnDone)

		converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
		if err == nil {
			t.Fatal("alreadyConverged() error = nil, want the DATABASE() read failure")
		}
		if converged {
			t.Fatal("alreadyConverged() = true on an unreadable session, want false")
		}
	})

	t.Run("selector's use fails", func(t *testing.T) {
		db, mock := newMockDB(t)

		expectCurrentDatabase(mock, nil)
		expectDatabaseExistsProbe(mock, "testdb", true)
		mock.ExpectExec(regexp.QuoteMeta("USE `testdb`")).
			WillReturnError(errors.New("database dropped underneath us"))

		converged, err := alreadyConverged(context.Background(), db, "testdb", testDatabaseSelector)
		if err == nil {
			t.Fatal("alreadyConverged() error = nil, want the USE failure")
		}
		if converged {
			t.Fatal("alreadyConverged() = true after a failed USE, want false")
		}
	})

	t.Run("selector rejects the name", func(t *testing.T) {
		db, mock := newMockDB(t)

		expectCurrentDatabase(mock, nil)
		expectDatabaseExistsProbe(mock, "bad`name", true)
		rejecting := DatabaseSelector(func(context.Context, DBConn, string) (string, error) {
			return "", errors.New("invalid identifier")
		})

		converged, err := alreadyConverged(context.Background(), db, "bad`name", rejecting)
		if err == nil {
			t.Fatal("alreadyConverged() error = nil, want the identifier rejection")
		}
		if converged {
			t.Fatal("alreadyConverged() = true on a name the selector refused, want false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations (a refused name must issue no USE): %v", err)
		}
	})

	// A selector that reports success without handing back a quoted name would
	// leave the dolt_ignore read to guess at qualification. Nothing downstream
	// may run on that: refuse it here.
	t.Run("selector returns no quoted name", func(t *testing.T) {
		db, mock := newMockDB(t)

		expectCurrentDatabase(mock, nil)
		expectDatabaseExistsProbe(mock, "testdb", true)
		silent := DatabaseSelector(func(context.Context, DBConn, string) (string, error) {
			return "", nil
		})

		converged, err := alreadyConverged(context.Background(), db, "testdb", silent)
		if err == nil {
			t.Fatal("alreadyConverged() error = nil, want the empty-name refusal")
		}
		if converged {
			t.Fatal("alreadyConverged() = true on a selector that quoted nothing, want false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}

// TestAlreadyConvergedDeclinesWhileTheMigrationLockIsHeld is the containment
// for the mutual-exclusion hole. migrationWorkNeeded only covers the FRONT
// half of MigrateUp: once the version cursors and content_hash columns have
// landed it reports "nothing pending" while the pass is still running
// backfills, dependency/aux PK rekeys, the ignored series, and the schema
// commit. Every predicate above can therefore be satisfied by a database that
// a peer is actively rewriting — a fleet-wide binary roll is exactly that
// window — so a held migration lock must veto the fast path.
func TestAlreadyConvergedDeclinesWhileTheMigrationLockIsHeld(t *testing.T) {
	for _, tt := range []struct {
		name string
		free any
	}{
		{name: "lock held by a peer", free: 0},
		{name: "server will not say", free: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMockDB(t)

			expectCurrentDatabase(mock, "testdb")
			expectNoMigrationWorkNeeded(mock)
			expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
			expectMigrationLockProbe(mock, "testdb", tt.free)

			converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
			if err != nil {
				t.Fatalf("alreadyConverged() error = %v", err)
			}
			if converged {
				t.Fatal("alreadyConverged() = true while the migration lock is not provably free, want false: a peer may be mid-pass rewriting tables this caller is about to read")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestAlreadyConvergedProbesTheLockLast pins the ordering the containment is
// cheap because of: the lock probe is the LAST term, so the ordinary
// not-converged answers cost nothing extra. A database one migration behind
// must never reach IS_FREE_LOCK at all — the mock is not primed for it, so a
// reordered probe surfaces as an unexpected statement.
func TestAlreadyConvergedProbesTheLockLast(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion()-1)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v", err)
	}
	if converged {
		t.Fatal("alreadyConverged() = true with the main cursor behind, want false")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestAlreadyConvergedRejectsUnderSeededDoltIgnore is the case
// seedDoltIgnorePatterns exists for: an out-of-band-materialized database
// arrives with its cursors at-latest and the ignore patterns missing. The fast
// path must refuse it so the locked pass can heal and commit the seed.
func TestAlreadyConvergedRejectsUnderSeededDoltIgnore(t *testing.T) {
	for _, missing := range seededIgnorePatterns(LatestVersion()) {
		t.Run(missing, func(t *testing.T) {
			db, mock := newMockDB(t)

			var present []string
			for _, pattern := range seededIgnorePatterns(LatestVersion()) {
				if pattern != missing {
					present = append(present, pattern)
				}
			}
			expectCurrentDatabase(mock, "testdb")
			expectNoMigrationWorkNeeded(mock)
			expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, present)

			converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
			if err != nil {
				t.Fatalf("alreadyConverged() error = %v", err)
			}
			if converged {
				t.Fatalf("alreadyConverged() = true with dolt_ignore pattern %q missing, want false", missing)
			}
		})
	}
}

// TestDoltIgnoreSeededQualifiesOnlyWhatWasSelected pins where the read's
// database name comes from. When a selector put the session on the database it
// hands back the quoted name and the read states it; when the session was
// already there, DATABASE() itself was the proof and the read is
// session-scoped. The probe never re-derives or re-quotes a name of its own —
// that rule is what let the domain/db import go.
func TestDoltIgnoreSeededQualifiesOnlyWhatWasSelected(t *testing.T) {
	for _, tt := range []struct {
		name      string
		qualifier string
		table     string
	}{
		{name: "selected", qualifier: "`testdb`", table: qualifiedDoltIgnore("testdb")},
		{name: "already on the database", qualifier: "", table: unqualifiedDoltIgnore},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			expectDoltIgnoreRead(mock, tt.table, seededIgnorePatterns(LatestVersion()))

			seeded, err := doltIgnoreSeeded(context.Background(), db, tt.qualifier, mainSource.latest())
			if err != nil {
				t.Fatalf("doltIgnoreSeeded() error = %v", err)
			}
			if !seeded {
				t.Fatal("doltIgnoreSeeded() = false on a fully seeded database, want true")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestDoltIgnoreSeededHonorsTheVersionGate pins both sides of the gate a
// version-gated pattern carries. Below its flip migration the pattern is not
// expected at all (seedDoltIgnorePatterns would not insert it either, and
// asserting it on a pre-flip database would strand a still-tracked table
// behind a suppressing pattern); at or past it, its absence is a miss.
func TestDoltIgnoreSeededHonorsTheVersionGate(t *testing.T) {
	for _, gated := range versionGatedDoltIgnorePatterns {
		t.Run(gated.pattern, func(t *testing.T) {
			ungated := append([]string(nil), doltIgnorePatterns...)

			t.Run("below the gate", func(t *testing.T) {
				db, mock := newMockDB(t)
				expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, ungated)

				seeded, err := doltIgnoreSeeded(context.Background(), db, "", gated.minMainVersion-1)
				if err != nil {
					t.Fatalf("doltIgnoreSeeded() error = %v", err)
				}
				if !seeded {
					t.Fatalf("doltIgnoreSeeded() = false below the %q gate (main version %d < %d), want true: the flip migration has not run, so the pattern is not expected yet",
						gated.pattern, gated.minMainVersion-1, gated.minMainVersion)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet SQL expectations (the gate must be settled without a cursor re-read): %v", err)
				}
			})

			t.Run("at the gate", func(t *testing.T) {
				db, mock := newMockDB(t)
				expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, ungated)

				seeded, err := doltIgnoreSeeded(context.Background(), db, "", gated.minMainVersion)
				if err != nil {
					t.Fatalf("doltIgnoreSeeded() error = %v", err)
				}
				if seeded {
					t.Fatalf("doltIgnoreSeeded() = true with %q missing at main version %d, want false", gated.pattern, gated.minMainVersion)
				}
			})
		})
	}
}

// TestAlreadyConvergedAcceptsOverriddenIgnorePattern pins that presence is
// judged on the pattern alone. An operator override (the row exists with
// ignored=false) is what INSERT IGNORE would leave untouched, so treating it
// as missing would send every invocation down the locked path forever — the
// saturation this change removes.
func TestAlreadyConvergedAcceptsOverriddenIgnorePattern(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	// The probe selects patterns only; the mock never offers the ignored
	// column, so a query that filtered on it would not match this expectation.
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectMigrationLockProbe(mock, "testdb", 1)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v", err)
	}
	if !converged {
		t.Fatal("alreadyConverged() = false on a fully seeded database, want true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestAlreadyConvergedOnForwardSkew documents — rather than changes —
// what the fast path does when the database is AHEAD of this binary. MigrateUp
// itself treats "cursor >= my latest" as nothing to do and returns (0, nil)
// without ever consulting the lock, so the fast path returning converged is
// the same answer for the same reason. Forward drift is a separate question,
// asked by CheckForwardDrift at the caller (cmd/bd/main.go), and this probe
// must not start answering it: doing so here would turn a diagnosable skew
// error into an unexplained lock acquisition on every invocation.
func TestAlreadyConvergedOnForwardSkew(t *testing.T) {
	db, mock := newMockDB(t)

	ahead := LatestVersion() + 5
	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeededAtVersion(mock, ahead)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(ahead))
	expectMigrationLockProbe(mock, "testdb", 1)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v", err)
	}
	if !converged {
		t.Fatal("alreadyConverged() = false on a forward-skewed database, want true: MigrateUp's own gate reports no work at or past latest, so the locked pass would return (0, nil) too")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestAlreadyConvergedFailsClosedOnUnreadableState pins the fail-closed
// contract from both sides. A cursor probe that errors is swallowed by
// atLatest into "work needed", so the answer is a plain not-converged; a
// dolt_ignore read that errors surfaces the error. MigrateUpWithLock treats
// anything but (true, nil) as "take the lock", so both end at the locked path.
func TestAlreadyConvergedFailsClosedOnUnreadableState(t *testing.T) {
	t.Run("cursor probe fails", func(t *testing.T) {
		db, mock := newMockDB(t)

		expectCurrentDatabase(mock, "testdb")
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.tables`).
			WillReturnError(sql.ErrConnDone)

		converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
		if err != nil {
			t.Fatalf("alreadyConverged() error = %v", err)
		}
		if converged {
			t.Fatal("alreadyConverged() = true on an unreadable cursor, want false")
		}
	})

	t.Run("dolt_ignore read fails", func(t *testing.T) {
		db, mock := newMockDB(t)

		expectCurrentDatabase(mock, "testdb")
		expectNoMigrationWorkNeeded(mock)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore")).
			WillReturnError(sql.ErrConnDone)

		converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
		if err == nil {
			t.Fatal("alreadyConverged() error = nil, want the dolt_ignore read failure")
		}
		if converged {
			t.Fatal("alreadyConverged() = true on a failed dolt_ignore read, want false")
		}
	})

	t.Run("lock probe fails", func(t *testing.T) {
		db, mock := newMockDB(t)

		expectCurrentDatabase(mock, "testdb")
		expectNoMigrationWorkNeeded(mock)
		expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT IS_FREE_LOCK(?)")).
			WillReturnError(sql.ErrConnDone)

		converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
		if err == nil {
			t.Fatal("alreadyConverged() error = nil, want the lock probe failure")
		}
		if converged {
			t.Fatal("alreadyConverged() = true on a failed lock probe, want false")
		}
	})

}

// TestMigrateUpWithLockLogsAnUnavailableFastPath pins the diagnostic on the
// one branch that is otherwise invisible. The probe is advisory and fails
// closed, so a probe that has silently stopped working looks exactly like a
// probe that correctly declined — and the symptom (every seat back on the
// saturated GET_LOCK) shows up nowhere near the cause. Discarding the error
// without a word is what made the unreachable-fast-path defect survive
// review; say so where BD_DEBUG and -v can see it.
func TestMigrateUpWithLockLogsAnUnavailableFastPath(t *testing.T) {
	conn, mock, cleanup := newMockConn(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnError(errors.New("probe transport broke"))
	lockName := MigrationLockName("testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WillReturnError(errors.New("stop here"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	logged := captureStderr(t, func() {
		debug.SetVerbose(true)
		defer debug.SetVerbose(false)
		if _, err := MigrateUpWithLock(context.Background(), conn, "testdb"); err == nil {
			t.Fatal("MigrateUpWithLock() error = nil, want the locked path's failure")
		}
	})

	if !strings.Contains(logged, "convergence fast path unavailable") ||
		!strings.Contains(logged, "probe transport broke") {
		t.Fatalf("debug output = %q, want the discarded probe error reported", logged)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (a broken probe must still fail closed onto the lock): %v", err)
	}
}

// captureStderr redirects os.Stderr — where debug.Logf writes — for the
// duration of fn and returns what was written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	func() {
		defer func() {
			os.Stderr = saved
			w.Close()
		}()
		fn()
	}()
	return <-done
}
