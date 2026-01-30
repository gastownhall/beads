// Package storage tests for interface compliance and contract verification.
package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Compile-time interface conformance checks.
// These verify that mock implementations can satisfy the interfaces.
// Real conformance tests for sqlite and memory are in their respective packages.
var (
	_ Storage     = (*mockStorage)(nil)
	_ Transaction = (*mockTransaction)(nil)
)

// mockStorage is a minimal mock for interface testing.
type mockStorage struct{}

func (m *mockStorage) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	return nil
}
func (m *mockStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	return nil
}
func (m *mockStorage) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts BatchCreateOptions) error {
	return nil
}
func (m *mockStorage) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	return nil
}
func (m *mockStorage) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	return nil
}
func (m *mockStorage) DeleteIssue(ctx context.Context, id string) error {
	return nil
}
func (m *mockStorage) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return nil
}
func (m *mockStorage) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return nil
}
func (m *mockStorage) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (m *mockStorage) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (m *mockStorage) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	return nil, nil
}
func (m *mockStorage) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	return nil, nil
}
func (m *mockStorage) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	return nil, nil
}
func (m *mockStorage) GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	return nil, nil
}
func (m *mockStorage) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	return nil, nil
}
func (m *mockStorage) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return nil
}
func (m *mockStorage) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return nil
}
func (m *mockStorage) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return nil, nil
}
func (m *mockStorage) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	return nil, nil
}
func (m *mockStorage) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	return nil, nil
}
func (m *mockStorage) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	return false, nil, nil
}
func (m *mockStorage) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	return nil, nil
}
func (m *mockStorage) GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	return nil, nil
}
func (m *mockStorage) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return nil
}
func (m *mockStorage) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	return nil, nil
}
func (m *mockStorage) GetAllEventsSince(ctx context.Context, sinceID int64) ([]*types.Event, error) {
	return nil, nil
}
func (m *mockStorage) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	return nil, nil
}
func (m *mockStorage) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	return nil, nil
}
func (m *mockStorage) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	return nil, nil
}
func (m *mockStorage) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	return nil, nil
}
func (m *mockStorage) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	return nil, nil
}
func (m *mockStorage) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	return nil, nil
}
func (m *mockStorage) GetDirtyIssues(ctx context.Context) ([]string, error) {
	return nil, nil
}
func (m *mockStorage) GetDirtyIssueHash(ctx context.Context, issueID string) (string, error) {
	return "", nil
}
func (m *mockStorage) ClearDirtyIssuesByID(ctx context.Context, issueIDs []string) error {
	return nil
}
func (m *mockStorage) GetExportHash(ctx context.Context, issueID string) (string, error) {
	return "", nil
}
func (m *mockStorage) SetExportHash(ctx context.Context, issueID, contentHash string) error {
	return nil
}
func (m *mockStorage) ClearAllExportHashes(ctx context.Context) error {
	return nil
}
func (m *mockStorage) GetJSONLFileHash(ctx context.Context) (string, error) {
	return "", nil
}
func (m *mockStorage) SetJSONLFileHash(ctx context.Context, fileHash string) error {
	return nil
}
func (m *mockStorage) GetNextChildID(ctx context.Context, parentID string) (string, error) {
	return "", nil
}
func (m *mockStorage) SetConfig(ctx context.Context, key, value string) error {
	return nil
}
func (m *mockStorage) GetConfig(ctx context.Context, key string) (string, error) {
	return "", nil
}
func (m *mockStorage) GetAllConfig(ctx context.Context) (map[string]string, error) {
	return nil, nil
}
func (m *mockStorage) DeleteConfig(ctx context.Context, key string) error {
	return nil
}
func (m *mockStorage) GetCustomStatuses(ctx context.Context) ([]string, error) {
	return nil, nil
}
func (m *mockStorage) GetCustomTypes(ctx context.Context) ([]string, error) {
	return nil, nil
}
func (m *mockStorage) SetMetadata(ctx context.Context, key, value string) error {
	return nil
}
func (m *mockStorage) GetMetadata(ctx context.Context, key string) (string, error) {
	return "", nil
}
func (m *mockStorage) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	return nil
}
func (m *mockStorage) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return nil
}
func (m *mockStorage) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return nil
}
func (m *mockStorage) RunInTransaction(ctx context.Context, fn func(tx Transaction) error) error {
	return nil
}
func (m *mockStorage) Close() error {
	return nil
}
func (m *mockStorage) Path() string {
	return ""
}
func (m *mockStorage) UnderlyingDB() *sql.DB {
	return nil
}
func (m *mockStorage) UnderlyingConn(ctx context.Context) (*sql.Conn, error) {
	return nil, nil
}

// mockTransaction is a minimal mock for Transaction interface testing.
type mockTransaction struct{}

