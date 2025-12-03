package syncbranch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/merge"
)

// CommitResult contains information about a worktree commit operation
type CommitResult struct {
	Committed  bool   // True if changes were committed
	Pushed     bool   // True if changes were pushed
	Branch     string // The sync branch name
	Message    string // Commit message used
}

// PullResult contains information about a worktree pull operation
type PullResult struct {
	Pulled        bool   // True if pull was performed
	Branch        string // The sync branch name
	JSONLPath     string // Path to the synced JSONL in main repo
	Merged        bool   // True if divergent histories were merged
	FastForwarded bool   // True if fast-forward was possible
	Pushed        bool   // True if changes were pushed after merge (bd-7ch)
}

// CommitToSyncBranch commits JSONL changes to the sync branch using a git worktree.
// This allows committing to a different branch without changing the user's working directory.
//
// IMPORTANT (bd-3s8 fix): Before committing, this function now performs a pre-emptive fetch
// and fast-forward if possible. This reduces the likelihood of divergence by ensuring we're
// building on top of the latest remote state when possible.
//
// Parameters:
//   - ctx: Context for cancellation
//   - repoRoot: Path to the git repository root
//   - syncBranch: Name of the sync branch (e.g., "beads-sync")
//   - jsonlPath: Absolute path to the JSONL file in the main repo
//   - push: If true, push to remote after commit
//
// Returns CommitResult with details about what was done, or error if failed.
func CommitToSyncBranch(ctx context.Context, repoRoot, syncBranch, jsonlPath string, push bool) (*CommitResult, error) {
	result := &CommitResult{
		Branch: syncBranch,
	}

	// Worktree path is under .git/beads-worktrees/<branch>
	worktreePath := filepath.Join(repoRoot, ".git", "beads-worktrees", syncBranch)

	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)

	// Ensure worktree exists
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w", err)
	}

	// Check worktree health and repair if needed
	if err := wtMgr.CheckWorktreeHealth(worktreePath); err != nil {
		// Try to recreate worktree
		if err := wtMgr.RemoveBeadsWorktree(worktreePath); err != nil {
			// Log but continue - removal might fail but recreation might work
			_ = err
		}
		if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
			return nil, fmt.Errorf("failed to recreate worktree after health check: %w", err)
		}
	}

	// Get remote name
	remote := getRemoteForBranch(ctx, worktreePath, syncBranch)

	// Pre-emptive fetch and fast-forward (bd-3s8 fix)
	// This reduces divergence by ensuring we commit on top of latest remote state
	if err := preemptiveFetchAndFastForward(ctx, worktreePath, syncBranch, remote); err != nil {
		// Non-fatal: if fetch fails (e.g., offline), we can still commit locally
		// The divergence will be handled during the next pull
		_ = err
	}

	// Convert absolute path to relative path from repo root
	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get relative JSONL path: %w", err)
	}

	// Sync JSONL file to worktree
	if err := wtMgr.SyncJSONLToWorktree(worktreePath, jsonlRelPath); err != nil {
		return nil, fmt.Errorf("failed to sync JSONL to worktree: %w", err)
	}

	// Also sync other beads files (deletions.jsonl, metadata.json)
	beadsDir := filepath.Dir(jsonlPath)
	for _, filename := range []string{"deletions.jsonl", "metadata.json"} {
		srcPath := filepath.Join(beadsDir, filename)
		if _, err := os.Stat(srcPath); err == nil {
			relPath, err := filepath.Rel(repoRoot, srcPath)
			if err == nil {
				_ = wtMgr.SyncJSONLToWorktree(worktreePath, relPath) // Best effort
			}
		}
	}

	// Check for changes in worktree
	worktreeJSONLPath := filepath.Join(worktreePath, jsonlRelPath)
	hasChanges, err := hasChangesInWorktree(ctx, worktreePath, worktreeJSONLPath)
	if err != nil {
		return nil, fmt.Errorf("failed to check for changes in worktree: %w", err)
	}

	if !hasChanges {
		return result, nil // No changes to commit
	}

	// Commit in worktree
	result.Message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	if err := commitInWorktree(ctx, worktreePath, jsonlRelPath, result.Message); err != nil {
		return nil, fmt.Errorf("failed to commit in worktree: %w", err)
	}
	result.Committed = true

	// Push if enabled
	if push {
		if err := pushFromWorktree(ctx, worktreePath, syncBranch); err != nil {
			return nil, fmt.Errorf("failed to push from worktree: %w", err)
		}
		result.Pushed = true
	}

	return result, nil
}

