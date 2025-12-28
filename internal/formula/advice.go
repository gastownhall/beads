// Package formula provides advice operators for step transformations.
//
// Advice operators are Lisp-style transformations that insert steps
// before, after, or around matching target steps. They enable
// cross-cutting concerns like logging, security scanning, or
// approval gates to be applied declaratively.
//
// Supported patterns:
//   - "design" - exact match
//   - "*.implement" - suffix match (any step ending in .implement)
//   - "shiny.*" - prefix match (any step starting with shiny.)
//   - "*" - match all steps
package formula

import (
	"path/filepath"
	"strings"
)

// MatchGlob checks if a step ID matches a glob pattern.
// Supported patterns:
//   - "exact" - exact match
//   - "*.suffix" - ends with .suffix
//   - "prefix.*" - starts with prefix.
//   - "*" - matches everything
//   - "prefix.*.suffix" - starts with prefix. and ends with .suffix
func MatchGlob(pattern, stepID string) bool {
	// Use filepath.Match for basic glob support
	matched, err := filepath.Match(pattern, stepID)
	if err == nil && matched {
		return true
	}

	// Handle additional patterns
	if pattern == "*" {
		return true
	}

	// *.suffix pattern (e.g., "*.implement")
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".implement"
		return strings.HasSuffix(stepID, suffix)
	}

	// prefix.* pattern (e.g., "shiny.*")
	if strings.HasSuffix(pattern, ".*") {
		prefix := pattern[:len(pattern)-1] // "shiny."
		return strings.HasPrefix(stepID, prefix)
	}

	// Exact match
	return pattern == stepID
}

// ApplyAdvice transforms a formula's steps by applying advice rules.
// Returns a new steps slice with advice steps inserted.
// The original steps slice is not modified.
//
// Self-matching prevention: Advice only matches steps that
// existed BEFORE this call. Steps inserted by advice (before/after/around)
// are not matched, preventing infinite recursion.
func ApplyAdvice(steps []*Step, advice []*AdviceRule) []*Step {
	if len(advice) == 0 {
		return steps
	}

	// Collect original step IDs to prevent self-matching
	originalIDs := collectStepIDs(steps)

	return applyAdviceWithGuard(steps, advice, originalIDs)
}

// ApplyAdviceToOriginalOnly applies advice rules but only matches steps
// whose IDs are in the originalIDs set. This prevents aspects from matching
// steps they themselves inserted.
func applyAdviceWithGuard(steps []*Step, advice []*AdviceRule, originalIDs map[string]bool) []*Step {
	result := make([]*Step, 0, len(steps)*2) // Pre-allocate for insertions

	for _, step := range steps {
		// Skip steps not in original set
		if !originalIDs[step.ID] {
			result = append(result, step)
			continue
		}
		// Find matching advice rules for this step
		var beforeSteps []*Step
		var afterSteps []*Step

		for _, rule := range advice {
			if !MatchGlob(rule.Target, step.ID) {
				continue
			}

			// Collect before steps
			if rule.Before != nil {
				beforeSteps = append(beforeSteps, adviceStepToStep(rule.Before, step))
			}
			if rule.Around != nil {
				for _, as := range rule.Around.Before {
					beforeSteps = append(beforeSteps, adviceStepToStep(as, step))
				}
			}

			// Collect after steps
			if rule.After != nil {
				afterSteps = append(afterSteps, adviceStepToStep(rule.After, step))
			}
			if rule.Around != nil {
				for _, as := range rule.Around.After {
					afterSteps = append(afterSteps, adviceStepToStep(as, step))
				}
			}
		}

		// Insert before steps
		for _, bs := range beforeSteps {
			result = append(result, bs)
		}

		// Clone the original step and update its dependencies
		clonedStep := cloneStep(step)

		// If there are before steps, the original step needs to depend on the last before step
		if len(beforeSteps) > 0 {
			lastBefore := beforeSteps[len(beforeSteps)-1]
			clonedStep.Needs = appendUnique(clonedStep.Needs, lastBefore.ID)
		}

		// Chain before steps together
		for i := 1; i < len(beforeSteps); i++ {
			beforeSteps[i].Needs = appendUnique(beforeSteps[i].Needs, beforeSteps[i-1].ID)
		}

		result = append(result, clonedStep)

		// Insert after steps and chain them
		for i, as := range afterSteps {
			if i == 0 {
				// First after step depends on the original step
				as.Needs = appendUnique(as.Needs, step.ID)
			} else {
				// Subsequent after steps chain to previous
				as.Needs = appendUnique(as.Needs, afterSteps[i-1].ID)
			}
			result = append(result, as)
		}

		// Recursively apply advice to children
		if len(step.Children) > 0 {
			clonedStep.Children = ApplyAdvice(step.Children, advice)
		}
	}

	return result
}

