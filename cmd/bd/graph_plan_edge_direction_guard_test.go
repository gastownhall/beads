package main

// The graph-plan supplement's Edge notes taught the edge FIELDS but never
// the DIRECTION: which endpoint of `{ "from_key": ..., "to_key": ...,
// "type": "blocks" }` is doing the waiting. A plan reader who guesses wrong
// wires the graph backwards — from/to are the plan's most consequential
// pair of names, and their meaning is only stated in graph_apply.go's
// Source/Target mapping (each edge applies as `bd dep add <from> <to>`:
// from is the dependent, to its dependency — gascity#7664's second doc bug).
//
// This guard holds the supplement and the code to each other:
//
//  1. the Edge notes must state the direction — at the base the bullet was
//     absent and the guard fails on its first claim,
//  2. the claim must be TRUE: applying a minimal plan whose single blocks
//     edge runs from node "waiter" to node "waited" leaves WAITER waiting —
//     out of `bd ready` — and WAITED ready, exactly as `bd dep add
//     <waiter> <waited>` would.
//
// The behavioral half runs the real bd binary against a fresh isolated
// workspace, the same fixture shape the create-atomicity tests use, so a
// from/to swap in graph_apply cannot survive the guard the supplement
// finally documents.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGraphPlanEdgeDirectionDocumentedAndTrue is the doc guard for the
// supplement's edge-direction bullet.
func TestGraphPlanEdgeDirectionDocumentedAndTrue(t *testing.T) {
	// 1. The supplement states the direction: from is the dependent, to its
	// dependency, the same direction bd dep add spells.
	for _, want := range []string{
		"from_key`/`from_id` names the DEPENDENT",
		"`to_key`/`to_id` its DEPENDENCY",
		"bd dep add <from> <to>",
		"`from` waits for `to`",
	} {
		if !strings.Contains(createGraphPlanSupplement, want) {
			t.Errorf("create_graph_plan.md Edge notes do not state the edge direction (missing %q)", want)
		}
	}

	// 2. The claim is true end-to-end: one plan, two nodes, one blocks edge
	// from "waiter" to "waited".
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	gateDocRunBD(t, bd, dir, "init", "--backend", "dolt", "--prefix", "ge",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	planPath := filepath.Join(dir, "plan.json")
	plan := `{
  "nodes": [
    { "key": "waiter", "title": "Waits on waited" },
    { "key": "waited", "title": "Waited on" }
  ],
  "edges": [
    { "from_key": "waiter", "to_key": "waited", "type": "blocks" }
  ]
}`
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	out := gateDocRunBD(t, bd, dir, "create", "--graph", planPath, "--json")
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in create --graph output:\n%s", out)
	}
	var result struct {
		IDs map[string]string `json:"ids"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &result); err != nil {
		t.Fatalf("parse create --graph --json output: %v\n%s", err, out)
	}
	waiter, ok := result.IDs["waiter"]
	if !ok || waiter == "" {
		t.Fatalf("create --graph result has no minted id for key waiter:\n%s", out)
	}
	waited, ok := result.IDs["waited"]
	if !ok || waited == "" {
		t.Fatalf("create --graph result has no minted id for key waited:\n%s", out)
	}

	// WAITER is the dependent: it waits for WAITED, so bd ready shows
	// WAITED and not WAITER. A from/to swap anywhere in the apply path
	// fails here.
	ready := gateDocRunBD(t, bd, dir, "ready", "--json")
	readyIDs := make(map[string]bool)
	for _, row := range gateDocJSONArray(t, ready) {
		if id, _ := row["id"].(string); id != "" {
			readyIDs[id] = true
		}
	}
	if readyIDs[waiter] {
		t.Errorf("`from` node %s is ready although its blocks edge makes it wait for %s — the documented direction is wrong", waiter, waited)
	}
	if !readyIDs[waited] {
		t.Errorf("`to` node %s is not ready although nothing waits for it — the documented direction is wrong", waited)
	}

	// And the edge reads the way `bd dep add <waiter> <waited>` would:
	// WAITER's dependency list names WAITED.
	deps := gateDocRunBD(t, bd, dir, "dep", "list", waiter, "--json")
	if !strings.Contains(deps, waited) {
		t.Errorf("bd dep list %s does not name %s as its dependency:\n%s", waiter, waited, deps)
	}
}
