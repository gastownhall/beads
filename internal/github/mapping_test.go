// Package github provides client and data types for the GitHub REST API.
package github

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestDefaultGitHubMappingConfig verifies the default configuration has proper mappings.
func TestDefaultGitHubMappingConfig(t *testing.T) {
	config := DefaultMappingConfig()

	// Verify priority mappings exist
	if len(config.PriorityMap) == 0 {
		t.Error("PriorityMap is empty, want default priority mappings")
	}
	if p, ok := config.PriorityMap["high"]; !ok || p != 1 {
		t.Errorf("PriorityMap[\"high\"] = %d, want 1", p)
	}

	// Verify state mappings exist
	if len(config.StateMap) == 0 {
		t.Error("StateMap is empty, want default state mappings")
	}
	if s, ok := config.StateMap["open"]; !ok || s != "open" {
		t.Errorf("StateMap[\"open\"] = %q, want \"open\"", s)
	}
	if s, ok := config.StateMap["closed"]; !ok || s != "closed" {
		t.Errorf("StateMap[\"closed\"] = %q, want \"closed\"", s)
	}

	// Verify type mappings exist
	if len(config.LabelTypeMap) == 0 {
		t.Error("LabelTypeMap is empty, want default type mappings")
	}
	if typ, ok := config.LabelTypeMap["bug"]; !ok || typ != "bug" {
		t.Errorf("LabelTypeMap[\"bug\"] = %q, want \"bug\"", typ)
	}
}

// TestPriorityFromLabels verifies parsing priority::* labels.
func TestPriorityFromLabels(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		name         string
		labels       []string
		wantPriority int
	}{
		{
			name:         "priority::critical",
			labels:       []string{"priority::critical", "bug"},
			wantPriority: 0,
		},
		{
			name:         "priority::high",
			labels:       []string{"priority::high"},
			wantPriority: 1,
		},
		{
			name:         "priority::medium",
			labels:       []string{"priority::medium", "type::feature"},
			wantPriority: 2,
		},
		{
			name:         "priority::low",
			labels:       []string{"priority::low"},
			wantPriority: 3,
		},
		{
			name:         "no priority label defaults to medium",
			labels:       []string{"bug", "backend"},
			wantPriority: 2,
		},
		{
			name:         "empty labels defaults to medium",
			labels:       []string{},
			wantPriority: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := priorityFromLabels(tt.labels, config)
			if got != tt.wantPriority {
				t.Errorf("priorityFromLabels(%v) = %d, want %d", tt.labels, got, tt.wantPriority)
			}
		})
	}
}

// TestStatusFromLabelsAndState verifies status determination from labels and GitHub state.
func TestStatusFromLabelsAndState(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		name       string
		labels     []string
		state      string
		wantStatus string
	}{
		{
			name:       "status::in_progress overrides open state",
			labels:     []string{"status::in_progress"},
			state:      "open",
			wantStatus: "in_progress",
		},
		{
			name:       "status::blocked",
			labels:     []string{"status::blocked", "bug"},
			state:      "open",
			wantStatus: "blocked",
		},
		{
			name:       "status::deferred",
			labels:     []string{"status::deferred"},
			state:      "open",
			wantStatus: "deferred",
		},
		{
			name:       "closed state wins over status labels",
			labels:     []string{"status::in_progress"},
			state:      "closed",
			wantStatus: "closed",
		},
		{
			name:       "open state without status label",
			labels:     []string{"bug"},
			state:      "open",
			wantStatus: "open",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusFromLabelsAndState(tt.labels, tt.state, config)
			if got != tt.wantStatus {
				t.Errorf("statusFromLabelsAndState(%v, %q) = %q, want %q", tt.labels, tt.state, got, tt.wantStatus)
			}
		})
	}
}

