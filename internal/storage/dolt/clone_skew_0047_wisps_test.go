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

// cloneSkewPhaseTimeout bounds one fixture phase: advancing the producer, the
// consumer's clone and first schema init, or the second init. Phases get
// independent budgets so a slow producer chain cannot drain the consumer's.
const cloneSkewPhaseTimeout = 4 * time.Minute

// TestMigrateUpCloneBelowLatestWithoutWispTables reproduces gastownhall/beads#4176:
// a fresh clone of a Dolt-server project arrives with schema_migrations below
// the binary's latest and without the dolt_ignore'd wisp tables, which never
// sync. The main sequence runs before the ignored sequence that recreates
// them, so every pending main migration must tolerate their absence. Opening
// such a clone must not fail in 0047 (or any later migration) with
// "table not found: wisps".
//
// Each row is a genuine DOLT_CLONE of a producer pushed at that cursor, so
// dolt_ignore, not a hand-maintained DROP list, decides what the consumer
// lacks:
//
//   - cursor 46 pins the historical failure: 0047's recompute reads wisps
//     and wisp_dependencies, and only the keyed {schema_migrations,47}
//     pre-migration repair creates them first. It recreates only those two;
//     the other clone-local tables stay absent in this row too.
//   - cursor 47 starts past that repair, so every later main migration, 0048
//     through the newest, runs without wisps or wisp_dependencies: the
//     cursor-46 row cannot catch a later migration that reads them, because
//     repair 47 already built them.
//   - cursor latest-1 runs only the newest main migration, which keeps it
//     covered even if a table-creating repair is later keyed below it: the
//     cursor-47 row would then reach the newest with the tables rebuilt.
//   - The rows run serially, sharing one producer that advances through the
//     cursors, so the chain is replayed once.
func TestMigrateUpCloneBelowLatestWithoutWispTables(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	// Pending main migrations on an existing database need explicit consent;
	// a consent refusal is not evidence about the missing tables.
	t.Setenv(schema.AllowMigrateEnv, "1")

	cursors := []int{46, 47}
	if latest := schema.LatestVersion(); latest-1 > 47 {
		cursors = append(cursors, latest-1)
	}

	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	dropDatabase := func(name string) {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = admin.ExecContext(dropCtx, "DROP DATABASE IF EXISTS `"+name+"`")
	}
	openDatabase := func(t *testing.T, name string) *sql.DB {
		t.Helper()
		db, err := sql.Open("mysql", doltutil.ServerDSN{
			Host: "127.0.0.1", Port: testServerPort, User: "root", Database: name, Timeout: 10 * time.Second,
		}.String())
		if err != nil {
			t.Fatalf("open database %s: %v", name, err)
		}
		return db
	}

	setupCtx, setupCancel := context.WithTimeout(context.Background(), testTimeout)
	defer setupCancel()
	producerName := uniqueTestDBName(t)
	t.Cleanup(func() { dropDatabase(producerName) })
	if _, err := admin.ExecContext(setupCtx, "CREATE DATABASE `"+producerName+"`"); err != nil {
		t.Fatalf("create producer database: %v", err)
	}
	producerDB := openDatabase(t, producerName)
	t.Cleanup(func() { producerDB.Close() })
	producer, err := producerDB.Conn(setupCtx)
	if err != nil {
		t.Fatalf("pin producer connection: %v", err)
	}
	t.Cleanup(func() { producer.Close() })

	remoteURL := "file://" + filepath.Join(t.TempDir(), "remote")
	if _, err := producer.ExecContext(setupCtx, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL); err != nil {
		t.Fatalf("add producer remote: %v", err)
	}

	for _, cursor := range cursors {
		t.Run(fmt.Sprintf("cursor_%d", cursor), func(t *testing.T) {
			// The producer's pushed state: main cursor at `cursor`, with the
			// wisp tables existing only in its own working set.
			producerCtx, producerCancel := context.WithTimeout(context.Background(), cloneSkewPhaseTimeout)
			defer producerCancel()
			if _, err := schema.MigrateUpTo(producerCtx, producer, cursor); err != nil {
				t.Fatalf("producer: migrate to %04d: %v", cursor, err)
			}
			for _, q := range []string{
				fmt.Sprintf("CALL DOLT_COMMIT('-Am', 'producer at schema %d')", cursor),
				"CALL DOLT_PUSH('origin', 'main')",
			} {
				if _, err := producer.ExecContext(producerCtx, q); err != nil {
					t.Fatalf("producer: %s: %v", q, err)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), cloneSkewPhaseTimeout)
			defer cancel()
			consumerName := uniqueTestDBName(t)
			t.Cleanup(func() { dropDatabase(consumerName) })
			if _, err := producer.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", remoteURL, consumerName); err != nil {
				t.Fatalf("clone consumer: %v", err)
			}
			consumer := openDatabase(t, consumerName)
			t.Cleanup(func() { consumer.Close() })

			// Preconditions: what a real behind-cursor clone receives.
			if got := cloneSkewMaxVersion(ctx, t, consumer, "schema_migrations"); got != cursor {
				t.Fatalf("consumer main cursor = %d before init, want %d", got, cursor)
			}
			for _, table := range []string{"wisps", "wisp_dependencies", "ignored_schema_migrations"} {
				if cloneSkewTableExists(ctx, t, consumer, table) {
					t.Fatalf("consumer already has %s before init; the clone fixture is not behind-cursor", table)
				}
			}
			var fkChecks int
			if err := consumer.QueryRowContext(ctx, "SELECT @@foreign_key_checks").Scan(&fkChecks); err != nil {
				t.Fatalf("read foreign_key_checks: %v", err)
			}
			if fkChecks != 1 {
				t.Fatalf("consumer foreign_key_checks = %d, want 1", fkChecks)
			}

			if _, err := initSchemaOnDBWithRetry(ctx, consumer); err != nil {
				t.Fatalf("schema init on a clone at cursor %d without wisp tables: %v", cursor, err)
			}

			wantMain, wantIgnored := schema.LatestVersion(), schema.LatestIgnoredVersion()
			assertConverged := func(ctx context.Context, stage string) {
				t.Helper()
				if got := cloneSkewMaxVersion(ctx, t, consumer, "schema_migrations"); got != wantMain {
					t.Errorf("%s: main cursor = %d, want %d", stage, got, wantMain)
				}
				if got := cloneSkewMaxVersion(ctx, t, consumer, "ignored_schema_migrations"); got != wantIgnored {
					t.Errorf("%s: ignored cursor = %d, want %d", stage, got, wantIgnored)
				}
				for _, table := range []string{"wisps", "wisp_dependencies"} {
					if !cloneSkewTableExists(ctx, t, consumer, table) {
						t.Errorf("%s: %s missing", stage, table)
					}
				}
			}
			assertConverged(ctx, "after init")

			// The second init gets its own budget: the first phase's may be
			// nearly spent.
			againCtx, againCancel := context.WithTimeout(context.Background(), cloneSkewPhaseTimeout)
			defer againCancel()
			applied, err := initSchemaOnDBWithRetry(againCtx, consumer)
			if err != nil {
				t.Fatalf("second schema init: %v", err)
			}
			if applied != 0 {
				t.Errorf("second schema init applied %d migration(s), want 0", applied)
			}
			assertConverged(againCtx, "after second init")
		})
	}
}

func cloneSkewMaxVersion(ctx context.Context, t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var v int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM `"+table+"`").Scan(&v); err != nil {
		t.Fatalf("read %s cursor: %v", table, err)
	}
	return v
}

func cloneSkewTableExists(ctx context.Context, t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table,
	).Scan(&n); err != nil {
		t.Fatalf("check %s table: %v", table, err)
	}
	return n == 1
}
