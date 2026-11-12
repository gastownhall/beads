package formula

import (
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
					Notes:       "{notes_only}",
					// The condition evaluator owns this field, so expansion
					// substitutes nothing in it.
					Condition: "{{condition_only}}",
					Children: []*Step{
						{ID: "{target}.child", Title: "Work on {{feature}}"},
					},
				},
			},
		}

		// {target}, {target.id} and {target.title} are replaced before any
		// variable is, so none of them is a name; {target.description} pulls
		// in the formula's description, whose {release} IS substituted. The
		// single-brace pattern also matches inside {{feature}}, so a
		// --var feature=x is consumed there too.
		got := strings.Join(ExpansionVarRefs(f, "main"), " ")
		if want := "component env feature gate notes_only owner release stage team"; got != want {
			t.Errorf("ExpansionVarRefs() = %q, want %q", got, want)
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

// gatedExpansionJSON is an expansion whose approve step sets every field
// expandStep used to drop (GH#6433), with values that hold no placeholder, so
// they read the same whether or not they are substituted.
const gatedExpansionJSON = `{
	"formula": "gated",
	"type": "expansion",
	"version": 1,
	"template": [
		{"id": "{target}.survey", "title": "Survey"},
		{
			"id": "{target}.approve",
			"title": "Approve",
			"needs": ["{target}.survey"],
			"notes": "Check the release notes",
			"metadata": {"owner": "team-a", "attempts": 3},
			"waits_for": "all-children",
			"condition": "{{run_review}}",
			"gate": {"type": "human", "id": "gid", "await_id": "approval", "timeout": "1h", "repo": "acme/beads"},
			"loop": {"count": 2, "body": [{"id": "body", "title": "Body"}]},
			"on_complete": {"for_each": "output.items", "bond": "mol-item", "vars": {"k": "{item}"}},
			"expand": "never-loaded",
			"expand_vars": {"x": "y"},
			"children": [
				{
					"id": "{target}.approve.child",
					"title": "Child",
					"needs": ["{target}.survey"],
					"notes": "Child notes",
					"condition": "{{child_flag}}",
					"waits_for": "any-children"
				}
			]
		}
	]
}`

// TestExpandStepPreservesTemplateFields checks that every materialization
// path keeps the template fields expandStep used to drop - Notes, Metadata,
// WaitsFor, Condition, Gate, Loop and OnComplete, on the step and on its
// children - and keeps clearing Expand/ExpandVars (GH#6433).
func TestExpandStepPreservesTemplateFields(t *testing.T) {
	newParser := func(t *testing.T) *Parser {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "gated.formula.json"), []byte(gatedExpansionJSON), 0644); err != nil {
			t.Fatal(err)
		}
		return NewParser(dir)
	}

	paths := []struct {
		name   string
		expand func(t *testing.T, p *Parser) []*Step
	}{
		{"compose.expand", func(t *testing.T, p *Parser) []*Step {
			steps, err := ApplyExpansions([]*Step{{ID: "impl", Title: "Impl"}},
				&ComposeRules{Expand: []*ExpandRule{{Target: "impl", With: "gated"}}}, p)
			if err != nil {
				t.Fatalf("ApplyExpansions failed: %v", err)
			}
			return steps
		}},
		{"compose.map", func(t *testing.T, p *Parser) []*Step {
			steps, err := ApplyExpansions([]*Step{{ID: "impl", Title: "Impl"}},
				&ComposeRules{Map: []*MapRule{{Select: "impl", With: "gated"}}}, p)
			if err != nil {
				t.Fatalf("ApplyExpansions failed: %v", err)
			}
			return steps
		}},
		{"inline expand", func(t *testing.T, p *Parser) []*Step {
			steps, err := ApplyInlineExpansions([]*Step{{ID: "impl", Title: "Impl", Expand: "gated"}}, p)
			if err != nil {
				t.Fatalf("ApplyInlineExpansions failed: %v", err)
			}
			return steps
		}},
		{"standalone materialization", func(t *testing.T, p *Parser) []*Step {
			f, err := p.LoadByName("gated")
			if err != nil {
				t.Fatalf("LoadByName failed: %v", err)
			}
			if err := MaterializeExpansion(f, "impl", nil); err != nil {
				t.Fatalf("MaterializeExpansion failed: %v", err)
			}
			return f.Steps
		}},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			byID := buildStepMap(path.expand(t, newParser(t)))
			got := byID["impl.approve"]
			if got == nil {
				t.Fatalf("impl.approve missing; steps: %v", byID)
			}

			if got.Notes != "Check the release notes" {
				t.Errorf("Notes = %q, want the template's notes", got.Notes)
			}
			if got.Metadata == nil {
				t.Errorf("Metadata = nil, want the template's metadata")
			} else if got.Metadata["owner"] != "team-a" || fmt.Sprint(got.Metadata["attempts"]) != "3" {
				t.Errorf("Metadata = %v, want owner=team-a attempts=3", got.Metadata)
			}
			if got.WaitsFor != "all-children" {
				t.Errorf("WaitsFor = %q, want all-children", got.WaitsFor)
			}
			if got.Condition != "{{run_review}}" {
				t.Errorf("Condition = %q, want {{run_review}}", got.Condition)
			}
			wantGate := Gate{Type: "human", ID: "gid", AwaitID: "approval", Timeout: "1h", Repo: "acme/beads"}
			if got.Gate == nil || *got.Gate != wantGate {
				t.Errorf("Gate = %+v, want %+v", got.Gate, wantGate)
			}
			if got.Loop == nil || got.Loop.Count != 2 || len(got.Loop.Body) != 1 || got.Loop.Body[0].Title != "Body" {
				t.Errorf("Loop = %+v, want count 2 over one Body step", got.Loop)
			}
			if oc := got.OnComplete; oc == nil || oc.ForEach != "output.items" || oc.Bond != "mol-item" || oc.Vars["k"] != "{item}" {
				t.Errorf("OnComplete = %+v, want the template's for-each", got.OnComplete)
			}
			if got.Expand != "" || got.ExpandVars != nil {
				t.Errorf("Expand/ExpandVars = %q/%v, want cleared (an expanded step never re-expands)", got.Expand, got.ExpandVars)
			}

			child := byID["impl.approve.child"]
			if child == nil {
				t.Fatalf("impl.approve.child missing")
			}
			if child.Notes != "Child notes" || child.Condition != "{{child_flag}}" || child.WaitsFor != "any-children" {
				t.Errorf("child Notes/Condition/WaitsFor = %q/%q/%q, want the template's", child.Notes, child.Condition, child.WaitsFor)
			}
		})
	}
}

