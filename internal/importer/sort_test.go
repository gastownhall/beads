package importer

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestGetHierarchyDepth(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		expected int
	}{
		{"top-level", "bd-abc123", 0},
		{"one level", "bd-abc123.1", 1},
		{"two levels", "bd-abc123.1.2", 2},
		{"three levels", "bd-abc123.1.2.3", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetHierarchyDepth(tt.id)
			if got != tt.expected {
				t.Errorf("GetHierarchyDepth(%q) = %d, want %d", tt.id, got, tt.expected)
			}
		})
	}
}

func TestSortByDepth(t *testing.T) {
	tests := []struct {
		name     string
		input    []*types.Issue
		expected []string
	}{
		{
			name: "already sorted",
			input: []*types.Issue{
				{ID: "bd-abc"},
				{ID: "bd-abc.1"},
				{ID: "bd-abc.2"},
			},
			expected: []string{"bd-abc", "bd-abc.1", "bd-abc.2"},
		},
		{
			name: "child before parent",
			input: []*types.Issue{
				{ID: "bd-abc.1"},
				{ID: "bd-abc"},
			},
			expected: []string{"bd-abc", "bd-abc.1"},
		},
		{
			name: "complex hierarchy",
			input: []*types.Issue{
				{ID: "bd-abc.1.2"},
				{ID: "bd-xyz"},
				{ID: "bd-abc"},
				{ID: "bd-abc.1"},
			},
			expected: []string{"bd-abc", "bd-xyz", "bd-abc.1", "bd-abc.1.2"},
		},
		{
			name: "stable sort same depth",
			input: []*types.Issue{
				{ID: "bd-zzz.1"},
				{ID: "bd-aaa.1"},
				{ID: "bd-mmm.1"},
			},
			expected: []string{"bd-aaa.1", "bd-mmm.1", "bd-zzz.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SortByDepth(tt.input)
			for i, issue := range tt.input {
				if issue.ID != tt.expected[i] {
					t.Errorf("Position %d: got %q, want %q", i, issue.ID, tt.expected[i])
				}
			}
		})
	}
}

func TestGroupByDepth(t *testing.T) {
	input := []*types.Issue{
		{ID: "bd-abc"},
		{ID: "bd-xyz"},
		{ID: "bd-abc.1"},
		{ID: "bd-abc.2"},
		{ID: "bd-abc.1.1"},
	}

	groups := GroupByDepth(input)

	if len(groups[0]) != 2 {
		t.Errorf("Depth 0: got %d issues, want 2", len(groups[0]))
	}
	if len(groups[1]) != 2 {
		t.Errorf("Depth 1: got %d issues, want 2", len(groups[1]))
	}
	if len(groups[2]) != 1 {
		t.Errorf("Depth 2: got %d issues, want 1", len(groups[2]))
	}

	if groups[0][0].ID != "bd-abc" && groups[0][1].ID != "bd-abc" {
		t.Error("bd-abc not found in depth 0")
	}
	if groups[0][0].ID != "bd-xyz" && groups[0][1].ID != "bd-xyz" {
		t.Error("bd-xyz not found in depth 0")
	}
	if groups[2][0].ID != "bd-abc.1.1" {
		t.Errorf("Depth 2: got %q, want bd-abc.1.1", groups[2][0].ID)
	}
}
