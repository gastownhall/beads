package doctor

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

func TestCheckSyncDivergence(t *testing.T) {
	t.Run("not a git repo", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-sync-div-*")
		check := CheckSyncDivergence(dir)
		if check.Status != StatusOK {
			t.Errorf("status=%q want %q", check.Status, StatusOK)
		}
		if !strings.Contains(check.Message, "N/A") {
			t.Errorf("message=%q want N/A", check.Message)
		}
	})

	t.Run("no beads directory", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-sync-div-nb-*")
		// Don't use initRepo which creates .beads
		runGit(t, dir, "init", "-b", "main")
		runGit(t, dir, "config", "user.email", "test@test.com")
		runGit(t, dir, "config", "user.name", "Test User")
		commitFile(t, dir, "README.md", "# test\n", "initial")

		check := CheckSyncDivergence(dir)
		if check.Status != StatusOK {
			t.Errorf("status=%q want %q", check.Status, StatusOK)
		}
		if !strings.Contains(check.Message, "N/A") {
			t.Errorf("message=%q want N/A", check.Message)
		}
	})

	t.Run("all synced", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-sync-div-ok-*")
		initRepo(t, dir, "main")

		// Create .beads with JSONL
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create and commit JSONL
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		jsonlContent := `{"id":"test-1","title":"Test issue","status":"open"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatal(err)
		}
		commitFile(t, dir, ".beads/issues.jsonl", jsonlContent, "add issues")

		check := CheckSyncDivergence(dir)
		if check.Status != StatusOK {
			t.Errorf("status=%q want %q (msg=%q detail=%q)", check.Status, StatusOK, check.Message, check.Detail)
		}
	})

	t.Run("uncommitted beads changes", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-sync-div-unc-*")
		initRepo(t, dir, "main")

		// Create .beads with JSONL and commit it
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		jsonlContent := `{"id":"test-1","title":"Test issue","status":"open"}` + "\n"
		commitFile(t, dir, ".beads/issues.jsonl", jsonlContent, "add issues")

		// Now modify the file without committing
		// This triggers both jsonl_git_mismatch AND uncommitted_beads
		newContent := jsonlContent + `{"id":"test-2","title":"Another issue","status":"open"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(newContent), 0644); err != nil {
			t.Fatal(err)
		}

		check := CheckSyncDivergence(dir)
		// Multiple divergence issues = error status
		if check.Status != StatusError {
			t.Errorf("status=%q want %q (msg=%q)", check.Status, StatusError, check.Message)
		}
		if !strings.Contains(check.Detail, "Uncommitted") {
			t.Errorf("detail=%q want to mention uncommitted", check.Detail)
		}
	})

	t.Run("JSONL differs from git HEAD", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-sync-div-diff-*")
		initRepo(t, dir, "main")

		// Create .beads with JSONL and commit it
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		jsonlContent := `{"id":"test-1","title":"Test issue","status":"open"}` + "\n"
		commitFile(t, dir, ".beads/issues.jsonl", jsonlContent, "add issues")

		// Modify without committing
		newContent := `{"id":"test-1","title":"Test issue","status":"closed"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(newContent), 0644); err != nil {
			t.Fatal(err)
		}

		check := CheckSyncDivergence(dir)
		if check.Status != StatusWarning && check.Status != StatusError {
			t.Errorf("status=%q want warning or error (msg=%q)", check.Status, check.Message)
		}
		// Should detect either JSONL differs or uncommitted changes
		if !strings.Contains(check.Detail, "JSONL") && !strings.Contains(check.Detail, "Uncommitted") {
			t.Errorf("detail=%q want to mention JSONL or uncommitted", check.Detail)
		}
	})

	t.Run("JSONL differs in dolt-native mode suggests restore", func(t *testing.T) {
		// Reset and initialize config for this test
		config.ResetForTesting()
		if err := config.Initialize(); err != nil {
			t.Fatalf("Initialize failed: %v", err)
		}
		// Set sync mode to dolt-native for this test
		config.Set("sync.mode", string(config.SyncModeDoltNative))
		defer func() {
			// Reset to clean state after test
			config.ResetForTesting()
			_ = config.Initialize()
		}()

		dir := mkTmpDirInTmp(t, "bd-sync-div-dolt-*")
		initRepo(t, dir, "main")

		// Create .beads with JSONL and commit it
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		jsonlContent := `{"id":"test-1","title":"Test issue","status":"open"}` + "\n"
		commitFile(t, dir, ".beads/issues.jsonl", jsonlContent, "add issues")

		// Modify without committing
		newContent := `{"id":"test-1","title":"Test issue","status":"closed"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(newContent), 0644); err != nil {
			t.Fatal(err)
		}

		check := CheckSyncDivergence(dir)
		if check.Status != StatusWarning && check.Status != StatusError {
			t.Errorf("status=%q want warning or error (msg=%q)", check.Status, check.Message)
		}
		// In dolt-native mode, should suggest git restore, not git add
		if !strings.Contains(check.Fix, "git restore") {
			t.Errorf("fix=%q want git restore for dolt-native mode", check.Fix)
		}
		if strings.Contains(check.Fix, "git add") {
			t.Errorf("fix=%q should NOT suggest git add in dolt-native mode", check.Fix)
		}
	})
}

