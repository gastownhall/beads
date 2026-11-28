// Package gitea provides client and data types for the Gitea and Forgejo REST v1 API.
package gitea

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// API configuration constants.
const (
	// DefaultAPIEndpoint is the REST v1 API prefix shared by Gitea and Forgejo.
	DefaultAPIEndpoint = "/api/v1"

	// DefaultTimeout is the default HTTP request timeout.
	DefaultTimeout = 30 * time.Second

	// MaxRetries is the maximum number of retries for rate-limited or transient requests.
	MaxRetries = 3

	// RetryDelay is the base delay between retries (exponential backoff).
	RetryDelay = time.Second

	// MaxPageSize is the maximum number of items requested per page (Gitea/Forgejo cap is 50).
	MaxPageSize = 50

	// MaxPages is the maximum number of pages to fetch before stopping.
	// This prevents infinite loops from malformed pagination headers.
	MaxPages = 1000

	// maxResponseSize caps response body reads at 50MB to avoid OOM from malformed responses.
	maxResponseSize = 50 * 1024 * 1024
)

// IssueFilter specifies server-side filters for fetching Gitea/Forgejo issues.
type IssueFilter struct {
	Labels    string // Comma-separated label names
	Milestone string // Milestone name or comma-separated milestone names
	Assignee  string // Username of assignee
}

// Client provides methods to interact with the Gitea / Forgejo REST v1 API.
type Client struct {
	Token      string       // Personal access token
	BaseURL    string       // Normalized web root (no trailing "/" or "/api/v1")
	Owner      string       // Repository owner (user or organization)
	Repo       string       // Repository name
	HTTPClient *http.Client // HTTP client used for API requests
	Retry      RetryConfig  // Retry/backoff policy for rate limits and transient errors

	labelMu    sync.Mutex
	labelCache map[string]int64 // lowercase label name -> numeric label ID
}

// Issue represents an issue from the Gitea / Forgejo REST API.
type Issue struct {
	ID          int64            `json:"id"`
	Number      int              `json:"number"`
	Title       string           `json:"title"`
	Body        string           `json:"body"`
	State       string           `json:"state"` // "open", "closed"
	CreatedAt   *time.Time       `json:"created_at"`
	UpdatedAt   *time.Time       `json:"updated_at"`
	ClosedAt    *time.Time       `json:"closed_at,omitempty"`
	Labels      []Label          `json:"labels"`
	Assignee    *User            `json:"assignee,omitempty"`
	Assignees   []User           `json:"assignees,omitempty"`
	User        *User            `json:"user,omitempty"`
	Milestone   *Milestone       `json:"milestone,omitempty"`
	HTMLURL     string           `json:"html_url"`
	URL         string           `json:"url,omitempty"`
	Repository  *RepositoryMeta  `json:"repository,omitempty"`
	PullRequest *PullRequestMeta `json:"pull_request,omitempty"`
	BlockedBy   []Issue          `json:"-"` // Hydrated via /dependencies on pull
}

// IsPullRequest returns true if this issue entry represents a pull request.
func (i *Issue) IsPullRequest() bool {
	return i != nil && i.PullRequest != nil
}

// LabelNames returns the names of all labels on this issue.
func (i *Issue) LabelNames() []string {
	if i == nil {
		return nil
	}
	names := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		if l.Name != "" {
			names = append(names, l.Name)
		}
	}
	return names
}

// PullRequestMeta indicates an issue is a pull request in Gitea/Forgejo responses.
type PullRequestMeta struct {
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
}

// RepositoryMeta holds lightweight repository metadata embedded in issue payloads.
type RepositoryMeta struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Owner    string `json:"owner"`
	FullName string `json:"full_name"`
}

// Label represents a Gitea / Forgejo label.
type Label struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

// User represents a Gitea / Forgejo user.
type User struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	Username string `json:"username,omitempty"`
	FullName string `json:"full_name,omitempty"`
	Email    string `json:"email,omitempty"`
}

// Handle returns the user's handle, preferring Username (Forgejo/Gitea canonical)
// and falling back to Login.
func (u *User) Handle() string {
	if u == nil {
		return ""
	}
	if u.Username != "" {
		return u.Username
	}
	return u.Login
}

// Milestone represents a Gitea / Forgejo milestone.
type Milestone struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
}

// Repository represents a Gitea / Forgejo repository.
type Repository struct {
	ID              int64            `json:"id"`
	Name            string           `json:"name"`
	FullName        string           `json:"full_name"`
	Description     string           `json:"description,omitempty"`
	HTMLURL         string           `json:"html_url"`
	DefaultBranch   string           `json:"default_branch,omitempty"`
	Private         bool             `json:"private"`
	HasIssues       bool             `json:"has_issues"`
	InternalTracker *InternalTracker `json:"internal_tracker,omitempty"`
	Owner           *User            `json:"owner,omitempty"`
}

// InternalTracker represents repository issue tracker capabilities.
type InternalTracker struct {
	EnableTimeTracker                bool `json:"enable_time_tracker"`
	AllowOnlyContributorsToTrackTime bool `json:"allow_only_contributors_to_track_time"`
	EnableIssueDependencies          bool `json:"enable_issue_dependencies"`
}

// IssueConversion holds the result of converting a Gitea/Forgejo issue to Beads.
type IssueConversion struct {
	Issue        *types.Issue
	Dependencies []DependencyInfo
}

// DependencyInfo represents a dependency to be created after issue import.
type DependencyInfo struct {
	FromNumber int
	ToNumber   int
	FromRepoID int64
	ToRepoID   int64
	FromRef    string
	ToRef      string
	Type       string
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
var PriorityMapping = map[string]int{
	"critical": 0, // P0
	"high":     1, // P1
	"medium":   2, // P2
	"low":      3, // P3
	"none":     4, // P4
}

// StatusMapping maps status label values to beads status strings.
var StatusMapping = map[string]string{
	"open":        "open",
	"in_progress": "in_progress",
	"blocked":     "blocked",
	"deferred":    "deferred",
	"closed":      "closed",
}

// typeMapping maps type label values to beads issue type strings.
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
