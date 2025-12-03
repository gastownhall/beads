package utils

import (
	"testing"
)

// TestExtractIssuePrefixAllLetterHash tests issue #446:
// Base36 hashes can be all-letters (no digits), but isLikelyHash requires
// at least one digit to distinguish from English words.
// This causes all-letter hashes like "bat", "dev", "oil" to be rejected,
// falling back to first-hyphen extraction and giving wrong prefix.
//
// Hash length scales with birthday algorithm: 3, 4, 5, 6, 7, 8 chars.
// All lengths can be all-letters by chance.
//
// See: https://github.com/steveyegge/beads/issues/446
func TestExtractIssuePrefixAllLetterHash(t *testing.T) {
	// Only 3-char all-letter suffixes should be accepted as hashes.
	// 4+ char all-letter suffixes still require a digit.
	allLetterHashes := []struct {
		issueID  string
		expected string
	}{
		// 3-char all-letter suffixes (actual IDs from xa-adapt) - SHOULD WORK
		{"xa-adt-bat", "xa-adt"},
		{"xa-adt-dev", "xa-adt"},
		{"xa-adt-fbi", "xa-adt"},
		{"xa-adt-oil", "xa-adt"},

		// 3-char with digits - already works
		{"xa-adt-r71", "xa-adt"},
		{"xa-adt-b4r", "xa-adt"},
		{"xa-adt-0lj", "xa-adt"},
	}

	for _, tc := range allLetterHashes {
		t.Run(tc.issueID, func(t *testing.T) {
			result := ExtractIssuePrefix(tc.issueID)
			if result != tc.expected {
				t.Errorf("ExtractIssuePrefix(%q) = %q; want %q", tc.issueID, result, tc.expected)
			}
		})
	}
}

// TestExtractIssuePrefixWordSuffix ensures 4+ char word suffixes still work
func TestExtractIssuePrefixWordSuffix(t *testing.T) {
	// These should use first-hyphen extraction (word suffixes, not hashes)
	wordSuffixes := []struct {
		issueID  string
		expected string
	}{
		{"vc-baseline-test", "vc"},     // 4-char "test" - word, not hash
		{"vc-baseline-hello", "vc"},    // 5-char word
		{"vc-some-feature", "vc"},      // multi-word suffix
	}

	for _, tc := range wordSuffixes {
		t.Run(tc.issueID, func(t *testing.T) {
			result := ExtractIssuePrefix(tc.issueID)
			if result != tc.expected {
				t.Errorf("ExtractIssuePrefix(%q) = %q; want %q", tc.issueID, result, tc.expected)
			}
		})
	}
}

// TestIsLikelyHashAllLetters verifies the root cause:
// isLikelyHash returns false for all-letter strings even though
// they are valid base36 hashes.
//
// Key insight: English word collision probability varies by length:
// - 3-char: 36^3 = 46K hashes, ~1000 common words = ~2% collision (TOO HIGH)
// - 4-char: 36^4 = 1.6M hashes, ~3000 common words = ~0.2% collision (acceptable)
// - 5+ char: collision rate negligible
//
// Proposed fix: accept all-letter for 3-char only, keep digit requirement for 4+.
func TestIsLikelyHashAllLetters(t *testing.T) {
	tests := []struct {
		suffix   string
		expected bool
		reason   string
	}{
		// With digits - should pass (and does)
		{"r71", true, "has digits"},
		{"b4r", true, "has digit"},
		{"0lj", true, "starts with digit"},
		{"a3f", true, "has digit"},
		{"a1b2", true, "4-char with digits"},
		{"test1", true, "5-char with digit"},

		// 3-char all letters - SHOULD pass (proposed fix)
		// English word collision is acceptable at 3 chars
		{"bat", true, "3-char base36 - accept all-letter"},
		{"dev", true, "3-char base36 - accept all-letter"},
		{"oil", true, "3-char base36 - accept all-letter"},
		{"fbi", true, "3-char base36 - accept all-letter"},
		{"abc", true, "3-char base36 - accept all-letter"},

		// 4+ char all letters - should FAIL (keep digit requirement)
		// Word collision is rare enough that digit requirement is safe
		{"test", false, "4-char all-letter - require digit"},
		{"abcd", false, "4-char all-letter - require digit"},
		{"hello", false, "5-char all-letter - require digit"},
		{"foobar", false, "6-char all-letter - require digit"},
		{"baseline", false, "8-char all-letter - require digit"},

		// Length bounds
		{"ab", false, "too short (2 chars)"},
		{"abcdefghi", false, "too long (9 chars)"},
	}

	for _, tc := range tests {
		t.Run(tc.suffix, func(t *testing.T) {
			result := isLikelyHash(tc.suffix)
			if result != tc.expected {
				t.Errorf("isLikelyHash(%q) = %v; want %v (%s)",
					tc.suffix, result, tc.expected, tc.reason)
			}
		})
	}
}

