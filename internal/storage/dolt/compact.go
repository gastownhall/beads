//go:build cgo

package dolt

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// CheckEligibility checks if an issue is eligible for compaction at the given tier.
// TODO: Implement compaction eligibility for Dolt backend.
func (s *DoltStore) CheckEligibility(_ context.Context, _ string, _ int) (bool, string, error) {
	return false, "compaction not yet implemented for Dolt backend", nil
}

// ApplyCompaction records a compaction result in the database.
// TODO: Implement compaction tracking for Dolt backend.
func (s *DoltStore) ApplyCompaction(_ context.Context, _ string, _ int, _, _ int, _ string) error {
	return fmt.Errorf("compaction not yet implemented for Dolt backend")
}

// GetTier1Candidates returns issues eligible for tier 1 compaction.
// TODO: Implement for Dolt backend.
func (s *DoltStore) GetTier1Candidates(_ context.Context) ([]*types.CompactionCandidate, error) {
	return nil, nil
}

// GetTier2Candidates returns issues eligible for tier 2 compaction.
// TODO: Implement for Dolt backend.
func (s *DoltStore) GetTier2Candidates(_ context.Context) ([]*types.CompactionCandidate, error) {
	return nil, nil
}
