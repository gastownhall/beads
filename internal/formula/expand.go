// Package formula provides expansion operators for macro-style step transformation.
//
// Expansion operators replace target steps with template-expanded steps.
// Unlike advice operators which insert steps around targets, expansion
// operators completely replace the target with the expansion template.
//
// Two operators are supported:
//   - expand: Apply template to a single target step
//   - map: Apply template to all steps matching a pattern
//
// Templates use {target} and {target.description} placeholders that are
// substituted with the target step's values during expansion.
package formula

import (
	"fmt"
	"strings"
)

// ApplyExpansions applies all expand and map rules to a formula's steps.
// Returns a new steps slice with expansions applied.
// The original steps slice is not modified.
//
// The parser is used to load referenced expansion formulas by name.
// If parser is nil, no expansions are applied.
func ApplyExpansions(steps []*Step, compose *ComposeRules, parser *Parser) ([]*Step, error) {
	if compose == nil || parser == nil {
		return steps, nil
	}

	if len(compose.Expand) == 0 && len(compose.Map) == 0 {
		return steps, nil
	}

	// Build a map of step ID -> step for quick lookup
	stepMap := buildStepMap(steps)

	// Track which steps have been expanded (to avoid double expansion)
	expanded := make(map[string]bool)

	// Apply expand rules first (specific targets)
	result := steps
	for _, rule := range compose.Expand {
		targetStep, ok := stepMap[rule.Target]
		if !ok {
			return nil, fmt.Errorf("expand: target step %q not found", rule.Target)
		}

		if expanded[rule.Target] {
			continue // Already expanded
		}

		// Load the expansion formula
		expFormula, err := parser.LoadByName(rule.With)
		if err != nil {
			return nil, fmt.Errorf("expand: loading %q: %w", rule.With, err)
		}

		if expFormula.Type != TypeExpansion {
			return nil, fmt.Errorf("expand: %q is not an expansion formula (type=%s)", rule.With, expFormula.Type)
		}

		if len(expFormula.Template) == 0 {
			return nil, fmt.Errorf("expand: %q has no template steps", rule.With)
		}

		// Expand the target step
		expandedSteps := expandStep(targetStep, expFormula.Template)

		// Replace the target step with expanded steps
		result = replaceStep(result, rule.Target, expandedSteps)
		expanded[rule.Target] = true

		// Update step map with new steps
		for _, s := range expandedSteps {
			stepMap[s.ID] = s
		}
		delete(stepMap, rule.Target)
	}

	// Apply map rules (pattern matching)
	for _, rule := range compose.Map {
		// Load the expansion formula
		expFormula, err := parser.LoadByName(rule.With)
		if err != nil {
			return nil, fmt.Errorf("map: loading %q: %w", rule.With, err)
		}

		if expFormula.Type != TypeExpansion {
			return nil, fmt.Errorf("map: %q is not an expansion formula (type=%s)", rule.With, expFormula.Type)
		}

		if len(expFormula.Template) == 0 {
			return nil, fmt.Errorf("map: %q has no template steps", rule.With)
		}

		// Find all matching steps
		var toExpand []*Step
		for _, step := range result {
			if MatchGlob(rule.Select, step.ID) && !expanded[step.ID] {
				toExpand = append(toExpand, step)
			}
		}

		// Expand each matching step
		for _, targetStep := range toExpand {
			expandedSteps := expandStep(targetStep, expFormula.Template)
			result = replaceStep(result, targetStep.ID, expandedSteps)
			expanded[targetStep.ID] = true

			// Update step map
			for _, s := range expandedSteps {
				stepMap[s.ID] = s
			}
			delete(stepMap, targetStep.ID)
		}
	}

	return result, nil
}

