package doctor

import "testing"

func TestIsValidIdentifier(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"beads", true},
		{"beads_db", true},
		{"Beads123", true},
		{"_private", true},
		{"123start", false},     // Can't start with number
		{"", false},             // Empty string
		{"db-name", false},      // Hyphen not allowed
		{"db.name", false},      // Dot not allowed
		{"db name", false},      // Space not allowed
		{"db;drop", false},      // Semicolon not allowed
		{"db'inject", false},    // Quote not allowed
		{"beads_test_db", true}, // Multiple underscores ok
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isValidIdentifier(tt.input)
			if got != tt.want {
				t.Errorf("isValidIdentifier(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
