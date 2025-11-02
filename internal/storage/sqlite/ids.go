package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// ValidateIssueIDPrefix validates that an issue ID matches the configured prefix
// Supports both top-level (bd-a3f8e9) and hierarchical (bd-a3f8e9.1) IDs
func ValidateIssueIDPrefix(id, prefix string) error {
	expectedPrefix := prefix + "-"
	if !strings.HasPrefix(id, expectedPrefix) {
		return fmt.Errorf("issue ID '%s' does not match configured prefix '%s'", id, prefix)
	}
	return nil
}

// GenerateIssueID generates a unique hash-based ID for an issue
// Uses adaptive length based on database size and tries multiple nonces on collision
func GenerateIssueID(ctx context.Context, conn *sql.Conn, prefix string, issue *types.Issue, actor string) (string, error) {
	// Get adaptive base length based on current database size
	baseLength, err := GetAdaptiveIDLength(ctx, conn, prefix)
	if err != nil {
		// Fallback to 6 on error
		baseLength = 6
	}
	
	// Try baseLength, baseLength+1, baseLength+2, up to max of 8
	maxLength := 8
	if baseLength > maxLength {
		baseLength = maxLength
	}
	
	for length := baseLength; length <= maxLength; length++ {
		// Try up to 10 nonces at each length
		for nonce := 0; nonce < 10; nonce++ {
			candidate := generateHashID(prefix, issue.Title, issue.Description, actor, issue.CreatedAt, length, nonce)
			
			// Check if this ID already exists
			var count int
			err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, candidate).Scan(&count)
			if err != nil {
				return "", fmt.Errorf("failed to check for ID collision: %w", err)
			}
			
			if count == 0 {
				return candidate, nil
			}
		}
	}
	
	return "", fmt.Errorf("failed to generate unique ID after trying lengths %d-%d with 10 nonces each", baseLength, maxLength)
}

// GenerateBatchIssueIDs generates unique IDs for multiple issues in a single batch
// Tracks used IDs to prevent intra-batch collisions
func GenerateBatchIssueIDs(ctx context.Context, conn *sql.Conn, prefix string, issues []*types.Issue, actor string, usedIDs map[string]bool) error {
	// Get adaptive base length based on current database size
	baseLength, err := GetAdaptiveIDLength(ctx, conn, prefix)
	if err != nil {
		// Fallback to 6 on error
		baseLength = 6
	}
	
	// Try baseLength, baseLength+1, baseLength+2, up to max of 8
	maxLength := 8
	if baseLength > maxLength {
		baseLength = maxLength
	}
	
	for i := range issues {
		if issues[i].ID == "" {
			var generated bool
			// Try lengths from baseLength to maxLength with progressive fallback
			for length := baseLength; length <= maxLength && !generated; length++ {
				for nonce := 0; nonce < 10; nonce++ {
					candidate := generateHashID(prefix, issues[i].Title, issues[i].Description, actor, issues[i].CreatedAt, length, nonce)
					
					// Check if this ID is already used in this batch or in the database
					if usedIDs[candidate] {
						continue
					}
					
					var count int
					err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, candidate).Scan(&count)
					if err != nil {
						return fmt.Errorf("failed to check for ID collision: %w", err)
					}
					
					if count == 0 {
						issues[i].ID = candidate
						usedIDs[candidate] = true
						generated = true
						break
					}
				}
			}
			
			if !generated {
				return fmt.Errorf("failed to generate unique ID for issue %d after trying lengths %d-%d with 10 nonces each", i, baseLength, maxLength)
			}
		}
	}
	return nil
}

// EnsureIDs generates or validates IDs for issues
// For issues with empty IDs, generates unique hash-based IDs
// For issues with existing IDs, validates they match the prefix and parent exists (if hierarchical)
func EnsureIDs(ctx context.Context, conn *sql.Conn, prefix string, issues []*types.Issue, actor string) error {
	usedIDs := make(map[string]bool)
	
	// First pass: record explicitly provided IDs
	for i := range issues {
		if issues[i].ID != "" {
			// Validate that explicitly provided ID matches the configured prefix (bd-177)
			if err := ValidateIssueIDPrefix(issues[i].ID, prefix); err != nil {
				return err
			}
			
			// For hierarchical IDs (bd-a3f8e9.1), validate parent exists
			if strings.Contains(issues[i].ID, ".") {
				// Extract parent ID (everything before the last dot)
				lastDot := strings.LastIndex(issues[i].ID, ".")
				parentID := issues[i].ID[:lastDot]
				
				var parentCount int
				err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, parentID).Scan(&parentCount)
				if err != nil {
					return fmt.Errorf("failed to check parent existence: %w", err)
				}
				if parentCount == 0 {
					return fmt.Errorf("parent issue %s does not exist", parentID)
				}
			}
			
			usedIDs[issues[i].ID] = true
		}
	}
	
	// Second pass: generate IDs for issues that need them
	return GenerateBatchIssueIDs(ctx, conn, prefix, issues, actor, usedIDs)
}

// generateHashID creates a hash-based ID for a top-level issue.
// For child issues, use the parent ID with a numeric suffix (e.g., "bd-a3f8e9.1").
// Supports adaptive length from 4-8 chars based on database size (bd-ea2a13).
// Includes a nonce parameter to handle same-length collisions.
func generateHashID(prefix, title, description, creator string, timestamp time.Time, length, nonce int) string {
	// Combine inputs into a stable content string
	// Include nonce to handle hash collisions
	content := fmt.Sprintf("%s|%s|%s|%d|%d", title, description, creator, timestamp.UnixNano(), nonce)
	
	// Hash the content
	hash := sha256.Sum256([]byte(content))
	
	// Use variable length (4-8 hex chars)
	// length determines how many bytes to use (2, 2.5, 3, 3.5, or 4)
	var shortHash string
	switch length {
	case 4:
		shortHash = hex.EncodeToString(hash[:2])
	case 5:
		// 2.5 bytes: use 3 bytes but take only first 5 chars
		shortHash = hex.EncodeToString(hash[:3])[:5]
	case 6:
		shortHash = hex.EncodeToString(hash[:3])
	case 7:
		// 3.5 bytes: use 4 bytes but take only first 7 chars
		shortHash = hex.EncodeToString(hash[:4])[:7]
	case 8:
		shortHash = hex.EncodeToString(hash[:4])
	default:
		shortHash = hex.EncodeToString(hash[:3]) // default to 6
	}
	
	return fmt.Sprintf("%s-%s", prefix, shortHash)
}