// adviceStepToStep converts an AdviceStep to a Step.
// Substitutes {step.id} placeholders with the target step's ID.
func adviceStepToStep(as *AdviceStep, target *Step) *Step {
	// Substitute {step.id} in ID and Title
	id := substituteStepRef(as.ID, target)
	title := substituteStepRef(as.Title, target)
	if title == "" {
		title = id
	}
	desc := substituteStepRef(as.Description, target)

	return &Step{
		ID:            id,
		Title:         title,
		Description:   desc,
		Type:          as.Type,
		SourceFormula: target.SourceFormula, // Inherit source formula from target
		// SourceLocation will be "advice" to indicate this came from advice transformation
		SourceLocation: "advice",
	}
}

// substituteStepRef replaces {step.id} with the target step's ID.
func substituteStepRef(s string, target *Step) string {
	s = strings.ReplaceAll(s, "{step.id}", target.ID)
	s = strings.ReplaceAll(s, "{step.title}", target.Title)
	return s
}

// cloneStep creates a shallow copy of a step.
func cloneStep(s *Step) *Step {
	clone := *s
	// Deep copy slices
	if len(s.DependsOn) > 0 {
		clone.DependsOn = make([]string, len(s.DependsOn))
		copy(clone.DependsOn, s.DependsOn)
	}
	if len(s.Needs) > 0 {
		clone.Needs = make([]string, len(s.Needs))
		copy(clone.Needs, s.Needs)
	}
	if len(s.Labels) > 0 {
		clone.Labels = make([]string, len(s.Labels))
		copy(clone.Labels, s.Labels)
	}
	// Deep copy OnComplete if present
	if s.OnComplete != nil {
		clone.OnComplete = cloneOnComplete(s.OnComplete)
	}
	// Don't deep copy children here - ApplyAdvice handles that recursively
	return &clone
}

// cloneOnComplete creates a deep copy of an OnCompleteSpec.
func cloneOnComplete(oc *OnCompleteSpec) *OnCompleteSpec {
	if oc == nil {
		return nil
	}
	clone := *oc
	if len(oc.Vars) > 0 {
		clone.Vars = make(map[string]string, len(oc.Vars))
		for k, v := range oc.Vars {
			clone.Vars[k] = v
		}
	}
	return &clone
}

// appendUnique appends an item to a slice if not already present.
func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

// collectStepIDs returns a set of all step IDs (including nested children).
// Used by ApplyAdvice to prevent self-matching.
func collectStepIDs(steps []*Step) map[string]bool {
	ids := make(map[string]bool)
	var collect func([]*Step)
	collect = func(steps []*Step) {
		for _, step := range steps {
			ids[step.ID] = true
			if len(step.Children) > 0 {
				collect(step.Children)
			}
		}
	}
	collect(steps)
	return ids
}

// MatchPointcut checks if a step matches a pointcut.
func MatchPointcut(pc *Pointcut, step *Step) bool {
	// Glob match on step ID
	if pc.Glob != "" && !MatchGlob(pc.Glob, step.ID) {
		return false
	}

	// Type match
	if pc.Type != "" && step.Type != pc.Type {
		return false
	}

	// Label match
	if pc.Label != "" {
		found := false
		for _, l := range step.Labels {
			if l == pc.Label {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

// MatchAnyPointcut checks if a step matches any pointcut in the list.
func MatchAnyPointcut(pointcuts []*Pointcut, step *Step) bool {
	if len(pointcuts) == 0 {
		return true // No pointcuts means match all
	}
	for _, pc := range pointcuts {
		if MatchPointcut(pc, step) {
			return true
		}
	}
	return false
}
