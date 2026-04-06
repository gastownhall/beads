// Package main provides the bd CLI commands.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/github"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// GitHubConfig holds GitHub connection configuration.
type GitHubConfig struct {
	Token      string // Personal access token
	Owner      string // Repository owner (user or organization)
	Repo       string // Repository name
	Repository string // Combined "owner/repo" format
	URL        string // Custom API URL (for GitHub Enterprise)
}

// githubCmd is the root command for GitHub operations.
var githubCmd = &cobra.Command{
	Use:   "github",
	Short: "GitHub integration commands",
	Long: `Commands for syncing issues between beads and GitHub.

Configuration can be set via 'bd config' or environment variables:
  github.token / GITHUB_TOKEN           - Personal access token
  github.owner / GITHUB_OWNER           - Repository owner
  github.repo / GITHUB_REPO             - Repository name
  github.repository / GITHUB_REPOSITORY - Combined "owner/repo" format
  github.url / GITHUB_API_URL           - Custom API URL (GitHub Enterprise)`,
}

// githubSyncCmd synchronizes issues between beads and GitHub.
var githubSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync issues with GitHub",
	Long: `Synchronize issues between beads and GitHub.

By default, performs bidirectional sync:
- Pulls new/updated issues from GitHub to beads
- Pushes local beads issues to GitHub

Use --pull-only or --push-only to limit direction.`,
	RunE: runGitHubSync,
}

// githubStatusCmd displays GitHub configuration and sync status.
var githubStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show GitHub sync status",
	Long:  `Display current GitHub configuration and sync status.`,
	RunE:  runGitHubStatus,
}

// githubReposCmd lists accessible GitHub repositories.
var githubReposCmd = &cobra.Command{
	Use:   "repos",
	Short: "List accessible GitHub repositories",
	Long:  `List GitHub repositories that the configured token has access to.`,
	RunE:  runGitHubRepos,
}

var (
	githubSyncDryRun   bool
	githubSyncPullOnly bool
	githubSyncPushOnly bool
	githubPreferLocal  bool
	githubPreferGitHub bool
	githubPreferNewer  bool
)

// GitHubConflictStrategy defines how to resolve conflicts between local and GitHub versions.
type GitHubConflictStrategy string

const (
	// GitHubConflictPreferNewer uses the most recently updated version (default).
	GitHubConflictPreferNewer GitHubConflictStrategy = "prefer-newer"
	// GitHubConflictPreferLocal always keeps the local beads version.
	GitHubConflictPreferLocal GitHubConflictStrategy = "prefer-local"
	// GitHubConflictPreferGitHub always uses the GitHub version.
	GitHubConflictPreferGitHub GitHubConflictStrategy = "prefer-github"
)

// getGitHubConflictStrategy determines the conflict strategy from flag values.
// Returns error if multiple conflicting flags are set.
func getGitHubConflictStrategy(preferLocal, preferGitHub, preferNewer bool) (GitHubConflictStrategy, error) {
	flagsSet := 0
	if preferLocal {
		flagsSet++
	}
	if preferGitHub {
		flagsSet++
	}
	if preferNewer {
		flagsSet++
	}
	if flagsSet > 1 {
		return "", fmt.Errorf("cannot use multiple conflict resolution flags")
	}

	if preferLocal {
		return GitHubConflictPreferLocal, nil
	}
	if preferGitHub {
		return GitHubConflictPreferGitHub, nil
	}
	return GitHubConflictPreferNewer, nil
}

// parseGitHubSourceSystem parses a source system string like "github:https://...:42"
// Returns the issue number and ok (whether it's a valid GitHub source).
func parseGitHubSourceSystem(sourceSystem string) (number int, ok bool) {
	if !strings.HasPrefix(sourceSystem, "github:") {
		return 0, false
	}

	// Find last ":" and parse the number after it
	lastColon := strings.LastIndex(sourceSystem, ":")
	if lastColon == -1 || lastColon == len(sourceSystem)-1 {
		return 0, false
	}

	var err error
	number, err = strconv.Atoi(sourceSystem[lastColon+1:])
	if err != nil {
		return 0, false
	}

	return number, true
}

func init() {
	// Add subcommands to github
	githubCmd.AddCommand(githubSyncCmd)
	githubCmd.AddCommand(githubStatusCmd)
	githubCmd.AddCommand(githubReposCmd)

	// Add flags to sync command
	githubSyncCmd.Flags().BoolVar(&githubSyncDryRun, "dry-run", false, "Show what would be synced without making changes")
	githubSyncCmd.Flags().BoolVar(&githubSyncPullOnly, "pull-only", false, "Only pull issues from GitHub")
	githubSyncCmd.Flags().BoolVar(&githubSyncPushOnly, "push-only", false, "Only push issues to GitHub")

	// Conflict resolution flags (mutually exclusive)
	githubSyncCmd.Flags().BoolVar(&githubPreferLocal, "prefer-local", false, "On conflict, keep local beads version")
	githubSyncCmd.Flags().BoolVar(&githubPreferGitHub, "prefer-github", false, "On conflict, use GitHub version")
	githubSyncCmd.Flags().BoolVar(&githubPreferNewer, "prefer-newer", false, "On conflict, use most recent version (default)")
	registerSelectiveSyncFlags(githubSyncCmd)

	// Register github command with root
	rootCmd.AddCommand(githubCmd)
}