// TestExpandStepCoversAllStepFields fills every exported Step field and fails
// when the expanded step comes back with one of them zero, so a Step field
// added later is either carried through expansion or deliberately listed here.
// Adapted from PR #6635's guard.
func TestExpandStepCoversAllStepFields(t *testing.T) {
	// An expanded step must never re-expand (see expandStepWith).
	allowZero := map[string]bool{"Expand": true, "ExpandVars": true}

	prio := 2
	tmpl := &Step{}
	v := reflect.ValueOf(tmpl).Elem()
	for i := 0; i < v.NumField(); i++ {
		field, f := v.Type().Field(i), v.Field(i)
		if !field.IsExported() {
			continue // generatedNeeds: TestExpandStepKeepsGeneratedNeeds
		}
		switch f.Type() {
		case reflect.TypeOf(""):
			f.SetString("x")
		case reflect.TypeOf(&prio):
			f.Set(reflect.ValueOf(&prio))
		case reflect.TypeOf([]string{}):
			f.Set(reflect.ValueOf([]string{"x"}))
		case reflect.TypeOf(map[string]string{}):
			f.Set(reflect.ValueOf(map[string]string{"k": "v"}))
		case reflect.TypeOf(map[string]interface{}{}):
			f.Set(reflect.ValueOf(map[string]interface{}{"k": "v"}))
		case reflect.TypeOf([]*Step{}):
			f.Set(reflect.ValueOf([]*Step{{ID: "child", Title: "Child"}}))
		case reflect.TypeOf(&Gate{}):
			f.Set(reflect.ValueOf(&Gate{Type: "timer"}))
		case reflect.TypeOf(&LoopSpec{}):
			f.Set(reflect.ValueOf(&LoopSpec{Count: 1, Body: []*Step{{ID: "b"}}}))
		case reflect.TypeOf(&OnCompleteSpec{}):
			f.Set(reflect.ValueOf(&OnCompleteSpec{Bond: "b"}))
		default:
			t.Fatalf("Step.%s has unhandled type %s; extend this test", field.Name, f.Type())
		}
	}

	result, err := expandStep(&Step{ID: "t"}, []*Step{tmpl}, nil)
	if err != nil {
		t.Fatalf("expandStep failed: %v", err)
	}
	got := reflect.ValueOf(result[0]).Elem()
	for i := 0; i < got.NumField(); i++ {
		field := got.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		zero := got.Field(i).IsZero()
		if allowZero[field.Name] && !zero {
			t.Errorf("expandStep carried Step.%s; want it cleared", field.Name)
		}
		if !allowZero[field.Name] && zero {
			t.Errorf("expandStep dropped Step.%s: zero in the expanded step although set in the template", field.Name)
		}
	}
}

