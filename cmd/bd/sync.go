package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/syncbranch"
)

var syncCmd = &cobra.Command{
	Use:     "sync",
	GroupID: "sync",
	Short:   "Export database to JSONL (sync with git)",
	Long: `Export database to JSONL for git synchronization.

By default, exports the current database state to JSONL.
Does NOT stage or commit - that's the user's job.

Commands:
  bd sync              Export to JSONL (prep for push)
  bd sync --import     Import from JSONL (after pull)
  bd sync --status     Show sync state
  bd sync --resolve    Resolve conflicts (uses configured strategy)
  bd sync --force      Force full export/import (skip incremental)
  bd sync --full       Full sync: pull → merge → export → commit → push (legacy)

Conflict Resolution:
  bd sync --resolve              Use configured conflict.strategy
  bd sync --resolve --ours       Keep local versions
  bd sync --resolve --theirs     Keep remote versions
  bd sync --resolve --manual     Interactive resolution with prompts

The --manual flag shows a diff for each conflict and prompts you to choose:
  l/local  - Keep local version
  r/remote - Keep remote version
  m/merge  - Auto-merge (LWW for scalars, union for collections)
  s/skip   - Skip (keep local, conflict remains for later)
  a/all    - Accept auto-merge for all remaining conflicts
  q/quit   - Quit and skip all remaining conflicts
  d/diff   - Show full JSON diff

The --full flag provides the legacy full sync behavior for backwards compatibility.`,
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
		importFlag, _ := cmd.Flags().GetBool("import")
		status, _ := cmd.Flags().GetBool("status")
		merge, _ := cmd.Flags().GetBool("merge")
		fromMain, _ := cmd.Flags().GetBool("from-main")
		noGitHistory, _ := cmd.Flags().GetBool("no-git-history")
		squash, _ := cmd.Flags().GetBool("squash")
		checkIntegrity, _ := cmd.Flags().GetBool("check")
		acceptRebase, _ := cmd.Flags().GetBool("accept-rebase")
		fullSync, _ := cmd.Flags().GetBool("full")
		resolve, _ := cmd.Flags().GetBool("resolve")
		resolveOurs, _ := cmd.Flags().GetBool("ours")
		resolveTheirs, _ := cmd.Flags().GetBool("theirs")
		resolveManual, _ := cmd.Flags().GetBool("manual")
		forceFlag, _ := cmd.Flags().GetBool("force")

		// --import is shorthand for --import-only
		if importFlag {
			importOnly = true
		}

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
		//
		// GH#984: Must use fallbackToDirectMode() instead of just closing daemon.
		// When connected to daemon, PersistentPreRun skips store initialization.
		// Just closing daemon leaves store=nil, causing "no database store available"
		// errors in post-checkout hook's `bd sync --import-only`.
		if daemonClient != nil {
			debug.Logf("sync: forcing direct mode for consistency")
			if err := fallbackToDirectMode("sync requires direct database access"); err != nil {
				FatalError("failed to initialize direct mode: %v", err)
			}
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

		// Handle --set-mode flag
		setMode, _ := cmd.Flags().GetString("set-mode")
		if setMode != "" {
			if err := SetSyncMode(ctx, store, setMode); err != nil {
				FatalError("failed to set sync mode: %v", err)
			}
			fmt.Printf("✓ Sync mode set to: %s (%s)\n", setMode, SyncModeDescription(setMode))
			return
		}

		// Find JSONL path
		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			FatalError("not in a bd workspace (no .beads directory found)")
		}

		// If status mode, show sync state (new format per spec)
		if status {
			if err := showSyncStateStatus(ctx, jsonlPath); err != nil {
				FatalError("%v", err)
			}
			return
		}

		// If resolve mode, resolve conflicts
		if resolve {
			strategy := config.GetConflictStrategy() // use configured default
			if resolveOurs {
				strategy = config.ConflictStrategyOurs
			} else if resolveTheirs {
				strategy = config.ConflictStrategyTheirs
			} else if resolveManual {
				strategy = config.ConflictStrategyManual
			}
			if err := resolveSyncConflicts(ctx, jsonlPath, strategy, dryRun); err != nil {
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
				if err := importFromJSONLInline(ctx, jsonlPath, renameOnImport, noGitHistory, false); err != nil {
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

		// DEFAULT BEHAVIOR: Export to JSONL only (per spec)
		// Does NOT stage or commit - that's the user's job.
		// Use --full for legacy full sync behavior (pull → merge → export → commit → push)
		if !fullSync {
			if err := doExportSync(ctx, jsonlPath, forceFlag, dryRun); err != nil {
				FatalError("%v", err)
			}
			return
		}

		// FULL SYNC MODE (--full flag): Legacy behavior
		// Pull → Merge → Export → Commit → Push

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
				if rc, err := beads.GetRepoContext(); err == nil {
					syncBranchRepoRoot = rc.RepoRoot
				}
			}
		}
		hasSyncBranchConfig := syncBranchName != ""

		// GH#1166: Block sync if currently on the sync branch
		// This must happen BEFORE worktree operations - after entering a worktree,
		// GetCurrentBranch() would return the worktree's branch, not the original.
		if hasSyncBranchConfig {
			if syncbranch.IsSyncBranchSameAsCurrent(ctx, syncBranchName) {
				FatalError("Cannot sync to '%s': it's your current branch. "+
					"Checkout a different branch first, or use a dedicated sync branch like 'beads-sync'.",
					syncBranchName)
			}
		}

		// bd-wayc3: Check for redirect + sync-branch incompatibility
		// Redirect and sync-branch are mutually exclusive:
		// - Redirect says: "My database is in another repo (I am a client)"
		// - Sync-branch says: "I own my database and sync it myself via worktree"
		// When redirect is active, the sync-branch worktree operations fail because
		// the beads files are in a different git repo than the current working directory.
		redirectInfo := beads.GetRedirectInfo()
		if redirectInfo.IsRedirected {
			if hasSyncBranchConfig {
				fmt.Printf("⚠️  Redirect active (-> %s), skipping sync-branch operations\n", redirectInfo.TargetDir)
				fmt.Println("   Hint: Redirected clones should not have sync-branch configured")
				fmt.Println("   The owner of the target .beads directory handles sync-branch")
			} else {
				fmt.Printf("→ Redirect active (-> %s)\n", redirectInfo.TargetDir)
			}
			// For redirected clones, just do import/export - skip all git operations
			// The target repo's owner (e.g., mayor) handles git commit/push via sync-branch
			if dryRun {
				fmt.Println("→ [DRY RUN] Would export to JSONL (redirected clone, git operations skipped)")
				fmt.Println("✓ Dry run complete (no changes made)")
			} else {
				fmt.Println("→ Exporting to JSONL (redirected clone, skipping git operations)...")
				if err := exportToJSONL(ctx, jsonlPath); err != nil {
					FatalError("exporting: %v", err)
				}
				fmt.Println("✓ Export complete (target repo owner handles git sync)")
			}
			return
		}

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
	// Mode-specific pull behavior:
	// - dolt-native/belt-and-suspenders with Dolt remote: Pull from Dolt
	// - sync.branch configured: Pull from sync branch via worktree
	// - Default (git-portable): Normal git pull
	syncMode := GetSyncMode(ctx, store)
	shouldUseDolt := ShouldUseDoltRemote(ctx, store)

	if shouldUseDolt {
		// Try Dolt pull for dolt-native and belt-and-suspenders modes
		rs, ok := storage.AsRemote(store)
		if ok {
			fmt.Println("→ Pulling from Dolt remote...")
			if err := rs.Pull(ctx); err != nil {
				// Don't fail if no remote configured
				if strings.Contains(err.Error(), "remote") {
					fmt.Println("⚠ No Dolt remote configured, skipping Dolt pull")
				} else {
					return fmt.Errorf("dolt pull failed: %w", err)
				}
			} else {
				fmt.Println("✓ Pulled from Dolt remote")
			}
		} else if syncMode == SyncModeDoltNative {
			return fmt.Errorf("dolt-native sync mode requires Dolt backend")
		}
		// For belt-and-suspenders, continue with git pull even if Dolt pull failed
	}

	// Git-based pull (for git-portable, belt-and-suspenders, or when Dolt not available)
	if ShouldExportJSONL(ctx, store) {
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
	}

	// For dolt-native mode, we're done after pulling from Dolt remote
	// Dolt handles merging internally, no JSONL workflow needed
	if syncMode == SyncModeDoltNative {
		fmt.Println("\n✓ Sync complete (dolt-native mode)")
		return nil
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

// doExportSync exports the current database state based on sync mode.
// - git-portable, realtime: Export to JSONL
// - dolt-native: Commit and push to Dolt remote (skip JSONL)
// - belt-and-suspenders: Both JSONL export and Dolt push
// Does NOT stage or commit to git - that's the user's job.
func doExportSync(ctx context.Context, jsonlPath string, force, dryRun bool) error {
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	syncMode := GetSyncMode(ctx, store)
	shouldExportJSONL := ShouldExportJSONL(ctx, store)
	shouldUseDolt := ShouldUseDoltRemote(ctx, store)

	if dryRun {
		if shouldExportJSONL {
			fmt.Println("→ [DRY RUN] Would export database to JSONL")
		}
		if shouldUseDolt {
			fmt.Println("→ [DRY RUN] Would commit and push to Dolt remote")
		}
		return nil
	}

	// Handle Dolt remote operations for dolt-native and belt-and-suspenders modes
	if shouldUseDolt {
		rs, ok := storage.AsRemote(store)
		if !ok {
			if syncMode == SyncModeDoltNative {
				return fmt.Errorf("dolt-native sync mode requires Dolt backend (current backend doesn't support remote operations)")
			}
			// belt-and-suspenders: warn but continue with JSONL
			fmt.Println("⚠ Dolt remote not available, falling back to JSONL-only")
		} else {
			fmt.Println("→ Committing to Dolt...")
			if err := rs.Commit(ctx, "bd sync: auto-commit"); err != nil {
				// Ignore "nothing to commit" errors
				if !strings.Contains(err.Error(), "nothing to commit") {
					return fmt.Errorf("dolt commit failed: %w", err)
				}
			}

			fmt.Println("→ Pushing to Dolt remote...")
			if err := rs.Push(ctx); err != nil {
				// Don't fail if no remote configured
				if !strings.Contains(err.Error(), "remote") {
					return fmt.Errorf("dolt push failed: %w", err)
				}
				fmt.Println("⚠ No Dolt remote configured, skipping push")
			} else {
				fmt.Println("✓ Pushed to Dolt remote")
			}
		}
	}

	// Export to JSONL for git-portable, realtime, and belt-and-suspenders modes
	if shouldExportJSONL {
		fmt.Println("Exporting beads to JSONL...")

		// Get count of dirty (changed) issues for incremental tracking
		var changedCount int
		if !force {
			dirtyIDs, err := store.GetDirtyIssues(ctx)
			if err != nil {
				debug.Logf("warning: failed to get dirty issues: %v", err)
			} else {
				changedCount = len(dirtyIDs)
			}
		}

		// Export to JSONL
		result, err := exportToJSONLDeferred(ctx, jsonlPath)
		if err != nil {
			return fmt.Errorf("exporting: %w", err)
		}

		// Finalize export (update metadata)
		finalizeExport(ctx, result)

		// Report results
		totalCount := 0
		if result != nil {
			totalCount = len(result.ExportedIDs)
		}

		if changedCount > 0 && !force {
			fmt.Printf("✓ Exported %d issues (%d changed since last sync)\n", totalCount, changedCount)
		} else {
			fmt.Printf("✓ Exported %d issues\n", totalCount)
		}
		fmt.Printf("✓ %s updated\n", jsonlPath)
	}

	return nil
}

// showSyncStateStatus shows the current sync state per the spec.
// Output format:
//
//	Sync mode: git-portable
//	Last export: 2026-01-16 10:30:00 (commit abc123)
//	Pending changes: 3 issues modified since last export
//	Import branch: none
//	Conflicts: none
func showSyncStateStatus(ctx context.Context, jsonlPath string) error {
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	beadsDir := filepath.Dir(jsonlPath)

	// Sync mode (from config)
	syncCfg := config.GetSyncConfig()
	fmt.Printf("Sync mode: %s (%s)\n", syncCfg.Mode, SyncModeDescription(string(syncCfg.Mode)))
	fmt.Printf("  Export on: %s, Import on: %s\n", syncCfg.ExportOn, syncCfg.ImportOn)

	// Conflict strategy
	conflictCfg := config.GetConflictConfig()
	fmt.Printf("Conflict strategy: %s\n", conflictCfg.Strategy)

	// Federation config (if set)
	fedCfg := config.GetFederationConfig()
	if fedCfg.Remote != "" {
		fmt.Printf("Federation remote: %s\n", fedCfg.Remote)
		if fedCfg.Sovereignty != "" {
			fmt.Printf("  Sovereignty: %s\n", fedCfg.Sovereignty)
		}
	}

	// Last export time
	lastExport, err := store.GetMetadata(ctx, "last_import_time")
	if err != nil || lastExport == "" {
		fmt.Println("Last export: never")
	} else {
		// Try to parse and format nicely
		t, err := time.Parse(time.RFC3339Nano, lastExport)
		if err != nil {
			fmt.Printf("Last export: %s\n", lastExport)
		} else {
			// Try to get the last commit hash for the JSONL file
			commitHash := getLastJSONLCommitHash(ctx, jsonlPath)
			if commitHash != "" {
				fmt.Printf("Last export: %s (commit %s)\n", t.Format("2006-01-02 15:04:05"), commitHash[:7])
			} else {
				fmt.Printf("Last export: %s\n", t.Format("2006-01-02 15:04:05"))
			}
		}
	}

	// Pending changes (dirty issues)
	dirtyIDs, err := store.GetDirtyIssues(ctx)
	if err != nil {
		fmt.Println("Pending changes: unknown (error getting dirty issues)")
	} else if len(dirtyIDs) == 0 {
		fmt.Println("Pending changes: none")
	} else {
		fmt.Printf("Pending changes: %d issues modified since last export\n", len(dirtyIDs))
	}

	// Import branch (sync branch status)
	syncBranch, _ := syncbranch.Get(ctx, store)
	if syncBranch == "" {
		fmt.Println("Import branch: none")
	} else {
		fmt.Printf("Import branch: %s\n", syncBranch)
	}

	// Conflicts - check for sync conflict state file
	syncConflictPath := filepath.Join(beadsDir, "sync_conflicts.json")
	if _, err := os.Stat(syncConflictPath); err == nil {
		conflictState, err := LoadSyncConflictState(beadsDir)
		if err != nil {
			fmt.Println("Conflicts: unknown (error reading sync state)")
		} else if len(conflictState.Conflicts) > 0 {
			fmt.Printf("Conflicts: %d unresolved\n", len(conflictState.Conflicts))
			for _, c := range conflictState.Conflicts {
				fmt.Printf("  - %s: %s\n", c.IssueID, c.Reason)
			}
		} else {
			fmt.Println("Conflicts: none")
		}
	} else {
		fmt.Println("Conflicts: none")
	}

	return nil
}

// getLastJSONLCommitHash returns the short commit hash of the last commit
// that touched the JSONL file, or empty string if unknown.
func getLastJSONLCommitHash(ctx context.Context, jsonlPath string) string {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return ""
	}

	cmd := rc.GitCmd(ctx, "log", "-1", "--format=%h", "--", jsonlPath)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(output))
}

// SyncConflictState tracks pending sync conflicts.
type SyncConflictState struct {
	Conflicts []SyncConflictRecord `json:"conflicts,omitempty"`
}

// SyncConflictRecord represents a conflict detected during sync.
type SyncConflictRecord struct {
	IssueID       string `json:"issue_id"`
	Reason        string `json:"reason"`
	LocalVersion  string `json:"local_version,omitempty"`
	RemoteVersion string `json:"remote_version,omitempty"`
	Strategy      string `json:"strategy,omitempty"` // how it was resolved
}

// LoadSyncConflictState loads the sync conflict state from disk.
func LoadSyncConflictState(beadsDir string) (*SyncConflictState, error) {
	path := filepath.Join(beadsDir, "sync_conflicts.json")
	// #nosec G304 -- path is derived from the workspace .beads directory
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &SyncConflictState{}, nil
		}
		return nil, err
	}

	var state SyncConflictState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SaveSyncConflictState saves the sync conflict state to disk.
