// Package gitlab provides client and data types for the GitLab REST API.
package gitlab

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestDefaultGitLabMappingConfig verifies the default configuration has proper mappings.
func TestDefaultGitLabMappingConfig(t *testing.T) {
	config := DefaultMappingConfig()

	// Verify priority mappings exist
	if len(config.PriorityMap) == 0 {
		t.Error("PriorityMap is empty, want default priority mappings")
	}
	// Should map "high" to priority 1
	if p, ok := config.PriorityMap["high"]; !ok || p != 1 {
		t.Errorf("PriorityMap[\"high\"] = %d, want 1", p)
	}

	// Verify state mappings exist
	if len(config.StateMap) == 0 {
		t.Error("StateMap is empty, want default state mappings")
	}
	// "opened" should map to "open"
	if s, ok := config.StateMap["opened"]; !ok || s != "open" {
		t.Errorf("StateMap[\"opened\"] = %q, want \"open\"", s)
	}

	// Verify type mappings exist
	if len(config.LabelTypeMap) == 0 {
		t.Error("LabelTypeMap is empty, want default type mappings")
	}
	// "bug" should map to "bug"
	if typ, ok := config.LabelTypeMap["bug"]; !ok || typ != "bug" {
		t.Errorf("LabelTypeMap[\"bug\"] = %q, want \"bug\"", typ)
	}

	// Verify relation mappings exist
	if len(config.RelationMap) == 0 {
		t.Error("RelationMap is empty, want default relation mappings")
	}
	// "blocks" should map to "blocks"
	if r, ok := config.RelationMap["blocks"]; !ok || r != "blocks" {
		t.Errorf("RelationMap[\"blocks\"] = %q, want \"blocks\"", r)
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
			wantPriority: 2, // Default to medium
		},
		{
			name:         "empty labels defaults to medium",
			labels:       []string{},
			wantPriority: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PriorityFromLabels(tt.labels, config)
			if got != tt.wantPriority {
				t.Errorf("PriorityFromLabels(%v) = %d, want %d", tt.labels, got, tt.wantPriority)
			}
		})
	}
}

// TestStatusFromLabelsAndState verifies status determination from labels and GitLab state.
func TestStatusFromLabelsAndState(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		name       string
		labels     []string
		state      string
		wantStatus string
	}{
		{
			name:       "status::in_progress overrides opened state",
			labels:     []string{"status::in_progress"},
			state:      "opened",
			wantStatus: "in_progress",
		},
		{
			name:       "status::blocked",
			labels:     []string{"status::blocked", "bug"},
			state:      "opened",
			wantStatus: "blocked",
		},
		{
			name:       "status::deferred",
			labels:     []string{"status::deferred"},
			state:      "opened",
			wantStatus: "deferred",
		},
		{
			name:       "closed state wins over status labels",
			labels:     []string{"status::in_progress"},
			state:      "closed",
			wantStatus: "closed",
		},
		{
			name:       "opened state without status label",
			labels:     []string{"bug"},
			state:      "opened",
			wantStatus: "open",
		},
		{
			name:       "reopened state maps to open",
			labels:     []string{},
			state:      "reopened",
			wantStatus: "open",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StatusFromLabelsAndState(tt.labels, tt.state, config)
			if got != tt.wantStatus {
				t.Errorf("StatusFromLabelsAndState(%v, %q) = %q, want %q", tt.labels, tt.state, got, tt.wantStatus)
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
			got := TypeFromLabels(tt.labels, config)
			if got != tt.wantType {
				t.Errorf("TypeFromLabels(%v) = %q, want %q", tt.labels, got, tt.wantType)
			}
		})
	}
}

