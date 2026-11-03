//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// The #7073 shape (reported by @bee-ghosttrack): a lineage whose HEAD carries
// a committed __temp__ rebuild intermediate replicates to every fresh clone,
// whose first open renames the intermediate onto the ignored final name and
// leaves an unstageable rename delta that wedges pull. MigrateUp's open-time
// residue reconcile must untrack the intermediate from HEAD before the series
// run, unwedging the clone. residue=false is the control: the same lineage
// with no intermediate at HEAD must keep working unchanged.
func TestEmbeddedFreshCloneOfTempResidueLineageUnwedgesPull(t *testing.T) {
	for _, residue := range []bool{true, false} {
		t.Run(fmt.Sprintf("residue=%v", residue), func(t *testing.T) {
			ctx := t.Context()
			f := newPristineEmbeddedDoltFixture(t, "residueprobe")
			closeEmbeddedDoltStore(t, f.store)

			remote := filepath.Join(t.TempDir(), "remote")
			if err := os.MkdirAll(remote, 0o700); err != nil {
				t.Fatal(err)
			}
			remoteURL := "file://" + remote
			withRawConn(t, f.dataDir, f.database, func(conn *sql.Conn) {
				if residue {
					// Commit the intermediate without the ignore pattern, the
					// way an older binary did: with the pattern in place an
					// unforced DOLT_ADD silently skips the name.
					mustExecOn(t, ctx, conn, "DELETE FROM dolt_ignore WHERE pattern='__temp__%'")
					mustExecOn(t, ctx, conn, "CREATE TABLE __temp__wisp_child_counters (parent_id VARCHAR(255) NOT NULL PRIMARY KEY, last_child INT NOT NULL DEFAULT 0)")
					mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-A')")
					mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'auto-commit caught the rebuild intermediate')")
				}
				mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
				mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
			})

			cloneBeads := filepath.Join(t.TempDir(), "clone")
			cloneData := filepath.Join(cloneBeads, "embeddeddolt")
			if err := os.MkdirAll(cloneData, 0o700); err != nil {
				t.Fatal(err)
			}
			withRawConn(t, cloneData, "", func(conn *sql.Conn) {
				mustExecOn(t, ctx, conn, "CALL DOLT_CLONE(?, ?)", remoteURL, f.database)
			})

			store, err := embeddeddolt.Open(ctx, cloneBeads, f.database, "main")
			if err != nil {
				t.Fatalf("open fresh clone: %v", err)
			}
			closeEmbeddedDoltStore(t, store)

			// The hub publishes one more commit; the clone must be able to
			// pull it. The residue rename otherwise leaves an unstageable
			// non-add delta that makes every pull fail.
			withRawConn(t, f.dataDir, f.database, func(conn *sql.Conn) {
				mustExecOn(t, ctx, conn, "INSERT INTO config (`key`, value) VALUES ('hub.x', 'y')")
				mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'hub')")
				mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
			})

			store, err = embeddeddolt.Open(ctx, cloneBeads, f.database, "main")
			if err != nil {
				t.Fatalf("reopen fresh clone: %v", err)
			}
			perr := store.Pull(ctx)
			closeEmbeddedDoltStore(t, store)
			if perr != nil {
				t.Fatalf("pull after open of a %s lineage: %v", residueWord(residue), perr)
			}

			withRawConn(t, cloneData, f.database, func(conn *sql.Conn) {
				if tracked := trackedAtHead(t, ctx, conn, "__temp__wisp_child_counters"); tracked {
					t.Error("__temp__wisp_child_counters is still tracked at HEAD after the residue reconcile")
				}
				// No rename or delete residue may survive the repair: the
				// unstageable "__temp__X -> X" rename half is exactly what
				// wedged the pull, and the ignored final table must not be
				// reported as a tracked change either.
				for _, dirty := range dirtyTableNames(t, ctx, conn) {
					if slices.Contains([]string{"__temp__wisp_child_counters", "wisp_child_counters"}, dirty) {
						t.Errorf("dolt_status reports %q after the residue reconcile, want no rebuild residue", dirty)
					}
				}
			})
		})
	}
}

