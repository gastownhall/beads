package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// linearCmd is the root command for Linear integration.
var linearCmd = &cobra.Command{
	Use:     "linear",
	GroupID: "advanced",
	Short:   "Linear integration commands",
	Long: `Synchronize issues between beads and Linear.

Configuration:
  bd config set linear.api_key "YOUR_API_KEY"
  bd config set linear.team_id "TEAM_ID"

Environment variables (alternative to config):
  LINEAR_API_KEY - Linear API key
  LINEAR_TEAM_ID - Linear team ID (UUID)

Data Mapping (optional, sensible defaults provided):
  Priority mapping (Linear 0-4 to Beads 0-4):
    bd config set linear.priority_map.0 4    # No priority -> Backlog
    bd config set linear.priority_map.1 0    # Urgent -> Critical
    bd config set linear.priority_map.2 1    # High -> High
    bd config set linear.priority_map.3 2    # Medium -> Medium
    bd config set linear.priority_map.4 3    # Low -> Low

  State mapping (Linear state type to Beads status):
    bd config set linear.state_map.backlog open
    bd config set linear.state_map.unstarted open
    bd config set linear.state_map.started in_progress
    bd config set linear.state_map.completed closed
    bd config set linear.state_map.canceled closed
    bd config set linear.state_map.my_custom_state in_progress  # Custom state names

  Label to issue type mapping:
    bd config set linear.label_type_map.bug bug
    bd config set linear.label_type_map.feature feature
    bd config set linear.label_type_map.epic epic

  Relation type mapping (Linear relations to Beads dependencies):
    bd config set linear.relation_map.blocks blocks
    bd config set linear.relation_map.blockedBy blocks
    bd config set linear.relation_map.duplicate duplicates
    bd config set linear.relation_map.related related

  ID generation (optional, hash IDs to match bd/Jira hash mode):
    bd config set linear.id_mode "hash"      # hash (default)
    bd config set linear.hash_length "6"     # hash length 3-8 (default: 6)

Examples:
  bd linear sync --pull         # Import issues from Linear
  bd linear sync --push         # Export issues to Linear
  bd linear sync                # Bidirectional sync (pull then push)
  bd linear sync --dry-run      # Preview sync without changes
  bd linear status              # Show sync status`,
}

// linearSyncCmd handles synchronization with Linear.
var linearSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Synchronize issues with Linear",
	Long: `Synchronize issues between beads and Linear.

Modes:
  --pull         Import issues from Linear into beads
  --push         Export issues from beads to Linear
  (no flags)     Bidirectional sync: pull then push, with conflict resolution

Conflict Resolution:
  By default, newer timestamp wins. Override with:
  --prefer-local    Always prefer local beads version
  --prefer-linear   Always prefer Linear version

Examples:
  bd linear sync --pull                # Import from Linear
  bd linear sync --push --create-only  # Push new issues only
  bd linear sync --dry-run             # Preview without changes
  bd linear sync --prefer-local        # Bidirectional, local wins`,
	Run: runLinearSync,
}

// linearStatusCmd shows the current sync status.
var linearStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Linear sync status",
	Long: `Show the current Linear sync status, including:
  - Last sync timestamp
  - Configuration status
  - Number of issues with Linear links
  - Issues pending push (no external_ref)`,
	Run: runLinearStatus,
}

// linearTeamsCmd lists available teams.
var linearTeamsCmd = &cobra.Command{
	Use:   "teams",
	Short: "List available Linear teams",
	Long: `List all teams accessible with your Linear API key.

Use this to find the team ID (UUID) needed for configuration.

Example:
  bd linear teams
  bd config set linear.team_id "12345678-1234-1234-1234-123456789abc"`,
	Run: runLinearTeams,
}

func init() {
	linearSyncCmd.Flags().Bool("pull", false, "Pull issues from Linear")
	linearSyncCmd.Flags().Bool("push", false, "Push issues to Linear")
	linearSyncCmd.Flags().Bool("dry-run", false, "Preview sync without making changes")
	linearSyncCmd.Flags().Bool("prefer-local", false, "Prefer local version on conflicts")
	linearSyncCmd.Flags().Bool("prefer-linear", false, "Prefer Linear version on conflicts")
	linearSyncCmd.Flags().Bool("create-only", false, "Only create new issues, don't update existing")
	linearSyncCmd.Flags().Bool("update-refs", true, "Update external_ref after creating Linear issues")
	linearSyncCmd.Flags().String("state", "all", "Issue state to sync: open, closed, all")

	linearCmd.AddCommand(linearSyncCmd)
	linearCmd.AddCommand(linearStatusCmd)
	linearCmd.AddCommand(linearTeamsCmd)
	rootCmd.AddCommand(linearCmd)
}

