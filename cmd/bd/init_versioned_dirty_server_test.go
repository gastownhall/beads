//go:build cgo

package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// The #6089 adoption tests: bd init against a server database whose working
// set is dirty. The unversioned shape (legacy v1.0.0-era seeder residue, no
// main migration cursor) is adopted by committing the working set first; the
// versioned shape keeps the #4566 refusal — an unversioned adoption must not
// become a blanket license to commit over a versioned database's
// half-applied concurrent work.

// runInitServerBD runs the bd binary under test in repoDir with env, keeping
// stdout and stderr separate so the recovery note (#6089's stderr line) can
// be asserted independently of the JSON document init prints on stdout.
func runInitServerBD(t *testing.T, repoDir string, env []string, args ...string) (stdout, stderr string, exitErr error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, buildBDUnderTest(t), args...)
	cmd.Dir = repoDir
	cmd.Env = env
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		t.Fatalf("bd %s did not finish before the deadline: %v\n%s",
			strings.Join(args, " "), ctxErr, stderrBuf.String())
	}
	return stdoutBuf.String(), stderrBuf.String(), err
}

// initServerTestArgs is bd init against the suite's test Dolt server with
// an explicitly named database: the --external route the reporter's
// `gt install` shape uses (a server the caller manages), with the fixture
// work skipped so only the store adoption is under test.
func initServerTestArgs(database string, extra ...string) []string {
	args := []string{
		"init", "--server", "--external",
		"--server-host", "127.0.0.1",
		"--server-port", fmt.Sprintf("%d", testDoltServerPort),
		"--database", database,
		"--prefix", "hq",
		"--skip-hooks", "--skip-agents",
	}
	return append(args, extra...)
}

// openInitServerTestDB opens a raw *sql.DB to database on the suite's test
// server, for fixture setup and direct-SQL assertions.
func openInitServerTestDB(t *testing.T, database string) *sql.DB {
	t.Helper()
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: database}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", database, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// createInitServerTestDatabase creates database on the suite's test server
// and returns a connection to it. The caller owns dropping it
// (dropTestDatabase).
func createInitServerTestDatabase(t *testing.T, database string) *sql.DB {
	t.Helper()
	adminDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root"}.String()
	admin, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+database+"`"); err != nil {
		t.Fatalf("create database %s: %v", database, err)
	}
	return openInitServerTestDB(t, database)
}

// initServerTestCommit commits everything in database with the given
// message, the way the v1.0.0-era SDK's callers did.
func initServerTestCommit(t *testing.T, db *sql.DB, message string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', ?, '--author', 'Beads Test <beads@example.com>')", message,
	); err != nil {
		t.Fatalf("commit %q: %v", message, err)
	}
}

// initServerTestDirtyTables reads dolt_status into a set of dirty table
// names (staged or not).
func initServerTestDirtyTables(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	dirty := map[string]bool{}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		dirty[table] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	return dirty
}

// initServerTestRecoveryCommitCount counts Dolt commits carrying the fixed
// #6089 adoption message.
func initServerTestRecoveryCommitCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = ?",
		initUnversionedRecoveryCommitMessage).Scan(&count); err != nil {
		t.Fatalf("count recovery commits: %v", err)
	}
	return count
}

// initServerTestMainCursor reads the recorded main migration cursor
// (0 for an absent or empty cursor table).
func initServerTestMainCursor(t *testing.T, db *sql.DB) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var exists bool
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) > 0 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'schema_migrations'",
	).Scan(&exists); err != nil {
		t.Fatalf("probe schema_migrations: %v", err)
	}
	if !exists {
		return 0
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	return version
}

// TestCLI_InitServerVersionedDirtyWorkingSetStillRefuses_E2E pins the
// dirty-data guard the #6089 adoption must not weaken: a VERSIONED database
// (recorded main cursor 51) with an uncommitted issues row is refused by
// init — before and after the unversioned adoption fix alike — with no
// labeled recovery commit and the dirty row left uncommitted. Migration
// consent is granted (BD_ALLOW_MIGRATE=1) so the refusal under test is the
// #4566 dirty-table guard, not the earlier consent gate.
func TestCLI_InitServerVersionedDirtyWorkingSetStillRefuses_E2E(t *testing.T) {
	skipIfNoDolt(t)
	// Consent for BOTH gates: BD_ALLOW_MIGRATE unlocks the migration-consent
	// gate, BD_ALLOW_REMOTE_MIGRATE the #5920 shared-store gate that fires
	// first on the suite's shared test server, so the refusal under test is
	// the #4566 dirty-table guard alone.
	env := append(externalServerTestEnv(t), "BD_ALLOW_MIGRATE=1", "BD_ALLOW_REMOTE_MIGRATE=1")

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
	db := createInitServerTestDatabase(t, database)

	// A committed v51 baseline, then one uncommitted issues row: the
	// shape of a concurrent writer's half-applied work on a database with
	// a real migration history.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := schema.MigrateUpTo(ctx, db, 51); err != nil {
		t.Fatalf("migrate %s to v51: %v", database, err)
	}
	initServerTestCommit(t, db, "test: v51 baseline")
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES (?, ?, '', '', '', '')",
		database+"-dirty-1", "uncommitted concurrent row",
	); err != nil {
		t.Fatalf("dirty issues table: %v", err)
	}

	_, stderr, err := runInitServerBD(t, repoDir, env, initServerTestArgs(database)...)
	if err == nil {
		t.Fatalf("bd init adopted a versioned dirty database (want the #4566 refusal)\n%s", stderr)
	}
	if !strings.Contains(stderr, "pending schema migrations alter pre-existing dirty tables") {
		t.Fatalf("bd init stderr = %q, want the dirty-table refusal", stderr)
	}
	if !strings.Contains(stderr, "issues") {
		t.Fatalf("bd init stderr = %q, want the dirty table named", stderr)
	}
	if got := initServerTestRecoveryCommitCount(t, db); got != 0 {
		t.Fatalf("recovery commits = %d, want 0 on a refused versioned database", got)
	}
	if !initServerTestDirtyTables(t, db)["issues"] {
		t.Fatal("the uncommitted issues row must stay uncommitted after the refusal")
	}
	if got := initServerTestMainCursor(t, db); got != 51 {
		t.Fatalf("main cursor = %d, want 51 (the refusal must not migrate)", got)
	}
}
