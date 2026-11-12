package formula

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// templateWithCarriedFields returns a fresh expansion template step whose
// carried fields are exactly the ones expandStep used to drop (GH#6433):
// Notes, Metadata, WaitsFor, Condition, Gate, Loop and OnComplete. A child
// step exercises the same handling one level down. Expand/ExpandVars are set
// so the test can pin that expanded steps never carry them (an expansion
// template that kept them would re-expand itself through
// ApplyInlineExpansions, which re-scans expanded steps).
func templateWithCarriedFields() *Step {
	return &Step{
		ID:        "{target}.collect",
		Title:     "Collect: {target.title}",
		Needs:     []string{"{target}.survey"},
		Notes:     "Template notes",
		Metadata:  map[string]interface{}{"origin": "expansion", "nested": map[string]interface{}{"leaf": "value"}},
		WaitsFor:  "all-children",
		Condition: "{{deploy}}",
		Gate:      &Gate{Type: "human", ID: "lead"},
		Loop:      &LoopSpec{Count: 2, Body: []*Step{{ID: "{target}.iter", Title: "Iterate"}}},
		OnComplete: &OnCompleteSpec{
			ForEach: "output.items",
			Bond:    "fanout",
			Vars:    map[string]string{"item": "{item}"},
		},
		Expand:     "recursion-here",
		ExpandVars: map[string]string{"seed": "value"},
		Children: []*Step{
			{
				ID:       "{target}.survey",
				Title:    "Survey",
				Notes:    "Child notes",
				WaitsFor: "any-children",
				Gate:     &Gate{Type: "timer", ID: "tick"},
			},
		},
	}
}

// assertCarriedTemplateFields asserts that got carries the template's
// non-substituted fields and clears Expand/ExpandVars. It checks the values
// that carry through identically at every entry point; placeholder
// substitution is covered by TestExpandStepSubstitutesTemplateFields.
func assertCarriedTemplateFields(t *testing.T, got *Step) {
	t.Helper()
	if got.Notes != "Template notes" {
		t.Errorf("expanded step Notes = %q, want the template's %q", got.Notes, "Template notes")
	}
	if got.Metadata == nil || got.Metadata["origin"] != "expansion" {
		t.Errorf("expanded step Metadata = %v, want the template's metadata", got.Metadata)
	} else if nested, ok := got.Metadata["nested"].(map[string]interface{}); !ok || nested["leaf"] != "value" {
		t.Errorf("expanded step Metadata nested map = %v, want a carried nested map", got.Metadata["nested"])
	}
	if got.WaitsFor != "all-children" {
		t.Errorf("expanded step WaitsFor = %q, want %q", got.WaitsFor, "all-children")
	}
	if got.Condition != "{{deploy}}" {
		t.Errorf("expanded step Condition = %q, want %q", got.Condition, "{{deploy}}")
	}
	if got.Gate == nil || got.Gate.Type != "human" || got.Gate.ID != "lead" {
		t.Errorf("expanded step Gate = %+v, want the template's human/lead gate", got.Gate)
	}
	if got.Loop == nil || got.Loop.Count != 2 || len(got.Loop.Body) != 1 || got.Loop.Body[0].Title != "Iterate" {
		t.Errorf("expanded step Loop = %+v, want the template's count-2 loop with its body", got.Loop)
	}
	if got.OnComplete == nil || got.OnComplete.ForEach != "output.items" || got.OnComplete.Bond != "fanout" {
		t.Errorf("expanded step OnComplete = %+v, want the template's on_complete", got.OnComplete)
	} else if got.OnComplete.Vars["item"] != "{item}" {
		t.Errorf("expanded step OnComplete.Vars = %v, want the template's vars", got.OnComplete.Vars)
	}
	if got.Expand != "" {
		t.Errorf("expanded step Expand = %q, want cleared (an expansion template must not re-expand itself)", got.Expand)
	}
	if got.ExpandVars != nil {
		t.Errorf("expanded step ExpandVars = %v, want cleared", got.ExpandVars)
	}
	if len(got.Children) != 1 {
		t.Fatalf("expanded step has %d children, want 1", len(got.Children))
	}
	child := got.Children[0]
	if child.Notes != "Child notes" {
		t.Errorf("expanded child Notes = %q, want %q", child.Notes, "Child notes")
	}
	if child.WaitsFor != "any-children" {
		t.Errorf("expanded child WaitsFor = %q, want %q", child.WaitsFor, "any-children")
	}
	if child.Gate == nil || child.Gate.Type != "timer" {
		t.Errorf("expanded child Gate = %+v, want the template's timer gate", child.Gate)
	}
}

