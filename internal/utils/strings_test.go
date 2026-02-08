package utils

import (
	"reflect"
	"testing"
)

func TestNormalizeLabels(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "empty slice",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "nil slice",
			input:    nil,
			expected: []string{},
		},
		{
			name:     "single label",
			input:    []string{"bug"},
			expected: []string{"bug"},
		},
		{
			name:     "multiple labels",
			input:    []string{"bug", "critical", "frontend"},
			expected: []string{"bug", "critical", "frontend"},
		},
		{
			name:     "labels with whitespace",
			input:    []string{"  bug  ", " critical", "frontend "},
			expected: []string{"bug", "critical", "frontend"},
		},
		{
			name:     "duplicate labels",
			input:    []string{"bug", "bug", "critical"},
			expected: []string{"bug", "critical"},
		},
		{
			name:     "duplicates after trimming",
			input:    []string{"bug", "  bug  ", " bug"},
			expected: []string{"bug"},
		},
		{
			name:     "empty strings",
			input:    []string{"bug", "", "critical"},
			expected: []string{"bug", "critical"},
		},
		{
			name:     "whitespace-only strings",
			input:    []string{"bug", "   ", "critical", "\t", "\n"},
			expected: []string{"bug", "critical"},
		},
		{
			name:     "preserves order",
			input:    []string{"zebra", "apple", "banana"},
			expected: []string{"zebra", "apple", "banana"},
		},
		{
			name:     "complex case with all issues",
			input:    []string{"  bug  ", "", "bug", "critical", "   ", "frontend", "critical", "  frontend  "},
			expected: []string{"bug", "critical", "frontend"},
		},
		{
			name:     "unicode labels",
			input:    []string{"🐛 bug", "  🐛 bug  ", "🚀 feature"},
			expected: []string{"🐛 bug", "🚀 feature"},
		},
		{
			name:     "case-sensitive",
			input:    []string{"Bug", "bug", "BUG"},
			expected: []string{"Bug", "bug", "BUG"},
		},
		{
			name:     "labels with internal spaces",
			input:    []string{"needs review", "  needs review  ", "in progress"},
			expected: []string{"needs review", "in progress"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeLabels(tt.input)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("NormalizeLabels(%v) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeLabels_PreservesCapacity(t *testing.T) {
	input := []string{"bug", "critical", "frontend"}
	result := NormalizeLabels(input)

	// Result should have reasonable capacity (not excessive allocation)
	if cap(result) > len(input)*2 {
		t.Errorf("NormalizeLabels capacity too large: got %d, input len %d", cap(result), len(input))
	}
}

func TestNormalizeIssueType(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "mr alias",
			input:    "mr",
			expected: "merge-request",
		},
		{
			name:     "MR uppercase",
			input:    "MR",
			expected: "merge-request",
		},
		{
			name:     "feat alias",
			input:    "feat",
			expected: "feature",
		},
		{
			name:     "FEAT uppercase",
			input:    "FEAT",
			expected: "feature",
		},
		{
			name:     "mol alias",
			input:    "mol",
			expected: "molecule",
		},
		{
			name:     "Mol mixed case",
			input:    "Mol",
			expected: "molecule",
		},
		{
			name:     "non-alias unchanged",
			input:    "bug",
			expected: "bug",
		},
		{
			name:     "full name unchanged",
			input:    "merge-request",
			expected: "merge-request",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "feature unchanged",
			input:    "feature",
			expected: "feature",
		},
		{
			name:     "task unchanged",
			input:    "task",
			expected: "task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeIssueType(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeIssueType(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
