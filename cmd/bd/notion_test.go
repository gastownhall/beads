//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/notion"
	"github.com/steveyegge/beads/internal/tracker"
)

func TestNotionCommandsRegistered(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"init", "connect", "status", "sync"} {
		if _, _, err := notionCmd.Find([]string{name}); err != nil {
			t.Fatalf("missing subcommand %q: %v", name, err)
		}
	}
}

func TestGetNotionConfigPrefersStoreOverEnv(t *testing.T) {
	saveAndRestoreGlobals(t)
	ctx := context.Background()
	testStore, cleanup := setupTestDB(t)
	defer cleanup()

	store = testStore
	if err := store.SetConfig(ctx, "notion.token", "store-token"); err != nil {
		t.Fatalf("SetConfig(notion.token): %v", err)
	}
	if err := store.SetConfig(ctx, "notion.data_source_id", "store-ds"); err != nil {
		t.Fatalf("SetConfig(notion.data_source_id): %v", err)
	}
	if err := store.SetConfig(ctx, "notion.view_url", "https://store/view"); err != nil {
		t.Fatalf("SetConfig(notion.view_url): %v", err)
	}

	t.Setenv("NOTION_TOKEN", "env-token")
	t.Setenv("NOTION_DATA_SOURCE_ID", "env-ds")
	t.Setenv("NOTION_VIEW_URL", "https://env/view")

	cfg := getNotionConfig()
	if cfg.Token != "store-token" || cfg.DataSourceID != "store-ds" || cfg.ViewURL != "https://store/view" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestRunNotionStatusJSONWithMissingConfig(t *testing.T) {
	saveAndRestoreGlobals(t)
	jsonOutput = true
	store = nil
	dbPath = ""
	t.Setenv("NOTION_TOKEN", "")
	t.Setenv("NOTION_DATA_SOURCE_ID", "")
	t.Setenv("NOTION_VIEW_URL", "")

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	if err := runNotionStatus(cmd, nil); err != nil {
		t.Fatalf("runNotionStatus returned error: %v", err)
	}

	var resp notion.StatusResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal status json: %v\n%s", err, stdout.String())
	}
	if resp.Configured {
		t.Fatal("expected configured=false")
	}
	if !strings.Contains(resp.Error, "notion.token") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestRunNotionInitPersistsTargetConfig(t *testing.T) {
	saveAndRestoreGlobals(t)
	ctx := context.Background()
	testStore, cleanup := setupTestDB(t)
	defer cleanup()
	store = testStore
	jsonOutput = true
	notionInitParent = "329e5bf9-7fae-8080-bb4a-d94e1387655d"
	notionInitTitle = "Beads Issues"
	t.Setenv("NOTION_TOKEN", "env-token")
	originalFactory := newNotionSetupClient
	t.Cleanup(func() { newNotionSetupClient = originalFactory })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/databases" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		for _, want := range []string{
			`"page_id":"329e5bf9-7fae-8080-bb4a-d94e1387655d"`,
			`"initial_data_source"`,
			`"Beads ID"`,
			`"Status"`,
		} {
			if !strings.Contains(string(body), want) {
				t.Fatalf("request body missing %q\n%s", want, body)
			}
		}
		_, _ = io.WriteString(w, `{"id":"db_123","url":"https://www.notion.so/db123","data_sources":[{"id":"ds_123","name":"Beads Issues"}]}`)
	}))
	defer server.Close()
	newNotionSetupClient = func(token string) *notion.Client {
		return notion.NewClient(token).WithBaseURL(server.URL)
	}

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(ctx)

	if err := runNotionInit(cmd, nil); err != nil {
		t.Fatalf("runNotionInit returned error: %v", err)
	}

	dataSourceID, err := store.GetConfig(ctx, "notion.data_source_id")
	if err != nil || dataSourceID != "ds_123" {
		t.Fatalf("notion.data_source_id = %q, err=%v", dataSourceID, err)
	}
	viewURL, err := store.GetConfig(ctx, "notion.view_url")
	if err != nil || viewURL != "https://www.notion.so/db123" {
		t.Fatalf("notion.view_url = %q, err=%v", viewURL, err)
	}
}

