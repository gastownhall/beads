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
	"slices"
	"sort"
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
		expandedSteps, err := expandStep(targetStep, expFormula.Template, vars)
		if err != nil {
			return nil, fmt.Errorf("expand %q: %w", rule.Target, err)
		}

		// Propagate target step's dependencies to root steps of the expansion.
		// Root steps are those whose needs/dependsOn only reference IDs within
		// the expansion (or are empty) — they are the entry points.
		propagateTargetDeps(targetStep, expandedSteps)

		// Replace the target step with expanded steps
		result = replaceStep(result, rule.Target, expandedSteps)
		expanded[rule.Target] = true

		// Update dependencies: any step that depended on the target should now
		// depend on the last step of the expansion
		if len(expandedSteps) > 0 {
			lastStepID := expandedSteps[len(expandedSteps)-1].ID
			result = UpdateDependenciesForExpansion(result, rule.Target, lastStepID)
		}

		// Rebuild stepMap from result so subsequent iterations see resolved deps
		stepMap = buildStepMap(result)
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
			expandedSteps, err := expandStep(targetStep, expFormula.Template, vars)
			if err != nil {
				return nil, fmt.Errorf("map %q -> %q: %w", rule.Select, targetStep.ID, err)
			}

			// Propagate target step's dependencies to root steps of the expansion
			propagateTargetDeps(targetStep, expandedSteps)

			result = replaceStep(result, targetStep.ID, expandedSteps)
			expanded[targetStep.ID] = true

			// Update dependencies: any step that depended on the target should now
			// depend on the last step of the expansion
			if len(expandedSteps) > 0 {
				lastStepID := expandedSteps[len(expandedSteps)-1].ID
				result = UpdateDependenciesForExpansion(result, targetStep.ID, lastStepID)
			}

			// Rebuild stepMap from result so subsequent iterations see resolved deps
			stepMap = buildStepMap(result)
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

// expandStep expands a target step using the given template, starting at
// recursion depth 0 (expandStepWith tracks depth for nested children; if it
// exceeds DefaultMaxExpansionDepth, an error is returned).
// The vars parameter provides variable values for {varname} substitution.
func expandStep(target *Step, template []*Step, vars map[string]string) ([]*Step, error) {
	return expandStepWith(target, template, 0, func(s string) string {
		return substituteVars(s, vars)
	})
}

// expandStepWith is expandStep with the {varname} substitution supplied as
// subst, which receives every string the expansion substitutes variables into -
// after the {target} placeholders in it are replaced, where those apply. It
// exists so ExpansionVarRefs can see exactly those strings without keeping a
// second list of the fields that carry them.
//
// Each expanded step starts as a copy of the template step, with the fields
// that carry placeholders rewritten over the copy (GH#6433). A Step field
// added later therefore carries through by default; the failure mode inverts
// from "the field silently vanishes" (invisible in the cooked output) to "a
// forgotten substitution shows up as a literal placeholder".
//
// Fields that are rewritten rather than copied verbatim:
//
//   - ID, Title, Description, Notes, Assignee, Labels, DependsOn, Needs,
//     generatedNeeds and WaitsFor: target placeholders, then subst.
//   - Metadata: a new decoded tree, target placeholders and subst on the
//     string leaves only (expandStepMetadata).
//   - Priority: the pointed-to integer is copied, so two expansions of one
//     template never share it.
//   - Gate, OnComplete: detached copies with every field verbatim. Their
//     double-brace fields belong to the cook/pour-time substitution, and
//     OnComplete's {item}/{index} are runtime bindings, none of which
//     expansion-time vars may touch.
//   - Condition: verbatim, without even target replacement. It is a
//     double-brace expression FilterStepsByCondition evaluates at
//     cook/pour time against the enclosing formula's vars; rewriting inside
//     its braces would corrupt it.
//   - Children: expanded recursively, as before.
func expandStepWith(target *Step, template []*Step, depth int, subst func(string) string) ([]*Step, error) {
	if depth > DefaultMaxExpansionDepth {
		return nil, fmt.Errorf("expansion depth limit exceeded: max %d levels (currently at %d) - step %q",
			DefaultMaxExpansionDepth, depth, target.ID)
	}

	result := make([]*Step, 0, len(template))

	for _, tmpl := range template {
		expanded := *tmpl

		expanded.ID = subst(substituteTargetPlaceholders(tmpl.ID, target))
		expanded.Title = subst(substituteTargetPlaceholders(tmpl.Title, target))
		expanded.Description = subst(substituteTargetPlaceholders(tmpl.Description, target))
		expanded.Notes = subst(substituteTargetPlaceholders(tmpl.Notes, target))
		expanded.Assignee = subst(tmpl.Assignee)
		expanded.WaitsFor = subst(substituteTargetPlaceholders(tmpl.WaitsFor, target))

		expanded.Labels = substituteStepRefs(tmpl.Labels, target, subst)
		expanded.DependsOn = substituteStepRefs(tmpl.DependsOn, target, subst)
		expanded.Needs = substituteStepRefs(tmpl.Needs, target, subst)
		expanded.generatedNeeds = substituteStepRefs(tmpl.generatedNeeds, target, subst)

		if tmpl.Metadata != nil {
			metadata, err := expandStepMetadata(tmpl.Metadata, target, 0, subst)
			if err != nil {
				return nil, fmt.Errorf("template step %q: %w", tmpl.ID, err)
			}
			// tmpl.Metadata is map[string]interface{} by type, so the
			// recursive copy below returns that same shape for it.
			expanded.Metadata = metadata.(map[string]interface{})
		}

		if tmpl.Priority != nil {
			priority := *tmpl.Priority
			expanded.Priority = &priority
		}

		expanded.Gate = cloneGate(tmpl.Gate)
		expanded.OnComplete = cloneOnComplete(tmpl.OnComplete)

		// Handle children recursively with depth tracking
		if len(tmpl.Children) > 0 {
			children, err := expandStepWith(target, tmpl.Children, depth+1, subst)
			if err != nil {
				return nil, err
			}
			expanded.Children = children
		}

		result = append(result, &expanded)
	}

	return result, nil
}

