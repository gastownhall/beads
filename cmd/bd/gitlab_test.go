// Package main provides the bd CLI commands.
package main

import (
	"strings"
	"testing"
)

// TestGitLabConfigFromEnv verifies config is read from environment variables.
func TestGitLabConfigFromEnv(t *testing.T) {
	// Clear global state to avoid stale connections from prior tests
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	// Set environment variables
	t.Setenv("GITLAB_URL", "https://gitlab.example.com")
	t.Setenv("GITLAB_TOKEN", "test-token-123")
	t.Setenv("GITLAB_PROJECT_ID", "42")

	config := getGitLabConfig()

	if config.URL != "https://gitlab.example.com" {
		t.Errorf("URL = %q, want %q", config.URL, "https://gitlab.example.com")
	}
	if config.Token != "test-token-123" {
		t.Errorf("Token = %q, want %q", config.Token, "test-token-123")
	}
	if config.ProjectID != "42" {
		t.Errorf("ProjectID = %q, want %q", config.ProjectID, "42")
	}
}

// TestGitLabConfigValidation verifies validation catches missing required fields.
func TestGitLabConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		config    GitLabConfig
		wantError string
	}{
		{
			name:      "missing URL",
			config:    GitLabConfig{Token: "tok", ProjectID: "1"},
			wantError: "gitlab.url",
		},
		{
			name:      "missing token",
			config:    GitLabConfig{URL: "https://gitlab.com", ProjectID: "1"},
			wantError: "gitlab.token",
		},
		{
			name:      "missing project_id and group_id",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok"},
			wantError: "gitlab.project_id or gitlab.group_id",
		},
		{
			name:      "all present",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok", ProjectID: "1"},
			wantError: "",
		},
		{
			name:      "group_id only (no project_id) is valid",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok", GroupID: "mygroup"},
			wantError: "",
		},
		{
			name:      "group_id with default_project_id is valid",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok", GroupID: "mygroup", DefaultProjectID: "123"},
			wantError: "",
		},
		{
			name:      "both project_id and group_id is valid",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok", ProjectID: "1", GroupID: "mygroup"},
			wantError: "",
		},
		{
			name:      "plain HTTP rejected",
			config:    GitLabConfig{URL: "http://gitlab.example.com", Token: "tok", ProjectID: "1"},
			wantError: "HTTPS",
		},
		{
			name:      "localhost HTTP allowed",
			config:    GitLabConfig{URL: "http://localhost:8080", Token: "tok", ProjectID: "1"},
			wantError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGitLabConfig(tt.config)
			if tt.wantError == "" {
				if err != nil {
					t.Errorf("validateGitLabConfig() = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Error("validateGitLabConfig() = nil, want error")
				} else if !strings.Contains(err.Error(), tt.wantError) {
					t.Errorf("validateGitLabConfig() = %v, want error containing %q", err, tt.wantError)
				}
			}
		})
	}
}

