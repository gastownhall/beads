package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestExportToJSONLWithStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create test issue
	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: "Test description",
		IssueType:   types.TypeBug,
		Priority:    1,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Export to JSONL
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("exportToJSONLWithStore failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Fatal("JSONL file was not created")
	}

	// Read and verify content
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read JSONL: %v", err)
	}

	var exported types.Issue
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatalf("failed to unmarshal JSONL: %v", err)
	}

	if exported.ID != "test-1" {
		t.Errorf("expected ID 'test-1', got %s", exported.ID)
	}
	if exported.Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %s", exported.Title)
	}
}

func TestExportToJSONLWithStore_EmptyDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage (empty)
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create existing JSONL with content
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	existingIssue := &types.Issue{
		ID:        "existing-1",
		Title:     "Existing",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	data, _ := json.Marshal(existingIssue)
	if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write existing JSONL: %v", err)
	}

	// Should refuse to export empty DB over non-empty JSONL
	err = exportToJSONLWithStore(ctx, store, jsonlPath)
	if err == nil {
		t.Fatal("expected error when exporting empty DB over non-empty JSONL")
	}
}

func TestImportToJSONLWithStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage first to initialize database
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create JSONL with test data
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: "Test description",
		IssueType:   types.TypeBug,
		Priority:    1,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	data, _ := json.Marshal(issue)
	if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}

	// Import from JSONL
	if err := importToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("importToJSONLWithStore failed: %v", err)
	}

	// Verify issue was imported
	imported, err := store.GetIssue(ctx, "test-1")
	if err != nil {
		t.Fatalf("failed to get imported issue: %v", err)
	}

	if imported.Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %s", imported.Title)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage and add issues
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create multiple issues with dependencies
	issue1 := &types.Issue{
		ID:        "test-1",
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	issue2 := &types.Issue{
		ID:        "test-2",
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeFeature,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("failed to create issue1: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("failed to create issue2: %v", err)
	}

	// Add dependency
	dep := &types.Dependency{
		IssueID:     "test-2",
		DependsOnID: "test-1",
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Add labels
	if err := store.AddLabel(ctx, "test-1", "bug", "test"); err != nil {
		t.Fatalf("failed to add label: %v", err)
	}

	// Export
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Create new database
	dbPath2 := filepath.Join(tmpDir, ".beads", "beads2.db")
	store2, err := sqlite.New(context.Background(), dbPath2)
	if err != nil {
		t.Fatalf("failed to create store2: %v", err)
	}
	defer store2.Close()

	// Set issue_prefix for second database
	if err := store2.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix for store2: %v", err)
	}

	// Import
	if err := importToJSONLWithStore(ctx, store2, jsonlPath); err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Verify issues
	imported1, err := store2.GetIssue(ctx, "test-1")
	if err != nil {
		t.Fatalf("failed to get imported issue1: %v", err)
	}
	if imported1.Title != "Issue 1" {
		t.Errorf("expected title 'Issue 1', got %s", imported1.Title)
	}

	imported2, err := store2.GetIssue(ctx, "test-2")
	if err != nil {
		t.Fatalf("failed to get imported issue2: %v", err)
	}
	if imported2.Title != "Issue 2" {
		t.Errorf("expected title 'Issue 2', got %s", imported2.Title)
	}

	// Verify dependency
	deps, err := store2.GetDependencies(ctx, "test-2")
	if err != nil {
		t.Fatalf("failed to get dependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "test-1" {
		t.Errorf("expected dependency test-2 -> test-1, got %v", deps)
	}

	// Verify labels
	labels, err := store2.GetLabels(ctx, "test-1")
	if err != nil {
		t.Fatalf("failed to get labels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "bug" {
		t.Errorf("expected label 'bug', got %v", labels)
	}
}

// TestExportUpdatesMetadata verifies that export updates last_import_hash metadata (bd-ymj fix)
func TestExportUpdatesMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create test issue
	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: "Test description",
		IssueType:   types.TypeBug,
		Priority:    1,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// First export
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("first export failed: %v", err)
	}

	// Update metadata using the actual daemon helper function (bd-ar2.3 fix)
	// This verifies that updateExportMetadata (used by createExportFunc and createSyncFunc) works correctly
	mockLogger := newTestLogger()
	updateExportMetadata(ctx, store, jsonlPath, mockLogger, "")

	// Verify metadata was set (renamed from last_import_hash to jsonl_content_hash - bd-39o)
	lastHash, err := store.GetMetadata(ctx, "jsonl_content_hash")
	if err != nil {
		t.Fatalf("failed to get jsonl_content_hash: %v", err)
	}
	if lastHash == "" {
		t.Error("expected jsonl_content_hash to be set after export")
	}

	lastTime, err := store.GetMetadata(ctx, "last_import_time")
	if err != nil {
		t.Fatalf("failed to get last_import_time: %v", err)
	}
	if lastTime == "" {
		t.Error("expected last_import_time to be set after export")
	}

	// Second export should succeed without "content has changed" error
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("second export failed (metadata not updated properly): %v", err)
	}

	// Verify validatePreExport doesn't fail with "content has changed"
	if err := validatePreExport(ctx, store, jsonlPath); err != nil {
		t.Fatalf("validatePreExport failed after metadata update: %v", err)
	}
}

