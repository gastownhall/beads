// Package gitlab provides client and data types for the GitLab REST API.
package gitlab

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// MappingConfig configures how GitLab fields map to beads fields.
type MappingConfig struct {
	PriorityMap  map[string]int    // priority label value → beads priority (0-4)
	StateMap     map[string]string // GitLab state → beads status
	LabelTypeMap map[string]string // type label value → beads issue type
	RelationMap  map[string]string // GitLab link type → beads dependency type
}

// DefaultMappingConfig returns the default mapping configuration.
// Uses exported mapping constants from types.go as the single source of truth.
func DefaultMappingConfig() *MappingConfig {
	// Copy PriorityMapping to avoid external modification
	priorityMap := make(map[string]int, len(PriorityMapping))
	for k, v := range PriorityMapping {
		priorityMap[k] = v
	}

	// Copy typeMapping to avoid external modification
	labelTypeMap := make(map[string]string, len(typeMapping))
	for k, v := range typeMapping {
		labelTypeMap[k] = v
	}

	return &MappingConfig{
		PriorityMap: priorityMap,
		// StateMap maps GitLab states to beads statuses
		// Note: GitLab uses "opened"/"closed"/"reopened", beads uses "open"/"closed"
		StateMap: map[string]string{
			"opened":   StatusMapping["open"],
			"closed":   StatusMapping["closed"],
			"reopened": StatusMapping["open"], // reopened maps to open
		},
		LabelTypeMap: labelTypeMap,
		RelationMap: map[string]string{
			"blocks":        "blocks",
			"is_blocked_by": "blocked_by",
			"relates_to":    "related",
		},
	}
}

// priorityFromLabels extracts priority from GitLab labels.
// Returns default priority (2 = medium) if no priority label found.
func priorityFromLabels(labels []string, config *MappingConfig) int {
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "priority" {
			if p, ok := config.PriorityMap[strings.ToLower(value)]; ok {
				return p
			}
		}
	}
	return 2 // Default to medium
}

// statusFromLabelsAndState determines beads status from GitLab labels and state.
// GitLab's closed state takes precedence over status labels.
func statusFromLabelsAndState(labels []string, state string, config *MappingConfig) string {
	// Closed state always wins
	if state == "closed" {
		return "closed"
	}

	// Check for status label
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "status" {
			normalized := strings.ToLower(value)
			switch normalized {
			case "open":
				return "open"
			case "in_progress":
				return "in_progress"
			case "review":
				return "in_progress" // review maps to in_progress in beads
			case "blocked":
				return "blocked"
			case "deferred":
				return "deferred"
			case "done":
				return "closed"
			}
		}
	}

	// Default: map GitLab state to beads status
	if s, ok := config.StateMap[state]; ok {
		return s
	}
	return "open"
}

// typeFromLabels extracts issue type from GitLab labels.
// Checks both scoped (type::bug) and bare (bug) labels.
// Returns "task" if no type label found.
func typeFromLabels(labels []string, config *MappingConfig) string {
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "type" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
		// Also check bare labels (no prefix)
		if prefix == "" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
	}
	return "task" // Default to task
}

// GitLabIssueToBeads converts a GitLab Issue to a beads Issue.
func GitLabIssueToBeads(gl *Issue, config *MappingConfig) *IssueConversion {
	webURL := gl.WebURL
	sourceSystem := fmt.Sprintf("gitlab:%d:%d", gl.ProjectID, gl.IID)

	issue := &types.Issue{
		Title:        gl.Title,
		Description:  gl.Description,
		ExternalRef:  &webURL,
		SourceSystem: sourceSystem,
		IssueType:    types.IssueType(typeFromLabels(gl.Labels, config)),
		Priority:     priorityFromLabels(gl.Labels, config),
		Status:       types.Status(statusFromLabelsAndState(gl.Labels, gl.State, config)),
		Labels:       filterNonScopedLabels(gl.Labels),
	}

	// Set estimate from weight (convert to minutes - assume 1 weight = 1 hour)
	if gl.Weight > 0 {
		estimatedMinutes := gl.Weight * 60
		issue.EstimatedMinutes = &estimatedMinutes
	}

	// Set assignee from GitLab user
	if gl.Assignee != nil {
		issue.Assignee = gl.Assignee.Username
	}

	// Set timestamps
	if gl.CreatedAt != nil {
		issue.CreatedAt = *gl.CreatedAt
	}
	if gl.UpdatedAt != nil {
		issue.UpdatedAt = *gl.UpdatedAt
	}

	return &IssueConversion{
		Issue:        issue,
		Dependencies: []DependencyInfo{},
	}
}

