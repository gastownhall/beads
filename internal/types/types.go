// Package types defines core data structures for the bd issue tracker.
package types

import (
	"fmt"
	"time"
)

// Issue represents a trackable work item
type Issue struct {
	ID                 string         `json:"id"`
	Title              string         `json:"title"`
	Description        string         `json:"description"`
	Design             string         `json:"design,omitempty"`
	AcceptanceCriteria string         `json:"acceptance_criteria,omitempty"`
	Notes              string         `json:"notes,omitempty"`
	Status             Status         `json:"status"`
	Priority           int            `json:"priority"`
	IssueType          IssueType      `json:"issue_type"`
	Assignee           string         `json:"assignee,omitempty"`
	EstimatedMinutes   *int           `json:"estimated_minutes,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	ClosedAt           *time.Time     `json:"closed_at,omitempty"`
	ExternalRef        *string        `json:"external_ref,omitempty"` // e.g., "gh-9", "jira-ABC"
	Dependencies       []*Dependency  `json:"dependencies,omitempty"` // Populated only for export/import
}

// Validate checks if the issue has valid field values
func (i *Issue) Validate() error {
	if len(i.Title) == 0 {
		return fmt.Errorf("title is required")
	}
	if len(i.Title) > 500 {
		return fmt.Errorf("title must be 500 characters or less (got %d)", len(i.Title))
	}
	if i.Priority < 0 || i.Priority > 4 {
		return fmt.Errorf("priority must be between 0 and 4 (got %d)", i.Priority)
	}
	if !i.Status.IsValid() {
		return fmt.Errorf("invalid status: %s", i.Status)
	}
	if !i.IssueType.IsValid() {
		return fmt.Errorf("invalid issue type: %s", i.IssueType)
	}
	if i.EstimatedMinutes != nil && *i.EstimatedMinutes < 0 {
		return fmt.Errorf("estimated_minutes cannot be negative")
	}
	return nil
}

// Status represents the current state of an issue
type Status string

// Issue status constants
const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	StatusClosed     Status = "closed"
)

// IsValid checks if the status value is valid
func (s Status) IsValid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusBlocked, StatusClosed:
		return true
	}
	return false
}

// IssueType categorizes the kind of work
type IssueType string

// Issue type constants
const (
	TypeBug     IssueType = "bug"
	TypeFeature IssueType = "feature"
	TypeTask    IssueType = "task"
	TypeEpic    IssueType = "epic"
	TypeChore   IssueType = "chore"
)

// IsValid checks if the issue type value is valid
func (t IssueType) IsValid() bool {
	switch t {
	case TypeBug, TypeFeature, TypeTask, TypeEpic, TypeChore:
		return true
	}
	return false
}

// Dependency represents a relationship between issues
type Dependency struct {
	IssueID     string         `json:"issue_id"`
	DependsOnID string         `json:"depends_on_id"`
	Type        DependencyType `json:"type"`
	CreatedAt   time.Time      `json:"created_at"`
	CreatedBy   string         `json:"created_by"`
}

// DependencyType categorizes the relationship
type DependencyType string

// Dependency type constants
const (
	DepBlocks         DependencyType = "blocks"
	DepRelated        DependencyType = "related"
	DepParentChild    DependencyType = "parent-child"
	DepDiscoveredFrom DependencyType = "discovered-from"
)

// IsValid checks if the dependency type value is valid
func (d DependencyType) IsValid() bool {
	switch d {
	case DepBlocks, DepRelated, DepParentChild, DepDiscoveredFrom:
		return true
	}
	return false
}

// Label represents a tag on an issue
type Label struct {
	IssueID string `json:"issue_id"`
	Label   string `json:"label"`
}

// Event represents an audit trail entry
type Event struct {
	ID        int64      `json:"id"`
	IssueID   string     `json:"issue_id"`
	EventType EventType  `json:"event_type"`
	Actor     string     `json:"actor"`
	OldValue  *string    `json:"old_value,omitempty"`
	NewValue  *string    `json:"new_value,omitempty"`
	Comment   *string    `json:"comment,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// EventType categorizes audit trail events
type EventType string

// Event type constants for audit trail
const (
	EventCreated           EventType = "created"
	EventUpdated           EventType = "updated"
	EventStatusChanged     EventType = "status_changed"
	EventCommented         EventType = "commented"
	EventClosed            EventType = "closed"
	EventReopened          EventType = "reopened"
	EventDependencyAdded   EventType = "dependency_added"
	EventDependencyRemoved EventType = "dependency_removed"
	EventLabelAdded        EventType = "label_added"
	EventLabelRemoved      EventType = "label_removed"
)

// BlockedIssue extends Issue with blocking information
type BlockedIssue struct {
	Issue
	BlockedByCount int      `json:"blocked_by_count"`
	BlockedBy      []string `json:"blocked_by"`
}

// TreeNode represents a node in a dependency tree
type TreeNode struct {
	Issue
	Depth     int  `json:"depth"`
	Truncated bool `json:"truncated"`
}

// Statistics provides aggregate metrics
type Statistics struct {
	TotalIssues      int     `json:"total_issues"`
	OpenIssues       int     `json:"open_issues"`
	InProgressIssues int     `json:"in_progress_issues"`
	ClosedIssues     int     `json:"closed_issues"`
	BlockedIssues    int     `json:"blocked_issues"`
	ReadyIssues      int     `json:"ready_issues"`
	AverageLeadTime  float64 `json:"average_lead_time_hours"`
}

// IssueFilter is used to filter issue queries
type IssueFilter struct {
	Status    *Status
	Priority  *int
	IssueType *IssueType
	Assignee  *string
	Labels    []string
	Limit     int
}

// WorkFilter is used to filter ready work queries
type WorkFilter struct {
	Status   Status
	Priority *int
	Assignee *string
	Limit    int
}
