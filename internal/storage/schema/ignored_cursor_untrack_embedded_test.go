//go:build cgo

package schema

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	embedded "github.com/dolthub/driver/v2"
	mysql "github.com/go-sql-driver/mysql"
)

type cursorRecoveryDB struct {
	*sql.DB
	connector *embedded.Connector
}

func (db *cursorRecoveryDB) Close() error {
	return errors.Join(db.DB.Close(), db.connector.Close())
}

func openCursorRecoveryDB(t *testing.T, dir string, create bool) *cursorRecoveryDB {
	t.Helper()
	cfg := embedded.Config{
		Directory: dir, CommitName: "migration test", CommitEmail: "test@example.com",
		MultiStatements: true,
	}
	if !create {
		cfg.Database = "cursor_recovery"
	}
	connector, err := embedded.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := &cursorRecoveryDB{sql.OpenDB(connector), connector}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if create {
		if _, err := db.Exec("CREATE DATABASE cursor_recovery"); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		return openCursorRecoveryDB(t, dir, false)
	}
	return db
}

// interruptCursorRestore executes real SQL, then stops the restore immediately
// after the selected durable statement, before the next restore step.
type interruptCursorRestore struct {
	DBConn
	interrupted error
	after       string
}

func (db interruptCursorRestore) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := db.DBConn.ExecContext(ctx, query, args...)
	if err == nil && strings.HasPrefix(query, db.after) {
		return result, db.interrupted
	}
	return result, err
}

func TestIgnoredCursorRestoreInterruptedAfterCreate(t *testing.T) {
	testInterruptedCursorRestore(t, "CREATE TABLE", false)
}

func TestIgnoredCursorRestoreInterruptedBeforeCleanup(t *testing.T) {
	for _, after := range []string{"INSERT IGNORE INTO", "RENAME TABLE"} {
		t.Run(after, func(t *testing.T) {
			for _, tracked := range []bool{false, true} {
				name := "untracked_scratch"
				if tracked {
					name = "tracked_scratch"
				}
				t.Run(name, func(t *testing.T) { testInterruptedCursorRestore(t, after, tracked) })
			}
		})
	}
}

func testInterruptedCursorRestore(t *testing.T, after string, tracked bool) {
	dir := t.TempDir()
	db := openCursorRecoveryDB(t, dir, true)
	ctx := context.Background()
	for _, query := range []string{
		ignoredCursorScratch.bootstrapSQL(),
		"INSERT INTO dolt_ignore VALUES ('ignored_schema_migrations', true), ('local_metadata', true), ('wisp_%', true)",
		"CREATE TABLE wisps (id INT PRIMARY KEY)",
		"CREATE TABLE wisp_dependencies (id INT PRIMARY KEY)",
		"CREATE TABLE leases (id INT PRIMARY KEY, granted_node TEXT)",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	for _, version := range []int{1, LatestIgnoredVersion()} {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+ignoredCursorUntrackTempTable+
			" (version, applied_at, content_hash) VALUES (?, '2026-09-01 12:00:00', ?)",
			version, strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
	}
	interrupted := errors.New("interrupted after " + after)
	err := restoreIgnoredCursorRows(ctx, interruptCursorRestore{db, interrupted, after})
	if !errors.Is(err, interrupted) {
		t.Fatalf("restore error = %v, want injected interruption", err)
	}
	if tracked {
		if present, err := schemaTableExists(ctx, db, "wisp_ignored_schema_migrations_restore"); err != nil {
			t.Fatal(err)
		} else if present {
			// Model residue that predates the ignore entry (or was force-added).
			if err := DrainCall(ctx, db, "CALL DOLT_ADD('-f', 'wisp_ignored_schema_migrations_restore')"); err != nil {
				t.Fatal(err)
			}
		}
		if err := DrainCall(ctx, db, "CALL DOLT_ADD('-A')"); err != nil {
			t.Fatal(err)
		}
		if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'fixture: incidental commit of recovery tables')"); err != nil {
			t.Fatal(err)
		}
	}
	// Close the embedded engine, then reopen the on-disk fixture. No synthetic SQL
	// results decide whether the incomplete cursor is mistaken for a live one.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openCursorRecoveryDB(t, dir, false)
	if healed, err := healTrackedIgnoredCursorTable(ctx, db); err != nil || !healed {
		t.Fatalf("reopened heal = %v, %v", healed, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE "+
		"applied_at = '2026-09-01 12:00:00' AND content_hash = ?", strings.Repeat("a", 64)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("restored cursor rows = %d, want 2 with original timestamps and hashes", count)
	}
	if pending, err := PendingIgnoredVersions(ctx, db); err != nil || len(pending) != 0 {
		t.Fatalf("migration replay after recovery: pending=%v, error=%v", pending, err)
	}
	if scratch, err := schemaTableExists(ctx, db, ignoredCursorUntrackTempTable); err != nil || scratch {
		t.Fatalf("scratch remains after recovery: %v, %v", scratch, err)
	}
	if tracked, err := tableTrackedAtHead(ctx, db, "", ignoredCursorUntrackTempTable); err != nil || tracked {
		t.Fatalf("scratch remains at HEAD: %v, %v", tracked, err)
	}
	if staging, err := schemaTableExists(ctx, db, "wisp_ignored_schema_migrations_restore"); err != nil || staging {
		t.Fatalf("staging table remains after recovery: %v, %v", staging, err)
	}
	if tracked, err := tableTrackedAtHead(ctx, db, "", "wisp_ignored_schema_migrations_restore"); err != nil || tracked {
		t.Fatalf("staging table remains at HEAD: %v, %v", tracked, err)
	}
}

