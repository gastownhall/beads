package versioncontrolops

import "testing"

// counterConflictRow builds one dolt_conflicts_child_counters row in raw
// scanned form: parent keys per side, base/our/their last_child values. A nil
// value is the side's NULL (the row or column is absent on that side).
func counterConflictRow(baseParent any, ourParent, theirParent any, baseLast, ourLast, theirLast any) rawConflictRow {
	return rawConflictRow{
		cols: []string{
			"base_parent_id", "our_parent_id", "their_parent_id",
			"base_last_child", "our_last_child", "their_last_child",
			"our_diff_type", "their_diff_type",
		},
		vals: []any{baseParent, ourParent, theirParent, baseLast, ourLast, theirLast, nil, nil},
	}
}

// TestMergeChildCountersConflictRow pins the pure counter-merger's every
// rule (GH#4796, bd-28sb25 AC-1/AC-9): a both-present same-parent conflict
// settles to max — ours higher, theirs higher, equal values, and add/add
// alike — while a row missing on one side, differing parent keys, or a
// last_child that is not an integer declines to the operator.
func TestMergeChildCountersConflictRow(t *testing.T) {
	tests := []struct {
		name       string
		row        rawConflictRow
		wantParent string
		wantLast   int64
		wantOK     bool
	}{
		{
			name:       "ours higher settles to ours",
			row:        counterConflictRow("bd-cc", "bd-cc", "bd-cc", 70, 73, 72),
			wantParent: "bd-cc",
			wantLast:   73,
			wantOK:     true,
		},
		{
			name:       "theirs higher settles to theirs",
			row:        counterConflictRow("bd-cc", "bd-cc", "bd-cc", 70, 72, 73),
			wantParent: "bd-cc",
			wantLast:   73,
			wantOK:     true,
		},
		{
			name:       "equal values settle to the same max",
			row:        counterConflictRow("bd-cc", "bd-cc", "bd-cc", 70, 71, 71),
			wantParent: "bd-cc",
			wantLast:   71,
			wantOK:     true,
		},
		{
			name:       "add/add (no base row) settles to max",
			row:        counterConflictRow(nil, "bd-cc", "bd-cc", nil, 71, 72),
			wantParent: "bd-cc",
			wantLast:   72,
			wantOK:     true,
		},
		{
			name:   "our side deleted the counter row",
			row:    counterConflictRow("bd-cc", nil, "bd-cc", 70, nil, 73),
			wantOK: false,
		},
		{
			name:   "their side deleted the counter row",
			row:    counterConflictRow("bd-cc", "bd-cc", nil, 70, 73, nil),
			wantOK: false,
		},
		{
			name:   "our last_child malformed",
			row:    counterConflictRow("bd-cc", "bd-cc", "bd-cc", 70, "seventy-three", 72),
			wantOK: false,
		},
		{
			name:   "their last_child malformed",
			row:    counterConflictRow("bd-cc", "bd-cc", "bd-cc", 70, 73, "nan"),
			wantOK: false,
		},
		{
			name:   "our last_child negative",
			row:    counterConflictRow("bd-cc", "bd-cc", "bd-cc", 70, -5, 72),
			wantOK: false,
		},
		{
			name:   "their last_child negative",
			row:    counterConflictRow("bd-cc", "bd-cc", "bd-cc", 70, 73, -1),
			wantOK: false,
		},
		{
			name:   "parent keys differ",
			row:    counterConflictRow("bd-cc", "bd-cc", "bd-other", 70, 73, 72),
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := mergeChildCountersConflictRow(tt.row)
			if ok != tt.wantOK {
				t.Fatalf("mergeChildCountersConflictRow ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got.parentID != tt.wantParent {
				t.Errorf("parentID = %q, want %q", got.parentID, tt.wantParent)
			}
			if got.lastChild != tt.wantLast {
				t.Errorf("lastChild = %d, want %d", got.lastChild, tt.wantLast)
			}
		})
	}
}
