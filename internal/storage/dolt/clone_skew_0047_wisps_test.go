package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// cloneSkewPhaseTimeout bounds one fixture phase (a producer advance, or a
// consumer's clone + schema init), the same budget the original #4176 repro
// used for its single full pass. Phases get independent budgets so a slow
// producer chain cannot drain the consumer's init deadline.
const cloneSkewPhaseTimeout = 4 * time.Minute

// TestMigrateUpCloneBelowLatestWithoutWispTables reproduces
// gastownhall/beads#4176: a fresh Dolt-server clone whose schema_migrations
// cursor arrives below the binary's latest reaches main migration 0047,
// whose recompute joins the dolt_ignore'd wisps/wisp_dependencies tables.
// Those tables never replicate (0019_wisps_dolt_ignore,
// 0040_ignored_tables_also_nonlocal_tables), and MigrateUp runs the main
// sequence before the ignored sequence that recreates them locally, so the
// clone hits "table not found: wisps" unless something recreates them first.
// The keyed pre-migration repair ({schema_migrations, 47} ->
// ensureWispTablesForMixedBlockedRecompute,
// internal/storage/schema/migration_repairs.go) does exactly that ahead of
// 0047's frozen SQL.
//
// The cursor-46 subtest pins that historical repair: a clone at exactly the
// reported crash cursor must open through the consented production
// schema-init path and converge on the binary's latest cursors.
//
// The consumers are genuine DOLT_CLONEs of a file remote, not databases with
// a hand-maintained table-drop list: only a real clone exercises
// dolt_ignore's replication cut — the committed schema_migrations cursor
// travels to the clone while the wisp tables stay behind — and only a real
// producer/clone pair fails loudly when the fixture drifts from what a
// behind-cursor clone actually contains.
func TestMigrateUpCloneBelowLatestWithoutWispTables(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	// The production runner checks migration consent before any write
	// (schema.MigrateUp); a consent refusal is not evidence of the reported
	// crash, so consent explicitly. The consumer is a clone this test
	// brought into existence, exactly the shape `bd dolt pull`/`bd clone`
	// hand to the open path.
	t.Setenv(schema.AllowMigrateEnv, "1")

	latestMain := schema.LatestVersion()
	latestIgnored := schema.LatestIgnoredVersion()

	// Sequential subtests sharing one producer that advances through the
	// sorted cursors, so the main migration chain replays once instead of
	// twice. Subtests are deliberately serial: one test slot, one producer.
	cursors := []int{46}

	// ctx outlives every phase context: it only pins the producer
	// connection for the whole test; each phase carries its own deadline.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { admin.Close() })

	dropDB := func(name string) {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = admin.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
	}

	// --- producer: built to a historical cursor WITHOUT ever calling New,
	// so no full MigrateUp runs there and the ignored lane never creates
	// the local tables ahead of the committed history. ---
	producerDB := uniqueTestDBName(t)
	t.Cleanup(func() { dropDB(producerDB) })
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+producerDB+"`"); err != nil {
		t.Fatalf("create producer database: %v", err)
	}
	producer, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: producerDB, Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open producer connection: %v", err)
	}
	producer.SetMaxOpenConns(1)
	t.Cleanup(func() { producer.Close() })
	pconn, err := producer.Conn(ctx)
	if err != nil {
		t.Fatalf("pin producer connection: %v", err)
	}
	t.Cleanup(func() { pconn.Close() })

	remoteURL := "file://" + filepath.Join(t.TempDir(), "clone_skew_remote")
	producerVersion := 0
	remoteAdded := false

	// advanceProducer migrates the producer up to cursor (main sequence
	// only, via MigrateUpTo, which does not commit per step), commits its
	// tracked state, and publishes it to the file remote. A single producer
	// advances monotonically, so the chain 1..cursor applies exactly once.
	advanceProducer := func(t *testing.T, cursor int) {
		t.Helper()
		if producerVersion >= cursor {
			return
		}
		actx, acancel := context.WithTimeout(context.Background(), cloneSkewPhaseTimeout)
		defer acancel()
		if _, err := schema.MigrateUpTo(actx, pconn, cursor); err != nil {
			t.Fatalf("migrate producer to %04d: %v", cursor, err)
		}
		if _, err := pconn.ExecContext(actx,
			fmt.Sprintf("CALL DOLT_COMMIT('-Am', 'producer at schema %d')", cursor)); err != nil {
			t.Fatalf("commit producer at schema %d: %v", cursor, err)
		}
		if !remoteAdded {
			if _, err := pconn.ExecContext(actx, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL); err != nil {
				t.Fatalf("add file remote: %v", err)
			}
			remoteAdded = true
		}
		if _, err := pconn.ExecContext(actx, "CALL DOLT_PUSH('origin', 'main')"); err != nil {
			t.Fatalf("push producer at schema %d: %v", cursor, err)
		}
		producerVersion = cursor
	}

	for _, cursor := range cursors {
		t.Run(fmt.Sprintf("cursor_%04d", cursor), func(t *testing.T) {
			advanceProducer(t, cursor)

			cctx, ccancel := context.WithTimeout(context.Background(), cloneSkewPhaseTimeout)
			defer ccancel()

			// --- consumer: a genuine clone of the just-pushed remote. ---
			consumerDB := uniqueTestDBName(t)
			t.Cleanup(func() { dropDB(consumerDB) })
			if _, err := admin.ExecContext(cctx, "CALL DOLT_CLONE(?, ?)", remoteURL, consumerDB); err != nil {
				t.Fatalf("clone consumer at schema %d: %v", cursor, err)
			}
			consumer, err := sql.Open("mysql", doltutil.ServerDSN{
				Host: "127.0.0.1", Port: testServerPort, User: "root", Database: consumerDB, Timeout: 10 * time.Second,
			}.String())
			if err != nil {
				t.Fatalf("open consumer connection: %v", err)
			}
			consumer.SetMaxOpenConns(1)
			t.Cleanup(func() { consumer.Close() })

			// Precondition: the clone arrived at exactly the producer's
			// cursor, carrying none of the dolt_ignore'd local tables and
			// no ignored cursor — the reported clone shape. A failure here
			// is a fixture error, not evidence about the migration path;
			// never silently replace the clone with a weaker fixture.
			var got int
			if err := consumer.QueryRowContext(cctx,
				"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&got); err != nil {
				t.Fatalf("read consumer main cursor: %v", err)
			}
			if got != cursor {
				t.Fatalf("consumer main cursor = %d, want %d (fixture did not produce the requested clone)", got, cursor)
			}
			for _, table := range []string{"wisps", "wisp_dependencies", "ignored_schema_migrations"} {
				var n int
				if err := consumer.QueryRowContext(cctx,
					"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
					table).Scan(&n); err != nil {
					t.Fatalf("check %s absence: %v", table, err)
				}
				if n != 0 {
					t.Fatalf("fixture error: %s already present in the %d-cursor clone", table, cursor)
				}
			}
			var fk int
			if err := consumer.QueryRowContext(cctx, "SELECT @@SESSION.foreign_key_checks").Scan(&fk); err != nil {
				t.Fatalf("read foreign_key_checks: %v", err)
			}
			if fk != 1 {
				t.Fatalf("fixture error: clone session has foreign_key_checks = %d, want 1", fk)
			}

			// The consented production schema-init path, on the locked
			// migration runner a real open uses. This is where #4176
			// crashed: main migration 0047 (or, for the latest-1 clone,
			// the newest main migration) runs before the ignored sequence.
			if _, err := initSchemaOnDBWithRetry(cctx, consumer); err != nil {
				t.Fatalf("consented schema init on a %d-cursor clone without wisp tables: %v", cursor, err)
			}

			assertCursor := func(table string, want int) {
				t.Helper()
				var v int
				if err := consumer.QueryRowContext(cctx,
					fmt.Sprintf("SELECT COALESCE(MAX(version), 0) FROM %s", table)).Scan(&v); err != nil {
					t.Fatalf("read %s after init: %v", table, err)
				}
				if v != want {
					t.Errorf("%s after init = %d, want %d", table, v, want)
				}
			}
			assertTable := func(table string) {
				t.Helper()
				var n int
				if err := consumer.QueryRowContext(cctx,
					"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
					table).Scan(&n); err != nil {
					t.Fatalf("check %s presence: %v", table, err)
				}
				if n != 1 {
					t.Errorf("%s missing after schema init", table)
				}
			}

			assertCursor("schema_migrations", latestMain)
			assertCursor("ignored_schema_migrations", latestIgnored)
			assertTable("wisps")
			assertTable("wisp_dependencies")

			// Idempotence edge check: re-running the production init on
			// the now-current clone applies nothing and moves no cursor.
			ictx, icancel := context.WithTimeout(context.Background(), cloneSkewPhaseTimeout)
			defer icancel()
			applied, err := initSchemaOnDBWithRetry(ictx, consumer)
			if err != nil {
				t.Fatalf("second schema init: %v", err)
			}
			if applied != 0 {
				t.Errorf("second schema init applied %d migrations, want 0", applied)
			}
			assertCursor("schema_migrations", latestMain)
			assertCursor("ignored_schema_migrations", latestIgnored)
		})
	}
}
