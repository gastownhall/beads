package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/ui"
)

// runInitRegisteredBackend provisions a workspace on a registered backend
// that can create one (backends.CanCreate). It is the minimal init: the
// existing-data guard, the standard .beads/.gitignore, the backend's own store
// creation, metadata.json naming the backend and a fresh project id, then the
// issue prefix and the identity written through the open store. The remote,
// hook and agent setup of the Dolt route is Dolt's and is left out.
func runInitRegisteredBackend(ctx context.Context, name, prefix string, quiet, initIfMissing bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	beadsDir := resolveInitBeadsDir()
	if beadsDir == "" {
		return fmt.Errorf("cannot determine the workspace directory for bd init")
	}
	if prefix == "" {
		prefix = config.GetString("issue-prefix")
	}
	if prefix == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %v", err)
		}
		prefix = filepath.Base(cwd)
	}
	prefix = normalizeIssuePrefix(prefix)

	if existing, err := configfile.LoadForDiscovery(beadsDir); err != nil {
		return fmt.Errorf("failed to load %s: %w", configfile.ConfigPath(beadsDir), err)
	} else if existing != nil {
		if initIfMissing {
			if !quiet {
				fmt.Printf("  %s Workspace already initialized at %s (backend %q)\n", ui.RenderPass("✓"), beadsDir, existing.GetBackend())
			}
			return nil
		}
		return alreadyInitialized("beads workspace already exists at %s (backend %q); use bd commands normally, or move it aside before initializing again", beadsDir, existing.GetBackend())
	}
	if err := checkExistingBeadsDataAt(beadsDir, prefix); err != nil {
		return err
	}
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", beadsDir, err)
	}
	// The standard .beads/.gitignore keeps the runtime files, locks and
	// credential key every workspace can grow out of git. It knows only the
	// built-in engines' directories: the backend's Create keeps its own store
	// out of version control.
	if err := doctor.EnsureGitignoreForBeadsDir(beadsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to create/update .gitignore: %v\n", err)
	}
	if err := backends.Create(ctx, name, beadsDir); err != nil {
		return fmt.Errorf("failed to create the %s workspace store: %w", name, err)
	}

	cfg := configfile.DefaultConfig()
	cfg.Backend = name
	cfg.ProjectID = configfile.GenerateProjectID()
	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("failed to write %s: %w", configfile.ConfigPath(beadsDir), err)
	}

	// Init opens to write, the posture the Dolt route's own open has: a
	// backend that cannot journal on a journaling workspace is refused here
	// rather than by every later write.
	store, err := newRegisteredBackendStore(ctx, name, beadsDir, false, true)
	if err != nil {
		return openStoreError(name, err)
	}
	defer func() { _ = store.Close() }()
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		return fmt.Errorf("failed to set issue prefix: %w", err)
	}
	if err := store.SetMetadata(ctx, "_project_id", cfg.ProjectID); err != nil {
		return fmt.Errorf("failed to record the workspace identity: %w", err)
	}
	if !quiet {
		fmt.Printf("  %s Initialized beads workspace at %s\n", ui.RenderPass("✓"), beadsDir)
		fmt.Printf("  %s Backend: %s\n", ui.RenderPass("✓"), name)
		fmt.Printf("  %s Issue prefix: %s\n", ui.RenderPass("✓"), prefix)
	}
	return nil
}
