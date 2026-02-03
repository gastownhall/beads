package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// TestDaemonAutoSyncFromYAML verifies that daemon.auto-sync is read from config.yaml.
func TestDaemonAutoSyncFromYAML(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create config.yaml with daemon.auto-sync: true
	configYAML := `daemon:
  auto-sync: true
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Create database without daemon settings
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	// Change to temp directory and reinitialize config
	t.Chdir(tmpDir)
	initConfigForTest(t)

	// Create a mock cobra command
	cmd := &cobra.Command{}
	cmd.Flags().Bool("auto-commit", false, "")
	cmd.Flags().Bool("auto-push", false, "")
	cmd.Flags().Bool("auto-pull", false, "")

	// Test loadDaemonAutoSettings
	autoCommit, autoPush, autoPull := loadDaemonAutoSettings(cmd, false, false, false)

	// auto-sync: true should enable all three
	if !autoCommit {
		t.Errorf("Expected autoCommit=true when daemon.auto-sync=true in YAML, got false")
	}
	if !autoPush {
		t.Errorf("Expected autoPush=true when daemon.auto-sync=true in YAML, got false")
	}
	if !autoPull {
		t.Errorf("Expected autoPull=true when daemon.auto-sync=true in YAML, got false")
	}
}

// TestDaemonAutoCommitOnlyFromYAML verifies that individual daemon.auto-commit works
// without enabling auto-push and auto-pull.
func TestDaemonAutoCommitOnlyFromYAML(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create config.yaml with only daemon.auto-commit: true
	configYAML := `daemon:
  auto-commit: true
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Create database without daemon settings
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	// Change to temp directory and reinitialize config
	t.Chdir(tmpDir)
	initConfigForTest(t)

	// Create a mock cobra command
	cmd := &cobra.Command{}
	cmd.Flags().Bool("auto-commit", false, "")
	cmd.Flags().Bool("auto-push", false, "")
	cmd.Flags().Bool("auto-pull", false, "")

	// Test loadDaemonAutoSettings
	autoCommit, autoPush, autoPull := loadDaemonAutoSettings(cmd, false, false, false)

	// Individual YAML settings should NOT enable other options
	if !autoCommit {
		t.Errorf("Expected autoCommit=true when daemon.auto-commit=true in YAML, got false")
	}
	if autoPush {
		t.Errorf("Expected autoPush=false when only daemon.auto-commit is set in YAML, got true")
	}
	if autoPull {
		t.Errorf("Expected autoPull=false when only daemon.auto-commit is set in YAML, got true")
	}
}

// TestDaemonIndividualSettingsFromYAML verifies that individual settings can be
// set independently via YAML config.
func TestDaemonIndividualSettingsFromYAML(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create config.yaml with mixed individual settings
	configYAML := `daemon:
  auto-commit: true
  auto-push: false
  auto-pull: true
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Create database without daemon settings
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	// Change to temp directory and reinitialize config
	t.Chdir(tmpDir)
	initConfigForTest(t)

	// Create a mock cobra command
	cmd := &cobra.Command{}
	cmd.Flags().Bool("auto-commit", false, "")
	cmd.Flags().Bool("auto-push", false, "")
	cmd.Flags().Bool("auto-pull", false, "")

	// Test loadDaemonAutoSettings
	autoCommit, autoPush, autoPull := loadDaemonAutoSettings(cmd, false, false, false)

	if !autoCommit {
		t.Errorf("Expected autoCommit=true, got false")
	}
	if autoPush {
		t.Errorf("Expected autoPush=false, got true")
	}
	if !autoPull {
		t.Errorf("Expected autoPull=true, got false")
	}
}

// TestDaemonEnvVarOverridesYAML verifies that env vars take precedence over YAML config.
func TestDaemonEnvVarOverridesYAML(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create config.yaml with daemon.auto-sync: true
	configYAML := `daemon:
  auto-sync: true
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Create database
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	// Change to temp directory and reinitialize config
	t.Chdir(tmpDir)
	initConfigForTest(t)

	// Set env var to override YAML (env var takes precedence)
	t.Setenv("BEADS_AUTO_SYNC", "false")

	// Create a mock cobra command
	cmd := &cobra.Command{}
	cmd.Flags().Bool("auto-commit", false, "")
	cmd.Flags().Bool("auto-push", false, "")
	cmd.Flags().Bool("auto-pull", false, "")

	// Test loadDaemonAutoSettings
	autoCommit, autoPush, _ := loadDaemonAutoSettings(cmd, false, false, false)

	// Env var BEADS_AUTO_SYNC=false should override YAML auto-sync: true
	if autoCommit {
		t.Errorf("Expected autoCommit=false (env var override), got true")
	}
	if autoPush {
		t.Errorf("Expected autoPush=false (env var override), got true")
	}
	// auto-pull defaults based on other factors when auto-sync=false
}

