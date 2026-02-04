package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// mustParseTime parses RFC3339 timestamp or panics (for test setup)
func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	if err != nil {
		panic("invalid time: " + s)
	}
	return t
}

// ptr creates a pointer to a value (helper for test literals)
func ptr[T any](v T) *T {
	return &v
}

// testIssue creates an Issue from JSON for testing.
// This is the most reliable way to create test Issues since it matches
// how they're created in production (from JSONL parsing).
func testIssue(jsonStr string) Issue {
	var issue Issue
	if err := json.Unmarshal([]byte(jsonStr), &issue); err != nil {
		panic("invalid JSON in test: " + err.Error())
	}
	issue.RawLine = jsonStr
	return issue
}

// testIssueNoRaw creates an Issue from JSON without setting RawLine
func testIssueNoRaw(jsonStr string) Issue {
	var issue Issue
	if err := json.Unmarshal([]byte(jsonStr), &issue); err != nil {
		panic("invalid JSON in test: " + err.Error())
	}
	return issue
}

// TestMergeStatus tests the status merging logic with special rules
func TestMergeStatus(t *testing.T) {
	tests := []struct {
		name     string
		base     types.Status
		left     types.Status
		right    types.Status
		expected types.Status
	}{
		{
			name:     "no changes",
			base:     types.StatusOpen,
			left:     types.StatusOpen,
			right:    types.StatusOpen,
			expected: types.StatusOpen,
		},
		{
			name:     "left closed, right open - closed wins",
			base:     types.StatusOpen,
			left:     types.StatusClosed,
			right:    types.StatusOpen,
			expected: types.StatusClosed,
		},
		{
			name:     "left open, right closed - closed wins",
			base:     types.StatusOpen,
			left:     types.StatusOpen,
			right:    types.StatusClosed,
			expected: types.StatusClosed,
		},
		{
			name:     "both closed",
			base:     types.StatusOpen,
			left:     types.StatusClosed,
			right:    types.StatusClosed,
			expected: types.StatusClosed,
		},
		{
			name:     "base closed, left open, right open - open (standard merge)",
			base:     types.StatusClosed,
			left:     types.StatusOpen,
			right:    types.StatusOpen,
			expected: types.StatusOpen,
		},
		{
			name:     "base closed, left open, right closed - closed wins",
			base:     types.StatusClosed,
			left:     types.StatusOpen,
			right:    types.StatusClosed,
			expected: types.StatusClosed,
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

// TestMergeDependencies tests 3-way dependency merge with removal semantics (bd-ndye)
func TestMergeDependencies(t *testing.T) {
	tests := []struct {
		name     string
		base     []Dependency
		left     []Dependency
		right    []Dependency
		expected []Dependency
	}{
		{
			name:     "empty all sides",
			base:     []Dependency{},
			left:     []Dependency{},
			right:    []Dependency{},
			expected: []Dependency{},
		},
		{
			name: "left adds dep (not in base)",
			base: []Dependency{},
			left: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			right: []Dependency{},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		},
		{
			name: "right adds dep (not in base)",
			base: []Dependency{},
			left: []Dependency{},
			right: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		},
		{
			name: "both add different deps (not in base)",
			base: []Dependency{},
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
			name: "both add same dep (not in base) - no duplicates",
			base: []Dependency{},
			left: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			right: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-02T00:00:00Z"},
			},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"}, // Left preferred
			},
		},
		{
			name: "left removes dep from base - REMOVAL WINS",
			base: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			left:     []Dependency{}, // Left removed it
			right: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			expected: []Dependency{}, // Should be empty - removal wins
		},
		{
			name: "right removes dep from base - REMOVAL WINS",
			base: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			left: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			right:    []Dependency{}, // Right removed it
			expected: []Dependency{}, // Should be empty - removal wins
		},
		{
			name: "both keep dep from base",
			base: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			left: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			right: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-02T00:00:00Z"},
			},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		},
		{
			name: "complex: left removes one, right adds one",
			base: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
			},
			left: []Dependency{}, // Left removed bd-2
			right: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks", CreatedAt: "2024-01-01T00:00:00Z"},
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"}, // Right added bd-3
			},
			expected: []Dependency{
				{IssueID: "bd-1", DependsOnID: "bd-3", Type: "related", CreatedAt: "2024-01-01T00:00:00Z"}, // Only the new one
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeDependencies(tt.base, tt.left, tt.right)
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
	t1Base := mustParseTime("2024-01-01T00:00:00Z")
	t2Base := mustParseTime("2024-01-02T00:00:00Z")
	t1Nano := mustParseTime("2024-01-01T00:00:00.123456Z")
	t2Nano := mustParseTime("2024-01-01T00:00:00.123455Z")

	tests := []struct {
		name     string
		t1       time.Time
		t2       time.Time
		expected time.Time
	}{
		{
			name:     "both zero",
			t1:       time.Time{},
			t2:       time.Time{},
			expected: time.Time{},
		},
		{
			name:     "t1 zero",
			t1:       time.Time{},
			t2:       t2Base,
			expected: t2Base,
		},
		{
			name:     "t2 zero",
			t1:       t1Base,
			t2:       time.Time{},
			expected: t1Base,
		},
		{
			name:     "t1 newer",
			t1:       t2Base,
			t2:       t1Base,
			expected: t2Base,
		},
		{
			name:     "t2 newer",
			t1:       t1Base,
			t2:       t2Base,
			expected: t2Base,
		},
		{
			name:     "identical timestamps",
			t1:       t1Base,
			t2:       t1Base,
			expected: t1Base,
		},
		{
			name:     "with fractional seconds (RFC3339Nano)",
			t1:       t1Nano,
			t2:       t2Nano,
			expected: t1Nano,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := maxTime(tt.t1, tt.t2)
			if !result.Equal(tt.expected) {
				t.Errorf("maxTime() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestIsTimeAfter tests timestamp comparison
func TestIsTimeAfter(t *testing.T) {
	t1Base := mustParseTime("2024-01-01T00:00:00Z")
	t2Base := mustParseTime("2024-01-02T00:00:00Z")

	tests := []struct {
		name     string
		t1       time.Time
		t2       time.Time
		expected bool
	}{
		{
			name:     "both zero - prefer left",
			t1:       time.Time{},
			t2:       time.Time{},
			expected: true,
		},
		{
			name:     "t1 zero - t2 wins",
			t1:       time.Time{},
			t2:       t2Base,
			expected: false,
		},
		{
			name:     "t2 zero - t1 wins",
			t1:       t1Base,
			t2:       time.Time{},
			expected: true,
		},
		{
			name:     "t1 newer",
			t1:       t2Base,
			t2:       t1Base,
			expected: true,
		},
		{
			name:     "t2 newer",
			t1:       t1Base,
			t2:       t2Base,
			expected: false,
		},
		{
			name:     "identical timestamps - left wins (bd-8nz)",
			t1:       t1Base,
			t2:       t1Base,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTimeAfter(tt.t1, tt.t2)
			if result != tt.expected {
				t.Errorf("isTimeAfter(%v, %v) = %v, want %v", tt.t1, tt.t2, result, tt.expected)
			}
		})
	}
}

// TestMerge3Way_SimpleUpdates tests simple field update scenarios
func TestMerge3Way_SimpleUpdates(t *testing.T) {
	base := []Issue{
		testIssue(`{"id":"bd-abc123","title":"Original title","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
	}

	t.Run("left updates title", func(t *testing.T) {
		left := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Updated title","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`),
		}
		right := base

		result, conflicts := merge3Way(base, left, right, false)
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
			testIssue(`{"id":"bd-abc123","title":"Original title","status":"in_progress","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Status != types.StatusInProgress {
			t.Errorf("expected status 'in_progress', got %q", result[0].Status)
		}
	})

	t.Run("both update different fields", func(t *testing.T) {
		left := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Updated title","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`),
		}
		right := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Original title","status":"in_progress","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Title != "Updated title" {
			t.Errorf("expected title 'Updated title', got %q", result[0].Title)
		}
		if result[0].Status != types.StatusInProgress {
			t.Errorf("expected status 'in_progress', got %q", result[0].Status)
		}
	})
}

// TestMergePriority tests priority merging including bd-d0t fix
func TestMergePriority(t *testing.T) {
	tests := []struct {
		name     string
		base     int
		left     int
		right    int
		expected int
	}{
		{
			name:     "no changes",
			base:     2,
			left:     2,
			right:    2,
			expected: 2,
		},
		{
			name:     "left changed",
			base:     2,
			left:     1,
			right:    2,
			expected: 1,
		},
		{
			name:     "right changed",
			base:     2,
			left:     2,
			right:    3,
			expected: 3,
		},
		{
			name:     "both changed to same value",
			base:     2,
			left:     1,
			right:    1,
			expected: 1,
		},
		{
			name:     "conflict - higher priority wins (lower number)",
			base:     2,
			left:     3,
			right:    1,
			expected: 1,
		},
		// bd-d0t fix: 0 is treated as "unset"
		{
			name:     "bd-d0t: left unset (0), right has explicit priority",
			base:     2,
			left:     0,
			right:    3,
			expected: 3, // explicit priority wins over unset
		},
		{
			name:     "bd-d0t: left has explicit priority, right unset (0)",
			base:     2,
			left:     3,
			right:    0,
			expected: 3, // explicit priority wins over unset
		},
		{
			name:     "bd-d0t: both unset (0)",
			base:     2,
			left:     0,
			right:    0,
			expected: 0,
		},
		{
			name:     "bd-d0t: base unset, left sets priority, right unchanged",
			base:     0,
			left:     1,
			right:    0,
			expected: 1, // left changed from 0 to 1
		},
		{
			name:     "bd-d0t: base unset, right sets priority, left unchanged",
			base:     0,
			left:     0,
			right:    2,
			expected: 2, // right changed from 0 to 2
		},
		// bd-1kf fix: negative priorities should be handled consistently
		{
			name:     "bd-1kf: negative priority should win over unset (0)",
			base:     2,
			left:     0,
			right:    -1,
			expected: -1, // negative priority is explicit, should win over unset
		},
		{
			name:     "bd-1kf: negative priority on left should win over unset (0) on right",
			base:     2,
			left:     -1,
			right:    0,
			expected: -1, // negative priority is explicit, should win over unset
		},
		{
			name:     "bd-1kf: conflict between negative priorities - lower wins",
			base:     2,
			left:     -2,
			right:    -1,
			expected: -2, // -2 is higher priority (more urgent) than -1
		},
		{
			name:     "bd-1kf: negative vs positive priority conflict",
			base:     2,
			left:     -1,
			right:    1,
			expected: -1, // -1 is higher priority (lower number) than 1
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergePriority(tt.base, tt.left, tt.right)
			if result != tt.expected {
				t.Errorf("mergePriority(%d, %d, %d) = %d, want %d",
					tt.base, tt.left, tt.right, result, tt.expected)
			}
		})
	}
}

// TestMerge3Way_AutoResolve tests auto-resolution of conflicts
func TestMerge3Way_AutoResolve(t *testing.T) {
	t.Run("conflicting title changes - latest updated_at wins", func(t *testing.T) {
		base := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Original","updated_at":"2024-01-01T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		// Left has older updated_at
		left := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Left version","updated_at":"2024-01-02T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		// Right has newer updated_at - this should win
		right := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Right version","updated_at":"2024-01-03T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
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
			testIssue(`{"id":"bd-abc123","priority":2,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		// Left has lower priority (higher number)
		left := []Issue{
			testIssue(`{"id":"bd-abc123","priority":3,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		// Right has higher priority (lower number) - this should win
		right := []Issue{
			testIssue(`{"id":"bd-abc123","priority":1,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
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
			testIssue(`{"id":"bd-abc123","notes":"Original notes","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		left := []Issue{
			testIssue(`{"id":"bd-abc123","notes":"Left notes","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		right := []Issue{
			testIssue(`{"id":"bd-abc123","notes":"Right notes","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
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
			testIssue(`{"id":"bd-abc123","issue_type":"task","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		// Local change - should win
		left := []Issue{
			testIssue(`{"id":"bd-abc123","issue_type":"bug","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		right := []Issue{
			testIssue(`{"id":"bd-abc123","issue_type":"feature","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts with auto-resolution, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		// Local (left) wins for issue_type
		if result[0].IssueType != types.TypeBug {
			t.Errorf("expected issue_type 'bug' (local wins), got %q", result[0].IssueType)
		}
	})
}

// TestMerge3Way_Deletions tests deletion detection scenarios
func TestMerge3Way_Deletions(t *testing.T) {
	t.Run("deleted in left, unchanged in right", func(t *testing.T) {
		base := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Will be deleted","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		left := []Issue{} // Deleted in left
		right := base     // Unchanged in right

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 0 {
			t.Errorf("expected deletion to be accepted, got %d issues", len(result))
		}
	})

	t.Run("deleted in right, unchanged in left", func(t *testing.T) {
		base := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Will be deleted","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		left := base     // Unchanged in left
		right := []Issue{} // Deleted in right

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 0 {
			t.Errorf("expected deletion to be accepted, got %d issues", len(result))
		}
	})

	t.Run("deleted in left, modified in right - deletion wins", func(t *testing.T) {
		base := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Original","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		left := []Issue{} // Deleted in left
		right := []Issue{ // Modified in right
			testIssue(`{"id":"bd-abc123","title":"Modified","status":"in_progress","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts, got %d", len(conflicts))
		}
		if len(result) != 0 {
			t.Errorf("expected deletion to win (0 results), got %d", len(result))
		}
	})

	t.Run("deleted in right, modified in left - deletion wins", func(t *testing.T) {
		base := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Original","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		left := []Issue{ // Modified in left
			testIssue(`{"id":"bd-abc123","title":"Modified","status":"in_progress","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		right := []Issue{} // Deleted in right

		result, conflicts := merge3Way(base, left, right, false)
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
			testIssue(`{"id":"bd-abc123","title":"New issue","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		right := []Issue{}

		result, conflicts := merge3Way(base, left, right, false)
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
			testIssue(`{"id":"bd-abc123","title":"New issue","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
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
		issueData := testIssue(`{"id":"bd-abc123","title":"New issue","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)
		left := []Issue{issueData}
		right := []Issue{issueData}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
	})

	t.Run("added in both with different content - auto-resolved", func(t *testing.T) {
		base := []Issue{}
		// Left has older updated_at
		left := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Left version","updated_at":"2024-01-02T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		// Right has newer updated_at - should win
		right := []Issue{
			testIssue(`{"id":"bd-abc123","title":"Right version","updated_at":"2024-01-03T00:00:00Z","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
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
			testIssue(`{"id":"bd-test","title":"Test issue","status":"closed","closed_at":"2024-01-02T00:00:00Z","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`),
		}
		// Left: still closed with closed_at
		left := base
		// Right: somehow got reopened but WITHOUT removing closed_at (the bug scenario)
		right := []Issue{
			testIssue(`{"id":"bd-test","title":"Test issue","status":"open","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-03T00:00:00Z","created_by":"user1"}`),
		}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}

		// CRITICAL: Status should be closed (closed wins over open)
		if result[0].Status != types.StatusClosed {
			t.Errorf("expected status 'closed', got %q", result[0].Status)
		}

		// CRITICAL: If status is closed, closed_at MUST be set
		if result[0].Status == types.StatusClosed && (result[0].ClosedAt == nil || result[0].ClosedAt.IsZero()) {
			t.Error("INVALID STATE: status='closed' but closed_at is empty")
		}

		// CRITICAL: If status is open, closed_at MUST be empty
		if result[0].Status == types.StatusOpen && result[0].ClosedAt != nil && !result[0].ClosedAt.IsZero() {
			t.Errorf("INVALID STATE: status='open' but closed_at='%v'", result[0].ClosedAt)
		}
	})

	t.Run("bd-hv01 regression: closed issue not resurrected", func(t *testing.T) {
		// Base: issue is open
		base := []Issue{
			testIssue(`{"id":"bd-hv01","title":"Test issue","status":"open","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","created_by":"user1"}`),
		}
		// Left: issue is closed (newer)
		left := []Issue{
			testIssue(`{"id":"bd-hv01","title":"Test issue","status":"closed","closed_at":"2024-01-02T00:00:00Z","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`),
		}
		// Right: issue is still open (stale)
		right := base

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		// Issue should remain closed (left's version)
		if result[0].Status != types.StatusClosed {
			t.Errorf("expected status 'closed', got %q - issue was resurrected!", result[0].Status)
		}
		if result[0].ClosedAt == nil || result[0].ClosedAt.IsZero() {
			t.Error("expected closed_at to be set, got empty/nil")
		}
		// UpdatedAt should be the max (left's newer timestamp)
		expectedUpdatedAt := mustParseTime("2024-01-02T00:00:00Z")
		if !result[0].UpdatedAt.Equal(expectedUpdatedAt) {
			t.Errorf("expected updated_at '2024-01-02T00:00:00Z', got %v", result[0].UpdatedAt)
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

// TestIsTombstone tests the tombstone detection helper
func TestIsTombstone(t *testing.T) {
	tests := []struct {
		name     string
		status   types.Status
		expected bool
	}{
		{
			name:     "tombstone status",
			status:   types.StatusTombstone,
			expected: true,
		},
		{
			name:     "open status",
			status:   types.StatusOpen,
			expected: false,
		},
		{
			name:     "closed status",
			status:   types.StatusClosed,
			expected: false,
		},
		{
			name:     "in_progress status",
			status:   types.StatusInProgress,
			expected: false,
		},
		{
			name:     "empty status",
			status:   "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := Issue{Issue: types.Issue{Status: tt.status}}
			result := IsTombstone(issue)
			if result != tt.expected {
				t.Errorf("IsTombstone() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestMergeTombstones tests merging two tombstones
func TestMergeTombstones(t *testing.T) {
	tests := []struct {
		name            string
		leftDeletedAt   string
		rightDeletedAt  string
		expectedSide    string // "left" or "right"
	}{
		{
			name:           "left deleted later",
			leftDeletedAt:  "2024-01-02T00:00:00Z",
			rightDeletedAt: "2024-01-01T00:00:00Z",
			expectedSide:   "left",
		},
		{
			name:           "right deleted later",
			leftDeletedAt:  "2024-01-01T00:00:00Z",
			rightDeletedAt: "2024-01-02T00:00:00Z",
			expectedSide:   "right",
		},
		{
			name:           "same timestamp - left wins (tie breaker)",
			leftDeletedAt:  "2024-01-01T00:00:00Z",
			rightDeletedAt: "2024-01-01T00:00:00Z",
			expectedSide:   "left",
		},
		{
			name:           "with fractional seconds",
			leftDeletedAt:  "2024-01-01T00:00:00.123456Z",
			rightDeletedAt: "2024-01-01T00:00:00.123455Z",
			expectedSide:   "left",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leftTime := mustParseTime(tt.leftDeletedAt)
			rightTime := mustParseTime(tt.rightDeletedAt)
			left := Issue{
				Issue: types.Issue{
					ID:        "bd-test",
					Status:    StatusTombstone,
					DeletedAt: &leftTime,
					DeletedBy: "user-left",
				},
			}
			right := Issue{
				Issue: types.Issue{
					ID:        "bd-test",
					Status:    StatusTombstone,
					DeletedAt: &rightTime,
					DeletedBy: "user-right",
				},
			}
			result := mergeTombstones(left, right)
			if tt.expectedSide == "left" && result.DeletedBy != "user-left" {
				t.Errorf("expected left tombstone to win, got right")
			}
			if tt.expectedSide == "right" && result.DeletedBy != "user-right" {
				t.Errorf("expected right tombstone to win, got left")
			}
		})
	}
}

// TestMerge3Way_TombstoneVsLive tests tombstone vs live issue scenarios
func TestMerge3Way_TombstoneVsLive(t *testing.T) {
	// Base issue (live)
	baseIssue := testIssue(`{"id":"bd-abc123","title":"Original title","status":"open","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)

	// Recent tombstone (not expired) - create dynamically
	recentDeletedAt := time.Now().Add(-24 * time.Hour)
	recentTombstoneJSON := `{"id":"bd-abc123","title":"Original title","status":"tombstone","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1","deleted_at":"` + recentDeletedAt.Format(time.RFC3339) + `","deleted_by":"user2","delete_reason":"Duplicate issue","original_type":"task"}`
	recentTombstone := testIssue(recentTombstoneJSON)

	// Expired tombstone (older than TTL)
	expiredDeletedAt := time.Now().Add(-60 * 24 * time.Hour)
	expiredTombstoneJSON := `{"id":"bd-abc123","title":"Original title","status":"tombstone","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1","deleted_at":"` + expiredDeletedAt.Format(time.RFC3339) + `","deleted_by":"user2","delete_reason":"Duplicate issue","original_type":"task"}`
	expiredTombstone := testIssue(expiredTombstoneJSON)

	// Modified live issue
	modifiedLive := testIssue(`{"id":"bd-abc123","title":"Updated title","status":"in_progress","priority":1,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-03T00:00:00Z","created_by":"user1"}`)

	t.Run("recent tombstone in left wins over live in right", func(t *testing.T) {
		base := []Issue{baseIssue}
		left := []Issue{recentTombstone}
		right := []Issue{modifiedLive}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone to win, got status %q", result[0].Status)
		}
	})

	t.Run("recent tombstone in right wins over live in left", func(t *testing.T) {
		base := []Issue{baseIssue}
		left := []Issue{modifiedLive}
		right := []Issue{recentTombstone}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone to win, got status %q", result[0].Status)
		}
	})

	t.Run("expired tombstone in left loses to live in right (resurrection)", func(t *testing.T) {
		base := []Issue{baseIssue}
		left := []Issue{expiredTombstone}
		right := []Issue{modifiedLive}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Status != "in_progress" {
			t.Errorf("expected live issue to win over expired tombstone, got status %q", result[0].Status)
		}
		if result[0].Title != "Updated title" {
			t.Errorf("expected live issue's title, got %q", result[0].Title)
		}
	})

	t.Run("expired tombstone in right loses to live in left (resurrection)", func(t *testing.T) {
		base := []Issue{baseIssue}
		left := []Issue{modifiedLive}
		right := []Issue{expiredTombstone}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Status != "in_progress" {
			t.Errorf("expected live issue to win over expired tombstone, got status %q", result[0].Status)
		}
	})
}

// TestMerge3Way_TombstoneVsTombstone tests merging two tombstones
func TestMerge3Way_TombstoneVsTombstone(t *testing.T) {
	baseIssue := testIssue(`{"id":"bd-abc123","title":"Original title","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)

	t.Run("later tombstone wins", func(t *testing.T) {
		leftTombstone := testIssue(`{"id":"bd-abc123","title":"Original title","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"2024-01-02T00:00:00Z","deleted_by":"user-left","delete_reason":"Left reason"}`)
		// Right has later deleted_at
		rightTombstone := testIssue(`{"id":"bd-abc123","title":"Original title","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"2024-01-03T00:00:00Z","deleted_by":"user-right","delete_reason":"Right reason"}`)

		base := []Issue{baseIssue}
		left := []Issue{leftTombstone}
		right := []Issue{rightTombstone}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].DeletedBy != "user-right" {
			t.Errorf("expected right tombstone to win (later deleted_at), got DeletedBy %q", result[0].DeletedBy)
		}
		if result[0].DeleteReason != "Right reason" {
			t.Errorf("expected right tombstone's reason, got %q", result[0].DeleteReason)
		}
	})
}

// TestMerge3Way_TombstoneNoBase tests tombstone scenarios without a base
func TestMerge3Way_TombstoneNoBase(t *testing.T) {
	t.Run("tombstone added only in left", func(t *testing.T) {
		tombstone := testIssue(`{"id":"bd-abc123","title":"New tombstone","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"2024-01-02T00:00:00Z","deleted_by":"user1"}`)

		result, conflicts := merge3Way([]Issue{}, []Issue{tombstone}, []Issue{}, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone, got status %q", result[0].Status)
		}
	})

	t.Run("tombstone added only in right", func(t *testing.T) {
		tombstone := testIssue(`{"id":"bd-abc123","title":"New tombstone","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"2024-01-02T00:00:00Z","deleted_by":"user1"}`)

		result, conflicts := merge3Way([]Issue{}, []Issue{}, []Issue{tombstone}, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone, got status %q", result[0].Status)
		}
	})

	t.Run("tombstone in left vs live in right (no base)", func(t *testing.T) {
		recentDeletedAt := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
		recentTombstone := testIssue(`{"id":"bd-abc123","title":"Issue","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"` + recentDeletedAt + `","deleted_by":"user1"}`)
		live := testIssue(`{"id":"bd-abc123","title":"Issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)

		result, conflicts := merge3Way([]Issue{}, []Issue{recentTombstone}, []Issue{live}, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		// Recent tombstone should win
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone to win, got status %q", result[0].Status)
		}
	})
}

// TestMerge3WayWithTTL tests the TTL-configurable merge function
func TestMerge3WayWithTTL(t *testing.T) {
	baseIssue := testIssue(`{"id":"bd-abc123","title":"Original","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)

	// Tombstone deleted 10 days ago
	tombstoneDeletedAt := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	tombstone := testIssue(`{"id":"bd-abc123","title":"Original","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"` + tombstoneDeletedAt + `","deleted_by":"user2"}`)

	liveIssue := testIssue(`{"id":"bd-abc123","title":"Updated","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)

	t.Run("with short TTL tombstone is expired", func(t *testing.T) {
		// 7 day TTL + 1 hour grace = tombstone (10 days old) is expired
		shortTTL := 7 * 24 * time.Hour
		base := []Issue{baseIssue}
		left := []Issue{tombstone}
		right := []Issue{liveIssue}

		result, _ := Merge3WayWithTTL(base, left, right, shortTTL, false)
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		// With short TTL, tombstone is expired, live issue wins
		if result[0].Status != types.StatusOpen {
			t.Errorf("expected live issue to win with short TTL, got status %q", result[0].Status)
		}
	})

	t.Run("with long TTL tombstone is not expired", func(t *testing.T) {
		// 30 day TTL = tombstone (10 days old) is NOT expired
		longTTL := 30 * 24 * time.Hour
		base := []Issue{baseIssue}
		left := []Issue{tombstone}
		right := []Issue{liveIssue}

		result, _ := Merge3WayWithTTL(base, left, right, longTTL, false)
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		// With long TTL, tombstone is NOT expired, tombstone wins
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone to win with long TTL, got status %q", result[0].Status)
		}
	})
}

// TestMergeStatus_Tombstone tests status merging with tombstone
func TestMergeStatus_Tombstone(t *testing.T) {
	tests := []struct {
		name     string
		base     types.Status
		left     types.Status
		right    types.Status
		expected types.Status
	}{
		{
			name:     "tombstone in left wins over open in right",
			base:     types.StatusOpen,
			left:     StatusTombstone,
			right:    types.StatusOpen,
			expected: StatusTombstone,
		},
		{
			name:     "tombstone in right wins over open in left",
			base:     types.StatusOpen,
			left:     types.StatusOpen,
			right:    StatusTombstone,
			expected: StatusTombstone,
		},
		{
			name:     "tombstone in left wins over closed in right",
			base:     types.StatusOpen,
			left:     StatusTombstone,
			right:    types.StatusClosed,
			expected: StatusTombstone,
		},
		{
			name:     "both tombstone",
			base:     types.StatusOpen,
			left:     StatusTombstone,
			right:    StatusTombstone,
			expected: StatusTombstone,
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

// TestMerge3Way_TombstoneWithImplicitDeletion tests bd-ki14 fix:
// tombstones should be preserved even when the other side implicitly deleted
func TestMerge3Way_TombstoneWithImplicitDeletion(t *testing.T) {
	baseIssue := testIssue(`{"id":"bd-abc123","title":"Original","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)

	tombstoneDeletedAt := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	tombstone := testIssue(`{"id":"bd-abc123","title":"Original","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"` + tombstoneDeletedAt + `","deleted_by":"user2","delete_reason":"Duplicate"}`)

	t.Run("bd-ki14: tombstone in left preserved when right implicitly deleted", func(t *testing.T) {
		base := []Issue{baseIssue}
		left := []Issue{tombstone}
		right := []Issue{} // Implicitly deleted in right

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue (tombstone preserved), got %d", len(result))
		}
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone to be preserved, got status %q", result[0].Status)
		}
		if result[0].DeletedBy != "user2" {
			t.Errorf("expected tombstone fields preserved, got DeletedBy %q", result[0].DeletedBy)
		}
	})

	t.Run("bd-ki14: tombstone in right preserved when left implicitly deleted", func(t *testing.T) {
		base := []Issue{baseIssue}
		left := []Issue{} // Implicitly deleted in left
		right := []Issue{tombstone}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue (tombstone preserved), got %d", len(result))
		}
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone to be preserved, got status %q", result[0].Status)
		}
	})

	t.Run("bd-ki14: live issue in left still deleted when right implicitly deleted", func(t *testing.T) {
		base := []Issue{baseIssue}
		modifiedLive := testIssue(`{"id":"bd-abc123","title":"Modified","status":"in_progress","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)
		left := []Issue{modifiedLive}
		right := []Issue{} // Implicitly deleted in right

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		// Live issue should be deleted (implicit deletion wins for non-tombstones)
		if len(result) != 0 {
			t.Errorf("expected implicit deletion to win for live issue, got %d results", len(result))
		}
	})
}

// TestMergeTombstones_EmptyDeletedAt tests bd-6x5 fix:
// handling empty DeletedAt timestamps in tombstone merging
func TestMergeTombstones_EmptyDeletedAt(t *testing.T) {
	tests := []struct {
		name           string
		leftDeletedAt  *time.Time
		rightDeletedAt *time.Time
		expectedSide   string // "left" or "right"
	}{
		{
			name:           "bd-6x5: both nil - left wins as tie-breaker",
			leftDeletedAt:  nil,
			rightDeletedAt: nil,
			expectedSide:   "left",
		},
		{
			name:           "bd-6x5: left nil, right valid - right wins",
			leftDeletedAt:  nil,
			rightDeletedAt: ptr(mustParseTime("2024-01-01T00:00:00Z")),
			expectedSide:   "right",
		},
		{
			name:           "bd-6x5: left valid, right nil - left wins",
			leftDeletedAt:  ptr(mustParseTime("2024-01-01T00:00:00Z")),
			rightDeletedAt: nil,
			expectedSide:   "left",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := Issue{
				Issue: types.Issue{
					ID:        "bd-test",
					Status:    StatusTombstone,
					DeletedAt: tt.leftDeletedAt,
					DeletedBy: "user-left",
				},
			}
			right := Issue{
				Issue: types.Issue{
					ID:        "bd-test",
					Status:    StatusTombstone,
					DeletedAt: tt.rightDeletedAt,
					DeletedBy: "user-right",
				},
			}
			result := mergeTombstones(left, right)
			if tt.expectedSide == "left" && result.DeletedBy != "user-left" {
				t.Errorf("expected left tombstone to win, got DeletedBy %q", result.DeletedBy)
			}
			if tt.expectedSide == "right" && result.DeletedBy != "user-right" {
				t.Errorf("expected right tombstone to win, got DeletedBy %q", result.DeletedBy)
			}
		})
	}
}

// TestMergeIssue_TombstoneFields tests bd-1sn fix:
// tombstone fields should be copied when status becomes tombstone via safety fallback
func TestMergeIssue_TombstoneFields(t *testing.T) {
	t.Run("bd-1sn: tombstone fields copied from left when tombstone via mergeStatus", func(t *testing.T) {
		base := testIssue(`{"id":"bd-test","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)
		left := testIssue(`{"id":"bd-test","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"2024-01-02T00:00:00Z","deleted_by":"user2","delete_reason":"Duplicate","original_type":"task"}`)
		right := testIssue(`{"id":"bd-test","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)

		result, _ := mergeIssue(base, left, right)
		if result.Status != StatusTombstone {
			t.Errorf("expected tombstone status, got %q", result.Status)
		}
		expectedDeletedAt := mustParseTime("2024-01-02T00:00:00Z")
		if result.DeletedAt == nil || !result.DeletedAt.Equal(expectedDeletedAt) {
			t.Errorf("expected DeletedAt to be copied, got %v", result.DeletedAt)
		}
		if result.DeletedBy != "user2" {
			t.Errorf("expected DeletedBy to be copied, got %q", result.DeletedBy)
		}
		if result.DeleteReason != "Duplicate" {
			t.Errorf("expected DeleteReason to be copied, got %q", result.DeleteReason)
		}
		if result.OriginalType != "task" {
			t.Errorf("expected OriginalType to be copied, got %q", result.OriginalType)
		}
	})

	t.Run("bd-1sn: tombstone fields copied from right when it has later deleted_at", func(t *testing.T) {
		base := testIssue(`{"id":"bd-test","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)
		left := testIssue(`{"id":"bd-test","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"2024-01-02T00:00:00Z","deleted_by":"user-left","delete_reason":"Left reason"}`)
		right := testIssue(`{"id":"bd-test","status":"tombstone","created_at":"2024-01-01T00:00:00Z","created_by":"user1","deleted_at":"2024-01-03T00:00:00Z","deleted_by":"user-right","delete_reason":"Right reason"}`)

		result, _ := mergeIssue(base, left, right)
		if result.Status != StatusTombstone {
			t.Errorf("expected tombstone status, got %q", result.Status)
		}
		// Right has later deleted_at, so right's fields should be used
		if result.DeletedBy != "user-right" {
			t.Errorf("expected DeletedBy from right (later), got %q", result.DeletedBy)
		}
		if result.DeleteReason != "Right reason" {
			t.Errorf("expected DeleteReason from right, got %q", result.DeleteReason)
		}
	})
}

// TestIsExpiredTombstone tests edge cases for the IsExpiredTombstone function (bd-fmo)
func TestIsExpiredTombstone(t *testing.T) {
	now := time.Now()

	// Helper to create a test issue with DeletedAt
	makeIssue := func(status types.Status, deletedAt *time.Time) Issue {
		return Issue{Issue: types.Issue{ID: "bd-test", Status: status, DeletedAt: deletedAt}}
	}

	tests := []struct {
		name     string
		issue    Issue
		ttl      time.Duration
		expected bool
	}{
		{
			name:     "non-tombstone returns false",
			issue:    makeIssue(types.StatusOpen, ptr(now.Add(-100*24*time.Hour))),
			ttl:      24 * time.Hour,
			expected: false,
		},
		{
			name:     "closed status returns false",
			issue:    makeIssue(types.StatusClosed, ptr(now.Add(-100*24*time.Hour))),
			ttl:      24 * time.Hour,
			expected: false,
		},
		{
			name:     "tombstone with nil deleted_at returns false",
			issue:    makeIssue(StatusTombstone, nil),
			ttl:      24 * time.Hour,
			expected: false,
		},
		{
			name:     "tombstone with zero deleted_at returns false",
			issue:    makeIssue(StatusTombstone, ptr(time.Time{})),
			ttl:      24 * time.Hour,
			expected: false,
		},
		{
			name:     "recent tombstone (within TTL) returns false",
			issue:    makeIssue(StatusTombstone, ptr(now.Add(-1*time.Hour))),
			ttl:      24 * time.Hour,
			expected: false,
		},
		{
			name:     "old tombstone (beyond TTL) returns true",
			issue:    makeIssue(StatusTombstone, ptr(now.Add(-48*time.Hour))),
			ttl:      24 * time.Hour,
			expected: true,
		},
		{
			name:     "tombstone just inside TTL boundary (with clock skew grace) returns false",
			issue:    makeIssue(StatusTombstone, ptr(now.Add(-24*time.Hour))),
			ttl:      24 * time.Hour,
			expected: false,
		},
		{
			name:     "tombstone just past TTL boundary (with clock skew grace) returns true",
			issue:    makeIssue(StatusTombstone, ptr(now.Add(-26*time.Hour))),
			ttl:      24 * time.Hour,
			expected: true,
		},
		{
			name:     "ttl=0 falls back to DefaultTombstoneTTL (30 days)",
			issue:    makeIssue(StatusTombstone, ptr(now.Add(-20*24*time.Hour))),
			ttl:      0,
			expected: false,
		},
		{
			name:     "ttl=0 with old tombstone (beyond default TTL) returns true",
			issue:    makeIssue(StatusTombstone, ptr(now.Add(-60*24*time.Hour))),
			ttl:      0,
			expected: true,
		},
		{
			name:     "very short TTL (1 minute) works correctly",
			issue:    makeIssue(StatusTombstone, ptr(now.Add(-2*time.Hour))),
			ttl:      1 * time.Minute,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsExpiredTombstone(tt.issue, tt.ttl)
			if result != tt.expected {
				t.Errorf("IsExpiredTombstone() = %v, want %v (deleted_at=%v, ttl=%v)",
					result, tt.expected, tt.issue.DeletedAt, tt.ttl)
			}
		})
	}
}

// TestMerge3Way_TombstoneBaseBothLiveResurrection tests the scenario where
// the base version is a tombstone but both left and right have live versions.
// This can happen if Clone A deletes an issue, Clones B and C sync (getting tombstone),
// then both B and C independently recreate an issue with same ID. (bd-bob)
func TestMerge3Way_TombstoneBaseBothLiveResurrection(t *testing.T) {
	// Base is a tombstone (issue was deleted)
	deletedAt := time.Now().Add(-10 * 24 * time.Hour)
	baseTombstone := Issue{
		Issue: types.Issue{
			ID:           "bd-abc123",
			Title:        "Original title",
			Status:       StatusTombstone,
			Priority:     2,
			CreatedAt:    mustParseTime("2024-01-01T00:00:00Z"),
			UpdatedAt:    mustParseTime("2024-01-05T00:00:00Z"),
			CreatedBy:    "user1",
			DeletedAt:    &deletedAt,
			DeletedBy:    "user2",
			DeleteReason: "Obsolete",
			OriginalType: "task",
		},
	}

	// Left resurrects the issue with new content
	leftLive := testIssue(`{"id":"bd-abc123","title":"Resurrected by left","status":"open","priority":2,"type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-10T00:00:00Z","created_by":"user1"}`)

	// Right also resurrects with different content
	rightLive := testIssue(`{"id":"bd-abc123","title":"Resurrected by right","status":"in_progress","priority":1,"type":"bug","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-15T00:00:00Z","created_by":"user1"}`)

	t.Run("both sides resurrect with different content - standard merge applies", func(t *testing.T) {
		base := []Issue{baseTombstone}
		left := []Issue{leftLive}
		right := []Issue{rightLive}

		result, conflicts := merge3Way(base, left, right, false)

		// Should not have conflicts - merge rules apply
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}

		merged := result[0]

		// Issue should be live (not tombstone)
		if merged.Status == StatusTombstone {
			t.Error("expected live issue after both sides resurrected, got tombstone")
		}

		// Title: right wins because it has later UpdatedAt
		if merged.Title != "Resurrected by right" {
			t.Errorf("expected title from right (later UpdatedAt), got %q", merged.Title)
		}

		// Priority: higher priority wins (lower number = more urgent)
		if merged.Priority != 1 {
			t.Errorf("expected priority 1 (higher), got %d", merged.Priority)
		}

		// Status: standard 3-way merge applies. When both sides changed from base,
		// left wins (standard merge conflict resolution). Note: status does NOT use
		// UpdatedAt tiebreaker like title does - it uses mergeField which picks left.
		if merged.Status != types.StatusOpen {
			t.Errorf("expected status 'open' from left (both changed from base), got %q", merged.Status)
		}

		// Tombstone fields should NOT be present on merged result
		if merged.DeletedAt != nil && !merged.DeletedAt.IsZero() {
			t.Errorf("expected nil/zero DeletedAt on resurrected issue, got %v", merged.DeletedAt)
		}
		if merged.DeletedBy != "" {
			t.Errorf("expected empty DeletedBy on resurrected issue, got %q", merged.DeletedBy)
		}
	})

	t.Run("both resurrect with same status - no conflict", func(t *testing.T) {
		leftOpen := leftLive
		leftOpen.Status = types.StatusOpen
		rightOpen := rightLive
		rightOpen.Status = types.StatusOpen

		base := []Issue{baseTombstone}
		left := []Issue{leftOpen}
		right := []Issue{rightOpen}

		result, conflicts := merge3Way(base, left, right, false)

		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Status != types.StatusOpen {
			t.Errorf("expected status 'open', got %q", result[0].Status)
		}
	})

	t.Run("one side closes after resurrection", func(t *testing.T) {
		// Left resurrects and keeps open
		leftOpen := leftLive
		leftOpen.Status = types.StatusOpen

		// Right resurrects and then closes
		rightClosed := rightLive
		rightClosed.Status = types.StatusClosed
		closedAt := mustParseTime("2024-01-16T00:00:00Z")
		rightClosed.ClosedAt = &closedAt

		base := []Issue{baseTombstone}
		left := []Issue{leftOpen}
		right := []Issue{rightClosed}

		result, conflicts := merge3Way(base, left, right, false)

		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		// Closed should win over open
		if result[0].Status != types.StatusClosed {
			t.Errorf("expected closed to win over open, got %q", result[0].Status)
		}
	})
}

// TestMerge3Way_TombstoneVsLiveTimestampPrecisionMismatch tests bd-ncwo:
// When the same issue has different CreatedAt timestamp precision (e.g., with/without nanoseconds),
// the tombstone should still win over the live version.
func TestMerge3Way_TombstoneVsLiveTimestampPrecisionMismatch(t *testing.T) {
	// This test simulates the ghost resurrection bug where timestamp precision
	// differences caused the same issue to be treated as two different issues.
	// The key fix (bd-ncwo) adds ID-based fallback matching when keys don't match.

	t.Run("tombstone wins despite different CreatedAt precision", func(t *testing.T) {
		// Base: issue with status=closed
		baseIssue := testIssue(`{"id":"bd-ghost1","title":"Original title","status":"closed","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-10T00:00:00Z","created_by":"user1"}`)

		// Left: tombstone with DIFFERENT timestamp precision (has microseconds)
		deletedAt := time.Now().Add(-24 * time.Hour)
		tombstone := Issue{
			Issue: types.Issue{
				ID:           "bd-ghost1",
				Title:        "(deleted)",
				Status:       StatusTombstone,
				Priority:     2,
				CreatedAt:    mustParseTime("2024-01-01T00:00:00.000000Z"),
				UpdatedAt:    mustParseTime("2024-01-15T00:00:00Z"),
				CreatedBy:    "user1",
				DeletedAt:    &deletedAt,
				DeletedBy:    "user2",
				DeleteReason: "Duplicate issue",
			},
		}

		// Right: same closed issue (same precision as base)
		closedIssue := testIssue(`{"id":"bd-ghost1","title":"Original title","status":"closed","priority":2,"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-12T00:00:00Z","created_by":"user1"}`)

		base := []Issue{baseIssue}
		left := []Issue{tombstone}
		right := []Issue{closedIssue}

		result, conflicts := merge3Way(base, left, right, false)

		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}

		// CRITICAL: Should have exactly 1 issue, not 2 (no duplicates)
		if len(result) != 1 {
			t.Fatalf("expected 1 issue (no duplicates), got %d - this suggests ID-based matching failed", len(result))
		}

		// Tombstone should win over closed
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone to win, got status %q", result[0].Status)
		}
		if result[0].DeletedBy != "user2" {
			t.Errorf("expected tombstone fields preserved, got DeletedBy %q", result[0].DeletedBy)
		}
	})

	t.Run("tombstone wins with CreatedBy mismatch", func(t *testing.T) {
		// Test case where CreatedBy differs (e.g., empty vs populated)
		deletedAt := time.Now().Add(-24 * time.Hour)
		tombstone := Issue{
			Issue: types.Issue{
				ID:           "bd-ghost2",
				Title:        "(deleted)",
				Status:       StatusTombstone,
				Priority:     2,
				CreatedAt:    mustParseTime("2024-01-01T00:00:00Z"),
				CreatedBy:    "", // Empty CreatedBy
				DeletedAt:    &deletedAt,
				DeletedBy:    "user2",
				DeleteReason: "Cleanup",
			},
		}

		closedIssue := testIssue(`{"id":"bd-ghost2","title":"Original title","status":"closed","priority":2,"created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)

		base := []Issue{}
		left := []Issue{tombstone}
		right := []Issue{closedIssue}

		result, conflicts := merge3Way(base, left, right, false)

		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}

		// Should have exactly 1 issue
		if len(result) != 1 {
			t.Fatalf("expected 1 issue (no duplicates), got %d", len(result))
		}

		// Tombstone should win
		if result[0].Status != StatusTombstone {
			t.Errorf("expected tombstone to win despite CreatedBy mismatch, got status %q", result[0].Status)
		}
	})

	t.Run("no duplicates when both have same ID but different keys", func(t *testing.T) {
		// Ensure we don't create duplicate entries
		liveLeft := testIssue(`{"id":"bd-ghost3","title":"Left version","status":"open","created_at":"2024-01-01T00:00:00.123456Z","created_by":"user1"}`)
		liveRight := testIssue(`{"id":"bd-ghost3","title":"Right version","status":"in_progress","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)

		base := []Issue{}
		left := []Issue{liveLeft}
		right := []Issue{liveRight}

		result, conflicts := merge3Way(base, left, right, false)

		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}

		// CRITICAL: Should have exactly 1 issue, not 2
		if len(result) != 1 {
			t.Fatalf("expected 1 issue (no duplicates for same ID), got %d", len(result))
		}
	})
}

// TestMerge3Way_DeterministicOutputOrder verifies that merge output is sorted by ID
// for consistent, reproducible results regardless of input order or map iteration.
// This is important for:
// - Reproducible git diffs between merges
// - Cross-machine consistency
// - Matching bd export behavior
func TestMerge3Way_DeterministicOutputOrder(t *testing.T) {
	// Create issues with IDs that would appear in different orders
	// if map iteration order determined output order
	issueA := testIssue(`{"id":"beads-aaa","title":"A","status":"open","created_at":"2024-01-01T00:00:00Z"}`)
	issueB := testIssue(`{"id":"beads-bbb","title":"B","status":"open","created_at":"2024-01-02T00:00:00Z"}`)
	issueC := testIssue(`{"id":"beads-ccc","title":"C","status":"open","created_at":"2024-01-03T00:00:00Z"}`)
	issueZ := testIssue(`{"id":"beads-zzz","title":"Z","status":"open","created_at":"2024-01-04T00:00:00Z"}`)
	issueM := testIssue(`{"id":"beads-mmm","title":"M","status":"open","created_at":"2024-01-05T00:00:00Z"}`)

	t.Run("output is sorted by ID", func(t *testing.T) {
		// Input in arbitrary (non-sorted) order
		base := []Issue{}
		left := []Issue{issueZ, issueA, issueM}
		right := []Issue{issueC, issueB}

		result, conflicts := merge3Way(base, left, right, false)

		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}

		if len(result) != 5 {
			t.Fatalf("expected 5 issues, got %d", len(result))
		}

		// Verify output is sorted by ID
		expectedOrder := []string{"beads-aaa", "beads-bbb", "beads-ccc", "beads-mmm", "beads-zzz"}
		for i, expected := range expectedOrder {
			if result[i].ID != expected {
				t.Errorf("result[%d].ID = %q, want %q", i, result[i].ID, expected)
			}
		}
	})

	t.Run("deterministic across multiple runs", func(t *testing.T) {
		// Run merge multiple times to verify consistent ordering
		base := []Issue{}
		left := []Issue{issueZ, issueA, issueM}
		right := []Issue{issueC, issueB}

		var firstRunIDs []string
		for run := 0; run < 10; run++ {
			result, _ := merge3Way(base, left, right, false)

			var ids []string
			for _, issue := range result {
				ids = append(ids, issue.ID)
			}

			if run == 0 {
				firstRunIDs = ids
			} else {
				// Compare to first run
				for i, id := range ids {
					if id != firstRunIDs[i] {
						t.Errorf("run %d: result[%d].ID = %q, want %q (non-deterministic output)", run, i, id, firstRunIDs[i])
					}
				}
			}
		}
	})
}
// TestMerge3Way_CloseReasonPreservation tests that close_reason and closed_by_session
// are preserved during merge/sync operations (GH#891)
func TestMerge3Way_CloseReasonPreservation(t *testing.T) {
	t.Run("close_reason preserved when both sides closed - later closed_at wins", func(t *testing.T) {
		base := []Issue{testIssue(`{"id":"bd-close1","title":"Test Issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}
		left := []Issue{testIssue(`{"id":"bd-close1","title":"Test Issue","status":"closed","closed_at":"2024-01-02T00:00:00Z","close_reason":"Fixed in commit abc","closed_by_session":"session-left","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}
		right := []Issue{testIssue(`{"id":"bd-close1","title":"Test Issue","status":"closed","closed_at":"2024-01-03T00:00:00Z","close_reason":"Fixed in commit xyz","closed_by_session":"session-right","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		// Right has later closed_at, so right's close_reason should win
		if result[0].CloseReason != "Fixed in commit xyz" {
			t.Errorf("expected close_reason 'Fixed in commit xyz', got %q", result[0].CloseReason)
		}
		if result[0].ClosedBySession != "session-right" {
			t.Errorf("expected closed_by_session 'session-right', got %q", result[0].ClosedBySession)
		}
	})

	t.Run("close_reason preserved when left has later closed_at", func(t *testing.T) {
		base := []Issue{testIssue(`{"id":"bd-close2","title":"Test Issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}
		left := []Issue{testIssue(`{"id":"bd-close2","title":"Test Issue","status":"closed","closed_at":"2024-01-03T00:00:00Z","close_reason":"Resolved by PR #123","closed_by_session":"session-left","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}
		right := []Issue{testIssue(`{"id":"bd-close2","title":"Test Issue","status":"closed","closed_at":"2024-01-02T00:00:00Z","close_reason":"Duplicate","closed_by_session":"session-right","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		// Left has later closed_at, so left's close_reason should win
		if result[0].CloseReason != "Resolved by PR #123" {
			t.Errorf("expected close_reason 'Resolved by PR #123', got %q", result[0].CloseReason)
		}
		if result[0].ClosedBySession != "session-left" {
			t.Errorf("expected closed_by_session 'session-left', got %q", result[0].ClosedBySession)
		}
	})

	t.Run("close_reason cleared when status becomes open", func(t *testing.T) {
		base := []Issue{testIssue(`{"id":"bd-close3","title":"Test Issue","status":"closed","closed_at":"2024-01-02T00:00:00Z","close_reason":"Fixed","closed_by_session":"session-old","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}
		left := []Issue{testIssue(`{"id":"bd-close3","title":"Test Issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}
		right := []Issue{testIssue(`{"id":"bd-close3","title":"Test Issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		if result[0].Status != types.StatusOpen {
			t.Errorf("expected status 'open', got %q", result[0].Status)
		}
		if result[0].CloseReason != "" {
			t.Errorf("expected empty close_reason when reopened, got %q", result[0].CloseReason)
		}
		if result[0].ClosedBySession != "" {
			t.Errorf("expected empty closed_by_session when reopened, got %q", result[0].ClosedBySession)
		}
	})

	t.Run("close_reason from single side preserved", func(t *testing.T) {
		base := []Issue{testIssue(`{"id":"bd-close4","title":"Test Issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}
		left := []Issue{testIssue(`{"id":"bd-close4","title":"Test Issue","status":"closed","closed_at":"2024-01-02T00:00:00Z","close_reason":"Won't fix - by design","closed_by_session":"session-abc","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}
		right := []Issue{testIssue(`{"id":"bd-close4","title":"Test Issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("expected no conflicts, got %d", len(conflicts))
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 merged issue, got %d", len(result))
		}
		// Closed wins over open
		if result[0].Status != types.StatusClosed {
			t.Errorf("expected status 'closed', got %q", result[0].Status)
		}
		// Close reason from the closed side should be preserved
		if result[0].CloseReason != "Won't fix - by design" {
			t.Errorf("expected close_reason 'Won't fix - by design', got %q", result[0].CloseReason)
		}
		if result[0].ClosedBySession != "session-abc" {
			t.Errorf("expected closed_by_session 'session-abc', got %q", result[0].ClosedBySession)
		}
	})

	t.Run("close_reason survives round-trip through JSONL", func(t *testing.T) {
		// This tests the full merge pipeline including JSON marshaling/unmarshaling
		tmpDir := t.TempDir()

		baseContent := `{"id":"bd-jsonl1","title":"Test Issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`
		leftContent := `{"id":"bd-jsonl1","title":"Test Issue","status":"closed","closed_at":"2024-01-02T00:00:00Z","close_reason":"Fixed in commit def456","closed_by_session":"session-jsonl","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`
		rightContent := `{"id":"bd-jsonl1","title":"Test Issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`

		basePath := filepath.Join(tmpDir, "base.jsonl")
		leftPath := filepath.Join(tmpDir, "left.jsonl")
		rightPath := filepath.Join(tmpDir, "right.jsonl")
		outputPath := filepath.Join(tmpDir, "output.jsonl")

		if err := os.WriteFile(basePath, []byte(baseContent+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(leftPath, []byte(leftContent+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rightPath, []byte(rightContent+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := Merge3Way(outputPath, basePath, leftPath, rightPath, false); err != nil {
			t.Fatalf("Merge3Way failed: %v", err)
		}

		// Read output and verify close_reason is preserved
		outputData, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatal(err)
		}

		var outputIssue Issue
		if err := json.Unmarshal(outputData[:len(outputData)-1], &outputIssue); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}

		if outputIssue.Status != "closed" {
			t.Errorf("expected status 'closed', got %q", outputIssue.Status)
		}
		if outputIssue.CloseReason != "Fixed in commit def456" {
			t.Errorf("expected close_reason 'Fixed in commit def456', got %q", outputIssue.CloseReason)
		}
		if outputIssue.ClosedBySession != "session-jsonl" {
			t.Errorf("expected closed_by_session 'session-jsonl', got %q", outputIssue.ClosedBySession)
		}
	})
}

// TestMerge3Way_FieldPreservation tests that all issue fields are preserved through merge (GH#1480)
func TestMerge3Way_FieldPreservation(t *testing.T) {
	t.Run("metadata is preserved through merge", func(t *testing.T) {
		metadata := json.RawMessage(`{"files":["foo.go","bar.go"],"tool":"linter@1.0"}`)
		base := []Issue{testIssue(`{"id":"bd-meta1","title":"Issue with metadata","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1","metadata":{"files":["foo.go","bar.go"],"tool":"linter@1.0"}}`)}
		left := []Issue{testIssue(`{"id":"bd-meta1","title":"Issue with metadata","status":"in_progress","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1","metadata":{"files":["foo.go","bar.go"],"tool":"linter@1.0"}}`)}
		right := base

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Metadata == nil {
			t.Error("metadata was lost during merge")
		} else if string(result[0].Metadata) != string(metadata) {
			t.Errorf("metadata mismatch: got %s, want %s", result[0].Metadata, metadata)
		}
	})

	t.Run("labels are preserved through merge", func(t *testing.T) {
		labels := []string{"bug", "high-priority", "backend"}
		base := []Issue{testIssue(`{"id":"bd-label1","title":"Issue with labels","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1","labels":["bug","high-priority","backend"]}`)}
		left := []Issue{testIssue(`{"id":"bd-label1","title":"Updated title","status":"open","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1","labels":["bug","high-priority","backend"]}`)}
		right := base

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if len(result[0].Labels) != len(labels) {
			t.Errorf("labels count mismatch: got %d, want %d", len(result[0].Labels), len(labels))
		}
		for i, label := range labels {
			if i >= len(result[0].Labels) || result[0].Labels[i] != label {
				t.Errorf("label mismatch at index %d", i)
			}
		}
	})

	t.Run("assignee and owner are preserved through merge", func(t *testing.T) {
		base := []Issue{testIssue(`{"id":"bd-assign1","title":"Assigned issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1","assignee":"dev@example.com","owner":"owner@example.com"}`)}
		left := []Issue{testIssue(`{"id":"bd-assign1","title":"Assigned issue","status":"in_progress","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1","assignee":"dev@example.com","owner":"owner@example.com"}`)}
		right := base

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Assignee != "dev@example.com" {
			t.Errorf("assignee lost: got %q, want %q", result[0].Assignee, "dev@example.com")
		}
		if result[0].Owner != "owner@example.com" {
			t.Errorf("owner lost: got %q, want %q", result[0].Owner, "owner@example.com")
		}
	})

	t.Run("content fields are preserved through merge", func(t *testing.T) {
		base := []Issue{testIssue(`{"id":"bd-content1","title":"Issue with content","description":"Original description","design":"# Design\n\nArchitecture details","acceptance_criteria":"- [ ] Criterion 1\n- [ ] Criterion 2","notes":"Some notes","spec_id":"SPEC-123","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1"}`)}
		left := []Issue{testIssue(`{"id":"bd-content1","title":"Issue with content","description":"Original description","design":"# Design\n\nArchitecture details","acceptance_criteria":"- [ ] Criterion 1\n- [ ] Criterion 2","notes":"Some notes","spec_id":"SPEC-123","status":"in_progress","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1"}`)}
		right := base

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].Design != "# Design\n\nArchitecture details" {
			t.Errorf("design lost or corrupted")
		}
		if result[0].AcceptanceCriteria != "- [ ] Criterion 1\n- [ ] Criterion 2" {
			t.Errorf("acceptance_criteria lost or corrupted")
		}
		if result[0].SpecID != "SPEC-123" {
			t.Errorf("spec_id lost: got %q", result[0].SpecID)
		}
	})

	t.Run("scheduling fields are preserved through merge", func(t *testing.T) {
		base := []Issue{testIssue(`{"id":"bd-sched1","title":"Scheduled issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1","due_at":"2024-02-01T00:00:00Z","defer_until":"2024-01-15T00:00:00Z"}`)}
		left := base
		right := base

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		expectedDueAt := mustParseTime("2024-02-01T00:00:00Z")
		if result[0].DueAt == nil || !result[0].DueAt.Equal(expectedDueAt) {
			t.Errorf("due_at lost: got %v", result[0].DueAt)
		}
		expectedDeferUntil := mustParseTime("2024-01-15T00:00:00Z")
		if result[0].DeferUntil == nil || !result[0].DeferUntil.Equal(expectedDeferUntil) {
			t.Errorf("defer_until lost: got %v", result[0].DeferUntil)
		}
	})

	t.Run("gate fields are preserved through merge", func(t *testing.T) {
		base := []Issue{testIssue(`{"id":"bd-gate1","title":"Gate issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1","await_type":"gh:run","await_id":"12345","waiters":["user1@example.com","user2@example.com"]}`)}
		left := base
		right := base

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if result[0].AwaitType != "gh:run" {
			t.Errorf("await_type lost: got %q", result[0].AwaitType)
		}
		if result[0].AwaitID != "12345" {
			t.Errorf("await_id lost: got %q", result[0].AwaitID)
		}
		if len(result[0].Waiters) != 2 {
			t.Errorf("waiters lost: got %d, want 2", len(result[0].Waiters))
		}
	})

	t.Run("flags are preserved through merge", func(t *testing.T) {
		base := []Issue{testIssue(`{"id":"bd-flags1","title":"Flagged issue","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1","pinned":true,"is_template":true,"ephemeral":false}`)}
		left := base
		right := base

		result, conflicts := merge3Way(base, left, right, false)
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts: %v", conflicts)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result))
		}
		if !result[0].Pinned {
			t.Error("pinned flag lost")
		}
		if !result[0].IsTemplate {
			t.Error("is_template flag lost")
		}
	})

	t.Run("metadata survives JSONL round-trip", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create JSONL files with metadata
		baseContent := `{"id":"bd-jsonl-meta","title":"Test","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1","metadata":{"key":"value","count":42}}`
		leftContent := `{"id":"bd-jsonl-meta","title":"Test","status":"in_progress","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1","metadata":{"key":"value","count":42}}`
		rightContent := baseContent

		basePath := filepath.Join(tmpDir, "base.jsonl")
		leftPath := filepath.Join(tmpDir, "left.jsonl")
		rightPath := filepath.Join(tmpDir, "right.jsonl")
		outputPath := filepath.Join(tmpDir, "output.jsonl")

		if err := os.WriteFile(basePath, []byte(baseContent+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(leftPath, []byte(leftContent+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rightPath, []byte(rightContent+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := Merge3Way(outputPath, basePath, leftPath, rightPath, false); err != nil {
			t.Fatalf("Merge3Way failed: %v", err)
		}

		// Read output and verify metadata is preserved
		outputData, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatal(err)
		}

		var outputIssue Issue
		if err := json.Unmarshal(outputData[:len(outputData)-1], &outputIssue); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}

		if outputIssue.Metadata == nil {
			t.Error("metadata was lost during JSONL round-trip")
		} else {
			// Verify the metadata content
			var meta map[string]interface{}
			if err := json.Unmarshal(outputIssue.Metadata, &meta); err != nil {
				t.Fatalf("failed to parse metadata: %v", err)
			}
			if meta["key"] != "value" {
				t.Errorf("metadata key mismatch: got %v", meta["key"])
			}
			if meta["count"] != float64(42) { // JSON numbers are float64
				t.Errorf("metadata count mismatch: got %v", meta["count"])
			}
		}
	})

	t.Run("labels survive JSONL round-trip", func(t *testing.T) {
		tmpDir := t.TempDir()

		baseContent := `{"id":"bd-jsonl-labels","title":"Test","status":"open","created_at":"2024-01-01T00:00:00Z","created_by":"user1","labels":["bug","critical","backend"]}`
		leftContent := `{"id":"bd-jsonl-labels","title":"Test Updated","status":"open","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z","created_by":"user1","labels":["bug","critical","backend"]}`
		rightContent := baseContent

		basePath := filepath.Join(tmpDir, "base.jsonl")
		leftPath := filepath.Join(tmpDir, "left.jsonl")
		rightPath := filepath.Join(tmpDir, "right.jsonl")
		outputPath := filepath.Join(tmpDir, "output.jsonl")

		if err := os.WriteFile(basePath, []byte(baseContent+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(leftPath, []byte(leftContent+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rightPath, []byte(rightContent+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := Merge3Way(outputPath, basePath, leftPath, rightPath, false); err != nil {
			t.Fatalf("Merge3Way failed: %v", err)
		}

		outputData, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatal(err)
		}

		var outputIssue Issue
		if err := json.Unmarshal(outputData[:len(outputData)-1], &outputIssue); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}

		if len(outputIssue.Labels) != 3 {
			t.Errorf("labels lost: got %d, want 3", len(outputIssue.Labels))
		}
		expectedLabels := []string{"bug", "critical", "backend"}
		for i, label := range expectedLabels {
			if i >= len(outputIssue.Labels) || outputIssue.Labels[i] != label {
				t.Errorf("label mismatch at index %d: got %v", i, outputIssue.Labels)
				break
			}
		}
	})
}

// TestIssueFieldParity ensures merge.Issue has all JSON fields from types.Issue (GH#1481).
// This is a compile-time/test-time check to prevent struct drift - when new fields are added
// to types.Issue, this test will fail until merge.Issue is updated to match.
//
// The test extracts JSON tag names from both structs and verifies that any JSON field
// present in types.Issue is also present in merge.Issue. Fields with json:"-" tags and
// internal-only fields (like ContentHash) are excluded from comparison.
func TestIssueFieldParity(t *testing.T) {
	// Get JSON field names from types.Issue
	typesFields := getJSONFieldNames(reflect.TypeOf(types.Issue{}))

	// Get JSON field names from merge.Issue
	mergeFields := getJSONFieldNames(reflect.TypeOf(Issue{}))

	// Build a set of merge fields for O(1) lookup
	mergeFieldSet := make(map[string]bool)
	for _, f := range mergeFields {
		mergeFieldSet[f] = true
	}

	// Fields that are intentionally excluded from merge.Issue:
	// - ContentHash: internal computation, not in JSONL
	// - SourceRepo, IDPrefix, PrefixOverride: internal routing, not exported
	// - BondedFrom, Creator, Validations: complex nested types, preserved via pass-through
	// - WorkType: advanced feature, preserved via pass-through
	// - EventKind, Actor, Target, Payload: event-specific, preserved via pass-through
	excluded := map[string]bool{
		"content_hash":   true, // internal, json:"-"
		"source_repo":    true, // internal, json:"-"
		"id_prefix":      true, // internal, json:"-"
		"prefix_override": true, // internal, json:"-"
		// Complex nested types - these are preserved by JSON round-trip
		// but not explicitly handled in merge logic
		"bonded_from":  true,
		"creator":      true,
		"validations":  true,
		"work_type":    true,
		"event_kind":   true,
		"actor":        true,
		"target":       true,
		"payload":      true,
	}

	// Check for missing fields
	var missing []string
	for _, field := range typesFields {
		if excluded[field] {
			continue
		}
		if !mergeFieldSet[field] {
			missing = append(missing, field)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("merge.Issue is missing JSON fields from types.Issue (GH#1481 drift protection):\n"+
			"  Missing fields: %v\n\n"+
			"To fix: Add these fields to the Issue struct in internal/merge/merge.go\n"+
			"and update the merge logic in mergeIssue() to handle them.\n"+
			"See GH#1480 for an example of how field drift caused data loss.",
			missing)
	}
}

// getJSONFieldNames extracts JSON tag names from a struct type, recursively handling embedded structs.
// Returns field names as they appear in JSON (using the tag name, not Go field name).
// Skips fields with json:"-" tags (internal fields).
func getJSONFieldNames(t reflect.Type) []string {
	var fields []string

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Handle embedded structs by recursing
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			fields = append(fields, getJSONFieldNames(field.Type)...)
			continue
		}

		// Get JSON tag
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Extract field name from tag (before any comma)
		jsonName := strings.Split(jsonTag, ",")[0]
		if jsonName == "" || jsonName == "-" {
			continue
		}

		fields = append(fields, jsonName)
	}

	return fields
}
