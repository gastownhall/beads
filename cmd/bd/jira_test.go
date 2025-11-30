package main

import (
	"testing"
)

func TestIsJiraExternalRef(t *testing.T) {
	tests := []struct {
		name        string
		externalRef string
		jiraURL     string
		want        bool
	}{
		{
			name:        "valid Jira Cloud URL",
			externalRef: "https://company.atlassian.net/browse/PROJ-123",
			jiraURL:     "https://company.atlassian.net",
			want:        true,
		},
		{
			name:        "valid Jira Cloud URL with trailing slash in config",
			externalRef: "https://company.atlassian.net/browse/PROJ-123",
			jiraURL:     "https://company.atlassian.net/",
			want:        true,
		},
		{
			name:        "valid Jira Server URL",
			externalRef: "https://jira.company.com/browse/PROJ-456",
			jiraURL:     "https://jira.company.com",
			want:        true,
		},
		{
			name:        "mismatched Jira host",
			externalRef: "https://other.atlassian.net/browse/PROJ-123",
			jiraURL:     "https://company.atlassian.net",
			want:        false,
		},
		{
			name:        "GitHub issue URL",
			externalRef: "https://github.com/org/repo/issues/123",
			jiraURL:     "https://company.atlassian.net",
			want:        false,
		},
		{
			name:        "empty external_ref",
			externalRef: "",
			jiraURL:     "https://company.atlassian.net",
			want:        false,
		},
		{
			name:        "no jiraURL configured - valid pattern",
			externalRef: "https://any.atlassian.net/browse/PROJ-123",
			jiraURL:     "",
			want:        true,
		},
		{
			name:        "no jiraURL configured - invalid pattern",
			externalRef: "https://github.com/org/repo/issues/123",
			jiraURL:     "",
			want:        false,
		},
		{
			name:        "browse in path but not Jira format",
			externalRef: "https://example.com/browse/docs/page",
			jiraURL:     "",
			want:        true, // Contains /browse/, so matches pattern
		},
		{
			name:        "browse in path with jiraURL check",
			externalRef: "https://example.com/browse/docs/page",
			jiraURL:     "https://company.atlassian.net",
			want:        false, // Host doesn't match
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isJiraExternalRef(tt.externalRef, tt.jiraURL)
			if got != tt.want {
				t.Errorf("isJiraExternalRef(%q, %q) = %v, want %v",
					tt.externalRef, tt.jiraURL, got, tt.want)
			}
		})
	}
}

func TestJiraSyncStats(t *testing.T) {
	// Test that stats struct initializes correctly
	stats := JiraSyncStats{}

	if stats.Pulled != 0 {
		t.Errorf("expected Pulled to be 0, got %d", stats.Pulled)
	}
	if stats.Pushed != 0 {
		t.Errorf("expected Pushed to be 0, got %d", stats.Pushed)
	}
	if stats.Created != 0 {
		t.Errorf("expected Created to be 0, got %d", stats.Created)
	}
	if stats.Updated != 0 {
		t.Errorf("expected Updated to be 0, got %d", stats.Updated)
	}
	if stats.Skipped != 0 {
		t.Errorf("expected Skipped to be 0, got %d", stats.Skipped)
	}
	if stats.Errors != 0 {
		t.Errorf("expected Errors to be 0, got %d", stats.Errors)
	}
	if stats.Conflicts != 0 {
		t.Errorf("expected Conflicts to be 0, got %d", stats.Conflicts)
	}
}

func TestJiraSyncResult(t *testing.T) {
	// Test result struct initialization
	result := JiraSyncResult{
		Success: true,
		Stats: JiraSyncStats{
			Created: 5,
			Updated: 3,
		},
		LastSync: "2025-01-15T10:30:00Z",
	}

	if !result.Success {
		t.Error("expected Success to be true")
	}
	if result.Stats.Created != 5 {
		t.Errorf("expected Created to be 5, got %d", result.Stats.Created)
	}
	if result.Stats.Updated != 3 {
		t.Errorf("expected Updated to be 3, got %d", result.Stats.Updated)
	}
	if result.LastSync != "2025-01-15T10:30:00Z" {
		t.Errorf("unexpected LastSync value: %s", result.LastSync)
	}
	if result.Error != "" {
		t.Errorf("expected Error to be empty, got %s", result.Error)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected Warnings to be empty, got %v", result.Warnings)
	}
}

func TestPullStats(t *testing.T) {
	stats := PullStats{
		Created: 10,
		Updated: 5,
		Skipped: 2,
	}

	if stats.Created != 10 {
		t.Errorf("expected Created to be 10, got %d", stats.Created)
	}
	if stats.Updated != 5 {
		t.Errorf("expected Updated to be 5, got %d", stats.Updated)
	}
	if stats.Skipped != 2 {
		t.Errorf("expected Skipped to be 2, got %d", stats.Skipped)
	}
}

func TestPushStats(t *testing.T) {
	stats := PushStats{
		Created: 8,
		Updated: 4,
		Skipped: 1,
		Errors:  2,
	}

	if stats.Created != 8 {
		t.Errorf("expected Created to be 8, got %d", stats.Created)
	}
	if stats.Updated != 4 {
		t.Errorf("expected Updated to be 4, got %d", stats.Updated)
	}
	if stats.Skipped != 1 {
		t.Errorf("expected Skipped to be 1, got %d", stats.Skipped)
	}
	if stats.Errors != 2 {
		t.Errorf("expected Errors to be 2, got %d", stats.Errors)
	}
}