// TestMaskGitLabToken verifies token masking for display.
func TestMaskGitLabToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "normal token",
			token: "glpat-xxxxxxxxxxxxxxxxxxxx",
			want:  "glpa****",
		},
		{
			name:  "short token",
			token: "abc",
			want:  "****",
		},
		{
			name:  "empty token",
			token: "",
			want:  "(not set)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskGitLabToken(tt.token)
			if got != tt.want {
				t.Errorf("maskGitLabToken(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

// TestGitLabConfigEnvVar verifies environment variable mapping.
func TestGitLabConfigEnvVar(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"gitlab.url", "GITLAB_URL"},
		{"gitlab.token", "GITLAB_TOKEN"},
		{"gitlab.project_id", "GITLAB_PROJECT_ID"},
		{"gitlab.group_id", "GITLAB_GROUP_ID"},
		{"gitlab.default_project_id", "GITLAB_DEFAULT_PROJECT_ID"},
		{"gitlab.filter_labels", "GITLAB_FILTER_LABELS"},
		{"gitlab.filter_project", "GITLAB_FILTER_PROJECT"},
		{"gitlab.filter_milestone", "GITLAB_FILTER_MILESTONE"},
		{"gitlab.filter_assignee", "GITLAB_FILTER_ASSIGNEE"},
		{"gitlab.unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := gitlabConfigToEnvVar(tt.key)
			if got != tt.want {
				t.Errorf("gitlabConfigToEnvVar(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// TestGitLabClientCreation verifies client is created with correct config.
func TestGitLabClientCreation(t *testing.T) {
	// Clear global state to avoid stale connections from prior tests
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("GITLAB_URL", "https://gitlab.test.com")
	t.Setenv("GITLAB_TOKEN", "test-token-abc")
	t.Setenv("GITLAB_PROJECT_ID", "99")

	config := getGitLabConfig()
	client := getGitLabClient(config)

	if client.BaseURL != "https://gitlab.test.com" {
		t.Errorf("client.BaseURL = %q, want %q", client.BaseURL, "https://gitlab.test.com")
	}
	if client.Token != "test-token-abc" {
		t.Errorf("client.Token = %q, want %q", client.Token, "test-token-abc")
	}
	if client.ProjectID != "99" {
		t.Errorf("client.ProjectID = %q, want %q", client.ProjectID, "99")
	}
}

// TestGitLabConfigFromEnv_GroupID verifies group config is read from environment variables.
func TestGitLabConfigFromEnv_GroupID(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("GITLAB_URL", "https://gitlab.example.com")
	t.Setenv("GITLAB_TOKEN", "test-token-123")
	t.Setenv("GITLAB_GROUP_ID", "mygroup")
	t.Setenv("GITLAB_DEFAULT_PROJECT_ID", "456")

	config := getGitLabConfig()

	if config.GroupID != "mygroup" {
		t.Errorf("GroupID = %q, want %q", config.GroupID, "mygroup")
	}
	if config.DefaultProjectID != "456" {
		t.Errorf("DefaultProjectID = %q, want %q", config.DefaultProjectID, "456")
	}
}

// TestGitLabClientCreation_WithGroupID verifies client is created with GroupID when configured.
func TestGitLabClientCreation_WithGroupID(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("GITLAB_URL", "https://gitlab.test.com")
	t.Setenv("GITLAB_TOKEN", "test-token-abc")
	t.Setenv("GITLAB_PROJECT_ID", "99")
	t.Setenv("GITLAB_GROUP_ID", "mygroup")

	config := getGitLabConfig()
	client := getGitLabClient(config)

	if client.GroupID != "mygroup" {
		t.Errorf("client.GroupID = %q, want %q", client.GroupID, "mygroup")
	}
	if client.ProjectID != "99" {
		t.Errorf("client.ProjectID = %q, want %q", client.ProjectID, "99")
	}
}

// TestGitLabCmdRegistration verifies the gitlab command and subcommands are registered.
func TestGitLabCmdRegistration(t *testing.T) {
	// Check that gitlabCmd has expected subcommands
	subcommands := gitlabCmd.Commands()

	var hasSync, hasStatus, hasProjects bool
	for _, cmd := range subcommands {
		switch cmd.Name() {
		case "sync":
			hasSync = true
		case "status":
			hasStatus = true
		case "projects":
			hasProjects = true
		}
	}

	if !hasSync {
		t.Error("gitlabCmd missing 'sync' subcommand")
	}
	if !hasStatus {
		t.Error("gitlabCmd missing 'status' subcommand")
	}
	if !hasProjects {
		t.Error("gitlabCmd missing 'projects' subcommand")
	}
}

// TestBuildCLIFilter_NoFlags verifies nil when no flags set.
func TestBuildCLIFilter_NoFlags(t *testing.T) {
	// Save and restore global flag state
	savedLabel, savedProject, savedMilestone, savedAssignee := gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee
	t.Cleanup(func() {
		gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee = savedLabel, savedProject, savedMilestone, savedAssignee
	})

	gitlabFilterLabel = ""
	gitlabFilterProject = ""
	gitlabFilterMilestone = ""
	gitlabFilterAssignee = ""

	filter := buildCLIFilter()
	if filter != nil {
		t.Errorf("buildCLIFilter() = %+v, want nil when no flags set", filter)
	}
}

// TestBuildCLIFilter_WithFlags verifies filter is built from flags.
func TestBuildCLIFilter_WithFlags(t *testing.T) {
	savedLabel, savedProject, savedMilestone, savedAssignee := gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee
	t.Cleanup(func() {
		gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee = savedLabel, savedProject, savedMilestone, savedAssignee
	})

	gitlabFilterLabel = "bug,backend"
	gitlabFilterProject = "42"
	gitlabFilterMilestone = "Sprint 1"
	gitlabFilterAssignee = "kyriakos"

	filter := buildCLIFilter()
	if filter == nil {
		t.Fatal("buildCLIFilter() = nil, want non-nil")
	}
	if filter.Labels != "bug,backend" {
		t.Errorf("Labels = %q, want %q", filter.Labels, "bug,backend")
	}
	if filter.ProjectID != 42 {
		t.Errorf("ProjectID = %d, want 42", filter.ProjectID)
	}
	if filter.Milestone != "Sprint 1" {
		t.Errorf("Milestone = %q, want %q", filter.Milestone, "Sprint 1")
	}
	if filter.Assignee != "kyriakos" {
		t.Errorf("Assignee = %q, want %q", filter.Assignee, "kyriakos")
	}
}

// TestBuildCLIFilter_PartialFlags verifies filter works with some flags.
func TestBuildCLIFilter_PartialFlags(t *testing.T) {
	savedLabel, savedProject, savedMilestone, savedAssignee := gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee
	t.Cleanup(func() {
		gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee = savedLabel, savedProject, savedMilestone, savedAssignee
	})

	gitlabFilterLabel = "frontend"
	gitlabFilterProject = ""
	gitlabFilterMilestone = ""
	gitlabFilterAssignee = ""

	filter := buildCLIFilter()
	if filter == nil {
		t.Fatal("buildCLIFilter() = nil, want non-nil")
	}
	if filter.Labels != "frontend" {
		t.Errorf("Labels = %q, want %q", filter.Labels, "frontend")
	}
	if filter.ProjectID != 0 {
		t.Errorf("ProjectID = %d, want 0", filter.ProjectID)
	}
}

// TestSyncCmdHasFilterFlags verifies filter flags are registered on sync command.
func TestSyncCmdHasFilterFlags(t *testing.T) {
	flags := []string{"label", "project", "milestone", "assignee", "type", "exclude-type", "no-ephemeral"}
	for _, name := range flags {
		f := gitlabSyncCmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("sync command missing --%s flag", name)
		}
	}
}

// TestParseTypeList verifies comma-separated type parsing.
func TestParseTypeList(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"epic", 1},
		{"epic,task,feature", 3},
		{" epic , task ", 2},
	}
	for _, tt := range tests {
		got := parseTypeList(tt.input)
		if len(got) != tt.want {
			t.Errorf("parseTypeList(%q) len = %d, want %d", tt.input, len(got), tt.want)
		}
	}
}

// TestNoEphemeralDefaultTrue verifies --no-ephemeral defaults to true.
func TestNoEphemeralDefaultTrue(t *testing.T) {
	f := gitlabSyncCmd.Flags().Lookup("no-ephemeral")
	if f == nil {
		t.Fatal("missing --no-ephemeral flag")
	}
	if f.DefValue != "true" {
		t.Errorf("--no-ephemeral default = %q, want %q", f.DefValue, "true")
	}
}
