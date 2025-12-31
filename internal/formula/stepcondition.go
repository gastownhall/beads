// Package formula provides Step.Condition evaluation for compile-time step filtering.
//
// Step.Condition is simpler than the runtime condition evaluation in condition.go.
// It evaluates at cook/pour time to include or exclude steps based on formula variables.
//
// Supported formats:
//   - "{{var}}" - truthy check (non-empty, non-"false", non-"0")
//   - "!{{var}}" - negated truthy check (include if var is falsy)
//   - "{{var}} == value" - equality check
//   - "{{var}} != value" - inequality check
package formula

import (
	"fmt"
	"regexp"
	"strings"
)

// Step condition patterns
var (
	// {{var}} - simple variable reference for truthy check
	stepCondVarPattern = regexp.MustCompile(`^\{\{(\w+)\}\}$`)

	// !{{var}} - negated truthy check
	stepCondNegatedVarPattern = regexp.MustCompile(`^!\{\{(\w+)\}\}$`)

	// {{var}} == value or {{var}} != value
	stepCondComparePattern = regexp.MustCompile(`^\{\{(\w+)\}\}\s*(==|!=)\s*(.+)$`)
)

// EvaluateStepCondition evaluates a step's condition against variable values.
// Returns true if the step should be included, false if it should be skipped.
//
// Condition formats:
//   - "" (empty) - always include
//   - "{{var}}" - include if var is truthy (non-empty, non-"false", non-"0")
//   - "!{{var}}" - include if var is NOT truthy (negated)
//   - "{{var}} == value" - include if var equals value
//   - "{{var}} != value" - include if var does not equal value
func EvaluateStepCondition(condition string, vars map[string]string) (bool, error) {
	condition = strings.TrimSpace(condition)

	// Empty condition means always include
	if condition == "" {
		return true, nil
	}

	// Try truthy pattern: {{var}}
	if m := stepCondVarPattern.FindStringSubmatch(condition); m != nil {
		varName := m[1]
		value := vars[varName]
		return isTruthy(value), nil
	}

	// Try negated truthy pattern: !{{var}}
	if m := stepCondNegatedVarPattern.FindStringSubmatch(condition); m != nil {
		varName := m[1]
		value := vars[varName]
		return !isTruthy(value), nil
	}

	// Try comparison pattern: {{var}} == value or {{var}} != value
	if m := stepCondComparePattern.FindStringSubmatch(condition); m != nil {
		varName := m[1]
		operator := m[2]
		expected := strings.TrimSpace(m[3])

		// Remove quotes from expected value if present
		expected = unquoteValue(expected)

		actual := vars[varName]

		switch operator {
		case "==":
			return actual == expected, nil
		case "!=":
			return actual != expected, nil
		}
	}

	return false, fmt.Errorf("invalid step condition format: %q (expected {{var}} or {{var}} == value)", condition)
}

// isTruthy returns true if a value is considered "truthy" for step conditions.
// Falsy values: empty string, "false", "0", "no", "off"
// All other values are truthy.
func isTruthy(value string) bool {
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	switch lower {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

// unquoteValue removes surrounding quotes from a value if present.
func unquoteValue(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// FilterStepsByCondition filters a list of steps based on their Condition field.
// Steps with conditions that evaluate to false are excluded from the result.
// Children of excluded steps are also excluded.
//
// Parameters:
//   - steps: the steps to filter
//   - vars: variable values for condition evaluation
//
// Returns the filtered steps and any error encountered during evaluation.
func FilterStepsByCondition(steps []*Step, vars map[string]string) ([]*Step, error) {
	if vars == nil {
		vars = make(map[string]string)
	}

	result := make([]*Step, 0, len(steps))

	for _, step := range steps {
		// Evaluate step condition
		include, err := EvaluateStepCondition(step.Condition, vars)
		if err != nil {
			return nil, fmt.Errorf("step %q: %w", step.ID, err)
		}

		if !include {
			// Skip this step and all its children
			continue
		}

		// Clone the step to avoid mutating input
		clone := cloneStep(step)

		// Recursively filter children
		if len(step.Children) > 0 {
			filteredChildren, err := FilterStepsByCondition(step.Children, vars)
			if err != nil {
				return nil, err
			}
			clone.Children = filteredChildren
		}

		result = append(result, clone)
	}

	return result, nil
}