type commitDuringCursorRestore struct{ DBConn }

func (db commitDuringCursorRestore) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := db.DBConn.ExecContext(ctx, query, args...)
	if err == nil && strings.HasPrefix(query, "INSERT IGNORE INTO wisp_ignored_schema_migrations_restore") {
		if err := DrainCall(ctx, db.DBConn, "CALL DOLT_ADD('-A')"); err != nil {
			return result, err
		}
		err = DrainCall(ctx, db.DBConn, "CALL DOLT_COMMIT('-m', 'fixture: concurrent blanket commit')")
	}
	return result, err
}

// A blanket commit DURING the same restore must not turn its final rename into
// tracked dirt. Cleaning residue only at startup misses this window.
func TestIgnoredCursorRestoreConcurrentCommit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openCursorRecoveryDB(t, dir, true)
	if _, err := seedDoltIgnorePatterns(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, ignoredCursorScratch.bootstrapSQL()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+ignoredCursorUntrackTempTable+" (version) VALUES (1), (2)"); err != nil {
		t.Fatal(err)
	}
	if err := restoreIgnoredCursorRows(ctx, commitDuringCursorRestore{db}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openCursorRecoveryDB(t, dir, false)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations").Scan(&count); err != nil || count != 2 {
		t.Fatalf("cursor rows after reopen = %d, %v; want 2", count, err)
	}
	for _, table := range []string{ignoredSource.cursorTable, ignoredCursorUntrackTempTable, "wisp_ignored_schema_migrations_restore"} {
		if tracked, err := tableTrackedAtHead(ctx, db, "", table); err != nil || tracked {
			t.Fatalf("%s remains tracked after recovery: %v, %v", table, tracked, err)
		}
	}
	if dirty, err := committableDirtyTables(ctx, db); err != nil || len(dirty) != 0 {
		t.Fatalf("recovery left committable dirt: %v, %v", dirty, err)
	}
}

