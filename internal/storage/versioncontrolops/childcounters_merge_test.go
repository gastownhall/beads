package versioncontrolops

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// counterConflictRow builds a dolt_conflicts_child_counters row. A nil
// parent on a side stands for the NULLs dolt writes when that side has no row
// (add/add has no base; modify/delete has no our or their row).
func counterConflictRow(t *testing.T, parent [3]any, lastChild [3]any) rawConflictRow {
	t.Helper()
	return conflictRowFor(t, map[string][3]any{
		"parent_id":  parent,
		"last_child": lastChild,
	})
}

// TestMergeChildCountersConflictRow pins the counter merge rule (bd-28sb25,
// GH#4796): child_counters.last_child is a high-water mark, so a row present
// on BOTH sides with valid values merges to the maximum whichever side holds
// it, with or without a base row. A missing side is a deletion, never zero,
// and a malformed value is never guessed at: both decline.
func TestMergeChildCountersConflictRow(t *testing.T) {
	p := "bd-epic"
	cases := []struct {
		name      string
		parent    [3]any
		lastChild [3]any
		wantOK    bool
		want      int64
		wantWrite bool // merged value differs from ours, so ours must be rewritten
	}{
		{name: "theirs_lower", parent: [3]any{p, p, p}, lastChild: [3]any{int64(70), int64(73), int64(72)}, wantOK: true, want: 73},
		{name: "theirs_higher", parent: [3]any{p, p, p}, lastChild: [3]any{int64(70), int64(72), int64(73)}, wantOK: true, want: 73, wantWrite: true},
		{name: "add_add", parent: [3]any{nil, p, p}, lastChild: [3]any{nil, int64(2), int64(5)}, wantOK: true, want: 5, wantWrite: true},
		{name: "equal", parent: [3]any{p, p, p}, lastChild: [3]any{int64(1), int64(4), int64(4)}, wantOK: true, want: 4},
		{name: "text_values", parent: [3]any{[]byte(p), []byte(p), []byte(p)}, lastChild: [3]any{[]byte("70"), []byte("71"), []byte("75")}, wantOK: true, want: 75, wantWrite: true},
		{name: "zero_is_valid", parent: [3]any{nil, p, p}, lastChild: [3]any{nil, int64(0), int64(3)}, wantOK: true, want: 3, wantWrite: true},
		{name: "our_side_deleted", parent: [3]any{p, nil, p}, lastChild: [3]any{int64(70), nil, int64(72)}},
		{name: "their_side_deleted", parent: [3]any{p, p, nil}, lastChild: [3]any{int64(70), int64(73), nil}},
		{name: "malformed_ours", parent: [3]any{p, p, p}, lastChild: [3]any{int64(70), "seventy-three", int64(72)}},
		{name: "malformed_theirs", parent: [3]any{p, p, p}, lastChild: [3]any{int64(70), int64(73), []byte("7x")}},
		{name: "negative", parent: [3]any{p, p, p}, lastChild: [3]any{int64(70), int64(-1), int64(72)}},
		{name: "null_value", parent: [3]any{p, p, p}, lastChild: [3]any{int64(70), nil, int64(72)}},
		{name: "keys_differ", parent: [3]any{p, p, "bd-other"}, lastChild: [3]any{int64(70), int64(73), int64(72)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := mergeChildCountersConflictRow(counterConflictRow(t, tc.parent, tc.lastChild))
			if ok != tc.wantOK {
				t.Fatalf("mergeable = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if m.lastChild != tc.want {
				t.Errorf("merged last_child = %d, want %d", m.lastChild, tc.want)
			}
			if m.write != tc.wantWrite {
				t.Errorf("write = %v, want %v", m.write, tc.wantWrite)
			}
			if got := formatConflictValue(m.parentID); got == nil || *got != p {
				t.Errorf("parent key = %v, want %s", m.parentID, p)
			}
		})
	}
}

// TestMergeChildCountersConflictRow_UnknownColumnDeclines guards the
// "unknown shapes decline" rule: a peer schema that added a data column the
// two sides disagree on is not a pure high-water-mark conflict.
func TestMergeChildCountersConflictRow_UnknownColumnDeclines(t *testing.T) {
	p := "bd-epic"
	row := conflictRowFor(t, map[string][3]any{
		"parent_id":  {p, p, p},
		"last_child": {int64(70), int64(73), int64(72)},
		"extra":      {"a", "b", "c"},
	})
	if _, ok := mergeChildCountersConflictRow(row); ok {
		t.Fatal("a conflict on an unknown child_counters column must decline")
	}
}

// TestSettleMergeDeclinesUnsafeChildCounterConflicts is bd-28sb25 AC-9 at
// the settlement layer: a child_counters conflict row missing on one side or
// carrying a malformed last_child (which the INT column cannot hold, so the
// row is synthetic) makes settlement decline with a MergeConflictsError that
// names child_counters, after aborting the merge. Ordered sqlmock fails on any
// UPDATE or conflict DELETE, so no row of the table, valid or not, is resolved.
func TestSettleMergeDeclinesUnsafeChildCounterConflicts(t *testing.T) {
	cols := []string{"base_parent_id", "our_parent_id", "their_parent_id",
		"base_last_child", "our_last_child", "their_last_child", "our_diff_type", "their_diff_type"}
	cases := map[string][][]driver.Value{
		"malformed": {
			{"bd-a", "bd-a", "bd-a", int64(70), int64(73), int64(72), "modified", "modified"},
			{"bd-b", "bd-b", "bd-b", int64(1), "two", int64(3), "modified", "modified"},
		},
		"their_side_deleted": {
			{"bd-a", "bd-a", "bd-a", int64(70), int64(71), int64(75), "modified", "modified"},
			{"bd-b", "bd-b", nil, int64(70), int64(73), nil, "modified", "removed"},
		},
		"our_side_deleted": {
			{"bd-b", nil, "bd-b", int64(70), nil, int64(72), "removed", "modified"},
		},
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			rec, mock := newDrainMock(t)
			expectConflicts(mock, "child_counters")
			conflictRows := sqlmock.NewRows(cols)
			for _, r := range rows {
				conflictRows.AddRow(r...)
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `dolt_conflicts_child_counters`")).WillReturnRows(conflictRows)
			expectConflicts(mock, "child_counters")
			expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")

			err := SettleMerge(context.Background(), rec, nil, true, "")
			var mce *MergeConflictsError
			if !errors.As(err, &mce) {
				t.Fatalf("SettleMerge = %v, want a MergeConflictsError", err)
			}
			if len(mce.Conflicts) != 1 || mce.Conflicts[0].Field != "child_counters" {
				t.Errorf("conflicts = %+v, want child_counters", mce.Conflicts)
			}
			if !strings.Contains(err.Error(), "child_counters") {
				t.Errorf("error %q does not name child_counters", err)
			}
			if rec.sentStatement("UPDATE") || rec.sentStatement("DELETE") {
				t.Error("a declined child_counters conflict must apply no resolution")
			}
			rec.assertDrained(t, mock)
		})
	}
}
