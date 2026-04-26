package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ado"
	"github.com/steveyegge/beads/internal/github"
	"github.com/steveyegge/beads/internal/gitlab"
	"github.com/steveyegge/beads/internal/jira"
	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/notion"
	"github.com/steveyegge/beads/internal/tracker"
)

// trackerPushPullFlags holds shared flags for push/pull subcommands.
type trackerPushPullFlags struct {
	dryRun bool
}

// --- ADO push/pull ---

var adoPushCmd = &cobra.Command{
	Use:   "push [bead-ids...]",
	Short: "Push specific beads to Azure DevOps",
	Long: `Push one or more beads issues to Azure DevOps.

Accepts bead IDs as positional arguments.
Equivalent to: bd ado sync --push-only --issues <ids>`,
	RunE: runADOPush,
}

var adoPullCmd = &cobra.Command{
	Use:   "pull [refs...]",
	Short: "Pull specific items from Azure DevOps",
	Long: `Pull one or more items from Azure DevOps.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd ado sync --pull-only --issues <refs>`,
	RunE: runADOPull,
}

// --- Jira push/pull ---

var jiraPushCmd = &cobra.Command{
	Use:   "push [bead-ids...]",
	Short: "Push specific beads to Jira",
	Long: `Push one or more beads issues to Jira.

Accepts bead IDs as positional arguments.
Equivalent to: bd jira sync --push --issues <ids>`,
	Run: runJiraPush,
}

var jiraPullCmd = &cobra.Command{
	Use:   "pull [refs...]",
	Short: "Pull specific items from Jira",
	Long: `Pull one or more items from Jira.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd jira sync --pull --issues <refs>`,
	Run: runJiraPull,
}

// --- Linear push/pull ---

var linearPushCmd = &cobra.Command{
	Use:   "push [bead-ids...]",
	Short: "Push specific beads to Linear",
	Long: `Push one or more beads issues to Linear.

Accepts bead IDs as positional arguments.
Equivalent to: bd linear sync --push --issues <ids>`,
	Run: runLinearPush,
}

var linearPullCmd = &cobra.Command{
	Use:   "pull [refs...]",
	Short: "Pull specific items from Linear",
	Long: `Pull one or more items from Linear.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd linear sync --pull --issues <refs>`,
	Run: runLinearPull,
}

// --- GitHub push/pull ---

var githubPushCmd = &cobra.Command{
	Use:   "push [bead-ids...]",
	Short: "Push specific beads to GitHub",
	Long: `Push one or more beads issues to GitHub.

Accepts bead IDs as positional arguments.
Equivalent to: bd github sync --push-only --issues <ids>`,
	RunE: runGitHubPush,
}

var githubPullCmd = &cobra.Command{
	Use:   "pull [refs...]",
	Short: "Pull specific items from GitHub",
	Long: `Pull one or more items from GitHub.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd github sync --pull-only --issues <refs>`,
	RunE: runGitHubPull,
}

// --- GitLab push/pull ---

var gitlabPushCmd = &cobra.Command{
	Use:   "push [bead-ids...]",
	Short: "Push specific beads to GitLab",
	Long: `Push one or more beads issues to GitLab.

Accepts bead IDs as positional arguments.
Equivalent to: bd gitlab sync --push-only --issues <ids>`,
	RunE: runGitLabPush,
}

var gitlabPullCmd = &cobra.Command{
	Use:   "pull [refs...]",
	Short: "Pull specific items from GitLab",
	Long: `Pull one or more items from GitLab.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd gitlab sync --pull-only --issues <refs>`,
	RunE: runGitLabPull,
}

// --- Notion push/pull ---

var notionPushCmd = &cobra.Command{
	Use:   "push [bead-ids...]",
	Short: "Push specific beads to Notion",
	Long: `Push one or more beads issues to Notion.

Accepts bead IDs as positional arguments.
Equivalent to: bd notion sync --push --issues <ids>`,
	RunE: runNotionPush,
}

var notionPullCmd = &cobra.Command{
	Use:   "pull [refs...]",
	Short: "Pull specific items from Notion",
	Long: `Pull one or more items from Notion.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd notion sync --pull --issues <refs>`,
	RunE: runNotionPull,
}

