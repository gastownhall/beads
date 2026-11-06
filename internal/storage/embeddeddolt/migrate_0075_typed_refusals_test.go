//go:build cgo

package embeddeddolt_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// seedCommitMessage is the scoped commit migrateUpConsented mints for
// freshly asserted dolt_ignore patterns (schema.go commitSeededDoltIgnore).
// A refused pass may legitimately have minted it — see the assertion in
// TestEmbeddedMigrate0075OwedRepairRefusesDirtyGraphTables.
const seedCommitMessage = "schema: seed dolt_ignore patterns"

// These tests pin AC-5 of gastownhall/beads#7037: when main consent is
// missing, or an owed repair meets pre-existing uncommitted edits to issues,
// dependencies or metadata, the typed refusal must fire BEFORE any repair
// write or marker advance — no obligation key minted or consumed, no repair
// commit, no cursor movement, and the user's dirt left exactly as it was.

// seedOversetCrossingStore builds the AC-1 crossing shape — main 74, ignored
// plane caught up to 31, one committed 0059-era over-set flag — with the
// connection closed, ready for a fresh open through MigrateUp.
func seedOversetCrossingStore(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 74)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	rows := []stampedIssue{
		{id: "rf-blocker", status: "open", stamp: historicalStamp(1)},
		{id: "rf-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
		{id: "rf-related", status: "open", stamp: historicalStamp(3)},
	}
	insertStampedIssues(t, ctx, conn, "issues", rows, true)
	insertIssueEdges(t, ctx, conn, [][3]string{
		{"rf-blocked", "rf-blocker", "blocks"},
		{"rf-related", "rf-blocked", "relates-to"},
	})
	mustExecOn(t, ctx, conn, "UPDATE issues SET is_blocked = 1, updated_at = updated_at WHERE id = 'rf-related'")
	// Commit BEFORE the ignored plane exists: a DOLT_ADD('-A') after it would
	// swallow the ignored cursor rows into tracked history, and the #4356
	// untrack heal on the next open mints commits the refusal assertions here
	// must not have to account for.
	commitFixture(t, ctx, conn, "test: v74 store with a 0059-era over-set flag")
	if _, err := schema.MigrateIgnoredUpTo(ctx, conn, 31); err != nil {
		t.Fatalf("MigrateIgnoredUpTo: %v", err)
	}
	return dataDir
}

// seedReceivedKeyStore builds the received-obligation shape: main already at
// 75 with the obligation key committed at HEAD (an origin that crashed after
// 0075's commit and pushed), and this clone's ignored cursor claiming 32 —
// current cursors, so nothing is pending by version and only the key at HEAD
// says work remains. The connection is closed on return.
func seedReceivedKeyStore(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 74)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	rows := []stampedIssue{
		{id: "rf-blocker", status: "open", stamp: historicalStamp(1)},
		{id: "rf-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
		{id: "rf-related", status: "open", stamp: historicalStamp(3)},
	}
	insertStampedIssues(t, ctx, conn, "issues", rows, true)
	insertIssueEdges(t, ctx, conn, [][3]string{
		{"rf-blocked", "rf-blocker", "blocks"},
		{"rf-related", "rf-blocked", "relates-to"},
	})
	mustExecOn(t, ctx, conn, "UPDATE issues SET is_blocked = 1, updated_at = updated_at WHERE id = 'rf-related'")

	// Cross 0075 through the bounded main-only path: its hook mints the key,
	// the cursor row lands, and one commit publishes both with the fixture —
	// exactly the state a crashed origin pushed.
	if _, err := schema.MigrateUpTo(ctx, conn, 75); err != nil {
		t.Fatalf("MigrateUpTo(75): %v", err)
	}
	commitFixture(t, ctx, conn, "test: origin committed 0075 and its obligation, then crashed")

	// The clone's own ignored plane: caught up through 31, then a cursor row
	// for 32 planted with the real file hash — the position a clone holds
	// when it opened debt-free on a pre-0075-main store before pulling.
	if _, err := schema.MigrateIgnoredUpTo(ctx, conn, 31); err != nil {
		t.Fatalf("MigrateIgnoredUpTo: %v", err)
	}
	sqlText, err := schema.IgnoredMigrationSQL("0032_recompute_mixed_is_blocked_dolt11886.up.sql")
	if err != nil {
		t.Fatalf("read 0032 marker: %v", err)
	}
	sum := sha256.Sum256([]byte(sqlText))
	mustExecOn(t, ctx, conn,
		"INSERT INTO ignored_schema_migrations (version, content_hash) VALUES (32, ?)",
		hex.EncodeToString(sum[:]))
	return dataDir
}

