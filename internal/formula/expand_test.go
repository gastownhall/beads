package formula

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

	result, err := expandStep(target, template, nil)
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
	_, err := expandStep(target, template, nil)
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
	result, err := expandStep(target, shallowTemplate, nil)
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

// TestUpdateDependenciesForExpansionKeepsGeneratedNeedsGenerated: an edge a
// transform added onto the expanded step is still a generated edge once it
// names the expansion's last step, so FilterStepsByCondition routes it rather
// than reporting it as a reference the formula wrote.
func TestUpdateDependenciesForExpansionKeepsGeneratedNeedsGenerated(t *testing.T) {
	join := &Step{ID: "deploy", Title: "Deploy", Needs: []string{"design"}}
	addGeneratedNeed(join, "implement")

	result := UpdateDependenciesForExpansion([]*Step{join}, "implement", "implement.refine")

	if got, want := result[0].Needs, []string{"design", "implement.refine"}; !slices.Equal(got, want) {
		t.Fatalf("deploy Needs = %v, want %v", got, want)
	}
	if !isGeneratedNeed(result[0], "implement.refine") {
		t.Errorf("deploy's need on implement.refine is no longer generated")
	}
	if isGeneratedNeed(result[0], "design") {
		t.Errorf("deploy's written need on design became generated")
	}
	if !isGeneratedNeed(join, "implement") {
		t.Errorf("input step modified: its generated need on implement was renamed")
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

func TestSubstituteVars(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		vars     map[string]string
		expected string
	}{
		{
			name:     "single var substitution",
			input:    "Deploy to {environment}",
			vars:     map[string]string{"environment": "production"},
			expected: "Deploy to production",
		},
		{
			name:     "multiple var substitution",
			input:    "{component} v{version}",
			vars:     map[string]string{"component": "auth", "version": "2.0"},
			expected: "auth v2.0",
		},
		{
			name:     "unmatched placeholder stays",
			input:    "{known} and {unknown}",
			vars:     map[string]string{"known": "replaced"},
			expected: "replaced and {unknown}",
		},
		{
			name:     "empty vars map",
			input:    "no {change}",
			vars:     nil,
			expected: "no {change}",
		},
		{
			name:     "empty string",
			input:    "",
			vars:     map[string]string{"foo": "bar"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := substituteVars(tt.input, tt.vars)
			if result != tt.expected {
				t.Errorf("substituteVars(%q, %v) = %q, want %q", tt.input, tt.vars, result, tt.expected)
			}
		})
	}
}

func TestMergeVars(t *testing.T) {
	formula := &Formula{
		Vars: map[string]*VarDef{
			"env":     {Default: StringPtr("staging")},
			"version": {Default: StringPtr("1.0")},
			"name":    {Required: true}, // No default
		},
	}

	t.Run("overrides take precedence", func(t *testing.T) {
		overrides := map[string]string{"env": "production"}
		result := mergeVars(formula, overrides)

		if result["env"] != "production" {
			t.Errorf("env = %q, want 'production'", result["env"])
		}
		if result["version"] != "1.0" {
			t.Errorf("version = %q, want '1.0'", result["version"])
		}
	})

	t.Run("override adds new var", func(t *testing.T) {
		overrides := map[string]string{"custom": "value"}
		result := mergeVars(formula, overrides)

		if result["custom"] != "value" {
			t.Errorf("custom = %q, want 'value'", result["custom"])
		}
	})

	t.Run("nil overrides uses defaults", func(t *testing.T) {
		result := mergeVars(formula, nil)

		if result["env"] != "staging" {
			t.Errorf("env = %q, want 'staging'", result["env"])
		}
	})
}

func TestApplyExpansionsWithVars(t *testing.T) {
	// Create a temporary directory with an expansion formula that uses vars
	tmpDir := t.TempDir()

	// Create an expansion formula with variables
	envExpansion := `{
		"formula": "env-deploy",
		"type": "expansion",
		"version": 1,
		"vars": {
			"environment": {"default": "staging"},
			"replicas": {"default": "1"}
		},
		"template": [
			{"id": "{target}.prepare-{environment}", "title": "Prepare {environment} for {target.title}"},
			{"id": "{target}.deploy-{environment}", "title": "Deploy to {environment} with {replicas} replicas", "needs": ["{target}.prepare-{environment}"]}
		]
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "env-deploy.formula.json"), []byte(envExpansion), 0644)
	if err != nil {
		t.Fatal(err)
	}

	parser := NewParser(tmpDir)

	t.Run("expand with var overrides", func(t *testing.T) {
		steps := []*Step{
			{ID: "design", Title: "Design"},
			{ID: "release", Title: "Release v2"},
			{ID: "test", Title: "Test"},
		}

		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{
					Target: "release",
					With:   "env-deploy",
					Vars:   map[string]string{"environment": "production", "replicas": "3"},
				},
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		if len(result) != 4 {
			t.Fatalf("expected 4 steps, got %d", len(result))
		}

		// Check expanded step IDs include var substitution
		expectedIDs := []string{"design", "release.prepare-production", "release.deploy-production", "test"}
		for i, exp := range expectedIDs {
			if result[i].ID != exp {
				t.Errorf("result[%d].ID = %q, want %q", i, result[i].ID, exp)
			}
		}

		// Check title includes both target and var substitution
		if result[2].Title != "Deploy to production with 3 replicas" {
			t.Errorf("deploy title = %q, want 'Deploy to production with 3 replicas'", result[2].Title)
		}

		// Check that needs was also substituted correctly
		if len(result[2].Needs) != 1 || result[2].Needs[0] != "release.prepare-production" {
			t.Errorf("deploy needs = %v, want [release.prepare-production]", result[2].Needs)
		}
	})

	t.Run("expand with default vars", func(t *testing.T) {
		steps := []*Step{
			{ID: "release", Title: "Release"},
		}

		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{Target: "release", With: "env-deploy"},
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		// Check that defaults are used
		if result[0].ID != "release.prepare-staging" {
			t.Errorf("result[0].ID = %q, want 'release.prepare-staging'", result[0].ID)
		}
		if result[1].Title != "Deploy to staging with 1 replicas" {
			t.Errorf("deploy title = %q, want 'Deploy to staging with 1 replicas'", result[1].Title)
		}
	})

	t.Run("map with var overrides", func(t *testing.T) {
		steps := []*Step{
			{ID: "deploy.api", Title: "Deploy API"},
			{ID: "deploy.web", Title: "Deploy Web"},
		}

		compose := &ComposeRules{
			Map: []*MapRule{
				{
					Select: "deploy.*",
					With:   "env-deploy",
					Vars:   map[string]string{"environment": "prod"},
				},
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		// Each deploy.* step should expand with prod environment
		expectedIDs := []string{
			"deploy.api.prepare-prod", "deploy.api.deploy-prod",
			"deploy.web.prepare-prod", "deploy.web.deploy-prod",
		}
		if len(result) != len(expectedIDs) {
			t.Fatalf("expected %d steps, got %d", len(expectedIDs), len(result))
		}
		for i, exp := range expectedIDs {
			if result[i].ID != exp {
				t.Errorf("result[%d].ID = %q, want %q", i, result[i].ID, exp)
			}
		}
	})
}

func TestApplyExpansionsDuplicateIDs(t *testing.T) {
	// Create a temporary directory with an expansion formula
	tmpDir := t.TempDir()

	// Create expansion formula that generates "{target}.draft"
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

	parser := NewParser(tmpDir)

	// Test: expansion creates duplicate with existing step
	t.Run("duplicate with existing step", func(t *testing.T) {
		// "implement.draft" already exists, expansion will try to create it again
		steps := []*Step{
			{ID: "design", Title: "Design"},
			{ID: "implement", Title: "Implement the feature"},
			{ID: "implement.draft", Title: "Existing draft"}, // Conflicts with expansion
			{ID: "test", Title: "Test"},
		}

		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{Target: "implement", With: "rule-of-five"},
			},
		}

		_, err := ApplyExpansions(steps, compose, parser)
		if err == nil {
			t.Fatal("expected error for duplicate step IDs, got nil")
		}

		if !strings.Contains(err.Error(), "duplicate step IDs") {
			t.Errorf("expected duplicate step IDs error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "implement.draft") {
			t.Errorf("expected error to mention 'implement.draft', got: %v", err)
		}
	})

	// Test: map creates duplicates across multiple expansions
	t.Run("map creates cross-expansion duplicates", func(t *testing.T) {
		// Create a formula that generates static IDs (not using {target})
		staticExpansion := `{
			"formula": "static-ids",
			"type": "expansion",
			"version": 1,
			"template": [
				{"id": "shared-step", "title": "Shared step"},
				{"id": "another-shared", "title": "Another shared"}
			]
		}`
		err := os.WriteFile(filepath.Join(tmpDir, "static-ids.formula.json"), []byte(staticExpansion), 0644)
		if err != nil {
			t.Fatal(err)
		}

		steps := []*Step{
			{ID: "impl.auth", Title: "Implement auth"},
			{ID: "impl.api", Title: "Implement API"},
		}

		compose := &ComposeRules{
			Map: []*MapRule{
				{Select: "impl.*", With: "static-ids"},
			},
		}

		_, err = ApplyExpansions(steps, compose, parser)
		if err == nil {
			t.Fatal("expected error for duplicate step IDs from map, got nil")
		}

		if !strings.Contains(err.Error(), "duplicate step IDs") {
			t.Errorf("expected duplicate step IDs error, got: %v", err)
		}
	})
}

func TestApplyExpansionsCrossExpansionDeps(t *testing.T) {
	// Regression test: when two steps have a dependency chain and both are expanded,
	// the first substep of the second expansion must inherit the resolved dependency
	// on the last substep of the first expansion.
	tmpDir := t.TempDir()

	// Expansion template: target → target.sub-a, target.sub-b (chained)
	expansion := `{
		"formula": "two-step-expansion",
		"type": "expansion",
		"version": 1,
		"template": [
			{"id": "{target}.sub-a", "title": "Sub A of {target.title}"},
			{"id": "{target}.sub-b", "title": "Sub B of {target.title}", "needs": ["{target}.sub-a"]}
		]
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "two-step-expansion.formula.json"), []byte(expansion), 0644)
	if err != nil {
		t.Fatal(err)
	}

	parser := NewParser(tmpDir)

	t.Run("expand two chained steps preserves cross-expansion deps", func(t *testing.T) {
		steps := []*Step{
			{ID: "step-1", Title: "Step 1"},
			{ID: "step-2", Title: "Step 2", Needs: []string{"step-1"}},
		}

		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{Target: "step-1", With: "two-step-expansion"},
				{Target: "step-2", With: "two-step-expansion"},
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		if len(result) != 4 {
			t.Fatalf("expected 4 steps, got %d", len(result))
		}

		// Verify step IDs
		expectedIDs := []string{"step-1.sub-a", "step-1.sub-b", "step-2.sub-a", "step-2.sub-b"}
		for i, exp := range expectedIDs {
			if result[i].ID != exp {
				t.Errorf("result[%d].ID = %q, want %q", i, result[i].ID, exp)
			}
		}

		// KEY ASSERTION: step-2.sub-a must need step-1.sub-b (the last step of step-1's expansion)
		// Before the fix, this was [] (empty).
		step2SubA := result[2]
		if len(step2SubA.Needs) != 1 || step2SubA.Needs[0] != "step-1.sub-b" {
			t.Errorf("step-2.sub-a.Needs = %v, want [step-1.sub-b]", step2SubA.Needs)
		}

		// step-2.sub-b should need step-2.sub-a (internal template dep)
		step2SubB := result[3]
		if len(step2SubB.Needs) != 1 || step2SubB.Needs[0] != "step-2.sub-a" {
			t.Errorf("step-2.sub-b.Needs = %v, want [step-2.sub-a]", step2SubB.Needs)
		}

		// step-1.sub-a should have no deps (root of entire chain)
		if len(result[0].Needs) != 0 {
			t.Errorf("step-1.sub-a.Needs = %v, want []", result[0].Needs)
		}
	})

	t.Run("expand three chained steps preserves full dependency chain", func(t *testing.T) {
		steps := []*Step{
			{ID: "step-1", Title: "Step 1"},
			{ID: "step-2", Title: "Step 2", Needs: []string{"step-1"}},
			{ID: "step-3", Title: "Step 3", Needs: []string{"step-2"}},
		}

		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{Target: "step-1", With: "two-step-expansion"},
				{Target: "step-2", With: "two-step-expansion"},
				{Target: "step-3", With: "two-step-expansion"},
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		if len(result) != 6 {
			t.Fatalf("expected 6 steps, got %d", len(result))
		}

		// step-2.sub-a needs step-1.sub-b
		if len(result[2].Needs) != 1 || result[2].Needs[0] != "step-1.sub-b" {
			t.Errorf("step-2.sub-a.Needs = %v, want [step-1.sub-b]", result[2].Needs)
		}

		// step-3.sub-a needs step-2.sub-b
		if len(result[4].Needs) != 1 || result[4].Needs[0] != "step-2.sub-b" {
			t.Errorf("step-3.sub-a.Needs = %v, want [step-2.sub-b]", result[4].Needs)
		}
	})

	t.Run("expand with depends_on preserves cross-expansion deps", func(t *testing.T) {
		steps := []*Step{
			{ID: "step-1", Title: "Step 1"},
			{ID: "step-2", Title: "Step 2", DependsOn: []string{"step-1"}},
		}

		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{Target: "step-1", With: "two-step-expansion"},
				{Target: "step-2", With: "two-step-expansion"},
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		// step-2.sub-a must depend on step-1.sub-b via DependsOn
		step2SubA := result[2]
		if len(step2SubA.DependsOn) != 1 || step2SubA.DependsOn[0] != "step-1.sub-b" {
			t.Errorf("step-2.sub-a.DependsOn = %v, want [step-1.sub-b]", step2SubA.DependsOn)
		}
	})

	t.Run("non-expanded step after expanded step gets deps rewritten", func(t *testing.T) {
		// Verify that non-expanded steps still get their refs rewritten (existing behavior)
		steps := []*Step{
			{ID: "step-1", Title: "Step 1"},
			{ID: "step-2", Title: "Step 2", Needs: []string{"step-1"}},
		}

		compose := &ComposeRules{
			Expand: []*ExpandRule{
				{Target: "step-1", With: "two-step-expansion"},
				// step-2 is NOT expanded
			},
		}

		result, err := ApplyExpansions(steps, compose, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions failed: %v", err)
		}

		if len(result) != 3 {
			t.Fatalf("expected 3 steps, got %d", len(result))
		}

		// step-2 (non-expanded) should now need step-1.sub-b
		if len(result[2].Needs) != 1 || result[2].Needs[0] != "step-1.sub-b" {
			t.Errorf("step-2.Needs = %v, want [step-1.sub-b]", result[2].Needs)
		}
	})
}

