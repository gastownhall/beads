package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/notion"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type notionConfig struct {
	DataSourceID string
	ViewURL      string
}

type notionUnsupportedPushStats struct {
	counts map[types.IssueType]int
}

func newNotionUnsupportedPushStats() *notionUnsupportedPushStats {
	return &notionUnsupportedPushStats{counts: make(map[types.IssueType]int)}
}

func (s *notionUnsupportedPushStats) record(issueType types.IssueType) {
	if s == nil || strings.TrimSpace(string(issueType)) == "" {
		return
	}
	s.counts[issueType]++
}

func (s *notionUnsupportedPushStats) warningText() string {
	if s == nil || len(s.counts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.counts))
	for issueType, count := range s.counts {
		parts = append(parts, fmt.Sprintf("%s=%d", issueType, count))
	}
	sort.Strings(parts)
	return fmt.Sprintf(
		"Skipped unsupported Notion issue types: %s (supported: bug, feature, task, epic, chore)",
		strings.Join(parts, ", "),
	)
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
	registerSelectiveSyncFlags(notionSyncCmd)

	notionCmd.AddCommand(
		notionInitCmd,
		notionConnectCmd,
		notionStatusCmd,
		notionSyncCmd,
	)
	rootCmd.AddCommand(notionCmd)
}

