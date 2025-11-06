// Package merge implements 3-way merge for beads JSONL files.
//
// This code is vendored from https://github.com/neongreen/mono/tree/main/beads-merge
// Original author: Emily (@neongreen, https://github.com/neongreen)
//
// MIT License
// Copyright (c) 2025 Emily
// See ATTRIBUTION.md for full license text
package merge

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestMergeField(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		left  string
		right string
		want  string
	}{
		{
			name:  "no change",
			base:  "original",
			left:  "original",
			right: "original",
			want:  "original",
		},
		{
			name:  "only left changed",
			base:  "original",
			left:  "changed",
			right: "original",
			want:  "changed",
		},
		{
			name:  "only right changed",
			base:  "original",
			left:  "original",
			right: "changed",
			want:  "changed",
		},
		{
			name:  "both changed to same",
			base:  "original",
			left:  "changed",
			right: "changed",
			want:  "changed",
		},
		{
			name:  "both changed differently - prefer left",
			base:  "original",
			left:  "left-change",
			right: "right-change",
			want:  "left-change",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeField(tt.base, tt.left, tt.right)
			if got != tt.want {
				t.Errorf("mergeField() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMaxTime(t *testing.T) {
	t1 := time.Date(2025, 10, 16, 20, 51, 29, 0, time.UTC)
	t2 := time.Date(2025, 10, 16, 20, 51, 30, 0, time.UTC)

	tests := []struct {
		name string
		t1   time.Time
		t2   time.Time
		want time.Time
	}{
		{
			name: "t1 after t2",
			t1:   t2,
			t2:   t1,
			want: t2,
		},
		{
			name: "t2 after t1",
			t1:   t1,
			t2:   t2,
			want: t2,
		},
		{
			name: "equal times",
			t1:   t1,
			t2:   t1,
			want: t1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maxTime(tt.t1, tt.t2)
			if !got.Equal(tt.want) {
				t.Errorf("maxTime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeDependencies(t *testing.T) {
	left := []*types.Dependency{
		{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks"},
		{IssueID: "bd-1", DependsOnID: "bd-3", Type: "blocks"},
	}
	right := []*types.Dependency{
		{IssueID: "bd-1", DependsOnID: "bd-3", Type: "blocks"}, // duplicate
		{IssueID: "bd-1", DependsOnID: "bd-4", Type: "blocks"},
	}

	result := mergeDependencies(left, right)

	if len(result) != 3 {
		t.Errorf("mergeDependencies() returned %d deps, want 3", len(result))
	}

	// Check all expected deps are present
	seen := make(map[string]bool)
	for _, dep := range result {
		key := dep.DependsOnID
		seen[key] = true
	}

	expected := []string{"bd-2", "bd-3", "bd-4"}
	for _, exp := range expected {
		if !seen[exp] {
			t.Errorf("mergeDependencies() missing dependency on %s", exp)
		}
	}
}

func TestMergeLabels(t *testing.T) {
	left := []string{"bug", "p1", "frontend"}
	right := []string{"frontend", "urgent"} // frontend is duplicate

	result := mergeLabels(left, right)

	if len(result) != 4 {
		t.Errorf("mergeLabels() returned %d labels, want 4", len(result))
	}

	// Check all expected labels are present
	seen := make(map[string]bool)
	for _, label := range result {
		seen[label] = true
	}

	expected := []string{"bug", "p1", "frontend", "urgent"}
	for _, exp := range expected {
		if !seen[exp] {
			t.Errorf("mergeLabels() missing label %s", exp)
		}
	}
}

func TestMerge3Way_SimpleUpdate(t *testing.T) {
	now := time.Now()
	
	base := []*types.Issue{
		{
			ID:        "bd-1",
			Title:     "Original Title",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	// Left changes title
	left := []*types.Issue{
		{
			ID:        "bd-1",
			Title:     "Updated Title",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	// Right changes status
	right := []*types.Issue{
		{
			ID:        "bd-1",
			Title:     "Original Title",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	result, conflicts := Merge3Way(base, left, right)

	if len(conflicts) > 0 {
		t.Errorf("Merge3Way() produced unexpected conflicts: %v", conflicts)
	}

	if len(result) != 1 {
		t.Fatalf("Merge3Way() returned %d issues, want 1", len(result))
	}

	// Should merge both changes
	if result[0].Title != "Updated Title" {
		t.Errorf("Merge3Way() title = %v, want 'Updated Title'", result[0].Title)
	}
	if result[0].Status != types.StatusInProgress {
		t.Errorf("Merge3Way() status = %v, want 'in_progress'", result[0].Status)
	}
}

func TestMerge3Way_DeletionDetection(t *testing.T) {
	now := time.Now()
	
	base := []*types.Issue{
		{
			ID:        "bd-1",
			Title:     "To Be Deleted",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	// Left deletes the issue
	left := []*types.Issue{}

	// Right keeps it unchanged
	right := []*types.Issue{
		{
			ID:        "bd-1",
			Title:     "To Be Deleted",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	result, conflicts := Merge3Way(base, left, right)

	if len(conflicts) > 0 {
		t.Errorf("Merge3Way() produced unexpected conflicts: %v", conflicts)
	}

	// Deletion should be accepted (issue removed in left, unchanged in right)
	if len(result) != 0 {
		t.Errorf("Merge3Way() returned %d issues, want 0 (deletion accepted)", len(result))
	}
}

func TestMerge3Way_AddedInBoth(t *testing.T) {
	now := time.Now()
	
	base := []*types.Issue{}

	// Both add the same issue (identical)
	left := []*types.Issue{
		{
			ID:        "bd-2",
			Title:     "New Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeBug,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	right := []*types.Issue{
		{
			ID:        "bd-2",
			Title:     "New Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeBug,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	result, conflicts := Merge3Way(base, left, right)

	if len(conflicts) > 0 {
		t.Errorf("Merge3Way() produced unexpected conflicts: %v", conflicts)
	}

	if len(result) != 1 {
		t.Errorf("Merge3Way() returned %d issues, want 1", len(result))
	}
}
