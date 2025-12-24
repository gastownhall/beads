package syncbranch

import (
	"context"
	"os"
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
