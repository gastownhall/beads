//go:build cgo

package main

import (
	"encoding/json"
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

	t.Run("json", func(t *testing.T) {
		dir, a, b, c := fixture(t, "bsj")
		stdout, stderr, err := runBDSplit(t, bd, dir, "close", a.ID, "--suggest-next", "--json")
		if err != nil {
			t.Fatalf("bd close --suggest-next --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		requireSuggestNextJSONKeepsBlockedStatus(t, stdout, a.ID, b.ID, c.ID)
		if got := bdShow(t, bd, dir, b.ID); got.Status != types.StatusBlocked {
			t.Errorf("bd close --json changed %s's explicit status to %q, want it left blocked", b.ID, got.Status)
		}
	})
}

// requireSuggestNextJSONKeepsBlockedStatus asserts the stdout of
// `bd close <closedID> --suggest-next --json` for the fixture of
// TestEmbeddedCloseSuggestNextBlockedStatus: the object it always printed,
// {"closed": [...], "schema_version": N, "unblocked": [...]} and nothing
// beside it, naming heldID
// with the status 'blocked' it still has and openID as open, and none of the
// text hint (bd-e5d8fd). A JSON consumer reads the status from the entry.
func requireSuggestNextJSONKeepsBlockedStatus(t *testing.T, stdout, closedID, heldID, openID string) {
	t.Helper()
	if strings.Contains(stdout, "still status 'blocked'") || strings.Contains(stdout, "Newly unblocked") {
		t.Errorf("bd close --suggest-next --json prints the text list:\n%s", stdout)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("bd close --suggest-next --json is not one JSON object: %v\n%s", err, stdout)
	}
	if len(got) != 3 || got["closed"] == nil || got["schema_version"] == nil || got["unblocked"] == nil {
		t.Errorf("bd close --suggest-next --json keys changed, want closed, schema_version and unblocked only:\n%s", stdout)
	}
	var closed, unblocked []*types.Issue
	if err := json.Unmarshal(got["closed"], &closed); err != nil || len(closed) != 1 || closed[0].ID != closedID || closed[0].Status != types.StatusClosed {
		t.Errorf("closed = %s (%v), want [%s] closed", got["closed"], err, closedID)
	}
	if err := json.Unmarshal(got["unblocked"], &unblocked); err != nil {
		t.Fatalf("parse unblocked: %v\n%s", err, got["unblocked"])
	}
	statusByID := map[string]types.Status{}
	for _, issue := range unblocked {
		statusByID[issue.ID] = issue.Status
	}
	if len(unblocked) != 2 || statusByID[heldID] != types.StatusBlocked || statusByID[openID] != types.StatusOpen {
		t.Errorf("unblocked = %v, want %s blocked and %s open", statusByID, heldID, openID)
	}
}
