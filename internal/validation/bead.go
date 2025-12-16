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

// ValidatePriority parses and validates a priority string.
// Returns the parsed priority (0-4) or an error if invalid.
// Supports both numeric (0-4) and P-prefix format (P0-P4).
func ValidatePriority(priorityStr string) (int, error) {
	priority := ParsePriority(priorityStr)
	if priority == -1 {
		return -1, fmt.Errorf("invalid priority %q (expected 0-4 or P0-P4, not words like high/medium/low)", priorityStr)
	}
	return priority, nil
}

// ValidateIDFormat validates that an ID has the correct format.
// Supports: prefix-number (bd-42), prefix-hash (bd-a3f8e9), or hierarchical (bd-a3f8e9.1)
// Returns the prefix part or an error if invalid.
func ValidateIDFormat(id string) (string, error) {
	if id == "" {
		return "", nil
	}

	// Must contain hyphen
	if !strings.Contains(id, "-") {
		return "", fmt.Errorf("invalid ID format '%s' (expected format: prefix-hash or prefix-hash.number, e.g., 'bd-a3f8e9' or 'bd-a3f8e9.1')", id)
	}

	// Extract prefix (before the first hyphen)
	hyphenIdx := strings.Index(id, "-")
	prefix := id[:hyphenIdx]

	return prefix, nil
}

// ValidatePrefix checks that the requested prefix matches the database prefix.
// Returns an error if they don't match (unless force is true).
func ValidatePrefix(requestedPrefix, dbPrefix string, force bool) error {
	if force || dbPrefix == "" || dbPrefix == requestedPrefix {
		return nil
	}

	return fmt.Errorf("prefix mismatch: database uses '%s' but you specified '%s' (use --force to override)", dbPrefix, requestedPrefix)
}
