package formula

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubstituteTargetPlaceholders(t *testing.T) {
	target := &Step{
		ID:          "implement",
		Title:       "Implement the feature",
		Description: "Write the code for the feature",
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic target substitution",
			input:    "{target}.draft",
			expected: "implement.draft",
		},
		{
			name:     "target.id substitution",
			input:    "{target.id}.refine",
			expected: "implement.refine",
		},
		{
			name:     "target.title substitution",
			input:    "Working on: {target.title}",
			expected: "Working on: Implement the feature",
		},
		{
			name:     "target.description substitution",
			input:    "Task: {target.description}",
			expected: "Task: Write the code for the feature",
		},
		{
			name:     "multiple substitutions",
			input:    "{target}: {target.description}",
			expected: "implement: Write the code for the feature",
		},
		{
			name:     "no placeholders",
			input:    "plain text",
			expected: "plain text",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := substituteTargetPlaceholders(tt.input, target)
			if result != tt.expected {
				t.Errorf("substituteTargetPlaceholders(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExpandStep(t *testing.T) {
	target := &Step{
		ID:          "implement",
		Title:       "Implement the feature",
		Description: "Write the code",
	}

	template := []*Step{
		{
			ID:          "{target}.draft",
			Title:       "Draft: {target.title}",
			Description: "Initial attempt at: {target.description}",
		},
		{
			ID:    "{target}.refine",
			Title: "Refine: {target.title}",
			Needs: []string{"{target}.draft"},
		},
	}

	result, err := expandStep(target, template, 0)
	if err != nil {
		t.Fatalf("expandStep failed: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(result))
	}

	// Check first step
	if result[0].ID != "implement.draft" {
		t.Errorf("step 0 ID = %q, want %q", result[0].ID, "implement.draft")
	}
	if result[0].Title != "Draft: Implement the feature" {
		t.Errorf("step 0 Title = %q, want %q", result[0].Title, "Draft: Implement the feature")
	}
	if result[0].Description != "Initial attempt at: Write the code" {
		t.Errorf("step 0 Description = %q, want %q", result[0].Description, "Initial attempt at: Write the code")
	}

	// Check second step
	if result[1].ID != "implement.refine" {
		t.Errorf("step 1 ID = %q, want %q", result[1].ID, "implement.refine")
	}
	if len(result[1].Needs) != 1 || result[1].Needs[0] != "implement.draft" {
		t.Errorf("step 1 Needs = %v, want [implement.draft]", result[1].Needs)
	}
}

func TestExpandStepDepthLimit(t *testing.T) {
	target := &Step{
		ID:          "root",
		Title:       "Root step",
		Description: "A deeply nested template",
	}

	// Create a deeply nested template that exceeds the depth limit
	// Build from inside out: depth 6 is the deepest
	deepChild := &Step{ID: "level-6", Title: "Level 6"}
	for i := 5; i >= 0; i-- {
		deepChild = &Step{
			ID:       fmt.Sprintf("level-%d", i),
			Title:    fmt.Sprintf("Level %d", i),
			Children: []*Step{deepChild},
		}
	}

	template := []*Step{deepChild}

	// With depth 0 start, going to level 6 means 7 levels total (0-6)
	// DefaultMaxExpansionDepth is 5, so this should fail
	_, err := expandStep(target, template, 0)
	if err == nil {
		t.Fatal("expected depth limit error, got nil")
	}

	if !strings.Contains(err.Error(), "expansion depth limit exceeded") {
		t.Errorf("expected depth limit error, got: %v", err)
	}

	// Verify that templates within the limit succeed
	// Build a 5-level deep template (levels 0-4, which is exactly at the limit)
	shallowChild := &Step{ID: "level-4", Title: "Level 4"}
	for i := 3; i >= 0; i-- {
		shallowChild = &Step{
			ID:       fmt.Sprintf("level-%d", i),
			Title:    fmt.Sprintf("Level %d", i),
			Children: []*Step{shallowChild},
		}
	}

	shallowTemplate := []*Step{shallowChild}
	result, err := expandStep(target, shallowTemplate, 0)
	if err != nil {
		t.Fatalf("expected shallow template to succeed, got: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 top-level step, got %d", len(result))
	}
}

func TestReplaceStep(t *testing.T) {
	steps := []*Step{
		{ID: "design", Title: "Design"},
		{ID: "implement", Title: "Implement"},
		{ID: "test", Title: "Test"},
	}

	replacement := []*Step{
		{ID: "implement.draft", Title: "Draft"},
		{ID: "implement.refine", Title: "Refine"},
	}

	result := replaceStep(steps, "implement", replacement)

	if len(result) != 4 {
		t.Fatalf("expected 4 steps, got %d", len(result))
	}

	expected := []string{"design", "implement.draft", "implement.refine", "test"}
	for i, exp := range expected {
		if result[i].ID != exp {
			t.Errorf("result[%d].ID = %q, want %q", i, result[i].ID, exp)
		}
	}
}

func TestApplyExpansions(t *testing.T) {
	// Create a temporary directory with an expansion formula
	tmpDir := t.TempDir()

	// Create rule-of-five expansion formula
	ruleOfFive := `{
		"formula": "rule-of-five",
		"type": "expansion",
		"version": 1,
		"template": [
			{"id": "{target}.draft", "title": "Draft: {target.title}"},
			{"id": "{target}.refine", "title": "Refine", "needs": ["{target}.draft"]}
		]
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "rule-of-five.formula.json"), []byte(ruleOfFive), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create parser with temp dir as search path
	parser := NewParser(tmpDir)

	// Test expand operator
	t.Run("expand single step", func(t *testing.T) {
		steps := []*Step{
			{ID: "design", Title: "Design"},
			{ID: "implement", Title: "Implement the feature"},
			{ID: "test", Title: "Test"},
		}

		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{Target: "implement", With: "rule-of-five"},
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		if len(result) != 4 {
			t.Fatalf("expected 4 steps, got %d", len(result))
		}

		expected := []string{"design", "implement.draft", "implement.refine", "test"}
		for i, exp := range expected {
			if result[i].ID != exp {
				t.Errorf("result[%d].ID = %q, want %q", i, result[i].ID, exp)
			}
		}
	})

	// Test map operator
	t.Run("map over pattern", func(t *testing.T) {
		steps := []*Step{
			{ID: "design", Title: "Design"},
			{ID: "impl.auth", Title: "Implement auth"},
			{ID: "impl.api", Title: "Implement API"},
			{ID: "test", Title: "Test"},
		}

		compose := &ComposeRules{
			Map: []*MapRule{
				{Select: "impl.*", With: "rule-of-five"},
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		// design + (impl.auth -> 2 steps) + (impl.api -> 2 steps) + test = 6
		if len(result) != 6 {
			t.Fatalf("expected 6 steps, got %d", len(result))
		}

		// Verify the expanded IDs
		expectedIDs := []string{
			"design",
			"impl.auth.draft", "impl.auth.refine",
			"impl.api.draft", "impl.api.refine",
			"test",
		}
		for i, exp := range expectedIDs {
			if result[i].ID != exp {
				t.Errorf("result[%d].ID = %q, want %q", i, result[i].ID, exp)
			}
		}
	})

	// Test map over nested children (gt-8tmz.33)
	t.Run("map over nested children", func(t *testing.T) {
		steps := []*Step{
			{ID: "design", Title: "Design"},
			{
				ID:    "phase",
				Title: "Implementation Phase",
				Children: []*Step{
					{ID: "implement.auth", Title: "Implement auth"},
					{ID: "implement.api", Title: "Implement API"},
				},
			},
			{ID: "test", Title: "Test"},
		}

		compose := &ComposeRules{
			Map: []*MapRule{
				{Select: "*.auth", With: "rule-of-five"},
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		// The nested implement.auth should be expanded
		// Result should have: design, phase (with expanded children), test
		if len(result) != 3 {
			t.Fatalf("expected 3 top-level steps, got %d", len(result))
		}

		// Check that phase has expanded children
		phase := result[1]
		if phase.ID != "phase" {
			t.Fatalf("expected phase step, got %q", phase.ID)
		}

		// implement.auth expanded to 2 steps + implement.api unchanged = 3 children
		if len(phase.Children) != 3 {
			t.Fatalf("expected 3 children in phase, got %d: %v", len(phase.Children), getChildIDs(phase.Children))
		}

		// Verify expanded IDs
		childIDs := getChildIDs(phase.Children)
		expectedChildren := []string{"implement.auth.draft", "implement.auth.refine", "implement.api"}
		for i, exp := range expectedChildren {
			if childIDs[i] != exp {
				t.Errorf("phase.Children[%d].ID = %q, want %q", i, childIDs[i], exp)
			}
		}
	})

	// Test missing formula
	t.Run("missing expansion formula", func(t *testing.T) {
		steps := []*Step{{ID: "test", Title: "Test"}}
		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{Target: "test", With: "nonexistent"},
			},
		}

		_, err := ApplyExpansions(steps, compose, parser)
		if err == nil {
			t.Error("expected error for missing formula")
		}
	})

	// Test missing target step
	t.Run("missing target step", func(t *testing.T) {
		steps := []*Step{{ID: "test", Title: "Test"}}
		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{Target: "nonexistent", With: "rule-of-five"},
			},
		}

		_, err := ApplyExpansions(steps, compose, parser)
		if err == nil {
			t.Error("expected error for missing target step")
		}
	})
}

func TestBuildStepMap(t *testing.T) {
	steps := []*Step{
		{
			ID:    "parent",
			Title: "Parent",
			Children: []*Step{
				{ID: "child1", Title: "Child 1"},
				{ID: "child2", Title: "Child 2"},
			},
		},
		{ID: "sibling", Title: "Sibling"},
	}

	stepMap := buildStepMap(steps)

	if len(stepMap) != 4 {
		t.Errorf("expected 4 steps in map, got %d", len(stepMap))
	}

	expectedIDs := []string{"parent", "child1", "child2", "sibling"}
	for _, id := range expectedIDs {
		if _, ok := stepMap[id]; !ok {
			t.Errorf("step %q not found in map", id)
		}
	}
}

func TestUpdateDependenciesForExpansion(t *testing.T) {
	steps := []*Step{
		{ID: "design", Title: "Design"},
		{ID: "test", Title: "Test", Needs: []string{"implement"}},
		{ID: "deploy", Title: "Deploy", DependsOn: []string{"implement", "test"}},
	}

	result := UpdateDependenciesForExpansion(steps, "implement", "implement.refine")

	// Check test step
	if len(result[1].Needs) != 1 || result[1].Needs[0] != "implement.refine" {
		t.Errorf("test step Needs = %v, want [implement.refine]", result[1].Needs)
	}

	// Check deploy step
	if len(result[2].DependsOn) != 2 {
		t.Fatalf("deploy step DependsOn length = %d, want 2", len(result[2].DependsOn))
	}
	if result[2].DependsOn[0] != "implement.refine" {
		t.Errorf("deploy step DependsOn[0] = %q, want %q", result[2].DependsOn[0], "implement.refine")
	}
	if result[2].DependsOn[1] != "test" {
		t.Errorf("deploy step DependsOn[1] = %q, want %q", result[2].DependsOn[1], "test")
	}
}

// getChildIDs extracts IDs from a slice of steps (helper for tests).
func getChildIDs(steps []*Step) []string {
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = s.ID
	}
	return ids
}
