// Package main provides the bd CLI commands.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/gitlab"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// GitLabConfig holds GitLab connection configuration.
type GitLabConfig struct {
	URL              string // GitLab instance URL (e.g., "https://gitlab.com")
	Token            string // Personal access token
	ProjectID        string // Project ID or URL-encoded path
	GroupID          string // Optional group ID for group-level issue fetching
	DefaultProjectID string // Project ID for creating issues in group mode
}

// gitlabCmd is the root command for GitLab operations.
var gitlabCmd = &cobra.Command{
	Use:   "gitlab",
	Short: "GitLab integration commands",
	Long: `Commands for syncing issues between beads and GitLab.

Configuration can be set via 'bd config' or environment variables:
  gitlab.url / GITLAB_URL                         - GitLab instance URL
  gitlab.token / GITLAB_TOKEN                     - Personal access token
  gitlab.project_id / GITLAB_PROJECT_ID           - Project ID or path
  gitlab.group_id / GITLAB_GROUP_ID               - Group ID for group-level sync
  gitlab.default_project_id / GITLAB_DEFAULT_PROJECT_ID - Project for creating issues in group mode`,
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGitLabSync,
}

// gitlabStatusCmd displays GitLab configuration and sync status.
var gitlabStatusCmd = &cobra.Command{
	Use:           "status",
	Short:         "Show GitLab sync status",
	Long:          `Display current GitLab configuration and sync status.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGitLabStatus,
}

// gitlabProjectsCmd lists accessible GitLab projects.
var gitlabProjectsCmd = &cobra.Command{
	Use:           "projects",
	Short:         "List accessible GitLab projects",
	Long:          `List GitLab projects that the configured token has access to.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGitLabProjects,
}

var (
	gitlabSyncDryRun   bool
	gitlabSyncPullOnly bool
	gitlabSyncPushOnly bool
	gitlabPreferLocal  bool
	gitlabPreferGitLab bool
	gitlabPreferNewer  bool

	// Filter flags for sync
	gitlabFilterLabel     string
	gitlabFilterProject   string
	gitlabFilterMilestone string
	gitlabFilterAssignee  string

	// Type filtering flags
	gitlabTypeFilter   string
	gitlabExcludeTypes string
	gitlabNoEphemeral  bool
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

	// Filter flags (override config defaults)
	gitlabSyncCmd.Flags().StringVar(&gitlabFilterLabel, "label", "", "Filter by labels (comma-separated, AND logic)")
	gitlabSyncCmd.Flags().StringVar(&gitlabFilterProject, "project", "", "Filter to issues from this project ID (group mode)")
	gitlabSyncCmd.Flags().StringVar(&gitlabFilterMilestone, "milestone", "", "Filter by milestone title")
	gitlabSyncCmd.Flags().StringVar(&gitlabFilterAssignee, "assignee", "", "Filter by assignee username")
	registerSelectiveSyncFlags(gitlabSyncCmd)

	// Type filtering flags
	gitlabSyncCmd.Flags().StringVar(&gitlabTypeFilter, "type", "", "Only sync these issue types (comma-separated, e.g. 'epic,feature,task')")
	gitlabSyncCmd.Flags().StringVar(&gitlabExcludeTypes, "exclude-type", "", "Exclude these issue types from sync (comma-separated)")
	gitlabSyncCmd.Flags().BoolVar(&gitlabNoEphemeral, "no-ephemeral", true, "Exclude ephemeral/wisp issues from push (default: true)")

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
	config.GroupID = getGitLabConfigValue(ctx, "gitlab.group_id")
	config.DefaultProjectID = getGitLabConfigValue(ctx, "gitlab.default_project_id")

	return config
}

