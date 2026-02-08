package formula

import (
	"os"
	"testing"
)

func TestParseCondition_FieldConditions(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		wantType ConditionType
		wantStep string
		wantOp   Operator
	}{
		{
			name:     "step status",
			expr:     "step.status == 'complete'",
			wantType: ConditionTypeField,
			wantStep: "step",
			wantOp:   OpEqual,
		},
		{
			name:     "named step status",
			expr:     "review.status == 'complete'",
			wantType: ConditionTypeField,
			wantStep: "review",
			wantOp:   OpEqual,
		},
		{
			name:     "step output field",
			expr:     "review.output.approved == true",
			wantType: ConditionTypeField,
			wantStep: "review",
			wantOp:   OpEqual,
		},
		{
			name:     "not equal",
			expr:     "step.status != 'failed'",
			wantType: ConditionTypeField,
			wantStep: "step",
			wantOp:   OpNotEqual,
		},
		{
			name:     "nested output",
			expr:     "test.output.errors.count == 0",
			wantType: ConditionTypeField,
			wantStep: "test",
			wantOp:   OpEqual,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := ParseCondition(tt.expr)
			if err != nil {
				t.Fatalf("ParseCondition(%q) error: %v", tt.expr, err)
			}
			if cond.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", cond.Type, tt.wantType)
			}
			if cond.StepRef != tt.wantStep {
				t.Errorf("StepRef = %v, want %v", cond.StepRef, tt.wantStep)
			}
			if cond.Operator != tt.wantOp {
				t.Errorf("Operator = %v, want %v", cond.Operator, tt.wantOp)
			}
		})
	}
}

func TestParseCondition_AggregateConditions(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		wantFunc string
		wantOver string
	}{
		{
			name:     "children all complete",
			expr:     "children(step).all(status == 'complete')",
			wantFunc: "all",
			wantOver: "children",
		},
		{
			name:     "children any failed",
			expr:     "children(review).any(status == 'failed')",
			wantFunc: "any",
			wantOver: "children",
		},
		{
			name:     "steps count",
			expr:     "steps.complete >= 3",
			wantFunc: "count",
			wantOver: "steps",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := ParseCondition(tt.expr)
			if err != nil {
				t.Fatalf("ParseCondition(%q) error: %v", tt.expr, err)
			}
			if cond.Type != ConditionTypeAggregate {
				t.Errorf("Type = %v, want %v", cond.Type, ConditionTypeAggregate)
			}
			if cond.AggregateFunc != tt.wantFunc {
				t.Errorf("AggregateFunc = %v, want %v", cond.AggregateFunc, tt.wantFunc)
			}
			if cond.AggregateOver != tt.wantOver {
				t.Errorf("AggregateOver = %v, want %v", cond.AggregateOver, tt.wantOver)
			}
		})
	}
}

func TestParseCondition_ExternalConditions(t *testing.T) {
	tests := []struct {
		name        string
		expr        string
		wantExtType string
		wantExtArg  string
	}{
		{
			name:        "file exists single quotes",
			expr:        "file.exists('go.mod')",
			wantExtType: "file.exists",
			wantExtArg:  "go.mod",
		},
		{
			name:        "file exists double quotes",
			expr:        `file.exists("package.json")`,
			wantExtType: "file.exists",
			wantExtArg:  "package.json",
		},
		{
			name:        "env var",
			expr:        "env.CI == 'true'",
			wantExtType: "env",
			wantExtArg:  "CI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := ParseCondition(tt.expr)
			if err != nil {
				t.Fatalf("ParseCondition(%q) error: %v", tt.expr, err)
			}
			if cond.Type != ConditionTypeExternal {
				t.Errorf("Type = %v, want %v", cond.Type, ConditionTypeExternal)
			}
			if cond.ExternalType != tt.wantExtType {
				t.Errorf("ExternalType = %v, want %v", cond.ExternalType, tt.wantExtType)
			}
			if cond.ExternalArg != tt.wantExtArg {
				t.Errorf("ExternalArg = %v, want %v", cond.ExternalArg, tt.wantExtArg)
			}
		})
	}
}

