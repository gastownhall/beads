package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/notion"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type notionConfigOperation struct {
	key   string
	value string
	err   error
}

type notionConfigCall struct {
	key   string
	value string
}

// notionConfigRecorder scripts the config operations a test expects. Writes
// are recorded independently and never become values that later reads return.
type notionConfigRecorder struct {
	reads   []notionConfigOperation
	sets    []notionConfigOperation
	deletes []notionConfigOperation

	readCalls   []notionConfigCall
	setCalls    []notionConfigCall
	deleteCalls []notionConfigCall
}

// notionConfigDoltStore is the production-adapter shell for these tests. Its
// nil embedded store makes any operation outside config read/set/delete panic.
type notionConfigDoltStore struct {
	storage.DoltStorage
	recorder *notionConfigRecorder
}

func (s *notionConfigDoltStore) GetConfig(ctx context.Context, key string) (string, error) {
	return s.recorder.GetConfig(ctx, key)
}

func (s *notionConfigDoltStore) SetConfig(ctx context.Context, key, value string) error {
	return s.recorder.SetConfig(ctx, key, value)
}

func (s *notionConfigDoltStore) DeleteConfig(ctx context.Context, key string) error {
	return s.recorder.DeleteConfig(ctx, key)
}

func (r *notionConfigRecorder) GetConfig(_ context.Context, key string) (string, error) {
	r.readCalls = append(r.readCalls, notionConfigCall{key: key})
	if len(r.reads) == 0 {
		return "", errors.New("unexpected config read")
	}
	op := r.reads[0]
	r.reads = r.reads[1:]
	if op.key != key {
		return "", errors.New("unexpected config read key")
	}
	return op.value, op.err
}

func (r *notionConfigRecorder) SetConfig(_ context.Context, key, value string) error {
	r.setCalls = append(r.setCalls, notionConfigCall{key: key, value: value})
	if len(r.sets) == 0 {
		return errors.New("unexpected config set")
	}
	op := r.sets[0]
	r.sets = r.sets[1:]
	if op.key != key || op.value != value {
		return errors.New("unexpected config set values")
	}
	return op.err
}

func (r *notionConfigRecorder) DeleteConfig(_ context.Context, key string) error {
	r.deleteCalls = append(r.deleteCalls, notionConfigCall{key: key})
	if len(r.deletes) == 0 {
		return errors.New("unexpected config delete")
	}
	op := r.deletes[0]
	r.deletes = r.deletes[1:]
	if op.key != key {
		return errors.New("unexpected config delete key")
	}
	return op.err
}

func (r *notionConfigRecorder) assertConsumed(t *testing.T) {
	t.Helper()
	if len(r.reads) != 0 || len(r.sets) != 0 || len(r.deletes) != 0 {
		t.Fatalf("unconsumed config script: reads=%+v sets=%+v deletes=%+v", r.reads, r.sets, r.deletes)
	}
}

func installNotionRecorderStore(t *testing.T, recorder *notionConfigRecorder, decorated bool) *notionConfigDoltStore {
	t.Helper()
	saveAndRestoreGlobals(t)
	raw := &notionConfigDoltStore{recorder: recorder}
	if decorated {
		store = storage.NewHookFiringStore(raw, nil)
	} else {
		store = raw
	}
	setStoreActive(true)
	return raw
}

func assertNotionConfigCalls(t *testing.T, recorder *notionConfigRecorder, reads, sets, deletes []notionConfigCall) {
	t.Helper()
	if !reflect.DeepEqual(recorder.readCalls, reads) || !reflect.DeepEqual(recorder.setCalls, sets) || !reflect.DeepEqual(recorder.deleteCalls, deletes) {
		t.Fatalf("calls = reads:%+v sets:%+v deletes:%+v, want reads:%+v sets:%+v deletes:%+v", recorder.readCalls, recorder.setCalls, recorder.deleteCalls, reads, sets, deletes)
	}
	recorder.assertConsumed(t)
}

func TestNotionCommandsRegistered(t *testing.T) {
	// Not parallel: Find mutates Cobra flag state on the global command tree.

	for _, name := range []string{"init", "connect", "status", "sync"} {
		if _, _, err := notionCmd.Find([]string{name}); err != nil {
			t.Fatalf("missing subcommand %q: %v", name, err)
		}
	}
}

