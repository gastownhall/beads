package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/syncbranch"
)

var syncCmd = &cobra.Command{
	Use:     "sync",
	GroupID: "sync",
	Short:   "Synchronize issues with git remote",
	Long: `Synchronize issues with git remote:
1. Pull from remote (fetch + merge)
2. Merge local and remote issues (3-way merge with LWW)
3. Export merged state to JSONL
4. Commit changes to git
5. Push to remote

The 3-way merge algorithm prevents data loss during concurrent edits
by comparing base state with both local and remote changes.

Use --no-pull to skip pulling (just export, commit, push).
Use --squash to accumulate changes without committing (reduces commit noise).
Use --flush-only to just export pending changes to JSONL (useful for pre-commit hooks).
Use --import-only to just import from JSONL (useful after git pull).
Use --status to show diff between sync branch and main branch.
Use --merge to merge the sync branch back to main branch.`,
	Run: func(cmd *cobra.Command, _ []string) {
		CheckReadonly("sync")
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
		noGitHistory, _ := cmd.Flags().GetBool("no-git-history")
		squash, _ := cmd.Flags().GetBool("squash")
		checkIntegrity, _ := cmd.Flags().GetBool("check")
		acceptRebase, _ := cmd.Flags().GetBool("accept-rebase")

		// If --no-push not explicitly set, check no-push config
		if !cmd.Flags().Changed("no-push") {
			noPush = config.GetBool("no-push")
		}

		// Force direct mode for sync operations.
		// This prevents stale daemon SQLite connections from corrupting exports.
		// If the daemon was running but its database file was deleted and recreated
		// (e.g., during recovery), the daemon's SQLite connection points to the old
		// (deleted) file, causing export to return incomplete/corrupt data.
		// Using direct mode ensures we always read from the current database file.
		if daemonClient != nil {
			debug.Logf("sync: forcing direct mode for consistency")
			_ = daemonClient.Close()
			daemonClient = nil
		}

		// Initialize local store after daemon disconnect.
		// When daemon was connected, PersistentPreRun returns early without initializing
		// the store global. Commands like --import-only need the store, so we must
		// initialize it here after closing the daemon connection.
		if err := ensureStoreActive(); err != nil {
			FatalError("failed to initialize store: %v", err)
		}

		// Resolve noGitHistory based on fromMain (fixes #417)
		noGitHistory = resolveNoGitHistoryForFromMain(fromMain, noGitHistory)

		// Find JSONL path
		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			FatalError("not in a bd workspace (no .beads directory found)")
		}

		// If status mode, show diff between sync branch and main
		if status {
			if err := showSyncStatus(ctx); err != nil {
				FatalError("%v", err)
			}
			return
		}

		// If check mode, run pre-sync integrity checks
		if checkIntegrity {
			showSyncIntegrityCheck(ctx, jsonlPath)
			return
		}

		// If merge mode, merge sync branch to main
		if merge {
			if err := mergeSyncBranch(ctx, dryRun); err != nil {
				FatalError("%v", err)
			}
			return
		}

		// If from-main mode, one-way sync from main branch (gt-ick9: ephemeral branch support)
		if fromMain {
			if err := doSyncFromMain(ctx, jsonlPath, renameOnImport, dryRun, noGitHistory); err != nil {
				FatalError("%v", err)
			}
			return
		}

		// If import-only mode, just import and exit
		// Use inline import to avoid subprocess path resolution issues with .beads/redirect (bd-ysal)
		if importOnly {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would import from JSONL")
			} else {
				fmt.Println("→ Importing from JSONL...")
				if err := importFromJSONLInline(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
					FatalError("importing: %v", err)
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
					FatalError("exporting: %v", err)
				}
			}
			return
		}

		// If squash mode, export to JSONL but skip git operations
		// This accumulates changes for a single commit later
		if squash {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would export pending changes to JSONL (squash mode)")
			} else {
				fmt.Println("→ Exporting pending changes to JSONL (squash mode)...")
				if err := exportToJSONL(ctx, jsonlPath); err != nil {
					FatalError("exporting: %v", err)
				}
				fmt.Println("✓ Changes accumulated in JSONL")
				fmt.Println("  Run 'bd sync' (without --squash) to commit all accumulated changes")
			}
			return
		}

		// Check if we're in a git repository
		if !isGitRepo() {
			FatalErrorWithHint("not in a git repository", "run 'git init' to initialize a repository")
		}

		// Preflight: check for merge/rebase in progress
		if inMerge, err := gitHasUnmergedPaths(); err != nil {
			FatalError("checking git state: %v", err)
		} else if inMerge {
			FatalErrorWithHint("unmerged paths or merge in progress", "resolve conflicts, run 'bd import' if needed, then 'bd sync' again")
		}

		// GH#885: Preflight check for uncommitted JSONL changes
		// This detects when a previous sync exported but failed before commit,
		// leaving the JSONL in an inconsistent state across worktrees.
		if hasUncommitted, err := gitHasUncommittedBeadsChanges(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to check for uncommitted changes: %v\n", err)
		} else if hasUncommitted {
			fmt.Println("→ Detected uncommitted JSONL changes (possible incomplete sync)")
			fmt.Println("→ Re-exporting from database to reconcile state...")
			// Force a fresh export to ensure JSONL matches current DB state
			if err := exportToJSONL(ctx, jsonlPath); err != nil {
				FatalError("re-exporting to reconcile state: %v", err)
			}
			fmt.Println("✓ State reconciled")
		}

		// GH#638: Check sync.branch BEFORE upstream check
		// When sync.branch is configured, we should use worktree-based sync even if
		// the current branch has no upstream (e.g., detached HEAD in jj, git worktrees)
		var syncBranchName, syncBranchRepoRoot string
		if err := ensureStoreActive(); err == nil && store != nil {
			if sb, _ := syncbranch.Get(ctx, store); sb != "" {
				syncBranchName = sb
				if rr, err := syncbranch.GetRepoRoot(ctx); err == nil {
					syncBranchRepoRoot = rr
				}
			}
		}
		hasSyncBranchConfig := syncBranchName != ""

		// Preflight: check for upstream tracking
		// If no upstream, automatically switch to --from-main mode (gt-ick9: ephemeral branch support)
		// GH#638: Skip this fallback if sync.branch is explicitly configured
		if !noPull && !gitHasUpstream() && !hasSyncBranchConfig {
			if hasGitRemote(ctx) {
				// Remote exists but no upstream - use from-main mode
				fmt.Println("→ No upstream configured, using --from-main mode")
				// Force noGitHistory=true for auto-detected from-main mode (fixes #417)
				if err := doSyncFromMain(ctx, jsonlPath, renameOnImport, dryRun, true); err != nil {
					FatalError("%v", err)
				}
				return
			}
			// If no remote at all, gitPull/gitPush will gracefully skip
		}

		// Pull-first sync: Pull → Merge → Export → Commit → Push
		// This eliminates the export-before-pull data loss pattern (#911) by
		// seeing remote changes before exporting local state.
		if err := doPullFirstSync(ctx, jsonlPath, renameOnImport, noGitHistory, dryRun, noPush, noPull, message, acceptRebase, syncBranchName, syncBranchRepoRoot); err != nil {
			FatalError("%v", err)
		}
	},
}

