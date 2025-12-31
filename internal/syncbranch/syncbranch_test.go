package syncbranch

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestValidateBranchName(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"empty is valid", "", false},
		{"simple branch", "main", false},
		{"branch with hyphen", "feature-branch", false},
		{"branch with slash", "feature/my-feature", false},
		{"branch with underscore", "feature_branch", false},
		{"branch with dot", "release-1.0", false},
		{"complex valid branch", "feature/user-auth_v2.1", false},
		
		{"invalid: HEAD", "HEAD", true},
		{"invalid: single dot", ".", true},
		{"invalid: double dot", "..", true},
		{"invalid: contains ..", "feature..branch", true},
		{"invalid: starts with slash", "/feature", true},
		{"invalid: ends with slash", "feature/", true},
		{"invalid: starts with hyphen", "-feature", true},
		{"invalid: ends with hyphen", "feature-", true},
		{"invalid: starts with dot", ".feature", true},
		{"invalid: ends with dot", "feature.", true},
		{"invalid: special char @", "feature@branch", true},
		{"invalid: special char #", "feature#branch", true},
		{"invalid: space", "feature branch", true},
		{"invalid: too long", string(make([]byte, 256)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBranchName(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBranchName(%q) error = %v, wantErr %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSyncBranchName(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		// Valid sync branches
		{"beads-sync is valid", "beads-sync", false},
		{"feature branch is valid", "feature-branch", false},
		{"empty is valid", "", false},

		// GH#807: main and master should be rejected for sync branch
		{"main is invalid for sync", "main", true},
		{"master is invalid for sync", "master", true},

		// Standard branch name validation still applies
		{"invalid: HEAD", "HEAD", true},
		{"invalid: contains ..", "feature..branch", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSyncBranchName(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSyncBranchName(%q) error = %v, wantErr %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

func newTestStore(t *testing.T) *sqlite.SQLiteStorage {
	t.Helper()
	store, err := sqlite.New(context.Background(), "file::memory:?mode=memory&cache=private")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		_ = store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	return store
}

func TestGet(t *testing.T) {
	ctx := context.Background()

	t.Run("returns empty when not set", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()
		
		branch, err := Get(ctx, store)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if branch != "" {
			t.Errorf("Get() = %q, want empty string", branch)
		}
	})

	t.Run("returns database config value", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()
		
		if err := store.SetConfig(ctx, ConfigKey, "beads-metadata"); err != nil {
			t.Fatalf("SetConfig() error = %v", err)
		}
		
		branch, err := Get(ctx, store)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if branch != "beads-metadata" {
			t.Errorf("Get() = %q, want %q", branch, "beads-metadata")
		}
	})

	t.Run("environment variable overrides database", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()
		
		// Set database config
		if err := store.SetConfig(ctx, ConfigKey, "beads-metadata"); err != nil {
			t.Fatalf("SetConfig() error = %v", err)
		}
		
		// Set environment variable
		os.Setenv(EnvVar, "env-branch")
		defer os.Unsetenv(EnvVar)
		
		branch, err := Get(ctx, store)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if branch != "env-branch" {
			t.Errorf("Get() = %q, want %q (env should override db)", branch, "env-branch")
		}
	})

	t.Run("returns error for invalid env var", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()
		
		os.Setenv(EnvVar, "invalid..branch")
		defer os.Unsetenv(EnvVar)
		
		_, err := Get(ctx, store)
		if err == nil {
			t.Error("Get() expected error for invalid env var, got nil")
		}
	})

	t.Run("returns error for invalid db config", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()
		
		// Directly set invalid value (bypassing validation)
		if err := store.SetConfig(ctx, ConfigKey, "invalid..branch"); err != nil {
			t.Fatalf("SetConfig() error = %v", err)
		}
		
		_, err := Get(ctx, store)
		if err == nil {
			t.Error("Get() expected error for invalid db config, got nil")
		}
	})
}

func TestSet(t *testing.T) {
	ctx := context.Background()

	t.Run("sets valid branch name", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()
		
		if err := Set(ctx, store, "beads-metadata"); err != nil {
			t.Fatalf("Set() error = %v", err)
		}
		
		value, err := store.GetConfig(ctx, ConfigKey)
		if err != nil {
			t.Fatalf("GetConfig() error = %v", err)
		}
		if value != "beads-metadata" {
			t.Errorf("GetConfig() = %q, want %q", value, "beads-metadata")
		}
	})

	t.Run("allows empty string", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()
		
		if err := Set(ctx, store, ""); err != nil {
			t.Fatalf("Set() error = %v for empty string", err)
		}
		
		value, err := store.GetConfig(ctx, ConfigKey)
		if err != nil {
			t.Fatalf("GetConfig() error = %v", err)
		}
		if value != "" {
			t.Errorf("GetConfig() = %q, want empty string", value)
		}
	})

	t.Run("rejects invalid branch name", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()

		err := Set(ctx, store, "invalid..branch")
		if err == nil {
			t.Error("Set() expected error for invalid branch name, got nil")
		}
	})

	// GH#807: Verify Set() rejects main/master (not just ValidateSyncBranchName)
	t.Run("rejects main as sync branch", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()

		err := Set(ctx, store, "main")
		if err == nil {
			t.Error("Set() expected error for 'main', got nil")
		}
		if err != nil && !strings.Contains(err.Error(), "cannot use 'main'") {
			t.Errorf("Set() error should mention 'cannot use main', got: %v", err)
		}
	})

	t.Run("rejects master as sync branch", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()

		err := Set(ctx, store, "master")
		if err == nil {
			t.Error("Set() expected error for 'master', got nil")
		}
		if err != nil && !strings.Contains(err.Error(), "cannot use 'master'") {
			t.Errorf("Set() error should mention 'cannot use master', got: %v", err)
		}
	})
}