// TestEmbeddedMigrate0075ConsentRefusalPrecedesRepairWrites pins the consent
// half of AC-5: on a store whose main sequence is pending (0075 not crossed),
// an unconsented open must refuse before ANY write — no obligation key, no
// cursor movement, no repair commit, no working-set dirt on the guarded
// tables.
func TestEmbeddedMigrate0075ConsentRefusalPrecedesRepairWrites(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedOversetCrossingStore(t)

	// Package-wide consent is on (TestMain); this test must see the gate.
	schema.SetLocalMigrateConsent(false)
	t.Cleanup(func() { schema.SetLocalMigrateConsent(true) })
	t.Setenv(schema.AllowMigrateEnv, "")

	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		commits := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
		_, err := schema.MigrateUp(ctx, conn)
		if !schema.IsMigrateConsentError(err) {
			t.Fatalf("MigrateUp without consent = %v, want a *schema.MigrateConsentError", err)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != 74 {
			t.Errorf("main cursor = %d after the refusal, want 74 (nothing may advance)", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_repair_owed_0075'"); got != 0 {
			t.Errorf("obligation key holds %d row(s) after the refusal; the mint is a repair write", got)
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "rf-related"); !got {
			t.Errorf("rf-related flag cleared by the refused pass; the recompute is a repair write")
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log"); got != commits {
			t.Errorf("refused open minted %d commit(s) (dolt_log %d -> %d)", got-commits, commits, got)
		}
		for _, table := range []string{"issues", "metadata", "dependencies", "schema_migrations"} {
			if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_status WHERE table_name = '"+table+"'"); n != 0 {
				t.Errorf("%s dirtied by the refused pass; consent must precede every write", table)
			}
		}
	})
}