func TestGetNotionConfigPrefersStoreOverEnv(t *testing.T) {
	recorder := &notionConfigRecorder{reads: []notionConfigOperation{
		{key: "notion.data_source_id", value: "store-ds"},
		{key: "notion.view_url", value: "https://store/view"},
	}}
	installNotionRecorderStore(t, recorder, false)

	t.Setenv("NOTION_TOKEN", "env-token")
	t.Setenv("NOTION_DATA_SOURCE_ID", "env-ds")
	t.Setenv("NOTION_VIEW_URL", "https://env/view")

	cfg := getNotionConfig()
	if cfg.DataSourceID != "store-ds" || cfg.ViewURL != "https://store/view" {
		t.Fatalf("config = %+v", cfg)
	}
	assertNotionConfigCalls(t, recorder,
		[]notionConfigCall{{key: "notion.data_source_id"}, {key: "notion.view_url"}}, nil, nil)
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
	if !strings.Contains(resp.Error, "bd config set notion.token") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestRunNotionInitPersistsTargetConfig(t *testing.T) {
	tests := []struct {
		name       string
		viewURL    string
		decorated  bool
		sets       []notionConfigOperation
		deletes    []notionConfigOperation
		wantSets   []notionConfigCall
		wantDelete []notionConfigCall
	}{
		{
			name:    "saves returned target",
			viewURL: "https://www.notion.so/db123",
			sets: []notionConfigOperation{
				{key: "notion.data_source_id", value: "ds_123"},
				{key: "notion.view_url", value: "https://www.notion.so/db123"},
			},
			wantSets: []notionConfigCall{{key: "notion.data_source_id", value: "ds_123"}, {key: "notion.view_url", value: "https://www.notion.so/db123"}},
		},
		{
			name:       "clears blank view through unwrapped decorated store",
			decorated:  true,
			sets:       []notionConfigOperation{{key: "notion.data_source_id", value: "ds_123"}},
			deletes:    []notionConfigOperation{{key: "notion.view_url"}},
			wantSets:   []notionConfigCall{{key: "notion.data_source_id", value: "ds_123"}},
			wantDelete: []notionConfigCall{{key: "notion.view_url"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &notionConfigRecorder{
				reads:   []notionConfigOperation{{key: "notion.token"}},
				sets:    tt.sets,
				deletes: tt.deletes,
			}
			rawStore := installNotionRecorderStore(t, recorder, tt.decorated)
			if tt.decorated && notionConfigDeleteTarget() != rawStore {
				t.Fatal("notionConfigDeleteTarget did not unwrap the decorated store")
			}

			oldFactory := newNotionSetupClient
			oldParent, oldTitle, oldJSON := notionInitParent, notionInitTitle, jsonOutput
			t.Cleanup(func() {
				newNotionSetupClient = oldFactory
				notionInitParent, notionInitTitle, jsonOutput = oldParent, oldTitle, oldJSON
			})
			notionInitParent = "329e5bf9-7fae-8080-bb4a-d94e1387655d"
			notionInitTitle = "Beads Issues"
			jsonOutput = false
			t.Setenv("NOTION_TOKEN", "env-token")

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
				_, _ = fmt.Fprintf(w, `{"id":"db_123","url":%q,"data_sources":[{"id":"ds_123","name":"Beads Issues"}]}`, tt.viewURL)
			}))
			defer server.Close()
			newNotionSetupClient = func(token string) *notion.Client {
				return notion.NewClient(token).WithBaseURL(server.URL)
			}

			cmd := &cobra.Command{}
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetContext(context.Background())
			if err := runNotionInit(cmd, nil); err != nil {
				t.Fatalf("runNotionInit returned error: %v", err)
			}
			if !strings.Contains(stdout.String(), "Saved data source: ds_123") {
				t.Fatalf("stdout = %q", stdout.String())
			}
			if tt.viewURL == "" && strings.Contains(stdout.String(), "Launch URL:") {
				t.Fatalf("stdout unexpectedly contains launch URL: %q", stdout.String())
			}
			assertNotionConfigCalls(t, recorder,
				[]notionConfigCall{{key: "notion.token"}}, tt.wantSets, tt.wantDelete)
		})
	}
}

func TestRunNotionConnectResolvesDataSourceURL(t *testing.T) {
	url := "https://www.notion.so/workspace/329e5bf97fae8080bb4ad94e1387655d"
	recorder := &notionConfigRecorder{
		reads: []notionConfigOperation{{key: "notion.token"}},
		sets: []notionConfigOperation{
			{key: "notion.data_source_id", value: "329e5bf9-7fae-8080-bb4a-d94e1387655d"},
			{key: "notion.view_url", value: url},
		},
	}
	installNotionRecorderStore(t, recorder, false)
	oldFactory := newNotionSetupClient
	oldURL, oldJSON := notionConnectURL, jsonOutput
	t.Cleanup(func() {
		newNotionSetupClient = oldFactory
		notionConnectURL, jsonOutput = oldURL, oldJSON
	})
	notionConnectURL = url
	jsonOutput = false
	t.Setenv("NOTION_TOKEN", "env-token")

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
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())
	if err := runNotionConnect(cmd, nil); err != nil {
		t.Fatalf("runNotionConnect returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Connected Notion data source 329e5bf9-7fae-8080-bb4a-d94e1387655d") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	assertNotionConfigCalls(t, recorder,
		[]notionConfigCall{{key: "notion.token"}},
		[]notionConfigCall{{key: "notion.data_source_id", value: "329e5bf9-7fae-8080-bb4a-d94e1387655d"}, {key: "notion.view_url", value: url}}, nil)
}

