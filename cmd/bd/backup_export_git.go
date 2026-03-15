package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

const (
	backupExportGitCommitMessage = "bd backup export-git"
)

type backupExportGitOptions struct {
	Branch string
	Remote string
	DryRun bool
	Force  bool
}

type backupExportGitResult struct {
	Branch        string `json:"branch"`
	Remote        string `json:"remote"`
	BackupDir     string `json:"backup_dir"`
	Exported      bool   `json:"exported,omitempty"`
	Changed       bool   `json:"changed,omitempty"`
	Committed     bool   `json:"committed,omitempty"`
	Pushed        bool   `json:"pushed,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`
	DryRun        bool   `json:"dry_run,omitempty"`
	WouldExport   bool   `json:"would_export,omitempty"`
	WouldCopy     bool   `json:"would_copy,omitempty"`
	WouldCommit   bool   `json:"would_commit,omitempty"`
	WouldPush     bool   `json:"would_push,omitempty"`
}

var backupExportGitCmd = &cobra.Command{
	Use:   "export-git",
	Short: "Export the current JSONL backup snapshot to a git branch",
	Long: `Export the current JSONL backup snapshot using the existing bd backup format,
copy it into a git branch, commit if changed, and push the branch.

This command is intended for storing backup artifacts in git. It does not
change primary storage or Dolt remote configuration.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		branch, _ := cmd.Flags().GetString("branch")
		remote, _ := cmd.Flags().GetString("remote")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		force, _ := cmd.Flags().GetBool("force")

		result, err := runBackupExportGit(rootCtx, backupExportGitOptions{
			Branch: branch,
			Remote: remote,
			DryRun: dryRun,
			Force:  force,
		})
		if err != nil {
			return err
		}

		if jsonOutput {
			data, marshalErr := marshalBackupExportGitResultJSON(result)
			if marshalErr != nil {
				return marshalErr
			}
			fmt.Println(string(data))
			return nil
		}

		printBackupExportGitResult(result)
		return nil
	},
}

func init() {
	backupExportGitCmd.Flags().String("branch", backupGitDefaultBranch, "Target git branch for backup artifacts")
	backupExportGitCmd.Flags().String("remote", backupGitDefaultRemote, "Git remote to push")
	backupExportGitCmd.Flags().Bool("dry-run", false, "Show what would happen without creating a worktree, committing, or pushing")
	backupExportGitCmd.Flags().Bool("force", false, "Force a fresh backup export before comparing and copying")
	backupCmd.AddCommand(backupExportGitCmd)
}

func runBackupExportGit(ctx context.Context, opts backupExportGitOptions) (_ *backupExportGitResult, retErr error) {
	opts = normalizeBackupExportGitOptions(opts)

	repoRoot, err := findGitRoot(".")
	if err != nil {
		return nil, fmt.Errorf("not in a git repository")
	}

	backupDirPath, err := backupDir()
	if err != nil {
		return nil, err
	}
	absBackupDir, err := filepath.Abs(backupDirPath)
	if err != nil {
		return nil, fmt.Errorf("resolve backup directory: %w", err)
	}

	result := &backupExportGitResult{
		Branch:    opts.Branch,
		Remote:    opts.Remote,
		BackupDir: absBackupDir,
	}

	if opts.DryRun {
		result.DryRun = true
		result.WouldExport = true
		result.WouldCopy = true
		result.WouldCommit = true
		result.WouldPush = true
		return result, nil
	}

	state, err := runBackupExport(ctx, opts.Force)
	if err != nil {
		return nil, err
	}
	result.Exported = true

	tempRoot, worktreeDir, err := createTempBackupGitWorktree("bd-backup-export-git-*")
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

	if err := addBackupBranchWorktree(ctx, repoRoot, opts.Remote, opts.Branch, worktreeDir); err != nil {
		return nil, err
	}
	worktreeAdded = true

	dstBackupDir := filepath.Join(worktreeDir, filepath.FromSlash(backupGitPathspec))
	if err := syncBackupSnapshot(backupDirPath, dstBackupDir); err != nil {
		return nil, fmt.Errorf("copy backup snapshot: %w", err)
	}
	if err := writeBackupGitManifest(dstBackupDir, state); err != nil {
		return nil, fmt.Errorf("write backup manifest: %w", err)
	}

	if err := gitExecInDir(ctx, worktreeDir, "add", "-A", "-f", "--", backupGitPathspec); err != nil {
		return nil, fmt.Errorf("git add %s: %w", backupGitPathspec, err)
	}

	changed, err := gitHasCachedChanges(ctx, worktreeDir, backupGitPathspec)
	if err != nil {
		return nil, err
	}
	result.Changed = changed
	if !changed {
		return result, nil
	}

	if err := gitExecInDir(ctx, worktreeDir, "commit", "-m", backupExportGitCommitMessage, "--", backupGitPathspec); err != nil {
		return result, fmt.Errorf("git commit: %w", err)
	}
	result.Committed = true
	result.CommitMessage = backupExportGitCommitMessage

	if err := gitExecInDir(ctx, worktreeDir, "push", "-u", opts.Remote, opts.Branch); err != nil {
		return result, fmt.Errorf("git push %s %s: %w", opts.Remote, opts.Branch, err)
	}
	result.Pushed = true

	return result, nil
}

func normalizeBackupExportGitOptions(opts backupExportGitOptions) backupExportGitOptions {
	opts.Branch = normalizeBackupGitRef(opts.Branch, backupGitDefaultBranch)
	opts.Remote = normalizeBackupGitRef(opts.Remote, backupGitDefaultRemote)
	return opts
}

func printBackupExportGitResult(result *backupExportGitResult) {
	if result.DryRun {
		fmt.Printf("Dry run: would export backup snapshot to git branch %s\n", result.Branch)
		fmt.Printf("  Remote: %s\n", result.Remote)
		fmt.Printf("  Would copy: %s/\n", backupGitPathspec)
		fmt.Println("  Would commit if changed")
		fmt.Println("  Would push to remote")
		return
	}

	if !result.Changed {
		fmt.Println("No backup snapshot changes to export")
		fmt.Printf("  Branch: %s\n", result.Branch)
		fmt.Printf("  Remote: %s\n", result.Remote)
		return
	}

	fmt.Printf("Exported backup snapshot to git branch %s\n", result.Branch)
	fmt.Printf("  Remote: %s\n", result.Remote)
	fmt.Printf("  Path: %s/\n", backupGitPathspec)
	fmt.Println("  Commit: created")
	fmt.Println("  Push: complete")
}

func marshalBackupExportGitResultJSON(result *backupExportGitResult) ([]byte, error) {
	payload := map[string]any{
		"branch":     result.Branch,
		"remote":     result.Remote,
		"backup_dir": result.BackupDir,
	}
	if result.DryRun {
		payload["dry_run"] = true
		payload["would_export"] = result.WouldExport
		payload["would_copy"] = result.WouldCopy
		payload["would_commit"] = result.WouldCommit
		payload["would_push"] = result.WouldPush
		return json.MarshalIndent(payload, "", "  ")
	}

	payload["exported"] = result.Exported
	payload["changed"] = result.Changed
	payload["committed"] = result.Committed
	payload["pushed"] = result.Pushed
	if result.CommitMessage != "" {
		payload["commit_message"] = result.CommitMessage
	}
	return json.MarshalIndent(payload, "", "  ")
}
