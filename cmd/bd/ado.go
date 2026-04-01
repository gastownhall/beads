// Package main provides the bd CLI commands.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ado"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// ADOConfig holds Azure DevOps connection configuration.
type ADOConfig struct {
	PAT      string   // Personal access token
	Org      string   // Organization name
	Project  string   // Primary project name (backward compat)
	Projects []string // All project names
	URL      string   // Custom base URL (for on-prem)
}

// adoCmd is the root command for Azure DevOps operations.
var adoCmd = &cobra.Command{
	Use:   "ado",
	Short: "Azure DevOps integration commands",
	Long: `Commands for syncing issues between beads and Azure DevOps.

Configuration can be set via 'bd config' or environment variables:
  ado.org / AZURE_DEVOPS_ORG              - Organization name
  ado.project / AZURE_DEVOPS_PROJECT      - Project name (single)
  ado.projects / AZURE_DEVOPS_PROJECTS    - Project names (comma-separated)
  ado.pat / AZURE_DEVOPS_PAT              - Personal access token
  ado.url / AZURE_DEVOPS_URL              - Custom base URL (on-prem)`,
}

// adoSyncCmd synchronizes issues between beads and Azure DevOps.
var adoSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync issues with Azure DevOps",
	Long: `Synchronize issues between beads and Azure DevOps.

By default, performs bidirectional sync:
- Pulls new/updated work items from Azure DevOps to beads
- Pushes local beads issues to Azure DevOps

Use --pull-only or --push-only to limit direction.

Filters (--area-path, --iteration-path, --types, --states) restrict
which work items are synced. On pull, they limit the WIQL query. On push,
--types and --states filter local beads before pushing to ADO. Use
--no-create with push to skip creating new ADO work items (only update
existing linked items). Filters can also be persisted via config:
  ado.filter.area_path, ado.filter.iteration_path,
  ado.filter.types, ado.filter.states
CLI flags override config values when both are set.`,
	RunE: runADOSync,
}

// adoStatusCmd displays Azure DevOps configuration and sync status.
var adoStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Azure DevOps sync status",
	Long:  `Display current Azure DevOps configuration and sync status.`,
	RunE:  runADOStatus,
}

// adoProjectsCmd lists accessible Azure DevOps projects.
var adoProjectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "List accessible Azure DevOps projects",
	Long:  `List Azure DevOps projects that the configured token has access to.`,
	RunE:  runADOProjects,
}

var (
	adoSyncDryRun     bool
	adoSyncPullOnly   bool
	adoSyncPushOnly   bool
	adoPreferLocal    bool
	adoPreferADO      bool
	adoPreferNewer    bool
	adoBootstrapMatch bool
	adoNoCreate       bool
	adoReconcile      bool

	// Pull filter flags
	adoFilterAreaPath      string
	adoFilterIterationPath string
	adoFilterTypes         string
	adoFilterStates        string
)

// ADOConflictStrategy defines how to resolve conflicts between local and ADO versions.
type ADOConflictStrategy string

const (
	// ADOConflictPreferNewer uses the most recently updated version (default).
	ADOConflictPreferNewer ADOConflictStrategy = "prefer-newer"
	// ADOConflictPreferLocal always keeps the local beads version.
	ADOConflictPreferLocal ADOConflictStrategy = "prefer-local"
	// ADOConflictPreferADO always uses the Azure DevOps version.
	ADOConflictPreferADO ADOConflictStrategy = "prefer-ado"
)

// getADOConflictStrategy determines the conflict strategy from flag values.
// Returns error if multiple conflicting flags are set.
func getADOConflictStrategy(preferLocal, preferADO, preferNewer bool) (ADOConflictStrategy, error) {
	flagsSet := 0
	if preferLocal {
		flagsSet++
	}
	if preferADO {
		flagsSet++
	}
	if preferNewer {
		flagsSet++
	}
	if flagsSet > 1 {
		return "", fmt.Errorf("cannot use multiple conflict resolution flags")
	}

	if preferLocal {
		return ADOConflictPreferLocal, nil
	}
	if preferADO {
		return ADOConflictPreferADO, nil
	}
	return ADOConflictPreferNewer, nil
}

