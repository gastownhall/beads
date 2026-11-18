package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// conflictRowFor builds a rawConflictRow the way dolt_conflicts_<table> reports
// one: base_/our_/their_ columns plus the diff-type metadata. A nil side value
// stands for the NULLs dolt writes when that side has no row.
func conflictRowFor(t *testing.T, cells map[string][3]any) rawConflictRow {
	t.Helper()
	row := rawConflictRow{}
	// Deterministic order: id first, then the rest as given (map order does not
	// matter to the merge rules, but a stable id column keeps failures readable).
	names := []string{"id"}
	for name := range cells {
		if name != "id" {
			names = append(names, name)
		}
	}
	for _, name := range names {
		v, ok := cells[name]
		if !ok {
			continue
		}
		for i, side := range []string{"base", "our", "their"} {
			row.cols = append(row.cols, side+"_"+name)
			row.vals = append(row.vals, v[i])
		}
	}
	row.cols = append(row.cols, "our_diff_type", "their_diff_type")
	row.vals = append(row.vals, "modified", "modified")
	return row
}

// merged reports the value the plan writes for col, and whether it writes one.
func (m issuesRowMerge) merged(col string) (any, bool) {
	for i, c := range m.columns {
		if c == col {
			return m.values[i], true
		}
	}
	return nil, false
}

const (
	tsBase   = "2026-07-10 10:00:00"
	tsOurs   = "2026-07-10 11:00:00"
	tsTheirs = "2026-07-10 12:00:00"
)

// TestMergeIssuesConflictRow_DisjointFieldsBothSurvive is the flagship case:
// both sides edited the SAME issue since the merge base but different fields,
// so the row conflicts only because every mutation stamps updated_at. Neither
// side's edit may be dropped.
func TestMergeIssuesConflictRow_DisjointFieldsBothSurvive(t *testing.T) {
	row := conflictRowFor(t, map[string][3]any{
		"id":         {"bd-1", "bd-1", "bd-1"},
		"status":     {"open", "in_progress", "open"}, // only we changed it
		"assignee":   {"", "", "alice"},               // only they changed it
		"title":      {"same", "same", "same"},        // untouched
		"updated_at": {tsBase, tsOurs, tsTheirs},
	})

	m, ok := mergeIssuesConflictRow(row)
	if !ok {
		t.Fatal("expected disjoint-field conflict to be field-mergeable")
	}
	if _, written := m.merged("status"); written {
		t.Error("our status edit must be kept, not overwritten")
	}
	v, written := m.merged("assignee")
	if !written || v != "alice" {
		t.Errorf("their assignee edit must survive, got %v (written=%v)", v, written)
	}
	if _, written := m.merged("title"); written {
		t.Error("an agreeing column must not be written")
	}
	// updated_at merges to max(ours, theirs): only they moved it past ours.
	v, written = m.merged("updated_at")
	if !written || v != tsTheirs {
		t.Errorf("updated_at must merge to the later timestamp, got %v (written=%v)", v, written)
	}
}

// TestMergeIssuesConflictRow_ContestedCellLWW checks the one genuine conflict
// class: both sides changed the SAME cell, so the later updated_at wins it.
// Both statuses are non-closed: a closed-versus-non-closed contest declines
// instead (TestMergeIssuesConflictRow_CloseRaceDeclines).
func TestMergeIssuesConflictRow_ContestedCellLWW(t *testing.T) {
	t.Run("theirs newer", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":         {"bd-2", "bd-2", "bd-2"},
			"status":     {"open", "in_progress", "blocked"},
			"updated_at": {tsBase, tsOurs, tsTheirs},
		})
		m, ok := mergeIssuesConflictRow(row)
		if !ok {
			t.Fatal("expected contested cell with distinct timestamps to be mergeable")
		}
		if v, written := m.merged("status"); !written || v != "blocked" {
			t.Errorf("later writer must win the contested cell, got %v (written=%v)", v, written)
		}
	})
	t.Run("ours newer", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":         {"bd-3", "bd-3", "bd-3"},
			"status":     {"open", "in_progress", "blocked"},
			"updated_at": {tsBase, tsTheirs, tsOurs},
		})
		m, ok := mergeIssuesConflictRow(row)
		if !ok {
			t.Fatal("expected contested cell with distinct timestamps to be mergeable")
		}
		if _, written := m.merged("status"); written {
			t.Error("our newer status must be kept, not overwritten")
		}
		if _, written := m.merged("updated_at"); written {
			t.Error("updated_at must stay at our (later) value")
		}
	})
}

// TestMergeIssuesConflictRow_AmbiguousLeftAlone covers the classes LWW has no
// answer for: equal or unparseable timestamps on a contested cell.
func TestMergeIssuesConflictRow_AmbiguousLeftAlone(t *testing.T) {
	t.Run("equal timestamps", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":         {"bd-4", "bd-4", "bd-4"},
			"status":     {"open", "in_progress", "blocked"},
			"updated_at": {tsBase, tsOurs, tsOurs},
		})
		if _, ok := mergeIssuesConflictRow(row); ok {
			t.Error("a contested cell with equal updated_at must be left for the operator")
		}
	})
	t.Run("unparseable timestamp", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":         {"bd-5", "bd-5", "bd-5"},
			"status":     {"open", "in_progress", "blocked"},
			"updated_at": {tsBase, "not-a-time", tsTheirs},
		})
		if _, ok := mergeIssuesConflictRow(row); ok {
			t.Error("a contested cell with an unparseable updated_at must be left for the operator")
		}
	})
	t.Run("equal timestamps but disjoint fields", func(t *testing.T) {
		// No cell is contested, so the tiebreak is never needed: this MUST
		// still merge even though the timestamps are identical.
		row := conflictRowFor(t, map[string][3]any{
			"id":         {"bd-6", "bd-6", "bd-6"},
			"status":     {"open", "in_progress", "open"},
			"assignee":   {"", "", "alice"},
			"updated_at": {tsBase, tsOurs, tsOurs},
		})
		if _, ok := mergeIssuesConflictRow(row); !ok {
			t.Error("disjoint edits must merge regardless of the timestamps")
		}
	})
}

