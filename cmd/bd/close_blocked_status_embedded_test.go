//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedCloseSuggestNextBlockedStatus pins bd close --suggest-next on
// the direct route for a dependent whose last blocker closes while its own
// status is 'blocked' (bd-e5d8fd): the list names the release command under
// it, an open dependent prints as it always did, and the close leaves the
// explicit status as it is. TestProxiedServerClose2B's
// close_suggest_next_blocked_status subtests are its proxied twins.
func TestEmbeddedCloseSuggestNextBlockedStatus(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// fixture creates blocker A and two issues that depend only on it: B,
	// whose status is set to 'blocked', and C, left open.
	fixture := func(t *testing.T, prefix string) (dir string, a, b, c *types.Issue) {
		t.Helper()
		dir, _, _ = bdInit(t, bd, "--prefix", prefix)
		a = bdCreate(t, bd, dir, "Blocker A")
		b = bdCreate(t, bd, dir, "Held B", "--deps", "depends-on:"+a.ID)
		c = bdCreate(t, bd, dir, "Waiting C", "--deps", "depends-on:"+a.ID)
		bdUpdate(t, bd, dir, b.ID, "--status", "blocked")
		return dir, a, b, c
	}

	t.Run("text", func(t *testing.T) {
		dir, a, b, c := fixture(t, "bst")
		out := bdClose(t, bd, dir, a.ID, "--suggest-next")
		hint := "    → still status 'blocked' (bd close leaves it). If ready to release: bd update " + b.ID + " --status open\n"
		if !strings.Contains(out, "  • "+b.ID+" — Held B (P2)\n"+hint) {
			t.Errorf("bd close --suggest-next does not name %s's release under it:\n%s", b.ID, out)
		}
		if strings.Count(out, "still status 'blocked'") != 1 {
			t.Errorf("bd close --suggest-next prints the blocked-status hint for more than %s:\n%s", b.ID, out)
		}
		if !strings.Contains(out, "  • "+c.ID+" — Waiting C (P2)\n") {
			t.Errorf("bd close --suggest-next changed open %s's line:\n%s", c.ID, out)
		}
		if got := bdShow(t, bd, dir, b.ID); got.Status != types.StatusBlocked {
			t.Errorf("bd close changed %s's explicit status to %q, want it left blocked", b.ID, got.Status)
		}
	})
}