func TestUpdateExportMetadataMultiRepo(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath1 := filepath.Join(tmpDir, "repo1", ".beads", "issues.jsonl")
	jsonlPath2 := filepath.Join(tmpDir, "repo2", ".beads", "issues.jsonl")

	// Create storage
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create test issues for each repo
	issue1 := &types.Issue{
		ID:          "test-1",
		Title:       "Test Issue 1",
		Description: "Repo 1 issue",
		IssueType:   types.TypeBug,
		Priority:    1,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		SourceRepo:  "repo1",
	}
	issue2 := &types.Issue{
		ID:          "test-2",
		Title:       "Test Issue 2",
		Description: "Repo 2 issue",
		IssueType:   types.TypeBug,
		Priority:    1,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		SourceRepo:  "repo2",
	}

	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("failed to create issue1: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("failed to create issue2: %v", err)
	}

	// Create directories for JSONL files
	if err := os.MkdirAll(filepath.Dir(jsonlPath1), 0755); err != nil {
		t.Fatalf("failed to create dir for jsonlPath1: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(jsonlPath2), 0755); err != nil {
		t.Fatalf("failed to create dir for jsonlPath2: %v", err)
	}

	// Export issues to JSONL files
	if err := exportToJSONLWithStore(ctx, store, jsonlPath1); err != nil {
		t.Fatalf("failed to export to jsonlPath1: %v", err)
	}
	if err := exportToJSONLWithStore(ctx, store, jsonlPath2); err != nil {
		t.Fatalf("failed to export to jsonlPath2: %v", err)
	}

	// Create mock logger
	mockLogger := newTestLogger()

	// Update metadata for each repo with different keys (bd-ar2.2 multi-repo support)
	updateExportMetadata(ctx, store, jsonlPath1, mockLogger, jsonlPath1)
	updateExportMetadata(ctx, store, jsonlPath2, mockLogger, jsonlPath2)

	// Verify per-repo metadata was set with correct keys (bd-web8: keys are sanitized)
	// Renamed from last_import_hash to jsonl_content_hash (bd-39o)
	hash1Key := "jsonl_content_hash:" + sanitizeMetadataKey(jsonlPath1)
	hash1, err := store.GetMetadata(ctx, hash1Key)
	if err != nil {
		t.Fatalf("failed to get %s: %v", hash1Key, err)
	}
	if hash1 == "" {
		t.Errorf("expected %s to be set", hash1Key)
	}

	hash2Key := "jsonl_content_hash:" + sanitizeMetadataKey(jsonlPath2)
	hash2, err := store.GetMetadata(ctx, hash2Key)
	if err != nil {
		t.Fatalf("failed to get %s: %v", hash2Key, err)
	}
	if hash2 == "" {
		t.Errorf("expected %s to be set", hash2Key)
	}

	// Verify that single-repo metadata key is NOT set (we're using per-repo keys)
	globalHash, err := store.GetMetadata(ctx, "jsonl_content_hash")
	if err != nil {
		t.Fatalf("failed to get jsonl_content_hash: %v", err)
	}
	if globalHash != "" {
		t.Error("expected global jsonl_content_hash to not be set when using per-repo keys")
	}

	// Note: last_import_mtime removed in bd-v0y fix (git doesn't preserve mtime)
}

// TestExportWithMultiRepoConfigUpdatesAllMetadata verifies that export with multi-repo
// config correctly updates metadata for ALL JSONL files with proper keySuffix (bd-ar2.8)
func TestExportWithMultiRepoConfigUpdatesAllMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	primaryDir := filepath.Join(tmpDir, "primary")
	additionalDir := filepath.Join(tmpDir, "additional")

	// Set up directory structure
	for _, dir := range []string{primaryDir, additionalDir} {
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create %s: %v", beadsDir, err)
		}
	}

	dbPath := filepath.Join(primaryDir, ".beads", "beads.db")
	primaryJSONL := filepath.Join(primaryDir, ".beads", "issues.jsonl")
	additionalJSONL := filepath.Join(additionalDir, ".beads", "issues.jsonl")

	// Create storage
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create test issues
	issue1 := &types.Issue{
		ID:          "test-1",
		Title:       "Primary Issue",
		Description: "Issue in primary repo",
		IssueType:   types.TypeBug,
		Priority:    1,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		SourceRepo:  primaryDir,
	}
	issue2 := &types.Issue{
		ID:          "test-2",
		Title:       "Additional Issue",
		Description: "Issue in additional repo",
		IssueType:   types.TypeFeature,
		Priority:    2,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		SourceRepo:  additionalDir,
	}

	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("failed to create issue1: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("failed to create issue2: %v", err)
	}

	// Export to both JSONL files
	if err := exportToJSONLWithStore(ctx, store, primaryJSONL); err != nil {
		t.Fatalf("failed to export to primary JSONL: %v", err)
	}
	if err := exportToJSONLWithStore(ctx, store, additionalJSONL); err != nil {
		t.Fatalf("failed to export to additional JSONL: %v", err)
	}

	// Simulate multi-repo export flow (as in createExportFunc)
	// This tests the full integration: getMultiRepoJSONLPaths -> getRepoKeyForPath -> updateExportMetadata
	mockLogger := newTestLogger()

	// Simulate multi-repo mode with stable keys
	multiRepoPaths := []string{primaryJSONL, additionalJSONL}
	repoKeys := []string{primaryDir, additionalDir}

	for i, path := range multiRepoPaths {
		repoKey := repoKeys[i]
		updateExportMetadata(ctx, store, path, mockLogger, repoKey)
	}

	// Verify metadata for primary repo (bd-web8: keys are sanitized, bd-39o: renamed key)
	primaryHashKey := "jsonl_content_hash:" + sanitizeMetadataKey(primaryDir)
	primaryHash, err := store.GetMetadata(ctx, primaryHashKey)
	if err != nil {
		t.Fatalf("failed to get %s: %v", primaryHashKey, err)
	}
	if primaryHash == "" {
		t.Errorf("expected %s to be set after export", primaryHashKey)
	}

	primaryTimeKey := "last_import_time:" + sanitizeMetadataKey(primaryDir)
	primaryTime, err := store.GetMetadata(ctx, primaryTimeKey)
	if err != nil {
		t.Fatalf("failed to get %s: %v", primaryTimeKey, err)
	}
	if primaryTime == "" {
		t.Errorf("expected %s to be set after export", primaryTimeKey)
	}

	// Note: last_import_mtime removed in bd-v0y fix (git doesn't preserve mtime)

	// Verify metadata for additional repo (bd-web8: keys are sanitized, bd-39o: renamed key)
	additionalHashKey := "jsonl_content_hash:" + sanitizeMetadataKey(additionalDir)
	additionalHash, err := store.GetMetadata(ctx, additionalHashKey)
	if err != nil {
		t.Fatalf("failed to get %s: %v", additionalHashKey, err)
	}
	if additionalHash == "" {
		t.Errorf("expected %s to be set after export", additionalHashKey)
	}

	additionalTimeKey := "last_import_time:" + sanitizeMetadataKey(additionalDir)
	additionalTime, err := store.GetMetadata(ctx, additionalTimeKey)
	if err != nil {
		t.Fatalf("failed to get %s: %v", additionalTimeKey, err)
	}
	if additionalTime == "" {
		t.Errorf("expected %s to be set after export", additionalTimeKey)
	}

	// Note: last_import_mtime removed in bd-v0y fix (git doesn't preserve mtime)

	// Note: In this test both JSONL files have the same content (all issues),
	// so hashes will be identical. In real multi-repo mode, ExportToMultiRepo
	// filters by SourceRepo, so hashes would differ. What matters here is that
	// metadata is set with correct per-repo keys.

	// Verify global metadata keys are NOT set (multi-repo mode uses suffixed keys)
	globalHash, err := store.GetMetadata(ctx, "jsonl_content_hash")
	if err != nil {
		t.Fatalf("failed to get jsonl_content_hash: %v", err)
	}
	if globalHash != "" {
		t.Error("expected global jsonl_content_hash to not be set in multi-repo mode")
	}

	// Test that subsequent exports don't fail with "content has changed" error
	if err := exportToJSONLWithStore(ctx, store, primaryJSONL); err != nil {
		t.Errorf("second export to primary JSONL failed (metadata not updated properly): %v", err)
	}
	if err := exportToJSONLWithStore(ctx, store, additionalJSONL); err != nil {
		t.Errorf("second export to additional JSONL failed (metadata not updated properly): %v", err)
	}
}