func runLinearSync(cmd *cobra.Command, args []string) {
	pull, _ := cmd.Flags().GetBool("pull")
	push, _ := cmd.Flags().GetBool("push")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	preferLocal, _ := cmd.Flags().GetBool("prefer-local")
	preferLinear, _ := cmd.Flags().GetBool("prefer-linear")
	createOnly, _ := cmd.Flags().GetBool("create-only")
	updateRefs, _ := cmd.Flags().GetBool("update-refs")
	state, _ := cmd.Flags().GetString("state")

	if !dryRun {
		CheckReadonly("linear sync")
	}

	if preferLocal && preferLinear {
		fmt.Fprintf(os.Stderr, "Error: cannot use both --prefer-local and --prefer-linear\n")
		os.Exit(1)
	}

	if err := ensureStoreActive(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: database not available: %v\n", err)
		os.Exit(1)
	}

	if err := validateLinearConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if !pull && !push {
		pull = true
		push = true
	}

	ctx := rootCtx
	result := &linear.SyncResult{Success: true}
	var forceUpdateIDs map[string]bool
	var skipUpdateIDs map[string]bool
	var prePullConflicts []linear.Conflict
	var prePullSkipLinearIDs map[string]bool

	if pull {
		if preferLocal || preferLinear {
			conflicts, err := detectLinearConflicts(ctx)
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("conflict detection failed: %v", err))
			} else if len(conflicts) > 0 {
				prePullConflicts = conflicts
				if preferLocal {
					prePullSkipLinearIDs = make(map[string]bool, len(conflicts))
					forceUpdateIDs = make(map[string]bool, len(conflicts))
					for _, conflict := range conflicts {
						prePullSkipLinearIDs[conflict.LinearIdentifier] = true
						forceUpdateIDs[conflict.IssueID] = true
					}
				} else if preferLinear {
					skipUpdateIDs = make(map[string]bool, len(conflicts))
					for _, conflict := range conflicts {
						skipUpdateIDs[conflict.IssueID] = true
					}
				}
			}
		}

		if dryRun {
			fmt.Println("→ [DRY RUN] Would pull issues from Linear")
		} else {
			fmt.Println("→ Pulling issues from Linear...")
		}

		pullStats, err := doPullFromLinear(ctx, dryRun, state, prePullSkipLinearIDs)
		if err != nil {
			result.Success = false
			result.Error = err.Error()
			if jsonOutput {
				outputJSON(result)
			} else {
				fmt.Fprintf(os.Stderr, "Error pulling from Linear: %v\n", err)
			}
			os.Exit(1)
		}

		result.Stats.Pulled = pullStats.Created + pullStats.Updated
		result.Stats.Created += pullStats.Created
		result.Stats.Updated += pullStats.Updated
		result.Stats.Skipped += pullStats.Skipped

		if !dryRun {
			fmt.Printf("✓ Pulled %d issues (%d created, %d updated)\n",
				result.Stats.Pulled, pullStats.Created, pullStats.Updated)
		}
	}

	if pull && push {
		conflicts := prePullConflicts
		var err error
		if conflicts == nil {
			conflicts, err = detectLinearConflicts(ctx)
		}
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("conflict detection failed: %v", err))
		} else if len(conflicts) > 0 {
			result.Stats.Conflicts = len(conflicts)
			if dryRun {
				if preferLocal {
					fmt.Printf("→ [DRY RUN] Would resolve %d conflicts (preferring local)\n", len(conflicts))
					forceUpdateIDs = make(map[string]bool, len(conflicts))
					for _, conflict := range conflicts {
						forceUpdateIDs[conflict.IssueID] = true
					}
				} else if preferLinear {
					fmt.Printf("→ [DRY RUN] Would resolve %d conflicts (preferring Linear)\n", len(conflicts))
					skipUpdateIDs = make(map[string]bool, len(conflicts))
					for _, conflict := range conflicts {
						skipUpdateIDs[conflict.IssueID] = true
					}
				} else {
					fmt.Printf("→ [DRY RUN] Would resolve %d conflicts (newer wins)\n", len(conflicts))
					var linearWins []linear.Conflict
					var localWins []linear.Conflict
					for _, conflict := range conflicts {
						if conflict.LinearUpdated.After(conflict.LocalUpdated) {
							linearWins = append(linearWins, conflict)
						} else {
							localWins = append(localWins, conflict)
						}
					}
					if len(localWins) > 0 {
						forceUpdateIDs = make(map[string]bool, len(localWins))
						for _, conflict := range localWins {
							forceUpdateIDs[conflict.IssueID] = true
						}
					}
					if len(linearWins) > 0 {
						skipUpdateIDs = make(map[string]bool, len(linearWins))
						for _, conflict := range linearWins {
							skipUpdateIDs[conflict.IssueID] = true
						}
					}
				}
			} else if preferLocal {
				fmt.Printf("→ Resolving %d conflicts (preferring local)\n", len(conflicts))
				if forceUpdateIDs == nil {
					forceUpdateIDs = make(map[string]bool, len(conflicts))
					for _, conflict := range conflicts {
						forceUpdateIDs[conflict.IssueID] = true
					}
				}
			} else if preferLinear {
				fmt.Printf("→ Resolving %d conflicts (preferring Linear)\n", len(conflicts))
				if skipUpdateIDs == nil {
					skipUpdateIDs = make(map[string]bool, len(conflicts))
					for _, conflict := range conflicts {
						skipUpdateIDs[conflict.IssueID] = true
					}
				}
				if prePullConflicts == nil {
					if err := reimportLinearConflicts(ctx, conflicts); err != nil {
						result.Warnings = append(result.Warnings, fmt.Sprintf("conflict resolution failed: %v", err))
					}
				}
			} else {
				fmt.Printf("→ Resolving %d conflicts (newer wins)\n", len(conflicts))
				if err := resolveLinearConflictsByTimestamp(ctx, conflicts); err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("conflict resolution failed: %v", err))
				}
			}
		}
	}

	if push {
		if dryRun {
			fmt.Println("→ [DRY RUN] Would push issues to Linear")
		} else {
			fmt.Println("→ Pushing issues to Linear...")
		}

		pushStats, err := doPushToLinear(ctx, dryRun, createOnly, updateRefs, forceUpdateIDs, skipUpdateIDs)
		if err != nil {
			result.Success = false
			result.Error = err.Error()
			if jsonOutput {
				outputJSON(result)
			} else {
				fmt.Fprintf(os.Stderr, "Error pushing to Linear: %v\n", err)
			}
			os.Exit(1)
		}

		result.Stats.Pushed = pushStats.Created + pushStats.Updated
		result.Stats.Created += pushStats.Created
		result.Stats.Updated += pushStats.Updated
		result.Stats.Skipped += pushStats.Skipped
		result.Stats.Errors += pushStats.Errors

		if !dryRun {
			fmt.Printf("✓ Pushed %d issues (%d created, %d updated)\n",
				result.Stats.Pushed, pushStats.Created, pushStats.Updated)
		}
	}

	if !dryRun && result.Success {
		result.LastSync = time.Now().Format(time.RFC3339)
		if err := store.SetConfig(ctx, "linear.last_sync", result.LastSync); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("failed to update last_sync: %v", err))
		}
	}

	if jsonOutput {
		outputJSON(result)
	} else if dryRun {
		fmt.Println("\n✓ Dry run complete (no changes made)")
	} else {
		fmt.Println("\n✓ Linear sync complete")
		if len(result.Warnings) > 0 {
			fmt.Println("\nWarnings:")
			for _, w := range result.Warnings {
				fmt.Printf("  - %s\n", w)
			}
		}
	}
}

