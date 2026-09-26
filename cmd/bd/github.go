// Package main provides the bd CLI commands.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/github"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGitHubSync,
}

// githubStatusCmd displays GitHub configuration and sync status.
var githubStatusCmd = &cobra.Command{
	Use:           "status",
	Short:         "Show GitHub sync status",
	Long:          `Display current GitHub configuration and sync status.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGitHubStatus,
}

// githubReposCmd lists accessible GitHub repositories.
var githubReposCmd = &cobra.Command{
	Use:           "repos",
	Short:         "List accessible GitHub repositories",
	Long:          `List GitHub repositories that the configured token has access to.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGitHubRepos,
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
	// Secret keys (e.g. github.token) are stored in config.yaml, not the
	// Dolt database, to avoid leaking secrets when pushing to remotes.
	if config.IsYamlOnlyKey(key) {
		if value := config.GetString(key); value != "" {
			return value
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
		return fmt.Errorf("github.token is not configured. Set via 'bd config set github.token <token>' or GITHUB_TOKEN environment variable")
	}
	if config.Owner == "" {
		return fmt.Errorf("github.owner is not configured. Set via 'bd config set github.owner <owner>' or GITHUB_OWNER environment variable")
	}
	if config.Repo == "" {
		return fmt.Errorf("github.repo is not configured. Set via 'bd config set github.repo <repo>' or GITHUB_REPO environment variable")
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
	if usesProxiedServer() {
		return HandleErrorRespectJSON("github status is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("github-status")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

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
	if usesProxiedServer() {
		return HandleErrorRespectJSON("github repos is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("github-repos")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	config := getGitHubConfig()
	if config.Token == "" {
		return HandleError("github.token is not configured. Set via 'bd config set github.token <token>' or GITHUB_TOKEN environment variable")
	}

	out := cmd.OutOrStdout()
	client := getGitHubClient(config)
	ctx := context.Background()

	repos, err := client.ListRepositories(ctx)
	if err != nil {
		return HandleError("failed to fetch repositories: %v", err)
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
	if usesProxiedServer() {
		return HandleErrorRespectJSON("github sync is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("github-sync")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	config := getGitHubConfig()
	if err := validateGitHubConfig(config); err != nil {
		return HandleError("%v", err)
	}

	if !githubSyncDryRun {
		CheckReadonly("github sync")
	}

	if githubSyncPullOnly && githubSyncPushOnly {
		return HandleError("cannot use both --pull-only and --push-only")
	}

	conflictStrategy, err := getGitHubConflictStrategy(githubPreferLocal, githubPreferGitHub, githubPreferNewer)
	if err != nil {
		return HandleError("%v (--prefer-local, --prefer-github, --prefer-newer)", err)
	}

	if err := ensureStoreActive(); err != nil {
		return HandleError("database not available: %v", err)
	}

	out := cmd.OutOrStdout()
	ctx := context.Background()

	gt := &github.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return HandleError("initializing GitHub tracker: %v", err)
	}

	// Create the sync engine
	engine := tracker.NewEngine(gt, store, actor)
	engine.OnMessage = func(msg string) { _, _ = fmt.Fprintln(out, "  "+msg) }
	engine.OnWarning = func(msg string) { _, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	// Set up GitHub-specific pull and push hooks
	engine.PullHooks = buildGitHubPullHooks(ctx)
	engine.PushHooks = buildGitHubPushHooks(gt)

	// Build sync options from CLI flags
	pull := !githubSyncPushOnly
	push := !githubSyncPullOnly

	opts := tracker.SyncOptions{
		Pull:   pull,
		Push:   push,
		DryRun: githubSyncDryRun,
	}

	if err := applySelectiveSyncFlags(cmd, &opts, push); err != nil {
		return HandleError("%v", err)
	}

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

	result, err := engine.Sync(ctx, opts)
	if err != nil {
		return HandleError("%v", err)
	}

	// Relationship push pass: converge beads parent/child links and "blocks"
	// dependencies into GitHub sub-issues and issue dependencies. Runs after
	// the content sync so relationships stay correct even when issue content
	// itself was unchanged and the main push loop skipped the issue.
	//
	// This pass deliberately leaves github.last_sync alone. That cursor is the
	// engine's record of when local rows were last reconciled, and
	// DetectConflicts treats every issue updated after it as a local edit;
	// advancing it here would hide edits made since the engine set it from the
	// next sync's conflict detection. The engine owns the cursor
	// (internal/tracker/engine.go), and this pass writes no local rows.
	var linksPushed int
	if push {
		var linkWarnings []string
		warnLink := func(msg string) {
			linkWarnings = append(linkWarnings, msg)
			_, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
		linksPushed = pushGitHubDependencyLinks(ctx, gt, store, opts, githubSyncDryRun, out, warnLink)
		result.Warnings = append(result.Warnings, linkWarnings...)
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
		if linksPushed > 0 {
			_, _ = fmt.Fprintf(out, "✓ Synced %d relationship links\n", linksPushed)
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

// githubLinkSyncData holds the desired GitHub relationship links collected
// from beads dependency state for the issues in scope of a sync/push pass.
type githubLinkSyncData struct {
	DesiredLinks []github.DependencyLink
}

// collectGitHubLinkSyncData walks the beads issues in scope of opts and
// derives the GitHub relationship links (sub-issue for epic/child,
// blocked_by for beads "blocks" dependencies) that should exist remotely.
// Only issues already linked to GitHub (an ExternalRef that scope resolves to
// an issue number in the configured repository) can contribute or receive a
// link; refs pointing at another repository or host are skipped, since the
// relationship endpoints take bare issue numbers scoped to one repo.
//
// The workspace read is issueops.Reader's, reached through the store's own
// accessor so it carries whatever layers that store carries; the request it
// takes is githubLinkSyncListRequest below.
func collectGitHubLinkSyncData(ctx context.Context, st storage.Storage, scope github.RefScope, opts tracker.SyncOptions) (githubLinkSyncData, []string) {
	if st == nil {
		return githubLinkSyncData{}, []string{"GitHub relationship sync skipped: database not available"}
	}

	reader, err := st.IssueReader()
	if err != nil {
		return githubLinkSyncData{}, []string{fmt.Sprintf("GitHub relationship sync skipped: %v", err)}
	}
	page, err := reader.List(ctx, githubLinkSyncListRequest())
	if err != nil {
		return githubLinkSyncData{}, []string{fmt.Sprintf("GitHub relationship sync skipped: %v", err)}
	}
	allIssues := make([]*types.Issue, 0, len(page.Items))
	for _, item := range page.Items {
		if item == nil || item.Issue == nil {
			continue
		}
		allIssues = append(allIssues, item.Issue)
	}

	scopedIssues := filterGitHubLinkScopedIssues(allIssues, opts)
	scopedIssueIDs := make(map[string]bool, len(scopedIssues))
	for _, issue := range scopedIssues {
		if issue != nil && issue.ID != "" {
			scopedIssueIDs[issue.ID] = true
		}
	}

	var warnings []string
	var desired []github.DependencyLink
	for _, issue := range scopedIssues {
		if issue.ExternalRef == nil {
			continue
		}
		deps, err := st.GetDependenciesWithMetadata(ctx, issue.ID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("GitHub relationship sync skipped dependencies for %s: %v", issue.ID, err))
			continue
		}
		for _, dep := range deps {
			if !scopedIssueIDs[dep.ID] {
				continue
			}
			switch dep.DependencyType {
			case types.DepParentChild:
				if link, ok := scope.SubIssueLinkFromParentChild(issue, dep); ok {
					desired = append(desired, link)
				}
			case types.DepBlocks:
				if link, ok := scope.BlockedByLinkFromBeadsDependency(issue, dep); ok {
					desired = append(desired, link)
				}
			}
		}
	}

	return githubLinkSyncData{DesiredLinks: desired}, warnings
}

// githubLinkSyncListRequest is the whole-workspace read the relationship pass
// runs: every bead in both planes, at any status, of any type, unpaged.
//
// SCOPE IS NOT THIS REQUEST'S JOB. filterGitHubLinkScopedIssues narrows to the
// tracker.SyncOptions the caller was given, the same way the engine's push loop
// narrows the same unfiltered read with shouldPushIssue
// (internal/tracker/engine.go). Expressing half that selection here in a second
// vocabulary is how the two would drift, so this lifts every default a listing
// applies rather than reproducing the engine's choices:
//
//	AllFlag         drops the default status exclusions, and with them the
//	                pinned predicate. A relationship is still true for a closed
//	                bead, and the engine pushes closed issues too.
//	IncludeAllTypes drops the template, gate and infra type exclusions AND the
//	                plane decision, so the wisps table is read. opts.
//	                ExcludeEphemeral is what drops that plane, downstream, when
//	                the caller asked for it.
//	Limit           zero, not nil: nil takes the shared list default, which
//	                would silently cap relationship sync at one page.
//
// SkipLabels and SkipCounts choose what is HYDRATED, never which rows match.
// This pass reads ID, ExternalRef, IssueType and Ephemeral and nothing else,
// and the counts are per-row aggregate joins the scan has no use for.
func githubLinkSyncListRequest() issueops.ListRequest {
	unlimited := 0
	return issueops.ListRequest{
		AllFlag:         true,
		IncludeAllTypes: true,
		Limit:           &unlimited,
		SkipLabels:      true,
		SkipCounts:      true,
	}
}

func filterGitHubLinkScopedIssues(issues []*types.Issue, opts tracker.SyncOptions) []*types.Issue {
	var issueIDSet map[string]bool
	if len(opts.IssueIDs) > 0 {
		issueIDSet = make(map[string]bool, len(opts.IssueIDs))
		for _, id := range opts.IssueIDs {
			issueIDSet[id] = true
		}
	}

	result := make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if issueIDSet != nil && !issueIDSet[issue.ID] {
			continue
		}
		if opts.ExcludeEphemeral && issue.Ephemeral {
			continue
		}
		if len(opts.TypeFilter) > 0 && !slices.Contains(opts.TypeFilter, issue.IssueType) {
			continue
		}
		if slices.Contains(opts.ExcludeTypes, issue.IssueType) {
			continue
		}
		result = append(result, issue)
	}
	return result
}

// pushGitHubDependencyLinks runs the relationship push pass: it converts
// beads epic/child links and "blocks" dependencies among the scoped issues
// (per opts) into GitHub sub-issues and issue dependencies. Additive — stale
// remote relationships are left untouched. Shared by `bd github sync` and
// `bd github push` so both reach the same relationship parity. Dry-run plan
// lines are written to out (unless --json); warnings are delivered via warn.
// Returns the number of relationships created.
func pushGitHubDependencyLinks(ctx context.Context, gt *github.Tracker, st storage.Storage, opts tracker.SyncOptions, dryRun bool, out io.Writer, warn func(string)) int {
	if gt == nil {
		return 0
	}
	linkData, collectWarnings := collectGitHubLinkSyncData(ctx, st, gt.RefScope(), opts)
	for _, warning := range collectWarnings {
		warn(warning)
	}

	resolver := gt.LinkResolver()
	if resolver == nil || len(linkData.DesiredLinks) == 0 {
		return 0
	}

	res := resolver.PushLinks(ctx, linkData.DesiredLinks, github.PushLinkOptions{
		DryRun: dryRun,
		OnPlan: func(link github.DependencyLink) {
			if !jsonOutput {
				_, _ = fmt.Fprintf(out, "  [dry-run] Would create GitHub %s relationship: #%d -> #%d\n",
					link.LinkType, link.FromNumber, link.ToNumber)
			}
		},
	})
	for _, err := range res.Errors {
		warn(fmt.Sprintf("GitHub relationship sync: %v", err))
	}
	return res.Created
}

// buildGitHubPushHooks creates PushHooks for GitHub-specific push behavior.
// The ContentEqual hook lets the engine skip issues whose pushable fields
// already match GitHub, so repeated `github sync --push-only` / `github push`
// runs don't re-PATCH unchanged issues (gastownhall/beads#4214).
func buildGitHubPushHooks(gt *github.Tracker) *tracker.PushHooks {
	config := gt.MappingConfig()
	if config == nil {
		config = github.DefaultMappingConfig()
	}
	return &tracker.PushHooks{
		ContentEqual: func(local *types.Issue, remote *tracker.TrackerIssue) bool {
			if remote == nil {
				return false
			}
			gh, ok := remote.Raw.(*github.Issue)
			if !ok || gh == nil {
				return false
			}
			return github.PushFieldsEqual(local, gh, config)
		},
		// ContentHash lets the engine skip the per-issue GitHub fetch entirely
		// when an issue is unchanged since its last push, so a no-op
		// `github sync --push-only` makes ~zero REST calls instead of one GET
		// per linked issue (gastownhall/beads#4214).
		ContentHash: func(local *types.Issue) string {
			return github.PushContentHash(local, config)
		},
		// TargetScope supplies the host and repository omitted by shorthand refs
		// such as github:42, so changing GitHub target configuration invalidates
		// the local no-op cache.
		TargetScope: gt.PushTargetScope,
	}
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
