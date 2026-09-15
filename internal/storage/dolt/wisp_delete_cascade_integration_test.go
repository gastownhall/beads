//go:build integration && !windows

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// setupWispCascadeStore starts a real, standalone dolt sql-server (no Docker)
// via doltserver.Start and returns a store on a fresh, fully
// schema-initialized database. This mirrors TestFreshBootstrapHealIncarnation's
// setup rather than the package's default setupTestStore (dolt_test.go),
// which requires a Docker-testcontainer Dolt server that this sandbox does
// not have available.
func setupWispCascadeStore(t *testing.T) *DoltStore {
	t.Helper()
	integration.RequireDolt(t)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("start local dolt server: %v", err)
	}
	t.Cleanup(func() {
		current, stateErr := doltserver.IsRunning(beadsDir)
		if stateErr != nil || current == nil || !current.Running {
			return
		}
		if err := doltserver.Stop(beadsDir); err != nil {
			t.Errorf("stop local dolt server: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg := &Config{
		Path:            filepath.Join(beadsDir, "store"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      state.Port,
		ServerUser:      "root",
		Database:        "wisp_cascade_test",
		CreateIfMissing: true,
		MaxOpenConns:    1,
		CommitterName:   "Beads Test",
		CommitterEmail:  "beads@example.com",
	}
	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	// A freshly bootstrapped store picks up FK constraints on the four wisp
	// aux tables via cli_migrations.go, but the live production store (hq)
	// this bug was filed against does not: the migration that would add them
	// (migrations/ignored/0004_add_wisp_aux_fks.up.sql) was never promoted
	// into the applied migrations/ track. That gap is exactly what produced
	// the 3.03M orphan rows in the bug report. Drop the constraints here so
	// this test exercises deleteWisp/deleteWispBatchTx's own cascade
	// responsibility instead of passing only because a fresh store's FK
	// happens to do the cleanup for it.
	dropWispAuxFKs(t, ctx, store.db)

	return store
}

// dropWispAuxFKs removes the wisp auxiliary-table FK constraints so the test
// store's schema matches the live hq store. See setupWispCascadeStore.
func dropWispAuxFKs(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		"ALTER TABLE wisp_labels DROP FOREIGN KEY fk_wisp_labels_issue",
		"ALTER TABLE wisp_events DROP FOREIGN KEY fk_wisp_events_issue",
		"ALTER TABLE wisp_comments DROP FOREIGN KEY fk_wisp_comments_issue",
		"ALTER TABLE wisp_child_counters DROP FOREIGN KEY fk_wisp_child_counters_parent",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("drop wisp aux FK (%s): %v", stmt, err)
		}
	}
}

// dropWispDependencyFKs removes the wisp_dependencies FK constraints (added
// by migration 0047 on a fresh bootstrap) so the test store's schema matches
// the live hq store, whose wisp_dependencies table carries only the
// ck_wisp_dep_one_target CHECK constraint and no FKs. Without this, the
// fresh store's ON DELETE CASCADE would clean wisp_dependencies for free and
// the test could not fail against a delete path that leaks.
func dropWispDependencyFKs(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		"ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_issue",
		"ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_wisp_target",
		"ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_issue_target",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("drop wisp_dependencies FK (%s): %v", stmt, err)
		}
	}
}

// wispAuxTables lists the four wisp auxiliary tables a wisp delete must
// cascade into. This mirrors issueops.DeleteCascadeTables(true), which
// already documents this exact set as belonging to a wisp delete — separate
// from wisps itself and the wisp_dependencies/dependencies pair, which have
// their own regression coverage in TestDeleteWispBatch_CleansUpDependencies.
var wispAuxTables = []struct{ table, column string }{
	{"wisp_labels", "issue_id"},
	{"wisp_events", "issue_id"},
	{"wisp_comments", "issue_id"},
	{"wisp_child_counters", "parent_id"},
}