// preemptiveFetchAndFastForward fetches from remote and fast-forwards if possible.
// This reduces divergence by keeping the local sync branch up-to-date before committing.
// Returns nil on success, or error if fetch/ff fails (caller should treat as non-fatal).
func preemptiveFetchAndFastForward(ctx context.Context, worktreePath, branch, remote string) error {
	// Fetch from remote
	fetchCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "fetch", remote, branch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		// Check if remote branch doesn't exist yet (first sync)
		if strings.Contains(string(output), "couldn't find remote ref") {
			return nil // Not an error - remote branch doesn't exist yet
		}
		return fmt.Errorf("fetch failed: %w", err)
	}

	// Check if we can fast-forward
	localAhead, remoteAhead, err := getDivergence(ctx, worktreePath, branch, remote)
	if err != nil {
		return fmt.Errorf("divergence check failed: %w", err)
	}

	// If remote has new commits and we have no local commits, fast-forward
	if remoteAhead > 0 && localAhead == 0 {
		mergeCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "merge", "--ff-only",
			fmt.Sprintf("%s/%s", remote, branch))
		if output, err := mergeCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("fast-forward failed: %w\n%s", err, output)
		}
	}

	return nil
}

// PullFromSyncBranch pulls changes from the sync branch and copies JSONL to the main repo.
// This fetches remote changes without affecting the user's working directory.
//
// IMPORTANT (bd-3s8 fix): This function handles diverged histories gracefully by performing
// a content-based merge instead of relying on git's commit-level merge. When local and remote
// sync branches have diverged:
//  1. Fetch remote changes (don't pull)
//  2. Find the merge base
//  3. Extract JSONL from base, local, and remote
//  4. Perform 3-way content merge using bd's merge algorithm
//  5. Reset to remote's history (adopt remote commit graph)
//  6. Commit merged content on top
//
// IMPORTANT (bd-7ch): After successful content merge, auto-pushes to remote by default.
// Includes safety check: warns (but doesn't block) if >50% issues vanished AND >5 existed.
// "Vanished" means removed from issues.jsonl entirely, NOT status=closed.
//
// This ensures sync never fails due to git merge conflicts, as we handle merging at the
// JSONL content level where we have semantic understanding of the data.
//
// Parameters:
//   - ctx: Context for cancellation
//   - repoRoot: Path to the git repository root
//   - syncBranch: Name of the sync branch (e.g., "beads-sync")
//   - jsonlPath: Absolute path to the JSONL file in the main repo
//   - push: If true, push to remote after merge (bd-7ch)
//
// Returns PullResult with details about what was done, or error if failed.
func PullFromSyncBranch(ctx context.Context, repoRoot, syncBranch, jsonlPath string, push bool) (*PullResult, error) {
	result := &PullResult{
		Branch:    syncBranch,
		JSONLPath: jsonlPath,
	}

	// Worktree path is under .git/beads-worktrees/<branch>
	worktreePath := filepath.Join(repoRoot, ".git", "beads-worktrees", syncBranch)

	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)

	// Ensure worktree exists
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w", err)
	}

	// Get remote name
	remote := getRemoteForBranch(ctx, worktreePath, syncBranch)

	// Convert absolute path to relative path from repo root
	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get relative JSONL path: %w", err)
	}

	// Step 1: Fetch from remote (don't pull - we handle merge ourselves)
	fetchCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "fetch", remote, syncBranch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		// Check if remote branch doesn't exist yet (first sync)
		if strings.Contains(string(output), "couldn't find remote ref") {
			// Remote branch doesn't exist - nothing to pull
			result.Pulled = false
			return result, nil
		}
		return nil, fmt.Errorf("git fetch failed in worktree: %w\n%s", err, output)
	}

	// Step 2: Check for divergence
	localAhead, remoteAhead, err := getDivergence(ctx, worktreePath, syncBranch, remote)
	if err != nil {
		return nil, fmt.Errorf("failed to check divergence: %w", err)
	}

	// Case 1: Already up to date (remote has nothing new)
	if remoteAhead == 0 {
		result.Pulled = true
		// Still copy JSONL in case worktree has uncommitted changes
		if err := copyJSONLToMainRepo(worktreePath, jsonlRelPath, jsonlPath); err != nil {
			return nil, err
		}
		return result, nil
	}

	// Case 2: Can fast-forward (we have no local commits ahead of remote)
	if localAhead == 0 {
		// Simple fast-forward merge
		mergeCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "merge", "--ff-only",
			fmt.Sprintf("%s/%s", remote, syncBranch))
		if output, err := mergeCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git merge --ff-only failed: %w\n%s", err, output)
		}
		result.Pulled = true
		result.FastForwarded = true

		// Copy JSONL to main repo
		if err := copyJSONLToMainRepo(worktreePath, jsonlRelPath, jsonlPath); err != nil {
			return nil, err
		}
		return result, nil
	}

	// Case 3: DIVERGED - perform content-based merge (bd-3s8 fix)
	// This is the key fix: instead of git merge (which can fail), we:
	// 1. Extract JSONL content from base, local, and remote
	// 2. Merge at content level using our 3-way merge algorithm
	// 3. Reset to remote's commit history
	// 4. Commit merged content on top

	// bd-7ch: Extract local content before merge for safety check
	localContent, _ := extractJSONLFromCommit(ctx, worktreePath, "HEAD", jsonlRelPath)

	mergedContent, err := performContentMerge(ctx, worktreePath, syncBranch, remote, jsonlRelPath)
	if err != nil {
		return nil, fmt.Errorf("content merge failed: %w", err)
	}

	// Also merge deletions.jsonl if it exists
	beadsRelDir := filepath.Dir(jsonlRelPath)
	deletionsRelPath := filepath.Join(beadsRelDir, "deletions.jsonl")
	mergedDeletions, deletionsErr := performDeletionsMerge(ctx, worktreePath, syncBranch, remote, deletionsRelPath)
	// deletionsErr is non-fatal - file might not exist

	// Reset worktree to remote's history (adopt their commit graph)
	resetCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "reset", "--hard",
		fmt.Sprintf("%s/%s", remote, syncBranch))
	if output, err := resetCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git reset failed: %w\n%s", err, output)
	}

	// Write merged content
	worktreeJSONLPath := filepath.Join(worktreePath, jsonlRelPath)
	if err := os.MkdirAll(filepath.Dir(worktreeJSONLPath), 0750); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(worktreeJSONLPath, mergedContent, 0600); err != nil {
		return nil, fmt.Errorf("failed to write merged JSONL: %w", err)
	}

	// Write merged deletions if we have them
	if deletionsErr == nil && len(mergedDeletions) > 0 {
		deletionsPath := filepath.Join(worktreePath, deletionsRelPath)
		if err := os.WriteFile(deletionsPath, mergedDeletions, 0600); err != nil {
			// Non-fatal - deletions are supplementary
			_ = err
		}
	}

	// Check if merge produced any changes from remote
	hasChanges, err := hasChangesInWorktree(ctx, worktreePath, worktreeJSONLPath)
	if err != nil {
		return nil, fmt.Errorf("failed to check for changes: %w", err)
	}

	// Commit merged content if there are changes
	if hasChanges {
		message := fmt.Sprintf("bd sync: merge divergent histories (%d local + %d remote commits)",
			localAhead, remoteAhead)
		if err := commitInWorktree(ctx, worktreePath, jsonlRelPath, message); err != nil {
			return nil, fmt.Errorf("failed to commit merged content: %w", err)
		}
	}

	result.Pulled = true
	result.Merged = true

	// Copy merged JSONL to main repo
	if err := copyJSONLToMainRepo(worktreePath, jsonlRelPath, jsonlPath); err != nil {
		return nil, err
	}

	// bd-7ch: Auto-push after successful content merge
	if push && hasChanges {
		// Safety check: count issues before and after merge to detect mass deletion
		localCount := countIssuesInContent(localContent)
		mergedCount := countIssuesInContent(mergedContent)

		// Warn if >50% issues vanished AND >5 existed before
		// "Vanished" = removed from JSONL entirely (not status=closed)
		if localCount > 5 && mergedCount < localCount {
			vanishedPercent := float64(localCount-mergedCount) / float64(localCount) * 100
			if vanishedPercent > 50 {
				fmt.Fprintf(os.Stderr, "⚠️  Warning: %.0f%% of issues vanished during merge (%d → %d issues)\n",
					vanishedPercent, localCount, mergedCount)
				fmt.Fprintf(os.Stderr, "   This may indicate accidental mass deletion. Pushing anyway.\n")
				fmt.Fprintf(os.Stderr, "   If this was unintended, use 'git reflog' on the sync branch to recover.\n")
			}
		}

		// Push regardless of safety check (don't block happy path)
		if err := pushFromWorktree(ctx, worktreePath, syncBranch); err != nil {
			return nil, fmt.Errorf("failed to push after merge: %w", err)
		}
		result.Pushed = true
	}

	return result, nil
}

