package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/types"
)

// JiraSyncStats tracks statistics for a Jira sync operation.
type JiraSyncStats struct {
	Pulled    int `json:"pulled"`
	Pushed    int `json:"pushed"`
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Skipped   int `json:"skipped"`
	Errors    int `json:"errors"`
	Conflicts int `json:"conflicts"`
}

// JiraSyncResult represents the result of a Jira sync operation.
type JiraSyncResult struct {
	Success  bool          `json:"success"`
	Stats    JiraSyncStats `json:"stats"`
	LastSync string        `json:"last_sync,omitempty"`
	Error    string        `json:"error,omitempty"`
	Warnings []string      `json:"warnings,omitempty"`
}

var jiraCmd = &cobra.Command{
	Use:     "jira",
	GroupID: "advanced",
	Short:   "Jira integration commands",
	Long: `Synchronize issues between beads and Jira.

Configuration:
  bd config set jira.url "https://company.atlassian.net"
  bd config set jira.project "PROJ"
  bd config set jira.api_token "YOUR_TOKEN"
  bd config set jira.username "your_email@company.com"  # For Jira Cloud

Environment variables (alternative to config):
  JIRA_API_TOKEN - Jira API token
  JIRA_USERNAME  - Jira username/email

Examples:
  bd jira sync --pull         # Import issues from Jira
  bd jira sync --push         # Export issues to Jira
  bd jira sync                # Bidirectional sync (pull then push)
  bd jira sync --dry-run      # Preview sync without changes
  bd jira status              # Show sync status`,
}

var jiraSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Synchronize issues with Jira",
	Long: `Synchronize issues between beads and Jira.

Modes:
  --pull         Import issues from Jira into beads
  --push         Export issues from beads to Jira
  (no flags)     Bidirectional sync: pull then push, with conflict resolution

Conflict Resolution:
  By default, newer timestamp wins. Override with:
  --prefer-local   Always prefer local beads version
  --prefer-jira    Always prefer Jira version

Examples:
  bd jira sync --pull                # Import from Jira
  bd jira sync --push --create-only  # Push new issues only
  bd jira sync --dry-run             # Preview without changes
  bd jira sync --prefer-local        # Bidirectional, local wins`,
	Run: func(cmd *cobra.Command, args []string) {
		// Flag errors are unlikely but check one to ensure cobra is working
		pull, _ := cmd.Flags().GetBool("pull")
		push, _ := cmd.Flags().GetBool("push")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		preferLocal, _ := cmd.Flags().GetBool("prefer-local")
		preferJira, _ := cmd.Flags().GetBool("prefer-jira")
		createOnly, _ := cmd.Flags().GetBool("create-only")
		updateRefs, _ := cmd.Flags().GetBool("update-refs")
		state, _ := cmd.Flags().GetString("state")

		// Block writes in readonly mode (sync modifies data)
		if !dryRun {
			CheckReadonly("jira sync")
		}

		// Validate conflicting flags
		if preferLocal && preferJira {
			fmt.Fprintf(os.Stderr, "Error: cannot use both --prefer-local and --prefer-jira\n")
			os.Exit(1)
		}

		// Ensure store is available
		if err := ensureStoreActive(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: database not available: %v\n", err)
			os.Exit(1)
		}

		// Ensure we have Jira configuration
		if err := validateJiraConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Default mode: bidirectional (pull then push)
		if !pull && !push {
			pull = true
			push = true
		}

		ctx := rootCtx
		result := &JiraSyncResult{Success: true}

		// Step 1: Pull from Jira
		if pull {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would pull issues from Jira")
			} else {
				fmt.Println("→ Pulling issues from Jira...")
			}

			pullStats, err := doPullFromJira(ctx, dryRun, state)
			if err != nil {
				result.Success = false
				result.Error = err.Error()
				if jsonOutput {
					outputJSON(result)
				} else {
					fmt.Fprintf(os.Stderr, "Error pulling from Jira: %v\n", err)
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

		// Step 2: Handle conflicts (if bidirectional)
		if pull && push && !dryRun {
			conflicts, err := detectJiraConflicts(ctx)
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("conflict detection failed: %v", err))
			} else if len(conflicts) > 0 {
				result.Stats.Conflicts = len(conflicts)
				if preferLocal {
					fmt.Printf("→ Resolving %d conflicts (preferring local)\n", len(conflicts))
					// Local wins - no action needed, push will overwrite
				} else if preferJira {
					fmt.Printf("→ Resolving %d conflicts (preferring Jira)\n", len(conflicts))
					// Jira wins - re-import conflicting issues
					if err := reimportConflicts(ctx, conflicts); err != nil {
						result.Warnings = append(result.Warnings, fmt.Sprintf("conflict resolution failed: %v", err))
					}
				} else {
					// Default: timestamp-based (newer wins)
					fmt.Printf("→ Resolving %d conflicts (newer wins)\n", len(conflicts))
					if err := resolveConflictsByTimestamp(ctx, conflicts); err != nil {
						result.Warnings = append(result.Warnings, fmt.Sprintf("conflict resolution failed: %v", err))
					}
				}
			}
		}

		// Step 3: Push to Jira
		if push {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would push issues to Jira")
			} else {
				fmt.Println("→ Pushing issues to Jira...")
			}

			pushStats, err := doPushToJira(ctx, dryRun, createOnly, updateRefs)
			if err != nil {
				result.Success = false
				result.Error = err.Error()
				if jsonOutput {
					outputJSON(result)
				} else {
					fmt.Fprintf(os.Stderr, "Error pushing to Jira: %v\n", err)
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

		// Update last sync timestamp
		if !dryRun && result.Success {
			result.LastSync = time.Now().Format(time.RFC3339)
			if err := store.SetConfig(ctx, "jira.last_sync", result.LastSync); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("failed to update last_sync: %v", err))
			}
		}

		// Output result
		if jsonOutput {
			outputJSON(result)
		} else if dryRun {
			fmt.Println("\n✓ Dry run complete (no changes made)")
		} else {
			fmt.Println("\n✓ Jira sync complete")
			if len(result.Warnings) > 0 {
				fmt.Println("\nWarnings:")
				for _, w := range result.Warnings {
					fmt.Printf("  - %s\n", w)
				}
			}
		}
	},
}

var jiraStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Jira sync status",
	Long: `Show the current Jira sync status, including:
  - Last sync timestamp
  - Configuration status
  - Number of issues with Jira links
  - Issues pending push (no external_ref)`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		// Ensure store is available
		if err := ensureStoreActive(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Get configuration
		jiraURL, _ := store.GetConfig(ctx, "jira.url")
		jiraProject, _ := store.GetConfig(ctx, "jira.project")
		lastSync, _ := store.GetConfig(ctx, "jira.last_sync")

		// Check if configured
		configured := jiraURL != "" && jiraProject != ""

		// Count issues with Jira links
		allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		withJiraRef := 0
		pendingPush := 0
		for _, issue := range allIssues {
			if issue.ExternalRef != nil && isJiraExternalRef(*issue.ExternalRef, jiraURL) {
				withJiraRef++
			} else if issue.ExternalRef == nil {
				// Only count issues without any external_ref as pending push
				pendingPush++
			}
			// Issues with non-Jira external_ref are not counted in either category
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"configured":    configured,
				"jira_url":      jiraURL,
				"jira_project":  jiraProject,
				"last_sync":     lastSync,
				"total_issues":  len(allIssues),
				"with_jira_ref": withJiraRef,
				"pending_push":  pendingPush,
			})
			return
		}

		fmt.Println("Jira Sync Status")
		fmt.Println("================")
		fmt.Println()

		if !configured {
			fmt.Println("Status: Not configured")
			fmt.Println()
			fmt.Println("To configure Jira integration:")
			fmt.Println("  bd config set jira.url \"https://company.atlassian.net\"")
			fmt.Println("  bd config set jira.project \"PROJ\"")
			fmt.Println("  bd config set jira.api_token \"YOUR_TOKEN\"")
			fmt.Println("  bd config set jira.username \"your@email.com\"")
			return
		}

		fmt.Printf("Jira URL:     %s\n", jiraURL)
		fmt.Printf("Project:      %s\n", jiraProject)
		if lastSync != "" {
			fmt.Printf("Last Sync:    %s\n", lastSync)
		} else {
			fmt.Println("Last Sync:    Never")
		}
		fmt.Println()
		fmt.Printf("Total Issues: %d\n", len(allIssues))
		fmt.Printf("With Jira:    %d\n", withJiraRef)
		fmt.Printf("Local Only:   %d\n", pendingPush)

		if pendingPush > 0 {
			fmt.Println()
			fmt.Printf("Run 'bd jira sync --push' to push %d local issue(s) to Jira\n", pendingPush)
		}
	},
}