// TestDaemonCLIFlagOverridesYAML verifies that CLI flags take precedence over YAML config.
func TestDaemonCLIFlagOverridesYAML(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create config.yaml with daemon settings
	configYAML := `daemon:
  auto-commit: true
  auto-push: true
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Create database
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	// Change to temp directory and reinitialize config
	t.Chdir(tmpDir)
	initConfigForTest(t)

	// Create a mock cobra command with flags explicitly set
	cmd := &cobra.Command{}
	cmd.Flags().Bool("auto-commit", false, "")
	cmd.Flags().Bool("auto-push", false, "")
	cmd.Flags().Bool("auto-pull", false, "")

	// Simulate CLI flag being explicitly set
	_ = cmd.Flags().Set("auto-commit", "false")

	// Test loadDaemonAutoSettings - CLI flag should override YAML config
	autoCommit, autoPush, autoPull := loadDaemonAutoSettings(cmd, false, false, false)

	// CLI flag --auto-commit=false should override YAML auto-commit: true
	if autoCommit {
		t.Errorf("Expected autoCommit=false (CLI flag override), got true")
	}
	// auto-push should still come from YAML since flag wasn't changed
	if !autoPush {
		t.Errorf("Expected autoPush=true (from YAML), got false")
	}
	if autoPull {
		t.Errorf("Expected autoPull=false (not set), got true")
	}
}

// TestDaemonIndividualEnvVarOverridesYAML verifies that individual env vars
// (BEADS_AUTO_PULL) override individual YAML settings.
func TestDaemonIndividualEnvVarOverridesYAML(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create config.yaml with auto-commit: true and auto-pull: false
	configYAML := `daemon:
  auto-commit: true
  auto-pull: false
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Create database
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	// Change to temp directory and reinitialize config
	t.Chdir(tmpDir)
	initConfigForTest(t)

	// Set individual env var to override YAML (BEADS_AUTO_PULL should override daemon.auto-pull)
	t.Setenv("BEADS_AUTO_PULL", "true")

	// Create a mock cobra command
	cmd := &cobra.Command{}
	cmd.Flags().Bool("auto-commit", false, "")
	cmd.Flags().Bool("auto-push", false, "")
	cmd.Flags().Bool("auto-pull", false, "")

	// Test loadDaemonAutoSettings
	autoCommit, autoPush, autoPull := loadDaemonAutoSettings(cmd, false, false, false)

	// auto-commit should come from YAML (true)
	if !autoCommit {
		t.Errorf("Expected autoCommit=true (from YAML), got false")
	}
	// auto-push should be false (not set anywhere)
	if autoPush {
		t.Errorf("Expected autoPush=false (not set), got true")
	}
	// auto-pull should be true (env var BEADS_AUTO_PULL=true overrides YAML auto-pull: false)
	if !autoPull {
		t.Errorf("Expected autoPull=true (env var override), got false")
	}
}