// TestMergeIssuesConflictRow_StructuralConflictsLeftAlone covers add/add and
// delete/modify, which have no field-level answer.
func TestMergeIssuesConflictRow_StructuralConflictsLeftAlone(t *testing.T) {
	t.Run("add/add", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":         {nil, "bd-7", "bd-7"},
			"status":     {nil, "open", "closed"},
			"updated_at": {nil, tsOurs, tsTheirs},
		})
		if _, ok := mergeIssuesConflictRow(row); ok {
			t.Error("add/add must be left for the operator")
		}
	})
	t.Run("delete/modify", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":         {"bd-8", nil, "bd-8"},
			"status":     {"open", nil, "closed"},
			"updated_at": {tsBase, nil, tsTheirs},
		})
		if _, ok := mergeIssuesConflictRow(row); ok {
			t.Error("delete/modify must be left for the operator")
		}
	})
}

// TestMergeIssuesConflictRow_NullVsEmpty pins that SQL NULL and the empty
// string are distinct values, so an assignee cleared to ” on one side is a
// real edit and not mistaken for the NULL base.
func TestMergeIssuesConflictRow_NullVsEmpty(t *testing.T) {
	row := conflictRowFor(t, map[string][3]any{
		"id":         {"bd-9", "bd-9", "bd-9"},
		"assignee":   {nil, "", nil},
		"updated_at": {tsBase, tsOurs, tsTheirs},
	})
	m, ok := mergeIssuesConflictRow(row)
	if !ok {
		t.Fatal("expected the row to merge: only one side changed assignee")
	}
	if _, written := m.merged("assignee"); written {
		t.Error("our '' edit must be kept: their side still matches the NULL base")
	}
}

// TestMergeIssuesConflictRow_ByteValuesCompareEqual pins that a driver
// returning []byte for one side and string for the other does not read as a
// difference (the normalization conflictCellsEqual relies on).
func TestMergeIssuesConflictRow_ByteValuesCompareEqual(t *testing.T) {
	row := conflictRowFor(t, map[string][3]any{
		"id":         {"bd-10", "bd-10", []byte("bd-10")},
		"status":     {[]byte("open"), "open", []byte("open")},
		"assignee":   {"", "", "alice"},
		"updated_at": {tsBase, tsOurs, tsTheirs},
	})
	m, ok := mergeIssuesConflictRow(row)
	if !ok {
		t.Fatal("expected the row to merge")
	}
	if _, written := m.merged("status"); written {
		t.Error("[]byte and string spellings of the same value must not read as a conflict")
	}
}

// TestParseConflictTimestamp covers the shapes an updated_at cell arrives in.
func TestParseConflictTimestamp(t *testing.T) {
	want := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		in   any
	}{
		{"mysql datetime text", "2026-07-10 12:00:00"},
		{"mysql datetime bytes", []byte("2026-07-10 12:00:00")},
		{"rfc3339", "2026-07-10T12:00:00Z"},
		{"driver time", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseConflictTimestamp(tc.in)
			if !ok || !got.Equal(want) {
				t.Errorf("parseConflictTimestamp(%v) = %v, %v; want %v, true", tc.in, got, ok, want)
			}
		})
	}
	for _, bad := range []any{nil, "", "yesterday"} {
		if _, ok := parseConflictTimestamp(bad); ok {
			t.Errorf("parseConflictTimestamp(%v) must not parse", bad)
		}
	}
}

// TestUnionConflictKeyColumnsCoverTheUnionTables pins the union-merged table
// set against its resolver, so adding one without its key columns cannot
// silently produce a resolver that deletes nothing.
func TestUnionConflictKeyColumnsCoverTheUnionTables(t *testing.T) {
	for _, table := range []string{"labels", "comments", "events"} {
		if cols := unionConflictKeyColumns[table]; len(cols) == 0 {
			t.Errorf("union table %s has no key columns", table)
		}
	}
}

// TestDataColumnsExcludesMetaAndKey pins the column classification the merge
// rules walk: dolt's diff_type/cardinality metadata is not row data, and the
// key column is never written back.
func TestDataColumnsExcludesMetaAndKey(t *testing.T) {
	row := rawConflictRow{
		cols: []string{
			"base_id", "our_id", "their_id",
			"base_status", "our_status", "their_status",
			"our_diff_type", "their_diff_type",
			"base_cardinality", "our_cardinality", "their_cardinality",
			"our_their_thing", "base_their_thing", "their_their_thing",
		},
		vals: make([]any, 14),
	}
	got := row.dataColumns("id")
	want := map[string]bool{"status": true, "their_thing": true}
	if len(got) != len(want) {
		t.Fatalf("dataColumns = %v, want %v", got, want)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected data column %q", c)
		}
	}
}

// TestMergeIssuesConflictRow_UpdatedAtAlwaysWrittenWithTheMerge is the
// regression pin for the ON UPDATE CURRENT_TIMESTAMP trap: issues.updated_at
// is `DATETIME NOT NULL ... ON UPDATE CURRENT_TIMESTAMP`, so any UPDATE that
// omits it restamps the row with this clone's wall clock — breaking the
// max(ours, theirs) contract, making the same merge produce different bytes on
// each replica (so the next sync re-conflicts), and poisoning the import
// stale-guard. Whenever the plan writes ANYTHING it must therefore also write
// updated_at explicitly, including the case where OUR timestamp is the max.
func TestMergeIssuesConflictRow_UpdatedAtAlwaysWrittenWithTheMerge(t *testing.T) {
	// Ours is the later writer; they made a disjoint edit that we must keep.
	row := conflictRowFor(t, map[string][3]any{
		"id":         {"bd-11", "bd-11", "bd-11"},
		"status":     {"open", "in_progress", "open"},
		"assignee":   {"", "", "alice"},
		"updated_at": {tsBase, tsTheirs, tsOurs}, // ours = 12:00, theirs = 11:00
	})
	m, ok := mergeIssuesConflictRow(row)
	if !ok {
		t.Fatal("expected disjoint-field conflict to merge")
	}
	if v, written := m.merged("assignee"); !written || v != "alice" {
		t.Fatalf("their disjoint edit must survive, got %v (written=%v)", v, written)
	}
	v, written := m.merged("updated_at")
	if !written {
		t.Fatal("updated_at must be written explicitly whenever the plan updates the row (ON UPDATE CURRENT_TIMESTAMP would restamp it otherwise)")
	}
	if v != tsTheirs {
		t.Errorf("updated_at must be the merged max (our later value), got %v", v)
	}
}

