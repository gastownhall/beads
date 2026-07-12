package sqlkit

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// This file fills in the capability surface the CLI verbs reach but the earlier
// implement-set left as typed-unsupported stubs on the backend shell. Every
// method is a thin delegation to the backend-neutral issueops layer, mirroring
// the internal/storage/dolt/*.go bodies with the Dolt residue (DOLT_ADD/COMMIT,
// per-lifetime caches, the wisp-batch write-timeout workaround) dropped: wisps
// live in the same database here, so a single *sql.Tx serves every table and
// issueops does its own wisp partitioning internally.

// --- blocking / newly-unblocked reads ---

// IsBlocked reports whether an issue is blocked by open dependencies, returning
// the blocker IDs.
func (s *Store) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	var blocked bool
	var blockers []string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		blocked, blockers, e = issueops.IsBlockedInTx(ctx, tx, issueID)
		return e
	})
	if err != nil {
		return false, nil, err
	}
	return blocked, blockers, nil
}

// GetNewlyUnblockedByClose finds issues that become unblocked when closedIssueID
// is closed.
func (s *Store) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.GetNewlyUnblockedByCloseInTx(ctx, tx, closedIssueID)
		return e
	})
	return result, err
}

// --- claim / delete / child-id writes ---

// ClaimIssue atomically claims an issue (assignee=actor, status=in_progress)
// only if it is currently unassigned. Runs in a mutation tx so the claim and its
// is_blocked reprojection commit atomically.
func (s *Store) ClaimIssue(ctx context.Context, id string, actor string) error {
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		_, err := issueops.ClaimIssueInTx(ctx, tx, id, actor)
		return err
	})
}

// UnclaimIssue releases a claim (clears assignee, returns to open) in a mutation
// tx so the change and its is_blocked reprojection commit atomically.
func (s *Store) UnclaimIssue(ctx context.Context, id string, actor string) error {
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		return issueops.UnclaimIssueInTx(ctx, tx, id, actor)
	})
}

// ClaimReadyIssue atomically claims the first ready issue matching filter, or
// returns (nil, nil) when nothing is ready.
func (s *Store) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	var claimed *types.Issue
	err := s.withMutationTx(ctx, func(tx *sql.Tx) error {
		var e error
		claimed, e = issueops.ClaimReadyIssueInTx(ctx, tx, filter, actor)
		return e
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// DeleteIssues deletes multiple issues in a single transaction. issueops does
// its own wisp partitioning and cascade/force/orphan handling, so all IDs go
// straight through — dolt's outer wisp-batch loop is a write-timeout workaround
// that does not apply here. The partial result is preserved alongside an error
// so OrphanedIssues survives the non-cascade/non-force rejection path.
func (s *Store) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	if len(ids) == 0 {
		return &types.DeleteIssuesResult{}, nil
	}
	var result *types.DeleteIssuesResult
	err := s.withMutationTx(ctx, func(tx *sql.Tx) error {
		r, e := issueops.DeleteIssuesInTx(ctx, tx, ids, cascade, force, dryRun)
		result = r
		return e
	})
	return result, err
}

// GetNextChildID returns the next available child ID for a parent, seeding and
// bumping the child_counters row in the same tx.
func (s *Store) GetNextChildID(ctx context.Context, parentID string) (string, error) {
	var childID string
	err := s.withMutationTx(ctx, func(tx *sql.Tx) error {
		var e error
		childID, e = issueops.GetNextChildIDTx(ctx, tx, parentID)
		return e
	})
	return childID, err
}

// --- custom statuses / types (uncached; the dolt cache is only an optimization) ---

// GetCustomStatusesDetailed returns the configured custom statuses with their
// full definitions.
func (s *Store) GetCustomStatusesDetailed(ctx context.Context) ([]types.CustomStatus, error) {
	// Read the normalized custom_statuses table (ORDER BY name), matching the
	// embedded-Dolt reference — SetConfig("status.custom") keeps it in sync. On a tx
	// error fall back to config.yaml exactly as dolt does.
	var result []types.CustomStatus
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.ResolveCustomStatusesDetailedInTx(ctx, tx)
		return e
	})
	if err != nil {
		if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
			return issueops.ParseStatusFallback(yamlStatuses), nil
		}
		return nil, nil
	}
	return result, nil
}

// GetCustomStatuses returns the configured custom status names.
func (s *Store) GetCustomStatuses(ctx context.Context) ([]string, error) {
	detailed, err := s.GetCustomStatusesDetailed(ctx)
	if err != nil {
		return nil, err
	}
	return types.CustomStatusNames(detailed), nil
}

