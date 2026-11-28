// Package main provides the bd CLI commands.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/endpointtrust"
	"github.com/steveyegge/beads/internal/gitea"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// GiteaConfig holds Gitea / Forgejo connection configuration.
type GiteaConfig struct {
	URL        string // Gitea or Forgejo instance base URL (HTTPS, or http:// on loopback)
	Token      string // Personal access token (stored in config.yaml or GITEA_TOKEN)
	Owner      string // Repository owner (user or organization)
	Repo       string // Repository name
	Repository string // Combined "owner/repo" format from gitea.repo
	TLSCA      string // Optional custom CA PEM bundle path (gitea.tls_ca / GITEA_TLS_CA)
}

var giteaCmd = &cobra.Command{
	Use:   "gitea",
	Short: "Gitea/Forgejo integration commands",
	Long: `Commands for syncing issues between beads and Gitea or Forgejo instances
(including Codeberg and self-hosted deployments).

Configuration can be set via 'bd config' or environment variables:
  gitea.url / GITEA_URL       - Gitea/Forgejo instance URL (e.g., https://codeberg.org)
  gitea.token / GITEA_TOKEN   - Personal access token (stored in config.yaml)
  gitea.repo / GITEA_REPO     - Repository in owner/repo format
  gitea.owner / GITEA_OWNER   - Repository owner (alternative to owner/repo)
  gitea.tls_ca / GITEA_TLS_CA - Optional PEM CA bundle path for self-signed TLS`,
}

var giteaSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync issues with Gitea/Forgejo",
	Long: `Synchronize issues between beads and a Gitea or Forgejo repository.

By default, performs bidirectional sync:
- Pulls new/updated issues from Gitea/Forgejo to beads
- Pushes local beads issues to Gitea/Forgejo

Use --pull-only or --push-only to limit direction.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGiteaSync,
}

var giteaStatusCmd = &cobra.Command{
	Use:           "status",
	Short:         "Show Gitea/Forgejo sync status",
	Long:          `Display current Gitea/Forgejo configuration and sync status.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGiteaStatus,
}

