// Package types defines core data structures for the bd issue tracker.
package types

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// Issue represents a trackable work item
type Issue struct {
	ID                 string         `json:"id"`
	ContentHash        string         `json:"content_hash,omitempty"` // SHA256 hash of canonical content (excludes ID, timestamps)
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
	CompactionLevel    int            `json:"compaction_level,omitempty"`
	CompactedAt        *time.Time     `json:"compacted_at,omitempty"`
	CompactedAtCommit  *string        `json:"compacted_at_commit,omitempty"` // Git commit hash when compacted
	OriginalSize       int            `json:"original_size,omitempty"`
	Labels             []string       `json:"labels,omitempty"` // Populated only for export/import
	Dependencies       []*Dependency  `json:"dependencies,omitempty"` // Populated only for export/import
	Comments           []*Comment     `json:"comments,omitempty"`     // Populated only for export/import
}

// ComputeContentHash creates a deterministic hash of the issue's content.
// Uses all substantive fields (excluding ID, timestamps, and compaction metadata)
// to ensure that identical content produces identical hashes across all clones.
func (i *Issue) ComputeContentHash() string {
	h := sha256.New()
	
	// Hash all substantive fields in a stable order
	h.Write([]byte(i.Title))
	h.Write([]byte{0}) // separator
	h.Write([]byte(i.Description))
	h.Write([]byte{0})
	h.Write([]byte(i.Design))
	h.Write([]byte{0})
	h.Write([]byte(i.AcceptanceCriteria))
	h.Write([]byte{0})
	h.Write([]byte(i.Notes))
	h.Write([]byte{0})
	h.Write([]byte(i.Status))
	h.Write([]byte{0})
	h.Write([]byte(fmt.Sprintf("%d", i.Priority)))
	h.Write([]byte{0})
	h.Write([]byte(i.IssueType))
	h.Write([]byte{0})
	h.Write([]byte(i.Assignee))
	h.Write([]byte{0})
	
	if i.ExternalRef != nil {
		h.Write([]byte(*i.ExternalRef))
	}
	
	return fmt.Sprintf("%x", h.Sum(nil))
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
	// Enforce closed_at invariant: closed_at should be set if and only if status is closed
	if i.Status == StatusClosed && i.ClosedAt == nil {
		return fmt.Errorf("closed issues must have closed_at timestamp")
	}
	if i.Status != StatusClosed && i.ClosedAt != nil {
		return fmt.Errorf("non-closed issues cannot have closed_at timestamp")
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

// Comment represents a comment on an issue
type Comment struct {
	ID        int64     `json:"id"`
	IssueID   string    `json:"issue_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
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
	EventCompacted         EventType = "compacted"
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
	TotalIssues              int     `json:"total_issues"`
	OpenIssues               int     `json:"open_issues"`
	InProgressIssues         int     `json:"in_progress_issues"`
	ClosedIssues             int     `json:"closed_issues"`
	BlockedIssues            int     `json:"blocked_issues"`
	ReadyIssues              int     `json:"ready_issues"`
	EpicsEligibleForClosure  int     `json:"epics_eligible_for_closure"`
	AverageLeadTime          float64 `json:"average_lead_time_hours"`
}

// IssueFilter is used to filter issue queries
type IssueFilter struct {
	Status      *Status
	Priority    *int
	IssueType   *IssueType
	Assignee    *string
	Labels      []string  // AND semantics: issue must have ALL these labels
	LabelsAny   []string  // OR semantics: issue must have AT LEAST ONE of these labels
	TitleSearch string
	IDs         []string  // Filter by specific issue IDs
	Limit       int
}

// SortPolicy determines how ready work is ordered
type SortPolicy string

// Sort policy constants
const (
	// SortPolicyHybrid prioritizes recent issues by priority, older by age
	// Recent = created within 48 hours
	// This is the default for backwards compatibility
	SortPolicyHybrid SortPolicy = "hybrid"

	// SortPolicyPriority always sorts by priority first, then creation date
	// Use for autonomous execution, CI/CD, priority-driven workflows
	SortPolicyPriority SortPolicy = "priority"

	// SortPolicyOldest always sorts by creation date (oldest first)
	// Use for backlog clearing, preventing issue starvation
	SortPolicyOldest SortPolicy = "oldest"
)

// IsValid checks if the sort policy value is valid
func (s SortPolicy) IsValid() bool {
	switch s {
	case SortPolicyHybrid, SortPolicyPriority, SortPolicyOldest, "":
		return true
	}
	return false
}

// WorkFilter is used to filter ready work queries
type WorkFilter struct {
	Status     Status
	Priority   *int
	Assignee   *string
	Limit      int
	SortPolicy SortPolicy
}

// EpicStatus represents an epic with its completion status
type EpicStatus struct {
	Epic            *Issue `json:"epic"`
	TotalChildren   int    `json:"total_children"`
	ClosedChildren  int    `json:"closed_children"`
	EligibleForClose bool  `json:"eligible_for_close"`
}