// substituteStepRefs returns a fresh slice with {target} placeholders and
// then expansion vars substituted into each entry. A nil slice stays nil,
// and a non-nil slice of any length - including empty - comes back detached
// from the input, so expanding a template twice never shares backing
// arrays. The copy is always fresh even for entries without placeholders,
// because the caller may mutate the result's slice fields.
func substituteStepRefs(src []string, target *Step, subst func(string) string) []string {
	if src == nil {
		return nil
	}
	out := make([]string, len(src))
	for i, s := range src {
		out[i] = subst(substituteTargetPlaceholders(s, target))
	}
	return out
}

// expandStepMetadata returns a new decoded metadata tree with {target}
// placeholders and expansion vars substituted into every string leaf. Map
// keys and non-string scalars are carried verbatim: keys are never rewritten
// (they are identifiers, not prose), and numbers and booleans are the
// immutable values TOML/JSON decoding produced.
//
// The recursion is bounded at maxMetadataScanDepth, like metadataStrings:
// metadata is arbitrary user-supplied nesting, and the alternatives at the
// bound are worse than an error - sharing the tail would alias template and
// expansion, silently dropping it would lose data.
func expandStepMetadata(v interface{}, target *Step, depth int, subst func(string) string) (interface{}, error) {
	if depth >= maxMetadataScanDepth {
		return nil, fmt.Errorf("metadata nested deeper than %d levels: expansion refuses to share or drop the tail", maxMetadataScanDepth)
	}
	switch val := v.(type) {
	case string:
		return subst(substituteTargetPlaceholders(val, target)), nil
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, elem := range val {
			copied, err := expandStepMetadata(elem, target, depth+1, subst)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", k, err)
			}
			out[k] = copied
		}
		return out, nil
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, elem := range val {
			copied, err := expandStepMetadata(elem, target, depth+1, subst)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			out[i] = copied
		}
		return out, nil
	default:
		return val, nil
	}
}

