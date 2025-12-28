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
//
// A maximum expansion depth (default 5) prevents runaway nested expansions.
// This allows massive work generation while providing a safety bound.
package formula

import (
	"fmt"
	"strings"
)

// DefaultMaxExpansionDepth is the maximum depth for recursive template expansion.
// This prevents runaway nested expansions while still allowing substantial work
// generation. The limit applies to template children, not to expansion rules.
const DefaultMaxExpansionDepth = 5

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

		// Merge formula default vars with rule overrides
		vars := mergeVars(expFormula, rule.Vars)

		// Expand the target step (start at depth 0)
		expandedSteps, err := expandStep(targetStep, expFormula.Template, 0, vars)
		if err != nil {
			return nil, fmt.Errorf("expand %q: %w", rule.Target, err)
		}

		// Replace the target step with expanded steps
		result = replaceStep(result, rule.Target, expandedSteps)
		expanded[rule.Target] = true

		// Update dependencies: any step that depended on the target should now
		// depend on the last step of the expansion
		if len(expandedSteps) > 0 {
			lastStepID := expandedSteps[len(expandedSteps)-1].ID
			result = UpdateDependenciesForExpansion(result, rule.Target, lastStepID)
		}

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

		// Merge formula default vars with rule overrides
		vars := mergeVars(expFormula, rule.Vars)

		// Find all matching steps (including nested children)
		// Rebuild stepMap to capture any changes from previous expansions
		stepMap = buildStepMap(result)
		var toExpand []*Step
		for id, step := range stepMap {
			if MatchGlob(rule.Select, id) && !expanded[id] {
				toExpand = append(toExpand, step)
			}
		}

		// Expand each matching step
		for _, targetStep := range toExpand {
			expandedSteps, err := expandStep(targetStep, expFormula.Template, 0, vars)
			if err != nil {
				return nil, fmt.Errorf("map %q -> %q: %w", rule.Select, targetStep.ID, err)
			}
			result = replaceStep(result, targetStep.ID, expandedSteps)
			expanded[targetStep.ID] = true

			// Update dependencies: any step that depended on the target should now
			// depend on the last step of the expansion
			if len(expandedSteps) > 0 {
				lastStepID := expandedSteps[len(expandedSteps)-1].ID
				result = UpdateDependenciesForExpansion(result, targetStep.ID, lastStepID)
			}

			// Update step map
			for _, s := range expandedSteps {
				stepMap[s.ID] = s
			}
			delete(stepMap, targetStep.ID)
		}
	}

	// Validate no duplicate step IDs after expansion
	if dups := findDuplicateStepIDs(result); len(dups) > 0 {
		return nil, fmt.Errorf("duplicate step IDs after expansion: %v", dups)
	}

	return result, nil
}

// findDuplicateStepIDs returns any duplicate step IDs found in the steps slice.
// It recursively checks all children.
func findDuplicateStepIDs(steps []*Step) []string {
	seen := make(map[string]int)
	countStepIDs(steps, seen)

	var dups []string
	for id, count := range seen {
		if count > 1 {
			dups = append(dups, id)
		}
	}
	return dups
}

// countStepIDs counts occurrences of each step ID recursively.
func countStepIDs(steps []*Step, counts map[string]int) {
	for _, step := range steps {
		counts[step.ID]++
		if len(step.Children) > 0 {
			countStepIDs(step.Children, counts)
		}
	}
}

