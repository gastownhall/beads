package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// setupTestStore creates a test storage with issue_prefix configured
func setupTestStore(t *testing.T, dbPath string) *sqlite.SQLiteStorage {
	t.Helper()
	
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	
	return store
}

// TestDBNeedsExport_InSync verifies dbNeedsExport returns false when DB and JSONL are in sync
func TestDBNeedsExport_InSync(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	store := setupTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create an issue in DB
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Wait a moment to ensure DB mtime isn't newer
	time.Sleep(10 * time.Millisecond)

	// Touch JSONL to make it newer than DB
	now := time.Now()
	if err := os.Chtimes(jsonlPath, now, now); err != nil {
		t.Fatalf("Failed to touch JSONL: %v", err)
	}

	// DB and JSONL should be in sync
	needsExport, err := dbNeedsExport(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("dbNeedsExport failed: %v", err)
	}

	if needsExport {
		t.Errorf("Expected needsExport=false (DB and JSONL in sync), got true")
	}
}

// TestDBNeedsExport_DBNewer verifies dbNeedsExport returns true when DB is modified
func TestDBNeedsExport_DBNewer(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	store := setupTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create and export issue
	issue1 := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	err := store.CreateIssue(ctx, issue1, "test-user")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Wait and modify DB
	time.Sleep(10 * time.Millisecond)
	issue2 := &types.Issue{
		Title:     "Another Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err = store.CreateIssue(ctx, issue2, "test-user")
	if err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}

	// DB is newer, should need export
	needsExport, err := dbNeedsExport(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("dbNeedsExport failed: %v", err)
	}

	if !needsExport {
		t.Errorf("Expected needsExport=true (DB modified), got false")
	}
}

// TestDBNeedsExport_CountMismatch verifies dbNeedsExport returns true when counts differ
func TestDBNeedsExport_CountMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	store := setupTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create and export issue
	issue1 := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	err := store.CreateIssue(ctx, issue1, "test-user")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Add another issue to DB but don't export
	issue2 := &types.Issue{
		Title:     "Another Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	err = store.CreateIssue(ctx, issue2, "test-user")
	if err != nil {
		t.Fatalf("Failed to create second issue: %v", err)
	}

	// Make JSONL appear newer (but counts differ)
	time.Sleep(10 * time.Millisecond)
	now := time.Now().Add(1 * time.Hour) // Way in the future
	if err := os.Chtimes(jsonlPath, now, now); err != nil {
		t.Fatalf("Failed to touch JSONL: %v", err)
	}

	// Counts mismatch, should need export
	needsExport, err := dbNeedsExport(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("dbNeedsExport failed: %v", err)
	}

	if !needsExport {
		t.Errorf("Expected needsExport=true (count mismatch), got false")
	}
}

// TestDBNeedsExport_NoJSONL verifies dbNeedsExport returns true when JSONL doesn't exist
func TestDBNeedsExport_NoJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	store := setupTestStore(t, dbPath)
	defer store.Close()

	ctx := context.Background()

	// Create issue but don't export
	issue := &types.Issue{
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
	}
	err := store.CreateIssue(ctx, issue, "test-user")
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// JSONL doesn't exist, should need export
	needsExport, err := dbNeedsExport(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("dbNeedsExport failed: %v", err)
	}

	if !needsExport {
		t.Fatalf("Expected needsExport=true (JSONL missing), got false")
	}
}

// =============================================================================
// 3-Way Merge Tests (Phase 2)
// =============================================================================

// makeTestIssue creates a test issue with specified fields
func makeTestIssue(id, title string, status types.Status, priority int, updatedAt time.Time) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     title,
		Status:    status,
		Priority:  priority,
		IssueType: types.TypeTask,
		UpdatedAt: updatedAt,
		CreatedAt: updatedAt.Add(-time.Hour), // Created 1 hour before update
	}
}

