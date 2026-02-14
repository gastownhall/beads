//go:build cgo

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestInitCommand(t *testing.T) {
	tests := []struct {
		name           string
		prefix         string
		quiet          bool
		wantOutputText string
		wantNoOutput   bool
	}{
		{
			name:           "init with default prefix",
			prefix:         "",
			quiet:          false,
			wantOutputText: "bd initialized successfully",
		},
		{
			name:           "init with custom prefix",
			prefix:         "myproject",
			quiet:          false,
			wantOutputText: "myproject-<hash>",
		},
		{
			name:         "init with quiet flag",
			prefix:       "test",
			quiet:        true,
			wantNoOutput: true,
		},
		{
			name:           "init with prefix ending in hyphen",
			prefix:         "test-",
			quiet:          false,
			wantOutputText: "test-<hash>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset global state
			origDBPath := dbPath
			defer func() { dbPath = origDBPath }()
			dbPath = ""

			// Reset Cobra command state
			rootCmd.SetArgs([]string{})
			initCmd.Flags().Set("prefix", "")
			initCmd.Flags().Set("quiet", "false")

			tmpDir := t.TempDir()
			t.Chdir(tmpDir)

			// Capture output
			var buf bytes.Buffer
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w
			defer func() {
				os.Stdout = oldStdout
			}()

			// Build command arguments
			args := []string{"init"}
			if tt.prefix != "" {
				args = append(args, "--prefix", tt.prefix)
			}
			if tt.quiet {
				args = append(args, "--quiet")
			}

			rootCmd.SetArgs(args)

			// Run command
			var err error
			err = rootCmd.Execute()

			// Restore stdout and read output
			w.Close()
			buf.ReadFrom(r)
			os.Stdout = oldStdout
			output := buf.String()

			if err != nil {
				t.Fatalf("init command failed: %v", err)
			}

			// Check output
			if tt.wantNoOutput {
				if output != "" {
					t.Errorf("Expected no output with --quiet, got: %s", output)
				}
			} else if tt.wantOutputText != "" {
				if !strings.Contains(output, tt.wantOutputText) {
					t.Errorf("Expected output to contain %q, got: %s", tt.wantOutputText, output)
				}
			}

			// Verify .beads directory was created
			beadsDir := filepath.Join(tmpDir, ".beads")
			if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
				t.Error(".beads directory was not created")
			}

			// Verify .gitignore was created with proper content
			gitignorePath := filepath.Join(beadsDir, ".gitignore")
			gitignoreContent, err := os.ReadFile(gitignorePath)
			if err != nil {
				t.Errorf(".gitignore file was not created: %v", err)
			} else {
				// Check for essential patterns
				gitignoreStr := string(gitignoreContent)
				expectedPatterns := []string{
					"*.db",
					"*.db?*",
					"*.db-journal",
					"*.db-wal",
					"*.db-shm",
					"daemon.log",
					"daemon.pid",
					"bd.sock",
					"beads.base.jsonl",
					"beads.left.jsonl",
					"beads.right.jsonl",
					"Do NOT add negation patterns", // Comment explaining fork protection
				}
				for _, pattern := range expectedPatterns {
					if !strings.Contains(gitignoreStr, pattern) {
						t.Errorf(".gitignore missing expected pattern: %s", pattern)
					}
				}
			}

			// Verify database was created (always beads.db now)
			dbPath := filepath.Join(beadsDir, "beads.db")
			if _, err := os.Stat(dbPath); os.IsNotExist(err) {
				t.Errorf("Database file was not created at %s", dbPath)
			}

			// Verify database has correct prefix
			// Note: This database was already created by init command, just open it
			store, err := openExistingTestDB(t, dbPath)
			if err != nil {
				t.Fatalf("Failed to open database: %v", err)
			}
			defer store.Close()

			ctx := context.Background()
			prefix, err := store.GetConfig(ctx, "issue_prefix")
			if err != nil {
				t.Fatalf("Failed to get issue prefix from database: %v", err)
			}

			expectedPrefix := tt.prefix
			if expectedPrefix == "" {
				expectedPrefix = filepath.Base(tmpDir)
			} else {
				expectedPrefix = strings.TrimRight(expectedPrefix, "-")
			}

			if prefix != expectedPrefix {
				t.Errorf("Expected prefix %q, got %q", expectedPrefix, prefix)
			}

			// Verify version metadata was set
			version, err := store.GetMetadata(ctx, "bd_version")
			if err != nil {
				t.Errorf("Failed to get bd_version metadata: %v", err)
			}
			if version == "" {
				t.Error("bd_version metadata was not set")
			}
		})
	}
}

// Note: Error case testing is omitted because the init command calls os.Exit()
// on errors, which makes it difficult to test in a unit test context.
// GH#807: Rejection of main/master as sync branch is tested at unit level in
// internal/syncbranch/syncbranch_test.go (TestValidateSyncBranchName, TestSet).

