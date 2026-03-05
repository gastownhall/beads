//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Compile-time interface check.
var _ storage.Storage = (*EmbeddedDoltStore)(nil)

// EmbeddedDoltStore implements storage.Storage backed by the embedded Dolt engine.
type EmbeddedDoltStore struct {
	db        *sql.DB
	cleanup   func() error
	dataDir   string
	closeOnce sync.Once
	closeErr  error
}

// New creates an EmbeddedDoltStore using the embedded Dolt engine.
// beadsDir is the .beads/ root; the data directory is derived as <beadsDir>/embeddeddolt/.
func New(ctx context.Context, beadsDir, database, branch string) (*EmbeddedDoltStore, error) {
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return nil, fmt.Errorf("embeddeddolt: creating data directory: %w", err)
	}
	db, cleanup, err := OpenSQL(ctx, dataDir, database, branch)
	if err != nil {
		return nil, err
	}
	return &EmbeddedDoltStore{db: db, cleanup: cleanup, dataDir: dataDir}, nil
}

func (s *EmbeddedDoltStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	panic("embeddeddolt: CreateIssue not implemented")
}

func (s *EmbeddedDoltStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	panic("embeddeddolt: CreateIssues not implemented")
}

func (s *EmbeddedDoltStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	panic("embeddeddolt: GetIssue not implemented")
}

func (s *EmbeddedDoltStore) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	panic("embeddeddolt: GetIssueByExternalRef not implemented")
}

func (s *EmbeddedDoltStore) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	panic("embeddeddolt: GetIssuesByIDs not implemented")
}

func (s *EmbeddedDoltStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	panic("embeddeddolt: UpdateIssue not implemented")
}

func (s *EmbeddedDoltStore) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	panic("embeddeddolt: CloseIssue not implemented")
}

func (s *EmbeddedDoltStore) DeleteIssue(ctx context.Context, id string) error {
	panic("embeddeddolt: DeleteIssue not implemented")
}

func (s *EmbeddedDoltStore) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	panic("embeddeddolt: SearchIssues not implemented")
}

func (s *EmbeddedDoltStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	panic("embeddeddolt: AddDependency not implemented")
}

func (s *EmbeddedDoltStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	panic("embeddeddolt: RemoveDependency not implemented")
}

func (s *EmbeddedDoltStore) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	panic("embeddeddolt: GetDependencies not implemented")
}

func (s *EmbeddedDoltStore) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	panic("embeddeddolt: GetDependents not implemented")
}

func (s *EmbeddedDoltStore) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	panic("embeddeddolt: GetDependenciesWithMetadata not implemented")
}

func (s *EmbeddedDoltStore) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	panic("embeddeddolt: GetDependentsWithMetadata not implemented")
}

func (s *EmbeddedDoltStore) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	panic("embeddeddolt: GetDependencyTree not implemented")
}

func (s *EmbeddedDoltStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	panic("embeddeddolt: AddLabel not implemented")
}

func (s *EmbeddedDoltStore) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	panic("embeddeddolt: RemoveLabel not implemented")
}

func (s *EmbeddedDoltStore) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	panic("embeddeddolt: GetLabels not implemented")
}

func (s *EmbeddedDoltStore) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	panic("embeddeddolt: GetIssuesByLabel not implemented")
}

func (s *EmbeddedDoltStore) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	panic("embeddeddolt: GetReadyWork not implemented")
}

func (s *EmbeddedDoltStore) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	panic("embeddeddolt: GetBlockedIssues not implemented")
}

func (s *EmbeddedDoltStore) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	panic("embeddeddolt: GetEpicsEligibleForClosure not implemented")
}

func (s *EmbeddedDoltStore) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	panic("embeddeddolt: AddIssueComment not implemented")
}

func (s *EmbeddedDoltStore) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	panic("embeddeddolt: GetIssueComments not implemented")
}

func (s *EmbeddedDoltStore) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	panic("embeddeddolt: GetEvents not implemented")
}

func (s *EmbeddedDoltStore) GetAllEventsSince(ctx context.Context, sinceID int64) ([]*types.Event, error) {
	panic("embeddeddolt: GetAllEventsSince not implemented")
}

func (s *EmbeddedDoltStore) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	panic("embeddeddolt: GetStatistics not implemented")
}

func (s *EmbeddedDoltStore) SetConfig(ctx context.Context, key, value string) error {
	panic("embeddeddolt: SetConfig not implemented")
}

func (s *EmbeddedDoltStore) GetConfig(ctx context.Context, key string) (string, error) {
	panic("embeddeddolt: GetConfig not implemented")
}

func (s *EmbeddedDoltStore) GetAllConfig(ctx context.Context) (map[string]string, error) {
	panic("embeddeddolt: GetAllConfig not implemented")
}

func (s *EmbeddedDoltStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	panic("embeddeddolt: RunInTransaction not implemented")
}

// Close shuts down the embedded Dolt engine and releases all resources.
// It is safe to call multiple times; only the first call performs cleanup.
func (s *EmbeddedDoltStore) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.cleanup()
	})
	return s.closeErr
}
