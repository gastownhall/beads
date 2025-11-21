package validation

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// ParsePriority extracts and validates a priority value from content.
// Supports both numeric (0-4) and P-prefix format (P0-P4).
// Returns the parsed priority (0-4) or -1 if invalid.
func ParsePriority(content string) int {
	content = strings.TrimSpace(content)
	
	// Handle "P1", "P0", etc. format
	if strings.HasPrefix(strings.ToUpper(content), "P") {
		content = content[1:] // Strip the "P" prefix
	}
	
	var p int
	if _, err := fmt.Sscanf(content, "%d", &p); err == nil && p >= 0 && p <= 4 {
		return p
	}
	return -1 // Invalid
}

// ParseIssueType extracts and validates an issue type from content.
// Returns the validated type or error if invalid.
func ParseIssueType(content string) (types.IssueType, error) {
	issueType := types.IssueType(strings.TrimSpace(content))

	// Validate issue type
	validTypes := map[types.IssueType]bool{
		types.TypeBug:     true,
		types.TypeFeature: true,
		types.TypeTask:    true,
		types.TypeEpic:    true,
		types.TypeChore:   true,
	}

	if !validTypes[issueType] {
		return types.TypeTask, fmt.Errorf("invalid issue type: %s", content)
	}

	return issueType, nil
}
