package jira

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func TestRegistered(t *testing.T) {
	factory := tracker.Get("jira")
	if factory == nil {
		t.Fatal("jira tracker not registered")
	}
	tr := factory()
	if tr.Name() != "jira" {
		t.Errorf("Name() = %q, want %q", tr.Name(), "jira")
	}
	if tr.DisplayName() != "Jira" {
		t.Errorf("DisplayName() = %q, want %q", tr.DisplayName(), "Jira")
	}
	if tr.ConfigPrefix() != "jira" {
		t.Errorf("ConfigPrefix() = %q, want %q", tr.ConfigPrefix(), "jira")
	}
}

func TestIsExternalRef(t *testing.T) {
	tr := &Tracker{jiraURL: "https://company.atlassian.net"}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://company.atlassian.net/browse/PROJ-123", true},
		{"https://company.atlassian.net/browse/TEAM-1", true},
		{"https://other.atlassian.net/browse/PROJ-123", false},
		{"https://linear.app/team/issue/PROJ-123", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tr.IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

func TestExtractIdentifier(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want string
	}{
		{"https://company.atlassian.net/browse/PROJ-123", "PROJ-123"},
		{"https://company.atlassian.net/browse/TEAM-1", "TEAM-1"},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestBuildExternalRef(t *testing.T) {
	tr := &Tracker{jiraURL: "https://company.atlassian.net"}
	ti := &tracker.TrackerIssue{Identifier: "PROJ-123"}
	ref := tr.BuildExternalRef(ti)
	want := "https://company.atlassian.net/browse/PROJ-123"
	if ref != want {
		t.Errorf("BuildExternalRef() = %q, want %q", ref, want)
	}
}

func TestJiraToTrackerIssue(t *testing.T) {
	ji := &Issue{
		ID:  "10001",
		Key: "PROJ-42",
		Self: "https://company.atlassian.net/rest/api/3/issue/10001",
		Fields: IssueFields{
			Summary:     "Fix login bug",
			Description: json.RawMessage(`"A plain text description"`),
			Status:      &StatusField{ID: "1", Name: "In Progress"},
			Priority:    &PriorityField{ID: "2", Name: "High"},
			IssueType:   &IssueTypeField{ID: "10001", Name: "Bug"},
			Project:     &ProjectField{ID: "10000", Key: "PROJ"},
			Assignee:    &UserField{AccountID: "abc123", DisplayName: "Alice", EmailAddress: "alice@example.com"},
			Labels:      []string{"backend", "urgent"},
			Created:     "2025-01-15T10:30:00.000+0000",
			Updated:     "2025-01-16T14:20:00.000+0000",
		},
	}

	ti := jiraToTrackerIssue(ji)

	if ti.ID != "10001" {
		t.Errorf("ID = %q, want %q", ti.ID, "10001")
	}
	if ti.Identifier != "PROJ-42" {
		t.Errorf("Identifier = %q, want %q", ti.Identifier, "PROJ-42")
	}
	if ti.Title != "Fix login bug" {
		t.Errorf("Title = %q, want %q", ti.Title, "Fix login bug")
	}
	if ti.Description != "A plain text description" {
		t.Errorf("Description = %q, want %q", ti.Description, "A plain text description")
	}
	if ti.Assignee != "Alice" {
		t.Errorf("Assignee = %q, want %q", ti.Assignee, "Alice")
	}
	if ti.AssigneeEmail != "alice@example.com" {
		t.Errorf("AssigneeEmail = %q, want %q", ti.AssigneeEmail, "alice@example.com")
	}
	if ti.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if ti.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
	if len(ti.Labels) != 2 {
		t.Errorf("Labels length = %d, want 2", len(ti.Labels))
	}
}

func TestDescriptionToPlainText(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"null", json.RawMessage(`null`), ""},
		{"empty", json.RawMessage(``), ""},
		{"plain string", json.RawMessage(`"hello world"`), "hello world"},
		{"ADF document", json.RawMessage(`{
			"type": "doc",
			"content": [
				{
					"type": "paragraph",
					"content": [
						{"type": "text", "text": "First paragraph"}
					]
				},
				{
					"type": "paragraph",
					"content": [
						{"type": "text", "text": "Second paragraph"}
					]
				}
			]
		}`), "First paragraph\nSecond paragraph"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DescriptionToPlainText(tt.raw)
			if got != tt.want {
				t.Errorf("DescriptionToPlainText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlainTextToADF(t *testing.T) {
	adf := PlainTextToADF("Hello\nWorld")
	if adf == nil {
		t.Fatal("PlainTextToADF returned nil")
	}

	var doc struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
		Content []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(adf, &doc); err != nil {
		t.Fatalf("Failed to parse ADF: %v", err)
	}
	if doc.Type != "doc" {
		t.Errorf("doc type = %q, want %q", doc.Type, "doc")
	}
	if len(doc.Content) != 2 {
		t.Fatalf("content length = %d, want 2", len(doc.Content))
	}
	if doc.Content[0].Content[0].Text != "Hello" {
		t.Errorf("first paragraph text = %q, want %q", doc.Content[0].Content[0].Text, "Hello")
	}
}

func TestFieldMapperIssueToBeads(t *testing.T) {
	ji := &Issue{
		ID:   "10001",
		Key:  "PROJ-42",
		Self: "https://company.atlassian.net/rest/api/3/issue/10001",
		Fields: IssueFields{
			Summary:     "Test issue",
			Description: json.RawMessage(`"Description text"`),
			Status:      &StatusField{Name: "In Progress"},
			Priority:    &PriorityField{Name: "High"},
			IssueType:   &IssueTypeField{Name: "Bug"},
			Assignee:    &UserField{DisplayName: "Bob"},
			Labels:      []string{"frontend"},
			Created:     time.Now().Format(time.RFC3339),
			Updated:     time.Now().Format(time.RFC3339),
		},
	}

	ti := jiraToTrackerIssue(ji)
	mapper := &jiraFieldMapper{}
	conv := mapper.IssueToBeads(&ti)

	if conv == nil {
		t.Fatal("IssueToBeads returned nil")
	}
	if conv.Issue.Title != "Test issue" {
		t.Errorf("Title = %q, want %q", conv.Issue.Title, "Test issue")
	}
	if conv.Issue.Description != "Description text" {
		t.Errorf("Description = %q, want %q", conv.Issue.Description, "Description text")
	}
	if conv.Issue.Priority != 1 {
		t.Errorf("Priority = %d, want 1 (High)", conv.Issue.Priority)
	}
	if conv.Issue.Owner != "Bob" {
		t.Errorf("Owner = %q, want %q", conv.Issue.Owner, "Bob")
	}
}

func TestFieldMapperIssueToTracker(t *testing.T) {
	mapper := &jiraFieldMapper{}

	issue := &types.Issue{
		Title:       "New feature",
		Description: "Feature description",
		Priority:    0,
		IssueType:   types.TypeBug,
		Labels:      []string{"critical"},
	}

	fields := mapper.IssueToTracker(issue)

	if fields["summary"] != "New feature" {
		t.Errorf("summary = %v, want %q", fields["summary"], "New feature")
	}
	if fields["description"] == nil {
		t.Error("description should not be nil")
	}
	issueType, ok := fields["issuetype"].(map[string]string)
	if !ok || issueType["name"] != "Bug" {
		t.Errorf("issuetype = %v, want Bug", fields["issuetype"])
	}
	priority, ok := fields["priority"].(map[string]string)
	if !ok || priority["name"] != "Highest" {
		t.Errorf("priority = %v, want Highest", fields["priority"])
	}
}
