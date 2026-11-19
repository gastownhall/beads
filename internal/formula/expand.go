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
	"regexp"
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
// The vars parameter provides variable values for {varname} substitution:
// the expansion formula's [vars] defaults overridden by a compose rule's vars
// or an inline step's expand_vars. The expanded steps' conditions read those
// values too (bindTemplateConditions).
func expandStep(target *Step, template []*Step, vars map[string]string) ([]*Step, error) {
	expanded, err := expandTemplate(target, template, vars)
	if err != nil {
		return nil, err
	}
	if err := bindTemplateConditions(expanded, vars); err != nil {
		return nil, err
	}
	return expanded, nil
}

// expandTemplate is expandStep without binding the conditions, for an
// expansion formula that is itself the poured formula (MaterializeExpansion):
// its variables are the poured ones, so the cook evaluates its conditions.
func expandTemplate(target *Step, template []*Step, vars map[string]string) ([]*Step, error) {
	return expandStepWith(target, template, 0, func(s string) string {
		return substituteExpansionVars(s, vars)
	})
}

// expandStepWith is expandStep with the {varname} substitution supplied as
// subst, which receives every string the expansion substitutes variables into -
// after the {target} placeholders in it are replaced, where those apply. It
// exists so ExpansionVarRefs can see exactly those strings without keeping a
// second list of the fields that carry them.
func expandStepWith(target *Step, template []*Step, depth int, subst func(string) string) ([]*Step, error) {
	if depth > DefaultMaxExpansionDepth {
		return nil, fmt.Errorf("expansion depth limit exceeded: max %d levels (currently at %d) - step %q",
			DefaultMaxExpansionDepth, depth, target.ID)
	}

	result := make([]*Step, 0, len(template))

	for _, tmpl := range template {
		// Control flow (ApplyControlFlow) runs before expansions, so a loop
		// on a template step would never be expanded; refuse it rather than
		// drop it. ErrValidation: pour and wisp report a formula that does
		// not validate as it is, where any other cook error makes them say
		// it was not found.
		if tmpl.Loop != nil {
			return nil, fmt.Errorf("%w: template step %q declares a loop, which expansion does not run; move the loop onto the step that expands this template, or the loop body's steps into the template", ErrValidation, tmpl.ID)
		}

		// Start from a copy of the template step so that every field carries
		// through by default - a field added to Step later included - and
		// then rewrite the fields the expansion transforms. Anything that
		// holds mutable state is rebuilt below, so no expanded step shares
		// it with the template or with another expansion of it.
		expanded := *tmpl

		// Text fields: {target} placeholders first, then {var} substitution.
		// Neither touches a {{name}} reference, which the cook fills in
		// from the poured formula's variables.
		expandText := func(s string) string {
			return subst(substituteTargetPlaceholders(s, target))
		}
		expanded.ID = expandText(tmpl.ID)
		expanded.Title = expandText(tmpl.Title)
		expanded.Description = expandText(tmpl.Description)
		expanded.Notes = expandText(tmpl.Notes)
		expanded.Assignee = subst(tmpl.Assignee)
		if tmpl.Priority != nil {
			priority := *tmpl.Priority
			expanded.Priority = &priority
		}
		// waits_for can name its spawner (children-of({target}.spawn)), so it
		// resolves like needs; the bare all-children/any-children values
		// carry no placeholder and pass through unchanged.
		expanded.WaitsFor = expandText(tmpl.WaitsFor)

		expanded.Labels = expandStrings(tmpl.Labels, expandText)
		expanded.DependsOn = expandStrings(tmpl.DependsOn, expandText)
		expanded.Needs = expandStrings(tmpl.Needs, expandText)
		expanded.generatedNeeds = expandStrings(tmpl.generatedNeeds, expandText)
		expanded.generatedDependsOn = expandStrings(tmpl.generatedDependsOn, expandText)

		metadata, err := expandMetadata(tmpl.Metadata, expandText)
		if err != nil {
			return nil, fmt.Errorf("template step %q: %w", tmpl.ID, err)
		}
		expanded.Metadata = metadata

		// The condition is carried verbatim: it is a {{var}} expression, not
		// text. expandStep then decides it from the expansion's value where
		// the expansion binds its variable (bindTemplateConditions); any
		// other is evaluated against the poured formula's variables.
		//
		// The gate gets its own copy, with its text fields expanded like
		// the step's (so a compose.map gate can name its target); the cook
		// fills in their {{name}} references later.
		if tmpl.Gate != nil {
			gate := *tmpl.Gate
			gate.ID = expandText(gate.ID)
			gate.AwaitID = expandText(gate.AwaitID)
			gate.Timeout = expandText(gate.Timeout)
			gate.Repo = expandText(gate.Repo)
			expanded.Gate = &gate
		}
		// on_complete is copied as written, in its own copy. bd does not run
		// it (docs/workflows/molecules.md), so it changes nothing in what is
		// poured.
		expanded.OnComplete = cloneOnComplete(tmpl.OnComplete)

		// Nested template expansion is deliberately disabled: a template
		// step's expand/expand_vars are cleared, so an expanded step is
		// never expanded again. Only the inline-expansion path would act on
		// them, and the compose and standalone paths never would, so
		// carrying them would make nested expansion depend on how the
		// template was reached. applyInlineExpansionsRecursive still walks
		// the steps an inline expansion returns, and with these cleared it
		// finds nothing there to expand.
		expanded.Expand = ""
		expanded.ExpandVars = nil

		// Handle children recursively with depth tracking
		expanded.Children = nil
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

// expandStrings returns a new slice holding expand applied to each element
// of ss; nil stays nil and empty stays empty.
func expandStrings(ss []string, expand func(string) string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = expand(s)
	}
	return out
}

// expandMetadata returns a copy of a template step's decoded metadata with
// expand applied to every string leaf. Keys and non-string scalars are kept
// as they are, and every map and list is a new one, so the result shares no
// mutable state with the template. Nesting is bounded like metadataStrings
// (the root map is depth zero): a value at maxMetadataScanDepth or deeper is
// an error rather than being dropped or shared.
func expandMetadata(m map[string]interface{}, expand func(string) string) (map[string]interface{}, error) {
	if m == nil {
		return nil, nil
	}
	out, err := expandMetadataValue(m, expand, 0)
	if err != nil {
		return nil, err
	}
	return out.(map[string]interface{}), nil
}

func expandMetadataValue(v interface{}, expand func(string) string, depth int) (interface{}, error) {
	if depth >= maxMetadataScanDepth {
		return nil, fmt.Errorf("%w: metadata is nested deeper than %d levels", ErrValidation, maxMetadataScanDepth)
	}
	switch val := v.(type) {
	case string:
		return expand(val), nil
	case map[string]interface{}:
		if val == nil {
			return val, nil
		}
		out := make(map[string]interface{}, len(val))
		for k, elem := range val {
			e, err := expandMetadataValue(elem, expand, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = e
		}
		return out, nil
	case []interface{}:
		if val == nil {
			return val, nil
		}
		out := make([]interface{}, len(val))
		for i, elem := range val {
			e, err := expandMetadataValue(elem, expand, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = e
		}
		return out, nil
	case []map[string]interface{}:
		// The TOML decoder's shape for an array of tables.
		if val == nil {
			return val, nil
		}
		out := make([]map[string]interface{}, len(val))
		for i, elem := range val {
			e, err := expandMetadataValue(elem, expand, depth+1)
			if err != nil {
				return nil, err
			}
			out[i], _ = e.(map[string]interface{})
		}
		return out, nil
	default:
		return v, nil
	}
}

// conditionBinding is the value an expansion bound a template condition's
// variable to, where that value made the condition false (see
// bindTemplateConditions).
type conditionBinding struct {
	condition string // the condition as the template wrote it
	variable  string
	value     string
	expansion string // the expansion formula's name, "" when not known
}

func (b *conditionBinding) String() string {
	if b.expansion == "" {
		return fmt.Sprintf("the expansion sets %s = %q", b.variable, b.value)
	}
	return fmt.Sprintf("expansion %q sets %s = %q", b.expansion, b.variable, b.value)
}

// conditionPassThroughPattern matches a bound value that hands the poured
// formula's variable through to the expansion: exactly "{{name}}".
var conditionPassThroughPattern = regexp.MustCompile(`^\{\{(\w+)\}\}$`)

// bindTemplateConditions makes each expanded step's condition (children
// included) read the expansion's value for a variable the expansion binds:
// vars, its [vars] defaults overridden by a compose rule's vars or an inline
// step's expand_vars, the values its {name} placeholders get. Gas City's
// engine binds template conditions the same way
// (materializeExpandedStepConditions). By its value:
//
//   - a value with no brace in it decides the condition now. A true
//     condition is cleared; a false one is cleared and marks the step
//     excludedBy, so that FilterStepsByCondition drops it at the usual point
//     of the cook and routes or refuses the references to it there;
//   - "{{name}}" passes the poured formula's variable through: the condition
//     is rewritten to test name, and the cook evaluates it;
//   - any other value with a brace in it is refused (ErrValidation). The
//     expansion fills {name} placeholders only in template text, never in a
//     value, so the condition would test the placeholder's own text.
//
// A condition on a variable the expansion does not bind is left as it is,
// for the cook to evaluate against the poured formula's variables, as is one
// that EvaluateStepCondition cannot read, which the cook then reports.
func bindTemplateConditions(steps []*Step, vars map[string]string) error {
	for _, step := range steps {
		if err := bindTemplateCondition(step, vars); err != nil {
			return err
		}
		if err := bindTemplateConditions(step.Children, vars); err != nil {
			return err
		}
	}
	return nil
}

func bindTemplateCondition(step *Step, vars map[string]string) error {
	name := stepConditionVar(step.Condition)
	if name == "" {
		return nil
	}
	value, bound := vars[name]
	if !bound {
		return nil
	}
	if m := conditionPassThroughPattern.FindStringSubmatch(value); m != nil {
		step.Condition = strings.Replace(step.Condition, "{{"+name+"}}", "{{"+m[1]+"}}", 1)
		return nil
	}
	if strings.ContainsAny(value, "{}") {
		return fmt.Errorf("%w: step %q: condition %q tests %s, which %s sets to %q: a condition reads a bound value as it is, so to test a variable of the formula that pours the expansion, bind %s to \"{{name}}\"",
			ErrValidation, step.ID, step.Condition, name, expansionName(step), value, name)
	}
	include, err := EvaluateStepCondition(step.Condition, map[string]string{name: value})
	if err != nil {
		return fmt.Errorf("template step %q: %w", step.ID, err)
	}
	if !include {
		step.excludedBy = &conditionBinding{
			condition: step.Condition,
			variable:  name,
			value:     value,
			expansion: step.SourceFormula,
		}
	}
	step.Condition = ""
	return nil
}

// expansionName names the expansion that built step, for an error.
func expansionName(step *Step) string {
	if step.SourceFormula == "" {
		return "the expansion"
	}
	return fmt.Sprintf("expansion %q", step.SourceFormula)
}

// substituteTargetPlaceholders replaces {target} and {target.*} placeholders.
func substituteTargetPlaceholders(s string, target *Step) string {
	return replaceExpansionPlaceholders(s, func(name string) (string, bool) {
		switch name {
		case "target", "target.id":
			return target.ID, true
		case "target.title":
			return target.Title, true
		case "target.description":
			return target.Description, true
		}
		return "", false
	})
}

// substituteExpansionVars replaces {name} placeholders with the expansion's
// variable values, leaving a placeholder it has no value for as it is.
func substituteExpansionVars(s string, vars map[string]string) string {
	if len(vars) == 0 {
		return s
	}
	return replaceExpansionPlaceholders(s, func(name string) (string, bool) {
		if !isExpansionVarName(name) {
			return "", false
		}
		value, ok := vars[name]
		return value, ok
	})
}

// isExpansionVarName reports whether a placeholder's name can be a variable:
// {target.title} and the like are target placeholders, never variables.
func isExpansionVarName(name string) bool {
	return !strings.Contains(name, ".")
}

// expansionPlaceholderPattern matches a single-brace placeholder ({name},
// {target.title}) and, as a whole, a double-brace {{name}} reference. The
// double-brace form belongs to the cook, which fills it in from the poured
// formula's variables; matching it whole is what keeps the {name} inside it
// from being replaced, which would leave a {value} that neither stage reads.
var expansionPlaceholderPattern = regexp.MustCompile(`\{\{[^{}]*\}\}|\{([\w.]+)\}`)

// replaceExpansionPlaceholders replaces each single-brace placeholder in s
// that lookup has a value for. Double-brace references are left as they are.
func replaceExpansionPlaceholders(s string, lookup func(name string) (string, bool)) string {
	if !strings.Contains(s, "{") {
		return s
	}
	return expansionPlaceholderPattern.ReplaceAllStringFunc(s, func(match string) string {
		if strings.HasPrefix(match, "{{") {
			return match
		}
		if value, ok := lookup(match[1 : len(match)-1]); ok {
			return value
		}
		return match
	})
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
//
// The rewritten edge stands in for the whole expansion, a step the formula
// never named, so it is recorded as generated (generatedNeeds,
// generatedDependsOn): when a condition drops the last step,
// FilterStepsByCondition routes the edge to the steps that one depends on
// instead of refusing the pour. That holds also when the last step takes X's
// own ID (a template step with id = "{target}"). An edge that the formula
// wrote onto the last step itself stays the formula's, and the rewritten one
// merges into it.
func UpdateDependenciesForExpansion(steps []*Step, expandedID string, lastExpandedStepID string) []*Step {
	result := make([]*Step, len(steps))

	for i, step := range steps {
		clone := cloneStep(step)

		clone.DependsOn, clone.generatedDependsOn = retargetExpansionRefs(clone.DependsOn, clone.generatedDependsOn, expandedID, lastExpandedStepID)
		clone.Needs, clone.generatedNeeds = retargetExpansionRefs(clone.Needs, clone.generatedNeeds, expandedID, lastExpandedStepID)

		// Handle children recursively
		if len(step.Children) > 0 {
			clone.Children = UpdateDependenciesForExpansion(step.Children, expandedID, lastExpandedStepID)
		}

		result[i] = clone
	}

	return result
}

// retargetExpansionRefs rewrites each of refs that names expandedID to name
// last, and returns the new refs with generated, the subset of them a
// transform added, updated to match (see UpdateDependenciesForExpansion). It
// returns its arguments unchanged when no ref names expandedID.
func retargetExpansionRefs(refs, generated []string, expandedID, last string) ([]string, []string) {
	if !slices.Contains(refs, expandedID) {
		return refs, generated
	}
	formulaWroteLast := last != expandedID && slices.Contains(refs, last) && !slices.Contains(generated, last)

	newRefs := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref == expandedID {
			ref = last
		}
		if ref == last && slices.Contains(newRefs, last) {
			continue
		}
		newRefs = append(newRefs, ref)
	}

	newGenerated := make([]string, 0, len(generated)+1)
	for _, ref := range generated {
		if ref != expandedID && ref != last {
			newGenerated = append(newGenerated, ref)
		}
	}
	if !formulaWroteLast {
		newGenerated = append(newGenerated, last)
	}
	return newRefs, newGenerated
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
				prependTargetDependsOn(s, target)
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

// prependTargetDependsOn is prependTargetNeeds for depends_on.
func prependTargetDependsOn(root, target *Step) {
	for _, ref := range target.generatedDependsOn {
		if !slices.Contains(root.DependsOn, ref) {
			root.generatedDependsOn = append(root.generatedDependsOn, ref)
		}
	}
	root.DependsOn = append(append([]string{}, target.DependsOn...), root.DependsOn...)
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

	// The formula is the poured one, so vars are the poured variables and
	// the cook evaluates its conditions against them: not expandStep, which
	// would decide them now from the same values.
	expandedSteps, err := expandTemplate(materializeTarget(f, targetID), f.Template, vars)
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
		replaceExpansionPlaceholders(s, func(name string) (string, bool) {
			if isExpansionVarName(name) {
				seen[name] = true
			}
			return "", false
		})
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
