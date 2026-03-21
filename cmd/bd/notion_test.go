package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/notion"
	noutput "github.com/steveyegge/beads/internal/notion/output"
	nstate "github.com/steveyegge/beads/internal/notion/state"
	"github.com/steveyegge/beads/internal/storage"
	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type fakeNotionStatusService struct {
	resp  *notion.StatusResponse
	err   error
	req   notion.StatusRequest
	calls int
}

func (f *fakeNotionStatusService) Status(_ context.Context, req notion.StatusRequest) (*notion.StatusResponse, error) {
	f.calls++
	f.req = req
	return f.resp, f.err
}

type fakeNotionSyncEngine struct {
	result *itracker.SyncResult
	err    error
	opts   itracker.SyncOptions
}

func (f *fakeNotionSyncEngine) Sync(_ context.Context, opts itracker.SyncOptions) (*itracker.SyncResult, error) {
	f.opts = opts
	return f.result, f.err
}

type fakeNotionTracker struct{}

type fakeNotionManagementService struct {
	initReq struct {
		parentID string
		title    string
	}
	connectReq struct {
		databaseID string
		viewURL    string
	}
	stateExportPath  string
	stateImportPath  string
	configShowCalls  int
	configClearCalls int
	stateShowCalls   int
	stateDoctorCalls int
	err              error
}

func (f *fakeNotionManagementService) Init(_ context.Context, parentID, title string) error {
	f.initReq.parentID = parentID
	f.initReq.title = title
	return f.err
}

func (f *fakeNotionManagementService) Connect(_ context.Context, databaseID, viewURL string) error {
	f.connectReq.databaseID = databaseID
	f.connectReq.viewURL = viewURL
	return f.err
}

func (f *fakeNotionManagementService) ConfigShow() error {
	f.configShowCalls++
	return f.err
}

func (f *fakeNotionManagementService) ConfigClear() error {
	f.configClearCalls++
	return f.err
}

func (f *fakeNotionManagementService) StateShow() error {
	f.stateShowCalls++
	return f.err
}

func (f *fakeNotionManagementService) StateExport(outputPath string) error {
	f.stateExportPath = outputPath
	return f.err
}

func (f *fakeNotionManagementService) StateImport(inputPath string) error {
	f.stateImportPath = inputPath
	return f.err
}

func (f *fakeNotionManagementService) StateDoctor(context.Context) error {
	f.stateDoctorCalls++
	return f.err
}

func (f *fakeNotionTracker) Name() string                                { return "notion" }
func (f *fakeNotionTracker) DisplayName() string                         { return "Notion" }
func (f *fakeNotionTracker) ConfigPrefix() string                        { return "notion" }
func (f *fakeNotionTracker) Init(context.Context, storage.Storage) error { return nil }
func (f *fakeNotionTracker) Validate() error                             { return nil }
func (f *fakeNotionTracker) Close() error                                { return nil }
func (f *fakeNotionTracker) FetchIssues(context.Context, itracker.FetchOptions) ([]itracker.TrackerIssue, error) {
	return nil, nil
}
func (f *fakeNotionTracker) FetchIssue(context.Context, string) (*itracker.TrackerIssue, error) {
	return nil, nil
}
func (f *fakeNotionTracker) CreateIssue(context.Context, *types.Issue) (*itracker.TrackerIssue, error) {
	return nil, nil
}
func (f *fakeNotionTracker) UpdateIssue(context.Context, string, *types.Issue) (*itracker.TrackerIssue, error) {
	return nil, nil
}
func (f *fakeNotionTracker) FieldMapper() itracker.FieldMapper              { return nil }
func (f *fakeNotionTracker) IsExternalRef(string) bool                      { return false }
func (f *fakeNotionTracker) ExtractIdentifier(string) string                { return "" }
func (f *fakeNotionTracker) BuildExternalRef(*itracker.TrackerIssue) string { return "" }

func TestNotionStatusFlagsRegistered(t *testing.T) {
	if notionStatusCmd.Flags().Lookup("database-id") == nil {
		t.Fatal("missing --database-id")
	}
	if notionStatusCmd.Flags().Lookup("view-url") == nil {
		t.Fatal("missing --view-url")
	}
}