func runLinearStatus(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	if err := ensureStoreActive(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	apiKey, _ := getLinearConfig(ctx, "linear.api_key")
	teamID, _ := getLinearConfig(ctx, "linear.team_id")
	lastSync, _ := store.GetConfig(ctx, "linear.last_sync")

	configured := apiKey != "" && teamID != ""

	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	withLinearRef := 0
	pendingPush := 0
	for _, issue := range allIssues {
		if issue.ExternalRef != nil && linear.IsLinearExternalRef(*issue.ExternalRef) {
			withLinearRef++
		} else if issue.ExternalRef == nil {
			pendingPush++
		}
	}

	if jsonOutput {
		hasAPIKey := apiKey != ""
		outputJSON(map[string]interface{}{
			"configured":      configured,
			"has_api_key":     hasAPIKey,
			"team_id":         teamID,
			"last_sync":       lastSync,
			"total_issues":    len(allIssues),
			"with_linear_ref": withLinearRef,
			"pending_push":    pendingPush,
		})
		return
	}

	fmt.Println("Linear Sync Status")
	fmt.Println("==================")
	fmt.Println()

	if !configured {
		fmt.Println("Status: Not configured")
		fmt.Println()
		fmt.Println("To configure Linear integration:")
		fmt.Println("  bd config set linear.api_key \"YOUR_API_KEY\"")
		fmt.Println("  bd config set linear.team_id \"TEAM_ID\"")
		fmt.Println()
		fmt.Println("Or use environment variables:")
		fmt.Println("  export LINEAR_API_KEY=\"YOUR_API_KEY\"")
		fmt.Println("  export LINEAR_TEAM_ID=\"TEAM_ID\"")
		return
	}

	fmt.Printf("Team ID:      %s\n", teamID)
	fmt.Printf("API Key:      %s\n", maskAPIKey(apiKey))
	if lastSync != "" {
		fmt.Printf("Last Sync:    %s\n", lastSync)
	} else {
		fmt.Println("Last Sync:    Never")
	}
	fmt.Println()
	fmt.Printf("Total Issues: %d\n", len(allIssues))
	fmt.Printf("With Linear:  %d\n", withLinearRef)
	fmt.Printf("Local Only:   %d\n", pendingPush)

	if pendingPush > 0 {
		fmt.Println()
		fmt.Printf("Run 'bd linear sync --push' to push %d local issue(s) to Linear\n", pendingPush)
	}
}

func runLinearTeams(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	apiKey, apiKeySource := getLinearConfig(ctx, "linear.api_key")
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "Error: Linear API key not configured\n")
		fmt.Fprintf(os.Stderr, "Run: bd config set linear.api_key \"YOUR_API_KEY\"\n")
		fmt.Fprintf(os.Stderr, "Or:  export LINEAR_API_KEY=YOUR_API_KEY\n")
		os.Exit(1)
	}

	debug.Logf("Using API key from %s", apiKeySource)

	client := linear.NewClient(apiKey, "")

	teams, err := client.FetchTeams(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching teams: %v\n", err)
		os.Exit(1)
	}

	if len(teams) == 0 {
		fmt.Println("No teams found (check your API key permissions)")
		return
	}

	if jsonOutput {
		outputJSON(teams)
		return
	}

	fmt.Println("Available Linear Teams")
	fmt.Println("======================")
	fmt.Println()
	fmt.Printf("%-40s  %-6s  %s\n", "ID (use this for linear.team_id)", "Key", "Name")
	fmt.Printf("%-40s  %-6s  %s\n", "----------------------------------------", "------", "----")
	for _, team := range teams {
		fmt.Printf("%-40s  %-6s  %s\n", team.ID, team.Key, team.Name)
	}
	fmt.Println()
	fmt.Println("To configure:")
	fmt.Println("  bd config set linear.team_id \"<ID>\"")
}

