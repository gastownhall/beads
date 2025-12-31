package rpc

import (
	"encoding/json"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Operation constants for all bd commands
const (
	OpPing            = "ping"
	OpStatus          = "status"
	OpHealth          = "health"
	OpMetrics         = "metrics"
	OpCreate          = "create"
	OpUpdate          = "update"
	OpClose           = "close"
	OpList            = "list"
	OpCount           = "count"
	OpShow            = "show"
	OpReady           = "ready"
	OpBlocked         = "blocked"
	OpStale           = "stale"
	OpStats           = "stats"
	OpDepAdd          = "dep_add"
	OpDepRemove       = "dep_remove"
	OpDepTree         = "dep_tree"
	OpLabelAdd        = "label_add"
	OpLabelRemove     = "label_remove"
	OpCommentList     = "comment_list"
	OpCommentAdd      = "comment_add"
	OpBatch           = "batch"
	OpResolveID       = "resolve_id"

	OpCompact         = "compact"
	OpCompactStats    = "compact_stats"
	OpExport          = "export"
	OpImport          = "import"
	OpEpicStatus      = "epic_status"
	OpGetMutations        = "get_mutations"
	OpGetMoleculeProgress = "get_molecule_progress"
	OpShutdown            = "shutdown"
	OpDelete              = "delete"
	OpGetWorkerStatus     = "get_worker_status"
	OpGetConfig           = "get_config"
	OpMolStale            = "mol_stale"

	// Gate operations
	OpGateCreate = "gate_create"
	OpGateList   = "gate_list"
	OpGateShow   = "gate_show"
	OpGateClose  = "gate_close"
	OpGateWait   = "gate_wait"
)

// Request represents an RPC request from client to daemon
type Request struct {
	Operation     string          `json:"operation"`
	Args          json.RawMessage `json:"args"`
	Actor         string          `json:"actor,omitempty"`
	RequestID     string          `json:"request_id,omitempty"`
	Cwd           string          `json:"cwd,omitempty"`            // Working directory for database discovery
	ClientVersion string          `json:"client_version,omitempty"` // Client version for compatibility checks
	ExpectedDB    string          `json:"expected_db,omitempty"`    // Expected database path for validation (absolute)
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
	Parent             string   `json:"parent,omitempty"` // Parent ID for hierarchical issues
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	IssueType          string   `json:"issue_type"`
	Priority           int      `json:"priority"`
	Design             string   `json:"design,omitempty"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	Assignee           string   `json:"assignee,omitempty"`
	ExternalRef        string   `json:"external_ref,omitempty"`  // Link to external issue trackers
	EstimatedMinutes   *int     `json:"estimated_minutes,omitempty"` // Time estimate in minutes
	Labels             []string `json:"labels,omitempty"`
	Dependencies       []string `json:"dependencies,omitempty"`
	// Waits-for dependencies
	WaitsFor     string `json:"waits_for,omitempty"`      // Spawner issue ID to wait for
	WaitsForGate string `json:"waits_for_gate,omitempty"` // Gate type: all-children or any-children
	// Messaging fields
	Sender    string `json:"sender,omitempty"`    // Who sent this (for messages)
	Ephemeral bool   `json:"ephemeral,omitempty"` // If true, not exported to JSONL; bulk-deleted when closed
	RepliesTo string `json:"replies_to,omitempty"` // Issue ID for conversation threading
	// ID generation
	IDPrefix  string `json:"id_prefix,omitempty"`  // Override prefix for ID generation (mol, eph, etc.)
	CreatedBy string `json:"created_by,omitempty"` // Who created the issue
	// Molecule type (for swarm coordination)
	MolType string `json:"mol_type,omitempty"` // swarm, patrol, or work (default)
	// Agent identity fields (only valid when IssueType == "agent")
	RoleType string `json:"role_type,omitempty"` // polecat|crew|witness|refinery|mayor|deacon
	Rig      string `json:"rig,omitempty"`       // Rig name (empty for town-level agents)
	// Event fields (only valid when IssueType == "event")
	EventCategory string `json:"event_category,omitempty"` // Namespaced category (e.g., patrol.muted, agent.started)
	EventActor    string `json:"event_actor,omitempty"`    // Entity URI who caused this event
	EventTarget   string `json:"event_target,omitempty"`   // Entity URI or bead ID affected
	EventPayload  string `json:"event_payload,omitempty"`  // Event-specific JSON data
}

// UpdateArgs represents arguments for the update operation
type UpdateArgs struct {
	ID                 string   `json:"id"`
	Title              *string  `json:"title,omitempty"`
	Description        *string  `json:"description,omitempty"`
	Status             *string  `json:"status,omitempty"`
	Priority           *int     `json:"priority,omitempty"`
	Design             *string  `json:"design,omitempty"`
	AcceptanceCriteria *string  `json:"acceptance_criteria,omitempty"`
	Notes              *string  `json:"notes,omitempty"`
	Assignee           *string  `json:"assignee,omitempty"`
	ExternalRef        *string  `json:"external_ref,omitempty"` // Link to external issue trackers
	EstimatedMinutes   *int     `json:"estimated_minutes,omitempty"` // Time estimate in minutes
	IssueType          *string  `json:"issue_type,omitempty"`        // Issue type (bug|feature|task|epic|chore)
	AddLabels          []string `json:"add_labels,omitempty"`
	RemoveLabels       []string `json:"remove_labels,omitempty"`
	SetLabels          []string `json:"set_labels,omitempty"`
	// Messaging fields
	Sender    *string `json:"sender,omitempty"`    // Who sent this (for messages)
	Ephemeral *bool   `json:"ephemeral,omitempty"` // If true, not exported to JSONL; bulk-deleted when closed
	RepliesTo *string `json:"replies_to,omitempty"` // Issue ID for conversation threading
	// Graph link fields
	RelatesTo    *string `json:"relates_to,omitempty"`    // JSON array of related issue IDs
	DuplicateOf  *string `json:"duplicate_of,omitempty"`  // Canonical issue ID if duplicate
	SupersededBy *string `json:"superseded_by,omitempty"` // Replacement issue ID if obsolete
	// Pinned field
	Pinned *bool `json:"pinned,omitempty"` // If true, issue is a persistent context marker
	// Reparenting field
	Parent *string `json:"parent,omitempty"` // New parent issue ID (reparents the issue)
	// Agent slot fields
	HookBead *string `json:"hook_bead,omitempty"` // Current work on agent's hook (0..1)
	RoleBead *string `json:"role_bead,omitempty"` // Role definition bead for agent
	// Agent state fields
	AgentState   *string `json:"agent_state,omitempty"`   // Agent state (idle|running|stuck|stopped|dead)
	LastActivity *bool   `json:"last_activity,omitempty"` // If true, update last_activity to now
	// Agent identity fields
	RoleType *string `json:"role_type,omitempty"` // polecat|crew|witness|refinery|mayor|deacon
	Rig      *string `json:"rig,omitempty"`       // Rig name (empty for town-level agents)
	// Event fields (only valid when IssueType == "event")
	EventCategory *string `json:"event_category,omitempty"` // Namespaced category (e.g., patrol.muted, agent.started)
	EventActor    *string `json:"event_actor,omitempty"`    // Entity URI who caused this event
	EventTarget   *string `json:"event_target,omitempty"`   // Entity URI or bead ID affected
	EventPayload  *string `json:"event_payload,omitempty"`  // Event-specific JSON data
}

// CloseArgs represents arguments for the close operation
type CloseArgs struct {
	ID          string `json:"id"`
	Reason      string `json:"reason,omitempty"`
	SuggestNext bool   `json:"suggest_next,omitempty"` // Return newly unblocked issues (GH#679)
}

// CloseResult is returned when SuggestNext is true (GH#679)
// When SuggestNext is false, just the closed issue is returned for backward compatibility
type CloseResult struct {
	Closed    *types.Issue   `json:"closed"`              // The issue that was closed
	Unblocked []*types.Issue `json:"unblocked,omitempty"` // Issues newly unblocked by closing
}

// DeleteArgs represents arguments for the delete operation
type DeleteArgs struct {
	IDs     []string `json:"ids"`               // Issue IDs to delete
	Force   bool     `json:"force,omitempty"`   // Force deletion without confirmation
	DryRun  bool     `json:"dry_run,omitempty"` // Preview mode
	Cascade bool     `json:"cascade,omitempty"` // Recursively delete dependents
	Reason  string   `json:"reason,omitempty"`  // Reason for deletion
}

// ListArgs represents arguments for the list operation
type ListArgs struct {
	Query     string   `json:"query,omitempty"`
	Status    string   `json:"status,omitempty"`
	Priority  *int     `json:"priority,omitempty"`
	IssueType string   `json:"issue_type,omitempty"`
	Assignee  string   `json:"assignee,omitempty"`
	Label     string   `json:"label,omitempty"`      // Deprecated: use Labels
	Labels    []string `json:"labels,omitempty"`     // AND semantics
	LabelsAny []string `json:"labels_any,omitempty"` // OR semantics
	IDs       []string `json:"ids,omitempty"`        // Filter by specific issue IDs
	Limit     int      `json:"limit,omitempty"`
	
	// Pattern matching
	TitleContains       string `json:"title_contains,omitempty"`
	DescriptionContains string `json:"description_contains,omitempty"`
	NotesContains       string `json:"notes_contains,omitempty"`
	
	// Date ranges (ISO 8601 format)
	CreatedAfter  string `json:"created_after,omitempty"`
	CreatedBefore string `json:"created_before,omitempty"`
	UpdatedAfter  string `json:"updated_after,omitempty"`
	UpdatedBefore string `json:"updated_before,omitempty"`
	ClosedAfter   string `json:"closed_after,omitempty"`
	ClosedBefore  string `json:"closed_before,omitempty"`
	
	// Empty/null checks
	EmptyDescription bool `json:"empty_description,omitempty"`
	NoAssignee       bool `json:"no_assignee,omitempty"`
	NoLabels         bool `json:"no_labels,omitempty"`
	
	// Priority range
	PriorityMin *int `json:"priority_min,omitempty"`
	PriorityMax *int `json:"priority_max,omitempty"`

	// Pinned filtering
	Pinned *bool `json:"pinned,omitempty"`

	// Template filtering
	IncludeTemplates bool `json:"include_templates,omitempty"`

	// Parent filtering
	ParentID string `json:"parent_id,omitempty"`

	// Ephemeral filtering
	Ephemeral *bool `json:"ephemeral,omitempty"`

	// Molecule type filtering
	MolType string `json:"mol_type,omitempty"`

	// Status exclusion (for default non-closed behavior, GH#788)
	ExcludeStatus []string `json:"exclude_status,omitempty"`
}

// CountArgs represents arguments for the count operation
type CountArgs struct {
	// Supports all the same filters as ListArgs
	Query     string   `json:"query,omitempty"`
	Status    string   `json:"status,omitempty"`
	Priority  *int     `json:"priority,omitempty"`
	IssueType string   `json:"issue_type,omitempty"`
	Assignee  string   `json:"assignee,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	LabelsAny []string `json:"labels_any,omitempty"`
	IDs       []string `json:"ids,omitempty"`

	// Pattern matching
	TitleContains       string `json:"title_contains,omitempty"`
	DescriptionContains string `json:"description_contains,omitempty"`
	NotesContains       string `json:"notes_contains,omitempty"`

	// Date ranges
	CreatedAfter  string `json:"created_after,omitempty"`
	CreatedBefore string `json:"created_before,omitempty"`
	UpdatedAfter  string `json:"updated_after,omitempty"`
	UpdatedBefore string `json:"updated_before,omitempty"`
	ClosedAfter   string `json:"closed_after,omitempty"`
	ClosedBefore  string `json:"closed_before,omitempty"`

	// Empty/null checks
	EmptyDescription bool `json:"empty_description,omitempty"`
	NoAssignee       bool `json:"no_assignee,omitempty"`
	NoLabels         bool `json:"no_labels,omitempty"`

	// Priority range
	PriorityMin *int `json:"priority_min,omitempty"`
	PriorityMax *int `json:"priority_max,omitempty"`

	// Grouping option (only one can be specified)
	GroupBy string `json:"group_by,omitempty"` // "status", "priority", "type", "assignee", "label"
}

// ShowArgs represents arguments for the show operation
type ShowArgs struct {
	ID string `json:"id"`
}

// ResolveIDArgs represents arguments for the resolve_id operation
type ResolveIDArgs struct {
	ID string `json:"id"`
}

// ReadyArgs represents arguments for the ready operation
type ReadyArgs struct {
	Assignee   string   `json:"assignee,omitempty"`
	Unassigned bool     `json:"unassigned,omitempty"`
	Priority   *int     `json:"priority,omitempty"`
	Type       string   `json:"type,omitempty"`
	Limit      int      `json:"limit,omitempty"`
	SortPolicy string   `json:"sort_policy,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	LabelsAny  []string `json:"labels_any,omitempty"`
	ParentID   string   `json:"parent_id,omitempty"` // Filter to descendants of this bead/epic
	MolType    string   `json:"mol_type,omitempty"`  // Filter by molecule type: swarm, patrol, or work
}

// BlockedArgs represents arguments for the blocked operation
type BlockedArgs struct {
	ParentID string `json:"parent_id,omitempty"` // Filter to descendants of this bead/epic
}

// StaleArgs represents arguments for the stale command
type StaleArgs struct {
	Days   int    `json:"days,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
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

// CommentListArgs represents arguments for listing comments on an issue
type CommentListArgs struct {
	ID string `json:"id"`
}

// CommentAddArgs represents arguments for adding a comment to an issue
type CommentAddArgs struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Text   string `json:"text"`
}

// EpicStatusArgs represents arguments for the epic status operation
type EpicStatusArgs struct {
	EligibleOnly bool `json:"eligible_only,omitempty"`
}

// PingResponse is the response for a ping operation
type PingResponse struct {
	Message string `json:"message"`
	Version string `json:"version"`
}

// StatusResponse represents the daemon status metadata
type StatusResponse struct {
	Version              string  `json:"version"`                  // Server/daemon version
	WorkspacePath        string  `json:"workspace_path"`           // Absolute path to workspace root
	DatabasePath         string  `json:"database_path"`            // Absolute path to database file
	SocketPath           string  `json:"socket_path"`              // Path to Unix socket
	PID                  int     `json:"pid"`                      // Process ID
	UptimeSeconds        float64 `json:"uptime_seconds"`           // Time since daemon started
	LastActivityTime     string  `json:"last_activity_time"`       // ISO 8601 timestamp of last request
	ExclusiveLockActive  bool    `json:"exclusive_lock_active"`    // Whether an exclusive lock is held
	ExclusiveLockHolder  string  `json:"exclusive_lock_holder,omitempty"` // Lock holder name if active
	// Daemon configuration
	AutoCommit   bool   `json:"auto_commit"`            // Whether auto-commit is enabled
	AutoPush     bool   `json:"auto_push"`              // Whether auto-push is enabled
	AutoPull     bool   `json:"auto_pull"`              // Whether auto-pull is enabled (periodic remote sync)
	LocalMode    bool   `json:"local_mode"`             // Whether running in local-only mode (no git)
	SyncInterval string `json:"sync_interval"`          // Sync interval (e.g., "5s")
	DaemonMode   string `json:"daemon_mode"`            // Sync mode: "poll" or "events"
}

// HealthResponse is the response for a health check operation
type HealthResponse struct {
	Status         string  `json:"status"`                   // "healthy", "degraded", "unhealthy"
	Version        string  `json:"version"`                  // Server/daemon version
	ClientVersion  string  `json:"client_version,omitempty"` // Client version from request
	Compatible     bool    `json:"compatible"`               // Whether versions are compatible
	Uptime         float64 `json:"uptime_seconds"`
	DBResponseTime float64 `json:"db_response_ms"`
	ActiveConns    int32   `json:"active_connections"`
	MaxConns       int     `json:"max_connections"`
	MemoryAllocMB  uint64  `json:"memory_alloc_mb"`
	Error          string  `json:"error,omitempty"`
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

// CompactArgs represents arguments for the compact operation
type CompactArgs struct {
	IssueID   string `json:"issue_id,omitempty"`   // Empty for --all
	Tier      int    `json:"tier"`                 // 1 or 2
	DryRun    bool   `json:"dry_run"`
	Force     bool   `json:"force"`
	All       bool   `json:"all"`
	APIKey    string `json:"api_key,omitempty"`
	Workers   int    `json:"workers,omitempty"`
	BatchSize int    `json:"batch_size,omitempty"`
}

// CompactStatsArgs represents arguments for compact stats operation
type CompactStatsArgs struct {
	Tier int `json:"tier,omitempty"`
}

// CompactResponse represents the response from a compact operation
type CompactResponse struct {
	Success      bool              `json:"success"`
	IssueID      string            `json:"issue_id,omitempty"`
	Results      []CompactResult   `json:"results,omitempty"`     // For batch operations
	Stats        *CompactStatsData `json:"stats,omitempty"`       // For stats operation
	OriginalSize int               `json:"original_size,omitempty"`
	CompactedSize int              `json:"compacted_size,omitempty"`
	Reduction    string            `json:"reduction,omitempty"`
	Duration     string            `json:"duration,omitempty"`
	DryRun       bool              `json:"dry_run,omitempty"`
}

// CompactResult represents the result of compacting a single issue
type CompactResult struct {
	IssueID       string `json:"issue_id"`
	Success       bool   `json:"success"`
	Error         string `json:"error,omitempty"`
	OriginalSize  int    `json:"original_size,omitempty"`
	CompactedSize int    `json:"compacted_size,omitempty"`
	Reduction     string `json:"reduction,omitempty"`
}

// CompactStatsData represents compaction statistics
type CompactStatsData struct {
	Tier1Candidates int     `json:"tier1_candidates"`
	Tier2Candidates int     `json:"tier2_candidates"`
	TotalClosed     int     `json:"total_closed"`
	Tier1MinAge     string  `json:"tier1_min_age"`
	Tier2MinAge     string  `json:"tier2_min_age"`
	EstimatedSavings string `json:"estimated_savings,omitempty"`
}

// ExportArgs represents arguments for the export operation
type ExportArgs struct {
	JSONLPath string `json:"jsonl_path"` // Path to export JSONL file
}

// ImportArgs represents arguments for the import operation
type ImportArgs struct {
	JSONLPath string `json:"jsonl_path"` // Path to import JSONL file
}

// GetMutationsArgs represents arguments for retrieving recent mutations
type GetMutationsArgs struct {
	Since int64 `json:"since"` // Unix timestamp in milliseconds (0 for all recent)
}

// Gate operations

// GateCreateArgs represents arguments for creating a gate
type GateCreateArgs struct {
	Title     string        `json:"title"`
	AwaitType string        `json:"await_type"` // gh:run, gh:pr, timer, human, mail
	AwaitID   string        `json:"await_id"`   // ID/value for the await type
	Timeout   time.Duration `json:"timeout"`    // Timeout duration
	Waiters   []string      `json:"waiters"`    // Mail addresses to notify when gate clears
}

// GateCreateResult represents the result of creating a gate
type GateCreateResult struct {
	ID string `json:"id"` // Created gate ID
}

// GateListArgs represents arguments for listing gates
type GateListArgs struct {
	All bool `json:"all"` // Include closed gates
}

// GateShowArgs represents arguments for showing a gate
type GateShowArgs struct {
	ID string `json:"id"` // Gate ID (partial or full)
}

// GateCloseArgs represents arguments for closing a gate
type GateCloseArgs struct {
	ID     string `json:"id"`               // Gate ID (partial or full)
	Reason string `json:"reason,omitempty"` // Close reason
}

// GateWaitArgs represents arguments for adding waiters to a gate
type GateWaitArgs struct {
	ID      string   `json:"id"`      // Gate ID (partial or full)
	Waiters []string `json:"waiters"` // Additional waiters to add
}

// GateWaitResult represents the result of adding waiters
type GateWaitResult struct {
	AddedCount int `json:"added_count"` // Number of new waiters added
}

// GetWorkerStatusArgs represents arguments for retrieving worker status
type GetWorkerStatusArgs struct {
	// Assignee filters to a specific worker (optional, empty = all workers)
	Assignee string `json:"assignee,omitempty"`
}

// WorkerStatus represents the status of a single worker and their current work
type WorkerStatus struct {
	Assignee      string `json:"assignee"`                 // Worker identifier
	MoleculeID    string `json:"molecule_id,omitempty"`    // Parent molecule/epic ID (if working on a step)
	MoleculeTitle string `json:"molecule_title,omitempty"` // Parent molecule/epic title
	CurrentStep   int    `json:"current_step,omitempty"`   // Current step number (1-indexed)
	TotalSteps    int    `json:"total_steps,omitempty"`    // Total number of steps in molecule
	StepID        string `json:"step_id,omitempty"`        // Current step issue ID
	StepTitle     string `json:"step_title,omitempty"`     // Current step issue title
	LastActivity  string `json:"last_activity"`            // ISO 8601 timestamp of last update
	Status        string `json:"status"`                   // Current work status (in_progress, blocked, etc.)
}

// GetWorkerStatusResponse is the response for get_worker_status operation
type GetWorkerStatusResponse struct {
	Workers []WorkerStatus `json:"workers"`
}

// GetMoleculeProgressArgs represents arguments for the get_molecule_progress operation
type GetMoleculeProgressArgs struct {
	MoleculeID string `json:"molecule_id"` // The ID of the molecule (parent issue)
}

// MoleculeStep represents a single step within a molecule
type MoleculeStep struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`     // "done", "current", "ready", "blocked"
	StartTime *string `json:"start_time"` // ISO 8601 timestamp when step was created
	CloseTime *string `json:"close_time"` // ISO 8601 timestamp when step was closed (if done)
}

// MoleculeProgress represents the progress of a molecule (parent issue with steps)
type MoleculeProgress struct {
	MoleculeID string         `json:"molecule_id"`
	Title      string         `json:"title"`
	Assignee   string         `json:"assignee"`
	Steps      []MoleculeStep `json:"steps"`
}

// GetConfigArgs represents arguments for getting daemon config
type GetConfigArgs struct {
	Key string `json:"key"` // Config key to retrieve (e.g., "issue_prefix")
}

// GetConfigResponse represents the response from get_config operation
type GetConfigResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// MolStaleArgs represents arguments for the mol stale operation
type MolStaleArgs struct {
	BlockingOnly   bool `json:"blocking_only"`   // Only show molecules blocking other work
	UnassignedOnly bool `json:"unassigned_only"` // Only show unassigned molecules
	ShowAll        bool `json:"show_all"`        // Include molecules with 0 children
}

// StaleMolecule holds info about a stale molecule (for RPC response)
type StaleMolecule struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	TotalChildren  int      `json:"total_children"`
	ClosedChildren int      `json:"closed_children"`
	Assignee       string   `json:"assignee,omitempty"`
	BlockingIssues []string `json:"blocking_issues,omitempty"`
	BlockingCount  int      `json:"blocking_count"`
}

// MolStaleResponse holds the result of the mol stale operation
type MolStaleResponse struct {
	StaleMolecules []*StaleMolecule `json:"stale_molecules"`
	TotalCount     int              `json:"total_count"`
	BlockingCount  int              `json:"blocking_count"`
}
