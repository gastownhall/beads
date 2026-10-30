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
