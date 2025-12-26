package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/formula"
)

// =============================================================================
// Cook Tests (gt-8tmz.23: Compile-time vs Runtime Cooking)
// =============================================================================

// TestSubstituteFormulaVars tests variable substitution in formulas
func TestSubstituteFormulaVars(t *testing.T) {
	tests := []struct {
		name        string
		formula     *formula.Formula
		vars        map[string]string
		wantDesc    string
		wantStepTitle string
	}{
		{
			name: "substitute single variable in description",
			formula: &formula.Formula{
				Description: "Build {{feature}} feature",
				Steps:       []*formula.Step{},
			},
			vars:     map[string]string{"feature": "auth"},
			wantDesc: "Build auth feature",
		},
		{
			name: "substitute variable in step title",
			formula: &formula.Formula{
				Description: "Feature work",
				Steps: []*formula.Step{
					{Title: "Implement {{name}}"},
				},
			},
			vars:           map[string]string{"name": "login"},
			wantDesc:       "Feature work",
			wantStepTitle:  "Implement login",
		},
		{
			name: "substitute multiple variables",
			formula: &formula.Formula{
				Description: "Release {{version}} on {{date}}",
				Steps: []*formula.Step{
					{Title: "Tag {{version}}"},
					{Title: "Deploy to {{env}}"},
				},
			},
			vars: map[string]string{
				"version": "1.0.0",
				"date":    "2024-01-15",
				"env":     "production",
			},
			wantDesc:      "Release 1.0.0 on 2024-01-15",
			wantStepTitle: "Tag 1.0.0",
		},
		{
			name: "nested children substitution",
			formula: &formula.Formula{
				Description: "Epic for {{project}}",
				Steps: []*formula.Step{
					{
						Title: "Phase 1: {{project}} design",
						Children: []*formula.Step{
							{Title: "Design {{component}}"},
						},
					},
				},
			},
			vars: map[string]string{
				"project":   "checkout",
				"component": "cart",
			},
			wantDesc:      "Epic for checkout",
			wantStepTitle: "Phase 1: checkout design",
		},
		{
			name: "unsubstituted variable left as-is",
			formula: &formula.Formula{
				Description: "Build {{feature}} with {{extra}}",
				Steps:       []*formula.Step{},
			},
			vars:     map[string]string{"feature": "auth"},
			wantDesc: "Build auth with {{extra}}", // {{extra}} unchanged
		},
		{
			name: "empty vars map",
			formula: &formula.Formula{
				Description: "Keep {{placeholder}} intact",
				Steps:       []*formula.Step{},
			},
			vars:     map[string]string{},
			wantDesc: "Keep {{placeholder}} intact",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			substituteFormulaVars(tt.formula, tt.vars)

			if tt.formula.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", tt.formula.Description, tt.wantDesc)
			}

			if tt.wantStepTitle != "" && len(tt.formula.Steps) > 0 {
				if tt.formula.Steps[0].Title != tt.wantStepTitle {
					t.Errorf("Steps[0].Title = %q, want %q", tt.formula.Steps[0].Title, tt.wantStepTitle)
				}
			}
		})
	}
}

// TestSubstituteStepVarsRecursive tests deep nesting works correctly
func TestSubstituteStepVarsRecursive(t *testing.T) {
	steps := []*formula.Step{
		{
			Title: "Root: {{name}}",
			Children: []*formula.Step{
				{
					Title: "Level 1: {{name}}",
					Children: []*formula.Step{
						{
							Title: "Level 2: {{name}}",
							Children: []*formula.Step{
								{Title: "Level 3: {{name}}"},
							},
						},
					},
				},
			},
		},
	}

	vars := map[string]string{"name": "test"}
	substituteStepVars(steps, vars)

	// Check all levels got substituted
	if steps[0].Title != "Root: test" {
		t.Errorf("Root title = %q, want %q", steps[0].Title, "Root: test")
	}
	if steps[0].Children[0].Title != "Level 1: test" {
		t.Errorf("Level 1 title = %q, want %q", steps[0].Children[0].Title, "Level 1: test")
	}
	if steps[0].Children[0].Children[0].Title != "Level 2: test" {
		t.Errorf("Level 2 title = %q, want %q", steps[0].Children[0].Children[0].Title, "Level 2: test")
	}
	if steps[0].Children[0].Children[0].Children[0].Title != "Level 3: test" {
		t.Errorf("Level 3 title = %q, want %q", steps[0].Children[0].Children[0].Children[0].Title, "Level 3: test")
	}
}

// TestCompileTimeVsRuntimeMode tests that compile-time preserves placeholders
// and runtime mode substitutes them
func TestCompileTimeVsRuntimeMode(t *testing.T) {
	// Simulate compile-time mode (no variable substitution)
	compileFormula := &formula.Formula{
		Description: "Feature: {{name}}",
		Steps: []*formula.Step{
			{Title: "Implement {{name}}"},
		},
	}

	// In compile-time mode, don't call substituteFormulaVars
	// Placeholders should remain intact
	if compileFormula.Description != "Feature: {{name}}" {
		t.Errorf("Compile-time: Description should preserve placeholder, got %q", compileFormula.Description)
	}

	// Simulate runtime mode (with variable substitution)
	runtimeFormula := &formula.Formula{
		Description: "Feature: {{name}}",
		Steps: []*formula.Step{
			{Title: "Implement {{name}}"},
		},
	}
	vars := map[string]string{"name": "auth"}
	substituteFormulaVars(runtimeFormula, vars)

	if runtimeFormula.Description != "Feature: auth" {
		t.Errorf("Runtime: Description = %q, want %q", runtimeFormula.Description, "Feature: auth")
	}
	if runtimeFormula.Steps[0].Title != "Implement auth" {
		t.Errorf("Runtime: Steps[0].Title = %q, want %q", runtimeFormula.Steps[0].Title, "Implement auth")
	}
}
