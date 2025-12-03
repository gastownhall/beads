package utils

import (
	"fmt"
	"strings"
)

// ExtractIssuePrefix extracts the prefix from an issue ID like "bd-123" -> "bd"
// Uses the last hyphen before a numeric or hash suffix:
//   - "beads-vscode-1" -> "beads-vscode" (numeric suffix)
//   - "web-app-a3f8e9" -> "web-app" (hash suffix)
//   - "my-cool-app-123" -> "my-cool-app" (numeric suffix)
// Only uses first hyphen for non-ID suffixes like "vc-baseline-test" -> "vc"
func ExtractIssuePrefix(issueID string) string {
	// Try last hyphen first (handles multi-part prefixes like "beads-vscode-1")
	lastIdx := strings.LastIndex(issueID, "-")
	if lastIdx <= 0 {
		return ""
	}

	suffix := issueID[lastIdx+1:]
	// Check if suffix looks like an issue ID component (numeric or hash-like)
	if len(suffix) > 0 {
		// Extract just the numeric part (handle "123.1.2" -> check "123")
		numPart := suffix
		if dotIdx := strings.Index(suffix, "."); dotIdx > 0 {
			numPart = suffix[:dotIdx]
		}

		// Check if it's numeric
		var num int
		if _, err := fmt.Sscanf(numPart, "%d", &num); err == nil {
			// Suffix is numeric, use last hyphen
			return issueID[:lastIdx]
		}

		// Check if it looks like a hash (hexadecimal characters, 4+ chars)
		// Hash IDs are typically 4-8 hex characters (e.g., "a3f8e9", "1a2b")
		if isLikelyHash(numPart) {
			// Suffix looks like a hash, use last hyphen
			return issueID[:lastIdx]
		}
	}

	// Suffix is not numeric or hash-like (e.g., "vc-baseline-test"), fall back to first hyphen
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
