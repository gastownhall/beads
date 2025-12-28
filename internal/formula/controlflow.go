// Package formula provides control flow operators for step transformation.
//
// Control flow operators enable:
//   - loop: Repeat a body of steps (fixed count or conditional)
//   - branch: Fork-join parallel execution patterns
//   - gate: Conditional waits before steps proceed
//
// These operators are applied during formula cooking to transform
// the step graph before creating the proto bead.
package formula

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ApplyLoops expands loop bodies in a formula's steps.
// Fixed-count loops expand the body N times with indexed step IDs.
// Conditional loops expand once and add a "loop:until" label for runtime evaluation.
// Returns a new steps slice with loops expanded.
func ApplyLoops(steps []*Step) ([]*Step, error) {
	result := make([]*Step, 0, len(steps))

	for _, step := range steps {
		if step.Loop == nil {
			// No loop - recursively process children
			clone := cloneStep(step)
			if len(step.Children) > 0 {
				children, err := ApplyLoops(step.Children)
				if err != nil {
					return nil, err
				}
				clone.Children = children
			}
			result = append(result, clone)
			continue
		}

		// Validate loop spec
		if err := validateLoopSpec(step.Loop, step.ID); err != nil {
			return nil, err
		}

		// Expand the loop
		expanded, err := expandLoop(step)
		if err != nil {
			return nil, err
		}
		result = append(result, expanded...)
	}

	return result, nil
}

// validateLoopSpec checks that a loop spec is valid.
func validateLoopSpec(loop *LoopSpec, stepID string) error {
	if len(loop.Body) == 0 {
		return fmt.Errorf("loop %q: body is required", stepID)
	}

	// Count the number of loop types specified
	loopTypes := 0
	if loop.Count > 0 {
		loopTypes++
	}
	if loop.Until != "" {
		loopTypes++
	}
	if loop.Range != "" {
		loopTypes++
	}

	if loopTypes == 0 {
		return fmt.Errorf("loop %q: one of count, until, or range is required", stepID)
	}
	if loopTypes > 1 {
		return fmt.Errorf("loop %q: only one of count, until, or range can be specified", stepID)
	}

	if loop.Until != "" && loop.Max == 0 {
		return fmt.Errorf("loop %q: max is required when until is set", stepID)
	}

	if loop.Count < 0 {
		return fmt.Errorf("loop %q: count must be positive", stepID)
	}

	if loop.Max < 0 {
		return fmt.Errorf("loop %q: max must be positive", stepID)
	}

	// Validate until condition syntax if present
	if loop.Until != "" {
		if _, err := ParseCondition(loop.Until); err != nil {
			return fmt.Errorf("loop %q: invalid until condition %q: %w", stepID, loop.Until, err)
		}
	}

	// Validate range syntax if present
	if loop.Range != "" {
		if err := ValidateRange(loop.Range); err != nil {
			return fmt.Errorf("loop %q: invalid range %q: %w", stepID, loop.Range, err)
		}
	}

	return nil
}

// expandLoop expands a loop step into its constituent steps.
func expandLoop(step *Step) ([]*Step, error) {
	return expandLoopWithVars(step, nil)
}