// TestUpdateExportMetadataInvalidKeySuffix verifies that invalid keySuffix is rejected (bd-ar2.12)
func TestUpdateExportMetadataInvalidKeySuffix(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create test issue
	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: "Test description",
		IssueType:   types.TypeBug,
		Priority:    1,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Export to JSONL
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Create mock logger
	mockLogger := newTestLogger()

	// Update metadata with keySuffix containing ':' (bd-web8: should be auto-sanitized)
	// This simulates Windows absolute paths like "C:\Users\..."
	keySuffixWithColon := "C:/Users/repo/path"
	updateExportMetadata(ctx, store, jsonlPath, mockLogger, keySuffixWithColon)

	// Verify metadata WAS set with sanitized key (colons replaced with underscores)
	// bd-39o: renamed from last_import_hash to jsonl_content_hash
	sanitized := sanitizeMetadataKey(keySuffixWithColon)
	sanitizedKey := "jsonl_content_hash:" + sanitized
	hash, err := store.GetMetadata(ctx, sanitizedKey)
	if err != nil {
		t.Fatalf("failed to get metadata: %v", err)
	}
	if hash == "" {
		t.Errorf("expected metadata to be set with sanitized key %s", sanitizedKey)
	}

	// Verify that the original unsanitized key was NOT used
	unsanitizedKey := "jsonl_content_hash:" + keySuffixWithColon
	unsanitizedHash, err := store.GetMetadata(ctx, unsanitizedKey)
	if err != nil {
		t.Fatalf("failed to check unsanitized key: %v", err)
	}
	if unsanitizedHash != "" {
		t.Errorf("expected unsanitized key %s to NOT be set", unsanitizedKey)
	}
}