// doPullFirstSync implements the pull-first sync flow:
// Pull → Merge → Export → Commit → Push
//
// This eliminates the export-before-pull data loss pattern (#911) by
// seeing remote changes before exporting local state.
//
// The 3-way merge uses:
// - Base state: Last successful sync (.beads/sync_base.jsonl)
// - Local state: Current database contents
// - Remote state: JSONL after git pull
//
// When noPull is true, skips the pull/merge steps and just does:
// Export → Commit → Push
func doPullFirstSync(ctx context.Context, jsonlPath string, renameOnImport, noGitHistory, dryRun, noPush, noPull bool, message string, acceptRebase bool, syncBranch, syncBranchRepoRoot string) error {
	beadsDir := filepath.Dir(jsonlPath)
	_ = acceptRebase // Reserved for future sync branch force-push detection

	if dryRun {
		if noPull {
			fmt.Println("→ [DRY RUN] Would export pending changes to JSONL")
			fmt.Println("→ [DRY RUN] Would commit changes")
			if !noPush {
				fmt.Println("→ [DRY RUN] Would push to remote")
			}
		} else {
			fmt.Println("→ [DRY RUN] Would pull from remote")
			fmt.Println("→ [DRY RUN] Would load base state from sync_base.jsonl")
			fmt.Println("→ [DRY RUN] Would merge base, local, and remote issues (3-way)")
			fmt.Println("→ [DRY RUN] Would export merged state to JSONL")
			fmt.Println("→ [DRY RUN] Would update sync_base.jsonl")
			fmt.Println("→ [DRY RUN] Would commit and push changes")
		}
		fmt.Println("\n✓ Dry run complete (no changes made)")
		return nil
	}

	// If noPull, use simplified export-only flow
	if noPull {
		return doExportOnlySync(ctx, jsonlPath, noPush, message)
	}

	// Step 1: Load local state from DB BEFORE pulling
	// This captures the current DB state before remote changes arrive
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("activating store: %w", err)
	}

	// Derive sync-branch config from parameters (detected at caller)
	hasSyncBranchConfig := syncBranch != ""

	localIssues, err := store.SearchIssues(ctx, "", beads.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("loading local issues: %w", err)
	}
	fmt.Printf("→ Loaded %d local issues from database\n", len(localIssues))

	// Acquire exclusive lock to prevent concurrent sync corruption
	lockPath := filepath.Join(beadsDir, ".sync.lock")
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("acquiring sync lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("another sync is in progress")
	}
	defer func() { _ = lock.Unlock() }()

	// Step 2: Load base state (last successful sync)
	fmt.Println("→ Loading base state...")
	baseIssues, err := loadBaseState(beadsDir)
	if err != nil {
		return fmt.Errorf("loading base state: %w", err)
	}
	if baseIssues == nil {
		fmt.Println("  No base state found (first sync)")
	} else {
		fmt.Printf("  Loaded %d issues from base state\n", len(baseIssues))
	}

	// Step 3: Pull from remote
	// When sync.branch is configured, pull from the sync branch via worktree
	// Otherwise, use normal git pull on the current branch
	if hasSyncBranchConfig {
		fmt.Printf("→ Pulling from sync branch '%s'...\n", syncBranch)
		pullResult, err := syncbranch.PullFromSyncBranch(ctx, syncBranchRepoRoot, syncBranch, jsonlPath, false)
		if err != nil {
			return fmt.Errorf("pulling from sync branch: %w", err)
		}
		// Display any safety warnings from the pull
		for _, warning := range pullResult.SafetyWarnings {
			fmt.Fprintln(os.Stderr, warning)
		}
		if pullResult.Merged {
			fmt.Println("  Merged divergent sync branch histories")
		} else if pullResult.FastForwarded {
			fmt.Println("  Fast-forwarded to remote")
		}
	} else {
		fmt.Println("→ Pulling from remote...")
		if err := gitPull(ctx, ""); err != nil {
			return fmt.Errorf("pulling: %w", err)
		}
	}

	// Step 4: Load remote state from JSONL (after pull)
	remoteIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		return fmt.Errorf("loading remote issues from JSONL: %w", err)
	}
	fmt.Printf("  Loaded %d remote issues from JSONL\n", len(remoteIssues))

	// Step 5: Perform 3-way merge
	fmt.Println("→ Merging base, local, and remote issues (3-way)...")
	mergeResult := MergeIssues(baseIssues, localIssues, remoteIssues)

	// Report merge results
	localCount, remoteCount, sameCount := 0, 0, 0
	for _, strategy := range mergeResult.Strategy {
		switch strategy {
		case StrategyLocal:
			localCount++
		case StrategyRemote:
			remoteCount++
		case StrategySame:
			sameCount++
		}
	}
	fmt.Printf("  Merged: %d issues total\n", len(mergeResult.Merged))
	fmt.Printf("    Local wins: %d, Remote wins: %d, Same: %d, Conflicts (LWW): %d\n",
		localCount, remoteCount, sameCount, mergeResult.Conflicts)

	// Step 6: Import merged state to DB
	// First, write merged result to JSONL so import can read it
	fmt.Println("→ Writing merged state to JSONL...")
	if err := writeMergedStateToJSONL(jsonlPath, mergeResult.Merged); err != nil {
		return fmt.Errorf("writing merged state: %w", err)
	}

	fmt.Println("→ Importing merged state to database...")
	if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
		return fmt.Errorf("importing merged state: %w", err)
	}

	// Step 7: Export from DB to JSONL (ensures DB is source of truth)
	fmt.Println("→ Exporting from database to JSONL...")
	if err := exportToJSONL(ctx, jsonlPath); err != nil {
		return fmt.Errorf("exporting: %w", err)
	}

	// Step 8: Check for changes and commit
	// Step 9: Push to remote
	// When sync.branch is configured, use worktree-based commit/push to sync branch
	// Otherwise, use normal git commit/push on the current branch
	if hasSyncBranchConfig {
		fmt.Printf("→ Committing to sync branch '%s'...\n", syncBranch)
		commitResult, err := syncbranch.CommitToSyncBranch(ctx, syncBranchRepoRoot, syncBranch, jsonlPath, !noPush)
		if err != nil {
			return fmt.Errorf("committing to sync branch: %w", err)
		}
		if commitResult.Committed {
			fmt.Printf("  Committed: %s\n", commitResult.Message)
			if commitResult.Pushed {
				fmt.Println("  Pushed to remote")
			}
		} else {
			fmt.Println("→ No changes to commit")
		}
	} else {
		hasChanges, err := gitHasBeadsChanges(ctx)
		if err != nil {
			return fmt.Errorf("checking git status: %w", err)
		}

		if hasChanges {
			fmt.Println("→ Committing changes...")
			if err := gitCommitBeadsDir(ctx, message); err != nil {
				return fmt.Errorf("committing: %w", err)
			}
		} else {
			fmt.Println("→ No changes to commit")
		}

		// Push to remote
		if !noPush && hasChanges {
			fmt.Println("→ Pushing to remote...")
			if err := gitPush(ctx, ""); err != nil {
				return fmt.Errorf("pushing: %w", err)
			}
		}
	}

	// Step 10: Update base state for next sync (after successful push)
	// Base state only updates after confirmed push to ensure consistency
	fmt.Println("→ Updating base state...")
	// Reload from exported JSONL to capture any normalization from import/export cycle
	finalIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		return fmt.Errorf("reloading final state: %w", err)
	}
	if err := saveBaseState(beadsDir, finalIssues); err != nil {
		return fmt.Errorf("saving base state: %w", err)
	}
	fmt.Printf("  Saved %d issues to base state\n", len(finalIssues))

	// Step 11: Clear sync state on successful sync
	if bd := beads.FindBeadsDir(); bd != "" {
		_ = ClearSyncState(bd)
	}

	fmt.Println("\n✓ Sync complete")
	return nil
}

