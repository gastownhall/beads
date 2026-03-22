package notion

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// MappingConfig defines fixed conversions between Notion sync payloads and beads core types.
type MappingConfig struct {
	PriorityToBeads  map[string]int
	PriorityToNotion map[int]string
	StatusToBeads    map[string]types.Status
	StatusToNotion   map[types.Status]string
	TypeToBeads      map[string]types.IssueType
	TypeToNotion     map[types.IssueType]string
}

// DefaultMappingConfig returns the v1 fixed mapping for the dedicated Notion beads schema.
func DefaultMappingConfig() *MappingConfig {
	return &MappingConfig{
		PriorityToBeads: map[string]int{
			"critical": 0,
			"high":     1,
			"medium":   2,
			"low":      3,
			"backlog":  4,
		},
		PriorityToNotion: map[int]string{
			0: "Critical",
			1: "High",
			2: "Medium",
			3: "Low",
			4: "Backlog",
		},
		StatusToBeads: map[string]types.Status{
			"open":        types.StatusOpen,
			"in_progress": types.StatusInProgress,
			"blocked":     types.StatusBlocked,
			"deferred":    types.StatusDeferred,
			"closed":      types.StatusClosed,
		},
		StatusToNotion: map[types.Status]string{
			types.StatusOpen:       "Open",
			types.StatusInProgress: "In Progress",
			types.StatusBlocked:    "Blocked",
			types.StatusDeferred:   "Deferred",
			types.StatusClosed:     "Closed",
		},
		TypeToBeads: map[string]types.IssueType{
			"bug":     types.TypeBug,
			"feature": types.TypeFeature,
			"task":    types.TypeTask,
			"epic":    types.TypeEpic,
			"chore":   types.TypeChore,
		},
		TypeToNotion: map[types.IssueType]string{
			types.TypeBug:     "Bug",
			types.TypeFeature: "Feature",
			types.TypeTask:    "Task",
			types.TypeEpic:    "Epic",
			types.TypeChore:   "Chore",
		},
	}
}

// BeadsIssueFromPullIssue converts one pulled Notion issue into a beads core issue.
func BeadsIssueFromPullIssue(issue PulledIssue, config *MappingConfig) (*types.Issue, error) {
	if config == nil {
		config = DefaultMappingConfig()
	}

	status := statusToBeads(issue.Status, config)
	priority := priorityToBeads(issue.Priority, config)
	issueTypeRaw := issue.IssueType
	if strings.TrimSpace(issueTypeRaw) == "" {
		issueTypeRaw = issue.Type
	}
	issueType := typeToBeads(issueTypeRaw, config)

	createdAt, hasCreated, err := parseMappingTimestamp(string(issue.CreatedAt))
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	updatedAt, hasUpdated, err := parseMappingTimestamp(string(issue.UpdatedAt))
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	now := time.Now().UTC()
	if !hasCreated {
		createdAt = now
	}
	if !hasUpdated {
		updatedAt = createdAt
	}

	beadsIssue := &types.Issue{
		ID:           strings.TrimSpace(issue.ID),
		Title:        strings.TrimSpace(issue.Title),
		Description:  strings.TrimSpace(issue.Description),
		Status:       status,
		Priority:     priority,
		IssueType:    issueType,
		Assignee:     issue.Assignee,
		Labels:       append([]string(nil), issue.Labels...),
		SourceSystem: "notion",
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}

	if externalRef := BuildNotionExternalRef(&issue); externalRef != "" {
		beadsIssue.ExternalRef = &externalRef
	}

	return beadsIssue, nil
}

// TrackerIssueFromPullIssue converts a pull issue to the generic tracker representation.
func TrackerIssueFromPullIssue(issue PulledIssue, config *MappingConfig) (*tracker.TrackerIssue, error) {
	beadsIssue, err := BeadsIssueFromPullIssue(issue, config)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSpace(issue.ExternalRef)
	if canonical, ok := CanonicalizeNotionPageURL(url); ok {
		url = canonical
	}
	if url == "" {
		url = notionPageURL(firstNonEmpty(issue.NotionPageID, issue.ID))
	}

	trackerIssue := &tracker.TrackerIssue{
		ID:          issue.NotionPageID,
		Identifier:  ExtractNotionIdentifier(firstNonEmpty(url, issue.NotionPageID)),
		URL:         url,
		Title:       beadsIssue.Title,
		Description: beadsIssue.Description,
		Priority:    beadsIssue.Priority,
		State:       beadsIssue.Status,
		Type:        beadsIssue.IssueType,
		Labels:      append([]string(nil), beadsIssue.Labels...),
		Assignee:    beadsIssue.Assignee,
		CreatedAt:   beadsIssue.CreatedAt,
		UpdatedAt:   beadsIssue.UpdatedAt,
		Raw:         &issue,
	}

	return trackerIssue, nil
}

// PushIssueFromIssue converts one beads issue into the Notion push issue shape.
func PushIssueFromIssue(issue *types.Issue, config *MappingConfig) (*PushIssue, error) {
	if issue == nil {
		return nil, fmt.Errorf("issue is nil")
	}
	if config == nil {
		config = DefaultMappingConfig()
	}

	status := statusToNotion(issue.Status, config)
	priority := priorityToNotion(issue.Priority, config)
	issueType := typeToNotion(issue.IssueType, config)

	pushIssue := &PushIssue{
		ID:          issue.ID,
		Title:       issue.Title,
		Description: issue.Description,
		Status:      status,
		Priority:    priority,
		IssueType:   issueType,
		Assignee:    issue.Assignee,
	}

	if issue.ExternalRef != nil && strings.TrimSpace(*issue.ExternalRef) != "" {
		if canonical, ok := CanonicalizeNotionExternalRef(*issue.ExternalRef); ok {
			pushIssue.ExternalRef = canonical
		}
	}

	return pushIssue, nil
}

