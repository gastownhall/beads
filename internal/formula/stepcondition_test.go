package formula

import (
	"errors"
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

// TestFilterStepsByConditionRejectsDroppedReferences pins the contract the
// pour path depends on: the filter is the only pass that sees the step set
// after conditions have run, so it is the one that must refuse to keep a
// step that still names a step it dropped. Formula.Validate ran inside
// parser.Resolve, against the formula as written, so it cannot see this.
func TestFilterStepsByConditionRejectsDroppedReferences(t *testing.T) {
	t.Run("kept needs to a dropped sibling errors", func(t *testing.T) {
		steps := []*Step{
			{ID: "consumer", Title: "Consumer", Needs: []string{"fanout"}},
			{ID: "fanout", Title: "Fanout", Condition: "{{fanout_enabled}}"},
		}
		result, err := FilterStepsByCondition(steps, map[string]string{"fanout_enabled": "false"})
		if err == nil {
			t.Fatalf("filter kept a needs reference to a dropped step; result IDs: %v", collectStepIDsForTest(result))
		}
		if result != nil {
			t.Errorf("filter returned steps alongside the error: %v", collectStepIDsForTest(result))
		}
		if !errors.Is(err, ErrValidation) {
			t.Errorf("error %v does not wrap ErrValidation", err)
		}
		for _, want := range []string{"consumer", "needs", "fanout", "{{fanout_enabled}}"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("kept depends_on to a dropped sibling errors", func(t *testing.T) {
		steps := []*Step{
			{ID: "consumer", Title: "Consumer", DependsOn: []string{"fanout"}},
			{ID: "fanout", Title: "Fanout", Condition: "{{fanout_enabled}}"},
		}
		_, err := FilterStepsByCondition(steps, map[string]string{"fanout_enabled": "false"})
		if err == nil {
			t.Fatal("filter kept a depends_on reference to a dropped step")
		}
		for _, want := range []string{"consumer", "depends_on", "fanout", "{{fanout_enabled}}"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("target dropped through its parent's condition names the parent", func(t *testing.T) {
		steps := []*Step{
			{ID: "consumer", Title: "Consumer", Needs: []string{"worker"}},
			{ID: "parent", Title: "Parent", Condition: "{{enable}}", Children: []*Step{
				{ID: "worker", Title: "Worker"},
			}},
		}
		_, err := FilterStepsByCondition(steps, map[string]string{"enable": "false"})
		if err == nil {
			t.Fatal("filter kept a needs reference to a child dropped with its parent")
		}
		for _, want := range []string{"consumer", "worker", "parent", "{{enable}}"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("two broken references are both listed", func(t *testing.T) {
		steps := []*Step{
			{ID: "consumer", Title: "Consumer", Needs: []string{"alpha", "beta"}},
			{ID: "alpha", Title: "Alpha", Condition: "{{a}}"},
			{ID: "beta", Title: "Beta", Condition: "{{b}}"},
		}
		_, err := FilterStepsByCondition(steps, map[string]string{"a": "false", "b": "false"})
		if err == nil {
			t.Fatal("filter kept needs references to two dropped steps")
		}
		if got := strings.Count(err.Error(), "drops it from this pour"); got != 2 {
			t.Errorf("error lists %d broken references, want 2: %v", got, err)
		}
		if !strings.Contains(err.Error(), `"alpha"`) || !strings.Contains(err.Error(), `"beta"`) {
			t.Errorf("error does not name both dropped targets: %v", err)
		}
	})

	t.Run("all conditions true keeps every step", func(t *testing.T) {
		steps := []*Step{
			{ID: "fanout", Title: "Fanout", Condition: "{{fanout_enabled}}"},
			{ID: "consumer", Title: "Consumer", Needs: []string{"fanout"}},
		}
		result, err := FilterStepsByCondition(steps, map[string]string{"fanout_enabled": "true"})
		if err != nil {
			t.Fatalf("filter rejected a formula whose every condition is true: %v", err)
		}
		if got, want := strings.Join(collectStepIDsForTest(result), ","), "fanout,consumer"; got != want {
			t.Errorf("filtered IDs = %q, want %q", got, want)
		}
	})

	t.Run("unreferenced dropped leaf is excluded without error", func(t *testing.T) {
		steps := []*Step{
			{ID: "build", Title: "Build"},
			{ID: "test", Title: "Test", Needs: []string{"build"}},
			{ID: "deploy", Title: "Deploy", Needs: []string{"test"}, Condition: "{{deploy}}"},
		}
		result, err := FilterStepsByCondition(steps, map[string]string{"deploy": "false"})
		if err != nil {
			t.Fatalf("filter rejected a formula whose dropped step nothing references: %v", err)
		}
		if got, want := strings.Join(collectStepIDsForTest(result), ","), "build,test"; got != want {
			t.Errorf("filtered IDs = %q, want %q", got, want)
		}
	})

	t.Run("input steps are not modified", func(t *testing.T) {
		steps := []*Step{
			{ID: "consumer", Title: "Consumer", Needs: []string{"fanout"}},
			{ID: "fanout", Title: "Fanout", Condition: "{{fanout_enabled}}"},
		}
		if _, err := FilterStepsByCondition(steps, map[string]string{"fanout_enabled": "false"}); err == nil {
			t.Fatal("filter kept a needs reference to a dropped step")
		}
		if len(steps[0].Needs) != 1 || steps[0].Needs[0] != "fanout" {
			t.Errorf("input Needs rewritten: %v", steps[0].Needs)
		}
		if steps[1].ID != "fanout" || steps[1].Condition != "{{fanout_enabled}}" {
			t.Errorf("input step mutated: %+v", steps[1])
		}
	})
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
