package schema

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestRepairPartialNonlocalRegistrationClearsStuckRows covers the stuck state:
// the cursor stopped at 39 because migration 0040 committed rows into
// dolt_nonlocal_tables and then lost its connection before the version was
// recorded. The repair must clear those rows so 0040 can replay.
func TestRepairPartialNonlocalRegistrationClearsStuckRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectScalar(mock, "SELECT COUNT(*) FROM dolt_nonlocal_tables", "count", 1)
	mock.ExpectExec(`DELETE FROM dolt_nonlocal_tables`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The delete must be committed, not merely applied: 0040 re-inserts the
	// same rows, so an uncommitted delete leaves the working set identical to
	// HEAD and 0040's own DOLT_COMMIT fails with "nothing to commit".
	mock.ExpectExec(`CALL DOLT_ADD\('dolt_nonlocal_tables'\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CALL DOLT_COMMIT\('-m', 'schema: clear partially applied nonlocal table registrations'\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := mainSource.repairPartialNonlocalRegistration(context.Background(), db, 39, LatestVersion()); err != nil {
		t.Fatalf("repairPartialNonlocalRegistration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRepairPartialNonlocalRegistrationNoOpWhenEmpty locks the healthy-database
// case: a fresh database is below 0040 too, but its dolt_nonlocal_tables is
// empty, so the repair must probe and then write nothing.
func TestRepairPartialNonlocalRegistrationNoOpWhenEmpty(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectScalar(mock, "SELECT COUNT(*) FROM dolt_nonlocal_tables", "count", 0)

	if err := mainSource.repairPartialNonlocalRegistration(context.Background(), db, 0, LatestVersion()); err != nil {
		t.Fatalf("repairPartialNonlocalRegistration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRepairPartialNonlocalRegistrationNoOpAtOrPastVersion40 locks the case that
// matters for existing healthy databases: once the cursor has recorded 0040, the
// rows in dolt_nonlocal_tables are the real registrations, and the repair must
// not so much as look at them. Any SQL issued here would be a bug — sqlmock
// fails the test on an unexpected statement.
func TestRepairPartialNonlocalRegistrationNoOpAtOrPastVersion40(t *testing.T) {
	for _, current := range []int{40, 41, LatestVersion()} {
		t.Run(strconv.Itoa(current), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			if err := mainSource.repairPartialNonlocalRegistration(context.Background(), db, current, LatestVersion()); err != nil {
				t.Fatalf("repairPartialNonlocalRegistration: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// TestRepairPartialNonlocalRegistrationNoOpBelowTarget covers the bounded
// MigrateUpTo path: if this pass will not reach 0040, there is nothing to clear
// the way for.
func TestRepairPartialNonlocalRegistrationNoOpBelowTarget(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	if err := mainSource.repairPartialNonlocalRegistration(context.Background(), db, 10, 39); err != nil {
		t.Fatalf("repairPartialNonlocalRegistration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRepairPartialNonlocalRegistrationSkipsIgnoredSource keeps the repair on
// the main cursor. The ignored source has its own numbering, in which 40 means
// nothing.
func TestRepairPartialNonlocalRegistrationSkipsIgnoredSource(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	if err := ignoredSource.repairPartialNonlocalRegistration(context.Background(), db, 0, LatestVersion()); err != nil {
		t.Fatalf("repairPartialNonlocalRegistration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestMigration0040ReplaysOnlyAfterNonlocalRepair is the end-to-end shape of the
// bug, on a real Dolt database. It builds the stuck state exactly — migrations
// through 0039 recorded, and 0040's first INSERT committed but its version never
// written — then shows that replaying 0040 fails with the duplicate primary key
// that bricks the database, and that it applies cleanly once the repair has run.
func TestMigration0040ReplaysOnlyAfterNonlocalRepair(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "stuck-0040")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create stuck dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, migrationsSQLUpTo(t, nonlocalRegistrationVersion-1))

	// Migration 0040 got its first INSERT and DOLT_COMMIT in, then lost the
	// connection: the row is durable, the version is not.
	runDoltSQL(t, dir,
		"INSERT INTO dolt_nonlocal_tables (table_name, target_ref, options) VALUES ('wisps', 'main', 'immediate');"+
			"CALL DOLT_COMMIT('-Am', 'create nonlocal table wisps');")

	if rows := queryDoltCSV(t, dir, "SELECT COALESCE(MAX(version), 0) AS v FROM schema_migrations"); len(rows) != 1 ||
		rows[0]["v"] != strconv.Itoa(nonlocalRegistrationVersion-1) {
		t.Fatalf("stuck-state cursor = %v, want %d", rows, nonlocalRegistrationVersion-1)
	}

	migration0040 := migrationSQLForVersion(t, nonlocalRegistrationVersion)

	// Without the repair, the replay is the brick.
	if out, err := tryDoltSQL(dir, migration0040); err == nil {
		t.Fatalf("replaying migration %04d on the stuck state unexpectedly succeeded; the repro no longer reproduces",
			nonlocalRegistrationVersion)
	} else if !strings.Contains(out, "duplicate primary key") {
		t.Fatalf("replaying migration %04d failed with %q, want a duplicate primary key error", nonlocalRegistrationVersion, out)
	}

	// A bare delete is not enough: 0040 restores exactly the row that was
	// removed, so the working set matches HEAD and 0040's own DOLT_COMMIT
	// fails. This is the second brick the repair has to avoid.
	if out, err := tryDoltSQL(dir, "DELETE FROM dolt_nonlocal_tables;"+migration0040); err == nil {
		t.Fatalf("replaying migration %04d after an uncommitted delete unexpectedly succeeded", nonlocalRegistrationVersion)
	} else if !strings.Contains(out, "nothing to commit") {
		t.Fatalf("replaying migration %04d after an uncommitted delete failed with %q, want a nothing-to-commit error",
			nonlocalRegistrationVersion, out)
	}

	// The repair issues exactly these statements.
	runDoltSQL(t, dir, "DELETE FROM dolt_nonlocal_tables;"+
		"CALL DOLT_ADD('dolt_nonlocal_tables');"+
		"CALL DOLT_COMMIT('-m', 'schema: clear partially applied nonlocal table registrations');")

	if out, err := tryDoltSQL(dir, migration0040); err != nil {
		t.Fatalf("replaying migration %04d after the repair failed: %v\nOutput: %s", nonlocalRegistrationVersion, err, out)
	}

	rows := queryDoltCSV(t, dir, "SELECT table_name FROM dolt_nonlocal_tables ORDER BY table_name")
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r["table_name"])
	}
	want := "local_metadata,repo_mtimes,wisp_*,wisps"
	if strings.Join(got, ",") != want {
		t.Fatalf("dolt_nonlocal_tables after repaired replay = %v, want %s", got, want)
	}
}

// migrationsSQLUpTo renders the bootstrap plus every main migration through
// maxVersion, in the `dolt sql` dialect, with each version recorded exactly as
// migrate would record it.
func migrationsSQLUpTo(t *testing.T, maxVersion int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(mainSource.bootstrapSQL())
	b.WriteString(";\n")
	for _, f := range mainSource.list() {
		if f.version > maxVersion {
			break
		}
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + f.name)
		if err != nil {
			t.Fatalf("read migration %s: %v", f.name, err)
		}
		b.WriteString(cliCompatibleMigrationSQL(f.name, string(data)))
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "\nINSERT IGNORE INTO %s (version, content_hash) VALUES (%d, '%s');\n",
			mainSource.cursorTable, f.version, hex.EncodeToString(sum[:]))
	}
	return b.String()
}

func migrationSQLForVersion(t *testing.T, version int) string {
	t.Helper()
	for _, f := range mainSource.list() {
		if f.version != version {
			continue
		}
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + f.name)
		if err != nil {
			t.Fatalf("read migration %s: %v", f.name, err)
		}
		return cliCompatibleMigrationSQL(f.name, string(data))
	}
	t.Fatalf("no main migration at version %d", version)
	return ""
}

// tryDoltSQL runs a statement batch and returns its combined output alongside
// the error, so a test can assert on a failure instead of aborting on it.
func tryDoltSQL(dir, query string) (string, error) {
	cmd := exec.Command("dolt", "sql", "-q", query)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	return string(output), err
}
