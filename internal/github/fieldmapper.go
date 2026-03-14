package github

import (
	"fmt"

	"github.com/gastownhall/beads/internal/tracker"
	"github.com/gastownhall/beads/internal/types"
)

// githubFieldMapper implements tracker.FieldMapper for GitHub.
type githubFieldMapper struct {
	config *MappingConfig
}

func (m *githubFieldMapper) PriorityToBeads(trackerPriority interface{}) int {
	// GitHub uses label-based priority (string), not numeric
	if label, ok := trackerPriority.(string); ok {
		if p, exists := m.config.PriorityMap[label]; exists {
			return p
		}
	}
	return 2
}

func (m *githubFieldMapper) PriorityToTracker(beadsPriority int) interface{} {
	// Inverse lookup: find the label for this priority
	for label, p := range m.config.PriorityMap {
		if p == beadsPriority {
			return label
		}
	}
	return "medium"
}

func (m *githubFieldMapper) StatusToBeads(trackerState interface{}) types.Status {
	if state, ok := trackerState.(string); ok {
		if status, exists := m.config.StateMap[state]; exists {
			return types.Status(status)
		}
		// GitHub-specific defaults
		switch state {
		case "open":
			return types.StatusOpen
		case "closed":
			return types.StatusClosed
		}
	}
	return types.StatusOpen
}

func (m *githubFieldMapper) StatusToTracker(beadsStatus types.Status) interface{} {
	switch beadsStatus {
	case types.StatusClosed:
		return "closed"
	default:
		return "open"
	}
}

func (m *githubFieldMapper) TypeToBeads(trackerType interface{}) types.IssueType {
	if t, ok := trackerType.(string); ok {
		if issueType, exists := m.config.LabelTypeMap[t]; exists {
			return types.IssueType(issueType)
		}
	}
	return types.TypeTask
}

func (m *githubFieldMapper) TypeToTracker(beadsType types.IssueType) interface{} {
	return string(beadsType)
}

func (m *githubFieldMapper) IssueToBeads(ti *tracker.TrackerIssue) *tracker.IssueConversion {
	gh, ok := ti.Raw.(*Issue)
	if !ok {
		return nil
	}

	conv := GitHubIssueToBeads(gh, m.config)
	if conv == nil {
		return nil
	}

	// Convert github.DependencyInfo to tracker.DependencyInfo
	var deps []tracker.DependencyInfo
	for _, d := range conv.Dependencies {
		deps = append(deps, tracker.DependencyInfo{
			FromExternalID: fmt.Sprintf("%d", d.FromGitHubNumber),
			ToExternalID:   fmt.Sprintf("%d", d.ToGitHubNumber),
			Type:           d.Type,
		})
	}

	return &tracker.IssueConversion{
		Issue:        conv.Issue,
		Dependencies: deps,
	}
}

func (m *githubFieldMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	return BeadsIssueToGitHubFields(issue, m.config)
}
