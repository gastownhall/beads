package storage

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// BulkIssueStore provides extended issue CRUD beyond the base Storage interface.
type BulkIssueStore interface {
	CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts BatchCreateOptions) error
	DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error)
	DeleteIssuesBySourceRepo(ctx context.Context, sourceRepo string) (int, error)
	UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error
	ClaimIssue(ctx context.Context, id string, actor string) error
	ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error)
	// HeartbeatIssue refreshes the lease on an in_progress issue held by actor,
	// pushing its expiry forward so a reaper won't reclaim it. Returns
	// ErrNotClaimable/ErrAlreadyClaimed if actor no longer holds the lease.
	HeartbeatIssue(ctx context.Context, id, actor string) error
	// ReclaimExpiredLeases reverts in_progress issues whose lease expired more
	// than olderThan ago back to ready (clearing the assignee), recovering work
	// stranded by dead workers. Returns the issues it reclaimed.
	ReclaimExpiredLeases(ctx context.Context, olderThan time.Duration, actor string) ([]types.ReclaimedLease, error)
	PromoteFromEphemeral(ctx context.Context, id string, actor string) error
	GetNextChildID(ctx context.Context, parentID string) (string, error)
}