// BeadsIssueToGitLabFields converts a beads Issue to GitLab API update fields.
func BeadsIssueToGitLabFields(issue *types.Issue, config *MappingConfig) map[string]interface{} {
	fields := map[string]interface{}{
		"title":       issue.Title,
		"description": issue.Description,
	}

	// Build labels from type, priority, and status
	var labels []string

	// Add type label
	if issue.IssueType != "" {
		labels = append(labels, "type::"+string(issue.IssueType))
	}

	// Add priority label
	priorityLabel := priorityToLabel(issue.Priority)
	if priorityLabel != "" {
		labels = append(labels, "priority::"+priorityLabel)
	}

	// Add status label for board column placement
	switch issue.Status {
	case types.StatusOpen:
		labels = append(labels, "status::open")
	case types.StatusInProgress:
		labels = append(labels, "status::in_progress")
	case types.StatusBlocked:
		labels = append(labels, "status::blocked")
	case types.StatusDeferred:
		labels = append(labels, "status::deferred")
	case types.StatusClosed:
		labels = append(labels, "status::done")
	}

	// Add any existing non-scoped labels
	labels = append(labels, issue.Labels...)

	fields["labels"] = labels

	// Set weight from estimate (convert minutes to weight - 60 minutes = 1 weight)
	if issue.EstimatedMinutes != nil && *issue.EstimatedMinutes > 0 {
		fields["weight"] = *issue.EstimatedMinutes / 60
	}

	// Set state_event for closed issues
	if issue.Status == types.StatusClosed {
		fields["state_event"] = "close"
	}

	return fields
}

// priorityToLabel converts beads priority (0-4) to GitLab priority label value.
func priorityToLabel(priority int) string {
	switch priority {
	case 0:
		return "critical"
	case 1:
		return "high"
	case 2:
		return "medium"
	case 3:
		return "low"
	case 4:
		return "none"
	default:
		return "medium"
	}
}

// issueLinksToDependencies converts GitLab IssueLinks to beads DependencyInfo.
func issueLinksToDependencies(sourceIID int, links []IssueLink, config *MappingConfig) []DependencyInfo {
	var deps []DependencyInfo

	for _, link := range links {
		var toIID int
		var depType string

		// Determine direction and target
		if link.SourceIssue != nil && link.SourceIssue.IID == sourceIID {
			// We are the source, target is the dependency
			if link.TargetIssue != nil {
				toIID = link.TargetIssue.IID
			}
		} else if link.TargetIssue != nil && link.TargetIssue.IID == sourceIID {
			// We are the target, source is the dependency
			if link.SourceIssue != nil {
				toIID = link.SourceIssue.IID
			}
		}

		// Map link type
		if t, ok := config.RelationMap[link.LinkType]; ok {
			depType = t
		} else {
			depType = "related"
		}

		deps = append(deps, DependencyInfo{
			FromGitLabIID: sourceIID,
			ToGitLabIID:   toIID,
			Type:          depType,
		})
	}

	return deps
}

// filterNonScopedLabels returns only labels without scoped prefixes.
// Removes priority::*, status::*, and type::* labels.
func filterNonScopedLabels(labels []string) []string {
	var filtered []string
	for _, label := range labels {
		prefix, _ := parseLabelPrefix(label)
		// Skip scoped labels that we handle specially
		if prefix == "priority" || prefix == "status" || prefix == "type" {
			continue
		}
		filtered = append(filtered, label)
	}
	return filtered
}
