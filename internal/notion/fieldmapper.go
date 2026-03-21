package notion

import (
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// FieldMapper implements tracker.FieldMapper for Notion.
type FieldMapper struct {
	config *MappingConfig
}

// NewFieldMapper constructs a Notion field mapper with the default mapping when config is nil.
func NewFieldMapper(config *MappingConfig) *FieldMapper {
	if config == nil {
		config = DefaultMappingConfig()
	}
	return &FieldMapper{config: config}
}

func (m *FieldMapper) PriorityToBeads(trackerPriority interface{}) int {
	value, ok := trackerPriority.(string)
	if !ok {
		return 2
	}
	priority, err := priorityToBeads(value, m.config)
	if err != nil {
		return 2
	}
	return priority
}

func (m *FieldMapper) PriorityToTracker(beadsPriority int) interface{} {
	priority, err := priorityToNotion(beadsPriority, m.config)
	if err != nil {
		return ""
	}
	return priority
}

func (m *FieldMapper) StatusToBeads(trackerState interface{}) types.Status {
	value, ok := trackerState.(string)
	if !ok {
		return types.StatusOpen
	}
	status, err := statusToBeads(value, m.config)
	if err != nil {
		return types.StatusOpen
	}
	return status
}

func (m *FieldMapper) StatusToTracker(beadsStatus types.Status) interface{} {
	status, err := statusToNotion(beadsStatus, m.config)
	if err != nil {
		return ""
	}
	return status
}

func (m *FieldMapper) TypeToBeads(trackerType interface{}) types.IssueType {
	value, ok := trackerType.(string)
	if !ok {
		return types.TypeTask
	}
	issueType, err := typeToBeads(value, m.config)
	if err != nil {
		return types.TypeTask
	}
	return issueType
}

func (m *FieldMapper) TypeToTracker(beadsType types.IssueType) interface{} {
	return typeToNotion(beadsType, m.config)
}

func (m *FieldMapper) IssueToBeads(ti *tracker.TrackerIssue) *tracker.IssueConversion {
	if ti == nil {
		return nil
	}

	switch raw := ti.Raw.(type) {
	case *PulledIssue:
		issue, err := BeadsIssueFromPullIssue(*raw, m.config)
		if err != nil {
			return nil
		}
		return &tracker.IssueConversion{Issue: issue}
	case PulledIssue:
		issue, err := BeadsIssueFromPullIssue(raw, m.config)
		if err != nil {
			return nil
		}
		return &tracker.IssueConversion{Issue: issue}
	default:
		return nil
	}
}

func (m *FieldMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	pushIssue, err := PushIssueFromIssue(issue, m.config)
	if err != nil {
		return map[string]interface{}{}
	}

	return map[string]interface{}{
		"id":           pushIssue.ID,
		"title":        pushIssue.Title,
		"description":  pushIssue.Description,
		"status":       pushIssue.Status,
		"priority":     pushIssue.Priority,
		"issue_type":   pushIssue.IssueType,
		"assignee":     pushIssue.Assignee,
		"labels":       pushIssue.Labels,
		"external_ref": pushIssue.ExternalRef,
	}
}
