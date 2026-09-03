//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
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

// pourJSONResult mirrors the anonymous pourResult wrapper `bd mol pour --json`
// emits (cmd/bd/pour.go): the embedded InstantiateResult fields plus attached
// count and phase.
type pourJSONResult struct {
	NewEpicID string            `json:"new_epic_id"`
	IDMapping map[string]string `json:"id_mapping"`
	Created   int               `json:"created"`
}

// readyIDs runs `bd ready --json` and returns the set of ready issue IDs.
func readyIDs(t *testing.T, bd, dir string) map[string]bool {
	t.Helper()
	out, err := bdRunWithFlockRetry(t, bd, dir, "ready", "--json", "--limit", "0")
	if err != nil {
		t.Fatalf("bd ready --json: %v\noutput:\n%s", err, out)
	}
	var ready []types.IssueWithCounts
	if err := json.Unmarshal(out, &ready); err != nil {
		t.Fatalf("parse ready --json: %v\noutput:\n%s", err, out)
	}
	ids := make(map[string]bool, len(ready))
	for _, r := range ready {
		ids[r.ID] = true
	}
	return ids
}

// TestBdMolPour_NeedsAndWaitsForSameTarget_SpawnerOpenBlocksWaiter is the
// regression test for the review gap in GH#3875: the fix for GH#3783 drops
// the DepBlocks edge from `needs` onto the waits_for spawner on the premise
// that the DepWaitsFor edge subsumes its blocking semantics. also_blocks
// metadata (set on exactly this collapsed edge, see cmd/bd/cook.go) closes
// the resulting pre-fanout window: waitsForGateBlockedSQL now additionally
// blocks while the spawner itself is open, not only while it has an open
// parent-child child.
//
// This asserts the waiter stays blocked immediately after pour (spawner
// open, zero children spawned yet), and unblocks once the spawner closes.
func TestBdMolPour_NeedsAndWaitsForSameTarget_SpawnerOpenBlocksWaiter(t *testing.T) {
	bd := buildEmbeddedBD(t)

	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "qnw")

	formulaDir := filepath.Join(beadsDir, "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}

	const formulaTOML = `formula = "qnw-repro"
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
	formulaPath := filepath.Join(formulaDir, "qnw-repro.formula.toml")
	if err := os.WriteFile(formulaPath, []byte(formulaTOML), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	out, err := bdRunWithFlockRetry(t, bd, dir, "mol", "pour", "qnw-repro", "--json")
	if err != nil {
		t.Fatalf("bd mol pour --json: %v\noutput:\n%s", err, out)
	}

	var result pourJSONResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse pour --json: %v\noutput:\n%s", err, out)
	}

	spawnerID, ok := result.IDMapping["qnw-repro.survey-workers"]
	if !ok {
		t.Fatalf("id_mapping missing qnw-repro.survey-workers: %#v", result.IDMapping)
	}
	waiterID, ok := result.IDMapping["qnw-repro.wait"]
	if !ok {
		t.Fatalf("id_mapping missing qnw-repro.wait: %#v", result.IDMapping)
	}

	// The spawner is open with zero children spawned yet — the waiter must
	// be blocked, not ready. Before the also_blocks fix this regressed:
	// dropping the blocks edge left the waits-for gate blind to a still-open,
	// childless spawner.
	if readyIDs(t, bd, dir)[waiterID] {
		t.Fatalf("waiter %s is ready while its spawner %s is still open with no children spawned yet (pre-fanout window)", waiterID, spawnerID)
	}

	if _, err := bdRunWithFlockRetry(t, bd, dir, "close", spawnerID, "--reason", "no fanout needed"); err != nil {
		t.Fatalf("bd close %s: %v", spawnerID, err)
	}

	// The spawner is now closed (and never spawned any children) — the
	// waiter must unblock.
	if !readyIDs(t, bd, dir)[waiterID] {
		t.Fatalf("waiter %s did not become ready after its childless spawner %s closed", waiterID, spawnerID)
	}
}