// cloneGate returns a detached copy of a step's gate, or nil. Every field is
// verbatim: the double-brace fields a gate carries are substituted at
// cook/pour time, and expansion-time single-brace vars must not touch them.
func cloneGate(g *Gate) *Gate {
	if g == nil {
		return nil
	}
	return &Gate{
		Type:    g.Type,
		ID:      g.ID,
		AwaitID: g.AwaitID,
		Timeout: g.Timeout,
		Repo:    g.Repo,
	}
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
		if def.Default != nil {
			result[name] = *def.Default
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
		for j, need := range clone.generatedNeeds {
			if need == expandedID {
				clone.generatedNeeds[j] = lastExpandedStepID
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

// propagateTargetDeps copies the target step's Needs and DependsOn to the root
// steps of an expansion. Root steps are those whose existing dependencies only
// reference other steps within the expansion (i.e., they have no external deps
// from the template). This preserves cross-expansion dependency chains that would
// otherwise be lost when the target step is replaced.
func propagateTargetDeps(target *Step, expandedSteps []*Step) {
	if len(target.Needs) == 0 && len(target.DependsOn) == 0 {
		return
	}

	expandedIDs := make(map[string]bool, len(expandedSteps))
	for _, s := range expandedSteps {
		expandedIDs[s.ID] = true
	}

	for _, s := range expandedSteps {
		isRoot := true
		for _, n := range s.Needs {
			if expandedIDs[n] {
				isRoot = false
				break
			}
		}
		if isRoot {
			for _, d := range s.DependsOn {
				if expandedIDs[d] {
					isRoot = false
					break
				}
			}
		}
		if isRoot {
			// Prepend target's deps (new slice to avoid aliasing)
			if len(target.Needs) > 0 {
				prependTargetNeeds(s, target)
			}
			if len(target.DependsOn) > 0 {
				s.DependsOn = append(append([]string{}, target.DependsOn...), s.DependsOn...)
			}
		}
	}
}

// prependTargetNeeds puts target's needs in front of root's (in a new slice,
// to avoid aliasing). A need that a transform added to target stays generated
// on root, unless root's own template already needs that step.
func prependTargetNeeds(root, target *Step) {
	for _, ref := range target.generatedNeeds {
		if !slices.Contains(root.Needs, ref) {
			root.generatedNeeds = append(root.generatedNeeds, ref)
		}
	}
	root.Needs = append(append([]string{}, target.Needs...), root.Needs...)
}

// MaterializeExpansion converts a standalone expansion formula into a cookable
// form by expanding its Template into Steps. A synthetic target step is created
// using targetID as the step ID and the formula's own name/description for
// {target.title} and {target.description} placeholders.
//
// This enables expansion formulas to be directly instantiated via wisp/pour
// without requiring a Compose wrapper (bd-qzb).
//
// No-op if the formula is not an expansion type, has no Template, or already
// has Steps.
func MaterializeExpansion(f *Formula, targetID string, vars map[string]string) error {
	if f.Type != TypeExpansion || len(f.Template) == 0 || len(f.Steps) > 0 {
		return nil
	}

	expandedSteps, err := expandStep(materializeTarget(f, targetID), f.Template, vars)
	if err != nil {
		return fmt.Errorf("materializing expansion %q: %w", f.Formula, err)
	}

	f.Steps = expandedSteps
	return nil
}

// materializeTarget is the synthetic target step a standalone expansion
// formula's template is expanded against.
func materializeTarget(f *Formula, targetID string) *Step {
	return &Step{
		ID:          targetID,
		Title:       f.Formula,
		Description: f.Description,
	}
}

// ExpansionVarRefs returns the names of the {varname} placeholders that
// MaterializeExpansion(f, targetID, vars) substitutes from vars, deduplicated
// and sorted; nil when f is not an expansion formula with a Template.
//
// Those names are consumed while the template is expanded, so none of them
// survives into the steps MaterializeExpansion produces: a caller that needs
// to know which variables a standalone expansion formula can consume has to
// ask before materializing it. The answer comes from running that same
// expansion with a substitution that records names instead of replacing them,
// so it covers every field the expansion substitutes into - including a name
// that {target.description} pulls in from the formula's own description.
//
// Unlike MaterializeExpansion, this does not skip a formula that already has
// Steps: FilterStepsByCondition can remove all of them first, and then the
// template is materialized after all, so the names are reported either way.
func ExpansionVarRefs(f *Formula, targetID string) []string {
	if f.Type != TypeExpansion || len(f.Template) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	record := func(s string) string {
		for _, m := range rangeVarPattern.FindAllStringSubmatch(s, -1) {
			seen[m[1]] = true
		}
		return s
	}
	if _, err := expandStepWith(materializeTarget(f, targetID), f.Template, 0, record); err != nil {
		// The template cannot be expanded at all; MaterializeExpansion
		// reports why.
		return nil
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
			expandedSteps, err := expandStep(step, expFormula.Template, vars)
			if err != nil {
				return nil, fmt.Errorf("inline expand on step %q: %w", step.ID, err)
			}

			// Propagate the original step's dependencies to root steps of the expansion
			propagateTargetDeps(step, expandedSteps)

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
