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

	// GH#5720: every open epic is listed, including a childless epic and an
	// epic whose only "child" is a dotted-ID issue with no parent-child edge.
	pAll := newSharedProxiedProject(t, bd, "epa")
	linked := bdProxiedCreate(t, bd, pAll.dir, "Epic with linked child", "--type", "epic")
	bdProxiedCreate(t, bd, pAll.dir, "Linked child", "--type", "task", "--parent", linked.ID)
	childless := bdProxiedCreate(t, bd, pAll.dir, "Childless epic", "--type", "epic")
	dotted := bdProxiedCreate(t, bd, pAll.dir, "Epic with dotted issue only", "--type", "epic")
	dottedIssue := bdProxiedCreate(t, bd, pAll.dir, "Unlinked dotted issue", "--type", "task", "--id", dotted.ID+".1")

	epicStatusAll := func(t *testing.T, args ...string) map[string]*types.EpicStatus {
		t.Helper()
		out, err := bdProxiedRun(t, bd, pAll.dir, append([]string{"epic", "status", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("epic status %v: %v\n%s", args, err, out)
		}
		var rows []*types.EpicStatus
		if err := json.Unmarshal(out, &rows); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return epicStatusByID(t, rows)
	}
	epicStatusAllHuman := func(t *testing.T, ids ...string) map[string]string {
		t.Helper()
		out, err := bdProxiedRun(t, bd, pAll.dir, "epic", "status")
		if err != nil {
			t.Fatalf("epic status: %v\n%s", err, out)
		}
		return epicStatusBlocks(t, string(out), ids...)
	}

	t.Run("status_lists_every_open_epic", func(t *testing.T) {
		byID := epicStatusAll(t)
		if len(byID) != 3 {
			t.Errorf("epic status listed %v, want exactly the 3 open epics", epicRowIDs(byID))
		}
		assertEpicRow(t, byID, linked.ID, 1, 0, false)
		assertEpicRow(t, byID, childless.ID, 0, 0, false)
		assertEpicRow(t, byID, dotted.ID, 0, 0, false)

		blocks := epicStatusAllHuman(t, linked.ID, childless.ID, dotted.ID)
		if !strings.Contains(blocks[linked.ID], "Progress: 0/1 children closed (0%)") {
			t.Errorf("linked epic block = %q, want 0/1 progress", blocks[linked.ID])
		}
		assertZeroChildEpicBlock(t, blocks, childless.ID)
		assertZeroChildEpicBlock(t, blocks, dotted.ID)

		// Closing the unlinked dotted issue changes nothing: it is not a
		// child, so it is neither counted nor makes its epic eligible.
		if out, err := bdProxiedRun(t, bd, pAll.dir, "close", dottedIssue.ID); err != nil {
			t.Fatalf("close dotted issue: %v\n%s", err, out)
		}
		byID = epicStatusAll(t)
		assertEpicRow(t, byID, dotted.ID, 0, 0, false)
		assertEpicRow(t, byID, childless.ID, 0, 0, false)
		assertZeroChildEpicBlock(t, epicStatusAllHuman(t, dotted.ID), dotted.ID)
	})

	// GH#5720: zero-child epics are listed by epic status but never
	// eligible, so close-eligible neither selects nor closes a childless
	// epic or one whose only dotted-ID issue (no parent-child edge) was
	// closed for a completing reason. A complete epic proves it acts.
	t.Run("close_eligible_skips_zero_child_epics", func(t *testing.T) {
		pz := newSharedProxiedProject(t, bd, "epz")
		zChildless := bdProxiedCreate(t, bd, pz.dir, "Childless epic", "--type", "epic")
		zDotted := bdProxiedCreate(t, bd, pz.dir, "Epic with dotted issue only", "--type", "epic")
		zDottedIssue := bdProxiedCreate(t, bd, pz.dir, "Unlinked dotted issue", "--type", "task", "--id", zDotted.ID+".1")
		zDone := bdProxiedCreate(t, bd, pz.dir, "Complete epic", "--type", "epic")
		zDoneChild := bdProxiedCreate(t, bd, pz.dir, "Done child", "--type", "task", "--parent", zDone.ID)
		for _, id := range []string{zDottedIssue.ID, zDoneChild.ID} {
			if out, err := bdProxiedRun(t, bd, pz.dir, "close", id); err != nil {
				t.Fatalf("close %s: %v\n%s", id, err, out)
			}
		}

		out, err := bdProxiedRun(t, bd, pz.dir, "epic", "close-eligible", "--dry-run")
		if err != nil {
			t.Fatalf("dry-run: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), zDone.ID) {
			t.Errorf("dry-run should list complete epic %s: %s", zDone.ID, out)
		}
		for _, id := range []string{zChildless.ID, zDotted.ID} {
			if strings.Contains(string(out), id+":") {
				t.Errorf("dry-run must not select zero-child epic %s: %s", id, out)
			}
		}

		out, err = bdProxiedRun(t, bd, pz.dir, "epic", "close-eligible", "--dry-run", "--json")
		if err != nil {
			t.Fatalf("dry-run --json: %v\n%s", err, out)
		}
		var eligible []*types.EpicStatus
		if err := json.Unmarshal(out, &eligible); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(eligible) != 1 || eligible[0].Epic == nil || eligible[0].Epic.ID != zDone.ID {
			t.Errorf("dry-run --json selected %+v, want only %s", eligible, zDone.ID)
		}

		out, err = bdProxiedRun(t, bd, pz.dir, "epic", "close-eligible", "--json")
		if err != nil {
			t.Fatalf("close-eligible: %v\n%s", err, out)
		}
		var res struct {
			Closed []string `json:"closed"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(res.Closed) != 1 || res.Closed[0] != zDone.ID {
			t.Errorf("close-eligible closed %v, want only %s", res.Closed, zDone.ID)
		}

		for _, id := range []string{zChildless.ID, zDotted.ID} {
			if show := bdProxiedShow(t, bd, pz.dir, id); show.Status != types.StatusOpen {
				t.Errorf("zero-child epic %s status = %s, want open", id, show.Status)
			}
		}
		if show := bdProxiedShow(t, bd, pz.dir, zDone.ID); show.Status != types.StatusClosed {
			t.Errorf("complete epic %s status = %s, want closed", zDone.ID, show.Status)
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