// getGitLabConfigValue reads a GitLab configuration value from store or environment.
func getGitLabConfigValue(ctx context.Context, key string) string {
	// Secret/yaml-only keys (e.g. gitlab.token) live in config.yaml, not the
	// Dolt database, to avoid leaking secrets when the DB is pushed to remotes.
	// Read them from config.yaml first, then env, and never touch the store.
	// Mirrors internal/gitlab/tracker.go getConfig after upstream 99653e059.
	if config.IsYamlOnlyKey(key) {
		if val := config.GetString(key); val != "" {
			return val
		}
		if envKey := gitlabConfigToEnvVar(key); envKey != "" {
			if val := os.Getenv(envKey); val != "" {
				return val
			}
		}
		return ""
	}

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
	case "gitlab.group_id":
		return "GITLAB_GROUP_ID"
	case "gitlab.default_project_id":
		return "GITLAB_DEFAULT_PROJECT_ID"
	case "gitlab.filter_labels":
		return "GITLAB_FILTER_LABELS"
	case "gitlab.filter_project":
		return "GITLAB_FILTER_PROJECT"
	case "gitlab.filter_milestone":
		return "GITLAB_FILTER_MILESTONE"
	case "gitlab.filter_assignee":
		return "GITLAB_FILTER_ASSIGNEE"
	default:
		return ""
	}
}

