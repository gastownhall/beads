//go:build cgo

package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestInboxImportIntegration tests the full import workflow:
// add inbox items via store, then import them via CLI, verify local issues created.
func TestInboxImportIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "inb")

	// Open store to add inbox items directly (simulating a received send)
	store := openStore(t, beadsDir, "inb")
	ctx := t.Context()

	items := []*types.InboxItem{
		{
			InboxID:         "test-inbox-001",
			SenderProjectID: "upstream",
			SenderIssueID:   "up-10",
			Title:           "Fix login bug",
			Description:     "Users can't log in",
			Priority:        1,
			IssueType:       "bug",
			SenderRef:       "beads://upstream/up-10",
		},
		{
			InboxID:         "test-inbox-002",
			SenderProjectID: "upstream",
			SenderIssueID:   "up-20",
			Title:           "Add caching",
			Description:     "Performance improvement",
			Priority:        2,
			IssueType:       "feature",
			SenderRef:       "beads://upstream/up-20",
		},
	}
	for _, item := range items {
		if err := store.AddInboxItem(ctx, item); err != nil {
			t.Fatalf("AddInboxItem(%s): %v", item.InboxID, err)
		}
	}
	store.Close()

	// Run bd inbox list --json
	listOut := bdRun(t, bd, dir, "handoff", "inbox", "list", "--json")
	var pending []*types.InboxItem
	if err := json.Unmarshal([]byte(listOut), &pending); err != nil {
		t.Fatalf("parse inbox list JSON: %v\n%s", err, listOut)
	}
	if len(pending) != 2 {
		t.Fatalf("inbox list returned %d items, want 2", len(pending))
	}

	// Import all pending items
	importOut := bdRun(t, bd, dir, "handoff", "inbox", "import", "--json")
	var importResult map[string]interface{}
	if err := json.Unmarshal([]byte(importOut), &importResult); err != nil {
		t.Fatalf("parse import JSON: %v\n%s", err, importOut)
	}
	if int(importResult["imported"].(float64)) != 2 {
		t.Errorf("imported = %v, want 2", importResult["imported"])
	}

	// Verify inbox is now empty
	listOut2 := bdRun(t, bd, dir, "handoff", "inbox", "list", "--json")
	var pending2 []*types.InboxItem
	if err := json.Unmarshal([]byte(listOut2), &pending2); err != nil {
		t.Fatalf("parse inbox list JSON: %v\n%s", err, listOut2)
	}
	if len(pending2) != 0 {
		t.Errorf("inbox should be empty after import, got %d items", len(pending2))
	}
}

// TestInboxImportSpecificItem tests importing a single item by ID prefix.
func TestInboxImportSpecificItem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "isi")

	store := openStore(t, beadsDir, "isi")
	ctx := t.Context()

	if err := store.AddInboxItem(ctx, &types.InboxItem{
		InboxID:         "aaa-specific-001",
		SenderProjectID: "proj-a",
		SenderIssueID:   "a-1",
		Title:           "First item",
		Priority:        1,
		IssueType:       "task",
		SenderRef:       "beads://proj-a/a-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddInboxItem(ctx, &types.InboxItem{
		InboxID:         "bbb-specific-002",
		SenderProjectID: "proj-b",
		SenderIssueID:   "b-1",
		Title:           "Second item",
		Priority:        2,
		IssueType:       "task",
		SenderRef:       "beads://proj-b/b-1",
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Import only the first item by prefix
	importOut := bdRun(t, bd, dir, "handoff", "inbox", "import", "aaa", "--json")
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(importOut), &result); err != nil {
		t.Fatalf("parse import JSON: %v\n%s", err, importOut)
	}
	if int(result["imported"].(float64)) != 1 {
		t.Errorf("imported = %v, want 1", result["imported"])
	}

	// Second item should still be pending
	listOut := bdRun(t, bd, dir, "handoff", "inbox", "list", "--json")
	var pending []*types.InboxItem
	if err := json.Unmarshal([]byte(listOut), &pending); err != nil {
		t.Fatalf("parse list JSON: %v\n%s", err, listOut)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 remaining pending item, got %d", len(pending))
	}
}

// TestInboxRejectIntegration tests rejecting an inbox item.
func TestInboxRejectIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rej")

	store := openStore(t, beadsDir, "rej")
	ctx := t.Context()

	if err := store.AddInboxItem(ctx, &types.InboxItem{
		InboxID:         "reject-me-001",
		SenderProjectID: "proj",
		SenderIssueID:   "r-1",
		Title:           "Unwanted item",
		Priority:        3,
		IssueType:       "task",
		SenderRef:       "beads://proj/r-1",
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Reject with reason
	rejectOut := bdRun(t, bd, dir, "handoff", "inbox", "reject", "reject-me-001", "not relevant", "--json")
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(rejectOut), &result); err != nil {
		t.Fatalf("parse reject JSON: %v\n%s", err, rejectOut)
	}
	if result["rejected"] != true {
		t.Errorf("rejected = %v, want true", result["rejected"])
	}

	// Inbox should be empty
	listOut := bdRun(t, bd, dir, "handoff", "inbox", "list", "--json")
	var pending []*types.InboxItem
	if err := json.Unmarshal([]byte(listOut), &pending); err != nil {
		t.Fatalf("parse list JSON: %v\n%s", err, listOut)
	}
	if len(pending) != 0 {
		t.Errorf("inbox should be empty after reject, got %d", len(pending))
	}
}