// getDivergence returns how many commits local is ahead and behind remote.
// Returns (localAhead, remoteAhead, error)
func getDivergence(ctx context.Context, worktreePath, branch, remote string) (int, int, error) {
	// Use rev-list to count commits in each direction
	// --left-right --count gives us "local\tremote"
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-list",
		"--left-right", "--count",
		fmt.Sprintf("HEAD...%s/%s", remote, branch))
	output, err := cmd.Output()
	if err != nil {
		// If this fails, remote branch might not exist locally yet
		// Check if it's a tracking issue
		return 0, 0, fmt.Errorf("failed to get divergence: %w", err)
	}

	// Parse "N\tM" format
	parts := strings.Fields(strings.TrimSpace(string(output)))
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %s", output)
	}

	localAhead, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse local ahead count: %w", err)
	}

	remoteAhead, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse remote ahead count: %w", err)
	}

	return localAhead, remoteAhead, nil
}

// performContentMerge extracts JSONL from base, local, and remote, then merges content.
// Returns the merged JSONL content.
func performContentMerge(ctx context.Context, worktreePath, branch, remote, jsonlRelPath string) ([]byte, error) {
	// Find merge base
	mergeBaseCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "merge-base",
		"HEAD", fmt.Sprintf("%s/%s", remote, branch))
	mergeBaseOutput, err := mergeBaseCmd.Output()
	if err != nil {
		// No common ancestor - treat as empty base
		mergeBaseOutput = nil
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOutput))

	// Create temp files for 3-way merge
	tmpDir, err := os.MkdirTemp("", "bd-merge-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	baseFile := filepath.Join(tmpDir, "base.jsonl")
	localFile := filepath.Join(tmpDir, "local.jsonl")
	remoteFile := filepath.Join(tmpDir, "remote.jsonl")
	outputFile := filepath.Join(tmpDir, "merged.jsonl")

	// Extract base JSONL (may not exist if this is first divergence)
	if mergeBase != "" {
		baseContent, err := extractJSONLFromCommit(ctx, worktreePath, mergeBase, jsonlRelPath)
		if err != nil {
			// Base file might not exist in ancestor - use empty file
			baseContent = []byte{}
		}
		if err := os.WriteFile(baseFile, baseContent, 0600); err != nil {
			return nil, fmt.Errorf("failed to write base file: %w", err)
		}
	} else {
		// No merge base - use empty file
		if err := os.WriteFile(baseFile, []byte{}, 0600); err != nil {
			return nil, fmt.Errorf("failed to write empty base file: %w", err)
		}
	}

	// Extract local JSONL (current HEAD in worktree)
	localContent, err := extractJSONLFromCommit(ctx, worktreePath, "HEAD", jsonlRelPath)
	if err != nil {
		// Local file might not exist - use empty
		localContent = []byte{}
	}
	if err := os.WriteFile(localFile, localContent, 0600); err != nil {
		return nil, fmt.Errorf("failed to write local file: %w", err)
	}

	// Extract remote JSONL
	remoteRef := fmt.Sprintf("%s/%s", remote, branch)
	remoteContent, err := extractJSONLFromCommit(ctx, worktreePath, remoteRef, jsonlRelPath)
	if err != nil {
		// Remote file might not exist - use empty
		remoteContent = []byte{}
	}
	if err := os.WriteFile(remoteFile, remoteContent, 0600); err != nil {
		return nil, fmt.Errorf("failed to write remote file: %w", err)
	}

	// Perform 3-way merge using bd's merge algorithm
	// The merge function writes to outputFile (first arg) and returns error if conflicts
	err = merge.Merge3Way(outputFile, baseFile, localFile, remoteFile, false)
	if err != nil {
		// Check if it's a conflict error
		if strings.Contains(err.Error(), "merge completed with") {
			// There were conflicts - this is rare for JSONL since most fields can be
			// auto-merged. When it happens, it means both sides changed the same field
			// to different values. We fail here rather than writing corrupt JSONL.
			return nil, fmt.Errorf("merge conflict: %w (manual resolution required)", err)
		}
		return nil, fmt.Errorf("3-way merge failed: %w", err)
	}

	// Read merged result
	mergedContent, err := os.ReadFile(outputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read merged file: %w", err)
	}

	return mergedContent, nil
}