// TestMergeIssue_NoBase_LocalOnly tests first sync with only local issue
func TestMergeIssue_NoBase_LocalOnly(t *testing.T) {
	local := makeTestIssue("bd-1234", "Local Issue", types.StatusOpen, 1, time.Now())

	merged, strategy, _ := MergeIssue(nil, local, nil)

	if strategy != StrategyLocal {
		t.Errorf("Expected strategy=%s, got %s", StrategyLocal, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	if merged.ID != "bd-1234" {
		t.Errorf("Expected ID=bd-1234, got %s", merged.ID)
	}
}

// TestMergeIssue_NoBase_RemoteOnly tests first sync with only remote issue
func TestMergeIssue_NoBase_RemoteOnly(t *testing.T) {
	remote := makeTestIssue("bd-5678", "Remote Issue", types.StatusOpen, 2, time.Now())

	merged, strategy, _ := MergeIssue(nil, nil, remote)

	if strategy != StrategyRemote {
		t.Errorf("Expected strategy=%s, got %s", StrategyRemote, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	if merged.ID != "bd-5678" {
		t.Errorf("Expected ID=bd-5678, got %s", merged.ID)
	}
}

// TestMergeIssue_NoBase_BothExist_LocalNewer tests first sync where both have same issue, local is newer
func TestMergeIssue_NoBase_BothExist_LocalNewer(t *testing.T) {
	now := time.Now()
	local := makeTestIssue("bd-1234", "Local Title", types.StatusOpen, 1, now.Add(time.Hour))
	remote := makeTestIssue("bd-1234", "Remote Title", types.StatusOpen, 2, now)

	merged, strategy, _ := MergeIssue(nil, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	if merged.Title != "Local Title" {
		t.Errorf("Expected local title (newer), got %s", merged.Title)
	}
}

// TestMergeIssue_NoBase_BothExist_RemoteNewer tests first sync where both have same issue, remote is newer
func TestMergeIssue_NoBase_BothExist_RemoteNewer(t *testing.T) {
	now := time.Now()
	local := makeTestIssue("bd-1234", "Local Title", types.StatusOpen, 1, now)
	remote := makeTestIssue("bd-1234", "Remote Title", types.StatusOpen, 2, now.Add(time.Hour))

	merged, strategy, _ := MergeIssue(nil, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	if merged.Title != "Remote Title" {
		t.Errorf("Expected remote title (newer), got %s", merged.Title)
	}
}

// TestMergeIssue_NoBase_BothExist_SameTime tests first sync where both have same timestamp (remote wins)
func TestMergeIssue_NoBase_BothExist_SameTime(t *testing.T) {
	now := time.Now()
	local := makeTestIssue("bd-1234", "Local Title", types.StatusOpen, 1, now)
	remote := makeTestIssue("bd-1234", "Remote Title", types.StatusOpen, 2, now)

	merged, strategy, _ := MergeIssue(nil, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	// Remote wins on tie (per design.md Decision 3)
	if merged.Title != "Remote Title" {
		t.Errorf("Expected remote title (tie goes to remote), got %s", merged.Title)
	}
}

// TestMergeIssue_NoChanges tests 3-way merge with no changes anywhere
func TestMergeIssue_NoChanges(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Same Title", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Same Title", types.StatusOpen, 1, now)
	remote := makeTestIssue("bd-1234", "Same Title", types.StatusOpen, 1, now)

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategySame {
		t.Errorf("Expected strategy=%s, got %s", StrategySame, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
}

// TestMergeIssue_OnlyLocalChanged tests 3-way merge where only local changed
func TestMergeIssue_OnlyLocalChanged(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original Title", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Updated Title", types.StatusOpen, 1, now.Add(time.Hour))
	remote := makeTestIssue("bd-1234", "Original Title", types.StatusOpen, 1, now)

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyLocal {
		t.Errorf("Expected strategy=%s, got %s", StrategyLocal, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	if merged.Title != "Updated Title" {
		t.Errorf("Expected updated title, got %s", merged.Title)
	}
}

// TestMergeIssue_OnlyRemoteChanged tests 3-way merge where only remote changed
func TestMergeIssue_OnlyRemoteChanged(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original Title", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Original Title", types.StatusOpen, 1, now)
	remote := makeTestIssue("bd-1234", "Updated Title", types.StatusOpen, 1, now.Add(time.Hour))

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyRemote {
		t.Errorf("Expected strategy=%s, got %s", StrategyRemote, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	if merged.Title != "Updated Title" {
		t.Errorf("Expected updated title, got %s", merged.Title)
	}
}

// TestMergeIssue_BothMadeSameChange tests 3-way merge where both made identical change
func TestMergeIssue_BothMadeSameChange(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original Title", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Same Update", types.StatusClosed, 2, now.Add(time.Hour))
	remote := makeTestIssue("bd-1234", "Same Update", types.StatusClosed, 2, now.Add(time.Hour))

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategySame {
		t.Errorf("Expected strategy=%s, got %s", StrategySame, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	if merged.Title != "Same Update" {
		t.Errorf("Expected 'Same Update', got %s", merged.Title)
	}
}

// TestMergeIssue_TrueConflict_LocalNewer tests true conflict where local is newer
func TestMergeIssue_TrueConflict_LocalNewer(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Local Update", types.StatusInProgress, 1, now.Add(2*time.Hour))
	remote := makeTestIssue("bd-1234", "Remote Update", types.StatusClosed, 2, now.Add(time.Hour))

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	// Local is newer, should win
	if merged.Title != "Local Update" {
		t.Errorf("Expected local title (newer), got %s", merged.Title)
	}
	if merged.Status != types.StatusInProgress {
		t.Errorf("Expected local status, got %s", merged.Status)
	}
}

// TestMergeIssue_TrueConflict_RemoteNewer tests true conflict where remote is newer
func TestMergeIssue_TrueConflict_RemoteNewer(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Local Update", types.StatusInProgress, 1, now.Add(time.Hour))
	remote := makeTestIssue("bd-1234", "Remote Update", types.StatusClosed, 2, now.Add(2*time.Hour))

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	// Remote is newer, should win
	if merged.Title != "Remote Update" {
		t.Errorf("Expected remote title (newer), got %s", merged.Title)
	}
	if merged.Status != types.StatusClosed {
		t.Errorf("Expected remote status, got %s", merged.Status)
	}
}

// TestMergeIssue_LocalDeleted_RemoteUnchanged tests local deletion when remote unchanged
func TestMergeIssue_LocalDeleted_RemoteUnchanged(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "To Delete", types.StatusOpen, 1, now)
	remote := makeTestIssue("bd-1234", "To Delete", types.StatusOpen, 1, now)

	merged, strategy, _ := MergeIssue(base, nil, remote)

	if strategy != StrategyLocal {
		t.Errorf("Expected strategy=%s (honor local deletion), got %s", StrategyLocal, strategy)
	}
	if merged != nil {
		t.Errorf("Expected nil (deleted), got issue %s", merged.ID)
	}
}

// TestMergeIssue_LocalDeleted_RemoteChanged tests local deletion but remote changed
func TestMergeIssue_LocalDeleted_RemoteChanged(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original", types.StatusOpen, 1, now)
	remote := makeTestIssue("bd-1234", "Remote Updated", types.StatusClosed, 2, now.Add(time.Hour))

	merged, strategy, _ := MergeIssue(base, nil, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s (conflict: deleted vs updated), got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue (remote changed), got nil")
	}
	if merged.Title != "Remote Updated" {
		t.Errorf("Expected remote title (changed wins over delete), got %s", merged.Title)
	}
}

// TestMergeIssue_RemoteDeleted_LocalUnchanged tests remote deletion when local unchanged
func TestMergeIssue_RemoteDeleted_LocalUnchanged(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "To Delete", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "To Delete", types.StatusOpen, 1, now)

	merged, strategy, _ := MergeIssue(base, local, nil)

	if strategy != StrategyRemote {
		t.Errorf("Expected strategy=%s (honor remote deletion), got %s", StrategyRemote, strategy)
	}
	if merged != nil {
		t.Errorf("Expected nil (deleted), got issue %s", merged.ID)
	}
}

// TestMergeIssue_RemoteDeleted_LocalChanged tests remote deletion but local changed
func TestMergeIssue_RemoteDeleted_LocalChanged(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Local Updated", types.StatusClosed, 2, now.Add(time.Hour))

	merged, strategy, _ := MergeIssue(base, local, nil)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s (conflict: updated vs deleted), got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue (local changed), got nil")
	}
	if merged.Title != "Local Updated" {
		t.Errorf("Expected local title (changed wins over delete), got %s", merged.Title)
	}
}

// TestMergeIssues_Empty tests merging empty sets
func TestMergeIssues_Empty(t *testing.T) {
	result := MergeIssues(nil, nil, nil)
	if len(result.Merged) != 0 {
		t.Errorf("Expected 0 merged issues, got %d", len(result.Merged))
	}
	if result.Conflicts != 0 {
		t.Errorf("Expected 0 conflicts, got %d", result.Conflicts)
	}
}

// TestMergeIssues_MultipleIssues tests merging multiple issues with different scenarios
func TestMergeIssues_MultipleIssues(t *testing.T) {
	now := time.Now()

	// Base state
	base := []*types.Issue{
		makeTestIssue("bd-0001", "Unchanged", types.StatusOpen, 1, now),
		makeTestIssue("bd-0002", "Will change locally", types.StatusOpen, 1, now),
		makeTestIssue("bd-0003", "Will change remotely", types.StatusOpen, 1, now),
		makeTestIssue("bd-0004", "To delete locally", types.StatusOpen, 1, now),
	}

	// Local state
	local := []*types.Issue{
		makeTestIssue("bd-0001", "Unchanged", types.StatusOpen, 1, now),
		makeTestIssue("bd-0002", "Changed locally", types.StatusInProgress, 1, now.Add(time.Hour)),
		makeTestIssue("bd-0003", "Will change remotely", types.StatusOpen, 1, now),
		// bd-0004 deleted locally
		makeTestIssue("bd-0005", "New local issue", types.StatusOpen, 1, now), // New issue
	}

	// Remote state
	remote := []*types.Issue{
		makeTestIssue("bd-0001", "Unchanged", types.StatusOpen, 1, now),
		makeTestIssue("bd-0002", "Will change locally", types.StatusOpen, 1, now),
		makeTestIssue("bd-0003", "Changed remotely", types.StatusClosed, 2, now.Add(time.Hour)),
		makeTestIssue("bd-0004", "To delete locally", types.StatusOpen, 1, now), // Unchanged from base
		makeTestIssue("bd-0006", "New remote issue", types.StatusOpen, 1, now),  // New issue
	}

	result := MergeIssues(base, local, remote)

	// Should have 5 issues:
	// - bd-0001: same
	// - bd-0002: local changed
	// - bd-0003: remote changed
	// - bd-0004: deleted (not in merged)
	// - bd-0005: new local
	// - bd-0006: new remote
	if len(result.Merged) != 5 {
		t.Errorf("Expected 5 merged issues, got %d", len(result.Merged))
	}

	// Verify strategies
	expectedStrategies := map[string]string{
		"bd-0001": StrategySame,
		"bd-0002": StrategyLocal,
		"bd-0003": StrategyRemote,
		"bd-0004": StrategyLocal, // Deleted locally
		"bd-0005": StrategyLocal,
		"bd-0006": StrategyRemote,
	}

	for id, expected := range expectedStrategies {
		if got := result.Strategy[id]; got != expected {
			t.Errorf("Issue %s: expected strategy=%s, got %s", id, expected, got)
		}
	}

	// Verify bd-0004 is not in merged (deleted)
	for _, issue := range result.Merged {
		if issue.ID == "bd-0004" {
			t.Errorf("bd-0004 should be deleted, but found in merged")
		}
	}
}

// TestBaseState_LoadSave tests loading and saving base state
func TestBaseState_LoadSave(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now().Truncate(time.Second) // Truncate for JSON round-trip

	issues := []*types.Issue{
		{
			ID:        "bd-0001",
			Title:     "Test Issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			UpdatedAt: now,
			CreatedAt: now.Add(-time.Hour),
		},
		{
			ID:        "bd-0002",
			Title:     "Test Issue 2",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeBug,
			UpdatedAt: now,
			CreatedAt: now.Add(-time.Hour),
		},
	}

	// Save base state
	if err := saveBaseState(tmpDir, issues); err != nil {
		t.Fatalf("saveBaseState failed: %v", err)
	}

	// Verify file exists
	baseStatePath := filepath.Join(tmpDir, syncBaseFileName)
	if _, err := os.Stat(baseStatePath); os.IsNotExist(err) {
		t.Fatalf("Base state file not created")
	}

	// Load base state
	loaded, err := loadBaseState(tmpDir)
	if err != nil {
		t.Fatalf("loadBaseState failed: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("Expected 2 issues, got %d", len(loaded))
	}

	// Verify issue content
	if loaded[0].ID != "bd-0001" || loaded[0].Title != "Test Issue 1" {
		t.Errorf("First issue mismatch: got ID=%s, Title=%s", loaded[0].ID, loaded[0].Title)
	}
	if loaded[1].ID != "bd-0002" || loaded[1].Title != "Test Issue 2" {
		t.Errorf("Second issue mismatch: got ID=%s, Title=%s", loaded[1].ID, loaded[1].Title)
	}
}

// TestBaseState_LoadMissing tests loading when no base state exists
func TestBaseState_LoadMissing(t *testing.T) {
	tmpDir := t.TempDir()

	loaded, err := loadBaseState(tmpDir)
	if err != nil {
		t.Fatalf("loadBaseState failed: %v", err)
	}

	if loaded != nil {
		t.Errorf("Expected nil for missing base state, got %d issues", len(loaded))
	}
}

// TestBaseState_LoadMalformed tests loading sync_base.jsonl with malformed lines
func TestBaseState_LoadMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	baseStatePath := filepath.Join(tmpDir, syncBaseFileName)

	// Create file with mix of valid and malformed lines
	content := `{"id":"bd-0001","title":"Valid Issue","status":"open","priority":1,"issue_type":"task"}
not valid json at all
{"id":"bd-0002","title":"Another Valid","status":"closed","priority":2,"issue_type":"bug"}
{truncated json
{"id":"bd-0003","title":"Third Valid","status":"open","priority":3,"issue_type":"task"}
`
	if err := os.WriteFile(baseStatePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Capture stderr to verify warning is produced
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	loaded, err := loadBaseState(tmpDir)

	// Restore stderr and read captured output
	w.Close()
	os.Stderr = oldStderr
	var stderrBuf bytes.Buffer
	stderrBuf.ReadFrom(r)
	stderrOutput := stderrBuf.String()

	if err != nil {
		t.Fatalf("loadBaseState failed: %v", err)
	}

	// Should have loaded 3 valid issues, skipping 2 malformed lines
	if len(loaded) != 3 {
		t.Errorf("Expected 3 valid issues, got %d", len(loaded))
	}

	// Verify correct issues loaded
	expectedIDs := []string{"bd-0001", "bd-0002", "bd-0003"}
	for i, expected := range expectedIDs {
		if i >= len(loaded) {
			t.Errorf("Missing issue at index %d", i)
			continue
		}
		if loaded[i].ID != expected {
			t.Errorf("Issue %d: expected ID=%s, got %s", i, expected, loaded[i].ID)
		}
	}

	// Verify warnings were produced for malformed lines (lines 2 and 4)
	if !strings.Contains(stderrOutput, "line 2") {
		t.Errorf("Expected warning for line 2, got: %s", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "line 4") {
		t.Errorf("Expected warning for line 4, got: %s", stderrOutput)
	}
}

// TestIssueEqual tests the issueEqual helper function
func TestIssueEqual(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		a, b     *types.Issue
		expected bool
	}{
		{
			name:     "both nil",
			a:        nil,
			b:        nil,
			expected: true,
		},
		{
			name:     "a nil",
			a:        nil,
			b:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			expected: false,
		},
		{
			name:     "b nil",
			a:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			b:        nil,
			expected: false,
		},
		{
			name:     "identical",
			a:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			b:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			expected: true,
		},
		{
			name:     "different ID",
			a:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			b:        makeTestIssue("bd-5678", "Test", types.StatusOpen, 1, now),
			expected: false,
		},
		{
			name:     "different title",
			a:        makeTestIssue("bd-1234", "Test A", types.StatusOpen, 1, now),
			b:        makeTestIssue("bd-1234", "Test B", types.StatusOpen, 1, now),
			expected: false,
		},
		{
			name:     "different status",
			a:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			b:        makeTestIssue("bd-1234", "Test", types.StatusClosed, 1, now),
			expected: false,
		},
		{
			name:     "different priority",
			a:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			b:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 2, now),
			expected: false,
		},
		{
			name:     "different updated_at",
			a:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			b:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now.Add(time.Hour)),
			expected: false,
		},
		{
			name: "different dependencies",
			a: func() *types.Issue {
				i := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
				i.Dependencies = []*types.Dependency{{DependsOnID: "bd-5678", Type: types.DepBlocks}}
				return i
			}(),
			b:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			expected: false,
		},
		{
			name: "same dependencies different order",
			a: func() *types.Issue {
				i := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
				i.Dependencies = []*types.Dependency{
					{DependsOnID: "bd-5678", Type: types.DepBlocks},
					{DependsOnID: "bd-9999", Type: types.DepBlocks},
				}
				return i
			}(),
			b: func() *types.Issue {
				i := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
				i.Dependencies = []*types.Dependency{
					{DependsOnID: "bd-9999", Type: types.DepBlocks},
					{DependsOnID: "bd-5678", Type: types.DepBlocks},
				}
				return i
			}(),
			expected: true,
		},
		{
			name: "different comments",
			a: func() *types.Issue {
				i := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
				i.Comments = []*types.Comment{{Author: "alice", Text: "hello", CreatedAt: now}}
				return i
			}(),
			b:        makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now),
			expected: false,
		},
		{
			name: "same comments",
			a: func() *types.Issue {
				i := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
				i.Comments = []*types.Comment{{Author: "alice", Text: "hello", CreatedAt: now}}
				return i
			}(),
			b: func() *types.Issue {
				i := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
				i.Comments = []*types.Comment{{Author: "alice", Text: "hello", CreatedAt: now}}
				return i
			}(),
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := issueEqual(tc.a, tc.b)
			if result != tc.expected {
				t.Errorf("issueEqual returned %v, expected %v", result, tc.expected)
			}
		})
	}
}

// =============================================================================
// Field-Level Merge Tests (Phase 3)
// =============================================================================

// makeTestIssueWithLabels creates a test issue with labels
func makeTestIssueWithLabels(id, title string, status types.Status, priority int, updatedAt time.Time, labels []string) *types.Issue {
	issue := makeTestIssue(id, title, status, priority, updatedAt)
	issue.Labels = labels
	return issue
}

// TestFieldMerge_LWW_LocalNewer tests field-level merge where local is newer
func TestFieldMerge_LWW_LocalNewer(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Local Update", types.StatusInProgress, 2, now.Add(2*time.Hour))
	remote := makeTestIssue("bd-1234", "Remote Update", types.StatusClosed, 3, now.Add(time.Hour))

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	// Local is newer, should have local's scalar values
	if merged.Title != "Local Update" {
		t.Errorf("Expected title='Local Update' (local is newer), got %s", merged.Title)
	}
	if merged.Status != types.StatusInProgress {
		t.Errorf("Expected status=in_progress (local is newer), got %s", merged.Status)
	}
	if merged.Priority != 2 {
		t.Errorf("Expected priority=2 (local is newer), got %d", merged.Priority)
	}
}

// TestFieldMerge_LWW_RemoteNewer tests field-level merge where remote is newer
func TestFieldMerge_LWW_RemoteNewer(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Local Update", types.StatusInProgress, 2, now.Add(time.Hour))
	remote := makeTestIssue("bd-1234", "Remote Update", types.StatusClosed, 3, now.Add(2*time.Hour))

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	// Remote is newer, should have remote's scalar values
	if merged.Title != "Remote Update" {
		t.Errorf("Expected title='Remote Update' (remote is newer), got %s", merged.Title)
	}
	if merged.Status != types.StatusClosed {
		t.Errorf("Expected status=closed (remote is newer), got %s", merged.Status)
	}
	if merged.Priority != 3 {
		t.Errorf("Expected priority=3 (remote is newer), got %d", merged.Priority)
	}
}

// TestFieldMerge_LWW_SameTimestamp tests field-level merge where timestamps are equal (remote wins)
func TestFieldMerge_LWW_SameTimestamp(t *testing.T) {
	now := time.Now()
	base := makeTestIssue("bd-1234", "Original", types.StatusOpen, 1, now.Add(-time.Hour))
	local := makeTestIssue("bd-1234", "Local Update", types.StatusInProgress, 2, now)
	remote := makeTestIssue("bd-1234", "Remote Update", types.StatusClosed, 3, now)

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}
	// Same timestamp: remote wins (per design.md Decision 3)
	if merged.Title != "Remote Update" {
		t.Errorf("Expected title='Remote Update' (remote wins on tie), got %s", merged.Title)
	}
	if merged.Status != types.StatusClosed {
		t.Errorf("Expected status=closed (remote wins on tie), got %s", merged.Status)
	}
}

// TestLabelUnion_BothAdd tests label union when both local and remote add different labels
func TestLabelUnion_BothAdd(t *testing.T) {
	now := time.Now()
	base := makeTestIssueWithLabels("bd-1234", "Test", types.StatusOpen, 1, now, []string{"original"})
	local := makeTestIssueWithLabels("bd-1234", "Test Local", types.StatusOpen, 1, now.Add(time.Hour), []string{"original", "local-added"})
	remote := makeTestIssueWithLabels("bd-1234", "Test Remote", types.StatusOpen, 1, now.Add(2*time.Hour), []string{"original", "remote-added"})

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}

	// Labels should be union of both
	expectedLabels := []string{"local-added", "original", "remote-added"}
	if len(merged.Labels) != len(expectedLabels) {
		t.Errorf("Expected %d labels, got %d: %v", len(expectedLabels), len(merged.Labels), merged.Labels)
	}
	for _, expected := range expectedLabels {
		found := false
		for _, actual := range merged.Labels {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected label %q in merged labels %v", expected, merged.Labels)
		}
	}
}

// TestLabelUnion_LocalOnly tests label union when only local adds labels
func TestLabelUnion_LocalOnly(t *testing.T) {
	now := time.Now()
	base := makeTestIssueWithLabels("bd-1234", "Test", types.StatusOpen, 1, now, []string{"original"})
	local := makeTestIssueWithLabels("bd-1234", "Test Local", types.StatusOpen, 1, now.Add(time.Hour), []string{"original", "local-added"})
	remote := makeTestIssueWithLabels("bd-1234", "Test Remote", types.StatusOpen, 1, now.Add(2*time.Hour), []string{"original"})

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}

	// Labels should include local-added even though remote is newer for scalars
	expectedLabels := []string{"local-added", "original"}
	if len(merged.Labels) != len(expectedLabels) {
		t.Errorf("Expected %d labels, got %d: %v", len(expectedLabels), len(merged.Labels), merged.Labels)
	}
}

// TestLabelUnion_RemoteOnly tests label union when only remote adds labels
func TestLabelUnion_RemoteOnly(t *testing.T) {
	now := time.Now()
	base := makeTestIssueWithLabels("bd-1234", "Test", types.StatusOpen, 1, now, []string{"original"})
	local := makeTestIssueWithLabels("bd-1234", "Test Local", types.StatusOpen, 1, now.Add(2*time.Hour), []string{"original"})
	remote := makeTestIssueWithLabels("bd-1234", "Test Remote", types.StatusOpen, 1, now.Add(time.Hour), []string{"original", "remote-added"})

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}

	// Labels should include remote-added even though local is newer for scalars
	expectedLabels := []string{"original", "remote-added"}
	if len(merged.Labels) != len(expectedLabels) {
		t.Errorf("Expected %d labels, got %d: %v", len(expectedLabels), len(merged.Labels), merged.Labels)
	}
}

// TestDependencyUnion tests dependency union when both add different dependencies
func TestDependencyUnion(t *testing.T) {
	now := time.Now()

	localDep := &types.Dependency{
		IssueID:     "bd-1234",
		DependsOnID: "bd-aaaa",
		Type:        types.DepBlocks,
		CreatedAt:   now,
	}
	remoteDep := &types.Dependency{
		IssueID:     "bd-1234",
		DependsOnID: "bd-bbbb",
		Type:        types.DepBlocks,
		CreatedAt:   now,
	}

	base := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
	local := makeTestIssue("bd-1234", "Test Local", types.StatusInProgress, 1, now.Add(time.Hour))
	local.Dependencies = []*types.Dependency{localDep}
	remote := makeTestIssue("bd-1234", "Test Remote", types.StatusClosed, 1, now.Add(2*time.Hour))
	remote.Dependencies = []*types.Dependency{remoteDep}

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}

	// Dependencies should be union of both
	if len(merged.Dependencies) != 2 {
		t.Errorf("Expected 2 dependencies, got %d", len(merged.Dependencies))
	}

	// Check both dependencies are present
	foundAAA := false
	foundBBB := false
	for _, dep := range merged.Dependencies {
		if dep.DependsOnID == "bd-aaaa" {
			foundAAA = true
		}
		if dep.DependsOnID == "bd-bbbb" {
			foundBBB = true
		}
	}
	if !foundAAA {
		t.Error("Expected dependency to bd-aaaa in merged")
	}
	if !foundBBB {
		t.Error("Expected dependency to bd-bbbb in merged")
	}
}