func init() {
	// ADO push/pull
	adoPushCmd.Flags().Bool("dry-run", false, "Preview push without making changes")
	adoPullCmd.Flags().Bool("dry-run", false, "Preview pull without making changes")
	adoCmd.AddCommand(adoPushCmd)
	adoCmd.AddCommand(adoPullCmd)

	// Jira push/pull
	jiraPushCmd.Flags().Bool("dry-run", false, "Preview push without making changes")
	jiraPullCmd.Flags().Bool("dry-run", false, "Preview pull without making changes")
	jiraCmd.AddCommand(jiraPushCmd)
	jiraCmd.AddCommand(jiraPullCmd)

	// Linear push/pull
	linearPushCmd.Flags().Bool("dry-run", false, "Preview push without making changes")
	linearPullCmd.Flags().Bool("dry-run", false, "Preview pull without making changes")
	linearPullCmd.Flags().Bool("relations", false, "Import Linear relations as bd dependencies when pulling")
	linearCmd.AddCommand(linearPushCmd)
	linearCmd.AddCommand(linearPullCmd)

	// GitHub push/pull
	githubPushCmd.Flags().Bool("dry-run", false, "Preview push without making changes")
	githubPullCmd.Flags().Bool("dry-run", false, "Preview pull without making changes")
	githubCmd.AddCommand(githubPushCmd)
	githubCmd.AddCommand(githubPullCmd)

	// GitLab push/pull
	gitlabPushCmd.Flags().Bool("dry-run", false, "Preview push without making changes")
	gitlabPullCmd.Flags().Bool("dry-run", false, "Preview pull without making changes")
	gitlabCmd.AddCommand(gitlabPushCmd)
	gitlabCmd.AddCommand(gitlabPullCmd)

	// Notion push/pull
	notionPushCmd.Flags().Bool("dry-run", false, "Preview push without making changes")
	notionPullCmd.Flags().Bool("dry-run", false, "Preview pull without making changes")
	notionCmd.AddCommand(notionPushCmd)
	notionCmd.AddCommand(notionPullCmd)
}

// outputSyncResult writes sync results as JSON or human-readable text.
func outputSyncResult(result *tracker.SyncResult, dryRun bool) {
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	if dryRun {
		fmt.Println("Dry run mode - no changes will be made")
	}
	if result.Stats.Pulled > 0 {
		fmt.Printf("✓ Pulled %d issues (%d created, %d updated)\n",
			result.Stats.Pulled, result.Stats.Created, result.Stats.Updated)
	}
	if result.Stats.Pushed > 0 {
		fmt.Printf("✓ Pushed %d issues\n", result.Stats.Pushed)
	}
	if dryRun {
		fmt.Println("\nRun without --dry-run to apply changes")
	}
}

// --- ADO implementations ---

func runADOPush(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one bead ID is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("ado push")
	}

	cfg := getADOConfig()
	if err := validateADOConfig(cfg); err != nil {
		return err
	}
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := cmd.Context()
	at := &ado.Tracker{}
	if err := at.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing Azure DevOps tracker: %w", err)
	}

	engine := tracker.NewEngine(at, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Push:     true,
		Pull:     false,
		DryRun:   dryRun,
		IssueIDs: args,
	})
	if err != nil {
		return err
	}
	outputSyncResult(result, dryRun)
	return nil
}

func runADOPull(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one bead ID or external reference is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("ado pull")
	}

	cfg := getADOConfig()
	if err := validateADOConfig(cfg); err != nil {
		return err
	}
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := cmd.Context()
	at := &ado.Tracker{}
	if err := at.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing Azure DevOps tracker: %w", err)
	}

	engine := tracker.NewEngine(at, store, actor)
	engine.PullHooks = buildADOPullHooks(ctx, at, false, false, new(int), engine.OnWarning)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Pull:     true,
		Push:     false,
		DryRun:   dryRun,
		IssueIDs: args,
	})
	if err != nil {
		return err
	}
	outputSyncResult(result, dryRun)
	return nil
}

