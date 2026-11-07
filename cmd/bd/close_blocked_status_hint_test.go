package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPrintNewlyUnblockedFlagsBlockedStatus pins bd close --suggest-next's
// line for a dependent whose last blocker the close took away but whose own
// status is still 'blocked' (bd-e5d8fd): bd close leaves that status as it
// is, so the list names it and the reviewed command that releases it, without
// promising the issue is ready. An open dependent prints as it always did.
func TestPrintNewlyUnblockedFlagsBlockedStatus(t *testing.T) {
	initConfigForTest(t)
	got := captureStdout(t, func() error {
		printNewlyUnblocked([]*types.Issue{
			{ID: "bd-b", Title: "Held", Priority: 1, Status: types.StatusBlocked},
			{ID: "bd-c", Title: "Ready", Priority: 2, Status: types.StatusOpen},
		})
		return nil
	})
	want := "\nNewly unblocked:\n" +
		"  • bd-b — Held (P1)\n" +
		"    → still status 'blocked' (bd close leaves it). If ready to release: bd update bd-b --status open\n" +
		"  • bd-c — Ready (P2)\n"
	if got != want {
		t.Errorf("printNewlyUnblocked prints\n%q\nwant\n%q", got, want)
	}
}
