package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Synchronize issues with git remote",
	Long: `Synchronize issues with git remote in a single operation:
1. Export pending changes to JSONL
2. Commit changes to git
3. Pull from remote (with conflict resolution)
4. Import updated JSONL
5. Push local commits to remote

This command wraps the entire git-based sync workflow for multi-device use.

Use --flush-only to just export pending changes to JSONL (useful for pre-commit hooks).`,
	Run: func(cmd *cobra.Command, _ []string) {
		ctx := context.Background()

		message, _ := cmd.Flags().GetString("message")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		noPush, _ := cmd.Flags().GetBool("no-push")
		noPull, _ := cmd.Flags().GetBool("no-pull")
		renameOnImport, _ := cmd.Flags().GetBool("rename-on-import")
		flushOnly, _ := cmd.Flags().GetBool("flush-only")

		// Find JSONL path
		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			fmt.Fprintf(os.Stderr, "Error: not in a bd workspace (no .beads directory found)\n")
			os.Exit(1)
		}

		// If flush-only mode, just export and exit
		if flushOnly {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would export pending changes to JSONL")
			} else {
				if err := exportToJSONL(ctx, jsonlPath); err != nil {
					fmt.Fprintf(os.Stderr, "Error exporting: %v\n", err)
					os.Exit(1)
				}
			}
			return
		}

		// Check if we're in a git repository
		if !isGitRepo() {
			fmt.Fprintf(os.Stderr, "Error: not in a git repository\n")
			fmt.Fprintf(os.Stderr, "Hint: run 'git init' to initialize a repository\n")
			os.Exit(1)
		}

		// Preflight: check for merge/rebase in progress
		if inMerge, err := gitHasUnmergedPaths(); err != nil {
			fmt.Fprintf(os.Stderr, "Error checking git state: %v\n", err)
			os.Exit(1)
		} else if inMerge {
			fmt.Fprintf(os.Stderr, "Error: unmerged paths or merge in progress\n")
			fmt.Fprintf(os.Stderr, "Hint: resolve conflicts, run 'bd import' if needed, then 'bd sync' again\n")
			os.Exit(1)
		}

		// Preflight: check for upstream tracking
		if !noPull && !gitHasUpstream() {
			fmt.Fprintf(os.Stderr, "Error: no upstream configured for current branch\n")
			fmt.Fprintf(os.Stderr, "Hint: git push -u origin <branch-name> (then rerun bd sync)\n")
			os.Exit(1)
		}

		// Step 1: Export pending changes
		if dryRun {
			fmt.Println("→ [DRY RUN] Would export pending changes to JSONL")
		} else {
			fmt.Println("→ Exporting pending changes to JSONL...")
			if err := exportToJSONL(ctx, jsonlPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error exporting: %v\n", err)
				os.Exit(1)
			}
		}

		// Step 2: Check if there are changes to commit
		hasChanges, err := gitHasChanges(ctx, jsonlPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error checking git status: %v\n", err)
			os.Exit(1)
		}

		if hasChanges {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would commit changes to git")
			} else {
				fmt.Println("→ Committing changes to git...")
				if err := gitCommit(ctx, jsonlPath, message); err != nil {
					fmt.Fprintf(os.Stderr, "Error committing: %v\n", err)
					os.Exit(1)
				}
			}
		} else {
			fmt.Println("→ No changes to commit")
		}

		// Step 3: Pull from remote
		if !noPull {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would pull from remote")
			} else {
				fmt.Println("→ Pulling from remote...")
				if err := gitPull(ctx); err != nil {
					fmt.Fprintf(os.Stderr, "Error pulling: %v\n", err)
					fmt.Fprintf(os.Stderr, "Hint: resolve conflicts manually and run 'bd import' then 'bd sync' again\n")
					os.Exit(1)
				}

				// Step 4: Import updated JSONL after pull
				fmt.Println("→ Importing updated JSONL...")
				if err := importFromJSONL(ctx, jsonlPath, renameOnImport); err != nil {
					fmt.Fprintf(os.Stderr, "Error importing: %v\n", err)
					os.Exit(1)
				}
			}
		}

		// Step 5: Push to remote
		if !noPush && hasChanges {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would push to remote")
			} else {
				fmt.Println("→ Pushing to remote...")
				if err := gitPush(ctx); err != nil {
					fmt.Fprintf(os.Stderr, "Error pushing: %v\n", err)
					fmt.Fprintf(os.Stderr, "Hint: pull may have brought new changes, run 'bd sync' again\n")
					os.Exit(1)
				}
			}
		}

		if dryRun {
			fmt.Println("\n✓ Dry run complete (no changes made)")
		} else {
			fmt.Println("\n✓ Sync complete")
		}
	},
}

func init() {
	syncCmd.Flags().StringP("message", "m", "", "Commit message (default: auto-generated)")
	syncCmd.Flags().Bool("dry-run", false, "Preview sync without making changes")
	syncCmd.Flags().Bool("no-push", false, "Skip pushing to remote")
	syncCmd.Flags().Bool("no-pull", false, "Skip pulling from remote")
	syncCmd.Flags().Bool("rename-on-import", false, "Rename imported issues to match database prefix (updates all references)")
	syncCmd.Flags().Bool("flush-only", false, "Only export pending changes to JSONL (skip git operations)")
	rootCmd.AddCommand(syncCmd)
}

