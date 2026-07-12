package plugin

import (
	"encoding/json"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

const ProtocolVersion = "beads.backend.v1alpha1"

type Issue = types.Issue
type BlockedIssue = types.BlockedIssue
type Comment = types.Comment
type CommitInfo = storage.CommitInfo
type CompactionCandidate = types.CompactionCandidate
type Conflict = storage.Conflict
type CustomStatus = types.CustomStatus
type Dependency = types.Dependency
type DependencyAddOptions = storage.DependencyAddOptions
type DependencyCounts = types.DependencyCounts
type DeleteIssuesResult = types.DeleteIssuesResult
type DiffEntry = storage.DiffEntry
type EpicStatus = types.EpicStatus
type Event = types.Event
type FederationPeer = storage.FederationPeer
type HistoryEntry = storage.HistoryEntry
type IssueFilter = types.IssueFilter
type IssueSnapshot = types.IssueSnapshot
type IssueWithCounts = types.IssueWithCounts
type IssueWithDependencyMetadata = types.IssueWithDependencyMetadata
type MergeSlotResult = storage.MergeSlotResult
type MergeSlotStatus = storage.MergeSlotStatus
type MoleculeLastActivity = types.MoleculeLastActivity
type MoleculeProgressStats = types.MoleculeProgressStats
type RemoteInfo = storage.RemoteInfo
type ReclaimedLease = types.ReclaimedLease
type StaleFilter = types.StaleFilter
type Statistics = types.Statistics
type SyncStatus = storage.SyncStatus
type TreeNode = types.TreeNode
type Transaction = storage.Transaction
type VCStatus = storage.Status
type WorkFilter = types.WorkFilter
type WispFilter = types.WispFilter

type Status = types.Status
type IssueType = types.IssueType

type OrphanHandling = storage.OrphanHandling

type BatchCreateOptions struct {
	OrphanHandling                 OrphanHandling `json:"orphan_handling,omitempty"`
	SkipPrefixValidation           bool           `json:"skip_prefix_validation,omitempty"`
	ConflictSkip                   bool           `json:"conflict_skip,omitempty"`
	RejectStaleUpserts             bool           `json:"reject_stale_upserts,omitempty"`
	SkipDependencyValidationErrors bool           `json:"skip_dependency_validation_errors,omitempty"`
}

func BatchCreateOptionsFromStorage(opts storage.BatchCreateOptions) BatchCreateOptions {
	return BatchCreateOptions{
		OrphanHandling:                 opts.OrphanHandling,
		SkipPrefixValidation:           opts.SkipPrefixValidation,
		ConflictSkip:                   opts.ConflictSkip,
		RejectStaleUpserts:             opts.RejectStaleUpserts,
		SkipDependencyValidationErrors: opts.SkipDependencyValidationErrors,
	}
}

func (opts BatchCreateOptions) Storage() storage.BatchCreateOptions {
	return storage.BatchCreateOptions{
		OrphanHandling:                 opts.OrphanHandling,
		SkipPrefixValidation:           opts.SkipPrefixValidation,
		ConflictSkip:                   opts.ConflictSkip,
		RejectStaleUpserts:             opts.RejectStaleUpserts,
		SkipDependencyValidationErrors: opts.SkipDependencyValidationErrors,
	}
}

const (
	StatusOpen       = types.StatusOpen
	StatusInProgress = types.StatusInProgress
	StatusClosed     = types.StatusClosed

	TypeTask    = types.TypeTask
	TypeBug     = types.TypeBug
	TypeFeature = types.TypeFeature
	TypeEpic    = types.TypeEpic
)

type Hello struct {
	Protocol     string       `json:"protocol"`
	Backend      string       `json:"backend"`
	Capabilities Capabilities `json:"capabilities"`
}

type Capabilities struct {
	Embedded          bool `json:"embedded"`
	Transactions      bool `json:"transactions"`
	RawSQL            bool `json:"raw_sql"`
	Leases            bool `json:"leases"`
	Maintenance       bool `json:"maintenance"`
	Versioning        bool `json:"versioning"`
	Branching         bool `json:"branching"`
	DoltRemotes       bool `json:"dolt_remotes"`
	ConcurrentWriters bool `json:"concurrent_writers"`
}

type Diagnostic struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Request struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	ID     string          `json:"id,omitempty"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type OpenParams struct {
	BeadsDir string `json:"beads_dir"`
	Database string `json:"database,omitempty"`
	Branch   string `json:"branch,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type OpenResult struct {
	SessionID string `json:"session_id"`
}

type SessionParams struct {
	SessionID string `json:"session_id"`
}