func TestEvaluateCondition_Field(t *testing.T) {
	ctx := &ConditionContext{
		CurrentStep: "test",
		Steps: map[string]*StepState{
			"design": {
				ID:     "design",
				Status: "complete",
				Output: map[string]interface{}{
					"approved": true,
				},
			},
			"test": {
				ID:     "test",
				Status: "in_progress",
				Output: map[string]interface{}{
					"errors": map[string]interface{}{
						"count": float64(0),
					},
				},
			},
			"review": {
				ID:     "review",
				Status: "pending",
				Output: map[string]interface{}{
					"approved": false,
				},
			},
		},
	}

	tests := []struct {
		name      string
		expr      string
		wantSatis bool
	}{
		{
			name:      "step complete - true",
			expr:      "design.status == 'complete'",
			wantSatis: true,
		},
		{
			name:      "step complete - false",
			expr:      "review.status == 'complete'",
			wantSatis: false,
		},
		{
			name:      "current step status",
			expr:      "step.status == 'in_progress'",
			wantSatis: true,
		},
		{
			name:      "output bool true",
			expr:      "design.output.approved == true",
			wantSatis: true,
		},
		{
			name:      "output bool false",
			expr:      "review.output.approved == true",
			wantSatis: false,
		},
		{
			name:      "nested output",
			expr:      "test.output.errors.count == 0",
			wantSatis: true,
		},
		{
			name:      "not equal satisfied",
			expr:      "design.status != 'failed'",
			wantSatis: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateCondition(tt.expr, ctx)
			if err != nil {
				t.Fatalf("EvaluateCondition(%q) error: %v", tt.expr, err)
			}
			if result.Satisfied != tt.wantSatis {
				t.Errorf("Satisfied = %v, want %v (reason: %s)", result.Satisfied, tt.wantSatis, result.Reason)
			}
		})
	}
}

func TestEvaluateCondition_Aggregate(t *testing.T) {
	ctx := &ConditionContext{
		CurrentStep: "aggregate",
		Steps: map[string]*StepState{
			"parent": {
				ID:     "parent",
				Status: "in_progress",
				Children: []*StepState{
					{ID: "child1", Status: "complete"},
					{ID: "child2", Status: "complete"},
					{ID: "child3", Status: "complete"},
				},
			},
			"mixed": {
				ID:     "mixed",
				Status: "in_progress",
				Children: []*StepState{
					{ID: "m1", Status: "complete"},
					{ID: "m2", Status: "failed"},
					{ID: "m3", Status: "pending"},
				},
			},
			"step1": {ID: "step1", Status: "complete"},
			"step2": {ID: "step2", Status: "complete"},
			"step3": {ID: "step3", Status: "complete"},
			"step4": {ID: "step4", Status: "pending"},
		},
	}

	tests := []struct {
		name      string
		expr      string
		wantSatis bool
	}{
		{
			name:      "all children complete - true",
			expr:      "children(parent).all(status == 'complete')",
			wantSatis: true,
		},
		{
			name:      "all children complete - false",
			expr:      "children(mixed).all(status == 'complete')",
			wantSatis: false,
		},
		{
			name:      "any children failed - true",
			expr:      "children(mixed).any(status == 'failed')",
			wantSatis: true,
		},
		{
			name:      "any children failed - false",
			expr:      "children(parent).any(status == 'failed')",
			wantSatis: false,
		},
		{
			name:      "steps count >= satisfied",
			expr:      "steps.complete >= 3",
			wantSatis: true,
		},
		{
			name:      "steps count >= not satisfied",
			expr:      "steps.complete >= 5",
			wantSatis: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateCondition(tt.expr, ctx)
			if err != nil {
				t.Fatalf("EvaluateCondition(%q) error: %v", tt.expr, err)
			}
			if result.Satisfied != tt.wantSatis {
				t.Errorf("Satisfied = %v, want %v (reason: %s)", result.Satisfied, tt.wantSatis, result.Reason)
			}
		})
	}
}

