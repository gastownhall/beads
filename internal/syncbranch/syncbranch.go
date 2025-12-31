package syncbranch

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"

	// Import SQLite driver (same as used by storage/sqlite)
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const (
	// ConfigKey is the database config key for sync branch
	ConfigKey = "sync.branch"

	// ConfigYAMLKey is the config.yaml key for sync branch
	ConfigYAMLKey = "sync-branch"

	// EnvVar is the environment variable for sync branch
	EnvVar = "BEADS_SYNC_BRANCH"
)

// branchNamePattern validates git branch names
// Based on git-check-ref-format rules
var branchNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*[a-zA-Z0-9]$`)

// ValidateBranchName checks if a branch name is valid according to git rules
func ValidateBranchName(name string) error {
	if name == "" {
		return nil // Empty is valid (means use current branch)
	}

	// Basic length check
	if len(name) > 255 {
		return fmt.Errorf("branch name too long (max 255 characters)")
	}

	// Check pattern
	if !branchNamePattern.MatchString(name) {
		return fmt.Errorf("invalid branch name: must start and end with alphanumeric, can contain .-_/ in middle")
	}

	// Disallow certain patterns
	if name == "HEAD" || name == "." || name == ".." {
		return fmt.Errorf("invalid branch name: %s is reserved", name)
	}

	// No consecutive dots
	if regexp.MustCompile(`\.\.`).MatchString(name) {
		return fmt.Errorf("invalid branch name: cannot contain '..'")
	}

	// No leading/trailing slashes
	if name[0] == '/' || name[len(name)-1] == '/' {
		return fmt.Errorf("invalid branch name: cannot start or end with '/'")
	}

	return nil
}

// ValidateSyncBranchName checks if a branch name is valid for use as sync.branch.
// GH#807: Setting sync.branch to 'main' or 'master' causes problems because the
// worktree mechanism will check out that branch, preventing the user from checking
// it out in their working directory.
func ValidateSyncBranchName(name string) error {
	if err := ValidateBranchName(name); err != nil {
		return err
	}

	// GH#807: Reject main/master as sync branch - these cause worktree conflicts
	if name == "main" || name == "master" {
		return fmt.Errorf("cannot use '%s' as sync branch: git worktrees prevent checking out the same branch in multiple locations. Use a dedicated branch like 'beads-sync' instead", name)
	}

	return nil
}

// Get retrieves the sync branch configuration with the following precedence:
// 1. BEADS_SYNC_BRANCH environment variable
// 2. sync-branch from config.yaml (version controlled, shared across clones)
// 3. sync.branch from database config (legacy, for backward compatibility)
// 4. Empty string (meaning use current branch)
func Get(ctx context.Context, store storage.Storage) (string, error) {
	// Check environment variable first (highest priority)
	if envBranch := os.Getenv(EnvVar); envBranch != "" {
		if err := ValidateBranchName(envBranch); err != nil {
			return "", fmt.Errorf("invalid %s: %w", EnvVar, err)
		}
		return envBranch, nil
	}

	// Check config.yaml (version controlled, shared across clones)
	// This is the recommended way to configure sync branch for teams
	if yamlBranch := config.GetString(ConfigYAMLKey); yamlBranch != "" {
		if err := ValidateBranchName(yamlBranch); err != nil {
			return "", fmt.Errorf("invalid %s in config.yaml: %w", ConfigYAMLKey, err)
		}
		return yamlBranch, nil
	}

	// Check database config (legacy, for backward compatibility)
	dbBranch, err := store.GetConfig(ctx, ConfigKey)
	if err != nil {
		return "", fmt.Errorf("failed to get %s from config: %w", ConfigKey, err)
	}

	if dbBranch != "" {
		if err := ValidateBranchName(dbBranch); err != nil {
			return "", fmt.Errorf("invalid %s in database: %w", ConfigKey, err)
		}
	}

	return dbBranch, nil
}

// GetFromYAML retrieves sync branch from config.yaml only (no database lookup).
// This is useful for hooks and checks that need to know if sync-branch is configured
// in the version-controlled config without database access.
func GetFromYAML() string {
	// Check environment variable first
	if envBranch := os.Getenv(EnvVar); envBranch != "" {
		return envBranch
	}
	return config.GetString(ConfigYAMLKey)
}

// IsConfigured returns true if sync-branch is configured in config.yaml or env var.
// This is a fast check that doesn't require database access.
func IsConfigured() bool {
	return GetFromYAML() != ""
}

// IsConfiguredWithDB returns true if sync-branch is configured in any source:
// 1. BEADS_SYNC_BRANCH environment variable
// 2. sync-branch in config.yaml
// 3. sync.branch in database config
//
// The dbPath parameter should be the path to the beads.db file.
// If dbPath is empty, it will use beads.FindDatabasePath() to locate the database.
// This function is safe to call even if the database doesn't exist (returns false in that case).
func IsConfiguredWithDB(dbPath string) bool {
	// First check env var and config.yaml (fast path)
	if GetFromYAML() != "" {
		return true
	}

	// Try to read from database
	if dbPath == "" {
		// Use existing beads.FindDatabasePath() which is worktree-aware
		dbPath = beads.FindDatabasePath()
		if dbPath == "" {
			return false
		}
	}

	// Read sync.branch from database config table
	branch := getConfigFromDB(dbPath, ConfigKey)
	return branch != ""
}

// getConfigFromDB reads a config value directly from the database file.
// This is a lightweight read that doesn't require the full storage layer.
// Returns empty string if the database doesn't exist or the key is not found.
func getConfigFromDB(dbPath string, key string) string {
	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return ""
	}

	// Open database in read-only mode
	// Use file: prefix as required by ncruces/go-sqlite3 driver
	connStr := fmt.Sprintf("file:%s?mode=ro", dbPath)
	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return ""
	}
	defer db.Close()

	// Query the config table
	var value string
	err = db.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return ""
	}

	return value
}

// Set stores the sync branch configuration in the database
func Set(ctx context.Context, store storage.Storage, branch string) error {
	// GH#807: Use sync-specific validation that rejects main/master
	if err := ValidateSyncBranchName(branch); err != nil {
		return err
	}

	return store.SetConfig(ctx, ConfigKey, branch)
}

// Unset removes the sync branch configuration from the database
func Unset(ctx context.Context, store storage.Storage) error {
	return store.DeleteConfig(ctx, ConfigKey)
}
