package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/notion/mcpclient"
	"github.com/steveyegge/beads/internal/notion/output"
	"github.com/steveyegge/beads/internal/notion/state"
	"github.com/steveyegge/beads/internal/notion/wire"
)

type Service struct {
	io        *output.IO
	authStore *state.AuthStore
	paths     *state.Paths
	connector sessionConnector
}

type sessionConnector interface {
	Connect(ctx context.Context) (mcpclient.Session, error)
}

type serviceStatusResponse struct {
	Ready         bool                    `json:"ready"`
	DataSourceID  string                  `json:"data_source_id,omitempty"`
	ViewURL       string                  `json:"view_url,omitempty"`
	SchemaVersion string                  `json:"schema_version,omitempty"`
	Configured    bool                    `json:"configured,omitempty"`
	SavedConfig   bool                    `json:"saved_config_present,omitempty"`
	ConfigSource  string                  `json:"config_source,omitempty"`
	Auth          *serviceStatusAuth      `json:"auth,omitempty"`
	Database      *serviceStatusDatabase  `json:"database,omitempty"`
	Views         []wire.ViewInfo         `json:"views,omitempty"`
	Schema        *wire.SchemaStatus      `json:"schema,omitempty"`
	Archive       *wire.ArchiveCapability `json:"archive,omitempty"`
	State         *serviceStatusState     `json:"state,omitempty"`
}

type serviceStatusAuth struct {
	OK   bool           `json:"ok"`
	User *wire.AuthUser `json:"user,omitempty"`
}

type serviceStatusDatabase struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

type serviceStatusState struct {
	Path           string              `json:"path,omitempty"`
	Present        bool                `json:"present,omitempty"`
	ManagedCount   int                 `json:"managed_count,omitempty"`
	ViewConfigured bool                `json:"view_configured,omitempty"`
	DoctorSummary  *wire.DoctorSummary `json:"doctor_summary,omitempty"`
}

type servicePullResponse struct {
	Issues  []wire.Issue            `json:"issues"`
	Archive *wire.ArchiveCapability `json:"archive,omitempty"`
	State   *serviceStatusState     `json:"state,omitempty"`
}

type servicePushResponse struct {
	DryRun               bool                     `json:"dry_run"`
	ArchiveRequested     bool                     `json:"archive_requested,omitempty"`
	ArchiveSupported     bool                     `json:"archive_supported,omitempty"`
	ArchiveReason        string                   `json:"archive_reason,omitempty"`
	InputCount           int                      `json:"input_count"`
	CreatedCount         int                      `json:"created_count"`
	UpdatedCount         int                      `json:"updated_count"`
	SkippedCount         int                      `json:"skipped_count"`
	ArchivedCount        int                      `json:"archived_count,omitempty"`
	BodyUpdatedCount     int                      `json:"body_updated_count,omitempty"`
	CommentsCreatedCount int                      `json:"comments_created_count,omitempty"`
	Errors               []servicePushResultError `json:"errors,omitempty"`
	Warnings             []string                 `json:"warnings,omitempty"`
	Created              []servicePushResultItem  `json:"created,omitempty"`
	Updated              []servicePushResultItem  `json:"updated,omitempty"`
	Skipped              []servicePushResultItem  `json:"skipped,omitempty"`
	Archived             []servicePushResultItem  `json:"archived,omitempty"`
	BodyUpdated          []servicePushResultItem  `json:"body_updated,omitempty"`
	CommentsCreated      []servicePushResultItem  `json:"comments_created,omitempty"`
}