// TestExpandStepKeepsGeneratedNeeds checks that a template need a transform
// added stays generated after expansion, with the same substitution as Needs.
func TestExpandStepKeepsGeneratedNeeds(t *testing.T) {
	tmpl := &Step{ID: "{target}.b", Title: "B", Needs: []string{"{target}.a", "{target}.{stage}"}}
	tmpl.generatedNeeds = []string{"{target}.{stage}"}

	result, err := expandStep(&Step{ID: "impl"}, []*Step{tmpl}, map[string]string{"stage": "prep"})
	if err != nil {
		t.Fatalf("expandStep failed: %v", err)
	}
	got := result[0]
	if want := []string{"impl.a", "impl.prep"}; !slices.Equal(got.Needs, want) {
		t.Errorf("Needs = %v, want %v", got.Needs, want)
	}
	if want := []string{"impl.prep"}; !slices.Equal(got.generatedNeeds, want) {
		t.Errorf("generatedNeeds = %v, want %v", got.generatedNeeds, want)
	}
	if !isGeneratedNeed(got, "impl.prep") || isGeneratedNeed(got, "impl.a") {
		t.Errorf("generated provenance moved: generatedNeeds = %v", got.generatedNeeds)
	}
	if want := []string{"{target}.{stage}"}; !slices.Equal(tmpl.generatedNeeds, want) {
		t.Errorf("template generatedNeeds = %v, want it untouched", tmpl.generatedNeeds)
	}
}