// uuidRegex matches valid UUID format (with or without hyphens).
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)

func isValidUUID(s string) bool {
	return uuidRegex.MatchString(s)
}

// validateLinearConfig checks that required Linear configuration is present.
func validateLinearConfig() error {
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := rootCtx

	apiKey, _ := getLinearConfig(ctx, "linear.api_key")
	if apiKey == "" {
		return fmt.Errorf("Linear API key not configured\nRun: bd config set linear.api_key \"YOUR_API_KEY\"\nOr: export LINEAR_API_KEY=YOUR_API_KEY")
	}

	teamID, _ := getLinearConfig(ctx, "linear.team_id")
	if teamID == "" {
		return fmt.Errorf("linear.team_id not configured\nRun: bd config set linear.team_id \"TEAM_ID\"\nOr: export LINEAR_TEAM_ID=TEAM_ID")
	}

	if !isValidUUID(teamID) {
		return fmt.Errorf("linear.team_id appears invalid (expected UUID format like '12345678-1234-1234-1234-123456789abc')\nCurrent value: %s", teamID)
	}

	return nil
}

// maskAPIKey returns a masked version of an API key for display.
// Shows first 4 and last 4 characters, with dots in between.
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// getLinearConfig reads a Linear configuration value, handling both daemon mode
// (where store is nil) and direct mode. Returns the value and its source.
// Priority: project config > environment variable.
func getLinearConfig(ctx context.Context, key string) (value string, source string) {
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
	envKey := linearConfigToEnvVar(key)
	if envKey != "" {
		value = os.Getenv(envKey)
		if value != "" {
			return value, fmt.Sprintf("environment variable (%s)", envKey)
		}
	}

	return "", ""
}

// linearConfigToEnvVar maps Linear config keys to their environment variable names.
func linearConfigToEnvVar(key string) string {
	switch key {
	case "linear.api_key":
		return "LINEAR_API_KEY"
	case "linear.team_id":
		return "LINEAR_TEAM_ID"
	default:
		return ""
	}
}

// getLinearClient creates a configured Linear client from beads config.
func getLinearClient(ctx context.Context) (*linear.Client, error) {
	apiKey, _ := getLinearConfig(ctx, "linear.api_key")
	if apiKey == "" {
		return nil, fmt.Errorf("Linear API key not configured")
	}

	teamID, _ := getLinearConfig(ctx, "linear.team_id")
	if teamID == "" {
		return nil, fmt.Errorf("Linear team ID not configured")
	}

	client := linear.NewClient(apiKey, teamID)

	if store != nil {
		if endpoint, _ := store.GetConfig(ctx, "linear.api_endpoint"); endpoint != "" {
			client = client.WithEndpoint(endpoint)
		}
	}

	return client, nil
}

