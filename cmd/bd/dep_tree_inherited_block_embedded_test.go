//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// TestEmbeddedDepTreeRootBlockedThroughParent pins #6630: a child of a
// blocked epic is blocked (bd blocked lists it, bd ready omits it), so
// `bd dep tree` must not mark it [READY]. The marker now agrees with the
// stored transitive is_blocked flag — the same projection ready/blocked read —
// which covers blocking inherited through the parent chain and blockers a
// depth limit hid, neither of which the visible direct-edge scan can see.
func TestEmbeddedDepTreeRootBlockedThroughParent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dp")

	blocker := bdCreate(t, bd, dir, "Blocker", "--type", "task")
	epic := bdCreate(t, bd, dir, "Blocked epic", "--type", "epic")
	bdDep(t, bd, dir, "add", epic.ID, blocker.ID)
	child := bdCreate(t, bd, dir, "Child", "--type", "task", "--parent", epic.ID)

	t.Run("tree_text_marks_the_child_blocked", func(t *testing.T) {
		out := bdDep(t, bd, dir, "tree", child.ID)
		if strings.Contains(out, "[READY]") || !strings.Contains(out, "[BLOCKED]") {
			t.Fatalf("child of a blocked epic must render [BLOCKED], not [READY]:\n%s", out)
		}
	})

	t.Run("tree_text_depth_limited_still_blocked", func(t *testing.T) {
		// max-depth 1 returns the root row alone, so the epic's direct blocker
		// is invisible to the renderer either way — the flag decides.
		for _, root := range []string{child.ID, epic.ID} {
			out := bdDep(t, bd, dir, "tree", root, "--max-depth", "1")
			if strings.Contains(out, "[READY]") || !strings.Contains(out, "[BLOCKED]") {
				t.Fatalf("tree %s --max-depth 1 must render [BLOCKED], not [READY]:\n%s", root, out)
			}
		}
	})

	t.Run("consistent_with_ready_and_blocked", func(t *testing.T) {
		ready := readyIDs(t, bd, dir)
		if ready[child.ID] || ready[epic.ID] {
			t.Errorf("ready must omit the blocked epic and its child, got %v", ready)
		}
		if !ready[blocker.ID] {
			t.Errorf("ready must list the open blocker %s, got %v", blocker.ID, ready)
		}
		blocked := idSet(bdBlockedJSON(t, bd, dir))
		if !blocked[child.ID] || !blocked[epic.ID] {
			t.Errorf("blocked must list the epic and its child, got %v", blocked)
		}
	})

	t.Run("closing_the_blocker_makes_both_ready", func(t *testing.T) {
		bdClose(t, bd, dir, blocker.ID)
		for _, root := range []string{child.ID, epic.ID} {
			out := bdDep(t, bd, dir, "tree", root)
			if !strings.Contains(out, "[READY]") || strings.Contains(out, "[BLOCKED]") {
				t.Fatalf("tree %s after the blocker closed must render [READY], not [BLOCKED]:\n%s", root, out)
			}
		}
	})
}