// --- Jira implementations ---

func runJiraPush(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		FatalError("at least one bead ID is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("jira push")
	}

	if err := ensureStoreActive(); err != nil {
		FatalError("database not available: %v", err)
	}
	if err := validateJiraConfig(); err != nil {
		FatalError("%v", err)
	}

	ctx := rootCtx
	jt := &jira.Tracker{}
	if err := jt.Init(ctx, store); err != nil {
		FatalError("initializing Jira tracker: %v", err)
	}

	engine := tracker.NewEngine(jt, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }
	engine.PushHooks = buildJiraPushHooks(ctx)

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Push:     true,
		Pull:     false,
		DryRun:   dryRun,
		IssueIDs: args,
	})
	if err != nil {
		FatalError("sync failed: %v", err)
	}
	outputSyncResult(result, dryRun)
}

func runJiraPull(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		FatalError("at least one bead ID or external reference is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("jira pull")
	}

	if err := ensureStoreActive(); err != nil {
		FatalError("database not available: %v", err)
	}
	if err := validateJiraConfig(); err != nil {
		FatalError("%v", err)
	}

	ctx := rootCtx
	jt := &jira.Tracker{}
	if err := jt.Init(ctx, store); err != nil {
		FatalError("initializing Jira tracker: %v", err)
	}

	engine := tracker.NewEngine(jt, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Pull:     true,
		Push:     false,
		DryRun:   dryRun,
		IssueIDs: args,
	})
	if err != nil {
		FatalError("sync failed: %v", err)
	}
	outputSyncResult(result, dryRun)
}

// --- Linear implementations ---

func runLinearPush(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		FatalError("at least one bead ID is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("linear push")
	}

	if err := ensureStoreActive(); err != nil {
		FatalError("database not available: %v", err)
	}
	if err := validateLinearConfig(nil); err != nil {
		FatalError("%v", err)
	}

	ctx := rootCtx
	teamIDs := getLinearTeamIDs(ctx, nil)
	if len(teamIDs) > 1 {
		FatalError("linear push does not support multiple configured teams\nUse: bd linear sync --push --team <TEAM_ID>")
	}

	lt := &linear.Tracker{}
	lt.SetTeamIDs(teamIDs)
	if err := lt.Init(ctx, store); err != nil {
		FatalError("initializing Linear tracker: %v", err)
	}
	if err := lt.ValidatePushStateMappings(ctx); err != nil {
		FatalError("%v", err)
	}

	engine := tracker.NewEngine(lt, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }
	engine.PushHooks = buildLinearPushHooks(ctx, lt, len(args) > 0)

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Push:             true,
		Pull:             false,
		DryRun:           dryRun,
		ExcludeEphemeral: true,
		IssueIDs:         args,
	})
	if err != nil {
		FatalError("sync failed: %v", err)
	}
	outputSyncResult(result, dryRun)
}

func runLinearPull(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		FatalError("at least one bead ID or external reference is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	relations, _ := cmd.Flags().GetBool("relations")
	if !dryRun {
		CheckReadonly("linear pull")
	}

	if err := ensureStoreActive(); err != nil {
		FatalError("database not available: %v", err)
	}
	if err := validateLinearConfig(nil); err != nil {
		FatalError("%v", err)
	}

	ctx := rootCtx
	teamIDs := getLinearTeamIDs(ctx, nil)

	lt := &linear.Tracker{}
	lt.SetTeamIDs(teamIDs)
	if err := lt.Init(ctx, store); err != nil {
		FatalError("initializing Linear tracker: %v", err)
	}

	engine := tracker.NewEngine(lt, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }
	engine.PullHooks = buildLinearPullHooks(ctx)

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Pull:              true,
		Push:              false,
		DryRun:            dryRun,
		IssueIDs:          args,
		DependencySources: linearPullDependencySources(relations),
	})
	if err != nil {
		FatalError("sync failed: %v", err)
	}
	outputSyncResult(result, dryRun)
}

// --- GitHub implementations ---

