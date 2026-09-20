package dolt

import (
	"context"
	"database/sql"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/types"
)

// derivedIDDigestColumns mirrors the FROZEN digest column lists in
// internal/storage/schema (auxRekeyTables) and the insert-time derivation in
// issueops. Deliberately duplicated here: if either side drifts from the
// frozen lists, the recomputation below stops matching the stored ids and
// these tests fail.
var derivedIDDigestColumns = map[string]string{
	"events":   "issue_id, event_type, actor, old_value, new_value, comment, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')",
	"comments": "issue_id, author, text, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')",
}

// assertTableIDsContentDerived scans every row of table, recomputes the
// content digest from the stored (server-rendered) column values, and asserts
// the table's id set is exactly the derived ids for ordinals 0..n-1 per
// digest group — i.e. the ids the schema backfill would have assigned. This
// is the insert-time/backfill equivalence at the heart of bd-ri8bd: a row
// minted by the new insert paths must already sit on its convergent id.
func assertTableIDsContentDerived(ctx context.Context, t *testing.T, db *sql.DB, table string) {
	t.Helper()
	columns := derivedIDDigestColumns[table]

	rows, err := db.QueryContext(ctx, "SELECT id, "+columns+" FROM "+table) //nolint:gosec // test-local constant tables
	if err != nil {
		t.Fatalf("scan %s: %v", table, err)
	}
	defer rows.Close()

	// Derive the field count from the driver's own column list rather than
	// counting commas in the columns string: a column expression can contain
	// an internal comma (e.g. DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')),
	// which a naive strings.Count over-counts as an extra top-level field.
	colNames, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns %s: %v", table, err)
	}
	nFields := len(colNames) - 1
	groups := make(map[string][]string)
	for rows.Next() {
		var id string
		fields := make([]sql.NullString, nFields)
		dests := make([]any, 0, nFields+1)
		dests = append(dests, &id)
		for i := range fields {
			dests = append(dests, &fields[i])
		}
		if err := rows.Scan(dests...); err != nil {
			t.Fatalf("scan %s row: %v", table, err)
		}
		groups[rowid.Digest(fields)] = append(groups[rowid.Digest(fields)], id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("scan %s: %v", table, err)
	}
	if len(groups) == 0 {
		t.Fatalf("no %s rows to verify", table)
	}

	for digest, ids := range groups {
		want := make([]string, len(ids))
		for i := range ids {
			want[i] = rowid.New(table, i, digest)
		}
		got := append([]string(nil), ids...)
		sort.Strings(got)
		sort.Strings(want)
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s digest %s: ids = %v, want derived %v", table, digest[:12], got, want)
				break
			}
		}
	}
}

// TestInsertTimeIDsMatchBackfillDerivation drives the real store write paths
// (issue create with labels, update, comment) and verifies every resulting
// events/comments row landed on its content-derived id — recomputed from the
// server-rendered stored values, so a drift between the Go-side digest inputs
// (app-stamped created_at text, NULL-vs-empty encoding) and what the column
// actually holds fails here, not in a fleet merge.
func TestInsertTimeIDsMatchBackfillDerivation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		Title:       "derived id fixture",
		Description: "insert-time derivation",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		Labels:      []string{"lane-a"},
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"status": "in_progress"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, issue.ID, "tester", "a derived comment"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}

	assertTableIDsContentDerived(ctx, t, store.db, "events")
	assertTableIDsContentDerived(ctx, t, store.db, "comments")
}