func residueWord(residue bool) string {
	if residue {
		return "residue"
	}
	return "clean"
}

// The already-wedged shape of #7073: a clone whose earlier open already
// renamed the committed __temp__ intermediate onto the ignored final name,
// leaving the temp table tracked at HEAD while the working set holds the
// renamed rows. Every pull from that lineage fails until a writable open
// untracks the residue. The repair must keep the renamed working rows,
// clear the rename delta, and let pull succeed (reported by
// @bee-ghosttrack).
func TestEmbeddedTempResidueUnwedgesARenamedStore(t *testing.T) {
	ctx := t.Context()
	f := newPristineEmbeddedDoltFixture(t, "residuerenamed")
	closeEmbeddedDoltStore(t, f.store)

	remote := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remote, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteURL := "file://" + remote
	withRawConn(t, f.dataDir, f.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	cloneBeads := filepath.Join(t.TempDir(), "clone")
	cloneData := filepath.Join(cloneBeads, "embeddeddolt")
	if err := os.MkdirAll(cloneData, 0o700); err != nil {
		t.Fatal(err)
	}
	withRawConn(t, cloneData, "", func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_CLONE(?, ?)", remoteURL, f.database)
	})

	// Wedge the clone the way an older binary did: commit the rebuild
	// intermediate at HEAD (with the ignore pattern removed so DOLT_ADD
	// stages it), restore the pattern, then rename the intermediate onto
	// the ignored final name. On a fresh clone the final table does not
	// exist yet (ignored tables never replicate), so the rename lands.
	withRawConn(t, cloneData, f.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "DELETE FROM dolt_ignore WHERE pattern='__temp__%'")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'drop the __temp__ ignore pattern')")
		mustExecOn(t, ctx, conn, "CREATE TABLE __temp__wisp_child_counters (parent_id VARCHAR(255) NOT NULL PRIMARY KEY, last_child INT NOT NULL DEFAULT 0)")
		mustExecOn(t, ctx, conn, "INSERT INTO __temp__wisp_child_counters VALUES ('p1', 3), ('p2', 7)")
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-A')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'auto-commit caught the rebuild intermediate')")
		mustExecOn(t, ctx, conn, "INSERT INTO dolt_ignore (pattern, ignored) VALUES ('__temp__%', true)")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'restore the __temp__ ignore pattern')")
		mustExecOn(t, ctx, conn, "DROP TABLE IF EXISTS wisp_child_counters")
		mustExecOn(t, ctx, conn, "RENAME TABLE __temp__wisp_child_counters TO wisp_child_counters")
	})

	// The wedge shape: the intermediate is tracked at HEAD while the
	// working set holds the renamed final table.
	withRawConn(t, cloneData, f.database, func(conn *sql.Conn) {
		if !trackedAtHead(t, ctx, conn, "__temp__wisp_child_counters") {
			t.Fatal("__temp__wisp_child_counters is not tracked at HEAD: the fixture does not reproduce the wedge")
		}
	})

	// The writable open must repair: untrack the residue from HEAD, keep
	// the renamed rows in the working set, and leave no rename delta.
	store, err := embeddeddolt.Open(ctx, cloneBeads, f.database, "main")
	if err != nil {
		t.Fatalf("open the wedged clone: %v", err)
	}
	closeEmbeddedDoltStore(t, store)

	withRawConn(t, cloneData, f.database, func(conn *sql.Conn) {
		if tracked := trackedAtHead(t, ctx, conn, "__temp__wisp_child_counters"); tracked {
			t.Error("__temp__wisp_child_counters is still tracked at HEAD after the residue reconcile")
		}
		for parent, want := range map[string]int{"p1": 3, "p2": 7} {
			var got int
			if err := conn.QueryRowContext(ctx, "SELECT last_child FROM wisp_child_counters WHERE parent_id = ?", parent).Scan(&got); err != nil {
				t.Errorf("row %q after the residue reconcile: %v", parent, err)
				continue
			}
			if got != want {
				t.Errorf("row %q after the residue reconcile: last_child = %d, want %d", parent, got, want)
			}
		}
		for _, dirty := range dirtyTableNames(t, ctx, conn) {
			if slices.Contains([]string{"__temp__wisp_child_counters", "wisp_child_counters"}, dirty) {
				t.Errorf("dolt_status reports %q after the residue reconcile, want no rebuild residue", dirty)
			}
		}
	})

	// The hub publishes one more commit; the repaired clone must pull it.
	withRawConn(t, f.dataDir, f.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "INSERT INTO config (`key`, value) VALUES ('hub.x', 'y')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'hub')")
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	store, err = embeddeddolt.Open(ctx, cloneBeads, f.database, "main")
	if err != nil {
		t.Fatalf("reopen the repaired clone: %v", err)
	}
	perr := store.Pull(ctx)
	closeEmbeddedDoltStore(t, store)
	if perr != nil {
		t.Fatalf("pull after the residue reconcile: %v", perr)
	}
}

