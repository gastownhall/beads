//go:build cgo

package main

import (
	"encoding/json"
	"testing"
)

func TestProxiedServerMolStale(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("complete_but_unclosed_appears_then_clears_on_close", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mst")
		root := bdProxiedCreate(t, bd, p.dir, "Stale root", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Stale child", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "close", child.ID); err != nil {
			t.Fatalf("bd close child: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "stale", "--json")
		if err != nil {
			t.Fatalf("bd mol stale --json: %v\n%s", err, out)
		}
		var got struct {
			StaleMolecules []struct {
				ID string `json:"id"`
			} `json:"stale_molecules"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		found := false
		for _, m := range got.StaleMolecules {
			if m.ID == root.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected %s in stale molecules, got %+v", root.ID, got.StaleMolecules)
		}

		if out, err := bdProxiedRun(t, bd, p.dir, "close", root.ID); err != nil {
			t.Fatalf("bd close root: %v\n%s", err, out)
		}
		out2, err := bdProxiedRun(t, bd, p.dir, "mol", "stale", "--json")
		if err != nil {
			t.Fatalf("bd mol stale --json (after close): %v\n%s", err, out2)
		}
		var got2 struct {
			StaleMolecules []struct {
				ID string `json:"id"`
			} `json:"stale_molecules"`
		}
		if err := json.Unmarshal(out2, &got2); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out2)
		}
		for _, m := range got2.StaleMolecules {
			if m.ID == root.ID {
				t.Errorf("root %s should no longer be stale after closing", root.ID)
			}
		}
	})

	t.Run("unassigned_filter", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mstu")
		assignedRoot := bdProxiedCreate(t, bd, p.dir, "Assigned stale root", "--type", "epic", "--assignee", "someone")
		assignedChild := bdProxiedCreate(t, bd, p.dir, "Assigned stale child", "--type", "task", "--parent", assignedRoot.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "close", assignedChild.ID); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}
		unassignedRoot := bdProxiedCreate(t, bd, p.dir, "Unassigned stale root", "--type", "epic")
		unassignedChild := bdProxiedCreate(t, bd, p.dir, "Unassigned stale child", "--type", "task", "--parent", unassignedRoot.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "close", unassignedChild.ID); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "stale", "--unassigned", "--json")
		if err != nil {
			t.Fatalf("bd mol stale --unassigned --json: %v\n%s", err, out)
		}
		var got struct {
			StaleMolecules []struct {
				ID string `json:"id"`
			} `json:"stale_molecules"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		ids := map[string]bool{}
		for _, m := range got.StaleMolecules {
			ids[m.ID] = true
		}
		if ids[assignedRoot.ID] {
			t.Errorf("assigned root %s should be excluded by --unassigned", assignedRoot.ID)
		}
		if !ids[unassignedRoot.ID] {
			t.Errorf("unassigned root %s should be included by --unassigned", unassignedRoot.ID)
		}
	})

	t.Run("all_flag_does_not_surface_childless_epics", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "msta")
		lonely := bdProxiedCreate(t, bd, p.dir, "Childless open epic", "--type", "epic")
		// GH#5720: an epic whose only dotted-ID issue has no parent-child
		// edge has zero children too, even once that issue is closed.
		dotted := bdProxiedCreate(t, bd, p.dir, "Epic with dotted issue only", "--type", "epic")
		dottedIssue := bdProxiedCreate(t, bd, p.dir, "Unlinked dotted issue", "--type", "task", "--id", dotted.ID+".1")
		if out, err := bdProxiedRun(t, bd, p.dir, "close", dottedIssue.ID); err != nil {
			t.Fatalf("bd close dotted issue: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "stale", "--all", "--json")
		if err != nil {
			t.Fatalf("bd mol stale --all --json: %v\n%s", err, out)
		}
		var got struct {
			StaleMolecules []struct {
				ID string `json:"id"`
			} `json:"stale_molecules"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		for _, m := range got.StaleMolecules {
			if m.ID == lonely.ID || m.ID == dotted.ID {
				t.Errorf("zero-child epic %s unexpectedly present: mol stale lists only epics eligible for close, and an epic with no parent-child children never is (GetEpicsEligibleForClosure returns it as a {0 0 false} row on both backends)", m.ID)
			}
		}
	})
}
