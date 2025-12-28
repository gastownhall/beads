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

// ValidAgentRoles are the known agent role types for ID pattern validation
var ValidAgentRoles = []string{
	"mayor",    // Town-level: gt-mayor
	"deacon",   // Town-level: gt-deacon
	"witness",  // Per-rig: gt-witness-<rig>
	"refinery", // Per-rig: gt-refinery-<rig>
	"crew",     // Per-rig with name: gt-crew-<rig>-<name>
	"polecat",  // Per-rig with name: gt-polecat-<rig>-<name>
}

// TownLevelRoles are agent roles that don't have a rig
var TownLevelRoles = []string{"mayor", "deacon"}

// NamedRoles are agent roles that include a worker name
var NamedRoles = []string{"crew", "polecat"}

// ValidateAgentID validates that an agent ID follows the expected pattern.
// Patterns:
//   - Town-level: gt-<role> (e.g., gt-mayor, gt-deacon)
//   - Per-rig: gt-<role>-<rig> (e.g., gt-witness-gastown)
//   - Named: gt-<role>-<rig>-<name> (e.g., gt-polecat-gastown-nux)
//
// Returns nil if the ID is valid, or an error describing the issue.
func ValidateAgentID(id string) error {
	if id == "" {
		return fmt.Errorf("agent ID is required")
	}

	// Must start with gt-
	if !strings.HasPrefix(id, "gt-") {
		return fmt.Errorf("agent ID must start with 'gt-' (got %q)", id)
	}

	// Split into parts after the prefix
	rest := id[3:] // Skip "gt-"
	parts := strings.Split(rest, "-")
	if len(parts) < 1 || parts[0] == "" {
		return fmt.Errorf("agent ID must include role type: gt-<role>[-<rig>[-<name>]] (got %q)", id)
	}

	role := parts[0]

	// Check if role is valid
	validRole := false
	for _, r := range ValidAgentRoles {
		if role == r {
			validRole = true
			break
		}
	}
	if !validRole {
		return fmt.Errorf("invalid agent role %q (valid: %s)", role, strings.Join(ValidAgentRoles, ", "))
	}

	// Check town-level roles (no rig allowed)
	for _, r := range TownLevelRoles {
		if role == r {
			if len(parts) > 1 {
				return fmt.Errorf("town-level agent %q cannot have rig suffix (expected gt-%s, got %q)", role, role, id)
			}
			return nil // Valid town-level agent
		}
	}

	// Per-rig agents require at least a rig
	if len(parts) < 2 {
		return fmt.Errorf("per-rig agent %q requires rig: gt-%s-<rig> (got %q)", role, role, id)
	}

	rig := parts[1]
	if rig == "" {
		return fmt.Errorf("rig name cannot be empty in %q", id)
	}

	// Check named roles (require name)
	for _, r := range NamedRoles {
		if role == r {
			if len(parts) < 3 {
				return fmt.Errorf("agent %q requires name: gt-%s-<rig>-<name> (got %q)", role, role, id)
			}
			name := parts[2]
			if name == "" {
				return fmt.Errorf("agent name cannot be empty in %q", id)
			}
			// Extra parts after name are allowed (e.g., for complex identifiers)
			return nil // Valid named agent
		}
	}

	// Regular per-rig agents (witness, refinery) - should have exactly 2 parts
	if len(parts) > 2 {
		return fmt.Errorf("agent %q takes only rig: gt-%s-<rig> (got %q)", role, role, id)
	}

	return nil // Valid per-rig agent
}