func TestRunNotionConnectResolvesDataSourceURL(t *testing.T) {
	saveAndRestoreGlobals(t)
	ctx := context.Background()
	testStore, cleanup := setupTestDB(t)
	defer cleanup()
	store = testStore
	t.Setenv("NOTION_TOKEN", "env-token")
	notionConnectURL = "https://www.notion.so/workspace/329e5bf97fae8080bb4ad94e1387655d"
	originalFactory := newNotionSetupClient
	t.Cleanup(func() { newNotionSetupClient = originalFactory })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data_sources/329e5bf9-7fae-8080-bb4a-d94e1387655d":
			_, _ = io.WriteString(w, `{"id":"329e5bf9-7fae-8080-bb4a-d94e1387655d","properties":{"Name":{"type":"title"},"Beads ID":{"type":"rich_text"},"Status":{"type":"select"},"Priority":{"type":"select"},"Type":{"type":"select"},"Description":{"type":"rich_text"},"Assignee":{"type":"rich_text"},"Labels":{"type":"multi_select"}}}`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	newNotionSetupClient = func(token string) *notion.Client {
		return notion.NewClient(token).WithBaseURL(server.URL)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	if err := runNotionConnect(cmd, nil); err != nil {
		t.Fatalf("runNotionConnect returned error: %v", err)
	}

	dataSourceID, err := store.GetConfig(ctx, "notion.data_source_id")
	if err != nil || dataSourceID != "329e5bf9-7fae-8080-bb4a-d94e1387655d" {
		t.Fatalf("notion.data_source_id = %q, err=%v", dataSourceID, err)
	}
}

func TestRunNotionConnectResolvesDatabaseURL(t *testing.T) {
	saveAndRestoreGlobals(t)
	ctx := context.Background()
	testStore, cleanup := setupTestDB(t)
	defer cleanup()
	store = testStore
	t.Setenv("NOTION_TOKEN", "env-token")
	notionConnectURL = "https://www.notion.so/workspace/429e5bf97fae8080bb4ad94e1387655d"
	originalFactory := newNotionSetupClient
	t.Cleanup(func() { newNotionSetupClient = originalFactory })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data_sources/429e5bf9-7fae-8080-bb4a-d94e1387655d":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"object_not_found","message":"not found"}`)
		case "/databases/429e5bf9-7fae-8080-bb4a-d94e1387655d":
			_, _ = io.WriteString(w, `{"id":"429e5bf9-7fae-8080-bb4a-d94e1387655d","data_sources":[{"id":"529e5bf9-7fae-8080-bb4a-d94e1387655d","name":"Beads Issues"}]}`)
		case "/data_sources/529e5bf9-7fae-8080-bb4a-d94e1387655d":
			_, _ = io.WriteString(w, `{"id":"529e5bf9-7fae-8080-bb4a-d94e1387655d","properties":{"Name":{"type":"title"},"Beads ID":{"type":"rich_text"},"Status":{"type":"select"},"Priority":{"type":"select"},"Type":{"type":"select"},"Description":{"type":"rich_text"},"Assignee":{"type":"rich_text"},"Labels":{"type":"multi_select"}}}`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	newNotionSetupClient = func(token string) *notion.Client {
		return notion.NewClient(token).WithBaseURL(server.URL)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	if err := runNotionConnect(cmd, nil); err != nil {
		t.Fatalf("runNotionConnect returned error: %v", err)
	}

	dataSourceID, err := store.GetConfig(ctx, "notion.data_source_id")
	if err != nil || dataSourceID != "529e5bf9-7fae-8080-bb4a-d94e1387655d" {
		t.Fatalf("notion.data_source_id = %q, err=%v", dataSourceID, err)
	}
}

func TestRunNotionStatusUsesHTTPClient(t *testing.T) {
	saveAndRestoreGlobals(t)
	originalFactory := newNotionStatusClient
	t.Cleanup(func() { newNotionStatusClient = originalFactory })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/me":
			if got := r.Header.Get("Authorization"); got != "Bearer env-token" {
				t.Fatalf("authorization = %q", got)
			}
			_, _ = io.WriteString(w, `{"id":"user-1","name":"Osamu","type":"person","person":{"email":"osamu@example.com"}}`)
		case "/data_sources/ds_123":
			_, _ = io.WriteString(w, `{"id":"ds_123","url":"https://www.notion.so/source","title":[{"plain_text":"Tasks"}],"properties":{"Name":{"type":"title"},"Beads ID":{"type":"rich_text"},"Status":{"type":"select"},"Priority":{"type":"select"},"Type":{"type":"select"},"Description":{"type":"rich_text"},"Assignee":{"type":"rich_text"},"Labels":{"type":"multi_select"}}}`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	newNotionStatusClient = func(token string) *notion.Client {
		return notion.NewClient(token).WithBaseURL(server.URL)
	}
	jsonOutput = true
	store = nil
	dbPath = ""
	t.Setenv("NOTION_TOKEN", "env-token")
	t.Setenv("NOTION_DATA_SOURCE_ID", "ds_123")
	t.Setenv("NOTION_VIEW_URL", "https://www.notion.so/view")

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	if err := runNotionStatus(cmd, nil); err != nil {
		t.Fatalf("runNotionStatus returned error: %v", err)
	}

	var resp notion.StatusResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal status json: %v\n%s", err, stdout.String())
	}
	if !resp.Ready {
		t.Fatalf("expected ready response, got %+v", resp)
	}
	if resp.Database == nil || resp.Database.Title != "Tasks" {
		t.Fatalf("database = %+v", resp.Database)
	}
	if resp.Auth == nil || !resp.Auth.OK || resp.Auth.User == nil || resp.Auth.User.Email != "osamu@example.com" {
		t.Fatalf("auth = %+v", resp.Auth)
	}
}

func TestRenderNotionSyncResultUsesPhaseStats(t *testing.T) {
	saveAndRestoreGlobals(t)
	notionSyncDryRun = true
	t.Cleanup(func() { notionSyncDryRun = false })

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	err := renderNotionSyncResult(cmd, &tracker.SyncResult{
		Stats: tracker.SyncStats{Pulled: 2, Pushed: 3, Conflicts: 1},
		PullStats: tracker.PullStats{
			Created: 1,
			Updated: 1,
		},
		PushStats: tracker.PushStats{
			Created: 2,
			Updated: 1,
		},
	})
	if err != nil {
		t.Fatalf("renderNotionSyncResult returned error: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"Dry run mode",
		"Pulled 2 issues (1 created, 1 updated)",
		"Pushed 3 issues (2 created, 1 updated)",
		"Resolved 1 conflicts",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q\n%s", want, out)
		}
	}
}

func TestValidateNotionConfigMessages(t *testing.T) {
	t.Parallel()

	err := validateNotionConfig(notionConfig{})
	if err == nil || !strings.Contains(err.Error(), "notion.token") {
		t.Fatalf("err = %v", err)
	}
	err = validateNotionConfig(notionConfig{Token: "token"})
	if err == nil || !strings.Contains(err.Error(), "bd notion init") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetNotionConfigReadsDBPathWhenStoreUnset(t *testing.T) {
	saveAndRestoreGlobals(t)
	tempDir := t.TempDir()
	testDBPath := filepath.Join(tempDir, "test.db")
	testStore := newTestStore(t, testDBPath)
	defer testStore.Close()

	ctx := context.Background()
	if err := testStore.SetConfig(ctx, "notion.token", "path-token"); err != nil {
		t.Fatalf("SetConfig(notion.token): %v", err)
	}
	if err := testStore.SetConfig(ctx, "notion.data_source_id", "path-ds"); err != nil {
		t.Fatalf("SetConfig(notion.data_source_id): %v", err)
	}

	store = nil
	dbPath = testDBPath
	t.Setenv("NOTION_TOKEN", "")
	t.Setenv("NOTION_DATA_SOURCE_ID", "")
	t.Setenv("NOTION_VIEW_URL", "")

	cfg := getNotionConfig()
	if cfg.Token != "path-token" || cfg.DataSourceID != "path-ds" {
		t.Fatalf("config = %+v", cfg)
	}
}