type servicePushResultItem struct {
	ID           string `json:"id,omitempty"`
	Title        string `json:"title,omitempty"`
	ExternalRef  string `json:"external_ref,omitempty"`
	NotionPageID string `json:"notion_page_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type servicePushResultError struct {
	ID      string `json:"id,omitempty"`
	Stage   string `json:"stage,omitempty"`
	Message string `json:"message,omitempty"`
}

type pushUpdateTarget struct {
	PageID string
	Issue  wire.PushIssue
}

const (
	managedIssueFetchWorkerCount = 6
	managedPageUpdateWorkerCount = 6
)

func NewService(io *output.IO, authStore *state.AuthStore) *Service {
	return &Service{
		io:        io,
		authStore: authStore,
		paths:     authStore.Paths(),
		connector: mcpclient.New(authStore),
	}
}

func NewCommand(io *output.IO, authStore *state.AuthStore) *cobra.Command {
	svc := NewService(io, authStore)

	var statusDatabaseID string
	var statusViewURL string
	var initParent string
	var initTitle string
	var connectDatabaseID string
	var connectViewURL string
	var exportOutput string
	var importInput string
	var pushInput string
	var pushDatabaseID string
	var pushViewURL string
	var pushDryRun bool
	var pushArchiveMissing bool
	var pullCacheMaxAge time.Duration
	var pushCacheMaxAge time.Duration

	beadsCmd := &cobra.Command{
		Use:   "beads",
		Short: "Beads-oriented Notion workflows",
	}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Create a dedicated Beads database and default table view",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return svc.Init(cmd.Context(), initParent, initTitle)
		},
	}
	initCmd.Flags().StringVar(&initParent, "parent", "", "Parent page ID")
	initCmd.Flags().StringVar(&initTitle, "title", wire.DefaultDatabaseTitle, "Database title")
	_ = initCmd.MarkFlagRequired("parent")
	beadsCmd.AddCommand(initCmd)

	connectCmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect to an existing Beads database and view",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return svc.Connect(cmd.Context(), connectDatabaseID, connectViewURL)
		},
	}
	connectCmd.Flags().StringVar(&connectDatabaseID, "database-id", "", "Notion database ID")
	connectCmd.Flags().StringVar(&connectViewURL, "view-url", "", "Notion view URL")
	_ = connectCmd.MarkFlagRequired("database-id")
	_ = connectCmd.MarkFlagRequired("view-url")
	beadsCmd.AddCommand(connectCmd)

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Check database connectivity and beads schema readiness",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return svc.Status(cmd.Context(), statusDatabaseID, statusViewURL)
		},
	}
	statusCmd.Flags().StringVar(&statusDatabaseID, "database-id", "", "Notion database ID")
	statusCmd.Flags().StringVar(&statusViewURL, "view-url", "", "Notion view URL")
	beadsCmd.AddCommand(statusCmd)

	configCmd := &cobra.Command{Use: "config", Short: "Inspect or clear the saved Beads target"}
	configCmd.AddCommand(&cobra.Command{Use: "show", Short: "Show saved Beads configuration", RunE: func(cmd *cobra.Command, _ []string) error { return svc.ConfigShow() }})
	configCmd.AddCommand(&cobra.Command{Use: "clear", Short: "Clear saved Beads configuration", RunE: func(cmd *cobra.Command, _ []string) error { return svc.ConfigClear() }})
	beadsCmd.AddCommand(configCmd)

	stateCmd := &cobra.Command{Use: "state", Short: "Inspect and manage saved Beads page state"}
	stateCmd.AddCommand(&cobra.Command{Use: "show", Short: "Show saved Beads page state", RunE: func(cmd *cobra.Command, _ []string) error { return svc.StateShow() }})
	stateExportCmd := &cobra.Command{
		Use:   "export",
		Short: "Export saved Beads page state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return svc.StateExport(exportOutput)
		},
	}
	stateExportCmd.Flags().StringVar(&exportOutput, "output", "", "Export file path, or - for stdout")
	_ = stateExportCmd.MarkFlagRequired("output")
	stateCmd.AddCommand(stateExportCmd)
	stateImportCmd := &cobra.Command{
		Use:   "import",
		Short: "Import saved Beads page state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return svc.StateImport(importInput)
		},
	}
	stateImportCmd.Flags().StringVar(&importInput, "input", "", "Import file path, or - for stdin")
	_ = stateImportCmd.MarkFlagRequired("input")
	stateCmd.AddCommand(stateImportCmd)
	stateCmd.AddCommand(&cobra.Command{Use: "doctor", Short: "Diagnose saved Beads state against live Notion pages", RunE: func(cmd *cobra.Command, _ []string) error { return svc.StateDoctor(cmd.Context()) }})
	beadsCmd.AddCommand(stateCmd)

	pullCmd := &cobra.Command{
		Use:   "pull",
		Short: "Pull locally managed pages",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return svc.Pull(cmd.Context(), pullCacheMaxAge)
		},
	}
	pullCmd.Flags().DurationVar(&pullCacheMaxAge, "cache-max-age", 0, "Reuse cached managed-page snapshots younger than this duration")
	beadsCmd.AddCommand(pullCmd)
	pushCmd := &cobra.Command{
		Use:   "push",
		Short: "Push issue JSON to Notion",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return svc.Push(cmd.Context(), pushInput, pushDatabaseID, pushViewURL, pushDryRun, pushArchiveMissing, pushCacheMaxAge)
		},
	}
	pushCmd.Flags().StringVar(&pushInput, "input", "", "Push input file path, or - for stdin")
	pushCmd.Flags().StringVar(&pushDatabaseID, "database-id", "", "Override the target Notion database ID")
	pushCmd.Flags().StringVar(&pushViewURL, "view-url", "", "Override the target Notion view URL")
	pushCmd.Flags().BoolVar(&pushDryRun, "dry-run", false, "Plan the push without mutating Notion")
	pushCmd.Flags().BoolVar(&pushArchiveMissing, "archive-missing", false, "Plan archiving for managed pages missing from input")
	pushCmd.Flags().DurationVar(&pushCacheMaxAge, "cache-max-age", 0, "Reuse cached managed-page snapshots younger than this duration")
	_ = pushCmd.MarkFlagRequired("input")
	beadsCmd.AddCommand(pushCmd)

	return beadsCmd
}

func (s *Service) Init(ctx context.Context, parentID, title string) error {
	session, err := s.requireSession(ctx, "beads init")
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	if title == "" {
		title = wire.DefaultDatabaseTitle
	}
	schemaSQL := "CREATE TABLE \"" + title + "\" (\"Name\" TITLE, \"Beads ID\" RICH_TEXT, \"Status\" SELECT('Open','In Progress','Blocked','Deferred','Closed'), \"Priority\" SELECT('Critical','High','Medium','Low','Backlog'), \"Type\" SELECT('Bug','Feature','Task','Epic','Chore'), \"Description\" RICH_TEXT, \"Assignee\" RICH_TEXT, \"Labels\" MULTI_SELECT)"
	createDB, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "notion-create-database",
		Arguments: map[string]any{
			"title":  title,
			"schema": schemaSQL,
			"parent": map[string]any{"page_id": parentID, "type": "page_id"},
		},
	})
	if err != nil {
		return output.Wrap(err, "failed to create beads database")
	}
	dbText, err := wire.ResultText(createDB, "beads init database")
	if err != nil {
		return output.Wrap(err, "failed to decode create-database result")
	}
	info := wire.ExtractBeadsDatabaseInfoFromText(dbText)
	if info.DatabaseID == "" || info.DataSourceID == "" {
		return output.NewError("Invalid database creation response", "database_id or data_source_id is missing", "Retry with a parent page that allows database creation", 1)
	}

	createView, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "notion-create-view",
		Arguments: map[string]any{
			"database_id":    info.DatabaseID,
			"data_source_id": "collection://" + info.DataSourceID,
			"type":           "table",
			"name":           wire.DefaultViewName,
		},
	})
	if err != nil {
		return output.Wrap(err, "failed to create default view")
	}
	viewText, err := wire.ResultText(createView, "beads init view")
	if err != nil {
		return output.Wrap(err, "failed to decode create-view result")
	}
	viewURL := wire.ExtractViewURLFromText(viewText)
	if viewURL == "" {
		return output.NewError("Invalid view creation response", "view_url is missing", "Retry after confirming the created database exposes a table view", 1)
	}

	cfg := &state.BeadsConfig{
		DatabaseID:    info.DatabaseID,
		DataSourceID:  info.DataSourceID,
		ViewURL:       viewURL,
		SchemaVersion: state.BeadsSchemaVersion,
	}
	if err := s.activateTarget(cfg); err != nil {
		return err
	}
	return s.io.JSON(map[string]any{
		"database_id":    cfg.DatabaseID,
		"data_source_id": cfg.DataSourceID,
		"view_url":       cfg.ViewURL,
		"schema_version": cfg.SchemaVersion,
		"saved":          true,
	})
}

func (s *Service) Connect(ctx context.Context, databaseID, viewURL string) error {
	session, err := s.requireSession(ctx, "beads connect")
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	fetchResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "notion-fetch",
		Arguments: map[string]any{"id": databaseID},
	})
	if err != nil {
		return output.Wrap(err, "failed to fetch target database")
	}
	fetchText, err := wire.ResultText(fetchResult, "beads connect fetch")
	if err != nil {
		return output.Wrap(err, "failed to decode fetch response")
	}
	info := wire.ExtractBeadsDatabaseInfoFromText(fetchText)
	schema := wire.AssessSchema(wire.DetectBeadsPropertiesFromFetchText(fetchText), true)
	if len(schema.Missing) > 0 {
		return output.NewError("Invalid beads database schema", "the target database is missing required properties: "+strings.Join(schema.Missing, ", "), "Use the dedicated beads schema before running \"bd notion connect\"", 1)
	}
	if info.DataSourceID == "" {
		return output.NewError("Missing data source ID", "could not extract a data_source_id from the fetched database", "Retry with a database page created by Notion tools", 1)
	}
	foundView := false
	for _, view := range info.Views {
		if view.URL == viewURL {
			foundView = true
			break
		}
	}
	if !foundView {
		return output.NewError("Unknown beads view URL", "the provided view URL is not listed on the target database", "Run notion-fetch again and copy a view:// URL from the database metadata", 1)
	}

	cfg := &state.BeadsConfig{
		DatabaseID:    databaseID,
		DataSourceID:  info.DataSourceID,
		ViewURL:       viewURL,
		SchemaVersion: state.BeadsSchemaVersion,
	}
	if err := s.activateTarget(cfg); err != nil {
		return err
	}
	return s.io.JSON(map[string]any{
		"database_id":    cfg.DatabaseID,
		"data_source_id": cfg.DataSourceID,
		"view_url":       cfg.ViewURL,
		"schema_version": cfg.SchemaVersion,
		"saved":          true,
	})
}

func (s *Service) activateTarget(cfg *state.BeadsConfig) error {
	if err := state.SaveBeadsTarget(s.paths, cfg); err != nil {
		return output.Wrap(err, "failed to save beads target")
	}
	return nil
}

func (s *Service) ConfigShow() error {
	cfg, err := state.ReadBeadsConfig(s.paths)
	if err != nil {
		return output.Wrap(err, "failed to read beads config")
	}
	return s.io.JSON(map[string]any{
		"configured": cfg != nil,
		"path":       s.paths.BeadsConfigPath,
		"config":     cfg,
	})
}

func (s *Service) ConfigClear() error {
	existedCfg, err := state.ClearBeadsConfig(s.paths)
	if err != nil {
		return output.Wrap(err, "failed to clear beads config")
	}
	_, err = state.ClearBeadsState(s.paths)
	if err != nil {
		return output.Wrap(err, "failed to clear beads state")
	}
	return s.io.JSON(map[string]any{
		"cleared": true,
		"existed": existedCfg,
		"path":    s.paths.BeadsConfigPath,
	})
}

func (s *Service) StateShow() error {
	st, err := state.ReadBeadsState(s.paths)
	if err != nil {
		return output.Wrap(err, "failed to read beads state")
	}
	return s.io.JSON(map[string]any{
		"configured": st != nil,
		"path":       s.paths.BeadsStatePath,
		"state":      st,
	})
}

func (s *Service) StateExport(outputPath string) error {
	st, err := state.ReadBeadsState(s.paths)
	if err != nil {
		return output.Wrap(err, "failed to read beads state")
	}
	if st == nil {
		return output.NewError("Missing beads state", "beads state export needs a saved beads-state.json file", "Run beads init, connect, or state import first", 1)
	}
	if outputPath == "-" {
		return s.io.JSON(st)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return output.Wrap(err, "failed to encode beads state")
	}
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		return output.Wrap(err, "failed to write beads state export")
	}
	return s.io.JSON(map[string]any{
		"exported": true,
		"path":     outputPath,
		"state":    st,
	})
}

func (s *Service) StateImport(inputPath string) error {
	var reader io.Reader
	if inputPath == "-" {
		reader = os.Stdin
	} else {
		//nolint:gosec // State import intentionally reads the caller-provided file path.
		data, err := os.ReadFile(inputPath)
		if err != nil {
			return output.Wrap(err, "failed to read state import file")
		}
		reader = bytes.NewReader(data)
	}
	cfg, err := state.ReadBeadsConfig(s.paths)
	if err != nil {
		return output.Wrap(err, "failed to read beads config")
	}
	st, err := state.ImportBeadsState(s.paths, reader, cfg)
	if err != nil {
		return output.Wrap(err, "failed to import beads state")
	}
	return s.io.JSON(map[string]any{
		"imported": true,
		"path":     s.paths.BeadsStatePath,
		"state":    st,
	})
}

func (s *Service) StateDoctor(ctx context.Context) error {
	st, err := state.ReadBeadsState(s.paths)
	if err != nil {
		return output.Wrap(err, "failed to read beads state")
	}
	if st == nil {
		return output.NewError("Missing beads state", "beads state doctor needs a saved beads-state.json file", "Run beads init, connect, or state import first", 1)
	}
	session, err := s.requireSession(ctx, "beads state doctor")
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	entries := make([]wire.DoctorEntry, 0, len(st.PageIDs))
	keys := make([]string, 0, len(st.PageIDs))
	for id := range st.PageIDs {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	for _, beadsID := range keys {
		pageID := st.PageIDs[beadsID]
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "notion-fetch", Arguments: map[string]any{"id": pageID}})
		if err != nil {
			entries = append(entries, wire.DoctorEntry{
				BeadsID: beadsID,
				PageID:  pageID,
				Status:  "missing_page",
				Message: err.Error(),
			})
			continue
		}
		text, err := wire.ResultText(result, "beads state doctor page")
		if err != nil {
			entries = append(entries, wire.DoctorEntry{
				BeadsID: beadsID,
				PageID:  pageID,
				Status:  "property_mismatch",
				Message: err.Error(),
			})
			continue
		}
		actualBeadsID, notionPageID, title, err := wire.ExtractPageIdentityFromText(text)
		if err != nil {
			entries = append(entries, wire.DoctorEntry{
				BeadsID: beadsID,
				PageID:  pageID,
				Status:  "property_mismatch",
				Message: err.Error(),
			})
			continue
		}
		entries = append(entries, wire.BuildDoctorEntry(beadsID, pageID, actualBeadsID, notionPageID, title))
	}
	summary := wire.SummarizeDoctorEntries(entries)
	return s.io.JSON(map[string]any{
		"path":    s.paths.BeadsStatePath,
		"entries": entries,
		"summary": summary,
	})
}

func (s *Service) Pull(ctx context.Context, cacheMaxAge time.Duration) error {
	cfg, err := s.requireSavedConfig("pull")
	if err != nil {
		return err
	}
	st, err := s.loadStateForTarget(cfg.DatabaseID, "saved beads config")
	if err != nil {
		return err
	}

	session, err := s.requireSession(ctx, "beads pull")
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	archive := wire.ArchiveCapability{Supported: false, Mode: wire.ArchiveSupportModeNone}
	if tools, err := session.ListTools(ctx, nil); err == nil {
		archive = wire.DetectArchiveSupport(tools.Tools)
	}
	issues, fetchedIssues, err := s.pullManagedIssues(ctx, st, cacheMaxAge)
	if err != nil {
		return err
	}
	if len(fetchedIssues) > 0 {
		verifiedAt := time.Now().UTC().Format(time.RFC3339Nano)
		for _, issue := range fetchedIssues {
			s.cacheManagedIssue(st, issue, verifiedAt)
		}
		if err := state.SaveBeadsState(s.paths, st); err != nil {
			return output.Wrap(err, "failed to save beads state")
		}
	}

	return s.io.JSON(&servicePullResponse{
		Issues:  issues,
		Archive: &archive,
		State: &serviceStatusState{
			Path:           s.paths.BeadsStatePath,
			Present:        true,
			ManagedCount:   len(st.PageIDs),
			ViewConfigured: cfg.ViewURL != "",
			DoctorSummary:  buildStateSummary(st),
		},
	})
}

func (s *Service) pullManagedIssues(ctx context.Context, st *state.BeadsState, cacheMaxAge time.Duration) ([]wire.Issue, []wire.Issue, error) {
	ids := st.IDs()
	if len(ids) == 0 {
		return []wire.Issue{}, nil, nil
	}

	type fetchTask struct {
		index   int
		beadsID string
		pageID  string
	}

	tasksToFetch := make([]fetchTask, 0, len(ids))
	issues := make([]wire.Issue, len(ids))
	for index, id := range ids {
		if cached, ok := cachedManagedIssueForPull(st, id, cacheMaxAge); ok {
			issues[index] = cached
			continue
		}
		tasksToFetch = append(tasksToFetch, fetchTask{index: index, beadsID: id, pageID: st.PageIDFor(id)})
	}
	if len(tasksToFetch) == 0 {
		return issues, nil, nil
	}
	tasks := make(chan fetchTask, len(tasksToFetch))
	for _, task := range tasksToFetch {
		tasks <- task
	}
	close(tasks)

	workerCount := managedIssueFetchWorkerCount
	if len(tasksToFetch) < workerCount {
		workerCount = len(tasksToFetch)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		firstErr error
		errOnce  sync.Once
		wg       sync.WaitGroup
		fetched  []wire.Issue
		fetchMu  sync.Mutex
	)
	setErr := func(err error) {
		if err == nil {
			return
		}
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, err := s.requireSession(ctx, "beads pull worker")
			if err != nil {
				setErr(err)
				return
			}
			defer func() { _ = session.Close() }()
			for task := range tasks {
				if ctx.Err() != nil {
					return
				}
				issue, err := s.fetchManagedIssue(ctx, session, task.beadsID, task.pageID)
				if err != nil {
					setErr(err)
					return
				}
				issues[task.index] = issue
				fetchMu.Lock()
				fetched = append(fetched, issue)
				fetchMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, nil, firstErr
	}
	return issues, fetched, nil
}

func (s *Service) cacheManagedIssue(st *state.BeadsState, issue wire.Issue, verifiedAt string) {
	if st == nil {
		return
	}
	pageID := serviceFirstNonEmpty(issue.NotionPageID, st.PageIDFor(issue.ID))
	st.SetEntry(issue.ID, state.BeadsStateEntry{
		PageID:         pageID,
		CachedIssue:    wire.SnapshotFromIssue(issue),
		LastVerifiedAt: verifiedAt,
	})
}

func (s *Service) Push(ctx context.Context, inputPath, databaseID, viewURL string, dryRun, archiveMissing bool, cacheMaxAge time.Duration) error {
	input, err := s.readPushInput(inputPath)
	if err != nil {
		return err
	}
	return s.pushIssueSet(ctx, input, databaseID, viewURL, dryRun, archiveMissing, cacheMaxAge)
}

func (s *Service) PushPayload(ctx context.Context, payload []byte, databaseID, viewURL string, dryRun, archiveMissing bool, cacheMaxAge time.Duration) error {
	input, err := s.parsePushInput(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	return s.pushIssueSet(ctx, input, databaseID, viewURL, dryRun, archiveMissing, cacheMaxAge)
}

func (s *Service) pushIssueSet(ctx context.Context, input wire.PushIssueSet, databaseID, viewURL string, dryRun, archiveMissing bool, cacheMaxAge time.Duration) error {
	duplicates := wire.FindDuplicateBeadsIDs(collectPushIDs(input.Issues))
	if len(duplicates) > 0 {
		return output.NewError("Duplicate Beads ID values in input", "input contains duplicate Beads ID values: "+strings.Join(duplicates, ", "), "Ensure each issue id appears only once before pushing", 1)
	}
	pushableIssues, unsupportedSkipped, warnings := filterUnsupportedPushIssues(input.Issues)

	resolvedDatabaseID, resolvedViewURL, err := s.resolvePushTarget(databaseID, viewURL)
	if err != nil {
		return err
	}
	st, err := s.loadStateForTarget(resolvedDatabaseID, "requested push target")
	if err != nil {
		return err
	}

	session, err := s.requireSession(ctx, "beads push")
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	archive := wire.ArchiveCapability{Supported: false, Mode: wire.ArchiveSupportModeNone}
	if err == nil {
		archive = wire.DetectArchiveSupport(tools.Tools)
	}

	databaseInfo, pushSchema, err := s.fetchPushDatabaseInfo(ctx, session, resolvedDatabaseID, resolvedViewURL)
	if err != nil {
		return err
	}

	existingByID := map[string]wire.Issue{}
	for _, issue := range input.ExistingIssues {
		if trimmedID := strings.TrimSpace(issue.ID); trimmedID != "" {
			existingByID[trimmedID] = normalizeManagedIssueFromState(issue, st)
		}
	}
	brokenStateIDs := map[string]servicePushResultError{}
	stateIDs := managedStateIDsForPush(st.PageIDs, pushableIssues, archiveMissing)
	for _, id := range stateIDs {
		if _, ok := existingByID[id]; ok {
			continue
		}
		if cached, ok := cachedManagedIssueForPush(st, id, cacheMaxAge); ok {
			existingByID[id] = cached
			continue
		}
		issue, err := s.fetchManagedIssue(ctx, session, id, st.PageIDs[id])
		if err != nil {
			brokenStateIDs[id] = servicePushResultError{ID: id, Stage: "state_fetch", Message: err.Error()}
			continue
		}
		existingByID[id] = issue
	}

	inputIDs := map[string]struct{}{}
	toCreate := make([]wire.PushIssue, 0)
	toUpdate := make([]pushUpdateTarget, 0)
	skipped := append([]servicePushResultItem(nil), unsupportedSkipped...)
	errors := make([]servicePushResultError, 0, len(brokenStateIDs))

	brokenKeys := make([]string, 0, len(brokenStateIDs))
	for id := range brokenStateIDs {
		brokenKeys = append(brokenKeys, id)
	}
	slices.Sort(brokenKeys)
	for _, id := range brokenKeys {
		errors = append(errors, brokenStateIDs[id])
	}

	for _, issue := range input.Issues {
		inputIDs[issue.ID] = struct{}{}
	}

	for _, issue := range pushableIssues {
		if broken, ok := brokenStateIDs[issue.ID]; ok {
			skipped = append(skipped, servicePushResultItem{ID: issue.ID, Title: issue.Title, NotionPageID: st.PageIDs[issue.ID], Reason: broken.Stage})
			continue
		}
		current, exists := existingByID[issue.ID]
		if !exists {
			toCreate = append(toCreate, issue)
			continue
		}
		pageID := serviceFirstNonEmpty(current.NotionPageID, st.PageIDs[issue.ID])
		if pushIssueMatches(current, issue) {
			skipped = append(skipped, servicePushResultItem{
				ID:           issue.ID,
				Title:        issue.Title,
				ExternalRef:  current.ExternalRef,
				NotionPageID: pageID,
				Reason:       "unchanged",
			})
			continue
		}
		toUpdate = append(toUpdate, pushUpdateTarget{PageID: pageID, Issue: issue})
	}

	archived := make([]servicePushResultItem, 0)
	if archiveMissing {
		for _, id := range stateIDs {
			if _, ok := inputIDs[id]; ok {
				continue
			}
			current := existingByID[id]
			archived = append(archived, servicePushResultItem{
				ID:           id,
				Title:        current.Title,
				ExternalRef:  current.ExternalRef,
				NotionPageID: serviceFirstNonEmpty(current.NotionPageID, st.PageIDs[id]),
				Reason:       "missing_from_input",
			})
		}
	}

	created := make([]servicePushResultItem, 0, len(toCreate))
	for _, issue := range toCreate {
		created = append(created, servicePushResultItem{ID: issue.ID, Title: issue.Title})
	}
	updated := make([]servicePushResultItem, 0, len(toUpdate))
	for _, item := range toUpdate {
		updated = append(updated, servicePushResultItem{
			ID:           item.Issue.ID,
			Title:        item.Issue.Title,
			NotionPageID: item.PageID,
			ExternalRef:  existingByID[item.Issue.ID].ExternalRef,
		})
	}

	response := &servicePushResponse{
		DryRun:               dryRun,
		ArchiveRequested:     archiveMissing,
		ArchiveSupported:     archive.Supported,
		ArchiveReason:        archive.Reason,
		InputCount:           len(input.Issues),
		CreatedCount:         len(toCreate),
		UpdatedCount:         len(toUpdate),
		SkippedCount:         len(skipped),
		ArchivedCount:        len(archived),
		BodyUpdatedCount:     0,
		CommentsCreatedCount: 0,
		Errors:               errors,
		Warnings:             warnings,
		Created:              created,
		Updated:              updated,
		Skipped:              skipped,
		Archived:             archived,
		BodyUpdated:          []servicePushResultItem{},
		CommentsCreated:      []servicePushResultItem{},
	}

	if dryRun {
		return s.io.JSON(response)
	}
	if len(archived) > 0 && !archive.Supported {
		return output.NewError("Archive-missing is unavailable on the current live Notion MCP", serviceFirstNonEmpty(archive.Reason, "the connected Notion MCP does not support archiving managed pages"), `Run "bd notion sync --push --dry-run" to inspect candidates, then archive or remove them manually in Notion`, 1)
	}

	created, err = s.createPagesForPush(ctx, session, st, databaseInfo.DataSourceID, toCreate, pushSchema)
	if err != nil {
		return err
	}
	response.Created = created
	if err := s.updatePagesForPush(ctx, toUpdate, pushSchema); err != nil {
		return err
	}
	if len(toUpdate) > 0 {
		for _, item := range toUpdate {
			st.SetPageID(item.Issue.ID, item.PageID)
		}
		if err := state.SaveBeadsState(s.paths, st); err != nil {
			return output.Wrap(err, "failed to save beads state")
		}
	}
	for _, item := range archived {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "notion-update-page",
			Arguments: map[string]any{
				"page_id": item.NotionPageID,
				"command": "archive",
			},
		})
		if err != nil {
			return output.Wrap(err, "failed to archive managed page "+item.ID)
		}
		st.Delete(item.ID)
	}
	if len(created) > 0 || len(archived) > 0 {
		if err := state.SaveBeadsState(s.paths, st); err != nil {
			return output.Wrap(err, "failed to save beads state")
		}
	}
	response.Created = created
	return s.io.JSON(response)
}

func (s *Service) readPushInput(inputPath string) (wire.PushIssueSet, error) {
	var reader io.Reader
	if inputPath == "-" {
		reader = os.Stdin
	} else {
		//nolint:gosec // Push input intentionally reads the caller-provided file path.
		data, err := os.ReadFile(inputPath)
		if err != nil {
			return wire.PushIssueSet{}, output.Wrap(err, "failed to read push input")
		}
		reader = bytes.NewReader(data)
	}
	return s.parsePushInput(reader)
}

func (s *Service) parsePushInput(reader io.Reader) (wire.PushIssueSet, error) {
	input, err := wire.ParsePushInput(reader)
	if err != nil {
		return wire.PushIssueSet{}, output.NewError("Invalid push input", err.Error(), "Pass a JSON array or an object with an issues array", 1)
	}
	return input, nil
}

func (s *Service) requireSavedConfig(command string) (*state.BeadsConfig, error) {
	cfg, err := state.ReadBeadsConfig(s.paths)
	if err != nil {
		return nil, output.Wrap(err, "failed to read beads config")
	}
	if cfg == nil {
		return nil, output.NewError("Missing beads database target", "beads "+command+" needs saved beads config with a database id", `Run "bd notion init --parent <page-id>" or "bd notion connect --database-id <id> --view-url <url>" first`, 1)
	}
	return cfg, nil
}

func (s *Service) loadStateForTarget(databaseID, targetLabel string) (*state.BeadsState, error) {
	st, err := state.ReadBeadsState(s.paths)
	if err != nil {
		return nil, output.Wrap(err, "failed to read beads state")
	}
	if st == nil {
		return state.EmptyBeadsState(databaseID), nil
	}
	if st.DatabaseID != databaseID {
		return nil, output.NewError("State database mismatch", "saved beads-state.json targets a different database than the "+targetLabel, `Run "bd notion connect --database-id <id> --view-url <url>" or "bd notion state import --input <path|->" for that database`, 1)
	}
	return st, nil
}

func (s *Service) resolvePushTarget(databaseID, viewURL string) (string, string, error) {
	if databaseID != "" || viewURL != "" {
		if databaseID == "" || viewURL == "" {
			return "", "", output.NewError("Incomplete push target override", "when overriding saved config, beads push requires both --database-id and --view-url", `Pass both flags, or configure defaults with "bd notion connect"`, 1)
		}
		return databaseID, viewURL, nil
	}
	cfg, err := s.requireSavedConfig("push")
	if err != nil {
		return "", "", err
	}
	return cfg.DatabaseID, cfg.ViewURL, nil
}

func managedStateIDsForPush(pageIDs map[string]string, input []wire.PushIssue, archiveMissing bool) []string {
	if len(pageIDs) == 0 {
		return nil
	}

	ids := make([]string, 0, len(pageIDs))
	if archiveMissing {
		for id := range pageIDs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return ids
	}

	seen := make(map[string]struct{}, len(input))
	for _, issue := range input {
		if _, ok := pageIDs[issue.ID]; !ok {
			continue
		}
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		seen[issue.ID] = struct{}{}
		ids = append(ids, issue.ID)
	}
	slices.Sort(ids)
	return ids
}

func (s *Service) fetchPushDatabaseInfo(ctx context.Context, session mcpclient.Session, databaseID, viewURL string) (wire.DatabaseInfo, wire.SchemaStatus, error) {
	fetchResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "notion-fetch", Arguments: map[string]any{"id": databaseID}})
	if err != nil {
		return wire.DatabaseInfo{}, wire.SchemaStatus{}, output.Wrap(err, "failed to fetch target database")
	}
	fetchText, err := wire.ResultText(fetchResult, "beads push fetch")
	if err != nil {
		return wire.DatabaseInfo{}, wire.SchemaStatus{}, output.Wrap(err, "failed to decode database fetch response")
	}
	info := wire.ExtractBeadsDatabaseInfoFromText(fetchText)
	schema := wire.AssessSchema(wire.DetectBeadsPropertiesFromFetchText(fetchText), true)
	if len(schema.Missing) > 0 {
		return wire.DatabaseInfo{}, wire.SchemaStatus{}, output.NewError("Invalid beads database schema", "the target database is missing required properties: "+strings.Join(schema.Missing, ", "), "Use the dedicated beads schema before pushing issues", 1)
	}
	if info.DataSourceID == "" {
		return wire.DatabaseInfo{}, wire.SchemaStatus{}, output.NewError("Missing data source ID", "could not extract a data_source_id from the fetched database", "Retry with a database page created by Notion tools", 1)
	}
	if viewURL != "" {
		found := false
		for _, view := range info.Views {
			if view.URL == viewURL {
				found = true
				break
			}
		}
		if !found {
			return wire.DatabaseInfo{}, wire.SchemaStatus{}, output.NewError("Unknown beads view URL", "the configured view URL is not listed on the target database", "Rerun notion-fetch and update the saved config if the view changed", 1)
		}
	}
	return info, schema, nil
}

func (s *Service) fetchManagedIssue(ctx context.Context, session mcpclient.Session, expectedBeadsID, pageID string) (wire.Issue, error) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "notion-fetch", Arguments: map[string]any{"id": pageID}})
	if err != nil {
		return wire.Issue{}, output.Wrap(err, "failed to fetch managed page "+expectedBeadsID)
	}
	text, err := wire.ResultText(result, "managed page fetch")
	if err != nil {
		return wire.Issue{}, output.Wrap(err, "failed to decode managed page fetch")
	}
	payload, err := wire.ResultJSONMap(result, "managed page fetch")
	if err != nil {
		return wire.Issue{}, output.Wrap(err, "failed to decode managed page payload")
	}
	issue, err := wire.NormalizePageFetchPayload(text, payload)
	if err != nil {
		return wire.Issue{}, output.Wrap(err, "failed to normalize managed page "+expectedBeadsID)
	}
	if issue.ID != expectedBeadsID {
		return wire.Issue{}, output.NewError("Managed page Beads ID mismatch", "saved state expects "+expectedBeadsID+" but the Notion page reports "+issue.ID, `Run "bd notion state doctor" to inspect the saved state`, 1)
	}
	issue.NotionPageID = serviceFirstNonEmpty(issue.NotionPageID, pageID)
	issue.ExternalRef = serviceFirstNonEmpty(issue.ExternalRef, "notion:"+issue.NotionPageID)
	return issue, nil
}

func (s *Service) createPagesForPush(ctx context.Context, session mcpclient.Session, st *state.BeadsState, dataSourceID string, issues []wire.PushIssue, schema wire.SchemaStatus) ([]servicePushResultItem, error) {
	if len(issues) == 0 {
		return []servicePushResultItem{}, nil
	}
	pages := make([]map[string]any, 0, len(issues))
	for _, issue := range issues {
		pages = append(pages, map[string]any{"properties": wire.FilterPropertiesBySchema(wire.BuildProperties(issue), schema)})
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "notion-create-pages",
		Arguments: map[string]any{
			"parent": map[string]any{"data_source_id": dataSourceID, "type": "data_source_id"},
			"pages":  pages,
		},
	})
	if err != nil {
		return nil, output.Wrap(err, "failed to create Notion pages")
	}
	payload, err := wire.ResultJSONMap(result, "beads push create")
	if err != nil {
		return nil, err
	}
	createdPages, _ := payload["pages"].([]any)
	items := make([]servicePushResultItem, 0, len(issues))
	stateChanged := false
	for index, issue := range issues {
		item := servicePushResultItem{ID: issue.ID, Title: issue.Title}
		if index < len(createdPages) {
			if page, ok := createdPages[index].(map[string]any); ok {
				item.NotionPageID = serviceFirstNonEmpty(stringValue(page["id"]), stringValue(page["page_id"]))
				item.ExternalRef = serviceFirstNonEmpty(stringValue(page["url"]), stringValue(page["external_ref"]))
			}
		}
		if item.NotionPageID != "" {
			st.SetPageID(issue.ID, item.NotionPageID)
			stateChanged = true
		} else {
			item.Reason = "missing_page_id"
		}
		if item.ExternalRef == "" && item.NotionPageID != "" {
			item.ExternalRef = "notion:" + item.NotionPageID
		}
		items = append(items, item)
	}
	if stateChanged {
		if err := state.SaveBeadsState(s.paths, st); err != nil {
			return nil, output.Wrap(err, "failed to save beads state")
		}
	}
	return items, nil
}

func (s *Service) updatePagesForPush(ctx context.Context, items []pushUpdateTarget, schema wire.SchemaStatus) error {
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if item.PageID == "" {
			return output.NewError("Invalid target row", "managed issue "+item.Issue.ID+" does not expose a Notion page id", `Run "bd notion state doctor" to inspect the saved state`, 1)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tasks := make(chan pushUpdateTarget, len(items))
	for _, item := range items {
		tasks <- item
	}
	close(tasks)

	workerCount := min(managedPageUpdateWorkerCount, len(items))
	var (
		wg       sync.WaitGroup
		firstErr error
		errOnce  sync.Once
	)
	setErr := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	for range workerCount {
		session, err := s.requireSession(ctx, "beads push update worker")
		if err != nil {
			setErr(err)
			break
		}
		wg.Add(1)
		go func(session mcpclient.Session) {
			defer wg.Done()
			defer func() { _ = session.Close() }()
			for item := range tasks {
				if ctx.Err() != nil {
					return
				}
				_, err := session.CallTool(ctx, &mcp.CallToolParams{
					Name: "notion-update-page",
					Arguments: map[string]any{
						"page_id":    item.PageID,
						"command":    "update_properties",
						"properties": wire.FilterPropertiesBySchema(wire.BuildUpdateProperties(item.Issue), schema),
					},
				})
				if err != nil {
					setErr(output.Wrap(err, "failed to update managed page "+item.Issue.ID))
					return
				}
			}
		}(session)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return nil
}

func collectPushIDs(issues []wire.PushIssue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

func filterUnsupportedPushIssues(issues []wire.PushIssue) ([]wire.PushIssue, []servicePushResultItem, []string) {
	filtered := make([]wire.PushIssue, 0, len(issues))
	skipped := make([]servicePushResultItem, 0)
	counts := make(map[string]int)
	for _, issue := range issues {
		if issue.HasSupportedType() {
			filtered = append(filtered, issue)
			continue
		}
		issueType := strings.TrimSpace(issue.TypeValue())
		if issueType == "" {
			issueType = "unknown"
		}
		counts[issueType]++
		skipped = append(skipped, servicePushResultItem{
			ID:     issue.ID,
			Title:  issue.Title,
			Reason: "unsupported_issue_type",
		})
	}
	if len(counts) == 0 {
		return filtered, skipped, nil
	}
	parts := make([]string, 0, len(counts))
	for issueType, count := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", issueType, count))
	}
	slices.Sort(parts)
	warnings := []string{fmt.Sprintf("Skipped unsupported Notion issue types: %s (supported: bug, feature, task, epic, chore)", strings.Join(parts, ", "))}
	return filtered, skipped, warnings
}

func pushIssueMatches(current wire.Issue, next wire.PushIssue) bool {
	currentType := serviceFirstNonEmpty(current.Type, current.IssueType)
	nextType := serviceFirstNonEmpty(next.Type, next.IssueType)
	return current.Title == next.Title &&
		current.Description == next.Description &&
		current.Status == next.Status &&
		current.Priority == next.Priority &&
		currentType == nextType &&
		current.Assignee == next.Assignee &&
		slices.Equal(sortedStrings(current.Labels), sortedStrings(next.Labels))
}

func cachedManagedIssueForPull(st *state.BeadsState, beadsID string, cacheMaxAge time.Duration) (wire.Issue, bool) {
	if cacheMaxAge <= 0 {
		return wire.Issue{}, false
	}
	if st == nil {
		return wire.Issue{}, false
	}
	entry, ok := st.EntryFor(beadsID)
	if !ok || entry.CachedIssue == nil || !cacheEntryIsFresh(entry, cacheMaxAge) {
		return wire.Issue{}, false
	}
	issue := wire.IssueFromSnapshot(entry.CachedIssue)
	return normalizeManagedIssueFromState(issue, st), true
}

func cachedManagedIssueForPush(st *state.BeadsState, beadsID string, cacheMaxAge time.Duration) (wire.Issue, bool) {
	if cacheMaxAge <= 0 {
		return wire.Issue{}, false
	}
	if st == nil {
		return wire.Issue{}, false
	}
	entry, ok := st.EntryFor(beadsID)
	if !ok || entry.CachedIssue == nil || !cacheEntryIsFresh(entry, cacheMaxAge) {
		return wire.Issue{}, false
	}
	issue := wire.IssueFromSnapshot(entry.CachedIssue)
	return normalizeManagedIssueFromState(issue, st), true
}

func cacheEntryIsFresh(entry state.BeadsStateEntry, maxAge time.Duration) bool {
	if maxAge <= 0 || strings.TrimSpace(entry.LastVerifiedAt) == "" {
		return false
	}
	verifiedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(entry.LastVerifiedAt))
	if err != nil {
		return false
	}
	return time.Since(verifiedAt) <= maxAge
}

func normalizeManagedIssueFromState(issue wire.Issue, st *state.BeadsState) wire.Issue {
	issue.ID = strings.TrimSpace(issue.ID)
	if st != nil {
		issue.NotionPageID = serviceFirstNonEmpty(issue.NotionPageID, st.PageIDFor(issue.ID))
	}
	if issue.ExternalRef == "" && issue.NotionPageID != "" {
		issue.ExternalRef = "notion:" + issue.NotionPageID
	}
	issue.Type = serviceFirstNonEmpty(issue.Type, issue.IssueType)
	issue.IssueType = serviceFirstNonEmpty(issue.IssueType, issue.Type)
	issue.Labels = sortedStrings(issue.Labels)
	return issue
}

func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	slices.Sort(out)
	return out
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func (s *Service) Status(ctx context.Context, databaseID, viewURL string) error {
	cfg, err := state.ReadBeadsConfig(s.paths)
	if err != nil {
		return output.Wrap(err, "failed to read beads config")
	}
	st, err := state.ReadBeadsState(s.paths)
	if err != nil {
		return output.Wrap(err, "failed to read beads state")
	}
	resolvedDatabaseID := serviceFirstNonEmpty(databaseID, func() string {
		if cfg != nil {
			return cfg.DatabaseID
		}
		return ""
	}())
	resolvedViewURL := serviceFirstNonEmpty(viewURL, func() string {
		if databaseID == "" && cfg != nil {
			return cfg.ViewURL
		}
		return ""
	}())
	configSource := ""
	if databaseID != "" || viewURL != "" {
		configSource = "flags"
	} else if cfg != nil {
		configSource = "config"
	}
	response := &serviceStatusResponse{
		Ready:   false,
		ViewURL: resolvedViewURL,
		SchemaVersion: func() string {
			if cfg != nil {
				return cfg.SchemaVersion
			}
			return ""
		}(),
		Configured:   cfg != nil,
		SavedConfig:  cfg != nil,
		ConfigSource: configSource,
		Auth:         &serviceStatusAuth{OK: false},
		Archive: &wire.ArchiveCapability{
			Supported: false,
			Mode:      wire.ArchiveSupportModeNone,
			Reason:    "Not authenticated",
		},
		State: &serviceStatusState{
			Path:           s.paths.BeadsStatePath,
			Present:        st != nil,
			ManagedCount:   len(pageIDsOrEmpty(st)),
			ViewConfigured: resolvedViewURL == "" || cfg != nil,
		},
	}
	if resolvedDatabaseID == "" {
		return s.io.JSON(response)
	}
	response.Database = &serviceStatusDatabase{ID: resolvedDatabaseID}
	hasTokens, err := s.authStore.HasTokens()
	if err != nil {
		return output.Wrap(err, "failed to inspect auth state")
	}
	if !hasTokens {
		return s.io.JSON(response)
	}
	session, err := s.connector.Connect(ctx)
	if err != nil {
		return s.io.JSON(response)
	}
	defer func() { _ = session.Close() }()

	authResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "notion-get-users", Arguments: map[string]any{"user_id": "self"}})
	if err != nil {
		return s.io.JSON(response)
	}
	authPayload, err := wire.ResultJSONMap(authResult, "beads status auth")
	if err != nil {
		return output.Wrap(err, "failed to decode auth payload")
	}
	response.Auth = &serviceStatusAuth{OK: true, User: wire.ExtractSelfUser(authPayload)}

	fetchResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "notion-fetch", Arguments: map[string]any{"id": resolvedDatabaseID}})
	if err != nil {
		return s.io.JSON(response)
	}
	fetchText, err := wire.ResultText(fetchResult, "beads status fetch")
	if err != nil {
		return output.Wrap(err, "failed to decode database fetch response")
	}
	info := wire.ExtractBeadsDatabaseInfoFromText(fetchText)
	schema := wire.AssessSchema(wire.DetectBeadsPropertiesFromFetchText(fetchText), true)
	viewConfigured := resolvedViewURL == ""
	for _, view := range info.Views {
		if view.URL == resolvedViewURL {
			viewConfigured = true
			break
		}
	}
	tools, err := session.ListTools(ctx, nil)
	archive := wire.ArchiveCapability{Supported: false, Mode: wire.ArchiveSupportModeNone}
	if err == nil {
		archive = wire.DetectArchiveSupport(tools.Tools)
	}
	response.Database = &serviceStatusDatabase{ID: serviceFirstNonEmpty(info.DatabaseID, resolvedDatabaseID), URL: info.DatabaseURL}
	response.DataSourceID = info.DataSourceID
	response.Views = info.Views
	response.Schema = &schema
	response.Archive = &archive
	response.State = &serviceStatusState{
		Path:           s.paths.BeadsStatePath,
		Present:        st != nil,
		ManagedCount:   len(pageIDsOrEmpty(st)),
		ViewConfigured: viewConfigured,
		DoctorSummary:  buildStateSummary(st),
	}
	response.Ready = response.Auth.OK && info.DataSourceID != "" && len(schema.Missing) == 0 && viewConfigured
	return s.io.JSON(response)
}

func (s *Service) requireSession(ctx context.Context, command string) (mcpclient.Session, error) {
	hasTokens, err := s.authStore.HasTokens()
	if err != nil {
		return nil, output.Wrap(err, "failed to inspect auth state")
	}
	if !hasTokens {
		return nil, output.NewError("Not authenticated", command+" requires saved Notion credentials", "Run \"bd notion login\" first", 1)
	}
	session, err := s.connector.Connect(ctx)
	if err != nil {
		return nil, output.NewError("Not authenticated", command+" could not authenticate against the Notion MCP", "Run \"bd notion login\" again", 1)
	}
	return session, nil
}

func buildStateSummary(st *state.BeadsState) *wire.DoctorSummary {
	if st == nil {
		return nil
	}
	entries := make([]wire.DoctorEntry, 0, len(st.PageIDs))
	for beadsID, pageID := range st.PageIDs {
		entries = append(entries, wire.DoctorEntry{BeadsID: beadsID, PageID: pageID, Status: "ok"})
	}
	summary := wire.SummarizeDoctorEntries(entries)
	return &summary
}

func pageIDsOrEmpty(st *state.BeadsState) map[string]string {
	if st == nil {
		return map[string]string{}
	}
	return st.PageIDs
}

func serviceFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
