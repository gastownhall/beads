package formula

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestEvaluateStepCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		vars      map[string]string
		want      bool
		wantErr   bool
	}{
		// Empty condition - always include
		{
			name:      "empty condition",
			condition: "",
			vars:      nil,
			want:      true,
			wantErr:   false,
		},
		// Truthy checks: {{var}}
		{
			name:      "truthy - non-empty value",
			condition: "{{enabled}}",
			vars:      map[string]string{"enabled": "yes"},
			want:      true,
			wantErr:   false,
		},
		{
			name:      "truthy - empty value",
			condition: "{{enabled}}",
			vars:      map[string]string{"enabled": ""},
			want:      false,
			wantErr:   false,
		},
		{
			name:      "truthy - missing variable",
			condition: "{{enabled}}",
			vars:      map[string]string{},
			want:      false,
			wantErr:   false,
		},
		{
			name:      "truthy - false string",
			condition: "{{enabled}}",
			vars:      map[string]string{"enabled": "false"},
			want:      false,
			wantErr:   false,
		},
		{
			name:      "truthy - FALSE string",
			condition: "{{enabled}}",
			vars:      map[string]string{"enabled": "FALSE"},
			want:      false,
			wantErr:   false,
		},
		{
			name:      "truthy - 0 string",
			condition: "{{enabled}}",
			vars:      map[string]string{"enabled": "0"},
			want:      false,
			wantErr:   false,
		},
		{
			name:      "truthy - no string",
			condition: "{{enabled}}",
			vars:      map[string]string{"enabled": "no"},
			want:      false,
			wantErr:   false,
		},
		{
			name:      "truthy - off string",
			condition: "{{enabled}}",
			vars:      map[string]string{"enabled": "off"},
			want:      false,
			wantErr:   false,
		},
		{
			name:      "truthy - true string",
			condition: "{{enabled}}",
			vars:      map[string]string{"enabled": "true"},
			want:      true,
			wantErr:   false,
		},
		// Negated truthy checks: !{{var}}
		{
			name:      "negated - truthy value becomes false",
			condition: "!{{enabled}}",
			vars:      map[string]string{"enabled": "true"},
			want:      false,
			wantErr:   false,
		},
		{
			name:      "negated - falsy value becomes true",
			condition: "!{{enabled}}",
			vars:      map[string]string{"enabled": "false"},
			want:      true,
			wantErr:   false,
		},
		{
			name:      "negated - empty value becomes true",
			condition: "!{{enabled}}",
			vars:      map[string]string{"enabled": ""},
			want:      true,
			wantErr:   false,
		},
		{
			name:      "negated - missing variable becomes true",
			condition: "!{{enabled}}",
			vars:      map[string]string{},
			want:      true,
			wantErr:   false,
		},
		// Equality checks: {{var}} == value
		{
			name:      "equality - match",
			condition: "{{env}} == staging",
			vars:      map[string]string{"env": "staging"},
			want:      true,
			wantErr:   false,
		},
		{
			name:      "equality - no match",
			condition: "{{env}} == production",
			vars:      map[string]string{"env": "staging"},
			want:      false,
			wantErr:   false,
		},
		{
			name:      "equality - quoted value match",
			condition: "{{env}} == 'staging'",
			vars:      map[string]string{"env": "staging"},
			want:      true,
			wantErr:   false,
		},
		{
			name:      "equality - double quoted value match",
			condition: `{{env}} == "staging"`,
			vars:      map[string]string{"env": "staging"},
			want:      true,
			wantErr:   false,
		},
		// Inequality checks: {{var}} != value
		{
			name:      "inequality - different value",
			condition: "{{env}} != production",
			vars:      map[string]string{"env": "staging"},
			want:      true,
			wantErr:   false,
		},
		{
			name:      "inequality - same value",
			condition: "{{env}} != staging",
			vars:      map[string]string{"env": "staging"},
			want:      false,
			wantErr:   false,
		},
		// Invalid conditions
		{
			name:      "invalid - no variable braces",
			condition: "env == staging",
			vars:      map[string]string{"env": "staging"},
			want:      false,
			wantErr:   true,
		},
		{
			name:      "invalid - random text",
			condition: "something random",
			vars:      map[string]string{},
			want:      false,
			wantErr:   true,
		},
		// Edge cases
		{
			name:      "whitespace in condition",
			condition: "  {{env}}  ==  staging  ",
			vars:      map[string]string{"env": "staging"},
			want:      true,
			wantErr:   false,
		},
		{
			name:      "value with spaces",
			condition: "{{msg}} == 'hello world'",
			vars:      map[string]string{"msg": "hello world"},
			want:      true,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluateStepCondition(tt.condition, tt.vars)
			if (err != nil) != tt.wantErr {
				t.Errorf("EvaluateStepCondition() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("EvaluateStepCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTruthy(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"false", false},
		{"False", false},
		{"FALSE", false},
		{"0", false},
		{"no", false},
		{"No", false},
		{"NO", false},
		{"off", false},
		{"Off", false},
		{"OFF", false},
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"1", true},
		{"yes", true},
		{"on", true},
		{"anything", true},
		{"enabled", true},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := isTruthy(tt.value); got != tt.want {
				t.Errorf("isTruthy(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestFilterStepsByCondition(t *testing.T) {
	tests := []struct {
		name    string
		steps   []*Step
		vars    map[string]string
		wantIDs []string // Expected step IDs in result
		wantErr bool
	}{
		{
			name: "no conditions - all included",
			steps: []*Step{
				{ID: "step1", Title: "Step 1"},
				{ID: "step2", Title: "Step 2"},
			},
			vars:    nil,
			wantIDs: []string{"step1", "step2"},
		},
		{
			name: "truthy condition - included",
			steps: []*Step{
				{ID: "step1", Title: "Step 1", Condition: "{{enabled}}"},
			},
			vars:    map[string]string{"enabled": "true"},
			wantIDs: []string{"step1"},
		},
		{
			name: "truthy condition - excluded",
			steps: []*Step{
				{ID: "step1", Title: "Step 1", Condition: "{{enabled}}"},
			},
			vars:    map[string]string{"enabled": "false"},
			wantIDs: []string{},
		},
		{
			name: "mixed conditions",
			steps: []*Step{
				{ID: "step1", Title: "Step 1"},
				{ID: "step2", Title: "Step 2", Condition: "{{run_tests}}"},
				{ID: "step3", Title: "Step 3", Condition: "{{env}} == production"},
			},
			vars:    map[string]string{"run_tests": "yes", "env": "staging"},
			wantIDs: []string{"step1", "step2"},
		},
		{
			name: "children inherit parent filter",
			steps: []*Step{
				{
					ID:        "parent",
					Title:     "Parent",
					Condition: "{{include_parent}}",
					Children: []*Step{
						{ID: "child1", Title: "Child 1"},
						{ID: "child2", Title: "Child 2"},
					},
				},
			},
			vars:    map[string]string{"include_parent": "false"},
			wantIDs: []string{}, // Parent excluded, children go with it
		},
		{
			name: "child with own condition",
			steps: []*Step{
				{
					ID:    "parent",
					Title: "Parent",
					Children: []*Step{
						{ID: "child1", Title: "Child 1"},
						{ID: "child2", Title: "Child 2", Condition: "{{include_child2}}"},
					},
				},
			},
			vars:    map[string]string{"include_child2": "no"},
			wantIDs: []string{"parent", "child1"},
		},
		{
			name: "equality condition",
			steps: []*Step{
				{ID: "deploy-staging", Title: "Deploy Staging", Condition: "{{env}} == staging"},
				{ID: "deploy-prod", Title: "Deploy Prod", Condition: "{{env}} == production"},
			},
			vars:    map[string]string{"env": "staging"},
			wantIDs: []string{"deploy-staging"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := FilterStepsByCondition(tt.steps, tt.vars)
			if (err != nil) != tt.wantErr {
				t.Errorf("FilterStepsByCondition() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			// Collect all IDs (including children) from result
			gotIDs := collectStepIDsForTest(result)
			if len(gotIDs) != len(tt.wantIDs) {
				t.Errorf("FilterStepsByCondition() got %d steps %v, want %d steps %v",
					len(gotIDs), gotIDs, len(tt.wantIDs), tt.wantIDs)
				return
			}

			for i, wantID := range tt.wantIDs {
				if i >= len(gotIDs) || gotIDs[i] != wantID {
					t.Errorf("FilterStepsByCondition() step[%d] = %v, want %v", i, gotIDs, tt.wantIDs)
					return
				}
			}
		})
	}
}

// collectStepIDsForTest collects all step IDs (including children) in order.
func collectStepIDsForTest(steps []*Step) []string {
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID)
		ids = append(ids, collectStepIDsForTest(s.Children)...)
	}
	return ids
}

// TestFilterStepsByConditionRejectsDroppedReferences: a step the filter keeps
// must not name a step the same pass drops (GH#6437). Validate ran on the
// formula as written, so nothing else catches the dangling reference, and the
// cook would pour the referencing step with no edge.
func TestFilterStepsByConditionRejectsDroppedReferences(t *testing.T) {
	off := map[string]string{"fanout_enabled": "false"}
	tests := []struct {
		name     string
		steps    []*Step
		vars     map[string]string
		wantErrs []string // substrings of the error; nil means no error
		wantIDs  []string // kept step IDs (top level) when no error
	}{
		{
			name: "kept needs to a dropped sibling",
			steps: []*Step{
				{ID: "fanout", Condition: "{{fanout_enabled}}"},
				{ID: "consumer", Needs: []string{"fanout"}},
			},
			vars: off,
			wantErrs: []string{
				`step "consumer": needs references "fanout", but condition "{{fanout_enabled}}" on step "fanout" drops it from this pour`,
				`give "consumer" the same condition`,
			},
		},
		{
			name: "kept depends_on to a dropped sibling",
			steps: []*Step{
				{ID: "fanout", Condition: "{{fanout_enabled}}"},
				{ID: "consumer", DependsOn: []string{"fanout"}},
			},
			vars: off,
			wantErrs: []string{
				`step "consumer": depends_on references "fanout", but condition "{{fanout_enabled}}" on step "fanout" drops it from this pour`,
			},
		},
		{
			name: "kept child needs a step dropped through its parent's condition",
			steps: []*Step{
				{ID: "phase", Condition: "{{fanout_enabled}}", Children: []*Step{{ID: "inner"}}},
				{ID: "outer", Children: []*Step{{ID: "consumer", Needs: []string{"inner"}}}},
			},
			vars: off,
			wantErrs: []string{
				`step "consumer": needs references "inner", but condition "{{fanout_enabled}}" on step "phase" drops it from this pour`,
			},
		},
		{
			name: "inferred waits_for spawner is dropped",
			steps: []*Step{
				{ID: "fanout", Condition: "{{fanout_enabled}}"},
				{ID: "gate", Needs: []string{"fanout"}, WaitsFor: "all-children"},
			},
			vars: off,
			wantErrs: []string{
				`step "gate": waits_for "all-children" waits for the children of "fanout", but condition "{{fanout_enabled}}" on step "fanout" drops it from this pour`,
				`give "gate" the same condition, or name a spawner that is always poured`,
			},
		},
		{
			name: "explicit children-of spawner is dropped while needs[0] is kept",
			steps: []*Step{
				{ID: "start"},
				{ID: "fanout", Condition: "{{fanout_enabled}}"},
				{ID: "gate", Needs: []string{"start"}, WaitsFor: "children-of(fanout)"},
			},
			vars: off,
			wantErrs: []string{
				`step "gate": waits_for "children-of(fanout)" waits for the children of "fanout", but condition "{{fanout_enabled}}" on step "fanout" drops it from this pour`,
			},
		},
		{
			name: "waits_for spawner dropped through its parent's condition",
			steps: []*Step{
				{ID: "phase", Condition: "{{fanout_enabled}}", Children: []*Step{{ID: "fanout"}}},
				{ID: "start"},
				{ID: "gate", Needs: []string{"start"}, WaitsFor: "children-of(fanout)"},
			},
			vars: off,
			wantErrs: []string{
				`step "gate": waits_for "children-of(fanout)" waits for the children of "fanout", but condition "{{fanout_enabled}}" on step "phase" drops it from this pour`,
			},
		},
		{
			name: "gate sharing the spawner's condition is dropped with it",
			steps: []*Step{
				{ID: "fanout", Condition: "{{fanout_enabled}}"},
				{ID: "gate", Needs: []string{"fanout"}, WaitsFor: "all-children", Condition: "{{fanout_enabled}}"},
				{ID: "after"},
			},
			vars:    off,
			wantIDs: []string{"after"},
		},
		{
			name: "kept consumer of a gate dropped with its spawner",
			steps: []*Step{
				{ID: "fanout", Condition: "{{fanout_enabled}}"},
				{ID: "gate", Needs: []string{"fanout"}, WaitsFor: "all-children", Condition: "{{fanout_enabled}}"},
				{ID: "consumer", Needs: []string{"gate"}},
			},
			vars: off,
			wantErrs: []string{
				`step "consumer": needs references "gate", but condition "{{fanout_enabled}}" on step "gate" drops it from this pour (give "consumer" the same condition`,
			},
		},
		{
			name: "two broken references are listed in one error",
			steps: []*Step{
				{ID: "fanout", Condition: "{{fanout_enabled}}"},
				{ID: "a", Needs: []string{"fanout"}},
				{ID: "b", DependsOn: []string{"fanout"}},
			},
			vars: off,
			wantErrs: []string{
				`step "a": needs references "fanout"`,
				`step "b": depends_on references "fanout"`,
			},
		},
		{
			name: "consumer sharing the condition is dropped with it",
			steps: []*Step{
				{ID: "fanout", Condition: "{{fanout_enabled}}"},
				{ID: "consumer", Needs: []string{"fanout"}, Condition: "{{fanout_enabled}}"},
				{ID: "after"},
			},
			vars:    off,
			wantIDs: []string{"after"},
		},
		{
			name: "unreferenced dropped leaf",
			steps: []*Step{
				{ID: "build"},
				{ID: "test", Needs: []string{"build"}},
				{ID: "deploy", Needs: []string{"test"}, Condition: "{{deploy}} == true"},
			},
			vars:    map[string]string{"deploy": "false"},
			wantIDs: []string{"build", "test"},
		},
		{
			name: "all conditions true",
			steps: []*Step{
				{ID: "fanout", Condition: "{{fanout_enabled}}"},
				{ID: "consumer", Needs: []string{"fanout"}, DependsOn: []string{"fanout"}},
			},
			vars:    map[string]string{"fanout_enabled": "true"},
			wantIDs: []string{"fanout", "consumer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := stepTreeSnapshot(tt.steps)
			result, err := FilterStepsByCondition(tt.steps, tt.vars)
			if after := stepTreeSnapshot(tt.steps); after != before {
				t.Errorf("input steps modified:\nbefore %s\nafter  %s", before, after)
			}
			if tt.wantErrs == nil {
				if err != nil {
					t.Fatalf("FilterStepsByCondition() error = %v, want nil", err)
				}
				gotIDs := make([]string, 0, len(result))
				for _, s := range result {
					gotIDs = append(gotIDs, s.ID)
				}
				if !slices.Equal(gotIDs, tt.wantIDs) {
					t.Errorf("FilterStepsByCondition() IDs = %v, want %v", gotIDs, tt.wantIDs)
				}
				return
			}
			if err == nil {
				t.Fatalf("FilterStepsByCondition() = %d steps, nil error; want an error naming %q", len(result), tt.wantErrs)
			}
			if result != nil {
				t.Errorf("FilterStepsByCondition() returned %d steps alongside the error, want nil", len(result))
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("errors.Is(err, ErrValidation) = false for %v", err)
			}
			for _, want := range tt.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// TestFilterStepsByConditionRoutesGeneratedNeeds: an edge that a transform
// added on its own is not a reference the formula wrote, so a step that its
// condition drops does not fail the pour through it. The edge is routed onto
// the dropped step's own needs and depends_on that stay, and advice steps are
// dropped with their target. The steps come from the transforms a cook runs,
// so their edges are marked the way a cook marks them.
func TestFilterStepsByConditionRoutesGeneratedNeeds(t *testing.T) {
	loop := func(body ...*Step) []*Step {
		return []*Step{{ID: "round", Title: "Rounds", Loop: &LoopSpec{Count: 2, Body: body}}}
	}
	branch := &ComposeRules{Branch: []*BranchRule{{From: "prepare", Steps: []string{"unit", "e2e"}, Join: "deploy"}}}
	tests := []struct {
		name      string
		steps     []*Step
		compose   *ComposeRules
		advice    []*AdviceRule
		wantNeeds map[string][]string // kept step ID -> its needs after the filter
		wantGone  []string            // step IDs the filter drops
	}{
		{
			name: "loop chain onto a dropped last body step goes to that step's needs",
			steps: loop(
				&Step{ID: "work", Title: "Work"},
				&Step{ID: "review", Title: "Review", Needs: []string{"work"}, Condition: "{{on}}"},
			),
			wantNeeds: map[string][]string{"round.iter1.work": nil, "round.iter2.work": {"round.iter1.work"}},
			wantGone:  []string{"round.iter1.review", "round.iter2.review"},
		},
		{
			name: "loop chain is routed through consecutive dropped body steps",
			steps: loop(
				&Step{ID: "work", Title: "Work"},
				&Step{ID: "check", Title: "Check", Needs: []string{"work"}, Condition: "{{on}}"},
				&Step{ID: "review", Title: "Review", DependsOn: []string{"check"}, Condition: "{{on}}"},
			),
			wantNeeds: map[string][]string{"round.iter2.work": {"round.iter1.work"}},
			wantGone:  []string{"round.iter1.check", "round.iter1.review"},
		},
		{
			name: "loop chain onto a dropped step that needs nothing is removed",
			steps: loop(
				&Step{ID: "work", Title: "Work"},
				&Step{ID: "review", Title: "Review", Condition: "{{on}}"},
			),
			wantNeeds: map[string][]string{"round.iter2.work": nil},
		},
		{
			name: "loop chain that a bare waits_for gate would take as its spawner is routed too",
			steps: loop(
				&Step{ID: "gate", Title: "Gate", WaitsFor: "all-children"},
				&Step{ID: "work", Title: "Work"},
				&Step{ID: "review", Title: "Review", Needs: []string{"work"}, Condition: "{{on}}"},
			),
			wantNeeds: map[string][]string{"round.iter1.gate": nil, "round.iter2.gate": {"round.iter1.work"}},
			wantGone:  []string{"round.iter1.review", "round.iter2.review"},
		},
		{
			name: "branch join waits for the parallel steps that stay",
			steps: []*Step{
				{ID: "prepare", Title: "Prepare"},
				{ID: "unit", Title: "Unit"},
				{ID: "e2e", Title: "E2E", Condition: "{{on}}"},
				{ID: "deploy", Title: "Deploy"},
			},
			compose:   branch,
			wantNeeds: map[string][]string{"unit": {"prepare"}, "deploy": {"unit", "prepare"}},
			wantGone:  []string{"e2e"},
		},
		{
			name: "branch join with every parallel step dropped waits for from",
			steps: []*Step{
				{ID: "prepare", Title: "Prepare"},
				{ID: "unit", Title: "Unit", Condition: "{{on}}"},
				{ID: "e2e", Title: "E2E", Condition: "{{on}}"},
				{ID: "deploy", Title: "Deploy"},
			},
			compose:   branch,
			wantNeeds: map[string][]string{"deploy": {"prepare"}},
		},
		{
			name: "branch from dropped: the parallel steps wait for what from needed",
			steps: []*Step{
				{ID: "setup", Title: "Setup"},
				{ID: "prepare", Title: "Prepare", Needs: []string{"setup"}, Condition: "{{on}}"},
				{ID: "unit", Title: "Unit"},
				{ID: "e2e", Title: "E2E"},
				{ID: "deploy", Title: "Deploy"},
			},
			compose:   branch,
			wantNeeds: map[string][]string{"unit": {"setup"}, "e2e": {"setup"}, "deploy": {"unit", "e2e"}},
		},
		{
			name: "advice steps are dropped with their target",
			steps: []*Step{
				{ID: "build", Title: "Build"},
				{ID: "deploy", Title: "Deploy", Needs: []string{"build"}, Condition: "{{on}}"},
				{ID: "notify", Title: "Notify", Needs: []string{"build"}},
			},
			advice: []*AdviceRule{{
				Target: "deploy",
				Before: &AdviceStep{ID: "approve-{step.id}"},
				After:  &AdviceStep{ID: "verify-{step.id}"},
			}},
			wantNeeds: map[string][]string{"build": nil, "notify": {"build"}},
			wantGone:  []string{"approve-deploy", "deploy", "verify-deploy"},
		},
		{
			// The parser accepts advice without an id, so the dropped
			// advice step's ID is "", which is also what a step with no
			// waits_for spawner has.
			name: "advice step without an ID is dropped with its target",
			steps: []*Step{
				{ID: "build", Title: "Build"},
				{ID: "deploy", Title: "Deploy", Needs: []string{"build"}, Condition: "{{on}}"},
			},
			advice:    []*AdviceRule{{Target: "deploy", After: &AdviceStep{Title: "Verify"}}},
			wantNeeds: map[string][]string{"build": nil},
			wantGone:  []string{"deploy", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps, err := ApplyControlFlow(tt.steps, tt.compose)
			if err != nil {
				t.Fatalf("ApplyControlFlow() error = %v", err)
			}
			steps = ApplyAdvice(steps, tt.advice)

			before := stepTreeSnapshot(steps)
			result, err := FilterStepsByCondition(steps, map[string]string{"on": "false"})
			if after := stepTreeSnapshot(steps); after != before {
				t.Errorf("input steps modified:\nbefore %s\nafter  %s", before, after)
			}
			if err != nil {
				t.Fatalf("FilterStepsByCondition() error = %v, want nil: only a reference the formula wrote is an error", err)
			}

			kept := buildStepMap(result)
			for id, want := range tt.wantNeeds {
				step, ok := kept[id]
				if !ok {
					t.Errorf("step %q was dropped; kept %v", id, collectStepIDsForTest(result))
					continue
				}
				if !slices.Equal(step.Needs, want) {
					t.Errorf("step %q needs = %v, want %v", id, step.Needs, want)
				}
			}
			for _, id := range tt.wantGone {
				if _, ok := kept[id]; ok {
					t.Errorf("step %q kept, want it dropped", id)
				}
			}
		})
	}
}

// TestFilterStepsByConditionRejectsGateRoutedToNothing: a bare waits_for gate
// first in a loop body takes its spawner from the chain onto the iteration
// before. Routing that edge around a dropped step that needs nothing leaves
// the gate no needs, so the cook would pour it as a gate that waits for no
// one: it is rejected, like a dropped spawner the formula named.
func TestFilterStepsByConditionRejectsGateRoutedToNothing(t *testing.T) {
	steps, err := ApplyControlFlow([]*Step{{ID: "round", Title: "Rounds", Loop: &LoopSpec{Count: 2, Body: []*Step{
		{ID: "gate", Title: "Gate", WaitsFor: "all-children"},
		{ID: "review", Title: "Review", Condition: "{{on}}"},
	}}}}, nil)
	if err != nil {
		t.Fatalf("ApplyControlFlow() error = %v", err)
	}

	result, err := FilterStepsByCondition(steps, map[string]string{"on": "false"})
	if err == nil {
		t.Fatalf("FilterStepsByCondition() kept %v, nil error; want round.iter2.gate rejected", collectStepIDsForTest(result))
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("errors.Is(err, ErrValidation) = false for %v", err)
	}
	if want := `step "round.iter2.gate": waits_for "all-children" takes its spawner from the dependency bd, not the formula, added onto "round.iter1.review", but condition "{{on}}" on step "round.iter1.review" drops it from this pour and leaves the gate nothing to wait for (give "round.iter2.gate" the same condition`; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}

// TestFilterStepsByConditionWrittenNeedStaysWritten: a need the formula wrote
// is the formula's even where a transform would have added the same edge.
// compose.branch wires deploy to e2e, but deploy names e2e itself, so a
// condition that drops e2e still contradicts what the formula says.
func TestFilterStepsByConditionWrittenNeedStaysWritten(t *testing.T) {
	steps, err := ApplyControlFlow([]*Step{
		{ID: "prepare", Title: "Prepare"},
		{ID: "unit", Title: "Unit"},
		{ID: "e2e", Title: "E2E", Condition: "{{on}}"},
		{ID: "deploy", Title: "Deploy", Needs: []string{"e2e"}},
	}, &ComposeRules{Branch: []*BranchRule{{From: "prepare", Steps: []string{"unit", "e2e"}, Join: "deploy"}}})
	if err != nil {
		t.Fatalf("ApplyControlFlow() error = %v", err)
	}

	result, err := FilterStepsByCondition(steps, map[string]string{"on": "false"})
	if err == nil {
		t.Fatalf("FilterStepsByCondition() = %d steps, nil error; want the formula's needs on the dropped e2e rejected", len(result))
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("errors.Is(err, ErrValidation) = false for %v", err)
	}
	if want := `step "deploy": needs references "e2e", but condition "{{on}}" on step "e2e" drops it from this pour`; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}

// stepTreeSnapshot renders the fields FilterStepsByCondition reads, so a test
// can tell whether the input slice was modified.
func stepTreeSnapshot(steps []*Step) string {
	var b strings.Builder
	for _, s := range steps {
		fmt.Fprintf(&b, "{%s cond=%q needs=%v deps=%v waits=%q children=%s}",
			s.ID, s.Condition, s.Needs, s.DependsOn, s.WaitsFor, stepTreeSnapshot(s.Children))
	}
	return b.String()
}
