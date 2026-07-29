package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// CompactionStore provides issue compaction and tiering operations.
type CompactionStore interface {
	CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error)
	ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, compactedSize int, commitHash string) error
	GetTier1Candidates(ctx context.Context) ([]*types.CompactionCandidate, error)
	GetTier2Candidates(ctx context.Context) ([]*types.CompactionCandidate, error)

	// SnapshotIssue archives an issue's current text content before a
	// destructive compaction overwrites it. tier is the level being compacted
	// to. Must be called before the overwrite.
	SnapshotIssue(ctx context.Context, issueID string, tier int) error
	// GetCompactionSnapshot returns the most recent archived snapshot for an
	// issue, or (nil, nil) when none exists.
	GetCompactionSnapshot(ctx context.Context, issueID string) (*types.IssueSnapshot, error)
	// RestoreFromSnapshot restores an issue's content from its most recent
	// snapshot and steps its compaction level back down. Returns the applied
	// snapshot, or (nil, nil) when none exists.
	RestoreFromSnapshot(ctx context.Context, issueID string) (*types.IssueSnapshot, error)
}