// doExportOnlySync handles the --no-pull case: just export, commit, and push
func doExportOnlySync(ctx context.Context, jsonlPath string, noPush bool, message string) error {
	beadsDir := filepath.Dir(jsonlPath)

	// Acquire exclusive lock to prevent concurrent sync corruption
	lockPath := filepath.Join(beadsDir, ".sync.lock")
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("acquiring sync lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("another sync is in progress")
	}
	defer func() { _ = lock.Unlock() }()

	// Pre-export integrity checks
	if err := ensureStoreActive(); err == nil && store != nil {
		if err := validatePreExport(ctx, store, jsonlPath); err != nil {
			return fmt.Errorf("pre-export validation failed: %w", err)
		}
		if err := checkDuplicateIDs(ctx, store); err != nil {
			return fmt.Errorf("database corruption detected: %w", err)
		}
		if orphaned, err := checkOrphanedDeps(ctx, store); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: orphaned dependency check failed: %v\n", err)
		} else if len(orphaned) > 0 {
			fmt.Fprintf(os.Stderr, "Warning: found %d orphaned dependencies: %v\n", len(orphaned), orphaned)
		}
	}

	// Template validation before export
	if err := validateOpenIssuesForSync(ctx); err != nil {
		return err
	}

	fmt.Println("→ Exporting pending changes to JSONL...")
	if err := exportToJSONL(ctx, jsonlPath); err != nil {
		return fmt.Errorf("exporting: %w", err)
	}

	// Check for changes and commit
	hasChanges, err := gitHasBeadsChanges(ctx)
	if err != nil {
		return fmt.Errorf("checking git status: %w", err)
	}

	if hasChanges {
		fmt.Println("→ Committing changes...")
		if err := gitCommitBeadsDir(ctx, message); err != nil {
			return fmt.Errorf("committing: %w", err)
		}
	} else {
		fmt.Println("→ No changes to commit")
	}

	// Push to remote
	if !noPush && hasChanges {
		fmt.Println("→ Pushing to remote...")
		if err := gitPush(ctx, ""); err != nil {
			return fmt.Errorf("pushing: %w", err)
		}
	}

	// Clear sync state on successful sync
	if bd := beads.FindBeadsDir(); bd != "" {
		_ = ClearSyncState(bd)
	}

	fmt.Println("\n✓ Sync complete")
	return nil
}

