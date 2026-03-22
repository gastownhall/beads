package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/notion"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type notionConfig struct {
	Token        string
	DataSourceID string
	ViewURL      string
}

type notionSetupResult struct {
	Action       string `json:"action"`
	DatabaseID   string `json:"database_id,omitempty"`
	DataSourceID string `json:"data_source_id,omitempty"`
	ViewURL      string `json:"view_url,omitempty"`
	Message      string `json:"message,omitempty"`
}

var (
	notionInitParent string
	notionInitTitle  string
	notionConnectURL string

	notionSyncPull     bool
	notionSyncPush     bool
	notionSyncDryRun   bool
	notionPreferLocal  bool
	notionPreferNotion bool
	notionCreateOnly   bool
	notionSyncState    string
)

var newNotionStatusClient = notion.NewClient
var newNotionSetupClient = notion.NewClient

var notionCmd = &cobra.Command{
	Use:   "notion",
	Short: "Notion integration commands",
	Long:  "Commands for syncing issues between beads and Notion.",
}

var notionStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Notion sync status",
	RunE:  runNotionStatus,
}

var notionInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a dedicated Beads database in Notion",
	RunE:  runNotionInit,
}

var notionConnectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Connect bd to an existing Notion database or data source",
	RunE:  runNotionConnect,
}

var notionSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync issues with Notion",
	Long: "Synchronize issues between beads and Notion.\n\n" +
		"By default this performs bidirectional sync. Use --pull or --push to limit direction.",
	RunE: runNotionSync,
}

func init() {
	notionInitCmd.Flags().StringVar(&notionInitParent, "parent", "", "Parent page ID")
	notionInitCmd.Flags().StringVar(&notionInitTitle, "title", notion.DefaultDatabaseTitle, "Database title")
	_ = notionInitCmd.MarkFlagRequired("parent")

	notionConnectCmd.Flags().StringVar(&notionConnectURL, "url", "", "Existing Notion database or data source URL")
	_ = notionConnectCmd.MarkFlagRequired("url")

	notionSyncCmd.Flags().BoolVar(&notionSyncPull, "pull", false, "Only pull issues from Notion")
	notionSyncCmd.Flags().BoolVar(&notionSyncPush, "push", false, "Only push issues to Notion")
	notionSyncCmd.Flags().BoolVar(&notionSyncDryRun, "dry-run", false, "Preview changes without making mutations")
	notionSyncCmd.Flags().BoolVar(&notionPreferLocal, "prefer-local", false, "On conflict, keep the local beads version")
	notionSyncCmd.Flags().BoolVar(&notionPreferNotion, "prefer-notion", false, "On conflict, use the Notion version")
	notionSyncCmd.Flags().BoolVar(&notionCreateOnly, "create-only", false, "Only create missing remote pages, do not update existing ones")
	notionSyncCmd.Flags().StringVar(&notionSyncState, "state", "all", "Issue state to sync: open, closed, or all")

	notionCmd.AddCommand(notionInitCmd, notionConnectCmd, notionStatusCmd, notionSyncCmd)
	rootCmd.AddCommand(notionCmd)
}

func getNotionConfig() notionConfig {
	ctx := context.Background()
	return notionConfig{
		Token:        getNotionConfigValue(ctx, "notion.token", "NOTION_TOKEN"),
		DataSourceID: getNotionConfigValue(ctx, "notion.data_source_id", "NOTION_DATA_SOURCE_ID"),
		ViewURL:      getNotionConfigValue(ctx, "notion.view_url", "NOTION_VIEW_URL"),
	}
}

