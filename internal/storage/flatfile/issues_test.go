package flatfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var ctx = context.Background()

func TestCreateAndGetIssue(t *testing.T) {
	s := newTestStore(t)

	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test issue",
		Description: "A test issue",
		Priority:    2,
	}

	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	got, err := s.GetIssue(ctx, "test-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	if got.Title != "Test issue" {
		t.Errorf("Title = %q, want %q", got.Title, "Test issue")
	}
	if got.Status != types.StatusOpen {
		t.Errorf("Status = %q, want %q", got.Status, types.StatusOpen)
	}
	if got.CreatedBy != "tester" {
		t.Errorf("CreatedBy = %q, want %q", got.CreatedBy, "tester")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
}

func TestCreateIssueDuplicate(t *testing.T) {
	s := newTestStore(t)

	issue := &types.Issue{ID: "dup-1", Title: "First"}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	issue2 := &types.Issue{ID: "dup-1", Title: "Second"}
	if err := s.CreateIssue(ctx, issue2, "tester"); err == nil {
		t.Error("CreateIssue duplicate: expected error, got nil")
	}
}

func TestCreateIssues(t *testing.T) {
	s := newTestStore(t)

	// CreateIssues mirrors sqlkit and validates ID prefixes against config.
	if err := s.SetConfig(ctx, "issue_prefix", "batch"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	issues := []*types.Issue{
		{ID: "batch-1", Title: "First"},
		{ID: "batch-2", Title: "Second"},
		{ID: "batch-3", Title: "Third"},
	}

	if err := s.CreateIssues(ctx, issues, "tester"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}

	for _, id := range []string{"batch-1", "batch-2", "batch-3"} {
		if _, err := s.GetIssue(ctx, id); err != nil {
			t.Errorf("GetIssue(%s): %v", id, err)
		}
	}
}

func TestGetIssueNotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetIssue(ctx, "nonexistent")
	if err != storage.ErrNotFound {
		t.Errorf("GetIssue missing = %v, want ErrNotFound", err)
	}
}

func TestGetIssueByExternalRef(t *testing.T) {
	s := newTestStore(t)

	ref := "gh-42"
	issue := &types.Issue{ID: "ext-1", Title: "External", ExternalRef: &ref}
	s.CreateIssue(ctx, issue, "tester")

	got, err := s.GetIssueByExternalRef(ctx, "gh-42")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef: %v", err)
	}
	if got.ID != "ext-1" {
		t.Errorf("ID = %q, want %q", got.ID, "ext-1")
	}

	_, err = s.GetIssueByExternalRef(ctx, "gh-999")
	if err != storage.ErrNotFound {
		t.Errorf("missing ref = %v, want ErrNotFound", err)
	}
}

func TestGetIssuesByIDs(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "a-1", Title: "A"}, "tester")
	s.CreateIssue(ctx, &types.Issue{ID: "a-2", Title: "B"}, "tester")

	got, err := s.GetIssuesByIDs(ctx, []string{"a-1", "missing", "a-2"})
	if err != nil {
		t.Fatalf("GetIssuesByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestUpdateIssue(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "upd-1", Title: "Original", Priority: 1}, "tester")

	err := s.UpdateIssue(ctx, "upd-1", map[string]interface{}{
		"title":    "Updated",
		"priority": 3,
	}, "tester")
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	got, _ := s.GetIssue(ctx, "upd-1")
	if got.Title != "Updated" {
		t.Errorf("Title = %q, want %q", got.Title, "Updated")
	}
	if got.Priority != 3 {
		t.Errorf("Priority = %d, want 3", got.Priority)
	}
}

func TestUpdateIssueNotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.UpdateIssue(ctx, "missing", map[string]interface{}{"title": "x"}, "tester")
	if err != storage.ErrNotFound {
		t.Errorf("UpdateIssue missing = %v, want ErrNotFound", err)
	}
}

func TestUpdateIssueType(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "type-1", Title: "Task", IssueType: "task"}, "tester")

	if err := s.UpdateIssueType(ctx, "type-1", "epic", "tester"); err != nil {
		t.Fatalf("UpdateIssueType: %v", err)
	}

	got, _ := s.GetIssue(ctx, "type-1")
	if got.IssueType != "epic" {
		t.Errorf("IssueType = %q, want %q", got.IssueType, "epic")
	}
}

