package dolt

import (
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// row builds a CallRow the way the driver hands one back: every column as a
// NullString, NULL columns present but invalid.
func row(pairs ...any) schema.CallRow {
	r := make(schema.CallRow, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		name := pairs[i].(string)
		switch v := pairs[i+1].(type) {
		case nil:
			r[name] = sql.NullString{}
		case string:
			r[name] = sql.NullString{String: v, Valid: true}
		default:
			panic("row: value must be string or nil")
		}
	}
	return r
}

// TestParseMergeReportDistinguishesMergedFromNoOp pins the mapping from a Dolt
// merge/pull row to the answer callers actually want: did anything arrive?
//
// Every row below was MEASURED against dolt 2.1.8, not invented. The two that
// matter most are the pair that a fast_forward check gets wrong: a genuine
// three-way merge and a level no-op BOTH report fast_forward=0, so anything
// keying off that column calls a real merge "nothing happened".
func TestParseMergeReportDistinguishesMergedFromNoOp(t *testing.T) {
	cases := []struct {
		name       string
		row        schema.CallRow
		wantReport bool
		wantMerged bool
		wantConfl  bool
	}{
		{
			name:       "DOLT_PULL fast-forward merged",
			row:        row("fast_forward", "1", "conflicts", "0", "message", "merge successful"),
			wantReport: true, wantMerged: true,
		},
		{
			// The case fast_forward gets wrong: merged, but not a fast-forward.
			name:       "DOLT_PULL three-way merged",
			row:        row("fast_forward", "0", "conflicts", "0", "message", "merge successful"),
			wantReport: true, wantMerged: true,
		},
		{
			name:       "DOLT_PULL nothing to merge",
			row:        row("fast_forward", "0", "conflicts", "0", "message", "Everything up-to-date"),
			wantReport: true, wantMerged: false,
		},
		{
			// The second spelling of "nothing merged" — local is ahead. Proves
			// the parse matches the single positive rather than enumerating
			// negatives it cannot know the full set of.
			name:       "DOLT_PULL local already ahead",
			row:        row("fast_forward", "0", "conflicts", "0", "message", "cannot fast forward from a to b. a is ahead of b already"),
			wantReport: true, wantMerged: false,
		},
		{
			name:       "DOLT_MERGE merged, extra hash column ignored",
			row:        row("hash", "j8mark005d49cbhf0pnfb84dh8v7i79k", "fast_forward", "0", "conflicts", "0", "message", "merge successful"),
			wantReport: true, wantMerged: true,
		},
		{
			name:       "DOLT_MERGE nothing to merge",
			row:        row("hash", "", "fast_forward", "0", "conflicts", "0", "message", "cannot fast forward from a to b. a is ahead of b already"),
			wantReport: true, wantMerged: false,
		},
		{
			// dolt returns a NULL message whenever its internal message is
			// empty. NULL is not "merge successful".
			name:       "NULL message is not a merge",
			row:        row("fast_forward", "0", "conflicts", "0", "message", nil),
			wantReport: true, wantMerged: false,
		},
		{
			name:       "conflicts flag set",
			row:        row("fast_forward", "0", "conflicts", "1", "message", "merge successful"),
			wantReport: true, wantMerged: true, wantConfl: true,
		},
		{
			// A transport that returned no row at all — every CLI route. This
			// is NOT "nothing merged"; it is "no report", and Merged must not
			// be read when Reported is false.
			name: "no row means no report",
			row:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMergeReport(tc.row)
			if got.Reported != tc.wantReport {
				t.Errorf("Reported = %v, want %v", got.Reported, tc.wantReport)
			}
			if got.Merged != tc.wantMerged {
				t.Errorf("Merged = %v, want %v (message %q)", got.Merged, tc.wantMerged, got.Message)
			}
			if got.Conflicted != tc.wantConfl {
				t.Errorf("Conflicted = %v, want %v", got.Conflicted, tc.wantConfl)
			}
		})
	}
}

// TestParseMergeReportControlRejectsNearMisses is the control for the test
// above: if parseMergeReport reported Merged for anything that merely looks
// like success, the cases above would pass for the wrong reason.
func TestParseMergeReportControlRejectsNearMisses(t *testing.T) {
	nearMisses := []string{
		"Merge successful",  // dolt's literal is lower-case
		"merge successful.", // no trailing period upstream
		"merge succeeded",
		"fast forward",
		"",
	}
	for _, msg := range nearMisses {
		got := parseMergeReport(row("fast_forward", "1", "conflicts", "0", "message", msg))
		if got.Merged {
			t.Errorf("parseMergeReport(message=%q).Merged = true; a near-miss message must not count as a merge", msg)
		}
	}
}
