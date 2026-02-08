package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/importer"
	"github.com/steveyegge/beads/internal/utils"
)

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"0", true},
		{"123", true},
		{"999", true},
		{"abc", false},
		{"", true}, // empty string returns true (loop never runs)
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
		{"no-number", "no"}, // Has hyphen, suffix not numeric, first hyphen
		{"nonumber", ""},    // No hyphen
		{"", ""},
		// Multi-part non-numeric suffixes (bd-fasa regression tests)
		{"vc-baseline-test", "vc"},
		{"vc-92cl-gate-test", "vc"},
		{"bd-multi-part-id", "bd"},
		{"prefix-a-b-c-d", "prefix"},
		// Multi-part prefixes with numeric suffixes
		{"beads-vscode-1", "beads-vscode"},
		{"alpha-beta-123", "alpha-beta"},
		{"my-project-42", "my-project"},
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

	result := importer.GetPrefixList(prefixMap)

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
