package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	backupGitDefaultBranch    = "beads-backup"
	backupGitDefaultRemote    = "origin"
	backupGitPathspec         = ".beads/backup"
	backupGitManifestFilename = "manifest.json"
)

type backupGitManifest struct {
	Format            string         `json:"format"`
	BDVersion         string         `json:"bd_version"`
	SnapshotTimestamp time.Time      `json:"snapshot_timestamp"`
	LastDoltCommit    string         `json:"last_dolt_commit"`
	Counts            manifestCounts `json:"counts"`
}

type manifestCounts struct {
	Issues       int `json:"issues"`
	Events       int `json:"events"`
	Comments     int `json:"comments"`
	Dependencies int `json:"dependencies"`
	Labels       int `json:"labels"`
	Config       int `json:"config"`
}

func normalizeBackupGitRef(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func createTempBackupGitWorktree(prefix string) (string, string, error) {
	tempRoot, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", "", fmt.Errorf("create temp worktree dir: %w", err)
	}
	return tempRoot, filepath.Join(tempRoot, "worktree"), nil
}

func cleanupTempBackupGitWorktree(repoRoot, worktreeDir, tempRoot string, worktreeAdded bool) error {
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

func addBackupBranchWorktree(ctx context.Context, repoRoot, remote, branch, worktreeDir string) error {
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

func addBackupFetchWorktree(ctx context.Context, repoRoot, remote, branch, worktreeDir string) error {
	if err := gitExecInDir(ctx, repoRoot, "fetch", remote, branch); err != nil {
		return fmt.Errorf("git fetch %s %s: %w", remote, branch, err)
	}
	if err := gitExecInDir(ctx, repoRoot, "worktree", "add", "--detach", worktreeDir, "FETCH_HEAD"); err != nil {
		return fmt.Errorf("git worktree add FETCH_HEAD: %w", err)
	}
	return nil
}

func gitRefExists(ctx context.Context, dir, ref string) (bool, error) {
	exitCode, err := gitExitCodeInDir(ctx, dir, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		return false, fmt.Errorf("git show-ref %s: %w", ref, err)
	}
	if exitCode == 1 {
		return false, nil
	}
	if exitCode != 0 {
		return false, fmt.Errorf("git show-ref %s exited with code %d", ref, exitCode)
	}
	return true, nil
}

func gitHasCachedChanges(ctx context.Context, dir, pathspec string) (bool, error) {
	exitCode, err := gitExitCodeInDir(ctx, dir, "diff", "--cached", "--quiet", "--", pathspec)
	if err != nil {
		return false, fmt.Errorf("git diff --cached %s: %w", pathspec, err)
	}
	if exitCode == 1 {
		return true, nil
	}
	if exitCode != 0 {
		return false, fmt.Errorf("git diff --cached %s exited with code %d", pathspec, exitCode)
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

func writeBackupGitManifest(dstDir string, state *backupState) error {
	if state == nil {
		return fmt.Errorf("backup state is required")
	}

	manifest := backupGitManifest{
		Format:            "bd-backup-git-manifest-v1",
		BDVersion:         Version,
		SnapshotTimestamp: state.Timestamp,
		LastDoltCommit:    state.LastDoltCommit,
		Counts: manifestCounts{
			Issues:       state.Counts.Issues,
			Events:       state.Counts.Events,
			Comments:     state.Counts.Comments,
			Dependencies: state.Counts.Dependencies,
			Labels:       state.Counts.Labels,
			Config:       state.Counts.Config,
		},
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal backup manifest: %w", err)
	}
	data = append(data, '\n')

	return atomicWriteFile(filepath.Join(dstDir, backupGitManifestFilename), data)
}
