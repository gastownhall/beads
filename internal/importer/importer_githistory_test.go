package importer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/deletions"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestAutoImportPurgesBugBd4pv tests that auto-import doesn't incorrectly purge
// issues due to git history backfill finding them in old commits.
// This is a reproduction test for bd-4pv.
func TestAutoImportPurgesBugBd4pv(t *testing.T) {
	// Create a temp directory for a test git repo
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "test-repo")
	beadsDir := filepath.Join(repoDir, ".beads")

	// Initialize git repo
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init git repo: %v\n%s", err, out)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git email: %v", err)
	}
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git name: %v", err)
	}

	// Create .beads directory
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create initial issues.jsonl with 5 issues
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	initialContent := `{"id":"bd-abc1","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-abc2","title":"Issue 2","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-abc3","title":"Issue 3","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-abc4","title":"Issue 4","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-abc5","title":"Issue 5","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("failed to write initial JSONL: %v", err)
	}

	// Commit the initial state
	cmd = exec.Command("git", "add", ".beads/issues.jsonl")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "Initial issues")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git commit: %v\n%s", err, out)
	}

	// Now simulate what happens during auto-import:
	// 1. Database is empty
	// 2. Auto-import detects issues in git and imports them

	ctx := context.Background()
	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Set up prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Parse the JSONL issues
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-abc1", Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-abc2", Title: "Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-abc3", Title: "Issue 3", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-abc4", Title: "Issue 4", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-abc5", Title: "Issue 5", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
	}

	// Do the import WITHOUT NoGitHistory (the buggy behavior)
	opts := Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
		NoGitHistory:         false, // Bug: should be true for auto-import
	}

	result, err := ImportIssues(ctx, dbPath, store, issues, opts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Check how many issues are in the database
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	// With the bug, some or all issues might be purged
	// because git history finds them in the commit and thinks they were "deleted"
	t.Logf("Import result: created=%d, updated=%d, purged=%d, purgedIDs=%v",
		result.Created, result.Updated, result.Purged, result.PurgedIDs)
	t.Logf("Issues in DB after import: %d", len(allIssues))

	// The correct behavior is 5 issues in DB
	// The bug would result in fewer (potentially 0) due to incorrect purging
	if len(allIssues) != 5 {
		t.Errorf("Expected 5 issues in DB, got %d. This is the bd-4pv bug!", len(allIssues))
		t.Logf("Purged IDs: %v", result.PurgedIDs)
	}
}

// TestGitHistoryBackfillPurgesLocalIssues tests the scenario where git history
// backfill incorrectly purges issues that exist locally but were never in the remote JSONL.
// This is another aspect of the bd-4pv bug.
func TestGitHistoryBackfillPurgesLocalIssues(t *testing.T) {
	// Create a temp directory for a test git repo
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "test-repo")
	beadsDir := filepath.Join(repoDir, ".beads")

	// Initialize git repo
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init git repo: %v\n%s", err, out)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git email: %v", err)
	}
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git name: %v", err)
	}

	// Create .beads directory
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create initial issues.jsonl with 1 issue
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	initialContent := `{"id":"bd-shared1","title":"Shared Issue","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("failed to write initial JSONL: %v", err)
	}

	// Create empty deletions.jsonl
	deletionsPath := deletions.DefaultPath(beadsDir)
	if err := os.WriteFile(deletionsPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write deletions: %v", err)
	}

	// Commit the initial state
	cmd = exec.Command("git", "add", ".beads/")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "Initial issues")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git commit: %v\n%s", err, out)
	}

	// Create database with the shared issue AND local issues
	ctx := context.Background()
	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Set up prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create issues in DB: 1 shared (in JSONL) + 4 local-only
	now := time.Now()
	dbIssues := []*types.Issue{
		{ID: "bd-shared1", Title: "Shared Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-local1", Title: "Local Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-local2", Title: "Local Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-local3", Title: "Local Issue 3", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-local4", Title: "Local Issue 4", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	for _, issue := range dbIssues {
		issue.ContentHash = issue.ComputeContentHash()
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Verify DB has 5 issues
	allBefore, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	if len(allBefore) != 5 {
		t.Fatalf("Expected 5 issues before import, got %d", len(allBefore))
	}

	// Now import from JSONL (only has 1 issue: bd-shared1)
	// WITHOUT NoGitHistory - this is the bug
	incomingIssues := []*types.Issue{
		{ID: "bd-shared1", Title: "Shared Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
	}

	opts := Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
		NoGitHistory:         false, // Bug: local issues might be purged if they appear in git history
	}

	result, err := ImportIssues(ctx, dbPath, store, incomingIssues, opts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	t.Logf("Import result: created=%d, updated=%d, unchanged=%d, purged=%d, purgedIDs=%v",
		result.Created, result.Updated, result.Unchanged, result.Purged, result.PurgedIDs)

	// Check how many issues are in the database
	allAfter, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	t.Logf("Issues in DB after import: %d", len(allAfter))
	for _, issue := range allAfter {
		t.Logf("  - %s: %s", issue.ID, issue.Title)
	}

	// Expected: bd-shared1 + bd-local1..4 = 5 issues
	// The local issues should NOT be purged because:
	// 1. They're not in the deletions manifest
	// 2. They were never in git history (they're local-only)
	// 3. NoGitHistory=false but git history check shouldn't find bd-local* in history
	if len(allAfter) != 5 {
		t.Errorf("Expected 5 issues in DB, got %d. Local issues may have been incorrectly purged!", len(allAfter))
	}

	// Should have no purges (bd-local* were never in git history)
	if result.Purged != 0 {
		t.Errorf("Expected 0 purged issues, got %d (IDs: %v)", result.Purged, result.PurgedIDs)
	}
}

// TestNoGitHistoryPreventsIncorrectPurge tests that setting NoGitHistory prevents
// the purge of issues that exist in the DB but not in JSONL during auto-import.
// This is the fix for bd-4pv - auto-import should NOT run git history backfill.
func TestNoGitHistoryPreventsIncorrectPurge(t *testing.T) {
	// Create a temp directory for a test git repo
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "test-repo")
	beadsDir := filepath.Join(repoDir, ".beads")

	// Initialize git repo
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init git repo: %v\n%s", err, out)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git email: %v", err)
	}
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git name: %v", err)
	}

	// Create .beads directory
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create issues.jsonl with 1 issue
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	initialContent := `{"id":"bd-shared1","title":"Shared Issue","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("failed to write initial JSONL: %v", err)
	}

	// Create empty deletions.jsonl
	deletionsPath := deletions.DefaultPath(beadsDir)
	if err := os.WriteFile(deletionsPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write deletions: %v", err)
	}

	// Commit
	cmd = exec.Command("git", "add", ".beads/")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "Initial issues")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git commit: %v\n%s", err, out)
	}

	// Create database with 5 issues (1 shared + 4 local-only)
	ctx := context.Background()
	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Set up prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Create all 5 issues in DB
	now := time.Now()
	dbIssues := []*types.Issue{
		{ID: "bd-shared1", Title: "Shared Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-local1", Title: "Local Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-local2", Title: "Local Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-local3", Title: "Local Issue 3", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-local4", Title: "Local Issue 4", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	for _, issue := range dbIssues {
		issue.ContentHash = issue.ComputeContentHash()
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	// Import from JSONL (only has 1 issue) WITH NoGitHistory=true (the fix)
	incomingIssues := []*types.Issue{
		{ID: "bd-shared1", Title: "Shared Issue", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
	}

	opts := Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
		NoGitHistory:         true, // Fix: skip git history backfill during auto-import
	}

	result, err := ImportIssues(ctx, dbPath, store, incomingIssues, opts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	t.Logf("Import result: created=%d, updated=%d, unchanged=%d, purged=%d, purgedIDs=%v",
		result.Created, result.Updated, result.Unchanged, result.Purged, result.PurgedIDs)

	// Check how many issues are in the database
	allAfter, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	t.Logf("Issues in DB after import: %d", len(allAfter))
	for _, issue := range allAfter {
		t.Logf("  - %s: %s", issue.ID, issue.Title)
	}

	// With NoGitHistory=true, the 4 local issues should NOT be purged
	// because we skip git history backfill entirely during auto-import.
	// This is the correct behavior for auto-import - local work should be preserved.
	// Expected: all 5 issues remain
	if len(allAfter) != 5 {
		t.Errorf("Expected 5 issues in DB (local work preserved), got %d", len(allAfter))
	}

	// Should have no purges
	if result.Purged != 0 {
		t.Errorf("Expected 0 purged issues (NoGitHistory prevents purge), got %d (IDs: %v)", result.Purged, result.PurgedIDs)
	}
}

// TestAutoImportWithNoGitHistoryFlag tests the fix for bd-4pv
func TestAutoImportWithNoGitHistoryFlag(t *testing.T) {
	// Create a temp directory for a test git repo
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "test-repo")
	beadsDir := filepath.Join(repoDir, ".beads")

	// Initialize git repo
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init git repo: %v\n%s", err, out)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git email: %v", err)
	}
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git name: %v", err)
	}

	// Create .beads directory
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create initial issues.jsonl with 5 issues
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	initialContent := `{"id":"bd-xyz1","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-xyz2","title":"Issue 2","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-xyz3","title":"Issue 3","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-xyz4","title":"Issue 4","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-xyz5","title":"Issue 5","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("failed to write initial JSONL: %v", err)
	}

	// Also create a deletions.jsonl (empty)
	deletionsPath := deletions.DefaultPath(beadsDir)
	if err := os.WriteFile(deletionsPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write deletions: %v", err)
	}

	// Commit the initial state
	cmd = exec.Command("git", "add", ".beads/")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "Initial issues")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git commit: %v\n%s", err, out)
	}

	ctx := context.Background()
	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Set up prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Parse the JSONL issues
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-xyz1", Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-xyz2", Title: "Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-xyz3", Title: "Issue 3", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-xyz4", Title: "Issue 4", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-xyz5", Title: "Issue 5", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
	}

	// Do the import WITH NoGitHistory (the fix)
	opts := Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
		NoGitHistory:         true, // Fix: skip git history backfill during auto-import
	}

	result, err := ImportIssues(ctx, dbPath, store, issues, opts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Check how many issues are in the database
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	t.Logf("Import result: created=%d, updated=%d, purged=%d",
		result.Created, result.Updated, result.Purged)
	t.Logf("Issues in DB after import: %d", len(allIssues))

	// With the fix, all 5 issues should be in DB
	if len(allIssues) != 5 {
		t.Errorf("Expected 5 issues in DB, got %d", len(allIssues))
	}

	// Should have no purges
	if result.Purged != 0 {
		t.Errorf("Expected 0 purged issues, got %d", result.Purged)
	}
}

