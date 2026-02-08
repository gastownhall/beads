// Package gitlab provides client and data types for the GitLab REST API.
package gitlab

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestIssueJSONUnmarshal verifies that GitLab API JSON responses
// can be correctly unmarshaled into our Issue type.
func TestIssueJSONUnmarshal(t *testing.T) {
	// Sample GitLab API response for an issue
	jsonData := `{
		"id": 123456,
		"iid": 42,
		"project_id": 789,
		"title": "Fix authentication bug",
		"description": "Users cannot log in with SSO",
		"state": "opened",
		"created_at": "2024-01-15T10:30:00Z",
		"updated_at": "2024-01-16T14:45:00Z",
		"closed_at": null,
		"labels": ["bug", "priority::high"],
		"assignee": {
			"id": 101,
			"username": "jdoe",
			"name": "John Doe",
			"email": "jdoe@example.com"
		},
		"author": {
			"id": 102,
			"username": "alice",
			"name": "Alice Smith"
		},
		"milestone": {
			"id": 5,
			"iid": 1,
			"title": "Sprint 5"
		},
		"web_url": "https://gitlab.example.com/group/project/-/issues/42",
		"due_date": "2024-01-20",
		"weight": 3,
		"type": "issue"
	}`

	var issue Issue
	err := json.Unmarshal([]byte(jsonData), &issue)
	if err != nil {
		t.Fatalf("Failed to unmarshal issue: %v", err)
	}

	// Verify core fields
	if issue.ID != 123456 {
		t.Errorf("ID = %d, want 123456", issue.ID)
	}
	if issue.IID != 42 {
		t.Errorf("IID = %d, want 42", issue.IID)
	}
	if issue.ProjectID != 789 {
		t.Errorf("ProjectID = %d, want 789", issue.ProjectID)
	}
	if issue.Title != "Fix authentication bug" {
		t.Errorf("Title = %q, want %q", issue.Title, "Fix authentication bug")
	}
	if issue.Description != "Users cannot log in with SSO" {
		t.Errorf("Description = %q, want %q", issue.Description, "Users cannot log in with SSO")
	}
	if issue.State != "opened" {
		t.Errorf("State = %q, want %q", issue.State, "opened")
	}
	if issue.WebURL != "https://gitlab.example.com/group/project/-/issues/42" {
		t.Errorf("WebURL = %q, want %q", issue.WebURL, "https://gitlab.example.com/group/project/-/issues/42")
	}

	// Verify labels
	if len(issue.Labels) != 2 {
		t.Errorf("Labels count = %d, want 2", len(issue.Labels))
	}
	if issue.Labels[0] != "bug" {
		t.Errorf("Labels[0] = %q, want %q", issue.Labels[0], "bug")
	}

	// Verify assignee
	if issue.Assignee == nil {
		t.Fatal("Assignee is nil, want non-nil")
	}
	if issue.Assignee.Username != "jdoe" {
		t.Errorf("Assignee.Username = %q, want %q", issue.Assignee.Username, "jdoe")
	}

	// Verify author
	if issue.Author == nil {
		t.Fatal("Author is nil, want non-nil")
	}
	if issue.Author.Username != "alice" {
		t.Errorf("Author.Username = %q, want %q", issue.Author.Username, "alice")
	}

	// Verify milestone
	if issue.Milestone == nil {
		t.Fatal("Milestone is nil, want non-nil")
	}
	if issue.Milestone.Title != "Sprint 5" {
		t.Errorf("Milestone.Title = %q, want %q", issue.Milestone.Title, "Sprint 5")
	}

	// Verify weight
	if issue.Weight != 3 {
		t.Errorf("Weight = %d, want 3", issue.Weight)
	}

	// Verify type
	if issue.Type != "issue" {
		t.Errorf("Type = %q, want %q", issue.Type, "issue")
	}
}

// TestIssueWithLinks verifies that issue links (blocks/blocked_by) are parsed.
func TestIssueWithLinks(t *testing.T) {
	jsonData := `{
		"id": 100,
		"iid": 10,
		"project_id": 1,
		"title": "Blocked task",
		"state": "opened",
		"links": {
			"self": "https://gitlab.example.com/api/v4/projects/1/issues/10"
		}
	}`

	var issue Issue
	err := json.Unmarshal([]byte(jsonData), &issue)
	if err != nil {
		t.Fatalf("Failed to unmarshal issue: %v", err)
	}

	if issue.IID != 10 {
		t.Errorf("IID = %d, want 10", issue.IID)
	}
}

// TestIssueLinkUnmarshal verifies IssueLink JSON parsing.
func TestIssueLinkUnmarshal(t *testing.T) {
	jsonData := `{
		"source_issue": {
			"id": 100,
			"iid": 10,
			"title": "Blocking issue"
		},
		"target_issue": {
			"id": 200,
			"iid": 20,
			"title": "Blocked issue"
		},
		"link_type": "blocks"
	}`

	var link IssueLink
	err := json.Unmarshal([]byte(jsonData), &link)
	if err != nil {
		t.Fatalf("Failed to unmarshal issue link: %v", err)
	}

	if link.SourceIssue.IID != 10 {
		t.Errorf("SourceIssue.IID = %d, want 10", link.SourceIssue.IID)
	}
	if link.TargetIssue.IID != 20 {
		t.Errorf("TargetIssue.IID = %d, want 20", link.TargetIssue.IID)
	}
	if link.LinkType != "blocks" {
		t.Errorf("LinkType = %q, want %q", link.LinkType, "blocks")
	}
}

