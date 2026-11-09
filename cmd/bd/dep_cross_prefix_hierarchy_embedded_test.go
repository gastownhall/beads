//go:build cgo

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedCrossPrefixHierarchyViolationRefused pins GH#8299 (bd-6jk1y2)
// AC-5: a blocks edge between a cross-prefix pair that violates the blocking
// hierarchy rules is refused with the normal hierarchy error — singly and in a
// batch — and no invalid edge is committed. The prefix-era classifier exempted
// cross-prefix pairs from the check entirely, so a child could gate on (or be
// gated by) its own cross-prefix ancestor with no complaint and the edge
// stored. The chain spans the prefixes through explicit dep edges (not dotted
// ids), so the refusal under test is the ancestry walk's, and no shared
// endpoints exist between the chain and the violating edge, so nothing else
// (a same-type re-add conflict, the dotted-id guard) can preempt it.
func TestEmbeddedCrossPrefixHierarchyViolationRefused(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "b")

	// A two-level chain with one cross-prefix leg: anc is the grandparent,
	// par its child, child par's child. Only leg 1 crosses prefixes.
	anc := bdCreate(t, bd, dir, "Hierarchy grandparent", "--id", "a-anc7", "--force", "--type", "task")
	par := bdCreate(t, bd, dir, "Hierarchy parent", "--type", "task")
	child := bdCreate(t, bd, dir, "Hierarchy child", "--type", "task")
	third := bdCreate(t, bd, dir, "Unrelated third", "--type", "task")
	bdDep(t, bd, dir, "add", par.ID, anc.ID, "--type", "parent-child")
	bdDep(t, bd, dir, "add", child.ID, par.ID, "--type", "parent-child")

	// Singular add, blocker-is-ancestor shape: the child gating on its own
	// cross-prefix grandparent is the normal modeling refusal.
	out := bdDepFail(t, bd, dir, "add", child.ID, anc.ID, "--type", "blocks")
	if !strings.Contains(out, child.ID+" cannot be blocked by its ancestor "+anc.ID) {
		t.Fatalf("expected ancestor hierarchy refusal, got: %s", out)
	}

	// Singular add, blocker-is-descendant shape: the same pair, mirrored.
	out = bdDepFail(t, bd, dir, "add", anc.ID, child.ID, "--type", "blocks")
	if !strings.Contains(out, anc.ID+" cannot be blocked by its descendant "+child.ID) {
		t.Fatalf("expected descendant hierarchy refusal, got: %s", out)
	}

	// Neither refusal committed an edge: the chain still holds exactly its
	// own two legs, and the third issue none.
	for id, want := range map[string]float64{child.ID: 1, par.ID: 1, third.ID: 0} {
		if got := bdShowDetails(t, bd, dir, id)["dependency_count"]; got != want {
			t.Fatalf("%s dependency_count = %v, want %v (no invalid edge committed)", id, got, want)
		}
	}

	// Batch add is all-or-nothing: a valid edge ordered FIRST still rolls
	// back when a later edge hits the same refusal.
	batchPath := filepath.Join(t.TempDir(), "cross-prefix-hierarchy.jsonl")
	batchBody := fmt.Sprintf("{\"from\":%q,\"to\":%q,\"type\":\"blocks\"}\n", par.ID, third.ID) +
		fmt.Sprintf("{\"from\":%q,\"to\":%q,\"type\":\"blocks\"}\n", child.ID, anc.ID)
	if err := os.WriteFile(batchPath, []byte(batchBody), 0o600); err != nil {
		t.Fatalf("write batch file: %v", err)
	}
	out = bdDepFail(t, bd, dir, "add", "--file", batchPath)
	if !strings.Contains(out, child.ID+" cannot be blocked by its ancestor "+anc.ID) {
		t.Fatalf("expected bulk ancestor hierarchy refusal, got: %s", out)
	}
	// Rolled back: the valid edge from the same batch committed nothing.
	if got := bdShowDetails(t, bd, dir, par.ID)["dependency_count"]; got != float64(1) {
		t.Fatalf("parent dependency_count after refused batch = %v, want 1 — the batch is all-or-nothing", got)
	}
	if got := bdShowDetails(t, bd, dir, third.ID)["dependency_count"]; got != float64(0) {
		t.Fatalf("third dependency_count after refused batch = %v, want 0", got)
	}
}
