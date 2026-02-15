// Package tracker provides a plugin framework for external issue tracker integrations.
//
// It defines interfaces (IssueTracker, FieldMapper) and a shared SyncEngine that
// eliminates duplication between tracker integrations (Linear, GitLab, Jira, etc.).
//
// Design based on GitHub issue #1150 and PRs #1564-#1567, updated for Dolt-only storage.
package tracker

import (
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TrackerIssue represents an issue from an external tracker in a generic format.
// Each tracker adapter converts its native issue type to/from this intermediate form.
type TrackerIssue struct {
	// Core identification
	ID         string // External tracker's internal ID (e.g., UUID)
	Identifier string // Human-readable identifier (e.g., "TEAM-123", "PROJ-456")
	URL        string // Web URL to the issue

	// Content
	Title       string
	Description string

	// Classification
	Priority int         // Priority value (tracker-specific, mapped via FieldMapper)
	State    interface{} // Tracker-specific state object (mapped via FieldMapper)
	Type     interface{} // Tracker-specific type (mapped via FieldMapper)
	Labels   []string    // Labels/tags

	// Assignment
	Assignee      string // Assignee name or email
	AssigneeID    string // Assignee's tracker-specific ID
	AssigneeEmail string // Assignee email if available

	// Timestamps
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time

	// Relationships
	ParentID         string // Parent issue identifier (for subtasks/children)
	ParentInternalID string // Parent issue internal ID

	// Raw data for tracker-specific processing
	Raw interface{} // Original API response for tracker-specific access

	// Metadata for tracker-specific fields that don't map to core Issue fields.
	// Stored in Issue.Metadata for round-trip preservation.
	Metadata map[string]interface{}
}

// FetchOptions specifies options for fetching issues from an external tracker.
type FetchOptions struct {
	// State filter: "open", "closed", or "all" (default)
	State string

	// Incremental sync: only fetch issues updated since this time.
	Since *time.Time

	// Maximum number of issues to fetch (0 = no limit).
	Limit int
}

// SyncOptions configures the behavior of a sync operation.
type SyncOptions struct {
	// Pull imports issues from the external tracker.
	Pull bool
	// Push exports issues to the external tracker.
	Push bool
	// DryRun previews sync without making changes.
	DryRun bool
	// CreateOnly only creates new issues, doesn't update existing.
	CreateOnly bool
	// State filters issues: "open", "closed", or "all".
	State string
	// ConflictResolution specifies how to handle bidirectional conflicts.
	ConflictResolution ConflictResolution
	// TypeFilter limits which issue types are synced (empty = all).
	TypeFilter []types.IssueType
	// ExcludeTypes excludes specific issue types from sync.
	ExcludeTypes []types.IssueType
	// ExcludeEphemeral skips ephemeral/wisp issues from push (default behavior in CLI).
	ExcludeEphemeral bool
}

// SyncResult is the complete result of a sync operation.
type SyncResult struct {
	Success  bool      `json:"success"`
	Stats    SyncStats `json:"stats"`
	LastSync string    `json:"last_sync,omitempty"` // RFC3339 timestamp
	Error    string    `json:"error,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
}

// SyncStats accumulates sync statistics.
type SyncStats struct {
	Pulled    int `json:"pulled"`
	Pushed    int `json:"pushed"`
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Skipped   int `json:"skipped"`
	Errors    int `json:"errors"`
	Conflicts int `json:"conflicts"`
}

// PullStats tracks pull operation results.
type PullStats struct {
	Created     int
	Updated     int
	Skipped     int
	Incremental bool
	SyncedSince string
}

// PushStats tracks push operation results.
type PushStats struct {
	Created int
	Updated int
	Skipped int
	Errors  int
}

// Conflict represents a bidirectional modification conflict.
type Conflict struct {
	IssueID            string    // Beads issue ID
	LocalUpdated       time.Time // When the local version was last modified
	ExternalUpdated    time.Time // When the external version was last modified
	ExternalRef        string    // URL or identifier for the external issue
	ExternalIdentifier string    // External tracker's identifier (e.g., "TEAM-123")
	ExternalInternalID string    // External tracker's internal ID (for API calls)
}

// ConflictResolution specifies how to handle sync conflicts.
type ConflictResolution string

const (
	// ConflictTimestamp resolves conflicts by keeping the newer version.
	ConflictTimestamp ConflictResolution = "timestamp"
	// ConflictLocal always keeps the local beads version.
	ConflictLocal ConflictResolution = "local"
	// ConflictExternal always keeps the external tracker's version.
	ConflictExternal ConflictResolution = "external"
)

// IssueConversion holds the result of converting an external tracker issue to beads.
type IssueConversion struct {
	Issue        *types.Issue
	Dependencies []DependencyInfo
}

// DependencyInfo describes a dependency to create after all issues are imported.
type DependencyInfo struct {
	FromExternalID string // External identifier of the dependent issue
	ToExternalID   string // External identifier of the dependency target
	Type           string // Beads dependency type (blocks, related, duplicates, parent-child)
}
