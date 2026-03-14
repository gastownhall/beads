package storage

import (
	"context"

	"github.com/gastownhall/beads/internal/types"
)

// CompactionStore provides issue compaction and tiering operations.
type CompactionStore interface {
	CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error)
	ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, compactedSize int, commitHash string) error
	GetTier1Candidates(ctx context.Context) ([]*types.CompactionCandidate, error)
	GetTier2Candidates(ctx context.Context) ([]*types.CompactionCandidate, error)
}
