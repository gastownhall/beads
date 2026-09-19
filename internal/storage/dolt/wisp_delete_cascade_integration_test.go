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