func TestApplyInlineExpansionsCrossExpansionDeps(t *testing.T) {
	tmpDir := t.TempDir()

	expansion := `{
		"formula": "inline-exp",
		"type": "expansion",
		"version": 1,
		"template": [
			{"id": "{target}.first", "title": "First of {target.title}"},
			{"id": "{target}.second", "title": "Second of {target.title}", "needs": ["{target}.first"]}
		]
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "inline-exp.formula.json"), []byte(expansion), 0644)
	if err != nil {
		t.Fatal(err)
	}

	parser := NewParser(tmpDir)

	t.Run("inline expand preserves needs on expanded step", func(t *testing.T) {
		steps := []*Step{
			{ID: "setup", Title: "Setup"},
			{
				ID:     "work",
				Title:  "Work",
				Needs:  []string{"setup"},
				Expand: "inline-exp",
			},
		}

		result, err := ApplyInlineExpansions(steps, parser)
		if err != nil {
			t.Fatalf("ApplyInlineExpansions failed: %v", err)
		}

		// setup + work.first + work.second = 3
		if len(result) != 3 {
			t.Fatalf("expected 3 steps, got %d", len(result))
		}

		// work.first should inherit the "setup" dependency from the original step
		workFirst := result[1]
		if workFirst.ID != "work.first" {
			t.Fatalf("result[1].ID = %q, want work.first", workFirst.ID)
		}
		if len(workFirst.Needs) != 1 || workFirst.Needs[0] != "setup" {
			t.Errorf("work.first.Needs = %v, want [setup]", workFirst.Needs)
		}

		// work.second should only need work.first (internal template dep)
		workSecond := result[2]
		if len(workSecond.Needs) != 1 || workSecond.Needs[0] != "work.first" {
			t.Errorf("work.second.Needs = %v, want [work.first]", workSecond.Needs)
		}
	})

	// compose.branch makes work need prepare. The inline expansion moves that
	// need to work.first, where it is still compose.branch's edge and not the
	// formula's: when prepare's condition drops it, work.first is routed to
	// what prepare needed, and the pour does not fail.
	t.Run("a generated need moves to the root steps as generated", func(t *testing.T) {
		steps, err := ApplyControlFlow([]*Step{
			{ID: "setup", Title: "Setup"},
			{ID: "prepare", Title: "Prepare", Needs: []string{"setup"}, Condition: "{{on}}"},
			{ID: "work", Title: "Work", Expand: "inline-exp"},
			{ID: "deploy", Title: "Deploy"},
		}, &ComposeRules{Branch: []*BranchRule{{From: "prepare", Steps: []string{"work"}, Join: "deploy"}}})
		if err != nil {
			t.Fatalf("ApplyControlFlow failed: %v", err)
		}
		steps, err = ApplyInlineExpansions(steps, parser)
		if err != nil {
			t.Fatalf("ApplyInlineExpansions failed: %v", err)
		}

		result, err := FilterStepsByCondition(steps, map[string]string{"on": "false"})
		if err != nil {
			t.Fatalf("FilterStepsByCondition failed: %v", err)
		}
		workFirst := buildStepMap(result)["work.first"]
		if workFirst == nil {
			t.Fatalf("work.first missing after the filter")
		}
		if got, want := workFirst.Needs, []string{"setup"}; !slices.Equal(got, want) {
			t.Errorf("work.first.Needs = %v, want %v", got, want)
		}
	})
}

func TestFindDuplicateStepIDs(t *testing.T) {
	tests := []struct {
		name     string
		steps    []*Step
		expected []string
	}{
		{
			name: "no duplicates",
			steps: []*Step{
				{ID: "a"},
				{ID: "b"},
				{ID: "c"},
			},
			expected: nil,
		},
		{
			name: "top-level duplicate",
			steps: []*Step{
				{ID: "a"},
				{ID: "b"},
				{ID: "a"},
			},
			expected: []string{"a"},
		},
		{
			name: "nested duplicate",
			steps: []*Step{
				{ID: "parent", Children: []*Step{
					{ID: "child"},
				}},
				{ID: "child"}, // Duplicate with nested child
			},
			expected: []string{"child"},
		},
		{
			name: "deeply nested duplicate",
			steps: []*Step{
				{ID: "root", Children: []*Step{
					{ID: "level1", Children: []*Step{
						{ID: "level2"},
					}},
				}},
				{ID: "other", Children: []*Step{
					{ID: "level2"}, // Duplicate with deeply nested
				}},
			},
			expected: []string{"level2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dups := findDuplicateStepIDs(tt.steps)

			if len(dups) != len(tt.expected) {
				t.Fatalf("expected %d duplicates, got %d: %v", len(tt.expected), len(dups), dups)
			}

			// Check all expected duplicates are found (order may vary)
			for _, exp := range tt.expected {
				found := false
				for _, dup := range dups {
					if dup == exp {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected duplicate %q not found in %v", exp, dups)
				}
			}
		})
	}
}

func TestMaterializeExpansion(t *testing.T) {
	t.Run("rule-of-five style produces correct steps", func(t *testing.T) {
		f := &Formula{
			Formula:     "rule-of-five",
			Description: "Iterative refinement",
			Type:        TypeExpansion,
			Template: []*Step{
				{ID: "{target}.draft", Title: "Draft: {target.title}"},
				{ID: "{target}.refine-1", Title: "Refine 1", Needs: []string{"{target}.draft"}},
				{ID: "{target}.refine-2", Title: "Refine 2", Needs: []string{"{target}.refine-1"}},
				{ID: "{target}.refine-3", Title: "Refine 3", Needs: []string{"{target}.refine-2"}},
				{ID: "{target}.refine-4", Title: "Refine 4", Needs: []string{"{target}.refine-3"}},
			},
		}

		err := MaterializeExpansion(f, "main", nil)
		if err != nil {
			t.Fatalf("MaterializeExpansion failed: %v", err)
		}

		if len(f.Steps) != 5 {
			t.Fatalf("expected 5 steps, got %d", len(f.Steps))
		}

		expectedIDs := []string{"main.draft", "main.refine-1", "main.refine-2", "main.refine-3", "main.refine-4"}
		for i, exp := range expectedIDs {
			if f.Steps[i].ID != exp {
				t.Errorf("Steps[%d].ID = %q, want %q", i, f.Steps[i].ID, exp)
			}
		}

		// First step title uses {target.title} -> formula name
		if f.Steps[0].Title != "Draft: rule-of-five" {
			t.Errorf("Steps[0].Title = %q, want %q", f.Steps[0].Title, "Draft: rule-of-five")
		}

		// Needs chain resolved
		if len(f.Steps[1].Needs) != 1 || f.Steps[1].Needs[0] != "main.draft" {
			t.Errorf("Steps[1].Needs = %v, want [main.draft]", f.Steps[1].Needs)
		}
		if len(f.Steps[4].Needs) != 1 || f.Steps[4].Needs[0] != "main.refine-3" {
			t.Errorf("Steps[4].Needs = %v, want [main.refine-3]", f.Steps[4].Needs)
		}
	})

	t.Run("no-op for workflow formula", func(t *testing.T) {
		f := &Formula{
			Formula: "shiny",
			Type:    TypeWorkflow,
			Steps:   []*Step{{ID: "design", Title: "Design"}},
			Template: []*Step{
				{ID: "{target}.draft", Title: "Draft"},
			},
		}

		err := MaterializeExpansion(f, "main", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Steps unchanged
		if len(f.Steps) != 1 || f.Steps[0].ID != "design" {
			t.Errorf("Steps should be unchanged, got %v", getChildIDs(f.Steps))
		}
	})

	t.Run("no-op for expansion with existing steps", func(t *testing.T) {
		f := &Formula{
			Formula: "already-materialized",
			Type:    TypeExpansion,
			Steps:   []*Step{{ID: "existing", Title: "Already here"}},
			Template: []*Step{
				{ID: "{target}.draft", Title: "Draft"},
			},
		}

		err := MaterializeExpansion(f, "main", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Steps unchanged
		if len(f.Steps) != 1 || f.Steps[0].ID != "existing" {
			t.Errorf("Steps should be unchanged, got %v", getChildIDs(f.Steps))
		}
	})

	t.Run("no-op for expansion with empty template", func(t *testing.T) {
		f := &Formula{
			Formula:  "empty-expansion",
			Type:     TypeExpansion,
			Template: []*Step{},
		}

		err := MaterializeExpansion(f, "main", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(f.Steps) != 0 {
			t.Errorf("expected 0 steps, got %d", len(f.Steps))
		}
	})

	t.Run("single-brace vars substituted", func(t *testing.T) {
		f := &Formula{
			Formula: "env-deploy",
			Type:    TypeExpansion,
			Template: []*Step{
				{ID: "{target}.prepare-{env}", Title: "Prepare {env}"},
				{ID: "{target}.deploy-{env}", Title: "Deploy to {env}", Needs: []string{"{target}.prepare-{env}"}},
			},
		}

		vars := map[string]string{"env": "production"}
		err := MaterializeExpansion(f, "main", vars)
		if err != nil {
			t.Fatalf("MaterializeExpansion failed: %v", err)
		}

		if len(f.Steps) != 2 {
			t.Fatalf("expected 2 steps, got %d", len(f.Steps))
		}

		if f.Steps[0].ID != "main.prepare-production" {
			t.Errorf("Steps[0].ID = %q, want %q", f.Steps[0].ID, "main.prepare-production")
		}
		if f.Steps[1].ID != "main.deploy-production" {
			t.Errorf("Steps[1].ID = %q, want %q", f.Steps[1].ID, "main.deploy-production")
		}
		if f.Steps[1].Needs[0] != "main.prepare-production" {
			t.Errorf("Steps[1].Needs[0] = %q, want %q", f.Steps[1].Needs[0], "main.prepare-production")
		}
	})

	t.Run("double-brace vars pass through untouched", func(t *testing.T) {
		f := &Formula{
			Formula: "with-workflow-vars",
			Type:    TypeExpansion,
			Template: []*Step{
				{
					ID:          "{target}.work",
					Title:       "Work on {{feature}}",
					Description: "Build {{feature}} with brief: {{brief}}",
				},
			},
		}

		err := MaterializeExpansion(f, "main", nil)
		if err != nil {
			t.Fatalf("MaterializeExpansion failed: %v", err)
		}

		if f.Steps[0].Title != "Work on {{feature}}" {
			t.Errorf("Title = %q, want %q (double-brace should be preserved)", f.Steps[0].Title, "Work on {{feature}}")
		}
		if f.Steps[0].Description != "Build {{feature}} with brief: {{brief}}" {
			t.Errorf("Description = %q, want double-brace vars preserved", f.Steps[0].Description)
		}
	})
}

func TestExpansionVarRefs(t *testing.T) {
	t.Run("reports every name the expansion substitutes", func(t *testing.T) {
		f := &Formula{
			Formula:     "deploy",
			Description: "Ship {release}",
			Type:        TypeExpansion,
			Template: []*Step{
				{
					ID:          "{target}.build-{env}",
					Title:       "Build {component} for {target.title}",
					Description: "{target.description}",
					Assignee:    "{owner}",
					Labels:      []string{"team:{team}"},
					DependsOn:   []string{"{target}.prep-{stage}"},
					Needs:       []string{"{target.id}.gate-{gate}"},
					Notes:       "{note}",
					Metadata:    map[string]interface{}{"{key_only}": map[string]interface{}{"leaf": []interface{}{"{meta}"}}},
					WaitsFor:    "children-of({target}.spawn-{spawner})",
					// The gate's text fields are substituted like the
					// step's; the condition and on_complete are carried
					// verbatim, so nothing in them is substituted here.
					Condition:  "{{condition_only}}",
					Gate:       &Gate{Type: "gh:run", ID: "{gate_only}"},
					OnComplete: &OnCompleteSpec{Bond: "{bond_only}", Vars: map[string]string{"v": "{item}"}},
					Children: []*Step{
						{ID: "{target}.child", Title: "Work on {{feature}}"},
					},
				},
			},
		}

		// {target}, {target.id} and {target.title} are replaced before any
		// variable is, so none of them is a name; {target.description} pulls
		// in the formula's description, whose {release} IS substituted. A
		// {{feature}} reference is the cook's, not the expansion's, so the
		// {feature} inside it is not a name. Metadata keys are never
		// rewritten, so {key_only} is not a name.
		before, err := json.Marshal(f.Template)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(ExpansionVarRefs(f, "main"), " ")
		if want := "component env gate gate_only meta note owner release spawner stage team"; got != want {
			t.Errorf("ExpansionVarRefs() = %q, want %q", got, want)
		}
		if after, _ := json.Marshal(f.Template); string(after) != string(before) {
			t.Errorf("ExpansionVarRefs changed the template:\nbefore %s\nafter  %s", before, after)
		}
		if f.Steps != nil {
			t.Errorf("ExpansionVarRefs materialized the formula: Steps = %v", f.Steps)
		}
	})

	t.Run("reported even when the formula already has steps", func(t *testing.T) {
		// MaterializeExpansion skips this formula as it stands, but condition
		// filtering can remove every step first, and then the template is
		// materialized after all.
		f := &Formula{
			Formula:  "exp",
			Type:     TypeExpansion,
			Steps:    []*Step{{ID: "only", Title: "Only", Condition: "{{flag}}"}},
			Template: []*Step{{ID: "{target}.build", Title: "Build {component}"}},
		}

		got := strings.Join(ExpansionVarRefs(f, "main"), " ")
		if want := "component"; got != want {
			t.Errorf("ExpansionVarRefs() = %q, want %q", got, want)
		}
	})

	t.Run("nil unless the formula is an expansion with a template", func(t *testing.T) {
		workflow := &Formula{
			Formula:  "wf",
			Type:     TypeWorkflow,
			Template: []*Step{{ID: "{target}.build", Title: "Build {component}"}},
		}
		if got := ExpansionVarRefs(workflow, "main"); got != nil {
			t.Errorf("workflow formula: ExpansionVarRefs() = %v, want nil", got)
		}

		noTemplate := &Formula{Formula: "exp", Type: TypeExpansion}
		if got := ExpansionVarRefs(noTemplate, "main"); got != nil {
			t.Errorf("expansion without a template: ExpansionVarRefs() = %v, want nil", got)
		}
	})
}

// TestExpandStepPreservesTemplateFields: an expansion template step's notes,
// metadata, waits_for, gate and on_complete survive the expansion
// (bd-w153uq / #6433). Notes, metadata string leaves, waits_for and the
// gate's text fields get the same {target} and {var} substitution as the
// description, which leaves a {{name}} reference for the cook; on_complete is
// carried verbatim, even where it holds a {name} that the expansion has a
// value for.
func TestExpandStepPreservesTemplateFields(t *testing.T) {
	target := &Step{ID: "implement", Title: "Implement"}
	template := []*Step{
		{ID: "{target}.spawn", Title: "Spawn"},
		{
			ID:    "{target}.collect",
			Title: "Collect",
			Notes: "review {target.title} for {owner}",
			Metadata: map[string]interface{}{
				"owner":   "{owner}",
				"count":   int64(3),
				"enabled": true,
				"{owner}": "key is never rewritten",
				"nested": map[string]interface{}{
					"step": "{target}.spawn",
					"list": []interface{}{"{owner}", 1.5, nil},
				},
			},
			Needs:    []string{"{target}.spawn"},
			WaitsFor: "children-of({target}.spawn)",
			Gate: &Gate{
				Type:    "gh:run",
				ID:      "{owner}-ci",
				AwaitID: "{{run_id}}",
				Timeout: "1h",
				Repo:    "{owner}/repo",
			},
			OnComplete: &OnCompleteSpec{
				ForEach:    "output.items",
				Bond:       "mol-{owner}",
				Vars:       map[string]string{"name": "{item.name}", "owner": "{owner}", "at": "{index}"},
				Sequential: true,
			},
		},
		{ID: "{target}.wait", Title: "Wait", Needs: []string{"{target}.spawn"}, WaitsFor: "all-children"},
	}

	result, err := expandStep(target, template, map[string]string{"owner": "alice"})
	if err != nil {
		t.Fatalf("expandStep failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("got %d steps, want 3", len(result))
	}
	got := result[1]

	if want := "review Implement for alice"; got.Notes != want {
		t.Errorf("Notes = %q, want %q", got.Notes, want)
	}

	wantMeta := map[string]interface{}{
		"owner":   "alice",
		"count":   int64(3),
		"enabled": true,
		"{owner}": "key is never rewritten",
		"nested": map[string]interface{}{
			"step": "implement.spawn",
			"list": []interface{}{"alice", 1.5, nil},
		},
	}
	if !reflect.DeepEqual(got.Metadata, wantMeta) {
		t.Errorf("Metadata = %#v, want %#v", got.Metadata, wantMeta)
	}

	if want := "children-of(implement.spawn)"; got.WaitsFor != want {
		t.Errorf("WaitsFor = %q, want %q", got.WaitsFor, want)
	}
	if spec := ParseWaitsFor(got.WaitsFor); spec == nil || spec.SpawnerID != "implement.spawn" {
		t.Errorf("ParseWaitsFor(%q) = %+v, want spawner implement.spawn", got.WaitsFor, spec)
	}
	if want := "all-children"; result[2].WaitsFor != want {
		t.Errorf("literal WaitsFor = %q, want %q", result[2].WaitsFor, want)
	}

	wantGate := Gate{Type: "gh:run", ID: "alice-ci", AwaitID: "{{run_id}}", Timeout: "1h", Repo: "alice/repo"}
	if got.Gate == nil || *got.Gate != wantGate {
		t.Errorf("Gate = %+v, want %+v", got.Gate, wantGate)
	}
	if template[1].Gate.ID != "{owner}-ci" {
		t.Errorf("template gate ID = %q, want it unchanged", template[1].Gate.ID)
	}
	if got.OnComplete == nil || !reflect.DeepEqual(*got.OnComplete, *template[1].OnComplete) {
		t.Errorf("OnComplete = %+v, want verbatim %+v", got.OnComplete, template[1].OnComplete)
	}
}

// TestExpansionKeepsDoubleBraceReferences: a {{name}} reference in a template
// step's text is the cook's, filled in from the poured formula's variables,
// so the expansion leaves it whole even where it has a value for name, or
// name is target: replacing the {name} inside it left a {value} that neither
// stage reads (bd-w153uq / #6433).
func TestExpansionKeepsDoubleBraceReferences(t *testing.T) {
	target := &Step{ID: "work", Title: "Work"}
	tmpl := &Step{
		ID:          "{target}.s",
		Title:       "{story}: {{story}}",
		Description: "for {{target}} and {{target.title}}",
		Notes:       "story {{story}}",
		Labels:      []string{"story:{{story}}"},
		Metadata:    map[string]interface{}{"story": "{{story}}", "nested": []interface{}{"{story}/{{story}}"}},
		Gate:        &Gate{Type: "gh:run", ID: "{target}-{{story}}"},
	}
	result, err := expandStep(target, []*Step{tmpl}, map[string]string{"story": "s1", "target": "t1"})
	if err != nil {
		t.Fatalf("expandStep: %v", err)
	}
	got := result[0]
	for _, c := range []struct{ field, got, want string }{
		{"Title", got.Title, "s1: {{story}}"},
		{"Description", got.Description, "for {{target}} and {{target.title}}"},
		{"Notes", got.Notes, "story {{story}}"},
		{"Labels[0]", got.Labels[0], "story:{{story}}"},
		{"Gate.ID", got.Gate.ID, "work-{{story}}"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	wantMeta := map[string]interface{}{"story": "{{story}}", "nested": []interface{}{"s1/{{story}}"}}
	if !reflect.DeepEqual(got.Metadata, wantMeta) {
		t.Errorf("Metadata = %#v, want %#v", got.Metadata, wantMeta)
	}
}

// condExpansionJSON is an expansion formula whose template conditions test a
// variable it leaves unbound (flag), one its [vars] default binds
// (with_tests), and two that a compose rule's vars or an inline step's
// expand_vars bind in the cases below (env, run).
const condExpansionJSON = `{
	"formula": "cond-exp",
	"type": "expansion",
	"version": 1,
	"vars": {"with_tests": {"default": "true"}},
	"template": [
		{"id": "{target}.always", "title": "Always"},
		{"id": "{target}.on", "title": "On", "condition": "{{flag}}"},
		{"id": "{target}.tests", "title": "Tests", "condition": "{{with_tests}}"},
		{"id": "{target}.no-tests", "title": "No tests", "condition": "!{{with_tests}}"},
		{"id": "{target}.prod", "title": "Prod", "condition": "{{env}} == prod"},
		{"id": "{target}.run", "title": "Run", "condition": "{{run}}"}
	]
}`

// condExpansionPaths expands step "work" with cond-exp through each
// expansion path, the rule or the step binding bound.
func condExpansionPaths(t *testing.T) map[string]func(t *testing.T, bound map[string]string) ([]*Step, error) {
	t.Helper()
	return expansionPathsFor(t, "cond-exp", condExpansionJSON)
}

// expansionPathsFor expands step "work" with the expansion formula name,
// written from body, through each expansion path, the rule or the step
// binding bound.
func expansionPathsFor(t *testing.T, name, body string) map[string]func(t *testing.T, bound map[string]string) ([]*Step, error) {
	t.Helper()
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, name+".formula.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	parser := NewParser(tmpDir)
	return map[string]func(t *testing.T, bound map[string]string) ([]*Step, error){
		"compose_expand": func(t *testing.T, bound map[string]string) ([]*Step, error) {
			return ApplyExpansions([]*Step{{ID: "work", Title: "Work"}}, &ComposeRules{Expand: []*ExpandRule{{Target: "work", With: name, Vars: bound}}}, parser)
		},
		"compose_map": func(t *testing.T, bound map[string]string) ([]*Step, error) {
			return ApplyExpansions([]*Step{{ID: "work", Title: "Work"}}, &ComposeRules{Map: []*MapRule{{Select: "work", With: name, Vars: bound}}}, parser)
		},
		"inline": func(t *testing.T, bound map[string]string) ([]*Step, error) {
			return ApplyInlineExpansions([]*Step{{ID: "work", Title: "Work", Expand: name, ExpandVars: bound}}, parser)
		},
	}
}

func stepIDs(steps []*Step) []string {
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	return ids
}

// allStepIDs is stepIDs with each step's children, depth first.
func allStepIDs(steps []*Step) []string {
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID)
		ids = append(ids, allStepIDs(s.Children)...)
	}
	return ids
}

// childCondExpansionJSON is an expansion formula whose template conditions
// on a template step's children test a variable its [vars] default binds.
const childCondExpansionJSON = `{
	"formula": "child-cond-exp",
	"type": "expansion",
	"version": 1,
	"vars": {"with_tests": {"default": "true"}},
	"template": [
		{"id": "{target}.group", "title": "Group", "children": [
			{"id": "{target}.group.build", "title": "Build"},
			{"id": "{target}.group.tests", "title": "Tests", "condition": "{{with_tests}}"},
			{"id": "{target}.group.no-tests", "title": "No tests", "condition": "!{{with_tests}}"}
		]}
	]
}`

// TestTemplateChildConditionsReadTheExpansionsVars: on every expansion path,
// a template step's children read the expansion's value for a variable it
// binds in their conditions too, whatever the poured formula's variable of
// the same name says (bd-w153uq / #6433).
func TestTemplateChildConditionsReadTheExpansionsVars(t *testing.T) {
	for name, expand := range expansionPathsFor(t, "child-cond-exp", childCondExpansionJSON) {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				bound map[string]string
				vars  map[string]string
				want  []string
			}{
				{nil, map[string]string{"with_tests": "false"},
					[]string{"work.group", "work.group.build", "work.group.tests"}},
				{map[string]string{"with_tests": "false"}, map[string]string{"with_tests": "true"},
					[]string{"work.group", "work.group.build", "work.group.no-tests"}},
			} {
				steps, err := expand(t, tc.bound)
				if err != nil {
					t.Fatalf("bound %v: expand: %v", tc.bound, err)
				}
				for _, child := range steps[0].Children {
					if child.Condition != "" {
						t.Errorf("bound %v: %s condition = %q, want the decided condition cleared", tc.bound, child.ID, child.Condition)
					}
				}
				filtered, err := FilterStepsByCondition(steps, tc.vars)
				if err != nil {
					t.Fatalf("bound %v: FilterStepsByCondition: %v", tc.bound, err)
				}
				if ids := allStepIDs(filtered); !slices.Equal(ids, tc.want) {
					t.Errorf("bound %v, poured %v: kept %v, want %v", tc.bound, tc.vars, ids, tc.want)
				}
				// bd cook drops the same steps before its output.
				kept, err := DropExpansionExcludedSteps(steps)
				if err != nil {
					t.Fatalf("bound %v: DropExpansionExcludedSteps: %v", tc.bound, err)
				}
				if ids := allStepIDs(kept); !slices.Equal(ids, tc.want) {
					t.Errorf("bound %v: DropExpansionExcludedSteps kept %v, want %v", tc.bound, ids, tc.want)
				}
			}
		})
	}
}

// TestDropExpansionExcludedSteps: bd cook drops only the steps that their
// expansion's own value excluded, routing a generated edge onto one and
// refusing an edge the formula wrote, as the pour does; every other
// condition is left as it is for the pour to evaluate (bd-w153uq / #6433).
func TestDropExpansionExcludedSteps(t *testing.T) {
	for name, expand := range condExpansionPaths(t) {
		t.Run(name, func(t *testing.T) {
			steps, err := expand(t, map[string]string{"env": "dev", "run": "{{run_tests}}"})
			if err != nil {
				t.Fatalf("expand: %v", err)
			}
			kept, err := DropExpansionExcludedSteps(steps)
			if err != nil {
				t.Fatalf("DropExpansionExcludedSteps: %v", err)
			}
			got := make(map[string]string, len(kept))
			for _, s := range kept {
				got[s.ID] = s.Condition
			}
			// with_tests defaults to true and env is bound to dev, so
			// work.no-tests and work.prod go; flag and run_tests are the
			// pour's.
			want := map[string]string{
				"work.always": "",
				"work.on":     "{{flag}}",
				"work.tests":  "",
				"work.run":    "{{run_tests}}",
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("kept conditions = %v, want %v", got, want)
			}
		})
	}

	tmpDir := t.TempDir()
	expansion := `{
		"formula": "opt-exp",
		"type": "expansion",
		"version": 1,
		"vars": {"with_extra": {"default": "false"}},
		"template": [
			{"id": "{target}.draft", "title": "Draft"},
			{"id": "{target}.extra", "title": "Extra", "needs": ["{target}.draft"], "condition": "{{with_extra}}"}
		]
	}`
	if err := os.WriteFile(filepath.Join(tmpDir, "opt-exp.formula.json"), []byte(expansion), 0o644); err != nil {
		t.Fatal(err)
	}
	parser := NewParser(tmpDir)
	expanded, err := ApplyExpansions([]*Step{
		{ID: "work", Title: "Work"},
		{ID: "deploy", Title: "Deploy", Needs: []string{"work"}},
	}, &ComposeRules{Expand: []*ExpandRule{{Target: "work", With: "opt-exp"}}}, parser)
	if err != nil {
		t.Fatalf("ApplyExpansions: %v", err)
	}
	kept, err := DropExpansionExcludedSteps(expanded)
	if err != nil {
		t.Fatalf("DropExpansionExcludedSteps: %v", err)
	}
	if ids, want := stepIDs(kept), []string{"work.draft", "deploy"}; !slices.Equal(ids, want) {
		t.Errorf("kept %v, want %v", ids, want)
	}
	if got := buildStepMap(kept)["deploy"].Needs; !slices.Equal(got, []string{"work.draft"}) {
		t.Errorf("deploy needs %v, want [work.draft] (routed around the excluded last step)", got)
	}

	named := slices.Concat(expanded, []*Step{{ID: "audit", Title: "Audit", Needs: []string{"work.extra"}}})
	_, err = DropExpansionExcludedSteps(named)
	if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), `step "audit": needs references "work.extra"`) {
		t.Errorf("DropExpansionExcludedSteps err = %v, want the written reference refused with ErrValidation", err)
	}
}

// TestTemplateConditionsReadTheExpansionsVars: on every expansion path -
// compose.expand, compose.map and inline expand - a template step's condition
// reads the expansion's value for a variable the expansion binds (its [vars]
// default, a rule's vars or expand_vars), the value its {name} placeholders
// get, whatever the poured formula's variables say; "{{name}}" passes the
// poured formula's variable through; a condition on a variable the expansion
// does not bind reads the poured formula's variables (bd-w153uq / #6433).
func TestTemplateConditionsReadTheExpansionsVars(t *testing.T) {
	for name, expand := range condExpansionPaths(t) {
		t.Run(name, func(t *testing.T) {
			bound := map[string]string{"env": "prod", "run": "{{run_tests}}"}
			steps, err := expand(t, bound)
			if err != nil {
				t.Fatalf("expand: %v", err)
			}
			got := make(map[string]string, len(steps))
			for _, s := range steps {
				got[s.ID] = s.Condition
			}
			// A decided condition is cleared; "{{run_tests}}" is now what
			// work.run tests; flag is left for the cook.
			want := map[string]string{
				"work.always":   "",
				"work.on":       "{{flag}}",
				"work.tests":    "",
				"work.no-tests": "",
				"work.prod":     "",
				"work.run":      "{{run_tests}}",
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("expanded conditions = %v, want %v", got, want)
			}

			for _, tc := range []struct {
				vars map[string]string
				want []string
			}{
				// The poured formula's with_tests and env do not reach the
				// template: the expansion binds them.
				{map[string]string{"flag": "true", "run_tests": "true", "with_tests": "false", "env": "dev"},
					[]string{"work.always", "work.on", "work.tests", "work.prod", "work.run"}},
				{map[string]string{"flag": "false", "run_tests": "false", "with_tests": "true"},
					[]string{"work.always", "work.tests", "work.prod"}},
			} {
				filtered, err := FilterStepsByCondition(steps, tc.vars)
				if err != nil {
					t.Fatalf("FilterStepsByCondition(%v): %v", tc.vars, err)
				}
				if ids := stepIDs(filtered); !slices.Equal(ids, tc.want) {
					t.Errorf("FilterStepsByCondition(%v) kept %v, want %v", tc.vars, ids, tc.want)
				}
			}

			// The rule's or step's own value overrides the default.
			steps, err = expand(t, map[string]string{"with_tests": "false", "env": "dev", "run": "yes"})
			if err != nil {
				t.Fatalf("expand: %v", err)
			}
			filtered, err := FilterStepsByCondition(steps, map[string]string{"flag": "true", "with_tests": "true", "env": "prod"})
			if err != nil {
				t.Fatalf("FilterStepsByCondition: %v", err)
			}
			if ids, want := stepIDs(filtered), []string{"work.always", "work.on", "work.no-tests", "work.run"}; !slices.Equal(ids, want) {
				t.Errorf("kept %v, want %v", ids, want)
			}
		})
	}
}

// TestTemplateConditionRefusesAPlaceholderValue: the expansion fills {name}
// placeholders in template text, never in a value, so a condition whose
// variable is bound to "{skip}" would test that text, which is truthy, and
// keep or drop the step whatever the poured formula says. It is refused,
// naming the spelling that passes a variable through (bd-w153uq / #6433).
func TestTemplateConditionRefusesAPlaceholderValue(t *testing.T) {
	for name, expand := range condExpansionPaths(t) {
		t.Run(name, func(t *testing.T) {
			for _, value := range []string{"{run_tests}", "x{{run_tests}}"} {
				_, err := expand(t, map[string]string{"run": value})
				if err == nil {
					t.Fatalf("run = %q: expansion accepted it", value)
				}
				if !errors.Is(err, ErrValidation) {
					t.Errorf("run = %q: error %v does not wrap ErrValidation", value, err)
				}
				for _, want := range []string{`"work.run"`, `expansion "cond-exp"`, `"{{name}}"`} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("run = %q: error %q does not mention %s", value, err, want)
					}
				}
			}
		})
	}
}

// TestExpansionExcludedStepReferences: a template step that its expansion's
// value excludes is dropped by the condition filter whatever the poured
// variables are, its advice goes with it, a reference the template wrote to
// it is refused with the value that excluded it, and a dependency the
// expansion moved onto it from the step it replaced is routed around it
// (bd-w153uq / #6433).
func TestExpansionExcludedStepReferences(t *testing.T) {
	tmpDir := t.TempDir()
	expansion := `{
		"formula": "opt-exp",
		"type": "expansion",
		"version": 1,
		"vars": {"with_extra": {"default": "false"}},
		"template": [
			{"id": "{target}.draft", "title": "Draft"},
			{"id": "{target}.extra", "title": "Extra", "needs": ["{target}.draft"], "condition": "{{with_extra}}"}
		]
	}`
	if err := os.WriteFile(filepath.Join(tmpDir, "opt-exp.formula.json"), []byte(expansion), 0o644); err != nil {
		t.Fatal(err)
	}
	parser := NewParser(tmpDir)
	steps := []*Step{
		{ID: "work", Title: "Work"},
		{ID: "deploy", Title: "Deploy", Needs: []string{"work"}},
		{ID: "notify", Title: "Notify", DependsOn: []string{"work"}},
	}
	expanded, err := ApplyExpansions(steps, &ComposeRules{Expand: []*ExpandRule{{Target: "work", With: "opt-exp"}}}, parser)
	if err != nil {
		t.Fatalf("ApplyExpansions: %v", err)
	}
	// An aspect's advice is applied after expansions.
	advised := ApplyAdvice(expanded, []*AdviceRule{{Target: "work.extra", After: &AdviceStep{ID: "{step.id}-review", Title: "Review"}}})

	filtered, err := FilterStepsByCondition(advised, map[string]string{"with_extra": "true"})
	if err != nil {
		t.Fatalf("FilterStepsByCondition: %v", err)
	}
	byID := buildStepMap(filtered)
	if _, ok := byID["work.extra"]; ok {
		t.Errorf("work.extra kept although its expansion sets with_extra = false; kept %v", stepIDs(filtered))
	}
	if _, ok := byID["work.extra-review"]; ok {
		t.Errorf("the advice after work.extra was kept without its target; kept %v", stepIDs(filtered))
	}
	if got := byID["deploy"].Needs; !slices.Equal(got, []string{"work.draft"}) {
		t.Errorf("deploy needs %v, want [work.draft] (routed around the excluded last step)", got)
	}
	if got := byID["notify"].DependsOn; !slices.Equal(got, []string{"work.draft"}) {
		t.Errorf("notify depends_on %v, want [work.draft] (routed around the excluded last step)", got)
	}

	// A step that names the excluded step itself is refused, with the value
	// that excluded it.
	named := slices.Concat(expanded, []*Step{{ID: "audit", Title: "Audit", Needs: []string{"work.extra"}}})
	_, err = FilterStepsByCondition(named, nil)
	if err == nil || !errors.Is(err, ErrValidation) {
		t.Fatalf("FilterStepsByCondition err = %v, want a refusal wrapping ErrValidation", err)
	}
	for _, want := range []string{`step "audit": needs references "work.extra"`, `since expansion "opt-exp" sets with_extra = "false"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestExpansionStandInEdgeRoutesAroundADroppedLastStep: a needs or depends_on
// edge onto an expanded step is moved onto the expansion's last step, an edge
// the formula never wrote; when a condition drops that last step, the edge is
// routed to the steps it needs instead of refusing the pour, on compose.expand
// and compose.map, also where the last step takes the expanded step's own ID.
// An edge the formula wrote onto the last step itself is still refused
// (bd-w153uq / #6433).
func TestExpansionStandInEdgeRoutesAroundADroppedLastStep(t *testing.T) {
	tmpDir := t.TempDir()
	for name, lastID := range map[string]string{"tail-exp": "{target}.extra", "tail-self-exp": "{target}"} {
		body := fmt.Sprintf(`{
			"formula": %q,
			"type": "expansion",
			"version": 1,
			"template": [
				{"id": "{target}.draft", "title": "Draft"},
				{"id": %q, "title": "Extra", "needs": ["{target}.draft"], "condition": "{{with_extra}}"}
			]
		}`, name, lastID)
		if err := os.WriteFile(filepath.Join(tmpDir, name+".formula.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	twoExp := `{
		"formula": "two-exp",
		"type": "expansion",
		"version": 1,
		"template": [
			{"id": "{target}.x", "title": "X"},
			{"id": "{target}.y", "title": "Y", "needs": ["{target}.x"]}
		]
	}`
	if err := os.WriteFile(filepath.Join(tmpDir, "two-exp.formula.json"), []byte(twoExp), 0o644); err != nil {
		t.Fatal(err)
	}
	parser := NewParser(tmpDir)

	// A step expanded after the expansion with the conditional last step
	// passes the moved edge on to its own first step, still generated, so
	// the edge is routed there too.
	t.Run("chained_expansion", func(t *testing.T) {
		for _, field := range []string{"needs", "depends_on"} {
			implement := &Step{ID: "implement", Title: "Implement"}
			edges := func(s *Step) []string { return s.Needs }
			if field == "needs" {
				implement.Needs = []string{"design"}
			} else {
				implement.DependsOn = []string{"design"}
				edges = func(s *Step) []string { return s.DependsOn }
			}
			expanded, err := ApplyExpansions([]*Step{{ID: "design", Title: "Design"}, implement}, &ComposeRules{Expand: []*ExpandRule{
				{Target: "design", With: "tail-exp"},
				{Target: "implement", With: "two-exp"},
			}}, parser)
			if err != nil {
				t.Fatalf("%s: ApplyExpansions: %v", field, err)
			}
			for _, c := range []struct {
				withExtra, want string
			}{{"true", "design.extra"}, {"false", "design.draft"}} {
				filtered, err := FilterStepsByCondition(expanded, map[string]string{"with_extra": c.withExtra})
				if err != nil {
					t.Fatalf("%s, with_extra=%s: FilterStepsByCondition: %v", field, c.withExtra, err)
				}
				if got := edges(buildStepMap(filtered)["implement.x"]); !slices.Equal(got, []string{c.want}) {
					t.Errorf("%s, with_extra=%s: implement.x %s %v, want [%s]", field, c.withExtra, field, got, c.want)
				}
			}
		}
	})

	for _, tc := range []struct {
		name, with, last string
		map_             bool
	}{
		{"compose_expand", "tail-exp", "work.extra", false},
		{"compose_map", "tail-exp", "work.extra", true},
		{"last_step_takes_the_target_id", "tail-self-exp", "work", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []*Step{
				{ID: "work", Title: "Work"},
				{ID: "deploy", Title: "Deploy", Needs: []string{"work"}},
				{ID: "notify", Title: "Notify", DependsOn: []string{"work"}},
			}
			compose := &ComposeRules{Expand: []*ExpandRule{{Target: "work", With: tc.with}}}
			if tc.map_ {
				compose = &ComposeRules{Map: []*MapRule{{Select: "work", With: tc.with}}}
			}
			expanded, err := ApplyExpansions(steps, compose, parser)
			if err != nil {
				t.Fatalf("ApplyExpansions: %v", err)
			}

			for _, c := range []struct {
				withExtra string
				want      string
			}{{"true", tc.last}, {"false", "work.draft"}} {
				filtered, err := FilterStepsByCondition(expanded, map[string]string{"with_extra": c.withExtra})
				if err != nil {
					t.Fatalf("with_extra=%s: FilterStepsByCondition: %v", c.withExtra, err)
				}
				byID := buildStepMap(filtered)
				if got := byID["deploy"].Needs; !slices.Equal(got, []string{c.want}) {
					t.Errorf("with_extra=%s: deploy needs %v, want [%s]", c.withExtra, got, c.want)
				}
				if got := byID["notify"].DependsOn; !slices.Equal(got, []string{c.want}) {
					t.Errorf("with_extra=%s: notify depends_on %v, want [%s]", c.withExtra, got, c.want)
				}
			}
		})
	}

	t.Run("an_edge_the_formula_wrote_onto_the_last_step_is_refused", func(t *testing.T) {
		steps := []*Step{
			{ID: "work", Title: "Work"},
			{ID: "deploy", Title: "Deploy", Needs: []string{"work.extra", "work"}},
		}
		expanded, err := ApplyExpansions(steps, &ComposeRules{Expand: []*ExpandRule{{Target: "work", With: "tail-exp"}}}, parser)
		if err != nil {
			t.Fatalf("ApplyExpansions: %v", err)
		}
		if got := buildStepMap(expanded)["deploy"].Needs; !slices.Equal(got, []string{"work.extra"}) {
			t.Errorf("deploy needs %v, want [work.extra] (the moved edge merged into the written one)", got)
		}
		_, err = FilterStepsByCondition(expanded, map[string]string{"with_extra": "false"})
		if err == nil || !strings.Contains(err.Error(), `step "deploy": needs references "work.extra"`) {
			t.Errorf("FilterStepsByCondition err = %v, want the written reference refused", err)
		}
	})
}

// TestExpandStepTemplateIsolation: every expanded step owns its mutable
// state, so mutating one expansion's result changes neither the template nor
// another expansion of it (bd-w153uq / #6433).
func TestExpandStepTemplateIsolation(t *testing.T) {
	target := &Step{ID: "work", Title: "Work"}
	newTemplate := func() *Step {
		prio := 1
		return &Step{
			ID:        "{target}.s",
			Title:     "S",
			Priority:  &prio,
			Labels:    []string{"a"},
			DependsOn: []string{"x"},
			Needs:     []string{"y"},
			Metadata: map[string]interface{}{
				"nested":    map[string]interface{}{"list": []interface{}{"v", map[string]interface{}{"deep": "d"}}},
				"emptyMap":  map[string]interface{}{},
				"emptyList": []interface{}{},
				"nilValue":  nil,
			},
			Gate:       &Gate{Type: "human", Timeout: "1h"},
			OnComplete: &OnCompleteSpec{Bond: "b", Vars: map[string]string{}},
			Children:   []*Step{{ID: "{target}.c", Title: "C", Labels: []string{"c"}, Metadata: map[string]interface{}{"k": "v"}}},
		}
	}
	snapshot := func(s *Step) string {
		t.Helper()
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	tmpl := newTemplate()
	tmplBefore := snapshot(tmpl)
	a, err := expandStep(target, []*Step{tmpl}, nil)
	if err != nil {
		t.Fatalf("expandStep: %v", err)
	}
	b, err := expandStep(target, []*Step{tmpl}, nil)
	if err != nil {
		t.Fatalf("expandStep: %v", err)
	}
	bBefore := snapshot(b[0])

	// Empty containers keep their kind: an empty list stays an empty list,
	// not null, and OnComplete keeps a non-nil Vars map.
	if l, ok := a[0].Metadata["emptyList"].([]interface{}); !ok || l == nil {
		t.Errorf("Metadata[emptyList] = %#v, want a non-nil empty list", a[0].Metadata["emptyList"])
	}
	if v, ok := a[0].Metadata["nilValue"]; !ok || v != nil {
		t.Errorf("Metadata[nilValue] = %#v (present %v), want a present nil", v, ok)
	}
	if a[0].OnComplete == nil || a[0].OnComplete.Vars == nil {
		t.Errorf("OnComplete.Vars = nil, want a non-nil empty map")
	}

	got := a[0]
	if got.Priority == nil || got.Metadata == nil || got.Gate == nil || got.OnComplete == nil || len(got.Children) != 1 || got.Children[0].Metadata == nil {
		t.Fatalf("expanded step dropped template fields, nothing to mutate: %+v", got)
	}
	*got.Priority = 4
	got.Labels[0] = "mut"
	got.DependsOn[0] = "mut"
	got.Needs[0] = "mut"
	nested := got.Metadata["nested"].(map[string]interface{})
	list := nested["list"].([]interface{})
	list[0] = "mut"
	list[1].(map[string]interface{})["deep"] = "mut"
	nested["added"] = "mut"
	got.Metadata["emptyMap"].(map[string]interface{})["added"] = "mut"
	got.Gate.Timeout = "9h"
	got.OnComplete.Bond = "mut"
	got.OnComplete.Vars["added"] = "mut"
	got.Children[0].Title = "mut"
	got.Children[0].Labels[0] = "mut"
	got.Children[0].Metadata["k"] = "mut"

	if after := snapshot(tmpl); after != tmplBefore {
		t.Errorf("mutating an expanded step changed the template:\nbefore %s\nafter  %s", tmplBefore, after)
	}
	if after := snapshot(b[0]); after != bBefore {
		t.Errorf("mutating one expansion changed another:\nbefore %s\nafter  %s", bBefore, after)
	}
}

// nestedMetadata returns step metadata whose single string leaf sits at
// depth levels, counting the root map as depth zero (metadataStrings'
// convention).
func nestedMetadata(levels int, leaf string) map[string]interface{} {
	var v interface{} = leaf
	for i := 1; i < levels; i++ {
		v = map[string]interface{}{"k": v}
	}
	return map[string]interface{}{"k": v}
}

func metadataLeaf(t *testing.T, m map[string]interface{}) interface{} {
	t.Helper()
	var v interface{} = m
	for {
		mm, ok := v.(map[string]interface{})
		if !ok {
			return v
		}
		v = mm["k"]
	}
}

// TestExpandStepMetadataDepthBound: metadata nested up to the scan bound is
// cloned and substituted; anything deeper is an error naming the step, never
// dropped or shared with the template (bd-w153uq / #6433).
func TestExpandStepMetadataDepthBound(t *testing.T) {
	target := &Step{ID: "work", Title: "Work"}
	vars := map[string]string{"owner": "alice"}

	t.Run("below_bound", func(t *testing.T) {
		tmpl := &Step{ID: "{target}.s", Title: "S", Metadata: nestedMetadata(maxMetadataScanDepth-1, "{owner}")}
		result, err := expandStep(target, []*Step{tmpl}, vars)
		if err != nil {
			t.Fatalf("expandStep: %v", err)
		}
		if got := metadataLeaf(t, result[0].Metadata); got != "alice" {
			t.Errorf("deepest leaf = %#v, want %q", got, "alice")
		}
		if got := metadataLeaf(t, tmpl.Metadata); got != "{owner}" {
			t.Errorf("template leaf = %#v, want it unchanged", got)
		}
	})

	t.Run("at_bound", func(t *testing.T) {
		tmpl := &Step{ID: "{target}.s", Title: "S", Metadata: nestedMetadata(maxMetadataScanDepth, "{owner}")}
		_, err := expandStep(target, []*Step{tmpl}, vars)
		if err == nil {
			t.Fatal("expandStep accepted metadata nested past the bound")
		}
		// pour and wisp report only a cook error that wraps ErrValidation;
		// any other makes them say the formula was not found.
		if !errors.Is(err, ErrValidation) {
			t.Errorf("error %v does not wrap ErrValidation", err)
		}
		for _, want := range []string{`"{target}.s"`, "metadata", fmt.Sprint(maxMetadataScanDepth)} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})
}

// TestExpandStepRejectsTemplateLoop: control flow runs before expansions, so
// a loop on an expansion template step would never be expanded; it is an
// error naming the step instead of being dropped (bd-w153uq / #6433).
func TestExpandStepRejectsTemplateLoop(t *testing.T) {
	target := &Step{ID: "work", Title: "Work"}
	loop := &LoopSpec{Count: 2, Body: []*Step{{ID: "iter", Title: "Iter"}}}

	t.Run("top_level", func(t *testing.T) {
		_, err := expandStep(target, []*Step{{ID: "{target}.loop", Title: "L", Loop: loop}}, nil)
		if err == nil || !strings.Contains(err.Error(), `"{target}.loop"`) || !strings.Contains(err.Error(), "loop") {
			t.Fatalf("err = %v, want a loop error naming {target}.loop", err)
		}
		// pour and wisp report only a cook error that wraps ErrValidation;
		// any other makes them say the formula was not found.
		if !errors.Is(err, ErrValidation) {
			t.Errorf("error %v does not wrap ErrValidation", err)
		}
	})

	t.Run("child", func(t *testing.T) {
		tmpl := &Step{ID: "{target}.parent", Title: "P", Children: []*Step{{ID: "{target}.inner", Title: "I", Loop: loop}}}
		_, err := expandStep(target, []*Step{tmpl}, nil)
		if err == nil || !strings.Contains(err.Error(), `"{target}.inner"`) {
			t.Fatalf("err = %v, want a loop error naming {target}.inner", err)
		}
	})

	t.Run("wrappers", func(t *testing.T) {
		f := &Formula{Formula: "looped", Type: TypeExpansion, Template: []*Step{{ID: "{target}.loop", Title: "L", Loop: loop}}}
		if err := MaterializeExpansion(f, "main", nil); err == nil || !strings.Contains(err.Error(), "loop") {
			t.Errorf("MaterializeExpansion err = %v, want the loop error", err)
		}
		if f.Steps != nil {
			t.Errorf("MaterializeExpansion produced steps %v", f.Steps)
		}
		if got := ExpansionVarRefs(f, "main"); got != nil {
			t.Errorf("ExpansionVarRefs = %v, want nil for a template that cannot expand", got)
		}

		tmpDir := t.TempDir()
		body := `{"formula": "looped", "type": "expansion", "version": 1,
			"template": [{"id": "{target}.loop", "title": "L", "loop": {"count": 2, "body": [{"id": "iter", "title": "Iter"}]}}]}`
		if err := os.WriteFile(filepath.Join(tmpDir, "looped.formula.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		parser := NewParser(tmpDir)
		_, err := ApplyExpansions([]*Step{{ID: "work", Title: "Work"}}, &ComposeRules{Expand: []*ExpandRule{{Target: "work", With: "looped"}}}, parser)
		if err == nil || !strings.Contains(err.Error(), `"{target}.loop"`) || !errors.Is(err, ErrValidation) {
			t.Errorf("ApplyExpansions err = %v, want the loop error, wrapping ErrValidation", err)
		}
		_, err = ApplyInlineExpansions([]*Step{{ID: "work", Title: "Work", Expand: "looped"}}, parser)
		if err == nil || !strings.Contains(err.Error(), `"{target}.loop"`) || !errors.Is(err, ErrValidation) {
			t.Errorf("ApplyInlineExpansions err = %v, want the loop error, wrapping ErrValidation", err)
		}
	})
}

// TestExpandStepClearsInlineExpand pins the documented policy that nested
// template expansion directives are cleared: an expanded step carries no
// expand/expand_vars, so it is neither re-expanded nor an error to load
// (bd-w153uq / #6433). This matched the old behavior as well; it is
// compatibility coverage for the copy-based expansion.
func TestExpandStepClearsInlineExpand(t *testing.T) {
	target := &Step{ID: "work", Title: "Work"}
	tmpl := &Step{ID: "{target}.nested", Title: "N", Expand: "does-not-exist", ExpandVars: map[string]string{"k": "v"},
		Children: []*Step{{ID: "{target}.child", Title: "C", Expand: "does-not-exist"}}}

	result, err := expandStep(target, []*Step{tmpl}, nil)
	if err != nil {
		t.Fatalf("expandStep: %v", err)
	}
	if result[0].Expand != "" || result[0].ExpandVars != nil || result[0].Children[0].Expand != "" {
		t.Errorf("expand directives carried: Expand=%q ExpandVars=%v child Expand=%q",
			result[0].Expand, result[0].ExpandVars, result[0].Children[0].Expand)
	}

	tmpDir := t.TempDir()
	body := `{"formula": "outer", "type": "expansion", "version": 1,
		"template": [{"id": "{target}.nested", "title": "N", "expand": "does-not-exist", "expand_vars": {"k": "v"}}]}`
	if err := os.WriteFile(filepath.Join(tmpDir, "outer.formula.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := ApplyInlineExpansions([]*Step{{ID: "work", Title: "Work", Expand: "outer"}}, NewParser(tmpDir))
	if err != nil {
		t.Fatalf("ApplyInlineExpansions: %v", err)
	}
	if len(out) != 1 || out[0].ID != "work.nested" || out[0].Expand != "" {
		t.Errorf("ApplyInlineExpansions = %+v, want the single cleared step work.nested", out)
	}
}

// expandStepNotCarried names the exported Step fields an expansion
// deliberately does not carry onto the expanded step, with the reason.
// Every other field must survive; see TestExpandStepFieldGuard.
var expandStepNotCarried = map[string]string{
	"Loop":       "rejected: control flow runs before expansions, so a template loop would never be expanded (TestExpandStepRejectsTemplateLoop)",
	"Expand":     "cleared: nested template expansion directives are disabled (TestExpandStepClearsInlineExpand)",
	"ExpandVars": "cleared together with Expand",
}

// TestExpandStepFieldGuard fails when a Step field is added that the
// expansion drops: every exported field must be set in the fixture below and
// come through an expansion unchanged (no placeholders, so substitution is the
// identity), unless expandStepNotCarried names it (bd-w153uq / #6433).
func TestExpandStepFieldGuard(t *testing.T) {
	prio := 2
	fixture := &Step{
		ID:             "step",
		Title:          "Title",
		Description:    "Description",
		Notes:          "Notes",
		Type:           "bug",
		Priority:       &prio,
		Labels:         []string{"label"},
		Metadata:       map[string]interface{}{"key": "value", "n": int64(1)},
		DependsOn:      []string{"dep"},
		Needs:          []string{"need"},
		WaitsFor:       "all-children",
		Assignee:       "assignee",
		Expand:         "nested-expansion",
		ExpandVars:     map[string]string{"k": "v"},
		Condition:      "{{flag}}",
		Children:       []*Step{{ID: "child", Title: "Child"}},
		Gate:           &Gate{Type: "human", Timeout: "1h"},
		OnComplete:     &OnCompleteSpec{ForEach: "output.items", Bond: "mol", Vars: map[string]string{"k": "{item}"}},
		SourceFormula:  "source",
		SourceLocation: "template[0]",
	}

	stepType := reflect.TypeOf(Step{})
	in := reflect.ValueOf(fixture).Elem()
	for name := range expandStepNotCarried {
		if _, ok := stepType.FieldByName(name); !ok {
			t.Errorf("expandStepNotCarried names %q, which is not a Step field", name)
		}
	}
	for i := 0; i < stepType.NumField(); i++ {
		f := stepType.Field(i)
		// A template loop is an error (TestExpandStepRejectsTemplateLoop),
		// so the fixture cannot carry one.
		if !f.IsExported() || f.Name == "Loop" {
			continue
		}
		if in.Field(i).IsZero() {
			t.Errorf("Step.%s is zero in the guard fixture: set it, and make expandStep carry it or list it in expandStepNotCarried with a reason", f.Name)
		}
	}

	result, err := expandStep(&Step{ID: "target", Title: "Target"}, []*Step{fixture}, nil)
	if err != nil {
		t.Fatalf("expandStep: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d steps, want 1", len(result))
	}
	out := reflect.ValueOf(result[0]).Elem()
	for i := 0; i < stepType.NumField(); i++ {
		f := stepType.Field(i)
		if !f.IsExported() {
			continue
		}
		if _, skip := expandStepNotCarried[f.Name]; skip {
			if !out.Field(i).IsZero() {
				t.Errorf("Step.%s is listed as not carried but survived expansion: %#v", f.Name, out.Field(i).Interface())
			}
			continue
		}
		if !reflect.DeepEqual(out.Field(i).Interface(), in.Field(i).Interface()) {
			t.Errorf("Step.%s did not survive expansion: got %#v, want %#v", f.Name, out.Field(i).Interface(), in.Field(i).Interface())
		}
	}
}
