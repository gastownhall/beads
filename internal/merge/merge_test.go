package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMergeStatus tests the status merging logic with special rules
func TestMergeStatus(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		left     string
		right    string
		expected string
	}{
		{
			name:     "no changes",
			base:     "open",
			left:     "open",
			right:    "open",
			expected: "open",
		},
		{
			name:     "left closed, right open - closed wins",
			base:     "open",
			left:     "closed",
			right:    "open",
			expected: "closed",
		},
		{
			name:     "left open, right closed - closed wins",
			base:     "open",
			left:     "open",
			right:    "closed",
			expected: "closed",
		},
		{
			name:     "both closed",
			base:     "open",
			left:     "closed",
			right:    "closed",
			expected: "closed",
		},
		{
			name:     "base closed, left open, right open - open (standard merge)",
			base:     "closed",
			left:     "open",
			right:    "open",
			expected: "open",
		},
		{
			name:     "base closed, left open, right closed - closed wins",
			base:     "closed",
			left:     "open",
			right:    "closed",
			expected: "closed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeStatus(tt.base, tt.left, tt.right)
			if result != tt.expected {
				t.Errorf("mergeStatus(%q, %q, %q) = %q, want %q",
					tt.base, tt.left, tt.right, result, tt.expected)
			}
		})
	}
}

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

