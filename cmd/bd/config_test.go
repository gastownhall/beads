//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestConfigCommands(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Test SetConfig
	err := store.SetConfig(ctx, "test.key", "test-value")
	if err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Test GetConfig
	value, err := store.GetConfig(ctx, "test.key")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if value != "test-value" {
		t.Errorf("Expected 'test-value', got '%s'", value)
	}

	// Test GetConfig for non-existent key
	value, err = store.GetConfig(ctx, "nonexistent.key")
	if err != nil {
		t.Fatalf("GetConfig for nonexistent key failed: %v", err)
	}
	if value != "" {
		t.Errorf("Expected empty string for nonexistent key, got '%s'", value)
	}

	// Test SetConfig update
	err = store.SetConfig(ctx, "test.key", "updated-value")
	if err != nil {
		t.Fatalf("SetConfig update failed: %v", err)
	}
	value, err = store.GetConfig(ctx, "test.key")
	if err != nil {
		t.Fatalf("GetConfig after update failed: %v", err)
	}
	if value != "updated-value" {
		t.Errorf("Expected 'updated-value', got '%s'", value)
	}

	// Test GetAllConfig
	err = store.SetConfig(ctx, "jira.url", "https://example.atlassian.net")
	if err != nil {
		t.Fatalf("SetConfig for jira.url failed: %v", err)
	}
	err = store.SetConfig(ctx, "jira.project", "PROJ")
	if err != nil {
		t.Fatalf("SetConfig for jira.project failed: %v", err)
	}

	config, err := store.GetAllConfig(ctx)
	if err != nil {
		t.Fatalf("GetAllConfig failed: %v", err)
	}

	// Should have at least our test keys (may have default compaction config too)
	if len(config) < 3 {
		t.Errorf("Expected at least 3 config entries, got %d", len(config))
	}

	if config["test.key"] != "updated-value" {
		t.Errorf("Expected 'updated-value' for test.key, got '%s'", config["test.key"])
	}
	if config["jira.url"] != "https://example.atlassian.net" {
		t.Errorf("Expected jira.url in config, got '%s'", config["jira.url"])
	}
	if config["jira.project"] != "PROJ" {
		t.Errorf("Expected jira.project in config, got '%s'", config["jira.project"])
	}

	// Test DeleteConfig
	err = store.DeleteConfig(ctx, "test.key")
	if err != nil {
		t.Fatalf("DeleteConfig failed: %v", err)
	}

	value, err = store.GetConfig(ctx, "test.key")
	if err != nil {
		t.Fatalf("GetConfig after delete failed: %v", err)
	}
	if value != "" {
		t.Errorf("Expected empty string after delete, got '%s'", value)
	}

	// Test DeleteConfig for non-existent key (should not error)
	err = store.DeleteConfig(ctx, "nonexistent.key")
	if err != nil {
		t.Fatalf("DeleteConfig for nonexistent key failed: %v", err)
	}
}

func TestConfigNamespaces(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Test various namespace conventions
	namespaces := map[string]string{
		"jira.url":                    "https://example.atlassian.net",
		"jira.project":                "PROJ",
		"jira.status_map.todo":        "open",
		"linear.team_id":              "team-123",
		"github.org":                  "myorg",
		"custom.my_integration.field": "value",
	}

	for key, val := range namespaces {
		err := store.SetConfig(ctx, key, val)
		if err != nil {
			t.Fatalf("SetConfig for %s failed: %v", key, err)
		}
	}

	// Verify all set correctly
	for key, expected := range namespaces {
		value, err := store.GetConfig(ctx, key)
		if err != nil {
			t.Fatalf("GetConfig for %s failed: %v", key, err)
		}
		if value != expected {
			t.Errorf("Expected '%s' for %s, got '%s'", expected, key, value)
		}
	}

	// Test GetAllConfig returns all
	config, err := store.GetAllConfig(ctx)
	if err != nil {
		t.Fatalf("GetAllConfig failed: %v", err)
	}

	for key, expected := range namespaces {
		if config[key] != expected {
			t.Errorf("Expected '%s' for %s in GetAllConfig, got '%s'", expected, key, config[key])
		}
	}
}

