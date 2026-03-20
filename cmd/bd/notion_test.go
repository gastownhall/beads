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
	"github.com/steveyegge/beads/internal/storage"
	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type fakeNotionStatusService struct {
	resp *notion.StatusResponse
	err  error
	req  notion.StatusRequest
}

func (f *fakeNotionStatusService) Status(_ context.Context, req notion.StatusRequest) (*notion.StatusResponse, error) {
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

	if notionStatusCmd.Flags().Lookup("ncli-bin") == nil {
		t.Fatal("missing --ncli-bin")
	}
	if notionStatusCmd.Flags().Lookup("database-id") == nil {
		t.Fatal("missing --database-id")
	}
	if notionStatusCmd.Flags().Lookup("view-url") == nil {
		t.Fatal("missing --view-url")
	}
}

func TestRunNotionStatusPassesFlagsToClient(t *testing.T) {

	originalFactory := newNotionStatusClient
	originalJSON := jsonOutput
	originalBin := notionNCLIBin
	originalDB := notionDatabaseID
	originalView := notionViewURL
	originalStore := store
	t.Cleanup(func() {
		newNotionStatusClient = originalFactory
		jsonOutput = originalJSON
		notionNCLIBin = originalBin
		notionDatabaseID = originalDB
		notionViewURL = originalView
		store = originalStore
	})

	fake := &fakeNotionStatusService{resp: &notion.StatusResponse{Ready: true}}
	newNotionStatusClient = func(binaryPath string) notionStatusClient {
		if binaryPath != "/tmp/ncli" {
			t.Fatalf("binary path = %q, want /tmp/ncli", binaryPath)
		}
		return fake
	}
	jsonOutput = false
	notionNCLIBin = "/tmp/ncli"
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
	originalBin := notionNCLIBin
	originalDB := notionDatabaseID
	originalView := notionViewURL
	originalStore := store
	t.Cleanup(func() {
		newNotionStatusClient = originalFactory
		jsonOutput = originalJSON
		notionNCLIBin = originalBin
		notionDatabaseID = originalDB
		notionViewURL = originalView
		store = originalStore
	})

	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	if err := testStore.SetConfig(ctx, "notion.ncli_bin", "/store/ncli"); err != nil {
		t.Fatalf("SetConfig(notion.ncli_bin): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.database_id", "store-db"); err != nil {
		t.Fatalf("SetConfig(notion.database_id): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.view_url", "https://store/view"); err != nil {
		t.Fatalf("SetConfig(notion.view_url): %v", err)
	}
	store = testStore

	fake := &fakeNotionStatusService{resp: &notion.StatusResponse{Ready: true}}
	newNotionStatusClient = func(binaryPath string) notionStatusClient {
		if binaryPath != "/store/ncli" {
			t.Fatalf("binary path = %q, want /store/ncli", binaryPath)
		}
		return fake
	}
	jsonOutput = false
	notionNCLIBin = ""
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
	originalBin := notionNCLIBin
	originalDB := notionDatabaseID
	originalView := notionViewURL
	originalStore := store
	t.Cleanup(func() {
		newNotionStatusClient = originalFactory
		jsonOutput = originalJSON
		notionNCLIBin = originalBin
		notionDatabaseID = originalDB
		notionViewURL = originalView
		store = originalStore
	})

	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	if err := testStore.SetConfig(ctx, "notion.ncli_bin", "/store/ncli"); err != nil {
		t.Fatalf("SetConfig(notion.ncli_bin): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.database_id", "store-db"); err != nil {
		t.Fatalf("SetConfig(notion.database_id): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.view_url", "https://store/view"); err != nil {
		t.Fatalf("SetConfig(notion.view_url): %v", err)
	}
	store = testStore

	fake := &fakeNotionStatusService{resp: &notion.StatusResponse{Ready: true}}
	newNotionStatusClient = func(binaryPath string) notionStatusClient {
		if binaryPath != "/flag/ncli" {
			t.Fatalf("binary path = %q, want /flag/ncli", binaryPath)
		}
		return fake
	}
	jsonOutput = false
	notionNCLIBin = "/flag/ncli"
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

	wantErr := errors.New("ncli missing")
	newNotionStatusClient = func(string) notionStatusClient {
		return &fakeNotionStatusService{err: wantErr}
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runNotionStatus(cmd, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
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

	newNotionStatusClient = func(string) notionStatusClient {
		return &fakeNotionStatusService{resp: &notion.StatusResponse{Ready: true}}
	}
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

func TestPreflightNotionSyncRequiresReadyStatus(t *testing.T) {

	originalFactory := newNotionStatusClient
	originalStore := store
	originalBin := notionNCLIBin
	originalDB := notionDatabaseID
	originalView := notionViewURL
	t.Cleanup(func() {
		newNotionStatusClient = originalFactory
		store = originalStore
		notionNCLIBin = originalBin
		notionDatabaseID = originalDB
		notionViewURL = originalView
	})

	newNotionStatusClient = func(string) notionStatusClient {
		return &fakeNotionStatusService{resp: &notion.StatusResponse{Ready: false}}
	}

	err := preflightNotionSync(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Notion sync is not ready") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestPreflightNotionSyncUsesStoredOverridesWhenFlagsEmpty(t *testing.T) {
	originalFactory := newNotionStatusClient
	originalStore := store
	originalBin := notionNCLIBin
	originalDB := notionDatabaseID
	originalView := notionViewURL
	t.Cleanup(func() {
		newNotionStatusClient = originalFactory
		store = originalStore
		notionNCLIBin = originalBin
		notionDatabaseID = originalDB
		notionViewURL = originalView
	})

	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	if err := testStore.SetConfig(ctx, "notion.ncli_bin", "/store/ncli"); err != nil {
		t.Fatalf("SetConfig(notion.ncli_bin): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.database_id", "store-db"); err != nil {
		t.Fatalf("SetConfig(notion.database_id): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.view_url", "https://store/view"); err != nil {
		t.Fatalf("SetConfig(notion.view_url): %v", err)
	}
	store = testStore
	notionNCLIBin = ""
	notionDatabaseID = ""
	notionViewURL = ""

	newNotionStatusClient = func(binaryPath string) notionStatusClient {
		if binaryPath != "/store/ncli" {
			t.Fatalf("binary path = %q, want /store/ncli", binaryPath)
		}
		return &fakeNotionStatusService{
			resp: &notion.StatusResponse{Ready: true},
		}
	}

	if err := preflightNotionSync(ctx); err != nil {
		t.Fatalf("preflightNotionSync returned error: %v", err)
	}
}

func TestFakeTrackerSatisfiesInterface(t *testing.T) {

	var _ itracker.IssueTracker = (*fakeNotionTracker)(nil)
	_ = time.Second
}
