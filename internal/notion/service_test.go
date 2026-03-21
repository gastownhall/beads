package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steveyegge/beads/internal/notion/mcpclient"
	noutput "github.com/steveyegge/beads/internal/notion/output"
	"github.com/steveyegge/beads/internal/notion/state"
	"github.com/steveyegge/beads/internal/notion/wire"
)

type fakeSessionConnector struct {
	session mcpclient.Session
	err     error
}

func (c fakeSessionConnector) Connect(context.Context) (mcpclient.Session, error) {
	return c.session, c.err
}

type fakeSession struct {
	databaseID string
	pageData   map[string]fakeFetchedPage
	viewURL    string
	tools      []*mcp.Tool
}

type fakeFetchedPage struct {
	pageID       string
	beadsID      string
	title        string
	description  string
	databaseID   string
	dataSourceID string
}

func (s *fakeSession) CallTool(_ context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	switch params.Name {
	case "notion-fetch":
		args, _ := params.Arguments.(map[string]any)
		id, _ := args["id"].(string)
		if id == s.databaseID {
			return &mcp.CallToolResult{StructuredContent: map[string]any{
				"result": "<database url=\"https://www.notion.so/" + s.databaseID + "\"><data-source url=\"collection://data-source-1\"><view url=\"" + s.viewURL + "\" name=\"All Issues\" type=\"table\"><data-source-state>{\"schema\":{\"Name\":{},\"Beads ID\":{},\"Status\":{},\"Priority\":{},\"Type\":{},\"Description\":{},\"Assignee\":{},\"Labels\":{}}}</data-source-state>",
			}}, nil
		}
		if page, ok := s.pageData[id]; ok {
			return &mcp.CallToolResult{StructuredContent: map[string]any{
				"result":     "<parent-data-source url=\"collection://" + page.dataSourceID + "\" name=\"Beads Issues\"></parent-data-source><ancestor-2-database url=\"https://www.notion.so/" + page.databaseID + "\" title=\"Beads Issues\"></ancestor-2-database><properties>{\"Beads ID\":\"" + page.beadsID + "\",\"Name\":\"" + page.title + "\",\"Description\":\"" + page.description + "\",\"Status\":\"Open\",\"Priority\":\"Medium\",\"Type\":\"Task\",\"url\":\"https://www.notion.so/" + page.pageID + "\"}</properties>",
				"created_at": "2026-03-20T00:00:00Z",
				"updated_at": "2026-03-20T00:00:00Z",
			}}, nil
		}
	case "notion-update-page":
		return &mcp.CallToolResult{StructuredContent: map[string]any{"ok": true}}, nil
	}
	return nil, nil
}

func (s *fakeSession) ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: s.tools}, nil
}

func (s *fakeSession) Close() error { return nil }

func TestServicePushPayloadCreatesWhenSavedStateTargetsDifferentDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	paths := &state.Paths{
		ConfigDir:       tmpDir,
		LegacyConfigDir: filepath.Join(tmpDir, "legacy"),
		ClientPath:      filepath.Join(tmpDir, "client.json"),
		TokensPath:      filepath.Join(tmpDir, "tokens.json"),
		AuthStatePath:   filepath.Join(tmpDir, "auth-state.json"),
		BeadsConfigPath: filepath.Join(tmpDir, "beads.json"),
		BeadsStatePath:  filepath.Join(tmpDir, "beads-state.json"),
	}
	if err := state.SaveBeadsConfig(paths, &state.BeadsConfig{
		DatabaseID:    "22222222-2222-2222-2222-222222222222",
		DataSourceID:  "data-source-1",
		ViewURL:       "view://all-issues",
		SchemaVersion: wire.SchemaVersion,
	}); err != nil {
		t.Fatalf("SaveBeadsConfig returned error: %v", err)
	}
	if err := state.SaveBeadsState(paths, &state.BeadsState{
		DatabaseID: "22222222-2222-2222-2222-222222222222",
		Entries: map[string]state.BeadsStateEntry{
			"bd-1": {PageID: "11111111-1111-1111-1111-111111111111"},
		},
		PageIDs: map[string]string{
			"bd-1": "11111111-1111-1111-1111-111111111111",
		},
	}); err != nil {
		t.Fatalf("SaveBeadsState returned error: %v", err)
	}

	var stdout bytes.Buffer
	authStore := state.NewAuthStore(paths)
	if err := authStore.SaveTokens(state.StoredTokens{AccessToken: "test-token"}); err != nil {
		t.Fatalf("SaveTokens returned error: %v", err)
	}
	svc := NewService(noutput.NewIO(&stdout, &bytes.Buffer{}).WithJSON(true), authStore)
	svc.connector = fakeSessionConnector{session: &fakeSession{
		databaseID: "22222222-2222-2222-2222-222222222222",
		viewURL:    "view://all-issues",
		pageData: map[string]fakeFetchedPage{
			"11111111-1111-1111-1111-111111111111": {
				pageID:       "11111111-1111-1111-1111-111111111111",
				beadsID:      "bd-1",
				title:        "Existing page",
				description:  "old",
				databaseID:   "99999999-9999-9999-9999-999999999999",
				dataSourceID: "foreign-data-source",
			},
		},
	}}

	payload, err := json.Marshal(PushPayload{
		Issues: []PushIssue{{
			ID:          "bd-1",
			Title:       "Recreate in current db",
			Description: "new description",
			Status:      "open",
			Priority:    "medium",
			IssueType:   "task",
		}},
	})
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	if err := svc.PushPayload(context.Background(), payload, "", "", true, false, 0); err != nil {
		t.Fatalf("PushPayload returned error: %v", err)
	}

	var resp servicePushResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v; payload=%s", err, stdout.String())
	}
	if resp.CreatedCount != 1 {
		t.Fatalf("created_count = %d, want 1", resp.CreatedCount)
	}
	if resp.UpdatedCount != 0 {
		t.Fatalf("updated_count = %d, want 0", resp.UpdatedCount)
	}
	if len(resp.Warnings) == 0 || !strings.Contains(resp.Warnings[0], "beads-state.json") {
		t.Fatalf("warnings = %#v", resp.Warnings)
	}
}
