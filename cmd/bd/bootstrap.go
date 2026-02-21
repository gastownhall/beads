package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// bootstrapEmbeddedDolt checks if a Dolt clone from git remote is needed and runs it if so.
func bootstrapEmbeddedDolt(ctx context.Context, path string, cfg *dolt.Config) error {
	// Dolt-in-Git bootstrap: if sync.git-remote is configured and no local
	// dolt dir exists, clone from the git remote (refs/dolt/data).
	if gitRemoteURL := config.GetYamlConfig("sync.git-remote"); gitRemoteURL != "" {
		if bootstrapped, err := dolt.BootstrapFromGitRemote(ctx, path, gitRemoteURL); err != nil {
			return fmt.Errorf("git remote bootstrap failed: %v", err)
		} else if bootstrapped {
			return nil // Successfully cloned from git remote
		}
	}

	// If the dolt DB doesn't exist, that's an error — no JSONL fallback.
	if !hasDoltSubdir(path) {
		return fmt.Errorf("dolt database not found at %s (run 'bd init --backend=dolt' to create, or configure sync.git-remote for clone)", path)
	}

	return nil
}

// hasDoltSubdir checks if the given path contains any subdirectory with a .dolt directory inside.
func hasDoltSubdir(basePath string) bool {
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		doltDir := filepath.Join(basePath, entry.Name(), ".dolt")
		if info, err := os.Stat(doltDir); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
