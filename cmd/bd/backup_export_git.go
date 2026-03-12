package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const (
	backupExportGitDefaultBranch = "beads-backup"
	backupExportGitDefaultRemote = "origin"
	backupExportGitPathspec      = ".beads/backup"
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
	backupExportGitCmd.Flags().String("branch", backupExportGitDefaultBranch, "Target git branch for backup artifacts")
	backupExportGitCmd.Flags().String("remote", backupExportGitDefaultRemote, "Git remote to push")
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

	if _, err := runBackupExport(ctx, opts.Force); err != nil {
		return nil, err
	}
	result.Exported = true

	tempRoot, err := os.MkdirTemp("", "bd-backup-export-git-*")
	if err != nil {
		return nil, fmt.Errorf("create temp worktree dir: %w", err)
	}
	worktreeDir := filepath.Join(tempRoot, "worktree")
	worktreeAdded := false
	defer func() {
		cleanupErr := cleanupBackupExportGitWorktree(repoRoot, worktreeDir, tempRoot, worktreeAdded)
		if cleanupErr != nil {
			if retErr != nil {
				retErr = errors.Join(retErr, cleanupErr)
			} else {
				retErr = cleanupErr
			}
		}
	}()

	if err := addBackupExportGitWorktree(ctx, repoRoot, opts.Remote, opts.Branch, worktreeDir); err != nil {
		return nil, err
	}
	worktreeAdded = true

	dstBackupDir := filepath.Join(worktreeDir, filepath.FromSlash(backupExportGitPathspec))
	if err := syncBackupSnapshot(backupDirPath, dstBackupDir); err != nil {
		return nil, fmt.Errorf("copy backup snapshot: %w", err)
	}

	if err := gitExecInDir(ctx, worktreeDir, "add", "-A", "-f", "--", backupExportGitPathspec); err != nil {
		return nil, fmt.Errorf("git add %s: %w", backupExportGitPathspec, err)
	}

	changed, err := gitHasCachedChanges(ctx, worktreeDir, backupExportGitPathspec)
	if err != nil {
		return nil, err
	}
	result.Changed = changed
	if !changed {
		return result, nil
	}

	if err := gitExecInDir(ctx, worktreeDir, "commit", "-m", backupExportGitCommitMessage, "--", backupExportGitPathspec); err != nil {
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
	opts.Branch = strings.TrimSpace(opts.Branch)
	if opts.Branch == "" {
		opts.Branch = backupExportGitDefaultBranch
	}
	opts.Remote = strings.TrimSpace(opts.Remote)
	if opts.Remote == "" {
		opts.Remote = backupExportGitDefaultRemote
	}
	return opts
}

func printBackupExportGitResult(result *backupExportGitResult) {
	if result.DryRun {
		fmt.Printf("Dry run: would export backup snapshot to git branch %s\n", result.Branch)
		fmt.Printf("  Remote: %s\n", result.Remote)
		fmt.Printf("  Would copy: %s/\n", backupExportGitPathspec)
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
	fmt.Printf("  Path: %s/\n", backupExportGitPathspec)
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

func addBackupExportGitWorktree(ctx context.Context, repoRoot, remote, branch, worktreeDir string) error {
	localExists, err := gitRefExists(ctx, repoRoot, "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if localExists {
		if err := gitExecInDir(ctx, repoRoot, "worktree", "add", worktreeDir, branch); err != nil {
			return fmt.Errorf("git worktree add %s: %w", branch, err)
		}
		return nil
	}

	remoteRef := fmt.Sprintf("refs/remotes/%s/%s", remote, branch)
	remoteExists, err := gitRefExists(ctx, repoRoot, remoteRef)
	if err != nil {
		return err
	}
	if remoteExists {
		if err := gitExecInDir(ctx, repoRoot, "worktree", "add", "-b", branch, worktreeDir, remote+"/"+branch); err != nil {
			return fmt.Errorf("git worktree add %s from %s/%s: %w", branch, remote, branch, err)
		}
		return nil
	}

	if err := gitExecInDir(ctx, repoRoot, "worktree", "add", "-b", branch, worktreeDir); err != nil {
		return fmt.Errorf("git worktree add -b %s: %w", branch, err)
	}
	return nil
}

func cleanupBackupExportGitWorktree(repoRoot, worktreeDir, tempRoot string, worktreeAdded bool) error {
	var errs []error
	if worktreeAdded {
		if err := gitExecInDir(context.Background(), repoRoot, "worktree", "remove", "--force", worktreeDir); err != nil {
			errs = append(errs, fmt.Errorf("remove temp worktree: %w", err))
		}
	}
	if tempRoot != "" {
		if err := os.RemoveAll(tempRoot); err != nil {
			errs = append(errs, fmt.Errorf("remove temp worktree dir: %w", err))
		}
	}
	return errors.Join(errs...)
}

func gitRefExists(ctx context.Context, dir, ref string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", ref)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git show-ref %s: %w", ref, err)
	}
	return true, nil
}

func gitHasCachedChanges(ctx context.Context, dir, pathspec string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet", "--", pathspec)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return true, nil
		}
		return false, fmt.Errorf("git diff --cached %s: %w", pathspec, err)
	}
	return false, nil
}

func syncBackupSnapshot(srcDir, dstDir string) error {
	if err := os.RemoveAll(dstDir); err != nil {
		return fmt.Errorf("clear destination backup dir: %w", err)
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("create destination backup dir: %w", err)
	}
	return copyDirContents(srcDir, dstDir)
}

func copyDirContents(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read source backup dir: %w", err)
	}
	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", srcPath, err)
		}
		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, info.Mode().Perm()); err != nil {
				return fmt.Errorf("create %s: %w", dstPath, err)
			}
			if err := copyDirContents(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFileContents(srcPath, dstPath, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func copyFileContents(srcPath, dstPath string, mode os.FileMode) error {
	src, err := os.Open(srcPath) //nolint:gosec // source path is internal backup output
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode) //nolint:gosec // destination path is a temp worktree path constructed by the command
	if err != nil {
		return fmt.Errorf("create %s: %w", dstPath, err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy %s: %w", srcPath, err)
	}
	return nil
}
