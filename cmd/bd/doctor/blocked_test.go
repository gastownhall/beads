package doctor

import (
	"errors"
	"testing"
)

// The render pinned here is the whole user-visible surface of the Blocked
// State check's partial-view behavior (bd-muhnf0): a database with nothing
// dangling reads exactly as it always did — byte for byte, so nothing
// changes for every store whose own wisp-delete cascade still holds — a
// repaired database with dangling edges says what it left alone instead of
// failing, and a held count that could not be read is never read as 0.

func TestBlockedConsistencyCheck(t *testing.T) {
	cases := []struct {
		name    string
		stale   int64
		held    int64
		heldErr error
		want    DoctorCheck
	}{
		{
			name:  "clean is byte-identical to the pre-hold message",
			stale: 0,
			held:  0,
			want: DoctorCheck{
				Name:    BlockedConsistencyCheckName,
				Status:  StatusOK,
				Message: "is_blocked flags consistent with dependency graph",
			},
		},
		{
			name:  "dangling edges after repair name the rows left as found",
			stale: 0,
			held:  3,
			want: DoctorCheck{
				Name:    BlockedConsistencyCheckName,
				Status:  StatusOK,
				Message: "is_blocked flags consistent with dependency graph; 3 open row(s) depend on a wisp this database does not have — is_blocked left as found",
			},
		},
		{
			name:    "an unreadable held count is never read as zero",
			stale:   0,
			heldErr: errors.New("boom"),
			want: DoctorCheck{
				Name:    BlockedConsistencyCheckName,
				Status:  StatusOK,
				Message: "is_blocked flags consistent with dependency graph",
				Detail:  "could not count rows left as found: boom",
			},
		},
		{
			name:  "stale rows keep the warning and the fix",
			stale: 2,
			held:  0,
			want: DoctorCheck{
				Name:    BlockedConsistencyCheckName,
				Status:  StatusWarning,
				Message: "2 issue/wisp row(s) have a stale is_blocked flag — 'bd ready' may hide ready work or show blocked work",
				Detail:  "is_blocked is derived from the dependency graph; a skipped post-pull recompute can leave it stale",
				Fix:     "Run: bd doctor --fix (or 'bd recompute-blocked', which also works in embedded mode)",
			},
		},
		{
			name:  "stale rows with dangling edges say both",
			stale: 2,
			held:  1,
			want: DoctorCheck{
				Name:    BlockedConsistencyCheckName,
				Status:  StatusWarning,
				Message: "2 issue/wisp row(s) have a stale is_blocked flag — 'bd ready' may hide ready work or show blocked work",
				Detail:  "is_blocked is derived from the dependency graph; a skipped post-pull recompute can leave it stale; 1 open row(s) depend on a wisp this database does not have — is_blocked left as found",
				Fix:     "Run: bd doctor --fix (or 'bd recompute-blocked', which also works in embedded mode)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := blockedConsistencyCheck(tc.stale, tc.held, tc.heldErr)
			if got != tc.want {
				t.Fatalf("blockedConsistencyCheck(%d, %d, %v) = %+v, want %+v", tc.stale, tc.held, tc.heldErr, got, tc.want)
			}
		})
	}
}
