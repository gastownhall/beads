package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupSyncBranchWorktreeRepo(t *testing.T) (repoPath, worktreePath string) {
	t.Helper()

	repoPath = mkTmpDirInTmp(t, "bd-doctor-sync-worktree-*")
	initRepo(t, repoPath, "main")
	commitFile(t, repoPath, "README.md", "# test\n", "initial commit")
	commitFile(t, repoPath, ".gitignore", ".beads/beads.db\n.beads/beads.db-wal\n.beads/beads.db-shm\n", "ignore sqlite db")
	commitFile(t, repoPath, ".beads/config.yaml", "sync-branch: beads-sync\n", "configure sync branch")
	commitFile(t, repoPath, ".beads/issues.jsonl", `{"id":"bd-1","title":"Issue 1","status":"open"}`+"\n", "add main jsonl")

	runGit(t, repoPath, "branch", "beads-sync")

	worktreePath = filepath.Join(repoPath, ".git", "beads-worktrees", "beads-sync")
	runGit(t, repoPath, "worktree", "add", worktreePath, "beads-sync")

	return repoPath, worktreePath
}

func writeJSONLIssues(t *testing.T, jsonlPath string, count int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(jsonlPath), err)
	}

	var b strings.Builder
	for i := 1; i <= count; i++ {
		b.WriteString(fmt.Sprintf(`{"id":"bd-%d","title":"Issue %d","status":"open"}`, i, i))
		b.WriteString("\n")
	}
	if err := os.WriteFile(jsonlPath, []byte(b.String()), 0644); err != nil {
		t.Fatalf("write %s: %v", jsonlPath, err)
	}
}

func createSyncDoctorTestDB(t *testing.T, dbPath string, issueCount int, lastImportTime *time.Time) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(dbPath), err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE issues (id TEXT PRIMARY KEY, status TEXT, ephemeral INTEGER)"); err != nil {
		t.Fatalf("create issues table: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatalf("create config table: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatalf("create metadata table: %v", err)
	}

	for i := 1; i <= issueCount; i++ {
		if _, err := db.Exec("INSERT INTO issues (id, status, ephemeral) VALUES (?, ?, ?)", fmt.Sprintf("bd-%d", i), "open", 0); err != nil {
			t.Fatalf("insert issue %d: %v", i, err)
		}
	}

	if lastImportTime != nil {
		if _, err := db.Exec("INSERT INTO metadata (key, value) VALUES (?, ?)", "last_import_time", lastImportTime.Format(time.RFC3339)); err != nil {
			t.Fatalf("insert metadata: %v", err)
		}
	}
}

func TestCheckDatabaseJSONLSync_UsesSyncWorktreeJSONL(t *testing.T) {
	repoPath, worktreePath := setupSyncBranchWorktreeRepo(t)

	mainJSONL := filepath.Join(repoPath, ".beads", "issues.jsonl")
	worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
	dbPath := filepath.Join(repoPath, ".beads", "beads.db")

	// Main JSONL is stale, sync worktree JSONL has current state.
	writeJSONLIssues(t, mainJSONL, 1)
	writeJSONLIssues(t, worktreeJSONL, 3)
	createSyncDoctorTestDB(t, dbPath, 3, nil)

	check := CheckDatabaseJSONLSync(repoPath)
	if check.Status != StatusOK {
		t.Fatalf("status=%q want %q (msg=%q detail=%q)", check.Status, StatusOK, check.Message, check.Detail)
	}
}

func TestCheckSyncDivergence_UsesSyncWorktreeJSONLForMtime(t *testing.T) {
	repoPath, worktreePath := setupSyncBranchWorktreeRepo(t)

	mainJSONL := filepath.Join(repoPath, ".beads", "issues.jsonl")
	worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
	dbPath := filepath.Join(repoPath, ".beads", "beads.db")

	writeJSONLIssues(t, mainJSONL, 1)
	writeJSONLIssues(t, worktreeJSONL, 1)

	worktreeTime := time.Now().Add(-1 * time.Minute).Round(time.Second)
	mainTime := worktreeTime.Add(-10 * time.Minute)
	if err := os.Chtimes(worktreeJSONL, worktreeTime, worktreeTime); err != nil {
		t.Fatalf("chtimes worktree jsonl: %v", err)
	}
	if err := os.Chtimes(mainJSONL, mainTime, mainTime); err != nil {
		t.Fatalf("chtimes main jsonl: %v", err)
	}

	createSyncDoctorTestDB(t, dbPath, 1, &worktreeTime)

	check := CheckSyncDivergence(repoPath)
	if check.Status != StatusOK {
		t.Fatalf("status=%q want %q (msg=%q detail=%q)", check.Status, StatusOK, check.Message, check.Detail)
	}
}