// TestIsTimeAfter tests timestamp comparison including error handling
func TestIsTimeAfter(t *testing.T) {
	tests := []struct {
		name     string
		t1       string
		t2       string
		expected bool
	}{
		{
			name:     "both empty - prefer left",
			t1:       "",
			t2:       "",
			expected: false,
		},
		{
			name:     "t1 empty - t2 wins",
			t1:       "",
			t2:       "2024-01-02T00:00:00Z",
			expected: false,
		},
		{
			name:     "t2 empty - t1 wins",
			t1:       "2024-01-01T00:00:00Z",
			t2:       "",
			expected: true,
		},
		{
			name:     "t1 newer",
			t1:       "2024-01-02T00:00:00Z",
			t2:       "2024-01-01T00:00:00Z",
			expected: true,
		},
		{
			name:     "t2 newer",
			t1:       "2024-01-01T00:00:00Z",
			t2:       "2024-01-02T00:00:00Z",
			expected: false,
		},
		{
			name:     "identical timestamps - right wins (false)",
			t1:       "2024-01-01T00:00:00Z",
			t2:       "2024-01-01T00:00:00Z",
			expected: false,
		},
		{
			name:     "t1 invalid, t2 valid - t2 wins",
			t1:       "not-a-timestamp",
			t2:       "2024-01-01T00:00:00Z",
			expected: false,
		},
		{
			name:     "t1 valid, t2 invalid - t1 wins",
			t1:       "2024-01-01T00:00:00Z",
			t2:       "not-a-timestamp",
			expected: true,
		},
		{
			name:     "both invalid - prefer left",
			t1:       "invalid1",
			t2:       "invalid2",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTimeAfter(tt.t1, tt.t2)
			if result != tt.expected {
				t.Errorf("isTimeAfter(%q, %q) = %v, want %v", tt.t1, tt.t2, result, tt.expected)
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

// TestMerge3Way_AutoResolve tests auto-resolution of conflicts
func TestMerge3Way_AutoResolve(t *testing.T) {
	t.Run("conflicting title changes - latest updated_at wins", func(t *testing.T) {
		base := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Original",
				UpdatedAt: "2024-01-01T00:00:00Z",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Original","updated_at":"2024-01-01T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		left := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Left version",
				UpdatedAt: "2024-01-02T00:00:00Z", // Older
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Left version","updated_at":"2024-01-02T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Right version",
				UpdatedAt: "2024-01-03T00:00:00Z", // Newer - this should win
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Right version","updated_at":"2024-01-03T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts with auto-resolution, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		// Right has newer updated_at, so right's title wins
		if result[0].Title != "Right version" {
			t.Errorf("expected title 'Right version' (newer updated_at), got %q", result[0].Title)
		}
	})

	t.Run("conflicting priority changes - higher priority wins (lower number)", func(t *testing.T) {
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
				Priority:  3, // Lower priority (higher number)
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","priority":3,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{
			{
				ID:        "bd-abc123",
				Priority:  1, // Higher priority (lower number) - this should win
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","priority":1,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts with auto-resolution, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		// Lower priority number wins
		if result[0].Priority != 1 {
			t.Errorf("expected priority 1 (higher priority), got %d", result[0].Priority)
		}
	})

	t.Run("conflicting notes - concatenated", func(t *testing.T) {
		base := []Issue{
			{
				ID:        "bd-abc123",
				Notes:     "Original notes",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","notes":"Original notes","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		left := []Issue{
			{
				ID:        "bd-abc123",
				Notes:     "Left notes",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","notes":"Left notes","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{
			{
				ID:        "bd-abc123",
				Notes:     "Right notes",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","notes":"Right notes","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts with auto-resolution, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		// Notes should be concatenated
		expectedNotes := "Left notes\n\n---\n\nRight notes"
		if result[0].Notes != expectedNotes {
			t.Errorf("expected notes %q, got %q", expectedNotes, result[0].Notes)
		}
	})

	t.Run("conflicting issue_type - local (left) wins", func(t *testing.T) {
		base := []Issue{
			{
				ID:        "bd-abc123",
				IssueType: "task",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","issue_type":"task","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		left := []Issue{
			{
				ID:        "bd-abc123",
				IssueType: "bug", // Local change - should win
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","issue_type":"bug","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{
			{
				ID:        "bd-abc123",
				IssueType: "feature",
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","issue_type":"feature","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts with auto-resolution, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		// Local (left) wins for issue_type
		if result[0].IssueType != "bug" {
			t.Errorf("expected issue_type 'bug' (local wins), got %q", result[0].IssueType)
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

	t.Run("deleted in left, modified in right - deletion wins", func(t *testing.T) {
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
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts, got %d", len(conflicts))
		}
		if len(result) != 0 {
			t.Errorf("expected deletion to win (0 results), got %d", len(result))
		}
	})

	t.Run("deleted in right, modified in left - deletion wins", func(t *testing.T) {
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
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts, got %d", len(conflicts))
		}
		if len(result) != 0 {
			t.Errorf("expected deletion to win (0 results), got %d", len(result))
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

	t.Run("added in both with different content - auto-resolved", func(t *testing.T) {
		base := []Issue{}
		left := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Left version",
				UpdatedAt: "2024-01-02T00:00:00Z", // Older
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Left version","updated_at":"2024-01-02T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}
		right := []Issue{
			{
				ID:        "bd-abc123",
				Title:     "Right version",
				UpdatedAt: "2024-01-03T00:00:00Z", // Newer - should win
				CreatedAt: "2024-01-01T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-abc123","title":"Right version","updated_at":"2024-01-03T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts with auto-resolution, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		// Right has newer updated_at, so right's title wins
		if result[0].Title != "Right version" {
			t.Errorf("expected title 'Right version' (newer updated_at), got %q", result[0].Title)
		}
	})
}

// TestMerge3Way_ResurrectionPrevention tests bd-hv01 regression
func TestMerge3Way_ResurrectionPrevention(t *testing.T) {
	t.Run("bd-pq5k: no invalid state (status=open with closed_at)", func(t *testing.T) {
		// Simulate the broken merge case that was creating invalid data
		// Base: issue is closed
		base := []Issue{
			{
				ID:        "bd-test",
				Title:     "Test issue",
				Status:    "closed",
				ClosedAt:  "2024-01-02T00:00:00Z",
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-02T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-test","title":"Test issue","status":"closed","closed_at":"2024-01-02T00:00:00Z","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`,
			},
		}
		// Left: still closed with closed_at
		left := base
		// Right: somehow got reopened but WITHOUT removing closed_at (the bug scenario)
		right := []Issue{
			{
				ID:        "bd-test",
				Title:     "Test issue",
				Status:    "open", // reopened
				ClosedAt:  "",     // correctly removed
				CreatedAt: "2024-01-01T00:00:00Z",
				UpdatedAt: "2024-01-03T00:00:00Z",
				CreatedBy: "user1",
				RawLine:   `{"id":"bd-test","title":"Test issue","status":"open","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-03T00:00:00Z","created_by":"user1"}`,
			},
		}

		result, conflicts := merge3Way(base, left, right)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}

		// CRITICAL: Status should be closed (closed wins over open)
		if result[0].Status != "closed" {
			t.Errorf("expected status 'closed', got %q", result[0].Status)
		}

		// CRITICAL: If status is closed, closed_at MUST be set
		if result[0].Status == "closed" && result[0].ClosedAt == "" {
			t.Error("INVALID STATE: status='closed' but closed_at is empty")
		}

		// CRITICAL: If status is open, closed_at MUST be empty
		if result[0].Status == "open" && result[0].ClosedAt != "" {
			t.Errorf("INVALID STATE: status='open' but closed_at='%s'", result[0].ClosedAt)
		}
	})

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
