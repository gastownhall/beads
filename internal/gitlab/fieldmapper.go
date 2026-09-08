package gitlab

import (
	"fmt"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// gitlabFieldMapper implements tracker.FieldMapper for GitLab.
type gitlabFieldMapper struct {
	config *MappingConfig
}

func (m *gitlabFieldMapper) PriorityToBeads(trackerPriority interface{}) int {
	// GitLab uses label-based priority (string), not numeric
	if label, ok := trackerPriority.(string); ok {
		if p, exists := m.config.PriorityMap[label]; exists {
			return p
		}
	}
	return 2
}

func (m *gitlabFieldMapper) PriorityToTracker(beadsPriority int) interface{} {
	// Inverse lookup: find the label for this priority
	for label, p := range m.config.PriorityMap {
		if p == beadsPriority {
			return label
		}
	}
	return "medium"
}

func (m *gitlabFieldMapper) StatusToBeads(trackerState interface{}) types.Status {
	if state, ok := trackerState.(string); ok {
		if status, exists := m.config.StateMap[state]; exists {
			return types.Status(status)
		}
		// GitLab-specific defaults
		switch state {
		case "opened", "reopened":
			return types.StatusOpen
		case "closed":
			return types.StatusClosed
		}
	}
	return types.StatusOpen
}

func (m *gitlabFieldMapper) StatusToTracker(beadsStatus types.Status) interface{} {
	switch beadsStatus {
	case types.StatusClosed:
		return "closed"
	default:
		return "opened"
	}
}

func (m *gitlabFieldMapper) TypeToBeads(trackerType interface{}) types.IssueType {
	if t, ok := trackerType.(string); ok {
		if issueType, exists := m.config.LabelTypeMap[t]; exists {
			return types.IssueType(issueType)
		}
	}
	return types.TypeTask
}

func (m *gitlabFieldMapper) TypeToTracker(beadsType types.IssueType) interface{} {
	return string(beadsType)
}

func (m *gitlabFieldMapper) IssueToBeads(ti *tracker.TrackerIssue) *tracker.IssueConversion {
	gl, ok := ti.Raw.(*Issue)
	if !ok {
		return nil
	}

	conv := GitLabIssueToBeads(gl, m.config)
	if conv == nil {
		return nil
	}

	// Convert gitlab.DependencyInfo to tracker.DependencyInfo
	var deps []tracker.DependencyInfo
	for _, d := range conv.Dependencies {
		deps = append(deps, tracker.DependencyInfo{
			FromExternalID: fmt.Sprintf("%d", d.FromGitLabIID),
			ToExternalID:   fmt.Sprintf("%d", d.ToGitLabIID),
			Type:           d.Type,
		})
	}

	return &tracker.IssueConversion{
		Issue:        conv.Issue,
		Dependencies: deps,
	}
}

func (m *gitlabFieldMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	return BeadsIssueToGitLabFields(issue, m.config)
}