// expandStep expands a target step using the given template.
// Returns the expanded steps with placeholders substituted.
// The depth parameter tracks recursion depth for children; if it exceeds
// DefaultMaxExpansionDepth, an error is returned.
// The vars parameter provides variable values for {varname} substitution.
func expandStep(target *Step, template []*Step, depth int, vars map[string]string) ([]*Step, error) {
	if depth > DefaultMaxExpansionDepth {
		return nil, fmt.Errorf("expansion depth limit exceeded: max %d levels (currently at %d) - step %q",
			DefaultMaxExpansionDepth, depth, target.ID)
	}

	result := make([]*Step, 0, len(template))

	for _, tmpl := range template {
		expanded := &Step{
			ID:             substituteVars(substituteTargetPlaceholders(tmpl.ID, target), vars),
			Title:          substituteVars(substituteTargetPlaceholders(tmpl.Title, target), vars),
			Description:    substituteVars(substituteTargetPlaceholders(tmpl.Description, target), vars),
			Type:           tmpl.Type,
			Priority:       tmpl.Priority,
			Assignee:       substituteVars(tmpl.Assignee, vars),
			SourceFormula:  tmpl.SourceFormula,  // Preserve source from template
			SourceLocation: tmpl.SourceLocation, // Preserve source location
		}

		// Substitute placeholders in labels
		if len(tmpl.Labels) > 0 {
			expanded.Labels = make([]string, len(tmpl.Labels))
			for i, l := range tmpl.Labels {
				expanded.Labels[i] = substituteVars(substituteTargetPlaceholders(l, target), vars)
			}
		}

		// Substitute placeholders in dependencies
		if len(tmpl.DependsOn) > 0 {
			expanded.DependsOn = make([]string, len(tmpl.DependsOn))
			for i, d := range tmpl.DependsOn {
				expanded.DependsOn[i] = substituteVars(substituteTargetPlaceholders(d, target), vars)
			}
		}

		if len(tmpl.Needs) > 0 {
			expanded.Needs = make([]string, len(tmpl.Needs))
			for i, n := range tmpl.Needs {
				expanded.Needs[i] = substituteVars(substituteTargetPlaceholders(n, target), vars)
			}
		}

		// Handle children recursively with depth tracking
		if len(tmpl.Children) > 0 {
			children, err := expandStep(target, tmpl.Children, depth+1, vars)
			if err != nil {
				return nil, err
			}
			expanded.Children = children
		}

		result = append(result, expanded)
	}

	return result, nil
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

// mergeVars merges formula default vars with rule overrides.
// Override values take precedence over defaults.
func mergeVars(formula *Formula, overrides map[string]string) map[string]string {
	result := make(map[string]string)

	// Start with formula defaults
	for name, def := range formula.Vars {
		if def.Default != "" {
			result[name] = def.Default
		}
	}

	// Apply overrides (these win)
	for name, value := range overrides {
		result[name] = value
	}

	return result
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
// Searches recursively through children to find and replace the target.
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

// ApplyInlineExpansions applies Step.Expand fields to inline expansions.
// Steps with the Expand field set are replaced by the referenced expansion template.
// The step's ExpandVars are passed as variable overrides to the expansion.
//
// This differs from compose.Expand in that the expansion is declared inline on the
// step itself rather than in a central compose section.
//
// Returns a new steps slice with inline expansions applied.
// The original steps slice is not modified.
func ApplyInlineExpansions(steps []*Step, parser *Parser) ([]*Step, error) {
	if parser == nil {
		return steps, nil
	}

	return applyInlineExpansionsRecursive(steps, parser, 0)
}

// applyInlineExpansionsRecursive handles inline expansions for a slice of steps.
// depth tracks recursion to prevent infinite expansion loops.
func applyInlineExpansionsRecursive(steps []*Step, parser *Parser, depth int) ([]*Step, error) {
	if depth > DefaultMaxExpansionDepth {
		return nil, fmt.Errorf("inline expansion depth limit exceeded: max %d levels", DefaultMaxExpansionDepth)
	}

	var result []*Step

	for _, step := range steps {
		// Check if this step has an inline expansion
		if step.Expand != "" {
			// Load the expansion formula
			expFormula, err := parser.LoadByName(step.Expand)
			if err != nil {
				return nil, fmt.Errorf("inline expand on step %q: loading %q: %w", step.ID, step.Expand, err)
			}

			if expFormula.Type != TypeExpansion {
				return nil, fmt.Errorf("inline expand on step %q: %q is not an expansion formula (type=%s)",
					step.ID, step.Expand, expFormula.Type)
			}

			if len(expFormula.Template) == 0 {
				return nil, fmt.Errorf("inline expand on step %q: %q has no template steps", step.ID, step.Expand)
			}

			// Merge formula default vars with step's ExpandVars overrides
			vars := mergeVars(expFormula, step.ExpandVars)

			// Expand the step using the template (reuse existing expandStep)
			expandedSteps, err := expandStep(step, expFormula.Template, 0, vars)
			if err != nil {
				return nil, fmt.Errorf("inline expand on step %q: %w", step.ID, err)
			}

			// Recursively process expanded steps for nested inline expansions
			processedSteps, err := applyInlineExpansionsRecursive(expandedSteps, parser, depth+1)
			if err != nil {
				return nil, err
			}

			result = append(result, processedSteps...)
		} else {
			// No inline expansion - keep the step, but process children recursively
			clone := cloneStep(step)

			if len(step.Children) > 0 {
				processedChildren, err := applyInlineExpansionsRecursive(step.Children, parser, depth)
				if err != nil {
					return nil, err
				}
				clone.Children = processedChildren
			}

			result = append(result, clone)
		}
	}

	return result, nil
}