// expandStep expands a target step using the given template.
// Returns the expanded steps with placeholders substituted.
func expandStep(target *Step, template []*Step) []*Step {
	result := make([]*Step, 0, len(template))

	for _, tmpl := range template {
		expanded := &Step{
			ID:          substituteTargetPlaceholders(tmpl.ID, target),
			Title:       substituteTargetPlaceholders(tmpl.Title, target),
			Description: substituteTargetPlaceholders(tmpl.Description, target),
			Type:        tmpl.Type,
			Priority:    tmpl.Priority,
			Assignee:    tmpl.Assignee,
		}

		// Substitute placeholders in labels
		if len(tmpl.Labels) > 0 {
			expanded.Labels = make([]string, len(tmpl.Labels))
			for i, l := range tmpl.Labels {
				expanded.Labels[i] = substituteTargetPlaceholders(l, target)
			}
		}

		// Substitute placeholders in dependencies
		if len(tmpl.DependsOn) > 0 {
			expanded.DependsOn = make([]string, len(tmpl.DependsOn))
			for i, d := range tmpl.DependsOn {
				expanded.DependsOn[i] = substituteTargetPlaceholders(d, target)
			}
		}

		if len(tmpl.Needs) > 0 {
			expanded.Needs = make([]string, len(tmpl.Needs))
			for i, n := range tmpl.Needs {
				expanded.Needs[i] = substituteTargetPlaceholders(n, target)
			}
		}

		// Handle children recursively
		if len(tmpl.Children) > 0 {
			expanded.Children = expandStep(target, tmpl.Children)
		}

		result = append(result, expanded)
	}

	return result
}

// substituteTargetPlaceholders replaces {target} and {target.*} placeholders.
func substituteTargetPlaceholders(s string, target *Step) string {
	if s == "" {
		return s
	}

	// Replace {target} with target step ID
	s = strings.ReplaceAll(s, "{target}", target.ID)

	// Replace {target.id} with target step ID
	s = strings.ReplaceAll(s, "{target.id}", target.ID)

	// Replace {target.title} with target step title
	s = strings.ReplaceAll(s, "{target.title}", target.Title)

	// Replace {target.description} with target step description
	s = strings.ReplaceAll(s, "{target.description}", target.Description)

	return s
}

// buildStepMap creates a map of step ID to step (recursive).
func buildStepMap(steps []*Step) map[string]*Step {
	result := make(map[string]*Step)
	for _, step := range steps {
		result[step.ID] = step
		// Add children recursively
		for id, child := range buildStepMap(step.Children) {
			result[id] = child
		}
	}
	return result
}

// replaceStep replaces a step with the given ID with a slice of new steps.
// This is done at the top level only; children are not searched.
func replaceStep(steps []*Step, targetID string, replacement []*Step) []*Step {
	result := make([]*Step, 0, len(steps)+len(replacement)-1)

	for _, step := range steps {
		if step.ID == targetID {
			// Replace with expanded steps
			result = append(result, replacement...)
		} else {
			// Keep the step, but check children
			if len(step.Children) > 0 {
				// Clone step and replace in children
				clone := cloneStep(step)
				clone.Children = replaceStep(step.Children, targetID, replacement)
				result = append(result, clone)
			} else {
				result = append(result, step)
			}
		}
	}

	return result
}

// UpdateDependenciesForExpansion updates dependency references after expansion.
// When step X is expanded into X.draft, X.refine-1, etc., any step that
// depended on X should now depend on the last step in the expansion.
func UpdateDependenciesForExpansion(steps []*Step, expandedID string, lastExpandedStepID string) []*Step {
	result := make([]*Step, len(steps))

	for i, step := range steps {
		clone := cloneStep(step)

		// Update DependsOn references
		for j, dep := range clone.DependsOn {
			if dep == expandedID {
				clone.DependsOn[j] = lastExpandedStepID
			}
		}

		// Update Needs references
		for j, need := range clone.Needs {
			if need == expandedID {
				clone.Needs[j] = lastExpandedStepID
			}
		}

		// Handle children recursively
		if len(step.Children) > 0 {
			clone.Children = UpdateDependenciesForExpansion(step.Children, expandedID, lastExpandedStepID)
		}

		result[i] = clone
	}

	return result
}