// TestTypeFromLabels verifies parsing type::* labels.
func TestTypeFromLabels(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		name     string
		labels   []string
		wantType string
	}{
		{
			name:     "type::bug",
			labels:   []string{"type::bug", "priority::high"},
			wantType: "bug",
		},
		{
			name:     "type::feature",
			labels:   []string{"type::feature"},
			wantType: "feature",
		},
		{
			name:     "type::task",
			labels:   []string{"type::task"},
			wantType: "task",
		},
		{
			name:     "type::epic",
			labels:   []string{"type::epic"},
			wantType: "epic",
		},
		{
			name:     "type::chore",
			labels:   []string{"type::chore"},
			wantType: "chore",
		},
		{
			name:     "type::decision",
			labels:   []string{"type::decision"},
			wantType: "decision",
		},
		{
			name:     "type::spike",
			labels:   []string{"type::spike"},
			wantType: "spike",
		},
		{
			name:     "type::story",
			labels:   []string{"type::story"},
			wantType: "story",
		},
		{
			name:     "type::milestone",
			labels:   []string{"type::milestone"},
			wantType: "milestone",
		},
		{
			name:     "enhancement maps to feature",
			labels:   []string{"type::enhancement"},
			wantType: "feature",
		},
		{
			name:     "bare bug label (no prefix)",
			labels:   []string{"bug"},
			wantType: "bug",
		},
		{
			name:     "no type label defaults to task",
			labels:   []string{"priority::high"},
			wantType: "task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := typeFromLabels(tt.labels, config)
			if got != tt.wantType {
				t.Errorf("typeFromLabels(%v) = %q, want %q", tt.labels, got, tt.wantType)
			}
		})
	}
}

// TestGitHubIssueToBeads verifies full conversion from GitHub Issue to beads Issue.
func TestGitHubIssueToBeads(t *testing.T) {
	config := DefaultMappingConfig()

	createdAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2024, 1, 16, 14, 0, 0, 0, time.UTC)

	ghIssue := &Issue{
		ID:     123456,
		Number: 42,
		Title:  "Fix authentication bug",
		Body:   "Users cannot log in with SSO",
		State:  "open",
		Labels: []Label{
			{Name: "type::bug"},
			{Name: "priority::high"},
			{Name: "status::in_progress"},
		},
		CreatedAt: &createdAt,
		UpdatedAt: &updatedAt,
		Assignee: &User{
			ID:    101,
			Login: "jdoe",
			Name:  "John Doe",
		},
		User: &User{
			ID:    102,
			Login: "alice",
			Name:  "Alice Smith",
		},
		HTMLURL: "https://github.com/org/repo/issues/42",
	}

	conversion := GitHubIssueToBeads(ghIssue, config)
	if conversion == nil {
		t.Fatal("GitHubIssueToBeads returned nil")
	}

	issue := conversion.Issue
	if issue == nil {
		t.Fatal("conversion.Issue is nil")
	}

	// Verify basic fields
	if issue.Title != "Fix authentication bug" {
		t.Errorf("Title = %q, want %q", issue.Title, "Fix authentication bug")
	}
	if issue.Description != "Users cannot log in with SSO" {
		t.Errorf("Description = %q, want %q", issue.Description, "Users cannot log in with SSO")
	}

	// Verify external reference
	if issue.ExternalRef == nil || *issue.ExternalRef != "https://github.com/org/repo/issues/42" {
		t.Errorf("ExternalRef = %v, want GitHub URL", issue.ExternalRef)
	}

	// Verify mapped fields
	if issue.IssueType != types.TypeBug {
		t.Errorf("IssueType = %q, want %q", issue.IssueType, types.TypeBug)
	}
	if issue.Priority != 1 {
		t.Errorf("Priority = %d, want 1 (high)", issue.Priority)
	}
	if issue.Status != types.StatusInProgress {
		t.Errorf("Status = %q, want %q", issue.Status, types.StatusInProgress)
	}

	// Verify assignee
	if issue.Assignee != "jdoe" {
		t.Errorf("Assignee = %q, want \"jdoe\"", issue.Assignee)
	}
}

// TestGitHubIssueToBeads_ClosedIssue verifies closed issues are converted correctly.
func TestGitHubIssueToBeads_ClosedIssue(t *testing.T) {
	config := DefaultMappingConfig()

	closedAt := time.Date(2024, 1, 17, 10, 0, 0, 0, time.UTC)
	ghIssue := &Issue{
		ID:       100,
		Number:   10,
		Title:    "Closed task",
		State:    "closed",
		ClosedAt: &closedAt,
		Labels:   []Label{{Name: "status::in_progress"}}, // Should be overridden by closed state
		HTMLURL:  "https://github.com/org/repo/issues/10",
	}

	conversion := GitHubIssueToBeads(ghIssue, config)
	issue := conversion.Issue
	if issue == nil {
		t.Fatal("conversion.Issue is nil")
	}

	if issue.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q (closed state overrides labels)", issue.Status, types.StatusClosed)
	}
}