func getNotionConfig() notionConfig {
	ctx := context.Background()
	return notionConfig{
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

func resolveNotionAuth(ctx context.Context) (*notion.ResolvedAuth, error) {
	if store != nil {
		return notion.ResolveAuth(ctx, store)
	}
	if dbPath != "" {
		tempStore, err := openReadOnlyStoreForDBPath(ctx, dbPath)
		if err == nil {
			defer func() { _ = tempStore.Close() }()
			return notion.ResolveAuth(ctx, tempStore)
		}
	}
	if token := strings.TrimSpace(os.Getenv("NOTION_TOKEN")); token != "" {
		return &notion.ResolvedAuth{Token: token, Source: notion.AuthSourceEnv}, nil
	}
	return nil, nil
}

func validateNotionConfig(cfg notionConfig, auth *notion.ResolvedAuth) error {
	if auth == nil || strings.TrimSpace(auth.Token) == "" {
		return fmt.Errorf("Notion authentication is not configured. Set notion.token with 'bd config set notion.token <token>', or export NOTION_TOKEN")
	}
	if cfg.DataSourceID == "" {
		return fmt.Errorf("notion.data_source_id is not configured. Run 'bd notion init --parent <page-id>' or 'bd notion connect --url <notion-url>', or set it directly via bd config set notion.data_source_id <id> or NOTION_DATA_SOURCE_ID")
	}
	return nil
}

func validateNotionToken(auth *notion.ResolvedAuth) error {
	if auth == nil || strings.TrimSpace(auth.Token) == "" {
		return fmt.Errorf("Notion authentication is not configured. Set notion.token with 'bd config set notion.token <token>', or export NOTION_TOKEN")
	}
	return nil
}

func maskNotionAuth(auth *notion.ResolvedAuth) string {
	if auth == nil || strings.TrimSpace(auth.Token) == "" {
		return "(not set)"
	}
	token := auth.Token
	if len(token) <= 4 {
		return "****"
	}
	return token[:4] + "****"
}

func runNotionStatus(cmd *cobra.Command, _ []string) error {
	cfg := getNotionConfig()
	auth, err := resolveNotionAuth(cmd.Context())
	if err != nil {
		return err
	}
	result := notion.StatusResponse{
		Configured:   auth != nil && strings.TrimSpace(auth.Token) != "" && cfg.DataSourceID != "",
		DataSourceID: cfg.DataSourceID,
		ViewURL:      cfg.ViewURL,
	}
	if auth != nil && strings.TrimSpace(auth.Token) != "" {
		result.Auth = &notion.StatusAuth{OK: true, Source: string(auth.Source)}
	} else {
		result.Auth = &notion.StatusAuth{OK: false}
	}
	if !result.Configured {
		if err := validateNotionConfig(cfg, auth); err != nil {
			result.Error = err.Error()
		}
		if jsonOutput {
			return writeNotionJSON(cmd, result)
		}
		return renderNotionStatus(cmd, auth, cfg, &result)
	}

	client := newNotionStatusClient(auth.Token)
	ctx := cmd.Context()
	user, err := client.GetCurrentUser(ctx)
	if err != nil {
		result.Error = err.Error()
		result.Auth = &notion.StatusAuth{OK: false, Source: string(auth.Source)}
	} else {
		result.Auth = &notion.StatusAuth{
			OK:     true,
			Source: string(auth.Source),
			User:   statusUserFromNotionUser(user),
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
	return renderNotionStatus(cmd, auth, cfg, &result)
}

func runNotionInit(cmd *cobra.Command, _ []string) error {
	CheckReadonly("notion init")
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}
	auth, err := resolveNotionAuth(cmd.Context())
	if err != nil {
		return err
	}
	if err := validateNotionToken(auth); err != nil {
		return err
	}

	client := newNotionSetupClient(auth.Token)
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
	auth, err := resolveNotionAuth(cmd.Context())
	if err != nil {
		return err
	}
	if err := validateNotionToken(auth); err != nil {
		return err
	}

	client := newNotionSetupClient(auth.Token)
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

func renderNotionStatus(cmd *cobra.Command, auth *notion.ResolvedAuth, cfg notionConfig, result *notion.StatusResponse) error {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "Notion Configuration")
	_, _ = fmt.Fprintln(out, "====================")
	_, _ = fmt.Fprintf(out, "Auth:        %s\n", maskNotionAuth(auth))
	if auth != nil && auth.Source != "" {
		_, _ = fmt.Fprintf(out, "Auth source: %s\n", auth.Source)
	}
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
	auth, err := resolveNotionAuth(cmd.Context())
	if err != nil {
		return err
	}
	if err := validateNotionConfig(cfg, auth); err != nil {
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
	unsupportedStats := newNotionUnsupportedPushStats()
	engine.PushHooks = buildNotionPushHooks(ctx, nt, unsupportedStats)
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

	syncOpts := tracker.SyncOptions{
		Pull:               pull,
		Push:               push,
		DryRun:             notionSyncDryRun,
		CreateOnly:         notionCreateOnly,
		State:              notionSyncState,
		ExcludeEphemeral:   true,
		ConflictResolution: conflictResolution,
	}

	if err := applySelectiveSyncFlags(cmd, &syncOpts, push); err != nil {
		return err
	}

	result, err := engine.Sync(ctx, syncOpts)
	if err != nil {
		return err
	}
	if warning := unsupportedStats.warningText(); warning != "" {
		result.Warnings = append(result.Warnings, warning)
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
	if result.PullStats.Queried > 0 || result.PullStats.Candidates > 0 {
		_, _ = fmt.Fprintf(out, "Queried %d pages from Notion (%d pull candidates)\n",
			result.PullStats.Queried, result.PullStats.Candidates)
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
	for _, line := range summarizeNotionSyncWarnings(result.Warnings) {
		_, _ = fmt.Fprintln(out, line)
	}
	if notionSyncDryRun {
		_, _ = fmt.Fprintln(out, "Run without --dry-run to apply changes")
	}
	return nil
}

func summarizeNotionSyncWarnings(warnings []string) []string {
	staleTargetCount := 0
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		switch {
		case warning == "":
			continue
		case strings.HasPrefix(warning, "Skipped unsupported Notion issue types:"):
			continue
		case strings.Contains(warning, "outside the current target"):
			staleTargetCount++
		}
	}
	if staleTargetCount == 0 {
		return nil
	}
	return []string{
		fmt.Sprintf("Skipped %d linked issues that still point at a different Notion target. Clear external_ref to recreate them in this data source.", staleTargetCount),
	}
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

func buildNotionPushHooks(ctx context.Context, tr tracker.IssueTracker, stats *notionUnsupportedPushStats) *tracker.PushHooks {
	return &tracker.PushHooks{
		ShouldPush: func(issue *types.Issue) bool {
			if issue == nil || tr == nil {
				return false
			}
			if notion.SupportsIssueType(issue.IssueType, nil) {
				pushPrefix, _ := store.GetConfig(ctx, "notion.push_prefix")
				pushLabel, _ := store.GetConfig(ctx, "notion.push_label")
				return shouldPushNotionIssue(issue, tr, pushPrefix, pushLabel)
			}
			stats.record(issue.IssueType)
			return false
		},
	}
}

func shouldPushNotionIssue(issue *types.Issue, tr tracker.IssueTracker, pushPrefix, pushLabel string) bool {
	if issue == nil || tr == nil {
		return false
	}

	if issue.ExternalRef != nil && strings.TrimSpace(*issue.ExternalRef) != "" {
		return tr.IsExternalRef(*issue.ExternalRef)
	}

	if strings.TrimSpace(pushLabel) != "" && !matchesNotionPushLabel(issue, pushLabel) {
		return false
	}

	if strings.TrimSpace(pushPrefix) == "" {
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
}

func matchesNotionPushLabel(issue *types.Issue, pushLabel string) bool {
	if issue == nil || strings.TrimSpace(pushLabel) == "" {
		return false
	}

	configured := make(map[string]struct{})
	for _, raw := range strings.Split(pushLabel, ",") {
		label := strings.ToLower(strings.TrimSpace(raw))
		if label != "" {
			configured[label] = struct{}{}
		}
	}
	if len(configured) == 0 {
		return false
	}

	for _, raw := range issue.Labels {
		label := strings.ToLower(strings.TrimSpace(raw))
		if _, ok := configured[label]; ok {
			return true
		}
	}

	return false
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
		deleter, ok := storage.UnwrapStore(store).(storage.ConfigMetadataStore)
		if !ok {
			return fmt.Errorf("store does not support config deletion")
		}
		if err := deleter.DeleteConfig(ctx, "notion.view_url"); err != nil {
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

func statusUserFromNotionUser(user *notion.User) *notion.StatusUser {
	if user == nil {
		return nil
	}
	return &notion.StatusUser{
		ID:    user.ID,
		Name:  user.Name,
		Type:  user.Type,
		Email: userEmail(user),
	}
}

func userEmail(user *notion.User) string {
	if user == nil || user.Person == nil {
		return ""
	}
	return user.Person.Email
}
