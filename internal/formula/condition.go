// Package formula provides condition evaluation for gates and loops.
//
// Conditions are intentionally limited to keep evaluation decidable:
//   - Step status checks: step.status == 'complete'
//   - Step output access: step.output.approved == true
//   - Aggregates: children(step).all(status == 'complete')
//   - External checks: file.exists('go.mod'), env.CI == 'true'
//
// No arbitrary code execution is allowed.
package formula

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ConditionResult represents the result of evaluating a condition.
type ConditionResult struct {
	// Satisfied is true if the condition is met.
	Satisfied bool

	// Reason explains why the condition is satisfied or not.
	Reason string
}

// StepState represents the runtime state of a step for condition evaluation.
type StepState struct {
	// ID is the step identifier.
	ID string

	// Status is the step status: pending, in_progress, complete, failed.
	Status string

	// Output is the structured output from the step (if complete).
	// Keys are dot-separated paths, values are the output values.
	Output map[string]interface{}

	// Children are the child step states (for aggregate conditions).
	Children []*StepState
}

// ConditionContext provides the evaluation context for conditions.
type ConditionContext struct {
	// Steps maps step ID to step state.
	Steps map[string]*StepState

	// CurrentStep is the step being gated (for relative references).
	CurrentStep string

	// Vars are the formula variables (for variable substitution).
	Vars map[string]string
}

// Operator represents a comparison operator.
type Operator string

const (
	OpEqual        Operator = "=="
	OpNotEqual     Operator = "!="
	OpGreater      Operator = ">"
	OpGreaterEqual Operator = ">="
	OpLess         Operator = "<"
	OpLessEqual    Operator = "<="
)

// Condition represents a parsed condition expression.
type Condition struct {
	// Raw is the original condition string.
	Raw string

	// Type is the condition type: field, aggregate, external.
	Type ConditionType

	// For field conditions:
	StepRef  string   // Step ID reference (e.g., "review", "step" for current)
	Field    string   // Field path (e.g., "status", "output.approved")
	Operator Operator // Comparison operator
	Value    string   // Expected value

	// For aggregate conditions:
	AggregateFunc string // Function: all, any, count
	AggregateOver string // What to aggregate: children, descendants, steps

	// For external conditions:
	ExternalType string // file.exists, env
	ExternalArg  string // Argument (path or env var name)
}

// ConditionType categorizes conditions.
type ConditionType string

const (
	ConditionTypeField     ConditionType = "field"
	ConditionTypeAggregate ConditionType = "aggregate"
	ConditionTypeExternal  ConditionType = "external"
)

// Patterns for parsing conditions.
var (
	// step.status == 'complete' or review.output.approved == true or test.output.errors.count == 0
	fieldPattern = regexp.MustCompile(`^(\w+(?:\.\w+)*)\s*([=!<>]+)\s*(.+)$`)

	// children(step).all(status == 'complete')
	aggregatePattern = regexp.MustCompile(`^(children|descendants|steps)\((\w+)\)\.(all|any|count)\((.+)\)(.*)$`)

	// file.exists('go.mod')
	fileExistsPattern = regexp.MustCompile(`^file\.exists\(['"](.+)['"]\)$`)

	// env.CI == 'true'
	envPattern = regexp.MustCompile(`^env\.(\w+)\s*([=!<>]+)\s*(.+)$`)

	// steps.complete >= 3
	stepsStatPattern = regexp.MustCompile(`^steps\.(\w+)\s*([=!<>]+)\s*(\d+)$`)
)

