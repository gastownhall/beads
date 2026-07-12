package pluginprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestStoreForwardsBasicMethods(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, OpenOptions{
		Config: Config{
			Backend: "doltlite",
			Command: os.Args[0],
			Args:    []string{"-test.run=TestPluginProcessHelper", "--", "serve"},
		},
		BeadsDir: "/tmp/beads",
		Database: "beads",
		Branch:   "main",
		ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	value, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if value != "bd" {
		t.Fatalf("GetConfig = %q, want bd", value)
	}
	raw, err := store.ExecuteRawSQL(ctx, "SELECT id FROM issues")
	if err != nil {
		t.Fatalf("ExecuteRawSQL: %v", err)
	}
	if !raw.Read || len(raw.Columns) != 1 || raw.Columns[0] != "id" || len(raw.Rows) != 1 || raw.Rows[0]["id"] != "bd-1" {
		t.Fatalf("ExecuteRawSQL = %#v, want one id row", raw)
	}

	issue := &types.Issue{ID: "bd-1", Title: "plugin issue", Status: types.StatusOpen}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	got, err := store.GetIssue(ctx, "bd-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.ID != "bd-1" {
		t.Fatalf("GetIssue ID = %q, want bd-1", got.ID)
	}
	if err := store.AddLabel(ctx, "bd-1", "plugin", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	labels, err := store.GetLabels(ctx, "bd-1")
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "plugin" {
		t.Fatalf("GetLabels = %#v, want [plugin]", labels)
	}
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "bd-1" {
		t.Fatalf("GetReadyWork = %#v, want bd-1", ready)
	}
	withCounts, err := store.SearchIssuesWithCounts(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssuesWithCounts: %v", err)
	}
	if len(withCounts) != 1 || withCounts[0].Issue.ID != "bd-1" || withCounts[0].DependencyCount != 2 {
		t.Fatalf("SearchIssuesWithCounts = %#v, want bd-1 with dependency count", withCounts)
	}
	readyWithCounts, err := store.GetReadyWorkWithCounts(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWorkWithCounts: %v", err)
	}
	if len(readyWithCounts) != 1 || readyWithCounts[0].Issue.ID != "bd-1" || readyWithCounts[0].CommentCount != 3 {
		t.Fatalf("GetReadyWorkWithCounts = %#v, want bd-1 with comment count", readyWithCounts)
	}
	if _, err := store.Sync(ctx, "origin", "merge"); err == nil {
		t.Fatal("Sync succeeded, want unsupported error")
	}
	if err := store.Commit(ctx, "test commit"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestPluginProcessHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-1] != "serve" {
		return
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(response{
		OK: true,
		Result: mustJSON(hello{
			Protocol: protocolVersion,
			Backend:  "doltlite",
		}),
	}); err != nil {
		panic(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = enc.Encode(response{ID: req.ID, OK: false, Error: &protocolError{Code: "bad_request", Message: err.Error()}})
			continue
		}
		_ = enc.Encode(handleTestRequest(req))
	}
	os.Exit(0)
}

func handleTestRequest(req request) response {
	switch req.Method {
	case "open":
		var p openParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return testError(req.ID, "bad_params", err)
		}
		if p.BeadsDir != "/tmp/beads" || p.Database != "beads" || p.Branch != "main" || !p.ReadOnly {
			return testError(req.ID, "bad_open", fmt.Errorf("%+v", p))
		}
		return testOK(req.ID, openResult{SessionID: "session-1"})
	case "close":
		return testOK(req.ID, map[string]bool{"closed": true})
	case "set_config":
		return testOK(req.ID, map[string]string{"key": "issue_prefix"})
	case "get_config":
		return testOK(req.ID, map[string]string{"key": "issue_prefix", "value": "bd"})
	case "raw_sql":
		return testOK(req.ID, rawSQLResult{
			Columns: []string{"id"},
			Rows:    []map[string]interface{}{{"id": "bd-1"}},
			Read:    true,
		})
	case "create_issue", "get_issue", "update_issue":
		return testOK(req.ID, &types.Issue{ID: "bd-1", Title: "plugin issue", Status: types.StatusOpen})
	case "search_issues", "ready_work":
		return testOK(req.ID, []*types.Issue{{ID: "bd-1", Title: "plugin issue", Status: types.StatusOpen}})
	case "search_issues_with_counts":
		return testOK(req.ID, []*types.IssueWithCounts{{Issue: &types.Issue{ID: "bd-1", Title: "plugin issue", Status: types.StatusOpen}, DependencyCount: 2}})
	case "ready_work_with_counts":
		return testOK(req.ID, []*types.IssueWithCounts{{Issue: &types.Issue{ID: "bd-1", Title: "plugin issue", Status: types.StatusOpen}, CommentCount: 3}})
	case "add_label", "get_labels":
		return testOK(req.ID, []string{"plugin"})
	case "commit":
		return testOK(req.ID, map[string]bool{"committed": true})
	default:
		return testError(req.ID, "unknown_method", fmt.Errorf("%s", req.Method))
	}
}

func testOK(id string, payload any) response {
	return response{ID: id, OK: true, Result: mustJSON(payload)}
}

func testError(id, code string, err error) response {
	return response{ID: id, OK: false, Error: &protocolError{Code: code, Message: err.Error()}}
}

func mustJSON(payload any) json.RawMessage {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return data
}