func BuildPageProperties(pushIssue *PushIssue) map[string]interface{} {
	labels := make([]map[string]interface{}, 0, len(pushIssue.Labels))
	for _, label := range pushIssue.Labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		labels = append(labels, map[string]interface{}{"name": label})
	}
	return map[string]interface{}{
		PropertyTitle:       map[string]interface{}{"title": richTextRequest(pushIssue.Title)},
		PropertyBeadsID:     map[string]interface{}{"rich_text": richTextRequest(pushIssue.ID)},
		PropertyStatus:      map[string]interface{}{"select": map[string]interface{}{"name": pushIssue.Status}},
		PropertyPriority:    map[string]interface{}{"select": map[string]interface{}{"name": pushIssue.Priority}},
		PropertyType:        map[string]interface{}{"select": map[string]interface{}{"name": pushIssue.IssueType}},
		PropertyDescription: map[string]interface{}{"rich_text": richTextRequest(pushIssue.Description)},
		PropertyAssignee:    map[string]interface{}{"rich_text": richTextRequest(pushIssue.Assignee)},
		PropertyLabels:      map[string]interface{}{"multi_select": labels},
	}
}

func PulledIssueFromPage(page Page) PulledIssue {
	issue := PulledIssue{
		ID:           strings.TrimSpace(DataSourceTitle(page.Properties[PropertyBeadsID].RichText)),
		Title:        strings.TrimSpace(DataSourceTitle(page.Properties[PropertyTitle].Title)),
		Description:  strings.TrimSpace(DataSourceTitle(page.Properties[PropertyDescription].RichText)),
		Status:       strings.TrimSpace(pagePropertySelect(page.Properties[PropertyStatus])),
		Priority:     strings.TrimSpace(pagePropertySelect(page.Properties[PropertyPriority])),
		Type:         strings.TrimSpace(pagePropertySelect(page.Properties[PropertyType])),
		IssueType:    strings.TrimSpace(pagePropertySelect(page.Properties[PropertyType])),
		Assignee:     strings.TrimSpace(DataSourceTitle(page.Properties[PropertyAssignee].RichText)),
		Labels:       pagePropertyMultiSelect(page.Properties[PropertyLabels]),
		ExternalRef:  strings.TrimSpace(page.URL),
		NotionPageID: strings.TrimSpace(page.ID),
		CreatedAt:    NullableString(page.CreatedTime.UTC().Format(time.RFC3339)),
		UpdatedAt:    NullableString(page.LastEditedTime.UTC().Format(time.RFC3339)),
	}
	if externalRef := BuildNotionExternalRef(&issue); externalRef != "" {
		issue.ExternalRef = externalRef
	}
	return issue
}

func ValidateDataSourceSchema(ds *DataSource) *StatusSchema {
	result := &StatusSchema{Checked: true}
	for name := range requiredSchemaProperties {
		result.Required = append(result.Required, name)
	}
	sort.Strings(result.Required)
	if ds == nil {
		result.Missing = append([]string(nil), result.Required...)
		return result
	}
	for name := range ds.Properties {
		result.Detected = append(result.Detected, name)
	}
	sort.Strings(result.Detected)
	for _, name := range result.Required {
		property, ok := ds.Properties[name]
		if !ok || property.Type != requiredSchemaProperties[name] {
			result.Missing = append(result.Missing, name)
		}
	}
	return result
}

func priorityToBeads(raw string, config *MappingConfig) int {
	value, ok := config.PriorityToBeads[normalizeMappingValue(raw)]
	if !ok {
		return 2
	}
	return value
}

func priorityToNotion(priority int, config *MappingConfig) string {
	value, ok := config.PriorityToNotion[priority]
	if !ok {
		return "Medium"
	}
	return value
}

func statusToBeads(raw string, config *MappingConfig) types.Status {
	value, ok := config.StatusToBeads[normalizeMappingValue(raw)]
	if !ok {
		return types.StatusOpen
	}
	return value
}

func statusToNotion(status types.Status, config *MappingConfig) string {
	value, ok := config.StatusToNotion[status]
	if !ok {
		return "Open"
	}
	return value
}

func typeToBeads(raw string, config *MappingConfig) types.IssueType {
	value, ok := config.TypeToBeads[normalizeMappingValue(raw)]
	if !ok {
		return types.TypeTask
	}
	return value
}

func typeToNotion(issueType types.IssueType, config *MappingConfig) string {
	trimmed := strings.TrimSpace(string(issueType))
	if trimmed == "" {
		return "Task"
	}
	value, ok := config.TypeToNotion[issueType]
	if !ok {
		return "Task"
	}
	return value
}

func parseMappingTimestamp(raw string) (time.Time, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err == nil {
		return parsed, true, nil
	}
	parsed, err = time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false, err
	}
	return parsed, true, nil
}

func normalizeMappingValue(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, " ", "_")
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

func pagePropertySelect(prop PageProperty) string {
	if prop.Select == nil {
		return ""
	}
	return prop.Select.Name
}

func pagePropertyMultiSelect(prop PageProperty) []string {
	labels := make([]string, 0, len(prop.MultiSelect))
	for _, item := range prop.MultiSelect {
		name := strings.TrimSpace(item.Name)
		if name != "" {
			labels = append(labels, name)
		}
	}
	return labels
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