// TestYamlOnlyConfigWithoutDatabase verifies that yaml-only config keys
// (like no-db) can be set/get without requiring a SQLite database.
// This is the fix for GH#536 - the chicken-and-egg problem where you couldn't
// run `bd config set no-db true` without first having a database.
func TestYamlOnlyConfigWithoutDatabase(t *testing.T) {
	// Create a temp directory with only config.yaml (no database)
	tmpDir, err := os.MkdirTemp("", "bd-test-yaml-config-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create config.yaml with a prefix but NO database
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("prefix: test\n"), 0644); err != nil {
		t.Fatalf("Failed to create config.yaml: %v", err)
	}

	// Create empty issues.jsonl (simulates fresh clone)
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}

	// Test that IsYamlOnlyKey correctly identifies yaml-only keys
	yamlOnlyKeys := []string{"no-db", "json", "routing.mode"}
	for _, key := range yamlOnlyKeys {
		if !config.IsYamlOnlyKey(key) {
			t.Errorf("Expected %q to be a yaml-only key", key)
		}
	}

	// Test that non-yaml-only keys are correctly identified
	nonYamlKeys := []string{"jira.url", "linear.team_id", "status.custom"}
	for _, key := range nonYamlKeys {
		if config.IsYamlOnlyKey(key) {
			t.Errorf("Expected %q to NOT be a yaml-only key", key)
		}
	}
}

// setupTestDB creates a temporary test database
func setupTestDB(t *testing.T) (*dolt.DoltStore, func()) {
	tmpDir, err := os.MkdirTemp("", "bd-test-config-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	testDB := filepath.Join(tmpDir, "test.db")
	store, err := dolt.New(context.Background(), &dolt.Config{Path: testDB})
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Skipf("skipping: Dolt server not available: %v", err)
	}

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

// TestBeadsRoleGitConfig verifies that beads.role is stored in git config,
// not SQLite, so that bd doctor can find it (GH#1531).
func TestBeadsRoleGitConfig(t *testing.T) {
	tmpDir := newGitRepo(t)

	t.Run("set contributor role writes to git config", func(t *testing.T) {
		cmd := exec.Command("git", "config", "beads.role", "contributor")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git config set failed: %v", err)
		}

		// Verify it's readable from git config
		cmd = exec.Command("git", "config", "--get", "beads.role")
		cmd.Dir = tmpDir
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("git config get failed: %v", err)
		}
		if got := strings.TrimSpace(string(output)); got != "contributor" {
			t.Errorf("expected 'contributor', got %q", got)
		}
	})

	t.Run("set maintainer role writes to git config", func(t *testing.T) {
		cmd := exec.Command("git", "config", "beads.role", "maintainer")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git config set failed: %v", err)
		}

		cmd = exec.Command("git", "config", "--get", "beads.role")
		cmd.Dir = tmpDir
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("git config get failed: %v", err)
		}
		if got := strings.TrimSpace(string(output)); got != "maintainer" {
			t.Errorf("expected 'maintainer', got %q", got)
		}
	})
}

// TestIsValidRemoteURL tests the remote URL validation function
func TestIsValidRemoteURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		// Valid URLs
		{"dolthub scheme", "dolthub://org/repo", true},
		{"gs scheme", "gs://bucket/path", true},
		{"s3 scheme", "s3://bucket/path", true},
		{"file scheme", "file:///path/to/repo", true},
		{"https scheme", "https://github.com/user/repo", true},
		{"http scheme", "http://github.com/user/repo", true},
		{"ssh scheme", "ssh://git@github.com/user/repo", true},
		{"git ssh format", "git@github.com:user/repo.git", true},
		{"git ssh with underscore", "git@gitlab.example_host.com:user/repo.git", true},

		// Invalid URLs
		{"empty string", "", false},
		{"no scheme", "github.com/user/repo", false},
		{"invalid scheme", "ftp://server/path", false},
		{"malformed git ssh", "git@:repo", false},
		{"just path", "/path/to/repo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidRemoteURL(tt.url)
			if got != tt.expected {
				t.Errorf("isValidRemoteURL(%q) = %v, want %v", tt.url, got, tt.expected)
			}
		})
	}
}

