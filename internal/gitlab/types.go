// Package gitlab provides client and data types for the GitLab REST API.
//
// This package handles all interactions with GitLab's issue tracking system,
// including fetching, creating, and updating issues. It provides bidirectional
// mapping between GitLab's data model and Beads' internal types.
package gitlab

import (
	"net/http"
	"strings"
	"time"
)

// API configuration constants.
const (
	// DefaultAPIEndpoint is the GitLab API v4 endpoint suffix.
	DefaultAPIEndpoint = "/api/v4"

	// DefaultTimeout is the default HTTP request timeout.
	DefaultTimeout = 30 * time.Second

	// MaxRetries is the maximum number of retries for rate-limited requests.
	MaxRetries = 3

	// RetryDelay is the base delay between retries (exponential backoff).
	RetryDelay = time.Second

	// MaxPageSize is the maximum number of issues to fetch per page.
	MaxPageSize = 100

	// MaxPages is the maximum number of pages to fetch before stopping.
	// This prevents infinite loops from malformed X-Next-Page headers.
	MaxPages = 1000
)

// Client provides methods to interact with the GitLab REST API.
type Client struct {
	Token      string       // GitLab personal access token or OAuth token
	BaseURL    string       // GitLab instance URL (e.g., "https://gitlab.com/api/v4")
	ProjectID  string       // Project ID or URL-encoded path (e.g., "group/project")
	HTTPClient *http.Client // Optional custom HTTP client
}

// Issue represents an issue from the GitLab API.
type Issue struct {
	ID          int        `json:"id"`          // Global issue ID
	IID         int        `json:"iid"`         // Project-scoped issue ID
	ProjectID   int        `json:"project_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       string     `json:"state"`       // "opened", "closed", "reopened"
	CreatedAt   *time.Time `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	ClosedBy    *User      `json:"closed_by,omitempty"`
	Labels      []string   `json:"labels"`
	Assignee    *User      `json:"assignee,omitempty"`
	Assignees   []User     `json:"assignees,omitempty"`
	Author      *User      `json:"author,omitempty"`
	Milestone   *Milestone `json:"milestone,omitempty"`
	WebURL      string     `json:"web_url"`
	DueDate     string     `json:"due_date,omitempty"` // YYYY-MM-DD format
	Weight      int        `json:"weight,omitempty"`   // GitLab Premium feature
	Type        string     `json:"type,omitempty"`     // "issue", "incident", "test_case", "task"
	Confidential bool      `json:"confidential"`

	// Links contains related URLs (populated in some API responses)
	Links *IssueLinks `json:"links,omitempty"`
}

// IssueLinks contains related URLs for an issue.
type IssueLinks struct {
	Self       string `json:"self,omitempty"`
	Notes      string `json:"notes,omitempty"`
	AwardEmoji string `json:"award_emoji,omitempty"`
	Project    string `json:"project,omitempty"`
}

// User represents a GitLab user.
type User struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	WebURL    string `json:"web_url,omitempty"`
	State     string `json:"state,omitempty"` // "active", "blocked", etc.
}

// Milestone represents a GitLab milestone.
type Milestone struct {
	ID          int        `json:"id"`
	IID         int        `json:"iid"`
	ProjectID   int        `json:"project_id,omitempty"`
	GroupID     int        `json:"group_id,omitempty"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	State       string     `json:"state"` // "active", "closed"
	DueDate     string     `json:"due_date,omitempty"`
	StartDate   string     `json:"start_date,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	WebURL      string     `json:"web_url,omitempty"`
}

// Label represents a GitLab label.
type Label struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	Color             string `json:"color"`
	Description       string `json:"description,omitempty"`
	TextColor         string `json:"text_color,omitempty"`
	Priority          *int   `json:"priority,omitempty"`
	IsProjectLabel    bool   `json:"is_project_label,omitempty"`
	SubscribedBoolean bool   `json:"subscribed,omitempty"`
}

// IssueLink represents a link between two issues.
type IssueLink struct {
	SourceIssue *Issue `json:"source_issue"`
	TargetIssue *Issue `json:"target_issue"`
	LinkType    string `json:"link_type"` // "relates_to", "blocks", "is_blocked_by"
}

// Project represents a GitLab project.
type Project struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	Description       string `json:"description,omitempty"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch,omitempty"`
	Namespace         *Namespace `json:"namespace,omitempty"`
}