func init() {
	// Add subcommands to ado
	adoCmd.AddCommand(adoSyncCmd)
	adoCmd.AddCommand(adoStatusCmd)
	adoCmd.AddCommand(adoProjectsCmd)

	// Add flags to sync command
	adoSyncCmd.Flags().BoolVar(&adoSyncDryRun, "dry-run", false, "Show what would be synced without making changes")
	adoSyncCmd.Flags().BoolVar(&adoSyncPullOnly, "pull-only", false, "Only pull issues from Azure DevOps")
	adoSyncCmd.Flags().BoolVar(&adoSyncPushOnly, "push-only", false, "Only push issues to Azure DevOps")

	// Conflict resolution flags (mutually exclusive)
	adoSyncCmd.Flags().BoolVar(&adoPreferLocal, "prefer-local", false, "On conflict, keep local beads version")
	adoSyncCmd.Flags().BoolVar(&adoPreferADO, "prefer-ado", false, "On conflict, use Azure DevOps version")
	adoSyncCmd.Flags().BoolVar(&adoPreferNewer, "prefer-newer", false, "On conflict, use most recent version (default)")

	// Additional sync options
	adoSyncCmd.Flags().BoolVar(&adoBootstrapMatch, "bootstrap-match", false, "Enable heuristic matching for first sync")
	adoSyncCmd.Flags().BoolVar(&adoNoCreate, "no-create", false, "Never create new items in either direction (pull or push)")
	adoSyncCmd.Flags().BoolVar(&adoReconcile, "reconcile", false, "Force reconciliation scan for deleted items")

	// Pull filter flags (override config keys ado.filter.*)
	adoSyncCmd.Flags().StringVar(&adoFilterAreaPath, "area-path", "", "Filter to ADO area path (e.g., \"Project\\Team\")")
	adoSyncCmd.Flags().StringVar(&adoFilterIterationPath, "iteration-path", "", "Filter to ADO iteration path (e.g., \"Project\\Sprint 1\")")
	adoSyncCmd.Flags().StringVar(&adoFilterTypes, "types", "", "Filter to work item types, comma-separated (e.g., \"Bug,Task,User Story\")")
	adoSyncCmd.Flags().StringVar(&adoFilterStates, "states", "", "Filter to ADO states, comma-separated (e.g., \"New,Active,Resolved\")")
	adoSyncCmd.Flags().StringSlice("project", nil, "Project name(s) to sync (overrides configured project/projects)")

	// Register ado command with root
	rootCmd.AddCommand(adoCmd)
}

// getADOConfig returns Azure DevOps configuration from bd config or environment.
func getADOConfig() ADOConfig {
	ctx := context.Background()
	cfg := ADOConfig{}

	cfg.PAT = getADOConfigValue(ctx, "ado.pat")
	cfg.Org = getADOConfigValue(ctx, "ado.org")
	cfg.URL = getADOConfigValue(ctx, "ado.url")

	// Resolve projects from all sources.
	pluralVal := getADOConfigValue(ctx, "ado.projects")
	singularVal := getADOConfigValue(ctx, "ado.project")
	cfg.Projects = tracker.ResolveProjectIDs(nil, pluralVal, singularVal)
	if len(cfg.Projects) > 0 {
		cfg.Project = cfg.Projects[0]
	}

	return cfg
}

// getADOConfigValue reads an Azure DevOps configuration value from store or environment.
func getADOConfigValue(ctx context.Context, key string) string {
	// Try to read from store (works in direct mode)
	if store != nil {
		value, _ := store.GetConfig(ctx, key)
		if value != "" {
			return value
		}
	} else if dbPath != "" {
		tempStore, err := dolt.New(ctx, &dolt.Config{Path: dbPath})
		if err == nil {
			defer func() { _ = tempStore.Close() }()
			value, _ := tempStore.GetConfig(ctx, key)
			if value != "" {
				return value
			}
		}
	}

	// Fall back to environment variable
	envKey := adoConfigToEnvVar(key)
	if envKey != "" {
		if value := os.Getenv(envKey); value != "" {
			return value
		}
	}

	return ""
}

