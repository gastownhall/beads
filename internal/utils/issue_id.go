package utils

import (
	"fmt"
	"strings"
)

// ExtractIssuePrefix extracts the prefix from an issue ID like "bd-123" -> "bd"
// Uses the last hyphen before an alphanumeric suffix:
//   - "beads-vscode-1" -> "beads-vscode" (numeric suffix)
//   - "web-app-a3f8e9" -> "web-app" (hash suffix)
//   - "my-cool-app-123" -> "my-cool-app" (numeric suffix)
//   - "hacker-news-test" -> "hacker-news" (alphanumeric suffix, GH#405)
//
// Only uses first hyphen when suffix contains non-alphanumeric characters,
// which indicates it's not an issue ID but something like a project name.
func ExtractIssuePrefix(issueID string) string {
	// Try last hyphen first (handles multi-part prefixes like "beads-vscode-1")
	lastIdx := strings.LastIndex(issueID, "-")
	if lastIdx <= 0 {
		return ""
	}

	suffix := issueID[lastIdx+1:]
	if len(suffix) == 0 {
		// Trailing hyphen like "bd-" - return prefix before the hyphen
		return issueID[:lastIdx]
	}

	// Extract the base part before any dot (handle "123.1.2" -> check "123")
	basePart := suffix
	if dotIdx := strings.Index(suffix, "."); dotIdx > 0 {
		basePart = suffix[:dotIdx]
	}

	// Check if basePart is alphanumeric (valid issue ID suffix)
	// Issue IDs are always alphanumeric: numeric (1, 23) or hash (a3f, xyz, test)
	isAlphanumeric := len(basePart) > 0
	for _, c := range basePart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			isAlphanumeric = false
			break
		}
	}

	// If suffix is alphanumeric, this is an issue ID - use last hyphen
	// This handles all issue ID formats including word-like hashes (GH#405)
	if isAlphanumeric {
		return issueID[:lastIdx]
	}

	// Suffix contains special characters - not a standard issue ID
	// Fall back to first hyphen for cases like project names with descriptions
	firstIdx := strings.Index(issueID, "-")
	if firstIdx <= 0 {
		return ""
	}
	return issueID[:firstIdx]
}

// isLikelyHash checks if a string looks like a hash ID suffix.
// Returns true for base36 strings of 3-8 characters (0-9, a-z).
//
// For 3-char suffixes: accepts all base36 (including all-letter like "bat", "dev").
// For 4+ char suffixes: requires at least one digit to distinguish from English words.
//
// Rationale (word collision probability):
//   - 3-char: 36³ = 46K hashes, ~1000 common words = ~2% (accept false positives)
//   - 4-char: 36⁴ = 1.6M hashes, ~3000 words = ~0.2% (digit requirement is safe)
//   - 5+ char: collision rate negligible
//
// Hash IDs in beads use adaptive length scaling from 3-8 characters.
func isLikelyHash(s string) bool {
	if len(s) < 3 || len(s) > 8 {
		return false
	}
	// 3-char suffixes get a free pass (word collision acceptable)
	// 4+ char suffixes require at least one digit
	hasDigit := len(s) == 3
	// Check if all characters are base36 (0-9, a-z)
	for _, c := range s {
		if c >= '0' && c <= '9' {
			hasDigit = true
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return hasDigit
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