// getGitHubConfig returns GitHub configuration from bd config or environment.
func getGitHubConfig() GitHubConfig {
	ctx := context.Background()
	config := GitHubConfig{}

	config.Token = getGitHubConfigValue(ctx, "github.token")
	config.Owner = getGitHubConfigValue(ctx, "github.owner")
	config.Repo = getGitHubConfigValue(ctx, "github.repo")
	config.Repository = getGitHubConfigValue(ctx, "github.repository")
	config.URL = getGitHubConfigValue(ctx, "github.url")

	// Parse combined owner/repo format if individual fields are empty
	if (config.Owner == "" || config.Repo == "") && config.Repository != "" {
		parts := strings.SplitN(config.Repository, "/", 2)
		if len(parts) == 2 {
			if config.Owner == "" {
				config.Owner = parts[0]
			}
			if config.Repo == "" {
				config.Repo = parts[1]
			}
		}
	}

	return config
}

// getGitHubConfigValue reads a GitHub configuration value from store or environment.
func getGitHubConfigValue(ctx context.Context, key string) string {
	// Try to read from store (works in direct mode)
	if store != nil {
		value, _ := store.GetConfig(ctx, key)
		if value != "" {
			return value
		}
	} else if dbPath != "" {
		tempStore, err := openReadOnlyStoreForDBPath(ctx, dbPath)
		if err == nil {
			defer func() { _ = tempStore.Close() }()
			value, _ := tempStore.GetConfig(ctx, key)
			if value != "" {
				return value
			}
		}
	}

	// Fall back to environment variable
	envKey := githubConfigToEnvVar(key)
	if envKey != "" {
		if value := os.Getenv(envKey); value != "" {
			return value
		}
	}

	return ""
}

// githubConfigToEnvVar maps GitHub config keys to their environment variable names.
func githubConfigToEnvVar(key string) string {
	switch key {
	case "github.token":
		return "GITHUB_TOKEN"
	case "github.owner":
		return "GITHUB_OWNER"
	case "github.repo":
		return "GITHUB_REPO"
	case "github.repository":
		return "GITHUB_REPOSITORY"
	case "github.url":
		return "GITHUB_API_URL"
	default:
		return ""
	}
}

// validateGitHubConfig checks that required configuration is present.
func validateGitHubConfig(config GitHubConfig) error {
	if config.Token == "" {
		return fmt.Errorf("github.token is not configured. Set via 'bd config github.token <token>' or GITHUB_TOKEN environment variable")
	}
	if config.Owner == "" {
		return fmt.Errorf("github.owner is not configured. Set via 'bd config github.owner <owner>' or GITHUB_OWNER environment variable")
	}
	if config.Repo == "" {
		return fmt.Errorf("github.repo is not configured. Set via 'bd config github.repo <repo>' or GITHUB_REPO environment variable")
	}
	return nil
}

// maskGitHubToken masks a token for safe display.
// Shows only the first 4 characters to aid identification without
// revealing enough to reduce brute-force entropy.
func maskGitHubToken(token string) string {
	if token == "" {
		return "(not set)"
	}
	if len(token) <= 4 {
		return "****"
	}
	return token[:4] + "****"
}

// getGitHubClient creates a GitHub client from the current configuration.
func getGitHubClient(config GitHubConfig) *github.Client {
	client := github.NewClient(config.Token, config.Owner, config.Repo)
	if config.URL != "" {
		client = client.WithBaseURL(config.URL)
	}
	return client
}

// runGitHubStatus implements the github status command.
func runGitHubStatus(cmd *cobra.Command, args []string) error {
	config := getGitHubConfig()

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "GitHub Configuration")
	_, _ = fmt.Fprintln(out, "====================")
	_, _ = fmt.Fprintf(out, "Token:      %s\n", maskGitHubToken(config.Token))
	_, _ = fmt.Fprintf(out, "Owner:      %s\n", config.Owner)
	_, _ = fmt.Fprintf(out, "Repository: %s\n", config.Repo)
	if config.URL != "" {
		_, _ = fmt.Fprintf(out, "API URL:    %s\n", config.URL)
	}

	// Validate configuration
	if err := validateGitHubConfig(config); err != nil {
		_, _ = fmt.Fprintf(out, "\nStatus: ❌ Not configured\n")
		_, _ = fmt.Fprintf(out, "Error: %v\n", err)
		return nil
	}

	_, _ = fmt.Fprintf(out, "\nStatus: ✓ Configured\n")
	return nil
}

