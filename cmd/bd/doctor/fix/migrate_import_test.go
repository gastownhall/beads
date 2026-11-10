//go:build cgo

package fix

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
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

	requireFixDoltContainer(t)
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

	// Create Dolt store. CreateIfMissing: the per-test database does not
	// exist yet and the dolt.New create-guard refuses to create it
	// implicitly (bd-nxt5e).
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
	if err != nil {
		t.Fatalf("dolt.NewFromConfigWithOptions against running test container: %v", err)
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

	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
	if err != nil {
		t.Fatalf("dolt.NewFromConfigWithOptions against running test container: %v", err)
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

	requireFixDoltContainer(t)
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
	// No connect probe here: the database must not exist yet (creating it is
	// the behavior under test), and the port guard above already covers the
	// no-server case. A probe via dolt.NewFromConfig would always fail on
	// the missing database and silently skip the test (bd-nxt5e).

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

// TestDatabaseVersionWithBdVersion_LeavesAServerDatabaseItFindsAsIs runs the
// Database repair in a server-mode workspace with no .beads/dolt, as in every
// fresh clone and on every other machine, whose database is on the server
// with rows newer than the tracked issues.jsonl. The repair took the missing
// directory for a missing database, and imported issues.jsonl over the live
// rows: it rewrote a row to its tracked copy and brought back one deleted
// since. A database the repair's open finds is left as it is: the repair
// records the directory and the version, and imports nothing.
func TestDatabaseVersionWithBdVersion_LeavesAServerDatabaseItFindsAsIs(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	beadsDir := setupTestBeadsDir(t, tmpDir)

	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
	if err != nil {
		t.Fatalf("dolt.NewFromConfigWithOptions against running test container: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "mdp"); err != nil {
		t.Fatal(err)
	}
	live := &types.Issue{ID: "mdp-1", Title: "live title", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2}
	if err := store.CreateIssue(ctx, live, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLocalMetadata(ctx, "bd_version", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	// The tracked snapshot: mdp-1 as it was, and mdp-2, deleted since.
	writeTestJSONL(t, beadsDir, []types.Issue{
		{ID: "mdp-1", Title: "tracked title", Status: "open", IssueType: "task", Priority: 2},
		{ID: "mdp-2", Title: "deleted since", Status: "open", IssueType: "task", Priority: 2},
	})
	dbPath := filepath.Join(beadsDir, "dolt")
	if err := os.RemoveAll(dbPath); err != nil {
		t.Fatal(err)
	}

	if err := DatabaseVersionWithBdVersion(tmpDir, "0.56.1"); err != nil {
		t.Fatalf("DatabaseVersionWithBdVersion failed: %v", err)
	}

	store, err = dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	stored, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	var titles []string
	for _, issue := range stored {
		titles = append(titles, issue.ID+": "+issue.Title)
	}
	if len(stored) != 1 || stored[0].Title != "live title" {
		t.Errorf("the Database repair imported issues.jsonl over the server's database: issues %q, want only \"mdp-1: live title\"", titles)
	}
	if v, err := store.GetLocalMetadata(ctx, "bd_version"); err != nil || v != "0.56.1" {
		t.Errorf("bd_version = %q (%v), want the repair to record 0.56.1", v, err)
	}
	if info, err := os.Stat(dbPath); err != nil || !info.IsDir() {
		t.Errorf("the repair did not record the local database directory %s: %v", dbPath, err)
	}
}

// TestDatabaseVersionWithBdVersion_ImportsIntoAnEmptyServerDatabaseItFinds
// runs the Database repair in a server-mode workspace with no .beads/dolt
// whose database is on the server with no issues: one bd created without
// importing issues.jsonl, or one with no bd schema yet, which the repair's
// open sets up. A creating open that fails after CREATE DATABASE leaves the
// second; bd doctor --fix's schema gate withholds the repair from it on the
// server the gate reads, so only a direct call or a repair whose open reaches
// another server meets it. The repair's open finds the database rather than
// creating it, and the repair imported nothing, reported the repair done, and
// left the tracked issues unloaded. A database with no issues has no rows for
// the tracked copy to overwrite, so the repair imports issues.jsonl into it.
func TestDatabaseVersionWithBdVersion_ImportsIntoAnEmptyServerDatabaseItFinds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		create func(t *testing.T, beadsDir string)
	}{
		{name: "bd created the database", create: func(t *testing.T, beadsDir string) {
			store, err := dolt.NewFromConfigWithOptions(context.Background(), beadsDir, &dolt.Config{CreateIfMissing: true})
			if err != nil {
				t.Fatalf("dolt.NewFromConfigWithOptions against running test container: %v", err)
			}
			_ = store.Close()
		}},
		{name: "a creating open failed after CREATE DATABASE", create: func(t *testing.T, beadsDir string) {
			cfg, err := configfile.Load(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: fixTestServerPort(), User: "root"}.String())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			//nolint:gosec // G201: the database name is generated by the test (uniqueDBName)
			if _, err := db.ExecContext(context.Background(), fmt.Sprintf("CREATE DATABASE `%s`", cfg.DoltDatabase)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tmpDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			beadsDir := setupTestBeadsDir(t, tmpDir)
			tc.create(t, beadsDir)
			writeTestJSONL(t, beadsDir, []types.Issue{
				{ID: "mdp-1", Title: "First tracked", Status: "open", IssueType: "task", Priority: 2},
				{ID: "mdp-2", Title: "Second tracked", Status: "closed", IssueType: "bug", Priority: 1},
			})
			dbPath := filepath.Join(beadsDir, "dolt")
			if err := os.RemoveAll(dbPath); err != nil {
				t.Fatal(err)
			}

			if err := DatabaseVersionWithBdVersion(tmpDir, "0.56.1"); err != nil {
				t.Fatalf("DatabaseVersionWithBdVersion failed: %v", err)
			}

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
				t.Errorf("store has %d issues, want the 2 issues.jsonl tracks: the Database repair left the empty database it found unloaded", len(stored))
			}
			if v, err := store.GetLocalMetadata(ctx, "bd_version"); err != nil || v != "0.56.1" {
				t.Errorf("bd_version = %q (%v), want the repair to record 0.56.1", v, err)
			}
			if info, err := os.Stat(dbPath); err != nil || !info.IsDir() {
				t.Errorf("the repair did not record the local database directory %s: %v", dbPath, err)
			}
		})
	}
}

// TestDatabaseVersionWithBdVersion_RetriesAFailedImportIntoAnEmptyServerDatabase
// runs the Database repair twice in a server-mode workspace with no
// .beads/dolt whose database is on the server with no issues and was last
// stamped by another bd version. The first run's import fails: a line of
// issues.jsonl is not JSON. That run had already created .beads/dolt, so the
// run after the line was fixed took the directory for a loaded database: it
// updated the metadata, reported the repair done and left the tracked issues
// unloaded. A failed import leaves the directory missing and the version as
// it was, so bd doctor reports the Database problem again and the next run
// counts the database's issues again and imports them.
func TestDatabaseVersionWithBdVersion_RetriesAFailedImportIntoAnEmptyServerDatabase(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	beadsDir := setupTestBeadsDir(t, tmpDir)

	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
	if err != nil {
		t.Fatalf("dolt.NewFromConfigWithOptions against running test container: %v", err)
	}
	if err := store.SetLocalMetadata(ctx, "bd_version", "0.0.1"); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	dbPath := filepath.Join(beadsDir, "dolt")
	if err := os.RemoveAll(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"mdp-1","title":"First tr`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := DatabaseVersionWithBdVersion(tmpDir, "0.56.1"); err == nil {
		t.Fatal("DatabaseVersionWithBdVersion succeeded with a line of issues.jsonl that is not JSON, want its import error")
	}
	store, err = dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	v, err := store.GetLocalMetadata(ctx, "bd_version")
	_ = store.Close()
	if err != nil || v != "0.0.1" {
		t.Errorf("bd_version = %q (%v) after the failed repair, want 0.0.1 left as it was: with the version recorded, bd doctor reports the Database OK and bd doctor --fix never runs the repair again", v, err)
	}

	writeTestJSONL(t, beadsDir, []types.Issue{
		{ID: "mdp-1", Title: "First tracked", Status: "open", IssueType: "task", Priority: 2},
		{ID: "mdp-2", Title: "Second tracked", Status: "closed", IssueType: "bug", Priority: 1},
	})
	if err := DatabaseVersionWithBdVersion(tmpDir, "0.56.1"); err != nil {
		t.Fatalf("DatabaseVersionWithBdVersion failed on the run after the failed import: %v", err)
	}

	store, err = dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	stored, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(stored) != 2 {
		t.Errorf("store has %d issues, want the 2 issues.jsonl tracks: the run after a failed import did not import", len(stored))
	}
	if v, err := store.GetLocalMetadata(ctx, "bd_version"); err != nil || v != "0.56.1" {
		t.Errorf("bd_version = %q (%v), want the repair to record 0.56.1", v, err)
	}
	if info, err := os.Stat(dbPath); err != nil || !info.IsDir() {
		t.Errorf("the repair did not record the local database directory %s: %v", dbPath, err)
	}
}
