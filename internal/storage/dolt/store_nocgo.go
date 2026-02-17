//go:build !cgo

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// DoltStore is a stub for non-CGO builds. All methods return an error indicating
// that Dolt requires CGO. This allows the CLI to compile without CGO but report
// a clear error at runtime if Dolt operations are attempted.
type DoltStore struct{}

// Config mirrors the CGO Config struct for API compatibility.
type Config struct {
	Path           string
	CommitterName  string
	CommitterEmail string
	Remote         string
	Database       string
	ReadOnly       bool
	OpenTimeout    time.Duration

	ServerMode     bool
	ServerHost     string
	ServerPort     int
	ServerUser     string
	ServerPassword string
	ServerTLS      bool

	DisableWatchdog bool
}

var errNoCGO = fmt.Errorf("dolt: this binary was built without CGO support; rebuild with CGO_ENABLED=1")

// --- Types defined in CGO-only files ---

// CommitInfo represents a Dolt commit (stub for non-CGO builds).
type CommitInfo struct {
	Hash    string
	Author  string
	Email   string
	Date    time.Time
	Message string
}

// HistoryEntry represents a row from dolt_history_* table (stub for non-CGO builds).
type HistoryEntry struct {
	CommitHash string
	Committer  string
	CommitDate time.Time
	IssueData  map[string]interface{}
}

// DoltStatus represents the current repository status (stub for non-CGO builds).
type DoltStatus struct {
	Staged   []StatusEntry
	Unstaged []StatusEntry
}

// StatusEntry represents a changed table (stub for non-CGO builds).
type StatusEntry struct {
	Table  string
	Status string
}

// SyncResult contains the outcome of a Sync operation (stub for non-CGO builds).
type SyncResult struct {
	Peer              string
	StartTime         time.Time
	EndTime           time.Time
	Fetched           bool
	Merged            bool
	Pushed            bool
	PulledCommits     int
	PushedCommits     int
	Conflicts         []storage.Conflict
	ConflictsResolved bool
	Error             error
	PushError         error
}

// FederationPeer is an alias for storage.FederationPeer for convenience.
type FederationPeer = storage.FederationPeer

// ServerConfig holds configuration for the dolt sql-server (stub for non-CGO builds).
type ServerConfig struct {
	DataDir        string
	SQLPort        int
	RemotesAPIPort int
	Host           string
	LogFile        string
	User           string
	ReadOnly       bool
}

// Server manages a dolt sql-server process (stub for non-CGO builds).
type Server struct{}

// BootstrapResult contains statistics about the bootstrap operation (stub for non-CGO builds).
type BootstrapResult struct {
	IssuesImported       int
	IssuesSkipped        int
	RoutesImported       int
	InteractionsImported int
	ParseErrors          []ParseError
	PrefixDetected       string
}

// ParseError describes a JSONL parsing error (stub for non-CGO builds).
type ParseError struct {
	Line    int
	Message string
	Snippet string
}

// BootstrapRoute holds route data for bootstrap import (stub for non-CGO builds).
type BootstrapRoute struct {
	Prefix string
	Path   string
}

// BootstrapConfig controls bootstrap behavior (stub for non-CGO builds).
type BootstrapConfig struct {
	BeadsDir    string
	DoltPath    string
	LockTimeout time.Duration
	Database    string
	Routes      []BootstrapRoute
}

// AdaptiveIDConfig holds configuration for adaptive ID length scaling (stub for non-CGO builds).
type AdaptiveIDConfig struct {
	MaxCollisionProbability float64
	MinLength               int
	MaxLength               int
}

// Migration represents a single schema migration for Dolt (stub for non-CGO builds).
type Migration struct {
	Name string
	Func func(*sql.DB) error
}

// --- Server constants ---

const (
	DefaultSQLPort        = 3307
	DefaultRemotesAPIPort = 8080
)

// --- Constructors ---

// New returns an error in non-CGO builds.
func New(_ context.Context, _ *Config) (*DoltStore, error) {
	return nil, errNoCGO
}

// NewFromConfig returns an error in non-CGO builds.
func NewFromConfig(_ context.Context, _ string) (*DoltStore, error) {
	return nil, errNoCGO
}

// NewFromConfigWithOptions returns an error in non-CGO builds.
func NewFromConfigWithOptions(_ context.Context, _ string, _ *Config) (*DoltStore, error) {
	return nil, errNoCGO
}

// --- Public standalone functions ---

// GetBackendFromConfig returns the backend type from metadata.json.
func GetBackendFromConfig(_ string) string {
	return "dolt"
}