// GetCustomTypes returns the configured custom issue-type values.
func (s *Store) GetCustomTypes(ctx context.Context) ([]string, error) {
	// Read the normalized custom_types table (ORDER BY name), matching the embedded-Dolt
	// reference; SetConfig("types.custom") keeps it in sync. Fall back to config.yaml on a
	// tx error, as dolt does.
	var result []string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.ResolveCustomTypesInTx(ctx, tx)
		return e
	})
	if err != nil {
		if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
			return yamlTypes, nil
		}
		return nil, err
	}
	return result, nil
}

// --- infra types ---

// GetInfraTypes returns the infrastructure type names (routed to the wisps
// table). issueops resolves DB config → config.yaml → defaults; on a tx error it
// yields nil, in which case we fall back to YAML-then-defaults exactly as dolt
// does. These signatures have no error channel, so a silent nil here would let
// `bd wisp gc` hard-delete protected infra beads — hence the explicit fallback.
func (s *Store) GetInfraTypes(ctx context.Context) map[string]bool {
	var result map[string]bool
	if err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		result = issueops.ResolveInfraTypesInTx(ctx, tx)
		return nil
	}); err != nil || result == nil {
		var typeList []string
		if yamlTypes := config.GetInfraTypesFromYAML(); len(yamlTypes) > 0 {
			typeList = yamlTypes
		} else {
			typeList = domain.DefaultInfraTypes()
		}
		result = make(map[string]bool, len(typeList))
		for _, t := range typeList {
			result[t] = true
		}
	}
	return result
}

// IsInfraTypeCtx reports whether t is a configured infrastructure type.
func (s *Store) IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool {
	return s.GetInfraTypes(ctx)[string(t)]
}

// --- metadata / config ---

// GetMetadata reads a metadata value ("" when absent).
func (s *Store) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		value, e = issueops.GetMetadataInTx(ctx, tx, key)
		return e
	})
	return value, err
}

// SetMetadata writes a metadata value. Metadata cannot change is_blocked, so
// this uses a plain write tx.
func (s *Store) SetMetadata(ctx context.Context, key, value string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.SetMetadataInTx(ctx, tx, key, value)
	})
}

// DeleteConfig removes a config value.
func (s *Store) DeleteConfig(ctx context.Context, key string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.DeleteConfigInTx(ctx, tx, key)
	})
}

// --- batch reads (permanent + wisp mixed-table union handled inside issueops) ---

// GetBlockingInfoForIssues returns the blocked-by, blocks, and parent maps for a
// set of issue IDs.
func (s *Store) GetBlockingInfoForIssues(ctx context.Context, issueIDs []string) (
	blockedByMap map[string][]string,
	blocksMap map[string][]string,
	parentMap map[string]string,
	err error,
) {
	err = s.withReadTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		blockedByMap, blocksMap, parentMap, txErr = issueops.GetBlockingInfoForIssuesInTx(ctx, tx, issueIDs)
		return txErr
	})
	return
}

// GetDependencyCounts returns dependency/dependent counts for multiple issues.
func (s *Store) GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	var result map[string]*types.DependencyCounts
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.GetDependencyCountsInTx(ctx, tx, issueIDs)
		return e
	})
	return result, err
}

// GetCommentCounts returns the comment count for each issue in a single batch.
func (s *Store) GetCommentCounts(ctx context.Context, issueIDs []string) (map[string]int, error) {
	var result map[string]int
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.GetCommentCountsInTx(ctx, tx, issueIDs)
		return e
	})
	return result, err
}

// GetLabelsForIssues returns the labels for each issue in a single batch.
func (s *Store) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	var result map[string][]string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.GetLabelsForIssuesInTx(ctx, tx, issueIDs)
		return e
	})
	return result, err
}

// GetCommentsForIssues returns the comments for each issue in a single batch.
func (s *Store) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	var result map[string][]*types.Comment
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.GetCommentsForIssuesInTx(ctx, tx, issueIDs)
		return e
	})
	return result, err
}

// GetDependencyRecords returns the raw dependency rows for a single issue.
// Delegates to the batch helper, which partitions wisp vs permanent IDs and
// resolves targets via the split physical columns internally.
func (s *Store) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	var deps []*types.Dependency
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		byID, e := issueops.GetDependencyRecordsForIssuesInTx(ctx, tx, []string{issueID})
		if e != nil {
			return e
		}
		deps = byID[issueID]
		return nil
	})
	return deps, err
}

// GetDependencyRecordsForIssues returns the raw dependency rows for multiple
// issues.
func (s *Store) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	var result map[string][]*types.Dependency
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.GetDependencyRecordsForIssuesInTx(ctx, tx, issueIDs)
		return e
	})
	return result, err
}

// DetectCycles finds circular dependencies across both the permanent and wisp
// dependency tables.
func (s *Store) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	var result [][]*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.DetectCyclesInTx(ctx, tx)
		return e
	})
	return result, err
}
