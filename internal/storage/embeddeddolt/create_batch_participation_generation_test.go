//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// batchActor is the actor every batch in this file is created under, and so
// the change_actor its version rows must carry.
const batchActor = "tester"

// batchRow is one issues row's write-fence state.
type batchRow struct {
	id         string
	generation sql.NullInt64
	revision   int64
}

func (r batchRow) String() string {
	generation := "NULL"
	if r.generation.Valid {
		generation = fmt.Sprint(r.generation.Int64)
	}
	return fmt.Sprintf("%s{participation_generation: %s, current_revision: %d}", r.id, generation, r.revision)
}

// batchVersion is one issue_versions row, less its durable_state payload.
type batchVersion struct {
	id       string
	revision int64
	actor    string
}

// batchSnapshot is what a batch create leaves behind that the participation
// write fence governs, read on one connection: every issues row, every version
// row and the store epoch a stamp is sourced from. rows and versions are in id
// (then revision) order.
type batchSnapshot struct {
	rows     []batchRow
	versions []batchVersion
	// epoch is invalid while store_epoch has no row: the first mint seeds it.
	epoch sql.NullInt64
}

func (s batchSnapshot) row(id string) (batchRow, bool) {
	for _, row := range s.rows {
		if row.id == id {
			return row, true
		}
	}
	return batchRow{}, false
}

func (s batchSnapshot) versionsOf(id string) []batchVersion {
	var versions []batchVersion
	for _, version := range s.versions {
		if version.id == id {
			versions = append(versions, version)
		}
	}
	return versions
}

func snapshotBatch(t *testing.T, ctx context.Context, te *testEnv) batchSnapshot {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()

	var snap batchSnapshot
	readRows(t, ctx, db, "SELECT id, participation_generation, current_revision FROM issues ORDER BY id",
		func(rows *sql.Rows) error {
			var row batchRow
			err := rows.Scan(&row.id, &row.generation, &row.revision)
			snap.rows = append(snap.rows, row)
			return err
		})
	readRows(t, ctx, db, "SELECT issue_id, revision, change_actor FROM issue_versions ORDER BY issue_id, revision",
		func(rows *sql.Rows) error {
			var version batchVersion
			err := rows.Scan(&version.id, &version.revision, &version.actor)
			snap.versions = append(snap.versions, version)
			return err
		})
	// store_epoch is seeded by the first mint, so a store that never minted has
	// no row to read.
	err = db.QueryRowContext(ctx, "SELECT epoch FROM store_epoch WHERE id = 1").Scan(&snap.epoch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read store_epoch: %v", err)
	}
	return snap
}