type InitParams struct {
	BeadsDir string `json:"beads_dir"`
	Database string `json:"database,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	Actor    string `json:"actor,omitempty"`
}

type ConfigParams struct {
	SessionID string `json:"session_id"`
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
}

type RawSQLParams struct {
	SessionID string `json:"session_id"`
	Query     string `json:"query"`
}

type RawSQLResult struct {
	Columns      []string                 `json:"columns,omitempty"`
	Rows         []map[string]interface{} `json:"rows,omitempty"`
	RowsAffected int64                    `json:"rows_affected,omitempty"`
	Read         bool                     `json:"read"`
}

type IssueTypeParams struct {
	SessionID string    `json:"session_id"`
	IssueType IssueType `json:"issue_type"`
}

type RepoMtimeParams struct {
	SessionID string `json:"session_id"`
	RepoPath  string `json:"repo_path"`
	JSONLPath string `json:"jsonl_path,omitempty"`
	MtimeNS   int64  `json:"mtime_ns,omitempty"`
}

type MoleculeParams struct {
	SessionID  string `json:"session_id"`
	MoleculeID string `json:"molecule_id"`
}

type StaleIssuesParams struct {
	SessionID string      `json:"session_id"`
	Filter    StaleFilter `json:"filter,omitempty"`
}

type CompactionParams struct {
	SessionID     string   `json:"session_id"`
	IssueID       string   `json:"issue_id,omitempty"`
	Tier          int      `json:"tier,omitempty"`
	OriginalSize  int      `json:"original_size,omitempty"`
	CompactedSize int      `json:"compacted_size,omitempty"`
	CommitHash    string   `json:"commit_hash,omitempty"`
	InitialHash   string   `json:"initial_hash,omitempty"`
	BoundaryHash  string   `json:"boundary_hash,omitempty"`
	OldCommits    int      `json:"old_commits,omitempty"`
	RecentHashes  []string `json:"recent_hashes,omitempty"`
}

type EligibilityResult struct {
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason,omitempty"`
}

type MetadataParams struct {
	SessionID string `json:"session_id"`
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
}

type CreateIssueParams struct {
	SessionID string `json:"session_id"`
	Issue     *Issue `json:"issue"`
	Actor     string `json:"actor,omitempty"`
	Commit    bool   `json:"commit,omitempty"`
	Message   string `json:"message,omitempty"`
}

type CreateIssuesParams struct {
	SessionID string             `json:"session_id"`
	Issues    []*Issue           `json:"issues"`
	Actor     string             `json:"actor,omitempty"`
	Options   BatchCreateOptions `json:"options,omitempty"`
}

type IssueIDParams struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
}

type ExternalRefParams struct {
	SessionID   string `json:"session_id"`
	ExternalRef string `json:"external_ref"`
}

type IssueIDsParams struct {
	SessionID string   `json:"session_id"`
	IDs       []string `json:"ids"`
}

type DeleteIssuesParams struct {
	SessionID string   `json:"session_id"`
	IDs       []string `json:"ids"`
	Cascade   bool     `json:"cascade,omitempty"`
	Force     bool     `json:"force,omitempty"`
	DryRun    bool     `json:"dry_run,omitempty"`
}

type SourceRepoParams struct {
	SessionID  string `json:"session_id"`
	SourceRepo string `json:"source_repo"`
}

type PrefixRenameParams struct {
	SessionID string `json:"session_id"`
	OldPrefix string `json:"old_prefix"`
	NewPrefix string `json:"new_prefix"`
}

type UpdateIssueIDParams struct {
	SessionID string `json:"session_id"`
	OldID     string `json:"old_id"`
	NewID     string `json:"new_id"`
	Issue     *Issue `json:"issue,omitempty"`
	Actor     string `json:"actor,omitempty"`
}

type ClaimIssueParams struct {
	SessionID string     `json:"session_id"`
	ID        string     `json:"id,omitempty"`
	Filter    WorkFilter `json:"filter,omitempty"`
	Actor     string     `json:"actor,omitempty"`
}

type ReclaimExpiredLeasesParams struct {
	SessionID string        `json:"session_id"`
	OlderThan time.Duration `json:"older_than,omitempty"`
	Actor     string        `json:"actor,omitempty"`
}

type SearchIssuesParams struct {
	SessionID string      `json:"session_id"`
	Query     string      `json:"query,omitempty"`
	Filter    IssueFilter `json:"filter,omitempty"`
}

type UpdateIssueParams struct {
	SessionID string                 `json:"session_id"`
	ID        string                 `json:"id"`
	Updates   map[string]interface{} `json:"updates"`
	Actor     string                 `json:"actor,omitempty"`
	Commit    bool                   `json:"commit,omitempty"`
	Message   string                 `json:"message,omitempty"`
}

type ReopenIssueParams struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
	Reason    string `json:"reason,omitempty"`
	Actor     string `json:"actor,omitempty"`
}

