// Package reset provides core reset functionality for cleaning beads state.
// This package is CLI-agnostic and returns errors for the CLI to handle.
package reset

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// ResetOptions configures the reset operation
type ResetOptions struct {
	Hard     bool // Include git operations (git rm, commit)
	Backup   bool // Create backup before reset
	DryRun   bool // Preview only, don't execute
	SkipInit bool // Don't re-initialize after reset
}

// ResetResult contains the results of a reset operation
type ResetResult struct {
	IssuesDeleted     int
	TombstonesDeleted int
	BackupPath        string // if backup was created
	DaemonsKilled     int
}

// ImpactSummary describes what will be affected by a reset
type ImpactSummary struct {
	IssueCount      int
	OpenCount       int
	ClosedCount     int
	TombstoneCount  int
	HasUncommitted  bool // git dirty state in .beads/
}

// ValidateState checks if .beads/ directory exists and is valid for reset
func ValidateState() error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return fmt.Errorf("no .beads directory found - nothing to reset")
	}

	// Verify it's a directory
	info, err := os.Stat(beadsDir)
	if err != nil {
		return fmt.Errorf("failed to stat .beads directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf(".beads exists but is not a directory")
	}

	return nil
}

// CountImpact analyzes what will be deleted by a reset operation
func CountImpact() (*ImpactSummary, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return nil, fmt.Errorf("no .beads directory found")
	}

	summary := &ImpactSummary{}

	// Try to open database and count issues
	dbPath := beads.FindDatabasePath()
	if dbPath != "" {
		ctx := context.Background()
		store, err := sqlite.New(ctx, dbPath)
		if err == nil {
			defer func() { _ = store.Close() }()

			// Count all issues including tombstones
			allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
			if err == nil {
				summary.IssueCount = len(allIssues)
				for _, issue := range allIssues {
					if issue.IsTombstone() {
						summary.TombstoneCount++
					} else {
						switch issue.Status {
						case types.StatusOpen, types.StatusInProgress, types.StatusBlocked:
							summary.OpenCount++
						case types.StatusClosed:
							summary.ClosedCount++
						}
					}
				}
			}
		}
	}

	// Check git dirty state for .beads/
	summary.HasUncommitted = hasUncommittedBeadsFiles()

	return summary, nil
}

// Reset performs the core reset logic
func Reset(opts ResetOptions) (*ResetResult, error) {
	// Validate state first
	if err := ValidateState(); err != nil {
		return nil, err
	}

	beadsDir := beads.FindBeadsDir()
	result := &ResetResult{}

	// Dry run: just count what would be affected
	if opts.DryRun {
		summary, err := CountImpact()
		if err != nil {
			return nil, err
		}
		result.IssuesDeleted = summary.IssueCount - summary.TombstoneCount
		result.TombstonesDeleted = summary.TombstoneCount
		return result, nil
	}

	// Step 1: Kill all daemons
	daemons, err := daemon.DiscoverDaemons(nil)
	if err == nil {
		killResults := daemon.KillAllDaemons(daemons, true)
		result.DaemonsKilled = killResults.Stopped
	}

	// Step 2: Count issues before deletion (for result reporting)
	summary, _ := CountImpact()
	if summary != nil {
		result.IssuesDeleted = summary.IssueCount - summary.TombstoneCount
		result.TombstonesDeleted = summary.TombstoneCount
	}

	// Step 3: Create backup if requested
	if opts.Backup {
		backupPath, err := createBackup(beadsDir)
		if err != nil {
			return nil, fmt.Errorf("failed to create backup: %w", err)
		}
		result.BackupPath = backupPath
	}

	// Step 4: Hard mode - git rm BEFORE deleting files
	// (must happen while files still exist for git to track the removal)
	if opts.Hard {
		if err := gitRemoveBeads(); err != nil {
			return nil, fmt.Errorf("git rm failed: %w", err)
		}
	}

	// Step 5: Remove .beads directory
	if err := os.RemoveAll(beadsDir); err != nil {
		return nil, fmt.Errorf("failed to remove .beads directory: %w", err)
	}

	// Step 6: Re-initialize unless SkipInit is set
	if !opts.SkipInit {
		if err := reinitializeBeads(); err != nil {
			return nil, fmt.Errorf("re-initialization failed: %w", err)
		}
	}

	return result, nil
}

// createBackup creates a timestamped backup of the .beads directory
func createBackup(beadsDir string) (string, error) {
	return CreateBackup(beadsDir)
}

// gitRemoveBeads performs git rm on .beads directory and commits
func gitRemoveBeads() error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return nil
	}

	// Check git state
	gitState, err := CheckGitState(beadsDir)
	if err != nil {
		return err
	}

	// Skip if not a git repo
	if !gitState.IsRepo {
		return nil
	}

	// Remove JSONL files from git
	if err := GitRemoveBeads(beadsDir); err != nil {
		return err
	}

	// Commit the reset
	commitMsg := "Reset beads workspace\n\nRemoved .beads/ directory to start fresh."
	return GitCommitReset(commitMsg)
}

// reinitializeBeads calls bd init logic to recreate the workspace
func reinitializeBeads() error {
	// Get the current directory name for prefix auto-detection
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	// Create .beads directory
	beadsDir := filepath.Join(cwd, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		return fmt.Errorf("failed to create .beads directory: %w", err)
	}

	// Determine prefix from directory name
	prefix := filepath.Base(cwd)
	prefix = strings.TrimRight(prefix, "-")

	// Create database
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("failed to create database: %w", err)
	}

	// Set issue prefix in config
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		_ = store.Close()
		return fmt.Errorf("failed to set issue prefix: %w", err)
	}

	// Set sync.branch if in git repo (non-fatal if it fails)
	gitState, err := CheckGitState(beadsDir)
	if err == nil && gitState.IsRepo && gitState.Branch != "" && !gitState.IsDetached {
		// Ignore error - sync.branch is optional and CLI can set it later
		_ = store.SetConfig(ctx, "sync.branch", gitState.Branch)
	}

	// Close the database
	if err := store.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	return nil
}

// hasUncommittedBeadsFiles checks if .beads directory has uncommitted changes
func hasUncommittedBeadsFiles() bool {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return false
	}

	gitState, err := CheckGitState(beadsDir)
	if err != nil || !gitState.IsRepo {
		return false
	}

	return gitState.IsDirty
}