// storeConfigLoader adapts the store to the linear.ConfigLoader interface.
type storeConfigLoader struct {
	ctx context.Context
}

func (l *storeConfigLoader) GetAllConfig() (map[string]string, error) {
	return store.GetAllConfig(l.ctx)
}

// loadLinearMappingConfig loads mapping configuration from beads config.
func loadLinearMappingConfig(ctx context.Context) *linear.MappingConfig {
	if store == nil {
		return linear.DefaultMappingConfig()
	}
	return linear.LoadMappingConfig(&storeConfigLoader{ctx: ctx})
}

// getLinearIDMode returns the configured ID mode for Linear imports.
// Supported values: "hash" (default) or "db".
func getLinearIDMode(ctx context.Context) string {
	mode, _ := getLinearConfig(ctx, "linear.id_mode")
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "hash"
	}
	return mode
}

// getLinearHashLength returns the configured hash length for Linear imports.
// Values are clamped to the supported range 3-8.
func getLinearHashLength(ctx context.Context) int {
	raw, _ := getLinearConfig(ctx, "linear.hash_length")
	if raw == "" {
		return 6
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 6
	}
	if value < 3 {
		return 3
	}
	if value > 8 {
		return 8
	}
	return value
}

// detectLinearConflicts finds issues that have been modified both locally and in Linear
// since the last sync. This is a more expensive operation as it fetches individual
// issue timestamps from Linear.
func detectLinearConflicts(ctx context.Context) ([]linear.Conflict, error) {
	lastSyncStr, _ := store.GetConfig(ctx, "linear.last_sync")
	if lastSyncStr == "" {
		return nil, nil
	}

	lastSync, err := time.Parse(time.RFC3339, lastSyncStr)
	if err != nil {
		return nil, fmt.Errorf("invalid last_sync timestamp: %w", err)
	}

	config := loadLinearMappingConfig(ctx)

	client, err := getLinearClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create Linear client: %w", err)
	}

	// Get all local issues with Linear external refs
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, err
	}

	var conflicts []linear.Conflict

	for _, issue := range allIssues {
		if issue.ExternalRef == nil || !linear.IsLinearExternalRef(*issue.ExternalRef) {
			continue
		}

		if !issue.UpdatedAt.After(lastSync) {
			continue
		}

		linearIdentifier := linear.ExtractLinearIdentifier(*issue.ExternalRef)
		if linearIdentifier == "" {
			continue
		}

		linearIssue, err := client.FetchIssueByIdentifier(ctx, linearIdentifier)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to fetch Linear issue %s for conflict check: %v\n",
				linearIdentifier, err)
			continue
		}
		if linearIssue == nil {
			continue
		}

		linearUpdatedAt, err := time.Parse(time.RFC3339, linearIssue.UpdatedAt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse Linear UpdatedAt for %s: %v\n",
				linearIdentifier, err)
			continue
		}

		if !linearUpdatedAt.After(lastSync) {
			continue
		}

		localComparable := linear.NormalizeIssueForLinearHash(issue)
		linearComparable := linear.IssueToBeads(linearIssue, config).Issue.(*types.Issue)
		if localComparable.ComputeContentHash() == linearComparable.ComputeContentHash() {
			continue
		}

		conflicts = append(conflicts, linear.Conflict{
			IssueID:           issue.ID,
			LocalUpdated:      issue.UpdatedAt,
			LinearUpdated:     linearUpdatedAt,
			LinearExternalRef: *issue.ExternalRef,
			LinearIdentifier:  linearIdentifier,
			LinearInternalID:  linearIssue.ID,
		})
	}

	return conflicts, nil
}