// adoConfigToEnvVar maps Azure DevOps config keys to their environment variable names.
func adoConfigToEnvVar(key string) string {
	switch key {
	case "ado.pat":
		return "AZURE_DEVOPS_PAT"
	case "ado.org":
		return "AZURE_DEVOPS_ORG"
	case "ado.project":
		return "AZURE_DEVOPS_PROJECT"
	case "ado.projects":
		return "AZURE_DEVOPS_PROJECTS"
	case "ado.url":
		return "AZURE_DEVOPS_URL"
	default:
		return ""
	}
}

// validateADOConfig checks that required configuration is present.
func validateADOConfig(cfg ADOConfig) error {
	if cfg.PAT == "" {
		return fmt.Errorf("ado.pat not configured: set via 'bd config ado.pat <token>' or AZURE_DEVOPS_PAT env var")
	}
	if cfg.Org == "" && cfg.URL == "" {
		return fmt.Errorf("ado.org not configured: set via 'bd config ado.org <org>' or AZURE_DEVOPS_ORG env var")
	}
	if len(cfg.Projects) == 0 {
		return fmt.Errorf("no ADO project configured\nSet via 'bd config set ado.project <project>'\nOr:  'bd config set ado.projects \"proj1,proj2\"'\nOr: AZURE_DEVOPS_PROJECT env var")
	}
	return nil
}

// maskADOToken masks a token for safe display.
// Shows only the first 4 characters to aid identification without
// revealing enough to reduce brute-force entropy.
func maskADOToken(token string) string {
	if token == "" {
		return "(not set)"
	}
	if len(token) <= 4 {
		return "****"
	}
	return token[:4] + "****"
}

// getADOClient creates an Azure DevOps client from the current configuration.
func getADOClient(cfg ADOConfig) (*ado.Client, error) {
	client := ado.NewClient(ado.NewSecretString(cfg.PAT), cfg.Org, cfg.Project)
	if cfg.URL != "" {
		var err error
		client, err = client.WithBaseURL(cfg.URL)
		if err != nil {
			return nil, err
		}
	}
	return client, nil
}

// splitCSV splits a comma-separated string into trimmed, non-empty parts.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildADOPullFilters constructs PullFilters from CLI flags, falling back to
// config values (ado.filter.*). CLI flags override config when explicitly set.
// Returns nil when no filters are configured.
func buildADOPullFilters(ctx context.Context, cmd *cobra.Command) *ado.PullFilters {
	areaPath := adoFilterAreaPath
	if !cmd.Flags().Changed("area-path") {
		if v := getADOConfigValue(ctx, "ado.filter.area_path"); v != "" {
			areaPath = v
		}
	}

	iterationPath := adoFilterIterationPath
	if !cmd.Flags().Changed("iteration-path") {
		if v := getADOConfigValue(ctx, "ado.filter.iteration_path"); v != "" {
			iterationPath = v
		}
	}

	typesStr := adoFilterTypes
	if !cmd.Flags().Changed("types") {
		if v := getADOConfigValue(ctx, "ado.filter.types"); v != "" {
			typesStr = v
		}
	}

	statesStr := adoFilterStates
	if !cmd.Flags().Changed("states") {
		if v := getADOConfigValue(ctx, "ado.filter.states"); v != "" {
			statesStr = v
		}
	}

	types := splitCSV(typesStr)
	states := splitCSV(statesStr)

	if areaPath == "" && iterationPath == "" && len(types) == 0 && len(states) == 0 {
		return nil
	}

	return &ado.PullFilters{
		AreaPath:      areaPath,
		IterationPath: iterationPath,
		WorkItemTypes: types,
		States:        states,
	}
}