func TestEvaluateCondition_External(t *testing.T) {
	// Create a temp file for testing
	tmpFile, err := os.CreateTemp("", "condition_test_*.txt")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	// Set an env var for testing
	os.Setenv("TEST_CONDITION_VAR", "test_value")
	defer os.Unsetenv("TEST_CONDITION_VAR")

	ctx := &ConditionContext{
		Vars: map[string]string{
			"tempfile": tmpFile.Name(),
		},
	}

	tests := []struct {
		name      string
		expr      string
		wantSatis bool
	}{
		{
			name:      "file exists - true",
			expr:      "file.exists('" + tmpFile.Name() + "')",
			wantSatis: true,
		},
		{
			name:      "file exists - false",
			expr:      "file.exists('/nonexistent/file/path')",
			wantSatis: false,
		},
		{
			name:      "env var equals",
			expr:      "env.TEST_CONDITION_VAR == 'test_value'",
			wantSatis: true,
		},
		{
			name:      "env var not equals",
			expr:      "env.TEST_CONDITION_VAR == 'wrong'",
			wantSatis: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateCondition(tt.expr, ctx)
			if err != nil {
				t.Fatalf("EvaluateCondition(%q) error: %v", tt.expr, err)
			}
			if result.Satisfied != tt.wantSatis {
				t.Errorf("Satisfied = %v, want %v (reason: %s)", result.Satisfied, tt.wantSatis, result.Reason)
			}
		})
	}
}

func TestParseCondition_Errors(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{
			name: "empty",
			expr: "",
		},
		{
			name: "whitespace only",
			expr: "   ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCondition(tt.expr)
			if err == nil {
				t.Errorf("ParseCondition(%q) expected error, got nil", tt.expr)
			}
		})
	}
}

func TestCompareOperators(t *testing.T) {
	tests := []struct {
		actual   string
		op       Operator
		expected string
		want     bool
	}{
		{"5", OpGreater, "3", true},
		{"5", OpGreater, "5", false},
		{"5", OpGreaterEqual, "5", true},
		{"3", OpLess, "5", true},
		{"5", OpLess, "5", false},
		{"5", OpLessEqual, "5", true},
		{"abc", OpEqual, "abc", true},
		{"abc", OpNotEqual, "def", true},
	}

	for _, tt := range tests {
		t.Run(string(tt.op), func(t *testing.T) {
			got, _ := compare(tt.actual, tt.op, tt.expected)
			if got != tt.want {
				t.Errorf("compare(%q, %s, %q) = %v, want %v", tt.actual, tt.op, tt.expected, got, tt.want)
			}
		})
	}
}

func TestEvaluateCondition_EmptyChildren(t *testing.T) {
	ctx := &ConditionContext{
		CurrentStep: "parent",
		Steps: map[string]*StepState{
			"parent": {
				ID:       "parent",
				Status:   "in_progress",
				Children: []*StepState{}, // Empty children
			},
		},
	}

	// "all children complete" with no children should be false (not vacuous truth)
	result, err := EvaluateCondition("children(parent).all(status == 'complete')", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Satisfied {
		t.Errorf("empty children 'all' should be false, got true")
	}
}

func TestEvaluateCondition_NilOutput(t *testing.T) {
	ctx := &ConditionContext{
		CurrentStep: "test",
		Steps: map[string]*StepState{
			"test": {
				ID:     "test",
				Status: "complete",
				Output: nil, // No output
			},
		},
	}

	// Comparing nil output field should not panic
	result, err := EvaluateCondition("test.output.missing == 'value'", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Satisfied {
		t.Errorf("nil field comparison should be false")
	}
}

func TestEvaluateCondition_BoolOutput(t *testing.T) {
	ctx := &ConditionContext{
		CurrentStep: "test",
		Steps: map[string]*StepState{
			"test": {
				ID:     "test",
				Status: "complete",
				Output: map[string]interface{}{
					"approved": true, // actual bool, not string
				},
			},
		},
	}

	result, err := EvaluateCondition("test.output.approved == true", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Satisfied {
		t.Errorf("bool true should match 'true', got: %s", result.Reason)
	}
}

func TestEvaluateCondition_CountError(t *testing.T) {
	ctx := &ConditionContext{
		Steps: map[string]*StepState{
			"s1": {ID: "s1", Status: "complete"},
		},
	}

	// Directly construct a condition with invalid count value
	// (regex validation normally prevents this, but test defensive code)
	cond := &Condition{
		Raw:           "steps.complete >= notanumber",
		Type:          ConditionTypeAggregate,
		AggregateOver: "steps",
		AggregateFunc: "count",
		Field:         "complete",
		Operator:      OpGreaterEqual,
		Value:         "notanumber", // Invalid: not an integer
	}

	_, err := cond.Evaluate(ctx)
	if err == nil {
		t.Error("expected error for non-integer count value")
	}
}
