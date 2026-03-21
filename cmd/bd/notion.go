package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/notion"
	noutput "github.com/steveyegge/beads/internal/notion/output"
	nstate "github.com/steveyegge/beads/internal/notion/state"
	itracker "github.com/steveyegge/beads/internal/tracker"
)

type notionStatusClient interface {
	Status(ctx context.Context, req notion.StatusRequest) (*notion.StatusResponse, error)
}

type notionSyncEngine interface {
	Sync(ctx context.Context, opts itracker.SyncOptions) (*itracker.SyncResult, error)
}

type notionManagementService interface {
	Init(ctx context.Context, parentID, title string) error
	Connect(ctx context.Context, databaseID, viewURL string) error
	ConfigShow() error
	ConfigClear() error
	StateShow() error
	StateExport(outputPath string) error
	StateImport(inputPath string) error
	StateDoctor(ctx context.Context) error
}

type notionCommandDeps struct {
	io        *noutput.IO
	authStore *nstate.AuthStore
	service   notionManagementService
}

var (
	notionDatabaseID   string
	notionViewURL      string
	notionInitParent   string
	notionInitTitle    string
	notionConnectDBID  string
	notionConnectView  string
	notionStateExport  string
	notionStateImport  string
	notionSyncPull     bool
	notionSyncPush     bool
	notionSyncDryRun   bool
	notionPreferLocal  bool
	notionPreferNotion bool
	notionCreateOnly   bool
	notionSyncState    string
	notionCacheMaxAge  time.Duration
)

var newNotionStatusClient = func() notionStatusClient {
	return notion.NewClient()
}

var newNotionTracker = func() itracker.IssueTracker {
	return notion.NewTracker(
		notion.WithTrackerDatabaseID(notionDatabaseID),
		notion.WithTrackerViewURL(notionViewURL),
		notion.WithTrackerCacheMaxAge(notionCacheMaxAge),
	)
}

var newNotionSyncEngine = func(tr itracker.IssueTracker) notionSyncEngine {
	return itracker.NewEngine(tr, store, actor)
}

var newNotionCommandDeps = func(cmd *cobra.Command) (*notionCommandDeps, error) {
	paths, err := nstate.DefaultPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve Notion config paths: %w", err)
	}
	ioo := noutput.NewIO(cmd.OutOrStdout(), cmd.ErrOrStderr()).WithJSON(jsonOutput)
	authStore := nstate.NewAuthStore(paths)
	return &notionCommandDeps{
		io:        ioo,
		authStore: authStore,
		service:   notion.NewService(ioo, authStore),
	}, nil
}

var notionLoginAction = notion.Login
var notionLogoutAction = notion.Logout
var notionWhoAmIAction = notion.WhoAmI

var ensureNotionStoreActive = ensureStoreActive
var checkNotionReadonly = CheckReadonly

var notionCmd = &cobra.Command{
	Use:     "notion",
	GroupID: "advanced",
	Short:   "Notion integration commands",
	Long: "Synchronize issues between beads and Notion through the integrated Notion adapter.\n\n" +
		"This integration keeps the shared tracker engine and JSON contract while running Notion auth, schema, state, and sync operations directly inside bd.\n\n" +
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
		"  - auth and adapter readiness\n" +
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
		"Pull and bidirectional sync always read the saved Notion target config.\n" +
		"Database/view overrides are therefore only supported for push-only sync.\n\n" +
		"By default, local beads issues are created in Notion and Notion-linked issues are updated on subsequent syncs.\n\n" +
		"Archive and delete operations are not supported yet. Use --dry-run to inspect what would change until live MCP exposes archive support.",
	RunE: runNotionSync,
}

var notionLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate bd against Notion",
	RunE:  runNotionLogin,
}

var notionLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove saved Notion credentials",
	RunE:  runNotionLogout,
}

var notionWhoAmICmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show the authenticated Notion user",
	RunE:  runNotionWhoAmI,
}

var notionInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a dedicated Beads database and default table view in Notion",
	RunE:  runNotionInit,
}

var notionConnectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Connect bd to an existing Notion Beads database and view",
	RunE:  runNotionConnect,
}

var notionConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect or clear the saved Notion target",
}

var notionConfigShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the saved Notion target configuration",
	RunE:  runNotionConfigShow,
}

var notionConfigClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear the saved Notion target configuration",
	RunE:  runNotionConfigClear,
}

var notionStateCmd = &cobra.Command{
	Use:   "state",
	Short: "Inspect and manage saved Notion page state",
}

var notionStateShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show saved Notion page state",
	RunE:  runNotionStateShow,
}

var notionStateExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export saved Notion page state",
	RunE:  runNotionStateExport,
}

var notionStateImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import saved Notion page state",
	RunE:  runNotionStateImport,
}

var notionStateDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose saved Notion state against live pages",
	RunE:  runNotionStateDoctor,
}