// TestClientConfig verifies client configuration defaults.
func TestClientConfig(t *testing.T) {
	client := &Client{
		Token:     "test-token",
		BaseURL:   "https://gitlab.example.com/api/v4",
		ProjectID: "group/project",
	}

	if client.Token != "test-token" {
		t.Errorf("Token = %q, want %q", client.Token, "test-token")
	}
	if client.BaseURL != "https://gitlab.example.com/api/v4" {
		t.Errorf("BaseURL = %q, want %q", client.BaseURL, "https://gitlab.example.com/api/v4")
	}
}

// TestSyncStatsZeroValue verifies SyncStats initializes correctly.
func TestSyncStatsZeroValue(t *testing.T) {
	stats := SyncStats{}

	if stats.Pulled != 0 {
		t.Errorf("Pulled = %d, want 0", stats.Pulled)
	}
	if stats.Pushed != 0 {
		t.Errorf("Pushed = %d, want 0", stats.Pushed)
	}
}

// TestConflictFields verifies Conflict type has required fields.
func TestConflictFields(t *testing.T) {
	now := time.Now()
	conflict := Conflict{
		IssueID:           "bd-abc123",
		LocalUpdated:      now,
		GitLabUpdated:     now.Add(time.Hour),
		GitLabExternalRef: "https://gitlab.example.com/group/project/-/issues/42",
		GitLabIID:         42,
		GitLabID:          123456,
	}

	if conflict.IssueID != "bd-abc123" {
		t.Errorf("IssueID = %q, want %q", conflict.IssueID, "bd-abc123")
	}
	if conflict.GitLabIID != 42 {
		t.Errorf("GitLabIID = %d, want 42", conflict.GitLabIID)
	}
}

// TestStateMapping verifies GitLab states map to expected values.
func TestStateMapping(t *testing.T) {
	tests := []struct {
		gitlabState string
		wantValid   bool
	}{
		{"opened", true},
		{"closed", true},
		{"reopened", true}, // GitLab uses "reopened" state
		{"invalid", false},
	}

	for _, tt := range tests {
		got := isValidState(tt.gitlabState)
		if got != tt.wantValid {
			t.Errorf("isValidState(%q) = %v, want %v", tt.gitlabState, got, tt.wantValid)
		}
	}
}

// TestLabelParsing verifies label prefix parsing for priority/status mapping.
func TestLabelParsing(t *testing.T) {
	tests := []struct {
		label      string
		wantPrefix string
		wantValue  string
	}{
		{"priority::high", "priority", "high"},
		{"status::in_progress", "status", "in_progress"},
		{"type::bug", "type", "bug"},
		{"simple-label", "", "simple-label"},
		{"no-prefix", "", "no-prefix"},
	}

	for _, tt := range tests {
		prefix, value := parseLabelPrefix(tt.label)
		if prefix != tt.wantPrefix {
			t.Errorf("parseLabelPrefix(%q) prefix = %q, want %q", tt.label, prefix, tt.wantPrefix)
		}
		if value != tt.wantValue {
			t.Errorf("parseLabelPrefix(%q) value = %q, want %q", tt.label, value, tt.wantValue)
		}
	}
}

// TestgetPriorityFromLabel verifies priority label value to beads priority mapping.
func TestGetPriorityFromLabel(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{"critical", 0},
		{"CRITICAL", 0}, // Case insensitive
		{"high", 1},
		{"High", 1},
		{"medium", 2},
		{"low", 3},
		{"none", 4},
		{"invalid", -1},
		{"", -1},
		{"unknown", -1},
	}

	for _, tt := range tests {
		got := getPriorityFromLabel(tt.value)
		if got != tt.want {
			t.Errorf("getPriorityFromLabel(%q) = %d, want %d", tt.value, got, tt.want)
		}
	}
}

// TestgetStatusFromLabel verifies status label value to beads status mapping.
func TestGetStatusFromLabel(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"open", "open"},
		{"OPEN", "open"}, // Case insensitive
		{"in_progress", "in_progress"},
		{"In_Progress", "in_progress"},
		{"blocked", "blocked"},
		{"deferred", "deferred"},
		{"closed", "closed"},
		{"invalid", ""},
		{"", ""},
		{"unknown", ""},
	}

	for _, tt := range tests {
		got := getStatusFromLabel(tt.value)
		if got != tt.want {
			t.Errorf("getStatusFromLabel(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}
}

// TestgetTypeFromLabel verifies type label value to beads issue type mapping.
func TestGetTypeFromLabel(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"bug", "bug"},
		{"BUG", "bug"}, // Case insensitive
		{"feature", "feature"},
		{"task", "task"},
		{"epic", "epic"},
		{"chore", "chore"},
		{"enhancement", "feature"}, // Maps to feature
		{"Enhancement", "feature"},
		{"invalid", ""},
		{"", ""},
		{"unknown", ""},
	}

	for _, tt := range tests {
		got := getTypeFromLabel(tt.value)
		if got != tt.want {
			t.Errorf("getTypeFromLabel(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}
}

// TestIssueConversion verifies IssueConversion struct field access.
func TestIssueConversion(t *testing.T) {
	conversion := &IssueConversion{
		Issue: &types.Issue{
			Title:       "Test issue",
			Description: "Test description",
		},
		Dependencies: []DependencyInfo{},
	}

	if conversion.Issue == nil {
		t.Fatal("Issue field is nil, want *types.Issue")
	}
	if conversion.Issue.Title != "Test issue" {
		t.Errorf("Issue.Title = %q, want %q", conversion.Issue.Title, "Test issue")
	}
}