// reimportLinearConflicts re-imports conflicting issues from Linear (Linear wins).
// For each conflict, fetches the current state from Linear and updates the local copy.
func reimportLinearConflicts(ctx context.Context, conflicts []linear.Conflict) error {
	if len(conflicts) == 0 {
		return nil
	}

	client, err := getLinearClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create Linear client: %w", err)
	}

	config := loadLinearMappingConfig(ctx)
	resolved := 0
	failed := 0

	for _, conflict := range conflicts {
		linearIssue, err := client.FetchIssueByIdentifier(ctx, conflict.LinearIdentifier)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to fetch %s for resolution: %v\n",
				conflict.LinearIdentifier, err)
			failed++
			continue
		}
		if linearIssue == nil {
			fmt.Fprintf(os.Stderr, "  Warning: Linear issue %s not found, skipping\n",
				conflict.LinearIdentifier)
			failed++
			continue
		}

		updates := linear.BuildLinearToLocalUpdates(linearIssue, config)

		err = store.UpdateIssue(ctx, conflict.IssueID, updates, actor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to update local issue %s: %v\n",
				conflict.IssueID, err)
			failed++
			continue
		}

		fmt.Printf("  Resolved: %s <- %s (Linear wins)\n", conflict.IssueID, conflict.LinearIdentifier)
		resolved++
	}

	if failed > 0 {
		return fmt.Errorf("%d conflict(s) failed to resolve", failed)
	}

	fmt.Printf("  Resolved %d conflict(s) by keeping Linear version\n", resolved)
	return nil
}

// resolveLinearConflictsByTimestamp resolves conflicts by keeping the newer version.
// For each conflict, compares local and Linear UpdatedAt timestamps.
// If Linear is newer, re-imports from Linear. If local is newer, push will overwrite.
func resolveLinearConflictsByTimestamp(ctx context.Context, conflicts []linear.Conflict) error {
	if len(conflicts) == 0 {
		return nil
	}

	var linearWins []linear.Conflict
	var localWins []linear.Conflict

	for _, conflict := range conflicts {
		if conflict.LinearUpdated.After(conflict.LocalUpdated) {
			linearWins = append(linearWins, conflict)
		} else {
			localWins = append(localWins, conflict)
		}
	}

	if len(linearWins) > 0 {
		fmt.Printf("  %d conflict(s): Linear is newer, will re-import\n", len(linearWins))
	}
	if len(localWins) > 0 {
		fmt.Printf("  %d conflict(s): Local is newer, will push to Linear\n", len(localWins))
	}

	if len(linearWins) > 0 {
		err := reimportLinearConflicts(ctx, linearWins)
		if err != nil {
			return fmt.Errorf("failed to re-import Linear-wins conflicts: %w", err)
		}
	}

	if len(localWins) > 0 {
		for _, conflict := range localWins {
			fmt.Printf("  Resolved: %s -> %s (local wins, will push)\n",
				conflict.IssueID, conflict.LinearIdentifier)
		}
	}

	return nil
}