type UpdateIssueTypeParams struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
	IssueType string `json:"issue_type"`
	Actor     string `json:"actor,omitempty"`
}

type CloseIssueParams struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
	Reason    string `json:"reason,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Session   string `json:"session,omitempty"`
}

type DependencyParams struct {
	SessionID   string               `json:"session_id"`
	Dependency  *Dependency          `json:"dependency,omitempty"`
	IssueID     string               `json:"issue_id,omitempty"`
	DependsOnID string               `json:"depends_on_id,omitempty"`
	Actor       string               `json:"actor,omitempty"`
	Options     DependencyAddOptions `json:"options,omitempty"`
}

type TransactionParams struct {
	SessionID string `json:"session_id,omitempty"`
	TxID      string `json:"tx_id,omitempty"`
	CommitMsg string `json:"commit_msg,omitempty"`
}

type CycleEdgesParams struct {
	SessionID string      `json:"session_id"`
	Edges     [][2]string `json:"edges"`
}

type DependencyTreeParams struct {
	SessionID    string `json:"session_id"`
	IssueID      string `json:"issue_id"`
	MaxDepth     int    `json:"max_depth,omitempty"`
	ShowAllPaths bool   `json:"show_all_paths,omitempty"`
	Reverse      bool   `json:"reverse,omitempty"`
}

type AddLabelParams struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	Actor     string `json:"actor,omitempty"`
	Commit    bool   `json:"commit,omitempty"`
	Message   string `json:"message,omitempty"`
}

type LabelParams struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id,omitempty"`
	Label     string `json:"label,omitempty"`
	Actor     string `json:"actor,omitempty"`
}

type ReadyWorkParams struct {
	SessionID string     `json:"session_id"`
	Filter    WorkFilter `json:"filter,omitempty"`
}

type WispParams struct {
	SessionID string     `json:"session_id"`
	Filter    WispFilter `json:"filter,omitempty"`
}

type CommentParams struct {
	SessionID string    `json:"session_id"`
	IssueID   string    `json:"issue_id"`
	Author    string    `json:"author,omitempty"`
	Text      string    `json:"text,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type EventsParams struct {
	SessionID string    `json:"session_id"`
	IssueID   string    `json:"issue_id,omitempty"`
	Limit     int       `json:"limit,omitempty"`
	Since     time.Time `json:"since,omitempty"`
}

type RefParams struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name,omitempty"`
	URL       string `json:"url,omitempty"`
	Force     bool   `json:"force,omitempty"`
	Peer      string `json:"peer,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Hash      string `json:"hash,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type HistoryParams struct {
	SessionID string `json:"session_id"`
	IssueID   string `json:"issue_id,omitempty"`
	Ref       string `json:"ref,omitempty"`
	FromRef   string `json:"from_ref,omitempty"`
	ToRef     string `json:"to_ref,omitempty"`
}

type ResolveConflictParams struct {
	SessionID string `json:"session_id"`
	Table     string `json:"table"`
	Strategy  string `json:"strategy"`
}

type BackupParams struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name,omitempty"`
	URL       string `json:"url,omitempty"`
	Dir       string `json:"dir,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

type FederationPeerParams struct {
	SessionID string          `json:"session_id"`
	Name      string          `json:"name,omitempty"`
	Peer      *FederationPeer `json:"peer,omitempty"`
}

type CountIssuesParams struct {
	SessionID string      `json:"session_id"`
	Query     string      `json:"query,omitempty"`
	Filter    IssueFilter `json:"filter,omitempty"`
	GroupBy   string      `json:"group_by,omitempty"`
}

type CountIssueParams struct {
	SessionID string `json:"session_id"`
	IssueID   string `json:"issue_id"`
	Limit     int    `json:"limit,omitempty"`
}

type DependencyCountsParams struct {
	SessionID string   `json:"session_id"`
	IssueIDs  []string `json:"issue_ids"`
}

type BlockingInfoResult struct {
	BlockedBy map[string][]string `json:"blocked_by"`
	Blocks    map[string][]string `json:"blocks"`
	Parents   map[string]string   `json:"parents"`
}

type IsBlockedResult struct {
	Blocked   bool     `json:"blocked"`
	BlockedBy []string `json:"blocked_by,omitempty"`
}

type StatusCountParams struct {
	SessionID string `json:"session_id"`
	IssueID   string `json:"issue_id"`
	Status    Status `json:"status"`
}

type MergeSlotParams struct {
	SessionID string `json:"session_id"`
	Holder    string `json:"holder,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Wait      bool   `json:"wait,omitempty"`
}

type SlotParams struct {
	SessionID string `json:"session_id"`
	IssueID   string `json:"issue_id"`
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	Actor     string `json:"actor,omitempty"`
}

type CommitParams struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}
