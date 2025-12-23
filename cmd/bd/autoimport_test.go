package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestCheckAndAutoImport_NoAutoImportFlag(t *testing.T) {
	ctx := context.Background()
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(context.Background(), tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set the global flag
	oldNoAutoImport := noAutoImport
	noAutoImport = true
	defer func() { noAutoImport = oldNoAutoImport }()

	result := checkAndAutoImport(ctx, store)
	if result {
		t.Error("Expected auto-import to be disabled when noAutoImport is true")
	}
}

func TestAutoImportIfNewer_NoAutoImportFlag(t *testing.T) {
	// Test that autoImportIfNewer() respects noAutoImport flag directly (bd-4t7 fix)
	ctx := context.Background()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "bd.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create database
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Set prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create JSONL with an issue that should NOT be imported
	jsonlIssue := &types.Issue{
		ID:        "test-noimport-bd4t7",
		Title:     "Should Not Import",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(jsonlIssue); err != nil {
		t.Fatalf("Failed to encode issue: %v", err)
	}
	f.Close()

	// Save and set global state
	oldNoAutoImport := noAutoImport
	oldAutoImportEnabled := autoImportEnabled
	oldStore := store
	oldDbPath := dbPath
	oldRootCtx := rootCtx
	oldStoreActive := storeActive

	noAutoImport = true
	autoImportEnabled = false // Also set this for consistency
	store = testStore
	dbPath = testDBPath
	rootCtx = ctx
	storeActive = true

	defer func() {
		noAutoImport = oldNoAutoImport
		autoImportEnabled = oldAutoImportEnabled
		store = oldStore
		dbPath = oldDbPath
		rootCtx = oldRootCtx
		storeActive = oldStoreActive
	}()

	// Call autoImportIfNewer directly - should be blocked by noAutoImport check
	autoImportIfNewer()

	// Verify issue was NOT imported
	imported, err := testStore.GetIssue(ctx, "test-noimport-bd4t7")
	if err != nil {
		t.Fatalf("Failed to check for issue: %v", err)
	}
	if imported != nil {
		t.Error("autoImportIfNewer() imported despite noAutoImport=true - bd-4t7 fix failed")
	}
}

func TestCheckAndAutoImport_DatabaseHasIssues(t *testing.T) {
	ctx := context.Background()
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(context.Background(), tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create an issue
	issue := &types.Issue{
		ID:          "test-123",
		Title:       "Test",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	oldNoAutoImport := noAutoImport
	noAutoImport = false
	defer func() { noAutoImport = oldNoAutoImport }()

	result := checkAndAutoImport(ctx, store)
	if result {
		t.Error("Expected auto-import to skip when database has issues")
	}
}

func TestCheckAndAutoImport_EmptyDatabaseNoGit(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	tmpDB := filepath.Join(tmpDir, "test.db")
	store, err := sqlite.New(context.Background(), tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	oldNoAutoImport := noAutoImport
	oldJsonOutput := jsonOutput
	noAutoImport = false
	jsonOutput = true // Suppress output
	defer func() { 
		noAutoImport = oldNoAutoImport 
		jsonOutput = oldJsonOutput
	}()

	// Change to temp dir (no git repo)
	t.Chdir(tmpDir)

	result := checkAndAutoImport(ctx, store)
	if result {
		t.Error("Expected auto-import to skip when no git repo")
	}
}

func TestFindBeadsDir(t *testing.T) {
	// Create temp directory with .beads and a valid project file
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	// Create a config.yaml so beads.FindBeadsDir() recognizes this as a valid project
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: test\n"), 0600); err != nil {
		t.Fatalf("Failed to create config.yaml: %v", err)
	}

	// Change to tmpDir
	t.Chdir(tmpDir)

	found := beads.FindBeadsDir()
	if found == "" {
		t.Error("Expected to find .beads directory")
	}
	// Use EvalSymlinks to handle /var vs /private/var on macOS
	expectedPath, _ := filepath.EvalSymlinks(beadsDir)
	foundPath, _ := filepath.EvalSymlinks(found)
	if foundPath != expectedPath {
		t.Errorf("Expected %s, got %s", expectedPath, foundPath)
	}
}

func TestFindBeadsDir_NotFound(t *testing.T) {
	// Create temp directory without .beads
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)

	found := beads.FindBeadsDir()
	// findBeadsDir walks up to root, so it might find .beads in parent dirs
	// (e.g., user's home directory). Just verify it's not in tmpDir itself.
	if found != "" && filepath.Dir(found) == tmpDir {
		t.Errorf("Expected not to find .beads in tmpDir, but got %s", found)
	}
}

func TestFindBeadsDir_ParentDirectory(t *testing.T) {
	// Create structure: tmpDir/.beads and tmpDir/subdir
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	// Create a config.yaml so beads.FindBeadsDir() recognizes this as a valid project
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: test\n"), 0600); err != nil {
		t.Fatalf("Failed to create config.yaml: %v", err)
	}

	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	// Change to subdir
	t.Chdir(subDir)

	found := beads.FindBeadsDir()
	if found == "" {
		t.Error("Expected to find .beads directory in parent")
	}
	// Use EvalSymlinks to handle /var vs /private/var on macOS
	expectedPath, _ := filepath.EvalSymlinks(beadsDir)
	foundPath, _ := filepath.EvalSymlinks(found)
	if foundPath != expectedPath {
		t.Errorf("Expected %s, got %s", expectedPath, foundPath)
	}
}

func TestCheckGitForIssues_NoGitRepo(t *testing.T) {
	// Change to temp dir (not a git repo)
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	count, path, gitRef := checkGitForIssues()
	if count != 0 {
		t.Errorf("Expected 0 issues, got %d", count)
	}
	if path != "" {
		t.Errorf("Expected empty path, got %s", path)
	}
	if gitRef != "" {
		t.Errorf("Expected empty gitRef, got %s", gitRef)
	}
}

func TestCheckGitForIssues_NoBeadsDir(t *testing.T) {
	// Use current directory which has git but change to somewhere without .beads
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	count, path, _ := checkGitForIssues()
	if count != 0 || path != "" {
		t.Logf("No .beads dir: count=%d, path=%s (expected 0, empty)", count, path)
	}
}

func TestBoolToFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		condition bool
		flag      string
		want      string
	}{
		{"true condition", true, "--verbose", "--verbose"},
		{"false condition", false, "--verbose", ""},
		{"true with empty flag", true, "", ""},
		{"false with flag", false, "--debug", ""},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := boolToFlag(tt.condition, tt.flag)
			if got != tt.want {
				t.Errorf("boolToFlag(%v, %q) = %q, want %q", tt.condition, tt.flag, got, tt.want)
			}
		})
	}
}

