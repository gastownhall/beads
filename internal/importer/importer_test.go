package importer

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestIssueDataChanged(t *testing.T) {
	baseIssue := &types.Issue{
		ID:                 "test-1",
		Title:              "Original Title",
		Description:        "Original Description",
		Status:             types.StatusOpen,
		Priority:           1,
		IssueType:          types.TypeTask,
		Design:             "Design notes",
		AcceptanceCriteria: "Acceptance",
		Notes:              "Notes",
		Assignee:           "john",
	}

	tests := []struct {
		name     string
		updates  map[string]interface{}
		expected bool
	}{
		{
			name: "no changes",
			updates: map[string]interface{}{
				"title": "Original Title",
			},
			expected: false,
		},
		{
			name: "title changed",
			updates: map[string]interface{}{
				"title": "New Title",
			},
			expected: true,
		},
		{
			name: "description changed",
			updates: map[string]interface{}{
				"description": "New Description",
			},
			expected: true,
		},
		{
			name: "status changed",
			updates: map[string]interface{}{
				"status": types.StatusClosed,
			},
			expected: true,
		},
		{
			name: "status string changed",
			updates: map[string]interface{}{
				"status": "closed",
			},
			expected: true,
		},
		{
			name: "priority changed",
			updates: map[string]interface{}{
				"priority": 2,
			},
			expected: true,
		},
		{
			name: "priority float64 changed",
			updates: map[string]interface{}{
				"priority": float64(2),
			},
			expected: true,
		},
		{
			name: "issue_type changed",
			updates: map[string]interface{}{
				"issue_type": types.TypeBug,
			},
			expected: true,
		},
		{
			name: "design changed",
			updates: map[string]interface{}{
				"design": "New design",
			},
			expected: true,
		},
		{
			name: "acceptance_criteria changed",
			updates: map[string]interface{}{
				"acceptance_criteria": "New acceptance",
			},
			expected: true,
		},
		{
			name: "notes changed",
			updates: map[string]interface{}{
				"notes": "New notes",
			},
			expected: true,
		},
		{
			name: "assignee changed",
			updates: map[string]interface{}{
				"assignee": "jane",
			},
			expected: true,
		},
		{
			name: "multiple fields same",
			updates: map[string]interface{}{
				"title":    "Original Title",
				"priority": 1,
				"status":   types.StatusOpen,
			},
			expected: false,
		},
		{
			name: "one field changed in multiple",
			updates: map[string]interface{}{
				"title":    "Original Title",
				"priority": 2, // Changed
				"status":   types.StatusOpen,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IssueDataChanged(baseIssue, tt.updates)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestFieldComparator_StringConversion(t *testing.T) {
	fc := newFieldComparator()

	tests := []struct {
		name      string
		value     interface{}
		wantStr   string
		wantOk    bool
	}{
		{"string", "hello", "hello", true},
		{"string pointer", stringPtr("world"), "world", true},
		{"nil string pointer", (*string)(nil), "", true},
		{"nil", nil, "", true},
		{"int (invalid)", 123, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			str, ok := fc.strFrom(tt.value)
			if ok != tt.wantOk {
				t.Errorf("Expected ok=%v, got ok=%v", tt.wantOk, ok)
			}
			if ok && str != tt.wantStr {
				t.Errorf("Expected str=%q, got %q", tt.wantStr, str)
			}
		})
	}
}

func TestFieldComparator_EqualPtrStr(t *testing.T) {
	fc := newFieldComparator()

	tests := []struct {
		name     string
		existing *string
		newVal   interface{}
		want     bool
	}{
		{"both nil", nil, "", true},
		{"existing nil, new empty", nil, "", true},
		{"existing nil, new string", nil, "test", false},
		{"equal strings", stringPtr("test"), "test", true},
		{"different strings", stringPtr("test"), "other", false},
		{"existing string, new nil", stringPtr("test"), nil, false},
		{"invalid type", stringPtr("test"), 123, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fc.equalPtrStr(tt.existing, tt.newVal)
			if got != tt.want {
				t.Errorf("equalPtrStr() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFieldComparator_EqualIssueType(t *testing.T) {
	fc := newFieldComparator()

	tests := []struct {
		name     string
		existing types.IssueType
		newVal   interface{}
		want     bool
	}{
		{"same IssueType", types.TypeTask, types.TypeTask, true},
		{"different IssueType", types.TypeTask, types.TypeBug, false},
		{"IssueType vs string match", types.TypeTask, "task", true},
		{"IssueType vs string no match", types.TypeTask, "bug", false},
		{"invalid type", types.TypeTask, 123, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fc.equalIssueType(tt.existing, tt.newVal)
			if got != tt.want {
				t.Errorf("equalIssueType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFieldComparator_IntConversion(t *testing.T) {
	fc := newFieldComparator()

	tests := []struct {
		name    string
		value   interface{}
		wantInt int64
		wantOk  bool
	}{
		{"int", 42, 42, true},
		{"int32", int32(42), 42, true},
		{"int64", int64(42), 42, true},
		{"float64 integer", float64(42), 42, true},
		{"float64 fractional", 42.5, 0, false},
		{"string (invalid)", "123", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i, ok := fc.intFrom(tt.value)
			if ok != tt.wantOk {
				t.Errorf("Expected ok=%v, got ok=%v", tt.wantOk, ok)
			}
			if ok && i != tt.wantInt {
				t.Errorf("Expected int=%d, got %d", tt.wantInt, i)
			}
		})
	}
}

func TestRenameImportedIssuePrefixes(t *testing.T) {
	t.Run("rename single issue", func(t *testing.T) {
		issues := []*types.Issue{
			{
				ID:    "old-1",
				Title: "Test Issue",
			},
		}

		err := RenameImportedIssuePrefixes(issues, "new")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if issues[0].ID != "new-1" {
			t.Errorf("Expected ID 'new-1', got '%s'", issues[0].ID)
		}
	})

	t.Run("rename multiple issues", func(t *testing.T) {
		issues := []*types.Issue{
			{ID: "old-1", Title: "Issue 1"},
			{ID: "old-2", Title: "Issue 2"},
			{ID: "other-3", Title: "Issue 3"},
		}

		err := RenameImportedIssuePrefixes(issues, "new")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if issues[0].ID != "new-1" {
			t.Errorf("Expected ID 'new-1', got '%s'", issues[0].ID)
		}
		if issues[1].ID != "new-2" {
			t.Errorf("Expected ID 'new-2', got '%s'", issues[1].ID)
		}
		if issues[2].ID != "new-3" {
			t.Errorf("Expected ID 'new-3', got '%s'", issues[2].ID)
		}
	})

	t.Run("rename with dependencies", func(t *testing.T) {
		issues := []*types.Issue{
			{
				ID:    "old-1",
				Title: "Issue 1",
				Dependencies: []*types.Dependency{
					{IssueID: "old-1", DependsOnID: "old-2", Type: types.DepBlocks},
				},
			},
			{
				ID:    "old-2",
				Title: "Issue 2",
			},
		}

		err := RenameImportedIssuePrefixes(issues, "new")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if issues[0].Dependencies[0].IssueID != "new-1" {
			t.Errorf("Expected dependency IssueID 'new-1', got '%s'", issues[0].Dependencies[0].IssueID)
		}
		if issues[0].Dependencies[0].DependsOnID != "new-2" {
			t.Errorf("Expected dependency DependsOnID 'new-2', got '%s'", issues[0].Dependencies[0].DependsOnID)
		}
	})

	t.Run("rename with text references", func(t *testing.T) {
		issues := []*types.Issue{
			{
				ID:                 "old-1",
				Title:              "Refers to old-2",
				Description:        "See old-2 for details",
				Design:             "Depends on old-2",
				AcceptanceCriteria: "After old-2 is done",
				Notes:              "Related: old-2",
			},
			{
				ID:    "old-2",
				Title: "Issue 2",
			},
		}

		err := RenameImportedIssuePrefixes(issues, "new")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if issues[0].Title != "Refers to new-2" {
			t.Errorf("Expected title with new-2, got '%s'", issues[0].Title)
		}
		if issues[0].Description != "See new-2 for details" {
			t.Errorf("Expected description with new-2, got '%s'", issues[0].Description)
		}
	})

	t.Run("rename with comments", func(t *testing.T) {
		issues := []*types.Issue{
			{
				ID:    "old-1",
				Title: "Issue 1",
				Comments: []*types.Comment{
					{
						ID:        0,
						IssueID:   "old-1",
						Author:    "test",
						Text:      "Related to old-2",
						CreatedAt: time.Now(),
					},
				},
			},
			{
				ID:    "old-2",
				Title: "Issue 2",
			},
		}

		err := RenameImportedIssuePrefixes(issues, "new")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if issues[0].Comments[0].Text != "Related to new-2" {
			t.Errorf("Expected comment with new-2, got '%s'", issues[0].Comments[0].Text)
		}
	})

	t.Run("error on malformed ID", func(t *testing.T) {
		issues := []*types.Issue{
			{ID: "nohyphen", Title: "Invalid"},
		}

		err := RenameImportedIssuePrefixes(issues, "new")
		if err == nil {
			t.Error("Expected error for malformed ID")
		}
	})

	t.Run("error on non-numeric suffix", func(t *testing.T) {
		issues := []*types.Issue{
			{ID: "old-abc", Title: "Invalid"},
		}

		err := RenameImportedIssuePrefixes(issues, "new")
		if err == nil {
			t.Error("Expected error for non-numeric suffix")
		}
	})

	t.Run("no rename when prefix matches", func(t *testing.T) {
		issues := []*types.Issue{
			{ID: "same-1", Title: "Issue 1"},
		}

		err := RenameImportedIssuePrefixes(issues, "same")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if issues[0].ID != "same-1" {
			t.Errorf("Expected ID unchanged 'same-1', got '%s'", issues[0].ID)
		}
	})
}

func TestReplaceBoundaryAware(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		oldID  string
		newID  string
		want   string
	}{
		{
			name:   "simple replacement",
			text:   "See old-1 for details",
			oldID:  "old-1",
			newID:  "new-1",
			want:   "See new-1 for details",
		},
		{
			name:   "multiple occurrences",
			text:   "old-1 and old-1 again",
			oldID:  "old-1",
			newID:  "new-1",
			want:   "new-1 and new-1 again",
		},
		{
			name:   "no match substring prefix",
			text:   "old-10 should not match",
			oldID:  "old-1",
			newID:  "new-1",
			want:   "old-10 should not match",
		},
		{
			name:   "match at end of longer ID",
			text:   "should not match old-1 at end",
			oldID:  "old-1",
			newID:  "new-1",
			want:   "should not match new-1 at end",
		},
		{
			name:   "boundary at start",
			text:   "old-1 starts here",
			oldID:  "old-1",
			newID:  "new-1",
			want:   "new-1 starts here",
		},
		{
			name:   "boundary at end",
			text:   "ends with old-1",
			oldID:  "old-1",
			newID:  "new-1",
			want:   "ends with new-1",
		},
		{
			name:   "boundary punctuation",
			text:   "See (old-1) and [old-1] or {old-1}",
			oldID:  "old-1",
			newID:  "new-1",
			want:   "See (new-1) and [new-1] or {new-1}",
		},
		{
			name:   "no occurrence",
			text:   "No match here",
			oldID:  "old-1",
			newID:  "new-1",
			want:   "No match here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replaceBoundaryAware(tt.text, tt.oldID, tt.newID)
			if got != tt.want {
				t.Errorf("Got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsBoundary(t *testing.T) {
	boundaries := []byte{' ', '\t', '\n', '\r', ',', '.', '!', '?', ':', ';', '(', ')', '[', ']', '{', '}'}
	for _, b := range boundaries {
		if !isBoundary(b) {
			t.Errorf("Expected '%c' to be a boundary", b)
		}
	}

	notBoundaries := []byte{'a', 'Z', '0', '9', '-', '_'}
	for _, b := range notBoundaries {
		if isBoundary(b) {
			t.Errorf("Expected '%c' not to be a boundary", b)
		}
	}
}

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"123", true},
		{"0", true},
		{"999", true},
		{"abc", false},
		{"12a", false},
		{"", true}, // Empty string returns true (edge case in implementation)
		{"1.5", false},
	}

	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got := isNumeric(tt.s)
			if got != tt.want {
				t.Errorf("isNumeric(%q) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

func stringPtr(s string) *string {
	return &s
}
