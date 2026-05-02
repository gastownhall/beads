// Package github provides client and data types for the GitHub REST API.
//
// This package handles all interactions with GitHub's issue tracking system,
// including fetching, creating, and updating issues. It provides bidirectional
// mapping between GitHub's data model and Beads' internal types.
package github

import (
	"net/http"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// API configuration constants.
const (
	// DefaultBaseURL is the GitHub API v3 base URL.
	DefaultBaseURL = "https://api.github.com"

	// DefaultTimeout is the default HTTP request timeout.
	DefaultTimeout = 30 * time.Second

	// MaxRetries is the maximum number of retries for rate-limited requests.
	MaxRetries = 3

	// RetryDelay is the base delay between retries (exponential backoff).
	RetryDelay = time.Second

	// MaxPerPage is the maximum number of issues to fetch per page.
	MaxPerPage = 100

	// MaxPages is the maximum number of pages to fetch before stopping.
	// This prevents infinite loops from malformed Link headers.
	MaxPages = 1000
)

// Client provides methods to interact with the GitHub REST API.
type Client struct {
	Token      string       // GitHub personal access token
	BaseURL    string       // GitHub API base URL (e.g., "https://api.github.com")
	Owner      string       // Repository owner (user or organization)
	Repo       string       // Repository name
	HTTPClient *http.Client // Optional custom HTTP client
	Retry      RetryConfig  // Retry/backoff policy for rate limits and transient errors
}

// Issue represents an issue from the GitHub API.
type Issue struct {
	ID        int        `json:"id"`     // Global issue ID
	Number    int        `json:"number"` // Repository-scoped issue number
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"` // "open", "closed"
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	Labels    []Label    `json:"labels"`
	Assignee  *User      `json:"assignee,omitempty"`
	Assignees []User     `json:"assignees,omitempty"`
	User      *User      `json:"user,omitempty"` // Issue author
	Milestone *Milestone `json:"milestone,omitempty"`
	HTMLURL   string     `json:"html_url"`
	Locked    bool       `json:"locked"`

	// PullRequest is non-nil if this issue is actually a pull request.
	// Used to filter PRs out of issue listings.
	PullRequest *PullRequestRef `json:"pull_request,omitempty"`
}

// PullRequestRef is a minimal reference indicating an issue is a PR.
type PullRequestRef struct {
	URL string `json:"url,omitempty"`
}

// IsPullRequest returns true if this issue is actually a pull request.
func (i *Issue) IsPullRequest() bool {
	return i.PullRequest != nil
}

// LabelNames returns the names of all labels on this issue.
func (i *Issue) LabelNames() []string {
	names := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		names = append(names, l.Name)
	}
	return names
}

// User represents a GitHub user.
type User struct {
	ID        int    `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	HTMLURL   string `json:"html_url,omitempty"`
	Type      string `json:"type,omitempty"` // "User", "Organization", "Bot"
}

// Label represents a GitHub label.
type Label struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

// Milestone represents a GitHub milestone.
type Milestone struct {
	ID          int        `json:"id"`
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	State       string     `json:"state"` // "open", "closed"
	DueOn       *time.Time `json:"due_on,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	HTMLURL     string     `json:"html_url,omitempty"`
}

// Repository represents a GitHub repository.
type Repository struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"` // "owner/repo"
	Description   string `json:"description,omitempty"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Private       bool   `json:"private"`
	Owner         *User  `json:"owner,omitempty"`
}

// SyncStats tracks statistics for a GitHub sync operation.
type SyncStats struct {
	Pulled    int `json:"pulled"`
	Pushed    int `json:"pushed"`
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Skipped   int `json:"skipped"`
	Errors    int `json:"errors"`
	Conflicts int `json:"conflicts"`
}

// SyncResult represents the result of a GitHub sync operation.
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

// Conflict represents a conflict between local and GitHub versions.
// A conflict occurs when both the local and GitHub versions have been modified
// since the last sync.
type Conflict struct {
	IssueID           string    // Beads issue ID
	LocalUpdated      time.Time // When the local version was last modified
	GitHubUpdated     time.Time // When the GitHub version was last modified
	GitHubExternalRef string    // URL to the GitHub issue
	GitHubNumber      int       // GitHub issue number (repository-scoped)
	GitHubID          int       // GitHub's global issue ID
}

// IssueConversion holds the result of converting a GitHub issue to Beads.
// It includes the issue and any dependencies that should be created.
type IssueConversion struct {
	Issue        *types.Issue
	Dependencies []DependencyInfo
}

// DependencyInfo represents a dependency to be created after issue import.
// Stored separately since we need all issues imported before linking dependencies.
type DependencyInfo struct {
	FromGitHubNumber int    // GitHub number of the dependent issue
	ToGitHubNumber   int    // GitHub number of the dependency target
	Type             string // Beads dependency type (blocks, related, parent-child)
}

// Valid GitHub issue states.
var validStates = map[string]bool{
	"open":   true,
	"closed": true,
	"all":    true,
}

// isValidState checks if a GitHub state string is valid.
func isValidState(state string) bool {
	return validStates[state]
}

// parseLabelPrefix splits a label into prefix and value.
// Labels like "priority::high" are split into ("priority", "high").
// Labels without "::" return empty prefix and the original label as value.
func parseLabelPrefix(label string) (prefix, value string) {
	parts := strings.SplitN(label, "::", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", label
}

// PriorityMapping maps priority label values to beads priority (0-4).
// This is the single source of truth for priority mappings.
var PriorityMapping = map[string]int{
	"critical": 0, // P0
	"high":     1, // P1
	"medium":   2, // P2
	"low":      3, // P3
	"none":     4, // P4
}

// StatusMapping maps status label values to beads status strings.
// This is the single source of truth for status mappings.
var StatusMapping = map[string]string{
	"open":        "open",
	"in_progress": "in_progress",
	"blocked":     "blocked",
	"deferred":    "deferred",
	"closed":      "closed",
}

// typeMapping maps type label values to beads issue type strings.
// This is the single source of truth for type mappings.
var typeMapping = map[string]string{
	"bug":         "bug",
	"feature":     "feature",
	"task":        "task",
	"epic":        "epic",
	"chore":       "chore",
	"decision":    "decision",
	"spike":       "spike",
	"story":       "story",
	"milestone":   "milestone",
	"enhancement": "feature",
}

// getPriorityFromLabel returns the beads priority for a priority label value.
// Returns -1 if the value is not recognized.
func getPriorityFromLabel(value string) int {
	if p, ok := PriorityMapping[strings.ToLower(value)]; ok {
		return p
	}
	return -1
}

// getStatusFromLabel returns the beads status for a status label value.
// Returns empty string if the value is not recognized.
func getStatusFromLabel(value string) string {
	return StatusMapping[strings.ToLower(value)]
}

// getTypeFromLabel returns the beads issue type for a type label value.
// Returns empty string if the value is not recognized.
func getTypeFromLabel(value string) string {
	return typeMapping[strings.ToLower(value)]
}