// doPullFromLinear imports issues from Linear using the GraphQL API.
// Supports incremental sync by checking linear.last_sync config and only fetching
// issues updated since that timestamp.
func doPullFromLinear(ctx context.Context, dryRun bool, state string, skipLinearIDs map[string]bool) (*linear.PullStats, error) {
	stats := &linear.PullStats{}

	client, err := getLinearClient(ctx)
	if err != nil {
		return stats, fmt.Errorf("failed to create Linear client: %w", err)
	}

	var linearIssues []linear.Issue
	lastSyncStr, _ := store.GetConfig(ctx, "linear.last_sync")

	if lastSyncStr != "" {
		lastSync, err := time.Parse(time.RFC3339, lastSyncStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: invalid linear.last_sync timestamp, doing full sync\n")
			linearIssues, err = client.FetchIssues(ctx, state)
			if err != nil {
				return stats, fmt.Errorf("failed to fetch issues from Linear: %w", err)
			}
		} else {
			stats.Incremental = true
			stats.SyncedSince = lastSyncStr
			linearIssues, err = client.FetchIssuesSince(ctx, state, lastSync)
			if err != nil {
				return stats, fmt.Errorf("failed to fetch issues from Linear (incremental): %w", err)
			}
			if !dryRun {
				fmt.Printf("  Incremental sync since %s\n", lastSync.Format("2006-01-02 15:04:05"))
			}
		}
	} else {
		linearIssues, err = client.FetchIssues(ctx, state)
		if err != nil {
			return stats, fmt.Errorf("failed to fetch issues from Linear: %w", err)
		}
		if !dryRun {
			fmt.Println("  Full sync (no previous sync timestamp)")
		}
	}

	mappingConfig := loadLinearMappingConfig(ctx)

	idMode := getLinearIDMode(ctx)
	hashLength := getLinearHashLength(ctx)

	var beadsIssues []*types.Issue
	var allDeps []linear.DependencyInfo
	linearIDToBeadsID := make(map[string]string)

	for i := range linearIssues {
		conversion := linear.IssueToBeads(&linearIssues[i], mappingConfig)
		beadsIssues = append(beadsIssues, conversion.Issue.(*types.Issue))
		allDeps = append(allDeps, conversion.Dependencies...)
	}

	if len(beadsIssues) == 0 {
		fmt.Println("  No issues to import")
		return stats, nil
	}

	if len(skipLinearIDs) > 0 {
		var filteredIssues []*types.Issue
		skipped := 0
		for _, issue := range beadsIssues {
			if issue.ExternalRef == nil {
				filteredIssues = append(filteredIssues, issue)
				continue
			}
			linearID := linear.ExtractLinearIdentifier(*issue.ExternalRef)
			if linearID != "" && skipLinearIDs[linearID] {
				skipped++
				continue
			}
			filteredIssues = append(filteredIssues, issue)
		}
		if skipped > 0 {
			stats.Skipped += skipped
		}
		beadsIssues = filteredIssues

		if len(allDeps) > 0 {
			var filteredDeps []linear.DependencyInfo
			for _, dep := range allDeps {
				if skipLinearIDs[dep.FromLinearID] || skipLinearIDs[dep.ToLinearID] {
					continue
				}
				filteredDeps = append(filteredDeps, dep)
			}
			allDeps = filteredDeps
		}
	}

	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix == "" {
		prefix = "bd"
	}

	if idMode == "hash" {
		existingIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
		if err != nil {
			return stats, fmt.Errorf("failed to fetch existing issues for ID collision avoidance: %w", err)
		}
		usedIDs := make(map[string]bool, len(existingIssues))
		for _, issue := range existingIssues {
			if issue.ID != "" {
				usedIDs[issue.ID] = true
			}
		}

		idOpts := linear.IDGenerationOptions{
			BaseLength: hashLength,
			MaxLength:  8,
			UsedIDs:    usedIDs,
		}
		if err := linear.GenerateIssueIDs(beadsIssues, prefix, "linear-import", idOpts); err != nil {
			return stats, fmt.Errorf("failed to generate issue IDs: %w", err)
		}
	} else if idMode != "db" {
		return stats, fmt.Errorf("unsupported linear.id_mode %q (expected \"hash\" or \"db\")", idMode)
	}

	opts := ImportOptions{
		DryRun:     dryRun,
		SkipUpdate: false,
	}

	result, err := importIssuesCore(ctx, dbPath, store, beadsIssues, opts)
	if err != nil {
		return stats, fmt.Errorf("import failed: %w", err)
	}

	stats.Created = result.Created
	stats.Updated = result.Updated
	stats.Skipped = result.Skipped

	if dryRun {
		if stats.Incremental {
			fmt.Printf("  Would import %d issues from Linear (incremental since %s)\n",
				len(linearIssues), stats.SyncedSince)
		} else {
			fmt.Printf("  Would import %d issues from Linear (full sync)\n", len(linearIssues))
		}
		return stats, nil
	}

	allBeadsIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch issues for dependency mapping: %v\n", err)
		return stats, nil
	}

	for _, issue := range allBeadsIssues {
		if issue.ExternalRef != nil && linear.IsLinearExternalRef(*issue.ExternalRef) {
			linearID := linear.ExtractLinearIdentifier(*issue.ExternalRef)
			if linearID != "" {
				linearIDToBeadsID[linearID] = issue.ID
			}
		}
	}

	depsCreated := 0
	for _, dep := range allDeps {
		fromID, fromOK := linearIDToBeadsID[dep.FromLinearID]
		toID, toOK := linearIDToBeadsID[dep.ToLinearID]

		if !fromOK || !toOK {
			continue
		}

		dependency := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        types.DependencyType(dep.Type),
			CreatedAt:   time.Now(),
		}
		err := store.AddDependency(ctx, dependency, actor)
		if err != nil {
			if !strings.Contains(err.Error(), "already exists") &&
				!strings.Contains(err.Error(), "duplicate") {
				fmt.Fprintf(os.Stderr, "Warning: failed to create dependency %s -> %s (%s): %v\n",
					fromID, toID, dep.Type, err)
			}
		} else {
			depsCreated++
		}
	}

	if depsCreated > 0 {
		fmt.Printf("  Created %d dependencies from Linear relations\n", depsCreated)
	}

	return stats, nil
}

