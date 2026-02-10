package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

func TestExtractIssuePrefix(t *testing.T) {
	tests := []struct {
		name     string
		issueID  string
		expected string
	}{
		{"standard ID", "bd-123", "bd"},
		{"custom prefix", "myproject-456", "myproject"},
		{"hash ID", "bd-abc123def", "bd"},
		{"multi-part prefix with numeric suffix", "alpha-beta-1", "alpha-beta"},
		{"multi-part non-numeric suffix", "vc-baseline-test", "vc"}, // Falls back to first hyphen
		{"beads-vscode style", "beads-vscode-42", "beads-vscode"},
		{"no hyphen", "nohyphen", ""},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIssuePrefix(tt.issueID)
			if got != tt.expected {
				t.Errorf("extractIssuePrefix(%q) = %q, want %q", tt.issueID, got, tt.expected)
			}
		})
	}
}

func TestLoadIssuesFromJSONL(t *testing.T) {
	tempDir := t.TempDir()
	jsonlPath := filepath.Join(tempDir, "test.jsonl")

	// Create test JSONL file
	content := `{"id":"bd-1","title":"Test Issue 1","description":"Test"}
{"id":"bd-2","title":"Test Issue 2","description":"Another test"}

{"id":"bd-3","title":"Test Issue 3","description":"Third test"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	issues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("loadIssuesFromJSONL failed: %v", err)
	}

	if len(issues) != 3 {
		t.Errorf("Expected 3 issues, got %d", len(issues))
	}

	if issues[0].ID != "bd-1" || issues[0].Title != "Test Issue 1" {
		t.Errorf("First issue mismatch: %+v", issues[0])
	}
	if issues[1].ID != "bd-2" {
		t.Errorf("Second issue ID mismatch: %s", issues[1].ID)
	}
	if issues[2].ID != "bd-3" {
		t.Errorf("Third issue ID mismatch: %s", issues[2].ID)
	}
}

func TestLoadIssuesFromJSONL_InvalidJSON(t *testing.T) {
	tempDir := t.TempDir()
	jsonlPath := filepath.Join(tempDir, "invalid.jsonl")

	content := `{"id":"bd-1","title":"Valid"}
invalid json here
{"id":"bd-2","title":"Another valid"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err := loadIssuesFromJSONL(jsonlPath)
	if err == nil {
		t.Error("Expected error for invalid JSON, got nil")
	}
}

func TestLoadIssuesFromJSONL_NonExistent(t *testing.T) {
	_, err := loadIssuesFromJSONL("/nonexistent/file.jsonl")
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
}

func TestDetectPrefix(t *testing.T) {
	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	t.Run("from existing issues", func(t *testing.T) {
		memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))

		// Add issues with common prefix
		issues := []*types.Issue{
			{ID: "myapp-1", Title: "Issue 1"},
			{ID: "myapp-2", Title: "Issue 2"},
		}
		if err := memStore.LoadFromIssues(issues); err != nil {
			t.Fatalf("Failed to load issues: %v", err)
		}

		prefix, err := detectPrefix(beadsDir, memStore)
		if err != nil {
			t.Fatalf("detectPrefix failed: %v", err)
		}
		if prefix != "myapp" {
			t.Errorf("Expected prefix 'myapp', got '%s'", prefix)
		}
	})

	t.Run("mixed prefixes error", func(t *testing.T) {
		memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))

		issues := []*types.Issue{
			{ID: "app1-1", Title: "Issue 1"},
			{ID: "app2-2", Title: "Issue 2"},
		}
		if err := memStore.LoadFromIssues(issues); err != nil {
			t.Fatalf("Failed to load issues: %v", err)
		}

		_, err := detectPrefix(beadsDir, memStore)
		if err == nil {
			t.Error("Expected error for mixed prefixes, got nil")
		}
	})

	t.Run("empty database defaults to dir name", func(t *testing.T) {
		// Change to temp dir so we can control directory name
		namedDir := filepath.Join(tempDir, "myproject")
		if err := os.MkdirAll(namedDir, 0o755); err != nil {
			t.Fatalf("Failed to create named dir: %v", err)
		}
		t.Chdir(namedDir)

		memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))
		prefix, err := detectPrefix(beadsDir, memStore)
		if err != nil {
			t.Fatalf("detectPrefix failed: %v", err)
		}
		if prefix != "myproject" {
			t.Errorf("Expected prefix 'myproject', got '%s'", prefix)
		}
	})

	t.Run("config override", func(t *testing.T) {
		memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))
		prev := config.GetString("issue-prefix")
		config.Set("issue-prefix", "custom-prefix")
		t.Cleanup(func() { config.Set("issue-prefix", prev) })

		prefix, err := detectPrefix(beadsDir, memStore)
		if err != nil {
			t.Fatalf("detectPrefix failed: %v", err)
		}
		if prefix != "custom-prefix" {
			t.Errorf("Expected config override prefix, got %q", prefix)
		}
	})

	t.Run("sanitizes directory names", func(t *testing.T) {
		memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))
		weirdDir := filepath.Join(tempDir, "My Project!!!")
		if err := os.MkdirAll(weirdDir, 0o755); err != nil {
			t.Fatalf("Failed to create dir: %v", err)
		}
		t.Chdir(weirdDir)
		prev := config.GetString("issue-prefix")
		config.Set("issue-prefix", "")
		t.Cleanup(func() { config.Set("issue-prefix", prev) })

		prefix, err := detectPrefix(beadsDir, memStore)
		if err != nil {
			t.Fatalf("detectPrefix failed: %v", err)
		}
		if prefix != "myproject" {
			t.Errorf("Expected sanitized prefix 'myproject', got %q", prefix)
		}
	})

	t.Run("invalid directory falls back to bd", func(t *testing.T) {
		memStore := memory.New(filepath.Join(beadsDir, "issues.jsonl"))
		emptyDir := filepath.Join(tempDir, "!!!")
		if err := os.MkdirAll(emptyDir, 0o755); err != nil {
			t.Fatalf("Failed to create dir: %v", err)
		}
		t.Chdir(emptyDir)
		prev := config.GetString("issue-prefix")
		config.Set("issue-prefix", "")
		t.Cleanup(func() { config.Set("issue-prefix", prev) })

		prefix, err := detectPrefix(beadsDir, memStore)
		if err != nil {
			t.Fatalf("detectPrefix failed: %v", err)
		}
		if prefix != "bd" {
			t.Errorf("Expected fallback prefix 'bd', got %q", prefix)
		}
	})
}

