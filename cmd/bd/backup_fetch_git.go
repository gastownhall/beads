package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

type backupFetchGitOptions struct {
	Branch string
	Remote string
	DryRun bool
}

type backupFetchGitResult struct {
	Branch       string `json:"branch"`
	Remote       string `json:"remote"`
	Fetched      bool   `json:"fetched,omitempty"`
	Restored     bool   `json:"restored,omitempty"`
	Issues       int    `json:"issues,omitempty"`
	Comments     int    `json:"comments,omitempty"`
	Dependencies int    `json:"dependencies,omitempty"`
	Labels       int    `json:"labels,omitempty"`
	Events       int    `json:"events,omitempty"`
	Config       int    `json:"config,omitempty"`
	Warnings     int    `json:"warnings,omitempty"`
	DryRun       bool   `json:"dry_run,omitempty"`
	WouldFetch   bool   `json:"would_fetch,omitempty"`
	WouldRestore bool   `json:"would_restore,omitempty"`
}

var backupFetchGitCmd = &cobra.Command{
	Use:   "fetch-git",
	Short: "Fetch a JSONL backup snapshot from a git branch and restore it",
	Long: `Fetch a JSONL backup snapshot from a git branch and restore it using
the existing bd backup restore flow.

This command is intended as the companion to 'bd backup export-git'. It uses
a temporary git worktree, restores from .beads/backup/ in that snapshot, and
leaves your current branch and working tree untouched.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		branch, _ := cmd.Flags().GetString("branch")
		remote, _ := cmd.Flags().GetString("remote")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		result, err := runBackupFetchGit(rootCtx, store, backupFetchGitOptions{
			Branch: branch,
			Remote: remote,
			DryRun: dryRun,
		})
		if err != nil {
			return err
		}

		if jsonOutput {
			outputJSON(result)
			return nil
		}

		printBackupFetchGitResult(result)
		return nil
	},
}

func init() {
	backupFetchGitCmd.Flags().String("branch", backupGitDefaultBranch, "Git branch to fetch backup artifacts from")
	backupFetchGitCmd.Flags().String("remote", backupGitDefaultRemote, "Git remote to fetch from")
	backupFetchGitCmd.Flags().Bool("dry-run", false, "Show what would happen without fetching or restoring")
	backupCmd.AddCommand(backupFetchGitCmd)
}

func runBackupFetchGit(ctx context.Context, s *dolt.DoltStore, opts backupFetchGitOptions) (_ *backupFetchGitResult, retErr error) {
	opts = normalizeBackupFetchGitOptions(opts)

	repoRoot, err := findGitRoot(".")
	if err != nil {
		return nil, fmt.Errorf("not in a git repository")
	}

	result := &backupFetchGitResult{
		Branch: opts.Branch,
		Remote: opts.Remote,
	}

	if opts.DryRun {
		result.DryRun = true
		result.WouldFetch = true
		result.WouldRestore = true
		return result, nil
	}

	tempRoot, worktreeDir, err := createTempBackupGitWorktree("bd-backup-fetch-git-*")
	if err != nil {
		return nil, err
	}
	worktreeAdded := false
	defer func() {
		cleanupErr := cleanupTempBackupGitWorktree(repoRoot, worktreeDir, tempRoot, worktreeAdded)
		if cleanupErr != nil {
			if retErr != nil {
				retErr = errors.Join(retErr, cleanupErr)
			} else {
				retErr = cleanupErr
			}
		}
	}()

	if err := addBackupFetchWorktree(ctx, repoRoot, opts.Remote, opts.Branch, worktreeDir); err != nil {
		return nil, err
	}
	worktreeAdded = true
	result.Fetched = true

	snapshotDir := filepath.Join(worktreeDir, filepath.FromSlash(backupGitPathspec))
	if err := validateBackupRestoreDir(snapshotDir); err != nil {
		return nil, err
	}

	restoreResult, err := runBackupRestore(ctx, s, snapshotDir, false)
	if err != nil {
		return nil, err
	}

	result.Restored = true
	result.Issues = restoreResult.Issues
	result.Comments = restoreResult.Comments
	result.Dependencies = restoreResult.Dependencies
	result.Labels = restoreResult.Labels
	result.Events = restoreResult.Events
	result.Config = restoreResult.Config
	result.Warnings = restoreResult.Warnings

	return result, nil
}

func normalizeBackupFetchGitOptions(opts backupFetchGitOptions) backupFetchGitOptions {
	opts.Branch = normalizeBackupGitRef(opts.Branch, backupGitDefaultBranch)
	opts.Remote = normalizeBackupGitRef(opts.Remote, backupGitDefaultRemote)
	return opts
}

func printBackupFetchGitResult(result *backupFetchGitResult) {
	if result.DryRun {
		fmt.Printf("Dry run: would fetch backup snapshot from git branch %s\n", result.Branch)
		fmt.Printf("  Remote: %s\n", result.Remote)
		fmt.Printf("  Would restore: %s/\n", backupGitPathspec)
		return
	}

	fmt.Printf("Fetched backup snapshot from git branch %s and restored local database\n", result.Branch)
	fmt.Printf("  Remote: %s\n", result.Remote)
	fmt.Printf("  Issues: %d\n", result.Issues)
	fmt.Printf("  Comments: %d\n", result.Comments)
	fmt.Printf("  Dependencies: %d\n", result.Dependencies)
	fmt.Printf("  Labels: %d\n", result.Labels)
	fmt.Printf("  Events: %d\n", result.Events)
	fmt.Printf("  Config: %d\n", result.Config)
	if result.Warnings > 0 {
		fmt.Printf("  Warnings: %d\n", result.Warnings)
	}
}