func SaveSyncConflictState(beadsDir string, state *SyncConflictState) error {
	path := filepath.Join(beadsDir, "sync_conflicts.json")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ClearSyncConflictState removes the sync conflict state file.
func ClearSyncConflictState(beadsDir string) error {
	path := filepath.Join(beadsDir, "sync_conflicts.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// resolveSyncConflicts resolves pending sync conflicts using the specified strategy.
// Strategies:
//   - "newest": Keep whichever version has the newer updated_at timestamp (default)
//   - "ours": Keep local version
//   - "theirs": Keep remote version
//   - "manual": Interactive resolution with user prompts
func resolveSyncConflicts(ctx context.Context, jsonlPath string, strategy config.ConflictStrategy, dryRun bool) error {
	beadsDir := filepath.Dir(jsonlPath)

	conflictState, err := LoadSyncConflictState(beadsDir)
	if err != nil {
		return fmt.Errorf("loading sync conflicts: %w", err)
	}

	if len(conflictState.Conflicts) == 0 {
		fmt.Println("No conflicts to resolve")
		return nil
	}

	if dryRun {
		fmt.Printf("→ [DRY RUN] Would resolve %d conflicts using '%s' strategy\n", len(conflictState.Conflicts), strategy)
		for _, c := range conflictState.Conflicts {
			fmt.Printf("  - %s: %s\n", c.IssueID, c.Reason)
		}
		return nil
	}

	fmt.Printf("Resolving conflicts using '%s' strategy...\n", strategy)

	// Load base, local, and remote states for merge
	baseIssues, err := loadBaseState(beadsDir)
	if err != nil {
		return fmt.Errorf("loading base state: %w", err)
	}

	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}

	localIssues, err := store.SearchIssues(ctx, "", beads.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("loading local issues: %w", err)
	}

	remoteIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		return fmt.Errorf("loading remote issues: %w", err)
	}

	// Build maps for quick lookup
	baseMap := make(map[string]*beads.Issue)
	for _, issue := range baseIssues {
		baseMap[issue.ID] = issue
	}
	localMap := make(map[string]*beads.Issue)
	for _, issue := range localIssues {
		localMap[issue.ID] = issue
	}
	remoteMap := make(map[string]*beads.Issue)
	for _, issue := range remoteIssues {
		remoteMap[issue.ID] = issue
	}

	// Handle manual strategy with interactive resolution
	if strategy == config.ConflictStrategyManual {
		return resolveSyncConflictsManually(ctx, jsonlPath, beadsDir, conflictState, baseMap, localMap, remoteMap)
	}

	resolved := 0
	for _, conflict := range conflictState.Conflicts {
		local := localMap[conflict.IssueID]
		remote := remoteMap[conflict.IssueID]

		var winner string
		switch strategy {
		case config.ConflictStrategyOurs:
			winner = "local"
		case config.ConflictStrategyTheirs:
			winner = "remote"
		case config.ConflictStrategyNewest:
			fallthrough
		default:
			// Compare updated_at timestamps
			if local != nil && remote != nil {
				if local.UpdatedAt.After(remote.UpdatedAt) {
					winner = "local"
				} else {
					winner = "remote"
				}
			} else if local != nil {
				winner = "local"
			} else {
				winner = "remote"
			}
		}

		fmt.Printf("✓ %s: kept %s", conflict.IssueID, winner)
		if strategy == config.ConflictStrategyNewest {
			fmt.Print(" (newer)")
		}
		fmt.Println()
		resolved++
	}

	// Clear conflicts after resolution
	if err := ClearSyncConflictState(beadsDir); err != nil {
		return fmt.Errorf("clearing conflict state: %w", err)
	}

	// Re-run merge with the resolved conflicts
	mergeResult := MergeIssues(baseIssues, localIssues, remoteIssues)

	// Write merged state
	if err := writeMergedStateToJSONL(jsonlPath, mergeResult.Merged); err != nil {
		return fmt.Errorf("writing merged state: %w", err)
	}

	// Import to database
	if err := importFromJSONLInline(ctx, jsonlPath, false, false, false); err != nil {
		return fmt.Errorf("importing merged state: %w", err)
	}

	// Export to ensure consistency
	if err := exportToJSONL(ctx, jsonlPath); err != nil {
		return fmt.Errorf("exporting: %w", err)
	}

	// Update base state
	finalIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		return fmt.Errorf("reloading final state: %w", err)
	}
	if err := saveBaseState(beadsDir, finalIssues); err != nil {
		return fmt.Errorf("saving base state: %w", err)
	}

	fmt.Printf("✓ Merge complete (%d conflicts resolved)\n", resolved)

	return nil
}

