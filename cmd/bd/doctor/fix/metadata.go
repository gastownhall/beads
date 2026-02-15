package fix

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// FixMissingMetadata checks and repairs missing metadata fields in a Dolt database.
// Fields checked: bd_version, repo_id, clone_id.
// The bdVersion parameter should be the current CLI version string (from the caller
// in package main, since this package cannot import it directly).
// Returns nil if all fields are present or successfully repaired.
func FixMissingMetadata(path string, bdVersion string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil // Can't load config, nothing to fix
	}
	if cfg == nil {
		return nil // No config file, nothing to fix
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil // Not a Dolt backend, nothing to fix
	}

	ctx := context.Background()

	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	defer func() { _ = store.Close() }()

	var repaired []string

	// Check and repair bd_version
	if val, err := store.GetMetadata(ctx, "bd_version"); err == nil && val == "" {
		if bdVersion != "" {
			if err := store.SetMetadata(ctx, "bd_version", bdVersion); err != nil {
				return fmt.Errorf("failed to set bd_version metadata: %w", err)
			}
			repaired = append(repaired, "bd_version")
		}
	}

	// Check and repair repo_id
	if val, err := store.GetMetadata(ctx, "repo_id"); err == nil && val == "" {
		repoID, err := beads.ComputeRepoID()
		if err != nil {
			// Non-git environment: warn and skip (FR-015)
			fmt.Printf("  Warning: could not compute repo_id (not in a git repo?): %v\n", err)
		} else {
			if err := store.SetMetadata(ctx, "repo_id", repoID); err != nil {
				return fmt.Errorf("failed to set repo_id metadata: %w", err)
			}
			repaired = append(repaired, "repo_id")
		}
	}

	// Check and repair clone_id
	if val, err := store.GetMetadata(ctx, "clone_id"); err == nil && val == "" {
		cloneID, err := beads.GetCloneID()
		if err != nil {
			// Non-standard environment: warn and skip (FR-016)
			fmt.Printf("  Warning: could not compute clone_id: %v\n", err)
		} else {
			if err := store.SetMetadata(ctx, "clone_id", cloneID); err != nil {
				return fmt.Errorf("failed to set clone_id metadata: %w", err)
			}
			repaired = append(repaired, "clone_id")
		}
	}

	// Report results (FR-011: count and names; FR-012: silent if none)
	if len(repaired) > 0 {
		fmt.Printf("  Repaired %d metadata field(s): %s\n", len(repaired), strings.Join(repaired, ", "))
	}

	return nil
}
