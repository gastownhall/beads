package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/utils"
)

func TestIsBoundary(t *testing.T) {
	tests := []struct {
		input    byte
		expected bool
	}{
		{' ', true},
		{'\t', true},
		{'\n', true},
		{'\r', true},
		{'-', false}, // hyphen is part of issue IDs
		{'_', true},
		{'(', true},
		{')', true},
		{'[', true},
		{']', true},
		{'{', true},
		{'}', true},
		{',', true},
		{'.', true},
		{':', true},
		{';', true},
		{'a', false}, // lowercase letters are part of issue IDs
		{'z', false},
		{'A', true},  // uppercase is a boundary
		{'Z', true},  // uppercase is a boundary
		{'0', false}, // digits are part of issue IDs
		{'9', false},
	}

	for _, tt := range tests {
		result := isBoundary(tt.input)
		if result != tt.expected {
			t.Errorf("isBoundary(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"0", true},
		{"123", true},
		{"999", true},
		{"abc", false},
		{"", true},    // empty string returns true (loop never runs)
		{"12a", false},
	}

	for _, tt := range tests {
		result := isNumeric(tt.input)
		if result != tt.expected {
			t.Errorf("isNumeric(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

func TestGetWorktreeGitDir(_ *testing.T) {
	gitDir := getWorktreeGitDir()
	// Just verify it doesn't panic and returns a string
	_ = gitDir
}

func TestExtractPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"bd-123", "bd"},
		{"custom-1", "custom"},
		{"TEST-999", "TEST"},
		{"no-number", "no"},     // Has hyphen, so "no" is prefix
		{"nonumber", ""},        // No hyphen
		{"", ""},
	}

	for _, tt := range tests {
		result := utils.ExtractIssuePrefix(tt.input)
		if result != tt.expected {
			t.Errorf("ExtractIssuePrefix(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestGetPrefixList(t *testing.T) {
	prefixMap := map[string]int{
		"bd":     5,
		"custom": 3,
		"test":   1,
	}
	
	result := getPrefixList(prefixMap)
	
	// Should have 3 entries
	if len(result) != 3 {
		t.Errorf("Expected 3 entries, got %d", len(result))
	}
	
	// Function returns formatted strings like "bd- (5 issues)"
	// Just check we got sensible output
	for _, entry := range result {
		if entry == "" {
			t.Error("Got empty entry")
		}
	}
}