// extractJSONLFromCommit extracts a file's content from a specific git commit.
func extractJSONLFromCommit(ctx context.Context, worktreePath, commit, filePath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "show",
		fmt.Sprintf("%s:%s", commit, filePath))
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to extract %s from %s: %w", filePath, commit, err)
	}
	return output, nil
}

// performDeletionsMerge merges deletions.jsonl from local and remote.
// Deletions are merged by union - we keep all deletion records from both sides.
// This ensures that if either side deleted an issue, it stays deleted.
func performDeletionsMerge(ctx context.Context, worktreePath, branch, remote, deletionsRelPath string) ([]byte, error) {
	// Extract local deletions
	localDeletions, localErr := extractJSONLFromCommit(ctx, worktreePath, "HEAD", deletionsRelPath)

	// Extract remote deletions
	remoteRef := fmt.Sprintf("%s/%s", remote, branch)
	remoteDeletions, remoteErr := extractJSONLFromCommit(ctx, worktreePath, remoteRef, deletionsRelPath)

	// If neither exists, nothing to merge
	if localErr != nil && remoteErr != nil {
		return nil, fmt.Errorf("no deletions files to merge")
	}

	// If only one exists, use that
	if localErr != nil {
		return remoteDeletions, nil
	}
	if remoteErr != nil {
		return localDeletions, nil
	}

	// Both exist - merge by taking union of lines (deduplicated)
	// Each line in deletions.jsonl is a JSON object with an "id" field
	seen := make(map[string]bool)
	var merged []byte

	// Process local deletions
	for _, line := range strings.Split(string(localDeletions), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !seen[line] {
			seen[line] = true
			merged = append(merged, []byte(line+"\n")...)
		}
	}

	// Process remote deletions
	for _, line := range strings.Split(string(remoteDeletions), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !seen[line] {
			seen[line] = true
			merged = append(merged, []byte(line+"\n")...)
		}
	}

	return merged, nil
}