// TestExportToJSONLWithStore_IncludesTombstones verifies that tombstones are included
// in JSONL export by the daemon. This is a regression test for the bug where
// exportToJSONLWithStore used an empty IssueFilter (IncludeTombstones: false),
// causing deleted issues to not propagate via sync branch to other clones.
//
// Bug scenario:
// 1. User runs `bd delete <issue>` with daemon active
// 2. Database correctly marks issue as tombstone
// 3. Main .beads/issues.jsonl correctly shows status:"tombstone"
// 4. But sync branch worktree JSONL showed status:"open" (bug)
// 5. Other clones would not see the deletion
func TestExportToJSONLWithStore_IncludesTombstones(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create an open issue
	openIssue := &types.Issue{
		ID:        "test-1",
		Title:     "Open Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, openIssue, "test"); err != nil {
		t.Fatalf("failed to create open issue: %v", err)
	}

	// Create a tombstone issue (deleted)
	tombstoneIssue := &types.Issue{
		ID:        "test-2",
		Title:     "Deleted Issue",
		Status:    types.StatusTombstone,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, tombstoneIssue, "test"); err != nil {
		t.Fatalf("failed to create tombstone issue: %v", err)
	}

	// Export to JSONL using daemon's export function
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("exportToJSONLWithStore failed: %v", err)
	}

	// Read and parse the exported JSONL
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read JSONL: %v", err)
	}

	// Parse JSONL (one JSON object per line)
	lines := splitJSONLLines(data)
	if len(lines) != 2 {
		t.Fatalf("expected 2 issues in JSONL, got %d", len(lines))
	}

	// Verify both issues are present (including tombstone)
	var foundOpen, foundTombstone bool
	for _, line := range lines {
		var issue types.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			t.Fatalf("failed to unmarshal issue: %v", err)
		}
		if issue.ID == "test-1" && issue.Status == types.StatusOpen {
			foundOpen = true
		}
		if issue.ID == "test-2" && issue.Status == types.StatusTombstone {
			foundTombstone = true
		}
	}

	if !foundOpen {
		t.Error("expected open issue (test-1) to be in JSONL export")
	}
	if !foundTombstone {
		t.Error("expected tombstone issue (test-2) to be in JSONL export - tombstones must be included for sync propagation")
	}
}

// splitJSONLLines splits JSONL content into individual JSON lines
func splitJSONLLines(data []byte) [][]byte {
	var lines [][]byte
	var currentLine []byte
	for _, b := range data {
		if b == '\n' {
			if len(currentLine) > 0 {
				lines = append(lines, currentLine)
				currentLine = nil
			}
		} else {
			currentLine = append(currentLine, b)
		}
	}
	if len(currentLine) > 0 {
		lines = append(lines, currentLine)
	}
	return lines
}
