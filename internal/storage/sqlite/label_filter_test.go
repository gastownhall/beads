package sqlite

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Helper to get a pointer to Status
func statusPtr(s types.Status) *types.Status {
	return &s
}

// TestLabelPatternFiltering tests the --label-pattern glob filtering in SearchIssues
func TestLabelPatternFiltering(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with various labels
	issue1 := &types.Issue{Title: "Tech debt cleanup", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Tech legacy migration", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Feature work", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeFeature}
	issue4 := &types.Issue{Title: "Area frontend", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue5 := &types.Issue{Title: "Area backend", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issue1, issue2, issue3, issue4, issue5} {
		if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Add labels
	store.AddLabel(ctx, issue1.ID, "tech-debt", "test-user")
	store.AddLabel(ctx, issue2.ID, "tech-legacy", "test-user")
	store.AddLabel(ctx, issue3.ID, "feature", "test-user")
	store.AddLabel(ctx, issue4.ID, "area-frontend", "test-user")
	store.AddLabel(ctx, issue5.ID, "area-backend", "test-user")

	tests := []struct {
		name           string
		pattern        string
		expectedCount  int
		expectedTitles []string
	}{
		{
			name:           "glob tech-* matches tech-debt and tech-legacy",
			pattern:        "tech-*",
			expectedCount:  2,
			expectedTitles: []string{"Tech debt cleanup", "Tech legacy migration"},
		},
		{
			name:           "glob area-* matches area-frontend and area-backend",
			pattern:        "area-*",
			expectedCount:  2,
			expectedTitles: []string{"Area frontend", "Area backend"},
		},
		{
			name:           "glob *-frontend matches area-frontend",
			pattern:        "*-frontend",
			expectedCount:  1,
			expectedTitles: []string{"Area frontend"},
		},
		{
			name:           "glob nonexistent-* matches nothing",
			pattern:        "nonexistent-*",
			expectedCount:  0,
			expectedTitles: []string{},
		},
		{
			name:           "exact match feature",
			pattern:        "feature",
			expectedCount:  1,
			expectedTitles: []string{"Feature work"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			filter := types.IssueFilter{
				Status:       statusPtr(types.StatusOpen),
				LabelPattern: tc.pattern,
			}
			issues, err := store.SearchIssues(ctx, "", filter)
			if err != nil {
				t.Fatalf("SearchIssues failed: %v", err)
			}

			if len(issues) != tc.expectedCount {
				t.Errorf("Expected %d issues, got %d", tc.expectedCount, len(issues))
				for _, issue := range issues {
					t.Logf("  Got: %s (%s)", issue.Title, issue.ID)
				}
			}

			// Verify expected titles are present
			titles := make(map[string]bool)
			for _, issue := range issues {
				titles[issue.Title] = true
			}
			for _, expectedTitle := range tc.expectedTitles {
				if !titles[expectedTitle] {
					t.Errorf("Expected issue %q not found in results", expectedTitle)
				}
			}
		})
	}
}

// TestLabelRegexFiltering tests the --label-regex filtering in SearchIssues
func TestLabelRegexFiltering(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with various labels
	issue1 := &types.Issue{Title: "Tech debt cleanup", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Tech legacy migration", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Feature work", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeFeature}
	issue4 := &types.Issue{Title: "Priority high", Status: types.StatusOpen, Priority: 0, IssueType: types.TypeTask}
	issue5 := &types.Issue{Title: "Priority medium", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issue1, issue2, issue3, issue4, issue5} {
		if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Add labels
	store.AddLabel(ctx, issue1.ID, "tech-debt", "test-user")
	store.AddLabel(ctx, issue2.ID, "tech-legacy", "test-user")
	store.AddLabel(ctx, issue3.ID, "feature", "test-user")
	store.AddLabel(ctx, issue4.ID, "priority-p0", "test-user")
	store.AddLabel(ctx, issue5.ID, "priority-p1", "test-user")

	tests := []struct {
		name           string
		regex          string
		expectedCount  int
		expectedTitles []string
	}{
		{
			name:           "regex tech-(debt|legacy) matches both tech issues",
			regex:          "tech-(debt|legacy)",
			expectedCount:  2,
			expectedTitles: []string{"Tech debt cleanup", "Tech legacy migration"},
		},
		{
			name:           "regex priority-p[01] matches both priority issues",
			regex:          "priority-p[01]",
			expectedCount:  2,
			expectedTitles: []string{"Priority high", "Priority medium"},
		},
		{
			name:           "regex ^tech matches tech-debt and tech-legacy",
			regex:          "^tech",
			expectedCount:  2,
			expectedTitles: []string{"Tech debt cleanup", "Tech legacy migration"},
		},
		{
			name:           "regex debt$ matches tech-debt only",
			regex:          "debt$",
			expectedCount:  1,
			expectedTitles: []string{"Tech debt cleanup"},
		},
		{
			name:           "regex with no matches",
			regex:          "^nonexistent$",
			expectedCount:  0,
			expectedTitles: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			filter := types.IssueFilter{
				Status:     statusPtr(types.StatusOpen),
				LabelRegex: tc.regex,
			}
			issues, err := store.SearchIssues(ctx, "", filter)
			if err != nil {
				t.Fatalf("SearchIssues failed: %v", err)
			}

			if len(issues) != tc.expectedCount {
				t.Errorf("Expected %d issues, got %d", tc.expectedCount, len(issues))
				for _, issue := range issues {
					t.Logf("  Got: %s (%s)", issue.Title, issue.ID)
				}
			}

			// Verify expected titles are present
			titles := make(map[string]bool)
			for _, issue := range issues {
				titles[issue.Title] = true
			}
			for _, expectedTitle := range tc.expectedTitles {
				if !titles[expectedTitle] {
					t.Errorf("Expected issue %q not found in results", expectedTitle)
				}
			}
		})
	}
}

// TestLabelRegexInvalidPattern tests that invalid regex patterns return an error
func TestLabelRegexInvalidPattern(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue with a label
	issue := &types.Issue{Title: "Test issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	store.AddLabel(ctx, issue.ID, "test-label", "test-user")

	// Try an invalid regex pattern
	filter := types.IssueFilter{
		Status:     statusPtr(types.StatusOpen),
		LabelRegex: "[invalid(regex",
	}
	_, err := store.SearchIssues(ctx, "", filter)
	if err == nil {
		t.Error("Expected error for invalid regex pattern, got nil")
	}
}

// TestGetReadyWorkLabelPattern tests label pattern filtering in GetReadyWork
func TestGetReadyWorkLabelPattern(t *testing.T) {
	env := newTestEnv(t)

	// Create issues with various labels
	issue1 := env.CreateIssue("Tech debt task")
	issue2 := env.CreateIssue("Tech legacy task")
	issue3 := env.CreateIssue("Feature task")

	// Add labels
	env.Store.AddLabel(env.Ctx, issue1.ID, "tech-debt", "test-user")
	env.Store.AddLabel(env.Ctx, issue2.ID, "tech-legacy", "test-user")
	env.Store.AddLabel(env.Ctx, issue3.ID, "feature", "test-user")

	// Filter for tech-* labels
	ready := env.GetReadyWork(types.WorkFilter{
		Status:       types.StatusOpen,
		LabelPattern: "tech-*",
	})

	if len(ready) != 2 {
		t.Errorf("Expected 2 issues with tech-* labels, got %d", len(ready))
	}

	// Verify correct issues
	ids := make(map[string]bool)
	for _, issue := range ready {
		ids[issue.ID] = true
	}

	if !ids[issue1.ID] {
		t.Error("Expected tech-debt issue to be included")
	}
	if !ids[issue2.ID] {
		t.Error("Expected tech-legacy issue to be included")
	}
	if ids[issue3.ID] {
		t.Error("Feature issue should not be included in tech-* filter")
	}
}

// TestGetReadyWorkLabelRegex tests label regex filtering in GetReadyWork
func TestGetReadyWorkLabelRegex(t *testing.T) {
	env := newTestEnv(t)

	// Create issues with various labels
	issue1 := env.CreateIssue("Priority P0 task")
	issue2 := env.CreateIssue("Priority P1 task")
	issue3 := env.CreateIssue("Priority P2 task")
	issue4 := env.CreateIssue("No priority task")

	// Add labels
	env.Store.AddLabel(env.Ctx, issue1.ID, "priority-p0", "test-user")
	env.Store.AddLabel(env.Ctx, issue2.ID, "priority-p1", "test-user")
	env.Store.AddLabel(env.Ctx, issue3.ID, "priority-p2", "test-user")
	env.Store.AddLabel(env.Ctx, issue4.ID, "other", "test-user")

	// Filter for priority-p[01] (P0 and P1 only)
	ready := env.GetReadyWork(types.WorkFilter{
		Status:     types.StatusOpen,
		LabelRegex: "priority-p[01]",
	})

	if len(ready) != 2 {
		t.Errorf("Expected 2 issues with priority-p[01] labels, got %d", len(ready))
	}

	// Verify correct issues
	ids := make(map[string]bool)
	for _, issue := range ready {
		ids[issue.ID] = true
	}

	if !ids[issue1.ID] {
		t.Error("Expected priority-p0 issue to be included")
	}
	if !ids[issue2.ID] {
		t.Error("Expected priority-p1 issue to be included")
	}
	if ids[issue3.ID] {
		t.Error("Priority-p2 issue should not be included")
	}
	if ids[issue4.ID] {
		t.Error("Non-priority issue should not be included")
	}
}

// TestGetReadyWorkLabelRegexInvalid tests that invalid regex in GetReadyWork returns an error
func TestGetReadyWorkLabelRegexInvalid(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Set issue prefix to avoid initialization errors
	store.SetConfig(ctx, "issue_prefix", "bd")

	// Create an issue with a label
	issue := &types.Issue{Title: "Test issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	store.AddLabel(ctx, issue.ID, "test-label", "test-user")

	// Try an invalid regex pattern
	_, err := store.GetReadyWork(ctx, types.WorkFilter{
		Status:     types.StatusOpen,
		LabelRegex: "[invalid(regex",
	})
	if err == nil {
		t.Error("Expected error for invalid regex pattern in GetReadyWork, got nil")
	}
}

// TestLabelPatternAndRegexCombined tests that pattern and regex can work together
func TestLabelPatternAndRegexCombined(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues
	issue1 := &types.Issue{Title: "Both filters match", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue2 := &types.Issue{Title: "Only pattern matches", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue3 := &types.Issue{Title: "Only regex matches", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	issue4 := &types.Issue{Title: "Neither matches", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}

	for _, issue := range []*types.Issue{issue1, issue2, issue3, issue4} {
		if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("CreateIssue failed: %v", err)
		}
	}

	// Add labels - issue1 has both area-frontend and tech-debt
	store.AddLabel(ctx, issue1.ID, "area-frontend", "test-user")
	store.AddLabel(ctx, issue1.ID, "tech-debt", "test-user")
	store.AddLabel(ctx, issue2.ID, "area-backend", "test-user") // matches area-* but not tech-
	store.AddLabel(ctx, issue3.ID, "tech-legacy", "test-user")  // matches tech- but not area-*
	store.AddLabel(ctx, issue4.ID, "other", "test-user")        // matches neither

	// Filter with both pattern (area-*) and regex (tech-)
	// The issue must have at least one label matching each filter
	filter := types.IssueFilter{
		Status:       statusPtr(types.StatusOpen),
		LabelPattern: "area-*",
		LabelRegex:   "^tech-",
	}
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	// Only issue1 should match both filters
	if len(issues) != 1 {
		t.Errorf("Expected 1 issue matching both filters, got %d", len(issues))
		for _, issue := range issues {
			t.Logf("  Got: %s", issue.Title)
		}
	}

	if len(issues) == 1 && issues[0].ID != issue1.ID {
		t.Errorf("Expected issue1 to match, got %s", issues[0].Title)
	}
}