// The #7073 residue meeting the bd-red8u events flip: an auto-commit caught
// 0062's Phase 1 scratch (__temp__events_flip, holding the only other copy
// of the events rows) at HEAD before Phase 2 could run. The full MigrateUp
// path must untrack the scratch with its working rows intact, after which
// 0062's own union and restore (Phase 1's IF NOT EXISTS + INSERT IGNORE,
// Phase 4's restore) consume them under the contract that was already
// written for an interrupted flip.
func TestEmbeddedTempResidueUntrackPreservesEventsFlipRows(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	// Seed a committed issue + audit event on the (still versioned) events
	// plane, the way the flip test does.
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('flip-1', 'flip seed', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('flip-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-seed-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed committed events row')")

	// The residue: Phase 1's exact scratch DDL and row copy, committed at
	// HEAD by an unrelated auto-commit before Phase 2 ran.
	execFrozenGuard(t, ctx, conn, `
CREATE TABLE __temp__events_flip (
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NOT NULL,
    old_value LONGTEXT,
    new_value LONGTEXT,
    comment TEXT,
    created_at DATETIME NOT NULL,
    id CHAR(36) NOT NULL,
    PRIMARY KEY (id)
);
INSERT IGNORE INTO __temp__events_flip (issue_id, event_type, actor, old_value, new_value, comment, created_at, id)
    SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at, id FROM events;
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-f', '__temp__events_flip')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: auto-commit catches the flip scratch')")
	if !trackedAtHead(t, ctx, conn, "__temp__events_flip") {
		t.Fatal("__temp__events_flip is not tracked at HEAD: the fixture does not reproduce the residue")
	}

	// The production path: the reconcile cached-untracks the scratch before
	// the series run, and 0062 finishes the flip from the working copy.
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp(): %v", err)
	}

	if tracked := trackedAtHead(t, ctx, conn, "__temp__events_flip"); tracked {
		t.Error("__temp__events_flip is still tracked at HEAD after the residue reconcile")
	}
	// The flip completed: events is out of HEAD and the seed row is back in
	// the working set, restored from the scratch the reconcile preserved.
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM events AS OF 'HEAD'").Scan(new(int)); err == nil {
		t.Error("events still exists at HEAD; want the flip's drop committed")
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events WHERE id = 'evt-seed-1'"); got != 1 {
		t.Errorf("post-flip working-set events row evt-seed-1 = %d, want 1", got)
	}
	for _, dirty := range dirtyTableNames(t, ctx, conn) {
		if slices.Contains([]string{"events", "__temp__events_flip"}, dirty) {
			t.Errorf("dolt_status reports %q after the flip, want no rebuild residue", dirty)
		}
	}
}
