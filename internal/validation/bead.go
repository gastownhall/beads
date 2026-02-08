package validation

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
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
// Supports type aliases like "enhancement" -> "feature".
func ParseIssueType(content string) (types.IssueType, error) {
	// Normalize to support aliases like "enhancement" -> "feature"
	issueType := types.IssueType(strings.TrimSpace(content)).Normalize()

	// Use the canonical IsValid() from types package
	if !issueType.IsValid() {
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
// Also supports hyphenated prefixes like "bead-me-up-3e9" or "web-app-abc123".
// Returns the prefix part or an error if invalid.
func ValidateIDFormat(id string) (string, error) {
	if id == "" {
		return "", nil
	}

	// Must contain hyphen
	if !strings.Contains(id, "-") {
		return "", fmt.Errorf("invalid ID format '%s' (expected format: prefix-hash or prefix-hash.number, e.g., 'bd-a3f8e9' or 'bd-a3f8e9.1')", id)
	}

	// Use ExtractIssuePrefix which correctly handles hyphenated prefixes
	// by looking at the last hyphen and checking if suffix is hash-like.
	// This fixes the bug where "bead-me-up-3e9" was parsed as prefix "bead"
	// instead of "bead-me-up".
	prefix := utils.ExtractIssuePrefix(id)

	return prefix, nil
}

// validatePrefix checks that the requested prefix matches the database prefix.
// Returns an error if they don't match (unless force is true).
func validatePrefix(requestedPrefix, dbPrefix string, force bool) error {
	return validatePrefixWithAllowed(requestedPrefix, dbPrefix, "", force)
}

// validatePrefixWithAllowed checks that the requested prefix is allowed.
// It matches if:
// - force is true
// - dbPrefix is empty
// - requestedPrefix matches dbPrefix
// - requestedPrefix is in the comma-separated allowedPrefixes list
// - requestedPrefix is a prefix of any entry in allowedPrefixes (GH#1135)
//
// The prefix-of-allowed check handles cases where ExtractIssuePrefix returns
// a shorter prefix than intended. For example, "hq-cv-test" extracts as "hq"
// (because "test" is word-like), but if "hq-cv" is in allowedPrefixes, we
// should accept "hq" since it's clearly intended to be part of "hq-cv".
// Returns an error if none of these conditions are met.
func validatePrefixWithAllowed(requestedPrefix, dbPrefix, allowedPrefixes string, force bool) error {
	if force || dbPrefix == "" || dbPrefix == requestedPrefix {
		return nil
	}

	// Check if requestedPrefix is in the allowed list or is a prefix of an allowed entry
	if allowedPrefixes != "" {
		for _, allowed := range strings.Split(allowedPrefixes, ",") {
			allowed = strings.TrimSpace(allowed)
			if allowed == requestedPrefix {
				return nil
			}
			// GH#1135: Also accept if requestedPrefix is a prefix of an allowed entry.
			// This handles IDs like "hq-cv-test" where extraction yields "hq" but
			// the user configured "hq-cv" in allowed_prefixes.
			if strings.HasPrefix(allowed, requestedPrefix+"-") {
				return nil
			}
		}
	}

	// Build helpful error message
	if allowedPrefixes != "" {
		return fmt.Errorf("prefix mismatch: database uses '%s' (allowed: %s) but you specified '%s' (use --force to override)",
			dbPrefix, allowedPrefixes, requestedPrefix)
	}
	return fmt.Errorf("prefix mismatch: database uses '%s' but you specified '%s' (use --force to override)", dbPrefix, requestedPrefix)
}

// ValidateIDPrefixAllowed checks that an issue ID's prefix is allowed.
// Unlike validatePrefixWithAllowed which takes an extracted prefix, this function
// takes the full ID and checks if it starts with any allowed prefix.
// This correctly handles multi-hyphen prefixes like "hq-cv-" where the suffix
// might look like an English word (e.g., "hq-cv-test").
// (GH#1135)
//
// It matches if:
// - force is true
// - dbPrefix is empty
// - id starts with dbPrefix + "-"
// - id starts with any prefix in allowedPrefixes + "-"
// Returns an error if none of these conditions are met.
func ValidateIDPrefixAllowed(id, dbPrefix, allowedPrefixes string, force bool) error {
	if force || dbPrefix == "" {
		return nil
	}

	// Check if ID starts with the database prefix
	if strings.HasPrefix(id, dbPrefix+"-") {
		return nil
	}

	// Check if ID starts with any allowed prefix
	if allowedPrefixes != "" {
		for _, allowed := range strings.Split(allowedPrefixes, ",") {
			allowed = strings.TrimSpace(allowed)
			// Normalize: remove trailing - if present (we add it for matching)
			allowed = strings.TrimSuffix(allowed, "-")
			if allowed != "" && strings.HasPrefix(id, allowed+"-") {
				return nil
			}
		}
	}

	// Build helpful error message
	if allowedPrefixes != "" {
		return fmt.Errorf("prefix mismatch: database uses '%s-' (allowed: %s) but ID '%s' doesn't match any allowed prefix (use --force to override)",
			dbPrefix, allowedPrefixes, id)
	}
	return fmt.Errorf("prefix mismatch: database uses '%s-' but ID '%s' doesn't match (use --force to override)", dbPrefix, id)
}
