package dolt

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

const (
	defaultTier1Days = 30
	defaultTier2Days = 90
)

// getCompactDays reads the configured compaction threshold for the given tier,
// falling back to the default if the config key is missing or unparseable.
func (s *DoltStore) getCompactDays(ctx context.Context, tier int) int {
	key := "compact_tier1_days"
	def := defaultTier1Days
	if tier == 2 {
		key = "compact_tier2_days"
		def = defaultTier2Days
	}
	val, err := s.GetConfig(ctx, key)
	if err != nil || val == "" {
		return def
	}
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// CheckEligibility checks if an issue is eligible for compaction at the given tier.
func (s *DoltStore) CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error) {
	var eligible bool
	var reason string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		eligible, reason, err = issueops.CheckEligibilityInTx(ctx, tx, issueID, tier)
		return err
	})
	return eligible, reason, err
}

// ApplyCompaction records a compaction result in the database.
func (s *DoltStore) ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, _ int, commitHash string) error {
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return issueops.ApplyCompactionInTx(ctx, tx, issueID, tier, originalSize, commitHash)
	})
}

// GetTier1Candidates returns issues eligible for tier 1 compaction.
func (s *DoltStore) GetTier1Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	var result []*types.CompactionCandidate
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetTier1CandidatesInTx(ctx, tx)
		return err
	})
	return result, err
}

// GetTier2Candidates returns issues eligible for tier 2 compaction.
func (s *DoltStore) GetTier2Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	var result []*types.CompactionCandidate
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetTier2CandidatesInTx(ctx, tx)
		return err
	})
	return result, err
}
