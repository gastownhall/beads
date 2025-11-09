package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestFormatDependencyType(t *testing.T) {
	tests := []struct {
		name     string
		depType  types.DependencyType
		expected string
	}{
		{"blocks", types.DepBlocks, "blocks"},
		{"related", types.DepRelated, "related"},
		{"parent-child", types.DepParentChild, "parent-child"},
		{"discovered-from", types.DepDiscoveredFrom, "discovered-from"},
		{"unknown", types.DependencyType("unknown"), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDependencyType(tt.depType)
			if result != tt.expected {
				t.Errorf("formatDependencyType(%v) = %v, want %v", tt.depType, result, tt.expected)
			}
		})
	}
}