// TestAuxTimeDateFormatMatchesGoRendering pins the SQL-side rendering the aux
// row id derivation depends on: DATE_FORMAT(x, '%Y-%m-%d %H:%i:%s') against a
// stored DATETIME(0) value must render byte-identically to
// issueops.FormatAuxTime, the Go-side stamp every insert site binds and every
// digest is computed from (see derivedIDDigestColumns above and auxRekeyTables
// in internal/storage/schema, which both render created_at this way to
// re-derive ids for rows that predate the insert-time derivation).
//
// The two sides render identically today because DATE_FORMAT with this exact
// format string reproduces what CAST(x AS CHAR) happens to render for a
// DATETIME(0) column — but CAST's rendering is an engine default, not a pinned
// contract, and nothing stops a future Dolt version from changing it. Pinning
// the SQL side to DATE_FORMAT with this literal format removes that
// dependency; this test is what catches it if the two ever drift, before it
// silently forks the id space the backfill converged.
func TestAuxTimeDateFormatMatchesGoRendering(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		Title:     "date_format fixture",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	instant := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	goRendered := issueops.FormatAuxTime(instant)

	evt := issueops.AuxEvent{
		IssueID:   issue.ID,
		EventType: types.EventCommented,
		Actor:     "tester",
		Comment:   sql.NullString{String: "date_format probe", Valid: true},
		CreatedAt: goRendered,
	}
	if err := issueops.InsertDerivedEvent(ctx, store.db, "events", evt); err != nil {
		t.Fatalf("InsertDerivedEvent: %v", err)
	}

	var sqlRendered string
	if err := store.db.QueryRowContext(ctx,
		"SELECT DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s') FROM events WHERE issue_id = ? AND comment = 'date_format probe'",
		issue.ID).Scan(&sqlRendered); err != nil {
		t.Fatalf("DATE_FORMAT read-back: %v", err)
	}

	if sqlRendered != goRendered {
		t.Errorf("DATE_FORMAT(created_at, ...) = %q, want issueops.FormatAuxTime rendering %q", sqlRendered, goRendered)
	}
}

// TestDerivedEventOrdinalsPreserveLocalDuplicates pins the ordinal
// discipline: the same logical event recorded twice in the same second stays
// two rows, on ordinals 0 and 1 of the same digest — multiplicity within a
// database is preserved even though the id is a function of content.
func TestDerivedEventOrdinalsPreserveLocalDuplicates(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		Title:     "ordinal fixture",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	evt := issueops.AuxEvent{
		IssueID:   issue.ID,
		EventType: types.EventCommented,
		Actor:     "tester",
		Comment:   sql.NullString{String: "same twice", Valid: true},
		CreatedAt: "2026-07-29 10:00:00",
	}
	if err := issueops.InsertDerivedEvent(ctx, store.db, "events", evt); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := issueops.InsertDerivedEvent(ctx, store.db, "events", evt); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	rows, err := store.db.QueryContext(ctx,
		"SELECT id FROM events WHERE issue_id = ? AND comment = 'same twice' ORDER BY id", issue.ID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read events: %v", err)
	}

	digest := rowid.Digest([]sql.NullString{
		{String: issue.ID, Valid: true},
		{String: string(types.EventCommented), Valid: true},
		{String: "tester", Valid: true},
		{}, {},
		{String: "same twice", Valid: true},
		{String: "2026-07-29 10:00:00", Valid: true},
	})
	want := []string{rowid.New("events", 0, digest), rowid.New("events", 1, digest)}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("duplicate event ids = %v, want ordinals 0 and 1: %v", got, want)
	}
}

// TestDerivedCommentInsertCollapsesDuplicates pins the comment semantics: a
// second identical comment (same issue, author, text, second) is the same
// logical comment and collapses onto the existing row, exactly like the
// import path's historical existence check.
func TestDerivedCommentInsertCollapsesDuplicates(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		Title:     "collapse fixture",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	first, existed, err := issueops.InsertDerivedComment(ctx, store.db, "comments", issue.ID, "tester", "dup text", "2026-07-29 10:00:00")
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if existed {
		t.Fatal("first insert reported existed=true")
	}
	second, existed, err := issueops.InsertDerivedComment(ctx, store.db, "comments", issue.ID, "tester", "dup text", "2026-07-29 10:00:00")
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if !existed || second != first {
		t.Errorf("second insert = (%s, existed=%v), want collapse onto %s", second, existed, first)
	}

	var n int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM comments WHERE issue_id = ?", issue.ID).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if n != 1 {
		t.Errorf("comment rows = %d, want 1", n)
	}
}