// isGitRepo checks if the current directory is in a git repository
func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// gitHasUnmergedPaths checks for unmerged paths or merge in progress
func gitHasUnmergedPaths() (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}

	// Check for unmerged status codes (DD, AU, UD, UA, DU, AA, UU)
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) >= 2 {
			s := line[:2]
			if s == "DD" || s == "AU" || s == "UD" || s == "UA" || s == "DU" || s == "AA" || s == "UU" {
				return true, nil
			}
		}
	}

	// Check if MERGE_HEAD exists (merge in progress)
	if exec.Command("git", "rev-parse", "-q", "--verify", "MERGE_HEAD").Run() == nil {
		return true, nil
	}

	return false, nil
}

// gitHasUpstream checks if the current branch has an upstream configured
func gitHasUpstream() bool {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return cmd.Run() == nil
}

// gitHasChanges checks if the specified file has uncommitted changes
func gitHasChanges(ctx context.Context, filePath string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", filePath)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// gitCommit commits the specified file
func gitCommit(ctx context.Context, filePath string, message string) error {
	// Stage the file
	addCmd := exec.CommandContext(ctx, "git", "add", filePath)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Generate message if not provided
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// Commit
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, output)
	}

	return nil
}

// gitPull pulls from the current branch's upstream
func gitPull(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "pull")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull failed: %w\n%s", err, output)
	}
	return nil
}

// gitPush pushes to the current branch's upstream
func gitPush(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "push")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %w\n%s", err, output)
	}
	return nil
}

// exportToJSONL exports the database to JSONL format
func exportToJSONL(ctx context.Context, jsonlPath string) error {
	// If daemon is running, use RPC
	if daemonClient != nil {
		exportArgs := &rpc.ExportArgs{
			JSONLPath: jsonlPath,
		}
		resp, err := daemonClient.Export(exportArgs)
		if err != nil {
			return fmt.Errorf("daemon export failed: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("daemon export error: %s", resp.Error)
		}
		return nil
	}

	// Direct mode: access store directly
	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}

	// Safety check: prevent exporting empty database over non-empty JSONL
	if len(issues) == 0 {
		existingCount, countErr := countIssuesInJSONL(jsonlPath)
		if countErr != nil {
			// If we can't read the file, it might not exist yet, which is fine
			if !os.IsNotExist(countErr) {
				fmt.Fprintf(os.Stderr, "Warning: failed to read existing JSONL: %v\n", countErr)
			}
		} else if existingCount > 0 {
			return fmt.Errorf("refusing to export empty database over non-empty JSONL file (database: 0 issues, JSONL: %d issues)", existingCount)
		}
	}

	// Warning: check if export would lose >50% of issues
	existingCount, err := countIssuesInJSONL(jsonlPath)
	if err == nil && existingCount > 0 {
		lossPercent := float64(existingCount-len(issues)) / float64(existingCount) * 100
		if lossPercent > 50 {
			fmt.Fprintf(os.Stderr, "WARNING: Export would lose %.1f%% of issues (existing: %d, database: %d)\n",
				lossPercent, existingCount, len(issues))
		}
	}

	// Sort by ID for consistent output
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Populate dependencies for all issues (avoid N+1)
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues
	for _, issue := range issues {
		labels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get labels for %s: %w", issue.ID, err)
		}
		issue.Labels = labels
	}

	// Populate comments for all issues
	for _, issue := range issues {
		comments, err := store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get comments for %s: %w", issue.ID, err)
		}
		issue.Comments = comments
	}

	// Create temp file for atomic write
	dir := filepath.Dir(jsonlPath)
	base := filepath.Base(jsonlPath)
	tempFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	// Write JSONL
	encoder := json.NewEncoder(tempFile)
	exportedIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
		exportedIDs = append(exportedIDs, issue.ID)
	}

	// Close temp file before rename
	_ = tempFile.Close()

	// Atomic replace
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		return fmt.Errorf("failed to replace JSONL file: %w", err)
	}

	// Set appropriate file permissions (0600: rw-------)
	if err := os.Chmod(jsonlPath, 0600); err != nil {
		// Non-fatal warning
		fmt.Fprintf(os.Stderr, "Warning: failed to set file permissions: %v\n", err)
	}

	// Clear dirty flags for exported issues
	if err := store.ClearDirtyIssuesByID(ctx, exportedIDs); err != nil {
		// Non-fatal warning
		fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty flags: %v\n", err)
	}

	// Clear auto-flush state
	clearAutoFlushState()

	return nil
}

// importFromJSONL imports the JSONL file by running the import command
func importFromJSONL(ctx context.Context, jsonlPath string, renameOnImport bool) error {
	// Get current executable path to avoid "./bd" path issues
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve current executable: %w", err)
	}

	// Build args for import command
	args := []string{"import", "-i", jsonlPath, "--resolve-collisions"}
	if renameOnImport {
		args = append(args, "--rename-on-import")
	}

	// Run import command with --resolve-collisions to automatically handle conflicts
	cmd := exec.CommandContext(ctx, exe, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("import failed: %w\n%s", err, output)
	}
	// Suppress output unless there's an error
	return nil
}