// TestBeadsIssueToGitHubFields verifies conversion from beads Issue to GitHub update fields.
func TestBeadsIssueToGitHubFields(t *testing.T) {
	config := DefaultMappingConfig()

	beadsIssue := &types.Issue{
		Title:       "New feature request",
		Description: "Add dark mode support",
		IssueType:   types.TypeFeature,
		Priority:    1, // High
		Status:      types.StatusInProgress,
		Assignee:    "jdoe",
	}

	fields := BeadsIssueToGitHubFields(beadsIssue, config)

	// Verify title and body
	if fields["title"] != "New feature request" {
		t.Errorf("fields[\"title\"] = %v, want \"New feature request\"", fields["title"])
	}
	if fields["body"] != "Add dark mode support" {
		t.Errorf("fields[\"body\"] = %v, want \"Add dark mode support\"", fields["body"])
	}

	// Verify labels include type, priority, and status
	labels, ok := fields["labels"].([]string)
	if !ok {
		t.Fatalf("fields[\"labels\"] is not []string")
	}

	hasType := false
	hasPriority := false
	hasStatus := false
	for _, l := range labels {
		if l == "type::feature" {
			hasType = true
		}
		if l == "priority::high" {
			hasPriority = true
		}
		if l == "status::in_progress" {
			hasStatus = true
		}
	}
	if !hasType {
		t.Errorf("labels missing type::feature, got %v", labels)
	}
	if !hasPriority {
		t.Errorf("labels missing priority::high, got %v", labels)
	}
	if !hasStatus {
		t.Errorf("labels missing status::in_progress, got %v", labels)
	}

	// Verify state is "open" for in-progress
	if fields["state"] != "open" {
		t.Errorf("fields[\"state\"] = %v, want \"open\"", fields["state"])
	}
}

// TestBeadsIssueToGitHubFields_ClosedState verifies state is set for closed issues.
func TestBeadsIssueToGitHubFields_ClosedState(t *testing.T) {
	config := DefaultMappingConfig()

	closedIssue := &types.Issue{
		Title:  "Completed task",
		Status: types.StatusClosed,
	}

	fields := BeadsIssueToGitHubFields(closedIssue, config)

	if fields["state"] != "closed" {
		t.Errorf("fields[\"state\"] = %v, want \"closed\"", fields["state"])
	}
}

// TestFilterNonScopedLabels verifies that non-scoped labels are preserved.
func TestFilterNonScopedLabels(t *testing.T) {
	labels := []string{
		"type::bug",
		"priority::high",
		"status::in_progress",
		"backend",
		"needs-review",
		"urgent",
	}

	filtered := filterNonScopedLabels(labels)

	expected := []string{"backend", "needs-review", "urgent"}
	if len(filtered) != len(expected) {
		t.Fatalf("filterNonScopedLabels returned %d labels, want %d", len(filtered), len(expected))
	}

	for i, l := range filtered {
		if l != expected[i] {
			t.Errorf("filtered[%d] = %q, want %q", i, l, expected[i])
		}
	}
}

// TestPriorityToLabel verifies conversion from beads priority to GitHub label value.
func TestPriorityToLabel(t *testing.T) {
	tests := []struct {
		priority int
		want     string
	}{
		{0, "critical"},
		{1, "high"},
		{2, "medium"},
		{3, "low"},
		{4, "none"},
		{-1, "medium"},
		{5, "medium"},
		{100, "medium"},
	}

	for _, tt := range tests {
		got := priorityToLabel(tt.priority)
		if got != tt.want {
			t.Errorf("priorityToLabel(%d) = %q, want %q", tt.priority, got, tt.want)
		}
	}
}

// TestMappingConfigUsesTypesConstants verifies that DefaultMappingConfig uses
// the exported mapping constants from types.go, ensuring single source of truth.
func TestMappingConfigUsesTypesConstants(t *testing.T) {
	config := DefaultMappingConfig()

	// Priority mappings should match types.go PriorityMapping
	for key, want := range PriorityMapping {
		if got := config.PriorityMap[key]; got != want {
			t.Errorf("PriorityMap[%q] = %d, want %d (from PriorityMapping)", key, got, want)
		}
	}

	// Status mappings should match types.go StatusMapping
	if s := config.StateMap["open"]; s != StatusMapping["open"] {
		t.Errorf("StateMap[\"open\"] = %q, want %q", s, StatusMapping["open"])
	}
	if s := config.StateMap["closed"]; s != StatusMapping["closed"] {
		t.Errorf("StateMap[\"closed\"] = %q, want %q", s, StatusMapping["closed"])
	}

	// Type mappings should match types.go typeMapping
	for key, want := range typeMapping {
		if got := config.LabelTypeMap[key]; got != want {
			t.Errorf("LabelTypeMap[%q] = %q, want %q (from typeMapping)", key, got, want)
		}
	}
}