// ParseCondition parses a condition string into a Condition struct.
func ParseCondition(expr string) (*Condition, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty condition")
	}

	// Try file.exists pattern
	if m := fileExistsPattern.FindStringSubmatch(expr); m != nil {
		return &Condition{
			Raw:          expr,
			Type:         ConditionTypeExternal,
			ExternalType: "file.exists",
			ExternalArg:  m[1],
		}, nil
	}

	// Try env pattern
	if m := envPattern.FindStringSubmatch(expr); m != nil {
		return &Condition{
			Raw:          expr,
			Type:         ConditionTypeExternal,
			ExternalType: "env",
			ExternalArg:  m[1],
			Operator:     Operator(m[2]),
			Value:        unquote(m[3]),
		}, nil
	}

	// Try aggregate pattern: children(step).all(status == 'complete')
	if m := aggregatePattern.FindStringSubmatch(expr); m != nil {
		innerCond, err := ParseCondition(m[4])
		if err != nil {
			return nil, fmt.Errorf("parsing aggregate inner condition: %w", err)
		}
		cond := &Condition{
			Raw:           expr,
			Type:          ConditionTypeAggregate,
			AggregateOver: m[1], // children, descendants, steps
			StepRef:       m[2], // step reference
			AggregateFunc: m[3], // all, any, count
			Field:         innerCond.Field,
			Operator:      innerCond.Operator,
			Value:         innerCond.Value,
		}
		// Handle count comparison: children(x).count(...) >= 3
		if m[5] != "" {
			countMatch := regexp.MustCompile(`\s*([=!<>]+)\s*(\d+)$`).FindStringSubmatch(m[5])
			if countMatch != nil {
				cond.AggregateFunc = "count"
				cond.Operator = Operator(countMatch[1])
				cond.Value = countMatch[2]
			}
		}
		return cond, nil
	}

	// Try steps.stat pattern: steps.complete >= 3
	if m := stepsStatPattern.FindStringSubmatch(expr); m != nil {
		return &Condition{
			Raw:           expr,
			Type:          ConditionTypeAggregate,
			AggregateOver: "steps",
			AggregateFunc: "count",
			Field:         m[1], // complete, failed, etc.
			Operator:      Operator(m[2]),
			Value:         m[3],
		}, nil
	}

	// Try field pattern: step.status == 'complete' or step.output.approved == true
	if m := fieldPattern.FindStringSubmatch(expr); m != nil {
		fieldPath := m[1]
		parts := strings.SplitN(fieldPath, ".", 2)

		stepRef := "step" // default to current step
		field := fieldPath

		if len(parts) >= 2 {
			// Could be:
			// - step.status (keyword "step" + field)
			// - output.field (keyword "output" + path, relative to current step)
			// - review.status (step name + field)
			// - review.output.approved (step name + output.path)
			if parts[0] == "step" {
				// step.status or step.output.approved
				field = parts[1]
			} else if parts[0] == "output" {
				// output.field (relative to current step)
				field = fieldPath // keep as output.field
			} else {
				// step_name.field or step_name.output.path
				stepRef = parts[0]
				field = parts[1]
			}
		}

		return &Condition{
			Raw:      expr,
			Type:     ConditionTypeField,
			StepRef:  stepRef,
			Field:    field,
			Operator: Operator(m[2]),
			Value:    unquote(m[3]),
		}, nil
	}

	return nil, fmt.Errorf("unrecognized condition format: %s", expr)
}

// Evaluate evaluates the condition against the given context.
func (c *Condition) Evaluate(ctx *ConditionContext) (*ConditionResult, error) {
	switch c.Type {
	case ConditionTypeField:
		return c.evaluateField(ctx)
	case ConditionTypeAggregate:
		return c.evaluateAggregate(ctx)
	case ConditionTypeExternal:
		return c.evaluateExternal(ctx)
	default:
		return nil, fmt.Errorf("unknown condition type: %s", c.Type)
	}
}

func (c *Condition) evaluateField(ctx *ConditionContext) (*ConditionResult, error) {
	// Resolve step reference
	stepID := c.StepRef
	if stepID == "step" {
		stepID = ctx.CurrentStep
	}

	step, ok := ctx.Steps[stepID]
	if !ok {
		return &ConditionResult{
			Satisfied: false,
			Reason:    fmt.Sprintf("step %q not found", stepID),
		}, nil
	}

	// Get the field value
	var actual interface{}
	if c.Field == "status" {
		actual = step.Status
	} else if strings.HasPrefix(c.Field, "output.") {
		path := strings.TrimPrefix(c.Field, "output.")
		actual = getNestedValue(step.Output, path)
	} else {
		return nil, fmt.Errorf("unknown field: %s", c.Field)
	}

	// Compare
	satisfied, reason := compare(actual, c.Operator, c.Value)
	return &ConditionResult{
		Satisfied: satisfied,
		Reason:    reason,
	}, nil
}

