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

// TestExpandStepSubstitutesTemplateFields covers the expansion-time
// substitution of the fields the carry fix (GH#6433) began moving into
// expanded output: Notes, metadata string leaves at any nesting depth, the
// five gate strings, and an explicit waits_for spawner reference. Condition,
// Loop and OnComplete keep their values untouched, including placeholders
// whose names collide with expansion vars: those bind in their own phases.
func TestExpandStepSubstitutesTemplateFields(t *testing.T) {
	target := &Step{ID: "deploy", Title: "Deploy service", Description: "Ship the service"}
	vars := map[string]string{
		"env":     "prod",
		"owner":   "alice",
		"release": "v2",
		"deploy":  "yes",  // collides with Condition's {{deploy}}: must not touch it
		"n":       "5",    // collides with Loop.Range: must not touch it
		"item":    "unit", // collides with OnComplete's {item}: must not touch it
	}

	newTemplate := func() *Step {
		return &Step{
			ID:    "{target}.approval",
			Title: "Approve {env} for {target.title}",
			Notes: "Ask {owner} to approve {target.description} in {env} (keep {{keep}} as-is)",
			Metadata: map[string]interface{}{
				"origin": "{target.id}",
				"nested": map[string]interface{}{
					"release": "release-1-{release}",
					"count":   3,
					"on":      true,
					"nothing": nil,
					"keep":    "{{keep}}",
				},
				"list": []interface{}{
					"{env}",
					42,
					map[string]interface{}{"shard": "{target.title}"},
					nil,
				},
				// TOML array-of-tables decodes to []map[string]interface{}.
				"tables": []map[string]interface{}{
					{"region": "{env}"},
				},
			},
			WaitsFor: "children-of({target}.survey)",
			Needs:    []string{"{target}.survey"},
			Gate: &Gate{
				Type:    "human",
				ID:      "approver-{owner}",
				AwaitID: "await-{env}",
				Timeout: "1h-{release}",
				Repo:    "acme/{target}",
			},
			Condition:  "{{deploy}}",
			Loop:       &LoopSpec{Range: "1..{n}"},
			OnComplete: &OnCompleteSpec{Vars: map[string]string{"note": "{item}"}},
		}
	}

	res, err := expandStep(target, []*Step{newTemplate()}, vars)
	if err != nil {
		t.Fatal(err)
	}
	got := res[0]

	t.Run("notes", func(t *testing.T) {
		if want := "Ask alice to approve Ship the service in prod (keep {{keep}} as-is)"; got.Notes != want {
			t.Errorf("expanded step Notes = %q, want %q", got.Notes, want)
		}
	})

	t.Run("metadata string leaves", func(t *testing.T) {
		if got.Metadata == nil {
			t.Fatal("expanded step Metadata = nil, want substituted copy")
		}
		if got.Metadata["origin"] != "deploy" {
			t.Errorf("Metadata[origin] = %v, want %q ({target.id} resolved)", got.Metadata["origin"], "deploy")
		}
		nested, ok := got.Metadata["nested"].(map[string]interface{})
		if !ok {
			t.Fatalf("Metadata[nested] = %T, want a map", got.Metadata["nested"])
		}
		if nested["release"] != "release-1-v2" {
			t.Errorf("Metadata[nested][release] = %v, want %q", nested["release"], "release-1-v2")
		}
		// Keys and non-string scalars are never rewritten.
		if nested["count"] != 3 || nested["on"] != true || nested["nothing"] != nil {
			t.Errorf("Metadata[nested] non-string leaves = %v/%v/%v, want 3/true/<nil> unchanged", nested["count"], nested["on"], nested["nothing"])
		}
		if nested["keep"] != "{{keep}}" {
			t.Errorf("Metadata[nested][keep] = %v, want {{keep}}: an unresolved name stays literal", nested["keep"])
		}

		list, ok := got.Metadata["list"].([]interface{})
		if !ok || len(list) != 4 {
			t.Fatalf("Metadata[list] = %T, want a 4-element array", got.Metadata["list"])
		}
		if list[0] != "prod" || list[1] != 42 || list[3] != nil {
			t.Errorf("Metadata[list] scalars = %v/%v/%v, want prod/42/<nil>", list[0], list[1], list[3])
		}
		if shard, ok := list[2].(map[string]interface{}); !ok || shard["shard"] != "Deploy service" {
			t.Errorf("Metadata[list][2] = %v, want a map with shard %q", list[2], "Deploy service")
		}

		tables, ok := got.Metadata["tables"].([]map[string]interface{})
		if !ok || len(tables) != 1 {
			t.Fatalf("Metadata[tables] = %T, want a 1-element array of tables", got.Metadata["tables"])
		}
		if tables[0]["region"] != "prod" {
			t.Errorf("Metadata[tables][0][region] = %v, want %q", tables[0]["region"], "prod")
		}
	})

	t.Run("waits_for", func(t *testing.T) {
		if want := "children-of(deploy.survey)"; got.WaitsFor != want {
			t.Errorf("expanded step WaitsFor = %q, want %q (spawner reference resolved)", got.WaitsFor, want)
		}
	})

	t.Run("gate strings", func(t *testing.T) {
		if got.Gate == nil {
			t.Fatal("expanded step Gate = nil, want substituted copy")
		}
		want := &Gate{Type: "human", ID: "approver-alice", AwaitID: "await-prod", Timeout: "1h-v2", Repo: "acme/deploy"}
		if *got.Gate != *want {
			t.Errorf("expanded step Gate = %+v, want %+v", got.Gate, want)
		}
	})

	t.Run("condition loop and on_complete keep their placeholders", func(t *testing.T) {
		if got.Condition != "{{deploy}}" {
			t.Errorf("expanded step Condition = %q, want %q (the condition evaluator owns that syntax)", got.Condition, "{{deploy}}")
		}
		if got.Loop == nil || got.Loop.Range != "1..{n}" {
			t.Errorf("expanded step Loop.Range = %+v, want %q (bound at cook time)", got.Loop, "1..{n}")
		}
		if got.OnComplete == nil || got.OnComplete.Vars["note"] != "{item}" {
			t.Errorf("expanded step OnComplete.Vars = %v, want {item} bound at runtime", got.OnComplete.Vars)
		}
	})

	// A bare waits_for gate names no spawner, so nothing in it resolves
	// against the target; it must come through unchanged.
	t.Run("inferred waits_for form unchanged", func(t *testing.T) {
		tmpl := &Step{ID: "{target}.fan", Title: "Fan", Needs: []string{"{target}.survey"}, WaitsFor: "all-children"}
		res, err := expandStep(target, []*Step{tmpl}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res[0].WaitsFor != "all-children" {
			t.Errorf("WaitsFor = %q, want %q", res[0].WaitsFor, "all-children")
		}
	})

	// Absent metadata stays nil; nil and empty containers keep their shape.
	t.Run("metadata container shapes", func(t *testing.T) {
		var nilMap map[string]interface{}
		var nilList []interface{}
		tmpl := &Step{
			ID:    "{target}.shapes",
			Title: "Shapes",
			Metadata: map[string]interface{}{
				"nilmap":   nilMap,
				"nillist":  nilList,
				"emptymap": map[string]interface{}{},
				"emptyarr": []interface{}{},
			},
		}
		res, err := expandStep(target, []*Step{tmpl}, nil)
		if err != nil {
			t.Fatal(err)
		}
		meta := res[0].Metadata
		if m, ok := meta["nilmap"].(map[string]interface{}); !ok || m != nil {
			t.Errorf("nil map = %v (%T), want a preserved nil map", meta["nilmap"], meta["nilmap"])
		}
		if l, ok := meta["nillist"].([]interface{}); !ok || l != nil {
			t.Errorf("nil array = %v (%T), want a preserved nil array", meta["nillist"], meta["nillist"])
		}
		if m, ok := meta["emptymap"].(map[string]interface{}); !ok || m == nil || len(m) != 0 {
			t.Errorf("empty map = %v (%T), want a fresh non-nil empty map", meta["emptymap"], meta["emptymap"])
		}
		if l, ok := meta["emptyarr"].([]interface{}); !ok || l == nil || len(l) != 0 {
			t.Errorf("empty array = %v (%T), want a fresh non-nil empty array", meta["emptyarr"], meta["emptyarr"])
		}

		// A step without metadata keeps Metadata nil.
		res, err = expandStep(target, []*Step{{ID: "{target}.bare", Title: "Bare"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Metadata != nil {
			t.Errorf("Metadata = %v, want nil for a template without metadata", res[0].Metadata)
		}
	})

	// At the depth bound the remaining value is returned unchanged: a leaf
	// nested deeper than the bound ships with its placeholders still in it.
	t.Run("depth-bound tail unchanged", func(t *testing.T) {
		deep := map[string]interface{}{"leaf": "deep-{env}"}
		for range 50 {
			deep = map[string]interface{}{"d": deep}
		}
		tmpl := &Step{
			ID:       "{target}.deep",
			Title:    "Deep",
			Metadata: map[string]interface{}{"shallow": "shallow-{env}", "deep": deep},
		}
		res, err := expandStep(target, []*Step{tmpl}, vars)
		if err != nil {
			t.Fatal(err)
		}
		meta := res[0].Metadata
		if meta["shallow"] != "shallow-prod" {
			t.Errorf("shallow leaf = %v, want %q", meta["shallow"], "shallow-prod")
		}
		node, ok := meta["deep"].(map[string]interface{})
		if !ok {
			t.Fatalf("deep chain = %T, want a map", meta["deep"])
		}
		for range 50 {
			next, ok := node["d"].(map[string]interface{})
			if !ok {
				t.Fatalf("deep chain broke before the leaf at %v", node)
			}
			node = next
		}
		if node["leaf"] != "deep-{env}" {
			t.Errorf("deep leaf = %v, want %q: the tail past the bound is untouched", node["leaf"], "deep-{env}")
		}
	})

	// A metadata map that contains itself must not hang the expansion; the
	// depth bound stops the recursion, and the tail may share storage.
	t.Run("cyclic metadata terminates", func(t *testing.T) {
		cycle := map[string]interface{}{}
		cycle["self"] = cycle
		tmpl := &Step{
			ID:       "{target}.cycle",
			Title:    "Cycle",
			Metadata: map[string]interface{}{"cycle": cycle},
		}
		res, err := expandStep(target, []*Step{tmpl}, vars)
		if err != nil {
			t.Fatal(err)
		}
		outer, ok := res[0].Metadata["cycle"].(map[string]interface{})
		if !ok || len(outer) != 1 {
			t.Fatalf("expanded cyclic metadata = %T, want a single-key map", res[0].Metadata["cycle"])
		}
		if _, ok := outer["self"].(map[string]interface{}); !ok {
			t.Errorf("expanded cyclic metadata[self] = %T, want a map", outer["self"])
		}
	})
}

// TestApplyExpansionsMapDoesNotShareTemplateState pins that expanded steps
// never alias the template's mutable state (GH#6433): two expansions of one
// template — directly and through a compose map rule that expands two
// targets from the same loaded template — must not observe each other's or
// the template's mutations of metadata containers, gate fields, loop
// count/body or OnComplete vars. A maps.Clone-style shallow metadata copy
// fails the nested-container mutations; an aliased gate or loop fails the
// rest. Priority's shared pointer is longstanding behavior and not asserted.
func TestApplyExpansionsMapDoesNotShareTemplateState(t *testing.T) {
	// firstTable returns the first element of a tables array in either shape
	// it can arrive in: []map[string]interface{} straight from a Go template
	// or []interface{} after a JSON round-trip (a compose-map leg parses the
	// template from a formula file).
	firstTable := func(t *testing.T, v interface{}) map[string]interface{} {
		t.Helper()
		switch val := v.(type) {
		case []map[string]interface{}:
			return val[0]
		case []interface{}:
			m, ok := val[0].(map[string]interface{})
			if !ok {
				t.Fatalf("tables[0] = %T, want a map", val[0])
			}
			return m
		default:
			t.Fatalf("tables = %T, want an array of tables", v)
			return nil
		}
	}
	mutateFirst := func(t *testing.T, first *Step) {
		t.Helper()
		first.Metadata["added"] = "changed"
		first.Metadata["nested"].(map[string]interface{})["leaf"] = "changed"
		first.Metadata["list"].([]interface{})[0].(map[string]interface{})["shard"] = "changed"
		firstTable(t, first.Metadata["tables"])["region"] = "changed"
		first.Gate.Type = "timer"
		first.Gate.ID = "changed-gate"
		first.Loop.Count = 99
		first.Loop.Body[0].Title = "Changed"
		first.OnComplete.Vars["note"] = "changed"
	}
	assertOriginal := func(t *testing.T, label string, got *Step) {
		t.Helper()
		if got.Metadata["nested"].(map[string]interface{})["leaf"] != "orig" {
			t.Errorf("%s nested metadata leaf = %v, want orig: expansions share state", label, got.Metadata["nested"])
		}
		if got.Metadata["list"].([]interface{})[0].(map[string]interface{})["shard"] != "orig" {
			t.Errorf("%s array metadata shard = %v, want orig: expansions share state", label, got.Metadata["list"])
		}
		if firstTable(t, got.Metadata["tables"])["region"] != "orig" {
			t.Errorf("%s table metadata region = %v, want orig: expansions share state", label, got.Metadata["tables"])
		}
		if _, leaked := got.Metadata["added"]; leaked {
			t.Errorf("%s metadata gained the sibling expansion's key: expansions share state", label)
		}
		if got.Gate.Type != "human" || got.Gate.ID != "orig-gate" {
			t.Errorf("%s Gate = %+v, want the original human/orig-gate: expansions share state", label, got.Gate)
		}
		if got.Loop.Count != 2 || got.Loop.Body[0].Title != "Original" {
			t.Errorf("%s Loop = %+v, want count 2 and original body title: expansions share state", label, got.Loop)
		}
		if got.OnComplete.Vars["note"] != "orig" {
			t.Errorf("%s OnComplete.Vars = %v, want orig: expansions share state", label, got.OnComplete.Vars)
		}
	}
	newTemplate := func() *Step {
		return &Step{
			ID:    "{target}.work",
			Title: "Work on {target.title}",
			Metadata: map[string]interface{}{
				"nested": map[string]interface{}{"leaf": "orig"},
				"list":   []interface{}{map[string]interface{}{"shard": "orig"}},
				"tables": []map[string]interface{}{{"region": "orig"}},
			},
			Gate:       &Gate{Type: "human", ID: "orig-gate"},
			Loop:       &LoopSpec{Count: 2, Body: []*Step{{ID: "{target}.iter", Title: "Original"}}},
			OnComplete: &OnCompleteSpec{Vars: map[string]string{"note": "orig"}},
		}
	}

	t.Run("direct expansion of one template", func(t *testing.T) {
		tmpl := newTemplate()
		target := &Step{ID: "tgt", Title: "Target"}
		res1, err := expandStep(target, []*Step{tmpl}, nil)
		if err != nil {
			t.Fatal(err)
		}
		res2, err := expandStep(target, []*Step{tmpl}, nil)
		if err != nil {
			t.Fatal(err)
		}
		mutateFirst(t, res1[0])
		assertOriginal(t, "template", tmpl)
		assertOriginal(t, "second expansion", res2[0])
	})

	t.Run("compose map over two targets", func(t *testing.T) {
		f := &Formula{
			Formula:  "isolation-template",
			Version:  1,
			Type:     TypeExpansion,
			Template: []*Step{newTemplate()},
		}
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "isolation-template.formula.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
		steps := []*Step{
			{ID: "impl.alpha", Title: "Alpha"},
			{ID: "impl.beta", Title: "Beta"},
		}
		compose := &ComposeRules{
			Map: []*MapRule{{Select: "impl.*", With: "isolation-template"}},
		}
		res, err := ApplyExpansions(steps, compose, NewParser(dir))
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 2 {
			t.Fatalf("ApplyExpansions returned %d steps, want the two expanded steps", len(res))
		}
		mutateFirst(t, res[0])
		assertOriginal(t, "second target's expansion", res[1])
	})
}