func TestUnset(t *testing.T) {
	ctx := context.Background()

	t.Run("removes config value", func(t *testing.T) {
		store := newTestStore(t)
		defer store.Close()

		// Set a value first
		if err := Set(ctx, store, "beads-metadata"); err != nil {
			t.Fatalf("Set() error = %v", err)
		}

		// Verify it's set
		value, err := store.GetConfig(ctx, ConfigKey)
		if err != nil {
			t.Fatalf("GetConfig() error = %v", err)
		}
		if value != "beads-metadata" {
			t.Errorf("GetConfig() = %q, want %q", value, "beads-metadata")
		}

		// Unset it
		if err := Unset(ctx, store); err != nil {
			t.Fatalf("Unset() error = %v", err)
		}

		// Verify it's gone
		value, err = store.GetConfig(ctx, ConfigKey)
		if err != nil {
			t.Fatalf("GetConfig() error = %v", err)
		}
		if value != "" {
			t.Errorf("GetConfig() after Unset() = %q, want empty string", value)
		}
	})
}

func TestGetFromYAML(t *testing.T) {
	// Save and restore any existing env var
	origEnv := os.Getenv(EnvVar)
	defer os.Setenv(EnvVar, origEnv)

	t.Run("returns empty when nothing configured", func(t *testing.T) {
		os.Unsetenv(EnvVar)
		branch := GetFromYAML()
		// GetFromYAML checks env var first, then config.yaml
		// Without env var set, it should return what's in config.yaml (or empty)
		// We can't easily mock config.yaml here, so just verify no panic
		_ = branch
	})

	t.Run("returns env var value when set", func(t *testing.T) {
		os.Setenv(EnvVar, "env-sync-branch")
		defer os.Unsetenv(EnvVar)

		branch := GetFromYAML()
		if branch != "env-sync-branch" {
			t.Errorf("GetFromYAML() = %q, want %q", branch, "env-sync-branch")
		}
	})
}