// copyJSONLToMainRepo copies JSONL and related files from worktree to main repo.
func copyJSONLToMainRepo(worktreePath, jsonlRelPath, jsonlPath string) error {
	worktreeJSONLPath := filepath.Join(worktreePath, jsonlRelPath)

	// Check if worktree JSONL exists
	if _, err := os.Stat(worktreeJSONLPath); os.IsNotExist(err) {
		// No JSONL in worktree yet, nothing to sync
		return nil
	}

	// Copy JSONL from worktree to main repo
	data, err := os.ReadFile(worktreeJSONLPath)
	if err != nil {
		return fmt.Errorf("failed to read worktree JSONL: %w", err)
	}

	if err := os.WriteFile(jsonlPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write main JSONL: %w", err)
	}

	// Also sync other beads files back (deletions.jsonl, metadata.json)
	beadsDir := filepath.Dir(jsonlPath)
	worktreeBeadsDir := filepath.Dir(worktreeJSONLPath)
	for _, filename := range []string{"deletions.jsonl", "metadata.json"} {
		worktreeSrcPath := filepath.Join(worktreeBeadsDir, filename)
		if fileData, err := os.ReadFile(worktreeSrcPath); err == nil {
			dstPath := filepath.Join(beadsDir, filename)
			_ = os.WriteFile(dstPath, fileData, 0600) // Best effort, match JSONL permissions
		}
	}

	return nil
}