func TestNotionSyncFlagsRegistered(t *testing.T) {
	if notionSyncCmd.Flags().Lookup("cache-max-age") == nil {
		t.Fatal("missing --cache-max-age")
	}
}

func TestNotionManagementCommandsRegistered(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"login", "logout", "whoami", "init", "connect", "config", "state", "status", "sync"} {
		if _, _, err := notionCmd.Find([]string{name}); err != nil {
			t.Fatalf("missing subcommand %q: %v", name, err)
		}
	}
	if _, _, err := notionCmd.Find([]string{"config", "show"}); err != nil {
		t.Fatalf("missing config show: %v", err)
	}
	if _, _, err := notionCmd.Find([]string{"state", "doctor"}); err != nil {
		t.Fatalf("missing state doctor: %v", err)
	}
}

func TestRunNotionStatusPassesFlagsToClient(t *testing.T) {
	originalFactory := newNotionStatusClient
	originalJSON := jsonOutput
	originalDB := notionDatabaseID
	originalView := notionViewURL
	originalStore := store
	t.Cleanup(func() {
		newNotionStatusClient = originalFactory
		jsonOutput = originalJSON
		notionDatabaseID = originalDB
		notionViewURL = originalView
		store = originalStore
	})

	fake := &fakeNotionStatusService{resp: &notion.StatusResponse{Ready: true}}
	newNotionStatusClient = func() notionStatusClient {
		return fake
	}
	jsonOutput = false
	notionDatabaseID = "db_123"
	notionViewURL = "https://example.com/view"

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	if err := runNotionStatus(cmd, nil); err != nil {
		t.Fatalf("runNotionStatus returned error: %v", err)
	}
	if fake.req.DatabaseID != "db_123" {
		t.Fatalf("database id = %q", fake.req.DatabaseID)
	}
	if fake.req.ViewURL != "https://example.com/view" {
		t.Fatalf("view url = %q", fake.req.ViewURL)
	}
	if !strings.Contains(stdout.String(), "Ready: yes") {
		t.Fatalf("stdout = %q, want Ready: yes", stdout.String())
	}
}