func init() {
	// Sync command flags
	jiraSyncCmd.Flags().Bool("pull", false, "Pull issues from Jira")
	jiraSyncCmd.Flags().Bool("push", false, "Push issues to Jira")
	jiraSyncCmd.Flags().Bool("dry-run", false, "Preview sync without making changes")
	jiraSyncCmd.Flags().Bool("prefer-local", false, "Prefer local version on conflicts")
	jiraSyncCmd.Flags().Bool("prefer-jira", false, "Prefer Jira version on conflicts")
	jiraSyncCmd.Flags().Bool("create-only", false, "Only create new issues, don't update existing")
	jiraSyncCmd.Flags().Bool("update-refs", true, "Update external_ref after creating Jira issues")
	jiraSyncCmd.Flags().String("state", "all", "Issue state to sync: open, closed, all")

	jiraCmd.AddCommand(jiraSyncCmd)
	jiraCmd.AddCommand(jiraStatusCmd)
	rootCmd.AddCommand(jiraCmd)
}

// validateJiraConfig checks that required Jira configuration is present.
func validateJiraConfig() error {
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := rootCtx
	jiraURL, _ := store.GetConfig(ctx, "jira.url")
	jiraProject, _ := store.GetConfig(ctx, "jira.project")

	if jiraURL == "" {
		return fmt.Errorf("jira.url not configured\nRun: bd config set jira.url \"https://company.atlassian.net\"")
	}
	if jiraProject == "" {
		return fmt.Errorf("jira.project not configured\nRun: bd config set jira.project \"PROJ\"")
	}

	// Check for API token (from config or env)
	apiToken, _ := store.GetConfig(ctx, "jira.api_token")
	if apiToken == "" && os.Getenv("JIRA_API_TOKEN") == "" {
		return fmt.Errorf("Jira API token not configured\nRun: bd config set jira.api_token \"YOUR_TOKEN\"\nOr: export JIRA_API_TOKEN=YOUR_TOKEN")
	}

	return nil
}

// PullStats tracks pull operation statistics.
type PullStats struct {
	Created int
	Updated int
	Skipped int
}

// doPullFromJira imports issues from Jira using the Python script.
func doPullFromJira(ctx context.Context, dryRun bool, state string) (*PullStats, error) {
	stats := &PullStats{}

	// Find the Python script
	scriptPath, err := findJiraScript("jira2jsonl.py")
	if err != nil {
		return stats, fmt.Errorf("jira2jsonl.py not found: %w", err)
	}

	// Build command
	args := []string{scriptPath, "--from-config"}
	if state != "" && state != "all" {
		args = append(args, "--state", state)
	}

	// Run Python script to get JSONL output
	cmd := exec.CommandContext(ctx, "python3", args...)
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return stats, fmt.Errorf("failed to fetch from Jira: %w", err)
	}

	if dryRun {
		// Count issues in output
		scanner := bufio.NewScanner(strings.NewReader(string(output)))
		count := 0
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) != "" {
				count++
			}
		}
		fmt.Printf("  Would import %d issues from Jira\n", count)
		return stats, nil
	}

	// Parse JSONL and import
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	var issues []*types.Issue

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return stats, fmt.Errorf("failed to parse issue JSON: %w", err)
		}
		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return stats, fmt.Errorf("failed to read JSONL: %w", err)
	}

	// Import issues using shared logic
	opts := ImportOptions{
		DryRun:     false,
		SkipUpdate: false,
	}

	result, err := importIssuesCore(ctx, dbPath, store, issues, opts)
	if err != nil {
		return stats, fmt.Errorf("import failed: %w", err)
	}

	stats.Created = result.Created
	stats.Updated = result.Updated
	stats.Skipped = result.Skipped

	return stats, nil
}

// PushStats tracks push operation statistics.
type PushStats struct {
	Created int
	Updated int
	Skipped int
	Errors  int
}