func TestCloseIssue(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "close-1", Title: "Will close"}, "tester")

	if err := s.CloseIssue(ctx, "close-1", "done", "closer", "session-123"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	got, _ := s.GetIssue(ctx, "close-1")
	if got.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q", got.Status, types.StatusClosed)
	}
	if got.ClosedAt == nil {
		t.Error("ClosedAt is nil")
	}
	if got.CloseReason != "done" {
		t.Errorf("CloseReason = %q, want %q", got.CloseReason, "done")
	}
	if got.ClosedBySession != "session-123" {
		t.Errorf("ClosedBySession = %q, want %q", got.ClosedBySession, "session-123")
	}
}

func TestReopenIssue(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "reopen-1", Title: "Reopen me"}, "tester")
	s.CloseIssue(ctx, "reopen-1", "done", "closer", "sess")

	if err := s.ReopenIssue(ctx, "reopen-1", "not done", "opener"); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}

	got, _ := s.GetIssue(ctx, "reopen-1")
	if got.Status != types.StatusOpen {
		t.Errorf("Status = %q, want %q", got.Status, types.StatusOpen)
	}
	if got.ClosedAt != nil {
		t.Error("ClosedAt should be nil after reopen")
	}
}

func TestDeleteIssue(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "del-1", Title: "Delete me"}, "tester")

	if err := s.DeleteIssue(ctx, "del-1"); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}

	_, err := s.GetIssue(ctx, "del-1")
	if err != storage.ErrNotFound {
		t.Errorf("after delete: %v, want ErrNotFound", err)
	}
}

func TestDeleteIssueNotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.DeleteIssue(ctx, "nonexistent")
	if err != storage.ErrNotFound {
		t.Errorf("DeleteIssue missing = %v, want ErrNotFound", err)
	}
}

func TestUpdatePreservesTimestamps(t *testing.T) {
	s := newTestStore(t)

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	issue := &types.Issue{
		ID:        "ts-1",
		Title:     "Timestamps",
		CreatedAt: created,
		UpdatedAt: created,
		CreatedBy: "original",
	}
	s.CreateIssue(ctx, issue, "original")

	s.UpdateIssue(ctx, "ts-1", map[string]interface{}{"title": "Changed"}, "updater")

	got, _ := s.GetIssue(ctx, "ts-1")
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt changed: got %v, want %v", got.CreatedAt, created)
	}
	if got.CreatedBy != "original" {
		t.Errorf("CreatedBy changed: got %q, want %q", got.CreatedBy, "original")
	}
	if !got.UpdatedAt.After(created) {
		t.Error("UpdatedAt should be after original timestamp")
	}
}

func TestOperationsAfterClose(t *testing.T) {
	s := newTestStore(t)
	s.Close()

	if err := s.CreateIssue(ctx, &types.Issue{ID: "x"}, ""); err == nil {
		t.Error("CreateIssue after Close: expected error")
	}
	if _, err := s.GetIssue(ctx, "x"); err == nil {
		t.Error("GetIssue after Close: expected error")
	}
	if err := s.UpdateIssue(ctx, "x", nil, ""); err == nil {
		t.Error("UpdateIssue after Close: expected error")
	}
	if err := s.DeleteIssue(ctx, "x"); err == nil {
		t.Error("DeleteIssue after Close: expected error")
	}
}

func TestCreateIssueAutoID(t *testing.T) {
	s := newTestStore(t)

	// Set prefix so ID generation works.
	s.SetConfig(ctx, "issue_prefix", "test")

	issue := &types.Issue{
		Title:       "Auto-generated ID",
		Description: "Should get a hash-based ID",
	}

	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue with auto ID: %v", err)
	}

	if issue.ID == "" {
		t.Fatal("issue.ID should be set after CreateIssue")
	}
	if !strings.HasPrefix(issue.ID, "test-") {
		t.Errorf("issue.ID = %q, want prefix 'test-'", issue.ID)
	}

	// Verify it was persisted.
	got, err := s.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", issue.ID, err)
	}
	if got.Title != "Auto-generated ID" {
		t.Errorf("title = %q", got.Title)
	}
}

func TestCreateIssueAutoIDUnique(t *testing.T) {
	s := newTestStore(t)
	s.SetConfig(ctx, "issue_prefix", "uniq")

	ids := make(map[string]bool)
	for i := 0; i < 20; i++ {
		issue := &types.Issue{
			Title:       fmt.Sprintf("Issue %d", i),
			Description: fmt.Sprintf("Description %d", i),
		}
		if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %d: %v", i, err)
		}
		if ids[issue.ID] {
			t.Fatalf("duplicate ID: %s", issue.ID)
		}
		ids[issue.ID] = true
	}
}

