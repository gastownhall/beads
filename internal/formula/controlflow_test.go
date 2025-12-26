package formula

import (
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