// Bootstrap returns an error in non-CGO builds.
func Bootstrap(_ context.Context, _ BootstrapConfig) (bool, *BootstrapResult, error) {
	return false, nil, errNoCGO
}

// NewServer returns nil in non-CGO builds.
func NewServer(_ ServerConfig) *Server {
	return nil
}

// GetRunningServerPID returns 0 in non-CGO builds.
func GetRunningServerPID(_ string) int {
	return 0
}

// StopServerByPID returns an error in non-CGO builds.
func StopServerByPID(_ int) error {
	return errNoCGO
}

// DetectRunningServer returns false in non-CGO builds.
func DetectRunningServer() (string, int, bool) {
	return "", 0, false
}

// RunMigrations returns an error in non-CGO builds.
func RunMigrations(_ *sql.DB) error {
	return errNoCGO
}

// ListMigrations returns nil in non-CGO builds.
func ListMigrations() []string {
	return nil
}

// DefaultAdaptiveConfig returns a zero-value config in non-CGO builds.
func DefaultAdaptiveConfig() AdaptiveIDConfig {
	return AdaptiveIDConfig{}
}

// GetAdaptiveIDLengthTx returns an error in non-CGO builds.
func GetAdaptiveIDLengthTx(_ context.Context, _ *sql.Tx, _ string) (int, error) {
	return 0, errNoCGO
}

// --- Server methods ---

// Start returns an error in non-CGO builds.
func (s *Server) Start(_ context.Context) error { return errNoCGO }

// Stop returns nil in non-CGO builds.
func (s *Server) Stop() error { return nil }

// IsRunning returns false in non-CGO builds.
func (s *Server) IsRunning() bool { return false }

// SQLPort returns 0 in non-CGO builds.
func (s *Server) SQLPort() int { return 0 }

// RemotesAPIPort returns 0 in non-CGO builds.
func (s *Server) RemotesAPIPort() int { return 0 }

// Host returns "" in non-CGO builds.
func (s *Server) Host() string { return "" }

// DSN returns "" in non-CGO builds.
func (s *Server) DSN(_ string) string { return "" }

// --- DoltStore: Close / Path / UnderlyingDB ---

func (s *DoltStore) Close() error {
	return nil
}

func (s *DoltStore) Path() string {
	return ""
}

func (s *DoltStore) UnderlyingDB() *sql.DB {
	return nil
}

// --- DoltStore: Issue CRUD ---

func (s *DoltStore) CreateIssue(_ context.Context, _ *types.Issue, _ string) error {
	return errNoCGO
}

func (s *DoltStore) CreateIssues(_ context.Context, _ []*types.Issue, _ string) error {
	return errNoCGO
}

func (s *DoltStore) CreateIssuesWithFullOptions(_ context.Context, _ []*types.Issue, _ string, _ storage.BatchCreateOptions) error {
	return errNoCGO
}