func TestExpandStepPreservesTemplateFields(t *testing.T) {
	writeTemplateFormula := func(t *testing.T) *Parser {
		t.Helper()
		f := &Formula{
			Formula:  "carried-fields",
			Version:  1,
			Type:     TypeExpansion,
			Template: []*Step{templateWithCarriedFields()},
		}
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "carried-fields.formula.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
		return NewParser(dir)
	}

	entries := []struct {
		name string
		run  func(t *testing.T) *Step
	}{
		{"expandStep", func(t *testing.T) *Step {
			target := &Step{ID: "implement", Title: "Implement"}
			res, err := expandStep(target, []*Step{templateWithCarriedFields()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			return res[0]
		}},
		{"compose expand", func(t *testing.T) *Step {
			parser := writeTemplateFormula(t)
			steps := []*Step{{ID: "implement", Title: "Implement"}}
			compose := &ComposeRules{
				Expand: []*ExpandRule{{Target: "implement", With: "carried-fields"}},
			}
			res, err := ApplyExpansions(steps, compose, parser)
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 1 {
				t.Fatalf("ApplyExpansions returned %d steps, want the expanded step alone", len(res))
			}
			return res[0]
		}},
		{"compose map", func(t *testing.T) *Step {
			parser := writeTemplateFormula(t)
			steps := []*Step{{ID: "impl.auth", Title: "Implement auth"}, {ID: "keep", Title: "Keep"}}
			compose := &ComposeRules{
				Map: []*MapRule{{Select: "impl.*", With: "carried-fields"}},
			}
			res, err := ApplyExpansions(steps, compose, parser)
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 2 {
				t.Fatalf("ApplyExpansions returned %d steps, want the expanded step plus keep", len(res))
			}
			return res[0]
		}},
		{"inline expansion", func(t *testing.T) *Step {
			parser := writeTemplateFormula(t)
			steps := []*Step{{ID: "implement", Title: "Implement", Expand: "carried-fields"}}
			res, err := ApplyInlineExpansions(steps, parser)
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 1 {
				t.Fatalf("ApplyInlineExpansions returned %d steps, want the expanded step alone", len(res))
			}
			return res[0]
		}},
		{"standalone materialization", func(t *testing.T) *Step {
			f := &Formula{
				Formula:  "carried-fields",
				Version:  1,
				Type:     TypeExpansion,
				Template: []*Step{templateWithCarriedFields()},
			}
			if err := MaterializeExpansion(f, "main", nil); err != nil {
				t.Fatal(err)
			}
			if len(f.Steps) != 1 {
				t.Fatalf("MaterializeExpansion produced %d steps, want 1", len(f.Steps))
			}
			return f.Steps[0]
		}},
	}

	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			assertCarriedTemplateFields(t, e.run(t))
		})
	}
}

