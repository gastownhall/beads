package main

import (
	"context"
	"testing"
)

func TestGetRepoConfig_EmptyValue(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Test 1: No config set at all - should return empty map
	repos, err := getRepoConfig(ctx, store)
	if err != nil {
		t.Fatalf("getRepoConfig with no config failed: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("Expected empty map, got %d entries", len(repos))
	}

	// Test 2: Empty string value - should return empty map (this was the bug)
	// This simulates GetConfig returning ("", nil) which caused "unexpected end of JSON input"
	err = store.SetConfig(ctx, "repos.additional", "")
	if err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	repos, err = getRepoConfig(ctx, store)
	if err != nil {
		t.Fatalf("getRepoConfig with empty value failed: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("Expected empty map for empty value, got %d entries", len(repos))
	}

	// Test 3: Valid JSON value - should parse correctly
	err = store.SetConfig(ctx, "repos.additional", `{"alias1":"/path/to/repo1","alias2":"/path/to/repo2"}`)
	if err != nil {
		t.Fatalf("SetConfig with JSON failed: %v", err)
	}
	repos, err = getRepoConfig(ctx, store)
	if err != nil {
		t.Fatalf("getRepoConfig with valid JSON failed: %v", err)
	}
	if len(repos) != 2 {
		t.Errorf("Expected 2 repos, got %d", len(repos))
	}
	if repos["alias1"] != "/path/to/repo1" {
		t.Errorf("Expected '/path/to/repo1', got '%s'", repos["alias1"])
	}
	if repos["alias2"] != "/path/to/repo2" {
		t.Errorf("Expected '/path/to/repo2', got '%s'", repos["alias2"])
	}
}

func TestSetRepoConfig(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Set repos and verify round-trip
	repos := map[string]string{
		"planning": "/home/user/planning-repo",
		"shared":   "/home/user/shared-repo",
	}

	err := setRepoConfig(ctx, store, repos)
	if err != nil {
		t.Fatalf("setRepoConfig failed: %v", err)
	}

	// Read back
	result, err := getRepoConfig(ctx, store)
	if err != nil {
		t.Fatalf("getRepoConfig after set failed: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("Expected 2 repos, got %d", len(result))
	}
	if result["planning"] != "/home/user/planning-repo" {
		t.Errorf("Expected planning repo path, got '%s'", result["planning"])
	}
	if result["shared"] != "/home/user/shared-repo" {
		t.Errorf("Expected shared repo path, got '%s'", result["shared"])
	}
}

func TestRepoConfigEmptyMap(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Set empty map
	repos := make(map[string]string)
	err := setRepoConfig(ctx, store, repos)
	if err != nil {
		t.Fatalf("setRepoConfig with empty map failed: %v", err)
	}

	// Read back - should work and return empty map
	result, err := getRepoConfig(ctx, store)
	if err != nil {
		t.Fatalf("getRepoConfig after empty set failed: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Expected empty map, got %d entries", len(result))
	}
}