// TestCommentAppend tests comment append-merge with deduplication
func TestCommentAppend(t *testing.T) {
	now := time.Now()

	// Common comment (should be deduplicated)
	commonComment := &types.Comment{
		ID:        1,
		IssueID:   "bd-1234",
		Author:    "user1",
		Text:      "Common comment",
		CreatedAt: now.Add(-time.Hour),
	}
	localComment := &types.Comment{
		ID:        2,
		IssueID:   "bd-1234",
		Author:    "user2",
		Text:      "Local comment",
		CreatedAt: now,
	}
	remoteComment := &types.Comment{
		ID:        3,
		IssueID:   "bd-1234",
		Author:    "user3",
		Text:      "Remote comment",
		CreatedAt: now.Add(30 * time.Minute),
	}

	base := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now.Add(-2*time.Hour))
	base.Comments = []*types.Comment{commonComment}

	local := makeTestIssue("bd-1234", "Test Local", types.StatusInProgress, 1, now.Add(time.Hour))
	local.Comments = []*types.Comment{commonComment, localComment}

	remote := makeTestIssue("bd-1234", "Test Remote", types.StatusClosed, 1, now.Add(2*time.Hour))
	remote.Comments = []*types.Comment{commonComment, remoteComment}

	merged, strategy, _ := MergeIssue(base, local, remote)

	if strategy != StrategyMerged {
		t.Errorf("Expected strategy=%s, got %s", StrategyMerged, strategy)
	}
	if merged == nil {
		t.Fatal("Expected merged issue, got nil")
	}

	// Comments should be union (3 total: common, local, remote)
	if len(merged.Comments) != 3 {
		t.Errorf("Expected 3 comments, got %d", len(merged.Comments))
	}

	// Check comments are sorted chronologically
	for i := 0; i < len(merged.Comments)-1; i++ {
		if merged.Comments[i].CreatedAt.After(merged.Comments[i+1].CreatedAt) {
			t.Errorf("Comments not sorted chronologically: %v after %v",
				merged.Comments[i].CreatedAt, merged.Comments[i+1].CreatedAt)
		}
	}
}

