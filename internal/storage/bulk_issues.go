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
	// ErrNotClaimable/ErrAlreadyClaimed if actor no longer holds the lease, and
	// ErrUnleased when actor does hold the claim but the store disarmed
	// automatic leases (heartbeat renews, it never arms).
	HeartbeatIssue(ctx context.Context, id, actor string) error
	// ReclaimExpiredLeases reverts in_progress issues whose lease expired more
	// than olderThan ago back to ready (clearing the assignee), recovering work
	// stranded by dead workers. filter scopes which stale leases are eligible
	// (the zero ReclaimFilter reclaims globally, the historical behavior).
	// Returns the issues it reclaimed.
	ReclaimExpiredLeases(ctx context.Context, olderThan time.Duration, filter types.ReclaimFilter, actor string) ([]types.ReclaimedLease, error)
	// DisarmAutoLeases turns automatic lease stamping off (the lease.auto
	// config key) and clears the lease rows of the claims already holding one,
	// so the flip and the removal of the existing reclaim exposure land in one
	// transaction. Nothing is released: status, assignee and the ownership
	// fence are untouched. Returns the number of lease rows cleared.
	DisarmAutoLeases(ctx context.Context) (int64, error)
	PromoteFromEphemeral(ctx context.Context, id string, actor string) error
	GetNextChildID(ctx context.Context, parentID string) (string, error)
}