func TestRunNotionConnectResolvesDatabaseURL(t *testing.T) {
	ctx := context.Background()
	url := "https://www.notion.so/workspace/429e5bf97fae8080bb4ad94e1387655d"
	recorder := &notionConfigRecorder{sets: []notionConfigOperation{
		{key: "notion.data_source_id", value: "529e5bf9-7fae-8080-bb4a-d94e1387655d"},
		{key: "notion.view_url", value: url},
	}}

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
	result, err := runNotionConnectAfterValidation(ctx, notion.NewClient("env-token").WithBaseURL(server.URL), url, recorder, recorder)
	if err != nil {
		t.Fatalf("runNotionConnectAfterValidation returned error: %v", err)
	}
	if result.DataSourceID != "529e5bf9-7fae-8080-bb4a-d94e1387655d" || result.DatabaseID != "429e5bf9-7fae-8080-bb4a-d94e1387655d" {
		t.Fatalf("result = %+v", result)
	}
	recorder.assertConsumed(t)
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
	if resp.Auth.Source != "env" {
		t.Fatalf("auth source = %q", resp.Auth.Source)
	}
}

func TestResolveNotionAuthPrefersConfigTokenOverEnv(t *testing.T) {
	recorder := &notionConfigRecorder{reads: []notionConfigOperation{{key: "notion.token", value: "config-token"}}}
	installNotionRecorderStore(t, recorder, false)
	t.Setenv("NOTION_TOKEN", "env-token")

	auth, err := resolveNotionAuth(context.Background())
	if err != nil {
		t.Fatalf("resolveNotionAuth returned error: %v", err)
	}
	if auth == nil || auth.Token != "config-token" || auth.Source != notion.AuthSourceConfigToken {
		t.Fatalf("auth = %+v", auth)
	}
	assertNotionConfigCalls(t, recorder, []notionConfigCall{{key: "notion.token"}}, nil, nil)
}

func TestRenderNotionSyncResultUsesPhaseStats(t *testing.T) {
	saveAndRestoreGlobals(t)
	notionSyncDryRun = true
	t.Cleanup(func() { notionSyncDryRun = false })

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	renderNotionSyncResult(cmd, &tracker.SyncResult{
		Stats: tracker.SyncStats{Pulled: 2, Pushed: 3, Conflicts: 1},
		Warnings: []string{
			"Skipped unsupported Notion issue types: event=2",
			"Skipped bd-1: Notion external_ref points outside the current target; clear external_ref to recreate it in this data source",
			"Skipped bd-2: Notion external_ref points outside the current target; clear external_ref to recreate it in this data source",
		},
		PullStats: tracker.PullStats{
			Queried:    12,
			Candidates: 2,
			Created:    1,
			Updated:    1,
		},
		PushStats: tracker.PushStats{
			Created: 2,
			Updated: 1,
		},
	})
	out := stdout.String()
	for _, want := range []string{
		"Dry run mode",
		"Queried 12 pages from Notion (2 pull candidates)",
		"Pulled 2 issues (1 created, 1 updated)",
		"Pushed 3 issues (2 created, 1 updated)",
		"Resolved 1 conflicts",
		"Skipped 2 linked issues that still point at a different Notion target. Clear external_ref to recreate them in this data source.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"event=2",
		"bd-1",
		"bd-2",
	} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("stdout unexpectedly contained %q\n%s", unwanted, out)
		}
	}
}

func TestRenderNotionSyncResultOmitsMutationSummaryForSameMinuteNoopDryRun(t *testing.T) {
	saveAndRestoreGlobals(t)
	notionSyncDryRun = true
	t.Cleanup(func() { notionSyncDryRun = false })

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	renderNotionSyncResult(cmd, &tracker.SyncResult{
		PullStats: tracker.PullStats{
			Queried:    49,
			Candidates: 3,
		},
	})
	out := stdout.String()
	for _, want := range []string{
		"Dry run mode",
		"Queried 49 pages from Notion (3 pull candidates)",
		"Run without --dry-run to apply changes",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"Pulled ",
		"Pushed ",
		"Resolved ",
	} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("stdout unexpectedly contained %q\n%s", unwanted, out)
		}
	}
}

func TestValidateNotionConfigMessages(t *testing.T) {
	t.Parallel()

	err := validateNotionConfig(notionConfig{}, nil)
	if err == nil || !strings.Contains(err.Error(), "bd config set notion.token") {
		t.Fatalf("err = %v", err)
	}
	err = validateNotionConfig(notionConfig{}, &notion.ResolvedAuth{Token: "token", Source: notion.AuthSourceConfigToken})
	if err == nil || !strings.Contains(err.Error(), "bd notion init") {
		t.Fatalf("err = %v", err)
	}
}