// TestFieldMerge_EdgeCases tests edge cases in field-level merge
func TestFieldMerge_EdgeCases(t *testing.T) {
	t.Run("nil_labels", func(t *testing.T) {
		now := time.Now()
		base := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
		local := makeTestIssue("bd-1234", "Test Local", types.StatusInProgress, 1, now.Add(time.Hour))
		local.Labels = nil
		remote := makeTestIssue("bd-1234", "Test Remote", types.StatusClosed, 1, now.Add(2*time.Hour))
		remote.Labels = []string{"remote-label"}

		merged, _, _ := MergeIssue(base, local, remote)
		if merged == nil {
			t.Fatal("Expected merged issue, got nil")
		}

		// Should have remote label (union of nil and ["remote-label"])
		if len(merged.Labels) != 1 || merged.Labels[0] != "remote-label" {
			t.Errorf("Expected ['remote-label'], got %v", merged.Labels)
		}
	})

	t.Run("empty_labels", func(t *testing.T) {
		now := time.Now()
		base := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
		local := makeTestIssue("bd-1234", "Test Local", types.StatusInProgress, 1, now.Add(time.Hour))
		local.Labels = []string{}
		remote := makeTestIssue("bd-1234", "Test Remote", types.StatusClosed, 1, now.Add(2*time.Hour))
		remote.Labels = []string{"remote-label"}

		merged, _, _ := MergeIssue(base, local, remote)
		if merged == nil {
			t.Fatal("Expected merged issue, got nil")
		}

		// Should have remote label (union of [] and ["remote-label"])
		if len(merged.Labels) != 1 || merged.Labels[0] != "remote-label" {
			t.Errorf("Expected ['remote-label'], got %v", merged.Labels)
		}
	})

	t.Run("nil_dependencies", func(t *testing.T) {
		now := time.Now()
		dep := &types.Dependency{
			IssueID:     "bd-1234",
			DependsOnID: "bd-dep",
			Type:        types.DepBlocks,
			CreatedAt:   now,
		}

		base := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now)
		local := makeTestIssue("bd-1234", "Test Local", types.StatusInProgress, 1, now.Add(time.Hour))
		local.Dependencies = []*types.Dependency{dep}
		remote := makeTestIssue("bd-1234", "Test Remote", types.StatusClosed, 1, now.Add(2*time.Hour))
		remote.Dependencies = nil

		merged, _, _ := MergeIssue(base, local, remote)
		if merged == nil {
			t.Fatal("Expected merged issue, got nil")
		}

		// Should have the dependency from local
		if len(merged.Dependencies) != 1 {
			t.Errorf("Expected 1 dependency, got %d", len(merged.Dependencies))
		}
	})

	t.Run("nil_comments", func(t *testing.T) {
		now := time.Now()
		comment := &types.Comment{
			ID:        1,
			IssueID:   "bd-1234",
			Author:    "user",
			Text:      "Test comment",
			CreatedAt: now,
		}

		base := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now.Add(-time.Hour))
		local := makeTestIssue("bd-1234", "Test Local", types.StatusInProgress, 1, now.Add(time.Hour))
		local.Comments = nil
		remote := makeTestIssue("bd-1234", "Test Remote", types.StatusClosed, 1, now.Add(2*time.Hour))
		remote.Comments = []*types.Comment{comment}

		merged, _, _ := MergeIssue(base, local, remote)
		if merged == nil {
			t.Fatal("Expected merged issue, got nil")
		}

		// Should have the comment from remote
		if len(merged.Comments) != 1 {
			t.Errorf("Expected 1 comment, got %d", len(merged.Comments))
		}
	})

	t.Run("duplicate_dependencies_newer_wins", func(t *testing.T) {
		now := time.Now()

		// Same dependency in both, but with different metadata/timestamps
		localDep := &types.Dependency{
			IssueID:     "bd-1234",
			DependsOnID: "bd-dep",
			Type:        types.DepBlocks,
			CreatedAt:   now,
			CreatedBy:   "local-user",
		}
		remoteDep := &types.Dependency{
			IssueID:     "bd-1234",
			DependsOnID: "bd-dep",
			Type:        types.DepBlocks,
			CreatedAt:   now.Add(time.Hour), // Newer
			CreatedBy:   "remote-user",
		}

		base := makeTestIssue("bd-1234", "Test", types.StatusOpen, 1, now.Add(-time.Hour))
		local := makeTestIssue("bd-1234", "Test Local", types.StatusInProgress, 1, now.Add(time.Hour))
		local.Dependencies = []*types.Dependency{localDep}
		remote := makeTestIssue("bd-1234", "Test Remote", types.StatusClosed, 1, now.Add(2*time.Hour))
		remote.Dependencies = []*types.Dependency{remoteDep}

		merged, _, _ := MergeIssue(base, local, remote)
		if merged == nil {
			t.Fatal("Expected merged issue, got nil")
		}

		// Should have only 1 dependency (deduplicated), the newer one
		if len(merged.Dependencies) != 1 {
			t.Errorf("Expected 1 dependency, got %d", len(merged.Dependencies))
		}
		if merged.Dependencies[0].CreatedBy != "remote-user" {
			t.Errorf("Expected newer dependency (remote-user), got %s", merged.Dependencies[0].CreatedBy)
		}
	})
}

