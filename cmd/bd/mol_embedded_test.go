//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

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

// TestSpawnMolecule_SubstitutesTemplatedStepFields is the cook -> pour
// regression test for GH#5110 (labels, metadata) and GH#5754 (assignee).
// TestSpawnMolecule_PreservesStepLabels above fixed labels being dropped
// entirely, but its labels carry no {{vars}}, so the missing substitution went
// unnoticed: the labels ARE carried through - just literal, which silently
// stops matching every label query downstream.
//
// The assignee case is the severe one. An unsubstituted assignee makes the
// spawned bead unclosable by the actor that poured it, so the molecule can
// never advance - which is what the AssigneeMatches assertion pins, not just
// the string value.
func TestSpawnMolecule_SubstitutesTemplatedStepFields(t *testing.T) {
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