// TestMassDeletionSafetyGuard tests the fix for bd-21a where git-history-backfill
// would incorrectly purge the entire database when a JSONL was reset.
// The safety guard should abort if >50% of issues would be deleted.
func TestMassDeletionSafetyGuard(t *testing.T) {
	// Create a temp directory for a test git repo
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "test-repo")
	beadsDir := filepath.Join(repoDir, ".beads")

	// Initialize git repo
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init git repo: %v\n%s", err, out)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git email: %v", err)
	}
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to config git name: %v", err)
	}

	// Create .beads directory
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create initial issues.jsonl with 10 issues
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	initialContent := `{"id":"bd-mass01","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass02","title":"Issue 2","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass03","title":"Issue 3","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass04","title":"Issue 4","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass05","title":"Issue 5","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass06","title":"Issue 6","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass07","title":"Issue 7","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass08","title":"Issue 8","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass09","title":"Issue 9","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass10","title":"Issue 10","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("failed to write initial JSONL: %v", err)
	}

	// Also create a deletions.jsonl (empty)
	deletionsPath := deletions.DefaultPath(beadsDir)
	if err := os.WriteFile(deletionsPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write deletions: %v", err)
	}

	// Commit the initial state
	cmd = exec.Command("git", "add", ".beads/")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "Initial issues with 10 entries")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git commit: %v\n%s", err, out)
	}

	ctx := context.Background()
	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Set up prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	// First, import all 10 issues to the database
	now := time.Now()
	allIssues := []*types.Issue{
		{ID: "bd-mass01", Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass02", Title: "Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass03", Title: "Issue 3", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass04", Title: "Issue 4", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass05", Title: "Issue 5", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass06", Title: "Issue 6", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass07", Title: "Issue 7", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass08", Title: "Issue 8", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass09", Title: "Issue 9", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass10", Title: "Issue 10", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
	}

	// Initial import - NoGitHistory to just populate the DB
	opts := Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
		NoGitHistory:         true,
	}

	_, err = ImportIssues(ctx, dbPath, store, allIssues, opts)
	if err != nil {
		t.Fatalf("initial import failed: %v", err)
	}

	// Verify all 10 issues are in DB
	dbIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	if len(dbIssues) != 10 {
		t.Fatalf("Expected 10 issues after initial import, got %d", len(dbIssues))
	}

	// Now simulate a "reset" scenario:
	// JSONL is reset to only have 2 issues (80% would be deleted)
	resetContent := `{"id":"bd-mass01","title":"Issue 1","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"bd-mass02","title":"Issue 2","status":"open","priority":1,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
	if err := os.WriteFile(jsonlPath, []byte(resetContent), 0644); err != nil {
		t.Fatalf("failed to write reset JSONL: %v", err)
	}

	// Commit the reset state
	cmd = exec.Command("git", "add", ".beads/issues.jsonl")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git add reset: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "Reset JSONL to 2 issues")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git commit reset: %v\n%s", err, out)
	}

	// Now try to import the reset JSONL WITH git history enabled
	// This should trigger the safety guard since 8/10 = 80% > 50%
	resetIssues := []*types.Issue{
		{ID: "bd-mass01", Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "bd-mass02", Title: "Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
	}

	opts = Options{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
		NoGitHistory:         false, // Enable git history - this is the test!
	}

	result, err := ImportIssues(ctx, dbPath, store, resetIssues, opts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// The safety guard should have prevented any purges
	// because 8/10 = 80% > 50% threshold
	t.Logf("Import result: created=%d, updated=%d, unchanged=%d, purged=%d",
		result.Created, result.Updated, result.Unchanged, result.Purged)

	// Verify all 10 issues are STILL in DB (safety guard prevented deletion)
	dbIssues, err = store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues after reset import: %v", err)
	}

	t.Logf("Issues in DB after reset import: %d", len(dbIssues))

	if len(dbIssues) != 10 {
		t.Errorf("Expected 10 issues in DB (safety guard should prevent purge), got %d", len(dbIssues))
	}

	if result.Purged != 0 {
		t.Errorf("Expected 0 purged issues (safety guard), got %d (IDs: %v)", result.Purged, result.PurgedIDs)
	}
}
