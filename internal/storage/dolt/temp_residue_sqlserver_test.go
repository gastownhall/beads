package dolt

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestTempResidueUntrackOnSQLServer runs the #7073 cached untrack
// (gastownhall/beads#7073) through the server-mode migration path —
// MigrateUpWithLock on a pinned session of a real dolt sql-server, where the
// reporter's fleet runs. Two session modes, the two a fleet actually uses:
//
//   - pinned_transaction (the default): statements publish to the working
//     set and nothing reaches HEAD without an explicit DOLT_COMMIT. The
//     repair's scoping has teeth here, so the subtest pins AC-4's server
//     shape: with residue at HEAD and an unrelated STAGED edit beside it,
//     the repair's commit carries only the residue removal — the unstage
//     sweep clears the staged edit without discarding it, and it stays
//     uncommitted in the working set.
//   - transaction_commit (@@dolt_transaction_commit=1): every transaction
//     commit becomes a dolt commit of every non-ignored delta, which is the
//     mechanism that materialized the residue in the first place. No
//     unrelated-edit scoping is assertable under it — the mode publishes
//     every non-ignored working delta at every statement boundary, with or
//     without this repair — so the subtest pins what AC-6 asks of it: the
//     first MigrateUpWithLock repairs and returns nonzero, the second is
//     the converged fast path and returns zero without moving HEAD.
//
// Both modes: the repair must keep the residue's working rows (the cached
// removal never touches them), and the residue must leave HEAD.
func TestTempResidueUntrackOnSQLServer(t *testing.T) {
	for _, mode := range []struct {
		name     string
		txCommit bool
	}{
		{"pinned_transaction", false},
		{"transaction_commit", true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			skipIfNoDolt(t)
			acquireTestSlot()
			t.Cleanup(releaseTestSlot)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			t.Cleanup(cancel)

			dbName := uniqueTestDBName(t)
			admin, err := sql.Open("mysql", doltutil.ServerDSN{
				Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
			}.String())
			if err != nil {
				t.Fatalf("open admin connection: %v", err)
			}
			t.Cleanup(func() { admin.Close() })
			// No DROP DATABASE on cleanup: rapid CREATE/DROP cycles crash the
			// Dolt server (see events_preflip_restore_test.go).
			if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
				t.Fatalf("create database: %v", err)
			}
			db, err := sql.Open("mysql", doltutil.ServerDSN{
				Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName, Timeout: 10 * time.Second,
			}.String())
			if err != nil {
				t.Fatalf("open test database: %v", err)
			}
			t.Cleanup(func() { db.Close() })
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatalf("pin connection: %v", err)
			}
			t.Cleanup(func() { conn.Close() })

			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := conn.ExecContext(ctx, query, args...); err != nil {
					t.Fatalf("exec %.60q: %v", query, err)
				}
			}
			scalar := func(query string, args ...any) string {
				t.Helper()
				var v string
				if err := conn.QueryRowContext(ctx, query, args...).Scan(&v); err != nil {
					t.Fatalf("%.60q: %v", query, err)
				}
				return v
			}
			// The tracked-at-HEAD probe compares the listed names exactly
			// (LIKE's underscores are single-character wildcards).
			trackedAtHead := func(table string) bool {
				t.Helper()
				rows, err := conn.QueryContext(ctx, "SHOW TABLES AS OF 'HEAD' LIKE '"+table+"'")
				if err != nil {
					t.Fatalf("probing %s at HEAD: %v", table, err)
				}
				defer rows.Close()
				for rows.Next() {
					var name string
					if err := rows.Scan(&name); err != nil {
						t.Fatalf("probing %s at HEAD: %v", table, err)
					}
					if strings.EqualFold(name, table) {
						return true
					}
				}
				if err := rows.Err(); err != nil {
					t.Fatalf("probing %s at HEAD: %v", table, err)
				}
				return false
			}

			// The migrator's own pass builds the database at-latest, so the
			// repair below runs against exactly the state a wedged fleet seat
			// is in.
			if _, err := schema.MigrateUpWithLock(ctx, conn, dbName); err != nil {
				t.Fatalf("MigrateUpWithLock (build): %v", err)
			}

			// Commit the residue the way an older binary did: drop the
			// __temp__ pattern, let a blanket commit catch the intermediate,
			// restore the pattern.
			exec("DELETE FROM dolt_ignore WHERE pattern = '__temp__%'")
			exec("CALL DOLT_COMMIT('-Am', 'test: drop the __temp__ ignore pattern')")
			exec("CREATE TABLE __temp__wisp_child_counters (parent_id VARCHAR(255) NOT NULL PRIMARY KEY, last_child INT NOT NULL DEFAULT 0)")
			exec("INSERT INTO __temp__wisp_child_counters VALUES ('p1', 3), ('p2', 7)")
			exec("CALL DOLT_ADD('-A')")
			exec("CALL DOLT_COMMIT('-m', 'test: auto-commit caught the rebuild intermediate')")
			exec("INSERT INTO dolt_ignore (pattern, ignored) VALUES ('__temp__%', true)")
			exec("CALL DOLT_COMMIT('-Am', 'test: restore the __temp__ ignore pattern')")

			// An unrelated STAGED edit, the sweep's hazard: the repair must
			// clear it from the staging area without discarding it, and its
			// commit must not pick it up. Only assertable on the
			// pinned-transaction mode — under transaction-commit the mode
			// itself publishes every non-ignored working delta at every
			// statement boundary (migrate_temp_intermediates_test.go), with
			// or without this repair.
			if !mode.txCommit {
				exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('unrelated-staged', 'staged edit', '', '', '', '', 'open', 2, 'task')")
				exec("CALL DOLT_ADD('issues')")
			}

			if !trackedAtHead("__temp__wisp_child_counters") {
				t.Fatal("__temp__wisp_child_counters is not tracked at HEAD: the fixture does not reproduce the residue")
			}

			if mode.txCommit {
				exec("SET @@dolt_transaction_commit = 1")
			}

			headBefore := scalar("SELECT HASHOF('HEAD')")
			applied, err := schema.MigrateUpWithLock(ctx, conn, dbName)
			if err != nil {
				t.Fatalf("MigrateUpWithLock (repair): %v", err)
			}
			if applied == 0 {
				t.Fatal("MigrateUpWithLock (repair) = 0, want nonzero: the residue repair counts as work")
			}
			if trackedAtHead("__temp__wisp_child_counters") {
				t.Error("__temp__wisp_child_counters is still tracked at HEAD after the repair")
			}
			// The cached removal leaves the working rows verbatim.
			if got := scalar("SELECT COUNT(*) FROM __temp__wisp_child_counters"); got != "2" {
				t.Errorf("working residue rows after the repair = %s, want 2: the cached removal must not touch them", got)
			}
			headAfterRepair := scalar("SELECT HASHOF('HEAD')")
			if headAfterRepair == headBefore {
				t.Error("HEAD did not move across the repair: the removal commit must land")
			}
			if !mode.txCommit {
				// The unrelated staged edit stayed out of the repair's
				// commit and remained in the working set.
				if got := scalar("SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = 'unrelated-staged'"); got != "0" {
					t.Errorf("unrelated row committed at HEAD (%s), want 0: the repair's commit is scoped to the residue", got)
				}
				if got := scalar("SELECT COUNT(*) FROM issues WHERE id = 'unrelated-staged'"); got != "1" {
					t.Errorf("unrelated working rows after the repair = %s, want 1: the sweep must not discard them", got)
				}
				// And the repair's commit is the labeled, scoped one.
				want := "schema: untrack committed __temp__ rebuild residue (gastownhall/beads#7073): __temp__wisp_child_counters"
				if got := scalar("SELECT message FROM dolt_log LIMIT 1"); got != want {
					t.Errorf("latest commit message = %q, want the scoped residue untrack %q", got, want)
				}
			}

			// The converged second pass: zero work, HEAD unchanged.
			applied, err = schema.MigrateUpWithLock(ctx, conn, dbName)
			if err != nil {
				t.Fatalf("MigrateUpWithLock (converged): %v", err)
			}
			if applied != 0 {
				t.Fatalf("MigrateUpWithLock (converged) = %d, want 0", applied)
			}
			if got := scalar("SELECT HASHOF('HEAD')"); got != headAfterRepair {
				t.Fatalf("HEAD moved %s -> %s across a converged pass", headAfterRepair, got)
			}
		})
	}
}
