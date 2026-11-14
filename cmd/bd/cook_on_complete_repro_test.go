package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/types"
)

// TestCookFormulaOnCompleteNotSilentlyDropped pins bd-c8mhhj (GH#3782):
// a step's on_complete spec validates and cooks, then is discarded by
// processStepToIssue, so nothing at close time can ever act on it. Either
// cook must refuse on_complete (feature removed) or the poured step issue
// must carry the spec for a close-time executor to read (feature wired).
func TestCookFormulaOnCompleteNotSilentlyDropped(t *testing.T) {
	f := &formula.Formula{
		Formula: "survey",
		Version: 1,
		Type:    formula.TypeWorkflow,
		Steps: []*formula.Step{
			{
				ID:    "survey-workers",
				Title: "Survey workers",
				OnComplete: &formula.OnCompleteSpec{
					ForEach: "output.workers",
					Bond:    "mol-worker",
					Vars:    map[string]string{"worker_name": "{item.name}"},
				},
			},
		},
	}
	if err := f.Validate(); err != nil {
		t.Logf("Validate rejected on_complete (acceptable): %v", err)
		return
	}

	subgraph, err := cookFormulaToSubgraph(f, "survey")
	if err != nil {
		t.Logf("cook rejected on_complete (acceptable): %v", err)
		return
	}
	var step *types.Issue
	for _, issue := range subgraph.Issues {
		if issue.ID == "survey.survey-workers" {
			step = issue
		}
	}
	if step == nil {
		t.Fatal("survey.survey-workers issue not found in subgraph")
	}
	meta := string(step.Metadata)
	if !strings.Contains(meta, "for_each") || !strings.Contains(meta, "mol-worker") {
		t.Fatalf("on_complete accepted by Validate and cook but dropped from the poured step issue "+
			"(metadata=%q): closing the step can never trigger it", meta)
	}
}