func TestCreateIssueNoPrefix(t *testing.T) {
	s := newTestStore(t)
	// Don't set prefix.

	issue := &types.Issue{Title: "No prefix"}
	err := s.CreateIssue(ctx, issue, "tester")
	if err == nil {
		t.Fatal("expected error when no prefix configured")
	}
	if !strings.Contains(err.Error(), "issue_prefix") {
		t.Errorf("error = %q, want mention of issue_prefix", err)
	}
}

func TestReadIssueMySQLTimestamp(t *testing.T) {
	s := newTestStore(t)

	// Write a JSON file with MySQL-format datetime in a dependency.
	issueJSON := `{
  "id": "ts-mysql-1",
  "title": "Has MySQL timestamps",
  "status": "open",
  "priority": 2,
  "created_at": "2026-06-22T21:02:52Z",
  "updated_at": "2026-06-22T21:02:52Z",
  "dependencies": [
    {
      "issue_id": "ts-mysql-1",
      "depends_on_id": "ts-mysql-2",
      "type": "blocks",
      "created_at": "2026-06-22 14:31:53"
    }
  ]
}
`
	os.WriteFile(s.issueFilename("ts-mysql-1"), []byte(issueJSON), 0o644)

	got, err := s.readIssue("ts-mysql-1")
	if err != nil {
		t.Fatalf("readIssue with MySQL timestamp: %v", err)
	}
	if len(got.Dependencies) != 1 {
		t.Fatalf("got %d dependencies, want 1", len(got.Dependencies))
	}
	dep := got.Dependencies[0]
	want := time.Date(2026, 6, 22, 14, 31, 53, 0, time.UTC)
	if !dep.CreatedAt.Equal(want) {
		t.Errorf("dep.CreatedAt = %v, want %v", dep.CreatedAt, want)
	}
}