func TestCheckSQLiteMtimeDivergence(t *testing.T) {
	t.Run("no database", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-mtime-nodb-*")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		issue := checkSQLiteMtimeDivergence(dir, beadsDir)
		if issue != nil {
			t.Errorf("expected nil issue for no database, got %+v", issue)
		}
	})

	t.Run("no JSONL", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-mtime-nojsonl-*")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a dummy database
		dbPath := filepath.Join(beadsDir, "beads.db")
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = db.Exec("CREATE TABLE issues (id TEXT)")
		_, _ = db.Exec("CREATE TABLE metadata (key TEXT, value TEXT)")
		db.Close()

		issue := checkSQLiteMtimeDivergence(dir, beadsDir)
		if issue != nil {
			t.Errorf("expected nil issue for no JSONL, got %+v", issue)
		}
	})

	t.Run("no last_import_time", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-mtime-noimport-*")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create database without last_import_time
		dbPath := filepath.Join(beadsDir, "beads.db")
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = db.Exec("CREATE TABLE issues (id TEXT)")
		_, _ = db.Exec("CREATE TABLE metadata (key TEXT, value TEXT)")
		db.Close()

		// Create JSONL
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		issue := checkSQLiteMtimeDivergence(dir, beadsDir)
		if issue == nil {
			t.Error("expected issue for missing last_import_time")
		} else if issue.Type != "sqlite_mtime_stale" {
			t.Errorf("type=%q want sqlite_mtime_stale", issue.Type)
		}
	})

	t.Run("times match", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-mtime-match-*")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create JSONL first
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Get JSONL mtime
		jsonlInfo, _ := os.Stat(jsonlPath)
		importTime := jsonlInfo.ModTime()

		// Create database with matching last_import_time
		dbPath := filepath.Join(beadsDir, "beads.db")
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = db.Exec("CREATE TABLE issues (id TEXT)")
		_, _ = db.Exec("CREATE TABLE metadata (key TEXT, value TEXT)")
		_, _ = db.Exec("INSERT INTO metadata (key, value) VALUES (?, ?)",
			"last_import_time", importTime.Format(time.RFC3339))
		db.Close()

		issue := checkSQLiteMtimeDivergence(dir, beadsDir)
		if issue != nil {
			t.Errorf("expected nil issue for matching times, got %+v", issue)
		}
	})

	t.Run("JSONL newer than import", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-mtime-newer-*")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create database with old last_import_time
		dbPath := filepath.Join(beadsDir, "beads.db")
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = db.Exec("CREATE TABLE issues (id TEXT)")
		_, _ = db.Exec("CREATE TABLE metadata (key TEXT, value TEXT)")
		oldTime := time.Now().Add(-1 * time.Hour)
		_, _ = db.Exec("INSERT INTO metadata (key, value) VALUES (?, ?)",
			"last_import_time", oldTime.Format(time.RFC3339))
		db.Close()

		// Create JSONL (will have current mtime)
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		issue := checkSQLiteMtimeDivergence(dir, beadsDir)
		if issue == nil {
			t.Error("expected issue for JSONL newer than import")
		} else {
			if issue.Type != "sqlite_mtime_stale" {
				t.Errorf("type=%q want sqlite_mtime_stale", issue.Type)
			}
			if !strings.Contains(issue.FixCommand, "import") {
				t.Errorf("fix=%q want import command", issue.FixCommand)
			}
		}
	})

	// Regression test: verify we read from metadata table, not config table.
	// The sync code writes to metadata, so doctor must read from there.
	// This catches the bug where doctor queried 'config' instead of 'metadata'.
	t.Run("reads from metadata table not config", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-mtime-table-*")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create JSONL first
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Get JSONL mtime
		jsonlInfo, _ := os.Stat(jsonlPath)
		importTime := jsonlInfo.ModTime()

		// Create database with BOTH config and metadata tables (realistic schema)
		// Put last_import_time ONLY in metadata (as real sync code does)
		dbPath := filepath.Join(beadsDir, "beads.db")
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = db.Exec("CREATE TABLE issues (id TEXT)")
		_, _ = db.Exec("CREATE TABLE config (key TEXT, value TEXT)")
		_, _ = db.Exec("CREATE TABLE metadata (key TEXT, value TEXT)")
		// Only insert into metadata, NOT config
		_, _ = db.Exec("INSERT INTO metadata (key, value) VALUES (?, ?)",
			"last_import_time", importTime.Format(time.RFC3339))
		db.Close()

		issue := checkSQLiteMtimeDivergence(dir, beadsDir)
		if issue != nil {
			t.Errorf("expected nil issue when last_import_time is in metadata table, got %+v", issue)
		}
	})
}

