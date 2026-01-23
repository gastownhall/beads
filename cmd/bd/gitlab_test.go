// Package main provides the bd CLI commands.
package main

import (
	"os"
	"strings"
	"testing"
)

// TestGitLabConfigFromEnv verifies config is read from environment variables.
func TestGitLabConfigFromEnv(t *testing.T) {
	// Set environment variables
	os.Setenv("GITLAB_URL", "https://gitlab.example.com")
	os.Setenv("GITLAB_TOKEN", "test-token-123")
	os.Setenv("GITLAB_PROJECT_ID", "42")
	defer func() {
		os.Unsetenv("GITLAB_URL")
		os.Unsetenv("GITLAB_TOKEN")
		os.Unsetenv("GITLAB_PROJECT_ID")
	}()

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
			name:      "missing project_id",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok"},
			wantError: "gitlab.project_id",
		},
		{
			name:      "all present",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok", ProjectID: "1"},
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
			want:  "glpa****xxxx",
		},
		{
			name:  "short token",
			token: "abc",
			want:  "***",
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
	os.Setenv("GITLAB_URL", "https://gitlab.test.com")
	os.Setenv("GITLAB_TOKEN", "test-token-abc")
	os.Setenv("GITLAB_PROJECT_ID", "99")
	defer func() {
		os.Unsetenv("GITLAB_URL")
		os.Unsetenv("GITLAB_TOKEN")
		os.Unsetenv("GITLAB_PROJECT_ID")
	}()

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