func TestRepairMySQLTimestamps(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		isNil bool
	}{
		{
			name:  "no timestamps to repair",
			input: `{"created_at": "2026-06-22T14:31:53Z"}`,
			isNil: true,
		},
		{
			name:  "mysql datetime",
			input: `{"created_at": "2026-06-22 14:31:53"}`,
			want:  `{"created_at": "2026-06-22T14:31:53Z"}`,
		},
		{
			name:  "multiple mysql datetimes",
			input: `{"a": "2026-01-01 00:00:00", "b": "2026-12-31 23:59:59"}`,
			want:  `{"a": "2026-01-01T00:00:00Z", "b": "2026-12-31T23:59:59Z"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repairMySQLTimestamps([]byte(tt.input))
			if tt.isNil {
				if got != nil {
					t.Errorf("got %q, want nil", got)
				}
				return
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRepairIssueTypes(t *testing.T) {
	t.Run("no changes needed", func(t *testing.T) {
		input := `{"id":"x","ephemeral":false,"pinned":true}`
		got := repairIssueTypes([]byte(input))
		if got != nil {
			t.Errorf("expected nil for correct JSON, got %q", got)
		}
	})

	t.Run("integer booleans", func(t *testing.T) {
		input := `{"id":"x","ephemeral":0,"is_template":1,"pinned":0,"no_history":0}`
		got := repairIssueTypes([]byte(input))
		if got == nil {
			t.Fatal("expected repaired JSON, got nil")
		}
		var m map[string]interface{}
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("unmarshal repaired: %v", err)
		}
		if m["ephemeral"] != false {
			t.Errorf("ephemeral = %v, want false", m["ephemeral"])
		}
		if m["is_template"] != true {
			t.Errorf("is_template = %v, want true", m["is_template"])
		}
	})

	t.Run("dependency metadata object to string", func(t *testing.T) {
		input := `{"id":"x","dependencies":[{"issue_id":"a","depends_on_id":"b","type":"blocks","metadata":{},"created_at":"2026-01-01T00:00:00Z"}]}`
		got := repairIssueTypes([]byte(input))
		if got == nil {
			t.Fatal("expected repaired JSON, got nil")
		}
		var issue types.Issue
		if err := json.Unmarshal(got, &issue); err != nil {
			t.Fatalf("unmarshal into Issue: %v", err)
		}
		if len(issue.Dependencies) != 1 {
			t.Fatalf("got %d deps, want 1", len(issue.Dependencies))
		}
		if issue.Dependencies[0].Metadata != "{}" {
			t.Errorf("metadata = %q, want %q", issue.Dependencies[0].Metadata, "{}")
		}
	})

	t.Run("timeout_ns renamed to timeout", func(t *testing.T) {
		input := `{"id":"x","timeout_ns":5000000000}`
		got := repairIssueTypes([]byte(input))
		if got == nil {
			t.Fatal("expected repaired JSON, got nil")
		}
		var m map[string]interface{}
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("unmarshal repaired: %v", err)
		}
		if _, ok := m["timeout_ns"]; ok {
			t.Error("timeout_ns should have been removed")
		}
		if m["timeout"] != 5000000000.0 {
			t.Errorf("timeout = %v, want 5000000000", m["timeout"])
		}
	})

	t.Run("content_hash removed", func(t *testing.T) {
		input := `{"id":"x","content_hash":"abc123"}`
		got := repairIssueTypes([]byte(input))
		if got == nil {
			t.Fatal("expected repaired JSON, got nil")
		}
		var m map[string]interface{}
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("unmarshal repaired: %v", err)
		}
		if _, ok := m["content_hash"]; ok {
			t.Error("content_hash should have been removed")
		}
	})
}

func TestReadIssueIntegerBooleans(t *testing.T) {
	s := newTestStore(t)

	// Simulate a raw Dolt SQL export with integer booleans and object metadata.
	issueJSON := `{
  "id": "corrupt-1",
  "title": "Integer booleans",
  "status": "open",
  "priority": 2,
  "ephemeral": 0,
  "is_template": 0,
  "no_history": 0,
  "pinned": 0,
  "timeout_ns": 0,
  "content_hash": "abc123",
  "created_at": "2026-06-22T21:02:52Z",
  "updated_at": "2026-06-22T21:02:52Z",
  "metadata": {},
  "dependencies": [
    {
      "issue_id": "corrupt-1",
      "depends_on_id": "corrupt-2",
      "type": "relates-to",
      "created_at": "2026-06-22T21:02:52Z",
      "metadata": {}
    }
  ]
}
`
	os.WriteFile(s.issueFilename("corrupt-1"), []byte(issueJSON), 0o644)

	got, err := s.readIssue("corrupt-1")
	if err != nil {
		t.Fatalf("readIssue with integer booleans: %v", err)
	}
	if got.Ephemeral {
		t.Error("ephemeral should be false")
	}
	if got.IsTemplate {
		t.Error("is_template should be false")
	}
	if len(got.Dependencies) != 1 {
		t.Fatalf("got %d dependencies, want 1", len(got.Dependencies))
	}
	if got.Dependencies[0].Metadata != "{}" {
		t.Errorf("dep metadata = %q, want %q", got.Dependencies[0].Metadata, "{}")
	}
}

// Extends TestReadIssueIntegerBooleans: a raw Dolt SQL export row carries BOTH
// MySQL datetimes and TINYINT booleans (both repairs cite the same source, so
// co-occurrence is the expected case). The repairs must compose — a file
// needing both must not be dropped as corrupt.
func TestReadIssueCombinedTimestampAndTypeRepairs(t *testing.T) {
	s := newTestStore(t)

	issueJSON := `{
  "id": "corrupt-both-1",
  "title": "Combined repairs",
  "status": "open",
  "priority": 2,
  "ephemeral": 0,
  "is_template": 0,
  "pinned": 1,
  "content_hash": "abc123",
  "created_at": "2026-06-22 14:31:53",
  "updated_at": "2026-06-22 14:31:53",
  "dependencies": [
    {
      "issue_id": "corrupt-both-1",
      "depends_on_id": "corrupt-2",
      "type": "relates-to",
      "created_at": "2026-06-22 14:31:53",
      "metadata": {}
    }
  ]
}
`
	if err := os.WriteFile(s.issueFilename("corrupt-both-1"), []byte(issueJSON), 0o644); err != nil {
		t.Fatalf("write issue file: %v", err)
	}

	got, err := s.readIssue("corrupt-both-1")
	if err != nil {
		t.Fatalf("readIssue with combined MySQL datetimes and int booleans: %v", err)
	}
	want := time.Date(2026, 6, 22, 14, 31, 53, 0, time.UTC)
	if !got.CreatedAt.Equal(want) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, want)
	}
	if got.Ephemeral {
		t.Error("ephemeral should be false")
	}
	if !got.Pinned {
		t.Error("pinned should be true")
	}
	if len(got.Dependencies) != 1 {
		t.Fatalf("got %d dependencies, want 1", len(got.Dependencies))
	}
	if got.Dependencies[0].Metadata != "{}" {
		t.Errorf("dep metadata = %q, want %q", got.Dependencies[0].Metadata, "{}")
	}
	if !got.Dependencies[0].CreatedAt.Equal(want) {
		t.Errorf("dep created_at = %v, want %v", got.Dependencies[0].CreatedAt, want)
	}

	// The combined file must also survive loadAllIssues (no corrupt-skip).
	issues, err := s.loadAllIssues()
	if err != nil {
		t.Fatalf("loadAllIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "corrupt-both-1" {
		t.Fatalf("loadAllIssues = %d issues, want the repaired one", len(issues))
	}
}

func TestLoadAllIssuesSkipsCorrupt(t *testing.T) {
	s := newTestStore(t)

	// Write a valid issue.
	good := &types.Issue{
		ID:       "good-1",
		Title:    "Good issue",
		Priority: 1,
	}
	if err := s.CreateIssue(ctx, good, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Write a completely unparseable file.
	os.WriteFile(s.issueFilename("bad-1"), []byte(`{not json at all`), 0o644)

	issues, err := s.loadAllIssues()
	if err != nil {
		t.Fatalf("loadAllIssues should not fail on corrupt file: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("got %d issues, want 1 (corrupt file should be skipped)", len(issues))
	}
	if issues[0].ID != "good-1" {
		t.Errorf("got ID %q, want %q", issues[0].ID, "good-1")
	}
}

func TestCloseIssueDoesNotAssignActor(t *testing.T) {
	s := newTestStore(t)

	// issueops.CloseIssueInTx (the SQL reference) never writes assignee on
	// close; an unassigned issue must stay unassigned after CloseIssue.
	s.CreateIssue(ctx, &types.Issue{ID: "close-noassign", Title: "Unassigned"}, "tester")

	if err := s.CloseIssue(ctx, "close-noassign", "done", "closer", "sess"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	got, err := s.GetIssue(ctx, "close-noassign")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Assignee != "" {
		t.Errorf("Assignee = %q, want empty (close must not assign the closer)", got.Assignee)
	}
}

func TestCreateIssueWispIDPrefix(t *testing.T) {
	s := newTestStore(t)

	if err := s.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	// SQL create (issueops.CreateIssueInTxWithResult) mints ConfigPrefix+"-wisp"
	// for ephemeral issues with no explicit ID; flatfile must match.
	wisp := &types.Issue{Title: "Ephemeral wisp", Ephemeral: true}
	if err := s.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if !strings.HasPrefix(wisp.ID, "bd-wisp-") {
		t.Errorf("wisp ID = %q, want prefix %q", wisp.ID, "bd-wisp-")
	}

	// Non-wisp issues keep the plain prefix.
	plain := &types.Issue{Title: "Plain issue"}
	if err := s.CreateIssue(ctx, plain, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if strings.HasPrefix(plain.ID, "bd-wisp-") {
		t.Errorf("plain ID = %q, must not carry -wisp segment", plain.ID)
	}
}

func TestUpdateIssueTypedEnumValues(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "typed-1", Title: "Typed enums"}, "tester")

	// Real callers (merge_slot, mol_current) pass types.Status, not string;
	// the SQL path binds the value generically so it persists there.
	if err := s.UpdateIssue(ctx, "typed-1", map[string]interface{}{"status": types.StatusInProgress}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(status): %v", err)
	}
	got, err := s.GetIssue(ctx, "typed-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Status != types.StatusInProgress {
		t.Errorf("Status = %q, want %q", got.Status, types.StatusInProgress)
	}
	if got.StartedAt == nil {
		t.Error("StartedAt not stamped on typed in_progress transition")
	}

	if err := s.UpdateIssue(ctx, "typed-1", map[string]interface{}{"issue_type": types.TypeEpic}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(issue_type): %v", err)
	}
	if err := s.UpdateIssue(ctx, "typed-1", map[string]interface{}{"wisp_type": types.WispType("checkpoint")}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(wisp_type): %v", err)
	}
	got, _ = s.GetIssue(ctx, "typed-1")
	if got.IssueType != types.TypeEpic {
		t.Errorf("IssueType = %q, want %q", got.IssueType, types.TypeEpic)
	}
	if got.WispType != types.WispType("checkpoint") {
		t.Errorf("WispType = %q, want %q", got.WispType, "checkpoint")
	}

	// Typed close must reach the closed_at auto-management too.
	if err := s.UpdateIssue(ctx, "typed-1", map[string]interface{}{"status": types.StatusClosed}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(close): %v", err)
	}
	got, _ = s.GetIssue(ctx, "typed-1")
	if got.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q", got.Status, types.StatusClosed)
	}
	if got.ClosedAt == nil {
		t.Error("ClosedAt not stamped on typed close")
	}
}

func TestUpdateIssueGateAndMessagingFields(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "gate-1", Title: "A gate"}, "tester")

	// The SQL path persists all of these as columns (waiters JSON-marshaled);
	// real callers: bd gate wait (waiters), gate discovery (await_id).
	updates := map[string]interface{}{
		"waiters":  []string{"alice@example.com", "bob@example.com"},
		"await_id": "run-42",
		"sender":   "mailer",
		"mol_type": types.MolTypeSwarm,
	}
	if err := s.UpdateIssue(ctx, "gate-1", updates, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	got, err := s.GetIssue(ctx, "gate-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if len(got.Waiters) != 2 || got.Waiters[0] != "alice@example.com" || got.Waiters[1] != "bob@example.com" {
		t.Errorf("Waiters = %v, want the two persisted addresses", got.Waiters)
	}
	if got.AwaitID != "run-42" {
		t.Errorf("AwaitID = %q, want %q", got.AwaitID, "run-42")
	}
	if got.Sender != "mailer" {
		t.Errorf("Sender = %q, want %q", got.Sender, "mailer")
	}
	if got.MolType != types.MolTypeSwarm {
		t.Errorf("MolType = %q, want %q", got.MolType, types.MolTypeSwarm)
	}

	// Clearing waiters with nil mirrors writing NULL/empty on SQL.
	if err := s.UpdateIssue(ctx, "gate-1", map[string]interface{}{"waiters": nil}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(clear waiters): %v", err)
	}
	got, _ = s.GetIssue(ctx, "gate-1")
	if len(got.Waiters) != 0 {
		t.Errorf("Waiters = %v, want empty after clear", got.Waiters)
	}
}

func TestUpdateIssueUnhandledAllowedFieldErrors(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "evt-1", Title: "Event fields"}, "tester")

	// event_category passes IsAllowedUpdateField but no backend has such a
	// column: the SQL UPDATE fails, so flatfile must error too instead of
	// reporting success while dropping the value.
	err := s.UpdateIssue(ctx, "evt-1", map[string]interface{}{"event_category": "patrol"}, "tester")
	if err == nil {
		t.Fatal("UpdateIssue(event_category): expected error, got nil")
	}
}

func TestUpdateIssueManagesLease(t *testing.T) {
	s := newTestStore(t)

	s.CreateIssue(ctx, &types.Issue{ID: "lease-1", Title: "Lease me"}, "tester")

	// Direct in_progress+assignee update stamps a fresh lease (SQL
	// issueops.ManageLeaseOnUpdate parity) so ReclaimExpiredLeases can
	// recover the issue if the worker dies.
	before := time.Now().UTC()
	updates := map[string]interface{}{
		"status":   "in_progress",
		"assignee": "bob",
	}
	if err := s.UpdateIssue(ctx, "lease-1", updates, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	got, err := s.GetIssue(ctx, "lease-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.LeaseExpiresAt == nil {
		t.Fatal("LeaseExpiresAt is nil after in_progress+assignee update")
	}
	if !got.LeaseExpiresAt.After(before) {
		t.Errorf("LeaseExpiresAt = %v, want after %v", got.LeaseExpiresAt, before)
	}
	if got.HeartbeatAt == nil {
		t.Fatal("HeartbeatAt is nil after in_progress+assignee update")
	}

	// Returning the issue to open clears the now-stale lease fields.
	if err := s.UpdateIssue(ctx, "lease-1", map[string]interface{}{"status": "open"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(open): %v", err)
	}
	got, _ = s.GetIssue(ctx, "lease-1")
	if got.LeaseExpiresAt != nil || got.HeartbeatAt != nil {
		t.Errorf("lease fields not cleared on reopen: lease=%v heartbeat=%v", got.LeaseExpiresAt, got.HeartbeatAt)
	}

	// in_progress without an assignee is not leased work: no lease stamped.
	if err := s.UpdateIssue(ctx, "lease-1", map[string]interface{}{"assignee": "", "status": "in_progress"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(in_progress, no assignee): %v", err)
	}
	got, _ = s.GetIssue(ctx, "lease-1")
	if got.LeaseExpiresAt != nil || got.HeartbeatAt != nil {
		t.Errorf("lease stamped for unassigned in_progress: lease=%v heartbeat=%v", got.LeaseExpiresAt, got.HeartbeatAt)
	}
}

func TestCreateIssueValidates(t *testing.T) {
	s := newTestStore(t)

	// SQL backends run PrepareIssueForInsert → ValidateWithCustom on every
	// create; flatfile must reject the same inputs instead of persisting them.
	cases := []struct {
		name  string
		issue *types.Issue
	}{
		{"invalid status", &types.Issue{ID: "val-1", Title: "x", Status: types.Status("bogus")}},
		{"invalid issue type", &types.Issue{ID: "val-2", Title: "x", IssueType: types.IssueType("tsak")}},
		{"priority out of range", &types.Issue{ID: "val-3", Title: "x", Priority: 9}},
		{"malformed metadata", &types.Issue{ID: "val-4", Title: "x", Metadata: json.RawMessage("{oops")}},
		{"missing title", &types.Issue{ID: "val-5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.CreateIssue(ctx, tc.issue, "tester"); err == nil {
				t.Fatal("CreateIssue: expected validation error, got nil")
			}
			if _, err := s.GetIssue(ctx, tc.issue.ID); err != storage.ErrNotFound {
				t.Errorf("invalid issue persisted: GetIssue err = %v, want ErrNotFound", err)
			}
		})
	}

	// A configured custom type is accepted, matching ValidateWithCustom.
	if err := s.SetConfig(ctx, "types.custom", "research"); err != nil {
		t.Fatalf("SetConfig(types.custom): %v", err)
	}
	custom := &types.Issue{ID: "val-ok", Title: "x", IssueType: types.IssueType("research")}
	if err := s.CreateIssue(ctx, custom, "tester"); err != nil {
		t.Fatalf("CreateIssue(custom type): %v", err)
	}

	// The batch path validates every member before writing anything.
	batch := []*types.Issue{
		{ID: "val-b1", Title: "fine"},
		{ID: "val-b2", Title: "bad", Status: types.Status("bogus")},
	}
	if err := s.CreateIssues(ctx, batch, "tester"); err == nil {
		t.Fatal("CreateIssues: expected validation error, got nil")
	}
	if _, err := s.GetIssue(ctx, "val-b1"); err != storage.ErrNotFound {
		t.Errorf("batch member written despite failed batch: err = %v, want ErrNotFound", err)
	}
}

func TestRepairIssueTimestampsPreservesUserContent(t *testing.T) {
	s := newTestStore(t)

	// A Dolt/MySQL export with a broken created_at AND user content that is
	// exactly a datetime string. The repair must fix only the timestamp
	// fields; title/description/metadata are user data and stay verbatim.
	const datetime = "2026-06-22 14:31:53"
	raw := `{
  "id": "repair-1",
  "title": "` + datetime + `",
  "description": "deployed at ` + datetime + ` UTC",
  "status": "open",
  "metadata": {"note": "` + datetime + `"},
  "created_at": "` + datetime + `",
  "updated_at": "` + datetime + `",
  "comments": [{"id": "c1", "issue_id": "repair-1", "author": "a", "text": "x", "created_at": "` + datetime + `"}]
}
`
	if err := os.WriteFile(s.issueFilename("repair-1"), []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := s.GetIssue(ctx, "repair-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	want := time.Date(2026, 6, 22, 14, 31, 53, 0, time.UTC)
	if !got.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v (repaired)", got.CreatedAt, want)
	}
	if got.Title != datetime {
		t.Errorf("Title = %q, want %q (user content must not be rewritten)", got.Title, datetime)
	}
	if !strings.Contains(got.Description, datetime) {
		t.Errorf("Description = %q, want embedded %q preserved", got.Description, datetime)
	}
	if !strings.Contains(string(got.Metadata), datetime) {
		t.Errorf("Metadata = %q, want %q preserved", got.Metadata, datetime)
	}
	if len(got.Comments) != 1 || !got.Comments[0].CreatedAt.Equal(want) {
		t.Errorf("embedded comment created_at not repaired: %+v", got.Comments)
	}
}

// Oracle: the SQL reference (issueops updateIssueInTx) binds the raw update
// value, so assignee=nil becomes SQL NULL and clears the column, while
// issueops.ManageLeaseOnUpdate's explicit nil case clears the lease. Both
// halves must agree: after {"assignee": nil} the issue is unassigned AND
// lease-free, never an assigned issue with no lease.
func TestUpdateIssueNilAssigneeClears(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateIssue(ctx, &types.Issue{ID: "nilassign-1", Title: "t"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	err := s.UpdateIssue(ctx, "nilassign-1", map[string]interface{}{
		"status":   string(types.StatusInProgress),
		"assignee": "alice",
	}, "tester")
	if err != nil {
		t.Fatalf("UpdateIssue claim: %v", err)
	}
	claimed, err := s.GetIssue(ctx, "nilassign-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if claimed.Assignee != "alice" || claimed.LeaseExpiresAt == nil {
		t.Fatalf("precondition: assignee=%q lease=%v, want alice with lease", claimed.Assignee, claimed.LeaseExpiresAt)
	}

	if err := s.UpdateIssue(ctx, "nilassign-1", map[string]interface{}{"assignee": nil}, "tester"); err != nil {
		t.Fatalf("UpdateIssue nil assignee: %v", err)
	}
	got, err := s.GetIssue(ctx, "nilassign-1")
	if err != nil {
		t.Fatalf("GetIssue after clear: %v", err)
	}
	if got.Assignee != "" {
		t.Errorf("assignee = %q, want cleared", got.Assignee)
	}
	if got.LeaseExpiresAt != nil || got.HeartbeatAt != nil {
		t.Errorf("lease not cleared: expires=%v heartbeat=%v", got.LeaseExpiresAt, got.HeartbeatAt)
	}
}

// Oracle: readKVFile propagates a json.Unmarshal error for a corrupt
// config_kv.json (e.g. one carrying git merge-conflict markers). CreateIssue's
// ID generation must surface that error, not collapse it into the misleading
// "issue_prefix not configured" which sends the user to re-run init.
func TestCreateIssueSurfacesCorruptConfigError(t *testing.T) {
	s := newTestStore(t)

	if err := os.WriteFile(s.configKVPath, []byte("<<<<<<< HEAD\n{}\n"), 0o644); err != nil {
		t.Fatalf("corrupt config: %v", err)
	}

	err := s.CreateIssue(ctx, &types.Issue{Title: "needs generated id"}, "tester")
	if err == nil {
		t.Fatal("CreateIssue should fail on corrupt config")
	}
	if strings.Contains(err.Error(), "issue_prefix not configured") {
		t.Fatalf("corrupt config masked as not-configured: %v", err)
	}
	if !strings.Contains(err.Error(), "parse kv") {
		t.Errorf("error should carry the parse failure, got: %v", err)
	}
}

// Oracle: sqlkit.CreateIssue wraps issue+event+comment inserts in one
// transaction (withMutationTx), so a failed create leaves nothing behind and a
// retry succeeds. Flat-file CreateIssue must match: if recording the created
// event fails after the issue file is written, the issue file must be cleaned
// up so the retry does not fail with "already exists".
func TestCreateIssueFailedEventDoesNotPoisonRetry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}
	s := newTestStore(t)

	// Make the events dir unwritable so recordEvent fails after writeIssue.
	if err := os.Chmod(s.eventsDir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.eventsDir, 0o755) })

	issue := &types.Issue{ID: "retry-1", Title: "first attempt"}
	if err := s.CreateIssue(ctx, issue, "tester"); err == nil {
		t.Fatal("CreateIssue should fail with unwritable events dir")
	}
	if _, err := s.GetIssue(ctx, "retry-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("half-created issue left behind: GetIssue err = %v, want ErrNotFound", err)
	}

	// Restore writability; the retry must succeed, not hit "already exists".
	if err := os.Chmod(s.eventsDir, 0o755); err != nil {
		t.Fatalf("Chmod restore: %v", err)
	}
	retry := &types.Issue{ID: "retry-1", Title: "second attempt"}
	if err := s.CreateIssue(ctx, retry, "tester"); err != nil {
		t.Fatalf("retry CreateIssue: %v", err)
	}
	got, err := s.GetIssue(ctx, "retry-1")
	if err != nil {
		t.Fatalf("GetIssue after retry: %v", err)
	}
	if got.Title != "second attempt" {
		t.Errorf("title = %q, want %q", got.Title, "second attempt")
	}
}
