// Package main provides the bd CLI commands.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/gitlab"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/tracker"
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

// issueIDCounter is used to generate unique issue IDs.
var issueIDCounter uint64

// ConflictStrategy defines how to resolve conflicts between local and GitLab versions.
type ConflictStrategy string

const (
	// ConflictStrategyPreferNewer uses the most recently updated version (default).
	ConflictStrategyPreferNewer ConflictStrategy = "prefer-newer"
	// ConflictStrategyPreferLocal always keeps the local beads version.
	ConflictStrategyPreferLocal ConflictStrategy = "prefer-local"
	// ConflictStrategyPreferGitLab always uses the GitLab version.
	ConflictStrategyPreferGitLab ConflictStrategy = "prefer-gitlab"
)

// getConflictStrategy determines the conflict strategy from flag values.
// Returns error if multiple conflicting flags are set.
func getConflictStrategy(preferLocal, preferGitLab, preferNewer bool) (ConflictStrategy, error) {
	flagsSet := 0
	if preferLocal {
		flagsSet++
	}
	if preferGitLab {
		flagsSet++
	}
	if preferNewer {
		flagsSet++
	}
	if flagsSet > 1 {
		return "", fmt.Errorf("cannot use multiple conflict resolution flags")
	}

	if preferLocal {
		return ConflictStrategyPreferLocal, nil
	}
	if preferGitLab {
		return ConflictStrategyPreferGitLab, nil
	}
	return ConflictStrategyPreferNewer, nil
}

// generateIssueID creates a unique issue ID with the given prefix.
// Uses atomic counter combined with timestamp and random bytes to ensure uniqueness
// even when called rapidly or after process restart.
func generateIssueID(prefix string) string {
	counter := atomic.AddUint64(&issueIDCounter, 1)
	timestamp := time.Now().UnixNano() / 1000000 // milliseconds
	// Add random bytes to prevent collision on restart
	randBytes := make([]byte, 4)
	_, _ = rand.Read(randBytes)
	return fmt.Sprintf("%s-%d-%d-%x", prefix, timestamp, counter, randBytes)
}

// parseGitLabSourceSystem parses a source system string like "gitlab:123:42"
// Returns projectID, iid, and ok (whether it's a valid GitLab source).
func parseGitLabSourceSystem(sourceSystem string) (projectID, iid int, ok bool) {
	if !strings.HasPrefix(sourceSystem, "gitlab:") {
		return 0, 0, false
	}

	parts := strings.Split(sourceSystem, ":")
	if len(parts) != 3 {
		return 0, 0, false
	}

	var err error
	projectID, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}

	iid, err = strconv.Atoi(parts[2])
	if err != nil {
		return 0, 0, false
	}

	return projectID, iid, true
}

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
		tempStore, err := factory.NewWithOptions(ctx, configfile.BackendDolt, dbPath, factory.Options{LockTimeout: 5 * time.Second})
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
// Uses the tracker.Engine for all sync operations.
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

	// Validate conflict flags
	conflictStrategy, err := getConflictStrategy(gitlabPreferLocal, gitlabPreferGitLab, gitlabPreferNewer)
	if err != nil {
		return fmt.Errorf("%w (--prefer-local, --prefer-gitlab, --prefer-newer)", err)
	}

	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	out := cmd.OutOrStdout()
	ctx := context.Background()

	// Create and initialize the GitLab tracker
	gt := &gitlab.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing GitLab tracker: %w", err)
	}

	// Create the sync engine
	engine := tracker.NewEngine(gt, store, actor)
	engine.OnMessage = func(msg string) { _, _ = fmt.Fprintln(out, "  "+msg) }
	engine.OnWarning = func(msg string) { _, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	// Set up GitLab-specific pull hooks
	engine.PullHooks = buildGitLabPullHooks(ctx)

	// Build sync options from CLI flags
	pull := !gitlabSyncPushOnly
	push := !gitlabSyncPullOnly

	opts := tracker.SyncOptions{
		Pull:   pull,
		Push:   push,
		DryRun: gitlabSyncDryRun,
	}

	// Map conflict resolution
	switch conflictStrategy {
	case ConflictStrategyPreferLocal:
		opts.ConflictResolution = tracker.ConflictLocal
	case ConflictStrategyPreferGitLab:
		opts.ConflictResolution = tracker.ConflictExternal
	default:
		opts.ConflictResolution = tracker.ConflictTimestamp
	}

	if gitlabSyncDryRun {
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
	if !gitlabSyncDryRun {
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

	if gitlabSyncDryRun {
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintln(out, "Run without --dry-run to apply changes")
	}

	return nil
}

// buildGitLabPullHooks creates PullHooks for GitLab-specific pull behavior.
func buildGitLabPullHooks(ctx context.Context) *tracker.PullHooks {
	prefix := "bd"
	if store != nil {
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