func (s *DoltStore) GetIssue(_ context.Context, _ string) (*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetIssueByExternalRef(_ context.Context, _ string) (*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetIssuesByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) UpdateIssue(_ context.Context, _ string, _ map[string]interface{}, _ string) error {
	return errNoCGO
}

func (s *DoltStore) UpdateIssueID(_ context.Context, _, _ string, _ *types.Issue, _ string) error {
	return errNoCGO
}

func (s *DoltStore) ClaimIssue(_ context.Context, _ string, _ string) error {
	return errNoCGO
}

func (s *DoltStore) CloseIssue(_ context.Context, _ string, _ string, _ string, _ string) error {
	return errNoCGO
}

func (s *DoltStore) DeleteIssue(_ context.Context, _ string) error {
	return errNoCGO
}

func (s *DoltStore) DeleteIssues(_ context.Context, _ []string, _ bool, _ bool, _ bool) (*types.DeleteIssuesResult, error) {
	return nil, errNoCGO
}

func (s *DoltStore) DeleteIssuesBySourceRepo(_ context.Context, _ string) (int, error) {
	return 0, errNoCGO
}

// --- DoltStore: Search / Query ---

func (s *DoltStore) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetReadyWork(_ context.Context, _ types.WorkFilter) ([]*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetBlockedIssues(_ context.Context, _ types.WorkFilter) ([]*types.BlockedIssue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetEpicsEligibleForClosure(_ context.Context) ([]*types.EpicStatus, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetStaleIssues(_ context.Context, _ types.StaleFilter) ([]*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetStatistics(_ context.Context) (*types.Statistics, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetMoleculeProgress(_ context.Context, _ string) (*types.MoleculeProgressStats, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetNextChildID(_ context.Context, _ string) (string, error) {
	return "", errNoCGO
}

// --- DoltStore: Config / Metadata ---

func (s *DoltStore) SetConfig(_ context.Context, _, _ string) error {
	return errNoCGO
}

func (s *DoltStore) GetConfig(_ context.Context, _ string) (string, error) {
	return "", errNoCGO
}

func (s *DoltStore) GetAllConfig(_ context.Context) (map[string]string, error) {
	return nil, errNoCGO
}

func (s *DoltStore) DeleteConfig(_ context.Context, _ string) error {
	return errNoCGO
}

func (s *DoltStore) SetMetadata(_ context.Context, _, _ string) error {
	return errNoCGO
}

func (s *DoltStore) GetMetadata(_ context.Context, _ string) (string, error) {
	return "", errNoCGO
}

func (s *DoltStore) GetCustomStatuses(_ context.Context) ([]string, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetCustomTypes(_ context.Context) ([]string, error) {
	return nil, errNoCGO
}

// --- DoltStore: Labels ---

func (s *DoltStore) AddLabel(_ context.Context, _, _, _ string) error {
	return errNoCGO
}

func (s *DoltStore) RemoveLabel(_ context.Context, _, _, _ string) error {
	return errNoCGO
}

func (s *DoltStore) GetLabels(_ context.Context, _ string) ([]string, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetLabelsForIssues(_ context.Context, _ []string) (map[string][]string, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetIssuesByLabel(_ context.Context, _ string) ([]*types.Issue, error) {
	return nil, errNoCGO
}

// --- DoltStore: Dependencies ---

func (s *DoltStore) AddDependency(_ context.Context, _ *types.Dependency, _ string) error {
	return errNoCGO
}

func (s *DoltStore) RemoveDependency(_ context.Context, _, _, _ string) error {
	return errNoCGO
}

func (s *DoltStore) GetDependencies(_ context.Context, _ string) ([]*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetDependents(_ context.Context, _ string) ([]*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetDependenciesWithMetadata(_ context.Context, _ string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetDependentsWithMetadata(_ context.Context, _ string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetDependencyRecords(_ context.Context, _ string) ([]*types.Dependency, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetAllDependencyRecords(_ context.Context) (map[string][]*types.Dependency, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetDependencyRecordsForIssues(_ context.Context, _ []string) (map[string][]*types.Dependency, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetDependencyCounts(_ context.Context, _ []string) (map[string]*types.DependencyCounts, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetDependencyTree(_ context.Context, _ string, _ int, _ bool, _ bool) ([]*types.TreeNode, error) {
	return nil, errNoCGO
}

func (s *DoltStore) DetectCycles(_ context.Context) ([][]*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) IsBlocked(_ context.Context, _ string) (bool, []string, error) {
	return false, nil, errNoCGO
}

func (s *DoltStore) GetNewlyUnblockedByClose(_ context.Context, _ string) ([]*types.Issue, error) {
	return nil, errNoCGO
}

// --- DoltStore: Events / Comments ---

func (s *DoltStore) AddComment(_ context.Context, _, _, _ string) error {
	return errNoCGO
}

func (s *DoltStore) GetEvents(_ context.Context, _ string, _ int) ([]*types.Event, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetAllEventsSince(_ context.Context, _ int64) ([]*types.Event, error) {
	return nil, errNoCGO
}

func (s *DoltStore) AddIssueComment(_ context.Context, _, _, _ string) (*types.Comment, error) {
	return nil, errNoCGO
}

func (s *DoltStore) ImportIssueComment(_ context.Context, _, _, _ string, _ time.Time) (*types.Comment, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetIssueComments(_ context.Context, _ string) ([]*types.Comment, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetCommentsForIssues(_ context.Context, _ []string) (map[string][]*types.Comment, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetCommentCounts(_ context.Context, _ []string) (map[string]int, error) {
	return nil, errNoCGO
}

// --- DoltStore: Version Control ---

func (s *DoltStore) Commit(_ context.Context, _ string) error {
	return errNoCGO
}

func (s *DoltStore) Push(_ context.Context) error {
	return errNoCGO
}

func (s *DoltStore) Pull(_ context.Context) error {
	return errNoCGO
}

func (s *DoltStore) Branch(_ context.Context, _ string) error {
	return errNoCGO
}

func (s *DoltStore) Checkout(_ context.Context, _ string) error {
	return errNoCGO
}

func (s *DoltStore) Merge(_ context.Context, _ string) ([]storage.Conflict, error) {
	return nil, errNoCGO
}

func (s *DoltStore) CurrentBranch(_ context.Context) (string, error) {
	return "", errNoCGO
}

func (s *DoltStore) DeleteBranch(_ context.Context, _ string) error {
	return errNoCGO
}

func (s *DoltStore) Log(_ context.Context, _ int) ([]CommitInfo, error) {
	return nil, errNoCGO
}

func (s *DoltStore) AddRemote(_ context.Context, _, _ string) error {
	return errNoCGO
}

func (s *DoltStore) Status(_ context.Context) (*DoltStatus, error) {
	return nil, errNoCGO
}

// --- DoltStore: Versioned Storage ---

func (s *DoltStore) History(_ context.Context, _ string) ([]*storage.HistoryEntry, error) {
	return nil, errNoCGO
}

func (s *DoltStore) AsOf(_ context.Context, _, _ string) (*types.Issue, error) {
	return nil, errNoCGO
}

func (s *DoltStore) Diff(_ context.Context, _, _ string) ([]*storage.DiffEntry, error) {
	return nil, errNoCGO
}

func (s *DoltStore) ListBranches(_ context.Context) ([]string, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetCurrentCommit(_ context.Context) (string, error) {
	return "", errNoCGO
}

func (s *DoltStore) GetConflicts(_ context.Context) ([]storage.Conflict, error) {
	return nil, errNoCGO
}

func (s *DoltStore) CommitExists(_ context.Context, _ string) (bool, error) {
	return false, errNoCGO
}

func (s *DoltStore) ResolveConflicts(_ context.Context, _, _ string) error {
	return errNoCGO
}

// --- DoltStore: Federation ---

func (s *DoltStore) PushTo(_ context.Context, _ string) error {
	return errNoCGO
}

func (s *DoltStore) PullFrom(_ context.Context, _ string) ([]storage.Conflict, error) {
	return nil, errNoCGO
}

func (s *DoltStore) Fetch(_ context.Context, _ string) error {
	return errNoCGO
}

func (s *DoltStore) ListRemotes(_ context.Context) ([]storage.RemoteInfo, error) {
	return nil, errNoCGO
}

func (s *DoltStore) RemoveRemote(_ context.Context, _ string) error {
	return errNoCGO
}

func (s *DoltStore) SyncStatus(_ context.Context, _ string) (*storage.SyncStatus, error) {
	return nil, errNoCGO
}

func (s *DoltStore) Sync(_ context.Context, _, _ string) (*SyncResult, error) {
	return nil, errNoCGO
}

// --- DoltStore: Federation Credentials ---

func (s *DoltStore) AddFederationPeer(_ context.Context, _ *storage.FederationPeer) error {
	return errNoCGO
}

func (s *DoltStore) GetFederationPeer(_ context.Context, _ string) (*storage.FederationPeer, error) {
	return nil, errNoCGO
}

func (s *DoltStore) ListFederationPeers(_ context.Context) ([]*storage.FederationPeer, error) {
	return nil, errNoCGO
}

func (s *DoltStore) RemoveFederationPeer(_ context.Context, _ string) error {
	return errNoCGO
}

// --- DoltStore: Compaction ---

func (s *DoltStore) CheckEligibility(_ context.Context, _ string, _ int) (bool, string, error) {
	return false, "", errNoCGO
}

func (s *DoltStore) ApplyCompaction(_ context.Context, _ string, _ int, _, _ int, _ string) error {
	return errNoCGO
}

func (s *DoltStore) GetTier1Candidates(_ context.Context) ([]*types.CompactionCandidate, error) {
	return nil, errNoCGO
}

func (s *DoltStore) GetTier2Candidates(_ context.Context) ([]*types.CompactionCandidate, error) {
	return nil, errNoCGO
}

// --- DoltStore: Rename ---

func (s *DoltStore) RenameDependencyPrefix(_ context.Context, _, _ string) error {
	return errNoCGO
}

func (s *DoltStore) RenameCounterPrefix(_ context.Context, _, _ string) error {
	return errNoCGO
}

// --- DoltStore: Transaction ---

func (s *DoltStore) RunInTransaction(_ context.Context, _ func(tx storage.Transaction) error) error {
	return errNoCGO
}

// --- DoltStore: Multi-repo ---

func (s *DoltStore) ClearRepoMtime(_ context.Context, _ string) error {
	return errNoCGO
}
