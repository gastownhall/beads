// Package main provides the bd CLI commands.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/gitlab"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// GitLabConfig holds GitLab connection configuration.
type GitLabConfig struct {
	URL       string // GitLab instance URL (e.g., "https://gitlab.com")
	Token     string // Personal access token
	ProjectID string // Project ID or URL-encoded path
}

// gitlabCmd is the root command for GitLab operations.
var gitlabCmd = &cobra.Command{
	Use:   "gitlab",
	Short: "GitLab integration commands",
	Long: `Commands for syncing issues between beads and GitLab.

Configuration can be set via 'bd config' or environment variables:
  gitlab.url / GITLAB_URL         - GitLab instance URL
  gitlab.token / GITLAB_TOKEN     - Personal access token
  gitlab.project_id / GITLAB_PROJECT_ID - Project ID or path`,
}

// gitlabSyncCmd synchronizes issues between beads and GitLab.
var gitlabSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync issues with GitLab",
	Long: `Synchronize issues between beads and GitLab.

By default, performs bidirectional sync:
- Pulls new/updated issues from GitLab to beads
- Pushes local beads issues to GitLab

Use --pull-only or --push-only to limit direction.`,
	RunE: runGitLabSync,
}

// gitlabStatusCmd displays GitLab configuration and sync status.
var gitlabStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show GitLab sync status",
	Long:  `Display current GitLab configuration and sync status.`,
	RunE:  runGitLabStatus,
}

// gitlabProjectsCmd lists accessible GitLab projects.
var gitlabProjectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "List accessible GitLab projects",
	Long:  `List GitLab projects that the configured token has access to.`,
	RunE:  runGitLabProjects,
}

var (
	gitlabSyncDryRun   bool
	gitlabSyncPullOnly bool
	gitlabSyncPushOnly bool
)

func init() {
	// Add subcommands to gitlab
	gitlabCmd.AddCommand(gitlabSyncCmd)
	gitlabCmd.AddCommand(gitlabStatusCmd)
	gitlabCmd.AddCommand(gitlabProjectsCmd)

	// Add flags to sync command
	gitlabSyncCmd.Flags().BoolVar(&gitlabSyncDryRun, "dry-run", false, "Show what would be synced without making changes")
	gitlabSyncCmd.Flags().BoolVar(&gitlabSyncPullOnly, "pull-only", false, "Only pull issues from GitLab")
	gitlabSyncCmd.Flags().BoolVar(&gitlabSyncPushOnly, "push-only", false, "Only push issues to GitLab")

	// Register gitlab command with root
	rootCmd.AddCommand(gitlabCmd)
}

// getGitLabConfig returns GitLab configuration from bd config or environment.
func getGitLabConfig() GitLabConfig {
	ctx := context.Background()
	config := GitLabConfig{}

	config.URL, _ = getGitLabConfigValue(ctx, "gitlab.url")
	config.Token, _ = getGitLabConfigValue(ctx, "gitlab.token")
	config.ProjectID, _ = getGitLabConfigValue(ctx, "gitlab.project_id")

	return config
}

// getGitLabConfigValue reads a GitLab configuration value from store or environment.
func getGitLabConfigValue(ctx context.Context, key string) (value string, source string) {
	// Try to read from store (works in direct mode)
	if store != nil {
		value, _ = store.GetConfig(ctx, key)
		if value != "" {
			return value, "project config (bd config)"
		}
	} else if dbPath != "" {
		tempStore, err := sqlite.NewWithTimeout(ctx, dbPath, 5*time.Second)
		if err == nil {
			defer func() { _ = tempStore.Close() }()
			value, _ = tempStore.GetConfig(ctx, key)
			if value != "" {
				return value, "project config (bd config)"
			}
		}
	}

	// Fall back to environment variable
	envKey := gitlabConfigToEnvVar(key)
	if envKey != "" {
		value = os.Getenv(envKey)
		if value != "" {
			return value, fmt.Sprintf("environment variable (%s)", envKey)
		}
	}

	return "", ""
}

// gitlabConfigToEnvVar maps GitLab config keys to their environment variable names.
func gitlabConfigToEnvVar(key string) string {
	switch key {
	case "gitlab.url":
		return "GITLAB_URL"
	case "gitlab.token":
		return "GITLAB_TOKEN"
	case "gitlab.project_id":
		return "GITLAB_PROJECT_ID"
	default:
		return ""
	}
}