func TestRunNotionStatusUsesStoredOverridesWhenFlagsEmpty(t *testing.T) {
	originalFactory := newNotionStatusClient
	originalJSON := jsonOutput
	originalDB := notionDatabaseID
	originalView := notionViewURL
	originalStore := store
	t.Cleanup(func() {
		newNotionStatusClient = originalFactory
		jsonOutput = originalJSON
		notionDatabaseID = originalDB
		notionViewURL = originalView
		store = originalStore
	})

	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	if err := testStore.SetConfig(ctx, "notion.database_id", "store-db"); err != nil {
		t.Fatalf("SetConfig(notion.database_id): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.view_url", "https://store/view"); err != nil {
		t.Fatalf("SetConfig(notion.view_url): %v", err)
	}
	store = testStore

	fake := &fakeNotionStatusService{resp: &notion.StatusResponse{Ready: true}}
	newNotionStatusClient = func() notionStatusClient {
		return fake
	}
	jsonOutput = false
	notionDatabaseID = ""
	notionViewURL = ""

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(ctx)

	if err := runNotionStatus(cmd, nil); err != nil {
		t.Fatalf("runNotionStatus returned error: %v", err)
	}
	if fake.req.DatabaseID != "store-db" {
		t.Fatalf("database id = %q, want store-db", fake.req.DatabaseID)
	}
	if fake.req.ViewURL != "https://store/view" {
		t.Fatalf("view url = %q, want https://store/view", fake.req.ViewURL)
	}
}

func TestRunNotionStatusFlagsOverrideStoredConfig(t *testing.T) {
	originalFactory := newNotionStatusClient
	originalJSON := jsonOutput
	originalDB := notionDatabaseID
	originalView := notionViewURL
	originalStore := store
	t.Cleanup(func() {
		newNotionStatusClient = originalFactory
		jsonOutput = originalJSON
		notionDatabaseID = originalDB
		notionViewURL = originalView
		store = originalStore
	})

	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	if err := testStore.SetConfig(ctx, "notion.database_id", "store-db"); err != nil {
		t.Fatalf("SetConfig(notion.database_id): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.view_url", "https://store/view"); err != nil {
		t.Fatalf("SetConfig(notion.view_url): %v", err)
	}
	store = testStore

	fake := &fakeNotionStatusService{resp: &notion.StatusResponse{Ready: true}}
	newNotionStatusClient = func() notionStatusClient {
		return fake
	}
	jsonOutput = false
	notionDatabaseID = "flag-db"
	notionViewURL = "https://flag/view"

	cmd := &cobra.Command{}
	cmd.SetContext(ctx)

	if err := runNotionStatus(cmd, nil); err != nil {
		t.Fatalf("runNotionStatus returned error: %v", err)
	}
	if fake.req.DatabaseID != "flag-db" {
		t.Fatalf("database id = %q, want flag-db", fake.req.DatabaseID)
	}
	if fake.req.ViewURL != "https://flag/view" {
		t.Fatalf("view url = %q, want https://flag/view", fake.req.ViewURL)
	}
}

func TestRunNotionStatusReturnsClientError(t *testing.T) {
	originalFactory := newNotionStatusClient
	t.Cleanup(func() { newNotionStatusClient = originalFactory })

	wantErr := errors.New("service missing")
	newNotionStatusClient = func() notionStatusClient {
		return &fakeNotionStatusService{err: wantErr}
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runNotionStatus(cmd, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestRunNotionStatusSurfacesBridgeAuthError(t *testing.T) {
	originalFactory := newNotionStatusClient
	t.Cleanup(func() { newNotionStatusClient = originalFactory })

	newNotionStatusClient = func() notionStatusClient {
		return &fakeNotionStatusService{
			err: &notion.BridgeCLIError{
				What: "Not authenticated",
				Why:  "bd could not authenticate against the Notion MCP",
				Hint: "Run \"bd notion login\" again",
			},
		}
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runNotionStatus(cmd, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Not authenticated") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "Run \"bd notion login\" again") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunNotionLoginUsesIntegratedDeps(t *testing.T) {
	originalDeps := newNotionCommandDeps
	originalAction := notionLoginAction
	originalJSON := jsonOutput
	t.Cleanup(func() {
		newNotionCommandDeps = originalDeps
		notionLoginAction = originalAction
		jsonOutput = originalJSON
	})

	called := false
	newNotionCommandDeps = func(cmd *cobra.Command) (*notionCommandDeps, error) {
		return &notionCommandDeps{}, nil
	}
	notionLoginAction = func(_ context.Context, _ *noutput.IO, _ *nstate.AuthStore) error {
		called = true
		return nil
	}
	jsonOutput = true

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runNotionLogin(cmd, nil); err != nil {
		t.Fatalf("runNotionLogin returned error: %v", err)
	}
	if !called {
		t.Fatal("login action was not called")
	}
}

func TestRunNotionInitUsesManagementService(t *testing.T) {
	originalDeps := newNotionCommandDeps
	originalReadonly := checkNotionReadonly
	originalParent := notionInitParent
	originalTitle := notionInitTitle
	t.Cleanup(func() {
		newNotionCommandDeps = originalDeps
		checkNotionReadonly = originalReadonly
		notionInitParent = originalParent
		notionInitTitle = originalTitle
	})

	fakeSvc := &fakeNotionManagementService{}
	newNotionCommandDeps = func(cmd *cobra.Command) (*notionCommandDeps, error) {
		return &notionCommandDeps{service: fakeSvc}, nil
	}
	checkNotionReadonly = func(string) {}
	notionInitParent = "page-123"
	notionInitTitle = "Issues"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runNotionInit(cmd, nil); err != nil {
		t.Fatalf("runNotionInit returned error: %v", err)
	}
	if fakeSvc.initReq.parentID != "page-123" {
		t.Fatalf("parent id = %q", fakeSvc.initReq.parentID)
	}
	if fakeSvc.initReq.title != "Issues" {
		t.Fatalf("title = %q", fakeSvc.initReq.title)
	}
}

func TestRenderNotionStatusIncludesArchiveWarning(t *testing.T) {

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	renderNotionStatus(cmd, &notion.StatusResponse{
		Ready:        true,
		DataSourceID: "ds_123",
		Archive: &notion.ArchiveCapability{
			Supported: false,
			Reason:    "live MCP does not expose archive",
		},
	})

	output := stdout.String()
	if !strings.Contains(output, "Archive Support: unavailable") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, "Archive Reason: live MCP does not expose archive") {
		t.Fatalf("output = %q", output)
	}
}

func TestRenderNotionSyncResultUsesPullPhaseStats(t *testing.T) {
	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	renderNotionSyncResult(cmd, &itracker.SyncResult{
		Stats: itracker.SyncStats{
			Pulled:  1,
			Pushed:  1,
			Created: 1,
			Updated: 1,
		},
		PullStats: itracker.PullStats{Updated: 1},
		PushStats: itracker.PushStats{Created: 1},
	})

	output := stdout.String()
	if !strings.Contains(output, "Pulled 1 issues (0 created, 1 updated)") {
		t.Fatalf("output = %q", output)
	}
	if strings.Contains(output, "Pulled 1 issues (1 created, 1 updated)") {
		t.Fatalf("output = %q", output)
	}
}

func TestRenderNotionSyncResultPrintsWarnings(t *testing.T) {
	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	renderNotionSyncResult(cmd, &itracker.SyncResult{
		Warnings: []string{"Skipped unsupported Notion issue types: event=2"},
	})

	output := stdout.String()
	if !strings.Contains(output, "Warnings:") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, "event=2") {
		t.Fatalf("output = %q", output)
	}
}

func TestBuildNotionSyncOptions(t *testing.T) {

	originalPull := notionSyncPull
	originalPush := notionSyncPush
	originalDryRun := notionSyncDryRun
	originalCreateOnly := notionCreateOnly
	originalState := notionSyncState
	originalPreferLocal := notionPreferLocal
	originalPreferNotion := notionPreferNotion
	t.Cleanup(func() {
		notionSyncPull = originalPull
		notionSyncPush = originalPush
		notionSyncDryRun = originalDryRun
		notionCreateOnly = originalCreateOnly
		notionSyncState = originalState
		notionPreferLocal = originalPreferLocal
		notionPreferNotion = originalPreferNotion
	})

	notionSyncPull = true
	notionSyncPush = true
	notionSyncDryRun = true
	notionCreateOnly = true
	notionSyncState = "open"
	notionPreferNotion = true

	opts := buildNotionSyncOptions()
	if !opts.Pull || !opts.Push || !opts.DryRun || !opts.CreateOnly {
		t.Fatalf("unexpected sync opts: %#v", opts)
	}
	if opts.State != "open" {
		t.Fatalf("state = %q, want open", opts.State)
	}
	if opts.ConflictResolution != itracker.ConflictExternal {
		t.Fatalf("conflict resolution = %q, want external", opts.ConflictResolution)
	}
}

func TestRunNotionSyncUsesEngine(t *testing.T) {

	originalStatusFactory := newNotionStatusClient
	originalTrackerFactory := newNotionTracker
	originalEngineFactory := newNotionSyncEngine
	originalEnsure := ensureNotionStoreActive
	originalReadonly := checkNotionReadonly
	originalJSON := jsonOutput
	originalPull := notionSyncPull
	originalPush := notionSyncPush
	originalDryRun := notionSyncDryRun
	originalCreateOnly := notionCreateOnly
	originalState := notionSyncState
	originalPreferLocal := notionPreferLocal
	originalPreferNotion := notionPreferNotion
	t.Cleanup(func() {
		newNotionStatusClient = originalStatusFactory
		newNotionTracker = originalTrackerFactory
		newNotionSyncEngine = originalEngineFactory
		ensureNotionStoreActive = originalEnsure
		checkNotionReadonly = originalReadonly
		jsonOutput = originalJSON
		notionSyncPull = originalPull
		notionSyncPush = originalPush
		notionSyncDryRun = originalDryRun
		notionCreateOnly = originalCreateOnly
		notionSyncState = originalState
		notionPreferLocal = originalPreferLocal
		notionPreferNotion = originalPreferNotion
	})

	fakeStatus := &fakeNotionStatusService{resp: &notion.StatusResponse{Ready: true}}
	newNotionStatusClient = func() notionStatusClient { return fakeStatus }
	newNotionTracker = func() itracker.IssueTracker { return &fakeNotionTracker{} }
	fakeEngine := &fakeNotionSyncEngine{
		result: &itracker.SyncResult{
			Success: true,
			Stats:   itracker.SyncStats{Pulled: 1, Created: 1},
		},
	}
	newNotionSyncEngine = func(itracker.IssueTracker) notionSyncEngine { return fakeEngine }
	ensureNotionStoreActive = func() error { return nil }
	checkNotionReadonly = func(string) {}
	jsonOutput = false
	notionSyncPull = true
	notionSyncPush = false
	notionSyncDryRun = true
	notionSyncState = "all"

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetContext(context.Background())

	if err := runNotionSync(cmd, nil); err != nil {
		t.Fatalf("runNotionSync returned error: %v", err)
	}
	if !fakeEngine.opts.Pull || fakeEngine.opts.Push {
		t.Fatalf("unexpected sync opts: %#v", fakeEngine.opts)
	}
	if fakeStatus.calls != 0 {
		t.Fatalf("status client calls = %d, want 0", fakeStatus.calls)
	}
	if !strings.Contains(stdout.String(), "Dry run complete") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestValidateNotionSyncOverridesRejectsPullWithStoredOverrides(t *testing.T) {
	originalStore := store
	originalDB := notionDatabaseID
	originalView := notionViewURL
	t.Cleanup(func() {
		store = originalStore
		notionDatabaseID = originalDB
		notionViewURL = originalView
	})

	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	if err := testStore.SetConfig(ctx, "notion.database_id", "store-db"); err != nil {
		t.Fatalf("SetConfig(notion.database_id): %v", err)
	}
	store = testStore
	notionDatabaseID = ""
	notionViewURL = ""

	err := validateNotionSyncOverrides(ctx, itracker.SyncOptions{Pull: true})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "only supported with --push") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestValidateNotionSyncOverridesAllowsPushOnlyOverrides(t *testing.T) {
	originalStore := store
	originalDB := notionDatabaseID
	originalView := notionViewURL
	t.Cleanup(func() {
		store = originalStore
		notionDatabaseID = originalDB
		notionViewURL = originalView
	})

	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	if err := testStore.SetConfig(ctx, "notion.database_id", "store-db"); err != nil {
		t.Fatalf("SetConfig(notion.database_id): %v", err)
	}
	store = testStore
	notionDatabaseID = ""
	notionViewURL = ""

	if err := validateNotionSyncOverrides(ctx, itracker.SyncOptions{Push: true}); err != nil {
		t.Fatalf("validateNotionSyncOverrides returned error: %v", err)
	}
}

func TestValidateNotionSyncOverridesTreatsDefaultSyncAsPull(t *testing.T) {
	originalStore := store
	originalDB := notionDatabaseID
	originalView := notionViewURL
	t.Cleanup(func() {
		store = originalStore
		notionDatabaseID = originalDB
		notionViewURL = originalView
	})

	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	if err := testStore.SetConfig(ctx, "notion.view_url", "view://stored"); err != nil {
		t.Fatalf("SetConfig(notion.view_url): %v", err)
	}
	store = testStore
	notionDatabaseID = ""
	notionViewURL = ""

	err := validateNotionSyncOverrides(ctx, itracker.SyncOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFakeTrackerSatisfiesInterface(t *testing.T) {

	var _ itracker.IssueTracker = (*fakeNotionTracker)(nil)
	_ = time.Second
}