// Missing or vetoed staging ignore state must be checked in the advisory zone,
// while the tracked live cursor is still usable by a restricted client.
func TestIgnoredCursorHealDeclinesBeforeDropWithoutStagingIgnore(t *testing.T) {
	for _, overridden := range []bool{false, true} {
		name := "missing_pattern"
		if overridden {
			name = "explicit_override"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := openCursorRecoveryDB(t, t.TempDir(), true)
			for _, query := range []string{
				ignoredSource.bootstrapSQL(),
				"INSERT INTO ignored_schema_migrations (version) VALUES (1)",
				"INSERT INTO dolt_ignore VALUES ('ignored_schema_migrations', true)",
			} {
				if _, err := db.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			if overridden {
				if _, err := db.ExecContext(ctx, "INSERT INTO dolt_ignore VALUES ('wisp_ignored_schema_migrations_restore', false)"); err != nil {
					t.Fatal(err)
				}
			}
			if err := DrainCall(ctx, db, "CALL DOLT_ADD('-f', 'ignored_schema_migrations')"); err != nil {
				t.Fatal(err)
			}
			if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'fixture: tracked legacy cursor')"); err != nil {
				t.Fatal(err)
			}
			var client DBConn = db
			if !overridden {
				client = denyCursorStagingSeed{db}
			}
			healed, err := healTrackedIgnoredCursorTable(ctx, client)
			if err != nil || healed {
				t.Fatalf("unavailable staging ignore must decline before dropping the cursor: healed=%v, err=%v", healed, err)
			}
			var rows int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 1").Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("original cursor was changed: rows=%d, err=%v", rows, err)
			}
		})
	}
}

