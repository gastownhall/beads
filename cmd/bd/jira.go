package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/jira"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

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
  bd config set jira.push_prefix "hippo"       # Only push hippo-* issues to Jira
  bd config set jira.push_prefix "proj1,proj2" # Multiple prefixes (comma-separated)

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
	Run: runJiraSync,
}

var jiraStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Jira sync status",
	Long: `Show the current Jira sync status, including:
  - Last sync timestamp
  - Configuration status
  - Number of issues with Jira links
  - Issues pending push (no external_ref)`,
	Run: runJiraStatus,
}

func init() {
	jiraSyncCmd.Flags().Bool("pull", false, "Pull issues from Jira")
	jiraSyncCmd.Flags().Bool("push", false, "Push issues to Jira")
	jiraSyncCmd.Flags().Bool("dry-run", false, "Preview sync without making changes")
	jiraSyncCmd.Flags().Bool("prefer-local", false, "Prefer local version on conflicts")
	jiraSyncCmd.Flags().Bool("prefer-jira", false, "Prefer Jira version on conflicts")
	jiraSyncCmd.Flags().Bool("create-only", false, "Only create new issues, don't update existing")
	jiraSyncCmd.Flags().String("state", "all", "Issue state to sync: open, closed, all")

	jiraCmd.AddCommand(jiraSyncCmd)
	jiraCmd.AddCommand(jiraStatusCmd)
	rootCmd.AddCommand(jiraCmd)
}

func runJiraSync(cmd *cobra.Command, args []string) {
	pull, _ := cmd.Flags().GetBool("pull")
	push, _ := cmd.Flags().GetBool("push")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	preferLocal, _ := cmd.Flags().GetBool("prefer-local")
	preferJira, _ := cmd.Flags().GetBool("prefer-jira")
	createOnly, _ := cmd.Flags().GetBool("create-only")
	state, _ := cmd.Flags().GetString("state")

	if !dryRun {
		CheckReadonly("jira sync")
	}

	if preferLocal && preferJira {
		fmt.Fprintf(os.Stderr, "Error: cannot use both --prefer-local and --prefer-jira\n")
		os.Exit(1)
	}

	if err := ensureStoreActive(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: database not available: %v\n", err)
		os.Exit(1)
	}

	if err := validateJiraConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctx := rootCtx

	// Create and initialize the Jira tracker
	jt := &jira.Tracker{}
	if err := jt.Init(ctx, store); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing Jira tracker: %v\n", err)
		os.Exit(1)
	}

	// Create the sync engine
	engine := tracker.NewEngine(jt, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	// Set up Jira-specific push hooks (prefix filtering)
	engine.PushHooks = buildJiraPushHooks(ctx)

	// Build sync options from CLI flags
	opts := tracker.SyncOptions{
		Pull:       pull,
		Push:       push,
		DryRun:     dryRun,
		CreateOnly: createOnly,
		State:      state,
	}

	// Map conflict resolution
	if preferLocal {
		opts.ConflictResolution = tracker.ConflictLocal
	} else if preferJira {
		opts.ConflictResolution = tracker.ConflictExternal
	} else {
		opts.ConflictResolution = tracker.ConflictTimestamp
	}

	// Run sync
	result, err := engine.Sync(ctx, opts)
	if err != nil {
		if jsonOutput {
			outputJSON(result)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}

	// Output results
	if jsonOutput {
		outputJSON(result)
	} else if dryRun {
		fmt.Println("\n✓ Dry run complete (no changes made)")
	} else {
		if result.Stats.Pulled > 0 {
			fmt.Printf("✓ Pulled %d issues (%d created, %d updated)\n",
				result.Stats.Pulled, result.Stats.Created, result.Stats.Updated)
		}
		if result.Stats.Pushed > 0 {
			fmt.Printf("✓ Pushed %d issues\n", result.Stats.Pushed)
		}
		if result.Stats.Conflicts > 0 {
			fmt.Printf("→ Resolved %d conflicts\n", result.Stats.Conflicts)
		}
		fmt.Println("\n✓ Jira sync complete")
		if len(result.Warnings) > 0 {
			fmt.Println("\nWarnings:")
			for _, w := range result.Warnings {
				fmt.Printf("  - %s\n", w)
			}
		}
	}
}

// buildJiraPushHooks creates PushHooks for Jira-specific push behavior.
func buildJiraPushHooks(ctx context.Context) *tracker.PushHooks {
	return &tracker.PushHooks{
		ShouldPush: func(issue *types.Issue) bool {
			pushPrefix, _ := store.GetConfig(ctx, "jira.push_prefix")
			if pushPrefix == "" {
				return true
			}
			for _, prefix := range strings.Split(pushPrefix, ",") {
				prefix = strings.TrimSpace(prefix)
				prefix = strings.TrimSuffix(prefix, "-")
				if prefix != "" && strings.HasPrefix(issue.ID, prefix+"-") {
					return true
				}
			}
			return false
		},
	}
}

func runJiraStatus(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	if err := ensureStoreActive(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	jiraURL, _ := store.GetConfig(ctx, "jira.url")
	jiraProject, _ := store.GetConfig(ctx, "jira.project")
	lastSync, _ := store.GetConfig(ctx, "jira.last_sync")

	configured := jiraURL != "" && jiraProject != ""

	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	withJiraRef := 0
	pendingPush := 0
	for _, issue := range allIssues {
		if issue.ExternalRef != nil && jira.IsJiraExternalRef(*issue.ExternalRef, jiraURL) {
			withJiraRef++
		} else if issue.ExternalRef == nil {
			pendingPush++
		}
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

	apiToken, _ := store.GetConfig(ctx, "jira.api_token")
	if apiToken == "" && os.Getenv("JIRA_API_TOKEN") == "" {
		return fmt.Errorf("Jira API token not configured\nRun: bd config set jira.api_token \"YOUR_TOKEN\"\nOr: export JIRA_API_TOKEN=YOUR_TOKEN")
	}

	return nil
}