// TestEmbeddedMigrate0075OwedRepairRefusesDirtyGraphTables pins the dirty
// half of AC-5: an owed repair (crossing pass or key received at HEAD) with
// pre-existing committable edits to issues, dependencies or metadata refuses
// typed, before any repair write or marker advance, leaving the user's dirt
// untouched.
func TestEmbeddedMigrate0075OwedRepairRefusesDirtyGraphTables(t *testing.T) {
	requireEmbedded(t)

	dirty := map[string]func(t *testing.T, ctx context.Context, conn *sql.Conn){
		"issues":       func(t *testing.T, ctx context.Context, conn *sql.Conn) { mustExecOn(t, ctx, conn, "UPDATE issues SET title = 'user edit' WHERE id = 'rf-related'") },
		"metadata":     func(t *testing.T, ctx context.Context, conn *sql.Conn) { mustExecOn(t, ctx, conn, "INSERT INTO metadata (`key`, value) VALUES ('user-meta-edit', '1')") },
		"dependencies": func(t *testing.T, ctx context.Context, conn *sql.Conn) { mustExecOn(t, ctx, conn, "UPDATE dependencies SET type = 'relates-to' WHERE issue_id = 'rf-blocked'") },
	}

	for name, dirtyTable := range dirty {
		for _, shape := range []string{"crossing", "received-key"} {
			t.Run(shape+"/"+name, func(t *testing.T) {
				ctx := t.Context()
				var dataDir string
				if shape == "crossing" {
					dataDir = seedOversetCrossingStore(t)
				} else {
					dataDir = seedReceivedKeyStore(t)
				}
				withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
					dirtyTable(t, ctx, conn)
					commits := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
					var keyAtHeadBefore int
					if shape == "received-key" {
						keyAtHeadBefore = scalarInt(t, ctx, conn,
							"SELECT COUNT(*) FROM metadata AS OF 'HEAD' WHERE `key` = 'is_blocked_repair_owed_0075'")
						if keyAtHeadBefore != 1 {
							t.Fatalf("fixture: obligation key absent at HEAD (%d); the received shape needs it", keyAtHeadBefore)
						}
					}

					_, err := schema.MigrateUp(ctx, conn)
					var dirtyErr *schema.DirtyTablesError
					if !errors.As(err, &dirtyErr) {
						t.Fatalf("MigrateUp with dirty %s = %v, want a *schema.DirtyTablesError", name, err)
					}
					if len(dirtyErr.Tables) != 1 || dirtyErr.Tables[0] != name {
						t.Errorf("refusal names %v, want exactly [%s]", dirtyErr.Tables, name)
					}

					// Nothing advanced and nothing was repaired.
					wantCursor := 75
					if shape == "crossing" {
						wantCursor = 74
					}
					if got := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != wantCursor {
						t.Errorf("main cursor = %d after the refusal, want %d", got, wantCursor)
					}
					if got := readIsBlocked(t, ctx, conn, "issues", "rf-related"); !got {
						t.Errorf("rf-related flag cleared by the refused pass; the recompute is a repair write")
					}
					// The refusal must precede every repair write, but not
					// every commit: migrateUpConsented seeds canonical
					// dolt_ignore patterns a raw MigrateUpTo/MigrateIgnoredUpTo
					// fixture never registered, and that pass-owned seed
					// commit lands before the first step by design (#4566
					// self-heal contract; schema.go commitSeededDoltIgnore) —
					// a production store at these cursors holds the full set
					// and mints nothing. So: never a repair commit, at most
					// the one seed commit, and the user's dirt must not ride
					// into it.
					if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = '"+repairCommitMessage+"'"); got != 0 {
						t.Errorf("refused pass minted %d commit(s) carrying the repair message; the refusal precedes the repair commit", got)
					}
					if minted := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log") - commits; minted != 0 && minted != 1 {
						t.Errorf("refused pass minted %d commit(s); only the dolt_ignore seed commit may precede the refusal", minted)
					} else if minted == 1 {
						var newest string
						if err := conn.QueryRowContext(ctx,
							"SELECT message FROM dolt_log ORDER BY date DESC LIMIT 1").Scan(&newest); err != nil {
							t.Fatalf("read the newest commit message: %v", err)
						}
						if newest != seedCommitMessage {
							t.Errorf("the one commit minted before the refusal = %q, want the dolt_ignore seed commit %q", newest, seedCommitMessage)
						}
					}
					if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_status WHERE table_name = '"+name+"'"); n == 0 {
						t.Errorf("user edit to %s no longer in the working set after the refused pass; the seed commit must not sweep it", name)
					}
					if shape == "received-key" {
						if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM metadata AS OF 'HEAD' WHERE `key` = 'is_blocked_repair_owed_0075'"); got != keyAtHeadBefore {
							t.Errorf("obligation key at HEAD = %d after the refusal, want %d (the refusal precedes the mint and the repair)", got, keyAtHeadBefore)
						}
						if !ignoredCursorHas(t, ctx, conn, 32) {
							t.Errorf("ignored cursor row 32 consumed by the refused pass; the re-arm must not run past the refusal")
						}
					}
					// The user's dirt survived, uncommitted.
					switch name {
					case "issues":
						var title string
						if err := conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = 'rf-related'").Scan(&title); err != nil {
							t.Fatalf("read rf-related title: %v", err)
						}
						if title != "user edit" {
							t.Errorf("user edit to issues lost by the refused pass (title = %q)", title)
						}
					case "metadata":
						if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM metadata WHERE `key` = 'user-meta-edit'"); got != 1 {
							t.Errorf("user metadata row lost by the refused pass (%d rows)", got)
						}
					case "dependencies":
						var depType string
						if err := conn.QueryRowContext(ctx, "SELECT type FROM dependencies WHERE issue_id = 'rf-blocked'").Scan(&depType); err != nil {
							t.Fatalf("read rf-blocked edge type: %v", err)
						}
						if depType != "relates-to" {
							t.Errorf("user edit to dependencies lost by the refused pass (type = %q)", depType)
						}
					}
				})
			})
		}
	}
}

// TestEmbeddedMigrate0075IgnoresUnrelatedDirtyTables pins the guard's scope:
// dirt in a table the repair never touches does not refuse the pass — the
// repair runs to completion and the unrelated edit survives uncommitted, the
// same containment the raw-text pending-migration guard practices.
func TestEmbeddedMigrate0075IgnoresUnrelatedDirtyTables(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedOversetCrossingStore(t)

	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn,
			"INSERT INTO custom_statuses (name, category) VALUES ('user-hold', 'unspecified')")
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp with an unrelated dirty table: %v", err)
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "rf-related"); got {
			t.Errorf("rf-related still blocked after the repair; unrelated dirt must not block it")
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_repair_owed_0075'"); got != 0 {
			t.Errorf("obligation key holds %d row(s) after the repair", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM custom_statuses WHERE name = 'user-hold'"); got != 1 {
			t.Errorf("unrelated user edit lost by the pass (%d rows); the pass must leave it in the working set", got)
		}
		if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'custom_statuses'"); n == 0 {
			t.Errorf("unrelated user edit was committed by the pass; it must survive uncommitted")
		}
	})
}
