package linear

import (
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// linearFieldMapper implements tracker.FieldMapper for Linear.
type linearFieldMapper struct {
	config *MappingConfig
}

func (m *linearFieldMapper) PriorityToBeads(trackerPriority interface{}) int {
	if p, ok := trackerPriority.(int); ok {
		return PriorityToBeads(p, m.config)
	}
	return 2
}

func (m *linearFieldMapper) PriorityToTracker(beadsPriority int) interface{} {
	return PriorityToLinear(beadsPriority, m.config)
}

func (m *linearFieldMapper) StatusToBeads(trackerState interface{}) types.Status {
	if state, ok := trackerState.(*State); ok {
		return StateToBeadsStatus(state, m.config)
	}
	return types.StatusOpen
}

func (m *linearFieldMapper) StatusToTracker(beadsStatus types.Status) interface{} {
	return StatusToLinearStateType(beadsStatus)
}

func (m *linearFieldMapper) TypeToBeads(trackerType interface{}) types.IssueType {
	if labels, ok := trackerType.(*Labels); ok {
		return LabelToIssueType(labels, m.config)
	}
	return types.TypeTask
}

func (m *linearFieldMapper) TypeToTracker(beadsType types.IssueType) interface{} {
	return string(beadsType)
}

func (m *linearFieldMapper) IssueToBeads(ti *tracker.TrackerIssue) *tracker.IssueConversion {
	li, ok := ti.Raw.(*Issue)
	if !ok {
		return nil
	}

	conv := IssueToBeads(li, m.config)
	if conv == nil {
		return nil
	}

	issue, ok := conv.Issue.(*types.Issue)
	if !ok {
		return nil
	}

	var deps []tracker.DependencyInfo
	for _, d := range conv.Dependencies {
		deps = append(deps, tracker.DependencyInfo{
			FromExternalID: d.FromLinearID,
			ToExternalID:   d.ToLinearID,
			Type:           d.Type,
			Source:         tracker.DependencySource(d.Source),
		})
	}

	return &tracker.IssueConversion{
		Issue:        issue,
		Dependencies: deps,
	}
}

func (m *linearFieldMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	updates := map[string]interface{}{
		"title":       issue.Title,
		"description": issue.Description,
		"priority":    PriorityToLinear(issue.Priority, m.config),
	}
	return updates
}