// =============================================================================
// Clock Skew Detection Tests (Phase 2 - PR918)
// =============================================================================

// TestMergeClockSkewWarning tests that large timestamp differences produce a warning
func TestMergeClockSkewWarning(t *testing.T) {
	now := time.Now()

	t.Run("no_warning_under_24h", func(t *testing.T) {
		base := makeTestIssue("bd-1234", "Original", types.StatusOpen, 1, now)
		local := makeTestIssue("bd-1234", "Local Update", types.StatusInProgress, 1, now.Add(23*time.Hour))
		remote := makeTestIssue("bd-1234", "Remote Update", types.StatusClosed, 1, now)

		// Capture stderr
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		_, _, _ = MergeIssue(base, local, remote)

		w.Close()
		os.Stderr = oldStderr
		var stderrBuf bytes.Buffer
		stderrBuf.ReadFrom(r)
		stderrOutput := stderrBuf.String()

		// Should NOT produce warning for <24h difference
		if strings.Contains(stderrOutput, "clock skew") {
			t.Errorf("Expected no warning for 23h difference, got: %s", stderrOutput)
		}
	})

	t.Run("warning_over_24h_local_newer", func(t *testing.T) {
		base := makeTestIssue("bd-1234", "Original", types.StatusOpen, 1, now)
		local := makeTestIssue("bd-1234", "Local Update", types.StatusInProgress, 1, now.Add(48*time.Hour))
		remote := makeTestIssue("bd-1234", "Remote Update", types.StatusClosed, 1, now)

		// Capture stderr
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		_, _, _ = MergeIssue(base, local, remote)

		w.Close()
		os.Stderr = oldStderr
		var stderrBuf bytes.Buffer
		stderrBuf.ReadFrom(r)
		stderrOutput := stderrBuf.String()

		// Should produce warning for 48h difference
		if !strings.Contains(stderrOutput, "clock skew") {
			t.Errorf("Expected clock skew warning for 48h difference, got: %s", stderrOutput)
		}
		if !strings.Contains(stderrOutput, "bd-1234") {
			t.Errorf("Warning should contain issue ID, got: %s", stderrOutput)
		}
	})

	t.Run("warning_over_24h_remote_newer", func(t *testing.T) {
		base := makeTestIssue("bd-5678", "Original", types.StatusOpen, 1, now)
		local := makeTestIssue("bd-5678", "Local Update", types.StatusInProgress, 1, now)
		remote := makeTestIssue("bd-5678", "Remote Update", types.StatusClosed, 1, now.Add(72*time.Hour))

		// Capture stderr
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		_, _, _ = MergeIssue(base, local, remote)

		w.Close()
		os.Stderr = oldStderr
		var stderrBuf bytes.Buffer
		stderrBuf.ReadFrom(r)
		stderrOutput := stderrBuf.String()

		// Should produce warning for 72h difference
		if !strings.Contains(stderrOutput, "clock skew") {
			t.Errorf("Expected clock skew warning for 72h difference, got: %s", stderrOutput)
		}
		if !strings.Contains(stderrOutput, "bd-5678") {
			t.Errorf("Warning should contain issue ID, got: %s", stderrOutput)
		}
	})

	t.Run("warning_exactly_24h", func(t *testing.T) {
		base := makeTestIssue("bd-exact", "Original", types.StatusOpen, 1, now)
		local := makeTestIssue("bd-exact", "Local Update", types.StatusInProgress, 1, now.Add(24*time.Hour))
		remote := makeTestIssue("bd-exact", "Remote Update", types.StatusClosed, 1, now)

		// Capture stderr
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		_, _, _ = MergeIssue(base, local, remote)

		w.Close()
		os.Stderr = oldStderr
		var stderrBuf bytes.Buffer
		stderrBuf.ReadFrom(r)
		stderrOutput := stderrBuf.String()

		// Exactly 24h should NOT trigger warning (> not >=)
		if strings.Contains(stderrOutput, "clock skew") {
			t.Errorf("Expected no warning for exactly 24h difference, got: %s", stderrOutput)
		}
	})
}