// adoStatusResult holds the JSON output for the ado status command.
type adoStatusResult struct {
	Org        string   `json:"org"`
	Project    string   `json:"project"`
	Projects   []string `json:"projects,omitempty"`
	HasToken   bool     `json:"has_token"`
	URL        string   `json:"url,omitempty"`
	Configured bool     `json:"configured"`
	Error      string   `json:"error,omitempty"`
}

// runADOStatus implements the ado status command.
func runADOStatus(cmd *cobra.Command, _ []string) error {
	cfg := getADOConfig()

	if jsonOutput {
		result := adoStatusResult{
			Org:      cfg.Org,
			Project:  cfg.Project,
			Projects: cfg.Projects,
			HasToken: cfg.PAT != "",
			URL:      cfg.URL,
		}
		if err := validateADOConfig(cfg); err != nil {
			result.Configured = false
			result.Error = err.Error()
		} else {
			result.Configured = true
		}
		outputJSON(result)
		return nil
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "Azure DevOps Configuration")
	_, _ = fmt.Fprintln(out, "==========================")
	_, _ = fmt.Fprintf(out, "Organization: %s\n", cfg.Org)
	if len(cfg.Projects) <= 1 {
		_, _ = fmt.Fprintf(out, "Project:      %s\n", cfg.Project)
	} else {
		_, _ = fmt.Fprintf(out, "Projects:     %s (%d projects)\n", strings.Join(cfg.Projects, ", "), len(cfg.Projects))
	}
	_, _ = fmt.Fprintf(out, "PAT:          %s\n", maskADOToken(cfg.PAT))
	if cfg.URL != "" {
		_, _ = fmt.Fprintf(out, "Base URL:     %s\n", cfg.URL)
	}

	// Validate configuration
	if err := validateADOConfig(cfg); err != nil {
		_, _ = fmt.Fprintf(out, "\nStatus: ❌ Not configured\n")
		_, _ = fmt.Fprintf(out, "Error: %v\n", err)
		return nil
	}

	_, _ = fmt.Fprintf(out, "\nStatus: ✓ Configured\n")
	return nil
}

// runADOProjects implements the ado projects command.
func runADOProjects(cmd *cobra.Command, _ []string) error {
	cfg := getADOConfig()
	if cfg.PAT == "" {
		return fmt.Errorf("ado.pat not configured: set via 'bd config ado.pat <token>' or AZURE_DEVOPS_PAT env var")
	}
	if cfg.Org == "" && cfg.URL == "" {
		return fmt.Errorf("ado.org not configured: set via 'bd config ado.org <org>' or AZURE_DEVOPS_ORG env var")
	}

	out := cmd.OutOrStdout()
	client, err := getADOClient(cfg)
	if err != nil {
		return fmt.Errorf("invalid ADO configuration: %w", err)
	}
	ctx := context.Background()

	projects, err := client.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}

	if jsonOutput {
		outputJSON(projects)
		return nil
	}

	_, _ = fmt.Fprintln(out, "Azure DevOps Projects")
	_, _ = fmt.Fprintln(out, "=====================")
	for _, p := range projects {
		_, _ = fmt.Fprintf(out, "  %s\n", p.Name)
		if p.Description != "" {
			_, _ = fmt.Fprintf(out, "    %s\n", p.Description)
		}
	}

	if len(projects) == 0 {
		_, _ = fmt.Fprintln(out, "No projects found")
	}

	return nil
}

