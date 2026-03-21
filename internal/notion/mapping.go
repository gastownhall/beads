package notion

import (
	"encoding/json"
	"fmt"
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
			0: "critical",
			1: "high",
			2: "medium",
			3: "low",
			4: "backlog",
		},
		StatusToBeads: map[string]types.Status{
			"open":        types.StatusOpen,
			"in_progress": types.StatusInProgress,
			"blocked":     types.StatusBlocked,
			"deferred":    types.StatusDeferred,
			"closed":      types.StatusClosed,
		},
		StatusToNotion: map[types.Status]string{
			types.StatusOpen:       "open",
			types.StatusInProgress: "in_progress",
			types.StatusBlocked:    "blocked",
			types.StatusDeferred:   "deferred",
			types.StatusClosed:     "closed",
		},
		TypeToBeads: map[string]types.IssueType{
			"bug":     types.TypeBug,
			"feature": types.TypeFeature,
			"task":    types.TypeTask,
			"epic":    types.TypeEpic,
			"chore":   types.TypeChore,
		},
		TypeToNotion: map[types.IssueType]string{
			types.TypeBug:     "bug",
			types.TypeFeature: "feature",
			types.TypeTask:    "task",
			types.TypeEpic:    "epic",
			types.TypeChore:   "chore",
		},
	}
}