// doPushToJira exports issues to Jira using the Python script.
func doPushToJira(ctx context.Context, dryRun bool, createOnly bool, updateRefs bool) (*PushStats, error) {
	stats := &PushStats{}

	// Find the Python script
	scriptPath, err := findJiraScript("jsonl2jira.py")
	if err != nil {
		return stats, fmt.Errorf("jsonl2jira.py not found: %w", err)
	}

	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return stats, fmt.Errorf("failed to get issues: %w", err)
	}

	// Sort by ID for consistent output
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		return cmp.Compare(a.ID, b.ID)
	})

	// Generate JSONL for export
	var jsonlLines []string
	for _, issue := range issues {
		data, err := json.Marshal(issue)
		if err != nil {
			return stats, fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
		jsonlLines = append(jsonlLines, string(data))
	}

	jsonlContent := strings.Join(jsonlLines, "\n")

	// Build command
	args := []string{scriptPath, "--from-config"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if createOnly {
		args = append(args, "--create-only")
	}
	if updateRefs {
		args = append(args, "--update-refs")
	}

	cmd := exec.CommandContext(ctx, "python3", args...)
	cmd.Stdin = strings.NewReader(jsonlContent)
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return stats, fmt.Errorf("failed to push to Jira: %w", err)
	}

	// Parse output for statistics and external_ref updates
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Parse mapping output: {"bd_id": "...", "jira_key": "...", "external_ref": "..."}
		var mapping struct {
			BDID        string `json:"bd_id"`
			JiraKey     string `json:"jira_key"`
			ExternalRef string `json:"external_ref"`
		}
		if err := json.Unmarshal([]byte(line), &mapping); err == nil && mapping.BDID != "" {
			stats.Created++

			// Update external_ref if requested
			if updateRefs && !dryRun && mapping.ExternalRef != "" {
				updates := map[string]interface{}{
					"external_ref": mapping.ExternalRef,
				}
				if err := store.UpdateIssue(ctx, mapping.BDID, updates, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update external_ref for %s: %v\n", mapping.BDID, err)
					stats.Errors++
				}
			}
		}
	}

	return stats, nil
}

// findJiraScript locates the Jira Python script.
func findJiraScript(name string) (string, error) {
	// Check environment variable first (allows users to specify script location)
	if envPath := os.Getenv("BD_JIRA_SCRIPT"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath, nil
		}
		return "", fmt.Errorf("BD_JIRA_SCRIPT points to non-existent file: %s", envPath)
	}

	// Check common locations
	locations := []string{
		// Relative to current working directory
		filepath.Join("examples", "jira-import", name),
	}

	// Add executable-relative path
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		locations = append(locations, filepath.Join(exeDir, "examples", "jira-import", name))
		locations = append(locations, filepath.Join(exeDir, "..", "examples", "jira-import", name))
	}

	// Check BEADS_DIR or current .beads location
	if beadsDir := beads.FindBeadsDir(); beadsDir != "" {
		repoRoot := filepath.Dir(beadsDir)
		locations = append(locations, filepath.Join(repoRoot, "examples", "jira-import", name))
	}

	for _, loc := range locations {
		if _, err := os.Stat(loc); err == nil {
			absPath, err := filepath.Abs(loc)
			if err == nil {
				return absPath, nil
			}
			return loc, nil
		}
	}

	return "", fmt.Errorf(`script not found: %s

The Jira sync feature requires the Python script from the beads repository.

To fix this, either:
  1. Set BD_JIRA_SCRIPT to point to the script:
     export BD_JIRA_SCRIPT=/path/to/jira2jsonl.py

  2. Or download it from GitHub:
     curl -o jira2jsonl.py https://raw.githubusercontent.com/steveyegge/beads/main/examples/jira-import/jira2jsonl.py
     export BD_JIRA_SCRIPT=$PWD/jira2jsonl.py

Looked in: %v`, name, locations)
}

// JiraConflict represents a conflict between local and Jira versions.
type JiraConflict struct {
	IssueID         string
	LocalUpdated    time.Time
	JiraUpdated     time.Time
	JiraExternalRef string
}