// TestMergeLabels tests the mergeLabels helper function directly
func TestMergeLabels(t *testing.T) {
	tests := []struct {
		name     string
		local    []string
		remote   []string
		expected []string
	}{
		{
			name:     "both_empty",
			local:    nil,
			remote:   nil,
			expected: nil,
		},
		{
			name:     "local_only",
			local:    []string{"a", "b"},
			remote:   nil,
			expected: []string{"a", "b"},
		},
		{
			name:     "remote_only",
			local:    nil,
			remote:   []string{"x", "y"},
			expected: []string{"x", "y"},
		},
		{
			name:     "no_overlap",
			local:    []string{"a", "b"},
			remote:   []string{"x", "y"},
			expected: []string{"a", "b", "x", "y"},
		},
		{
			name:     "full_overlap",
			local:    []string{"a", "b"},
			remote:   []string{"a", "b"},
			expected: []string{"a", "b"},
		},
		{
			name:     "partial_overlap",
			local:    []string{"a", "b", "c"},
			remote:   []string{"b", "c", "d"},
			expected: []string{"a", "b", "c", "d"},
		},
		{
			name:     "duplicates_in_input",
			local:    []string{"a", "a", "b"},
			remote:   []string{"b", "b", "c"},
			expected: []string{"a", "b", "c"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := mergeLabels(tc.local, tc.remote)

			// Check length
			if len(result) != len(tc.expected) {
				t.Errorf("Expected %d labels, got %d: %v", len(tc.expected), len(result), result)
				return
			}

			// Check contents (result is sorted, so direct comparison works)
			for i, expected := range tc.expected {
				if i >= len(result) || result[i] != expected {
					t.Errorf("Expected %v, got %v", tc.expected, result)
					return
				}
			}
		})
	}
}