// A crash after DROP but before its Dolt commit leaves the cursor tracked at
// HEAD and absent from the working set. An operator's staging ignore override
// must defer the next migration pass without publishing an empty live cursor.
func TestMigrateUpDefersAfterCursorDropWithStagingOverride(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openCursorRecoveryDB(t, dir, true)
	if _, err := MigrateUp(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE ignored_schema_migrations SET applied_at = '2026-09-01 12:00:00'"); err != nil {
		t.Fatal(err)
	}
	var expected int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations").Scan(&expected); err != nil || expected == 0 {
		t.Fatalf("fixture cursor rows = %d, err=%v", expected, err)
	}
	if err := DrainCall(ctx, db, "CALL DOLT_ADD('-f', 'ignored_schema_migrations')"); err != nil {
		t.Fatal(err)
	}
	if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'fixture: tracked legacy cursor')"); err != nil {
		t.Fatal(err)
	}
	if err := backupIgnoredCursorRows(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		ignoredCursorRestore.bootstrapSQL(),
		"INSERT INTO " + ignoredCursorRestoreTable + " SELECT * FROM ignored_schema_migrations",
		"INSERT INTO dolt_ignore VALUES ('wisp_ignored_schema_migrations_restore', false)",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	interrupted := errors.New("interrupted after cursor DROP before commit")
	if err := commitIgnoredCursorUntrack(ctx, interruptCursorRestore{db, interrupted, "DROP TABLE"}); !errors.Is(err, interrupted) {
		t.Fatalf("untrack error = %v, want injected interruption", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openCursorRecoveryDB(t, dir, false)
	if tracked, err := tableTrackedAtHead(ctx, db, "", ignoredSource.cursorTable); err != nil || !tracked {
		t.Fatalf("fixture cursor tracked at HEAD = %v, err=%v", tracked, err)
	}
	if present, err := schemaTableExists(ctx, db, ignoredSource.cursorTable); err != nil || present {
		t.Fatalf("fixture live cursor present = %v, err=%v", present, err)
	}
	if ignored, err := tableActivelyIgnored(ctx, db, "", ignoredCursorRestoreTable); err != nil || ignored {
		t.Fatalf("fixture staging override ignored = %v, err=%v", ignored, err)
	}
	if applied, err := MigrateUp(ctx, db); !errors.Is(err, ErrIgnoredCursorRestoreDeferred) || applied != 0 {
		t.Errorf("open with staging override must defer migration: applied=%d, err=%v", applied, err)
	}
	if present, err := schemaTableExists(ctx, db, ignoredSource.cursorTable); err != nil || present {
		t.Errorf("deferred restore bootstrapped a live cursor: present=%v, err=%v", present, err)
	}
	for _, table := range []string{ignoredCursorUntrackTempTable, ignoredCursorRestoreTable} {
		var preserved int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE applied_at = '2026-09-01 12:00:00'").Scan(&preserved); err != nil || preserved != expected {
			t.Errorf("%s saved cursor rows = %d, want %d; err=%v", table, preserved, expected, err)
		}
	}
	if t.Failed() {
		return
	}
	// Once the operator removes the override, the next open resumes from the
	// saved rows instead of replaying migrations and replacing their timestamps.
	if _, err := db.ExecContext(ctx, "DELETE FROM dolt_ignore WHERE pattern = 'wisp_ignored_schema_migrations_restore'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openCursorRecoveryDB(t, dir, false)
	if _, err := MigrateUp(ctx, db); err != nil {
		t.Fatalf("reopen after removing override: %v", err)
	}
	var preserved int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE applied_at = '2026-09-01 12:00:00'").Scan(&preserved); err != nil || preserved != expected {
		t.Fatalf("cursor replayed after staging override: original rows=%d, want %d; err=%v", preserved, expected, err)
	}
}

// Once the cursor deletion is committed, an override on the restore namespace
// or table must preserve a usable scratch cursor until restoration is allowed.
func TestMigrateUpDefersAfterCursorDeletionCommitWithStagingOverride(t *testing.T) {
	for _, pattern := range []string{"wisp_%", "wisp_ignored_schema_migrations_restore"} {
		t.Run(pattern, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			db := openCursorRecoveryDB(t, dir, true)
			if _, err := MigrateUp(ctx, db); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, "UPDATE ignored_schema_migrations SET applied_at = '2026-09-01 12:00:00'"); err != nil {
				t.Fatal(err)
			}
			var expected int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations").Scan(&expected); err != nil || expected == 0 {
				t.Fatalf("fixture cursor rows = %d, err=%v", expected, err)
			}
			if err := DrainCall(ctx, db, "CALL DOLT_ADD('-f', 'ignored_schema_migrations')"); err != nil {
				t.Fatal(err)
			}
			if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'fixture: tracked legacy cursor')"); err != nil {
				t.Fatal(err)
			}
			if err := backupIgnoredCursorRows(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err := commitIgnoredCursorUntrack(ctx, db); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, "REPLACE INTO dolt_ignore (pattern, ignored) VALUES (?, false)", pattern); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db = openCursorRecoveryDB(t, dir, false)
			if tracked, err := tableTrackedAtHead(ctx, db, "", ignoredSource.cursorTable); err != nil || tracked {
				t.Fatalf("fixture cursor tracked at HEAD = %v, err=%v", tracked, err)
			}
			if ignored, err := tableActivelyIgnored(ctx, db, "", ignoredCursorRestoreTable); err != nil || ignored {
				t.Fatalf("fixture staging override ignored = %v, err=%v", ignored, err)
			}
			if applied, err := MigrateUp(ctx, db); !errors.Is(err, ErrIgnoredCursorRestoreDeferred) || applied != 0 {
				t.Errorf("open after committed deletion must defer migration: applied=%d, err=%v", applied, err)
			}
			if present, err := schemaTableExists(ctx, db, ignoredSource.cursorTable); err != nil || present {
				t.Errorf("deferred restore bootstrapped a live cursor: present=%v, err=%v", present, err)
			}
			var preserved int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+ignoredCursorUntrackTempTable+" WHERE applied_at = '2026-09-01 12:00:00'").Scan(&preserved); err != nil || preserved != expected {
				t.Errorf("saved cursor rows = %d, want %d; err=%v", preserved, expected, err)
			}
			if t.Failed() {
				return
			}
			if _, err := db.ExecContext(ctx, "DELETE FROM dolt_ignore WHERE pattern = ?", pattern); err != nil {
				t.Fatal(err)
			}
			// Removing the namespace override restores its original ignore row;
			// the exact-table override had no row before the operator added it.
			if pattern == "wisp_%" {
				if _, err := db.ExecContext(ctx, "INSERT INTO dolt_ignore VALUES ('wisp_%', true)"); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db = openCursorRecoveryDB(t, dir, false)
			if _, err := MigrateUp(ctx, db); err != nil {
				t.Fatalf("reopen after removing override: %v", err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE applied_at = '2026-09-01 12:00:00'").Scan(&preserved); err != nil || preserved != expected {
				t.Fatalf("cursor replayed after staging override: original rows=%d, want %d; err=%v", preserved, expected, err)
			}
			for _, table := range []string{ignoredCursorUntrackTempTable, ignoredCursorRestoreTable} {
				if present, err := schemaTableExists(ctx, db, table); err != nil || present {
					t.Errorf("%s remains after recovery: present=%v, err=%v", table, present, err)
				}
			}
		})
	}
}