func (c *Condition) evaluateAggregate(ctx *ConditionContext) (*ConditionResult, error) {
	// Get the set of steps to aggregate over
	var steps []*StepState

	switch c.AggregateOver {
	case "children":
		stepID := c.StepRef
		if stepID == "step" {
			stepID = ctx.CurrentStep
		}
		parent, ok := ctx.Steps[stepID]
		if !ok {
			return &ConditionResult{
				Satisfied: false,
				Reason:    fmt.Sprintf("step %q not found", stepID),
			}, nil
		}
		steps = parent.Children

	case "steps":
		// All steps in context
		for _, s := range ctx.Steps {
			steps = append(steps, s)
		}

	case "descendants":
		stepID := c.StepRef
		if stepID == "step" {
			stepID = ctx.CurrentStep
		}
		parent, ok := ctx.Steps[stepID]
		if !ok {
			return &ConditionResult{
				Satisfied: false,
				Reason:    fmt.Sprintf("step %q not found", stepID),
			}, nil
		}
		steps = collectDescendants(parent)
	}

	// Apply the aggregate function
	switch c.AggregateFunc {
	case "all":
		for _, s := range steps {
			satisfied, _ := matchStep(s, c.Field, c.Operator, c.Value)
			if !satisfied {
				return &ConditionResult{
					Satisfied: false,
					Reason:    fmt.Sprintf("step %q does not match: %s %s %s", s.ID, c.Field, c.Operator, c.Value),
				}, nil
			}
		}
		return &ConditionResult{
			Satisfied: true,
			Reason:    fmt.Sprintf("all %d steps match", len(steps)),
		}, nil

	case "any":
		for _, s := range steps {
			satisfied, _ := matchStep(s, c.Field, c.Operator, c.Value)
			if satisfied {
				return &ConditionResult{
					Satisfied: true,
					Reason:    fmt.Sprintf("step %q matches: %s %s %s", s.ID, c.Field, c.Operator, c.Value),
				}, nil
			}
		}
		return &ConditionResult{
			Satisfied: false,
			Reason:    fmt.Sprintf("no steps match: %s %s %s", c.Field, c.Operator, c.Value),
		}, nil

	case "count":
		count := 0
		for _, s := range steps {
			// For steps.complete pattern, field is the status to count
			if c.AggregateOver == "steps" && (c.Field == "complete" || c.Field == "failed" || c.Field == "pending" || c.Field == "in_progress") {
				if s.Status == c.Field {
					count++
				}
			} else {
				satisfied, _ := matchStep(s, c.Field, OpEqual, c.Value)
				if satisfied {
					count++
				}
			}
		}
		expected, _ := strconv.Atoi(c.Value)
		satisfied, reason := compareInt(count, c.Operator, expected)
		return &ConditionResult{
			Satisfied: satisfied,
			Reason:    reason,
		}, nil
	}

	return nil, fmt.Errorf("unknown aggregate function: %s", c.AggregateFunc)
}

func (c *Condition) evaluateExternal(ctx *ConditionContext) (*ConditionResult, error) {
	switch c.ExternalType {
	case "file.exists":
		path := c.ExternalArg
		// Substitute variables
		for k, v := range ctx.Vars {
			path = strings.ReplaceAll(path, "{{"+k+"}}", v)
		}
		_, err := os.Stat(path)
		exists := err == nil
		return &ConditionResult{
			Satisfied: exists,
			Reason:    fmt.Sprintf("file %q exists: %v", path, exists),
		}, nil

	case "env":
		actual := os.Getenv(c.ExternalArg)
		satisfied, reason := compare(actual, c.Operator, c.Value)
		return &ConditionResult{
			Satisfied: satisfied,
			Reason:    reason,
		}, nil
	}

	return nil, fmt.Errorf("unknown external type: %s", c.ExternalType)
}

