package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/notion"
	itracker "github.com/steveyegge/beads/internal/tracker"
)

type notionStatusClient interface {
	Status(ctx context.Context, req notion.StatusRequest) (*notion.StatusResponse, error)
}

type notionSyncEngine interface {
	Sync(ctx context.Context, opts itracker.SyncOptions) (*itracker.SyncResult, error)
}

var (
	notionNCLIBin      string
	notionDatabaseID   string
	notionViewURL      string
	notionSyncPull     bool
	notionSyncPush     bool
	notionSyncDryRun   bool
	notionPreferLocal  bool
	notionPreferNotion bool
	notionCreateOnly   bool
	notionSyncState    string
)

var newNotionStatusClient = func(binaryPath string) notionStatusClient {
	if strings.TrimSpace(binaryPath) == "" {
		return notion.NewClient()
	}
	return notion.NewClient(notion.WithBinaryPath(binaryPath))
}

var newNotionTracker = func() itracker.IssueTracker {
	return notion.NewTracker(
		notion.WithTrackerBinaryPath(notionNCLIBin),
		notion.WithTrackerDatabaseID(notionDatabaseID),
		notion.WithTrackerViewURL(notionViewURL),
	)
}

var newNotionSyncEngine = func(tr itracker.IssueTracker) notionSyncEngine {
	return itracker.NewEngine(tr, store, actor)
}

var ensureNotionStoreActive = ensureStoreActive
var checkNotionReadonly = CheckReadonly

var notionCmd = &cobra.Command{
	Use:     "notion",
	GroupID: "advanced",
	Short:   "Notion integration commands",
	Long: "Synchronize issues between beads and Notion through ncli beads commands.\n\n" +
		"This integration uses the local ncli binary rather than the Notion public API directly.\n\n" +
		"Examples:\n" +
		"  bd notion status\n" +
		"  bd notion sync\n" +
		"  bd notion sync --dry-run\n" +
		"  bd notion status --database-id <database-id> --view-url <view-url>",
}

var notionStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Notion sync status",
	Long: "Show the current Notion sync status, including:\n" +
		"  - ncli readiness\n" +
		"  - database and view selection\n" +
		"  - schema validation status\n" +
		"  - archive capability visibility",
	RunE: runNotionStatus,
}

var notionSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Synchronize issues with Notion",
	Long: "Synchronize issues between beads and Notion through the shared tracker engine.\n\n" +
		"Modes:\n" +
		"  --pull         Import issues from Notion into beads\n" +
		"  --push         Export issues from beads to Notion\n" +
		"  (no flags)     Bidirectional sync: pull then push, with conflict resolution\n\n" +
		"Pull and bidirectional sync always read the saved ncli beads config.\n" +
		"Database/view overrides are therefore only supported for push-only sync.\n\n" +
		"By default, local beads issues are created in Notion and Notion-linked issues are updated on subsequent syncs.\n\n" +
		"Archive and delete operations are not supported yet. If you need archive semantics, keep using the ncli dry-run flow until live MCP exposes archive support.",
	RunE: runNotionSync,
}

func init() {
	notionStatusCmd.Flags().StringVar(&notionNCLIBin, "ncli-bin", "", "Path to the ncli binary")
	notionStatusCmd.Flags().StringVar(&notionDatabaseID, "database-id", "", "Override the Notion database ID")
	notionStatusCmd.Flags().StringVar(&notionViewURL, "view-url", "", "Override the Notion view URL")

	notionSyncCmd.Flags().StringVar(&notionNCLIBin, "ncli-bin", "", "Path to the ncli binary")
	notionSyncCmd.Flags().StringVar(&notionDatabaseID, "database-id", "", "Override the Notion database ID")
	notionSyncCmd.Flags().StringVar(&notionViewURL, "view-url", "", "Override the Notion view URL")
	notionSyncCmd.Flags().BoolVar(&notionSyncPull, "pull", false, "Pull issues from Notion")
	notionSyncCmd.Flags().BoolVar(&notionSyncPush, "push", false, "Push issues to Notion")
	notionSyncCmd.Flags().BoolVar(&notionSyncDryRun, "dry-run", false, "Preview sync without making changes")
	notionSyncCmd.Flags().BoolVar(&notionPreferLocal, "prefer-local", false, "Prefer local beads version on conflicts")
	notionSyncCmd.Flags().BoolVar(&notionPreferNotion, "prefer-notion", false, "Prefer Notion version on conflicts")
	notionSyncCmd.Flags().BoolVar(&notionCreateOnly, "create-only", false, "Only create new issues, do not update existing ones")
	notionSyncCmd.Flags().StringVar(&notionSyncState, "state", "all", "Issue state to sync: open, closed, all")

	notionCmd.AddCommand(notionStatusCmd)
	notionCmd.AddCommand(notionSyncCmd)
	rootCmd.AddCommand(notionCmd)
}

