package utils

import (
	"fmt"
	"strings"
)

// ExtractIssuePrefix extracts the prefix from an issue ID like "bd-123" -> "bd"
// Uses the last hyphen before a numeric suffix, so "beads-vscode-1" -> "beads-vscode"
// For non-numeric suffixes like "vc-baseline-test", returns the first segment "vc"
func ExtractIssuePrefix(issueID string) string {
	// Try last hyphen first (handles multi-part prefixes like "beads-vscode-1")
	lastIdx := strings.LastIndex(issueID, "-")
	if lastIdx <= 0 {
		return ""
	}

	suffix := issueID[lastIdx+1:]
	// Check if suffix is numeric (or starts with a number for hierarchical IDs like "bd-123.1")
	if len(suffix) > 0 {
		// Extract just the numeric part (handle "123.1.2" -> check "123")
		numPart := suffix
		if dotIdx := strings.Index(suffix, "."); dotIdx > 0 {
			numPart = suffix[:dotIdx]
		}
		var num int
		if _, err := fmt.Sscanf(numPart, "%d", &num); err == nil {
			// Suffix is numeric, use last hyphen
			return issueID[:lastIdx]
		}
	}

	// Suffix is not numeric (e.g., "vc-baseline-test"), fall back to first hyphen
	firstIdx := strings.Index(issueID, "-")
	if firstIdx <= 0 {
		return ""
	}
	return issueID[:firstIdx]
}

// ExtractIssueNumber extracts the number from an issue ID like "bd-123" -> 123
func ExtractIssueNumber(issueID string) int {
	idx := strings.LastIndex(issueID, "-")
	if idx < 0 || idx == len(issueID)-1 {
		return 0
	}
	var num int
	_, _ = fmt.Sscanf(issueID[idx+1:], "%d", &num)
	return num
}
