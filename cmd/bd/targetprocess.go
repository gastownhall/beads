package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/targetprocess"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

var targetprocessCmd = &cobra.Command{
	Use:     "targetprocess",
	GroupID: "advanced",
	Short:   "Targetprocess integration commands",
	Long: `Synchronize issues between beads and Targetprocess.

Configuration:
  bd config set targetprocess.url "https://company.tpondemand.com"
  bd config set targetprocess.project_id "123"
  bd config set targetprocess.access_token "YOUR_TOKEN"
  bd config set targetprocess.push_prefix "bd"       # Only push bd-* issues
  bd config set targetprocess.push_prefix "app,ops"  # Multiple prefixes

Alternative authentication:
  bd config set targetprocess.token "SERVICE_TOKEN"
  bd config set targetprocess.username "login"
  bd config set targetprocess.password "password"

Environment variables:
  TARGETPROCESS_URL
  TARGETPROCESS_PROJECT_ID
  TARGETPROCESS_ACCESS_TOKEN
  TARGETPROCESS_TOKEN
  TARGETPROCESS_USERNAME
  TARGETPROCESS_PASSWORD

Examples:
  bd targetprocess sync --pull
  bd targetprocess sync --push
  bd targetprocess sync --dry-run
  bd targetprocess status`,
}

var targetprocessSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Synchronize issues with Targetprocess",
	Run:   runTargetprocessSync,
}

var targetprocessStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Targetprocess sync status",
	Run:   runTargetprocessStatus,
}

func init() {
	targetprocessSyncCmd.Flags().Bool("pull", false, "Pull issues from Targetprocess")
	targetprocessSyncCmd.Flags().Bool("push", false, "Push issues to Targetprocess")
	targetprocessSyncCmd.Flags().Bool("dry-run", false, "Preview sync without making changes")
	targetprocessSyncCmd.Flags().Bool("prefer-local", false, "Prefer local version on conflicts")
	targetprocessSyncCmd.Flags().Bool("prefer-targetprocess", false, "Prefer Targetprocess version on conflicts")
	targetprocessSyncCmd.Flags().Bool("create-only", false, "Only create new issues, don't update existing ones")
	targetprocessSyncCmd.Flags().String("state", "all", "Issue state to sync: open, closed, all")

	targetprocessCmd.AddCommand(targetprocessSyncCmd)
	targetprocessCmd.AddCommand(targetprocessStatusCmd)
	rootCmd.AddCommand(targetprocessCmd)
}