// validateGitLabConfig checks that required configuration is present.
func validateGitLabConfig(config GitLabConfig) error {
	if config.URL == "" {
		return fmt.Errorf("gitlab.url is not configured. Set via 'bd config set gitlab.url <url>' or GITLAB_URL environment variable")
	}
	if config.Token == "" {
		return fmt.Errorf("gitlab.token is not configured. Set via 'bd config set gitlab.token <token>' or GITLAB_TOKEN environment variable")
	}
	if config.ProjectID == "" && config.GroupID == "" {
		return fmt.Errorf("gitlab.project_id or gitlab.group_id is not configured. Set via 'bd config' or environment variables")
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
	client := gitlab.NewClient(config.Token, config.URL, config.ProjectID)
	if config.GroupID != "" {
		client = client.WithGroupID(config.GroupID)
	}
	return client
}

// runGitLabStatus implements the gitlab status command.
func runGitLabStatus(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("gitlab status is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("gitlab-status")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	config := getGitLabConfig()

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "GitLab Configuration")
	_, _ = fmt.Fprintln(out, "====================")
	_, _ = fmt.Fprintf(out, "URL:        %s\n", config.URL)
	_, _ = fmt.Fprintf(out, "Token:      %s\n", maskGitLabToken(config.Token))
	_, _ = fmt.Fprintf(out, "Project ID: %s\n", config.ProjectID)
	if config.GroupID != "" {
		_, _ = fmt.Fprintf(out, "Group ID:   %s\n", config.GroupID)
		_, _ = fmt.Fprintf(out, "Sync Mode:  group (fetches from all projects in group)\n")
		if config.DefaultProjectID != "" {
			_, _ = fmt.Fprintf(out, "Default Project ID: %s (for creating new issues)\n", config.DefaultProjectID)
		}
	} else {
		_, _ = fmt.Fprintf(out, "Sync Mode:  project\n")
	}

	// Show configured filters
	ctx := context.Background()
	filterLabels := getGitLabConfigValue(ctx, "gitlab.filter_labels")
	filterProject := getGitLabConfigValue(ctx, "gitlab.filter_project")
	filterMilestone := getGitLabConfigValue(ctx, "gitlab.filter_milestone")
	filterAssignee := getGitLabConfigValue(ctx, "gitlab.filter_assignee")
	if filterLabels != "" || filterProject != "" || filterMilestone != "" || filterAssignee != "" {
		_, _ = fmt.Fprintf(out, "\nFilters:\n")
		if filterLabels != "" {
			_, _ = fmt.Fprintf(out, "  Labels:    %s\n", filterLabels)
		}
		if filterProject != "" {
			_, _ = fmt.Fprintf(out, "  Project:   %s\n", filterProject)
		}
		if filterMilestone != "" {
			_, _ = fmt.Fprintf(out, "  Milestone: %s\n", filterMilestone)
		}
		if filterAssignee != "" {
			_, _ = fmt.Fprintf(out, "  Assignee:  %s\n", filterAssignee)
		}
	}

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
	if usesProxiedServer() {
		return HandleErrorRespectJSON("gitlab projects is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("gitlab-projects")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	config := getGitLabConfig()
	if err := validateGitLabConfig(config); err != nil {
		return HandleError("%v", err)
	}

	out := cmd.OutOrStdout()
	client := getGitLabClient(config)
	ctx := context.Background()

	projects, err := client.ListProjects(ctx)
	if err != nil {
		return HandleError("failed to fetch projects: %v", err)
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

// gitlabSyncResult holds the JSON output for the gitlab sync command.
type gitlabSyncResult struct {
	DryRun              bool     `json:"dry_run"`
	Pulled              int      `json:"pulled"`
	Pushed              int      `json:"pushed"`
	Created             int      `json:"created"`
	Updated             int      `json:"updated"`
	Skipped             int      `json:"skipped"`
	Conflicts           int      `json:"conflicts"`
	Errors              int      `json:"errors"`
	LinksPushed         int      `json:"links_pushed"`
	LinksLicenseSkipped int      `json:"links_license_skipped,omitempty"`
	MilestonesUpdated   int      `json:"milestones_updated,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
}

// runGitLabSync implements the gitlab sync command.
// Uses the tracker.Engine for all sync operations.
func runGitLabSync(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("gitlab sync is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("gitlab-sync")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	config := getGitLabConfig()
	if err := validateGitLabConfig(config); err != nil {
		return HandleError("%v", err)
	}

	if !gitlabSyncDryRun {
		CheckReadonly("gitlab sync")
	}

	if gitlabSyncPullOnly && gitlabSyncPushOnly {
		return HandleError("cannot use both --pull-only and --push-only")
	}

	conflictStrategy, err := getConflictStrategy(gitlabPreferLocal, gitlabPreferGitLab, gitlabPreferNewer)
	if err != nil {
		return HandleError("%v (--prefer-local, --prefer-gitlab, --prefer-newer)", err)
	}

	if err := ensureStoreActive(); err != nil {
		return HandleError("database not available: %v", err)
	}

	out := cmd.OutOrStdout()
	ctx := context.Background()

	gt := &gitlab.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return HandleError("initializing GitLab tracker: %v", err)
	}

	// Apply CLI filter overrides (take precedence over config defaults)
	if cliFilter := buildCLIFilter(); cliFilter != nil {
		gt.SetFilter(cliFilter)
	}

	// Create the sync engine
	engine := tracker.NewEngine(gt, store, actor)
	if !jsonOutput {
		engine.OnMessage = func(msg string) { _, _ = fmt.Fprintln(out, "  "+msg) }
	}
	engine.OnWarning = func(msg string) { _, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	// Set up GitLab-specific pull hooks
	engine.PullHooks = buildGitLabPullHooks(ctx)
	engine.PushHooks = buildGitLabPushHooks()

	// Build sync options from CLI flags
	pull := !gitlabSyncPushOnly
	push := !gitlabSyncPullOnly

	excludeTypes := parseTypeList(gitlabExcludeTypes)
	// Default: exclude internal coordination types from push unless
	// the user provided an explicit --type whitelist.
	if gitlabTypeFilter == "" && gitlabExcludeTypes == "" {
		excludeTypes = []types.IssueType{
			types.TypeMolecule,
			types.TypeMessage,
			types.TypeEvent,
		}
	}

	opts := tracker.SyncOptions{
		Pull:             pull,
		Push:             push,
		DryRun:           gitlabSyncDryRun,
		ExcludeEphemeral: gitlabNoEphemeral,
		TypeFilter:       parseTypeList(gitlabTypeFilter),
		ExcludeTypes:     excludeTypes,
	}

	if err := applySelectiveSyncFlags(cmd, &opts, push); err != nil {
		return HandleError("%v", err)
	}

	switch conflictStrategy {
	case ConflictStrategyPreferLocal:
		opts.ConflictResolution = tracker.ConflictLocal
	case ConflictStrategyPreferGitLab:
		opts.ConflictResolution = tracker.ConflictExternal
	default:
		opts.ConflictResolution = tracker.ConflictTimestamp
	}

	if gitlabSyncDryRun && !jsonOutput {
		_, _ = fmt.Fprintln(out, "Dry run mode - no changes will be made")
		_, _ = fmt.Fprintln(out)
	}

	result, err := engine.Sync(ctx, opts)
	if err != nil {
		return HandleError("%v", err)
	}

	var linkWarnings []string
	warnLink := func(msg string) {
		linkWarnings = append(linkWarnings, msg)
		_, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
	}

	// Dependency-link push pass: sync beads dependencies to GitLab issue links.
	var linksPushed int
	var linksLicenseSkipped int
	var milestonesUpdated int
	if push {
		linksPushed, linksLicenseSkipped, milestonesUpdated = pushGitLabDependencyLinks(ctx, gt, store, opts, gitlabSyncDryRun, out, warnLink)
	}

	if jsonOutput {
		return outputJSON(gitlabSyncResult{
			DryRun:              gitlabSyncDryRun,
			Pulled:              result.Stats.Pulled,
			Pushed:              result.Stats.Pushed,
			Created:             result.Stats.Created,
			Updated:             result.Stats.Updated,
			Skipped:             result.Stats.Skipped,
			Conflicts:           result.Stats.Conflicts,
			Errors:              result.Stats.Errors,
			LinksPushed:         linksPushed,
			LinksLicenseSkipped: linksLicenseSkipped,
			MilestonesUpdated:   milestonesUpdated,
			Warnings:            append(result.Warnings, linkWarnings...),
		})
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
		if linksPushed > 0 {
			_, _ = fmt.Fprintf(out, "✓ Synced %d dependency links\n", linksPushed)
		}
		if result.Stats.Conflicts > 0 {
			_, _ = fmt.Fprintf(out, "→ Resolved %d conflicts\n", result.Stats.Conflicts)
		}
	}

	if gitlabSyncDryRun {
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintln(out, "Run without --dry-run to apply changes")
	}

	if !gitlabSyncDryRun {
		commandDidWrite.Store(true)
	}

	return nil
}

// gitLabLicenseSkipMessage returns a single curated, actionable line explaining
// that N blocks/is_blocked_by links were skipped because the GitLab instance's
// license lacks the issue-blocking feature. Used instead of raw per-link API
// errors so the degradation is transparent and not mistaken for a real failure.
func gitLabLicenseSkipMessage(n int) string {
	noun := "link"
	if n != 1 {
		noun = "links"
	}
	return fmt.Sprintf(
		"Skipped %d dependency 'blocks' %s: GitLab 'blocks'/'is_blocked_by' requires Premium/Ultimate. "+
			"'relates_to' links and milestones were applied normally.", n, noun)
}

// pushGitLabDependencyLinks runs the dependency-link + epic-milestone push pass:
// it converts beads dependencies among the scoped issues (per opts) into GitLab
// issue links (additive — stale remote links are left untouched) and repairs
// epic-child milestones. Shared by `bd gitlab sync` and `bd gitlab push` so both
// reach the same link parity. Dry-run plan lines are written to out (unless
// --json); warnings are delivered via warn. Returns the number of links created,
// license-skipped, and milestones updated.
func pushGitLabDependencyLinks(ctx context.Context, gt *gitlab.Tracker, st storage.Storage, opts tracker.SyncOptions, dryRun bool, out io.Writer, warn func(string)) (linksPushed, linksLicenseSkipped, milestonesUpdated int) {
	linkData, collectWarnings := collectGitLabLinkSyncData(ctx, st, opts)
	for _, warning := range collectWarnings {
		warn(warning)
	}

	client := gt.GitLabClient()
	if client == nil {
		return 0, 0, 0
	}

	if len(linkData.DesiredLinks) > 0 {
		resolver := gitlab.NewLinkResolver(client)
		res := resolver.PushLinks(ctx, linkData.DesiredLinks, gitlab.PushLinkOptions{
			DryRun: dryRun,
			OnPlan: func(link gitlab.DependencyLink) {
				if !jsonOutput {
					_, _ = fmt.Fprintf(out, "  [dry-run] Would create GitLab dependency link: #%d %s #%d\n",
						link.SourceIID, link.LinkType, link.TargetIID)
				}
			},
		})
		linksPushed = res.Created
		linksLicenseSkipped = res.LicenseSkipped
		// Curated, license-aware degradation: one actionable line instead of
		// a raw per-link API error, kept distinct from genuine failures.
		if res.LicenseSkipped > 0 {
			warn(gitLabLicenseSkipMessage(res.LicenseSkipped))
		}
		for _, err := range res.Errors {
			warn(fmt.Sprintf("GitLab dependency link sync: %v", err))
		}
	}

	if len(linkData.ScopedIssues) > 0 {
		count, errs := gt.PushEpicMilestones(ctx, linkData.ScopedIssues, gitlab.EpicMilestoneOptions{
			DryRun: dryRun,
			OnPlan: func(issueID string, issueIID int, milestoneID int) {
				if !jsonOutput {
					_, _ = fmt.Fprintf(out, "  [dry-run] Would set GitLab milestone %d on %s (#%d)\n",
						milestoneID, issueID, issueIID)
				}
			},
		})
		milestonesUpdated = count
		for _, err := range errs {
			warn(fmt.Sprintf("GitLab epic milestone sync: %v", err))
		}
	}

	return linksPushed, linksLicenseSkipped, milestonesUpdated
}

type gitlabLinkSyncData struct {
	ScopedIssues []*types.Issue
	DesiredLinks []gitlab.DependencyLink
}

func collectGitLabLinkSyncData(ctx context.Context, st storage.Storage, opts tracker.SyncOptions) (gitlabLinkSyncData, []string) {
	if st == nil {
		return gitlabLinkSyncData{}, []string{"GitLab dependency link sync skipped: database not available"}
	}

	allIssues, err := st.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return gitlabLinkSyncData{}, []string{fmt.Sprintf("GitLab dependency link sync skipped: %v", err)}
	}

	var warnings []string
	var descendantSet map[string]bool
	if opts.ParentID != "" {
		descendantSet, err = buildGitLabDescendantSet(ctx, st, opts.ParentID)
		if err != nil {
			return gitlabLinkSyncData{}, []string{fmt.Sprintf("GitLab dependency link sync skipped: resolving parent %s: %v", opts.ParentID, err)}
		}
	}

	scopedIssues := filterGitLabLinkScopedIssues(allIssues, opts, descendantSet)
	scopedIssueIDs := gitlabScopedIssueIDSet(scopedIssues)
	desired := make([]gitlab.DependencyLink, 0, len(scopedIssues))
	for _, issue := range scopedIssues {
		deps, err := st.GetDependenciesWithMetadata(ctx, issue.ID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("GitLab dependency link sync skipped dependencies for %s: %v", issue.ID, err))
			continue
		}
		for _, dep := range deps {
			if !scopedIssueIDs[dep.ID] {
				continue
			}
			link, ok := gitlab.LinkFromBeadsDependency(issue, dep)
			if ok {
				desired = append(desired, link)
			}
		}
	}

	return gitlabLinkSyncData{
		ScopedIssues: scopedIssues,
		DesiredLinks: gitlab.DeduplicateLinks(desired),
	}, warnings
}

func filterGitLabLinkScopedIssues(issues []*types.Issue, opts tracker.SyncOptions, descendantSet map[string]bool) []*types.Issue {
	result := make([]*types.Issue, 0, len(issues))
	issueIDSet := gitlabIssueIDSet(opts.IssueIDs)
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if issueIDSet != nil && !issueIDSet[issue.ID] {
			continue
		}
		if descendantSet != nil && !descendantSet[issue.ID] {
			continue
		}
		if !gitlabIssueAllowedByPushFilters(issue, opts) {
			continue
		}
		result = append(result, issue)
	}
	return result
}

func gitlabIssueAllowedByPushFilters(issue *types.Issue, opts tracker.SyncOptions) bool {
	if issue == nil {
		return false
	}
	if opts.ExcludeEphemeral && issue.Ephemeral {
		return false
	}
	if len(opts.TypeFilter) > 0 {
		matched := false
		for _, issueType := range opts.TypeFilter {
			if issue.IssueType == issueType {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, issueType := range opts.ExcludeTypes {
		if issue.IssueType == issueType {
			return false
		}
	}
	if opts.State == "open" && issue.Status == types.StatusClosed {
		return false
	}
	return true
}

func buildGitLabDescendantSet(ctx context.Context, st storage.Storage, parentID string) (map[string]bool, error) {
	result := map[string]bool{parentID: true}
	queue := []string{parentID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		dependents, err := st.GetDependentsWithMetadata(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("getting dependents of %s: %w", current, err)
		}
		for _, dep := range dependents {
			if dep.DependencyType == types.DepParentChild && !result[dep.Issue.ID] {
				result[dep.Issue.ID] = true
				queue = append(queue, dep.Issue.ID)
			}
		}
	}
	return result, nil
}

func gitlabIssueIDSet(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	result := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			result[id] = true
		}
	}
	return result
}

func gitlabScopedIssueIDSet(issues []*types.Issue) map[string]bool {
	result := make(map[string]bool, len(issues))
	for _, issue := range issues {
		if issue != nil && issue.ID != "" {
			result[issue.ID] = true
		}
	}
	return result
}

// buildCLIFilter constructs an IssueFilter from CLI flags.
// Returns nil if no filter flags were provided.
func buildCLIFilter() *gitlab.IssueFilter {
	if gitlabFilterLabel == "" && gitlabFilterProject == "" &&
		gitlabFilterMilestone == "" && gitlabFilterAssignee == "" {
		return nil
	}
	filter := &gitlab.IssueFilter{
		Labels:    gitlabFilterLabel,
		Milestone: gitlabFilterMilestone,
		Assignee:  gitlabFilterAssignee,
	}
	if gitlabFilterProject != "" {
		if pid, err := strconv.Atoi(gitlabFilterProject); err == nil {
			filter.ProjectID = pid
		}
	}
	return filter
}

// buildGitLabPullHooks creates PullHooks for GitLab-specific pull behavior.
func buildGitLabPullHooks(ctx context.Context) *tracker.PullHooks {
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

// buildGitLabPushHooks creates PushHooks for GitLab-specific push behavior.
func buildGitLabPushHooks() *tracker.PushHooks {
	return &tracker.PushHooks{
		ContentEqual: gitLabPushContentEqual,
	}
}

func gitLabPushContentEqual(local *types.Issue, remote *tracker.TrackerIssue) bool {
	if local == nil || remote == nil {
		return false
	}

	// Epics are represented as GitLab milestones. GitLab can return a milestone
	// updated_at that is older than local beads bookkeeping even when pushed
	// fields are already identical, so use content equality to avoid repeat PUTs.
	if local.IssueType == types.TypeEpic && gitLabMilestonePushFieldsEqual(local, remote) {
		return true
	}

	// Preserve the engine's default skip behavior for everything this hook does
	// not handle explicitly.
	return !remote.UpdatedAt.Before(local.UpdatedAt)
}

func gitLabMilestonePushFieldsEqual(local *types.Issue, remote *tracker.TrackerIssue) bool {
	if !gitLabComparableTextEqual(local.Title, remote.Title) {
		return false
	}
	if !gitLabComparableTextEqual(local.Description, remote.Description) {
		return false
	}
	return gitLabMilestoneStateEqual(local.Status, remote.State)
}

func gitLabComparableTextEqual(a, b string) bool {
	return strings.TrimSpace(strings.ReplaceAll(a, "\r\n", "\n")) ==
		strings.TrimSpace(strings.ReplaceAll(b, "\r\n", "\n"))
}

func gitLabMilestoneStateEqual(status types.Status, remoteState interface{}) bool {
	remoteStateString, ok := remoteState.(string)
	if !ok {
		return false
	}
	remoteStateString = strings.ToLower(strings.TrimSpace(remoteStateString))
	switch status {
	case types.StatusClosed:
		return remoteStateString == "closed"
	default:
		return remoteStateString == "active"
	}
}

// parseTypeList splits a comma-separated string of issue types.
// Returns nil for empty input.
func parseTypeList(s string) []types.IssueType {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]types.IssueType, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, types.IssueType(p))
		}
	}
	return result
}
