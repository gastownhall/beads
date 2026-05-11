//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBdMolPour_NeedsAndWaitsForSameTarget is a regression test for GH#3783:
// a step that has both `needs = [X]` and `waits_for = "all-children"` (with
// no explicit spawner, so the spawner is inferred from needs[0] == X)
// causes bd mol pour to fail with a dep-type collision —
//
//	dependency <step> -> <X> already exists with type "blocks"
//	  (requested "waits-for")
//
// collectDependencies in cmd/bd/cook.go emits two edges on the same
// (source, target) pair: a DepBlocks edge from `needs`, then a DepWaitsFor
// edge from `waits_for` (which infers its spawner from needs[0]). Storage
// rightly rejects the duplicate.
//
// This blocks the documented pattern where a step gates on the children of
// the step it depends on (i.e. "wait for the fanout from the surveyor").
//
// After the fix, the DepWaitsFor edge subsumes the DepBlocks edge for that
// specific target — the waits-for dep already carries the blocking
// semantics plus the gate metadata, so the blocks dep is redundant.
func TestBdMolPour_NeedsAndWaitsForSameTarget(t *testing.T) {
	bd := buildEmbeddedBD(t)

	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "qnt")

	formulaDir := filepath.Join(beadsDir, "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}

	const formulaTOML = `formula = "qnt-repro"
version = 1
type = "workflow"

[[steps]]
id = "survey-workers"
title = "Survey"
type = "task"

[[steps]]
id = "wait"
title = "Wait for fanout"
type = "task"
needs = ["survey-workers"]
waits_for = "all-children"
`
	formulaPath := filepath.Join(formulaDir, "qnt-repro.formula.toml")
	if err := os.WriteFile(formulaPath, []byte(formulaTOML), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	// Pour should succeed. Before the fix, this exits 1 with:
	//   Error: pouring proto: failed to create dependency:
	//   dependency qnt-mol-XXX -> qnt-mol-YYY already exists with type "blocks"
	//     (requested "waits-for")
	out, err := bdRunWithFlockRetry(t, bd, dir, "mol", "pour", "qnt-repro")
	if err != nil {
		t.Fatalf("bd mol pour should succeed for a step with both needs and waits_for; got error: %v\noutput:\n%s", err, out)
	}

	outStr := string(out)
	if strings.Contains(outStr, "already exists with type") {
		t.Errorf("pour output contains the dep-collision error this test was meant to prevent:\n%s", outStr)
	}
}
