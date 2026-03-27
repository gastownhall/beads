package targetprocess

import (
	"strings"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type fieldMapper struct {
	statusMap map[string]string
	typeMap   map[string]string
}

func newFieldMapper(statusMap, typeMap map[string]string) tracker.FieldMapper {
	return &fieldMapper{
		statusMap: cloneStringMap(statusMap),
		typeMap:   cloneStringMap(typeMap),
	}
}

func (m *fieldMapper) PriorityToBeads(trackerPriority interface{}) int {
	switch v := trackerPriority.(type) {
	case string:
		switch strings.ToLower(v) {
		case "urgent", "critical":
			return 0
		case "high":
			return 1
		case "medium", "normal":
			return 2
		case "low":
			return 3
		case "nice to have", "backlog":
			return 4
		}
	case float64:
		if v >= 0 && v <= 4 {
			return int(v)
		}
	}

	return 2
}

func (m *fieldMapper) PriorityToTracker(beadsPriority int) interface{} {
	switch beadsPriority {
	case 0:
		return "Urgent"
	case 1:
		return "High"
	case 2:
		return "Medium"
	case 3:
		return "Low"
	case 4:
		return "Nice to have"
	default:
		return "Medium"
	}
}

func (m *fieldMapper) StatusToBeads(trackerState interface{}) types.Status {
	state, ok := trackerState.(string)
	if !ok {
		return types.StatusOpen
	}

	for beadsStatus, trackerStateName := range m.statusMap {
		if strings.EqualFold(state, trackerStateName) {
			return types.Status(beadsStatus)
		}
	}

	lower := strings.ToLower(state)
	switch {
	case strings.Contains(lower, "block"):
		return types.StatusBlocked
	case strings.Contains(lower, "defer"), strings.Contains(lower, "hold"), strings.Contains(lower, "postpone"):
		return types.StatusDeferred
	case strings.Contains(lower, "progress"), strings.Contains(lower, "active"), strings.Contains(lower, "start"), strings.Contains(lower, "review"), strings.Contains(lower, "test"), strings.Contains(lower, "fix"):
		return types.StatusInProgress
	case strings.Contains(lower, "done"), strings.Contains(lower, "close"), strings.Contains(lower, "complete"), strings.Contains(lower, "resolved"):
		return types.StatusClosed
	default:
		return types.StatusOpen
	}
}

func (m *fieldMapper) StatusToTracker(beadsStatus types.Status) interface{} {
	if stateName, ok := m.statusMap[string(beadsStatus)]; ok {
		return stateName
	}

	switch beadsStatus {
	case types.StatusClosed:
		return "Done"
	case types.StatusBlocked:
		return "Blocked"
	case types.StatusInProgress:
		return "In Progress"
	case types.StatusDeferred:
		return "Deferred"
	default:
		return "Open"
	}
}

func (m *fieldMapper) TypeToBeads(trackerType interface{}) types.IssueType {
	typeName, ok := trackerType.(string)
	if !ok {
		return types.TypeTask
	}

	for beadsType, trackerTypeName := range m.typeMap {
		if strings.EqualFold(typeName, trackerTypeName) {
			return types.IssueType(beadsType)
		}
	}

	switch strings.ToLower(typeName) {
	case "bug":
		return types.TypeBug
	case "epic":
		return types.TypeEpic
	case "feature":
		return types.TypeFeature
	case "task":
		return types.TypeTask
	case "userstory", "user story":
		return types.TypeFeature
	default:
		return types.TypeTask
	}
}

func (m *fieldMapper) TypeToTracker(beadsType types.IssueType) interface{} {
	if trackerTypeName, ok := m.typeMap[string(beadsType)]; ok {
		return trackerTypeName
	}

	switch beadsType {
	case types.TypeBug:
		return "Bug"
	case types.TypeEpic, types.TypeFeature, types.TypeTask, types.TypeChore:
		return "UserStory"
	default:
		return "UserStory"
	}
}

func (m *fieldMapper) IssueToBeads(ti *tracker.TrackerIssue) *tracker.IssueConversion {
	assignable, ok := ti.Raw.(*Assignable)
	if !ok || assignable == nil {
		return nil
	}

	issue := &types.Issue{
		Title:       assignable.Name,
		Description: assignable.Description,
		Status:      m.statusFromAssignable(assignable),
		IssueType:   m.typeFromAssignable(assignable),
		Priority:    m.priorityFromAssignable(assignable),
		Labels:      tagsToLabels(assignable.Tags),
	}

	if assignable.AssignedUser != nil {
		issue.Assignee = firstNonEmpty(assignable.AssignedUser.Login, assignable.AssignedUser.Email, strings.TrimSpace(assignable.AssignedUser.FirstName+" "+assignable.AssignedUser.LastName), assignable.AssignedUser.Name)
	}

	if ti.URL != "" {
		issue.ExternalRef = &ti.URL
	}

	return &tracker.IssueConversion{Issue: issue}
}

func (m *fieldMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	fields := map[string]interface{}{
		"Name":        issue.Title,
		"Description": issue.Description,
	}

	if tags := labelsToTags(issue.Labels); tags != "" {
		fields["Tags"] = tags
	}

	return fields
}

func (m *fieldMapper) statusFromAssignable(assignable *Assignable) types.Status {
	if assignable.EntityState == nil {
		return types.StatusOpen
	}
	if assignable.EntityState.IsFinal {
		return types.StatusClosed
	}
	return m.StatusToBeads(assignable.EntityState.Name)
}

func (m *fieldMapper) typeFromAssignable(assignable *Assignable) types.IssueType {
	if assignable.EntityType != nil && assignable.EntityType.Name != "" {
		return m.TypeToBeads(assignable.EntityType.Name)
	}
	if assignable.ResourceType != "" {
		return m.TypeToBeads(assignable.ResourceType)
	}
	return types.TypeTask
}

func (m *fieldMapper) priorityFromAssignable(assignable *Assignable) int {
	if assignable.Priority != nil && assignable.Priority.Name != "" {
		return m.PriorityToBeads(assignable.Priority.Name)
	}
	if assignable.NumericPriority != 0 {
		return m.PriorityToBeads(assignable.NumericPriority)
	}
	return 2
}

func cloneStringMap(input map[string]string) map[string]string {
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func tagsToLabels(tags string) []string {
	if strings.TrimSpace(tags) == "" {
		return nil
	}

	parts := strings.Split(tags, ",")
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		label := strings.TrimSpace(part)
		if label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}

func labelsToTags(labels []string) string {
	if len(labels) == 0 {
		return ""
	}

	clean := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label != "" {
			clean = append(clean, label)
		}
	}
	return strings.Join(clean, ", ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func assignableEntityType(assignable *Assignable) string {
	if assignable == nil {
		return ""
	}
	if assignable.EntityType != nil && assignable.EntityType.Name != "" {
		return assignable.EntityType.Name
	}
	return assignable.ResourceType
}
