package main

import (
	"testing"
)

func TestParseLabelArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		expectIDs   int
		expectLabel string
	}{
		{
			name:        "single ID single label",
			args:        []string{"bd-1", "bug"},
			expectIDs:   1,
			expectLabel: "bug",
		},
		{
			name:        "multiple IDs single label",
			args:        []string{"bd-1", "bd-2", "critical"},
			expectIDs:   2,
			expectLabel: "critical",
		},
		{
			name:        "three IDs one label",
			args:        []string{"bd-1", "bd-2", "bd-3", "bug"},
			expectIDs:   3,
			expectLabel: "bug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, label := parseLabelArgs(tt.args)

			if len(ids) != tt.expectIDs {
				t.Errorf("Expected %d IDs, got %d", tt.expectIDs, len(ids))
			}

			if label != tt.expectLabel {
				t.Errorf("Expected label %q, got %q", tt.expectLabel, label)
			}
		})
	}
}