// hasChangesInWorktree checks if there are uncommitted changes in the worktree
func hasChangesInWorktree(ctx context.Context, worktreePath, filePath string) (bool, error) {
	// Check the entire .beads directory for changes
	beadsDir := filepath.Dir(filePath)
	relBeadsDir, err := filepath.Rel(worktreePath, beadsDir)
	if err != nil {
		// Fallback to checking just the file
		relPath, err := filepath.Rel(worktreePath, filePath)
		if err != nil {
			return false, fmt.Errorf("failed to make path relative: %w", err)
		}
		cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "status", "--porcelain", relPath)
		output, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("git status failed in worktree: %w", err)
		}
		return len(strings.TrimSpace(string(output))) > 0, nil
	}

	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "status", "--porcelain", relBeadsDir)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed in worktree: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// commitInWorktree stages and commits changes in the worktree
func commitInWorktree(ctx context.Context, worktreePath, jsonlRelPath, message string) error {
	// Stage the entire .beads directory
	beadsRelDir := filepath.Dir(jsonlRelPath)

	addCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "add", beadsRelDir)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed in worktree: %w", err)
	}

	// Commit with --no-verify to skip hooks (pre-commit hook would fail in worktree context)
	// The worktree is internal to bd sync, so we don't need to run bd's pre-commit hook
	commitCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "commit", "--no-verify", "-m", message)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed in worktree: %w\n%s", err, output)
	}

	return nil
}

// pushFromWorktree pushes the sync branch from the worktree
func pushFromWorktree(ctx context.Context, worktreePath, branch string) error {
	remote := getRemoteForBranch(ctx, worktreePath, branch)

	// Push with explicit remote and branch, set upstream if not set
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "push", "--set-upstream", remote, branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed from worktree: %w\n%s", err, output)
	}

	return nil
}

// getRemoteForBranch gets the remote name for a branch, defaulting to "origin"
func getRemoteForBranch(ctx context.Context, worktreePath, branch string) string {
	remoteCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "config", "--get", fmt.Sprintf("branch.%s.remote", branch))
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		return "origin" // Default
	}
	return strings.TrimSpace(string(remoteOutput))
}

// GetRepoRoot returns the git repository root directory
func GetRepoRoot(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get git root: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// countIssuesInContent counts the number of non-empty lines in JSONL content.
// Each non-empty line represents one issue. Used for safety checks (bd-7ch).
func countIssuesInContent(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// HasGitRemote checks if any git remote exists
func HasGitRemote(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "git", "remote")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}
