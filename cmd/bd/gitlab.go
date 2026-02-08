// Package main provides the bd CLI commands.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/gitlab"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
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
	gitlabPreferLocal  bool
	gitlabPreferGitLab bool
	gitlabPreferNewer  bool
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

	// Conflict resolution flags (mutually exclusive)
	gitlabSyncCmd.Flags().BoolVar(&gitlabPreferLocal, "prefer-local", false, "On conflict, keep local beads version")
	gitlabSyncCmd.Flags().BoolVar(&gitlabPreferGitLab, "prefer-gitlab", false, "On conflict, use GitLab version")
	gitlabSyncCmd.Flags().BoolVar(&gitlabPreferNewer, "prefer-newer", false, "On conflict, use most recent version (default)")

	// Register gitlab command with root
	rootCmd.AddCommand(gitlabCmd)
}

// getGitLabConfig returns GitLab configuration from bd config or environment.
func getGitLabConfig() GitLabConfig {
	ctx := context.Background()
	config := GitLabConfig{}

	config.URL = getGitLabConfigValue(ctx, "gitlab.url")
	config.Token = getGitLabConfigValue(ctx, "gitlab.token")
	config.ProjectID = getGitLabConfigValue(ctx, "gitlab.project_id")

	return config
}

// getGitLabConfigValue reads a GitLab configuration value from store or environment.
func getGitLabConfigValue(ctx context.Context, key string) string {
	// Try to read from store (works in direct mode)
	if store != nil {
		value, _ := store.GetConfig(ctx, key)
		if value != "" {
			return value
		}
	} else if dbPath != "" {
		tempStore, err := sqlite.NewWithTimeout(ctx, dbPath, 5*time.Second)
		if err == nil {
			defer func() { _ = tempStore.Close() }()
			value, _ := tempStore.GetConfig(ctx, key)
			if value != "" {
				return value
			}
		}
	}

	// Fall back to environment variable
	envKey := gitlabConfigToEnvVar(key)
	if envKey != "" {
		if value := os.Getenv(envKey); value != "" {
			return value
		}
	}

	return ""
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
	// Reject non-HTTPS URLs to prevent sending tokens in cleartext.
	// Allow http://localhost and http://127.0.0.1 for local development/testing.
	if strings.HasPrefix(config.URL, "http://") &&
		!strings.HasPrefix(config.URL, "http://localhost") &&
		!strings.HasPrefix(config.URL, "http://127.0.0.1") {
		return fmt.Errorf("gitlab.url must use HTTPS (got %q). Use HTTPS to protect your access token", config.URL)
	}
	return nil
}

// maskGitLabToken masks a token for safe display.
// Shows only the first 4 characters to aid identification without
// revealing enough to reduce brute-force entropy.
func maskGitLabToken(token string) string {
	if token == "" {
		return "(not set)"
	}
	if len(token) <= 4 {
		return "****"
	}
	return token[:4] + "****"
}

// getGitLabClient creates a GitLab client from the current configuration.
func getGitLabClient(config GitLabConfig) *gitlab.Client {
	return gitlab.NewClient(config.Token, config.URL, config.ProjectID)
}

// runGitLabStatus implements the gitlab status command.
func runGitLabStatus(cmd *cobra.Command, args []string) error {
	config := getGitLabConfig()

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "GitLab Configuration")
	_, _ = fmt.Fprintln(out, "====================")
	_, _ = fmt.Fprintf(out, "URL:        %s\n", config.URL)
	_, _ = fmt.Fprintf(out, "Token:      %s\n", maskGitLabToken(config.Token))
	_, _ = fmt.Fprintf(out, "Project ID: %s\n", config.ProjectID)

	// Validate configuration
	if err := validateGitLabConfig(config); err != nil {
		_, _ = fmt.Fprintf(out, "\nStatus: ❌ Not configured\n")
		_, _ = fmt.Fprintf(out, "Error: %v\n", err)
		return nil
	}

	_, _ = fmt.Fprintf(out, "\nStatus: ✓ Configured\n")
	return nil
}