// TestGitLabIssueToBeads verifies full conversion from GitLab Issue to beads Issue.
func TestGitLabIssueToBeads(t *testing.T) {
	config := DefaultMappingConfig()

	createdAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2024, 1, 16, 14, 0, 0, 0, time.UTC)

	glIssue := &Issue{
		ID:          123456,
		IID:         42,
		ProjectID:   789,
		Title:       "Fix authentication bug",
		Description: "Users cannot log in with SSO",
		State:       "opened",
		CreatedAt:   &createdAt,
		UpdatedAt:   &updatedAt,
		Labels:      []string{"type::bug", "priority::high", "status::in_progress"},
		Assignee: &User{
			ID:       101,
			Username: "jdoe",
			Name:     "John Doe",
		},
		Author: &User{
			ID:       102,
			Username: "alice",
			Name:     "Alice Smith",
		},
		WebURL:  "https://gitlab.example.com/group/project/-/issues/42",
		DueDate: "2024-01-20",
		Weight:  3,
	}

	conversion := GitLabIssueToBeads(glIssue, config)
	if conversion == nil {
		t.Fatal("GitLabIssueToBeads returned nil")
	}

	issue, ok := conversion.Issue.(*types.Issue)
	if !ok || issue == nil {
		t.Fatal("conversion.Issue is not *types.Issue")
	}

	// Verify basic fields
	if issue.Title != "Fix authentication bug" {
		t.Errorf("Title = %q, want %q", issue.Title, "Fix authentication bug")
	}
	if issue.Description != "Users cannot log in with SSO" {
		t.Errorf("Description = %q, want %q", issue.Description, "Users cannot log in with SSO")
	}

	// Verify external reference
	if issue.ExternalRef == nil || *issue.ExternalRef != "https://gitlab.example.com/group/project/-/issues/42" {
		t.Errorf("ExternalRef = %v, want GitLab URL", issue.ExternalRef)
	}
	if issue.SourceSystem != "gitlab:789:42" {
		t.Errorf("SourceSystem = %q, want \"gitlab:789:42\"", issue.SourceSystem)
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

	// Verify estimate from weight (3 hours = 180 minutes)
	if issue.EstimatedMinutes == nil || *issue.EstimatedMinutes != 180 {
		t.Errorf("EstimatedMinutes = %v, want 180 (from weight 3)", issue.EstimatedMinutes)
	}
}

// TestGitLabIssueToBeads_ClosedIssue verifies closed issues are converted correctly.
func TestGitLabIssueToBeads_ClosedIssue(t *testing.T) {
	config := DefaultMappingConfig()

	closedAt := time.Date(2024, 1, 17, 10, 0, 0, 0, time.UTC)
	glIssue := &Issue{
		ID:       100,
		IID:      10,
		Title:    "Closed task",
		State:    "closed",
		ClosedAt: &closedAt,
		Labels:   []string{"status::in_progress"}, // Should be overridden by closed state
		WebURL:   "https://gitlab.example.com/project/-/issues/10",
	}

	conversion := GitLabIssueToBeads(glIssue, config)
	issue, ok := conversion.Issue.(*types.Issue)
	if !ok || issue == nil {
		t.Fatal("conversion.Issue is not *types.Issue")
	}

	if issue.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q (closed state overrides labels)", issue.Status, types.StatusClosed)
	}
}

// TestBeadsIssueToGitLabFields verifies conversion from beads Issue to GitLab update fields.
func TestBeadsIssueToGitLabFields(t *testing.T) {
	config := DefaultMappingConfig()

	estimatedMinutes := 300 // 5 hours
	beadsIssue := &types.Issue{
		Title:            "New feature request",
		Description:      "Add dark mode support",
		IssueType:        types.TypeFeature,
		Priority:         1, // High
		Status:           types.StatusInProgress,
		Assignee:         "jdoe",
		EstimatedMinutes: &estimatedMinutes,
	}

	fields := BeadsIssueToGitLabFields(beadsIssue, config)

	// Verify title and description
	if fields["title"] != "New feature request" {
		t.Errorf("fields[\"title\"] = %v, want \"New feature request\"", fields["title"])
	}
	if fields["description"] != "Add dark mode support" {
		t.Errorf("fields[\"description\"] = %v, want \"Add dark mode support\"", fields["description"])
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

	// Verify weight from estimate
	if fields["weight"] != 5 {
		t.Errorf("fields[\"weight\"] = %v, want 5", fields["weight"])
	}
}

// TestBeadsIssueToGitLabFields_StateEvent verifies state_event is set for closed issues.
func TestBeadsIssueToGitLabFields_StateEvent(t *testing.T) {
	config := DefaultMappingConfig()

	closedIssue := &types.Issue{
		Title:  "Completed task",
		Status: types.StatusClosed,
	}

	fields := BeadsIssueToGitLabFields(closedIssue, config)

	if fields["state_event"] != "close" {
		t.Errorf("fields[\"state_event\"] = %v, want \"close\"", fields["state_event"])
	}
}

// TestIssueLinksToDependencies verifies conversion of GitLab IssueLinks to beads Dependencies.
func TestIssueLinksToDependencies(t *testing.T) {
	config := DefaultMappingConfig()

	links := []IssueLink{
		{
			SourceIssue: &Issue{IID: 42, ProjectID: 789},
			TargetIssue: &Issue{IID: 43, ProjectID: 789},
			LinkType:    "blocks",
		},
		{
			SourceIssue: &Issue{IID: 42, ProjectID: 789},
			TargetIssue: &Issue{IID: 44, ProjectID: 789},
			LinkType:    "relates_to",
		},
		{
			SourceIssue: &Issue{IID: 42, ProjectID: 789},
			TargetIssue: &Issue{IID: 45, ProjectID: 789},
			LinkType:    "is_blocked_by",
		},
	}

	deps := IssueLinksToDependencies(42, links, config)

	if len(deps) != 3 {
		t.Fatalf("IssueLinksToDependencies returned %d dependencies, want 3", len(deps))
	}

	// Check blocks dependency
	if deps[0].Type != "blocks" {
		t.Errorf("deps[0].Type = %q, want \"blocks\"", deps[0].Type)
	}
	if deps[0].ToGitLabIID != 43 {
		t.Errorf("deps[0].ToGitLabIID = %d, want 43", deps[0].ToGitLabIID)
	}

	// Check relates_to dependency
	if deps[1].Type != "related" {
		t.Errorf("deps[1].Type = %q, want \"related\"", deps[1].Type)
	}

	// Check is_blocked_by (reverse of blocks)
	if deps[2].Type != "blocked_by" {
		t.Errorf("deps[2].Type = %q, want \"blocked_by\"", deps[2].Type)
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

	filtered := FilterNonScopedLabels(labels)

	// Should only have non-scoped labels
	expected := []string{"backend", "needs-review", "urgent"}
	if len(filtered) != len(expected) {
		t.Fatalf("FilterNonScopedLabels returned %d labels, want %d", len(filtered), len(expected))
	}

	for i, l := range filtered {
		if l != expected[i] {
			t.Errorf("filtered[%d] = %q, want %q", i, l, expected[i])
		}
	}
}

// TestPriorityToLabel verifies conversion from beads priority to GitLab label value.
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
		{-1, "medium"},  // Default for invalid
		{5, "medium"},   // Default for out of range
		{100, "medium"}, // Default for very large
	}

	for _, tt := range tests {
		got := priorityToLabel(tt.priority)
		if got != tt.want {
			t.Errorf("priorityToLabel(%d) = %q, want %q", tt.priority, got, tt.want)
		}
	}
}