type denyCursorStagingSeed struct{ DBConn }

func (db denyCursorStagingSeed) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(query, "INSERT IGNORE INTO dolt_ignore") {
		return nil, &mysql.MySQLError{Number: 1142, Message: "INSERT command denied for dolt_ignore"}
	}
	return db.DBConn.ExecContext(ctx, query, args...)
}

func TestHealthyCursorOpenDoesNotSeedRepairStaging(t *testing.T) {
	for _, restricted := range []bool{true, false} {
		name := "staged_user_edits"
		if restricted {
			name = "restricted_client"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := openCursorRecoveryDB(t, t.TempDir(), true)
			if _, err := MigrateUp(ctx, db); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{
				"DELETE FROM dolt_ignore WHERE pattern = 'wisp_ignored_schema_migrations_restore'",
				"CREATE TABLE user_edits (id INT PRIMARY KEY, value INT)",
				"INSERT INTO user_edits VALUES (1, 10)",
			} {
				if _, err := db.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			if err := DrainCall(ctx, db, "CALL DOLT_ADD('-A')"); err != nil {
				t.Fatal(err)
			}
			if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'fixture: previous ignore set')"); err != nil {
				t.Fatal(err)
			}
			var client DBConn = db
			if restricted {
				client = denyCursorStagingSeed{db}
			} else {
				if _, err := db.ExecContext(ctx, "UPDATE user_edits SET value = 20 WHERE id = 1"); err != nil {
					t.Fatal(err)
				}
				if err := DrainCall(ctx, db, "CALL DOLT_ADD('user_edits')"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := MigrateUp(ctx, client); err != nil {
				t.Fatalf("healthy open tried to seed repair-only state: %v", err)
			}
			var committed int
			if err := db.QueryRowContext(ctx, "SELECT value FROM user_edits AS OF 'HEAD' WHERE id = 1").Scan(&committed); err != nil || committed != 10 {
				t.Fatalf("healthy open committed user edits: value=%d, err=%v", committed, err)
			}
		})
	}
}

func TestCursorRestoreDoesNotCommitStagedUserEdits(t *testing.T) {
	ctx := context.Background()
	db := openCursorRecoveryDB(t, t.TempDir(), true)
	if _, err := seedDoltIgnorePatterns(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		ignoredCursorScratch.bootstrapSQL(),
		"INSERT INTO " + ignoredCursorUntrackTempTable + " (version) VALUES (1)",
		"CREATE TABLE user_edits (id INT PRIMARY KEY, value INT)",
		"INSERT INTO user_edits VALUES (1, 10)",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := DrainCall(ctx, db, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatal(err)
	}
	if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'fixture: before user edits')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE user_edits SET value = 20 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if err := DrainCall(ctx, db, "CALL DOLT_ADD('user_edits')"); err != nil {
		t.Fatal(err)
	}
	if err := restoreIgnoredCursorRows(ctx, db); err != nil {
		t.Fatal(err)
	}
	var committed, working int
	if err := db.QueryRowContext(ctx, "SELECT value FROM user_edits AS OF 'HEAD' WHERE id = 1").Scan(&committed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT value FROM user_edits WHERE id = 1").Scan(&working); err != nil {
		t.Fatal(err)
	}
	if committed != 10 || working != 20 {
		t.Fatalf("repair changed user edits: HEAD=%d working=%d; want 10, 20", committed, working)
	}
}

