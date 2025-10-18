package rpc

import (
	"encoding/json"

	"github.com/steveyegge/beads/internal/types"
)

// Operation constants for all bd commands
const (
	OpPing           = "ping"
	OpCreate         = "create"
	OpUpdate         = "update"
	OpClose          = "close"
	OpList           = "list"
	OpShow           = "show"
	OpReady          = "ready"
	OpStats          = "stats"
	OpDepAdd         = "dep_add"
	OpDepRemove      = "dep_remove"
	OpDepTree        = "dep_tree"
	OpLabelAdd       = "label_add"
	OpLabelRemove    = "label_remove"
	OpBatch          = "batch"
	OpReposList      = "repos_list"
	OpReposReady     = "repos_ready"
	OpReposStats     = "repos_stats"
	OpReposClearCache = "repos_clear_cache"
)

// Request represents an RPC request from client to daemon
type Request struct {
	Operation string          `json:"operation"`
	Args      json.RawMessage `json:"args"`
	Actor     string          `json:"actor,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Cwd       string          `json:"cwd,omitempty"` // Working directory for database discovery
}

// Response represents an RPC response from daemon to client
type Response struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// CreateArgs represents arguments for the create operation
type CreateArgs struct {
	ID                 string   `json:"id,omitempty"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	IssueType          string   `json:"issue_type"`
	Priority           int      `json:"priority"`
	Design             string   `json:"design,omitempty"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	Assignee           string   `json:"assignee,omitempty"`
	Labels             []string `json:"labels,omitempty"`
	Dependencies       []string `json:"dependencies,omitempty"`
}

// UpdateArgs represents arguments for the update operation
type UpdateArgs struct {
	ID                 string  `json:"id"`
	Title              *string `json:"title,omitempty"`
	Status             *string `json:"status,omitempty"`
	Priority           *int    `json:"priority,omitempty"`
	Design             *string `json:"design,omitempty"`
	AcceptanceCriteria *string `json:"acceptance_criteria,omitempty"`
	Notes              *string `json:"notes,omitempty"`
	Assignee           *string `json:"assignee,omitempty"`
}

// CloseArgs represents arguments for the close operation
type CloseArgs struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

// ListArgs represents arguments for the list operation
type ListArgs struct {
	Query     string `json:"query,omitempty"`
	Status    string `json:"status,omitempty"`
	Priority  *int   `json:"priority,omitempty"`
	IssueType string `json:"issue_type,omitempty"`
	Assignee  string `json:"assignee,omitempty"`
	Label     string `json:"label,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// ShowArgs represents arguments for the show operation
type ShowArgs struct {
	ID string `json:"id"`
}

// ReadyArgs represents arguments for the ready operation
type ReadyArgs struct {
	Assignee string `json:"assignee,omitempty"`
	Priority *int   `json:"priority,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// DepAddArgs represents arguments for adding a dependency
type DepAddArgs struct {
	FromID  string `json:"from_id"`
	ToID    string `json:"to_id"`
	DepType string `json:"dep_type"`
}

// DepRemoveArgs represents arguments for removing a dependency
type DepRemoveArgs struct {
	FromID  string `json:"from_id"`
	ToID    string `json:"to_id"`
	DepType string `json:"dep_type,omitempty"`
}

// DepTreeArgs represents arguments for the dep tree operation
type DepTreeArgs struct {
	ID       string `json:"id"`
	MaxDepth int    `json:"max_depth,omitempty"`
}

// LabelAddArgs represents arguments for adding a label
type LabelAddArgs struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// LabelRemoveArgs represents arguments for removing a label
type LabelRemoveArgs struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// PingResponse is the response for a ping operation
type PingResponse struct {
	Message string `json:"message"`
	Version string `json:"version"`
}

// BatchArgs represents arguments for batch operations
type BatchArgs struct {
	Operations []BatchOperation `json:"operations"`
}

// BatchOperation represents a single operation in a batch
type BatchOperation struct {
	Operation string          `json:"operation"`
	Args      json.RawMessage `json:"args"`
}

// BatchResponse contains the results of a batch operation
type BatchResponse struct {
	Results []BatchResult `json:"results"`
}

// BatchResult represents the result of a single operation in a batch
type BatchResult struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// ReposReadyArgs represents arguments for repos ready operation
type ReposReadyArgs struct {
	Assignee    string `json:"assignee,omitempty"`
	Priority    *int   `json:"priority,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	GroupByRepo bool   `json:"group_by_repo,omitempty"`
}

// RepoInfo represents information about a cached repository
type RepoInfo struct {
	Path       string `json:"path"`
	Prefix     string `json:"prefix"`
	IssueCount int    `json:"issue_count"`
	LastAccess string `json:"last_access"`
}

// RepoReadyWork represents ready work for a single repository
type RepoReadyWork struct {
	RepoPath string `json:"repo_path"`
	Issues   []*types.Issue `json:"issues"`
}

// ReposReadyIssue represents an issue with repo context
type ReposReadyIssue struct {
	RepoPath string       `json:"repo_path"`
	Issue    *types.Issue `json:"issue"`
}

// ReposStatsResponse contains combined statistics across repos
type ReposStatsResponse struct {
	Total   types.Statistics            `json:"total"`
	PerRepo map[string]types.Statistics `json:"per_repo"`
	Errors  map[string]string           `json:"errors,omitempty"`
}