// expandLoopWithVars expands a loop step using the given variable context.
// The vars map is used to resolve range expressions with variables.
func expandLoopWithVars(step *Step, vars map[string]string) ([]*Step, error) {
	var result []*Step

	if step.Loop.Count > 0 {
		// Fixed-count loop: expand body N times
		for i := 1; i <= step.Loop.Count; i++ {
			iterSteps, err := expandLoopIteration(step, i, nil)
			if err != nil {
				return nil, err
			}
			result = append(result, iterSteps...)
		}

		// Recursively expand any nested loops FIRST
		var err error
		result, err = ApplyLoops(result)
		if err != nil {
			return nil, err
		}

		// THEN chain iterations on the expanded result
		// This must happen AFTER recursive expansion so we chain the final steps
		if step.Loop.Count > 1 {
			result = chainExpandedIterations(result, step.ID, step.Loop.Count)
		}
	} else if step.Loop.Range != "" {
		// Range loop: expand body for each value in the computed range
		rangeSpec, err := ParseRange(step.Loop.Range, vars)
		if err != nil {
			return nil, fmt.Errorf("loop %q: %w", step.ID, err)
		}

		// Validate range
		if rangeSpec.End < rangeSpec.Start {
			return nil, fmt.Errorf("loop %q: range end (%d) is less than start (%d)",
				step.ID, rangeSpec.End, rangeSpec.Start)
		}

		// Expand body for each value in range
		count := rangeSpec.End - rangeSpec.Start + 1
		iterNum := 0
		for val := rangeSpec.Start; val <= rangeSpec.End; val++ {
			iterNum++
			// Build iteration vars: include the loop variable if specified
			iterVars := make(map[string]string)
			if step.Loop.Var != "" {
				iterVars[step.Loop.Var] = fmt.Sprintf("%d", val)
			}
			iterSteps, err := expandLoopIteration(step, iterNum, iterVars)
			if err != nil {
				return nil, err
			}
			result = append(result, iterSteps...)
		}

		// Recursively expand any nested loops FIRST
		result, err = ApplyLoops(result)
		if err != nil {
			return nil, err
		}

		// THEN chain iterations on the expanded result
		if count > 1 {
			result = chainExpandedIterations(result, step.ID, count)
		}
	} else {
		// Conditional loop: expand once with loop metadata
		// The runtime executor will re-run until condition is met or max reached
		iterSteps, err := expandLoopIteration(step, 1, nil)
		if err != nil {
			return nil, err
		}

		// Add loop metadata to first step for runtime evaluation
		if len(iterSteps) > 0 {
			firstStep := iterSteps[0]
			// Add labels for runtime loop control using JSON for unambiguous parsing
			loopMeta := map[string]interface{}{
				"until": step.Loop.Until,
				"max":   step.Loop.Max,
			}
			loopJSON, _ := json.Marshal(loopMeta)
			firstStep.Labels = append(firstStep.Labels, fmt.Sprintf("loop:%s", string(loopJSON)))
		}

		// Recursively expand any nested loops
		result, err = ApplyLoops(iterSteps)
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

// expandLoopIteration expands a single iteration of a loop.
// The iteration index is used to generate unique step IDs.
// The iterVars map contains loop variable bindings for this iteration.
//
//nolint:unparam // error return kept for API consistency with future error handling
func expandLoopIteration(step *Step, iteration int, iterVars map[string]string) ([]*Step, error) {
	result := make([]*Step, 0, len(step.Loop.Body))

	// Build set of step IDs within the loop body (for dependency rewriting)
	bodyStepIDs := collectBodyStepIDs(step.Loop.Body)

	for _, bodyStep := range step.Loop.Body {
		// Create unique ID for this iteration
		iterID := fmt.Sprintf("%s.iter%d.%s", step.ID, iteration, bodyStep.ID)

		// Substitute loop variables in title and description
		title := substituteLoopVars(bodyStep.Title, iterVars)
		description := substituteLoopVars(bodyStep.Description, iterVars)

		clone := &Step{
			ID:             iterID,
			Title:          title,
			Description:    description,
			Type:           bodyStep.Type,
			Priority:       bodyStep.Priority,
			Assignee:       bodyStep.Assignee,
			Condition:      bodyStep.Condition,
			WaitsFor:       bodyStep.WaitsFor,
			Expand:         bodyStep.Expand,
			Gate:           bodyStep.Gate,
			Loop:           cloneLoopSpec(bodyStep.Loop), // Support nested loops
			OnComplete:     cloneOnComplete(bodyStep.OnComplete),
			SourceFormula:  bodyStep.SourceFormula,                                       // Preserve source
			SourceLocation: fmt.Sprintf("%s.iter%d", bodyStep.SourceLocation, iteration), // Track iteration
		}

		// Clone ExpandVars if present, adding loop vars
		if len(bodyStep.ExpandVars) > 0 || len(iterVars) > 0 {
			clone.ExpandVars = make(map[string]string)
			for k, v := range bodyStep.ExpandVars {
				clone.ExpandVars[k] = v
			}
			// Add loop variables to ExpandVars for nested expansion
			for k, v := range iterVars {
				clone.ExpandVars[k] = v
			}
		}

		// Clone labels
		if len(bodyStep.Labels) > 0 {
			clone.Labels = make([]string, len(bodyStep.Labels))
			copy(clone.Labels, bodyStep.Labels)
		}

		// Clone dependencies - only prefix references to steps WITHIN the loop body
		clone.DependsOn = rewriteLoopDependencies(bodyStep.DependsOn, step.ID, iteration, bodyStepIDs)
		clone.Needs = rewriteLoopDependencies(bodyStep.Needs, step.ID, iteration, bodyStepIDs)

		// Recursively handle children with proper dependency rewriting
		if len(bodyStep.Children) > 0 {
			clone.Children = expandLoopChildren(bodyStep.Children, step.ID, iteration, bodyStepIDs)
		}

		result = append(result, clone)
	}

	return result, nil
}

// substituteLoopVars replaces {varname} placeholders with values from vars map.
func substituteLoopVars(s string, vars map[string]string) string {
	if vars == nil || s == "" {
		return s
	}
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// collectBodyStepIDs collects all step IDs within a loop body (including nested children).
func collectBodyStepIDs(body []*Step) map[string]bool {
	ids := make(map[string]bool)
	var collect func([]*Step)
	collect = func(steps []*Step) {
		for _, s := range steps {
			ids[s.ID] = true
			if len(s.Children) > 0 {
				collect(s.Children)
			}
		}
	}
	collect(body)
	return ids
}

// rewriteLoopDependencies rewrites dependency references for loop expansion.
// Only dependencies referencing steps WITHIN the loop body are prefixed.
// External dependencies are preserved as-is.
func rewriteLoopDependencies(deps []string, loopID string, iteration int, bodyStepIDs map[string]bool) []string {
	if len(deps) == 0 {
		return nil
	}

	result := make([]string, len(deps))
	for i, dep := range deps {
		if bodyStepIDs[dep] {
			// Internal dependency - prefix with iteration context
			result[i] = fmt.Sprintf("%s.iter%d.%s", loopID, iteration, dep)
		} else {
			// External dependency - preserve as-is
			result[i] = dep
		}
	}
	return result
}

// expandLoopChildren expands children within a loop iteration.
// Rewrites IDs and dependencies appropriately.
func expandLoopChildren(children []*Step, loopID string, iteration int, bodyStepIDs map[string]bool) []*Step {
	result := make([]*Step, len(children))
	for i, child := range children {
		clone := cloneStepDeep(child)
		clone.ID = fmt.Sprintf("%s.iter%d.%s", loopID, iteration, child.ID)
		clone.DependsOn = rewriteLoopDependencies(child.DependsOn, loopID, iteration, bodyStepIDs)
		clone.Needs = rewriteLoopDependencies(child.Needs, loopID, iteration, bodyStepIDs)

		// Recursively handle nested children
		if len(child.Children) > 0 {
			clone.Children = expandLoopChildren(child.Children, loopID, iteration, bodyStepIDs)
		}

		result[i] = clone
	}
	return result
}

// chainLoopIterations adds dependencies between loop iterations.
// Each iteration's first step depends on the previous iteration's last step.
func chainLoopIterations(steps []*Step, body []*Step, count int) []*Step {
	if len(body) == 0 || count < 2 {
		return steps
	}

	stepsPerIter := len(body)

	for iter := 2; iter <= count; iter++ {
		// First step of this iteration
		firstIdx := (iter - 1) * stepsPerIter
		// Last step of previous iteration
		lastStep := steps[(iter-2)*stepsPerIter+stepsPerIter-1]

		if firstIdx < len(steps) {
			steps[firstIdx].Needs = appendUnique(steps[firstIdx].Needs, lastStep.ID)
		}
	}

	return steps
}

// chainExpandedIterations chains iterations AFTER nested loop expansion.
// Unlike chainLoopIterations, this handles variable step counts per iteration
// by finding iteration boundaries via ID prefix matching.
func chainExpandedIterations(steps []*Step, loopID string, count int) []*Step {
	if len(steps) == 0 || count < 2 {
		return steps
	}

	// Find the first and last step index of each iteration
	// Iteration N has steps with ID prefix: {loopID}.iter{N}.
	iterFirstIdx := make(map[int]int) // iteration -> index of first step
	iterLastIdx := make(map[int]int)  // iteration -> index of last step

	for i, s := range steps {
		for iter := 1; iter <= count; iter++ {
			prefix := fmt.Sprintf("%s.iter%d.", loopID, iter)
			if strings.HasPrefix(s.ID, prefix) {
				if _, found := iterFirstIdx[iter]; !found {
					iterFirstIdx[iter] = i
				}
				iterLastIdx[iter] = i
				break
			}
		}
	}

	// Chain: first step of iteration N+1 depends on last step of iteration N
	for iter := 2; iter <= count; iter++ {
		firstIdx, hasFirst := iterFirstIdx[iter]
		prevLastIdx, hasPrevLast := iterLastIdx[iter-1]

		if hasFirst && hasPrevLast {
			lastStepID := steps[prevLastIdx].ID
			steps[firstIdx].Needs = appendUnique(steps[firstIdx].Needs, lastStepID)
		}
	}

	return steps
}

// ApplyBranches wires fork-join dependency patterns.
// For each branch rule:
//   - All branch steps depend on the 'from' step
//   - The 'join' step depends on all branch steps
//
// Returns a new steps slice with dependencies added.
// The original steps slice is not modified.
func ApplyBranches(steps []*Step, compose *ComposeRules) ([]*Step, error) {
	if compose == nil || len(compose.Branch) == 0 {
		return steps, nil
	}

	// Clone steps to avoid mutating input
	cloned := cloneStepsRecursive(steps)
	stepMap := buildStepMap(cloned)

	if err := applyBranchesWithMap(stepMap, compose); err != nil {
		return nil, err
	}

	return cloned, nil
}

// applyBranchesWithMap applies branch rules using a pre-built stepMap.
// This is the internal implementation used by both ApplyBranches and ApplyControlFlow.
// The stepMap entries are modified in place.
func applyBranchesWithMap(stepMap map[string]*Step, compose *ComposeRules) error {
	if compose == nil || len(compose.Branch) == 0 {
		return nil
	}

	for _, branch := range compose.Branch {
		// Validate the branch rule
		if branch.From == "" {
			return fmt.Errorf("branch: from is required")
		}
		if len(branch.Steps) == 0 {
			return fmt.Errorf("branch: steps is required")
		}
		if branch.Join == "" {
			return fmt.Errorf("branch: join is required")
		}

		// Verify all steps exist
		if _, ok := stepMap[branch.From]; !ok {
			return fmt.Errorf("branch: from step %q not found", branch.From)
		}
		if _, ok := stepMap[branch.Join]; !ok {
			return fmt.Errorf("branch: join step %q not found", branch.Join)
		}
		for _, stepID := range branch.Steps {
			if _, ok := stepMap[stepID]; !ok {
				return fmt.Errorf("branch: parallel step %q not found", stepID)
			}
		}

		// Add dependencies: branch steps depend on 'from'
		for _, stepID := range branch.Steps {
			step := stepMap[stepID]
			step.Needs = appendUnique(step.Needs, branch.From)
		}

		// Add dependencies: 'join' depends on all branch steps
		joinStep := stepMap[branch.Join]
		for _, stepID := range branch.Steps {
			joinStep.Needs = appendUnique(joinStep.Needs, stepID)
		}
	}

	return nil
}

// ApplyGates adds gate conditions to steps.
// For each gate rule:
//   - The target step gets a "gate:condition" label
//   - At runtime, the patrol executor evaluates the condition
//
// Returns a new steps slice with gate labels added.
// The original steps slice is not modified.
func ApplyGates(steps []*Step, compose *ComposeRules) ([]*Step, error) {
	if compose == nil || len(compose.Gate) == 0 {
		return steps, nil
	}

	// Clone steps to avoid mutating input
	cloned := cloneStepsRecursive(steps)
	stepMap := buildStepMap(cloned)

	if err := applyGatesWithMap(stepMap, compose); err != nil {
		return nil, err
	}

	return cloned, nil
}

// applyGatesWithMap applies gate rules using a pre-built stepMap.
// This is the internal implementation used by both ApplyGates and ApplyControlFlow.
// The stepMap entries are modified in place.
func applyGatesWithMap(stepMap map[string]*Step, compose *ComposeRules) error {
	if compose == nil || len(compose.Gate) == 0 {
		return nil
	}

	for _, gate := range compose.Gate {
		// Validate the gate rule
		if gate.Before == "" {
			return fmt.Errorf("gate: before is required")
		}
		if gate.Condition == "" {
			return fmt.Errorf("gate: condition is required")
		}

		// Validate the condition syntax
		_, err := ParseCondition(gate.Condition)
		if err != nil {
			return fmt.Errorf("gate: invalid condition %q: %w", gate.Condition, err)
		}

		// Find the target step
		step, ok := stepMap[gate.Before]
		if !ok {
			return fmt.Errorf("gate: target step %q not found", gate.Before)
		}

		// Add gate label for runtime evaluation using JSON for unambiguous parsing
		gateMeta := map[string]string{"condition": gate.Condition}
		gateJSON, _ := json.Marshal(gateMeta)
		gateLabel := fmt.Sprintf("gate:%s", string(gateJSON))
		step.Labels = appendUnique(step.Labels, gateLabel)
	}

	return nil
}

// ApplyControlFlow applies all control flow operators in the correct order:
// 1. Loops (expand iterations)
// 2. Branches (wire fork-join dependencies)
// 3. Gates (add condition labels)
//
// Returns a new steps slice. The original steps slice is not modified.
func ApplyControlFlow(steps []*Step, compose *ComposeRules) ([]*Step, error) {
	var err error

	// Apply loops first (expands steps) - ApplyLoops already returns new slice
	steps, err = ApplyLoops(steps)
	if err != nil {
		return nil, fmt.Errorf("applying loops: %w", err)
	}

	// Build stepMap once for branches and gates
	// No need to clone here since ApplyLoops already returned a new slice
	stepMap := buildStepMap(steps)

	// Apply branches (wires dependencies)
	if err := applyBranchesWithMap(stepMap, compose); err != nil {
		return nil, fmt.Errorf("applying branches: %w", err)
	}

	// Apply gates (adds labels)
	if err := applyGatesWithMap(stepMap, compose); err != nil {
		return nil, fmt.Errorf("applying gates: %w", err)
	}

	return steps, nil
}

// cloneStepDeep creates a deep copy of a step including children.
func cloneStepDeep(s *Step) *Step {
	clone := cloneStep(s)

	if len(s.Children) > 0 {
		clone.Children = make([]*Step, len(s.Children))
		for i, child := range s.Children {
			clone.Children[i] = cloneStepDeep(child)
		}
	}

	return clone
}

// cloneStepsRecursive creates a deep copy of a slice of steps.
func cloneStepsRecursive(steps []*Step) []*Step {
	result := make([]*Step, len(steps))
	for i, step := range steps {
		result[i] = cloneStepDeep(step)
	}
	return result
}

// cloneLoopSpec creates a deep copy of a LoopSpec.
func cloneLoopSpec(loop *LoopSpec) *LoopSpec {
	if loop == nil {
		return nil
	}
	clone := &LoopSpec{
		Count: loop.Count,
		Until: loop.Until,
		Max:   loop.Max,
		Range: loop.Range,
		Var:   loop.Var,
	}
	if len(loop.Body) > 0 {
		clone.Body = make([]*Step, len(loop.Body))
		for i, step := range loop.Body {
			clone.Body[i] = cloneStepDeep(step)
		}
	}
	return clone
}