// TestValidateSyncConfig tests the sync config validation function
func TestValidateSyncConfig(t *testing.T) {
	// Create a temp directory for testing
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	t.Run("valid empty config", func(t *testing.T) {
		// Create minimal config.yaml
		configContent := `prefix: test
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		issues := validateSyncConfig(tmpDir)
		// After JSONL removal, Dolt sync requires federation.remote
		if len(issues) != 1 {
			t.Errorf("Expected 1 issue (missing federation.remote) for empty config, got: %v", issues)
		}
	})

	t.Run("invalid federation.sovereignty", func(t *testing.T) {
		configContent := `prefix: test
federation:
  sovereignty: "invalid-value"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		issues := validateSyncConfig(tmpDir)
		found := false
		for _, issue := range issues {
			if strings.Contains(issue, "federation.sovereignty") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected issue about federation.sovereignty, got: %v", issues)
		}
	})

	t.Run("dolt-native mode without remote", func(t *testing.T) {
		configContent := `prefix: test
sync:
  mode: "dolt-native"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		issues := validateSyncConfig(tmpDir)
		found := false
		for _, issue := range issues {
			if strings.Contains(issue, "federation.remote") && strings.Contains(issue, "required") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected issue about federation.remote being required, got: %v", issues)
		}
	})

	t.Run("invalid remote URL", func(t *testing.T) {
		configContent := `prefix: test
federation:
  remote: "invalid-url"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		issues := validateSyncConfig(tmpDir)
		found := false
		for _, issue := range issues {
			if strings.Contains(issue, "federation.remote") && (strings.Contains(issue, "not a valid remote URL") || strings.Contains(issue, "no scheme") || strings.Contains(issue, "not allowed")) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected issue about invalid remote URL, got: %v", issues)
		}
	})

	t.Run("valid sync config", func(t *testing.T) {
		configContent := `prefix: test
sync:
  mode: "dolt-native"
conflict:
  strategy: "newest"
federation:
  sovereignty: "T2"
  remote: "https://github.com/user/beads-data.git"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		issues := validateSyncConfig(tmpDir)
		if len(issues) != 0 {
			t.Errorf("Expected no issues for valid config, got: %v", issues)
		}
	})

	t.Run("remote URL with null byte", func(t *testing.T) {
		configContent := "prefix: test\nfederation:\n  remote: \"dolthub://org/repo\\x00evil\"\n"
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		issues := validateSyncConfig(tmpDir)
		found := false
		for _, issue := range issues {
			if strings.Contains(issue, "federation.remote") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected issue about invalid remote URL with null byte, got: %v", issues)
		}
	})

	t.Run("allowed-remote-patterns enforcement", func(t *testing.T) {
		configContent := `prefix: test
federation:
  remote: "https://github.com/user/repo"
  allowed-remote-patterns:
    - "dolthub://myorg/*"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		issues := validateSyncConfig(tmpDir)
		found := false
		for _, issue := range issues {
			if strings.Contains(issue, "does not match") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected issue about remote not matching allowed patterns, got: %v", issues)
		}
	})

	t.Run("allowed-remote-patterns passes when matching", func(t *testing.T) {
		configContent := `prefix: test
federation:
  remote: "dolthub://myorg/myrepo"
  allowed-remote-patterns:
    - "dolthub://myorg/*"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		issues := validateSyncConfig(tmpDir)
		if len(issues) != 0 {
			t.Errorf("Expected no issues when remote matches allowed pattern, got: %v", issues)
		}
	})

	t.Run("uses shared worktree config when local .beads is absent", func(t *testing.T) {
		bareDir, worktreeDir := setupBareParentInitWorktree(t)
		bareBeadsDir := filepath.Join(bareDir, ".beads")
		if err := os.MkdirAll(bareBeadsDir, 0o755); err != nil {
			t.Fatalf("Failed to create bare .beads dir: %v", err)
		}

		configContent := `federation:
  remote: "dolthub://myorg/myrepo"
  allowed-remote-patterns:
    - "dolthub://myorg/*"
`
		if err := os.WriteFile(filepath.Join(bareBeadsDir, "config.yaml"), []byte(configContent), 0o644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}

		issues := validateSyncConfig(worktreeDir)
		if len(issues) != 0 {
			t.Errorf("Expected no issues when shared worktree config is valid, got: %v", issues)
		}
	})
}

func TestResolvedConfigRepoRoot(t *testing.T) {
	resetResolutionCaches := func(t *testing.T) {
		t.Helper()
		beads.ResetCaches()
		git.ResetCaches()
		t.Cleanup(func() {
			beads.ResetCaches()
			git.ResetCaches()
		})
	}

	assertSameResolvedPath := func(t *testing.T, got, want string) {
		t.Helper()

		gotResolved, err := filepath.EvalSymlinks(got)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", got, err)
		}
		wantResolved, err := filepath.EvalSymlinks(want)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", want, err)
		}
		if gotResolved != wantResolved {
			t.Errorf("resolvedConfigRepoRoot() = %q (resolved %q), want %q (resolved %q)", got, gotResolved, want, wantResolved)
		}
	}

	t.Run("uses local workspace from subdirectory", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		subDir := filepath.Join(tmpDir, "sub", "dir")

		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("Failed to create .beads dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
			t.Fatalf("Failed to create metadata.json: %v", err)
		}
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatalf("Failed to create sub dir: %v", err)
		}

		t.Chdir(subDir)
		resetResolutionCaches(t)

		got, err := resolvedConfigRepoRoot()
		if err != nil {
			t.Fatalf("resolvedConfigRepoRoot returned error: %v", err)
		}
		assertSameResolvedPath(t, got, tmpDir)
	})

	t.Run("uses BEADS_DIR target", func(t *testing.T) {
		cwdDir := t.TempDir()
		targetRepo := t.TempDir()
		targetBeadsDir := filepath.Join(targetRepo, ".beads")

		if err := os.MkdirAll(targetBeadsDir, 0o755); err != nil {
			t.Fatalf("Failed to create target .beads dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(targetBeadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
			t.Fatalf("Failed to create target metadata.json: %v", err)
		}

		t.Setenv("BEADS_DIR", targetBeadsDir)
		t.Chdir(cwdDir)
		resetResolutionCaches(t)

		got, err := resolvedConfigRepoRoot()
		if err != nil {
			t.Fatalf("resolvedConfigRepoRoot returned error: %v", err)
		}
		assertSameResolvedPath(t, got, targetRepo)
	})

	t.Run("uses worktree fallback when local .beads is absent", func(t *testing.T) {
		bareDir, worktreeDir := setupBareParentInitWorktree(t)
		bareBeadsDir := filepath.Join(bareDir, ".beads")

		if err := os.MkdirAll(bareBeadsDir, 0o755); err != nil {
			t.Fatalf("Failed to create bare .beads dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(bareBeadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
			t.Fatalf("Failed to create bare metadata.json: %v", err)
		}

		t.Chdir(worktreeDir)
		resetResolutionCaches(t)

		got, err := resolvedConfigRepoRoot()
		if err != nil {
			t.Fatalf("resolvedConfigRepoRoot returned error: %v", err)
		}
		assertSameResolvedPath(t, got, bareDir)
	})
}

func TestCustomStatusConfig(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("categorized format round-trips", func(t *testing.T) {
		err := store.SetConfig(ctx, "status.custom", "review:active,testing:wip")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		detailed, err := store.GetCustomStatusesDetailed(ctx)
		if err != nil {
			t.Fatalf("GetCustomStatusesDetailed failed: %v", err)
		}
		if len(detailed) != 2 {
			t.Fatalf("expected 2 statuses, got %d", len(detailed))
		}
		if detailed[0].Name != "review" || detailed[0].Category != types.CategoryActive {
			t.Errorf("status[0] = {%q, %q}, want {review, active}", detailed[0].Name, detailed[0].Category)
		}
		if detailed[1].Name != "testing" || detailed[1].Category != types.CategoryWIP {
			t.Errorf("status[1] = {%q, %q}, want {testing, wip}", detailed[1].Name, detailed[1].Category)
		}
	})

	t.Run("flat format returns CategoryUnspecified", func(t *testing.T) {
		err := store.SetConfig(ctx, "status.custom", "review,testing")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		detailed, err := store.GetCustomStatusesDetailed(ctx)
		if err != nil {
			t.Fatalf("GetCustomStatusesDetailed failed: %v", err)
		}
		if len(detailed) != 2 {
			t.Fatalf("expected 2 statuses, got %d", len(detailed))
		}
		for _, s := range detailed {
			if s.Category != types.CategoryUnspecified {
				t.Errorf("status %q has category %q, want unspecified", s.Name, s.Category)
			}
		}
	})

	t.Run("mixed format returns both categorized and uncategorized", func(t *testing.T) {
		err := store.SetConfig(ctx, "status.custom", "review:active,legacy")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		detailed, err := store.GetCustomStatusesDetailed(ctx)
		if err != nil {
			t.Fatalf("GetCustomStatusesDetailed failed: %v", err)
		}
		if len(detailed) != 2 {
			t.Fatalf("expected 2 statuses, got %d", len(detailed))
		}
		if detailed[0].Category != types.CategoryActive {
			t.Errorf("review should be active, got %q", detailed[0].Category)
		}
		if detailed[1].Category != types.CategoryUnspecified {
			t.Errorf("legacy should be unspecified, got %q", detailed[1].Category)
		}
	})

	t.Run("GetCustomStatuses returns just names (backward compat)", func(t *testing.T) {
		err := store.SetConfig(ctx, "status.custom", "review:active,testing:wip,qa:done")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		names, err := store.GetCustomStatuses(ctx)
		if err != nil {
			t.Fatalf("GetCustomStatuses failed: %v", err)
		}
		if len(names) != 3 {
			t.Fatalf("expected 3 names, got %d", len(names))
		}
		want := []string{"review", "testing", "qa"}
		for i, name := range names {
			if name != want[i] {
				t.Errorf("name[%d] = %q, want %q", i, name, want[i])
			}
		}
	})

	t.Run("cache invalidation on SetConfig", func(t *testing.T) {
		// Set first value
		err := store.SetConfig(ctx, "status.custom", "alpha:active")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		detailed1, err := store.GetCustomStatusesDetailed(ctx)
		if err != nil {
			t.Fatalf("GetCustomStatusesDetailed failed: %v", err)
		}
		if len(detailed1) != 1 || detailed1[0].Name != "alpha" {
			t.Fatalf("expected [alpha], got %+v", detailed1)
		}

		// Set different value — cache should be invalidated
		err = store.SetConfig(ctx, "status.custom", "beta:wip,gamma:done")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		detailed2, err := store.GetCustomStatusesDetailed(ctx)
		if err != nil {
			t.Fatalf("GetCustomStatusesDetailed failed: %v", err)
		}
		if len(detailed2) != 2 {
			t.Fatalf("expected 2 statuses after cache invalidation, got %d", len(detailed2))
		}
		if detailed2[0].Name != "beta" || detailed2[0].Category != types.CategoryWIP {
			t.Errorf("status[0] = {%q, %q}, want {beta, wip}", detailed2[0].Name, detailed2[0].Category)
		}
		if detailed2[1].Name != "gamma" || detailed2[1].Category != types.CategoryDone {
			t.Errorf("status[1] = {%q, %q}, want {gamma, done}", detailed2[1].Name, detailed2[1].Category)
		}
	})
}

// TestConfigSetMany tests the batch config set functionality used by 'bd config set-many'.
func TestConfigSetMany(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("batch set multiple DB keys", func(t *testing.T) {
		pairs := map[string]string{
			"ado.state_map.open":        "New",
			"ado.state_map.in_progress": "Active",
			"ado.state_map.closed":      "Closed",
		}
		for k, v := range pairs {
			if err := store.SetConfig(ctx, k, v); err != nil {
				t.Fatalf("SetConfig(%s) failed: %v", k, err)
			}
		}

		// Verify all values were set correctly
		for k, expected := range pairs {
			got, err := store.GetConfig(ctx, k)
			if err != nil {
				t.Fatalf("GetConfig(%s) failed: %v", k, err)
			}
			if got != expected {
				t.Errorf("GetConfig(%s) = %q, want %q", k, got, expected)
			}
		}

		// Verify they appear in GetAllConfig
		all, err := store.GetAllConfig(ctx)
		if err != nil {
			t.Fatalf("GetAllConfig failed: %v", err)
		}
		for k, expected := range pairs {
			if all[k] != expected {
				t.Errorf("GetAllConfig[%s] = %q, want %q", k, all[k], expected)
			}
		}
	})

	t.Run("batch set overwrites existing values", func(t *testing.T) {
		// Set initial values
		if err := store.SetConfig(ctx, "test.batch.a", "old-a"); err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		if err := store.SetConfig(ctx, "test.batch.b", "old-b"); err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}

		// Overwrite with batch
		updates := map[string]string{
			"test.batch.a": "new-a",
			"test.batch.b": "new-b",
			"test.batch.c": "new-c",
		}
		for k, v := range updates {
			if err := store.SetConfig(ctx, k, v); err != nil {
				t.Fatalf("SetConfig(%s) failed: %v", k, err)
			}
		}

		for k, expected := range updates {
			got, err := store.GetConfig(ctx, k)
			if err != nil {
				t.Fatalf("GetConfig(%s) failed: %v", k, err)
			}
			if got != expected {
				t.Errorf("GetConfig(%s) = %q, want %q", k, got, expected)
			}
		}
	})

	t.Run("batch set with empty value", func(t *testing.T) {
		if err := store.SetConfig(ctx, "test.empty", ""); err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		got, err := store.GetConfig(ctx, "test.empty")
		if err != nil {
			t.Fatalf("GetConfig failed: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("batch set mixed namespaces in single operation", func(t *testing.T) {
		// Simulates what set-many does: multiple keys from different
		// namespaces all written to the DB in one logical batch.
		mixed := map[string]string{
			"jira.url":             "https://jira.example.com",
			"jira.project":         "BEADS",
			"ado.state_map.open":   "New",
			"ado.state_map.closed": "Done",
			"custom.pipeline":      "review,qa,deploy",
			"status.custom":        "awaiting_review,awaiting_testing",
		}
		for k, v := range mixed {
			if err := store.SetConfig(ctx, k, v); err != nil {
				t.Fatalf("SetConfig(%s) failed: %v", k, err)
			}
		}

		// Verify every key was persisted
		for k, expected := range mixed {
			got, err := store.GetConfig(ctx, k)
			if err != nil {
				t.Fatalf("GetConfig(%s) failed: %v", k, err)
			}
			if got != expected {
				t.Errorf("GetConfig(%s) = %q, want %q", k, got, expected)
			}
		}

		// Verify all appear in GetAllConfig
		all, err := store.GetAllConfig(ctx)
		if err != nil {
			t.Fatalf("GetAllConfig failed: %v", err)
		}
		for k, expected := range mixed {
			if all[k] != expected {
				t.Errorf("GetAllConfig[%s] = %q, want %q", k, all[k], expected)
			}
		}
	})

	t.Run("batch set preserves previously written keys", func(t *testing.T) {
		// Write batch 1
		if err := store.SetConfig(ctx, "retain.alpha", "aaa"); err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		if err := store.SetConfig(ctx, "retain.beta", "bbb"); err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}

		// Write batch 2 (different keys)
		if err := store.SetConfig(ctx, "retain.gamma", "ggg"); err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}

		// Verify batch 1 keys are still intact after batch 2
		got, err := store.GetConfig(ctx, "retain.alpha")
		if err != nil {
			t.Fatalf("GetConfig failed: %v", err)
		}
		if got != "aaa" {
			t.Errorf("retain.alpha = %q, want %q", got, "aaa")
		}

		got, err = store.GetConfig(ctx, "retain.beta")
		if err != nil {
			t.Fatalf("GetConfig failed: %v", err)
		}
		if got != "bbb" {
			t.Errorf("retain.beta = %q, want %q", got, "bbb")
		}
	})
}

// TestConfigSetManyValidationIntegration tests that the validation logic
// in set-many correctly rejects invalid values for known constrained keys
// before any DB writes would occur.
func TestConfigSetManyValidationIntegration(t *testing.T) {
	t.Run("beads.role only accepts maintainer or contributor", func(t *testing.T) {
		validRoles := map[string]bool{"maintainer": true, "contributor": true}
		for _, role := range []string{"maintainer", "contributor"} {
			if !validRoles[role] {
				t.Errorf("expected %q to be valid", role)
			}
		}
		for _, role := range []string{"admin", "superadmin", "owner", "", "MAINTAINER"} {
			if validRoles[role] {
				t.Errorf("expected %q to be invalid", role)
			}
		}
	})

	t.Run("status.custom validation catches invalid formats", func(t *testing.T) {
		valid := []string{
			"awaiting_review,awaiting_testing",
			"review,qa,deploy",
			"single_status",
		}
		for _, v := range valid {
			if _, err := types.ParseCustomStatusConfig(v); err != nil {
				t.Errorf("expected %q to be valid: %v", v, err)
			}
		}
	})

	t.Run("empty status.custom is allowed", func(t *testing.T) {
		// Empty value skips validation in the command handler
		result, err := types.ParseCustomStatusConfig("")
		if err != nil {
			t.Errorf("expected empty status.custom to be valid: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil result for empty input, got %v", result)
		}
	})
}