// seedWispAuxRows inserts one row into each wisp auxiliary table, keyed to
// id. wisp_child_counters is keyed on parent_id, simulating that this wisp
// was itself a parent whose children were assigned sequential IDs.
func seedWispAuxRows(t *testing.T, ctx context.Context, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		"INSERT INTO wisp_labels (issue_id, label) VALUES (?, ?)", id, "aux-test-label"); err != nil {
		t.Fatalf("seed wisp_labels: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO wisp_events (id, issue_id, event_type, actor) VALUES (UUID(), ?, ?, ?)",
		id, "test_event", "test"); err != nil {
		t.Fatalf("seed wisp_events: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO wisp_comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)",
		id, "test", "aux test comment"); err != nil {
		t.Fatalf("seed wisp_comments: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO wisp_child_counters (parent_id, last_child) VALUES (?, ?)", id, 3); err != nil {
		t.Fatalf("seed wisp_child_counters: %v", err)
	}
}

// assertWispAuxRowsGone fails the test if any row remains in the four wisp
// auxiliary tables for id.
func assertWispAuxRowsGone(t *testing.T, ctx context.Context, db *sql.DB, id string) {
	t.Helper()
	for _, tc := range wispAuxTables {
		var count int
		//nolint:gosec // G201: tc.table/tc.column come from the fixed wispAuxTables literal, not input.
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", tc.table, tc.column)
		if err := db.QueryRowContext(ctx, q, id).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", tc.table, err)
		}
		if count != 0 {
			t.Errorf("expected 0 rows in %s for %s after delete, got %d", tc.table, id, count)
		}
	}
}

// TestWispDeleteCascade_CleansUpAuxiliaryTables is the regression test for
// be-zdqyl: deleteWisp and deleteWispBatchTx only ever removed rows from the
// wisps table itself (plus an explicit wisp_dependencies cleanup), leaving
// orphaned rows behind in wisp_labels, wisp_events, wisp_comments, and
// wisp_child_counters — exactly the table set issueops.DeleteCascadeTables
// (true) already documents a wisp delete as owning. Covers both the
// single-delete path (deleteWisp) and the batch path (deleteWispBatch, which
// chunks through deleteWispBatchTx).
func TestWispDeleteCascade_CleansUpAuxiliaryTables(t *testing.T) {
	store := setupWispCascadeStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	t.Run("single delete", func(t *testing.T) {
		wisp := createTestWisp(t, ctx, store, "single-delete wisp")
		t.Logf("DEBUG wisp.ID = %q", wisp.ID)
		seedWispAuxRows(t, ctx, store.db, wisp.ID)

		for _, tc := range wispAuxTables {
			var count int
			q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", tc.table, tc.column)
			if err := store.db.QueryRowContext(ctx, q, wisp.ID).Scan(&count); err != nil {
				t.Fatalf("pre-delete count %s: %v", tc.table, err)
			}
			t.Logf("DEBUG pre-delete %s count = %d", tc.table, count)
		}

		if err := store.deleteWisp(ctx, wisp.ID); err != nil {
			t.Fatalf("deleteWisp: %v", err)
		}

		assertWispAuxRowsGone(t, ctx, store.db, wisp.ID)
	})

	t.Run("batch delete", func(t *testing.T) {
		wispA := createTestWisp(t, ctx, store, "batch-delete wisp A")
		wispB := createTestWisp(t, ctx, store, "batch-delete wisp B")
		seedWispAuxRows(t, ctx, store.db, wispA.ID)
		seedWispAuxRows(t, ctx, store.db, wispB.ID)

		deleted, err := store.deleteWispBatch(ctx, []string{wispA.ID, wispB.ID})
		if err != nil {
			t.Fatalf("deleteWispBatch: %v", err)
		}
		if deleted != 2 {
			t.Fatalf("expected 2 deleted, got %d", deleted)
		}

		assertWispAuxRowsGone(t, ctx, store.db, wispA.ID)
		assertWispAuxRowsGone(t, ctx, store.db, wispB.ID)
	})
}

// assertWispAuxTablesTotalZero fails the test if any row remains anywhere in
// the four wisp auxiliary tables — a store-wide total, not scoped to a single
// id. Used by TestWispDeleteCascade_RepeatedCyclesLeaveNoOrphans, where the
// property under test is that orphan counts across the whole store stay at 0
// cycle over cycle, not just that a single known id's rows are gone.
func assertWispAuxTablesTotalZero(t *testing.T, ctx context.Context, db *sql.DB, cycle int) {
	t.Helper()
	for _, tc := range wispAuxTables {
		var count int
		//nolint:gosec // G201: tc.table comes from the fixed wispAuxTables literal, not input.
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s", tc.table)
		if err := db.QueryRowContext(ctx, q).Scan(&count); err != nil {
			t.Fatalf("cycle %d: count %s: %v", cycle, tc.table, err)
		}
		if count != 0 {
			t.Errorf("cycle %d: expected 0 total rows in %s, got %d", cycle, tc.table, count)
		}
	}
}

// TestWispDeleteCascade_RepeatedCyclesLeaveNoOrphans is the round-2 regression
// test for be-wnuyt's uncovered acceptance criterion: "orphan counts on a
// fresh store stay at 0 after a reap cycle." TestWispDeleteCascade_CleansUpAuxiliaryTables
// only ever takes a single before/after snapshot scoped to one wisp id; it
// cannot catch a bug that only shows up across repeated create/delete
// cycles (e.g. delete scoping that leaves a previous cycle's rows behind
// once several wisps have passed through the table). This test runs several
// create-seed-delete cycles and asserts the aux tables' store-wide totals —
// not per-id existence — are 0 after every single cycle.
func TestWispDeleteCascade_RepeatedCyclesLeaveNoOrphans(t *testing.T) {
	store := setupWispCascadeStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	const cycles = 5
	for i := 0; i < cycles; i++ {
		wisp := createTestWisp(t, ctx, store, fmt.Sprintf("repeat-cycle wisp %d", i))
		seedWispAuxRows(t, ctx, store.db, wisp.ID)

		if err := store.deleteWisp(ctx, wisp.ID); err != nil {
			t.Fatalf("cycle %d: deleteWisp: %v", i, err)
		}

		assertWispAuxTablesTotalZero(t, ctx, store.db, i)
	}
}

// TestWispDeleteCascade_CleansUpWispDependencies is the regression test for
// the hq dangling_parent_ref reaper anomaly: DeleteWispFromDependenciesInTx
// and DeleteWispsFromDependenciesInTx cleaned only the dependencies table,
// never wisp_dependencies — even though DeleteCascadeTables(true) declares
// wisp_dependencies as part of the wisp deletion set. On stores without the
// migration-0047 FKs (like live hq), every wisp deletion orphaned that
// wisp's wisp_dependencies rows on both sides (issue_id and
// depends_on_wisp_id). The package's existing regression tests for this
// behavior (TestDeleteWispBatch_CleansUpDependencies,
// TestDeleteWispBatch_BothDirectionsCleared in wisp_gc_test.go) require a
// Docker-testcontainer Dolt server; this covers the same contract — plus
// the single-delete path they omit — on the standalone-server harness.
func TestWispDeleteCascade_CleansUpWispDependencies(t *testing.T) {
	store := setupWispCascadeStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	dropWispDependencyFKs(t, ctx, store.db)

	t.Run("single delete clears both directions", func(t *testing.T) {
		// mid appears as both child (issue_id, via mid->parent) and parent
		// (depends_on_wisp_id, via child->mid) in wisp_dependencies.
		parent := createTestWisp(t, ctx, store, "wisp-dep parent")
		mid := createTestWisp(t, ctx, store, "wisp-dep mid")
		child := createTestWisp(t, ctx, store, "wisp-dep child")
		mustAddWispDep(t, ctx, store, mid.ID, parent.ID)
		mustAddWispDep(t, ctx, store, child.ID, mid.ID)

		if err := store.deleteWisp(ctx, mid.ID); err != nil {
			t.Fatalf("deleteWisp: %v", err)
		}

		if n := countWispDependencyRows(t, ctx, store.db, mid.ID); n != 0 {
			t.Errorf("expected 0 wisp_dependencies rows referencing deleted wisp %s, got %d", mid.ID, n)
		}
		// The surviving wisps' edge set must not have been over-deleted:
		// parent and child had no edge between them, so nothing remains.
		if err := store.deleteWisp(ctx, parent.ID); err != nil {
			t.Fatalf("deleteWisp parent: %v", err)
		}
		if err := store.deleteWisp(ctx, child.ID); err != nil {
			t.Fatalf("deleteWisp child: %v", err)
		}
	})

	t.Run("batch delete clears both directions", func(t *testing.T) {
		root := createTestWisp(t, ctx, store, "wisp-dep batch root")
		stepA := createTestWisp(t, ctx, store, "wisp-dep batch step-a")
		stepB := createTestWisp(t, ctx, store, "wisp-dep batch step-b")
		mustAddWispDep(t, ctx, store, stepA.ID, root.ID)
		mustAddWispDep(t, ctx, store, stepB.ID, stepA.ID)

		deleted, err := store.deleteWispBatch(ctx, []string{root.ID, stepA.ID, stepB.ID})
		if err != nil {
			t.Fatalf("deleteWispBatch: %v", err)
		}
		if deleted != 3 {
			t.Fatalf("expected 3 deleted, got %d", deleted)
		}

		if n := countWispDependencyRows(t, ctx, store.db, root.ID, stepA.ID, stepB.ID); n != 0 {
			t.Errorf("expected 0 wisp_dependencies rows after batch delete, got %d", n)
		}
	})
}
