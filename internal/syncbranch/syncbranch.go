package syncbranch

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/steveyegge/beads/internal/storage"
)

const (
	// ConfigKey is the database config key for sync branch
	ConfigKey = "sync.branch"
	
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

// Get retrieves the sync branch configuration with the following precedence:
// 1. BEADS_SYNC_BRANCH environment variable
// 2. sync.branch from database config
// 3. Empty string (meaning use current branch)
func Get(ctx context.Context, store storage.Storage) (string, error) {
	// Check environment variable first
	if envBranch := os.Getenv(EnvVar); envBranch != "" {
		if err := ValidateBranchName(envBranch); err != nil {
			return "", fmt.Errorf("invalid %s: %w", EnvVar, err)
		}
		return envBranch, nil
	}
	
	// Check database config
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

// Set stores the sync branch configuration in the database
func Set(ctx context.Context, store storage.Storage, branch string) error {
	if err := ValidateBranchName(branch); err != nil {
		return err
	}
	
	return store.SetConfig(ctx, ConfigKey, branch)
}

// Unset removes the sync branch configuration from the database
func Unset(ctx context.Context, store storage.Storage) error {
	return store.DeleteConfig(ctx, ConfigKey)
}