// doPushToLinear exports issues to Linear using the GraphQL API.
func doPushToLinear(ctx context.Context, dryRun bool, createOnly bool, updateRefs bool, forceUpdateIDs map[string]bool, skipUpdateIDs map[string]bool) (*linear.PushStats, error) {
	stats := &linear.PushStats{}

	client, err := getLinearClient(ctx)
	if err != nil {
		return stats, fmt.Errorf("failed to create Linear client: %w", err)
	}

	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return stats, fmt.Errorf("failed to get local issues: %w", err)
	}

	var toCreate []*types.Issue
	var toUpdate []*types.Issue

	for _, issue := range allIssues {
		if issue.IsTombstone() {
			continue
		}

		if issue.ExternalRef != nil && linear.IsLinearExternalRef(*issue.ExternalRef) {
			if !createOnly {
				toUpdate = append(toUpdate, issue)
			}
		} else if issue.ExternalRef == nil {
			toCreate = append(toCreate, issue)
		}
	}

	var stateCache *linear.StateCache
	if !dryRun && (len(toCreate) > 0 || (!createOnly && len(toUpdate) > 0)) {
		stateCache, err = linear.BuildStateCache(ctx, client)
		if err != nil {
			return stats, fmt.Errorf("failed to fetch team states: %w", err)
		}
	}

	mappingConfig := loadLinearMappingConfig(ctx)

	for _, issue := range toCreate {
		if dryRun {
			stats.Created++
			continue
		}

		linearPriority := linear.PriorityToLinear(issue.Priority, mappingConfig)
		stateID := stateCache.FindStateForBeadsStatus(issue.Status)

		description := linear.BuildLinearDescription(issue)

		linearIssue, err := client.CreateIssue(ctx, issue.Title, description, linearPriority, stateID, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to create issue '%s' in Linear: %v\n", issue.Title, err)
			stats.Errors++
			continue
		}

		stats.Created++
		fmt.Printf("  Created: %s -> %s\n", issue.ID, linearIssue.Identifier)

		if updateRefs && linearIssue.URL != "" {
			externalRef := linearIssue.URL
			if canonical, ok := linear.CanonicalizeLinearExternalRef(externalRef); ok {
				externalRef = canonical
			}
			updates := map[string]interface{}{
				"external_ref": externalRef,
			}
			if err := store.UpdateIssue(ctx, issue.ID, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to update external_ref for %s: %v\n", issue.ID, err)
				stats.Errors++
			}
		}
	}

	if len(toUpdate) > 0 && !createOnly {
		for _, issue := range toUpdate {
			if skipUpdateIDs != nil && skipUpdateIDs[issue.ID] {
				stats.Skipped++
				continue
			}

			linearIdentifier := linear.ExtractLinearIdentifier(*issue.ExternalRef)
			if linearIdentifier == "" {
				fmt.Fprintf(os.Stderr, "Warning: could not extract Linear identifier from %s: %s\n",
					issue.ID, *issue.ExternalRef)
				stats.Errors++
				continue
			}

			linearIssue, err := client.FetchIssueByIdentifier(ctx, linearIdentifier)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to fetch Linear issue %s: %v\n",
					linearIdentifier, err)
				stats.Errors++
				continue
			}
			if linearIssue == nil {
				fmt.Fprintf(os.Stderr, "Warning: Linear issue %s not found (may have been deleted)\n",
					linearIdentifier)
				stats.Skipped++
				continue
			}

			linearUpdatedAt, err := time.Parse(time.RFC3339, linearIssue.UpdatedAt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to parse Linear UpdatedAt for %s: %v\n",
					linearIdentifier, err)
				stats.Errors++
				continue
			}

			forcedUpdate := forceUpdateIDs != nil && forceUpdateIDs[issue.ID]
			if !forcedUpdate && !issue.UpdatedAt.After(linearUpdatedAt) {
				stats.Skipped++
				continue
			}

			if !forcedUpdate {
				localComparable := linear.NormalizeIssueForLinearHash(issue)
				linearComparable := linear.IssueToBeads(linearIssue, mappingConfig).Issue.(*types.Issue)
				if localComparable.ComputeContentHash() == linearComparable.ComputeContentHash() {
					stats.Skipped++
					continue
				}
			}

			if dryRun {
				stats.Updated++
				continue
			}

			description := linear.BuildLinearDescription(issue)

			updatePayload := map[string]interface{}{
				"title":       issue.Title,
				"description": description,
			}

			linearPriority := linear.PriorityToLinear(issue.Priority, mappingConfig)
			if linearPriority > 0 {
				updatePayload["priority"] = linearPriority
			}

			stateID := stateCache.FindStateForBeadsStatus(issue.Status)
			if stateID != "" {
				updatePayload["stateId"] = stateID
			}

			updatedLinearIssue, err := client.UpdateIssue(ctx, linearIssue.ID, updatePayload)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to update Linear issue %s: %v\n",
					linearIdentifier, err)
				stats.Errors++
				continue
			}

			stats.Updated++
			fmt.Printf("  Updated: %s -> %s\n", issue.ID, updatedLinearIssue.Identifier)
		}
	}

	if dryRun {
		fmt.Printf("  Would create %d issues in Linear\n", stats.Created)
		if !createOnly {
			fmt.Printf("  Would update %d issues in Linear\n", stats.Updated)
		}
	}

	return stats, nil
}