func init() {
	notionInitCmd.Flags().StringVar(&notionInitParent, "parent", "", "Parent page ID")
	notionInitCmd.Flags().StringVar(&notionInitTitle, "title", "Beads Issues", "Database title")
	_ = notionInitCmd.MarkFlagRequired("parent")

	notionConnectCmd.Flags().StringVar(&notionConnectDBID, "database-id", "", "Notion database ID")
	notionConnectCmd.Flags().StringVar(&notionConnectView, "view-url", "", "Notion view URL")
	_ = notionConnectCmd.MarkFlagRequired("database-id")
	_ = notionConnectCmd.MarkFlagRequired("view-url")

	notionStateExportCmd.Flags().StringVar(&notionStateExport, "output", "", "Export file path, or - for stdout")
	_ = notionStateExportCmd.MarkFlagRequired("output")
	notionStateImportCmd.Flags().StringVar(&notionStateImport, "input", "", "Import file path, or - for stdin")
	_ = notionStateImportCmd.MarkFlagRequired("input")

	notionStatusCmd.Flags().StringVar(&notionDatabaseID, "database-id", "", "Override the Notion database ID")
	notionStatusCmd.Flags().StringVar(&notionViewURL, "view-url", "", "Override the Notion view URL")

	notionSyncCmd.Flags().StringVar(&notionDatabaseID, "database-id", "", "Override the Notion database ID")
	notionSyncCmd.Flags().StringVar(&notionViewURL, "view-url", "", "Override the Notion view URL")
	notionSyncCmd.Flags().BoolVar(&notionSyncPull, "pull", false, "Pull issues from Notion")
	notionSyncCmd.Flags().BoolVar(&notionSyncPush, "push", false, "Push issues to Notion")
	notionSyncCmd.Flags().BoolVar(&notionSyncDryRun, "dry-run", false, "Preview sync without making changes")
	notionSyncCmd.Flags().BoolVar(&notionPreferLocal, "prefer-local", false, "Prefer local beads version on conflicts")
	notionSyncCmd.Flags().BoolVar(&notionPreferNotion, "prefer-notion", false, "Prefer Notion version on conflicts")
	notionSyncCmd.Flags().BoolVar(&notionCreateOnly, "create-only", false, "Only create new issues, do not update existing ones")
	notionSyncCmd.Flags().StringVar(&notionSyncState, "state", "all", "Issue state to sync: open, closed, all")
	notionSyncCmd.Flags().DurationVar(&notionCacheMaxAge, "cache-max-age", 0, "Reuse cached Notion snapshots younger than this duration during pull/push")

	notionConfigCmd.AddCommand(notionConfigShowCmd, notionConfigClearCmd)
	notionStateCmd.AddCommand(notionStateShowCmd, notionStateExportCmd, notionStateImportCmd, notionStateDoctorCmd)
	notionCmd.AddCommand(notionLoginCmd, notionLogoutCmd, notionWhoAmICmd, notionInitCmd, notionConnectCmd, notionConfigCmd, notionStateCmd)
	notionCmd.AddCommand(notionStatusCmd)
	notionCmd.AddCommand(notionSyncCmd)
	rootCmd.AddCommand(notionCmd)
}

func runNotionLogin(cmd *cobra.Command, _ []string) error {
	checkNotionReadonly("notion login")
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return notionLoginAction(cmd.Context(), deps.io, deps.authStore)
}

func runNotionLogout(cmd *cobra.Command, _ []string) error {
	checkNotionReadonly("notion logout")
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return notionLogoutAction(deps.io, deps.authStore)
}

func runNotionWhoAmI(cmd *cobra.Command, _ []string) error {
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return notionWhoAmIAction(cmd.Context(), deps.io, deps.authStore)
}

func runNotionInit(cmd *cobra.Command, _ []string) error {
	checkNotionReadonly("notion init")
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return deps.service.Init(cmd.Context(), notionInitParent, notionInitTitle)
}

func runNotionConnect(cmd *cobra.Command, _ []string) error {
	checkNotionReadonly("notion connect")
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return deps.service.Connect(cmd.Context(), notionConnectDBID, notionConnectView)
}

func runNotionConfigShow(cmd *cobra.Command, _ []string) error {
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return deps.service.ConfigShow()
}

func runNotionConfigClear(cmd *cobra.Command, _ []string) error {
	checkNotionReadonly("notion config clear")
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return deps.service.ConfigClear()
}

func runNotionStateShow(cmd *cobra.Command, _ []string) error {
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return deps.service.StateShow()
}

func runNotionStateExport(cmd *cobra.Command, _ []string) error {
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return deps.service.StateExport(notionStateExport)
}

func runNotionStateImport(cmd *cobra.Command, _ []string) error {
	checkNotionReadonly("notion state import")
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return deps.service.StateImport(notionStateImport)
}

func runNotionStateDoctor(cmd *cobra.Command, _ []string) error {
	deps, err := newNotionCommandDeps(cmd)
	if err != nil {
		return err
	}
	return deps.service.StateDoctor(cmd.Context())
}

func runNotionStatus(cmd *cobra.Command, _ []string) error {
	cfg := resolveNotionRuntimeConfig(cmd.Context())
	client := newNotionStatusClient()
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

func resolveNotionRuntimeConfig(ctx context.Context) notion.RuntimeConfig {
	return notion.ResolveRuntimeConfig(ctx, store, notion.RuntimeConfigInput{
		DatabaseID:    notionDatabaseID,
		DatabaseIDSet: notionDatabaseID != "",
		ViewURL:       notionViewURL,
		ViewURLSet:    notionViewURL != "",
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

	return fmt.Errorf("pull and bidirectional Notion sync use the saved Notion target config; database-id/view-url overrides are only supported with --push")
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
