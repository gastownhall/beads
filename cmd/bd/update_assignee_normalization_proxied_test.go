//go:build cgo

package main

import (
	"fmt"
	"testing"
)

// TestProxiedServerAssigneeHiddenEmptyClears pins bd-wkcfuz on the
// proxied-server route, whose update gather (gatherUpdateInput), assign
// positional (runAssignProxiedServer) and batch dispatch are separate input
// sites from the direct route: a bare "-" or whitespace-only assignee must
// clear exactly like the explicit empty string on each of them, while the
// live-claim fence, the --if-assignee guard and non-clear values keep their
// existing behavior.
func TestProxiedServerAssigneeHiddenEmptyClears(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	// AC-1: the proxied update gather normalizes hidden-empty assignees.
	t.Run("update_hidden_empty_clears", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "anu")
		for i, value := range []string{"-", " ", "\t"} {
			issue := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Proxied update clear %d", i))
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder")

			updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "other", "--assignee", value)
			if got := updated.Assignee; got != "" {
				t.Errorf("value %q: proxied update stored assignee %q, want cleared", value, got)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != "" {
				t.Errorf("value %q: persisted assignee = %q, want \"\"", value, got)
			}
			// Cleared means back in the pool: another actor can claim it.
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "taker", "--claim")
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != "taker" {
				t.Errorf("value %q: claim by another actor set assignee %q, want taker", value, got)
			}
		}
	})
}