// detectJiraConflicts finds issues that have been modified both locally and in Jira.
// It fetches each potentially conflicting issue from Jira to compare timestamps,
// only reporting a conflict if both sides have been modified since the last sync.
func detectJiraConflicts(ctx context.Context) ([]JiraConflict, error) {
	// Get last sync time
	lastSyncStr, _ := store.GetConfig(ctx, "jira.last_sync")
	if lastSyncStr == "" {
		// No previous sync - no conflicts possible
		return nil, nil
	}

	lastSync, err := time.Parse(time.RFC3339, lastSyncStr)
	if err != nil {
		return nil, fmt.Errorf("invalid last_sync timestamp: %w", err)
	}

	// Get all issues with Jira refs that were updated since last sync
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, err
	}

	// Get jiraURL for validation
	jiraURL, _ := store.GetConfig(ctx, "jira.url")

	var conflicts []JiraConflict
	for _, issue := range allIssues {
		if issue.ExternalRef == nil || !isJiraExternalRef(*issue.ExternalRef, jiraURL) {
			continue
		}

		// Check if local issue was updated since last sync
		if !issue.UpdatedAt.After(lastSync) {
			continue
		}

		// Local was updated - now check if Jira was also updated
		jiraKey := extractJiraKey(*issue.ExternalRef)
		if jiraKey == "" {
			// Can't extract key - treat as potential conflict for safety
			conflicts = append(conflicts, JiraConflict{
				IssueID:         issue.ID,
				LocalUpdated:    issue.UpdatedAt,
				JiraExternalRef: *issue.ExternalRef,
			})
			continue
		}

		// Fetch Jira issue timestamp
		jiraUpdated, err := fetchJiraIssueTimestamp(ctx, jiraKey)
		if err != nil {
			// Can't fetch from Jira - log warning and treat as potential conflict
			fmt.Fprintf(os.Stderr, "Warning: couldn't fetch Jira issue %s: %v\n", jiraKey, err)
			conflicts = append(conflicts, JiraConflict{
				IssueID:         issue.ID,
				LocalUpdated:    issue.UpdatedAt,
				JiraExternalRef: *issue.ExternalRef,
			})
			continue
		}

		// Only a conflict if Jira was ALSO updated since last sync
		if jiraUpdated.After(lastSync) {
			conflicts = append(conflicts, JiraConflict{
				IssueID:         issue.ID,
				LocalUpdated:    issue.UpdatedAt,
				JiraUpdated:     jiraUpdated,
				JiraExternalRef: *issue.ExternalRef,
			})
		}
	}

	return conflicts, nil
}

// reimportConflicts re-imports conflicting issues from Jira (Jira wins).
// NOTE: Full implementation would fetch the complete Jira issue and update local copy.
// Currently shows detailed conflict info for manual review.
func reimportConflicts(_ context.Context, conflicts []JiraConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	fmt.Fprintf(os.Stderr, "Warning: conflict resolution (--prefer-jira) not fully implemented\n")
	fmt.Fprintf(os.Stderr, "  %d issue(s) have conflicts - Jira version would win:\n", len(conflicts))
	for _, c := range conflicts {
		if !c.JiraUpdated.IsZero() {
			fmt.Fprintf(os.Stderr, "    - %s (local: %s, jira: %s)\n",
				c.IssueID,
				c.LocalUpdated.Format(time.RFC3339),
				c.JiraUpdated.Format(time.RFC3339))
		} else {
			fmt.Fprintf(os.Stderr, "    - %s (local: %s, jira: unknown)\n",
				c.IssueID,
				c.LocalUpdated.Format(time.RFC3339))
		}
	}
	return nil
}

// resolveConflictsByTimestamp resolves conflicts by keeping the newer version.
// Uses the actual Jira timestamps fetched during conflict detection to determine
// which version (local or Jira) should be preserved.
func resolveConflictsByTimestamp(_ context.Context, conflicts []JiraConflict) error {
	if len(conflicts) == 0 {
		return nil
	}

	var localWins, jiraWins, unknown int
	for _, c := range conflicts {
		if c.JiraUpdated.IsZero() {
			unknown++
		} else if c.LocalUpdated.After(c.JiraUpdated) {
			localWins++
		} else {
			jiraWins++
		}
	}

	fmt.Fprintf(os.Stderr, "Conflict resolution by timestamp:\n")
	fmt.Fprintf(os.Stderr, "  Local wins (newer): %d\n", localWins)
	fmt.Fprintf(os.Stderr, "  Jira wins (newer):  %d\n", jiraWins)
	if unknown > 0 {
		fmt.Fprintf(os.Stderr, "  Unknown (couldn't fetch): %d\n", unknown)
	}

	// Show details
	for _, c := range conflicts {
		if c.JiraUpdated.IsZero() {
			fmt.Fprintf(os.Stderr, "    - %s: local version kept (couldn't fetch Jira timestamp)\n", c.IssueID)
		} else if c.LocalUpdated.After(c.JiraUpdated) {
			fmt.Fprintf(os.Stderr, "    - %s: local wins (local: %s > jira: %s)\n",
				c.IssueID,
				c.LocalUpdated.Format(time.RFC3339),
				c.JiraUpdated.Format(time.RFC3339))
		} else {
			fmt.Fprintf(os.Stderr, "    - %s: jira wins (jira: %s >= local: %s)\n",
				c.IssueID,
				c.JiraUpdated.Format(time.RFC3339),
				c.LocalUpdated.Format(time.RFC3339))
		}
	}

	// NOTE: Full implementation would actually re-import the Jira version for jiraWins issues
	if jiraWins > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d issue(s) should be re-imported from Jira (not yet implemented)\n", jiraWins)
	}

	return nil
}