// TestInitSyncBranch groups sync-branch related init tests.
// GH#807: Verifies --branch flag behavior (rejection of main/master tested at unit level)
func TestInitSyncBranch(t *testing.T) {
	// resetInitState is a helper to reset global state for each subtest.
	resetInitState := func(t *testing.T) {
		t.Helper()
		origDBPath := dbPath
		t.Cleanup(func() { dbPath = origDBPath })
		dbPath = ""
		initCmd.Flags().Set("branch", "")
		initCmd.Flags().Set("force", "false")
	}

	t.Run("BranchFlagSetsSyncBranch", func(t *testing.T) {
		resetInitState(t)

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		if err := runCommandInDir(tmpDir, "git", "init", "--initial-branch=dev"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--branch", "beads-sync", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with --branch failed: %v", err)
		}

		dbFilePath := filepath.Join(tmpDir, ".beads", "beads.db")
		store, err := openExistingTestDB(t, dbFilePath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		syncBranch, err := store.GetConfig(ctx, "sync.branch")
		if err != nil {
			t.Fatalf("Failed to get sync.branch from database: %v", err)
		}
		if syncBranch != "beads-sync" {
			t.Errorf("Expected sync.branch 'beads-sync', got %q", syncBranch)
		}
	})

	// Verifies that init with --branch sets up .git/info/exclude to hide
	// untracked JSONL files from git status.
	t.Run("BranchFlagSetsGitExclude", func(t *testing.T) {
		resetInitState(t)

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		if err := runCommandInDir(tmpDir, "git", "init", "--initial-branch=dev"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}
		_ = runCommandInDir(tmpDir, "git", "config", "user.email", "test@test.com")
		_ = runCommandInDir(tmpDir, "git", "config", "user.name", "Test")

		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--branch", "beads-sync", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with --branch failed: %v", err)
		}

		excludePath := filepath.Join(tmpDir, ".git", "info", "exclude")
		content, err := os.ReadFile(excludePath)
		if err != nil {
			t.Fatalf("Failed to read .git/info/exclude: %v", err)
		}

		excludeContent := string(content)
		if !strings.Contains(excludeContent, ".beads/interactions.jsonl") {
			t.Errorf("Expected .git/info/exclude to contain '.beads/interactions.jsonl', got:\n%s", excludeContent)
		}
	})

	// Verifies that init without --branch flag still sets git index flags when
	// sync-branch is already configured in config.yaml (fresh clone scenario).
	t.Run("ExistingSyncBranchConfig", func(t *testing.T) {
		resetInitState(t)

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		if err := runCommandInDir(tmpDir, "git", "init", "--initial-branch=dev"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}
		_ = runCommandInDir(tmpDir, "git", "config", "user.email", "test@test.com")
		_ = runCommandInDir(tmpDir, "git", "config", "user.name", "Test")

		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("Failed to create .beads dir: %v", err)
		}
		configYaml := `sync-branch: "beads-sync"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYaml), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "interactions.jsonl"), []byte{}, 0644); err != nil {
			t.Fatalf("Failed to write interactions.jsonl: %v", err)
		}

		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet", "--force"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		excludePath := filepath.Join(tmpDir, ".git", "info", "exclude")
		content, err := os.ReadFile(excludePath)
		if err != nil {
			t.Fatalf("Failed to read .git/info/exclude: %v", err)
		}

		excludeContent := string(content)
		if !strings.Contains(excludeContent, ".beads/interactions.jsonl") {
			t.Errorf("Expected .git/info/exclude to contain '.beads/interactions.jsonl' when sync-branch is in config.yaml, got:\n%s", excludeContent)
		}
	})

	// Verifies that sync.branch is NOT auto-set when --branch is omitted.
	// GH#807: This was the root cause - init was auto-detecting current branch (e.g., main)
	t.Run("WithoutBranchFlag", func(t *testing.T) {
		resetInitState(t)

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		if err := runCommandInDir(tmpDir, "git", "init", "--initial-branch=main"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		dbFilePath := filepath.Join(tmpDir, ".beads", "beads.db")
		store, err := openExistingTestDB(t, dbFilePath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		syncBranch, err := store.GetConfig(ctx, "sync.branch")
		if err != nil {
			t.Fatalf("Failed to get sync.branch from database: %v", err)
		}
		if syncBranch != "" {
			t.Errorf("Expected sync.branch to be empty (not auto-detected), got %q", syncBranch)
		}
	})

	// Verifies that --branch flag persists to config.yaml.
	t.Run("BranchPersistsToConfigYaml", func(t *testing.T) {
		resetInitState(t)

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		if err := runCommandInDir(tmpDir, "git", "init", "--initial-branch=dev"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--branch", "beads-sync", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with --branch failed: %v", err)
		}

		configPath := filepath.Join(tmpDir, ".beads", "config.yaml")
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read config.yaml: %v", err)
		}

		configStr := string(content)

		if strings.Contains(configStr, "# sync-branch:") && !strings.Contains(configStr, "\nsync-branch:") {
			t.Errorf("BUG: --branch flag did not persist to config.yaml\n" +
				"Expected uncommented 'sync-branch: \"beads-sync\"'\n" +
				"Got commented '# sync-branch:' (only set in database, not config.yaml)")
		}

		if !strings.Contains(configStr, "sync-branch: \"beads-sync\"") {
			t.Errorf("config.yaml should contain 'sync-branch: \"beads-sync\"', got:\n%s", configStr)
		}
	})

	// Verifies that --branch flag works on reinit.
	// GH#927: When reinitializing with --branch, config.yaml should be updated even if it exists.
	t.Run("ReinitWithBranch", func(t *testing.T) {
		resetInitState(t)

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		if err := runCommandInDir(tmpDir, "git", "init", "--initial-branch=dev"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// First init WITHOUT --branch
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("First init failed: %v", err)
		}

		configPath := filepath.Join(tmpDir, ".beads", "config.yaml")
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read config.yaml: %v", err)
		}
		if !strings.Contains(string(content), "# sync-branch:") {
			t.Errorf("Initial config.yaml should have commented sync-branch")
		}

		// Reset Cobra flags for reinit
		initCmd.Flags().Set("branch", "")
		initCmd.Flags().Set("force", "false")

		// Reinit WITH --branch
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--branch", "beads-sync", "--force", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Reinit with --branch failed: %v", err)
		}

		content, err = os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read config.yaml after reinit: %v", err)
		}

		configStr := string(content)
		if !strings.Contains(configStr, "sync-branch: \"beads-sync\"") {
			t.Errorf("After reinit with --branch, config.yaml should contain uncommented 'sync-branch: \"beads-sync\"', got:\n%s", configStr)
		}
	})
}

func TestInitAlreadyInitialized(t *testing.T) {
	// Reset global state
	origDBPath := dbPath
	defer func() { dbPath = origDBPath }()
	dbPath = ""

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize once
	rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("First init failed: %v", err)
	}

	// Initialize again with same prefix and --force flag (bd-emg: safety guard)
	// Without --force, init should refuse when database already exists
	rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet", "--force"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Second init with --force failed: %v", err)
	}

	// Verify database still works (always beads.db now)
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	store, err := openExistingTestDB(t, dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("Failed to get prefix after re-init: %v", err)
	}

	if prefix != "test" {
		t.Errorf("Expected prefix 'test', got %q", prefix)
	}
}

func TestInitWithCustomDBPath(t *testing.T) {
	// Save original state
	origDBPath := dbPath
	defer func() { dbPath = origDBPath }()

	tmpDir := t.TempDir()
	customDBDir := filepath.Join(tmpDir, "custom", "location")

	// Change to a different directory to ensure --db flag is actually used
	workDir := filepath.Join(tmpDir, "workdir")
	if err := os.MkdirAll(workDir, 0750); err != nil {
		t.Fatalf("Failed to create work directory: %v", err)
	}

	t.Chdir(workDir)

	customDBPath := filepath.Join(customDBDir, "test.db")

	// Test with BEADS_DB environment variable (replacing --db flag test)
	t.Run("init with BEADS_DB pointing to custom path", func(t *testing.T) {
		dbPath = "" // Reset global
		os.Setenv("BEADS_DB", customDBPath)
		defer os.Unsetenv("BEADS_DB")

		rootCmd.SetArgs([]string{"init", "--prefix", "custom", "--quiet"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DB failed: %v", err)
		}

		// Verify database was created at custom location
		if _, err := os.Stat(customDBPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at custom path %s", customDBPath)
		}

		// Verify database works
		store, err := openExistingTestDB(t, customDBPath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix: %v", err)
		}

		if prefix != "custom" {
			t.Errorf("Expected prefix 'custom', got %q", prefix)
		}

		// Verify .beads/ directory was NOT created in work directory
		if _, err := os.Stat(filepath.Join(workDir, ".beads")); err == nil {
			t.Error(".beads/ directory should not be created when using BEADS_DB env var")
		}
	})

	// Test with BEADS_DB env var
	t.Run("init with BEADS_DB env var", func(t *testing.T) {
		dbPath = "" // Reset global
		envDBPath := filepath.Join(tmpDir, "env", "location", "env.db")
		os.Setenv("BEADS_DB", envDBPath)
		defer os.Unsetenv("BEADS_DB")

		rootCmd.SetArgs([]string{"init", "--prefix", "envtest", "--quiet"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DB failed: %v", err)
		}

		// Verify database was created at env location
		if _, err := os.Stat(envDBPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at BEADS_DB path %s", envDBPath)
		}

		// Verify database works
		store, err := openExistingTestDB(t, envDBPath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix: %v", err)
		}

		if prefix != "envtest" {
			t.Errorf("Expected prefix 'envtest', got %q", prefix)
		}
	})

	// Test that BEADS_DB path containing ".beads" doesn't create CWD/.beads
	t.Run("init with BEADS_DB path containing .beads", func(t *testing.T) {
		dbPath = "" // Reset global
		// Path contains ".beads" but is outside work directory
		customPath := filepath.Join(tmpDir, "storage", ".beads-backup", "test.db")
		os.Setenv("BEADS_DB", customPath)
		defer os.Unsetenv("BEADS_DB")

		rootCmd.SetArgs([]string{"init", "--prefix", "beadstest", "--quiet"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with custom .beads path failed: %v", err)
		}

		// Verify database was created at custom location
		if _, err := os.Stat(customPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at custom path %s", customPath)
		}

		// Verify .beads/ directory was NOT created in work directory
		if _, err := os.Stat(filepath.Join(workDir, ".beads")); err == nil {
			t.Error(".beads/ directory should not be created in CWD when BEADS_DB path contains .beads")
		}
	})

	// Test with multiple BEADS_DB variations
	t.Run("BEADS_DB with subdirectories", func(t *testing.T) {
		dbPath = "" // Reset global
		envPath := filepath.Join(tmpDir, "env", "subdirs", "test.db")

		os.Setenv("BEADS_DB", envPath)
		defer os.Unsetenv("BEADS_DB")

		rootCmd.SetArgs([]string{"init", "--prefix", "envtest2", "--quiet"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DB subdirs failed: %v", err)
		}

		// Verify database was created at env location
		if _, err := os.Stat(envPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at BEADS_DB path %s", envPath)
		}

		// Verify .beads/ directory was NOT created in work directory
		if _, err := os.Stat(filepath.Join(workDir, ".beads")); err == nil {
			t.Error(".beads/ directory should not be created in CWD when BEADS_DB is set")
		}
	})
}

func TestInitNoDbMode(t *testing.T) {
	// Reset global state
	origDBPath := dbPath
	origNoDb := noDb
	defer func() {
		dbPath = origDBPath
		noDb = origNoDb
	}()
	dbPath = ""
	noDb = false

	// Reset Cobra flags - critical for --no-db to work correctly
	rootCmd.PersistentFlags().Set("no-db", "false")

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Set BEADS_DIR to prevent git repo detection from finding project's .beads
	origBeadsDir := os.Getenv("BEADS_DIR")
	os.Setenv("BEADS_DIR", filepath.Join(tmpDir, ".beads"))
	// Reset caches so RepoContext picks up new BEADS_DIR and CWD
	beads.ResetCaches()
	git.ResetCaches()
	defer func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		// Reset caches on cleanup too
		beads.ResetCaches()
		git.ResetCaches()
	}()

	// Initialize with --no-db flag
	rootCmd.SetArgs([]string{"init", "--no-db", "--prefix", "test", "--quiet"})

	t.Logf("DEBUG: noDb before Execute=%v", noDb)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Init with --no-db failed: %v", err)
	}

	t.Logf("DEBUG: noDb after Execute=%v", noDb)

	// Debug: Check where files were created
	beadsDirEnv := os.Getenv("BEADS_DIR")
	t.Logf("DEBUG: tmpDir=%s", tmpDir)
	t.Logf("DEBUG: BEADS_DIR=%s", beadsDirEnv)
	t.Logf("DEBUG: CWD=%s", func() string { cwd, _ := os.Getwd(); return cwd }())

	// Check what files exist in tmpDir
	entries, _ := os.ReadDir(tmpDir)
	t.Logf("DEBUG: entries in tmpDir: %v", entries)
	if beadsDirEnv != "" {
		beadsEntries, err := os.ReadDir(beadsDirEnv)
		t.Logf("DEBUG: entries in BEADS_DIR: %v (err: %v)", beadsEntries, err)
	}

	// Verify issues.jsonl was created
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		// Also check at BEADS_DIR directly
		beadsDirJsonlPath := filepath.Join(beadsDirEnv, "issues.jsonl")
		if _, err2 := os.Stat(beadsDirJsonlPath); err2 == nil {
			t.Logf("DEBUG: issues.jsonl found at BEADS_DIR path: %s", beadsDirJsonlPath)
		}
		t.Error("issues.jsonl was not created in --no-db mode")
	}

	// Verify config.yaml was created with no-db: true
	configPath := filepath.Join(tmpDir, ".beads", "config.yaml")
	configContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config.yaml: %v", err)
	}

	configStr := string(configContent)
	if !strings.Contains(configStr, "no-db: true") {
		t.Error("config.yaml should contain 'no-db: true' in --no-db mode")
	}
	if !strings.Contains(configStr, "issue-prefix:") {
		t.Error("config.yaml should contain issue-prefix in --no-db mode")
	}

	// Reset config so it picks up the newly created config.yaml
	// (simulates a new process invocation which would load fresh config)
	initConfigForTest(t)

	// Verify config has correct values
	if !config.GetBool("no-db") {
		t.Error("config should have no-db=true after init --no-db")
	}
	if config.GetString("issue-prefix") != "test" {
		t.Errorf("config should have issue-prefix='test', got %q", config.GetString("issue-prefix"))
	}

	// NOTE: Testing subsequent command execution in the same process is complex
	// due to cobra's flag caching and global state. The key functionality
	// (init creating proper config.yaml for no-db mode) is verified above.
	// Real-world usage works correctly since each command is a fresh process.

	// Verify no SQLite database was created
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	if _, err := os.Stat(dbPath); err == nil {
		t.Error("SQLite database should not be created in --no-db mode")
	}
}

func TestInitMergeDriverAutoConfiguration(t *testing.T) {
	t.Run("merge driver auto-configured during init", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo first
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Run bd init with quiet mode
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Verify git config was set
		output, err := runCommandInDirWithOutput(tmpDir, "git", "config", "merge.beads.driver")
		if err != nil {
			t.Fatalf("Failed to get git config: %v", err)
		}
		if !strings.Contains(output, "bd merge") {
			t.Errorf("Expected merge driver to contain 'bd merge', got: %s", output)
		}

		// Verify .gitattributes was created
		gitattrsPath := filepath.Join(tmpDir, ".gitattributes")
		content, err := os.ReadFile(gitattrsPath)
		if err != nil {
			t.Fatalf("Failed to read .gitattributes: %v", err)
		}
		if !strings.Contains(string(content), ".beads/issues.jsonl merge=beads") {
			t.Error(".gitattributes should contain merge driver configuration")
		}
	})

	t.Run("skip merge driver with flag", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo first
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Run bd init with --skip-merge-driver
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--skip-merge-driver", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Verify git config was NOT set locally (use --local to avoid picking up global config)
		_, err := runCommandInDirWithOutput(tmpDir, "git", "config", "--local", "merge.beads.driver")
		if err == nil {
			t.Error("Expected git config to not be set with --skip-merge-driver")
		}

		// Verify .gitattributes was NOT created
		gitattrsPath := filepath.Join(tmpDir, ".gitattributes")
		if _, err := os.Stat(gitattrsPath); err == nil {
			t.Error(".gitattributes should not be created with --skip-merge-driver")
		}
	})

	t.Run("non-git repo skips merge driver silently", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// DON'T initialize git repo

		// Run bd init - should succeed even without git
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init should succeed in non-git directory: %v", err)
		}

		// Verify .beads was still created
		beadsDir := filepath.Join(tmpDir, ".beads")
		if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
			t.Error(".beads directory should be created even without git")
		}
	})

	t.Run("detect already-installed merge driver", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Pre-configure merge driver manually
		if err := runCommandInDir(tmpDir, "git", "config", "merge.beads.driver", "bd merge %A %O %A %B"); err != nil {
			t.Fatalf("Failed to set git config: %v", err)
		}

		// Create .gitattributes with merge driver
		gitattrsPath := filepath.Join(tmpDir, ".gitattributes")
		initialContent := "# Existing config\n.beads/issues.jsonl merge=beads\n"
		if err := os.WriteFile(gitattrsPath, []byte(initialContent), 0644); err != nil {
			t.Fatalf("Failed to create .gitattributes: %v", err)
		}

		// Run bd init - should detect existing config
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Verify git config still exists (not duplicated)
		output, err := runCommandInDirWithOutput(tmpDir, "git", "config", "merge.beads.driver")
		if err != nil {
			t.Fatalf("Git config should still be set: %v", err)
		}
		if !strings.Contains(output, "bd merge") {
			t.Errorf("Expected merge driver to contain 'bd merge', got: %s", output)
		}

		// Verify .gitattributes wasn't duplicated
		content, err := os.ReadFile(gitattrsPath)
		if err != nil {
			t.Fatalf("Failed to read .gitattributes: %v", err)
		}

		contentStr := string(content)
		// Count occurrences - should only appear once
		count := strings.Count(contentStr, ".beads/issues.jsonl merge=beads")
		if count != 1 {
			t.Errorf("Expected .gitattributes to contain merge config exactly once, found %d times", count)
		}

		// Should still have the comment
		if !strings.Contains(contentStr, "# Existing config") {
			t.Error(".gitattributes should preserve existing content")
		}
	})

	t.Run("append to existing .gitattributes", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		// Reset Cobra flags
		initCmd.Flags().Set("skip-merge-driver", "false")

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Create .gitattributes with existing content (no newline at end)
		gitattrsPath := filepath.Join(tmpDir, ".gitattributes")
		existingContent := "*.txt text\n*.jpg binary"
		if err := os.WriteFile(gitattrsPath, []byte(existingContent), 0644); err != nil {
			t.Fatalf("Failed to create .gitattributes: %v", err)
		}

		// Run bd init
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Verify .gitattributes was appended to, not overwritten
		content, err := os.ReadFile(gitattrsPath)
		if err != nil {
			t.Fatalf("Failed to read .gitattributes: %v", err)
		}

		contentStr := string(content)

		// Should contain original content
		if !strings.Contains(contentStr, "*.txt text") {
			t.Error(".gitattributes should preserve original content")
		}
		if !strings.Contains(contentStr, "*.jpg binary") {
			t.Error(".gitattributes should preserve original content")
		}

		// Should contain beads config
		if !strings.Contains(contentStr, ".beads/issues.jsonl merge=beads") {
			t.Error(".gitattributes should contain beads merge config")
		}

		// Beads config should come after existing content
		txtIdx := strings.Index(contentStr, "*.txt")
		beadsIdx := strings.Index(contentStr, ".beads/issues.jsonl")
		if txtIdx >= beadsIdx {
			t.Error("Beads config should be appended after existing content")
		}
	})

	t.Run("verify git config has correct settings", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		// Reset Cobra flags
		initCmd.Flags().Set("skip-merge-driver", "false")

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Run bd init
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Verify merge.beads.driver is set correctly
		driver, err := runCommandInDirWithOutput(tmpDir, "git", "config", "merge.beads.driver")
		if err != nil {
			t.Fatalf("Failed to get merge.beads.driver: %v", err)
		}
		driver = strings.TrimSpace(driver)
		expected := "bd merge %A %O %A %B"
		if driver != expected {
			t.Errorf("Expected merge.beads.driver to be %q, got %q", expected, driver)
		}

		// Verify merge.beads.name is set
		name, err := runCommandInDirWithOutput(tmpDir, "git", "config", "merge.beads.name")
		if err != nil {
			t.Fatalf("Failed to get merge.beads.name: %v", err)
		}
		name = strings.TrimSpace(name)
		if !strings.Contains(name, "bd") {
			t.Errorf("Expected merge.beads.name to contain 'bd', got %q", name)
		}
	})

	t.Run("auto-repair stale merge driver with invalid placeholders", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Configure stale merge driver with old invalid placeholders (%L/%R)
		// This simulates a user who initialized with bd version <0.24.0
		if err := runCommandInDir(tmpDir, "git", "config", "merge.beads.driver", "bd merge %L %R"); err != nil {
			t.Fatalf("Failed to set stale git config: %v", err)
		}

		// Create .gitattributes with merge driver
		gitattrsPath := filepath.Join(tmpDir, ".gitattributes")
		if err := os.WriteFile(gitattrsPath, []byte(".beads/beads.jsonl merge=beads\n"), 0644); err != nil {
			t.Fatalf("Failed to create .gitattributes: %v", err)
		}

		// Run bd init - should detect stale config and repair it
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Verify merge driver was updated to correct placeholders
		driver, err := runCommandInDirWithOutput(tmpDir, "git", "config", "merge.beads.driver")
		if err != nil {
			t.Fatalf("Failed to get merge.beads.driver: %v", err)
		}
		driver = strings.TrimSpace(driver)
		expected := "bd merge %A %O %A %B"
		if driver != expected {
			t.Errorf("Expected merge driver to be repaired to %q, got %q", expected, driver)
		}

		// Verify it no longer contains invalid placeholders
		if strings.Contains(driver, "%L") || strings.Contains(driver, "%R") {
			t.Errorf("Merge driver should not contain invalid %%L or %%R placeholders, got %q", driver)
		}
	})

	t.Run("detect canonical issues.jsonl filename in gitattributes", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Pre-configure correct merge driver and canonical filename in .gitattributes
		if err := runCommandInDir(tmpDir, "git", "config", "merge.beads.driver", "bd merge %A %O %A %B"); err != nil {
			t.Fatalf("Failed to set git config: %v", err)
		}

		// Create .gitattributes with canonical filename (issues.jsonl, not beads.jsonl)
		gitattrsPath := filepath.Join(tmpDir, ".gitattributes")
		if err := os.WriteFile(gitattrsPath, []byte(".beads/issues.jsonl merge=beads\n"), 0644); err != nil {
			t.Fatalf("Failed to create .gitattributes: %v", err)
		}

		// Run bd init - should detect existing correct config and NOT reinstall
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Verify merge driver is still correct (not reinstalled unnecessarily)
		driver, err := runCommandInDirWithOutput(tmpDir, "git", "config", "merge.beads.driver")
		if err != nil {
			t.Fatalf("Failed to get merge.beads.driver: %v", err)
		}
		driver = strings.TrimSpace(driver)
		expected := "bd merge %A %O %A %B"
		if driver != expected {
			t.Errorf("Expected merge driver to remain %q, got %q", expected, driver)
		}

		// Verify .gitattributes still has canonical filename (not overwritten)
		content, err := os.ReadFile(gitattrsPath)
		if err != nil {
			t.Fatalf("Failed to read .gitattributes: %v", err)
		}
		if !strings.Contains(string(content), ".beads/issues.jsonl merge=beads") {
			t.Errorf(".gitattributes should still contain canonical filename pattern")
		}
	})
}

// TestReadFirstIssueFromJSONL_ValidFile verifies reading first issue from valid JSONL
func TestReadFirstIssueFromJSONL_ValidFile(t *testing.T) {
	tempDir := t.TempDir()
	jsonlPath := filepath.Join(tempDir, "test.jsonl")

	// Create test JSONL file with multiple issues
	content := `{"id":"bd-1","title":"First Issue","description":"First test"}
{"id":"bd-2","title":"Second Issue","description":"Second test"}
{"id":"bd-3","title":"Third Issue","description":"Third test"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	issue, err := readFirstIssueFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("readFirstIssueFromJSONL failed: %v", err)
	}

	if issue == nil {
		t.Fatal("Expected non-nil issue, got nil")
	}

	// Verify we got the FIRST issue
	if issue.ID != "bd-1" {
		t.Errorf("Expected ID 'bd-1', got '%s'", issue.ID)
	}
	if issue.Title != "First Issue" {
		t.Errorf("Expected title 'First Issue', got '%s'", issue.Title)
	}
	if issue.Description != "First test" {
		t.Errorf("Expected description 'First test', got '%s'", issue.Description)
	}
}

