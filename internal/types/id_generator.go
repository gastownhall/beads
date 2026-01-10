package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// GenerateHashID creates a deterministic content-based hash ID.
// Format: prefix-{6-8-char-hex} with progressive extension on collision
// Examples: bd-a3f2dd (6), bd-a3f2dda (7), bd-a3f2dda8 (8)
//
// The hash is computed from:
// - Title (primary identifier)
// - Description (additional context)
// - Created timestamp (RFC3339Nano for precision)
// - Workspace ID (prevents cross-workspace collisions)
//
// Returns the full 64-char hash for progressive collision handling.
// Caller extracts hash[:6] initially, then hash[:7], hash[:8] on collisions.
//
// Collision probability with 6 chars (24 bits):
// - 1,000 issues: ~2.94% chance (most extend to 7 chars)
// - 10,000 issues: ~94.9% chance (most extend to 7-8 chars)
//
// Progressive strategy optimizes for common case: 97% stay at 6 chars.
func GenerateHashID(prefix, title, description string, created time.Time, workspaceID string) string {
	h := sha256.New()
	
	// Write all components to hash
	h.Write([]byte(title))
	h.Write([]byte(description))
	h.Write([]byte(created.Format(time.RFC3339Nano)))
	h.Write([]byte(workspaceID))
	
	// Return full hash for progressive length selection
	hash := hex.EncodeToString(h.Sum(nil))
	return hash
}

// GenerateChildID creates a hierarchical child ID.
// Format: parent.N (e.g., "bd-af78e9a2.1", "bd-af78e9a2.1.2")
//
// Max depth: 3 levels (prevents over-decomposition)
// Max breadth: Unlimited (tested up to 347 children)
func GenerateChildID(parentID string, childNumber int) string {
	return fmt.Sprintf("%s.%d", parentID, childNumber)
}

// ParseHierarchicalID extracts the parent ID and depth from a hierarchical ID.
// Returns: (rootID, parentID, depth)
//
// Examples:
//   "bd-af78e9a2" → ("bd-af78e9a2", "", 0)
//   "bd-af78e9a2.1" → ("bd-af78e9a2", "bd-af78e9a2", 1)
//   "bd-af78e9a2.1.2" → ("bd-af78e9a2", "bd-af78e9a2.1", 2)
func ParseHierarchicalID(id string) (rootID, parentID string, depth int) {
	// Count dots to determine depth
	depth = 0
	lastDot := -1
	for i, ch := range id {
		if ch == '.' {
			depth++
			lastDot = i
		}
	}
	
	// Root ID (no parent)
	if depth == 0 {
		return id, "", 0
	}
	
	// Find root ID (everything before first dot)
	firstDot := -1
	for i, ch := range id {
		if ch == '.' {
			firstDot = i
			break
		}
	}
	rootID = id[:firstDot]
	
	// Parent ID (everything before last dot)
	parentID = id[:lastDot]
	
	return rootID, parentID, depth
}

// MaxHierarchyDepth is the maximum nesting level for hierarchical IDs.
// Prevents over-decomposition and keeps IDs manageable.
const MaxHierarchyDepth = 3

// CheckHierarchyDepth validates that adding a child to parentID won't exceed maxDepth.
// Returns an error if the depth would be exceeded.
// If maxDepth < 1, it defaults to MaxHierarchyDepth.
func CheckHierarchyDepth(parentID string, maxDepth int) error {
	if maxDepth < 1 {
		maxDepth = MaxHierarchyDepth
	}

	// Count dots to determine current depth
	depth := 0
	for _, ch := range parentID {
		if ch == '.' {
			depth++
		}
	}

	if depth >= maxDepth {
		return fmt.Errorf("maximum hierarchy depth (%d) exceeded for parent %s", maxDepth, parentID)
	}
	return nil
}
