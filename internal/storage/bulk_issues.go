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
	// DisarmAutoLeases atomically sets lease.auto=off and NULLs the armed
	// leases on existing in_progress rows (both tiers) without releasing
	// them, so turning automatic stamping off and removing the existing
	// reclaim exposure happen in one transaction. Returns the number of rows
	// disarmed. Explicitly requested leases created afterward (WithLeaseTTL)
	// remain reclaimable.
	DisarmAutoLeases(ctx context.Context) (int64, error)
	// RenewLeases renews the leases named by refs — each keyed on (id, fence)
	// so a claim whose ownership moved since the caller's snapshot is
	// reported lost rather than silently renewed — in one transaction, and
	// returns a typed outcome per ref. Orchestrator-facing: the driver holds
	// its own liveness evidence and renews confirmed-live claims. Tier-aware.
	// Bounded batching is the caller's job (see RenewLeasesChunked, a
	// store-agnostic helper) so an unbounded set does not rewrite row_lock in
	// one transaction and livelock against concurrent worker writes.
	RenewLeases(ctx context.Context, refs []LeaseRef, ttl time.Duration) ([]LeaseRenewalResult, error)
	// CountActiveClaimsByOwner counts in_progress claims held by owner across
	// both tiers, so a session-close gate can ask "does this owner still hold
	// work?" without scanning statuses, tiers, and aliases.
	CountActiveClaimsByOwner(ctx context.Context, owner string) (int, error)
	PromoteFromEphemeral(ctx context.Context, id string, actor string) error
	GetNextChildID(ctx context.Context, parentID string) (string, error)
}
