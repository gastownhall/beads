package jira

import (
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// jiraFieldMapper implements tracker.FieldMapper for Jira.
type jiraFieldMapper struct {
	apiVersion  string            // "2" or "3" (default: "3")
	statusMap   map[string]string // beads status → Jira status name (from jira.status_map.* config)
	typeMap     map[string]string // beads type → Jira type (from jira.type_map.* config)
	priorityMap map[string]string // beads priority (as string "0"-"4") → Jira priority name (from jira.priority_map.* config)
}

func (m *jiraFieldMapper) PriorityToBeads(trackerPriority interface{}) int {
	if name, ok := trackerPriority.(string); ok {
		// Check custom map first (inverted: Jira name → beads priority).
		for beadsPri, jiraName := range m.priorityMap {
			if strings.EqualFold(name, jiraName) {
				if v, err := strconv.Atoi(beadsPri); err == nil && v >= 0 && v <= 4 {
					return v
				}
			}
		}
		// Jira defaults.
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
	// Check custom map first (beads priority as string key → Jira name).
	if m.priorityMap != nil {
		key := strconv.Itoa(beadsPriority)
		if name, ok := m.priorityMap[key]; ok {
			return name
		}
	}
	// Jira defaults.
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
		// Check custom map first (inverted: jira name → beads status).
		for beadsStatus, jiraName := range m.statusMap {
			if strings.EqualFold(state, jiraName) {
				return types.Status(beadsStatus)
			}
		}
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
	// Check custom map first.
	if name, ok := m.statusMap[string(beadsStatus)]; ok {
		return name
	}
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
	t, ok := trackerType.(string)
	if !ok {
		return types.TypeTask
	}

	// Check custom map first (inverted: Jira type → beads type).
	for beadsType, jiraType := range m.typeMap {
		if strings.EqualFold(t, jiraType) {
			return types.IssueType(beadsType)
		}
	}

	// Jira defaults.
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
	return types.TypeTask
}

func (m *jiraFieldMapper) TypeToTracker(beadsType types.IssueType) interface{} {
	if name, ok := m.typeMap[string(beadsType)]; ok {
		return name
	}
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

	deps := jiraIssueDependencies(ji)

	return &tracker.IssueConversion{
		Issue:        issue,
		Dependencies: deps,
	}
}

func (m *jiraFieldMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	fields := map[string]interface{}{
		"summary": issue.Title,
	}

	// v3 requires ADF (Atlassian Document Format); v2 accepts a plain string.
	if issue.Description != "" {
		if m.apiVersion == "2" {
			fields["description"] = issue.Description
		} else {
			fields["description"] = PlainTextToADF(issue.Description)
		}
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

func jiraIssueDependencies(ji *Issue) []tracker.DependencyInfo {
	if ji == nil || strings.TrimSpace(ji.Key) == "" {
		return nil
	}

	currentKey := strings.TrimSpace(ji.Key)
	var deps []tracker.DependencyInfo
	seen := make(map[string]struct{})

	addDep := func(from, to, depType string, source tracker.DependencySource) {
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		depType = strings.TrimSpace(depType)
		if from == "" || to == "" || from == to || depType == "" {
			return
		}
		key := from + "\x00" + to + "\x00" + depType
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		deps = append(deps, tracker.DependencyInfo{
			FromExternalID: from,
			ToExternalID:   to,
			Type:           depType,
			Source:         source,
		})
	}

	if ji.Fields.Parent != nil {
		addDep(currentKey, ji.Fields.Parent.Key, string(types.DepParentChild), tracker.DependencySourceParent)
	}

	for _, subtask := range ji.Fields.Subtasks {
		addDep(subtask.Key, currentKey, string(types.DepParentChild), tracker.DependencySourceParent)
	}

	for _, link := range ji.Fields.IssueLinks {
		if link.OutwardIssue != nil {
			from, to, depType := jiraLinkDependency(currentKey, link.OutwardIssue.Key, linkPhrase(link.Type, true))
			addDep(from, to, depType, tracker.DependencySourceRelation)
		}
		if link.InwardIssue != nil {
			from, to, depType := jiraLinkDependency(currentKey, link.InwardIssue.Key, linkPhrase(link.Type, false))
			addDep(from, to, depType, tracker.DependencySourceRelation)
		}
	}

	return deps
}

func linkPhrase(linkType *IssueLinkType, outward bool) string {
	if linkType == nil {
		return ""
	}
	if outward {
		return linkType.Outward
	}
	return linkType.Inward
}

func jiraLinkDependency(currentKey, linkedKey, phrase string) (string, string, string) {
	normalized := strings.ToLower(strings.TrimSpace(phrase))
	switch {
	case normalized == "":
		return currentKey, linkedKey, string(types.DepRelated)
	case strings.Contains(normalized, "block"):
		if strings.Contains(normalized, "blocked by") || strings.Contains(normalized, "is blocked") {
			return currentKey, linkedKey, string(types.DepBlocks)
		}
		return linkedKey, currentKey, string(types.DepBlocks)
	case strings.Contains(normalized, "duplicate") || strings.Contains(normalized, "clone"):
		return currentKey, linkedKey, string(types.DepDuplicates)
	case strings.Contains(normalized, "parent of"):
		return linkedKey, currentKey, string(types.DepParentChild)
	case strings.Contains(normalized, "child of") || strings.Contains(normalized, "subtask of"):
		return currentKey, linkedKey, string(types.DepParentChild)
	default:
		return currentKey, linkedKey, string(types.DepRelated)
	}
}