// TestMergeIssuesConflictRow_NoWriteNoUpdatedAt pins the other half: when the
// merge equals our row, no UPDATE runs at all, so nothing is written and the
// ON UPDATE clause never fires.
func TestMergeIssuesConflictRow_NoWriteNoUpdatedAt(t *testing.T) {
	row := conflictRowFor(t, map[string][3]any{
		"id":         {"bd-12", "bd-12", "bd-12"},
		"status":     {"open", "in_progress", "open"}, // only we changed it
		"updated_at": {tsBase, tsTheirs, tsBase},
	})
	m, ok := mergeIssuesConflictRow(row)
	if !ok {
		t.Fatal("expected the row to merge")
	}
	if len(m.columns) != 0 {
		t.Errorf("nothing should be written when our row already IS the merge, got %v", m.columns)
	}
}

// TestMergeIssuesConflictRow_CloseGroupIsAtomic pins that the close columns
// move together. Per-cell independence would mix one side's closed_at with the
// other side's close_reason or closed_by_session — a close no write path
// produces. Both sides closed here (two closes with different details); a
// closed-versus-non-closed contest declines instead
// (TestMergeIssuesConflictRow_CloseRaceDeclines).
func TestMergeIssuesConflictRow_CloseGroupIsAtomic(t *testing.T) {
	twoCloses := func(id string, ourUpdated, theirUpdated string) rawConflictRow {
		return conflictRowFor(t, map[string][3]any{
			"id":                {id, id, id},
			"status":            {"open", "closed", "closed"},
			"closed_at":         {nil, "2026-07-10 11:00:00", "2026-07-10 12:00:00"},
			"close_reason":      {"", "done here", "done there"},
			"closed_by_session": {"", "sess-ours", ""}, // only we set it
			"updated_at":        {tsBase, ourUpdated, theirUpdated},
		})
	}
	t.Run("theirs wins the group", func(t *testing.T) {
		m, ok := mergeIssuesConflictRow(twoCloses("bd-13", tsOurs, tsTheirs))
		if !ok {
			t.Fatal("expected two contested closes to merge by LWW")
		}
		if v, written := m.merged("closed_at"); !written || v != "2026-07-10 12:00:00" {
			t.Errorf("closed_at must come from the LWW winner, got %v (written=%v)", v, written)
		}
		if v, written := m.merged("close_reason"); !written || v != "done there" {
			t.Errorf("close_reason must come from the LWW winner, got %v (written=%v)", v, written)
		}
		// The member only WE changed still follows the winner: that is what
		// makes the group atomic.
		if v, written := m.merged("closed_by_session"); !written || v != "" {
			t.Errorf("closed_by_session must follow the group to the winner, got %v (written=%v)", v, written)
		}
		if _, written := m.merged("status"); written {
			t.Error("an agreeing status must not be written")
		}
	})
	t.Run("ours wins the group", func(t *testing.T) {
		m, ok := mergeIssuesConflictRow(twoCloses("bd-14", tsTheirs, tsOurs))
		if !ok {
			t.Fatal("expected two contested closes to merge by LWW")
		}
		for _, col := range []string{"status", "closed_at", "close_reason", "closed_by_session"} {
			if _, written := m.merged(col); written {
				t.Errorf("our close state must stand whole; %s was overwritten", col)
			}
		}
	})
	t.Run("ambiguous timestamps decline", func(t *testing.T) {
		if _, ok := mergeIssuesConflictRow(twoCloses("bd-15", tsOurs, tsOurs)); ok {
			t.Error("a contested close group with equal timestamps must go to the operator")
		}
	})
}

// TestMergeIssuesConflictRow_NonClosedStatusContestKeepsLWW pins the other
// half of the close-race carve-out: two non-closed statuses (built-in or
// custom) contested with distinct timestamps still settle last-write-wins with
// the close group moving atomically, in either timestamp order.
func TestMergeIssuesConflictRow_NonClosedStatusContestKeepsLWW(t *testing.T) {
	for _, tc := range []struct {
		name             string
		ours, theirs     string
		ourTS, theirTS   string
		wantTheirsStatus bool
	}{
		{"builtin theirs newer", "in_progress", "blocked", tsOurs, tsTheirs, true},
		{"builtin ours newer", "in_progress", "blocked", tsTheirs, tsOurs, false},
		{"custom theirs newer", "in_progress", "review", tsOurs, tsTheirs, true},
		{"custom ours newer", "review", "deferred", tsTheirs, tsOurs, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := conflictRowFor(t, map[string][3]any{
				"id":           {"bd-30", "bd-30", "bd-30"},
				"status":       {"open", tc.ours, tc.theirs},
				"closed_at":    {nil, nil, nil},
				"close_reason": {"", "", ""},
				"updated_at":   {tsBase, tc.ourTS, tc.theirTS},
			})
			m, ok := mergeIssuesConflictRow(row)
			if !ok {
				t.Fatal("a non-closed status contest with distinct timestamps must merge by LWW")
			}
			v, written := m.merged("status")
			if tc.wantTheirsStatus && (!written || v != tc.theirs) {
				t.Errorf("their newer status must win, got %v (written=%v)", v, written)
			}
			if !tc.wantTheirsStatus && written {
				t.Errorf("our newer status must stand, got %v written", v)
			}
			if m.closeRace != nil {
				t.Errorf("a non-closed contest is not a close race, got %+v", *m.closeRace)
			}
		})
	}

	t.Run("both closed keeps atomic LWW", func(t *testing.T) {
		for _, tc := range []struct {
			name           string
			ourTS, theirTS string
			wantTheirs     bool
		}{
			{"theirs newer", tsOurs, tsTheirs, true},
			{"ours newer", tsTheirs, tsOurs, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				row := conflictRowFor(t, map[string][3]any{
					"id":                {"bd-31", "bd-31", "bd-31"},
					"status":            {"in_progress", "closed", "closed"},
					"closed_at":         {nil, "2026-07-10 11:00:00", "2026-07-10 12:00:00"},
					"close_reason":      {"", "fixed", "duplicate"},
					"closed_by_session": {"", "", "sess-theirs"},
					"updated_at":        {tsBase, tc.ourTS, tc.theirTS},
				})
				m, ok := mergeIssuesConflictRow(row)
				if !ok {
					t.Fatal("two closes with distinct valid timestamps must merge by LWW")
				}
				if m.closeRace != nil {
					t.Errorf("two closes are not a close race, got %+v", *m.closeRace)
				}
				for col, theirVal := range map[string]any{
					"closed_at":         "2026-07-10 12:00:00",
					"close_reason":      "duplicate",
					"closed_by_session": "sess-theirs",
				} {
					v, written := m.merged(col)
					if tc.wantTheirs && (!written || v != theirVal) {
						t.Errorf("%s must come from the newer close, got %v (written=%v)", col, v, written)
					}
					if !tc.wantTheirs && written {
						t.Errorf("our newer close must stand whole; %s was overwritten with %v", col, v)
					}
				}
			})
		}
	})
}