func runTargetprocessSync(cmd *cobra.Command, args []string) {
	pull, _ := cmd.Flags().GetBool("pull")
	push, _ := cmd.Flags().GetBool("push")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	preferLocal, _ := cmd.Flags().GetBool("prefer-local")
	preferTargetprocess, _ := cmd.Flags().GetBool("prefer-targetprocess")
	createOnly, _ := cmd.Flags().GetBool("create-only")
	state, _ := cmd.Flags().GetString("state")

	if preferLocal && preferTargetprocess {
		FatalError("cannot use both --prefer-local and --prefer-targetprocess")
	}
	if !dryRun {
		CheckReadonly("targetprocess sync")
	}
	if err := ensureStoreActive(); err != nil {
		FatalError("database not available: %v", err)
	}
	if err := validateTargetprocessConfig(); err != nil {
		FatalError("%v", err)
	}

	ctx := rootCtx

	tp := &targetprocess.Tracker{}
	if err := tp.Init(ctx, store); err != nil {
		FatalError("initializing Targetprocess tracker: %v", err)
	}

	engine := tracker.NewEngine(tp, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }
	engine.PushHooks = buildTargetprocessPushHooks(ctx)

	opts := tracker.SyncOptions{
		Pull:       pull,
		Push:       push,
		DryRun:     dryRun,
		CreateOnly: createOnly,
		State:      state,
	}

	switch {
	case preferLocal:
		opts.ConflictResolution = tracker.ConflictLocal
	case preferTargetprocess:
		opts.ConflictResolution = tracker.ConflictExternal
	default:
		opts.ConflictResolution = tracker.ConflictTimestamp
	}

	result, err := engine.Sync(ctx, opts)
	if err != nil {
		if jsonOutput {
			outputJSON(result)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	if dryRun {
		fmt.Println("\n✓ Dry run complete (no changes made)")
		return
	}

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
	fmt.Println("\n✓ Targetprocess sync complete")
}

func buildTargetprocessPushHooks(ctx context.Context) *tracker.PushHooks {
	return &tracker.PushHooks{
		ShouldPush: func(issue *types.Issue) bool {
			pushPrefix, _ := store.GetConfig(ctx, "targetprocess.push_prefix")
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

func runTargetprocessStatus(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	if err := ensureStoreActive(); err != nil {
		FatalError("%v", err)
	}

	baseURL, _ := store.GetConfig(ctx, "targetprocess.url")
	projectID, _ := store.GetConfig(ctx, "targetprocess.project_id")
	lastSync, _ := store.GetConfig(ctx, "targetprocess.last_sync")
	configured := baseURL != "" && projectID != ""

	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		FatalError("%v", err)
	}

	withTargetprocessRef := 0
	pendingPush := 0
	for _, issue := range allIssues {
		if issue.ExternalRef != nil && targetprocess.IsExternalRef(*issue.ExternalRef, baseURL) {
			withTargetprocessRef++
		} else if issue.ExternalRef == nil {
			pendingPush++
		}
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"configured":               configured,
			"targetprocess_url":        baseURL,
			"targetprocess_project_id": projectID,
			"last_sync":                lastSync,
			"total_issues":             len(allIssues),
			"with_targetprocess_ref":   withTargetprocessRef,
			"pending_push":             pendingPush,
		})
		return
	}

	fmt.Println("Targetprocess Sync Status")
	fmt.Println("=========================")
	fmt.Println()

	if !configured {
		fmt.Println("Status: Not configured")
		fmt.Println()
		fmt.Println("To configure Targetprocess integration:")
		fmt.Println("  bd config set targetprocess.url \"https://company.tpondemand.com\"")
		fmt.Println("  bd config set targetprocess.project_id \"123\"")
		fmt.Println("  bd config set targetprocess.access_token \"YOUR_TOKEN\"")
		return
	}

	fmt.Printf("Targetprocess URL: %s\n", baseURL)
	fmt.Printf("Project ID:        %s\n", projectID)
	if lastSync != "" {
		fmt.Printf("Last Sync:         %s\n", lastSync)
	} else {
		fmt.Println("Last Sync:         Never")
	}
	fmt.Println()
	fmt.Printf("Total Issues:      %d\n", len(allIssues))
	fmt.Printf("With Targetprocess:%d\n", withTargetprocessRef)
	fmt.Printf("Local Only:        %d\n", pendingPush)
}

func validateTargetprocessConfig() error {
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := rootCtx
	baseURL, _ := store.GetConfig(ctx, "targetprocess.url")
	projectID, _ := store.GetConfig(ctx, "targetprocess.project_id")
	accessToken, _ := store.GetConfig(ctx, "targetprocess.access_token")
	serviceToken, _ := store.GetConfig(ctx, "targetprocess.token")
	username, _ := store.GetConfig(ctx, "targetprocess.username")
	password, _ := store.GetConfig(ctx, "targetprocess.password")

	if baseURL == "" && os.Getenv("TARGETPROCESS_URL") == "" {
		return fmt.Errorf("targetprocess.url not configured\nRun: bd config set targetprocess.url \"https://company.tpondemand.com\"")
	}
	if projectID == "" && os.Getenv("TARGETPROCESS_PROJECT_ID") == "" {
		return fmt.Errorf("targetprocess.project_id not configured\nRun: bd config set targetprocess.project_id \"123\"")
	}

	hasAccessToken := accessToken != "" || os.Getenv("TARGETPROCESS_ACCESS_TOKEN") != ""
	hasServiceToken := serviceToken != "" || os.Getenv("TARGETPROCESS_TOKEN") != ""
	hasBasicAuth := (username != "" || os.Getenv("TARGETPROCESS_USERNAME") != "") &&
		(password != "" || os.Getenv("TARGETPROCESS_PASSWORD") != "")
	if !hasAccessToken && !hasServiceToken && !hasBasicAuth {
		return fmt.Errorf("Targetprocess credentials not configured\nRun: bd config set targetprocess.access_token \"YOUR_TOKEN\"\nOr: bd config set targetprocess.token \"YOUR_SERVICE_TOKEN\"\nOr: bd config set targetprocess.username \"login\" and bd config set targetprocess.password \"password\"")
	}

	return nil
}
