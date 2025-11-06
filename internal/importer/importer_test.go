//go:build !integration
// +build !integration

package importer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
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

	t.Run("hash-based suffix rename", func(t *testing.T) {
		// Hash-based IDs (base36) are now valid and should be renamed
		issues := []*types.Issue{
			{ID: "old-a3f8", Title: "Hash suffix issue"},
		}

		err := RenameImportedIssuePrefixes(issues, "new")
		if err != nil {
			t.Errorf("Unexpected error for hash-based suffix: %v", err)
		}
		if issues[0].ID != "new-a3f8" {
			t.Errorf("Expected ID 'new-a3f8', got %q", issues[0].ID)
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
		// Numeric suffixes (traditional)
		{"123", true},
		{"0", true},
		{"999", true},
		// Hash-based suffixes (base36: 0-9, a-z)
		{"a3f8e9", true},
		{"09ea", true},
		{"abc123", true},
		{"zzz", true},
		// Invalid suffixes
		{"", false},      // Empty string now returns false
		{"1.5", false},   // Non-base36 characters
		{"A3F8", false},  // Uppercase not allowed
		{"@#$!", false},  // Special characters not allowed
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

func TestImportIssues_Basic(t *testing.T) {
	ctx := context.Background()
	
	// Create temp database
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	// Set config prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}
	
	// Import single issue
	issues := []*types.Issue{
		{
			ID:          "test-abc123",
			Title:       "Test Issue",
			Description: "Test description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
		},
	}
	
	result, err := ImportIssues(ctx, tmpDB, store, issues, Options{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	
	if result.Created != 1 {
		t.Errorf("Expected 1 created, got %d", result.Created)
	}
	
	// Verify issue was created
	retrieved, err := store.GetIssue(ctx, "test-abc123")
	if err != nil {
		t.Fatalf("Failed to retrieve issue: %v", err)
	}
	if retrieved.Title != "Test Issue" {
		t.Errorf("Expected title 'Test Issue', got '%s'", retrieved.Title)
	}
}

func TestImportIssues_Update(t *testing.T) {
	ctx := context.Background()
	
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}
	
	// Create initial issue
	issue1 := &types.Issue{
		ID:          "test-abc123",
		Title:       "Original Title",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	issue1.ContentHash = issue1.ComputeContentHash()
	
	err = store.CreateIssue(ctx, issue1, "test")
	if err != nil {
		t.Fatalf("Failed to create initial issue: %v", err)
	}
	
	// Import updated version with newer timestamp
	issue2 := &types.Issue{
		ID:          "test-abc123",
		Title:       "Updated Title",
		Description: "Updated description",
		Status:      types.StatusInProgress,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now().Add(time.Hour), // Newer than issue1
	}
	issue2.ContentHash = issue2.ComputeContentHash()
	
	result, err := ImportIssues(ctx, tmpDB, store, []*types.Issue{issue2}, Options{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	
	// The importer detects this as both a collision (1) and then upserts it (creates=1)
	// Total updates = collision count + actual upserts
	if result.Updated == 0 && result.Created == 0 {
		t.Error("Expected some updates or creates")
	}
	
	// Verify update
	retrieved, err := store.GetIssue(ctx, "test-abc123")
	if err != nil {
		t.Fatalf("Failed to retrieve issue: %v", err)
	}
	if retrieved.Title != "Updated Title" {
		t.Errorf("Expected title 'Updated Title', got '%s'", retrieved.Title)
	}
}

func TestImportIssues_DryRun(t *testing.T) {
	ctx := context.Background()
	
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}
	
	issues := []*types.Issue{
		{
			ID:        "test-abc123",
			Title:     "Test Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		},
	}
	
	// Dry run returns early when no collisions, so it reports what would be created
	result, err := ImportIssues(ctx, tmpDB, store, issues, Options{DryRun: true})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	
	// Should report that 1 issue would be created
	if result.Created != 1 {
		t.Errorf("Expected 1 would be created in dry run, got %d", result.Created)
	}
}

func TestImportIssues_Dependencies(t *testing.T) {
	ctx := context.Background()
	
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}
	
	issues := []*types.Issue{
		{
			ID:        "test-abc123",
			Title:     "Issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{IssueID: "test-abc123", DependsOnID: "test-def456", Type: types.DepBlocks},
			},
		},
		{
			ID:        "test-def456",
			Title:     "Issue 2",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		},
	}
	
	result, err := ImportIssues(ctx, tmpDB, store, issues, Options{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	
	if result.Created != 2 {
		t.Errorf("Expected 2 created, got %d", result.Created)
	}
	
	// Verify dependency was created
	deps, err := store.GetDependencies(ctx, "test-abc123")
	if err != nil {
		t.Fatalf("Failed to get dependencies: %v", err)
	}
	if len(deps) != 1 {
		t.Errorf("Expected 1 dependency, got %d", len(deps))
	}
}

func TestImportIssues_Labels(t *testing.T) {
	ctx := context.Background()
	
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}
	
	issues := []*types.Issue{
		{
			ID:        "test-abc123",
			Title:     "Test Issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Labels:    []string{"bug", "critical"},
		},
	}
	
	result, err := ImportIssues(ctx, tmpDB, store, issues, Options{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	
	if result.Created != 1 {
		t.Errorf("Expected 1 created, got %d", result.Created)
	}
	
	// Verify labels were created
	retrieved, err := store.GetIssue(ctx, "test-abc123")
	if err != nil {
		t.Fatalf("Failed to retrieve issue: %v", err)
	}
	if len(retrieved.Labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(retrieved.Labels))
	}
}

func TestGetOrCreateStore_ExistingStore(t *testing.T) {
	ctx := context.Background()
	
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	result, needClose, err := getOrCreateStore(ctx, tmpDB, store)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if needClose {
		t.Error("Expected needClose=false for existing store")
	}
	if result != store {
		t.Error("Expected same store instance")
	}
}

func TestGetOrCreateStore_NewStore(t *testing.T) {
	ctx := context.Background()
	
	tmpDB := t.TempDir() + "/test.db"
	
	// Create initial database
	initStore, err := sqlite.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	initStore.Close()
	
	// Test creating new connection
	result, needClose, err := getOrCreateStore(ctx, tmpDB, nil)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer result.Close()
	
	if !needClose {
		t.Error("Expected needClose=true for new store")
	}
	if result == nil {
		t.Error("Expected non-nil store")
	}
}

func TestGetOrCreateStore_EmptyPath(t *testing.T) {
	ctx := context.Background()
	
	_, _, err := getOrCreateStore(ctx, "", nil)
	if err == nil {
		t.Error("Expected error for empty database path")
	}
}

func TestGetPrefixList(t *testing.T) {
	tests := []struct {
		name     string
		prefixes map[string]int
		want     []string
	}{
		{
			name:     "single prefix",
			prefixes: map[string]int{"test": 5},
			want:     []string{"test- (5 issues)"},
		},
		{
			name:     "multiple prefixes",
			prefixes: map[string]int{"test": 3, "other": 2, "foo": 1},
			want:     []string{"foo- (1 issues)", "other- (2 issues)", "test- (3 issues)"},
		},
		{
			name:     "empty",
			prefixes: map[string]int{},
			want:     []string{},
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetPrefixList(tt.prefixes)
			if len(got) != len(tt.want) {
				t.Errorf("Length mismatch: got %d, want %d", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestValidateNoDuplicateExternalRefs(t *testing.T) {
	t.Run("no external_ref values", func(t *testing.T) {
		issues := []*types.Issue{
			{ID: "bd-1", Title: "Issue 1"},
			{ID: "bd-2", Title: "Issue 2"},
		}
		err := validateNoDuplicateExternalRefs(issues, false, nil)
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("unique external_ref values", func(t *testing.T) {
		ref1 := "JIRA-1"
		ref2 := "JIRA-2"
		issues := []*types.Issue{
			{ID: "bd-1", Title: "Issue 1", ExternalRef: &ref1},
			{ID: "bd-2", Title: "Issue 2", ExternalRef: &ref2},
		}
		err := validateNoDuplicateExternalRefs(issues, false, nil)
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("duplicate external_ref values", func(t *testing.T) {
		ref1 := "JIRA-1"
		ref2 := "JIRA-1"
		issues := []*types.Issue{
			{ID: "bd-1", Title: "Issue 1", ExternalRef: &ref1},
			{ID: "bd-2", Title: "Issue 2", ExternalRef: &ref2},
		}
		err := validateNoDuplicateExternalRefs(issues, false, nil)
		if err == nil {
			t.Error("Expected error for duplicate external_ref, got nil")
		}
		if err != nil && !strings.Contains(err.Error(), "duplicate external_ref values") {
			t.Errorf("Expected error about duplicates, got: %v", err)
		}
	})

	t.Run("duplicate external_ref values with clear flag", func(t *testing.T) {
		ref1 := "JIRA-1"
		ref2 := "JIRA-1"
		issues := []*types.Issue{
			{ID: "bd-1", Title: "Issue 1", ExternalRef: &ref1},
			{ID: "bd-2", Title: "Issue 2", ExternalRef: &ref2},
		}
		result := &Result{}
		err := validateNoDuplicateExternalRefs(issues, true, result)
		if err != nil {
			t.Errorf("Expected no error with clear flag, got: %v", err)
		}
		// First issue should keep external_ref, second should be cleared
		if issues[0].ExternalRef == nil || *issues[0].ExternalRef != "JIRA-1" {
			t.Error("Expected first issue to keep external_ref JIRA-1")
		}
		if issues[1].ExternalRef != nil {
			t.Error("Expected second issue to have cleared external_ref")
		}
		if result.Skipped != 1 {
			t.Errorf("Expected 1 skipped (cleared), got %d", result.Skipped)
		}
	})

	t.Run("multiple duplicates", func(t *testing.T) {
		jira1 := "JIRA-1"
		jira2 := "JIRA-2"
		issues := []*types.Issue{
			{ID: "bd-1", Title: "Issue 1", ExternalRef: &jira1},
			{ID: "bd-2", Title: "Issue 2", ExternalRef: &jira1},
			{ID: "bd-3", Title: "Issue 3", ExternalRef: &jira2},
			{ID: "bd-4", Title: "Issue 4", ExternalRef: &jira2},
		}
		err := validateNoDuplicateExternalRefs(issues, false, nil)
		if err == nil {
			t.Error("Expected error for duplicate external_ref, got nil")
		}
		if err != nil {
			if !strings.Contains(err.Error(), "JIRA-1") || !strings.Contains(err.Error(), "JIRA-2") {
				t.Errorf("Expected error to mention both JIRA-1 and JIRA-2, got: %v", err)
			}
		}
	})

	t.Run("ignores empty external_ref", func(t *testing.T) {
		empty := ""
		ref1 := "JIRA-1"
		issues := []*types.Issue{
			{ID: "bd-1", Title: "Issue 1", ExternalRef: &empty},
			{ID: "bd-2", Title: "Issue 2", ExternalRef: &empty},
			{ID: "bd-3", Title: "Issue 3", ExternalRef: &ref1},
		}
		err := validateNoDuplicateExternalRefs(issues, false, nil)
		if err != nil {
			t.Errorf("Expected no error for empty refs, got: %v", err)
		}
	})
}

func TestConcurrentExternalRefImports(t *testing.T) {
	t.Skip("TODO(bd-gpe7): Test hangs due to database deadlock - needs investigation")
	
	t.Run("sequential imports with same external_ref are detected as updates", func(t *testing.T) {
		store, err := sqlite.New(":memory:")
		if err != nil {
			t.Fatalf("Failed to create store: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			t.Fatalf("Failed to set prefix: %v", err)
		}

		externalRef := "JIRA-100"
		
		issue1 := &types.Issue{
			ID:          "bd-1",
			Title:       "First import",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			ExternalRef: &externalRef,
		}

		result1, err := ImportIssues(ctx, "", store, []*types.Issue{issue1}, Options{})
		if err != nil {
			t.Fatalf("First import failed: %v", err)
		}

		if result1.Created != 1 {
			t.Errorf("Expected 1 created, got %d", result1.Created)
		}

		issue2 := &types.Issue{
			ID:          "bd-2",
			Title:       "Second import (different ID, same external_ref)",
			Status:      types.StatusInProgress,
			Priority:    2,
			IssueType:   types.TypeTask,
			ExternalRef: &externalRef,
			UpdatedAt:   time.Now().Add(1 * time.Hour),
		}

		result2, err := ImportIssues(ctx, "", store, []*types.Issue{issue2}, Options{})
		if err != nil {
			t.Fatalf("Second import failed: %v", err)
		}

		if result2.Updated != 1 {
			t.Errorf("Expected 1 updated, got %d (created: %d)", result2.Updated, result2.Created)
		}

		finalIssue, err := store.GetIssueByExternalRef(ctx, externalRef)
		if err != nil {
			t.Fatalf("Failed to get final issue: %v", err)
		}

		if finalIssue.ID != "bd-1" {
			t.Errorf("Expected final issue ID to be bd-1, got %s", finalIssue.ID)
		}

		if finalIssue.Title != "Second import (different ID, same external_ref)" {
			t.Errorf("Expected title to be updated, got %s", finalIssue.Title)
		}
	})
}