func TestIsConfigured(t *testing.T) {
	// Save and restore any existing env var
	origEnv := os.Getenv(EnvVar)
	defer os.Setenv(EnvVar, origEnv)

	t.Run("returns true when env var is set", func(t *testing.T) {
		os.Setenv(EnvVar, "test-branch")
		defer os.Unsetenv(EnvVar)

		if !IsConfigured() {
			t.Error("IsConfigured() = false when env var is set, want true")
		}
	})

	t.Run("behavior with no env var", func(t *testing.T) {
		os.Unsetenv(EnvVar)
		// Just verify no panic - actual value depends on config.yaml
		_ = IsConfigured()
	})
}

func TestIsConfiguredWithDB(t *testing.T) {
	// Save and restore any existing env var
	origEnv := os.Getenv(EnvVar)
	defer os.Setenv(EnvVar, origEnv)

	t.Run("returns true when env var is set", func(t *testing.T) {
		os.Setenv(EnvVar, "test-branch")
		defer os.Unsetenv(EnvVar)

		if !IsConfiguredWithDB("") {
			t.Error("IsConfiguredWithDB() = false when env var is set, want true")
		}
	})

	t.Run("returns false for nonexistent database", func(t *testing.T) {
		os.Unsetenv(EnvVar)

		result := IsConfiguredWithDB("/nonexistent/path/beads.db")
		// Should return false because db doesn't exist
		if result {
			t.Error("IsConfiguredWithDB() = true for nonexistent db, want false")
		}
	})

	t.Run("returns false for empty path with no db found", func(t *testing.T) {
		os.Unsetenv(EnvVar)
		// When empty path is passed and beads.FindDatabasePath() returns empty,
		// IsConfiguredWithDB should return false
		// This tests the code path where dbPath is empty
		tmpDir, _ := os.MkdirTemp("", "test-no-beads-*")
		defer os.RemoveAll(tmpDir)

		origWd, _ := os.Getwd()
		os.Chdir(tmpDir)
		defer os.Chdir(origWd)

		result := IsConfiguredWithDB("")
		// Should return false because no database exists
		if result {
			t.Error("IsConfiguredWithDB('') with no db = true, want false")
		}
	})
}

func TestGetConfigFromDB(t *testing.T) {
	t.Run("returns empty for nonexistent database", func(t *testing.T) {
		result := getConfigFromDB("/nonexistent/path/beads.db", ConfigKey)
		if result != "" {
			t.Errorf("getConfigFromDB() for nonexistent db = %q, want empty", result)
		}
	})

	t.Run("returns empty when key not found", func(t *testing.T) {
		// Create a temporary database
		tmpDir, _ := os.MkdirTemp("", "test-beads-db-*")
		defer os.RemoveAll(tmpDir)
		dbPath := tmpDir + "/beads.db"

		// Create a valid SQLite database with the config table
		store, err := sqlite.New(context.Background(), "file:"+dbPath)
		if err != nil {
			t.Fatalf("Failed to create test database: %v", err)
		}
		store.Close()

		result := getConfigFromDB(dbPath, "nonexistent.key")
		if result != "" {
			t.Errorf("getConfigFromDB() for missing key = %q, want empty", result)
		}
	})

	t.Run("returns value when key exists", func(t *testing.T) {
		// Create a temporary database
		tmpDir, _ := os.MkdirTemp("", "test-beads-db-*")
		defer os.RemoveAll(tmpDir)
		dbPath := tmpDir + "/beads.db"

		// Create a valid SQLite database with the config table
		ctx := context.Background()
		// Use the same connection string format as getConfigFromDB expects
		store, err := sqlite.New(ctx, "file:"+dbPath+"?_journal_mode=DELETE")
		if err != nil {
			t.Fatalf("Failed to create test database: %v", err)
		}
		// Set issue_prefix first (required by storage)
		if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			store.Close()
			t.Fatalf("Failed to set issue_prefix: %v", err)
		}
		// Set the config value we're testing
		if err := store.SetConfig(ctx, ConfigKey, "test-sync-branch"); err != nil {
			store.Close()
			t.Fatalf("Failed to set config: %v", err)
		}
		store.Close()

		result := getConfigFromDB(dbPath, ConfigKey)
		if result != "test-sync-branch" {
			t.Errorf("getConfigFromDB() = %q, want %q", result, "test-sync-branch")
		}
	})
}