func TestIsNoDbModeConfigured(t *testing.T) {
	tests := []struct {
		name       string
		configYAML string
		createFile bool
		want       bool
	}{
		{
			name:       "no config.yaml exists",
			createFile: false,
			want:       false,
		},
		{
			name:       "config.yaml without no-db key",
			configYAML: "issue-prefix: test\nauthor: testuser\n",
			createFile: true,
			want:       false,
		},
		{
			name:       "no-db: true",
			configYAML: "no-db: true\n",
			createFile: true,
			want:       true,
		},
		{
			name:       "no-db: false",
			configYAML: "no-db: false\n",
			createFile: true,
			want:       false,
		},
		{
			name:       "no-db in comment should not match",
			configYAML: "# no-db: true\nissue-prefix: test\n",
			createFile: true,
			want:       false,
		},
		{
			name:       "no-db nested under section should not match",
			configYAML: "settings:\n  no-db: true\n",
			createFile: true,
			want:       false,
		},
		{
			name:       "no-db with other config",
			configYAML: "issue-prefix: bd\nno-db: true\nauthor: steve\n",
			createFile: true,
			want:       true,
		},
		{
			name:       "empty file",
			configYAML: "",
			createFile: true,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatalf("Failed to create beads dir: %v", err)
			}

			if tt.createFile {
				configPath := filepath.Join(beadsDir, "config.yaml")
				if err := os.WriteFile(configPath, []byte(tt.configYAML), 0644); err != nil {
					t.Fatalf("Failed to write config.yaml: %v", err)
				}
			}

			got := isNoDbModeConfigured(beadsDir)
			if got != tt.want {
				t.Errorf("isNoDbModeConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetLocalSyncBranch(t *testing.T) {
	tests := []struct {
		name        string
		configYAML  string
		envVar      string
		want        string
		createFile  bool
	}{
		{
			name:       "no config.yaml exists",
			createFile: false,
			want:       "",
		},
		{
			name:       "config.yaml has no sync-branch key",
			configYAML: "issue-prefix: test\nauthor: testuser\n",
			createFile: true,
			want:       "",
		},
		{
			name:       "sync-branch without quotes",
			configYAML: "sync-branch: my-branch\n",
			createFile: true,
			want:       "my-branch",
		},
		{
			name:       "sync-branch with double quotes",
			configYAML: `sync-branch: "my-quoted-branch"` + "\n",
			createFile: true,
			want:       "my-quoted-branch",
		},
		{
			name:       "sync-branch with single quotes",
			configYAML: `sync-branch: 'single-quoted'` + "\n",
			createFile: true,
			want:       "single-quoted",
		},
		{
			name:       "env var takes precedence",
			configYAML: "sync-branch: config-branch\n",
			createFile: true,
			envVar:     "env-branch",
			want:       "env-branch",
		},
		{
			name:       "empty file",
			configYAML: "",
			createFile: true,
			want:       "",
		},
		{
			name:       "whitespace-only lines",
			configYAML: "   \n\t\n  \n",
			createFile: true,
			want:       "",
		},
		{
			name:       "sync-branch after comments",
			configYAML: "# This is a comment\n# sync-branch: fake\nsync-branch: real-branch\n",
			createFile: true,
			want:       "real-branch",
		},
		{
			name:       "sync-branch with trailing comment",
			configYAML: "sync-branch: branch-name # inline comment not valid YAML but test it\n",
			createFile: true,
			want:       "branch-name",
		},
		{
			name:       "sync-branch with special characters",
			configYAML: "sync-branch: feature/my-branch_v2.0\n",
			createFile: true,
			want:       "feature/my-branch_v2.0",
		},
		{
			name:       "sync-branch indented under section (not top-level)",
			configYAML: "settings:\n  sync-branch: nested-branch\n",
			createFile: true,
			want:       "", // Only top-level sync-branch should be read
		},
		{
			name:       "mixed config with sync-branch",
			configYAML: "issue-prefix: bd\nauthor: steve\nsync-branch: beads-sync\npriority: P2\n",
			createFile: true,
			want:       "beads-sync",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp beads directory
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatalf("Failed to create beads dir: %v", err)
			}

			// Create config.yaml if needed
			if tt.createFile {
				configPath := filepath.Join(beadsDir, "config.yaml")
				if err := os.WriteFile(configPath, []byte(tt.configYAML), 0644); err != nil {
					t.Fatalf("Failed to write config.yaml: %v", err)
				}
			}

			// Set env var if specified
			if tt.envVar != "" {
				t.Setenv("BEADS_SYNC_BRANCH", tt.envVar)
			}

			got := getLocalSyncBranch(beadsDir)
			if got != tt.want {
				t.Errorf("getLocalSyncBranch() = %q, want %q", got, tt.want)
			}
		})
	}
}