func TestInitializeNoDbMode_SetsStoreActive(t *testing.T) {
	// This test verifies the fix for bd comment --no-db not working.
	// The bug was that initializeNoDbMode() set `store` but not `storeActive`,
	// so ensureStoreActive() would try to find a SQLite database.

	// Reset global state for test isolation
	ensureCleanGlobalState(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create a minimal JSONL file with one issue
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	content := `{"id":"bd-1","title":"Test Issue","status":"open"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Save and restore global state
	oldStore := store
	oldStoreActive := storeActive
	oldCwd, _ := os.Getwd()
	defer func() {
		storeMutex.Lock()
		store = oldStore
		storeActive = oldStoreActive
		storeMutex.Unlock()
		_ = os.Chdir(oldCwd)
	}()

	// Change to temp dir so initializeNoDbMode finds .beads
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}

	// Reset global state
	storeMutex.Lock()
	store = nil
	storeActive = false
	storeMutex.Unlock()

	// Initialize no-db mode
	if err := initializeNoDbMode(); err != nil {
		t.Fatalf("initializeNoDbMode failed: %v", err)
	}

	// Verify storeActive is now true
	storeMutex.Lock()
	active := storeActive
	s := store
	storeMutex.Unlock()

	if !active {
		t.Error("storeActive should be true after initializeNoDbMode")
	}
	if s == nil {
		t.Fatal("store should not be nil after initializeNoDbMode")
	}

	// ensureStoreActive should now return immediately without error
	if err := ensureStoreActive(); err != nil {
		t.Errorf("ensureStoreActive should succeed after initializeNoDbMode: %v", err)
	}

	// Verify comments work (this was the failing case)
	ctx := rootCtx
	comment, err := s.AddIssueComment(ctx, "bd-1", "testuser", "Test comment")
	if err != nil {
		t.Fatalf("AddIssueComment failed: %v", err)
	}
	if comment.Text != "Test comment" {
		t.Errorf("Expected 'Test comment', got %s", comment.Text)
	}

	comments, err := s.GetIssueComments(ctx, "bd-1")
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}
	if len(comments) != 1 {
		t.Errorf("Expected 1 comment, got %d", len(comments))
	}
}

func TestInitializeNoDbMode_SetsCmdCtxStoreActive(t *testing.T) {
	// GH#897: Verify that initializeNoDbMode sets cmdCtx.StoreActive, not just the global.
	// This is critical for commands like `comments add` that call ensureStoreActive().
	ensureCleanGlobalState(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create a minimal JSONL file with one issue
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	content := `{"id":"mmm-155","title":"Test Issue","status":"open"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Initialize CommandContext (simulates what PersistentPreRun does)
	initCommandContext()

	oldCwd, _ := os.Getwd()
	defer func() {
		_ = os.Chdir(oldCwd)
		resetCommandContext()
	}()

	// Change to temp dir so initializeNoDbMode finds .beads
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}

	// Initialize no-db mode
	if err := initializeNoDbMode(); err != nil {
		t.Fatalf("initializeNoDbMode failed: %v", err)
	}

	// Verify cmdCtx.StoreActive is true (this was the bug - it was only setting globals)
	ctx := GetCommandContext()
	if ctx == nil {
		t.Fatal("cmdCtx should not be nil after initCommandContext")
	}
	if !ctx.StoreActive {
		t.Error("cmdCtx.StoreActive should be true after initializeNoDbMode (GH#897)")
	}
	if ctx.Store == nil {
		t.Error("cmdCtx.Store should not be nil after initializeNoDbMode")
	}

	// ensureStoreActive should succeed
	if err := ensureStoreActive(); err != nil {
		t.Errorf("ensureStoreActive should succeed after initializeNoDbMode: %v", err)
	}

	// Comments should work
	comment, err := ctx.Store.AddIssueComment(rootCtx, "mmm-155", "testuser", "Test comment")
	if err != nil {
		t.Fatalf("AddIssueComment failed: %v", err)
	}
	if comment.Text != "Test comment" {
		t.Errorf("Expected 'Test comment', got %s", comment.Text)
	}
}