func (m *mockTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	return nil
}
func (m *mockTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	return nil
}
func (m *mockTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	return nil
}
func (m *mockTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	return nil
}
func (m *mockTransaction) DeleteIssue(ctx context.Context, id string) error {
	return nil
}
func (m *mockTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return nil, nil
}
func (m *mockTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	return nil, nil
}
func (m *mockTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return nil
}
func (m *mockTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return nil
}
func (m *mockTransaction) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	return nil, nil
}
func (m *mockTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return nil
}
func (m *mockTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return nil
}
func (m *mockTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return nil, nil
}
func (m *mockTransaction) SetConfig(ctx context.Context, key, value string) error {
	return nil
}
func (m *mockTransaction) GetConfig(ctx context.Context, key string) (string, error) {
	return "", nil
}
func (m *mockTransaction) SetMetadata(ctx context.Context, key, value string) error {
	return nil
}
func (m *mockTransaction) GetMetadata(ctx context.Context, key string) (string, error) {
	return "", nil
}
func (m *mockTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return nil
}
func (m *mockTransaction) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	return nil, nil
}
func (m *mockTransaction) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	return nil, nil
}

// TestConfig verifies the Config struct has expected fields.
func TestConfig(t *testing.T) {
	t.Run("sqlite config", func(t *testing.T) {
		cfg := Config{
			Backend: "sqlite",
			Path:    "/tmp/test.db",
		}
		if cfg.Backend != "sqlite" {
			t.Errorf("expected backend 'sqlite', got %q", cfg.Backend)
		}
		if cfg.Path != "/tmp/test.db" {
			t.Errorf("expected path '/tmp/test.db', got %q", cfg.Path)
		}
	})

	t.Run("postgres config", func(t *testing.T) {
		cfg := Config{
			Backend:  "postgres",
			Host:     "localhost",
			Port:     5432,
			Database: "beads",
			User:     "test",
			Password: "secret",
			SSLMode:  "disable",
		}
		if cfg.Backend != "postgres" {
			t.Errorf("expected backend 'postgres', got %q", cfg.Backend)
		}
		if cfg.Port != 5432 {
			t.Errorf("expected port 5432, got %d", cfg.Port)
		}
	})
}

// TestInterfaceDocumentation verifies interface methods exist with expected signatures.
// This serves as documentation and catches accidental signature changes.
func TestInterfaceDocumentation(t *testing.T) {
	t.Run("Storage interface has expected method groups", func(t *testing.T) {
		var s Storage = &mockStorage{}

		// Verify issue operations
		_ = s.CreateIssue
		_ = s.CreateIssues
		_ = s.CreateIssuesWithFullOptions
		_ = s.GetIssue
		_ = s.GetIssueByExternalRef
		_ = s.UpdateIssue
		_ = s.CloseIssue
		_ = s.DeleteIssue
		_ = s.SearchIssues

		// Verify dependency operations
		_ = s.AddDependency
		_ = s.RemoveDependency
		_ = s.GetDependencies
		_ = s.GetDependents
		_ = s.GetDependencyRecords
		_ = s.GetAllDependencyRecords
		_ = s.GetDependencyCounts
		_ = s.GetDependencyTree
		_ = s.DetectCycles

		// Verify label operations
		_ = s.AddLabel
		_ = s.RemoveLabel
		_ = s.GetLabels
		_ = s.GetLabelsForIssues
		_ = s.GetIssuesByLabel

		// Verify ready work operations
		_ = s.GetReadyWork
		_ = s.GetBlockedIssues
		_ = s.GetEpicsEligibleForClosure
		_ = s.GetStaleIssues

		// Verify event/comment operations
		_ = s.AddComment
		_ = s.GetEvents
		_ = s.GetAllEventsSince
		_ = s.AddIssueComment
		_ = s.GetIssueComments
		_ = s.GetCommentsForIssues

		// Verify statistics
		_ = s.GetStatistics

		// Verify dirty tracking
		_ = s.GetDirtyIssues
		_ = s.GetDirtyIssueHash
		_ = s.ClearDirtyIssuesByID

		// Verify export hash tracking
		_ = s.GetExportHash
		_ = s.SetExportHash
		_ = s.ClearAllExportHashes
		_ = s.GetJSONLFileHash
		_ = s.SetJSONLFileHash

		// Verify ID generation
		_ = s.GetNextChildID

		// Verify config operations
		_ = s.SetConfig
		_ = s.GetConfig
		_ = s.GetAllConfig
		_ = s.DeleteConfig
		_ = s.GetCustomStatuses
		_ = s.GetCustomTypes

		// Verify metadata operations
		_ = s.SetMetadata
		_ = s.GetMetadata

		// Verify prefix rename operations
		_ = s.UpdateIssueID
		_ = s.RenameDependencyPrefix
		_ = s.RenameCounterPrefix

		// Verify transaction support
		_ = s.RunInTransaction

		// Verify lifecycle
		_ = s.Close
		_ = s.Path
		_ = s.UnderlyingDB
		_ = s.UnderlyingConn
	})

	t.Run("Transaction interface has expected methods", func(t *testing.T) {
		var tx Transaction = &mockTransaction{}

		// Issue operations
		_ = tx.CreateIssue
		_ = tx.CreateIssues
		_ = tx.UpdateIssue
		_ = tx.CloseIssue
		_ = tx.DeleteIssue
		_ = tx.GetIssue
		_ = tx.SearchIssues

		// Dependency operations
		_ = tx.AddDependency
		_ = tx.RemoveDependency

		// Label operations
		_ = tx.AddLabel
		_ = tx.RemoveLabel

		// Config operations
		_ = tx.SetConfig
		_ = tx.GetConfig

		// Metadata operations
		_ = tx.SetMetadata
		_ = tx.GetMetadata

		// Comment operations
		_ = tx.AddComment
	})
}
