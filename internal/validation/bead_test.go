package validation

import (
	"testing"
)

func TestParsePriority(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		// Numeric format
		{"0", 0},
		{"1", 1},
		{"2", 2},
		{"3", 3},
		{"4", 4},
		
		// P-prefix format (uppercase)
		{"P0", 0},
		{"P1", 1},
		{"P2", 2},
		{"P3", 3},
		{"P4", 4},
		
		// P-prefix format (lowercase)
		{"p0", 0},
		{"p1", 1},
		{"p2", 2},
		
		// With whitespace
		{" 1 ", 1},
		{" P1 ", 1},
		
		// Invalid cases (returns -1)
		{"5", -1},      // Out of range
		{"-1", -1},     // Negative
		{"P5", -1},     // Out of range with prefix
		{"abc", -1},    // Not a number
		{"P", -1},      // Just the prefix
		{"PP1", -1},    // Double prefix
	}
	
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParsePriority(tt.input)
			if got != tt.expected {
				t.Errorf("ParsePriority(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}
