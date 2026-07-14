package sqlkit

import (
	"context"
	"database/sql"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddDependency adds a dependency between two issues. Routing (which
// dependency table the edge lives in, and how the target is classified) is
// resolved inside the mutation tx via IsActiveWispInTx; issueops.AddDependencyInTx
// validates, cycle-checks, and inserts. Running everything in withMutationTx
// commits the edge and its is_blocked reprojection atomically.
func (s *Store) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		isCrossPrefix := types.ExtractPrefix(dep.IssueID) != types.ExtractPrefix(dep.DependsOnID)

		writeTable, sourceTable := "dependencies", "issues"
		if issueops.IsActiveWispInTx(ctx, tx, dep.IssueID) {
			writeTable, sourceTable = "wisp_dependencies", "wisps"
		}

		targetTable := "issues"
		kind := issueops.DepTargetIssue
		switch {
		case isCrossPrefix, strings.HasPrefix(dep.DependsOnID, "external:"):
			kind = issueops.DepTargetExternal
		default:
			if issueops.IsActiveWispInTx(ctx, tx, dep.DependsOnID) {
				targetTable = "wisps"
				kind = issueops.DepTargetWisp
			}
		}

		return issueops.AddDependencyInTx(ctx, tx, dep, actor, issueops.AddDependencyOpts{
			SourceTable:   sourceTable,
			TargetTable:   targetTable,
			WriteTable:    writeTable,
			IsCrossPrefix: isCrossPrefix,
			TargetKind:    &kind,
		})
	})
}

// RemoveDependency removes a dependency between two issues. issueops handles
// wisp routing internally, so both source classes collapse to one call. actor
// is unused (kept to satisfy the storage.Storage signature).
func (s *Store) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		return issueops.RemoveDependencyInTx(ctx, tx, issueID, dependsOnID)
	})
}

// GetDependencies retrieves issues that this issue depends on.
func (s *Store) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependenciesInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// GetDependents retrieves issues that depend on this issue.
func (s *Store) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependentsInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// GetDependenciesWithMetadata returns dependencies with their edge metadata.
// issueops queries both dependencies and wisp_dependencies and hydrates the
// targets, so there is no store-level wisp branch or inline SQL.
func (s *Store) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var result []*types.IssueWithDependencyMetadata
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependenciesWithMetadataInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// GetDependentsWithMetadata returns dependents with their edge metadata.
func (s *Store) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var result []*types.IssueWithDependencyMetadata
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependentsWithMetadataInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// GetDependencyTree returns a dependency tree for visualization.
func (s *Store) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	var result []*types.TreeNode
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependencyTreeInTx(ctx, tx, issueID, maxDepth, showAllPaths, reverse)
		return err
	})
	return result, err
}

// CountDependents returns the number of issues that depend on issueID.
// Counts both dependency tables so the total matches GetDependentsWithMetadata:
// a dependent may be a permanent issue (edge in `dependencies`) or a wisp (edge
// in `wisp_dependencies`, routed there by WispTableRouting on the source).
//
// Both tables' targets are resolved via issueops.DepTargetExpr (the split
// physical columns) rather than the STORED generated depends_on_id, which a
// count(*) can fail to resolve under the pure-Go GMS analyzer.
func (s *Store) CountDependents(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var perm, wisp int64
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM dependencies WHERE `+issueops.DepTargetExpr+` = ?`, issueID).Scan(&perm); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM wisp_dependencies WHERE `+issueops.DepTargetExpr+` = ?`, issueID).Scan(&wisp); err != nil {
			return err
		}
		n = perm + wisp
		return nil
	})
	return n, err
}

// CountDependencies returns the number of issues that issueID depends on.
// Counts both dependency tables so the total matches GetDependenciesWithMetadata:
// a wisp's outgoing edges live in `wisp_dependencies`, a permanent issue's in
// `dependencies`. Counted as two separate queries summed in Go (see
// CountDependents for why a single combined query is avoided).
func (s *Store) CountDependencies(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var perm, wisp int64
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM dependencies WHERE issue_id = ?`, issueID).Scan(&perm); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM wisp_dependencies WHERE issue_id = ?`, issueID).Scan(&wisp); err != nil {
			return err
		}
		n = perm + wisp
		return nil
	})
	return n, err
}