// nonZeroStepForCoverage returns a Step with every exported field populated.
// It fatals when a field of Step is left zero, so adding a Step field forces
// this test (and expandStep) to account for it: that is the regression guard
// for the partial-field-list bug (GH#6433), adapted from PR #6635.
func nonZeroStepForCoverage(t *testing.T) *Step {
	t.Helper()
	n := 3
	s := &Step{
		ID:             "cov",
		Title:          "Coverage title",
		Description:    "Coverage description",
		Notes:          "Coverage notes",
		Type:           "task",
		Priority:       &n,
		Labels:         []string{"cov-label"},
		Metadata:       map[string]interface{}{"key": "value"},
		DependsOn:      []string{"cov-dep"},
		Needs:          []string{"cov-need"},
		WaitsFor:       "all-children",
		Assignee:       "cov-assignee",
		Expand:         "cov-expansion",
		ExpandVars:     map[string]string{"seed": "value"},
		Condition:      "{{cov-flag}}",
		Children:       []*Step{{ID: "cov-child", Title: "Child"}},
		Gate:           &Gate{Type: "human", ID: "cov-gate", AwaitID: "cov-await", Timeout: "1h", Repo: "acme/widgets"},
		Loop:           &LoopSpec{Count: 2, Body: []*Step{{ID: "cov-iter", Title: "Iterate"}}},
		OnComplete:     &OnCompleteSpec{ForEach: "output.items", Bond: "cov-bond", Vars: map[string]string{"x": "{item}"}, Parallel: true},
		SourceFormula:  "cov-source",
		SourceLocation: "steps[0]",
	}
	v := reflect.ValueOf(s).Elem()
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if f.PkgPath != "" {
			continue // unexported: generatedNeeds is covered by the sub-test below
		}
		if v.Field(i).IsZero() {
			t.Fatalf("Step field %s is not populated by nonZeroStepForCoverage; extend this helper so TestExpandStepCoversAllStepFields keeps meaning", f.Name)
		}
	}
	return s
}

// TestExpandStepCoversAllStepFields pins that no Step field is dropped by
// expansion: every field of a fully populated template step survives on the
// expanded step, except Expand/ExpandVars, which must be cleared. A new Step
// field zeroed by expandStep fails here instead of silently reverting to the
// partial-field-list behavior (GH#6433).
func TestExpandStepCoversAllStepFields(t *testing.T) {
	t.Run("every exported field survives", func(t *testing.T) {
		target := &Step{ID: "tgt", Title: "Target"}
		res, err := expandStep(target, []*Step{nonZeroStepForCoverage(t)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := res[0]
		v := reflect.ValueOf(got).Elem()
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if f.PkgPath != "" {
				continue // unexported: generatedNeeds is covered by the sub-test below
			}
			switch f.Name {
			case "Expand", "ExpandVars":
				if !v.Field(i).IsZero() {
					t.Errorf("expanded step %s = %v, want cleared", f.Name, v.Field(i).Interface())
				}
			default:
				if v.Field(i).IsZero() {
					t.Errorf("expanded step %s is zero: expandStep dropped a template field", f.Name)
				}
			}
		}
	})

	// Transform-added provenance must survive expansion too, substituted the
	// same way as Needs so it stays a subset of it: reclassifying a generated
	// edge as formula-authored (or dropping it) changes how
	// FilterStepsByCondition routes the edge around a dropped step.
	t.Run("generatedNeeds carries as a subset of Needs", func(t *testing.T) {
		target := &Step{ID: "tgt", Title: "Target"}
		tmpl := &Step{
			ID:             "{target}.wait",
			Title:          "Wait",
			Needs:          []string{"{target}.dep"},
			generatedNeeds: []string{"{target}.dep"},
		}
		res, err := expandStep(target, []*Step{tmpl}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := res[0]
		if len(got.Needs) != 1 || got.Needs[0] != "tgt.dep" {
			t.Errorf("expanded step Needs = %v, want [tgt.dep]", got.Needs)
		}
		if len(got.generatedNeeds) != 1 || got.generatedNeeds[0] != got.Needs[0] {
			t.Errorf("expanded step generatedNeeds = %v, want [tgt.dep]: carried provenance must stay a subset of Needs", got.generatedNeeds)
		}
	})
}