// runGitLabProjects implements the gitlab projects command.
func runGitLabProjects(cmd *cobra.Command, args []string) error {
	config := getGitLabConfig()
	if err := validateGitLabConfig(config); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	client := getGitLabClient(config)
	ctx := context.Background()

	projects, err := client.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch projects: %w", err)
	}

	_, _ = fmt.Fprintln(out, "Accessible GitLab Projects")
	_, _ = fmt.Fprintln(out, "==========================")
	for _, p := range projects {
		_, _ = fmt.Fprintf(out, "ID: %d\n", p.ID)
		_, _ = fmt.Fprintf(out, "  Name: %s\n", p.Name)
		_, _ = fmt.Fprintf(out, "  Path: %s\n", p.PathWithNamespace)
		_, _ = fmt.Fprintf(out, "  URL:  %s\n", p.WebURL)
		_, _ = fmt.Fprintln(out)
	}

	if len(projects) == 0 {
		_, _ = fmt.Fprintln(out, "No projects found (or no membership access)")
	}

	return nil
}

// runGitLabSync implements the gitlab sync command.
// Uses SyncContext for thread-safe operations instead of global variables.
func runGitLabSync(cmd *cobra.Command, args []string) error {
	config := getGitLabConfig()
	if err := validateGitLabConfig(config); err != nil {
		return err
	}

	if !gitlabSyncDryRun {
		CheckReadonly("gitlab sync")
	}

	if gitlabSyncPullOnly && gitlabSyncPushOnly {
		return fmt.Errorf("cannot use both --pull-only and --push-only")
	}

	// Determine conflict strategy from flags
	conflictStrategy, err := getConflictStrategy(gitlabPreferLocal, gitlabPreferGitLab, gitlabPreferNewer)
	if err != nil {
		return fmt.Errorf("%w (--prefer-local, --prefer-gitlab, --prefer-newer)", err)
	}

	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	out := cmd.OutOrStdout()
	client := getGitLabClient(config)
	ctx := context.Background()
	mappingConfig := gitlab.DefaultMappingConfig()

	// Create SyncContext from globals for thread-safe operations
	syncCtx := NewSyncContext()
	syncCtx.SetStore(store)
	syncCtx.SetActor(actor)
	syncCtx.SetDBPath(dbPath)

	if gitlabSyncDryRun {
		_, _ = fmt.Fprintln(out, "Dry run mode - no changes will be made")
		_, _ = fmt.Fprintln(out)
	}

	// Default: both pull and push
	pull := !gitlabSyncPushOnly
	push := !gitlabSyncPullOnly

	result := &gitlab.SyncResult{Success: true}

	// Pull from GitLab
	if pull {
		if gitlabSyncDryRun {
			_, _ = fmt.Fprintln(out, "→ [DRY RUN] Would pull issues from GitLab")
		} else {
			_, _ = fmt.Fprintln(out, "→ Pulling issues from GitLab...")
		}

		pullStats, err := doPullFromGitLabWithContext(ctx, syncCtx, client, mappingConfig, gitlabSyncDryRun, "all", nil)
		if err != nil {
			result.Success = false
			result.Error = err.Error()
			_, _ = fmt.Fprintf(out, "Error pulling from GitLab: %v\n", err)
			return err
		}

		result.Stats.Pulled = pullStats.Created + pullStats.Updated
		result.Stats.Created += pullStats.Created
		result.Stats.Updated += pullStats.Updated
		result.Stats.Skipped += pullStats.Skipped

		if !gitlabSyncDryRun {
			_, _ = fmt.Fprintf(out, "✓ Pulled %d issues (%d created, %d updated)\n",
				result.Stats.Pulled, pullStats.Created, pullStats.Updated)
		}
	}

	// Detect conflicts BEFORE push to prevent data loss.
	// Conflict handling depends on strategy:
	// - prefer-local: force push conflicting issues (local wins)
	// - prefer-gitlab: skip conflicting issues, then update local from GitLab
	// - prefer-newer: compare timestamps, force push if local newer, else skip
	var conflicts []gitlab.Conflict
	skipUpdateIDs := make(map[string]bool)
	forceUpdateIDs := make(map[string]bool)

	if pull && push && !gitlabSyncDryRun {
		var localIssues []*types.Issue
		if syncCtx.Store() != nil {
			var err error
			localIssues, err = syncCtx.Store().SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				_, _ = fmt.Fprintf(out, "Warning: failed to get local issues for conflict detection: %v\n", err)
			} else {
				conflicts, err = detectGitLabConflictsWithContext(ctx, syncCtx, client, localIssues)
				if err != nil {
					_, _ = fmt.Fprintf(out, "Warning: failed to detect conflicts: %v\n", err)
				} else if len(conflicts) > 0 {
					// Handle conflicts based on strategy
					for _, c := range conflicts {
						switch conflictStrategy {
						case ConflictStrategyPreferLocal:
							// Local wins: force push to GitLab
							forceUpdateIDs[c.IssueID] = true
						case ConflictStrategyPreferGitLab:
							// GitLab wins: skip push, will update local in resolution
							skipUpdateIDs[c.IssueID] = true
						case ConflictStrategyPreferNewer:
							// Use newer version
							if c.LocalUpdated.After(c.GitLabUpdated) {
								// Local is newer: force push
								forceUpdateIDs[c.IssueID] = true
							} else {
								// GitLab is newer: skip push, will update local
								skipUpdateIDs[c.IssueID] = true
							}
						}
					}
					_, _ = fmt.Fprintf(out, "→ Detected %d conflicts (strategy: %s)\n", len(conflicts), conflictStrategy)
				}
			}
		}
	}

	// Push to GitLab
	if push {
		if gitlabSyncDryRun {
			_, _ = fmt.Fprintln(out, "→ [DRY RUN] Would push issues to GitLab")
		} else {
			_, _ = fmt.Fprintln(out, "→ Pushing issues to GitLab...")
		}

		// Get local issues to push
		var localIssues []*types.Issue
		if syncCtx.Store() != nil {
			var err error
			localIssues, err = syncCtx.Store().SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				return fmt.Errorf("failed to get local issues: %w", err)
			}
		}

		// Pass both forceUpdateIDs (for prefer-local) and skipUpdateIDs (for prefer-gitlab)
		pushStats, err := doPushToGitLabWithContext(ctx, syncCtx, client, mappingConfig, localIssues, gitlabSyncDryRun, false, forceUpdateIDs, skipUpdateIDs)
		if err != nil {
			result.Success = false
			result.Error = err.Error()
			_, _ = fmt.Fprintf(out, "Error pushing to GitLab: %v\n", err)
			return err
		}

		result.Stats.Pushed = pushStats.Created + pushStats.Updated
		result.Stats.Created += pushStats.Created
		result.Stats.Updated += pushStats.Updated
		result.Stats.Skipped += pushStats.Skipped

		if !gitlabSyncDryRun {
			_, _ = fmt.Fprintf(out, "✓ Pushed %d issues (%d created, %d updated)\n",
				result.Stats.Pushed, pushStats.Created, pushStats.Updated)
		}
	}

	// Resolve conflicts: update local from GitLab for issues where GitLab won
	// (prefer-gitlab always, prefer-newer when GitLab was newer)
	// For prefer-local, conflicts were force-pushed so no local update needed.
	if len(skipUpdateIDs) > 0 && !gitlabSyncDryRun {
		_, _ = fmt.Fprintf(out, "→ Updating %d local issues from GitLab...\n", len(skipUpdateIDs))
		// Filter conflicts to only those where GitLab version should be applied locally
		var conflictsToResolve []gitlab.Conflict
		for _, c := range conflicts {
			if skipUpdateIDs[c.IssueID] {
				conflictsToResolve = append(conflictsToResolve, c)
			}
		}
		if err := resolveGitLabConflictsWithContext(ctx, syncCtx, client, mappingConfig, conflictsToResolve, conflictStrategy); err != nil {
			_, _ = fmt.Fprintf(out, "Warning: failed to resolve some conflicts: %v\n", err)
		} else {
			_, _ = fmt.Fprintf(out, "✓ Updated %d local issues\n", len(conflictsToResolve))
		}
		result.Stats.Conflicts = len(conflicts)
	}

	if gitlabSyncDryRun {
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintln(out, "Run without --dry-run to apply changes")
	}

	return nil
}
