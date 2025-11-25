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
	"github.com/steveyegge/beads/internal/syncbranch"
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

Use --flush-only to just export pending changes to JSONL (useful for pre-commit hooks).
Use --import-only to just import from JSONL (useful after git pull).
Use --status to show diff between sync branch and main branch.
Use --merge to merge the sync branch back to main branch.`,
	Run: func(cmd *cobra.Command, _ []string) {
		ctx := rootCtx

		message, _ := cmd.Flags().GetString("message")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		noPush, _ := cmd.Flags().GetBool("no-push")
		noPull, _ := cmd.Flags().GetBool("no-pull")
		renameOnImport, _ := cmd.Flags().GetBool("rename-on-import")
		flushOnly, _ := cmd.Flags().GetBool("flush-only")
		importOnly, _ := cmd.Flags().GetBool("import-only")
		status, _ := cmd.Flags().GetBool("status")
		merge, _ := cmd.Flags().GetBool("merge")
		fromMain, _ := cmd.Flags().GetBool("from-main")

		// Find JSONL path
		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			fmt.Fprintf(os.Stderr, "Error: not in a bd workspace (no .beads directory found)\n")
			os.Exit(1)
		}

		// If status mode, show diff between sync branch and main
		if status {
			if err := showSyncStatus(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// If merge mode, merge sync branch to main
		if merge {
			if err := mergeSyncBranch(ctx, dryRun); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// If from-main mode, one-way sync from main branch (gt-ick9: ephemeral branch support)
		if fromMain {
			if err := doSyncFromMain(ctx, jsonlPath, renameOnImport, dryRun); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// If import-only mode, just import and exit
		if importOnly {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would import from JSONL")
			} else {
				fmt.Println("→ Importing from JSONL...")
				if err := importFromJSONL(ctx, jsonlPath, renameOnImport); err != nil {
					fmt.Fprintf(os.Stderr, "Error importing: %v\n", err)
					os.Exit(1)
				}
				fmt.Println("✓ Import complete")
			}
			return
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
		// If no upstream, automatically switch to --from-main mode (gt-ick9: ephemeral branch support)
		if !noPull && !gitHasUpstream() {
			if hasGitRemote(ctx) {
				// Remote exists but no upstream - use from-main mode
				fmt.Println("→ No upstream configured, using --from-main mode")
				if err := doSyncFromMain(ctx, jsonlPath, renameOnImport, dryRun); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				return
			}
			// If no remote at all, gitPull/gitPush will gracefully skip
		}

		// Step 1: Export pending changes (but check for stale DB first)
		skipExport := false // Track if we should skip export due to ZFC import
		if dryRun {
			fmt.Println("→ [DRY RUN] Would export pending changes to JSONL")
		} else {
			// ZFC safety check (bd-l0r): if DB significantly diverges from JSONL,
			// force import first to sync with JSONL source of truth
			// After import, skip export to prevent overwriting JSONL (JSONL is source of truth)
			if err := ensureStoreActive(); err == nil && store != nil {
				dbCount, err := countDBIssuesFast(ctx, store)
				if err == nil {
					jsonlCount, err := countIssuesInJSONL(jsonlPath)
					if err == nil && jsonlCount > 0 && dbCount > jsonlCount {
						divergence := float64(dbCount-jsonlCount) / float64(jsonlCount)
						if divergence > 0.5 { // >50% more issues in DB than JSONL
							fmt.Printf("→ DB has %d issues but JSONL has %d (stale DB detected)\n", dbCount, jsonlCount)
							fmt.Println("→ Importing JSONL first (ZFC)...")
							if err := importFromJSONL(ctx, jsonlPath, renameOnImport); err != nil {
								fmt.Fprintf(os.Stderr, "Error importing (ZFC): %v\n", err)
								os.Exit(1)
							}
							// Skip export after ZFC import - JSONL is source of truth
							skipExport = true
							fmt.Println("→ Skipping export (JSONL is source of truth after ZFC import)")
						}
					}
				}
			}

			if !skipExport {
				// Pre-export integrity checks
				if err := ensureStoreActive(); err == nil && store != nil {
					if err := validatePreExport(ctx, store, jsonlPath); err != nil {
						fmt.Fprintf(os.Stderr, "Pre-export validation failed: %v\n", err)
						os.Exit(1)
					}
					if err := checkDuplicateIDs(ctx, store); err != nil {
						fmt.Fprintf(os.Stderr, "Database corruption detected: %v\n", err)
						os.Exit(1)
					}
					if orphaned, err := checkOrphanedDeps(ctx, store); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: orphaned dependency check failed: %v\n", err)
					} else if len(orphaned) > 0 {
						fmt.Fprintf(os.Stderr, "Warning: found %d orphaned dependencies: %v\n", len(orphaned), orphaned)
					}
				}

				fmt.Println("→ Exporting pending changes to JSONL...")
				if err := exportToJSONL(ctx, jsonlPath); err != nil {
					fmt.Fprintf(os.Stderr, "Error exporting: %v\n", err)
					os.Exit(1)
				}
			}

			// Capture left snapshot (pre-pull state) for 3-way merge
			// This is mandatory for deletion tracking integrity
			if err := captureLeftSnapshot(jsonlPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to capture snapshot (required for deletion tracking): %v\n", err)
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
		// Note: If no upstream, we already handled it above with --from-main mode
		if !noPull {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would pull from remote")
			} else {
				// Check merge driver configuration before pulling
				checkMergeDriverConfig()

				fmt.Println("→ Pulling from remote...")
				err := gitPull(ctx)
				if err != nil {
					// Check if it's a rebase conflict on beads.jsonl that we can auto-resolve
					if isInRebase() && hasJSONLConflict() {
						fmt.Println("→ Auto-resolving JSONL merge conflict...")

						// Export clean JSONL from DB (database is source of truth)
						if exportErr := exportToJSONL(ctx, jsonlPath); exportErr != nil {
							fmt.Fprintf(os.Stderr, "Error: failed to export for conflict resolution: %v\n", exportErr)
							fmt.Fprintf(os.Stderr, "Hint: resolve conflicts manually and run 'bd import' then 'bd sync' again\n")
							os.Exit(1)
						}

						// Mark conflict as resolved
						addCmd := exec.CommandContext(ctx, "git", "add", jsonlPath)
						if addErr := addCmd.Run(); addErr != nil {
							fmt.Fprintf(os.Stderr, "Error: failed to mark conflict resolved: %v\n", addErr)
							fmt.Fprintf(os.Stderr, "Hint: resolve conflicts manually and run 'bd import' then 'bd sync' again\n")
							os.Exit(1)
						}

						// Continue rebase
						if continueErr := runGitRebaseContinue(ctx); continueErr != nil {
							fmt.Fprintf(os.Stderr, "Error: failed to continue rebase: %v\n", continueErr)
							fmt.Fprintf(os.Stderr, "Hint: resolve conflicts manually and run 'bd import' then 'bd sync' again\n")
							os.Exit(1)
						}

						fmt.Println("✓ Auto-resolved JSONL conflict")
					} else {
						// Not an auto-resolvable conflict, fail with original error
						fmt.Fprintf(os.Stderr, "Error pulling: %v\n", err)

						// Check if this looks like a merge driver failure
						errStr := err.Error()
						if strings.Contains(errStr, "merge driver") ||
						   strings.Contains(errStr, "no such file or directory") ||
						   strings.Contains(errStr, "MERGE DRIVER INVOKED") {
							fmt.Fprintf(os.Stderr, "\nThis may be caused by an incorrect merge driver configuration.\n")
							fmt.Fprintf(os.Stderr, "Fix: bd doctor --fix\n\n")
						}

						fmt.Fprintf(os.Stderr, "Hint: resolve conflicts manually and run 'bd import' then 'bd sync' again\n")
						os.Exit(1)
					}
				}

				// Count issues before import for validation
				var beforeCount int
				if err := ensureStoreActive(); err == nil && store != nil {
					beforeCount, err = countDBIssues(ctx, store)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to count issues before import: %v\n", err)
					}
				}

				// Step 3.5: Perform 3-way merge and prune deletions
				if err := ensureStoreActive(); err == nil && store != nil {
					if err := applyDeletionsFromMerge(ctx, store, jsonlPath); err != nil {
						fmt.Fprintf(os.Stderr, "Error during 3-way merge: %v\n", err)
						os.Exit(1)
					}
				}

				// Step 4: Import updated JSONL after pull
				fmt.Println("→ Importing updated JSONL...")
				if err := importFromJSONL(ctx, jsonlPath, renameOnImport); err != nil {
					fmt.Fprintf(os.Stderr, "Error importing: %v\n", err)
					os.Exit(1)
				}

				// Validate import didn't cause data loss
				if beforeCount > 0 {
					if err := ensureStoreActive(); err == nil && store != nil {
						afterCount, err := countDBIssues(ctx, store)
						if err != nil {
							fmt.Fprintf(os.Stderr, "Warning: failed to count issues after import: %v\n", err)
						} else {
							if err := validatePostImport(beforeCount, afterCount); err != nil {
								fmt.Fprintf(os.Stderr, "Post-import validation failed: %v\n", err)
								os.Exit(1)
							}
						}
					}
				}

				// Post-pull ZFC check: if skipExport was set by initial ZFC detection,
				// or if DB has more issues than JSONL, skip re-export.
				// This prevents resurrection of deleted issues when syncing stale clones.
				skipReexport := skipExport // Carry forward initial ZFC detection
				if !skipReexport {
					if err := ensureStoreActive(); err == nil && store != nil {
						dbCountPostImport, dbErr := countDBIssuesFast(ctx, store)
						jsonlCountPostPull, jsonlErr := countIssuesInJSONL(jsonlPath)
						if dbErr == nil && jsonlErr == nil && jsonlCountPostPull > 0 {
							// Skip re-export if DB has more issues than JSONL (any amount)
							if dbCountPostImport > jsonlCountPostPull {
								fmt.Printf("→ DB (%d) has more issues than JSONL (%d) after pull\n",
									dbCountPostImport, jsonlCountPostPull)
								fmt.Println("→ Trusting JSONL as source of truth (skipping re-export)")
								fmt.Println("  Hint: Run 'bd import --delete-missing' to fully sync DB with JSONL")
								skipReexport = true
							}
						}
					}
				}

				// Step 4.5: Check if DB needs re-export (only if DB differs from JSONL)
				// This prevents the infinite loop: import → export → commit → dirty again
				if !skipReexport {
					if err := ensureStoreActive(); err == nil && store != nil {
						needsExport, err := dbNeedsExport(ctx, store, jsonlPath)
						if err != nil {
							fmt.Fprintf(os.Stderr, "Warning: failed to check if export needed: %v\n", err)
							// Conservative: assume export needed
							needsExport = true
						}

						if needsExport {
							fmt.Println("→ Re-exporting after import to sync DB changes...")
							if err := exportToJSONL(ctx, jsonlPath); err != nil {
								fmt.Fprintf(os.Stderr, "Error re-exporting after import: %v\n", err)
								os.Exit(1)
							}

							// Step 4.6: Commit the re-export if it created changes
							hasPostImportChanges, err := gitHasChanges(ctx, jsonlPath)
							if err != nil {
								fmt.Fprintf(os.Stderr, "Error checking git status after re-export: %v\n", err)
								os.Exit(1)
							}
							if hasPostImportChanges {
								fmt.Println("→ Committing DB changes from import...")
								if err := gitCommit(ctx, jsonlPath, "bd sync: apply DB changes after import"); err != nil {
									fmt.Fprintf(os.Stderr, "Error committing post-import changes: %v\n", err)
									os.Exit(1)
								}
								hasChanges = true // Mark that we have changes to push
							}
						} else {
							fmt.Println("→ DB and JSONL in sync, skipping re-export")
						}
					}
				}

				// Update base snapshot after successful import
				if err := updateBaseSnapshot(jsonlPath); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update base snapshot: %v\n", err)
				}

				// Clean up temporary snapshot files after successful merge
				sm := NewSnapshotManager(jsonlPath)
				if err := sm.Cleanup(); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to clean up snapshots: %v\n", err)
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
	syncCmd.Flags().Bool("import-only", false, "Only import from JSONL (skip git operations, useful after git pull)")
	syncCmd.Flags().Bool("status", false, "Show diff between sync branch and main branch")
	syncCmd.Flags().Bool("merge", false, "Merge sync branch back to main branch")
	syncCmd.Flags().Bool("from-main", false, "One-way sync from main branch (for ephemeral branches without upstream)")
	syncCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output sync statistics in JSON format")
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
// Uses git config directly for compatibility with Git for Windows
func gitHasUpstream() bool {
	// Get current branch name
	branchCmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return false
	}
	branch := strings.TrimSpace(string(branchOutput))
	
	// Check if remote and merge refs are configured
	remoteCmd := exec.Command("git", "config", "--get", fmt.Sprintf("branch.%s.remote", branch))
	mergeCmd := exec.Command("git", "config", "--get", fmt.Sprintf("branch.%s.merge", branch))
	
	remoteErr := remoteCmd.Run()
	mergeErr := mergeCmd.Run()
	
	return remoteErr == nil && mergeErr == nil
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

// hasGitRemote checks if a git remote exists in the repository
func hasGitRemote(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "git", "remote")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

// isInRebase checks if we're currently in a git rebase state
func isInRebase() bool {
	// Check for rebase-merge directory (interactive rebase)
	if _, err := os.Stat(".git/rebase-merge"); err == nil {
		return true
	}
	// Check for rebase-apply directory (non-interactive rebase)
	if _, err := os.Stat(".git/rebase-apply"); err == nil {
		return true
	}
	return false
}

// hasJSONLConflict checks if the beads JSONL file has a merge conflict
// Returns true only if the JSONL file (issues.jsonl or beads.jsonl) is the only file in conflict
func hasJSONLConflict() bool {
	cmd := exec.Command("git", "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false
	}

	var hasJSONLConflict bool
	var hasOtherConflict bool

	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 3 {
			continue
		}

		// Check for unmerged status codes (UU = both modified, AA = both added, etc.)
		status := line[:2]
		if status == "UU" || status == "AA" || status == "DD" ||
			status == "AU" || status == "UA" || status == "DU" || status == "UD" {
			filepath := strings.TrimSpace(line[3:])

			// Check for beads JSONL files (issues.jsonl or beads.jsonl in .beads/)
			if strings.HasSuffix(filepath, "issues.jsonl") || strings.HasSuffix(filepath, "beads.jsonl") {
				hasJSONLConflict = true
			} else {
				hasOtherConflict = true
			}
		}
	}

	// Only return true if ONLY the JSONL file has a conflict
	return hasJSONLConflict && !hasOtherConflict
}

// runGitRebaseContinue continues a rebase after resolving conflicts
func runGitRebaseContinue(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "rebase", "--continue")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git rebase --continue failed: %w\n%s", err, output)
	}
	return nil
}

// gitPull pulls from the current branch's upstream
// Returns nil if no remote configured (local-only mode)
func checkMergeDriverConfig() {
	// Get current merge driver configuration
	cmd := exec.Command("git", "config", "merge.beads.driver")
	output, err := cmd.Output()
	if err != nil {
		// No merge driver configured - this is OK, user may not need it
		return
	}

	currentConfig := strings.TrimSpace(string(output))

	// Check if using old incorrect placeholders
	if strings.Contains(currentConfig, "%L") || strings.Contains(currentConfig, "%R") {
		fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: Git merge driver is misconfigured!\n")
		fmt.Fprintf(os.Stderr, "   Current: %s\n", currentConfig)
		fmt.Fprintf(os.Stderr, "   Problem: Git only supports %%O (base), %%A (current), %%B (other)\n")
		fmt.Fprintf(os.Stderr, "            Using %%L/%%R will cause merge failures!\n")
		fmt.Fprintf(os.Stderr, "\n   Fix now: bd doctor --fix\n")
		fmt.Fprintf(os.Stderr, "   Or manually: git config merge.beads.driver \"bd merge %%A %%O %%A %%B\"\n\n")
	}
}

func gitPull(ctx context.Context) error {
	// Check if any remote exists (bd-biwp: support local-only repos)
	if !hasGitRemote(ctx) {
		return nil // Gracefully skip - local-only mode
	}
	
	// Get current branch name
	// Use symbolic-ref to work in fresh repos without commits (bd-flil)
	branchCmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOutput))
	
	// Get remote name for current branch (usually "origin")
	remoteCmd := exec.CommandContext(ctx, "git", "config", "--get", fmt.Sprintf("branch.%s.remote", branch))
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		// If no remote configured, default to "origin"
		remoteOutput = []byte("origin\n")
	}
	remote := strings.TrimSpace(string(remoteOutput))
	
	// Pull with explicit remote and branch
	cmd := exec.CommandContext(ctx, "git", "pull", remote, branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull failed: %w\n%s", err, output)
	}
	return nil
}

// gitPush pushes to the current branch's upstream
// Returns nil if no remote configured (local-only mode)
func gitPush(ctx context.Context) error {
	// Check if any remote exists (bd-biwp: support local-only repos)
	if !hasGitRemote(ctx) {
		return nil // Gracefully skip - local-only mode
	}

	cmd := exec.CommandContext(ctx, "git", "push")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %w\n%s", err, output)
	}
	return nil
}

// getDefaultBranch returns the default branch name (main or master)
// Checks remote HEAD first, then falls back to checking if main/master exist
func getDefaultBranch(ctx context.Context) string {
	// Try to get default branch from remote
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	output, err := cmd.Output()
	if err == nil {
		ref := strings.TrimSpace(string(output))
		// Extract branch name from refs/remotes/origin/main
		if strings.HasPrefix(ref, "refs/remotes/origin/") {
			return strings.TrimPrefix(ref, "refs/remotes/origin/")
		}
	}

	// Fallback: check if origin/main exists
	if exec.CommandContext(ctx, "git", "rev-parse", "--verify", "origin/main").Run() == nil {
		return "main"
	}

	// Fallback: check if origin/master exists
	if exec.CommandContext(ctx, "git", "rev-parse", "--verify", "origin/master").Run() == nil {
		return "master"
	}

	// Default to main
	return "main"
}

// doSyncFromMain performs a one-way sync from the default branch (main/master)
// Used for ephemeral branches without upstream tracking (gt-ick9)
// This fetches beads from main and imports them, discarding local beads changes.
func doSyncFromMain(ctx context.Context, jsonlPath string, renameOnImport bool, dryRun bool) error {
	if dryRun {
		fmt.Println("→ [DRY RUN] Would sync beads from main branch")
		fmt.Println("  1. Fetch origin main")
		fmt.Println("  2. Checkout .beads/ from origin/main")
		fmt.Println("  3. Import JSONL into database")
		fmt.Println("\n✓ Dry run complete (no changes made)")
		return nil
	}

	// Check if we're in a git repository
	if !isGitRepo() {
		return fmt.Errorf("not in a git repository")
	}

	// Check if remote exists
	if !hasGitRemote(ctx) {
		return fmt.Errorf("no git remote configured")
	}

	defaultBranch := getDefaultBranch(ctx)

	// Step 1: Fetch from main
	fmt.Printf("→ Fetching from origin/%s...\n", defaultBranch)
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", defaultBranch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch origin %s failed: %w\n%s", defaultBranch, err, output)
	}

	// Step 2: Checkout .beads/ directory from main
	fmt.Printf("→ Checking out beads from origin/%s...\n", defaultBranch)
	checkoutCmd := exec.CommandContext(ctx, "git", "checkout", fmt.Sprintf("origin/%s", defaultBranch), "--", ".beads/")
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout .beads/ from origin/%s failed: %w\n%s", defaultBranch, err, output)
	}

	// Step 3: Import JSONL
	fmt.Println("→ Importing JSONL...")
	if err := importFromJSONL(ctx, jsonlPath, renameOnImport); err != nil {
		return fmt.Errorf("import failed: %w", err)
	}

	fmt.Println("\n✓ Sync from main complete")
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

	// Update last_import_hash metadata to enable content-based staleness detection (bd-khnb fix)
	// After export, database and JSONL are in sync, so update hash to prevent unnecessary auto-import
	if currentHash, err := computeJSONLHash(jsonlPath); err == nil {
		if err := store.SetMetadata(ctx, "last_import_hash", currentHash); err != nil {
			// Non-fatal warning: Metadata update failures are intentionally non-fatal to prevent blocking
			// successful exports. System degrades gracefully to mtime-based staleness detection if metadata
			// is unavailable. This ensures export operations always succeed even if metadata storage fails.
			fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_hash: %v\n", err)
		}
		exportTime := time.Now().Format(time.RFC3339)
		if err := store.SetMetadata(ctx, "last_import_time", exportTime); err != nil {
			// Non-fatal warning (see above comment about graceful degradation)
			fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_time: %v\n", err)
		}
		// Note: mtime tracking removed in bd-v0y fix (git doesn't preserve mtime)
	}

	// Update database mtime to be >= JSONL mtime (fixes #278, #301, #321)
	// This prevents validatePreExport from incorrectly blocking on next export
	beadsDir := filepath.Dir(jsonlPath)
	dbPath := filepath.Join(beadsDir, "beads.db")
	if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
		// Non-fatal warning
		fmt.Fprintf(os.Stderr, "Warning: failed to update database mtime: %v\n", err)
	}

	return nil
}

// getCurrentBranch returns the name of the current git branch
// Uses symbolic-ref instead of rev-parse to work in fresh repos without commits (bd-flil)
func getCurrentBranch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// getSyncBranch returns the configured sync branch name
func getSyncBranch(ctx context.Context) (string, error) {
	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		return "", fmt.Errorf("failed to initialize store: %w", err)
	}

	syncBranch, err := syncbranch.Get(ctx, store)
	if err != nil {
		return "", fmt.Errorf("failed to get sync branch config: %w", err)
	}

	if syncBranch == "" {
		return "", fmt.Errorf("sync.branch not configured (run 'bd config set sync.branch <branch-name>')")
	}

	return syncBranch, nil
}

// showSyncStatus shows the diff between sync branch and main branch
func showSyncStatus(ctx context.Context) error {
	if !isGitRepo() {
		return fmt.Errorf("not in a git repository")
	}

	currentBranch, err := getCurrentBranch(ctx)
	if err != nil {
		return err
	}

	syncBranch, err := getSyncBranch(ctx)
	if err != nil {
		return err
	}

	// Check if sync branch exists
	checkCmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+syncBranch)
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("sync branch '%s' does not exist", syncBranch)
	}

	fmt.Printf("Current branch: %s\n", currentBranch)
	fmt.Printf("Sync branch: %s\n\n", syncBranch)

	// Show commit diff
	fmt.Println("Commits in sync branch not in main:")
	logCmd := exec.CommandContext(ctx, "git", "log", "--oneline", currentBranch+".."+syncBranch)
	logOutput, err := logCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get commit log: %w\n%s", err, logOutput)
	}

	if len(strings.TrimSpace(string(logOutput))) == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Print(string(logOutput))
	}

	fmt.Println("\nCommits in main not in sync branch:")
	logCmd = exec.CommandContext(ctx, "git", "log", "--oneline", syncBranch+".."+currentBranch)
	logOutput, err = logCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get commit log: %w\n%s", err, logOutput)
	}

	if len(strings.TrimSpace(string(logOutput))) == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Print(string(logOutput))
	}

	// Show file diff for .beads/beads.jsonl
	fmt.Println("\nFile differences in .beads/beads.jsonl:")
	diffCmd := exec.CommandContext(ctx, "git", "diff", currentBranch+"..."+syncBranch, "--", ".beads/beads.jsonl")
	diffOutput, err := diffCmd.CombinedOutput()
	if err != nil {
		// diff returns non-zero when there are differences, which is fine
		if len(diffOutput) == 0 {
			return fmt.Errorf("failed to get diff: %w", err)
		}
	}

	if len(strings.TrimSpace(string(diffOutput))) == 0 {
		fmt.Println("  (no differences)")
	} else {
		fmt.Print(string(diffOutput))
	}

	return nil
}

// mergeSyncBranch merges the sync branch back to main
func mergeSyncBranch(ctx context.Context, dryRun bool) error {
	if !isGitRepo() {
		return fmt.Errorf("not in a git repository")
	}

	currentBranch, err := getCurrentBranch(ctx)
	if err != nil {
		return err
	}

	syncBranch, err := getSyncBranch(ctx)
	if err != nil {
		return err
	}

	// Check if sync branch exists
	checkCmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+syncBranch)
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("sync branch '%s' does not exist", syncBranch)
	}

	// Verify we're on the main branch (not the sync branch)
	if currentBranch == syncBranch {
		return fmt.Errorf("cannot merge while on sync branch '%s' (checkout main branch first)", syncBranch)
	}

	// Check if main branch is clean
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		return fmt.Errorf("main branch has uncommitted changes, please commit or stash them first")
	}

	if dryRun {
		fmt.Printf("[DRY RUN] Would merge branch '%s' into '%s'\n", syncBranch, currentBranch)

		// Show what would be merged
		logCmd := exec.CommandContext(ctx, "git", "log", "--oneline", currentBranch+".."+syncBranch)
		logOutput, err := logCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to preview commits: %w", err)
		}

		if len(strings.TrimSpace(string(logOutput))) > 0 {
			fmt.Println("\nCommits that would be merged:")
			fmt.Print(string(logOutput))
		} else {
			fmt.Println("\nNo commits to merge (already up to date)")
		}

		return nil
	}

	// Perform the merge
	fmt.Printf("Merging branch '%s' into '%s'...\n", syncBranch, currentBranch)

	mergeCmd := exec.CommandContext(ctx, "git", "merge", "--no-ff", syncBranch, "-m",
		fmt.Sprintf("Merge %s into %s", syncBranch, currentBranch))
	mergeOutput, err := mergeCmd.CombinedOutput()
	if err != nil {
		// Check if it's a merge conflict
		if strings.Contains(string(mergeOutput), "CONFLICT") || strings.Contains(string(mergeOutput), "conflict") {
			fmt.Fprintf(os.Stderr, "Merge conflict detected:\n%s\n", mergeOutput)
			fmt.Fprintf(os.Stderr, "\nTo resolve:\n")
			fmt.Fprintf(os.Stderr, "1. Resolve conflicts in the affected files\n")
			fmt.Fprintf(os.Stderr, "2. Stage resolved files: git add <files>\n")
			fmt.Fprintf(os.Stderr, "3. Complete merge: git commit\n")
			fmt.Fprintf(os.Stderr, "4. After merge commit, run 'bd import' to sync database\n")
			return fmt.Errorf("merge conflict - see above for resolution steps")
		}
		return fmt.Errorf("merge failed: %w\n%s", err, mergeOutput)
	}

	fmt.Print(string(mergeOutput))
	fmt.Println("\n✓ Merge complete")

	// Suggest next steps
	fmt.Println("\nNext steps:")
	fmt.Println("1. Review the merged changes")
	fmt.Println("2. Run 'bd sync --import-only' to sync the database with merged JSONL")
	fmt.Println("3. Run 'bd sync' to push changes to remote")

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
	args := []string{"import", "-i", jsonlPath}
	if renameOnImport {
		args = append(args, "--rename-on-import")
	}

	// Run import command
	cmd := exec.CommandContext(ctx, exe, args...) // #nosec G204 - bd import command from trusted binary
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("import failed: %w\n%s", err, output)
	}
	
	// Show output (import command provides the summary)
	if len(output) > 0 {
		fmt.Print(string(output))
	}
	
	return nil
}