func runNotionStatus(cmd *cobra.Command, _ []string) error {
	cfg := resolveNotionRuntimeConfig(cmd.Context())
	client := newNotionStatusClient(cfg.BinaryPath)
	resp, err := client.Status(cmd.Context(), notion.StatusRequest{
		DatabaseID: cfg.DatabaseID,
		ViewURL:    cfg.ViewURL,
	})
	if err != nil {
		return err
	}

	if jsonOutput {
		outputJSON(resp)
		return nil
	}

	renderNotionStatus(cmd, resp)
	return nil
}

func runNotionSync(cmd *cobra.Command, _ []string) error {
	if !notionSyncDryRun {
		checkNotionReadonly("notion sync")
	}
	if notionPreferLocal && notionPreferNotion {
		return fmt.Errorf("cannot use both --prefer-local and --prefer-notion")
	}
	if err := ensureNotionStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}
	opts := buildNotionSyncOptions()
	if err := validateNotionSyncOverrides(cmd.Context(), opts); err != nil {
		return err
	}

	if err := preflightNotionSync(cmd.Context()); err != nil {
		return err
	}

	tr := newNotionTracker()
	if err := tr.Init(cmd.Context(), store); err != nil {
		return fmt.Errorf("initializing Notion tracker: %w", err)
	}

	engine := newNotionSyncEngine(tr)
	if concrete, ok := engine.(*itracker.Engine); ok {
		if jsonOutput {
			concrete.OnMessage = func(msg string) { fmt.Fprintln(cmd.ErrOrStderr(), "  "+msg) }
		} else {
			concrete.OnMessage = func(msg string) { fmt.Fprintln(cmd.OutOrStdout(), "  "+msg) }
		}
		concrete.OnWarning = func(msg string) { fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", msg) }
	}

	result, err := engine.Sync(cmd.Context(), opts)
	if err != nil {
		return err
	}
	if jsonOutput {
		outputJSON(result)
		return nil
	}
	renderNotionSyncResult(cmd, result)
	return nil
}

func preflightNotionSync(ctx context.Context) error {
	cfg := resolveNotionRuntimeConfig(ctx)
	client := newNotionStatusClient(cfg.BinaryPath)
	resp, err := client.Status(ctx, notion.StatusRequest{
		DatabaseID: cfg.DatabaseID,
		ViewURL:    cfg.ViewURL,
	})
	if err != nil {
		return err
	}
	if resp == nil || !resp.Ready {
		return fmt.Errorf("Notion sync is not ready; run \"bd notion status\" or \"ncli beads status\" to inspect configuration")
	}
	return nil
}

func resolveNotionRuntimeConfig(ctx context.Context) notion.RuntimeConfig {
	return notion.ResolveRuntimeConfig(ctx, store, notion.RuntimeConfigInput{
		BinaryPath:    notionNCLIBin,
		BinaryPathSet: strings.TrimSpace(notionNCLIBin) != "",
		DatabaseID:    notionDatabaseID,
		DatabaseIDSet: strings.TrimSpace(notionDatabaseID) != "",
		ViewURL:       notionViewURL,
		ViewURLSet:    strings.TrimSpace(notionViewURL) != "",
	})
}

