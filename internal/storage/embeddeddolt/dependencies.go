//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddDependency adds a dependency without recording a dependency_added event —
// the no-event default for create-with-deps and structural callers.
func (s *EmbeddedDoltStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return s.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{})
}

// AddDependencyWithOptions adds a dependency; EmitEvent records a
// dependency_added history event for the explicit dep verbs.
func (s *EmbeddedDoltStore) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, addOpts storage.DependencyAddOptions) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		// Embedded commits the whole working set on the connection, so the
		// event-written flag is not needed for selective staging (unlike DoltStore).
		_, err := issueops.AddDependencyInTx(ctx, tx, dep, actor, issueops.AddDependencyOpts{
			IsCrossPrefix: types.ExtractPrefix(dep.IssueID) != types.ExtractPrefix(dep.DependsOnID),
			EmitEvent:     addOpts.EmitEvent,
		})
		return err
	})
}

// RemoveDependency removes a dependency without recording a dependency_removed
// event — the no-event default for structural callers (issue delete, reparent,
// batch, duplicate cleanup). The explicit bd dep remove verb calls
// RemoveDependencyWithOptions with EmitEvent set.
func (s *EmbeddedDoltStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return s.RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, storage.DependencyRemoveOptions{})
}

// RemoveDependencyWithOptions removes a dependency; EmitEvent records a
// dependency_removed history event for the explicit dep verb.
func (s *EmbeddedDoltStore) RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, rmOpts storage.DependencyRemoveOptions) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		// Embedded commits the whole working set on the connection, so the
		// event-written flag is not needed for selective staging (unlike DoltStore).
		_, err := issueops.RemoveDependencyInTx(ctx, tx, issueID, dependsOnID, actor, rmOpts.EmitEvent)
		return err
	})
}

// GetIssuesByIDs retrieves multiple issues by ID.
func (s *EmbeddedDoltStore) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetIssuesByIDsInTx(ctx, tx, ids, nil)
		return err
	})
	return result, err
}

// GetDependenciesWithMetadata returns issues that the given issue depends on,
// along with the dependency type.
func (s *EmbeddedDoltStore) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var result []*types.IssueWithDependencyMetadata
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependenciesWithMetadataInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// GetDependentsWithMetadata returns issues that depend on the given issue,
// along with the dependency type.
func (s *EmbeddedDoltStore) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var result []*types.IssueWithDependencyMetadata
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependentsWithMetadataInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// DetectCycles finds dependency cycles across both permanent and wisp dependencies.
func (s *EmbeddedDoltStore) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	var result [][]*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.DetectCyclesInTx(ctx, tx)
		return err
	})
	return result, err
}