// TestExpandStepSubstitutesTemplateFields checks that Notes, WaitsFor, the
// Gate strings and metadata string leaves get the same {target}/{var}
// substitution as Title, while metadata keys and scalars and the Condition,
// Loop and OnComplete expressions - owned by later phases - are kept as written
// (GH#6433).
func TestExpandStepSubstitutesTemplateFields(t *testing.T) {
	target := &Step{ID: "impl", Title: "Implement", Description: "Do it"}
	vars := map[string]string{
		"env": "prod", "owner": "alice", "gate_type": "human", "timeout": "1h", "org": "acme",
		// Names the later phases own; expansion must not consume them.
		"deploy": "yes", "n": "3", "item": "X", "i": "7",
	}

	cyclic := map[string]interface{}{"name": "{env}"}
	cyclic["self"] = cyclic

	// deep nests 40 maps, each with a {env} leaf; the walk stops at the
	// metadata depth bound and keeps the rest as written.
	deep := map[string]interface{}{"v": "{env}"}
	level := deep
	for i := 1; i < 40; i++ {
		next := map[string]interface{}{"v": "{env}"}
		level["next"] = next
		level = next
	}

	tmpl := &Step{
		ID:       "{target}.approve",
		Title:    "Approve",
		Notes:    "Notes for {target} ({target.title}: {target.description}) in {env}, {{keep}}",
		WaitsFor: "children-of({target.id}.survey-{env})",
		Gate: &Gate{
			Type:    "{gate_type}",
			ID:      "{target}-gate",
			AwaitID: "await-{target.id}-{env}",
			Timeout: "{timeout}",
			Repo:    "{org}/{target}",
		},
		Metadata: map[string]interface{}{
			"owner":      "{owner}",
			"{env}":      "keys are never rewritten",
			"count":      3,
			"ratio":      1.5,
			"flag":       true,
			"none":       nil,
			"nested":     map[string]interface{}{"desc": "{target.description}", "deeper": map[string]interface{}{"env": "{env}"}},
			"list":       []interface{}{"{env}", 7, map[string]interface{}{"k": "{owner}"}, []interface{}{"{target}"}},
			"tables":     []map[string]interface{}{{"name": "{env}"}, {"n": 2}},
			"empty_map":  map[string]interface{}{},
			"empty_list": []interface{}{},
			"nil_map":    map[string]interface{}(nil),
			"nil_list":   []interface{}(nil),
			"cyclic":     cyclic,
			"deep":       deep,
		},
		Condition:  "{{deploy}}",
		Loop:       &LoopSpec{Range: "1..{n}", Var: "i", Body: []*Step{{ID: "body-{i}", Title: "Body {i} in {env}"}}},
		OnComplete: &OnCompleteSpec{ForEach: "output.items", Bond: "mol-{item}", Vars: map[string]string{"k": "{item}"}},
	}
	inferred := &Step{ID: "{target}.collect", Title: "Collect", WaitsFor: "all-children", Needs: []string{"{target}.survey-{env}"}}

	result, err := expandStep(target, []*Step{tmpl, inferred}, vars)
	if err != nil {
		t.Fatalf("expandStep failed: %v", err)
	}
	got, collect := result[0], result[1]

	if want := "Notes for impl (Implement: Do it) in prod, {{keep}}"; got.Notes != want {
		t.Errorf("Notes = %q, want %q", got.Notes, want)
	}
	if want := "children-of(impl.survey-prod)"; got.WaitsFor != want {
		t.Errorf("WaitsFor = %q, want %q", got.WaitsFor, want)
	}
	if collect.WaitsFor != "all-children" || !slices.Equal(collect.Needs, []string{"impl.survey-prod"}) {
		t.Errorf("inferred waits_for = %q needs %v, want all-children over [impl.survey-prod]", collect.WaitsFor, collect.Needs)
	}
	wantGate := Gate{Type: "human", ID: "impl-gate", AwaitID: "await-impl-prod", Timeout: "1h", Repo: "acme/impl"}
	if got.Gate == nil || *got.Gate != wantGate {
		t.Errorf("Gate = %+v, want %+v", got.Gate, wantGate)
	}

	if got.Condition != "{{deploy}}" {
		t.Errorf("Condition = %q, want {{deploy}} unchanged", got.Condition)
	}
	if l := got.Loop; l == nil || l.Range != "1..{n}" || l.Var != "i" || len(l.Body) != 1 ||
		l.Body[0].ID != "body-{i}" || l.Body[0].Title != "Body {i} in {env}" {
		t.Errorf("Loop = %+v, want range/var/body unchanged", got.Loop)
	}
	if oc := got.OnComplete; oc == nil || oc.Bond != "mol-{item}" || oc.Vars["k"] != "{item}" {
		t.Errorf("OnComplete = %+v, want bond/vars unchanged", got.OnComplete)
	}

	md := got.Metadata
	if md == nil {
		t.Fatalf("Metadata = nil, want the template's metadata substituted")
	}
	wantMD := map[string]interface{}{
		"owner":      "alice",
		"{env}":      "keys are never rewritten",
		"count":      3,
		"ratio":      1.5,
		"flag":       true,
		"none":       nil,
		"nested":     map[string]interface{}{"desc": "Do it", "deeper": map[string]interface{}{"env": "prod"}},
		"list":       []interface{}{"prod", 7, map[string]interface{}{"k": "alice"}, []interface{}{"impl"}},
		"tables":     []map[string]interface{}{{"name": "prod"}, {"n": 2}},
		"empty_map":  map[string]interface{}{},
		"empty_list": []interface{}{},
		"nil_map":    map[string]interface{}(nil),
		"nil_list":   []interface{}(nil),
	}
	for k, want := range wantMD {
		if v, ok := md[k]; !ok || !reflect.DeepEqual(v, want) {
			t.Errorf("Metadata[%q] = %#v, want %#v", k, v, want)
		}
	}

	// The cyclic map is never printed: fmt does not detect cycles.
	if c, ok := md["cyclic"].(map[string]interface{}); !ok || c["name"] != "prod" {
		t.Errorf("Metadata[cyclic][name] = %v, want prod", c["name"])
	}

	// Leaves above the bound are substituted; the tail past it is kept as
	// written (it may share storage with the template).
	first, last := 0, ""
	for lvl, i := md["deep"], 0; lvl != nil; i++ {
		m, ok := lvl.(map[string]interface{})
		if !ok {
			t.Fatalf("Metadata[deep] level %d = %#v, want a map", i, lvl)
		}
		if m["v"] == "prod" {
			first = i + 1
		}
		last = fmt.Sprint(m["v"])
		lvl = m["next"]
		if i > 40 {
			t.Fatalf("Metadata[deep] runs past its 40 levels")
		}
	}
	if first < 30 || last != "{env}" {
		t.Errorf("Metadata[deep]: %d levels substituted, deepest leaf %q; want the first 30+ substituted and the tail past the bound unchanged", first, last)
	}

	// The template is left as written.
	if tmpl.Notes != "Notes for {target} ({target.title}: {target.description}) in {env}, {{keep}}" ||
		tmpl.Gate.AwaitID != "await-{target.id}-{env}" || tmpl.Metadata["owner"] != "{owner}" ||
		tmpl.Metadata["tables"].([]map[string]interface{})[0]["name"] != "{env}" {
		t.Errorf("expanding rewrote the template's notes, gate or metadata")
	}
}
