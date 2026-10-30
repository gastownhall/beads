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

// droppedStep records why FilterStepsByCondition excluded a step: the
// condition text and the ID of the step that carried it (the step itself,
// or the dropped ancestor whose condition took a descendant with it).
type droppedStep struct {
	conditionStepID string
	condition       string
}

// FilterStepsByCondition filters a list of steps based on their Condition field.
// Steps with conditions that evaluate to false are excluded from the result.
// Children of excluded steps are also excluded.
//
// A kept step must not name a step this filter dropped: a depends_on or
// needs entry, or a waits_for spawner (explicit children-of(id) or the one
// inferred from needs[0]), referencing a dropped step ID is rejected with
// an error wrapping ErrValidation, naming the referencing step, the field,
// the dropped target and the condition that removed it. Formula.Validate
// runs inside parser.Resolve against the formula as written, before this
// filter, so this is the only check that sees the post-filter step set a
// pour actually cooks; without it the consumer pours with no dependency
// edge at all, or a waits_for gate keeps its gate:<value> label with no
// spawner behind it and waits for no one.
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

	dropped := make(map[string]droppedStep)
	result, err := filterStepsByCondition(steps, vars, dropped)
	if err != nil {
		return nil, err
	}
	if len(dropped) > 0 {
		if err := checkDroppedStepReferences(result, dropped); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// filterStepsByCondition is the filtering walk behind FilterStepsByCondition.
// It keeps the include/exclude logic (a false condition drops the step and
// its descendants; keepers are cloned) and additionally records every
// excluded step ID in dropped with the condition and the ID of the step
// that carried it.
func filterStepsByCondition(steps []*Step, vars map[string]string, dropped map[string]droppedStep) ([]*Step, error) {
	result := make([]*Step, 0, len(steps))

	for _, step := range steps {
		// Evaluate step condition
		include, err := EvaluateStepCondition(step.Condition, vars)
		if err != nil {
			return nil, fmt.Errorf("step %q: %w", step.ID, err)
		}

		if !include {
			// Skip this step and all its children
			recordDroppedStep(step, step.ID, step.Condition, dropped)
			continue
		}

		// Clone the step to avoid mutating input
		clone := cloneStep(step)

		// Recursively filter children
		if len(step.Children) > 0 {
			filteredChildren, err := filterStepsByCondition(step.Children, vars, dropped)
			if err != nil {
				return nil, err
			}
			clone.Children = filteredChildren
		}

		result = append(result, clone)
	}

	return result, nil
}

// recordDroppedStep marks step and every descendant as excluded by condition
// on conditionStepID, so a kept reference to any of them can name both.
func recordDroppedStep(step *Step, conditionStepID, condition string, dropped map[string]droppedStep) {
	dropped[step.ID] = droppedStep{conditionStepID: conditionStepID, condition: condition}
	for _, child := range step.Children {
		recordDroppedStep(child, conditionStepID, condition, dropped)
	}
}

// checkDroppedStepReferences rejects a kept step that still names a step the
// filter dropped. Every offending reference is listed; the error wraps
// ErrValidation so the pour/wisp/bond/seed callers report it as a formula
// error instead of falling through to proto-ID lookup.
func checkDroppedStepReferences(kept []*Step, dropped map[string]droppedStep) error {
	var errs []string
	var walk func([]*Step)
	walk = func(steps []*Step) {
		for _, step := range steps {
			if step == nil {
				continue
			}
			for _, refs := range []struct {
				field string
				ids   []string
			}{
				{"depends_on", step.DependsOn},
				{"needs", step.Needs},
			} {
				for _, id := range refs.ids {
					d, ok := dropped[id]
					if !ok {
						continue
					}
					errs = append(errs, fmt.Sprintf(
						"step %q: %s references %q, but condition %q on step %q drops it from this pour (give %q the same condition, or do not name a step that this pour excludes)",
						step.ID, refs.field, id, d.condition, d.conditionStepID, step.ID))
				}
			}
			if spec := ParseWaitsFor(step.WaitsFor); spec != nil {
				// Same spawner inference as collectDependencies: an
				// explicit children-of(id), else needs[0].
				spawnerID := spec.SpawnerID
				if spawnerID == "" && len(step.Needs) > 0 {
					spawnerID = step.Needs[0]
				}
				if d, ok := dropped[spawnerID]; ok {
					errs = append(errs, fmt.Sprintf(
						"step %q: waits_for %q waits for the children of %q, but condition %q on step %q drops it from this pour (give %q the same condition, or name a spawner that is always poured)",
						step.ID, step.WaitsFor, spawnerID, d.condition, d.conditionStepID, step.ID))
				}
			}
			walk(step.Children)
		}
	}
	walk(kept)

	if len(errs) > 0 {
		return fmt.Errorf("%w:\n  - %s", ErrValidation, strings.Join(errs, "\n  - "))
	}
	return nil
}
