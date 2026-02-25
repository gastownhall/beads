package fix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// uniqueDBName generates a unique database name for test isolation.
func uniqueDBName(t *testing.T) string {
	t.Helper()
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	return "mig_" + hex.EncodeToString(h[:6])
}

// setupTestBeadsDir creates a .beads dir with Dolt config pointing at the test server.
func setupTestBeadsDir(t *testing.T, tmpDir string) string {
	t.Helper()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	port := fixTestServerPort()
	dbName := uniqueDBName(t)

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		DoltDatabase:   dbName,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// writeTestJSONL writes issues to a JSONL file in the beads dir.
func writeTestJSONL(t *testing.T, beadsDir string, issues []types.Issue) string {
	t.Helper()
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, issue := range issues {
		data, _ := json.Marshal(issue)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	return jsonlPath
}

// TestImportJSONLIntoStore verifies that importJSONLIntoStore correctly reads
// a JSONL file and imports all issues into a Dolt store.
func TestImportJSONLIntoStore(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	beadsDir := setupTestBeadsDir(t, tmpDir)

	// Create Dolt store
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Skipf("skipping: Dolt not available: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Write test JSONL
	issues := []types.Issue{
		{ID: "test-1", Title: "First issue", Status: "open", IssueType: "task", Priority: 2},
		{ID: "test-2", Title: "Second issue", Status: "closed", IssueType: "bug", Priority: 1},
		{ID: "test-3", Title: "Third issue", Status: "open", IssueType: "feature", Priority: 3},
	}
	jsonlPath := writeTestJSONL(t, beadsDir, issues)

	// Import
	count, err := importJSONLIntoStore(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("importJSONLIntoStore failed: %v", err)
	}
	if count != 3 {
		t.Errorf("imported %d issues, want 3", count)
	}

	// Verify issues are in the store
	stored, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(stored) != 3 {
		t.Errorf("store has %d issues, want 3", len(stored))
	}

	// Verify prefix was detected
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if prefix != "test" {
		t.Errorf("prefix = %q, want %q", prefix, "test")
	}
}

// TestImportJSONLIntoStore_EmptyFile verifies empty JSONL is handled gracefully.
func TestImportJSONLIntoStore_EmptyFile(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	beadsDir := setupTestBeadsDir(t, tmpDir)

	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Skipf("skipping: Dolt not available: %v", err)
	}
	defer func() { _ = store.Close() }()

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	count, err := importJSONLIntoStore(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("importJSONLIntoStore failed: %v", err)
	}
	if count != 0 {
		t.Errorf("imported %d issues, want 0", count)
	}
}

// TestDatabaseVersionWithBdVersion_ImportsJSONL verifies that creating a new
// Dolt store via DatabaseVersion also imports issues from JSONL if present.
func TestDatabaseVersionWithBdVersion_ImportsJSONL(t *testing.T) {
	ctx := context.Background()

	// Create project with .beads dir, config, and JSONL but no Dolt store
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Initialize git repo (needed for validateBeadsWorkspace)
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	port := fixTestServerPort()
	dbName := uniqueDBName(t)

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		DoltDatabase:   dbName,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	// Pre-flight: verify Dolt server is reachable (skip in CI without server)
	probe, probeErr := dolt.NewFromConfig(ctx, beadsDir)
	if probeErr != nil {
		t.Skipf("skipping: Dolt not available: %v", probeErr)
	}
	_ = probe.Close()

	// Write JSONL with issues
	issues := []types.Issue{
		{ID: "mdp-1", Title: "Test migration", Status: "open", IssueType: "task", Priority: 2},
		{ID: "mdp-2", Title: "Another issue", Status: "closed", IssueType: "bug", Priority: 1},
	}
	writeTestJSONL(t, beadsDir, issues)

	// Run the Database fix — should create store AND import
	if err := DatabaseVersionWithBdVersion(tmpDir, "0.56.1"); err != nil {
		t.Fatalf("DatabaseVersionWithBdVersion failed: %v", err)
	}

	// Verify Dolt store was created and has issues
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	stored, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(stored) != 2 {
		t.Errorf("store has %d issues, want 2", len(stored))
	}
}
