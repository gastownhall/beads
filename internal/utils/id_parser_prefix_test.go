package utils

import "testing"

// TestParseIssueID covers the unexported prefix normalizer, so it lives in
// package utils. It is split out from the store-backed resolution tests
// because those import internal/storage/dolt, which reaches this package
// again through internal/workapi — an import cycle for an in-package test.
// The store-backed half is an external test package for that reason.

func TestParseIssueID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		prefix   string
		expected string
	}{
		{
			name:     "already has prefix",
			input:    "bd-a3f8e9",
			prefix:   "bd-",
			expected: "bd-a3f8e9",
		},
		{
			name:     "missing prefix",
			input:    "a3f8e9",
			prefix:   "bd-",
			expected: "bd-a3f8e9",
		},
		{
			name:     "hierarchical with prefix",
			input:    "bd-a3f8e9.1.2",
			prefix:   "bd-",
			expected: "bd-a3f8e9.1.2",
		},
		{
			name:     "hierarchical without prefix",
			input:    "a3f8e9.1.2",
			prefix:   "bd-",
			expected: "bd-a3f8e9.1.2",
		},
		{
			name:     "custom prefix with ID",
			input:    "ticket-123",
			prefix:   "ticket-",
			expected: "ticket-123",
		},
		{
			name:     "custom prefix without ID",
			input:    "123",
			prefix:   "ticket-",
			expected: "ticket-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseIssueID(tt.input, tt.prefix)
			if result != tt.expected {
				t.Errorf("parseIssueID(%q, %q) = %q; want %q", tt.input, tt.prefix, result, tt.expected)
			}
		})
	}
}

func TestLooksLikePrefixedID(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"aap-4ar", true},
		{"bd-abc123", true},
		{"hq-xyz", true},
		{"cr-99", true},
		{"myproj-task1", true},
		{"a-b", true},        // minimal valid prefix
		{"abc12345-x", true}, // 8-char prefix (max)

		// Invalid cases
		{"abc", false},         // no hyphen
		{"", false},            // empty
		{"-abc", false},        // hyphen at start
		{"ABC-123", false},     // uppercase
		{"abcdefghi-x", false}, // prefix too long (9 chars)
		{"abc-", false},        // empty suffix
		{"abc--def", false},    // suffix starts with hyphen
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := looksLikePrefixedID(tt.input)
			if result != tt.expected {
				t.Errorf("looksLikePrefixedID(%q) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}
}