func getNotionConfigValue(ctx context.Context, key, envVar string) string {
	if store != nil {
		value, _ := store.GetConfig(ctx, key)
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	} else if dbPath != "" {
		tempStore, err := openReadOnlyStoreForDBPath(ctx, dbPath)
		if err == nil {
			defer func() { _ = tempStore.Close() }()
			value, _ := tempStore.GetConfig(ctx, key)
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	if envVar != "" {
		return strings.TrimSpace(os.Getenv(envVar))
	}
	return ""
}

func validateNotionConfig(cfg notionConfig) error {
	if cfg.Token == "" {
		return fmt.Errorf("notion.token is not configured. Set via bd config set notion.token <token> or NOTION_TOKEN")
	}
	if cfg.DataSourceID == "" {
		return fmt.Errorf("notion.data_source_id is not configured. Run 'bd notion init --parent <page-id>' or 'bd notion connect --url <notion-url>', or set it directly via bd config set notion.data_source_id <id> or NOTION_DATA_SOURCE_ID")
	}
	return nil
}

func validateNotionToken(cfg notionConfig) error {
	if cfg.Token == "" {
		return fmt.Errorf("notion.token is not configured. Set via bd config set notion.token <token> or NOTION_TOKEN")
	}
	return nil
}

func maskNotionToken(token string) string {
	if token == "" {
		return "(not set)"
	}
	if len(token) <= 4 {
		return "****"
	}
	return token[:4] + "****"
}

func runNotionStatus(cmd *cobra.Command, _ []string) error {
	cfg := getNotionConfig()
	result := notion.StatusResponse{
		Configured:   cfg.Token != "" && cfg.DataSourceID != "",
		DataSourceID: cfg.DataSourceID,
		ViewURL:      cfg.ViewURL,
		Auth:         &notion.StatusAuth{OK: cfg.Token != ""},
	}
	if !result.Configured {
		if err := validateNotionConfig(cfg); err != nil {
			result.Error = err.Error()
		}
		if jsonOutput {
			return writeNotionJSON(cmd, result)
		}
		return renderNotionStatus(cmd, cfg, &result)
	}

	client := newNotionStatusClient(cfg.Token)
	ctx := cmd.Context()
	user, err := client.GetCurrentUser(ctx)
	if err != nil {
		result.Error = err.Error()
		result.Auth = &notion.StatusAuth{OK: false}
	} else {
		result.Auth = &notion.StatusAuth{
			OK: true,
			User: &notion.StatusUser{
				ID:    user.ID,
				Name:  user.Name,
				Type:  user.Type,
				Email: userEmail(user),
			},
		}
	}

	dataSource, dsErr := client.RetrieveDataSource(ctx, cfg.DataSourceID)
	if dsErr != nil {
		if result.Error == "" {
			result.Error = dsErr.Error()
		}
	} else {
		result.Database = &notion.StatusDatabase{
			ID:    dataSource.ID,
			Title: notion.DataSourceTitle(dataSource.Title),
			URL:   dataSource.URL,
		}
		result.Schema = notion.ValidateDataSourceSchema(dataSource)
		result.Ready = result.Auth != nil && result.Auth.OK && len(result.Schema.Missing) == 0
	}

	if jsonOutput {
		return writeNotionJSON(cmd, result)
	}
	return renderNotionStatus(cmd, cfg, &result)
}

func runNotionInit(cmd *cobra.Command, _ []string) error {
	CheckReadonly("notion init")
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}
	cfg := getNotionConfig()
	if err := validateNotionToken(cfg); err != nil {
		return err
	}

	client := newNotionSetupClient(cfg.Token)
	db, err := client.CreateDatabase(cmd.Context(), notionInitParent, notionInitTitle)
	if err != nil {
		return err
	}
	if len(db.DataSources) == 0 || strings.TrimSpace(db.DataSources[0].ID) == "" {
		return fmt.Errorf("Notion create database response did not include a child data source")
	}
	result := notionSetupResult{
		Action:       "init",
		DatabaseID:   strings.TrimSpace(db.ID),
		DataSourceID: strings.TrimSpace(db.DataSources[0].ID),
		ViewURL:      strings.TrimSpace(db.URL),
		Message:      "Notion target initialized",
	}
	if err := saveNotionTargetConfig(cmd.Context(), result.DataSourceID, result.ViewURL); err != nil {
		return err
	}
	if jsonOutput {
		return writeNotionJSON(cmd, result)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ Created Notion database %s\n", firstNonEmpty(result.DatabaseID, "(unknown)"))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Saved data source: %s\n", result.DataSourceID)
	if result.ViewURL != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Launch URL: %s\n", result.ViewURL)
	}
	return nil
}

func runNotionConnect(cmd *cobra.Command, _ []string) error {
	CheckReadonly("notion connect")
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}
	cfg := getNotionConfig()
	if err := validateNotionToken(cfg); err != nil {
		return err
	}

	client := newNotionSetupClient(cfg.Token)
	resolved, err := notion.ResolveDataSourceReference(cmd.Context(), client, notionConnectURL)
	if err != nil {
		return err
	}
	schema := notion.ValidateDataSourceSchema(resolved.DataSource)
	if len(schema.Missing) > 0 {
		return fmt.Errorf("target is missing required Notion properties: %s", strings.Join(schema.Missing, ", "))
	}
	result := notionSetupResult{
		Action:       "connect",
		DatabaseID:   "",
		DataSourceID: resolved.DataSourceID,
		ViewURL:      strings.TrimSpace(notionConnectURL),
		Message:      "Notion target connected",
	}
	if resolved.Database != nil {
		result.DatabaseID = strings.TrimSpace(resolved.Database.ID)
	}
	if err := saveNotionTargetConfig(cmd.Context(), result.DataSourceID, result.ViewURL); err != nil {
		return err
	}
	if jsonOutput {
		return writeNotionJSON(cmd, result)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ Connected Notion data source %s\n", result.DataSourceID)
	if result.ViewURL != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Launch URL: %s\n", result.ViewURL)
	}
	return nil
}