func buildNotionSyncOptions() itracker.SyncOptions {
	opts := itracker.SyncOptions{
		Pull:       notionSyncPull,
		Push:       notionSyncPush,
		DryRun:     notionSyncDryRun,
		CreateOnly: notionCreateOnly,
		State:      notionSyncState,
	}
	if notionPreferLocal {
		opts.ConflictResolution = itracker.ConflictLocal
	} else if notionPreferNotion {
		opts.ConflictResolution = itracker.ConflictExternal
	} else {
		opts.ConflictResolution = itracker.ConflictTimestamp
	}
	return opts
}

func validateNotionSyncOverrides(ctx context.Context, opts itracker.SyncOptions) error {
	if !notionSyncIncludesPull(opts) {
		return nil
	}

	cfg := resolveNotionRuntimeConfig(ctx)
	if strings.TrimSpace(cfg.DatabaseID) == "" && strings.TrimSpace(cfg.ViewURL) == "" {
		return nil
	}

	return fmt.Errorf("pull and bidirectional Notion sync use the saved ncli beads config; database-id/view-url overrides are only supported with --push")
}

func notionSyncIncludesPull(opts itracker.SyncOptions) bool {
	if !opts.Pull && !opts.Push {
		return true
	}
	return opts.Pull
}

func renderNotionStatus(cmd *cobra.Command, resp *notion.StatusResponse) {
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "Notion Sync Status")
	fmt.Fprintln(out, "==================")
	fmt.Fprintln(out)

	if resp == nil {
		fmt.Fprintln(out, "Status: unknown")
		return
	}

	fmt.Fprintf(out, "Ready: %s\n", yesNo(resp.Ready))
	if resp.Database != nil {
		if resp.Database.Title != "" {
			fmt.Fprintf(out, "Database: %s\n", resp.Database.Title)
		}
		if resp.Database.ID != "" {
			fmt.Fprintf(out, "Database ID: %s\n", resp.Database.ID)
		}
	}
	if resp.DataSourceID != "" {
		fmt.Fprintf(out, "Data Source ID: %s\n", resp.DataSourceID)
	}
	if len(resp.Views) > 0 {
		fmt.Fprintf(out, "Views: %d\n", len(resp.Views))
	}
	if resp.Schema != nil && len(resp.Schema.Missing) > 0 {
		fmt.Fprintf(out, "Schema Missing: %s\n", strings.Join(resp.Schema.Missing, ", "))
	}
	if resp.Archive != nil {
		if resp.Archive.Supported {
			fmt.Fprintln(out, "Archive Support: available")
		} else {
			fmt.Fprintln(out, "Archive Support: unavailable")
			if resp.Archive.Reason != "" {
				fmt.Fprintf(out, "Archive Reason: %s\n", resp.Archive.Reason)
			}
		}
	}
}

func renderNotionSyncResult(cmd *cobra.Command, result *itracker.SyncResult) {
	out := cmd.OutOrStdout()
	if result == nil {
		fmt.Fprintln(out, "No sync result returned")
		return
	}
	if notionSyncDryRun {
		fmt.Fprintln(out, "\n✓ Dry run complete (no changes made)")
		return
	}
	if result.Stats.Pulled > 0 {
		fmt.Fprintf(out, "✓ Pulled %d issues (%d created, %d updated)\n", result.Stats.Pulled, result.PullStats.Created, result.PullStats.Updated)
	}
	if result.Stats.Pushed > 0 {
		fmt.Fprintf(out, "✓ Pushed %d issues\n", result.Stats.Pushed)
	}
	if result.Stats.Conflicts > 0 {
		fmt.Fprintf(out, "→ Resolved %d conflicts\n", result.Stats.Conflicts)
	}
	fmt.Fprintln(out, "\n✓ Notion sync complete")
	if len(result.Warnings) > 0 {
		fmt.Fprintln(out, "\nWarnings:")
		for _, warning := range result.Warnings {
			fmt.Fprintf(out, "  - %s\n", warning)
		}
	}
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
