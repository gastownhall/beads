package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// DependencyQueryStore provides extended dependency queries beyond the base Storage interface.
type DependencyQueryStore interface {
	GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error)
	GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error)
	// GetDependentRecords returns raw dependency rows whose target is targetID
	// (the inbound edges), without hydrating the source issues, spanning both the
	// durable and wisp dependency tables. depType filters by dependency type
	// ("" = all); results are ordered by the dependency row's primary id ASC,
	// bounded by limit (0 = a store default, capped), and paged with afterID as a
	// keyset continuation over that id ("" = start). Mirrors the source-keyed
	// GetDependencyRecords but in the target direction; unblocks target-side
	// group membership reads that must see dangling children. RAW: applies no
	// visibility policy — the caller filters at hydration.
	GetDependentRecords(ctx context.Context, targetID string, depType string, limit int, afterID string) ([]*types.Dependency, error)
	// GetDependentRecordsForIssues returns raw dependency rows keyed by TARGET id:
	// for each id in targetIDs, the rows whose target is that id (its inbound edges
	// = its dependents), across both the durable and wisp dependency tables, ALL
	// dep types (the caller filters), de-duped by row id. It is the batched,
	// target-keyed mirror of GetDependencyRecordsForIssues — the whole-page read
	// that answers "what does each of these ids block/gate" without a per-id
	// fan-out. RAW: applies no visibility policy — the caller filters at hydration.
	GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error)
	// CountDependentRecords returns the total number of inbound edges of targetID
	// (same predicate/scope as GetDependentRecords, depType "" = all), across
	// both dependency tables, without paging. Callers that want a true total
	// membership count need it without walking every page. RAW count — no
	// visibility policy.
	CountDependentRecords(ctx context.Context, targetID string, depType string) (int, error)
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
	GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error)
	GetBlockingInfoForIssues(ctx context.Context, issueIDs []string) (blockedByMap map[string][]string, blocksMap map[string][]string, parentMap map[string]string, err error)
	IsBlocked(ctx context.Context, issueID string) (bool, []string, error)
	// IsBlockedBatch returns the denormalized, TRANSITIVE is_blocked flag for a
	// page of ids in one batched read (SELECT id, is_blocked FROM {issues,wisps}
	// WHERE id IN (...), batched at queryBatchSize) — the same value IsBlocked
	// returns per id, without an N-call fan-out or a per-row blocker recompute.
	// It reflects inherited/ancestor blockedness (a child of a blocked parent is
	// blocked with no direct blocking edge). ids present in neither table are
	// absent from the map; callers treat absent as not-blocked.
	IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error)
	GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error)
	DetectCycles(ctx context.Context) ([][]*types.Issue, error)
	FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error)

	// IterAllDependencyRecords streams every dependency edge in the rig as
	// a flat sequence of *types.Dependency rows. Callers that today walk
	// GetAllDependencyRecords (which returns map[string][]*types.Dependency)
	// can rebuild that map by streaming and grouping on Dependency.IssueID.
	IterAllDependencyRecords(ctx context.Context) (Iter[types.Dependency], error)

	// CountDependentsByStatus returns the number of issues that depend on issueID
	// and are in the given status. Preferred over CountDependents + per-row filtering
	// for the bd close epic-closure check.
	CountDependentsByStatus(ctx context.Context, issueID string, status types.Status) (int64, error)
}