func TestSaveNotionTargetConfig(t *testing.T) {
	t.Parallel()

	deleteErr := errors.New("delete failed")
	setErr := errors.New("set failed")
	tests := []struct {
		name     string
		recorder *notionConfigRecorder
		viewURL  string
		wantErr  error
		wantText string
		setCalls []notionConfigCall
		delCalls []notionConfigCall
	}{
		{
			name:     "data source set failure short circuits",
			recorder: &notionConfigRecorder{sets: []notionConfigOperation{{key: "notion.data_source_id", value: "ds_123", err: setErr}}},
			viewURL:  "https://www.notion.so/view",
			wantErr:  setErr,
			wantText: "save notion.data_source_id",
			setCalls: []notionConfigCall{{key: "notion.data_source_id", value: "ds_123"}},
		},
		{
			name:     "view delete failure preserves context",
			recorder: &notionConfigRecorder{sets: []notionConfigOperation{{key: "notion.data_source_id", value: "ds_123"}}, deletes: []notionConfigOperation{{key: "notion.view_url", err: deleteErr}}},
			wantErr:  deleteErr,
			wantText: "clear notion.view_url",
			setCalls: []notionConfigCall{{key: "notion.data_source_id", value: "ds_123"}},
			delCalls: []notionConfigCall{{key: "notion.view_url"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := saveNotionTargetConfigWithWriter(context.Background(), tt.recorder, tt.recorder, " ds_123 ", tt.viewURL)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("saveNotionTargetConfigWithWriter() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("saveNotionTargetConfigWithWriter() error = %q, want context %q", err, tt.wantText)
			}
			if !reflect.DeepEqual(tt.recorder.setCalls, tt.setCalls) || !reflect.DeepEqual(tt.recorder.deleteCalls, tt.delCalls) {
				t.Fatalf("calls = sets:%+v deletes:%+v, want sets:%+v deletes:%+v", tt.recorder.setCalls, tt.recorder.deleteCalls, tt.setCalls, tt.delCalls)
			}
			tt.recorder.assertConsumed(t)
		})
	}
}

func TestShouldPushNotionIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		issue      *types.Issue
		pushPrefix string
		pushLabel  string
		want       bool
	}{
		{
			name: "existing notion ref is allowed",
			issue: func() *types.Issue {
				extRef := "https://www.notion.so/Test-0123456789abcdef0123456789abcdef"
				return &types.Issue{ID: "beads-1", ExternalRef: &extRef}
			}(),
			want: true,
		},
		{
			name: "other tracker ref is rejected",
			issue: func() *types.Issue {
				extRef := "https://github.com/example/repo/issues/1"
				return &types.Issue{ID: "beads-1", ExternalRef: &extRef}
			}(),
			want: false,
		},
		{
			name:  "unlinked issue is allowed when no gate is configured",
			issue: &types.Issue{ID: "beads-1"},
			want:  true,
		},
		{
			name:       "prefix alone narrows issue set when no label gate is configured",
			issue:      &types.Issue{ID: "beads-1"},
			pushPrefix: "beads",
			want:       true,
		},
		{
			name:       "prefix mismatch still rejects issue without label gate",
			issue:      &types.Issue{ID: "beads-1"},
			pushPrefix: "proj",
			want:       false,
		},
		{
			name:      "configured label opts issue in",
			issue:     &types.Issue{ID: "beads-1", Labels: []string{"notion-sync"}},
			pushLabel: "notion-sync",
			want:      true,
		},
		{
			name:      "configured label still gates unlinked issue",
			issue:     &types.Issue{ID: "beads-1"},
			pushLabel: "notion-sync",
			want:      false,
		},
		{
			name:      "configured label is case insensitive",
			issue:     &types.Issue{ID: "beads-1", Labels: []string{"Notion-Sync"}},
			pushLabel: "notion-sync",
			want:      true,
		},
		{
			name:       "label plus matching prefix allows issue",
			issue:      &types.Issue{ID: "beads-1", Labels: []string{"notion-sync"}},
			pushPrefix: "beads",
			pushLabel:  "notion-sync",
			want:       true,
		},
		{
			name:       "label plus wrong prefix rejects issue",
			issue:      &types.Issue{ID: "beads-1", Labels: []string{"notion-sync"}},
			pushPrefix: "proj",
			pushLabel:  "notion-sync",
			want:       false,
		},
	}

	tr := &notion.Tracker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPushNotionIssue(tt.issue, tr, tt.pushPrefix, tt.pushLabel); got != tt.want {
				t.Fatalf("shouldPushNotionIssue() = %v, want %v", got, tt.want)
			}
		})
	}
}