// TestMergeFieldLevel_CompactionLevel tests compaction_level uses max strategy by default
func TestMergeFieldLevel_CompactionLevel(t *testing.T) {
	now := time.Now()

	t.Run("max_strategy_takes_higher_value", func(t *testing.T) {
		// Local has higher compaction_level
		local := makeTestIssue("bd-1234", "Local", types.StatusOpen, 1, now.Add(time.Hour))
		local.CompactionLevel = 5
		remote := makeTestIssue("bd-1234", "Remote", types.StatusOpen, 1, now)
		remote.CompactionLevel = 3

		merged, manualConflicts := mergeFieldLevel(nil, local, remote)

		if merged.CompactionLevel != 5 {
			t.Errorf("Expected compaction_level=5 (max), got %d", merged.CompactionLevel)
		}
		if len(manualConflicts) != 0 {
			t.Errorf("Expected no manual conflicts, got %d", len(manualConflicts))
		}
	})

	t.Run("max_strategy_takes_higher_from_remote", func(t *testing.T) {
		// Remote has higher compaction_level
		local := makeTestIssue("bd-1234", "Local", types.StatusOpen, 1, now.Add(time.Hour))
		local.CompactionLevel = 2
		remote := makeTestIssue("bd-1234", "Remote", types.StatusOpen, 1, now)
		remote.CompactionLevel = 7

		merged, manualConflicts := mergeFieldLevel(nil, local, remote)

		if merged.CompactionLevel != 7 {
			t.Errorf("Expected compaction_level=7 (max), got %d", merged.CompactionLevel)
		}
		if len(manualConflicts) != 0 {
			t.Errorf("Expected no manual conflicts, got %d", len(manualConflicts))
		}
	})
}

