package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestTouchDatabaseFile_UsesJSONLMtime(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "beads.db")
	jsonlPath := filepath.Join(tmp, "issues.jsonl")

	if err := os.WriteFile(dbPath, []byte(""), 0o600); err != nil {
		t.Fatalf("WriteFile db: %v", err)
	}
	if err := os.WriteFile(jsonlPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile jsonl: %v", err)
	}

	jsonlTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(jsonlPath, jsonlTime, jsonlTime); err != nil {
		t.Fatalf("Chtimes jsonl: %v", err)
	}

	if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
		t.Fatalf("TouchDatabaseFile: %v", err)
	}

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat db: %v", err)
	}
	if info.ModTime().Before(jsonlTime) {
		t.Fatalf("db mtime %v should be >= jsonl mtime %v", info.ModTime(), jsonlTime)
	}
}

func TestImportDetectPrefixFromIssues(t *testing.T) {
	if detectPrefixFromIssues(nil) != "" {
		t.Fatalf("expected empty")
	}

	issues := []*types.Issue{
		{ID: "test-1"},
		{ID: "test-2"},
		{ID: "other-1"},
	}
	if got := detectPrefixFromIssues(issues); got != "test" {
		t.Fatalf("got %q, want %q", got, "test")
	}
}

func TestCountLines(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "f.txt")
	if err := os.WriteFile(p, []byte("a\n\nb\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := countLines(p); got != 3 {
		t.Fatalf("countLines=%d, want 3", got)
	}
}

func TestCheckUncommittedChanges_Warns(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	if err := os.WriteFile("issues.jsonl", []byte("{\"id\":\"test-1\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_ = execCmd(t, "git", "add", "issues.jsonl")
	_ = execCmd(t, "git", "commit", "-m", "add issues")

	// Modify without committing.
	if err := os.WriteFile("issues.jsonl", []byte("{\"id\":\"test-1\"}\n{\"id\":\"test-2\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	warn := captureStderr(t, func() {
		checkUncommittedChanges("issues.jsonl", &ImportResult{})
	})
	if !strings.Contains(warn, "uncommitted changes") {
		t.Fatalf("expected warning, got: %q", warn)
	}

	noWarn := captureStderr(t, func() {
		checkUncommittedChanges("issues.jsonl", &ImportResult{Created: 1})
	})
	if noWarn != "" {
		t.Fatalf("expected no warning, got: %q", noWarn)
	}
}

func execCmd(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
	return string(out)
}
