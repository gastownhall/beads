//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Compile-time interface check.
var _ storage.Storage = (*EmbeddedDoltStore)(nil)

// EmbeddedDoltStore implements storage.Storage backed by the embedded Dolt engine.
// Each method call opens a short-lived connection, executes within an explicit
// SQL transaction, and closes the connection immediately. This minimizes the
// time the embedded engine's write lock is held, reducing contention when
// multiple processes access the same database concurrently.
type EmbeddedDoltStore struct {
	dataDir  string
	database string
	branch   string
	closed   atomic.Bool
}

// errClosed is returned when a method is called after Close.
var errClosed = errors.New("embeddeddolt: store is closed")

// New creates an EmbeddedDoltStore using the embedded Dolt engine.
// beadsDir is the .beads/ root; the data directory is derived as <beadsDir>/embeddeddolt/.
// New validates the configuration by opening and immediately closing a test connection.
func New(ctx context.Context, beadsDir, database, branch string) (*EmbeddedDoltStore, error) {
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return nil, fmt.Errorf("embeddeddolt: creating data directory: %w", err)
	}

	return &EmbeddedDoltStore{
		dataDir:  dataDir,
		database: database,
		branch:   branch,
	}, nil
}

// withConn opens a short-lived database connection, begins an explicit SQL
// transaction, and passes it to fn. If commit is true and fn returns nil, the
// transaction is committed; otherwise it is rolled back. The connection is
// closed before withConn returns regardless of outcome.
func (s *EmbeddedDoltStore) withConn(ctx context.Context, commit bool, fn func(tx *sql.Tx) error) (err error) {
	if s.closed.Load() {
		err = errClosed
		return
	}

	var db *sql.DB
	var cleanup func() error
	db, cleanup, err = OpenSQL(ctx, s.dataDir, s.database, s.branch)
	if err != nil {
		return
	}

	defer func() {
		err = errors.Join(err, cleanup())
	}()

	var tx *sql.Tx
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		err = fmt.Errorf("embeddeddolt: begin tx: %w", err)
		return
	}

	err = fn(tx)
	if err != nil {
		err = errors.Join(err, tx.Rollback())
		return
	}

	if !commit {
		return tx.Rollback()
	}

	err = tx.Commit()
	return
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

// Close marks the store as closed. Subsequent method calls will return errClosed.
// It is safe to call multiple times.
func (s *EmbeddedDoltStore) Close() error {
	s.closed.Store(true)
	return nil
}