// adoSyncResult holds the JSON output for the ado sync command.
type adoSyncResult struct {
	DryRun           bool     `json:"dry_run"`
	Pulled           int      `json:"pulled"`
	Pushed           int      `json:"pushed"`
	Created          int      `json:"created"`
	Updated          int      `json:"updated"`
	Skipped          int      `json:"skipped"`
	Conflicts        int      `json:"conflicts"`
	Errors           int      `json:"errors"`
	LinksPushed      int      `json:"links_pushed,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
	BootstrapMatched int      `json:"bootstrap_matched,omitempty"`
	Reconciled       bool     `json:"reconciled,omitempty"`
	ReconcileChecked int      `json:"reconcile_checked,omitempty"`
	ReconcileDeleted int      `json:"reconcile_deleted,omitempty"`
	ReconcileDenied  int      `json:"reconcile_denied,omitempty"`
}

// runADOSync implements the ado sync command.
// Uses the tracker.Engine for all sync operations.
func runADOSync(cmd *cobra.Command, _ []string) error {
	cfg := getADOConfig()
	if err := validateADOConfig(cfg); err != nil {
		return err
	}

	if !adoSyncDryRun {
		CheckReadonly("ado sync")
	}

	if adoSyncPullOnly && adoSyncPushOnly {
		return fmt.Errorf("cannot use both --pull-only and --push-only")
	}

	// Validate conflict flags
	conflictStrategy, err := getADOConflictStrategy(adoPreferLocal, adoPreferADO, adoPreferNewer)
	if err != nil {
		return fmt.Errorf("%w (--prefer-local, --prefer-ado, --prefer-newer)", err)
	}

	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	out := cmd.OutOrStdout()
	ctx := context.Background()

	// Create and initialize the ADO tracker
	at := &ado.Tracker{}
	cliProjects, _ := cmd.Flags().GetStringSlice("project")
	if len(cliProjects) > 0 {
		at.SetProjects(tracker.DeduplicateStrings(cliProjects))
	}
	if err := at.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing Azure DevOps tracker: %w", err)
	}

	// Build pull filters from CLI flags, falling back to config values.
	filters := buildADOPullFilters(ctx, cmd)
	if filters != nil {
		if err := filters.Validate(); err != nil {
			return fmt.Errorf("invalid pull filter: %w", err)
		}
		at.SetFilters(filters)
	}

	// Create the sync engine
	engine := tracker.NewEngine(at, store, actor)
	var warnings []string
	if !jsonOutput {
		engine.OnMessage = func(msg string) { _, _ = fmt.Fprintln(out, "  "+msg) }
	}
	engine.OnWarning = func(msg string) {
		warnings = append(warnings, msg)
		_, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
	}

	// Set up ADO-specific pull hooks (with bootstrap matching and no-create support)
	var bootstrapMatched int
	engine.PullHooks = buildADOPullHooks(ctx, at, adoBootstrapMatch, adoNoCreate, &bootstrapMatched, engine.OnWarning)

	// Set up ADO-specific push hooks (type/state/no-create filtering for push)
	engine.PushHooks = buildADOPushHooks(at.FieldMapper(), at.IsExternalRef, filters, adoNoCreate)

	// Build sync options from CLI flags
	pull := !adoSyncPushOnly
	push := !adoSyncPullOnly

	opts := tracker.SyncOptions{
		Pull:   pull,
		Push:   push,
		DryRun: adoSyncDryRun,
	}

	// Map conflict resolution
	switch conflictStrategy {
	case ADOConflictPreferLocal:
		opts.ConflictResolution = tracker.ConflictLocal
	case ADOConflictPreferADO:
		opts.ConflictResolution = tracker.ConflictExternal
	default:
		opts.ConflictResolution = tracker.ConflictTimestamp
	}

	if adoSyncDryRun && !jsonOutput {
		_, _ = fmt.Fprintln(out, "Dry run mode - no changes will be made")
		_, _ = fmt.Fprintln(out)
	}

	// Run sync
	result, err := engine.Sync(ctx, opts)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return err
	}

	// Link push pass: sync beads dependencies → ADO work item relations.
	var linksPushed int
	if !adoSyncDryRun && push {
		adoClient := at.ADOClient()
		if adoClient != nil {
			linkResolver := ado.NewLinkResolver(adoClient)
			lp, linkWarns := pushADOLinks(ctx, linkResolver, at, store, engine.OnWarning)
			linksPushed = lp
			warnings = append(warnings, linkWarns...)
		}
	}

	// Reconciliation: detect deleted/inaccessible ADO work items.
	var reconcileResult *ado.ReconcileResult
	if !adoSyncDryRun {
		client, cerr := getADOClient(cfg)
		if cerr != nil {
			warnings = append(warnings, fmt.Sprintf("Reconciliation skipped: %v", cerr))
		} else {
			reconciler := ado.NewReconciler(client, store)

			shouldReconcile := adoReconcile || reconciler.ShouldReconcile(ctx)
			if shouldReconcile {
				adoIDMap := collectADOWorkItemMap(ctx, at)
				workItemIDs := make([]int, 0, len(adoIDMap))
				for id := range adoIDMap {
					workItemIDs = append(workItemIDs, id)
				}
				if len(workItemIDs) > 0 {
					rr, rerr := reconciler.Reconcile(ctx, workItemIDs)
					if rerr != nil {
						warnings = append(warnings, fmt.Sprintf("Reconciliation failed: %v", rerr))
						if !jsonOutput {
							_, _ = fmt.Fprintf(os.Stderr, "Warning: Reconciliation failed: %v\n", rerr)
						}
					} else {
						reconcileResult = rr
						// Close local issues whose ADO work items were deleted.
						for _, idStr := range rr.Deleted {
							adoID, err := strconv.Atoi(idStr)
							if err != nil {
								continue
							}
							localID, ok := adoIDMap[adoID]
							if !ok {
								continue
							}
							reason := fmt.Sprintf("ADO work item %s deleted", idStr)
							if cerr := store.CloseIssue(ctx, localID, reason, actor, ""); cerr != nil {
								msg := fmt.Sprintf("Failed to close %s for deleted ADO #%s: %v", localID, idStr, cerr)
								warnings = append(warnings, msg)
								if !jsonOutput {
									_, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
								}
							} else {
								msg := fmt.Sprintf("Closed %s: ADO work item %s deleted", localID, idStr)
								warnings = append(warnings, msg)
								if !jsonOutput {
									_, _ = fmt.Fprintf(out, "  %s\n", msg)
								}
							}
						}
						for _, id := range rr.Denied {
							msg := fmt.Sprintf("ADO work item %s access denied (403)", id)
							warnings = append(warnings, msg)
							if !jsonOutput {
								_, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
							}
						}
					}
				}
				if err := reconciler.ResetCounter(ctx); err != nil && !jsonOutput {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to reset reconcile counter: %v\n", err)
				}
			} else {
				if err := reconciler.IncrementCounter(ctx); err != nil && !jsonOutput {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to increment reconcile counter: %v\n", err)
				}
			}
		}
	}

	// JSON output
	if jsonOutput {
		syncResult := adoSyncResult{
			DryRun:           adoSyncDryRun,
			Pulled:           result.Stats.Pulled,
			Pushed:           result.Stats.Pushed,
			Created:          result.Stats.Created,
			Updated:          result.Stats.Updated,
			Skipped:          result.Stats.Skipped,
			Conflicts:        result.Stats.Conflicts,
			Errors:           result.Stats.Errors,
			LinksPushed:      linksPushed,
			Warnings:         append(result.Warnings, warnings...),
			BootstrapMatched: bootstrapMatched,
		}
		if reconcileResult != nil {
			syncResult.Reconciled = true
			syncResult.ReconcileChecked = reconcileResult.Checked
			syncResult.ReconcileDeleted = len(reconcileResult.Deleted)
			syncResult.ReconcileDenied = len(reconcileResult.Denied)
		}
		outputJSON(syncResult)
		return nil
	}

	// Human-readable output
	if !adoSyncDryRun {
		if bootstrapMatched > 0 {
			_, _ = fmt.Fprintf(out, "✓ Bootstrap matched %d issues\n", bootstrapMatched)
		}
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
		if reconcileResult != nil {
			_, _ = fmt.Fprintf(out, "✓ Reconciled %d work items", reconcileResult.Checked)
			if len(reconcileResult.Deleted) > 0 || len(reconcileResult.Denied) > 0 {
				_, _ = fmt.Fprintf(out, " (%d deleted, %d denied)",
					len(reconcileResult.Deleted), len(reconcileResult.Denied))
			}
			_, _ = fmt.Fprintln(out)
		}
	}

	if adoSyncDryRun {
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintln(out, "Run without --dry-run to apply changes")
	}

	// Embedded mode: flush Dolt commit after sync writes.
	if isEmbeddedMode() && !adoSyncDryRun && store != nil {
		if _, commitErr := store.CommitPending(rootCtx, actor); commitErr != nil {
			return fmt.Errorf("failed to commit: %w", commitErr)
		}
	}

	return nil
}

// collectADOWorkItemMap gathers ADO work item IDs from local issues that
// have ADO external refs, returning a map of ADO numeric ID → local issue ID.
func collectADOWorkItemMap(ctx context.Context, at *ado.Tracker) map[int]string {
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil
	}

	m := make(map[int]string)
	for _, issue := range allIssues {
		if issue.ExternalRef == nil {
			continue
		}
		ref := *issue.ExternalRef
		if !at.IsExternalRef(ref) {
			continue
		}
		idStr := at.ExtractIdentifier(ref)
		if id, err := strconv.Atoi(idStr); err == nil {
			m[id] = issue.ID
		}
	}
	return m
}

// pushADOLinks syncs beads dependencies to ADO work item relations for all
// local issues with ADO external refs. Returns the number of links synced
// and any warnings.
func pushADOLinks(ctx context.Context, resolver *ado.LinkResolver, at *ado.Tracker, st storage.Storage, warn func(string)) (int, []string) {
	allIssues, err := st.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return 0, []string{fmt.Sprintf("Link sync skipped: %v", err)}
	}

	var warnings []string
	linkCount := 0

	for _, issue := range allIssues {
		if issue.ExternalRef == nil {
			continue
		}
		ref := *issue.ExternalRef
		if !at.IsExternalRef(ref) {
			continue
		}
		extIDStr := at.ExtractIdentifier(ref)
		workItemID, err := strconv.Atoi(extIDStr)
		if err != nil {
			continue
		}

		// Get local dependencies for this issue.
		deps, err := st.GetDependenciesWithMetadata(ctx, issue.ID)
		if err != nil {
			continue
		}

		// Build desired DependencyInfo list, resolving local IDs to ADO external IDs.
		var desired []tracker.DependencyInfo
		for _, dep := range deps {
			if dep.ExternalRef == nil {
				continue
			}
			depRef := *dep.ExternalRef
			if !at.IsExternalRef(depRef) {
				continue
			}
			targetExtID := at.ExtractIdentifier(depRef)
			if targetExtID == "" {
				continue
			}
			desired = append(desired, tracker.DependencyInfo{
				FromExternalID: extIDStr,
				ToExternalID:   targetExtID,
				Type:           string(dep.DependencyType),
			})
		}

		if len(desired) == 0 {
			continue
		}

		// Fetch current ADO work item to get existing relations.
		adoClient := at.ADOClient()
		items, ferr := adoClient.FetchWorkItems(ctx, []int{workItemID})
		if ferr != nil || len(items) == 0 {
			if warn != nil {
				warn(fmt.Sprintf("Failed to fetch ADO #%d for link sync: %v", workItemID, ferr))
			}
			continue
		}

		errs := resolver.PushLinks(ctx, workItemID, items[0].Relations, desired)
		for _, e := range errs {
			msg := fmt.Sprintf("Link sync ADO #%d: %v", workItemID, e)
			warnings = append(warnings, msg)
			if warn != nil {
				warn(msg)
			}
		}
		linkCount += len(desired) - len(errs)
	}

	return linkCount, warnings
}

// buildADOPullHooks creates PullHooks for ADO-specific pull behavior.
// When bootstrapMatch is true, incoming ADO items are matched against existing
// local issues by external_ref, source_system, and heuristic before creating
// duplicates. When noCreate is true, unmatched items are skipped entirely.
func buildADOPullHooks(ctx context.Context, at *ado.Tracker, bootstrapMatch, noCreate bool, matchCount *int, warn func(string)) *tracker.PullHooks {
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

	hooks := &tracker.PullHooks{
		GenerateID: func(_ context.Context, issue *types.Issue) error {
			if issue.ID == "" {
				issue.ID = generateIssueID(prefix)
			}
			return nil
		},
	}

	if bootstrapMatch || noCreate {
		// Pre-load and index local issues for bootstrap matching.
		var idx *ado.BootstrapIndex
		var bm *ado.BootstrapMatcher
		if bootstrapMatch {
			localIssues, _ := store.SearchIssues(ctx, "", types.IssueFilter{})
			idx = ado.BuildBootstrapIndex(localIssues)
			bm = ado.NewBootstrapMatcher(at.FieldMapper(), true)
		}

		hooks.ShouldImport = func(extIssue *tracker.TrackerIssue) bool {
			// Check if already linked via external ref.
			ref := at.BuildExternalRef(extIssue)
			existing, _ := store.GetIssueByExternalRef(ctx, ref)
			if existing != nil {
				return true // Already linked; let engine handle update.
			}

			// Try bootstrap matching against indexed local issues.
			if bm != nil {
				result := bm.FindMatchIndexed(extIssue, idx)
				if result.Matched {
					// Link the existing local issue to this ADO item.
					updates := map[string]interface{}{
						"external_ref":  ref,
						"source_system": "ado:" + extIssue.ID,
					}
					if err := store.UpdateIssue(ctx, result.BeadsID, updates, actor); err == nil {
						*matchCount++
						if warn != nil {
							warn(fmt.Sprintf("Bootstrap matched ADO #%s → %s (%s)", extIssue.ID, result.BeadsID, result.MatchType))
						}
						return true // GetIssueByExternalRef will now find it.
					}
				}
				if result.Candidates > 1 && warn != nil {
					warn(fmt.Sprintf("Ambiguous bootstrap match for ADO #%s: %d candidates", extIssue.ID, result.Candidates))
				}
			}

			// No match found — skip if noCreate, otherwise let engine create.
			return !noCreate
		}
	}

	return hooks
}

// buildADOPushHooks creates PushHooks for ADO-specific push filtering.
// When --types or --states are set, local beads are filtered before pushing
// to ADO by mapping the ADO filter values to beads types/statuses.
// When noCreate is true, only issues already linked to ADO work items
// are pushed (no new work items are created).
func buildADOPushHooks(mapper tracker.FieldMapper, isExternalRef func(string) bool, filters *ado.PullFilters, noCreate bool) *tracker.PushHooks {
	var allowedTypes map[types.IssueType]bool
	var allowedStatuses map[types.Status]bool

	if filters != nil && len(filters.WorkItemTypes) > 0 {
		allowedTypes = make(map[types.IssueType]bool, len(filters.WorkItemTypes))
		for _, adoType := range filters.WorkItemTypes {
			beadsType := mapper.TypeToBeads(adoType)
			allowedTypes[beadsType] = true
		}
	}

	if filters != nil && len(filters.States) > 0 {
		allowedStatuses = make(map[types.Status]bool, len(filters.States))
		for _, adoState := range filters.States {
			beadsStatus := mapper.StatusToBeads(adoState)
			allowedStatuses[beadsStatus] = true
		}
	}

	if allowedTypes == nil && allowedStatuses == nil && !noCreate {
		return nil
	}

	return &tracker.PushHooks{
		ShouldPush: func(issue *types.Issue) bool {
			if allowedTypes != nil && !allowedTypes[issue.IssueType] {
				return false
			}
			if allowedStatuses != nil && !allowedStatuses[issue.Status] {
				return false
			}
			if noCreate {
				if issue.ExternalRef == nil || *issue.ExternalRef == "" || !isExternalRef(*issue.ExternalRef) {
					return false
				}
			}
			return true
		},
	}
}