var giteaReposCmd = &cobra.Command{
	Use:           "repos",
	Short:         "List accessible Gitea/Forgejo repositories",
	Long:          `List Gitea/Forgejo repositories accessible to the configured token.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGiteaRepos,
}

var (
	giteaSyncDryRun      bool
	giteaSyncPullOnly    bool
	giteaSyncPushOnly    bool
	giteaPreferLocal     bool
	giteaPreferGitea     bool
	giteaPreferNewer     bool
	giteaFilterLabel     string
	giteaFilterMilestone string
	giteaFilterAssignee  string
	giteaTypeFilter      string
	giteaExcludeTypes    string
	giteaNoEphemeral     bool
)

func init() {
	giteaCmd.AddCommand(giteaSyncCmd)
	giteaCmd.AddCommand(giteaStatusCmd)
	giteaCmd.AddCommand(giteaReposCmd)

	giteaSyncCmd.Flags().BoolVar(&giteaSyncDryRun, "dry-run", false, "Show what would be synced without making changes")
	giteaSyncCmd.Flags().BoolVar(&giteaSyncPullOnly, "pull-only", false, "Only pull issues from Gitea/Forgejo")
	giteaSyncCmd.Flags().BoolVar(&giteaSyncPushOnly, "push-only", false, "Only push issues to Gitea/Forgejo")

	giteaSyncCmd.Flags().BoolVar(&giteaPreferLocal, "prefer-local", false, "On conflict, keep local beads version")
	giteaSyncCmd.Flags().BoolVar(&giteaPreferGitea, "prefer-gitea", false, "On conflict, use Gitea/Forgejo version")
	giteaSyncCmd.Flags().BoolVar(&giteaPreferNewer, "prefer-newer", false, "On conflict, use most recent version (default)")

	giteaSyncCmd.Flags().StringVar(&giteaFilterLabel, "label", "", "Filter by labels (comma-separated, AND logic)")
	giteaSyncCmd.Flags().StringVar(&giteaFilterMilestone, "milestone", "", "Filter by milestone title")
	giteaSyncCmd.Flags().StringVar(&giteaFilterAssignee, "assignee", "", "Filter by assignee username")
	registerSelectiveSyncFlags(giteaSyncCmd)

	giteaSyncCmd.Flags().StringVar(&giteaTypeFilter, "type", "", "Only sync these issue types (comma-separated, e.g. 'epic,feature,task')")
	giteaSyncCmd.Flags().StringVar(&giteaExcludeTypes, "exclude-type", "", "Exclude these issue types from sync (comma-separated)")
	giteaSyncCmd.Flags().BoolVar(&giteaNoEphemeral, "no-ephemeral", true, "Exclude ephemeral/wisp issues from push (default: true)")

	rootCmd.AddCommand(giteaCmd)
}

func getGiteaConfig() GiteaConfig {
	ctx := context.Background()
	cfg := GiteaConfig{}

	cfg.URL = strings.TrimSpace(getGiteaConfigValue(ctx, "gitea.url"))
	cfg.Token = getGiteaConfigValue(ctx, "gitea.token")
	cfg.Repository = strings.TrimSpace(getGiteaConfigValue(ctx, "gitea.repo"))
	cfg.TLSCA = gitea.ResolvePrivateTLSCA()

	ownerCfg := strings.TrimSpace(getGiteaConfigValue(ctx, "gitea.owner"))
	projectCfg := strings.TrimSpace(getGiteaConfigValue(ctx, "gitea.project"))
	cfg.Owner, cfg.Repo = gitea.ResolveOwnerRepo(cfg.Repository, ownerCfg, projectCfg)

	return cfg
}

func getGiteaConfigValue(ctx context.Context, key string) string {
	if key == "gitea.tls_ca" {
		return gitea.ResolvePrivateTLSCA()
	}
	// Secret keys (e.g. gitea.token) live in config.yaml, never the Dolt DB.
	if config.IsYamlOnlyKey(key) {
		if val := config.GetString(key); val != "" {
			return val
		}
		if envKey := giteaConfigToEnvVar(key); envKey != "" {
			if val := os.Getenv(envKey); val != "" {
				return val
			}
		}
		return ""
	}

	if store != nil {
		if value, _ := store.GetConfig(ctx, key); value != "" {
			return value
		}
	} else if dbPath != "" {
		tempStore, err := openReadOnlyStoreForDBPath(ctx, dbPath)
		if err == nil {
			defer func() { _ = tempStore.Close() }()
			if value, _ := tempStore.GetConfig(ctx, key); value != "" {
				return value
			}
		}
	}

	if envKey := giteaConfigToEnvVar(key); envKey != "" {
		if value := os.Getenv(envKey); value != "" {
			return value
		}
	}

	return ""
}

func giteaConfigToEnvVar(key string) string {
	switch key {
	case "gitea.url":
		return "GITEA_URL"
	case "gitea.token":
		return "GITEA_TOKEN"
	case "gitea.repo":
		return "GITEA_REPO"
	case "gitea.owner":
		return "GITEA_OWNER"
	case "gitea.project":
		return "GITEA_PROJECT"
	case "gitea.tls_ca":
		return "GITEA_TLS_CA"
	case "gitea.filter_labels":
		return "GITEA_FILTER_LABELS"
	case "gitea.filter_milestone":
		return "GITEA_FILTER_MILESTONE"
	case "gitea.filter_assignee":
		return "GITEA_FILTER_ASSIGNEE"
	default:
		return ""
	}
}

func validateGiteaURL(rawURL string) error {
	return gitea.ValidateURLScheme(rawURL)
}

func validateGiteaConnectionConfig(cfg GiteaConfig) error {
	if cfg.URL == "" {
		return fmt.Errorf("gitea.url is not configured. Set via 'bd config set gitea.url <url>' or GITEA_URL environment variable")
	}
	if err := validateGiteaURL(cfg.URL); err != nil {
		return err
	}
	if cfg.Token == "" {
		return fmt.Errorf("gitea.token is not configured. Set via 'bd config set gitea.token <token>' or GITEA_TOKEN environment variable")
	}
	return nil
}

func validateGiteaConfig(cfg GiteaConfig) error {
	if err := validateGiteaConnectionConfig(cfg); err != nil {
		return err
	}
	if cfg.Owner == "" || cfg.Repo == "" {
		return fmt.Errorf("gitea.repo is not configured. Set via 'bd config set gitea.repo <owner/repo>' or GITEA_REPO environment variable")
	}
	return nil
}

func maskGiteaToken(token string) string {
	if token == "" {
		return "(not set)"
	}
	if len(token) <= 4 {
		return "****"
	}
	return token[:4] + "****"
}

func getGiteaClient(cfg GiteaConfig) (*gitea.Client, error) {
	// Pass the unstripped raw URL to endpointtrust before normalizing BaseURL.
	if err := endpointtrust.Check(endpointtrust.WorkspaceKey(), "gitea", cfg.URL, endpointtrust.PrivateMatch("gitea.url", "GITEA_URL", cfg.URL)); err != nil {
		return nil, err
	}
	client := gitea.NewClient(cfg.Token, cfg.URL, cfg.Owner, cfg.Repo)
	if cfg.TLSCA != "" {
		var err error
		client, err = client.WithTLSCA(cfg.TLSCA)
		if err != nil {
			return nil, err
		}
	}
	return client, nil
}

func getGiteaConflictStrategy(preferLocal, preferGitea, preferNewer bool) (tracker.ConflictResolution, error) {
	flagsSet := 0
	if preferLocal {
		flagsSet++
	}
	if preferGitea {
		flagsSet++
	}
	if preferNewer {
		flagsSet++
	}
	if flagsSet > 1 {
		return "", fmt.Errorf("cannot use multiple conflict resolution flags")
	}
	if preferLocal {
		return tracker.ConflictLocal, nil
	}
	if preferGitea {
		return tracker.ConflictExternal, nil
	}
	return tracker.ConflictTimestamp, nil
}

func buildGiteaCLIFilter(ctx context.Context) *gitea.IssueFilter {
	labels := giteaFilterLabel
	if labels == "" {
		labels = getGiteaConfigValue(ctx, "gitea.filter_labels")
	}
	milestone := giteaFilterMilestone
	if milestone == "" {
		milestone = getGiteaConfigValue(ctx, "gitea.filter_milestone")
	}
	assignee := giteaFilterAssignee
	if assignee == "" {
		assignee = getGiteaConfigValue(ctx, "gitea.filter_assignee")
	}
	if labels == "" && milestone == "" && assignee == "" {
		return nil
	}
	return &gitea.IssueFilter{
		Labels:    labels,
		Milestone: milestone,
		Assignee:  assignee,
	}
}

type giteaStatusPayload struct {
	Configured bool              `json:"configured"`
	URL        string            `json:"url"`
	Token      string            `json:"token"`
	Owner      string            `json:"owner,omitempty"`
	Repo       string            `json:"repo,omitempty"`
	TLSCA      string            `json:"tls_ca,omitempty"`
	Filters    map[string]string `json:"filters,omitempty"`
	Error      string            `json:"error,omitempty"`
}

func runGiteaStatus(cmd *cobra.Command, _ []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("gitea status is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("gitea-status")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	cfg := getGiteaConfig()
	ctx := context.Background()
	filterLabels := getGiteaConfigValue(ctx, "gitea.filter_labels")
	filterMilestone := getGiteaConfigValue(ctx, "gitea.filter_milestone")
	filterAssignee := getGiteaConfigValue(ctx, "gitea.filter_assignee")
	valErr := validateGiteaConfig(cfg)

	if jsonOutput {
		filters := map[string]string{}
		if filterLabels != "" {
			filters["labels"] = filterLabels
		}
		if filterMilestone != "" {
			filters["milestone"] = filterMilestone
		}
		if filterAssignee != "" {
			filters["assignee"] = filterAssignee
		}
		payload := giteaStatusPayload{
			Configured: valErr == nil,
			URL:        cfg.URL,
			Token:      maskGiteaToken(cfg.Token),
			Owner:      cfg.Owner,
			Repo:       cfg.Repo,
			TLSCA:      cfg.TLSCA,
			Filters:    filters,
		}
		if valErr != nil {
			payload.Error = valErr.Error()
		}
		return outputJSON(payload)
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "Gitea / Forgejo Configuration")
	_, _ = fmt.Fprintln(out, "=============================")
	_, _ = fmt.Fprintf(out, "URL:        %s\n", cfg.URL)
	_, _ = fmt.Fprintf(out, "Token:      %s\n", maskGiteaToken(cfg.Token))
	if cfg.Owner != "" && cfg.Repo != "" {
		_, _ = fmt.Fprintf(out, "Repository: %s/%s\n", cfg.Owner, cfg.Repo)
	} else {
		_, _ = fmt.Fprintf(out, "Repository: %s\n", cfg.Repository)
	}
	if cfg.TLSCA != "" {
		_, _ = fmt.Fprintf(out, "TLS CA:     %s\n", cfg.TLSCA)
	}
	if filterLabels != "" || filterMilestone != "" || filterAssignee != "" {
		_, _ = fmt.Fprintln(out, "\nFilters:")
		if filterLabels != "" {
			_, _ = fmt.Fprintf(out, "  Labels:    %s\n", filterLabels)
		}
		if filterMilestone != "" {
			_, _ = fmt.Fprintf(out, "  Milestone: %s\n", filterMilestone)
		}
		if filterAssignee != "" {
			_, _ = fmt.Fprintf(out, "  Assignee:  %s\n", filterAssignee)
		}
	}

	if valErr != nil {
		_, _ = fmt.Fprintln(out, "\nStatus: ✗ Not configured")
		_, _ = fmt.Fprintf(out, "Error: %v\n", valErr)
		return nil
	}

	_, _ = fmt.Fprintln(out, "\nStatus: ✓ Configured")
	return nil
}

func runGiteaRepos(cmd *cobra.Command, _ []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("gitea repos is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("gitea-repos")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	cfg := getGiteaConfig()
	if err := validateGiteaConnectionConfig(cfg); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	client, err := getGiteaClient(cfg)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	ctx := context.Background()
	repos, err := client.ListRepositories(ctx)
	if err != nil {
		return HandleErrorRespectJSON("failed to fetch repositories: %v", err)
	}

	if jsonOutput {
		return outputJSON(repos)
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "Accessible Gitea / Forgejo Repositories")
	_, _ = fmt.Fprintln(out, "=======================================")
	for _, r := range repos {
		_, _ = fmt.Fprintf(out, "  %s\n", r.FullName)
		if r.Description != "" {
			_, _ = fmt.Fprintf(out, "    %s\n", r.Description)
		}
		_, _ = fmt.Fprintf(out, "    %s\n\n", r.HTMLURL)
	}
	if len(repos) == 0 {
		_, _ = fmt.Fprintln(out, "No repositories found")
	}
	return nil
}

type giteaSyncResult struct {
	DryRun       bool     `json:"dry_run"`
	Pulled       int      `json:"pulled"`
	Pushed       int      `json:"pushed"`
	Created      int      `json:"created"`
	Updated      int      `json:"updated"`
	Skipped      int      `json:"skipped"`
	Conflicts    int      `json:"conflicts"`
	Errors       int      `json:"errors"`
	LinksPushed  int      `json:"links_pushed"`
	LinksSkipped int      `json:"links_skipped,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

func runGiteaSync(cmd *cobra.Command, _ []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("gitea sync is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("gitea-sync")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	cfg := getGiteaConfig()
	if err := validateGiteaConfig(cfg); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if !giteaSyncDryRun {
		CheckReadonly("gitea sync")
	}

	if giteaSyncPullOnly && giteaSyncPushOnly {
		return HandleErrorRespectJSON("cannot use both --pull-only and --push-only")
	}

	conflictRes, err := getGiteaConflictStrategy(giteaPreferLocal, giteaPreferGitea, giteaPreferNewer)
	if err != nil {
		return HandleErrorRespectJSON("%v (--prefer-local, --prefer-gitea, --prefer-newer)", err)
	}

	if err := ensureStoreActive(); err != nil {
		return HandleErrorRespectJSON("database not available: %v", err)
	}

	out := cmd.OutOrStdout()
	ctx := context.Background()

	gt := &gitea.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return HandleErrorRespectJSON("initializing Gitea/Forgejo tracker: %v", err)
	}
	if cliFilter := buildGiteaCLIFilter(ctx); cliFilter != nil {
		gt.SetFilter(cliFilter)
	}

	engine := newGiteaEngine(gt, out)
	engine.PullHooks = buildGiteaPullHooks(ctx)

	pull := !giteaSyncPushOnly
	push := !giteaSyncPullOnly

	excludeTypes := parseTypeList(giteaExcludeTypes)
	if giteaTypeFilter == "" && giteaExcludeTypes == "" {
		excludeTypes = []types.IssueType{
			types.TypeMolecule,
			types.TypeMessage,
			types.TypeEvent,
		}
	}

	opts := tracker.SyncOptions{
		Pull:               pull,
		Push:               push,
		DryRun:             giteaSyncDryRun,
		ConflictResolution: conflictRes,
		TypeFilter:         parseTypeList(giteaTypeFilter),
		ExcludeTypes:       excludeTypes,
		ExcludeEphemeral:   giteaNoEphemeral,
	}

	if err := applySelectiveSyncFlags(cmd, &opts, push); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if giteaSyncDryRun && !jsonOutput {
		_, _ = fmt.Fprintln(out, "Dry run mode - no changes will be made")
		_, _ = fmt.Fprintln(out)
	}

	result, err := engine.Sync(ctx, opts)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	var linkWarnings []string
	warnLink := func(msg string) {
		linkWarnings = append(linkWarnings, msg)
		_, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
	}

	var linksPushed, linksSkipped int
	if push {
		linksPushed, linksSkipped = pushGiteaDependencyLinks(ctx, gt, store, opts, giteaSyncDryRun, out, warnLink)
		result.Warnings = append(result.Warnings, linkWarnings...)
	}

	if !giteaSyncDryRun {
		commandDidWrite.Store(true)
	}

	if jsonOutput {
		return outputJSON(giteaSyncResult{
			DryRun:       giteaSyncDryRun,
			Pulled:       result.Stats.Pulled,
			Pushed:       result.Stats.Pushed,
			Created:      result.Stats.Created,
			Updated:      result.Stats.Updated,
			Skipped:      result.Stats.Skipped,
			Conflicts:    result.Stats.Conflicts,
			Errors:       result.Stats.Errors,
			LinksPushed:  linksPushed,
			LinksSkipped: linksSkipped,
			Warnings:     result.Warnings,
		})
	}

	if !giteaSyncDryRun {
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
	} else {
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintln(out, "Run without --dry-run to apply changes")
	}

	return nil
}

type giteaLinkSyncData struct {
	DesiredLinks []gitea.DependencyLink
}

func collectGiteaLinkSyncData(ctx context.Context, st storage.Storage, scope gitea.RefScope, opts tracker.SyncOptions) (giteaLinkSyncData, []string) {
	if st == nil {
		return giteaLinkSyncData{}, []string{"Gitea dependency sync skipped: database not available"}
	}

	reader, err := st.IssueReader()
	if err != nil {
		return giteaLinkSyncData{}, []string{fmt.Sprintf("Gitea dependency sync skipped: %v", err)}
	}
	page, err := reader.List(ctx, giteaLinkSyncListRequest())
	if err != nil {
		return giteaLinkSyncData{}, []string{fmt.Sprintf("Gitea dependency sync skipped: %v", err)}
	}
	allIssues := make([]*types.Issue, 0, len(page.Items))
	for _, item := range page.Items {
		if item == nil || item.Issue == nil {
			continue
		}
		allIssues = append(allIssues, item.Issue)
	}

	var descendantSet map[string]bool
	if opts.ParentID != "" {
		descendantSet, err = buildSyncDescendantSet(ctx, st, opts.ParentID)
		if err != nil {
			return giteaLinkSyncData{}, []string{fmt.Sprintf("Gitea dependency sync skipped: resolving parent %s: %v", opts.ParentID, err)}
		}
	}

	scopedIssues := filterGiteaLinkScopedIssues(allIssues, opts, descendantSet)
	scopedIssueIDs := make(map[string]bool, len(scopedIssues))
	for _, issue := range scopedIssues {
		if issue != nil && issue.ID != "" {
			scopedIssueIDs[issue.ID] = true
		}
	}

	var warnings []string
	var desired []gitea.DependencyLink
	for _, issue := range scopedIssues {
		if issue.ExternalRef == nil {
			continue
		}
		deps, err := st.GetDependenciesWithMetadata(ctx, issue.ID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Gitea dependency sync skipped dependencies for %s: %v", issue.ID, err))
			continue
		}
		for _, dep := range deps {
			if !scopedIssueIDs[dep.ID] {
				continue
			}
			if dep.DependencyType == types.DepBlocks {
				if link, ok := scope.BlockedByLinkFromBeadsDependency(issue, dep); ok {
					desired = append(desired, link)
				}
			}
		}
	}

	return giteaLinkSyncData{DesiredLinks: desired}, warnings
}

func giteaLinkSyncListRequest() issueops.ListRequest {
	unlimited := 0
	return issueops.ListRequest{
		AllFlag:         true,
		IncludeAllTypes: true,
		Limit:           &unlimited,
		SkipLabels:      true,
		SkipCounts:      true,
	}
}

func filterGiteaLinkScopedIssues(issues []*types.Issue, opts tracker.SyncOptions, descendantSet map[string]bool) []*types.Issue {
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
		if descendantSet != nil && !descendantSet[issue.ID] {
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

func pushGiteaDependencyLinks(ctx context.Context, gt *gitea.Tracker, st storage.Storage, opts tracker.SyncOptions, dryRun bool, out io.Writer, warn func(string)) (created int, skipped int) {
	if gt == nil {
		return 0, 0
	}
	linkData, collectWarnings := collectGiteaLinkSyncData(ctx, st, gt.RefScope(), opts)
	for _, warning := range collectWarnings {
		warn(warning)
	}

	resolver := gt.LinkResolver()
	if resolver == nil || len(linkData.DesiredLinks) == 0 {
		return 0, 0
	}

	res := resolver.PushLinks(ctx, linkData.DesiredLinks, gitea.PushLinkOptions{
		DryRun: dryRun,
		OnPlan: func(link gitea.DependencyLink) {
			if !jsonOutput {
				_, _ = fmt.Fprintf(out, "  [dry-run] Would create Gitea dependency: #%d blocked_by #%d\n",
					link.FromNumber, link.ToNumber)
			}
		},
	})
	for _, err := range res.Errors {
		warn(fmt.Sprintf("Gitea dependency sync: %v", err))
	}
	if res.DependenciesDisabledSkipped > 0 {
		warn(fmt.Sprintf("Gitea dependency sync: %d link(s) skipped; issue dependencies appear disabled on this repository/instance", res.DependenciesDisabledSkipped))
	}
	if res.SourceMissing > 0 {
		warn(fmt.Sprintf("Gitea dependency sync: %d link(s) skipped; source issue not readable on Gitea/Forgejo", res.SourceMissing))
	}
	if res.TargetMissing > 0 {
		warn(fmt.Sprintf("Gitea dependency sync: %d link(s) skipped; blocker issue not readable on Gitea/Forgejo", res.TargetMissing))
	}
	return res.Created, res.DependenciesDisabledSkipped + res.SourceMissing + res.TargetMissing
}

func newGiteaEngine(gt *gitea.Tracker, out io.Writer) *tracker.Engine {
	engine := tracker.NewEngine(gt, store, currentActor())
	engine.OnMessage = func(msg string) {
		if !jsonOutput {
			_, _ = fmt.Fprintln(out, "  "+msg)
		}
	}
	engine.OnWarning = func(msg string) { _, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }
	engine.PushHooks = buildGiteaPushHooks(gt)
	return engine
}

func buildGiteaPushHooks(gt *gitea.Tracker) *tracker.PushHooks {
	cfg := gt.MappingConfig()
	if cfg == nil {
		cfg = gitea.DefaultMappingConfig()
	}
	pushFieldsEqual := func(local *types.Issue, remote *tracker.TrackerIssue) bool {
		if remote == nil {
			return false
		}
		rawIssue, ok := remote.Raw.(*gitea.Issue)
		if !ok || rawIssue == nil {
			return false
		}
		return gitea.PushFieldsEqual(local, rawIssue, cfg)
	}
	return &tracker.PushHooks{
		ContentEqual:    pushFieldsEqual,
		PushFieldsEqual: pushFieldsEqual,
		ContentHash: func(local *types.Issue) string {
			return gitea.PushContentHash(local, cfg)
		},
		TargetScope: gt.PushTargetScope,
	}
}

func buildGiteaPullHooks(ctx context.Context) *tracker.PullHooks {
	prefix := "bd"
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
