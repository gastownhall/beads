package jira

import (
	"strings"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// jiraFieldMapper implements tracker.FieldMapper for Jira.
type jiraFieldMapper struct{}

func (m *jiraFieldMapper) PriorityToBeads(trackerPriority interface{}) int {
	if name, ok := trackerPriority.(string); ok {
		switch name {
		case "Highest":
			return 0
		case "High":
			return 1
		case "Medium":
			return 2
		case "Low":
			return 3
		case "Lowest":
			return 4
		}
	}
	return 2
}

func (m *jiraFieldMapper) PriorityToTracker(beadsPriority int) interface{} {
	switch beadsPriority {
	case 0:
		return "Highest"
	case 1:
		return "High"
	case 2:
		return "Medium"
	case 3:
		return "Low"
	case 4:
		return "Lowest"
	default:
		return "Medium"
	}
}

func (m *jiraFieldMapper) StatusToBeads(trackerState interface{}) types.Status {
	if state, ok := trackerState.(string); ok {
		switch state {
		case "To Do", "Open", "Backlog", "New":
			return types.StatusOpen
		case "In Progress", "In Review":
			return types.StatusInProgress
		case "Blocked":
			return types.StatusBlocked
		case "Done", "Closed", "Resolved":
			return types.StatusClosed
		}
	}
	return types.StatusOpen
}

func (m *jiraFieldMapper) StatusToTracker(beadsStatus types.Status) interface{} {
	switch beadsStatus {
	case types.StatusOpen:
		return "To Do"
	case types.StatusInProgress:
		return "In Progress"
	case types.StatusBlocked:
		return "Blocked"
	case types.StatusClosed:
		return "Done"
	default:
		return "To Do"
	}
}

func (m *jiraFieldMapper) TypeToBeads(trackerType interface{}) types.IssueType {
	if t, ok := trackerType.(string); ok {
		switch t {
		case "Bug":
			return types.TypeBug
		case "Story", "Feature":
			return types.TypeFeature
		case "Epic":
			return types.TypeEpic
		case "Task", "Sub-task":
			return types.TypeTask
		}
	}
	return types.TypeTask
}

func (m *jiraFieldMapper) TypeToTracker(beadsType types.IssueType) interface{} {
	switch beadsType {
	case types.TypeBug:
		return "Bug"
	case types.TypeFeature:
		return "Story"
	case types.TypeEpic:
		return "Epic"
	default:
		return "Task"
	}
}

func (m *jiraFieldMapper) IssueToBeads(ti *tracker.TrackerIssue) *tracker.IssueConversion {
	ji, ok := ti.Raw.(*Issue)
	if !ok || ji == nil {
		return nil
	}

	issue := &types.Issue{
		Title:       ji.Fields.Summary,
		Description: DescriptionToPlainText(ji.Fields.Description),
		Priority:    m.PriorityToBeads(priorityName(ji)),
		Status:      m.StatusToBeads(statusName(ji)),
		IssueType:   m.TypeToBeads(typeName(ji)),
	}

	if ji.Fields.Assignee != nil {
		issue.Owner = ji.Fields.Assignee.DisplayName
	}

	if ji.Fields.Labels != nil {
		issue.Labels = ji.Fields.Labels
	}

	// Set external ref from issue URL
	if ji.Self != "" {
		ref := extractBrowseURL(ji)
		issue.ExternalRef = &ref
	}

	return &tracker.IssueConversion{
		Issue: issue,
	}
}

func (m *jiraFieldMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	fields := map[string]interface{}{
		"summary": issue.Title,
	}

	// Convert description to ADF
	if issue.Description != "" {
		fields["description"] = PlainTextToADF(issue.Description)
	}

	// Set issue type
	typeName := m.TypeToTracker(issue.IssueType)
	if name, ok := typeName.(string); ok {
		fields["issuetype"] = map[string]string{"name": name}
	}

	// Set priority
	priorityName := m.PriorityToTracker(issue.Priority)
	if name, ok := priorityName.(string); ok {
		fields["priority"] = map[string]string{"name": name}
	}

	// Set labels
	if len(issue.Labels) > 0 {
		fields["labels"] = issue.Labels
	}

	return fields
}

// Helper functions for safe field extraction from Jira issues.

func priorityName(ji *Issue) string {
	if ji.Fields.Priority != nil {
		return ji.Fields.Priority.Name
	}
	return ""
}

func statusName(ji *Issue) string {
	if ji.Fields.Status != nil {
		return ji.Fields.Status.Name
	}
	return ""
}

func typeName(ji *Issue) string {
	if ji.Fields.IssueType != nil {
		return ji.Fields.IssueType.Name
	}
	return ""
}

// extractBrowseURL builds the human-readable browse URL from a Jira issue.
// Self is "https://company.atlassian.net/rest/api/3/issue/10001";
// we need "https://company.atlassian.net/browse/PROJ-123".
func extractBrowseURL(ji *Issue) string {
	if ji.Self == "" || ji.Key == "" {
		return ""
	}
	if idx := strings.Index(ji.Self, "/rest/api/"); idx > 0 {
		return ji.Self[:idx] + "/browse/" + ji.Key
	}
	return ""
}
