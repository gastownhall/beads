//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

// TestEmbeddedSpawnMolecule_PreservesStepLabels verifies that a cooked formula with
// per-step labels results in spawned issues whose labels are persisted to the
// database. Regression for labels being silently dropped by cloneSubgraph in
// the same shape as the metadata bug fixed by gastownhall/beads#3341.
func TestEmbeddedSpawnMolecule_PreservesStepLabels(t *testing.T) {
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

// TestEmbeddedSpawnMolecule_SubstitutesTemplatedStepFields is the cook -> pour
// regression test for GH#5110 (labels, metadata) and GH#5754 (assignee).
// TestEmbeddedSpawnMolecule_PreservesStepLabels above fixed labels being dropped
// entirely, but its labels carry no {{vars}}, so the missing substitution went
// unnoticed: the labels ARE carried through - just literal, which silently
// stops matching every label query downstream.
//
// The assignee case is the severe one. An unsubstituted assignee makes the
// spawned bead unclosable by the actor that poured it, so the molecule can
// never advance - which is what the AssigneeMatches assertion pins, not just
// the string value.
func TestEmbeddedSpawnMolecule_SubstitutesTemplatedStepFields(t *testing.T) {
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

	const actor = "design-agent"

	f := &formula.Formula{
		Formula: "subst-test",
		Version: 1,
		Type:    formula.TypeWorkflow,
		Steps: []*formula.Step{
			{
				ID:       "qa",
				Title:    "API-contract QA: {{widget_id}}",
				Assignee: "{{agent}}",
				Labels:   []string{"qa-run", "widget:{{widget_id}}", "domain:{{domain}}"},
				Metadata: map[string]interface{}{"ado_id": "{{ado_id}}"},
			},
		},
	}

	subgraph, err := cookFormulaToSubgraph(f, "subst-test")
	if err != nil {
		t.Fatalf("cookFormulaToSubgraph failed: %v", err)
	}

	vars := map[string]string{
		"widget_id": "my_widget",
		"domain":    "payroll",
		"agent":     actor,
		"ado_id":    "731878",
	}
	result, err := spawnMolecule(ctx, s, subgraph, vars, "", actor, false, types.IDPrefixMol)
	if err != nil {
		t.Fatalf("spawnMolecule failed: %v", err)
	}

	newQAID, ok := result.IDMapping["subst-test.qa"]
	if !ok {
		t.Fatalf("result.IDMapping missing entry for subst-test.qa; got %v", result.IDMapping)
	}
	issue, err := s.GetIssue(ctx, newQAID)
	if err != nil {
		t.Fatalf("GetIssue(%s) failed: %v", newQAID, err)
	}

	labels, err := s.GetLabels(ctx, newQAID)
	if err != nil {
		t.Fatalf("GetLabels(%s) failed: %v", newQAID, err)
	}
	gotLabels := make(map[string]bool, len(labels))
	for _, l := range labels {
		gotLabels[l] = true
	}
	for _, want := range []string{"qa-run", "widget:my_widget", "domain:payroll"} {
		if !gotLabels[want] {
			t.Errorf("spawned issue %s missing substituted label %q; got %v", newQAID, want, labels)
		}
	}

	var metadata struct {
		ADOID string `json:"ado_id"`
	}
	if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
		t.Fatalf("spawned issue metadata = %s, not valid JSON: %v", issue.Metadata, err)
	}
	if metadata.ADOID != "731878" {
		t.Errorf("spawned issue metadata.ado_id = %q, want %q", metadata.ADOID, "731878")
	}

	if issue.Assignee != actor {
		t.Errorf("spawned issue assignee = %q, want %q", issue.Assignee, actor)
	}
	if err := validation.AssigneeMatches(actor, false)(newQAID, issue); err != nil {
		t.Errorf("spawned issue is not closable by the pouring actor %q: %v", actor, err)
	}
}

// cookPersistConditionFormula is the GH#7639 workflow for the persist path:
// an unconditional build step and a deploy step conditioned on {{deploy}}
// (default false). %[1]s is the formula/proto id.
const cookPersistConditionFormula = `formula = "%[1]s"
version = 1
type = "workflow"

[vars.deploy]
description = "Whether to include the deploy step"
default = "false"

[[steps]]
id = "build"
title = "Always build"

[[steps]]
id = "deploy"
title = "Deploy to production"
condition = "{{deploy}}"
`

// TestEmbeddedCookPersistAppliesStepConditions runs the real `bd cook
// --persist` in an isolated embedded workspace and reads the stored proto
// back: a step whose condition evaluates false must not become a child of
// the persisted proto, and an explicit --var override must bring it back
// (GH#7639).
func TestEmbeddedCookPersistAppliesStepConditions(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cq")

	const protoID = "cq-cond"
	formulaPath := filepath.Join(dir, protoID+".formula.toml")
	if err := os.WriteFile(formulaPath, []byte(fmt.Sprintf(cookPersistConditionFormula, protoID)), 0o600); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	bdCommand(t, bd, dir, "cook", formulaPath, "--persist", "--var", "deploy=false")

	if got := bdShow(t, bd, dir, protoID); got.Status != types.StatusOpen {
		t.Fatalf("persisted proto status = %s, want open", got.Status)
	}
	build := bdShow(t, bd, dir, protoID+".build")
	if build.Status != types.StatusOpen {
		t.Errorf("persisted build child status = %s, want open", build.Status)
	}
	bdShowFail(t, bd, dir, protoID+".deploy")

	// The override brings the conditioned step back, so the filter — not
	// something else — is what kept it out.
	bdCommand(t, bd, dir, "cook", formulaPath, "--persist", "--force", "--var", "deploy=true")
	deploy := bdShow(t, bd, dir, protoID+".deploy")
	if deploy.Status != types.StatusOpen {
		t.Errorf("persisted deploy child with deploy=true: status = %s, want open", deploy.Status)
	}
}