// runGitHubRepos implements the github repos command.
func runGitHubRepos(cmd *cobra.Command, args []string) error {
	config := getGitHubConfig()
	if config.Token == "" {
		return fmt.Errorf("github.token is not configured. Set via 'bd config github.token <token>' or GITHUB_TOKEN environment variable")
	}

	out := cmd.OutOrStdout()
	client := getGitHubClient(config)
	ctx := context.Background()

	repos, err := client.ListRepositories(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch repositories: %w", err)
	}

	_, _ = fmt.Fprintln(out, "Accessible GitHub Repositories")
	_, _ = fmt.Fprintln(out, "==============================")
	for _, r := range repos {
		_, _ = fmt.Fprintf(out, "  %s\n", r.FullName)
		if r.Description != "" {
			_, _ = fmt.Fprintf(out, "    %s\n", r.Description)
		}
		_, _ = fmt.Fprintf(out, "    %s\n", r.HTMLURL)
		_, _ = fmt.Fprintln(out)
	}

	if len(repos) == 0 {
		_, _ = fmt.Fprintln(out, "No repositories found")
	}

	return nil
}

// runGitHubSync implements the github sync command.
// Uses the tracker.Engine for all sync operations.
func runGitHubSync(cmd *cobra.Command, args []string) error {
	config := getGitHubConfig()
	if err := validateGitHubConfig(config); err != nil {
		return err
	}

	if !githubSyncDryRun {
		CheckReadonly("github sync")
	}

	if githubSyncPullOnly && githubSyncPushOnly {
		return fmt.Errorf("cannot use both --pull-only and --push-only")
	}

	// Validate conflict flags
	conflictStrategy, err := getGitHubConflictStrategy(githubPreferLocal, githubPreferGitHub, githubPreferNewer)
	if err != nil {
		return fmt.Errorf("%w (--prefer-local, --prefer-github, --prefer-newer)", err)
	}

	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	out := cmd.OutOrStdout()
	ctx := context.Background()

	// Create and initialize the GitHub tracker
	gt := &github.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing GitHub tracker: %w", err)
	}

	// Create the sync engine
	engine := tracker.NewEngine(gt, store, actor)
	engine.OnMessage = func(msg string) { _, _ = fmt.Fprintln(out, "  "+msg) }
	engine.OnWarning = func(msg string) { _, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	// Set up GitHub-specific pull hooks
	engine.PullHooks = buildGitHubPullHooks(ctx)

	// Build sync options from CLI flags
	pull := !githubSyncPushOnly
	push := !githubSyncPullOnly

	opts := tracker.SyncOptions{
		Pull:   pull,
		Push:   push,
		DryRun: githubSyncDryRun,
	}

	if err := applySelectiveSyncFlags(cmd, &opts, push); err != nil {
		return err
	}

	// Map conflict resolution
	switch conflictStrategy {
	case GitHubConflictPreferLocal:
		opts.ConflictResolution = tracker.ConflictLocal
	case GitHubConflictPreferGitHub:
		opts.ConflictResolution = tracker.ConflictExternal
	default:
		opts.ConflictResolution = tracker.ConflictTimestamp
	}

	if githubSyncDryRun {
		_, _ = fmt.Fprintln(out, "Dry run mode - no changes will be made")
		_, _ = fmt.Fprintln(out)
	}

	// Run sync
	result, err := engine.Sync(ctx, opts)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return err
	}

	// Output results
	if !githubSyncDryRun {
		if result.Stats.Pulled > 0 {
			_, _ = fmt.Fprintf(out, "✓ Pulled %d issues (%d created, %d updated)\n",
				result.Stats.Pulled, result.Stats.Created, result.Stats.Updated)
		}
		if result.Stats.Pushed > 0 {
			_, _ = fmt.Fprintf(out, "✓ Pushed %d issues\n", result.Stats.Pushed)
		}
		if result.Stats.Conflicts > 0 {
			_, _ = fmt.Fprintf(out, "→ Resolved %d conflicts\n", result.Stats.Conflicts)
		}
	}

	if githubSyncDryRun {
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintln(out, "Run without --dry-run to apply changes")
	}

	return nil
}

// buildGitHubPullHooks creates PullHooks for GitHub-specific pull behavior.
func buildGitHubPullHooks(ctx context.Context) *tracker.PullHooks {
	prefix := "bd"
	// YAML config takes precedence — in shared-server mode the DB
	// may belong to a different project (GH#2469).
	if p := config.GetString("issue-prefix"); p != "" {
		prefix = p
	} else if store != nil {
		if p, err := store.GetConfig(ctx, "issue_prefix"); err == nil && p != "" {
			prefix = p
		}
	}

	return &tracker.PullHooks{
		GenerateID: func(_ context.Context, issue *types.Issue) error {
			if issue.ID == "" {
				issue.ID = generateIssueID(prefix)
			}
			return nil
		},
	}
}