// resolveSyncConflictsManually handles manual conflict resolution with interactive prompts.
func resolveSyncConflictsManually(ctx context.Context, jsonlPath, beadsDir string, conflictState *SyncConflictState,
	baseMap, localMap, remoteMap map[string]*beads.Issue) error {

	// Build interactive conflicts list
	var interactiveConflicts []InteractiveConflict
	for _, c := range conflictState.Conflicts {
		interactiveConflicts = append(interactiveConflicts, InteractiveConflict{
			IssueID: c.IssueID,
			Local:   localMap[c.IssueID],
			Remote:  remoteMap[c.IssueID],
			Base:    baseMap[c.IssueID],
		})
	}

	// Run interactive resolution
	resolvedIssues, skipped, err := resolveConflictsInteractively(interactiveConflicts)
	if err != nil {
		return fmt.Errorf("interactive resolution: %w", err)
	}

	if skipped > 0 {
		fmt.Printf("\n⚠ %d conflict(s) skipped - will remain unresolved\n", skipped)
	}

	if len(resolvedIssues) == 0 && skipped == len(conflictState.Conflicts) {
		fmt.Println("No conflicts were resolved")
		return nil
	}

	// Build the merged issue list:
	// 1. Start with issues that weren't in conflict
	// 2. Add the resolved issues
	conflictIDSet := make(map[string]bool)
	for _, c := range conflictState.Conflicts {
		conflictIDSet[c.IssueID] = true
	}

	// Build resolved issue map for quick lookup
	resolvedMap := make(map[string]*beads.Issue)
	for _, issue := range resolvedIssues {
		if issue != nil {
			resolvedMap[issue.ID] = issue
		}
	}

	// Collect all unique IDs from base, local, remote
	allIDSet := make(map[string]bool)
	for id := range baseMap {
		allIDSet[id] = true
	}
	for id := range localMap {
		allIDSet[id] = true
	}
	for id := range remoteMap {
		allIDSet[id] = true
	}

	// Build final merged list
	var mergedIssues []*beads.Issue
	for id := range allIDSet {
		if conflictIDSet[id] {
			// This was a conflict
			if resolved, ok := resolvedMap[id]; ok {
				// User resolved this conflict - use their choice
				mergedIssues = append(mergedIssues, resolved)
			} else {
				// Skipped - keep local version in output, conflict remains for later
				if local := localMap[id]; local != nil {
					mergedIssues = append(mergedIssues, local)
				}
			}
		} else {
			// Not a conflict - use standard 3-way merge logic
			local := localMap[id]
			remote := remoteMap[id]
			base := baseMap[id]
			merged, _ := MergeIssue(base, local, remote)
			if merged != nil {
				mergedIssues = append(mergedIssues, merged)
			}
		}
	}

	// Clear resolved conflicts (keep skipped ones)
	if skipped == 0 {
		if err := ClearSyncConflictState(beadsDir); err != nil {
			return fmt.Errorf("clearing conflict state: %w", err)
		}
	} else {
		// Update conflict state to only keep skipped conflicts
		var remaining []SyncConflictRecord
		for _, c := range conflictState.Conflicts {
			if _, resolved := resolvedMap[c.IssueID]; !resolved {
				remaining = append(remaining, c)
			}
		}
		conflictState.Conflicts = remaining
		if err := SaveSyncConflictState(beadsDir, conflictState); err != nil {
			return fmt.Errorf("saving updated conflict state: %w", err)
		}
	}

	// Write merged state
	if err := writeMergedStateToJSONL(jsonlPath, mergedIssues); err != nil {
		return fmt.Errorf("writing merged state: %w", err)
	}

	// Import to database
	if err := importFromJSONLInline(ctx, jsonlPath, false, false, false); err != nil {
		return fmt.Errorf("importing merged state: %w", err)
	}

	// Export to ensure consistency
	if err := exportToJSONL(ctx, jsonlPath); err != nil {
		return fmt.Errorf("exporting: %w", err)
	}

	// Update base state
	finalIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		return fmt.Errorf("reloading final state: %w", err)
	}
	if err := saveBaseState(beadsDir, finalIssues); err != nil {
		return fmt.Errorf("saving base state: %w", err)
	}

	resolvedCount := len(resolvedIssues)
	fmt.Printf("\n✓ Manual resolution complete (%d resolved, %d skipped)\n", resolvedCount, skipped)

	return nil
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
	syncCmd.Flags().Bool("import", false, "Import from JSONL (shorthand for --import-only)")
	syncCmd.Flags().Bool("status", false, "Show sync state (pending changes, last export, conflicts)")
	syncCmd.Flags().Bool("merge", false, "Merge sync branch back to main branch")
	syncCmd.Flags().Bool("from-main", false, "One-way sync from main branch (for ephemeral branches without upstream)")
	syncCmd.Flags().Bool("no-git-history", false, "Skip git history backfill for deletions (use during JSONL filename migrations)")
	syncCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output sync statistics in JSON format")
	syncCmd.Flags().Bool("check", false, "Pre-sync integrity check: detect forced pushes, prefix mismatches, and orphaned issues")
	syncCmd.Flags().Bool("accept-rebase", false, "Accept remote sync branch history (use when force-push detected)")
	syncCmd.Flags().Bool("full", false, "Full sync: pull → merge → export → commit → push (legacy behavior)")
	syncCmd.Flags().Bool("resolve", false, "Resolve pending sync conflicts")
	syncCmd.Flags().Bool("ours", false, "Use 'ours' strategy for conflict resolution (with --resolve)")
	syncCmd.Flags().Bool("theirs", false, "Use 'theirs' strategy for conflict resolution (with --resolve)")
	syncCmd.Flags().Bool("manual", false, "Use interactive manual resolution for conflicts (with --resolve)")
	syncCmd.Flags().Bool("force", false, "Force full export/import (skip incremental optimization)")
	syncCmd.Flags().String("set-mode", "", "Set sync mode (git-portable, realtime, dolt-native, belt-and-suspenders)")
	rootCmd.AddCommand(syncCmd)
}

// Git helper functions moved to sync_git.go

// doSyncFromMain function moved to sync_import.go
// Export function moved to sync_export.go
// Sync branch functions moved to sync_branch.go
// Import functions moved to sync_import.go
// External beads dir functions moved to sync_branch.go
// Integrity check types and functions moved to sync_check.go
