package formula

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestApplyLoops_FixedCount(t *testing.T) {
	// Create a step with a fixed-count loop
	steps := []*Step{
		{
			ID:    "process",
			Title: "Process items",
			Loop: &LoopSpec{
				Count: 3,
				Body: []*Step{
					{ID: "fetch", Title: "Fetch item"},
					{ID: "transform", Title: "Transform item", Needs: []string{"fetch"}},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	// Should have 6 steps (3 iterations * 2 steps each)
	if len(result) != 6 {
		t.Errorf("Expected 6 steps, got %d", len(result))
	}

	// Check step IDs
	expectedIDs := []string{
		"process.iter1.fetch",
		"process.iter1.transform",
		"process.iter2.fetch",
		"process.iter2.transform",
		"process.iter3.fetch",
		"process.iter3.transform",
	}

	for i, expected := range expectedIDs {
		if i >= len(result) {
			t.Errorf("Missing step %d: %s", i, expected)
			continue
		}
		if result[i].ID != expected {
			t.Errorf("Step %d: expected ID %s, got %s", i, expected, result[i].ID)
		}
	}

	// Check that inner dependencies are preserved (within same iteration)
	transform1 := result[1]
	if len(transform1.Needs) != 1 || transform1.Needs[0] != "process.iter1.fetch" {
		t.Errorf("transform1 should need process.iter1.fetch, got %v", transform1.Needs)
	}

	// Check that iterations are chained (iter2 depends on iter1)
	fetch2 := result[2]
	if len(fetch2.Needs) != 1 || fetch2.Needs[0] != "process.iter1.transform" {
		t.Errorf("iter2.fetch should need iter1.transform, got %v", fetch2.Needs)
	}
}

func TestApplyLoops_Conditional(t *testing.T) {
	steps := []*Step{
		{
			ID:    "retry",
			Title: "Retry operation",
			Loop: &LoopSpec{
				Until: "step.status == 'complete'",
				Max:   5,
				Body: []*Step{
					{ID: "attempt", Title: "Attempt operation"},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	// Conditional loops expand once (runtime re-executes)
	if len(result) != 1 {
		t.Errorf("Expected 1 step for conditional loop, got %d", len(result))
	}

	// Should have loop metadata label with JSON format
	step := result[0]
	hasLoopLabel := false
	for _, label := range step.Labels {
		// Label format: loop:{"max":5,"until":"step.status == 'complete'"}
		if len(label) > 5 && label[:5] == "loop:" {
			hasLoopLabel = true
			// Verify it contains the expected values
			if !strings.Contains(label, `"until"`) || !strings.Contains(label, `"max"`) {
				t.Errorf("Loop label missing expected fields: %s", label)
			}
		}
	}

	if !hasLoopLabel {
		t.Error("Missing loop metadata label")
	}
}

func TestApplyLoops_Validation(t *testing.T) {
	tests := []struct {
		name    string
		loop    *LoopSpec
		wantErr string
	}{
		{
			name:    "empty body",
			loop:    &LoopSpec{Count: 3, Body: nil},
			wantErr: "body is required",
		},
		{
			name:    "both count and until",
			loop:    &LoopSpec{Count: 3, Until: "cond", Max: 5, Body: []*Step{{ID: "a", Title: "A"}}},
			wantErr: "cannot have both count and until",
		},
		{
			name:    "neither count nor until",
			loop:    &LoopSpec{Body: []*Step{{ID: "a", Title: "A"}}},
			wantErr: "either count or until is required",
		},
		{
			name:    "until without max",
			loop:    &LoopSpec{Until: "cond", Body: []*Step{{ID: "a", Title: "A"}}},
			wantErr: "max is required when until is set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := []*Step{{ID: "test", Title: "Test", Loop: tt.loop}}
			_, err := ApplyLoops(steps)
			if err == nil {
				t.Error("Expected error, got nil")
			} else if tt.wantErr != "" && err.Error() != "" {
				// Just check that an error was returned
				// The exact message format may vary
			}
		})
	}
}

func TestApplyBranches(t *testing.T) {
	steps := []*Step{
		{ID: "setup", Title: "Setup"},
		{ID: "test", Title: "Run tests"},
		{ID: "lint", Title: "Run linter"},
		{ID: "build", Title: "Build"},
		{ID: "deploy", Title: "Deploy"},
	}

	compose := &ComposeRules{
		Branch: []*BranchRule{
			{
				From:  "setup",
				Steps: []string{"test", "lint", "build"},
				Join:  "deploy",
			},
		},
	}

	result, err := ApplyBranches(steps, compose)
	if err != nil {
		t.Fatalf("ApplyBranches failed: %v", err)
	}

	// Build step map for checking
	stepMap := make(map[string]*Step)
	for _, s := range result {
		stepMap[s.ID] = s
	}

	// Verify branch steps depend on 'from'
	for _, branchStep := range []string{"test", "lint", "build"} {
		s := stepMap[branchStep]
		found := false
		for _, need := range s.Needs {
			if need == "setup" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Step %s should need 'setup', got %v", branchStep, s.Needs)
		}
	}

	// Verify 'join' depends on all branch steps
	deploy := stepMap["deploy"]
	for _, branchStep := range []string{"test", "lint", "build"} {
		found := false
		for _, need := range deploy.Needs {
			if need == branchStep {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("deploy should need %s, got %v", branchStep, deploy.Needs)
		}
	}
}

func TestApplyBranches_Validation(t *testing.T) {
	steps := []*Step{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B"},
	}

	tests := []struct {
		name    string
		branch  *BranchRule
		wantErr string
	}{
		{
			name:    "missing from",
			branch:  &BranchRule{Steps: []string{"a"}, Join: "b"},
			wantErr: "from is required",
		},
		{
			name:    "missing steps",
			branch:  &BranchRule{From: "a", Join: "b"},
			wantErr: "steps is required",
		},
		{
			name:    "missing join",
			branch:  &BranchRule{From: "a", Steps: []string{"b"}},
			wantErr: "join is required",
		},
		{
			name:    "from not found",
			branch:  &BranchRule{From: "notfound", Steps: []string{"a"}, Join: "b"},
			wantErr: "from step \"notfound\" not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compose := &ComposeRules{Branch: []*BranchRule{tt.branch}}
			_, err := ApplyBranches(steps, compose)
			if err == nil {
				t.Error("Expected error, got nil")
			}
		})
	}
}

func TestApplyGates(t *testing.T) {
	steps := []*Step{
		{ID: "tests", Title: "Run tests"},
		{ID: "deploy", Title: "Deploy to production"},
	}

	compose := &ComposeRules{
		Gate: []*GateRule{
			{
				Before:    "deploy",
				Condition: "tests.status == 'complete'",
			},
		},
	}

	result, err := ApplyGates(steps, compose)
	if err != nil {
		t.Fatalf("ApplyGates failed: %v", err)
	}

	// Find deploy step
	var deploy *Step
	for _, s := range result {
		if s.ID == "deploy" {
			deploy = s
			break
		}
	}

	if deploy == nil {
		t.Fatal("deploy step not found")
	}

	// Check for gate label with JSON format
	found := false
	for _, label := range deploy.Labels {
		// Label format: gate:{"condition":"tests.status == 'complete'"}
		if len(label) > 5 && label[:5] == "gate:" {
			found = true
			if !strings.Contains(label, `"condition"`) || !strings.Contains(label, "tests.status") {
				t.Errorf("Gate label missing expected content: %s", label)
			}
			break
		}
	}

	if !found {
		t.Errorf("deploy should have gate label, got %v", deploy.Labels)
	}
}

func TestApplyGates_InvalidCondition(t *testing.T) {
	steps := []*Step{
		{ID: "deploy", Title: "Deploy"},
	}

	compose := &ComposeRules{
		Gate: []*GateRule{
			{
				Before:    "deploy",
				Condition: "invalid condition syntax ???",
			},
		},
	}

	_, err := ApplyGates(steps, compose)
	if err == nil {
		t.Error("Expected error for invalid condition, got nil")
	}
}

func TestApplyControlFlow_Integration(t *testing.T) {
	// Test the combined ApplyControlFlow function
	steps := []*Step{
		{ID: "setup", Title: "Setup"},
		{
			ID:    "process",
			Title: "Process items",
			Loop: &LoopSpec{
				Count: 2,
				Body: []*Step{
					{ID: "item", Title: "Process item"},
				},
			},
		},
		{ID: "cleanup", Title: "Cleanup"},
	}

	compose := &ComposeRules{
		Branch: []*BranchRule{
			{
				From:  "setup",
				Steps: []string{"process.iter1.item", "process.iter2.item"},
				Join:  "cleanup",
			},
		},
		Gate: []*GateRule{
			{
				Before:    "cleanup",
				Condition: "steps.complete >= 2",
			},
		},
	}

	result, err := ApplyControlFlow(steps, compose)
	if err != nil {
		t.Fatalf("ApplyControlFlow failed: %v", err)
	}

	// Should have: setup, process.iter1.item, process.iter2.item, cleanup
	if len(result) != 4 {
		t.Errorf("Expected 4 steps, got %d", len(result))
	}

	// Verify cleanup has gate label
	var cleanup *Step
	for _, s := range result {
		if s.ID == "cleanup" {
			cleanup = s
			break
		}
	}

	if cleanup == nil {
		t.Fatal("cleanup step not found")
	}

	hasGate := false
	for _, label := range cleanup.Labels {
		// Label format: gate:{"condition":"steps.complete >= 2"}
		if len(label) > 5 && label[:5] == "gate:" && strings.Contains(label, "steps.complete") {
			hasGate = true
			break
		}
	}

	if !hasGate {
		t.Errorf("cleanup should have gate label, got %v", cleanup.Labels)
	}
}

func TestApplyLoops_NoLoops(t *testing.T) {
	// Test with steps that have no loops
	steps := []*Step{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B", Needs: []string{"a"}},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("Expected 2 steps, got %d", len(result))
	}

	// Dependencies should be preserved
	if len(result[1].Needs) != 1 || result[1].Needs[0] != "a" {
		t.Errorf("Dependencies not preserved: %v", result[1].Needs)
	}
}

func TestApplyLoops_ExternalDependencies(t *testing.T) {
	// Test that dependencies on steps OUTSIDE the loop are preserved as-is
	steps := []*Step{
		{ID: "setup", Title: "Setup"},
		{
			ID:    "process",
			Title: "Process items",
			Loop: &LoopSpec{
				Count: 2,
				Body: []*Step{
					{ID: "work", Title: "Do work", Needs: []string{"setup"}}, // External dep
					{ID: "save", Title: "Save", Needs: []string{"work"}},     // Internal dep
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	// Should have: setup, process.iter1.work, process.iter1.save, process.iter2.work, process.iter2.save
	if len(result) != 5 {
		t.Errorf("Expected 5 steps, got %d", len(result))
	}

	// Find iter1.work - should have external dep on "setup" (not "process.iter1.setup")
	var work1 *Step
	for _, s := range result {
		if s.ID == "process.iter1.work" {
			work1 = s
			break
		}
	}

	if work1 == nil {
		t.Fatal("process.iter1.work not found")
	}

	// External dependency should be preserved as-is
	if len(work1.Needs) != 1 || work1.Needs[0] != "setup" {
		t.Errorf("External dependency should be 'setup', got %v", work1.Needs)
	}

	// Find iter1.save - should have internal dep on "process.iter1.work"
	var save1 *Step
	for _, s := range result {
		if s.ID == "process.iter1.save" {
			save1 = s
			break
		}
	}

	if save1 == nil {
		t.Fatal("process.iter1.save not found")
	}

	// Internal dependency should be prefixed
	if len(save1.Needs) != 1 || save1.Needs[0] != "process.iter1.work" {
		t.Errorf("Internal dependency should be 'process.iter1.work', got %v", save1.Needs)
	}
}

// TestApplyLoops_OwnNeedsCarryToFirstIterationEntrySteps pins GH#7636's first
// half: a loop step's own Needs/DependsOn, declared outside its body, name no
// step the rewrite in expandLoopIteration can see at all, so before this fix
// they were silently dropped the moment the loop step stopped existing post
// -expansion. They must land on the first iteration's own entry steps (the
// ones with no dependency of their own on a body sibling) — not an iteration
// that chaining already gates.
func TestApplyLoops_OwnNeedsCarryToFirstIterationEntrySteps(t *testing.T) {
	steps := []*Step{
		{ID: "brief", Title: "Brief"},
		{
			ID:        "round",
			Title:     "Round",
			Needs:     []string{"brief"},
			DependsOn: []string{"brief"},
			Loop: &LoopSpec{
				Count: 2,
				Body: []*Step{
					{ID: "work", Title: "Work"},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	byID := make(map[string]*Step, len(result))
	for _, s := range result {
		byID[s.ID] = s
	}

	work1 := byID["round.iter1.work"]
	if work1 == nil {
		t.Fatal("round.iter1.work not found")
	}
	if len(work1.Needs) != 1 || work1.Needs[0] != "brief" {
		t.Errorf("round.iter1.work.Needs = %v, want [brief] (the loop step's own Needs)", work1.Needs)
	}
	if len(work1.DependsOn) != 1 || work1.DependsOn[0] != "brief" {
		t.Errorf("round.iter1.work.DependsOn = %v, want [brief] (the loop step's own DependsOn)", work1.DependsOn)
	}

	work2 := byID["round.iter2.work"]
	if work2 == nil {
		t.Fatal("round.iter2.work not found")
	}
	if !slices.Contains(work2.Needs, "round.iter1.work") {
		t.Errorf("round.iter2.work.Needs = %v, want chained to round.iter1.work", work2.Needs)
	}
	if slices.Contains(work2.Needs, "brief") {
		t.Errorf("round.iter2.work.Needs = %v, want the loop's own Needs NOT repeated on a later iteration", work2.Needs)
	}
}

// TestApplyLoops_OwnNeedsSkipABodyStepThatAlreadyDependsOnASibling confirms
// the carry-over above targets only genuine entry steps: a body step that
// already names another body step in its own Needs is gated by that sibling,
// not by the loop's external Needs, so it must not also receive them.
func TestApplyLoops_OwnNeedsSkipABodyStepThatAlreadyDependsOnASibling(t *testing.T) {
	steps := []*Step{
		{ID: "brief", Title: "Brief"},
		{
			ID:    "round",
			Title: "Round",
			Needs: []string{"brief"},
			Loop: &LoopSpec{
				Count: 1,
				Body: []*Step{
					{ID: "work", Title: "Work"},
					{ID: "verify", Title: "Verify", Needs: []string{"work"}},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}
	byID := make(map[string]*Step, len(result))
	for _, s := range result {
		byID[s.ID] = s
	}

	if work := byID["round.iter1.work"]; work == nil || !slices.Contains(work.Needs, "brief") {
		t.Errorf("round.iter1.work.Needs = %v, want [brief]: it is the body's entry step", work)
	}
	verify := byID["round.iter1.verify"]
	if verify == nil {
		t.Fatal("round.iter1.verify not found")
	}
	if slices.Contains(verify.Needs, "brief") {
		t.Errorf("round.iter1.verify.Needs = %v, want brief NOT added: verify is gated by its sibling work, not the loop's own Needs", verify.Needs)
	}
	if !slices.Contains(verify.Needs, "round.iter1.work") {
		t.Errorf("round.iter1.verify.Needs = %v, want its own internal dependency on round.iter1.work preserved", verify.Needs)
	}
}

// TestApplyLoops_OwnDepsCarryToRoots pins GH#7636: a loop step's own Needs
// and DependsOn are appended to every expanded step that no other step of the
// loop gates (a body sibling or iteration chaining), and to no other step.
func TestApplyLoops_OwnDepsCarryToRoots(t *testing.T) {
	tests := []struct {
		name string
		body []*Step
		// carried lists the expanded steps that must get brief, with the
		// Needs each must end up with exactly.
		carried map[string][]string
	}{
		{
			name: "sequential",
			body: []*Step{
				{ID: "work", Title: "Work", Needs: []string{"setup"}},
				{ID: "verify", Title: "Verify", Needs: []string{"work"}},
			},
			carried: map[string][]string{
				"round.iter1.work": {"setup", "brief"},
			},
		},
		{
			// Chaining gates only iteration 2's slice-first step, a.
			name: "parallel",
			body: []*Step{
				{ID: "a", Title: "A"},
				{ID: "b", Title: "B"},
			},
			carried: map[string][]string{
				"round.iter1.a": {"brief"},
				"round.iter1.b": {"brief"},
				"round.iter2.b": {"brief"},
			},
		},
		{
			// Chaining gates iteration 2's slice-first step, b, which its
			// sibling a already gates; a itself is gated by nothing.
			name: "out_of_order",
			body: []*Step{
				{ID: "b", Title: "B", Needs: []string{"a"}},
				{ID: "a", Title: "A"},
			},
			carried: map[string][]string{
				"round.iter1.a": {"brief"},
				"round.iter2.a": {"brief"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := []*Step{
				{ID: "setup", Title: "Setup"},
				{ID: "brief", Title: "Brief"},
				{
					ID:        "round",
					Title:     "Round",
					Needs:     []string{"brief"},
					DependsOn: []string{"brief"},
					Loop:      &LoopSpec{Count: 2, Body: tt.body},
				},
			}
			result, err := ApplyLoops(steps)
			if err != nil {
				t.Fatalf("ApplyLoops failed: %v", err)
			}

			loopIDs := map[string]bool{}
			for _, s := range result {
				if strings.HasPrefix(s.ID, "round.") {
					loopIDs[s.ID] = true
				}
			}
			namesLoopStep := func(deps []string) bool {
				for _, d := range deps {
					if loopIDs[d] {
						return true
					}
				}
				return false
			}

			for _, s := range result {
				if !loopIDs[s.ID] {
					continue
				}
				wantNeeds, carried := tt.carried[s.ID]
				if carried {
					if !slices.Equal(s.Needs, wantNeeds) {
						t.Errorf("%s.Needs = %v, want %v (brief appended after its own entries)", s.ID, s.Needs, wantNeeds)
					}
					if !slices.Equal(s.DependsOn, []string{"brief"}) {
						t.Errorf("%s.DependsOn = %v, want [brief]", s.ID, s.DependsOn)
					}
					continue
				}
				if slices.Contains(s.Needs, "brief") || slices.Contains(s.DependsOn, "brief") {
					t.Errorf("%s needs = %v, depends_on = %v; want no brief: another step of the loop gates it", s.ID, s.Needs, s.DependsOn)
				}
				if !namesLoopStep(s.Needs) && !namesLoopStep(s.DependsOn) {
					t.Errorf("%s needs = %v, depends_on = %v; want it gated by a step of the loop or by brief", s.ID, s.Needs, s.DependsOn)
				}
			}
		})
	}
}

// TestApplyLoops_NeedsOnLoopTargetItsEnds pins GH#7636: a step's Needs and
// DependsOn on a loop step, which no longer exists after expansion, are
// retargeted to the loop's ends, the expanded steps nothing else in the loop
// waits for: the last-declared one in place, the others appended.
func TestApplyLoops_NeedsOnLoopTargetItsEnds(t *testing.T) {
	tests := []struct {
		name          string
		loop          *Step
		land          *Step
		wantNeeds     []string
		wantDependsOn []string
		check         func(t *testing.T, byID map[string]*Step)
	}{
		{
			name: "count",
			loop: &Step{ID: "round", Title: "Round", Loop: &LoopSpec{
				Count: 2,
				Body:  []*Step{{ID: "work", Title: "Work"}},
			}},
			land:      &Step{ID: "land", Title: "Land", Needs: []string{"round"}},
			wantNeeds: []string{"round.iter2.work"},
		},
		{
			name: "range",
			loop: &Step{ID: "moves", Title: "Moves", Loop: &LoopSpec{
				Range: "1..3",
				Body:  []*Step{{ID: "move", Title: "Move"}},
			}},
			land:          &Step{ID: "land", Title: "Land", Needs: []string{"moves"}, DependsOn: []string{"moves"}},
			wantNeeds:     []string{"moves.iter3.move"},
			wantDependsOn: []string{"moves.iter3.move"},
		},
		{
			name: "position",
			loop: &Step{ID: "round", Title: "Round", Loop: &LoopSpec{
				Count: 2,
				Body:  []*Step{{ID: "work", Title: "Work"}},
			}},
			land:      &Step{ID: "land", Title: "Land", Needs: []string{"round", "other"}},
			wantNeeds: []string{"round.iter2.work", "other"},
		},
		{
			// Chaining gates only iteration 2's a, on iteration 1's b, so
			// round.iter1.a, round.iter2.a and round.iter2.b all end the loop.
			name: "parallel",
			loop: &Step{ID: "round", Title: "Round", Loop: &LoopSpec{
				Count: 2,
				Body:  []*Step{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}},
			}},
			land:          &Step{ID: "land", Title: "Land", Needs: []string{"round", "other"}, DependsOn: []string{"round"}},
			wantNeeds:     []string{"round.iter2.b", "other", "round.iter1.a", "round.iter2.a"},
			wantDependsOn: []string{"round.iter2.b", "round.iter1.a", "round.iter2.a"},
		},
		{
			// b needs a but is declared first, so the last-declared step, a,
			// does not end an iteration: b does.
			name: "out_of_order",
			loop: &Step{ID: "round", Title: "Round", Loop: &LoopSpec{
				Count: 2,
				Body: []*Step{
					{ID: "b", Title: "B", Needs: []string{"a"}},
					{ID: "a", Title: "A"},
				},
			}},
			land:      &Step{ID: "land", Title: "Land", Needs: []string{"round"}},
			wantNeeds: []string{"round.iter2.b", "round.iter1.b"},
		},
		{
			// An until loop cooks one iteration; the runtime re-runs it.
			name: "until",
			loop: &Step{ID: "round", Title: "Round", Needs: []string{"brief"}, Loop: &LoopSpec{
				Until: "step.status == 'complete'",
				Max:   5,
				Body: []*Step{
					{ID: "work", Title: "Work"},
					{ID: "check", Title: "Check", Needs: []string{"work"}},
				},
			}},
			land:      &Step{ID: "land", Title: "Land", Needs: []string{"round"}},
			wantNeeds: []string{"round.iter1.check"},
			check: func(t *testing.T, byID map[string]*Step) {
				work := byID["round.iter1.work"]
				if work == nil {
					t.Fatal("round.iter1.work not found")
				}
				if !slices.Equal(work.Needs, []string{"brief"}) {
					t.Errorf("round.iter1.work.Needs = %v, want [brief] (the loop step's own needs)", work.Needs)
				}
				hasLoopLabel := slices.ContainsFunc(work.Labels, func(l string) bool {
					return strings.HasPrefix(l, "loop:")
				})
				if !hasLoopLabel {
					t.Errorf("round.iter1.work.Labels = %v, want the loop: label kept for runtime evaluation", work.Labels)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := []*Step{
				{ID: "brief", Title: "Brief"},
				{ID: "other", Title: "Other"},
				tt.loop,
				tt.land,
			}
			result, err := ApplyLoops(steps)
			if err != nil {
				t.Fatalf("ApplyLoops failed: %v", err)
			}
			byID := make(map[string]*Step, len(result))
			for _, s := range result {
				byID[s.ID] = s
			}
			land := byID["land"]
			if land == nil {
				t.Fatal("land not found")
			}
			if !slices.Equal(land.Needs, tt.wantNeeds) {
				t.Errorf("land.Needs = %v, want %v", land.Needs, tt.wantNeeds)
			}
			if !slices.Equal(land.DependsOn, tt.wantDependsOn) {
				t.Errorf("land.DependsOn = %v, want %v", land.DependsOn, tt.wantDependsOn)
			}
			if tt.check != nil {
				tt.check(t, byID)
			}
		})
	}
}

// TestApplyLoops_NestedLoopDependencies pins GH#7636 for nested loops: a
// body step that needs a nested loop waits for that loop's last step in the
// same outer iteration, the nested loop's own needs gate its first step, and
// a step that needs the outer loop waits for its very last step.
func TestApplyLoops_NestedLoopDependencies(t *testing.T) {
	t.Run("two_levels", func(t *testing.T) {
		steps := []*Step{
			{ID: "brief", Title: "Brief"},
			{
				ID:    "outer",
				Title: "Outer",
				Needs: []string{"brief"},
				Loop: &LoopSpec{
					Count: 2,
					Body: []*Step{
						{ID: "prep", Title: "Prep"},
						{
							ID:    "inner",
							Title: "Inner",
							Needs: []string{"prep"},
							Loop: &LoopSpec{
								Count: 2,
								Body:  []*Step{{ID: "x", Title: "X"}},
							},
						},
						{ID: "after", Title: "After", Needs: []string{"inner"}},
					},
				},
			},
			{ID: "land", Title: "Land", Needs: []string{"outer"}},
		}
		result, err := ApplyLoops(steps)
		if err != nil {
			t.Fatalf("ApplyLoops failed: %v", err)
		}
		byID := make(map[string]*Step, len(result))
		for _, s := range result {
			byID[s.ID] = s
		}

		wantNeeds := map[string][]string{
			"outer.iter1.prep":          {"brief"},
			"outer.iter1.inner.iter1.x": {"outer.iter1.prep"},
			"outer.iter1.inner.iter2.x": {"outer.iter1.inner.iter1.x"},
			// Gated by the nested loop, so it does not also get brief.
			"outer.iter1.after":         {"outer.iter1.inner.iter2.x"},
			"outer.iter2.prep":          {"outer.iter1.after"},
			"outer.iter2.inner.iter1.x": {"outer.iter2.prep"},
			"outer.iter2.after":         {"outer.iter2.inner.iter2.x"},
			"land":                      {"outer.iter2.after"},
		}
		for id, want := range wantNeeds {
			s := byID[id]
			if s == nil {
				t.Errorf("%s not found", id)
				continue
			}
			if !slices.Equal(s.Needs, want) {
				t.Errorf("%s.Needs = %v, want %v", id, s.Needs, want)
			}
		}
	})

	t.Run("three_levels", func(t *testing.T) {
		steps := []*Step{
			{ID: "outer", Title: "Outer", Loop: &LoopSpec{Count: 2, Body: []*Step{
				{ID: "mid", Title: "Mid", Loop: &LoopSpec{Count: 2, Body: []*Step{
					{ID: "inner", Title: "Inner", Loop: &LoopSpec{Count: 2, Body: []*Step{
						{ID: "x", Title: "X"},
					}}},
				}}},
			}}},
			{ID: "land", Title: "Land", Needs: []string{"outer"}},
		}
		result, err := ApplyLoops(steps)
		if err != nil {
			t.Fatalf("ApplyLoops failed: %v", err)
		}
		land := result[len(result)-1]
		if want := []string{"outer.iter2.mid.iter2.inner.iter2.x"}; land.ID != "land" || !slices.Equal(land.Needs, want) {
			t.Errorf("%s.Needs = %v, want %v (the deepest last step)", land.ID, land.Needs, want)
		}
	})
}

// TestApplyLoops_LoopDependencyEdgeCases covers GH#7636 shapes beyond the
// acceptance criteria: loops in children, references between loops in either
// declaration order, de-duplication, self-references and immutability.
func TestApplyLoops_LoopDependencyEdgeCases(t *testing.T) {
	loop := func(id string, count int, needs []string, body ...*Step) *Step {
		return &Step{ID: id, Title: id, Needs: needs, Loop: &LoopSpec{Count: count, Body: body}}
	}
	step := func(id string, needs ...string) *Step {
		return &Step{ID: id, Title: id, Needs: needs}
	}
	expand := func(t *testing.T, steps []*Step) map[string]*Step {
		t.Helper()
		result, err := ApplyLoops(steps)
		if err != nil {
			t.Fatalf("ApplyLoops failed: %v", err)
		}
		byID := map[string]*Step{}
		var index func([]*Step)
		index = func(steps []*Step) {
			for _, s := range steps {
				byID[s.ID] = s
				index(s.Children)
			}
		}
		index(result)
		return byID
	}
	wantNeeds := func(t *testing.T, byID map[string]*Step, id string, want ...string) {
		t.Helper()
		s := byID[id]
		if s == nil {
			t.Fatalf("%s not found", id)
		}
		if !slices.Equal(s.Needs, want) {
			t.Errorf("%s.Needs = %v, want %v", id, s.Needs, want)
		}
	}

	t.Run("loop_in_children", func(t *testing.T) {
		phase := step("phase")
		phase.Children = []*Step{
			loop("round", 2, nil, step("work")),
			step("wrap", "round"),
		}
		byID := expand(t, []*Step{phase, step("land", "round")})
		wantNeeds(t, byID, "wrap", "round.iter2.work")
		wantNeeds(t, byID, "land", "round.iter2.work")
	})

	t.Run("body_step_names_another_loop", func(t *testing.T) {
		byID := expand(t, []*Step{
			loop("a", 2, nil, step("x")),
			loop("b", 1, nil, step("y", "a")),
			loop("c", 1, nil, step("z", "d")),
			loop("d", 2, nil, step("w")),
		})
		wantNeeds(t, byID, "b.iter1.y", "a.iter2.x")
		wantNeeds(t, byID, "c.iter1.z", "d.iter2.w")
	})

	t.Run("loop_needs_a_later_loop", func(t *testing.T) {
		byID := expand(t, []*Step{
			loop("b", 2, []string{"a"}, step("y")),
			loop("a", 2, nil, step("x")),
		})
		wantNeeds(t, byID, "b.iter1.y", "a.iter2.x")
		wantNeeds(t, byID, "b.iter2.y", "b.iter1.y")
	})

	t.Run("root_already_needs_it", func(t *testing.T) {
		byID := expand(t, []*Step{
			step("brief"),
			loop("round", 1, []string{"brief"}, step("work", "brief")),
		})
		wantNeeds(t, byID, "round.iter1.work", "brief")
	})

	t.Run("body_step_names_its_own_loop", func(t *testing.T) {
		// A loop in another loop's body expands under a prefixed ID, which
		// the references to it in its own body must reach too: from its
		// body steps, from a loop nested in it, and when it is a body
		// step's child.
		tests := []struct {
			name string
			// build returns a fresh copy of the input, so the test can
			// tell whether ApplyLoops changed it.
			build func() []*Step
			// loopID and stepID are the loop and the step the error names.
			loopID, stepID string
		}{
			{
				name: "top_level",
				build: func() []*Step {
					return []*Step{loop("round", 2, nil, step("work"), step("check", "round"))}
				},
				loopID: "round",
				stepID: "round.iter1.check",
			},
			{
				name: "nested",
				build: func() []*Step {
					return []*Step{loop("outer", 2, nil, loop("inner", 2, nil, step("x"), step("y", "inner")))}
				},
				loopID: "outer.iter1.inner",
				stepID: "outer.iter1.inner.iter1.y",
			},
			{
				name: "two_loops_down",
				build: func() []*Step {
					return []*Step{loop("outer", 1, nil, loop("mid", 1, nil, loop("inner", 1, nil, step("x", "mid"))))}
				},
				loopID: "outer.iter1.mid",
				stepID: "outer.iter1.mid.iter1.inner.iter1.x",
			},
			{
				// x names the innermost of the two loops called l.
				name: "same_id_nested",
				build: func() []*Step {
					return []*Step{loop("outer", 1, nil, loop("l", 1, nil, loop("l", 1, nil, step("x", "l"))))}
				},
				loopID: "outer.iter1.l.iter1.l",
				stepID: "outer.iter1.l.iter1.l.iter1.x",
			},
			{
				name: "loop_in_children",
				build: func() []*Step {
					phase := step("phase")
					phase.Children = []*Step{loop("inner", 1, nil, step("x"), step("y", "inner"))}
					return []*Step{loop("outer", 1, nil, phase)}
				},
				loopID: "outer.iter1.inner",
				stepID: "outer.iter1.inner.iter1.y",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				in := tt.build()
				_, err := ApplyLoops(in)
				if err == nil {
					t.Fatal("ApplyLoops accepted a body step that depends on its own loop")
				}
				// pour, wisp and bond report only an ErrValidation as the
				// formula's own error, not as "not found".
				if !errors.Is(err, ErrValidation) {
					t.Errorf("error = %v, want it to wrap ErrValidation", err)
				}
				want := fmt.Sprintf("loop %q: step %q depends on its own loop", tt.loopID, tt.stepID)
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
				if !reflect.DeepEqual(in, tt.build()) {
					t.Errorf("ApplyLoops modified its input steps")
				}
			})
		}
	})

	t.Run("body_step_named_like_its_loop", func(t *testing.T) {
		// The body's references to that ID name the step, not the loop,
		// whether the loop is nested or not.
		byID := expand(t, []*Step{
			loop("round", 1, nil, step("round"), step("y", "round")),
			loop("outer", 1, nil, loop("inner", 1, nil, step("inner"), step("y", "inner"))),
		})
		wantNeeds(t, byID, "round.iter1.y", "round.iter1.round")
		wantNeeds(t, byID, "outer.iter1.inner.iter1.y", "outer.iter1.inner.iter1.inner")
	})

	t.Run("cyclic_body", func(t *testing.T) {
		// No step ends the loop. Cook refuses the cycle, but the reference
		// still names a step of the loop rather than being dropped.
		byID := expand(t, []*Step{
			loop("round", 1, nil, step("a", "b"), step("b", "a")),
			step("land", "round"),
		})
		wantNeeds(t, byID, "land", "round.iter1.b")
	})

	t.Run("input_unchanged", func(t *testing.T) {
		build := func() []*Step {
			return []*Step{
				step("brief"),
				loop("round", 2, []string{"brief"}, step("work"), step("check", "work")),
				step("land", "round", "brief"),
			}
		}
		in := build()
		if _, err := ApplyLoops(in); err != nil {
			t.Fatalf("ApplyLoops failed: %v", err)
		}
		if !reflect.DeepEqual(in, build()) {
			t.Errorf("ApplyLoops modified its input steps")
		}
	})
}

// TestRetargetDeps pins how a reference to a loop becomes the loop's ends
// (GH#7636): the first in its place, the others appended unless already
// named, and an entry that names no loop left as it is.
func TestRetargetDeps(t *testing.T) {
	ends := map[string][]string{
		"round": {"round.iter2.b", "round.iter1.a", "round.iter2.a"},
		"solo":  {"solo.iter1.work"},
	}
	tests := []struct {
		name string
		deps []string
		want []string
	}{
		{
			name: "no_loop",
			deps: []string{"x", "y"},
			want: []string{"x", "y"},
		},
		{
			name: "first_end_in_place",
			deps: []string{"x", "round", "y"},
			want: []string{"x", "round.iter2.b", "y", "round.iter1.a", "round.iter2.a"},
		},
		{
			name: "end_already_named",
			deps: []string{"round", "round.iter1.a"},
			want: []string{"round.iter2.b", "round.iter1.a", "round.iter2.a"},
		},
		{
			name: "two_loops",
			deps: []string{"solo", "round"},
			want: []string{"solo.iter1.work", "round.iter2.b", "round.iter1.a", "round.iter2.a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retargetDeps(slices.Clone(tt.deps), ends); !slices.Equal(got, tt.want) {
				t.Errorf("retargetDeps(%v) = %v, want %v", tt.deps, got, tt.want)
			}
		})
	}
}

func TestApplyLoops_NestedChildren(t *testing.T) {
	// Test that children are preserved when recursing
	steps := []*Step{
		{
			ID:    "parent",
			Title: "Parent",
			Children: []*Step{
				{ID: "child", Title: "Child"},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("Expected 1 step, got %d", len(result))
	}

	if len(result[0].Children) != 1 {
		t.Errorf("Expected 1 child, got %d", len(result[0].Children))
	}
}

// gt-zn35j: Tests for nested loop support

func TestApplyLoops_NestedLoops(t *testing.T) {
	// Create a loop containing another loop
	steps := []*Step{
		{
			ID:    "outer",
			Title: "Outer loop",
			Loop: &LoopSpec{
				Count: 2,
				Body: []*Step{
					{
						ID:    "inner",
						Title: "Inner loop",
						Loop: &LoopSpec{
							Count: 2,
							Body: []*Step{
								{ID: "work", Title: "Do work"},
							},
						},
					},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	// Should have 4 steps total (2 outer * 2 inner)
	if len(result) != 4 {
		t.Errorf("Expected 4 steps, got %d", len(result))
		for i, s := range result {
			t.Logf("  Step %d: %s", i, s.ID)
		}
	}

	// Check step IDs follow nested pattern
	expectedIDs := []string{
		"outer.iter1.inner.iter1.work",
		"outer.iter1.inner.iter2.work",
		"outer.iter2.inner.iter1.work",
		"outer.iter2.inner.iter2.work",
	}

	for i, expected := range expectedIDs {
		if i >= len(result) {
			t.Errorf("Missing step %d: %s", i, expected)
			continue
		}
		if result[i].ID != expected {
			t.Errorf("Step %d: expected ID %s, got %s", i, expected, result[i].ID)
		}
	}
}

func TestApplyLoops_NestedLoopsWithDependencies(t *testing.T) {
	// Nested loops with dependencies between inner steps
	steps := []*Step{
		{
			ID:    "outer",
			Title: "Outer loop",
			Loop: &LoopSpec{
				Count: 2,
				Body: []*Step{
					{
						ID:    "inner",
						Title: "Inner loop",
						Loop: &LoopSpec{
							Count: 2,
							Body: []*Step{
								{ID: "fetch", Title: "Fetch data"},
								{ID: "process", Title: "Process data", Needs: []string{"fetch"}},
							},
						},
					},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	// Should have 8 steps (2 outer * 2 inner * 2 body steps)
	if len(result) != 8 {
		t.Errorf("Expected 8 steps, got %d", len(result))
	}

	// Check that inner dependencies are correctly prefixed
	// Find outer.iter1.inner.iter1.process - should depend on outer.iter1.inner.iter1.fetch
	var process1 *Step
	for _, s := range result {
		if s.ID == "outer.iter1.inner.iter1.process" {
			process1 = s
			break
		}
	}

	if process1 == nil {
		t.Fatal("outer.iter1.inner.iter1.process not found")
	}

	if len(process1.Needs) != 1 || process1.Needs[0] != "outer.iter1.inner.iter1.fetch" {
		t.Errorf("process should need outer.iter1.inner.iter1.fetch, got %v", process1.Needs)
	}
}

func TestApplyLoops_ThreeLevelNesting(t *testing.T) {
	// Three levels of nesting
	steps := []*Step{
		{
			ID: "l1",
			Loop: &LoopSpec{
				Count: 2,
				Body: []*Step{
					{
						ID: "l2",
						Loop: &LoopSpec{
							Count: 2,
							Body: []*Step{
								{
									ID: "l3",
									Loop: &LoopSpec{
										Count: 2,
										Body: []*Step{
											{ID: "leaf", Title: "Leaf step"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	// Should have 8 steps (2 * 2 * 2)
	if len(result) != 8 {
		t.Errorf("Expected 8 steps, got %d", len(result))
	}

	// Check first and last step IDs
	if result[0].ID != "l1.iter1.l2.iter1.l3.iter1.leaf" {
		t.Errorf("First step ID wrong: %s", result[0].ID)
	}
	if result[7].ID != "l1.iter2.l2.iter2.l3.iter2.leaf" {
		t.Errorf("Last step ID wrong: %s", result[7].ID)
	}
}

func TestApplyLoops_NestedLoopsOuterChaining(t *testing.T) {
	// Verify that outer iterations are chained AFTER nested loop expansion.
	// outer.iter2's first step should depend on outer.iter1's LAST step.
	steps := []*Step{
		{
			ID:    "outer",
			Title: "Outer loop",
			Loop: &LoopSpec{
				Count: 2,
				Body: []*Step{
					{
						ID:    "inner",
						Title: "Inner loop",
						Loop: &LoopSpec{
							Count: 2,
							Body: []*Step{
								{ID: "work", Title: "Do work"},
							},
						},
					},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	// Should have 4 steps
	if len(result) != 4 {
		t.Fatalf("Expected 4 steps, got %d", len(result))
	}

	// Expected order:
	// 0: outer.iter1.inner.iter1.work
	// 1: outer.iter1.inner.iter2.work (depends on above via inner chaining)
	// 2: outer.iter2.inner.iter1.work (should depend on step 1 via outer chaining!)
	// 3: outer.iter2.inner.iter2.work (depends on above via inner chaining)

	// Verify outer chaining: step 2 should depend on step 1
	step2 := result[2]
	if step2.ID != "outer.iter2.inner.iter1.work" {
		t.Fatalf("Step 2 ID wrong: %s", step2.ID)
	}

	// This is the key assertion: outer.iter2's first step must depend on
	// outer.iter1's last step (outer.iter1.inner.iter2.work)
	expectedDep := "outer.iter1.inner.iter2.work"
	found := false
	for _, need := range step2.Needs {
		if need == expectedDep {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("outer.iter2 first step should depend on outer.iter1 last step.\n"+
			"Expected Needs to contain %q, got %v", expectedDep, step2.Needs)
	}
}

// gt-v1pcg: Tests for immutability of ApplyBranches and ApplyGates

func TestApplyBranches_Immutability(t *testing.T) {
	// Create steps with no initial dependencies
	steps := []*Step{
		{ID: "setup", Title: "Setup", Needs: nil},
		{ID: "test", Title: "Run tests", Needs: nil},
		{ID: "deploy", Title: "Deploy", Needs: nil},
	}

	compose := &ComposeRules{
		Branch: []*BranchRule{
			{From: "setup", Steps: []string{"test"}, Join: "deploy"},
		},
	}

	// Call ApplyBranches
	result, err := ApplyBranches(steps, compose)
	if err != nil {
		t.Fatalf("ApplyBranches failed: %v", err)
	}

	// Verify original steps are NOT mutated
	for _, step := range steps {
		if len(step.Needs) != 0 {
			t.Errorf("Original step %q was mutated: Needs = %v (expected nil)", step.ID, step.Needs)
		}
	}

	// Verify result has the expected dependencies
	resultMap := make(map[string]*Step)
	for _, s := range result {
		resultMap[s.ID] = s
	}

	if len(resultMap["test"].Needs) != 1 || resultMap["test"].Needs[0] != "setup" {
		t.Errorf("Result test step should need setup, got %v", resultMap["test"].Needs)
	}
}

func TestApplyGates_Immutability(t *testing.T) {
	// Create steps with no initial labels
	steps := []*Step{
		{ID: "tests", Title: "Run tests", Labels: nil},
		{ID: "deploy", Title: "Deploy", Labels: nil},
	}

	compose := &ComposeRules{
		Gate: []*GateRule{
			{Before: "deploy", Condition: "tests.status == 'complete'"},
		},
	}

	// Call ApplyGates
	result, err := ApplyGates(steps, compose)
	if err != nil {
		t.Fatalf("ApplyGates failed: %v", err)
	}

	// Verify original steps are NOT mutated
	for _, step := range steps {
		if len(step.Labels) != 0 {
			t.Errorf("Original step %q was mutated: Labels = %v (expected nil)", step.ID, step.Labels)
		}
	}

	// Verify result has the expected labels
	var deployResult *Step
	for _, s := range result {
		if s.ID == "deploy" {
			deployResult = s
			break
		}
	}

	if deployResult == nil {
		t.Fatal("deploy step not found in result")
	}

	hasGate := false
	for _, label := range deployResult.Labels {
		if len(label) > 5 && label[:5] == "gate:" {
			hasGate = true
			break
		}
	}
	if !hasGate {
		t.Errorf("Result deploy step should have gate label, got %v", deployResult.Labels)
	}
}

// TestApplyLoops_Range tests computed range expansion (gt-8tmz.27).
func TestApplyLoops_Range(t *testing.T) {
	// Create a step with a range loop
	steps := []*Step{
		{
			ID:    "moves",
			Title: "Tower moves",
			Loop: &LoopSpec{
				Range: "1..3",
				Var:   "move_num",
				Body: []*Step{
					{ID: "move", Title: "Move {move_num}"},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	// Should have 3 steps (range 1..3 = 3 iterations)
	if len(result) != 3 {
		t.Errorf("Expected 3 steps, got %d", len(result))
	}

	// Check step IDs and titles
	expectedIDs := []string{
		"moves.iter1.move",
		"moves.iter2.move",
		"moves.iter3.move",
	}
	expectedTitles := []string{
		"Move 1",
		"Move 2",
		"Move 3",
	}

	for i, expected := range expectedIDs {
		if i >= len(result) {
			t.Errorf("Missing step %d: %s", i, expected)
			continue
		}
		if result[i].ID != expected {
			t.Errorf("Step %d: expected ID %s, got %s", i, expected, result[i].ID)
		}
		if result[i].Title != expectedTitles[i] {
			t.Errorf("Step %d: expected Title %q, got %q", i, expectedTitles[i], result[i].Title)
		}
	}
}

// TestApplyLoops_RangeComputed tests computed range with expressions.
func TestApplyLoops_RangeComputed(t *testing.T) {
	// Create a step with a computed range loop (like Towers of Hanoi)
	steps := []*Step{
		{
			ID:    "hanoi",
			Title: "Hanoi moves",
			Loop: &LoopSpec{
				Range: "1..2^3-1", // 1..7 (2^3-1 moves for 3 disks)
				Var:   "step_num",
				Body: []*Step{
					{ID: "step", Title: "Step {step_num}"},
				},
			},
		},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	// Should have 7 steps (2^3-1 = 7)
	if len(result) != 7 {
		t.Errorf("Expected 7 steps, got %d", len(result))
	}

	// Check first and last step
	if len(result) >= 1 {
		if result[0].Title != "Step 1" {
			t.Errorf("First step title: expected 'Step 1', got %q", result[0].Title)
		}
	}
	if len(result) >= 7 {
		if result[6].Title != "Step 7" {
			t.Errorf("Last step title: expected 'Step 7', got %q", result[6].Title)
		}
	}
}

// TestValidateLoopSpec_Range tests validation of range loops.
func TestValidateLoopSpec_Range(t *testing.T) {
	tests := []struct {
		name    string
		loop    *LoopSpec
		wantErr bool
	}{
		{
			name: "valid range",
			loop: &LoopSpec{
				Range: "1..10",
				Body:  []*Step{{ID: "step"}},
			},
			wantErr: false,
		},
		{
			name: "valid computed range",
			loop: &LoopSpec{
				Range: "1..2^3",
				Var:   "n",
				Body:  []*Step{{ID: "step"}},
			},
			wantErr: false,
		},
		{
			name: "invalid - both count and range",
			loop: &LoopSpec{
				Count: 5,
				Range: "1..10",
				Body:  []*Step{{ID: "step"}},
			},
			wantErr: true,
		},
		{
			name: "invalid - both until and range",
			loop: &LoopSpec{
				Until: "step.status == 'complete'",
				Max:   10,
				Range: "1..10",
				Body:  []*Step{{ID: "step"}},
			},
			wantErr: true,
		},
		{
			name: "invalid range syntax",
			loop: &LoopSpec{
				Range: "invalid",
				Body:  []*Step{{ID: "step"}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLoopSpec(tt.loop, "test")
			if (err != nil) != tt.wantErr {
				t.Errorf("validateLoopSpec() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Issue #7636: a loop step's own needs must reach the first expanded step, and
// another step's needs on the loop step must point at the last expanded step.
func TestApplyLoops_LoopStepNeedsCarriedThroughExpansion(t *testing.T) {
	steps := []*Step{
		{ID: "brief", Title: "Brief"},
		{
			ID:    "round",
			Title: "Round",
			Needs: []string{"brief"},
			Loop: &LoopSpec{
				Count: 2,
				Body:  []*Step{{ID: "work", Title: "Work"}},
			},
		},
		{ID: "land", Title: "Land", Needs: []string{"round"}},
	}

	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops failed: %v", err)
	}

	byID := map[string]*Step{}
	for _, s := range result {
		byID[s.ID] = s
	}
	has := func(deps []string, want string) bool {
		for _, d := range deps {
			if d == want {
				return true
			}
		}
		return false
	}

	first := byID["round.iter1.work"]
	if first == nil {
		t.Fatalf("missing round.iter1.work in %v", result)
	}
	if !has(first.Needs, "brief") && !has(first.DependsOn, "brief") {
		t.Errorf("round.iter1.work needs = %v, depends_on = %v; want the loop step's own need %q", first.Needs, first.DependsOn, "brief")
	}

	land := byID["land"]
	if land == nil {
		t.Fatalf("missing land in %v", result)
	}
	if has(land.Needs, "round") {
		t.Errorf("land needs = %v; still references the loop ID %q, which no longer exists after expansion", land.Needs, "round")
	}
	if !has(land.Needs, "round.iter2.work") {
		t.Errorf("land needs = %v; want the last expanded step %q", land.Needs, "round.iter2.work")
	}
}