func renderNotionStatus(cmd *cobra.Command, cfg notionConfig, result *notion.StatusResponse) error {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "Notion Configuration")
	_, _ = fmt.Fprintln(out, "====================")
	_, _ = fmt.Fprintf(out, "Token:       %s\n", maskNotionToken(cfg.Token))
	_, _ = fmt.Fprintf(out, "Data source: %s\n", cfg.DataSourceID)
	if cfg.ViewURL != "" {
		_, _ = fmt.Fprintf(out, "View URL:    %s\n", cfg.ViewURL)
	}
	if result.Database != nil {
		_, _ = fmt.Fprintf(out, "Database:    %s\n", result.Database.Title)
	}

	statusLine := "○ Not configured"
	switch {
	case result.Ready:
		statusLine = "✓ Ready"
	case result.Configured:
		statusLine = "◐ Not ready"
	}
	_, _ = fmt.Fprintf(out, "\nStatus: %s\n", statusLine)
	if result.Error != "" {
		_, _ = fmt.Fprintf(out, "Error: %s\n", result.Error)
	}
	if result.Schema != nil {
		if len(result.Schema.Missing) == 0 {
			_, _ = fmt.Fprintln(out, "Schema: ✓ Required properties present")
		} else {
			_, _ = fmt.Fprintf(out, "Schema: missing %s\n", strings.Join(result.Schema.Missing, ", "))
		}
	}
	return nil
}

func runNotionSync(cmd *cobra.Command, _ []string) error {
	cfg := getNotionConfig()
	if err := validateNotionConfig(cfg); err != nil {
		return err
	}
	if !notionSyncDryRun {
		CheckReadonly("notion sync")
	}
	if notionPreferLocal && notionPreferNotion {
		return fmt.Errorf("cannot use both --prefer-local and --prefer-notion")
	}
	if notionSyncPull && notionSyncPush {
		return fmt.Errorf("cannot use both --pull and --push")
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
	if jsonOutput {
		engine.OnMessage = func(msg string) { _, _ = fmt.Fprintln(cmd.ErrOrStderr(), "  "+msg) }
	} else {
		engine.OnMessage = func(msg string) { _, _ = fmt.Fprintln(cmd.OutOrStdout(), "  "+msg) }
	}
	engine.OnWarning = func(msg string) { _, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", msg) }

	pull := true
	push := true
	if notionSyncPull {
		push = false
	}
	if notionSyncPush {
		pull = false
	}

	conflictResolution := tracker.ConflictTimestamp
	if notionPreferLocal {
		conflictResolution = tracker.ConflictLocal
	}
	if notionPreferNotion {
		conflictResolution = tracker.ConflictExternal
	}

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Pull:               pull,
		Push:               push,
		DryRun:             notionSyncDryRun,
		CreateOnly:         notionCreateOnly,
		State:              notionSyncState,
		ConflictResolution: conflictResolution,
	})
	if err != nil {
		return err
	}

	if jsonOutput {
		return writeNotionJSON(cmd, result)
	}
	return renderNotionSyncResult(cmd, result)
}

func renderNotionSyncResult(cmd *cobra.Command, result *tracker.SyncResult) error {
	out := cmd.OutOrStdout()
	if notionSyncDryRun {
		_, _ = fmt.Fprintln(out, "Dry run mode")
	}
	if result.PullStats.Created > 0 || result.PullStats.Updated > 0 {
		_, _ = fmt.Fprintf(out, "✓ Pulled %d issues (%d created, %d updated)\n",
			result.Stats.Pulled, result.PullStats.Created, result.PullStats.Updated)
	}
	if result.PushStats.Created > 0 || result.PushStats.Updated > 0 {
		_, _ = fmt.Fprintf(out, "✓ Pushed %d issues (%d created, %d updated)\n",
			result.Stats.Pushed, result.PushStats.Created, result.PushStats.Updated)
	}
	if result.Stats.Conflicts > 0 {
		_, _ = fmt.Fprintf(out, "◐ Resolved %d conflicts\n", result.Stats.Conflicts)
	}
	if notionSyncDryRun {
		_, _ = fmt.Fprintln(out, "Run without --dry-run to apply changes")
	}
	return nil
}

func buildNotionPullHooks(ctx context.Context) *tracker.PullHooks {
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

func saveNotionTargetConfig(ctx context.Context, dataSourceID, viewURL string) error {
	if store == nil {
		return fmt.Errorf("database not available")
	}
	if err := store.SetConfig(ctx, "notion.data_source_id", strings.TrimSpace(dataSourceID)); err != nil {
		return fmt.Errorf("save notion.data_source_id: %w", err)
	}
	viewURL = strings.TrimSpace(viewURL)
	if viewURL == "" {
		if err := store.DeleteConfig(ctx, "notion.view_url"); err != nil {
			return fmt.Errorf("clear notion.view_url: %w", err)
		}
		return nil
	}
	if err := store.SetConfig(ctx, "notion.view_url", viewURL); err != nil {
		return fmt.Errorf("save notion.view_url: %w", err)
	}
	return nil
}

func writeNotionJSON(cmd *cobra.Command, value interface{}) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func userEmail(user *notion.User) string {
	if user == nil || user.Person == nil {
		return ""
	}
	return user.Person.Email
}
