package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/types"
)

// isValidBase36 checks if a string contains only base36 characters
func isValidBase36(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}

// isValidHex checks if a string contains only hex characters
func isValidHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// IsHierarchicalID checks if an issue ID is hierarchical (has a parent).
// Hierarchical IDs have the format {parentID}.{N} where N is a numeric child suffix.
// Returns true and the parent ID if hierarchical, false and empty string otherwise.
//
// This correctly handles prefixes that contain dots (e.g., "my.project-abc123"
// is NOT hierarchical, but "my.project-abc123.1" IS hierarchical with parent
// "my.project-abc123").
//
// The key insight is that hierarchical IDs always end with .{digits} where
// the digits represent the child number (1, 2, 3, etc.).
func IsHierarchicalID(id string) (isHierarchical bool, parentID string) {
	lastDot := strings.LastIndex(id, ".")
	if lastDot == -1 {
		return false, ""
	}

	// Check if the suffix after the last dot is purely numeric
	suffix := id[lastDot+1:]
	if len(suffix) == 0 {
		return false, ""
	}

	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false, ""
		}
	}

	// It's hierarchical - parent is everything before the last dot
	return true, id[:lastDot]
}

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

// tryResurrectParent attempts to find and resurrect a deleted parent issue from the import batch
// Returns true if parent was found and will be created, false otherwise
func tryResurrectParent(parentID string, issues []*types.Issue) bool {
	for _, issue := range issues {
		if issue.ID == parentID {
			return true // Parent exists in the batch being imported
		}
	}
	return false // Parent not in this batch
}

// EnsureIDs generates or validates IDs for issues
// For issues with empty IDs, generates unique hash-based IDs
// For issues with existing IDs, validates they match the prefix and parent exists (if hierarchical)
// For hierarchical IDs with missing parents, behavior depends on orphanHandling mode
// When skipPrefixValidation is true, existing IDs are not validated against the prefix (used during import)
func EnsureIDs(ctx context.Context, conn *sql.Conn, prefix string, issues []*types.Issue, actor string, orphanHandling OrphanHandling, skipPrefixValidation bool) error {
	usedIDs := make(map[string]bool)

	// First pass: record explicitly provided IDs and check for duplicates within batch
	for i := range issues {
		if issues[i].ID != "" {
			// Check for duplicate IDs within the batch
			if usedIDs[issues[i].ID] {
				return fmt.Errorf("duplicate issue ID within batch: %s", issues[i].ID)
			}

			// Check if ID already exists in database
			var existingCount int
			err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, issues[i].ID).Scan(&existingCount)
			if err != nil {
				return fmt.Errorf("failed to check ID existence: %w", err)
			}
			if existingCount > 0 {
				return fmt.Errorf("issue ID already exists: %s", issues[i].ID)
			}

			// Validate that explicitly provided ID matches the configured prefix (bd-177)
			// Skip validation during import to allow issues with different prefixes (e.g., from renamed repos)
			if !skipPrefixValidation {
				if err := ValidateIssueIDPrefix(issues[i].ID, prefix); err != nil {
					return wrapDBErrorf(err, "validate ID prefix for %s", issues[i].ID)
				}
			}

			// For hierarchical IDs (bd-a3f8e9.1), ensure parent exists
			// Use IsHierarchicalID to correctly handle prefixes with dots (GH#508)
			if isHierarchical, parentID := IsHierarchicalID(issues[i].ID); isHierarchical {
				var parentCount int
				err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, parentID).Scan(&parentCount)
				if err != nil {
					return fmt.Errorf("failed to check parent existence: %w", err)
				}
				if parentCount == 0 {
					// Handle missing parent based on mode
					switch orphanHandling {
					case OrphanStrict:
						return fmt.Errorf("parent issue %s does not exist (strict mode)", parentID)
					case OrphanResurrect:
						if !tryResurrectParent(parentID, issues) {
							return fmt.Errorf("parent issue %s does not exist and cannot be resurrected from import batch", parentID)
						}
						// Parent will be created in this batch (due to depth-sorting), so allow this child
					case OrphanSkip:
						// Mark issue for skipping by clearing its ID (will be filtered out later)
						issues[i].ID = ""
						continue
					case OrphanAllow:
						// Allow orphan - no validation
					default:
						// Default to allow for backward compatibility
					}
				}
			}

			usedIDs[issues[i].ID] = true
		}
	}

	// Second pass: generate IDs for issues that need them
	return GenerateBatchIssueIDs(ctx, conn, prefix, issues, actor, usedIDs)
}

// generateHashID creates a hash-based ID for a top-level issue.
// For child issues, use the parent ID with a numeric suffix (e.g., "bd-x7k9p.1").
// Supports adaptive length from 3-8 chars based on database size.
// Includes a nonce parameter to handle same-length collisions.
// Uses base36 encoding (0-9, a-z) for better information density than hex.
func generateHashID(prefix, title, description, creator string, timestamp time.Time, length, nonce int) string {
	return idgen.GenerateHashID(prefix, title, description, creator, timestamp, length, nonce)
}