// TestMergeIssuesConflictRow_NonScalarColumnsDecline pins that the columns bd
// merges structurally are never settled by per-cell LWW: `notes` is
// append-only (bd note = --append-notes) and `metadata` is a JSON object
// mutated key-wise, so LWW would silently delete one side's append or key.
func TestMergeIssuesConflictRow_NonScalarColumnsDecline(t *testing.T) {
	for _, col := range []string{"notes", "metadata"} {
		t.Run(col+" contested declines", func(t *testing.T) {
			row := conflictRowFor(t, map[string][3]any{
				"id":         {"bd-16", "bd-16", "bd-16"},
				col:          {"base", "base + ours", "base + theirs"},
				"updated_at": {tsBase, tsOurs, tsTheirs},
			})
			if _, ok := mergeIssuesConflictRow(row); ok {
				t.Errorf("a contested %s must go to the operator, not be settled by LWW", col)
			}
		})
		t.Run(col+" one-sided still merges", func(t *testing.T) {
			row := conflictRowFor(t, map[string][3]any{
				"id":         {"bd-17", "bd-17", "bd-17"},
				col:          {"base", "base", "base + theirs"},
				"assignee":   {"", "alice", ""},
				"updated_at": {tsBase, tsOurs, tsTheirs},
			})
			m, ok := mergeIssuesConflictRow(row)
			if !ok {
				t.Fatalf("a one-sided %s edit is not contested and must merge", col)
			}
			if v, written := m.merged(col); !written || v != "base + theirs" {
				t.Errorf("their %s append must survive, got %v (written=%v)", col, v, written)
			}
		})
	}
}

// TestMergeIssuesConflictRow_ContestedCellsAreNamed pins that every cell
// settled by timestamp is recorded, so the resolver can name the supersession
// on stderr instead of dropping an edit silently.
func TestMergeIssuesConflictRow_ContestedCellsAreNamed(t *testing.T) {
	row := conflictRowFor(t, map[string][3]any{
		"id":         {"bd-18", "bd-18", "bd-18"},
		"title":      {"seed", "ours", "theirs"},
		"assignee":   {"", "", "alice"},
		"updated_at": {tsBase, tsOurs, tsTheirs},
	})
	m, ok := mergeIssuesConflictRow(row)
	if !ok {
		t.Fatal("expected the row to merge")
	}
	named := map[string]bool{}
	for _, c := range m.lww {
		named[c] = true
	}
	if !named["title"] {
		t.Errorf("the contested cell must be named for the operator notice, got %v", m.lww)
	}
	if named["assignee"] {
		t.Errorf("a one-sided edit is not a supersession and must not be named, got %v", m.lww)
	}
}

// TestUnionRowIsSafe covers the union safety property directly: only a row
// present on both sides with agreeing columns may be unioned.
func TestUnionRowIsSafe(t *testing.T) {
	keyCols := []string{"issue_id", "label"}
	t.Run("identical row on both sides", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"issue_id": {"bd-1", "bd-1", "bd-1"},
			"label":    {"tier:opus", "tier:opus", "tier:opus"},
		})
		key, ok := unionRowIsSafe(row, keyCols)
		if !ok {
			t.Fatal("an identical row on both sides must be unionable")
		}
		if len(key.values) != 2 || key.values[0] != "bd-1" || key.values[1] != "tier:opus" {
			t.Errorf("union key must carry our side's key values, got %v", key.values)
		}
	})
	t.Run("missing on one side declines", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"issue_id": {"bd-1", nil, "bd-1"},
			"label":    {"tier:opus", nil, "tier:opus"},
		})
		if _, ok := unionRowIsSafe(row, keyCols); ok {
			t.Error("a deletion racing an insert must go to the operator, not be unioned")
		}
	})
	t.Run("diverging columns decline", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":     {"c-1", "c-1", "c-1"},
			"text":   {"hi", "hi there", "hi again"},
			"author": {"a", "a", "a"},
		})
		if _, ok := unionRowIsSafe(row, []string{"id"}); ok {
			t.Error("an append-only row whose columns diverge must go to the operator")
		}
	})
}

