//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerEpic(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "epc")

	// An epic with all children closed becomes eligible for closure.
	epic := bdProxiedCreate(t, bd, p.dir, "Shipping epic", "--type", "epic")
	c1 := bdProxiedCreate(t, bd, p.dir, "Child one", "--type", "task", "--parent", epic.ID)
	c2 := bdProxiedCreate(t, bd, p.dir, "Child two", "--type", "task", "--parent", epic.ID)

	// A second epic that stays incomplete (one open child).
	openEpic := bdProxiedCreate(t, bd, p.dir, "Incomplete epic", "--type", "epic")
	bdProxiedCreate(t, bd, p.dir, "Open child", "--type", "task", "--parent", openEpic.ID)

	// A partially-done epic: two children, one closed. It shows progress but is
	// never eligible for closure (one child stays open the whole test).
	partialEpic := bdProxiedCreate(t, bd, p.dir, "Partial epic", "--type", "epic")
	pc1 := bdProxiedCreate(t, bd, p.dir, "Partial child one", "--type", "task", "--parent", partialEpic.ID)
	bdProxiedCreate(t, bd, p.dir, "Partial child two", "--type", "task", "--parent", partialEpic.ID)
	if out, err := bdProxiedRun(t, bd, p.dir, "close", pc1.ID); err != nil {
		t.Fatalf("close partial child: %v\n%s", err, out)
	}

	epicStatus := func(t *testing.T, args ...string) []*types.EpicStatus {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"epic", "status", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("epic status %v: %v\n%s", args, err, out)
		}
		var got []*types.EpicStatus
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return got
	}

	t.Run("status_shows_progress", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "epic", "status")
		if err != nil {
			t.Fatalf("epic status: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, partialEpic.ID) {
			t.Errorf("expected partial epic %s in status output: %s", partialEpic.ID, s)
		}
		if !strings.Contains(s, "children closed") || !strings.Contains(s, "/") {
			t.Errorf("expected progress fraction in output: %s", s)
		}
	})

	t.Run("status_json_is_array", func(t *testing.T) {
		got := epicStatus(t)
		if len(got) < 1 {
			t.Errorf("expected at least 1 epic in status, got %d", len(got))
		}
	})

	t.Run("status_eligible_only_excludes_incomplete", func(t *testing.T) {
		// Neither the partially-done epic nor the fully-open epic is eligible.
		out, err := bdProxiedRun(t, bd, p.dir, "epic", "status", "--eligible-only")
		if err != nil {
			t.Fatalf("epic status --eligible-only: %v\n%s", err, out)
		}
		s := string(out)
		if strings.Contains(s, partialEpic.ID) {
			t.Errorf("partial epic should not appear with --eligible-only: %s", s)
		}
		if strings.Contains(s, openEpic.ID) {
			t.Errorf("incomplete epic should not appear with --eligible-only: %s", s)
		}
	})

	t.Run("status_no_open_epics", func(t *testing.T) {
		p2 := newSharedProxiedProject(t, bd, "epq")
		out, err := bdProxiedRun(t, bd, p2.dir, "epic", "status")
		if err != nil {
			t.Fatalf("epic status: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "No open epics") {
			t.Errorf("expected 'No open epics': %s", out)
		}
	})

	t.Run("status_not_eligible_while_open", func(t *testing.T) {
		for _, es := range epicStatus(t) {
			if es.Epic.ID == epic.ID && es.EligibleForClose {
				t.Errorf("epic should not be eligible before children closed")
			}
		}
	})

	// Close both children of the first epic.
	bdProxiedRun(t, bd, p.dir, "close", c1.ID)
	bdProxiedRun(t, bd, p.dir, "close", c2.ID)

	t.Run("status_eligible_after_children_closed", func(t *testing.T) {
		var found bool
		for _, es := range epicStatus(t) {
			if es.Epic.ID == epic.ID {
				found = true
				if !es.EligibleForClose {
					t.Errorf("epic %s should be eligible (2/2 closed), got %+v", epic.ID, es)
				}
				if es.TotalChildren != 2 || es.ClosedChildren != 2 {
					t.Errorf("expected 2/2 children, got %d/%d", es.ClosedChildren, es.TotalChildren)
				}
			}
		}
		if !found {
			t.Errorf("eligible epic %s missing from status", epic.ID)
		}
	})

	t.Run("dry_run_lists_without_closing", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "epic", "close-eligible", "--dry-run", "--json")
		if err != nil {
			t.Fatalf("dry-run: %v\n%s", err, out)
		}
		var eligible []*types.EpicStatus
		if err := json.Unmarshal(out, &eligible); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		var listed bool
		for _, es := range eligible {
			if es.Epic.ID == epic.ID {
				listed = true
			}
			if es.Epic.ID == openEpic.ID || es.Epic.ID == partialEpic.ID {
				t.Errorf("incomplete epic %s must not be listed as eligible", es.Epic.ID)
			}
		}
		if !listed {
			t.Errorf("dry-run should list eligible epic %s, got %+v", epic.ID, eligible)
		}
		// Confirm it was NOT closed.
		show := bdProxiedShow(t, bd, p.dir, epic.ID)
		if show.Status == types.StatusClosed {
			t.Errorf("dry-run must not close the epic")
		}
	})

	t.Run("write_closes_eligible", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "epic", "close-eligible", "--json")
		if err != nil {
			t.Fatalf("close-eligible: %v\n%s", err, out)
		}
		var res struct {
			Closed []string `json:"closed"`
			Count  int      `json:"count"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		var closedIt bool
		for _, id := range res.Closed {
			if id == epic.ID {
				closedIt = true
			}
		}
		if !closedIt {
			t.Errorf("expected %s in closed list, got %+v", epic.ID, res.Closed)
		}
		show := bdProxiedShow(t, bd, p.dir, epic.ID)
		if show.Status != types.StatusClosed {
			t.Errorf("epic %s should be closed after close-eligible, got %s", epic.ID, show.Status)
		}
	})
}