func TestCheckUncommittedBeadsChanges(t *testing.T) {
	t.Run("no uncommitted changes", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-uncommit-clean-*")
		initRepo(t, dir, "main")

		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		jsonlContent := `{"id":"test-1"}` + "\n"
		commitFile(t, dir, ".beads/issues.jsonl", jsonlContent, "add issues")

		issue := checkUncommittedBeadsChanges(dir, beadsDir)
		if issue != nil {
			t.Errorf("expected nil issue for clean state, got %+v", issue)
		}
	})

	t.Run("uncommitted changes present", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-uncommit-dirty-*")
		initRepo(t, dir, "main")

		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		jsonlContent := `{"id":"test-1"}` + "\n"
		commitFile(t, dir, ".beads/issues.jsonl", jsonlContent, "add issues")

		// Modify without committing
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		newContent := jsonlContent + `{"id":"test-2"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(newContent), 0644); err != nil {
			t.Fatal(err)
		}

		issue := checkUncommittedBeadsChanges(dir, beadsDir)
		if issue == nil {
			t.Error("expected issue for uncommitted changes")
		} else {
			if issue.Type != "uncommitted_beads" {
				t.Errorf("type=%q want uncommitted_beads", issue.Type)
			}
			if !strings.Contains(issue.Description, "Uncommitted") {
				t.Errorf("description=%q want Uncommitted", issue.Description)
			}
		}
	})
}

func TestFindJSONLFile(t *testing.T) {
	t.Run("issues.jsonl", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-findjsonl-*")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		found := findJSONLFile(beadsDir)
		if found != jsonlPath {
			t.Errorf("found=%q want %q", found, jsonlPath)
		}
	})

	t.Run("beads.jsonl", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-findjsonl2-*")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		found := findJSONLFile(beadsDir)
		if found != jsonlPath {
			t.Errorf("found=%q want %q", found, jsonlPath)
		}
	})

	t.Run("no jsonl", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-findjsonl3-*")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		found := findJSONLFile(beadsDir)
		if found != "" {
			t.Errorf("found=%q want empty", found)
		}
	})
}
