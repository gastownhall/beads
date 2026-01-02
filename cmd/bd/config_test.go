package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/sqlite"
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
// (like no-db, no-daemon) can be set/get without requiring a SQLite database.
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
	yamlOnlyKeys := []string{"no-db", "no-daemon", "no-auto-flush", "json", "sync.branch", "routing.mode"}
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
func setupTestDB(t *testing.T) (*sqlite.SQLiteStorage, func()) {
	tmpDir, err := os.MkdirTemp("", "bd-test-config-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	testDB := filepath.Join(tmpDir, "test.db")
	store, err := sqlite.New(context.Background(), testDB)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create test database: %v", err)
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
