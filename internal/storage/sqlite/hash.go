package sqlite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// computeIssueContentHash computes a SHA256 hash of an issue's content, excluding timestamps.
// This is used for detecting timestamp-only changes during export deduplication.
func computeIssueContentHash(issue *types.Issue) (string, error) {
	// Clone issue and zero out timestamps to exclude them from hash
	normalized := *issue
	normalized.CreatedAt = time.Time{}
	normalized.UpdatedAt = time.Time{}
	
	// Also zero out ClosedAt if present
	if normalized.ClosedAt != nil {
		zeroTime := time.Time{}
		normalized.ClosedAt = &zeroTime
	}
	
	// Serialize to JSON
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	
	// SHA256 hash
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