type denyCursorRename struct{ DBConn }

func (db denyCursorRename) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(query, "RENAME TABLE wisp_ignored_schema_migrations_restore") {
		return nil, &mysql.MySQLError{Number: 1142, Message: "ALTER command denied for cursor staging"}
	}
	return db.DBConn.ExecContext(ctx, query, args...)
}

func TestMigrateUpDefersAfterCursorRenameDenied(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openCursorRecoveryDB(t, dir, true)
	if _, err := MigrateUp(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE ignored_schema_migrations SET applied_at = '2026-09-01 12:00:00'"); err != nil {
		t.Fatal(err)
	}
	var expected int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations").Scan(&expected); err != nil {
		t.Fatal(err)
	}
	if err := backupIgnoredCursorRows(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE ignored_schema_migrations"); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateUp(ctx, denyCursorRename{db}); !errors.Is(err, ErrIgnoredCursorRestoreDeferred) {
		t.Fatalf("restricted open must decline migration work: %v", err)
	}
	if present, err := schemaTableExists(ctx, db, ignoredSource.cursorTable); err != nil || present {
		t.Fatalf("declined restore bootstrapped a live cursor: present=%v, err=%v", present, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openCursorRecoveryDB(t, dir, false)
	if _, err := MigrateUp(ctx, db); err != nil {
		t.Fatalf("privileged reopen: %v", err)
	}
	var preserved int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE applied_at = '2026-09-01 12:00:00'").Scan(&preserved); err != nil || preserved != expected {
		t.Fatalf("cursor replayed after permission decline: original rows=%d, want %d; err=%v", preserved, expected, err)
	}
	if pending, err := ignoredSource.pendingVersions(ctx, db); err != nil || len(pending) != 0 {
		t.Fatalf("pending migrations after privileged reopen = %v, %v", pending, err)
	}
}

// A first-time repair must discover missing restoration grants while the live
// cursor is still present, not after committing its deletion into HEAD.
func TestIgnoredCursorFirstRepairDeclinesBeforeDropWhenRenameDenied(t *testing.T) {
	ctx := context.Background()
	db := openCursorRecoveryDB(t, t.TempDir(), true)
	for _, query := range []string{
		ignoredSource.bootstrapSQL(),
		"INSERT INTO ignored_schema_migrations (version, applied_at) VALUES (1, '2026-09-01 12:00:00')",
		"INSERT INTO dolt_ignore VALUES ('ignored_schema_migrations', true), ('wisp_%', true)",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := DrainCall(ctx, db, "CALL DOLT_ADD('-f', 'ignored_schema_migrations')"); err != nil {
		t.Fatal(err)
	}
	if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'fixture: tracked legacy cursor')"); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := db.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&before); err != nil {
		t.Fatal(err)
	}
	healed, err := healTrackedIgnoredCursorTable(ctx, denyCursorRename{db})
	if err != nil || healed {
		t.Errorf("first repair without RENAME must decline: healed=%v, err=%v", healed, err)
	}
	if present, err := schemaTableExists(ctx, db, ignoredSource.cursorTable); err != nil || !present {
		t.Errorf("first repair deleted the live cursor: present=%v, err=%v", present, err)
	} else {
		var preserved int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE applied_at = '2026-09-01 12:00:00'").Scan(&preserved); err != nil || preserved != 1 {
			t.Errorf("live cursor rows=%d, err=%v; want original row", preserved, err)
		}
	}
	if err := db.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("first repair committed a deletion: HEAD moved from %s to %s", before, after)
	}
}