// BeadsIssueFromPullIssue converts one pulled Notion issue into a beads core issue.
func BeadsIssueFromPullIssue(issue PulledIssue, config *MappingConfig) (*types.Issue, error) {
	if config == nil {
		config = DefaultMappingConfig()
	}

	status, err := statusToBeads(issue.Status, config)
	if err != nil {
		return nil, err
	}
	priority, err := priorityToBeads(issue.Priority, config)
	if err != nil {
		return nil, err
	}
	issueTypeRaw := issue.IssueType
	if strings.TrimSpace(issueTypeRaw) == "" {
		issueTypeRaw = issue.Type
	}
	issueType, err := typeToBeads(issueTypeRaw, config)
	if err != nil {
		return nil, err
	}

	createdAt, err := parseMappingTimestamp(string(issue.CreatedAt))
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	updatedAt, err := parseMappingTimestamp(string(issue.UpdatedAt))
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	beadsIssue := &types.Issue{
		ID:           issue.ID,
		Title:        issue.Title,
		Description:  issue.Description,
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

	var url string
	if canonical, ok := CanonicalizeNotionPageURL(issue.ExternalRef); ok {
		url = canonical
	}
	if url == "" {
		url = strings.TrimSpace(issue.URL)
	}

	trackerIssue := &tracker.TrackerIssue{
		ID:          issue.NotionPageID,
		Identifier:  ExtractNotionIdentifier(urlOrFallback(url, issue.NotionPageID)),
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

// PushPayloadFromIssue converts a beads issue into the Notion push payload shape.
func PushPayloadFromIssue(issue *types.Issue, config *MappingConfig) (*PushPayload, error) {
	return PushPayloadFromIssues([]*types.Issue{issue}, config)
}

// PushPayloadFromIssues converts multiple beads issues into the Notion push payload shape.
func PushPayloadFromIssues(issues []*types.Issue, config *MappingConfig) (*PushPayload, error) {
	return PushPayloadFromIssuesWithExisting(issues, nil, config)
}

// PushPayloadFromIssuesWithExisting converts multiple beads issues plus optional remote snapshots into the Notion push payload shape.
func PushPayloadFromIssuesWithExisting(issues []*types.Issue, existing []ExistingIssue, config *MappingConfig) (*PushPayload, error) {
	pushIssues := make([]PushIssue, 0, len(issues))
	for _, issue := range issues {
		pushIssue, err := PushIssueFromIssue(issue, config)
		if err != nil {
			return nil, err
		}
		pushIssues = append(pushIssues, *pushIssue)
	}
	return &PushPayload{
		Issues:         pushIssues,
		ExistingIssues: append([]ExistingIssue(nil), existing...),
	}, nil
}

// PushIssueFromIssue converts one beads issue into the Notion push issue shape.
func PushIssueFromIssue(issue *types.Issue, config *MappingConfig) (*PushIssue, error) {
	if issue == nil {
		return nil, fmt.Errorf("issue is nil")
	}
	if config == nil {
		config = DefaultMappingConfig()
	}

	status, err := statusToNotion(issue.Status, config)
	if err != nil {
		return nil, err
	}
	priority, err := priorityToNotion(issue.Priority, config)
	if err != nil {
		return nil, err
	}
	issueType, err := typeToNotion(issue.IssueType, config)
	if err != nil {
		return nil, err
	}

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
		canonical, ok := CanonicalizeNotionExternalRef(*issue.ExternalRef)
		if !ok {
			return nil, fmt.Errorf("unsupported notion external ref %q", *issue.ExternalRef)
		}
		pushIssue.ExternalRef = canonical
	}

	return pushIssue, nil
}

// MarshalPushPayload renders the push payload as JSON.
func MarshalPushPayload(payload *PushPayload) ([]byte, error) {
	if payload == nil {
		return nil, fmt.Errorf("push payload is nil")
	}
	return json.Marshal(payload)
}

// ExistingIssueFromPullIssue narrows a pull response item to the fields Notion push can reuse.
func ExistingIssueFromPullIssue(issue PulledIssue) ExistingIssue {
	return ExistingIssue{
		ID:           strings.TrimSpace(issue.ID),
		Title:        strings.TrimSpace(issue.Title),
		Description:  strings.TrimSpace(issue.Description),
		Status:       strings.TrimSpace(issue.Status),
		Priority:     strings.TrimSpace(issue.Priority),
		IssueType:    strings.TrimSpace(firstNonEmpty(issue.IssueType, issue.Type)),
		Assignee:     strings.TrimSpace(issue.Assignee),
		Labels:       append([]string(nil), issue.Labels...),
		ExternalRef:  strings.TrimSpace(issue.ExternalRef),
		NotionPageID: strings.TrimSpace(issue.NotionPageID),
		CreatedAt:    strings.TrimSpace(string(issue.CreatedAt)),
		UpdatedAt:    strings.TrimSpace(string(issue.UpdatedAt)),
	}
}

func priorityToBeads(raw string, config *MappingConfig) (int, error) {
	value, ok := config.PriorityToBeads[strings.ToLower(strings.TrimSpace(raw))]
	if !ok {
		return 0, fmt.Errorf("unsupported notion priority %q", raw)
	}
	return value, nil
}

func priorityToNotion(priority int, config *MappingConfig) (string, error) {
	value, ok := config.PriorityToNotion[priority]
	if !ok {
		return "", fmt.Errorf("unsupported beads priority %d", priority)
	}
	return value, nil
}

func statusToBeads(raw string, config *MappingConfig) (types.Status, error) {
	value, ok := config.StatusToBeads[strings.ToLower(strings.TrimSpace(raw))]
	if !ok {
		return "", fmt.Errorf("unsupported notion status %q", raw)
	}
	return value, nil
}

func statusToNotion(status types.Status, config *MappingConfig) (string, error) {
	value, ok := config.StatusToNotion[status]
	if !ok {
		return "", fmt.Errorf("unsupported beads status %q", status)
	}
	return value, nil
}

func typeToBeads(raw string, config *MappingConfig) (types.IssueType, error) {
	value, ok := config.TypeToBeads[strings.ToLower(strings.TrimSpace(raw))]
	if !ok {
		return "", fmt.Errorf("unsupported notion issue type %q", raw)
	}
	return value, nil
}

func typeToNotion(issueType types.IssueType, config *MappingConfig) (string, error) {
	trimmed := strings.TrimSpace(string(issueType))
	if trimmed == "" {
		return "", nil
	}
	value, ok := config.TypeToNotion[issueType]
	if !ok {
		return trimmed, nil
	}
	return value, nil
}

func parseMappingTimestamp(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func urlOrFallback(url, pageID string) string {
	if url != "" {
		return url
	}
	return "notion:" + pageID
}
