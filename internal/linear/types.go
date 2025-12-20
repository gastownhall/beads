// Package linear provides client and data types for the Linear GraphQL API.
//
// This package handles all interactions with Linear's issue tracking system,
// including fetching, creating, and updating issues. It provides bidirectional
// mapping between Linear's data model and Beads' internal types.
package linear

import (
	"net/http"
	"time"
)

// API configuration constants.
const (
	// DefaultAPIEndpoint is the Linear GraphQL API endpoint.
	DefaultAPIEndpoint = "https://api.linear.app/graphql"

	// DefaultTimeout is the default HTTP request timeout.
	DefaultTimeout = 30 * time.Second

	// MaxRetries is the maximum number of retries for rate-limited requests.
	MaxRetries = 3

	// RetryDelay is the base delay between retries (exponential backoff).
	RetryDelay = time.Second

	// MaxPageSize is the maximum number of issues to fetch per page.
	MaxPageSize = 100
)

// Client provides methods to interact with the Linear GraphQL API.
type Client struct {
	APIKey     string
	TeamID     string
	Endpoint   string // GraphQL endpoint URL (defaults to DefaultAPIEndpoint)
	HTTPClient *http.Client
}

// GraphQLRequest represents a GraphQL request payload.
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLResponse represents a generic GraphQL response.
type GraphQLResponse struct {
	Data   []byte         `json:"data"`
	Errors []GraphQLError `json:"errors,omitempty"`
}

// GraphQLError represents a GraphQL error.
type GraphQLError struct {
	Message    string   `json:"message"`
	Path       []string `json:"path,omitempty"`
	Extensions struct {
		Code string `json:"code,omitempty"`
	} `json:"extensions,omitempty"`
}

// Issue represents an issue from the Linear API.
type Issue struct {
	ID          string     `json:"id"`
	Identifier  string     `json:"identifier"` // e.g., "TEAM-123"
	Title       string     `json:"title"`
	Description string     `json:"description"`
	URL         string     `json:"url"`
	Priority    int        `json:"priority"` // 0=no priority, 1=urgent, 2=high, 3=medium, 4=low
	State       *State     `json:"state"`
	Assignee    *User      `json:"assignee"`
	Labels      *Labels    `json:"labels"`
	Parent      *Parent    `json:"parent,omitempty"`
	Relations   *Relations `json:"relations,omitempty"`
	CreatedAt   string     `json:"createdAt"`
	UpdatedAt   string     `json:"updatedAt"`
	CompletedAt string     `json:"completedAt,omitempty"`
}

// State represents a workflow state in Linear.
type State struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "backlog", "unstarted", "started", "completed", "canceled"
}

// User represents a user in Linear.
type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// Labels represents paginated labels on an issue.
type Labels struct {
	Nodes []Label `json:"nodes"`
}

// Label represents a label in Linear.
type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Parent represents a parent issue reference.
type Parent struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
}

// Relation represents a relation between issues in Linear.
type Relation struct {
	ID           string `json:"id"`
	Type         string `json:"type"` // "blocks", "blockedBy", "duplicate", "related"
	RelatedIssue struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
	} `json:"relatedIssue"`
}

// Relations wraps the nodes array for relations.
type Relations struct {
	Nodes []Relation `json:"nodes"`
}

// TeamStates represents workflow states for a team.
type TeamStates struct {
	ID     string         `json:"id"`
	States *StatesWrapper `json:"states"`
}

// StatesWrapper wraps the nodes array for states.
type StatesWrapper struct {
	Nodes []State `json:"nodes"`
}

// IssuesResponse represents the response from issues query.
type IssuesResponse struct {
	Issues struct {
		Nodes    []Issue `json:"nodes"`
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"issues"`
}

// IssueCreateResponse represents the response from issueCreate mutation.
type IssueCreateResponse struct {
	IssueCreate struct {
		Success bool  `json:"success"`
		Issue   Issue `json:"issue"`
	} `json:"issueCreate"`
}

// IssueUpdateResponse represents the response from issueUpdate mutation.
type IssueUpdateResponse struct {
	IssueUpdate struct {
		Success bool  `json:"success"`
		Issue   Issue `json:"issue"`
	} `json:"issueUpdate"`
}

// TeamResponse represents the response from team query.
type TeamResponse struct {
	Team TeamStates `json:"team"`
}

// SyncStats tracks statistics for a Linear sync operation.
type SyncStats struct {
	Pulled    int `json:"pulled"`
	Pushed    int `json:"pushed"`
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Skipped   int `json:"skipped"`
	Errors    int `json:"errors"`
	Conflicts int `json:"conflicts"`
}

// SyncResult represents the result of a Linear sync operation.
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
	Incremental bool   // Whether this was an incremental sync
	SyncedSince string // Timestamp we synced since (if incremental)
}

// PushStats tracks push operation statistics.
type PushStats struct {
	Created int
	Updated int
	Skipped int
	Errors  int
}

// Conflict represents a conflict between local and Linear versions.
// A conflict occurs when both the local and Linear versions have been modified
// since the last sync.
type Conflict struct {
	IssueID           string    // Beads issue ID
	LocalUpdated      time.Time // When the local version was last modified
	LinearUpdated     time.Time // When the Linear version was last modified
	LinearExternalRef string    // URL to the Linear issue
	LinearIdentifier  string    // Linear issue identifier (e.g., "TEAM-123")
	LinearInternalID  string    // Linear's internal UUID (for API updates)
}

// IssueConversion holds the result of converting a Linear issue to Beads.
// It includes the issue and any dependencies that should be created.
type IssueConversion struct {
	Issue        interface{} // *types.Issue - avoiding circular import
	Dependencies []DependencyInfo
}

// DependencyInfo represents a dependency to be created after issue import.
// Stored separately since we need all issues imported before linking dependencies.
type DependencyInfo struct {
	FromLinearID string // Linear identifier of the dependent issue (e.g., "TEAM-123")
	ToLinearID   string // Linear identifier of the dependency target
	Type         string // Beads dependency type (blocks, related, duplicates, parent-child)
}

// StateCache caches workflow states for the team to avoid repeated API calls.
type StateCache struct {
	States      []State
	StatesByID  map[string]State
	OpenStateID string // First "unstarted" or "backlog" state
}

// Team represents a team in Linear.
type Team struct {
	ID   string `json:"id"`   // UUID
	Name string `json:"name"` // Display name
	Key  string `json:"key"`  // Short key used in issue identifiers (e.g., "ENG")
}

// TeamsResponse represents the response from teams query.
type TeamsResponse struct {
	Teams struct {
		Nodes []Team `json:"nodes"`
	} `json:"teams"`
}