// TestResolveIssuesFieldMerge_ZeroAffectedRowsIsNotProofOfDeletion pins the
// distinction the resolver used to miss: without clientFoundRows an UPDATE
// reports rows CHANGED, so a merged write the backend normalizes to the stored
// bytes affects zero rows even though the row is right there. The resolver must
// confirm with a matched-rows check before it accuses another session of having
// deleted the row, and must still refuse when the row really is gone.
func TestResolveIssuesFieldMerge_ZeroAffectedRowsIsNotProofOfDeletion(t *testing.T) {
	ctx := context.Background()
	plan := []issuesRowMerge{{
		ourKey:  "bd-1",
		columns: []string{"status", "updated_at"},
		values:  []any{"open", "2026-07-25 10:00:00"},
	}}
	const updateSQL = "UPDATE `issues` SET `status` = ?, `updated_at` = ? WHERE `id` = ?"
	const existsSQL = "SELECT COUNT(*) FROM `issues` WHERE `id` = ?"
	const deleteSQL = "DELETE FROM dolt_conflicts_issues WHERE our_id = ?"

	t.Run("no-op write on a live row resolves", func(t *testing.T) {
		db, mock := newMockDB(t)
		mock.ExpectExec(regexp.QuoteMeta(updateSQL)).
			WithArgs("open", "2026-07-25 10:00:00", "bd-1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta(existsSQL)).WithArgs("bd-1").
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
		mock.ExpectExec(regexp.QuoteMeta(deleteSQL)).WithArgs("bd-1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		if err := resolveIssuesFieldMerge(ctx, db, plan); err != nil {
			t.Fatalf("a write the backend normalized to a no-op must not abort the merge: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("an unreadable RowsAffected still checks the row", func(t *testing.T) {
		db, mock := newMockDB(t)
		mock.ExpectExec(regexp.QuoteMeta(updateSQL)).
			WithArgs("open", "2026-07-25 10:00:00", "bd-1").
			WillReturnResult(sqlmock.NewErrorResult(errors.New("driver has no row count")))
		mock.ExpectQuery(regexp.QuoteMeta(existsSQL)).WithArgs("bd-1").
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
		mock.ExpectExec(regexp.QuoteMeta(deleteSQL)).WithArgs("bd-1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		if err := resolveIssuesFieldMerge(ctx, db, plan); err != nil {
			t.Fatalf("an unreadable affected-row count must be settled by the check, not by guessing: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("a genuinely vanished row is still refused", func(t *testing.T) {
		db, mock := newMockDB(t)
		mock.ExpectExec(regexp.QuoteMeta(updateSQL)).
			WithArgs("open", "2026-07-25 10:00:00", "bd-1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta(existsSQL)).WithArgs("bd-1").
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
		// No delete: the conflict must be left for the operator.

		err := resolveIssuesFieldMerge(ctx, db, plan)
		if err == nil || !strings.Contains(err.Error(), "deleted concurrently") {
			t.Fatalf("err = %v, want the concurrent-deletion refusal", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("a failed check refuses rather than clearing the conflict", func(t *testing.T) {
		db, mock := newMockDB(t)
		mock.ExpectExec(regexp.QuoteMeta(updateSQL)).
			WithArgs("open", "2026-07-25 10:00:00", "bd-1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta(existsSQL)).WithArgs("bd-1").
			WillReturnError(errors.New("connection reset"))

		err := resolveIssuesFieldMerge(ctx, db, plan)
		if err == nil || !strings.Contains(err.Error(), "confirm issue") {
			t.Fatalf("err = %v, want the unconfirmable-row refusal", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("an ordinary changed row skips the extra check", func(t *testing.T) {
		db, mock := newMockDB(t)
		mock.ExpectExec(regexp.QuoteMeta(updateSQL)).
			WithArgs("open", "2026-07-25 10:00:00", "bd-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta(deleteSQL)).WithArgs("bd-1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		if err := resolveIssuesFieldMerge(ctx, db, plan); err != nil {
			t.Fatalf("resolveIssuesFieldMerge: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

// TestDuplicateConflictKey covers the second half: the resolvers delete by
// our-side key, so two live conflict rows sharing one key would be cleared
// together and make the next iteration abort on a message about the wrong
// thing. Rows with no our-side key cannot collide in a keyed delete and must
// keep flowing to the per-row safety checks.
func TestDuplicateConflictKey(t *testing.T) {
	rowsOf := func(cells ...map[string][3]any) []rawConflictRow {
		rows := make([]rawConflictRow, 0, len(cells))
		for _, c := range cells {
			rows = append(rows, conflictRowFor(t, c))
		}
		return rows
	}
	issuesKey := []string{issuesKeyColumn}

	t.Run("two rows for one issue are reported", func(t *testing.T) {
		rows := rowsOf(
			map[string][3]any{"id": {"bd-1", "bd-1", "bd-1"}, "status": {"open", "open", "closed"}},
			map[string][3]any{"id": {"bd-1", "bd-1", "bd-1"}, "status": {"open", "closed", "open"}},
		)
		dup, ok := duplicateConflictKey(issuesKey, rows)
		if !ok || dup != "bd-1" {
			t.Fatalf("duplicateConflictKey = %q, %v; want bd-1, true", dup, ok)
		}
	})

	t.Run("distinct issues pass", func(t *testing.T) {
		rows := rowsOf(
			map[string][3]any{"id": {"bd-1", "bd-1", "bd-1"}},
			map[string][3]any{"id": {"bd-2", "bd-2", "bd-2"}},
		)
		if dup, ok := duplicateConflictKey(issuesKey, rows); ok {
			t.Fatalf("distinct keys must pass, got duplicate %q", dup)
		}
	})

	t.Run("byte and string spellings of one key still collide", func(t *testing.T) {
		rows := rowsOf(
			map[string][3]any{"id": {"bd-1", []byte("bd-1"), "bd-1"}},
			map[string][3]any{"id": {"bd-1", "bd-1", "bd-1"}},
		)
		if _, ok := duplicateConflictKey(issuesKey, rows); !ok {
			t.Error("a []byte key and its string spelling name the same row; the guard must see through the driver's choice")
		}
	})

	t.Run("rows with no our side are left to the safety checks", func(t *testing.T) {
		// Both are delete/modify conflicts of ONE issue: their-side keys agree,
		// and only the NULL our-side keeps them from reading as duplicates.
		// Such a row is never the target of a keyed delete, so declining the
		// whole table here would refuse merges the safety checks handle.
		rows := rowsOf(
			map[string][3]any{"id": {"bd-1", nil, "bd-1"}},
			map[string][3]any{"id": {"bd-1", nil, "bd-1"}},
		)
		if dup, ok := duplicateConflictKey(issuesKey, rows); ok {
			t.Fatalf("delete/modify conflicts must not be read as duplicates, got %q", dup)
		}
	})

	t.Run("union tables key on every key column", func(t *testing.T) {
		keyCols := unionConflictKeyColumns["labels"]
		same := rowsOf(
			map[string][3]any{"issue_id": {"bd-1", "bd-1", "bd-1"}, "label": {"tier:opus", "tier:opus", "tier:opus"}},
			map[string][3]any{"issue_id": {"bd-1", "bd-1", "bd-1"}, "label": {"tier:opus", "tier:opus", "tier:opus"}},
		)
		if _, ok := duplicateConflictKey(keyCols, same); !ok {
			t.Error("two conflict rows for one (issue_id, label) must be reported")
		}
		differing := rowsOf(
			map[string][3]any{"issue_id": {"bd-1", "bd-1", "bd-1"}, "label": {"tier:opus", "tier:opus", "tier:opus"}},
			map[string][3]any{"issue_id": {"bd-1", "bd-1", "bd-1"}, "label": {"tier:fleet", "tier:fleet", "tier:fleet"}},
		)
		if dup, ok := duplicateConflictKey(keyCols, differing); ok {
			t.Fatalf("two labels of one issue are distinct rows, got duplicate %q", dup)
		}
	})

	t.Run("no key columns means no guard", func(t *testing.T) {
		rows := rowsOf(map[string][3]any{"id": {"x", "x", "x"}}, map[string][3]any{"id": {"x", "x", "x"}})
		if _, ok := duplicateConflictKey(nil, rows); ok {
			t.Error("without known key columns there is no keyed delete to guard")
		}
	})
}

// TestDuplicateConflictRowsDeclineRatherThanError pins the shape of the
// refusal, not just its trigger. The pre-screens must DECLINE (ok=false, nil
// error) so the caller still builds the MergeConflictsError that names the
// unresolved tables for the operator; a hard error there is swallowed by
// SettleMerge and turns a reportable conflict into an opaque pull failure.
func TestDuplicateConflictRowsDeclineRatherThanError(t *testing.T) {
	ctx := context.Background()

	t.Run("issues", func(t *testing.T) {
		db, mock := newMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `dolt_conflicts_issues`")).
			WillReturnRows(sqlmock.NewRows([]string{"base_id", "our_id", "their_id"}).
				AddRow("bd-1", "bd-1", "bd-1").
				AddRow("bd-1", "bd-1", "bd-1"))

		plan, mergeable, err := issuesConflictsAreFieldMergeable(ctx, db)
		if err != nil {
			t.Fatalf("duplicate rows must decline, not error: %v", err)
		}
		if mergeable || plan != nil {
			t.Fatalf("mergeable = %v, plan = %v; want a decline", mergeable, plan)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("union tables", func(t *testing.T) {
		db, mock := newMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `dolt_conflicts_labels`")).
			WillReturnRows(sqlmock.NewRows([]string{"base_issue_id", "our_issue_id", "their_issue_id", "base_label", "our_label", "their_label"}).
				AddRow("bd-1", "bd-1", "bd-1", "tier:opus", "tier:opus", "tier:opus").
				AddRow("bd-1", "bd-1", "bd-1", "tier:opus", "tier:opus", "tier:opus"))

		plan, safe, err := unionConflictsAreSafe(ctx, db, "labels")
		if err != nil {
			t.Fatalf("duplicate rows must decline, not error: %v", err)
		}
		if safe || plan != nil {
			t.Fatalf("safe = %v, plan = %v; want a decline", safe, plan)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

// TestIssuesConflictsAreFieldMergeableLoadsEveryRow guards the guard: distinct
// conflict rows must still flow through the real entry point and be planned.
func TestIssuesConflictsAreFieldMergeableLoadsEveryRow(t *testing.T) {
	ctx := context.Background()
	db, mock := newMockDB(t)
	cols := []string{"base_id", "our_id", "their_id", "base_status", "our_status", "their_status", "base_updated_at", "our_updated_at", "their_updated_at"}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `dolt_conflicts_issues`")).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("bd-1", "bd-1", "bd-1", "open", "open", "closed", "2026-07-25 09:00:00", "2026-07-25 09:00:00", "2026-07-25 10:00:00").
			AddRow("bd-2", "bd-2", "bd-2", "open", "open", "closed", "2026-07-25 09:00:00", "2026-07-25 09:00:00", "2026-07-25 10:00:00"))

	plan, mergeable, err := issuesConflictsAreFieldMergeable(ctx, db)
	if err != nil {
		t.Fatalf("issuesConflictsAreFieldMergeable: %v", err)
	}
	if !mergeable || len(plan) != 2 {
		t.Fatalf("mergeable = %v, len(plan) = %d; want true, 2", mergeable, len(plan))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// newMockDB returns a sqlmock-backed *sql.DB, which satisfies DBConn.
func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// TestMergeIssuesConflictRow_SettledRowGetsFreshRowLock pins the CAS hazard fix
// absorbed from gastownhall/beads#4682: a settle that changes the row must mint
// a row_lock distinct from BOTH parents' tokens, or a stale ExpectedVersion
// read against one pre-merge row could win a CAS against content the merge just
// changed.
func TestMergeIssuesConflictRow_SettledRowGetsFreshRowLock(t *testing.T) {
	row := conflictRowFor(t, map[string][3]any{
		"id":         {"bd-20", "bd-20", "bd-20"},
		"status":     {"open", "in_progress", "open"}, // only we changed it
		"assignee":   {"", "", "alice"},               // only they changed it
		"row_lock":   {int64(7), int64(111), int64(222)},
		"updated_at": {tsBase, tsOurs, tsTheirs},
	})
	m, ok := mergeIssuesConflictRow(row)
	if !ok {
		t.Fatal("expected disjoint-field conflict to merge")
	}
	v, written := m.merged("row_lock")
	if !written {
		t.Fatal("a settle that writes the row must stamp a fresh row_lock")
	}
	lock, isInt := v.(int64)
	if !isInt {
		t.Fatalf("row_lock must be minted as int64, got %T", v)
	}
	if lock == 111 || lock == 222 {
		t.Errorf("fresh row_lock %d must differ from both parents' tokens", lock)
	}
	if lock == 0 {
		t.Error("row_lock must never be 0 (the column default)")
	}
}

// TestMergeIssuesConflictRow_RowLockOnlyDivergenceIsNotAConflict pins the other
// half of excluding row_lock from cell classification: when the only cell both
// sides moved is the token itself — two replicas independently settling the
// same merge mint different random tokens, then sync with equal updated_at —
// the row must merge cleanly with no write (our row already IS the merge), not
// decline on an LWW tie no timestamp can break.
func TestMergeIssuesConflictRow_RowLockOnlyDivergenceIsNotAConflict(t *testing.T) {
	row := conflictRowFor(t, map[string][3]any{
		"id":         {"bd-21", "bd-21", "bd-21"},
		"status":     {"open", "open", "open"},
		"row_lock":   {int64(7), int64(111), int64(222)},
		"updated_at": {tsBase, tsOurs, tsOurs}, // equal: LWW could never settle this
	})
	m, ok := mergeIssuesConflictRow(row)
	if !ok {
		t.Fatal("a row whose only divergence is the row_lock token must merge, not decline")
	}
	if len(m.columns) != 0 {
		t.Errorf("nothing should be written when our row already IS the merge, got %v", m.columns)
	}
}

// TestMergeIssuesConflictRow_NoRowLockColumnDegradesGracefully pins the
// defensive gate added in the adversarial review of ea1256462 (finding 5): a
// pre-0054 schema has no our_row_lock/their_row_lock column at all, and the
// settle must still merge and write its other columns without naming
// "row_lock" in the write-back plan -- naming a column the conflict table
// doesn't have would turn a clean auto-merge into a hard pull failure.
func TestMergeIssuesConflictRow_NoRowLockColumnDegradesGracefully(t *testing.T) {
	row := conflictRowFor(t, map[string][3]any{
		"id":         {"bd-22", "bd-22", "bd-22"},
		"status":     {"open", "in_progress", "open"}, // only we changed it
		"assignee":   {"", "", "alice"},               // only they changed it
		"updated_at": {tsBase, tsOurs, tsTheirs},
		// deliberately no "row_lock" key: simulates a pre-0054 schema.
	})
	m, ok := mergeIssuesConflictRow(row)
	if !ok {
		t.Fatal("expected disjoint-field conflict to merge even without a row_lock column")
	}
	if _, written := m.merged("row_lock"); written {
		t.Error("must not write row_lock when the conflict table has no our_row_lock/their_row_lock column")
	}
	if _, written := m.merged("assignee"); !written {
		t.Error("the settle's other columns must still be written")
	}
}

// TestFreshRowLockDistinctFrom pins the reroll contract across the value shapes
// a driver can hand back for the parents' tokens.
func TestFreshRowLockDistinctFrom(t *testing.T) {
	for i := 0; i < 64; i++ {
		v := freshRowLockDistinctFrom(int64(111), []byte("222"))
		if v == 111 || v == 222 {
			t.Fatalf("minted token %d collides with a parent token", v)
		}
		if v == 0 {
			t.Fatal("minted token must be non-zero")
		}
	}
}

// TestMergeIssuesConflictRow_CloseRaceDeclines pins GH#7783 shape 1: when the
// close group is contested and exactly one side's status is closed, the merge
// declines whatever the timestamps say. Last-write-wins there lets a claim
// made without seeing a remote close silently reopen the issue (or a close
// silently discard a concurrent claim), so the operator must choose.
func TestMergeIssuesConflictRow_CloseRaceDeclines(t *testing.T) {
	const closedAt = "2026-07-10 11:30:00"
	for _, tc := range []struct {
		name                 string
		status, closedAtCell [3]any
		reason               [3]any
		updated              [3]any
		extra                map[string][3]any
		wantOurs, wantTheirs string
	}{
		{
			name:         "theirs closed, ours claimed later",
			status:       [3]any{"open", "in_progress", "closed"},
			closedAtCell: [3]any{nil, nil, closedAt},
			reason:       [3]any{"", "", "done on b"},
			updated:      [3]any{tsBase, tsTheirs, tsOurs}, // our claim is newer
			wantOurs:     "in_progress", wantTheirs: "closed",
		},
		{
			name:         "theirs closed and newer",
			status:       [3]any{"open", "in_progress", "closed"},
			closedAtCell: [3]any{nil, nil, closedAt},
			reason:       [3]any{"", "", "done on b"},
			updated:      [3]any{tsBase, tsOurs, tsTheirs},
			wantOurs:     "in_progress", wantTheirs: "closed",
		},
		{
			name:         "ours closed, theirs claimed (swapped sides)",
			status:       [3]any{"open", "closed", "in_progress"},
			closedAtCell: [3]any{nil, closedAt, nil},
			reason:       [3]any{"", "done here", ""},
			updated:      [3]any{tsBase, tsOurs, tsTheirs},
			wantOurs:     "closed", wantTheirs: "in_progress",
		},
		{
			name:         "equal timestamps",
			status:       [3]any{"open", "in_progress", "closed"},
			closedAtCell: [3]any{nil, nil, closedAt},
			reason:       [3]any{"", "", "done"},
			updated:      [3]any{tsBase, tsOurs, tsOurs},
			wantOurs:     "in_progress", wantTheirs: "closed",
		},
		{
			name:         "malformed timestamp",
			status:       [3]any{"open", "in_progress", "closed"},
			closedAtCell: [3]any{nil, nil, closedAt},
			reason:       [3]any{"", "", "done"},
			updated:      [3]any{tsBase, "not-a-time", tsTheirs},
			wantOurs:     "in_progress", wantTheirs: "closed",
		},
		{
			name:         "byte-valued cells",
			status:       [3]any{[]byte("open"), []byte("in_progress"), "closed"},
			closedAtCell: [3]any{nil, nil, []byte(closedAt)},
			reason:       [3]any{[]byte(""), "", []byte("done")},
			updated:      [3]any{[]byte(tsBase), []byte(tsOurs), tsTheirs},
			wantOurs:     "in_progress", wantTheirs: "closed",
		},
		{
			name:         "custom non-closed status",
			status:       [3]any{"open", "review", "closed"},
			closedAtCell: [3]any{nil, nil, closedAt},
			reason:       [3]any{"", "", "done"},
			updated:      [3]any{tsBase, tsOurs, tsTheirs},
			wantOurs:     "review", wantTheirs: "closed",
		},
		{
			name: "contested reopen",
			// We reopened; they re-closed with a new reason: closed_at is
			// contested and exactly one side ends closed.
			status:       [3]any{"closed", "open", "closed"},
			closedAtCell: [3]any{"2026-07-10 10:00:00", nil, closedAt},
			reason:       [3]any{"first", "", "second"},
			updated:      [3]any{tsBase, tsOurs, tsTheirs},
			wantOurs:     "open", wantTheirs: "closed",
		},
		{
			name:         "simultaneous notes conflict does not hide it",
			status:       [3]any{"open", "in_progress", "closed"},
			closedAtCell: [3]any{nil, nil, closedAt},
			reason:       [3]any{"", "", "done"},
			updated:      [3]any{tsBase, tsOurs, tsTheirs},
			extra:        map[string][3]any{"notes": {"base", "base + ours", "base + theirs"}},
			wantOurs:     "in_progress", wantTheirs: "closed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cells := map[string][3]any{
				"id":           {"bd-40", "bd-40", "bd-40"},
				"status":       tc.status,
				"closed_at":    tc.closedAtCell,
				"close_reason": tc.reason,
				"updated_at":   tc.updated,
			}
			for k, v := range tc.extra {
				cells[k] = v
			}
			m, ok := mergeIssuesConflictRow(conflictRowFor(t, cells))
			if ok {
				t.Fatal("a contested closed/non-closed close group must decline, not settle last-write-wins")
			}
			if m.closeRace == nil {
				t.Fatal("the decline must carry the close-race diagnostic")
			}
			if got := *m.closeRace; got.issue != "bd-40" || got.ourStatus != tc.wantOurs || got.theirStatus != tc.wantTheirs {
				t.Errorf("closeRace = %+v; want issue bd-40, ours %q, theirs %q", got, tc.wantOurs, tc.wantTheirs)
			}
		})
	}

	t.Run("missing status column still declines", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":         {"bd-41", "bd-41", "bd-41"},
			"closed_at":  {nil, nil, closedAt},
			"updated_at": {tsBase, tsOurs, tsTheirs},
		})
		// Drop their_status-equivalent: the row carries our_status only.
		row.cols = append(row.cols, "our_status")
		row.vals = append(row.vals, "in_progress")
		if _, ok := mergeIssuesConflictRow(row); ok {
			t.Error("a row missing a side of a column must still go to the operator")
		}
	})

	t.Run("independent close plus title still merges", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":           {"bd-42", "bd-42", "bd-42"},
			"title":        {"seed", "ours", "seed"},
			"status":       {"open", "open", "closed"},
			"closed_at":    {nil, nil, closedAt},
			"close_reason": {"", "", "done"},
			"updated_at":   {tsBase, tsOurs, tsTheirs},
		})
		m, ok := mergeIssuesConflictRow(row)
		if !ok {
			t.Fatal("an uncontested close beside a disjoint title edit must merge")
		}
		if v, written := m.merged("status"); !written || v != "closed" {
			t.Errorf("their close must survive, got %v (written=%v)", v, written)
		}
		if m.closeRace != nil {
			t.Errorf("an uncontested close is not a close race, got %+v", *m.closeRace)
		}
	})

	t.Run("one-sided reopen still merges", func(t *testing.T) {
		row := conflictRowFor(t, map[string][3]any{
			"id":           {"bd-43", "bd-43", "bd-43"},
			"title":        {"seed", "seed", "theirs"},
			"status":       {"closed", "open", "closed"},
			"closed_at":    {closedAt, nil, closedAt},
			"close_reason": {"done", "", "done"},
			"updated_at":   {tsBase, tsOurs, tsTheirs},
		})
		m, ok := mergeIssuesConflictRow(row)
		if !ok {
			t.Fatal("an intentional one-sided reopen beside a disjoint edit must merge")
		}
		if _, written := m.merged("status"); written {
			t.Error("our reopen must stand")
		}
		if m.closeRace != nil {
			t.Errorf("a one-sided reopen is not a close race, got %+v", *m.closeRace)
		}
	})
}

// TestIssuesConflictsAreFieldMergeable_CloseRaceNotice pins the wrapper half
// of the close-race decline: every loaded row is scanned, so an earlier
// ordinary decline does not hide a later close race; the notice names the
// issue and both statuses; and nothing is planned or written.
func TestIssuesConflictsAreFieldMergeable_CloseRaceNotice(t *testing.T) {
	ctx := context.Background()
	db, mock := newMockDB(t)
	cols := []string{
		"base_id", "our_id", "their_id",
		"base_status", "our_status", "their_status",
		"base_closed_at", "our_closed_at", "their_closed_at",
		"base_notes", "our_notes", "their_notes",
		"base_updated_at", "our_updated_at", "their_updated_at",
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `dolt_conflicts_issues`")).
		WillReturnRows(sqlmock.NewRows(cols).
			// bd-1: an ordinary decline (contested notes), loaded first.
			AddRow("bd-1", "bd-1", "bd-1", "open", "open", "open", nil, nil, nil,
				"base", "base + ours", "base + theirs", tsBase, tsOurs, tsTheirs).
			// bd-2: their close raced our later claim.
			AddRow("bd-2", "bd-2", "bd-2", "open", "in_progress", "closed", nil, nil, "2026-07-10 11:30:00",
				"", "", "", tsBase, tsTheirs, tsOurs).
			// bd-3: an ordinary mergeable row.
			AddRow("bd-3", "bd-3", "bd-3", "open", "open", "blocked", nil, nil, nil,
				"", "", "", tsBase, tsOurs, tsTheirs))

	var notices strings.Builder
	prev := mergeNoticeOut
	mergeNoticeOut = &notices
	t.Cleanup(func() { mergeNoticeOut = prev })

	plan, mergeable, err := issuesConflictsAreFieldMergeable(ctx, db)
	if err != nil {
		t.Fatalf("a close race must decline, not error: %v", err)
	}
	if mergeable || plan != nil {
		t.Fatalf("mergeable = %v, plan = %v; want a decline with no plan", mergeable, plan)
	}
	got := notices.String()
	for _, want := range []string{"bd-2", "closed", "in_progress"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice must name %q; got %q", want, got)
		}
	}
	if strings.Contains(got, "bd-1") || strings.Contains(got, "bd-3") {
		t.Errorf("only the close race earns the close-race notice; got %q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
