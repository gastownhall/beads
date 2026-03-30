package ado

import (
	"context"
	"fmt"
	"strings"
)

// defaultTransitions defines the default Agile-process state transition paths
// for common ADO work item types. The key is "WorkItemType:FromState" and the
// value is an ordered list of intermediate states to reach common targets.
// These paths represent the standard Agile template; custom process templates
// may override them via the ado.state_map.* configuration.
var defaultTransitions = map[string][]string{
	// Agile process — Bug
	"Bug:New":      {"Active", "Resolved", "Closed"},
	"Bug:Active":   {"Resolved", "Closed"},
	"Bug:Resolved": {"Closed"},

	// Agile process — Task
	"Task:New":    {"Active", "Closed"},
	"Task:Active": {"Closed"},

	// Agile process — User Story
	"User Story:New":      {"Active", "Resolved", "Closed"},
	"User Story:Active":   {"Resolved", "Closed"},
	"User Story:Resolved": {"Closed"},

	// Agile process — Epic
	"Epic:New":      {"Active", "Resolved", "Closed"},
	"Epic:Active":   {"Resolved", "Closed"},
	"Epic:Resolved": {"Closed"},

	// Agile process — Feature
	"Feature:New":      {"Active", "Resolved", "Closed"},
	"Feature:Active":   {"Resolved", "Closed"},
	"Feature:Resolved": {"Closed"},

	// Agile process — Issue
	"Issue:New":    {"Active", "Closed"},
	"Issue:Active": {"Closed"},

	// Scrum process — Product Backlog Item
	"Product Backlog Item:New":       {"Approved", "Committed", "Done"},
	"Product Backlog Item:Approved":  {"Committed", "Done"},
	"Product Backlog Item:Committed": {"Done"},

	// Scrum process — common types using To Do/Doing/Done
	"Task:To Do":  {"In Progress", "Done"},
	"Issue:To Do": {"Doing", "Done"},
	"Epic:To Do":  {"Doing", "Done"},

	// CMMI process
	"Bug:Proposed":         {"Active", "Resolved", "Closed"},
	"Task:Proposed":        {"Active", "Closed"},
	"Requirement:Proposed": {"Active", "Resolved", "Closed"},
}

// initialStates lists the known initial states for ADO work item creation.
// When creating a work item, the state must be one of these (or omitted).
var initialStates = map[string]bool{
	"New":      true,
	"To Do":    true,
	"Proposed": true,
}

// isInitialState reports whether the given state is a known ADO initial state
// that can be used when creating work items.
func isInitialState(state string) bool {
	return initialStates[state]
}

// resolveTransitionPath returns the sequence of intermediate states needed
// to transition from currentState to targetState for the given work item type.
// Returns nil if no transition is needed (states are equal) or if no known
// path exists (caller should attempt a direct update).
func resolveTransitionPath(workItemType, currentState, targetState string) []string {
	if currentState == targetState {
		return nil
	}

	key := workItemType + ":" + currentState
	path, ok := defaultTransitions[key]
	if !ok {
		return nil
	}

	// Find the target in the path and return all states up to and including it.
	for i, state := range path {
		if strings.EqualFold(state, targetState) {
			return path[:i+1]
		}
	}

	return nil
}

// transitionWorkItem transitions a work item from its current state to the
// target state by walking through intermediate states. It first attempts a
// direct state update; if that fails with a 400 error, it walks through the
// known transition path for the work item type.
func (c *Client) transitionWorkItem(ctx context.Context, workItemID int, workItemType, currentState, targetState string) (*WorkItem, error) {
	if currentState == targetState {
		return nil, nil
	}

	// Try direct transition first.
	fields := map[string]interface{}{FieldState: targetState}
	wi, err := c.UpdateWorkItem(ctx, workItemID, fields)
	if err == nil {
		return wi, nil
	}

	// If direct transition failed, check for a known path.
	if !strings.Contains(err.Error(), "status 400") {
		return nil, fmt.Errorf("transitioning to %q: %w", targetState, err)
	}

	path := resolveTransitionPath(workItemType, currentState, targetState)
	if len(path) == 0 {
		return nil, fmt.Errorf("no known transition path from %q to %q for %s: %w",
			currentState, targetState, workItemType, err)
	}

	// Walk through intermediate states.
	var lastWI *WorkItem
	for _, intermediate := range path {
		fields := map[string]interface{}{FieldState: intermediate}
		lastWI, err = c.UpdateWorkItem(ctx, workItemID, fields)
		if err != nil {
			return nil, fmt.Errorf("transitioning to intermediate state %q: %w", intermediate, err)
		}
	}
	return lastWI, nil
}
