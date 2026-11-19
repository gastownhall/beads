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

	// A childless epic and an epic whose only child is an unlinked dotted-ID
	// child (GH#5720's two omissions): both must appear in epic status as
	// 0/0 (0%), and neither may ever be close-eligible.
	childlessEpic := bdProxiedCreate(t, bd, p.dir, "Childless epic", "--type", "epic")
	dottedEpic := bdProxiedCreate(t, bd, p.dir, "Dotted child only epic", "--type", "epic")
	dottedChild := bdProxiedCreate(t, bd, p.dir, "Unlinked dotted child", "--type", "task", "--id", dottedEpic.ID+".1")

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

	t.Run("status_lists_childless_and_dotted_only_epics", func(t *testing.T) {
		// GH#5720 (bd-ufmqco): both omitted epics must appear once, as
		// {0, 0, false}.
		byID := map[string]*types.EpicStatus{}
		for _, es := range epicStatus(t) {
			byID[es.Epic.ID] = es
		}
		for _, id := range []string{childlessEpic.ID, dottedEpic.ID} {
			es := byID[id]
			if es == nil {
				t.Errorf("open epic %s missing from status; got %+v", id, byID)
				continue
			}
			if es.TotalChildren != 0 || es.ClosedChildren != 0 || es.EligibleForClose {
				t.Errorf("%s = {Total:%d Closed:%d Eligible:%v}, want {0 0 false}", id, es.TotalChildren, es.ClosedChildren, es.EligibleForClose)
			}
		}

		out, err := bdProxiedRun(t, bd, p.dir, "epic", "status")
		if err != nil {
			t.Fatalf("epic status: %v\n%s", err, out)
		}
		s := string(out)
		for _, id := range []string{childlessEpic.ID, dottedEpic.ID} {
			if !strings.Contains(s, id) {
				t.Errorf("open epic %s missing from human status output:\n%s", id, s)
			}
		}
		if n := strings.Count(s, "0/0 children closed (0%)"); n != 2 {
			t.Errorf("expected two 0/0 (0%%) rows, got %d:\n%s", n, s)
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

	t.Run("close_eligible_never_selects_zero_child_epics", func(t *testing.T) {
		// GH#5720 (bd-ufmqco) AC-3: close the dotted child with a completing
		// reason first, so the dotted-only epic looks "done" to anyone
		// counting dotted children. Without a parent-child edge it has zero
		// linked children and must never be selected — dry-run or write.
		if out, err := bdProxiedRun(t, bd, p.dir, "close", dottedChild.ID); err != nil {
			t.Fatalf("close dotted child: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "epic", "close-eligible", "--dry-run", "--json")
		if err != nil {
			t.Fatalf("close-eligible --dry-run: %v\n%s", err, out)
		}
		var eligible []*types.EpicStatus
		if err := json.Unmarshal(out, &eligible); err != nil {
			// With nothing eligible the command prints the empty result
			// object ({closed:[],count:0,reason:…}), which itself proves
			// the zero-child epics were not selected; anything else is a
			// parsing failure.
			var obj map[string]interface{}
			if err2 := json.Unmarshal(out, &obj); err2 != nil {
				t.Fatalf("unmarshal dry-run output: %v / %v\n%s", err, err2, out)
			}
			if _, ok := obj["closed"]; !ok {
				t.Fatalf("unexpected dry-run JSON shape:\n%s", out)
			}
			eligible = nil
		}
		for _, es := range eligible {
			if es.Epic.ID == childlessEpic.ID || es.Epic.ID == dottedEpic.ID {
				t.Errorf("zero-linked-child epic %s must not be listed as close-eligible", es.Epic.ID)
			}
		}

		out, err = bdProxiedRun(t, bd, p.dir, "epic", "close-eligible", "--json")
		if err != nil {
			t.Fatalf("close-eligible: %v\n%s", err, out)
		}
		var res struct {
			Closed []string `json:"closed"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		for _, id := range res.Closed {
			if id == childlessEpic.ID || id == dottedEpic.ID {
				t.Errorf("zero-linked-child epic %s was closed by close-eligible", id)
			}
		}
		for _, id := range []string{childlessEpic.ID, dottedEpic.ID} {
			show := bdProxiedShow(t, bd, p.dir, id)
			if show.Status != types.StatusOpen {
				t.Errorf("epic %s must remain open, got %s (close_reason %q)", id, show.Status, show.CloseReason)
			}
		}
	})

	t.Run("write_closes_with_reason", func(t *testing.T) {
		p2 := newSharedProxiedProject(t, bd, "epr")
		e2 := bdProxiedCreate(t, bd, p2.dir, "Reason epic", "--type", "epic")
		c2a := bdProxiedCreate(t, bd, p2.dir, "Reason child", "--type", "task", "--parent", e2.ID)
		if out, err := bdProxiedRun(t, bd, p2.dir, "close", c2a.ID); err != nil {
			t.Fatalf("close child: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p2.dir, "epic", "close-eligible", "--reason", "Sprint goal met", "--json")
		if err != nil {
			t.Fatalf("close-eligible --reason: %v\n%s", err, out)
		}
		var res struct {
			Closed []string `json:"closed"`
			Count  int      `json:"count"`
			Reason string   `json:"reason"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if res.Reason != "Sprint goal met" {
			t.Errorf("reason = %q, want %q", res.Reason, "Sprint goal met")
		}
		show := bdProxiedShow(t, bd, p2.dir, e2.ID)
		if show.Status != types.StatusClosed {
			t.Errorf("epic should be closed, got %s", show.Status)
		}
		if show.CloseReason != "Sprint goal met" {
			t.Errorf("persisted close_reason = %q, want %q", show.CloseReason, "Sprint goal met")
		}
	})
}