// TestInboxCleanIntegration tests cleaning processed inbox items.
func TestInboxCleanIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cln")

	store := openStore(t, beadsDir, "cln")
	ctx := t.Context()

	// Add and mark one imported, one rejected, one pending
	for _, item := range []*types.InboxItem{
		{InboxID: "clean-001", SenderProjectID: "p", SenderIssueID: "c1", Title: "Imported", IssueType: "task", SenderRef: "beads://p/c1"},
		{InboxID: "clean-002", SenderProjectID: "p", SenderIssueID: "c2", Title: "Rejected", IssueType: "task", SenderRef: "beads://p/c2"},
		{InboxID: "clean-003", SenderProjectID: "p", SenderIssueID: "c3", Title: "Pending", IssueType: "task", SenderRef: "beads://p/c3"},
	} {
		if err := store.AddInboxItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkInboxItemImported(ctx, "clean-001", "local-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkInboxItemRejected(ctx, "clean-002", "nope"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Clean processed items (dry-run requires RawDBAccessor, skip it)
	cleanOut := bdRun(t, bd, dir, "handoff", "inbox", "clean", "--json")
	var cleanResult map[string]interface{}
	if err := json.Unmarshal([]byte(cleanOut), &cleanResult); err != nil {
		t.Fatalf("parse clean JSON: %v\n%s", err, cleanOut)
	}
	if int(cleanResult["removed"].(float64)) != 2 {
		t.Errorf("removed = %v, want 2", cleanResult["removed"])
	}

	// Only pending item should remain
	listOut := bdRun(t, bd, dir, "handoff", "inbox", "list", "--json")
	var pending []*types.InboxItem
	if err := json.Unmarshal([]byte(listOut), &pending); err != nil {
		t.Fatalf("parse list JSON: %v\n%s", err, listOut)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 pending item after clean, got %d", len(pending))
	}
}

// TestInboxImportDedup tests that re-importing the same sender issue is skipped.
func TestInboxImportDedup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dup")

	store := openStore(t, beadsDir, "dup")
	ctx := t.Context()

	if err := store.AddInboxItem(ctx, &types.InboxItem{
		InboxID:         "dedup-001",
		SenderProjectID: "proj",
		SenderIssueID:   "d-1",
		Title:           "Original",
		Priority:        1,
		IssueType:       "bug",
		SenderRef:       "beads://proj/d-1",
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// First import
	bdRun(t, bd, dir, "handoff", "inbox", "import", "--json")

	// Add a second inbox item with same sender ref (simulating resend)
	store2 := openStore(t, beadsDir, "dup")
	if err := store2.AddInboxItem(ctx, &types.InboxItem{
		InboxID:         "dedup-002",
		SenderProjectID: "proj",
		SenderIssueID:   "d-1",
		Title:           "Resent original",
		Priority:        1,
		IssueType:       "bug",
		SenderRef:       "beads://proj/d-1",
	}); err != nil {
		t.Fatal(err)
	}
	store2.Close()

	// Second import should skip the duplicate
	importOut := bdRun(t, bd, dir, "handoff", "inbox", "import", "--json")
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(importOut), &result); err != nil {
		t.Fatalf("parse import JSON: %v\n%s", err, importOut)
	}
	if int(result["imported"].(float64)) != 0 {
		t.Errorf("imported = %v, want 0 (should skip duplicate)", result["imported"])
	}
}

// TestInboxRejectAlreadyImported tests rejecting an already-imported item fails.
func TestInboxRejectAlreadyImported(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rai")

	store := openStore(t, beadsDir, "rai")
	ctx := t.Context()

	if err := store.AddInboxItem(ctx, &types.InboxItem{
		InboxID:         "already-imported-001",
		SenderProjectID: "proj",
		SenderIssueID:   "ai-1",
		Title:           "Already imported",
		IssueType:       "task",
		SenderRef:       "beads://proj/ai-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkInboxItemImported(ctx, "already-imported-001", "local-99"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Reject should fail
	out := bdRunExpectFail(t, bd, dir, "handoff", "inbox", "reject", "already-imported-001")
	if !strings.Contains(out, "already imported") {
		t.Errorf("expected 'already imported' error, got: %s", out)
	}
}

// bdRun runs the bd binary and returns stdout. Fatals on non-zero exit.
func bdRun(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	out, err := bdRunWithFlockRetry(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return extractJSON(string(out))
}

// bdRunExpectFail runs the bd binary expecting failure. Returns combined output.
func bdRunExpectFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd %s should have failed, got: %s", strings.Join(args, " "), out)
	}
	return string(out)
}

// extractJSON extracts the first JSON object or array from output that may
// contain non-JSON lines (warnings, tips).
func extractJSON(s string) string {
	// Find first '{' or '['
	for i, c := range s {
		if c == '{' || c == '[' {
			return s[i:]
		}
	}
	return s
}
