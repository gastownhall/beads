package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMergeField tests the basic field merging logic
func TestMergeField(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		left     string
		right    string
		expected string
	}{
		{
			name:     "no changes",
			base:     "original",
			left:     "original",
			right:    "original",
			expected: "original",
		},
		{
			name:     "left changed",
			base:     "original",
			left:     "left-changed",
			right:    "original",
			expected: "left-changed",
		},
		{
			name:     "right changed",
			base:     "original",
			left:     "original",
			right:    "right-changed",
			expected: "right-changed",
		},
		{
			name:     "both changed to same value",
			base:     "original",
			left:     "both-changed",
			right:    "both-changed",
			expected: "both-changed",
		},
		{
			name:     "both changed to different values - prefers left",
			base:     "original",
			left:     "left-value",
			right:    "right-value",
			expected: "left-value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeField(tt.base, tt.left, tt.right)
			if result != tt.expected {
				t.Errorf("mergeField() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestMergeDependencies tests dependency union and deduplication
func TestMergeDependencies(t *testing.T) {
	tests := []struct {
		name     string
		left     []Dependency
		right    []Dependency
		expected []Dependency
	}{
		{
			name:     "empty both sides",
			left:     []Dependency{},
			right:    []Dependency{},
			expected: []Dependency{},
		},
		{
			name: "only left has deps",
			left: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			right: []Dependency{},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		},
		{
			name: "only right has deps",
			left: []Dependency{},
			right: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		},
		{
			name: "union of different deps",
			left: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			right: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		},
		{
			name: "deduplication of identical deps",
			left: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			right: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-02T00:00:00Z"}, // Different timestamp but same logical dep
			},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		},
		{
			name: "multiple deps with dedup",
			left: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			right: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-02T00:00:00Z"},
				{IssueID: "bd-1", DependsOnID: "bd-4", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"},
				{IssueID: "bd-1", DependsOnID: "bd-4", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeDependencies(tt.left, tt.right)
			if len(result) != len(tt.expected) {
				t.Errorf("mergeDependencies() returned %d deps, want %d", len(result), len(tt.expected))
				return
			}
			// Check each expected dep is present
			for _, exp := range tt.expected {
				found := false
				for _, res := range result {
					if res.IssueID == exp.IssueID &&
						res.DependsOnID == exp.DependsOnID &&
						res.Type == exp.Type {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected dependency %+v not found in result", exp)
				}
			}
		})
	}
}

// TestMaxTime tests timestamp merging (max wins)
func TestMaxTime(t *testing.T) {
	tests := []struct {
		name     string
		t1       string
		t2       string
		expected string
	}{
		{
			name:     "both empty",
			t1:       "",
			t2:       "",
			expected: "",
		},
		{
			name:     "t1 empty",
			t1:       "",
			t2:       "2024-01-02T00:00:00Z",
			expected: "2024-01-02T00:00:00Z",
		},
		{
			name:     "t2 empty",
			t1:       "2024-01-01T00:00:00Z",
			t2:       "",
			expected: "2024-01-01T00:00:00Z",
		},
		{
			name:     "t1 newer",
			t1:       "2024-01-02T00:00:00Z",
			t2:       "2024-01-01T00:00:00Z",
			expected: "2024-01-02T00:00:00Z",
		},
		{
			name:     "t2 newer",
			t1:       "2024-01-01T00:00:00Z",
			t2:       "2024-01-02T00:00:00Z",
			expected: "2024-01-02T00:00:00Z",
		},
		{
			name:     "identical timestamps",
			t1:       "2024-01-01T00:00:00Z",
			t2:       "2024-01-01T00:00:00Z",
			expected: "2024-01-01T00:00:00Z",
		},
		{
			name:     "with fractional seconds (RFC3339Nano)",
			t1:       "2024-01-01T00:00:00.123456Z",
			t2:       "2024-01-01T00:00:00.123455Z",
			expected: "2024-01-01T00:00:00.123456Z",
		},
		{
			name:     "invalid timestamps - returns t2 as fallback",
			t1:       "invalid",
			t2:       "also-invalid",
			expected: "also-invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := maxTime(tt.t1, tt.t2)
			if result != tt.expected {
				t.Errorf("maxTime() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestMerge3Way_SimpleUpdates tests simple field update scenarios
func TestMerge3Way_SimpleUpdates(t *testing.T) {
	base := []Issue{
		{
			ID:        "bd-abc123",
			Title:     "Original title",
			Status:    "open",
			Priority:  2,
			CreatedAt: "2024-01-01T00:00:00Z",
			UpdatedAt: "2024-01-01T00:00:00Z",
			CreatedBy: "user1",
			RawLine:   `{"id":"bd-abc123","title":"Original title","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
		},
	}

	t.Run("left updates title", func(t *testing.T) {
		left := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Updated title",
				Status:    "open",
				Priority:  2,
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-02T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Updated title","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := base

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Title != "Updated title" {
			t.Errorf("expected title 'Updated title', got %q", result[0].Title)
		}
	})

	t.Run("right updates status", func(t *testing.T) {
		left := base
		right := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Original title",
				Status:    "in_progress",
				Priority:  2,
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-02T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Original title","status":"in_progress","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Status != "in_progress" {
			t.Errorf("expected status 'in_progress', got %q", result[0].Status)
		}
	})

	t.Run("both update different fields", func(t *testing.T) {
		left := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Updated title",
				Status:    "open",
				Priority:  2,
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-02T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Updated title","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Original title",
				Status:    "in_progress",
				Priority:  2,
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-02T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Original title","status":"in_progress","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Title != "Updated title" {
			t.Errorf("expected title 'Updated title', got %q", result[0].Title)
		}
		if result[0].Status != "in_progress" {
			t.Errorf("expected status 'in_progress', got %q", result[0].Status)
		}
	})
}

// TestMerge3Way_Conflicts tests conflict detection
func TestMerge3Way_Conflicts(t *testing.T) {
	t.Run("conflicting title changes", func(t *testing.T) {
		base := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Original",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Original","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		left := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Left version",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Left version","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Right version",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Right version","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) == 0 {
			t.Error("expected conflict for divergent title changes")
		}
		if len(result) != 0 {
			t.Errorf("expected no merged issues with conflict, got %d", len(result))
		}
		if len(conflicts) > 0 {
			if conflicts[0] == "" {
				t.Error("conflict marker should not be empty")
			}
		}
	})

	t.Run("conflicting priority changes", func(t *testing.T) {
		base := []Issue{
			{
				ID:        "bd-abc123",
				Priority:  2,
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","priority":2,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		left := []Issue{
			{
				ID:        "bd-abc123",
				Priority:  0,
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","priority":0,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{
			{
				ID:        "bd-abc123",
				Priority:  1,
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","priority":1,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) == 0 {
			t.Error("expected conflict for divergent priority changes")
		}
		if len(result) != 0 {
			t.Errorf("expected no merged issues with conflict, got %d", len(result))
		}
	})
}

// TestMerge3Way_Deletions tests deletion detection scenarios
func TestMerge3Way_Deletions(t *testing.T) {
	t.Run("deleted in left, unchanged in right", func(t *testing.T) {
		base := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Will be deleted",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Will be deleted","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		left := []Issue{} // Deleted in left
		right := base     // Unchanged in right

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 0 {
			t.Errorf("expected deletion to be accepted, got %d issues", len(result))
		}
	})

	t.Run("deleted in right, unchanged in left", func(t *testing.T) {
		base := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Will be deleted",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Will be deleted","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		left := base     // Unchanged in left
		right := []Issue{} // Deleted in right

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 0 {
			t.Errorf("expected deletion to be accepted, got %d issues", len(result))
		}
	})

	t.Run("deleted in left, modified in right - conflict", func(t *testing.T) {
		base := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Original",
				Status:    "open",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Original","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		left := []Issue{} // Deleted in left
		right := []Issue{ // Modified in right
			{
				ID:        "bd-abc123",
				Title:     "Modified",
				Status:    "in_progress",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Modified","status":"in_progress","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) == 0 {
			t.Error("expected conflict for delete vs modify")
		}
		if len(result) != 0 {
			t.Errorf("expected no merged issues with conflict, got %d", len(result))
		}
	})

	t.Run("deleted in right, modified in left - conflict", func(t *testing.T) {
		base := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Original",
				Status:    "open",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Original","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		left := []Issue{ // Modified in left
			{
				ID:        "bd-abc123",
				Title:     "Modified",
				Status:    "in_progress",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Modified","status":"in_progress","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{} // Deleted in right

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) == 0 {
			t.Error("expected conflict for modify vs delete")
		}
		if len(result) != 0 {
			t.Errorf("expected no merged issues with conflict, got %d", len(result))
		}
	})
}

// TestMerge3Way_Additions tests issue addition scenarios
func TestMerge3Way_Additions(t *testing.T) {
	t.Run("added only in left", func(t *testing.T) {
		base := []Issue{}
		left := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "New issue",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"New issue","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Title != "New issue" {
			t.Errorf("expected title 'New issue', got %q", result[0].Title)
		}
	})

	t.Run("added only in right", func(t *testing.T) {
		base := []Issue{}
		left := []Issue{}
		right := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "New issue",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"New issue","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Title != "New issue" {
			t.Errorf("expected title 'New issue', got %q", result[0].Title)
		}
	})

	t.Run("added in both with identical content", func(t *testing.T) {
		base := []Issue{}
		issueData := Issue{
			ID:        "bd-abc123",
			Title:     "New issue",
			Status:    "open",
			Priority:  2,
			CreatedAt: "2024-01-01T00:00:00Z",
			CreatedBy: "user1",
			RawLine:   `{"id":"bd-abc123","title":"New issue","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
		}
		left := []Issue{issueData}
		right := []Issue{issueData}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
	})

	t.Run("added in both with different content - conflict", func(t *testing.T) {
		base := []Issue{}
		left := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Left version",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Left version","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Right version",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Right version","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) == 0 {
			t.Error("expected conflict for different additions")
		}
		if len(result) != 0 {
			t.Errorf("expected no merged issues with conflict, got %d", len(result))
		}
	})
}

// TestMerge3Way_ResurrectionPrevention tests bd-hv01 regression
func TestMerge3Way_ResurrectionPrevention(t *testing.T) {
	t.Run("bd-hv01 regression: closed issue not resurrected", func(t *testing.T) {
		// Base: issue is open
		base := []Issue{
			{
				ID:        "bd-hv01",
				Title:     "Test issue",
				Status:    "open",
				ClosedAt:  "",
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-hv01","title":"Test issue","status":"open","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		// Left: issue is closed (newer)
		left := []Issue{
			{
				ID:        "bd-hv01",
				Title:     "Test issue",
				Status:    "closed",
				ClosedAt:  "2024-01-02T00:00:00Z",
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-02T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-hv01","title":"Test issue","status":"closed","closed_at":"2024-01-02T00:00:00Z","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`,
			},
		}
		// Right: issue is still open (stale)
		right := base

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		// Issue should remain closed (left's version)
		if result[0].Status != "closed" {
			t.Errorf("expected status 'closed', got %q - issue was resurrected!", result[0].Status)
		}
		if result[0].ClosedAt == "" {
			t.Error("expected closed_at to be set, got empty string")
		}
		// UpdatedAt should be the max (left's newer timestamp)
		if result[0].UpdatedAt != "2024-01-02T00:00:00Z" {
			t.Errorf("expected updated_at '2024-01-02T00:00:00Z', got %q", result[0].UpdatedAt)
		}
	})
}

// TestMerge3Way_Integration tests full merge scenarios with file I/O
func TestMerge3Way_Integration(t *testing.T) {
	t.Run("full merge workflow", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create test files
		baseFile := filepath.Join(tmpDir, "base.jsonl")
		leftFile := filepath.Join(tmpDir, "left.jsonl")
		rightFile := filepath.Join(tmpDir, "right.jsonl")
		outputFile := filepath.Join(tmpDir, "output.jsonl")

		// Base: two issues
		baseData := `{"id":"bd-1","title":"Issue 1","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}
{"id":"bd-2","title":"Issue 2","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}
`
		if err := os.WriteFile(baseFile, []byte(baseData), 0644); err != nil {
			t.Fatalf("failed to write base file: %v", err)
		}

		// Left: update bd-1 title, add bd-3
		leftData := `{"id":"bd-1","title":"Updated Issue 1","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}
{"id":"bd-2","title":"Issue 2","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}
{"id":"bd-3","title":"New Issue 3","status":"open","priority":1,"created_at":"2024-01-02T00:00:00Z","created_by":"user1"}
`
		if err := os.WriteFile(leftFile, []byte(leftData), 0644); err != nil {
			t.Fatalf("failed to write left file: %v", err)
		}

		// Right: update bd-2 status, add bd-4
		rightData := `{"id":"bd-1","title":"Issue 1","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}
{"id":"bd-2","title":"Issue 2","status":"in_progress","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}
{"id":"bd-4","title":"New Issue 4","status":"open","priority":3,"created_at":"2024-01-02T00:00:00Z","created_by":"user1"}
`
		if err := os.WriteFile(rightFile, []byte(rightData), 0644); err != nil {
			t.Fatalf("failed to write right file: %v", err)
		}

		// Perform merge
		err := Merge3Way(outputFile, baseFile, leftFile, rightFile, false)
		if err != nil {
			t.Fatalf("merge failed: %v", err)
		}

		// Read result
		content, err := os.ReadFile(outputFile)
		if err != nil {
			t.Fatalf("failed to read output file: %v", err)
		}

		// Parse result
		var results []Issue
		for _, line := range splitLines(string(content)) {
			if line == "" {
				continue
			}
			var issue Issue
			if err := json.Unmarshal([]byte(line), &issue); err != nil {
				t.Fatalf("failed to parse output line: %v", err)
			}
			results = append(results, issue)
		}

		// Should have 4 issues: bd-1 (updated), bd-2 (updated), bd-3 (new), bd-4 (new)
		if len(results) != 4 {
			t.Fatalf("expected 4 issues, got %d", len(results))
		}

		// Verify bd-1 has updated title from left
		found1 := false
		for _, issue := range results {
			if issue.ID == "bd-1" {
				found1 = true
				if issue.Title != "Updated Issue 1" {
					t.Errorf("bd-1 title: expected 'Updated Issue 1', got %q", issue.Title)
				}
			}
		}
		if !found1 {
			t.Error("bd-1 not found in results")
		}

		// Verify bd-2 has updated status from right
		found2 := false
		for _, issue := range results {
			if issue.ID == "bd-2" {
				found2 = true
				if issue.Status != "in_progress" {
					t.Errorf("bd-2 status: expected 'in_progress', got %q", issue.Status)
				}
			}
		}
		if !found2 {
			t.Error("bd-2 not found in results")
		}
	})
}