// writeMergedStateToJSONL writes merged issues to JSONL file
func writeMergedStateToJSONL(path string, issues []*beads.Issue) error {
	tempPath := path + ".tmp"
	file, err := os.Create(tempPath) //nolint:gosec // path is trusted internal beads path
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)

	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			_ = file.Close() // Best-effort cleanup
			_ = os.Remove(tempPath)
			return err
		}
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath) // Best-effort cleanup
		return err
	}

	return os.Rename(tempPath, path)
}

func init() {
	syncCmd.Flags().StringP("message", "m", "", "Commit message (default: auto-generated)")
	syncCmd.Flags().Bool("dry-run", false, "Preview sync without making changes")
	syncCmd.Flags().Bool("no-push", false, "Skip pushing to remote")
	syncCmd.Flags().Bool("no-pull", false, "Skip pulling from remote")
	syncCmd.Flags().Bool("rename-on-import", false, "Rename imported issues to match database prefix (updates all references)")
	syncCmd.Flags().Bool("flush-only", false, "Only export pending changes to JSONL (skip git operations)")
	syncCmd.Flags().Bool("squash", false, "Accumulate changes in JSONL without committing (run 'bd sync' later to commit all)")
	syncCmd.Flags().Bool("import-only", false, "Only import from JSONL (skip git operations, useful after git pull)")
	syncCmd.Flags().Bool("status", false, "Show diff between sync branch and main branch")
	syncCmd.Flags().Bool("merge", false, "Merge sync branch back to main branch")
	syncCmd.Flags().Bool("from-main", false, "One-way sync from main branch (for ephemeral branches without upstream)")
	syncCmd.Flags().Bool("no-git-history", false, "Skip git history backfill for deletions (use during JSONL filename migrations)")
	syncCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output sync statistics in JSON format")
	syncCmd.Flags().Bool("check", false, "Pre-sync integrity check: detect forced pushes, prefix mismatches, and orphaned issues")
	syncCmd.Flags().Bool("accept-rebase", false, "Accept remote sync branch history (use when force-push detected)")
	rootCmd.AddCommand(syncCmd)
}

// Git helper functions moved to sync_git.go

// doSyncFromMain function moved to sync_import.go
// Export function moved to sync_export.go
// Sync branch functions moved to sync_branch.go
// Import functions moved to sync_import.go
// External beads dir functions moved to sync_branch.go
// Integrity check types and functions moved to sync_check.go