// Namespace represents a GitLab namespace (group or user).
type Namespace struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"` // "user" or "group"
	FullPath string `json:"full_path"`
	WebURL   string `json:"web_url,omitempty"`
}

// SyncStats tracks statistics for a GitLab sync operation.
type SyncStats struct {
	Pulled    int `json:"pulled"`
	Pushed    int `json:"pushed"`
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Skipped   int `json:"skipped"`
	Errors    int `json:"errors"`
	Conflicts int `json:"conflicts"`
}

// SyncResult represents the result of a GitLab sync operation.
type SyncResult struct {
	Success  bool      `json:"success"`
	Stats    SyncStats `json:"stats"`
	LastSync string    `json:"last_sync,omitempty"`
	Error    string    `json:"error,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
}

// PullStats tracks pull operation statistics.
type PullStats struct {
	Created     int
	Updated     int
	Skipped     int
	Incremental bool     // Whether this was an incremental sync
	SyncedSince string   // Timestamp we synced since (if incremental)
	Warnings    []string // Non-fatal warnings encountered during pull
}

// PushStats tracks push operation statistics.
type PushStats struct {
	Created int
	Updated int
	Skipped int
	Errors  int
}

// Conflict represents a conflict between local and GitLab versions.
// A conflict occurs when both the local and GitLab versions have been modified
// since the last sync.
type Conflict struct {
	IssueID           string    // Beads issue ID
	LocalUpdated      time.Time // When the local version was last modified
	GitLabUpdated     time.Time // When the GitLab version was last modified
	GitLabExternalRef string    // URL to the GitLab issue
	GitLabIID         int       // GitLab issue IID (project-scoped)
	GitLabID          int       // GitLab's global issue ID
}

// IssueConversion holds the result of converting a GitLab issue to Beads.
// It includes the issue and any dependencies that should be created.
// Note: This type is defined in types.go but populated by mapping.go which imports internal/types.
type IssueConversion struct {
	Issue        interface{} // *types.Issue when populated by mapping.go
	Dependencies []DependencyInfo
}

// DependencyInfo represents a dependency to be created after issue import.
// Stored separately since we need all issues imported before linking dependencies.
type DependencyInfo struct {
	FromGitLabIID int    // GitLab IID of the dependent issue
	ToGitLabIID   int    // GitLab IID of the dependency target
	Type          string // Beads dependency type (blocks, related, parent-child)
}

// StateCache caches issue states for the project.
type StateCache struct {
	Labels     []Label
	Milestones []Milestone
}

// Valid GitLab issue states.
var validStates = map[string]bool{
	"opened":   true,
	"closed":   true,
	"reopened": true, // GitLab uses this state after reopening
}

// IsValidState checks if a GitLab state string is valid.
func IsValidState(state string) bool {
	return validStates[state]
}

// ParseLabelPrefix splits a label into prefix and value.
// GitLab labels like "priority::high" are split into ("priority", "high").
// Labels without "::" return empty prefix and the original label as value.
func ParseLabelPrefix(label string) (prefix, value string) {
	parts := strings.SplitN(label, "::", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", label
}

// Priority label values and their beads priority mapping.
var priorityMapping = map[string]int{
	"critical": 0, // P0
	"high":     1, // P1
	"medium":   2, // P2
	"low":      3, // P3
	"none":     4, // P4
}

// Status label values and their beads status mapping.
var statusMapping = map[string]string{
	"open":        "open",
	"in_progress": "in_progress",
	"blocked":     "blocked",
	"deferred":    "deferred",
	"closed":      "closed",
}

// Type label values and their beads issue type mapping.
var typeMapping = map[string]string{
	"bug":         "bug",
	"feature":     "feature",
	"task":        "task",
	"epic":        "epic",
	"chore":       "chore",
	"enhancement": "feature",
}

// GetPriorityFromLabel returns the beads priority for a priority label value.
// Returns -1 if the value is not recognized.
func GetPriorityFromLabel(value string) int {
	if p, ok := priorityMapping[strings.ToLower(value)]; ok {
		return p
	}
	return -1
}

// GetStatusFromLabel returns the beads status for a status label value.
// Returns empty string if the value is not recognized.
func GetStatusFromLabel(value string) string {
	return statusMapping[strings.ToLower(value)]
}

// GetTypeFromLabel returns the beads issue type for a type label value.
// Returns empty string if the value is not recognized.
func GetTypeFromLabel(value string) string {
	return typeMapping[strings.ToLower(value)]
}