// TestIssueLinksToDependencies_AsTarget verifies conversion when we are the target issue.
func TestIssueLinksToDependencies_AsTarget(t *testing.T) {
	config := DefaultMappingConfig()

	// Issue 42 is the target (blocked by 43)
	links := []IssueLink{
		{
			SourceIssue: &Issue{IID: 43, ProjectID: 789},
			TargetIssue: &Issue{IID: 42, ProjectID: 789},
			LinkType:    "blocks",
		},
	}

	deps := IssueLinksToDependencies(42, links, config)

	if len(deps) != 1 {
		t.Fatalf("IssueLinksToDependencies returned %d dependencies, want 1", len(deps))
	}

	// When we are target, source becomes the dependency
	if deps[0].ToGitLabIID != 43 {
		t.Errorf("deps[0].ToGitLabIID = %d, want 43 (source issue)", deps[0].ToGitLabIID)
	}
	if deps[0].FromGitLabIID != 42 {
		t.Errorf("deps[0].FromGitLabIID = %d, want 42", deps[0].FromGitLabIID)
	}
}

// TestIssueLinksToDependencies_UnknownLinkType verifies unknown link types default to "related".
func TestIssueLinksToDependencies_UnknownLinkType(t *testing.T) {
	config := DefaultMappingConfig()

	links := []IssueLink{
		{
			SourceIssue: &Issue{IID: 42, ProjectID: 789},
			TargetIssue: &Issue{IID: 43, ProjectID: 789},
			LinkType:    "unknown_type",
		},
	}

	deps := IssueLinksToDependencies(42, links, config)

	if len(deps) != 1 {
		t.Fatalf("IssueLinksToDependencies returned %d dependencies, want 1", len(deps))
	}

	// Unknown link types should default to "related"
	if deps[0].Type != "related" {
		t.Errorf("deps[0].Type = %q, want \"related\" for unknown link type", deps[0].Type)
	}
}

// TestIssueLinksToDependencies_NilIssues verifies handling of nil source/target issues.
func TestIssueLinksToDependencies_NilIssues(t *testing.T) {
	config := DefaultMappingConfig()

	// Link with nil target
	links := []IssueLink{
		{
			SourceIssue: &Issue{IID: 42, ProjectID: 789},
			TargetIssue: nil,
			LinkType:    "blocks",
		},
	}

	deps := IssueLinksToDependencies(42, links, config)

	// Should still create a dependency but with ToGitLabIID = 0
	if len(deps) != 1 {
		t.Fatalf("IssueLinksToDependencies returned %d dependencies, want 1", len(deps))
	}

	if deps[0].ToGitLabIID != 0 {
		t.Errorf("deps[0].ToGitLabIID = %d, want 0 (nil target)", deps[0].ToGitLabIID)
	}
}
