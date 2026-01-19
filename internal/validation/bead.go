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

// ValidatePrefix checks that the requested prefix matches the database prefix.
// Returns an error if they don't match (unless force is true).
func ValidatePrefix(requestedPrefix, dbPrefix string, force bool) error {
	return ValidatePrefixWithAllowed(requestedPrefix, dbPrefix, "", force)
}

// ValidatePrefixWithAllowed checks that the requested prefix is allowed.
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
func ValidatePrefixWithAllowed(requestedPrefix, dbPrefix, allowedPrefixes string, force bool) error {
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
// Unlike ValidatePrefixWithAllowed which takes an extracted prefix, this function
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

// ValidAgentRoles are the known agent role types for ID pattern validation
var ValidAgentRoles = []string{
	"mayor",    // Town-level: gt-mayor
	"deacon",   // Town-level: gt-deacon
	"witness",  // Per-rig: gt-<rig>-witness
	"refinery", // Per-rig: gt-<rig>-refinery
	"crew",     // Per-rig with name: gt-<rig>-crew-<name>
	"polecat",  // Per-rig with name: gt-<rig>-polecat-<name>
}

// TownLevelRoles are agent roles that don't have a rig
var TownLevelRoles = []string{"mayor", "deacon"}

// RigLevelRoles are agent roles that have a rig but no name
var RigLevelRoles = []string{"witness", "refinery"}

// NamedRoles are agent roles that include a worker name
var NamedRoles = []string{"crew", "polecat"}

// isValidRole checks if a string is a valid agent role
func isValidRole(s string) bool {
	for _, r := range ValidAgentRoles {
		if s == r {
			return true
		}
	}
	return false
}

// isTownLevelRole checks if a role is a town-level role (no rig)
func isTownLevelRole(s string) bool {
	for _, r := range TownLevelRoles {
		if s == r {
			return true
		}
	}
	return false
}

// isRigLevelRole checks if a role is a rig-level singleton role
func isRigLevelRole(s string) bool {
	for _, r := range RigLevelRoles {
		if s == r {
			return true
		}
	}
	return false
}

// isNamedRole checks if a role requires a worker name
func isNamedRole(s string) bool {
	for _, r := range NamedRoles {
		if s == r {
			return true
		}
	}
	return false
}

// ExtractAgentPrefix extracts the prefix from an agent ID.
// Agent IDs have the format: prefix-rig-role-name or prefix-role
// The prefix is always the part before the first hyphen.
// Examples:
//   - "gt-gastown-polecat-nux" -> "gt"
//   - "nx-nexus-polecat-nux" -> "nx"
//   - "gt-mayor" -> "gt"
//   - "bd-beads-witness" -> "bd"
func ExtractAgentPrefix(id string) string {
	hyphenIdx := strings.Index(id, "-")
	if hyphenIdx <= 0 {
		return ""
	}
	return id[:hyphenIdx]
}

// ValidateAgentID validates that an agent ID follows the expected pattern.
// Canonical format: prefix-rig-role-name
// Patterns:
//   - Town-level: <prefix>-<role> (e.g., gt-mayor, bd-deacon)
//   - Per-rig singleton: <prefix>-<rig>-<role> (e.g., gt-gastown-witness)
//   - Per-rig named: <prefix>-<rig>-<role>-<name> (e.g., gt-gastown-polecat-nux)
//
// The prefix can be any rig's configured prefix (gt-, bd-, etc.).
// Rig names may contain hyphens (e.g., my-project), so we parse by scanning
// for known role tokens from the right side of the ID.
// Returns nil if the ID is valid, or an error describing the issue.
func ValidateAgentID(id string) error {
	if id == "" {
		return fmt.Errorf("agent ID is required")
	}

	// Must contain a hyphen to have a prefix
	hyphenIdx := strings.Index(id, "-")
	if hyphenIdx <= 0 {
		return fmt.Errorf("agent ID must have a prefix followed by '-' (got %q)", id)
	}

	// Split into parts after the prefix
	rest := id[hyphenIdx+1:] // Skip "<prefix>-"
	parts := strings.Split(rest, "-")
	if len(parts) < 1 || parts[0] == "" {
		return fmt.Errorf("agent ID must include content after prefix (got %q)", id)
	}

	// Case 1: Single part after prefix - must be town-level role
	if len(parts) == 1 {
		role := parts[0]
		if isTownLevelRole(role) {
			return nil // Valid town-level agent
		}
		if isValidRole(role) {
			return fmt.Errorf("agent role %q requires rig: <prefix>-<rig>-%s (got %q)", role, role, id)
		}
		return fmt.Errorf("invalid agent role %q (valid: %s)", role, strings.Join(ValidAgentRoles, ", "))
	}

	// For 2+ parts, scan from the right to find a known role.
	// This allows rig names to contain hyphens (e.g., "my-project").
	roleIdx := -1
	var role string
	for i := len(parts) - 1; i >= 0; i-- {
		if isValidRole(parts[i]) {
			roleIdx = i
			role = parts[i]
			break
		}
	}

	if roleIdx == -1 {
		return fmt.Errorf("invalid agent format: no valid role found in %q (valid roles: %s)", id, strings.Join(ValidAgentRoles, ", "))
	}

	// Extract rig (everything before role) and name (everything after role)
	rig := strings.Join(parts[:roleIdx], "-")
	name := strings.Join(parts[roleIdx+1:], "-")

	// Validate based on role type
	if isTownLevelRole(role) {
		if rig != "" || name != "" {
			return fmt.Errorf("town-level agent %q cannot have rig/name suffixes (expected <prefix>-%s, got %q)", role, role, id)
		}
		return nil
	}

	if isRigLevelRole(role) {
		if rig == "" {
			return fmt.Errorf("agent role %q requires rig: <prefix>-<rig>-%s (got %q)", role, role, id)
		}
		if name != "" {
			return fmt.Errorf("agent role %q cannot have name suffix (expected <prefix>-<rig>-%s, got %q)", role, role, id)
		}
		return nil // Valid rig-level singleton agent
	}

	if isNamedRole(role) {
		if rig == "" {
			return fmt.Errorf("rig name cannot be empty in %q", id)
		}
		if name == "" {
			return fmt.Errorf("agent role %q requires name: <prefix>-<rig>-%s-<name> (got %q)", role, role, id)
		}
		return nil // Valid named agent
	}

	return fmt.Errorf("invalid agent ID format: %q", id)
}