// Helper functions

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func getNestedValue(m map[string]interface{}, path string) interface{} {
	if m == nil {
		return nil
	}
	parts := strings.Split(path, ".")
	var current interface{} = m
	for _, part := range parts {
		if cm, ok := current.(map[string]interface{}); ok {
			current = cm[part]
		} else {
			return nil
		}
	}
	return current
}

func compare(actual interface{}, op Operator, expected string) (bool, string) {
	actualStr := fmt.Sprintf("%v", actual)

	switch op {
	case OpEqual:
		satisfied := actualStr == expected
		return satisfied, fmt.Sprintf("%q %s %q: %v", actualStr, op, expected, satisfied)
	case OpNotEqual:
		satisfied := actualStr != expected
		return satisfied, fmt.Sprintf("%q %s %q: %v", actualStr, op, expected, satisfied)
	case OpGreater, OpGreaterEqual, OpLess, OpLessEqual:
		// Try numeric comparison
		actualNum, err1 := strconv.ParseFloat(actualStr, 64)
		expectedNum, err2 := strconv.ParseFloat(expected, 64)
		if err1 == nil && err2 == nil {
			return compareFloat(actualNum, op, expectedNum)
		}
		// Fall back to string comparison
		return compareString(actualStr, op, expected)
	}
	return false, fmt.Sprintf("unknown operator: %s", op)
}

func compareInt(actual int, op Operator, expected int) (bool, string) {
	var satisfied bool
	switch op {
	case OpEqual:
		satisfied = actual == expected
	case OpNotEqual:
		satisfied = actual != expected
	case OpGreater:
		satisfied = actual > expected
	case OpGreaterEqual:
		satisfied = actual >= expected
	case OpLess:
		satisfied = actual < expected
	case OpLessEqual:
		satisfied = actual <= expected
	}
	return satisfied, fmt.Sprintf("%d %s %d: %v", actual, op, expected, satisfied)
}

func compareFloat(actual float64, op Operator, expected float64) (bool, string) {
	var satisfied bool
	switch op {
	case OpEqual:
		satisfied = actual == expected
	case OpNotEqual:
		satisfied = actual != expected
	case OpGreater:
		satisfied = actual > expected
	case OpGreaterEqual:
		satisfied = actual >= expected
	case OpLess:
		satisfied = actual < expected
	case OpLessEqual:
		satisfied = actual <= expected
	}
	return satisfied, fmt.Sprintf("%v %s %v: %v", actual, op, expected, satisfied)
}

func compareString(actual string, op Operator, expected string) (bool, string) {
	var satisfied bool
	switch op {
	case OpGreater:
		satisfied = actual > expected
	case OpGreaterEqual:
		satisfied = actual >= expected
	case OpLess:
		satisfied = actual < expected
	case OpLessEqual:
		satisfied = actual <= expected
	}
	return satisfied, fmt.Sprintf("%q %s %q: %v", actual, op, expected, satisfied)
}

func matchStep(s *StepState, field string, op Operator, expected string) (bool, string) {
	var actual interface{}
	if field == "status" {
		actual = s.Status
	} else if strings.HasPrefix(field, "output.") {
		path := strings.TrimPrefix(field, "output.")
		actual = getNestedValue(s.Output, path)
	} else {
		// Direct field name might be a status shorthand
		actual = s.Status
	}
	return compare(actual, op, expected)
}

func collectDescendants(s *StepState) []*StepState {
	var result []*StepState
	for _, child := range s.Children {
		result = append(result, child)
		result = append(result, collectDescendants(child)...)
	}
	return result
}

// EvaluateCondition is a convenience function that parses and evaluates a condition.
func EvaluateCondition(expr string, ctx *ConditionContext) (*ConditionResult, error) {
	cond, err := ParseCondition(expr)
	if err != nil {
		return nil, err
	}
	return cond.Evaluate(ctx)
}
