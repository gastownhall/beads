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
	ContentHash        string         `json:"-"` // Internal: SHA256 hash of canonical content (excludes ID, timestamps) - NOT exported to JSONL
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
	CloseReason        string         `json:"close_reason,omitempty"` // Reason provided when closing the issue
	ExternalRef        *string        `json:"external_ref,omitempty"` // e.g., "gh-9", "jira-ABC"
	CompactionLevel    int            `json:"compaction_level,omitempty"`
	CompactedAt        *time.Time     `json:"compacted_at,omitempty"`
	CompactedAtCommit  *string        `json:"compacted_at_commit,omitempty"` // Git commit hash when compacted
	OriginalSize       int            `json:"original_size,omitempty"`
	SourceRepo         string         `json:"-"` // Internal: Which repo owns this issue (multi-repo support) - NOT exported to JSONL
	Labels             []string       `json:"labels,omitempty"` // Populated only for export/import
	Dependencies       []*Dependency  `json:"dependencies,omitempty"` // Populated only for export/import
	Comments           []*Comment     `json:"comments,omitempty"`     // Populated only for export/import
	// Tombstone fields (bd-vw8): inline soft-delete support
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`    // When the issue was deleted
	DeletedBy    string     `json:"deleted_by,omitempty"`    // Who deleted the issue
	DeleteReason string     `json:"delete_reason,omitempty"` // Why the issue was deleted
	OriginalType string     `json:"original_type,omitempty"` // Issue type before deletion (for tombstones)

	// Messaging fields (bd-kwro): inter-agent communication support
	Sender    string `json:"sender,omitempty"`    // Who sent this (for messages)
	Ephemeral bool   `json:"ephemeral,omitempty"` // Can be bulk-deleted when closed
	// NOTE: RepliesTo, RelatesTo, DuplicateOf, SupersededBy moved to dependencies table
	// per Decision 004 (Edge Schema Consolidation). Use dependency API instead.

	// Pinned field (bd-7h5): persistent context markers
	Pinned bool `json:"pinned,omitempty"` // If true, issue is a persistent context marker, not a work item

	// Template field (beads-1ra): template molecule support
	IsTemplate bool `json:"is_template,omitempty"` // If true, issue is a read-only template molecule
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
	h.Write([]byte{0})
	if i.Pinned {
		h.Write([]byte("pinned"))
	}
	h.Write([]byte{0})
	if i.IsTemplate {
		h.Write([]byte("template"))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// DefaultTombstoneTTL is the default time-to-live for tombstones (30 days)
const DefaultTombstoneTTL = 30 * 24 * time.Hour

// MinTombstoneTTL is the minimum allowed TTL (7 days) to prevent data loss
const MinTombstoneTTL = 7 * 24 * time.Hour

// ClockSkewGrace is added to TTL to handle clock drift between machines
const ClockSkewGrace = 1 * time.Hour

// IsTombstone returns true if the issue has been soft-deleted (bd-vw8)
func (i *Issue) IsTombstone() bool {
	return i.Status == StatusTombstone
}

// IsExpired returns true if the tombstone has exceeded its TTL.
// Non-tombstone issues always return false.
// ttl is the configured TTL duration:
//   - If zero, DefaultTombstoneTTL (30 days) is used
//   - If negative, the tombstone is immediately expired (for --hard mode)
//   - If positive, ClockSkewGrace is added only for TTLs > 1 hour
func (i *Issue) IsExpired(ttl time.Duration) bool {
	// Non-tombstones never expire
	if !i.IsTombstone() {
		return false
	}

	// Tombstones without DeletedAt are not expired (safety: shouldn't happen in valid data)
	if i.DeletedAt == nil {
		return false
	}

	// Negative TTL means "immediately expired" - for --hard mode (bd-4q8 fix)
	if ttl < 0 {
		return true
	}

	// Use default TTL if not specified
	if ttl == 0 {
		ttl = DefaultTombstoneTTL
	}

	// Only add clock skew grace period for normal TTLs (> 1 hour).
	// For short TTLs (testing/development), skip grace period.
	effectiveTTL := ttl
	if ttl > ClockSkewGrace {
		effectiveTTL = ttl + ClockSkewGrace
	}

	// Check if the tombstone has exceeded its TTL
	expirationTime := i.DeletedAt.Add(effectiveTTL)
	return time.Now().After(expirationTime)
}

// Validate checks if the issue has valid field values (built-in statuses only)
func (i *Issue) Validate() error {
	return i.ValidateWithCustomStatuses(nil)
}

// ValidateWithCustomStatuses checks if the issue has valid field values,
// allowing custom statuses in addition to built-in ones.
func (i *Issue) ValidateWithCustomStatuses(customStatuses []string) error {
	if len(i.Title) == 0 {
		return fmt.Errorf("title is required")
	}
	if len(i.Title) > 500 {
		return fmt.Errorf("title must be 500 characters or less (got %d)", len(i.Title))
	}
	if i.Priority < 0 || i.Priority > 4 {
		return fmt.Errorf("priority must be between 0 and 4 (got %d)", i.Priority)
	}
	if !i.Status.IsValidWithCustom(customStatuses) {
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
	// Enforce tombstone invariants (bd-md2): deleted_at must be set for tombstones, and only for tombstones
	if i.Status == StatusTombstone && i.DeletedAt == nil {
		return fmt.Errorf("tombstone issues must have deleted_at timestamp")
	}
	if i.Status != StatusTombstone && i.DeletedAt != nil {
		return fmt.Errorf("non-tombstone issues cannot have deleted_at timestamp")
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
	StatusTombstone  Status = "tombstone" // Soft-deleted issue (bd-vw8)
	StatusPinned     Status = "pinned"    // Persistent bead that stays open indefinitely (bd-6v2)
)

// IsValid checks if the status value is valid (built-in statuses only)
func (s Status) IsValid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusBlocked, StatusClosed, StatusTombstone, StatusPinned:
		return true
	}
	return false
}

// IsValidWithCustom checks if the status is valid, including custom statuses.
// Custom statuses are user-defined via bd config set status.custom "status1,status2,..."
func (s Status) IsValidWithCustom(customStatuses []string) bool {
	// First check built-in statuses
	if s.IsValid() {
		return true
	}
	// Then check custom statuses
	for _, custom := range customStatuses {
		if string(s) == custom {
			return true
		}
	}
	return false
}

// IssueType categorizes the kind of work
type IssueType string

// Issue type constants
const (
	TypeBug          IssueType = "bug"
	TypeFeature      IssueType = "feature"
	TypeTask         IssueType = "task"
	TypeEpic         IssueType = "epic"
	TypeChore        IssueType = "chore"
	TypeMessage      IssueType = "message"       // Ephemeral communication between workers
	TypeMergeRequest IssueType = "merge-request" // Merge queue entry for refinery processing
	TypeMolecule     IssueType = "molecule"      // Template molecule for issue hierarchies (beads-1ra)
)

// IsValid checks if the issue type value is valid
func (t IssueType) IsValid() bool {
	switch t {
	case TypeBug, TypeFeature, TypeTask, TypeEpic, TypeChore, TypeMessage, TypeMergeRequest, TypeMolecule:
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
	// Metadata contains type-specific edge data (JSON blob)
	// Examples: similarity scores, approval details, skill proficiency
	Metadata string `json:"metadata,omitempty"`
	// ThreadID groups conversation edges for efficient thread queries
	// For replies-to edges, this identifies the conversation root
	ThreadID string `json:"thread_id,omitempty"`
}

// DependencyCounts holds counts for dependencies and dependents
type DependencyCounts struct {
	DependencyCount int `json:"dependency_count"` // Number of issues this issue depends on
	DependentCount  int `json:"dependent_count"`  // Number of issues that depend on this issue
}

// IssueWithDependencyMetadata extends Issue with dependency relationship type
// Note: We explicitly include all Issue fields to ensure proper JSON marshaling
type IssueWithDependencyMetadata struct {
	Issue
	DependencyType DependencyType `json:"dependency_type"`
}

// IssueWithCounts extends Issue with dependency relationship counts
type IssueWithCounts struct {
	*Issue
	DependencyCount int `json:"dependency_count"`
	DependentCount  int `json:"dependent_count"`
}

// DependencyType categorizes the relationship
type DependencyType string

// Dependency type constants
const (
	// Workflow types (affect ready work calculation)
	DepBlocks      DependencyType = "blocks"
	DepParentChild DependencyType = "parent-child"

	// Association types
	DepRelated        DependencyType = "related"
	DepDiscoveredFrom DependencyType = "discovered-from"

	// Graph link types (bd-kwro)
	DepRepliesTo  DependencyType = "replies-to"  // Conversation threading
	DepRelatesTo  DependencyType = "relates-to"  // Loose knowledge graph edges
	DepDuplicates DependencyType = "duplicates"  // Deduplication link
	DepSupersedes DependencyType = "supersedes"  // Version chain link

	// Entity types (HOP foundation - Decision 004)
	DepAuthoredBy DependencyType = "authored-by" // Creator relationship
	DepAssignedTo DependencyType = "assigned-to" // Assignment relationship
	DepApprovedBy DependencyType = "approved-by" // Approval relationship
)

// IsValid checks if the dependency type value is valid.
// Accepts any non-empty string up to 50 characters.
// Use IsWellKnown() to check if it's a built-in type.
func (d DependencyType) IsValid() bool {
	return len(d) > 0 && len(d) <= 50
}

// IsWellKnown checks if the dependency type is a well-known constant.
// Returns false for custom/user-defined types (which are still valid).
func (d DependencyType) IsWellKnown() bool {
	switch d {
	case DepBlocks, DepParentChild, DepRelated, DepDiscoveredFrom,
		DepRepliesTo, DepRelatesTo, DepDuplicates, DepSupersedes,
		DepAuthoredBy, DepAssignedTo, DepApprovedBy:
		return true
	}
	return false
}

// AffectsReadyWork returns true if this dependency type blocks work.
// Only "blocks" and "parent-child" relationships affect the ready work calculation.
func (d DependencyType) AffectsReadyWork() bool {
	return d == DepBlocks || d == DepParentChild
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
	Depth     int    `json:"depth"`
	ParentID  string `json:"parent_id"`
	Truncated bool   `json:"truncated"`
}

// Statistics provides aggregate metrics
type Statistics struct {
	TotalIssues              int     `json:"total_issues"`
	OpenIssues               int     `json:"open_issues"`
	InProgressIssues         int     `json:"in_progress_issues"`
	ClosedIssues             int     `json:"closed_issues"`
	BlockedIssues            int     `json:"blocked_issues"`
	ReadyIssues              int     `json:"ready_issues"`
	TombstoneIssues          int     `json:"tombstone_issues"` // Soft-deleted issues (bd-nyt)
	PinnedIssues             int     `json:"pinned_issues"`    // Persistent issues (bd-6v2)
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
	
	// Pattern matching
	TitleContains       string
	DescriptionContains string
	NotesContains       string
	
	// Date ranges
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	UpdatedAfter  *time.Time
	UpdatedBefore *time.Time
	ClosedAfter   *time.Time
	ClosedBefore  *time.Time
	
	// Empty/null checks
	EmptyDescription bool
	NoAssignee       bool
	NoLabels         bool
	
	// Numeric ranges
	PriorityMin *int
	PriorityMax *int

	// Tombstone filtering (bd-1bu)
	IncludeTombstones bool // If false (default), exclude tombstones from results

	// Ephemeral filtering (bd-kwro.9)
	Ephemeral *bool // Filter by ephemeral flag (nil = any, true = only ephemeral, false = only non-ephemeral)

	// Pinned filtering (bd-7h5)
	Pinned *bool // Filter by pinned flag (nil = any, true = only pinned, false = only non-pinned)

	// Template filtering (beads-1ra)
	IsTemplate *bool // Filter by template flag (nil = any, true = only templates, false = exclude templates)
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
	Type       string     // Filter by issue type (task, bug, feature, epic, merge-request, etc.)
	Priority   *int
	Assignee   *string
	Unassigned bool       // Filter for issues with no assignee
	Labels     []string   // AND semantics: issue must have ALL these labels
	LabelsAny  []string   // OR semantics: issue must have AT LEAST ONE of these labels
	Limit      int
	SortPolicy SortPolicy
}

// StaleFilter is used to filter stale issue queries
type StaleFilter struct {
	Days   int    // Issues not updated in this many days
	Status string // Filter by status (open|in_progress|blocked), empty = all non-closed
	Limit  int    // Maximum issues to return
}

// EpicStatus represents an epic with its completion status
type EpicStatus struct {
	Epic            *Issue `json:"epic"`
	TotalChildren   int    `json:"total_children"`
	ClosedChildren  int    `json:"closed_children"`
	EligibleForClose bool  `json:"eligible_for_close"`
}
