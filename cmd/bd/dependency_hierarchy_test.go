package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestIsDisallowedHierarchicalDependency(t *testing.T) {
	tests := []struct {
		name    string
		fromID  string
		toID    string
		depType types.DependencyType
		want    bool
	}{
		{
			name:    "immediate parent-child allowed",
			fromID:  "bd-root.1.2",
			toID:    "bd-root.1",
			depType: types.DepParentChild,
			want:    false,
		},
		{
			name:    "immediate parent blocks rejected",
			fromID:  "bd-root.1.2",
			toID:    "bd-root.1",
			depType: types.DepBlocks,
			want:    true,
		},
		{
			name:    "grandparent parent-child rejected",
			fromID:  "bd-root.1.2",
			toID:    "bd-root",
			depType: types.DepParentChild,
			want:    true,
		},
		{
			name:    "unrelated target allowed",
			fromID:  "bd-root.1.2",
			toID:    "bd-other",
			depType: types.DepBlocks,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDisallowedHierarchicalDependency(tt.fromID, tt.toID, tt.depType)
			if got != tt.want {
				t.Fatalf("isDisallowedHierarchicalDependency(%q, %q, %q) = %v, want %v", tt.fromID, tt.toID, tt.depType, got, tt.want)
			}
		})
	}
}
