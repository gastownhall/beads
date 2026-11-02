//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// depTreeRootLine runs bd dep tree on id (plus args) and returns the line that
// renders the root, the only line that carries a [READY]/[BLOCKED] marker.
func depTreeRootLine(t *testing.T, bd, dir, id string, args ...string) string {
	t.Helper()
	out := bdDep(t, bd, dir, append([]string{"tree", id}, args...)...)
	for _, line := range strings.Split(stripANSIForTest(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), id+":") {
			return line
		}
	}
	t.Fatalf("no root line for %s in dep tree output:\n%s", id, out)
	return ""
}

func wantRootMarker(t *testing.T, line, want string) {
	t.Helper()
	other := "[READY]"
	if want == "[READY]" {
		other = "[BLOCKED]"
	}
	if !strings.Contains(line, want) || strings.Contains(line, other) {
		t.Fatalf("root line = %q, want %s and no %s", line, want, other)
	}
}

// TestEmbeddedDepTreeRootBlockedThroughParent: a child of a blocked epic is
// blocked (bd blocked lists it, bd ready omits it), so dep tree must not mark
// it [READY] (bd-42wxtt, #6630). The epic itself stays [BLOCKED] when the
// depth bound hides its direct blocker, and both turn [READY] once the
// blocker closes.
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

	// The graph is the one ready and blocked already agree on.
	if ready := wakeReadyIDs(t, bd, dir); ready[child.ID] || ready[epic.ID] || !ready[blocker.ID] {
		t.Fatalf("bd ready = %v, want %s and neither %s nor %s", ready, blocker.ID, epic.ID, child.ID)
	}
	listed := map[string]bool{}
	for _, entry := range bdBlockedJSON(t, bd, dir) {
		if id, ok := entry["id"].(string); ok {
			listed[id] = true
		}
	}
	if !listed[child.ID] || !listed[epic.ID] {
		t.Fatalf("bd blocked = %v, want %s and %s", listed, child.ID, epic.ID)
	}

	wantRootMarker(t, depTreeRootLine(t, bd, dir, child.ID), "[BLOCKED]")
	wantRootMarker(t, depTreeRootLine(t, bd, dir, child.ID, "--max-depth", "1"), "[BLOCKED]")
	// E's direct blocker is past the depth bound, so no visible edge says so.
	wantRootMarker(t, depTreeRootLine(t, bd, dir, epic.ID, "--max-depth", "1"), "[BLOCKED]")

	bdClose(t, bd, dir, blocker.ID)
	wantRootMarker(t, depTreeRootLine(t, bd, dir, child.ID), "[READY]")
	wantRootMarker(t, depTreeRootLine(t, bd, dir, epic.ID), "[READY]")
}
