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
	"slices"
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
// A kept step whose needs, depends_on, or waits_for spawner names a step this
// pass excluded is rejected with an error wrapping ErrValidation: Validate
// checked the formula as written, before the filter ran, so the reference
// would otherwise reach the cook dangling and the step would be poured with
// no edge (or, for waits_for, as a gate that waits for no one).
//
// A needs edge that a transform added on its own (a loop chaining one
// iteration to the next, compose.branch wiring, advice) is not a reference
// the formula wrote, so it is routed around the excluded step instead, also
// where a bare waits_for would infer its spawner from it: the kept step
// needs the excluded step's own needs and depends_on, following any of those
// that were excluded too through theirs. A loop whose last body step is
// excluded stays chained through that step's needs, and a compose.branch
// join waits for the parallel steps that stay. A bare waits_for gate that the
// route leaves with no needs would wait for no one, so it is rejected like a
// spawner the formula named.
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
	if len(dropped) == 0 {
		return result, nil
	}

	var errs []string
	resolveDroppedReferences(result, dropped, &errs)
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w:\n  - %s", ErrValidation, strings.Join(errs, "\n  - "))
	}
	return result, nil
}

// droppedStep records why FilterStepsByCondition excluded a step: the
// condition that evaluated false and the ID of the step carrying it (the step
// itself, or the excluded ancestor it went with). deps are the excluded
// step's own needs and depends_on, where a generated edge onto it is routed.
type droppedStep struct {
	condition string
	carrierID string
	deps      []string
}

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
			recordDropped(step, droppedStep{condition: step.Condition, carrierID: step.ID}, dropped)
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

// recordDropped marks step and all its descendants as excluded for why, each
// with its own deps.
func recordDropped(step *Step, why droppedStep, dropped map[string]droppedStep) {
	why.deps = slices.Concat(step.Needs, step.DependsOn)
	dropped[step.ID] = why
	for _, child := range step.Children {
		recordDropped(child, why, dropped)
	}
}

// resolveDroppedReferences goes through the kept steps (the filter's clones)
// for references to a dropped step: it routes each generated needs edge
// around the dropped step, and appends one line to errs for each reference
// the formula wrote and for each bare waits_for gate that the route leaves
// nothing to wait for.
func resolveDroppedReferences(steps []*Step, dropped map[string]droppedStep, errs *[]string) {
	for _, step := range steps {
		// Read the spawner before routeNeeds rewrites the needs it may
		// be inferred from.
		spawner, generated := waitsForSpawner(step)
		for _, ref := range step.DependsOn {
			if why, ok := dropped[ref]; ok {
				*errs = append(*errs, droppedReferenceError(step.ID, "depends_on", ref, why))
			}
		}
		routeNeeds(step, dropped, errs)
		// "" names no spawner, also where a step without an ID was dropped.
		if why, ok := dropped[spawner]; ok && spawner != "" {
			switch {
			case !generated:
				*errs = append(*errs, fmt.Sprintf("step %q: waits_for %q waits for the children of %q, but condition %q on step %q drops it from this pour (give %q the same condition, or name a spawner that is always poured)",
					step.ID, step.WaitsFor, spawner, why.condition, why.carrierID, step.ID))
			case len(step.Needs) == 0:
				// routeNeeds routed the generated need and the cook infers
				// the spawner from the needs it left: none, so the cook would
				// pour the gate's label with no one to wait for.
				*errs = append(*errs, fmt.Sprintf("step %q: waits_for %q takes its spawner from the dependency bd, not the formula, added onto %q, but condition %q on step %q drops it from this pour and leaves the gate nothing to wait for (give %q the same condition, or name a spawner that is always poured)",
					step.ID, step.WaitsFor, spawner, why.condition, why.carrierID, step.ID))
			}
		}
		resolveDroppedReferences(step.Children, dropped, errs)
	}
}

// waitsForSpawner returns the step whose children step's waits_for waits for,
// as the cook infers it: children-of(id), else needs[0]; "" when there is
// none. generated reports a needs[0] that a transform added rather than the
// formula, such as the chain edge a bare gate first in a loop body gets from
// the iteration before: routeNeeds routes it like any other generated edge,
// and the cook infers the spawner from what the route leaves.
func waitsForSpawner(step *Step) (spawner string, generated bool) {
	spec := ParseWaitsFor(step.WaitsFor)
	if spec == nil {
		return "", false
	}
	if spec.SpawnerID == "" && len(step.Needs) > 0 {
		return step.Needs[0], isGeneratedNeed(step, step.Needs[0])
	}
	return spec.SpawnerID, false
}

// routeNeeds rewrites step's needs: a need onto a dropped step that a
// transform generated is replaced by the steps routeAround finds, and a need
// the formula wrote onto a dropped step appends a line to errs.
func routeNeeds(step *Step, dropped map[string]droppedStep, errs *[]string) {
	needs := step.Needs
	step.Needs = nil
	for _, ref := range needs {
		why, ok := dropped[ref]
		switch {
		case !ok:
			step.Needs = append(step.Needs, ref)
		case isGeneratedNeed(step, ref):
			for _, target := range routeAround(ref, dropped, map[string]bool{step.ID: true, ref: true}) {
				addGeneratedNeed(step, target)
			}
		default:
			*errs = append(*errs, droppedReferenceError(step.ID, "needs", ref, why))
		}
	}
}

// routeAround returns the kept steps that a generated edge onto the dropped
// step id leads to instead: the dropped step's own needs and depends_on, with
// each of those that was dropped too replaced by its own in turn. seen holds
// the steps already visited, starting with the step the edge comes from, so
// that no step is routed onto itself.
func routeAround(id string, dropped map[string]droppedStep, seen map[string]bool) []string {
	var targets []string
	for _, dep := range dropped[id].deps {
		if seen[dep] {
			continue
		}
		seen[dep] = true
		if _, ok := dropped[dep]; ok {
			targets = append(targets, routeAround(dep, dropped, seen)...)
			continue
		}
		targets = append(targets, dep)
	}
	return targets
}

func droppedReferenceError(stepID, field, ref string, why droppedStep) string {
	return fmt.Sprintf("step %q: %s references %q, but condition %q on step %q drops it from this pour (give %q the same condition, or do not name a step that this pour excludes)",
		stepID, field, ref, why.condition, why.carrierID, stepID)
}