// validateGitLabConfig checks that required configuration is present.
func validateGitLabConfig(config GitLabConfig) error {
	if config.URL == "" {
		return fmt.Errorf("gitlab.url is not configured. Set via 'bd config gitlab.url <url>' or GITLAB_URL environment variable")
	}
	if config.Token == "" {
		return fmt.Errorf("gitlab.token is not configured. Set via 'bd config gitlab.token <token>' or GITLAB_TOKEN environment variable")
	}
	if config.ProjectID == "" {
		return fmt.Errorf("gitlab.project_id is not configured. Set via 'bd config gitlab.project_id <id>' or GITLAB_PROJECT_ID environment variable")
	}
	return nil
}

// maskGitLabToken masks a token for safe display.
func maskGitLabToken(token string) string {
	if token == "" {
		return "(not set)"
	}
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "****" + token[len(token)-4:]
}

// getGitLabClient creates a GitLab client from the current configuration.
func getGitLabClient(config GitLabConfig) *gitlab.Client {
	return gitlab.NewClient(config.Token, config.URL, config.ProjectID)
}

// runGitLabStatus implements the gitlab status command.
func runGitLabStatus(cmd *cobra.Command, args []string) error {
	config := getGitLabConfig()

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "GitLab Configuration")
	fmt.Fprintln(out, "====================")
	fmt.Fprintf(out, "URL:        %s\n", config.URL)
	fmt.Fprintf(out, "Token:      %s\n", maskGitLabToken(config.Token))
	fmt.Fprintf(out, "Project ID: %s\n", config.ProjectID)

	// Validate configuration
	if err := validateGitLabConfig(config); err != nil {
		fmt.Fprintf(out, "\nStatus: ❌ Not configured\n")
		fmt.Fprintf(out, "Error: %v\n", err)
		return nil
	}

	fmt.Fprintf(out, "\nStatus: ✓ Configured\n")
	return nil
}

// runGitLabProjects implements the gitlab projects command.
func runGitLabProjects(cmd *cobra.Command, args []string) error {
	config := getGitLabConfig()
	if err := validateGitLabConfig(config); err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	// Fetch projects from GitLab API
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", config.URL+"/api/v4/projects?membership=true&per_page=100", nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", config.Token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch projects: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitLab API error: %s (status %d)", string(body), resp.StatusCode)
	}

	var projects []struct {
		ID                int    `json:"id"`
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
		WebURL            string `json:"web_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	fmt.Fprintln(out, "Accessible GitLab Projects")
	fmt.Fprintln(out, "==========================")
	for _, p := range projects {
		fmt.Fprintf(out, "ID: %d\n", p.ID)
		fmt.Fprintf(out, "  Name: %s\n", p.Name)
		fmt.Fprintf(out, "  Path: %s\n", p.PathWithNamespace)
		fmt.Fprintf(out, "  URL:  %s\n", p.WebURL)
		fmt.Fprintln(out)
	}

	if len(projects) == 0 {
		fmt.Fprintln(out, "No projects found (or no membership access)")
	}

	return nil
}

// runGitLabSync implements the gitlab sync command.
func runGitLabSync(cmd *cobra.Command, args []string) error {
	config := getGitLabConfig()
	if err := validateGitLabConfig(config); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	client := getGitLabClient(config)
	ctx := context.Background()

	if gitlabSyncDryRun {
		fmt.Fprintln(out, "Dry run mode - no changes will be made")
		fmt.Fprintln(out)
	}

	// Fetch issues from GitLab
	fmt.Fprintln(out, "Fetching issues from GitLab...")
	issues, err := client.FetchIssues(ctx, "all")
	if err != nil {
		return fmt.Errorf("failed to fetch issues: %w", err)
	}

	fmt.Fprintf(out, "Found %d issues\n", len(issues))
	fmt.Fprintln(out)

	// Display issues for dry run
	if gitlabSyncDryRun {
		fmt.Fprintln(out, "Issues to sync:")
		fmt.Fprintln(out, "---------------")
		for _, issue := range issues {
			fmt.Fprintf(out, "#%d: %s [%s]\n", issue.IID, issue.Title, issue.State)
		}
		return nil
	}

	// TODO: Implement actual sync with beads
	fmt.Fprintln(out, "Full sync not yet implemented - use --dry-run to preview")
	return nil
}