// TestMergeFieldLevel_EstimatedMinutes tests estimated_minutes uses manual strategy by default
func TestMergeFieldLevel_EstimatedMinutes(t *testing.T) {
	now := time.Now()

	t.Run("manual_strategy_flags_conflict_when_different", func(t *testing.T) {
		// Default is manual strategy - should flag conflict when values differ
		localMins := 120
		remoteMins := 60
		local := makeTestIssue("bd-1234", "Local", types.StatusOpen, 1, now.Add(time.Hour))
		local.EstimatedMinutes = &localMins
		remote := makeTestIssue("bd-1234", "Remote", types.StatusOpen, 1, now)
		remote.EstimatedMinutes = &remoteMins

		merged, manualConflicts := mergeFieldLevel(nil, local, remote)

		// Should keep local value as tentative
		if merged.EstimatedMinutes == nil || *merged.EstimatedMinutes != 120 {
			t.Errorf("Expected estimated_minutes=120 (local as tentative), got %v", merged.EstimatedMinutes)
		}
		// Should flag for manual resolution
		if len(manualConflicts) != 1 {
			t.Errorf("Expected 1 manual conflict, got %d", len(manualConflicts))
		} else {
			mc := manualConflicts[0]
			if mc.Field != "estimated_minutes" {
				t.Errorf("Expected field=estimated_minutes, got %s", mc.Field)
			}
			if mc.LocalValue != 120 {
				t.Errorf("Expected LocalValue=120, got %v", mc.LocalValue)
			}
			if mc.RemoteValue != 60 {
				t.Errorf("Expected RemoteValue=60, got %v", mc.RemoteValue)
			}
		}
	})

	t.Run("manual_strategy_no_conflict_when_same", func(t *testing.T) {
		// No conflict when values are the same
		mins := 120
		local := makeTestIssue("bd-1234", "Local", types.StatusOpen, 1, now.Add(time.Hour))
		local.EstimatedMinutes = &mins
		remote := makeTestIssue("bd-1234", "Remote", types.StatusOpen, 1, now)
		remoteMins := 120
		remote.EstimatedMinutes = &remoteMins

		merged, manualConflicts := mergeFieldLevel(nil, local, remote)

		if merged.EstimatedMinutes == nil || *merged.EstimatedMinutes != 120 {
			t.Errorf("Expected estimated_minutes=120, got %v", merged.EstimatedMinutes)
		}
		if len(manualConflicts) != 0 {
			t.Errorf("Expected no manual conflicts when values match, got %d", len(manualConflicts))
		}
	})

	t.Run("manual_strategy_nil_vs_value_flags_conflict", func(t *testing.T) {
		// Conflict when one is nil and other has value
		remoteMins := 60
		local := makeTestIssue("bd-1234", "Local", types.StatusOpen, 1, now.Add(time.Hour))
		local.EstimatedMinutes = nil
		remote := makeTestIssue("bd-1234", "Remote", types.StatusOpen, 1, now)
		remote.EstimatedMinutes = &remoteMins

		merged, manualConflicts := mergeFieldLevel(nil, local, remote)

		// Should keep local value (nil) as tentative
		if merged.EstimatedMinutes != nil {
			t.Errorf("Expected estimated_minutes=nil (local as tentative), got %v", *merged.EstimatedMinutes)
		}
		// Should flag for manual resolution
		if len(manualConflicts) != 1 {
			t.Errorf("Expected 1 manual conflict, got %d", len(manualConflicts))
		}
	})
}

// TestMaxInt tests the maxInt helper function
func TestMaxInt(t *testing.T) {
	tests := []struct {
		a, b, expected int
	}{
		{0, 0, 0},
		{1, 0, 1},
		{0, 1, 1},
		{5, 3, 5},
		{3, 5, 5},
		{-1, -2, -1},
		{-2, -1, -1},
	}

	for _, tc := range tests {
		result := maxInt(tc.a, tc.b)
		if result != tc.expected {
			t.Errorf("maxInt(%d, %d) = %d, expected %d", tc.a, tc.b, result, tc.expected)
		}
	}
}

// TestMaxIntPtr tests the maxIntPtr helper function
func TestMaxIntPtr(t *testing.T) {
	five := 5
	three := 3

	t.Run("both_nil_returns_nil", func(t *testing.T) {
		result := maxIntPtr(nil, nil)
		if result != nil {
			t.Errorf("Expected nil, got %v", result)
		}
	})

	t.Run("left_nil_returns_right", func(t *testing.T) {
		result := maxIntPtr(nil, &three)
		if result == nil || *result != 3 {
			t.Errorf("Expected 3, got %v", result)
		}
	})

	t.Run("right_nil_returns_left", func(t *testing.T) {
		result := maxIntPtr(&five, nil)
		if result == nil || *result != 5 {
			t.Errorf("Expected 5, got %v", result)
		}
	})

	t.Run("returns_larger_value", func(t *testing.T) {
		result := maxIntPtr(&three, &five)
		if result == nil || *result != 5 {
			t.Errorf("Expected 5, got %v", result)
		}

		result = maxIntPtr(&five, &three)
		if result == nil || *result != 5 {
			t.Errorf("Expected 5, got %v", result)
		}
	})
}

// TestMergeResult_ManualConflicts tests that ManualConflicts is properly initialized
func TestMergeResult_ManualConflicts(t *testing.T) {
	now := time.Now()
	base := []*types.Issue{
		makeTestIssue("bd-1234", "Base", types.StatusOpen, 1, now),
	}
	local := []*types.Issue{
		makeTestIssue("bd-1234", "Local", types.StatusOpen, 1, now.Add(time.Hour)),
	}
	remote := []*types.Issue{
		makeTestIssue("bd-1234", "Remote", types.StatusOpen, 1, now.Add(2*time.Hour)),
	}

	result := MergeIssues(base, local, remote)

	if result.ManualConflicts == nil {
		t.Error("ManualConflicts should be initialized, got nil")
	}
}