// TestReadFirstIssueFromJSONL_EmptyLines verifies skipping empty lines
func TestReadFirstIssueFromJSONL_EmptyLines(t *testing.T) {
	tempDir := t.TempDir()
	jsonlPath := filepath.Join(tempDir, "test.jsonl")

	// Create JSONL with empty lines before first valid issue
	content := `

{"id":"bd-1","title":"First Valid Issue"}
{"id":"bd-2","title":"Second Issue"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	issue, err := readFirstIssueFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("readFirstIssueFromJSONL failed: %v", err)
	}

	if issue == nil {
		t.Fatal("Expected non-nil issue, got nil")
	}

	if issue.ID != "bd-1" {
		t.Errorf("Expected ID 'bd-1', got '%s'", issue.ID)
	}
	if issue.Title != "First Valid Issue" {
		t.Errorf("Expected title 'First Valid Issue', got '%s'", issue.Title)
	}
}

// TestReadFirstIssueFromJSONL_EmptyFile verifies handling of empty file
func TestReadFirstIssueFromJSONL_EmptyFile(t *testing.T) {
	tempDir := t.TempDir()
	jsonlPath := filepath.Join(tempDir, "empty.jsonl")

	// Create empty file
	if err := os.WriteFile(jsonlPath, []byte(""), 0o600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	issue, err := readFirstIssueFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("readFirstIssueFromJSONL should not error on empty file: %v", err)
	}

	if issue != nil {
		t.Errorf("Expected nil issue for empty file, got %+v", issue)
	}
}

// TestSetupClaudeSettings_InvalidJSON verifies that invalid JSON in existing
// settings.local.json returns an error instead of silently overwriting.
// This is a regression test for bd-5bj where user settings were lost.
func TestSetupClaudeSettings_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .claude directory
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("Failed to create .claude directory: %v", err)
	}

	// Create settings.local.json with invalid JSON (array syntax in object context)
	// This is the exact pattern that caused the bug in the user's file
	invalidJSON := `{
  "permissions": {
    "allow": [
      "Bash(python3:*)"
    ],
    "deny": [
      "_comment": "Add commands to block here"
    ]
  }
}`
	settingsPath := filepath.Join(claudeDir, "settings.local.json")
	if err := os.WriteFile(settingsPath, []byte(invalidJSON), 0644); err != nil {
		t.Fatalf("Failed to write invalid settings: %v", err)
	}

	// Call setupClaudeSettings - should return an error
	var err error
	err = setupClaudeSettings(false)
	if err == nil {
		t.Fatal("Expected error for invalid JSON, got nil")
	}

	// Verify the error message mentions invalid JSON
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("Expected error to mention 'invalid JSON', got: %v", err)
	}

	// Verify the original file was NOT modified
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings file: %v", err)
	}

	if !strings.Contains(string(content), "permissions") {
		t.Error("Original file content should be preserved")
	}

	if strings.Contains(string(content), "bd onboard") {
		t.Error("File should NOT contain bd onboard prompt after error")
	}
}

// TestSetupClaudeSettings_ValidJSON verifies that valid JSON is properly updated
func TestSetupClaudeSettings_ValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .claude directory
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("Failed to create .claude directory: %v", err)
	}

	// Create settings.local.json with valid JSON
	validJSON := `{
  "permissions": {
    "allow": [
      "Bash(python3:*)"
    ]
  },
  "hooks": {
    "PreToolUse": []
  }
}`
	settingsPath := filepath.Join(claudeDir, "settings.local.json")
	if err := os.WriteFile(settingsPath, []byte(validJSON), 0644); err != nil {
		t.Fatalf("Failed to write valid settings: %v", err)
	}

	// Call setupClaudeSettings - should succeed
	var err error
	err = setupClaudeSettings(false)
	if err != nil {
		t.Fatalf("Expected no error for valid JSON, got: %v", err)
	}

	// Verify the file was updated with prompt AND preserved existing settings
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings file: %v", err)
	}

	contentStr := string(content)

	// Should contain the new prompt
	if !strings.Contains(contentStr, "bd onboard") {
		t.Error("File should contain bd onboard prompt")
	}

	// Should preserve existing permissions
	if !strings.Contains(contentStr, "permissions") {
		t.Error("File should preserve permissions section")
	}

	// Should preserve existing hooks
	if !strings.Contains(contentStr, "hooks") {
		t.Error("File should preserve hooks section")
	}

	if !strings.Contains(contentStr, "PreToolUse") {
		t.Error("File should preserve PreToolUse hook")
	}
}

// TestSetupClaudeSettings_NoExistingFile verifies behavior when no file exists
func TestSetupClaudeSettings_NoExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Don't create .claude directory - setupClaudeSettings should create it

	// Call setupClaudeSettings - should succeed
	var err error
	err = setupClaudeSettings(false)
	if err != nil {
		t.Fatalf("Expected no error when no file exists, got: %v", err)
	}

	// Verify the file was created with prompt
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.local.json")
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings file: %v", err)
	}

	if !strings.Contains(string(content), "bd onboard") {
		t.Error("File should contain bd onboard prompt")
	}
}

// TestInitBranchPersistsToConfigYaml verifies that --branch flag persists to config.yaml
// GH#927 Bug 3: The --branch flag sets sync.branch in database but NOT in config.yaml.
// This matters because config.yaml is version-controlled and shared across clones,
// while the database is local and gitignored.
// Note: TestInitBranchPersistsToConfigYaml and TestInitReinitWithBranch are now
// subtests of TestInitSyncBranch above.

// setupIsolatedGitConfig creates an empty git config in tmpDir and sets GIT_CONFIG_GLOBAL
// to prevent tests from using the real user's global git config.
func setupIsolatedGitConfig(t *testing.T, tmpDir string) {
	t.Helper()
	gitConfigPath := filepath.Join(tmpDir, ".gitconfig")
	if err := os.WriteFile(gitConfigPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfigPath)
}

// TestSetupGlobalGitIgnore_ReadOnly verifies graceful handling when the
// gitignore file cannot be written (prints manual instructions instead of failing).
func TestSetupGlobalGitIgnore_ReadOnly(t *testing.T) {
	t.Run("read-only file", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("macOS allows file owner to write to read-only (0444) files")
		}
		tmpDir := t.TempDir()
		setupIsolatedGitConfig(t, tmpDir)

		configDir := filepath.Join(tmpDir, ".config", "git")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			t.Fatal(err)
		}

		ignorePath := filepath.Join(configDir, "ignore")
		if err := os.WriteFile(ignorePath, []byte("# existing\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(ignorePath, 0444); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(ignorePath, 0644)

		output := captureStdout(t, func() error {
			return setupGlobalGitIgnore(tmpDir, "/test/project", false)
		})

		if !strings.Contains(output, "Unable to write") {
			t.Error("expected instructions for manual addition")
		}
		if !strings.Contains(output, "/test/project/.beads/") {
			t.Error("expected .beads pattern in output")
		}
	})

	t.Run("symlink to read-only file", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("macOS allows file owner to write to read-only (0444) files")
		}
		tmpDir := t.TempDir()
		setupIsolatedGitConfig(t, tmpDir)

		// Target file in a separate location
		targetDir := filepath.Join(tmpDir, "target")
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			t.Fatal(err)
		}
		targetFile := filepath.Join(targetDir, "ignore")
		if err := os.WriteFile(targetFile, []byte("# existing\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(targetFile, 0444); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(targetFile, 0644)

		// Symlink from expected location
		configDir := filepath.Join(tmpDir, ".config", "git")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(targetFile, filepath.Join(configDir, "ignore")); err != nil {
			t.Fatal(err)
		}

		output := captureStdout(t, func() error {
			return setupGlobalGitIgnore(tmpDir, "/test/project", false)
		})

		if !strings.Contains(output, "Unable to write") {
			t.Error("expected instructions for manual addition")
		}
		if !strings.Contains(output, "/test/project/.beads/") {
			t.Error("expected .beads pattern in output")
		}
	})
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := fn()

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stdout = oldStdout

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	return buf.String()
}

// TestInitPromptRoleConfig tests the beads.role git config read/write functions
func TestInitPromptRoleConfig(t *testing.T) {
	t.Run("getBeadsRole returns empty when not configured", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		role, hasRole := getBeadsRole()
		if hasRole {
			t.Errorf("Expected hasRole=false when not configured, got true with role=%q", role)
		}
		if role != "" {
			t.Errorf("Expected empty role when not configured, got %q", role)
		}
	})

	t.Run("setBeadsRole and getBeadsRole roundtrip", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Set role to contributor
		if err := setBeadsRole("contributor"); err != nil {
			t.Fatalf("Failed to set beads.role: %v", err)
		}

		role, hasRole := getBeadsRole()
		if !hasRole {
			t.Error("Expected hasRole=true after setting role")
		}
		if role != "contributor" {
			t.Errorf("Expected role 'contributor', got %q", role)
		}

		// Change to maintainer
		if err := setBeadsRole("maintainer"); err != nil {
			t.Fatalf("Failed to set beads.role: %v", err)
		}

		role, hasRole = getBeadsRole()
		if !hasRole {
			t.Error("Expected hasRole=true after setting role")
		}
		if role != "maintainer" {
			t.Errorf("Expected role 'maintainer', got %q", role)
		}
	})
}

// TestInitPromptSkippedWithFlags verifies that --contributor and --team flags skip the prompt
func TestInitPromptSkippedWithFlags(t *testing.T) {
	t.Run("contributor flag skips prompt and runs wizard", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		// Reset caches so RepoContext picks up new directory
		beads.ResetCaches()
		git.ResetCaches()
		defer func() {
			beads.ResetCaches()
			git.ResetCaches()
		}()

		// Reset Cobra flags
		initCmd.Flags().Set("contributor", "false")

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Verify no role is set initially
		role, hasRole := getBeadsRole()
		if hasRole {
			t.Fatalf("Expected no role initially, got %q", role)
		}

		// Run bd init with --contributor flag (quiet to suppress wizard output)
		// The wizard will fail because there's no planning repo, but that's OK
		// We just want to verify the flag bypasses the prompt
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--contributor", "--quiet"})
		_ = rootCmd.Execute() // Ignore error - wizard may fail

		// The --contributor flag should NOT set beads.role (that's done by prompt, not flag)
		// The flag just triggers the wizard directly
	})

	t.Run("team flag skips prompt", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		// Reset caches so RepoContext picks up new directory
		beads.ResetCaches()
		git.ResetCaches()
		defer func() {
			beads.ResetCaches()
			git.ResetCaches()
		}()

		// Reset Cobra flags
		initCmd.Flags().Set("team", "false")

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Verify no role is set initially
		role, hasRole := getBeadsRole()
		if hasRole {
			t.Fatalf("Expected no role initially, got %q", role)
		}

		// Run bd init with --team flag
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--team", "--quiet"})
		_ = rootCmd.Execute() // Ignore error - wizard may fail

		// The --team flag should not set beads.role
		// (team wizard is separate from contributor/maintainer roles)
	})
}

// TestInitPromptTTYDetection verifies shouldPromptForRole behavior
func TestInitPromptTTYDetection(t *testing.T) {
	// Note: In test environment, stdin is typically NOT a TTY (it's a pipe)
	// This test verifies the function works, not that we're in a TTY

	t.Run("shouldPromptForRole returns false in test environment", func(t *testing.T) {
		// In test environment, stdin is typically piped, not a TTY
		result := shouldPromptForRole()

		// We can't guarantee what the result will be in all test environments,
		// but we can verify the function doesn't panic and returns a bool
		if result {
			t.Log("Test environment has TTY stdin (unusual but acceptable)")
		} else {
			t.Log("Test environment does not have TTY stdin (expected)")
		}
	})
}

// TestInitPromptNonGitRepo verifies prompt is skipped in non-git directories
func TestInitPromptNonGitRepo(t *testing.T) {
	// Reset global state
	origDBPath := dbPath
	defer func() { dbPath = origDBPath }()
	dbPath = ""

	// Reset caches so RepoContext picks up new directory
	beads.ResetCaches()
	git.ResetCaches()
	defer func() {
		beads.ResetCaches()
		git.ResetCaches()
	}()

	// Reset Cobra flags that may be set from previous tests
	initCmd.Flags().Set("contributor", "false")
	initCmd.Flags().Set("team", "false")

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// DON'T initialize git repo

	// Run bd init - should succeed without prompting (no git repo)
	rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Init should succeed in non-git directory: %v", err)
	}

	// Verify .beads was created
	beadsDir := filepath.Join(tmpDir, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		t.Error(".beads directory should be created even without git")
	}
}

// TestInitPromptExistingRole verifies behavior when beads.role is already set
func TestInitPromptExistingRole(t *testing.T) {
	t.Run("existing role is preserved on reinit with --force", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		// Reset caches so RepoContext picks up new directory
		beads.ResetCaches()
		git.ResetCaches()
		defer func() {
			beads.ResetCaches()
			git.ResetCaches()
		}()

		// Reset Cobra flags that may be set from previous tests
		initCmd.Flags().Set("contributor", "false")
		initCmd.Flags().Set("team", "false")
		initCmd.Flags().Set("force", "false")

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Initialize git repo
		if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
			t.Fatalf("Failed to init git: %v", err)
		}

		// Set role before init
		if err := setBeadsRole("contributor"); err != nil {
			t.Fatalf("Failed to set beads.role: %v", err)
		}

		// Run bd init (non-interactive, so prompt is skipped)
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Verify role is still set
		role, hasRole := getBeadsRole()
		if !hasRole {
			t.Error("Expected beads.role to still be set after init")
		}
		if role != "contributor" {
			t.Errorf("Expected role 'contributor' to be preserved, got %q", role)
		}

		// Reset Cobra flags for reinit
		initCmd.Flags().Set("force", "false")

		// Reinit with --force (non-interactive)
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet", "--force"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Reinit failed: %v", err)
		}

		// Verify role is still set (not cleared by reinit)
		role, hasRole = getBeadsRole()
		if !hasRole {
			t.Error("Expected beads.role to still be set after reinit")
		}
		if role != "contributor" {
			t.Errorf("Expected role 'contributor' to be preserved after reinit, got %q", role)
		}
	})
}

// TestInitWithRedirect verifies that bd init creates the database in the redirect target,
// not in the local .beads directory. (GH#bd-0qel)
// TestInitRedirect groups redirect-related init tests.
func TestInitRedirect(t *testing.T) {
	resetRedirectState := func(t *testing.T) {
		t.Helper()
		origDBPath := dbPath
		origBeadsDir := os.Getenv("BEADS_DIR")
		t.Cleanup(func() {
			dbPath = origDBPath
			if origBeadsDir != "" {
				os.Setenv("BEADS_DIR", origBeadsDir)
			} else {
				os.Unsetenv("BEADS_DIR")
			}
		})
		dbPath = ""
		os.Unsetenv("BEADS_DIR")
		initCmd.Flags().Set("prefix", "")
		initCmd.Flags().Set("quiet", "false")
		initCmd.Flags().Set("force", "false")
	}

	t.Run("RedirectCreatesDBInTarget", func(t *testing.T) {
		resetRedirectState(t)

		tmpDir := t.TempDir()

		projectDir := filepath.Join(tmpDir, "project")
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatal(err)
		}

		localBeadsDir := filepath.Join(projectDir, ".beads")
		if err := os.MkdirAll(localBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		targetBeadsDir := filepath.Join(tmpDir, "canonical", ".beads")
		if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		redirectPath := filepath.Join(localBeadsDir, beads.RedirectFileName)
		if err := os.WriteFile(redirectPath, []byte("../canonical/.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}

		t.Chdir(projectDir)

		rootCmd.SetArgs([]string{"init", "--prefix", "redirect-test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with redirect failed: %v", err)
		}

		targetDBPath := filepath.Join(targetBeadsDir, "beads.db")
		if _, err := os.Stat(targetDBPath); os.IsNotExist(err) {
			t.Errorf("Database was NOT created in redirect target: %s", targetDBPath)
		}

		localDBPath := filepath.Join(localBeadsDir, "beads.db")
		if _, err := os.Stat(localDBPath); err == nil {
			t.Errorf("Database was incorrectly created in local .beads: %s (should be in redirect target)", localDBPath)
		}

		store, err := openExistingTestDB(t, targetDBPath)
		if err != nil {
			t.Fatalf("Failed to open database in redirect target: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get issue prefix from database: %v", err)
		}
		if prefix != "redirect-test" {
			t.Errorf("Expected prefix 'redirect-test', got %q", prefix)
		}
	})

	// Verifies that bd init errors when the redirect target already has a database,
	// preventing accidental overwrites. (GH#bd-0qel)
	t.Run("ErrorWhenTargetHasExistingDB", func(t *testing.T) {
		resetRedirectState(t)

		tmpDir := t.TempDir()

		canonicalDir := filepath.Join(tmpDir, "canonical")
		canonicalBeadsDir := filepath.Join(canonicalDir, ".beads")
		if err := os.MkdirAll(canonicalBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		canonicalDBPath := filepath.Join(canonicalBeadsDir, "beads.db")
		sqliteStore, err := sqlite.New(context.Background(), canonicalDBPath)
		if err != nil {
			t.Fatalf("Failed to create canonical database: %v", err)
		}
		if err := sqliteStore.SetConfig(context.Background(), "issue_prefix", "existing"); err != nil {
			t.Fatalf("Failed to set prefix in canonical database: %v", err)
		}
		sqliteStore.Close()

		projectDir := filepath.Join(tmpDir, "project")
		projectBeadsDir := filepath.Join(projectDir, ".beads")
		if err := os.MkdirAll(projectBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		redirectPath := filepath.Join(projectBeadsDir, beads.RedirectFileName)
		if err := os.WriteFile(redirectPath, []byte("../canonical/.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Use os.Chdir since checkExistingBeadsData reads CWD directly
		origWd, _ := os.Getwd()
		if err := os.Chdir(projectDir); err != nil {
			t.Fatal(err)
		}
		defer os.Chdir(origWd)

		err = checkExistingBeadsData("new-prefix")
		if err == nil {
			t.Fatal("Expected checkExistingBeadsData to return error when redirect target already has database")
		}

		errorMsg := err.Error()
		if !strings.Contains(errorMsg, "redirect target already has database") {
			t.Errorf("Expected error about redirect target having database, got: %s", errorMsg)
		}

		reopened, err := openExistingTestDB(t, canonicalDBPath)
		if err != nil {
			t.Fatalf("Failed to reopen canonical database: %v", err)
		}
		defer reopened.Close()

		ctx := context.Background()
		prefix, err := reopened.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix from canonical database: %v", err)
		}
		if prefix != "existing" {
			t.Errorf("Canonical database prefix should still be 'existing', got %q (was overwritten!)", prefix)
		}
	})
}

// =============================================================================
// BEADS_DIR Tests
// =============================================================================
// These tests verify that bd init respects the BEADS_DIR environment variable
// for both safety checks and database creation.

// TestInitBEADS_DIR groups BEADS_DIR-related init tests.
// Tests requirements FR-001, FR-002, FR-004, NFR-001.
func TestInitBEADS_DIR(t *testing.T) {
	// resetBeadsDirState resets global state and env vars for each subtest.
	resetBeadsDirState := func(t *testing.T) {
		t.Helper()
		origDBPath := dbPath
		t.Cleanup(func() {
			dbPath = origDBPath
			beads.ResetCaches()
			git.ResetCaches()
		})
		dbPath = ""
		beads.ResetCaches()
		git.ResetCaches()
		initCmd.Flags().Set("prefix", "")
		initCmd.Flags().Set("quiet", "false")
		initCmd.Flags().Set("backend", "")
	}

	// checkExistingBeadsData tests (FR-001, FR-004)
	t.Run("CheckExisting_NoExistingDB", func(t *testing.T) {
		resetBeadsDirState(t)

		tmpDir := t.TempDir()
		beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
		os.MkdirAll(beadsDirPath, 0755)

		os.Setenv("BEADS_DIR", beadsDirPath)
		t.Cleanup(func() { os.Unsetenv("BEADS_DIR") })
		beads.ResetCaches()

		err := checkExistingBeadsData("test")
		if err != nil {
			t.Errorf("Expected no error when BEADS_DIR has no database, got: %v", err)
		}
	})

	t.Run("CheckExisting_CWDIgnoredWhenSet", func(t *testing.T) {
		resetBeadsDirState(t)

		tmpDir := t.TempDir()

		// Create CWD with existing database (should be ignored)
		cwdBeadsDir := filepath.Join(tmpDir, "cwd", ".beads")
		os.MkdirAll(cwdBeadsDir, 0755)
		cwdDBPath := filepath.Join(cwdBeadsDir, beads.CanonicalDatabaseName)
		store, err := sqlite.New(context.Background(), cwdDBPath)
		if err != nil {
			t.Fatal(err)
		}
		store.Close()

		// Create BEADS_DIR location (no database)
		beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
		os.MkdirAll(beadsDirPath, 0755)

		os.Setenv("BEADS_DIR", beadsDirPath)
		t.Cleanup(func() { os.Unsetenv("BEADS_DIR") })
		beads.ResetCaches()

		origWd, _ := os.Getwd()
		os.Chdir(filepath.Join(tmpDir, "cwd"))
		defer os.Chdir(origWd)

		err = checkExistingBeadsData("test")
		if err != nil {
			t.Errorf("Expected no error when BEADS_DIR has no database (CWD should be ignored), got: %v", err)
		}
	})

	t.Run("CheckExisting_ErrorWhenDBExists", func(t *testing.T) {
		resetBeadsDirState(t)

		tmpDir := t.TempDir()

		beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
		os.MkdirAll(beadsDirPath, 0755)
		testDBPath := filepath.Join(beadsDirPath, beads.CanonicalDatabaseName)
		store, err := sqlite.New(context.Background(), testDBPath)
		if err != nil {
			t.Fatal(err)
		}
		store.Close()

		os.Setenv("BEADS_DIR", beadsDirPath)
		t.Cleanup(func() { os.Unsetenv("BEADS_DIR") })
		beads.ResetCaches()

		err = checkExistingBeadsData("test")
		if err == nil {
			t.Error("Expected error when BEADS_DIR already has database")
		}
		if !strings.Contains(err.Error(), beadsDirPath) {
			t.Errorf("Expected error to mention BEADS_DIR path %s, got: %v", beadsDirPath, err)
		}
	})

	// FR-002: init creates database at BEADS_DIR
	t.Run("InitCreatesDBAtBeadsDir", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Skipping BEADS_DIR test on Windows")
		}

		resetBeadsDirState(t)

		tmpDir := t.TempDir()

		beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
		os.MkdirAll(filepath.Dir(beadsDirPath), 0755)

		os.Setenv("BEADS_DIR", beadsDirPath)
		t.Cleanup(func() { os.Unsetenv("BEADS_DIR") })
		beads.ResetCaches()
		git.ResetCaches()

		cwdPath := filepath.Join(tmpDir, "workdir")
		os.MkdirAll(cwdPath, 0755)
		t.Chdir(cwdPath)

		rootCmd.SetArgs([]string{"init", "--prefix", "beadsdir-test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DIR failed: %v", err)
		}

		expectedDBPath := filepath.Join(beadsDirPath, beads.CanonicalDatabaseName)
		if _, err := os.Stat(expectedDBPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at BEADS_DIR path: %s", expectedDBPath)
		}

		cwdDBPath := filepath.Join(cwdPath, ".beads", beads.CanonicalDatabaseName)
		if _, err := os.Stat(cwdDBPath); err == nil {
			t.Errorf("Database should NOT have been created at CWD: %s", cwdDBPath)
		}

		store, err := openExistingTestDB(t, expectedDBPath)
		if err != nil {
			t.Fatalf("Failed to open database at BEADS_DIR: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix from database: %v", err)
		}
		if prefix != "beadsdir-test" {
			t.Errorf("Expected prefix 'beadsdir-test', got %q", prefix)
		}
	})

	// NFR-001: existing behavior unchanged when BEADS_DIR not set
	t.Run("WithoutBeadsDirNoBehaviorChange", func(t *testing.T) {
		resetBeadsDirState(t)

		os.Unsetenv("BEADS_DIR")
		beads.ResetCaches()
		git.ResetCaches()

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		rootCmd.SetArgs([]string{"init", "--prefix", "no-beadsdir", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init without BEADS_DIR failed: %v", err)
		}

		expectedDBPath := filepath.Join(tmpDir, ".beads", beads.CanonicalDatabaseName)
		if _, err := os.Stat(expectedDBPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at default CWD/.beads path: %s", expectedDBPath)
		}

		store, err := openExistingTestDB(t, expectedDBPath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix from database: %v", err)
		}
		if prefix != "no-beadsdir" {
			t.Errorf("Expected prefix 'no-beadsdir', got %q", prefix)
		}
	})

	// Precedence: BEADS_DB > BEADS_DIR
	t.Run("BEADS_DB_OverridesBeadsDir", func(t *testing.T) {
		resetBeadsDirState(t)

		beadsDirTarget := t.TempDir()
		beadsDBTarget := t.TempDir()

		beadsDirBeads := filepath.Join(beadsDirTarget, ".beads")
		if err := os.MkdirAll(beadsDirBeads, 0750); err != nil {
			t.Fatal(err)
		}

		beadsDBPath := filepath.Join(beadsDBTarget, "override.db")

		t.Setenv("BEADS_DIR", beadsDirBeads)
		t.Setenv("BEADS_DB", beadsDBPath)

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		rootCmd.SetArgs([]string{"init", "--prefix", "precedence", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DB + BEADS_DIR failed: %v", err)
		}

		if _, err := os.Stat(beadsDBPath); os.IsNotExist(err) {
			t.Errorf("Database was NOT created at BEADS_DB path: %s", beadsDBPath)
		}

		beadsDirDBPath := filepath.Join(beadsDirBeads, beads.CanonicalDatabaseName)
		if _, err := os.Stat(beadsDirDBPath); err == nil {
			t.Errorf("Database was incorrectly created at BEADS_DIR path: %s (BEADS_DB should override)", beadsDirDBPath)
		}

		store, err := openExistingTestDB(t, beadsDBPath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix from database: %v", err)
		}
		if prefix != "precedence" {
			t.Errorf("Expected prefix 'precedence', got %q", prefix)
		}
	})
}

// TestInit_WithBEADS_DIR_DoltBackend verifies that bd init with Dolt backend
// creates the database at BEADS_DIR when the environment variable is set.
// This tests requirements FR-002 for Dolt backend.
func TestInit_WithBEADS_DIR_DoltBackend(t *testing.T) {
	// Skip on Windows
	if runtime.GOOS == "windows" {
		t.Skip("Skipping BEADS_DIR Dolt test on Windows")
	}

	// Check if dolt is available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt not installed, skipping Dolt backend test")
	}

	// Reset global state
	origDBPath := dbPath
	defer func() { dbPath = origDBPath }()
	dbPath = ""

	// Save and restore BEADS_DIR
	origBeadsDir := os.Getenv("BEADS_DIR")
	defer func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		beads.ResetCaches()
		git.ResetCaches()
	}()

	// Reset Cobra flags
	initCmd.Flags().Set("prefix", "")
	initCmd.Flags().Set("quiet", "false")
	initCmd.Flags().Set("backend", "")

	tmpDir := t.TempDir()

	// Create external BEADS_DIR location
	beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
	os.MkdirAll(filepath.Dir(beadsDirPath), 0755)

	os.Setenv("BEADS_DIR", beadsDirPath)
	beads.ResetCaches()
	git.ResetCaches()

	// Change to a different working directory
	cwdPath := filepath.Join(tmpDir, "workdir")
	os.MkdirAll(cwdPath, 0755)
	t.Chdir(cwdPath)

	// Run bd init with Dolt backend
	rootCmd.SetArgs([]string{"init", "--prefix", "dolt-test", "--backend", "dolt", "--quiet"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Init with BEADS_DIR and Dolt backend failed: %v", err)
	}

	// Verify Dolt database was created at BEADS_DIR
	expectedDoltPath := filepath.Join(beadsDirPath, "dolt")
	if info, err := os.Stat(expectedDoltPath); os.IsNotExist(err) {
		t.Errorf("Dolt database was not created at BEADS_DIR path: %s", expectedDoltPath)
	} else if !info.IsDir() {
		t.Errorf("Expected Dolt path to be a directory: %s", expectedDoltPath)
	}

	// Verify database was NOT created at CWD
	cwdDoltPath := filepath.Join(cwdPath, ".beads", "dolt")
	if _, err := os.Stat(cwdDoltPath); err == nil {
		t.Errorf("Dolt database should NOT have been created at CWD: %s", cwdDoltPath)
	}
}

// Note: TestInit_WithoutBEADS_DIR_NoBehaviorChange and TestInit_BEADS_DB_OverridesBEADS_DIR
// are now subtests of TestInitBEADS_DIR above.
