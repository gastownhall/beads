package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestViperSourceLabel verifies source label formatting for different config sources.
func TestViperSourceLabel(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		source config.ConfigSource
		want   string
	}{
		{
			name:   "default source",
			key:    "backup.enabled",
			source: config.SourceDefault,
			want:   "default",
		},
		{
			name:   "config file source",
			key:    "git-remote",
			source: config.SourceConfigFile,
			want:   "config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := viperSourceLabel(tt.key, tt.source)
			if got != tt.want {
				t.Errorf("viperSourceLabel(%q, %v) = %q, want %q", tt.key, tt.source, got, tt.want)
			}
		})
	}
}

// TestViperSourceLabelEnvVar verifies env var source includes the variable name.
func TestViperSourceLabelEnvVar(t *testing.T) {
	t.Setenv("BD_ACTOR", "test-bot")

	got := viperSourceLabel("actor", config.SourceEnvVar)
	if got != "env: BD_ACTOR" {
		t.Errorf("viperSourceLabel with env var = %q, want %q", got, "env: BD_ACTOR")
	}
}

// TestIsContainerKey verifies container key detection.
func TestIsContainerKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"directory.labels", true},
		{"external_projects", true},
		{"repos", true},
		{"actor", false},
		{"backup.enabled", false},
		{"directory.labels.frontend", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := isContainerKey(tt.key); got != tt.want {
				t.Errorf("isContainerKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// TestFilterBySource verifies source filtering logic.
func TestFilterBySource(t *testing.T) {
	entries := []configEntry{
		{Key: "a", Value: "1", Source: "config.yaml"},
		{Key: "b", Value: "2", Source: "default"},
		{Key: "c", Value: "3", Source: "database"},
		{Key: "d", Value: "4", Source: "env: BD_ACTOR"},
		{Key: "e", Value: "5", Source: "config.yaml"},
		{Key: "f", Value: "6", Source: "metadata"},
		{Key: "g", Value: "7", Source: "git"},
	}

	tests := []struct {
		source string
		want   int
	}{
		{"config.yaml", 2},
		{"default", 1},
		{"database", 1},
		{"env", 1},
		{"metadata", 1},
		{"git", 1},
		{"nonexistent", 0},
	}

	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			filtered := filterBySource(entries, tt.source)
			if len(filtered) != tt.want {
				t.Errorf("filterBySource(%q) returned %d entries, want %d", tt.source, len(filtered), tt.want)
			}
		})
	}
}

// TestCollectMetadataEntries verifies metadata.json field collection.
func TestCollectMetadataEntries(t *testing.T) {
	// Create a temp directory with a metadata.json
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	metadataJSON := `{
  "database": "beads.db",
  "dolt_mode": "embedded",
  "project_id": "abc-123",
  "dolt_server_port": 3307,
  "dolt_server_tls": true
}`
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(metadataJSON), 0600); err != nil {
		t.Fatalf("Failed to write metadata.json: %v", err)
	}

	// Change to temp dir so FindBeadsDir can find it
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	entries := collectMetadataEntries()

	// Verify expected entries exist
	entryMap := make(map[string]configEntry)
	for _, e := range entries {
		entryMap[e.Key] = e
	}

	if e, ok := entryMap["dolt_mode"]; !ok {
		t.Error("expected dolt_mode in metadata entries")
	} else if e.Value != "embedded" {
		t.Errorf("dolt_mode = %q, want %q", e.Value, "embedded")
	} else if e.Source != "metadata" {
		t.Errorf("dolt_mode source = %q, want %q", e.Source, "metadata")
	}

	if e, ok := entryMap["project_id"]; !ok {
		t.Error("expected project_id in metadata entries")
	} else if e.Value != "abc-123" {
		t.Errorf("project_id = %q, want %q", e.Value, "abc-123")
	}

	if e, ok := entryMap["dolt_server_port"]; !ok {
		t.Error("expected dolt_server_port in metadata entries")
	} else if e.Value != "3307" {
		t.Errorf("dolt_server_port = %q, want %q", e.Value, "3307")
	}

	if e, ok := entryMap["dolt_server_tls"]; !ok {
		t.Error("expected dolt_server_tls in metadata entries")
	} else if e.Value != "true" {
		t.Errorf("dolt_server_tls = %q, want %q", e.Value, "true")
	}
}

// TestCollectMetadataEntriesNoBeadsDir verifies graceful handling when no .beads exists.
func TestCollectMetadataEntriesNoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	entries := collectMetadataEntries()
	if len(entries) != 0 {
		t.Errorf("expected no metadata entries without .beads dir, got %d", len(entries))
	}
}

// TestCollectViperEntries verifies that Viper key collection works with initialized config.
func TestCollectViperEntries(t *testing.T) {
	// Create a temp dir with config.yaml
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("git-remote: \"https://example.com/repo\"\n"), 0600); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	// Re-initialize config to pick up our test config.yaml
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize() failed: %v", err)
	}
	defer func() {
		config.ResetForTesting()
		// Re-initialize with original config
		os.Chdir(origDir) //nolint:errcheck
		_ = config.Initialize()
	}()

	entries := collectViperEntries()

	// Check that we got some entries
	if len(entries) == 0 {
		t.Fatal("expected at least some Viper entries")
	}

	// Check git-remote is present with config.yaml source
	found := false
	for _, e := range entries {
		if e.Key == "git-remote" {
			found = true
			if e.Value != "https://example.com/repo" {
				t.Errorf("git-remote value = %q, want %q", e.Value, "https://example.com/repo")
			}
			if e.Source != "config.yaml" {
				t.Errorf("git-remote source = %q, want %q", e.Source, "config.yaml")
			}
			break
		}
	}
	if !found {
		t.Error("expected git-remote key in Viper entries")
	}

	// Verify defaults with non-empty values are included
	foundDefault := false
	for _, e := range entries {
		if e.Source == "default" && e.Value != "" {
			foundDefault = true
			break
		}
	}
	if !foundDefault {
		t.Error("expected at least one default entry with a non-empty value")
	}

	// Verify empty defaults are excluded
	for _, e := range entries {
		if e.Source == "default" && e.Value == "" {
			t.Errorf("empty default %q should be excluded", e.Key)
		}
	}
}

// TestCollectViperEntriesWithEnvOverride verifies env var source detection.
func TestCollectViperEntriesWithEnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("actor: from-config\n"), 0600); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	t.Setenv("BD_ACTOR", "env-bot")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize() failed: %v", err)
	}
	defer func() {
		config.ResetForTesting()
		os.Chdir(origDir) //nolint:errcheck
		_ = config.Initialize()
	}()

	entries := collectViperEntries()

	for _, e := range entries {
		if e.Key == "actor" {
			if e.Source != "env: BD_ACTOR" {
				t.Errorf("actor source = %q, want %q", e.Source, "env: BD_ACTOR")
			}
			return
		}
	}
	t.Error("expected actor key in Viper entries")
}