// readRows runs query on db and hands each result row to scan.
func readRows(t *testing.T, ctx context.Context, db *sql.DB, query string, scan func(rows *sql.Rows) error) {
	t.Helper()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			t.Fatalf("%s: scan: %v", query, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// firstDiff describes the first entry at which a and b differ, or returns ""
// when they are equal.
func firstDiff[T comparable](a, b []T) string {
	for i := range max(len(a), len(b)) {
		switch {
		case i >= len(a):
			return fmt.Sprintf("entry %d is only in the second list: %v (%d entries vs %d)", i, b[i], len(a), len(b))
		case i >= len(b):
			return fmt.Sprintf("entry %d is only in the first list: %v (%d entries vs %d)", i, a[i], len(a), len(b))
		case a[i] != b[i]:
			return fmt.Sprintf("entry %d: %v vs %v", i, a[i], b[i])
		}
	}
	return ""
}

// batchIDs returns n explicit issue ids under prefix. Explicit ids absent from
// both planes are what make a batch eligible for the deferred multi-row insert.
func batchIDs(prefix string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-b%04d", prefix, i+1)
	}
	return ids
}

func newBatchIssue(id, title string) *types.Issue {
	return &types.Issue{ID: id, Title: title, Priority: 2, IssueType: types.TypeTask, Status: types.StatusOpen}
}

// createBatch creates the n issues batchIDs(prefix, n) names in one CreateIssues
// call on a fresh store and reports what the batch left behind. history sets
// whether versioned history is on for the call. perRow turns the batch-create
// fast paths off for it, so every row takes the per-row body instead of the
// deferred multi-row insert; they are back on before createBatch returns.
func createBatch(t *testing.T, prefix string, n int, history, perRow bool) batchSnapshot {
	t.Helper()
	ctx := t.Context()
	te := newTestEnv(t, prefix)
	te.store.SetVersionedHistoryEnabled(history)

	issues := make([]*types.Issue, 0, n)
	for _, id := range batchIDs(prefix, n) {
		issues = append(issues, newBatchIssue(id, "batch row "+id))
	}
	if perRow {
		restore := issueops.DisableCreateFastPathsForTest()
		defer restore()
	}
	if err := te.store.CreateIssues(ctx, issues, batchActor); err != nil {
		t.Fatalf("CreateIssues of %d rows: %v", n, err)
	}
	return snapshotBatch(t, ctx, te)
}

// requireStamped fails the test unless every id names an issue whose history
// the batch began: participation_generation equal to the store epoch, exactly
// one issue_versions row (revision 1, written by batchActor) and
// current_revision at that revision. current_revision's column default is also
// 1, so it is the stamp and the version row that show a mint happened.
func requireStamped(t *testing.T, snap batchSnapshot, ids ...string) {
	t.Helper()
	if !snap.epoch.Valid {
		t.Errorf("store_epoch has no row after the batch, so nothing minted a version: want %d rows stamped", len(ids))
		return
	}
	var unstamped []string
	for _, id := range ids {
		row, found := snap.row(id)
		versions := snap.versionsOf(id)
		wantVersions := []batchVersion{{id: id, revision: 1, actor: batchActor}}
		if !found || row.generation != (sql.NullInt64{Int64: snap.epoch.Int64, Valid: true}) ||
			row.revision != 1 || !slices.Equal(versions, wantVersions) {
			unstamped = append(unstamped, fmt.Sprintf("%v with versions %v", row, versions))
		}
	}
	if len(unstamped) > 0 {
		t.Errorf("%d of %d batch-created rows are not stamped with store epoch %d at revision 1 by %q; first: %s",
			len(unstamped), len(ids), snap.epoch.Int64, batchActor, unstamped[0])
	}
}

// TestCreateBatchParticipationGeneration pins the participation write fence at
// the seam a batch create mints through. A batch writes every row first and
// mints each created row's first version last, after the creation-time edges
// land, so the mint has to keep each row's shape across that deferral: a row
// the batch inserted is create-shaped and is stamped with the store epoch, and
// a row the batch only upserted over is update-shaped and is skipped while it
// is a legacy row (its participation_generation NULL).
//
// The subtests run one after another and none is parallel: the first turns the
// batch-create fast paths off for the whole process while its comparison run
// executes.
func TestCreateBatchParticipationGeneration(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("BrandNewRowsAreStampedAndMatchPerRow", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			prefix string
			rows   int
			// slow cases are skipped under -race.
			slow bool
		}{
			{name: "FitsOneInsertStatement", prefix: "pga", rows: 5},
			// The multi-row INSERT writes 100 rows per statement, so 250 rows
			// take three statements (100, 100 and 50).
			{name: "SpansThreeInsertStatements", prefix: "pgb", rows: 250, slow: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if tc.slow && raceEnabled {
					t.Skip("250-row batch skipped under -race, where the in-process engine's own instrumentation makes a batch this size very slow; the 5-row case still runs")
				}
				ids := batchIDs(tc.prefix, tc.rows)

				batch := createBatch(t, tc.prefix, tc.rows, true, false)
				requireStamped(t, batch, ids...)
				// An empty version list would pass the comparison below
				// against another empty one, so stop before it.
				if len(batch.rows) != tc.rows || len(batch.versions) != tc.rows {
					t.Fatalf("batch of %d new issues left %d issues rows and %d issue_versions rows, want one version row per issue",
						tc.rows, len(batch.rows), len(batch.versions))
				}

				// The same input one row at a time, on a second fresh store. The
				// stamp and the version rows must not depend on which body ran.
				perRow := createBatch(t, tc.prefix, tc.rows, true, true)
				requireStamped(t, perRow, ids...)
				if diff := firstDiff(batch.versions, perRow.versions); diff != "" {
					t.Errorf("issue_versions (issue_id, revision, change_actor) differ between the batch and per-row paths: %s", diff)
				}
				if diff := firstDiff(batch.rows, perRow.rows); diff != "" {
					t.Errorf("issues write-fence state differs between the batch and per-row paths: %s", diff)
				}
			})
		}
	})

	t.Run("ExistingLegacyRowStaysNull", func(t *testing.T) {
		ctx := t.Context()
		te := newTestEnv(t, "pgl")
		const (
			legacyID  = "pgl-old"
			newAID    = "pgl-newa"
			newBID    = "pgl-newb"
			rewritten = "legacy row, rewritten by the batch"
		)

		// A row created while history is off is a legacy row: nothing stamps it.
		te.store.SetVersionedHistoryEnabled(false)
		if err := te.store.CreateIssue(ctx, newBatchIssue(legacyID, "legacy row"), batchActor); err != nil {
			t.Fatalf("create legacy row: %v", err)
		}
		before := snapshotBatch(t, ctx, te)
		legacy, found := before.row(legacyID)
		if !found || legacy.generation.Valid {
			t.Fatalf("setup: row created with history off is %v (found %t), want a NULL participation_generation", legacy, found)
		}

		// The batch carries the legacy id again with a new title, so it takes
		// the per-row upsert (isNew false), beside two rows that are brand new.
		te.store.SetVersionedHistoryEnabled(true)
		batch := []*types.Issue{
			newBatchIssue(legacyID, rewritten),
			newBatchIssue(newAID, "new row a"),
			newBatchIssue(newBID, "new row b"),
		}
		if err := te.store.CreateIssues(ctx, batch, batchActor); err != nil {
			t.Fatalf("CreateIssues over the existing id %s: %v", legacyID, err)
		}
		te.assertIssueTitle(t, ctx, "issues", legacyID, rewritten)

		after := snapshotBatch(t, ctx, te)
		if got, _ := after.row(legacyID); got != legacy {
			t.Errorf("the batch changed the legacy row's write-fence state: before %v, after %v; "+
				"an upsert over a row that never began history must leave it that way", legacy, got)
		}
		if versions := after.versionsOf(legacyID); len(versions) != 0 {
			t.Errorf("legacy row %s has issue_versions rows %v after the batch, want none", legacyID, versions)
		}
		requireStamped(t, after, newAID, newBID)
	})

	t.Run("StampKeepsTheCallersUpdatedAt", func(t *testing.T) {
		ctx := t.Context()
		te := newTestEnv(t, "pgu")
		te.store.SetVersionedHistoryEnabled(true)

		// An import supplies each row's timestamps and the batch stores them as
		// given. issues.updated_at is ON UPDATE CURRENT_TIMESTAMP, so the UPDATE
		// that stamps a row replaces the supplied value with the clock unless it
		// sets the column to itself.
		supplied := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		ids := batchIDs("pgu", 5)
		batch := make([]*types.Issue, 0, len(ids))
		for _, id := range ids {
			issue := newBatchIssue(id, "imported row "+id)
			issue.CreatedAt, issue.UpdatedAt = supplied, supplied
			batch = append(batch, issue)
		}
		if err := te.store.CreateIssues(ctx, batch, batchActor); err != nil {
			t.Fatalf("CreateIssues of %d rows: %v", len(ids), err)
		}

		// A mint that never ran would leave updated_at alone too, so require the
		// stamp first.
		requireStamped(t, snapshotBatch(t, ctx, te), ids...)
		for _, id := range ids {
			var got time.Time
			te.queryScalar(t, ctx, "SELECT updated_at FROM issues WHERE id = ?", []any{id}, &got)
			if !got.UTC().Equal(supplied) {
				t.Errorf("%s: updated_at is %v after the batch, want the supplied %v", id, got.UTC(), supplied)
			}
		}
	})

	t.Run("HistoryOffLeavesNull", func(t *testing.T) {
		const rows = 5
		batch := createBatch(t, "pgo", rows, false, false)
		if len(batch.rows) != rows {
			t.Fatalf("batch of %d new issues left %d issues rows", rows, len(batch.rows))
		}
		for _, row := range batch.rows {
			// 1 is current_revision's column default, so a row nothing minted
			// for still reads 1.
			if row.generation.Valid || row.revision != 1 {
				t.Errorf("row created with history off is %v, want a NULL participation_generation and the default current_revision 1", row)
			}
		}
		if len(batch.versions) != 0 {
			t.Errorf("history off left %d issue_versions rows, want none; first: %v", len(batch.versions), batch.versions[0])
		}
	})
}
