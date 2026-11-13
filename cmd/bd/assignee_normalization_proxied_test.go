//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerAssigneeHiddenEmptyClears is the proxied-server half of
// bd-wkcfuz: the proxied update gather, the proxied assign handler, and the
// proxied batch route each read the assignee themselves, so each must fold a
// bare "-" and whitespace-only text into the `--assignee ""` clear, keep every
// other value verbatim, and leave the live-claim fence and --if-assignee
// literal.
func TestProxiedServerAssigneeHiddenEmptyClears(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	assigned := func(t *testing.T, p proxiedProject, title string) *types.Issue {
		t.Helper()
		return bdProxiedCreate(t, bd, p.dir, title, "--assignee", "holder")
	}

	t.Run("AC1_update_clears_like_explicit_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hpa")
		for _, value := range []string{"", "-", " ", "\t"} {
			issue := assigned(t, p, "Update clear via proxy")
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "dispatcher", "--assignee", value)
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "" {
				t.Errorf("update --assignee %q: assignee=%q, want cleared", value, got.Assignee)
				continue
			}
			found := false
			for _, r := range bdProxiedReadyJSON(t, bd, p, "--unassigned", "--limit", "0") {
				found = found || r.ID == issue.ID
			}
			if !found {
				t.Errorf("update --assignee %q: %s missing from ready --unassigned", value, issue.ID)
			}
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--claim", "--actor", "newcomer")
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "newcomer" || got.Status != types.StatusInProgress {
				t.Errorf("update --assignee %q then --claim by another actor: assignee=%q status=%s, want newcomer/in_progress", value, got.Assignee, got.Status)
			}
		}
	})

	t.Run("AC2_assign_positional_clears", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hpb")
		for _, value := range []string{"", "-", " "} {
			issue := assigned(t, p, "Assign clear via proxy")
			out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, value, "--actor", "dispatcher")
			if err != nil {
				t.Fatalf("assign %q failed: %v\n%s", value, err, out)
			}
			if !strings.Contains(string(out), "Unassigned") {
				t.Errorf("assign %q: want the Unassigned confirmation, got:\n%s", value, out)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "" {
				t.Errorf("assign %q: assignee=%q, want cleared", value, got.Assignee)
			}
		}
	})
}