func runGitHubPush(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one bead ID is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("github push")
	}

	config := getGitHubConfig()
	if err := validateGitHubConfig(config); err != nil {
		return err
	}
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := cmd.Context()
	gt := &github.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing GitHub tracker: %w", err)
	}

	engine := tracker.NewEngine(gt, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Push:     true,
		Pull:     false,
		DryRun:   dryRun,
		IssueIDs: args,
	})
	if err != nil {
		return err
	}
	outputSyncResult(result, dryRun)
	return nil
}

func runGitHubPull(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one bead ID or external reference is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("github pull")
	}

	config := getGitHubConfig()
	if err := validateGitHubConfig(config); err != nil {
		return err
	}
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := cmd.Context()
	gt := &github.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing GitHub tracker: %w", err)
	}

	engine := tracker.NewEngine(gt, store, actor)
	engine.PullHooks = buildGitHubPullHooks(ctx)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Pull:     true,
		Push:     false,
		DryRun:   dryRun,
		IssueIDs: args,
	})
	if err != nil {
		return err
	}
	outputSyncResult(result, dryRun)
	return nil
}

// --- GitLab implementations ---

func runGitLabPush(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one bead ID is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("gitlab push")
	}

	config := getGitLabConfig()
	if err := validateGitLabConfig(config); err != nil {
		return err
	}
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := cmd.Context()
	gt := &gitlab.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing GitLab tracker: %w", err)
	}

	engine := tracker.NewEngine(gt, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Push:     true,
		Pull:     false,
		DryRun:   dryRun,
		IssueIDs: args,
	})
	if err != nil {
		return err
	}
	outputSyncResult(result, dryRun)
	return nil
}

func runGitLabPull(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one bead ID or external reference is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("gitlab pull")
	}

	config := getGitLabConfig()
	if err := validateGitLabConfig(config); err != nil {
		return err
	}
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := cmd.Context()
	gt := &gitlab.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing GitLab tracker: %w", err)
	}

	engine := tracker.NewEngine(gt, store, actor)
	engine.PullHooks = buildGitLabPullHooks(ctx)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Pull:     true,
		Push:     false,
		DryRun:   dryRun,
		IssueIDs: args,
	})
	if err != nil {
		return err
	}
	outputSyncResult(result, dryRun)
	return nil
}

// --- Notion implementations ---

func runNotionPush(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one bead ID is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("notion push")
	}

	cfg := getNotionConfig()
	auth, err := resolveNotionAuth(cmd.Context())
	if err != nil {
		return err
	}
	if err := validateNotionConfig(cfg, auth); err != nil {
		return err
	}
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := cmd.Context()
	nt := &notion.Tracker{}
	if err := nt.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing Notion tracker: %w", err)
	}

	engine := tracker.NewEngine(nt, store, actor)
	unsupportedStats := newNotionUnsupportedPushStats()
	engine.PushHooks = buildNotionPushHooks(ctx, nt, unsupportedStats)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	result, syncErr := engine.Sync(ctx, tracker.SyncOptions{
		Push:             true,
		Pull:             false,
		DryRun:           dryRun,
		ExcludeEphemeral: true,
		IssueIDs:         args,
	})
	if syncErr != nil {
		return syncErr
	}
	outputSyncResult(result, dryRun)
	return nil
}

func runNotionPull(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one bead ID or external reference is required")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		CheckReadonly("notion pull")
	}

	cfg := getNotionConfig()
	auth, err := resolveNotionAuth(cmd.Context())
	if err != nil {
		return err
	}
	if err := validateNotionConfig(cfg, auth); err != nil {
		return err
	}
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := cmd.Context()
	nt := &notion.Tracker{}
	if err := nt.Init(ctx, store); err != nil {
		return fmt.Errorf("initializing Notion tracker: %w", err)
	}

	engine := tracker.NewEngine(nt, store, actor)
	engine.PullHooks = buildNotionPullHooks(ctx)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	result, syncErr := engine.Sync(ctx, tracker.SyncOptions{
		Pull:     true,
		Push:     false,
		DryRun:   dryRun,
		IssueIDs: args,
	})
	if syncErr != nil {
		return syncErr
	}
	outputSyncResult(result, dryRun)
	return nil
}
