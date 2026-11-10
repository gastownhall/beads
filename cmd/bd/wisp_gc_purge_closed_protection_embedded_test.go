//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestEmbeddedWispGCPurgeClosedHoldsBackLiveChildren pins the GH#5152
// closed-mode protection: a closed root that a live child still points at
// through a parent-child edge is held back — with its attribution edge —
// while `wisp gc --closed --force` still deletes the unrelated eligible
// closed wisps. At the base this run deleted the closed root and the child's
// edge with it, leaving the open child relationally orphaned.
//
// The same shape is what convergence looks like: once the child closes too, a
// subsequent run reclaims both.
func TestEmbeddedWispGCPurgeClosedHoldsBackLiveChildren(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pcp")

	root := bdCreate(t, bd, dir, "closed root wisp", "--ephemeral")
	child := bdCreate(t, bd, dir, "live child wisp", "--ephemeral")
	bdDepAdd(t, bd, dir, child.ID, root.ID, "-t", "parent-child")
	unrelated := bdCreate(t, bd, dir, "unrelated wisp", "--ephemeral")

	// Closing a root over an open child needs --force (upward refusal);
	// the child stays live.
	bdClose(t, bd, dir, root.ID, "--force")

	// All candidates skipped: the closed root is the only closed wisp and it
	// is held back, so the empty result still names the skip.
	out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--json")
	var skipped struct {
		DeletedCount         int    `json:"deleted_count"`
		LiveDependentSkipped int    `json:"live_dependent_skipped"`
		Message              string `json:"message"`
	}
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &skipped); err != nil {
		t.Fatalf("parse all-skipped gc --closed --json: %v\nraw:\n%s", err, out)
	}
	if skipped.DeletedCount != 0 || skipped.LiveDependentSkipped != 1 {
		t.Fatalf("want deleted_count=0 and live_dependent_skipped=1, got %+v\n%s", skipped, out)
	}
	if !bdShowSucceeds(t, bd, dir, root.ID) {
		t.Fatalf("closed root %s must survive while its child is live; output:\n%s", root.ID, out)
	}

	bdClose(t, bd, dir, unrelated.ID)

	// Unconfirmed run previews: the unrelated wisp is the one candidate, the
	// protected root is counted as skipped.
	out = bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--json")
	var preview struct {
		Candidates           int  `json:"candidates"`
		DryRun               bool `json:"dry_run"`
		LiveDependentSkipped int  `json:"live_dependent_skipped"`
	}
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &preview); err != nil {
		t.Fatalf("parse preview gc --closed --json: %v\nraw:\n%s", err, out)
	}
	if !preview.DryRun || preview.Candidates != 1 || preview.LiveDependentSkipped != 1 {
		t.Fatalf("want preview candidates=1, live_dependent_skipped=1, got %+v\n%s", preview, out)
	}

	// Force deletes exactly the unrelated wisp. Root, child and the child's
	// attribution edge all survive.
	out = bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--json")
	var del struct {
		Deleted              []string `json:"deleted"`
		DeletedCount         int      `json:"deleted_count"`
		LiveDependentSkipped int      `json:"live_dependent_skipped"`
	}
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &del); err != nil {
		t.Fatalf("parse forced gc --closed --json: %v\nraw:\n%s", err, out)
	}
	if del.DeletedCount != 1 || len(del.Deleted) != 1 || del.Deleted[0] != unrelated.ID {
		t.Fatalf("want --force to delete only %s, got %+v\n%s", unrelated.ID, del, out)
	}
	if del.LiveDependentSkipped != 1 {
		t.Fatalf("want live_dependent_skipped=1 in the delete result, got %+v\n%s", del, out)
	}
	if !bdShowSucceeds(t, bd, dir, root.ID) {
		t.Errorf("closed root %s was deleted despite its live child", root.ID)
	}
	if !bdShowSucceeds(t, bd, dir, child.ID) {
		t.Errorf("live child %s was deleted by a closed-mode gc", child.ID)
	}
	edge := bdCommand(t, bd, dir, "show", child.ID, "--json")
	if !strings.Contains(edge, root.ID) {
		t.Errorf("child %s lost its parent-child attribution edge to %s; show output:\n%s", child.ID, root.ID, edge)
	}

	// Convergence: close the child and the next run reclaims both, edges and
	// all.
	bdClose(t, bd, dir, child.ID)
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--json")
	bdShowFail(t, bd, dir, root.ID)
	bdShowFail(t, bd, dir, child.ID)
}

// TestEmbeddedWispGCPurgeClosedReclaimsBlocksLinkedAndFullyClosed pins the
// other side of the GH#5152 protection: it must not over-protect. A closed
// step whose live dependents arrive only through blocks stays reclaimable
// (#5735), and a molecule whose steps all closed is purged whole, parent-child
// edges included, because no live row needs them.
func TestEmbeddedWispGCPurgeClosedReclaimsBlocksLinkedAndFullyClosed(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Blocks-linked: the #5735 shape. The closed step is reclaimed even
	// though open wisps depend on it through blocks.
	t.Run("blocks_only_live_dependents_do_not_hold_back", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "pcb")
		step := bdCreate(t, bd, dir, "finished blocks-linked step", "--ephemeral").ID
		liveDep := bdCreate(t, bd, dir, "live blocked wisp", "--ephemeral").ID
		bdDepAdd(t, bd, dir, liveDep, step) // default type: blocks
		bdClose(t, bd, dir, step)

		out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--json")
		var del struct {
			Deleted []string `json:"deleted"`
		}
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &del); err != nil {
			t.Fatalf("parse forced gc --closed --json: %v\nraw:\n%s", err, out)
		}
		if len(del.Deleted) != 1 || del.Deleted[0] != step {
			t.Fatalf("want blocks-linked closed step %s reclaimed, got %v\n%s", step, del.Deleted, out)
		}
		if !bdShowSucceeds(t, bd, dir, liveDep) {
			t.Errorf("live blocked wisp %s must survive the purge", liveDep)
		}
	})

	// Fully closed: nothing is live, so the whole chain goes, edges and all.
	t.Run("fully_closed_molecule_is_purged_whole", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "pcf")
		root := bdCreate(t, bd, dir, "finished root", "--ephemeral").ID
		child := bdCreate(t, bd, dir, "finished child", "--ephemeral").ID
		bdDepAdd(t, bd, dir, child, root, "-t", "parent-child")
		bdClose(t, bd, dir, child)
		bdClose(t, bd, dir, root)

		out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--json")
		var del struct {
			DeletedCount int      `json:"deleted_count"`
			Deleted      []string `json:"deleted"`
		}
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &del); err != nil {
			t.Fatalf("parse forced gc --closed --json: %v\nraw:\n%s", err, out)
		}
		if del.DeletedCount != 2 {
			t.Fatalf("want both fully-closed wisps reclaimed, got %+v\n%s", del, out)
		}
		bdShowFail(t, bd, dir, root)
		bdShowFail(t, bd, dir, child)
	})
}
