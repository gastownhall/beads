//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedMolBondRoutesFormulaMutationToTarget is the end-to-end
// regression for #4350/#4714. A formula found in the town is bonded to an issue
// that exists only in a prefix-routed rig; every spawned issue and dependency
// must land in the rig database without advancing the town database.
func TestEmbeddedMolBondRoutesFormulaMutationToTarget(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	sourceDir, targetDir, targetBeadsDir := setupRoutedEmbeddedRepo(t, bd, "src", "tgt")
	target := bdCreate(t, bd, targetDir, "Routed bond target", "--type", "epic")

	formulasDir := filepath.Join(sourceDir, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatalf("create formulas dir: %v", err)
	}
	formulaBody := `formula = "mol-route"
description = "Routed bond regression"
version = 1
type = "workflow"

[[steps]]
id = "work"
title = "Routed work"
`
	if err := os.WriteFile(filepath.Join(formulasDir, "mol-route.formula.toml"), []byte(formulaBody), 0644); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	sourceBeadsDir := filepath.Join(sourceDir, ".beads")
	sourceBefore := embeddedCurrentCommit(t, sourceBeadsDir, "src")
	targetBefore := embeddedCurrentCommit(t, targetBeadsDir, "tgt")
	dryRunOut := bdCommand(t, bd, sourceDir, "mol", "bond", "mol-route", target.ID, "--type", "parallel", "--dry-run")
	if !strings.Contains(dryRunOut, target.ID) {
		t.Fatalf("dry-run did not resolve routed target %s:\n%s", target.ID, dryRunOut)
	}
	assertEmbeddedHeadUnchanged(t, sourceBeadsDir, "src", sourceBefore, "routed mol bond dry-run source")
	assertEmbeddedHeadUnchanged(t, targetBeadsDir, "tgt", targetBefore, "routed mol bond dry-run target")

	out := bdCommand(t, bd, sourceDir, "--json", "mol", "bond", "mol-route", target.ID, "--type", "parallel")

	var result BondResult
	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("bond output has no JSON object: %s", out)
	}
	if err := json.NewDecoder(strings.NewReader(out[jsonStart:])).Decode(&result); err != nil {
		t.Fatalf("decode bond result: %v\n%s", err, out)
	}
	if result.ResultID != target.ID || result.Spawned == 0 || len(result.IDMapping) == 0 {
		t.Fatalf("unexpected bond result: %+v", result)
	}

	assertEmbeddedHeadUnchanged(t, sourceBeadsDir, "src", sourceBefore, "routed mol bond source")
	assertEmbeddedHeadAdvanced(t, targetBeadsDir, "tgt", targetBefore, "routed mol bond target")

	targetStore := openStore(t, targetBeadsDir, "tgt")
	for oldID, newID := range result.IDMapping {
		if _, err := targetStore.GetIssue(t.Context(), newID); err != nil {
			t.Errorf("spawned %s -> %s missing from target store: %v", oldID, newID, err)
		}
	}
	sourceStore := openStore(t, sourceBeadsDir, "src")
	for _, newID := range result.IDMapping {
		if _, err := sourceStore.GetIssue(t.Context(), newID); err == nil {
			t.Errorf("spawned issue %s leaked into source store", newID)
		}
	}
}

// TestSpawnMolecule_PreservesStepLabels verifies that a cooked formula with
// per-step labels results in spawned issues whose labels are persisted to the
// database. Regression for labels being silently dropped by cloneSubgraph in
// the same shape as the metadata bug fixed by gastownhall/beads#3341.
func TestSpawnMolecule_PreservesStepLabels(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	ctx := t.Context()
	s, err := embeddeddolt.Open(ctx, t.TempDir(), "beads", "main")
	if err != nil {
		t.Fatalf("embeddeddolt.Open failed: %v", err)
	}
	defer s.Close()
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	f := &formula.Formula{
		Formula: "label-test",
		Version: 1,
		Type:    formula.TypeWorkflow,
		Steps: []*formula.Step{
			{
				ID:     "work",
				Title:  "Do the work",
				Labels: []string{"worker", "phase:build"},
			},
		},
	}

	subgraph, err := cookFormulaToSubgraph(f, "label-test")
	if err != nil {
		t.Fatalf("cookFormulaToSubgraph failed: %v", err)
	}

	result, err := spawnMolecule(ctx, s, subgraph, nil, "", "test", false, types.IDPrefixMol)
	if err != nil {
		t.Fatalf("spawnMolecule failed: %v", err)
	}

	newWorkID, ok := result.IDMapping["label-test.work"]
	if !ok {
		t.Fatalf("result.IDMapping missing entry for label-test.work; got %v", result.IDMapping)
	}
	labels, err := s.GetLabels(ctx, newWorkID)
	if err != nil {
		t.Fatalf("GetLabels(%s) failed: %v", newWorkID, err)
	}
	got := make(map[string]bool, len(labels))
	for _, l := range labels {
		got[l] = true
	}
	for _, want := range []string{"worker", "phase:build"} {
		if !got[want] {
			t.Errorf("spawned issue %s missing label %q; got %v", newWorkID, want, labels)
		}
	}
}