// isJiraExternalRef checks if an external_ref URL matches the configured Jira instance.
// It validates both the URL structure (/browse/PROJECT-123) and optionally the host.
func isJiraExternalRef(externalRef, jiraURL string) bool {
	// Must contain /browse/ pattern
	if !strings.Contains(externalRef, "/browse/") {
		return false
	}

	// If jiraURL is provided, validate the host matches
	if jiraURL != "" {
		jiraURL = strings.TrimSuffix(jiraURL, "/")
		if !strings.HasPrefix(externalRef, jiraURL) {
			return false
		}
	}

	return true
}

// extractJiraKey extracts the Jira issue key from an external_ref URL.
// For example, "https://company.atlassian.net/browse/PROJ-123" returns "PROJ-123".
func extractJiraKey(externalRef string) string {
	idx := strings.LastIndex(externalRef, "/browse/")
	if idx == -1 {
		return ""
	}
	return externalRef[idx+len("/browse/"):]
}

// fetchJiraIssueTimestamp fetches the updated timestamp for a single Jira issue.
// It returns the Jira issue's updated timestamp, or an error if the fetch fails.
func fetchJiraIssueTimestamp(ctx context.Context, jiraKey string) (time.Time, error) {
	var zero time.Time

	// Get Jira configuration
	jiraURL, _ := store.GetConfig(ctx, "jira.url")
	if jiraURL == "" {
		return zero, fmt.Errorf("jira.url not configured")
	}
	jiraURL = strings.TrimSuffix(jiraURL, "/")

	// Get credentials (config takes precedence over env)
	apiToken, _ := store.GetConfig(ctx, "jira.api_token")
	if apiToken == "" {
		apiToken = os.Getenv("JIRA_API_TOKEN")
	}
	if apiToken == "" {
		return zero, fmt.Errorf("jira API token not configured")
	}

	username, _ := store.GetConfig(ctx, "jira.username")
	if username == "" {
		username = os.Getenv("JIRA_USERNAME")
	}

	// Build API URL - use v3 for Jira Cloud (v2 is deprecated)
	// Only fetch the 'updated' field to minimize response size
	apiURL := fmt.Sprintf("%s/rest/api/3/issue/%s?fields=updated", jiraURL, jiraKey)

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return zero, fmt.Errorf("failed to create request: %w", err)
	}

	// Set authentication header
	isCloud := strings.Contains(jiraURL, "atlassian.net")
	if isCloud && username != "" {
		// Jira Cloud: Basic auth with email:api_token
		auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + apiToken))
		req.Header.Set("Authorization", "Basic "+auth)
	} else if username != "" {
		// Jira Server with username: Basic auth
		auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + apiToken))
		req.Header.Set("Authorization", "Basic "+auth)
	} else {
		// Jira Server without username: Bearer token (PAT)
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bd-jira-sync/1.0")

	// Execute request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("failed to fetch issue %s: %w", jiraKey, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return zero, fmt.Errorf("jira API returned %d for issue %s: %s", resp.StatusCode, jiraKey, string(body))
	}

	// Parse response
	var result struct {
		Fields struct {
			Updated string `json:"updated"`
		} `json:"fields"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return zero, fmt.Errorf("failed to parse Jira response: %w", err)
	}

	// Parse Jira timestamp (ISO 8601 format: 2024-01-15T10:30:00.000+0000)
	updated, err := parseJiraTimestamp(result.Fields.Updated)
	if err != nil {
		return zero, fmt.Errorf("failed to parse Jira timestamp: %w", err)
	}

	return updated, nil
}

// parseJiraTimestamp parses Jira's timestamp format into a time.Time.
// Jira uses ISO 8601 with timezone: 2024-01-15T10:30:00.000+0000 or 2024-01-15T10:30:00.000Z
func parseJiraTimestamp(ts string) (time.Time, error) {
	if ts == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}

	// Try common formats
	formats := []string{
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
		time.RFC3339Nano,
	}

	for _, format := range formats {
		if t, err := time.Parse(format, ts); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unrecognized timestamp format: %s", ts)
}
