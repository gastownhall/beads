package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/types"
)

var syncCmd = &cobra.Command{
	Use:     "sync",
	GroupID: "sync",
	Short:   "Synchronize issues with git remote",
	Long: `Synchronize issues with git remote in a single operation:
1. Export pending changes to JSONL
2. Commit changes to git
3. Pull from remote (with conflict resolution)
4. Import updated JSONL
5. Push local commits to remote

This command wraps the entire git-based sync workflow for multi-device use.

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

		// If --no-push not explicitly set, check no-push config
		if !cmd.Flags().Changed("no-push") {
			noPush = config.GetBool("no-push")
		}

		// bd-sync-corruption fix: Force direct mode for sync operations.
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

		// If check mode, run pre-sync integrity checks (bd-hlsw.1)
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
		if importOnly {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would import from JSONL")
			} else {
				fmt.Println("→ Importing from JSONL...")
				if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
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

		// If squash mode, export to JSONL but skip git operations (bd-o2e)
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

		// GH#638: Check sync.branch BEFORE upstream check
		// When sync.branch is configured, we should use worktree-based sync even if
		// the current branch has no upstream (e.g., detached HEAD in jj, git worktrees)
		var hasSyncBranchConfig bool
		if err := ensureStoreActive(); err == nil && store != nil {
			if syncBranch, _ := syncbranch.Get(ctx, store); syncBranch != "" {
				hasSyncBranchConfig = true
			}
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

		// Step 1: Export pending changes (but check for stale DB first)
		skipExport := false // Track if we should skip export due to ZFC import
		if dryRun {
			fmt.Println("→ [DRY RUN] Would export pending changes to JSONL")
		} else {
			// ZFC safety check (bd-l0r, bd-53c): if DB significantly diverges from JSONL,
			// force import first to sync with JSONL source of truth.
			// After import, skip export to prevent overwriting JSONL (JSONL is source of truth).
			//
			// bd-53c fix: Added REVERSE ZFC check - if JSONL has MORE issues than DB,
			// this indicates the DB is stale and exporting would cause data loss.
			// This catches the case where a fresh/stale clone tries to export an
			// empty or outdated database over a JSONL with many issues.
			if err := ensureStoreActive(); err == nil && store != nil {
				dbCount, err := countDBIssuesFast(ctx, store)
				if err == nil {
					jsonlCount, err := countIssuesInJSONL(jsonlPath)
					if err == nil && jsonlCount > 0 {
						// Case 1: DB has significantly more issues than JSONL
						// (original ZFC check - DB is ahead of JSONL)
						if dbCount > jsonlCount {
							divergence := float64(dbCount-jsonlCount) / float64(jsonlCount)
							if divergence > 0.5 { // >50% more issues in DB than JSONL
								fmt.Printf("→ DB has %d issues but JSONL has %d (stale JSONL detected)\n", dbCount, jsonlCount)
								fmt.Println("→ Importing JSONL first (ZFC)...")
								if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
									FatalError("importing (ZFC): %v", err)
								}
								// Skip export after ZFC import - JSONL is source of truth
								skipExport = true
								fmt.Println("→ Skipping export (JSONL is source of truth after ZFC import)")
							}
						}

						// Case 2 (bd-53c): JSONL has significantly more issues than DB
						// This is the DANGEROUS case - exporting would lose issues!
						// A stale/empty DB exporting over a populated JSONL causes data loss.
						if jsonlCount > dbCount && !skipExport {
							divergence := float64(jsonlCount-dbCount) / float64(jsonlCount)
							// Use stricter threshold for this dangerous case:
							// - Any loss > 20% is suspicious
							// - Complete loss (DB empty) is always blocked
							if dbCount == 0 || divergence > 0.2 {
								fmt.Printf("→ JSONL has %d issues but DB has only %d (stale DB detected - bd-53c)\n", jsonlCount, dbCount)
								fmt.Println("→ Importing JSONL first to prevent data loss...")
								if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
									FatalError("importing (reverse ZFC): %v", err)
								}
								// Skip export after import - JSONL is source of truth
								skipExport = true
								fmt.Println("→ Skipping export (JSONL is source of truth after reverse ZFC import)")
							}
						}
					}
				}

				// Case 3 (bd-f2f): JSONL content differs from DB (hash mismatch)
				// This catches the case where counts match but STATUS/content differs.
				// A stale DB exporting wrong status values over correct JSONL values
				// causes corruption that the 3-way merge propagates.
				//
				// Example: Remote has status=open, stale DB has status=closed (count=5 both)
				// Without this check: export writes status=closed → git merge keeps it → corruption
				// With this check: detect hash mismatch → import first → get correct status
				//
				// Note: Auto-import in autoflush.go also checks for hash changes during store
				// initialization, so this check may be redundant in most cases. However, it
				// provides defense-in-depth for cases where auto-import is disabled or bypassed.
				if !skipExport {
					repoKey := getRepoKeyForPath(jsonlPath)
					if hasJSONLChanged(ctx, store, jsonlPath, repoKey) {
						fmt.Println("→ JSONL content differs from last sync (bd-f2f)")
						fmt.Println("→ Importing JSONL first to prevent stale DB from overwriting changes...")
						if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
							FatalError("importing (bd-f2f hash mismatch): %v", err)
						}
						// Don't skip export - we still want to export any remaining local dirty issues
						// The import updated DB with JSONL content, and export will write merged state
						fmt.Println("→ Import complete, continuing with export of merged state")
					}
				}
			}

			if !skipExport {
				// Pre-export integrity checks
				if err := ensureStoreActive(); err == nil && store != nil {
					if err := validatePreExport(ctx, store, jsonlPath); err != nil {
						FatalError("pre-export validation failed: %v", err)
					}
					if err := checkDuplicateIDs(ctx, store); err != nil {
						FatalError("database corruption detected: %v", err)
					}
					if orphaned, err := checkOrphanedDeps(ctx, store); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: orphaned dependency check failed: %v\n", err)
					} else if len(orphaned) > 0 {
						fmt.Fprintf(os.Stderr, "Warning: found %d orphaned dependencies: %v\n", len(orphaned), orphaned)
					}
				}

				fmt.Println("→ Exporting pending changes to JSONL...")
				if err := exportToJSONL(ctx, jsonlPath); err != nil {
					FatalError("exporting: %v", err)
				}
			}

			// Capture left snapshot (pre-pull state) for 3-way merge
			// This is mandatory for deletion tracking integrity
			if err := captureLeftSnapshot(jsonlPath); err != nil {
				FatalError("failed to capture snapshot (required for deletion tracking): %v", err)
			}
		}

		// Check if BEADS_DIR points to an external repository (dand-oss fix)
		// If so, use direct git operations instead of worktree-based sync
		beadsDir := filepath.Dir(jsonlPath)
		isExternal := isExternalBeadsDir(ctx, beadsDir)

		if isExternal {
			// External BEADS_DIR: commit/pull directly to the beads repo
			fmt.Println("→ External BEADS_DIR detected, using direct commit...")

			// Check for changes in the external beads repo
			externalRepoRoot, err := getRepoRootFromPath(ctx, beadsDir)
			if err != nil {
				FatalError("%v", err)
			}

			// Check if there are changes to commit
			relBeadsDir, _ := filepath.Rel(externalRepoRoot, beadsDir)
			statusCmd := exec.CommandContext(ctx, "git", "-C", externalRepoRoot, "status", "--porcelain", relBeadsDir)
			statusOutput, _ := statusCmd.Output()
			externalHasChanges := len(strings.TrimSpace(string(statusOutput))) > 0

			if externalHasChanges {
				if dryRun {
					fmt.Printf("→ [DRY RUN] Would commit changes to external beads repo at %s\n", externalRepoRoot)
				} else {
					committed, err := commitToExternalBeadsRepo(ctx, beadsDir, message, !noPush)
					if err != nil {
						FatalError("%v", err)
					}
					if committed {
						if !noPush {
							fmt.Println("✓ Committed and pushed to external beads repo")
						} else {
							fmt.Println("✓ Committed to external beads repo")
						}
					}
				}
			} else {
				fmt.Println("→ No changes to commit in external beads repo")
			}

			if !noPull {
				if dryRun {
					fmt.Printf("→ [DRY RUN] Would pull from external beads repo at %s\n", externalRepoRoot)
				} else {
					fmt.Println("→ Pulling from external beads repo...")
					if err := pullFromExternalBeadsRepo(ctx, beadsDir); err != nil {
						FatalError("pulling: %v", err)
					}
					fmt.Println("✓ Pulled from external beads repo")

					// Re-import after pull to update local database
					fmt.Println("→ Importing JSONL...")
					if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
						FatalError("importing: %v", err)
					}
				}
			}

			fmt.Println("\n✓ Sync complete")
			return
		}

		// Check if sync.branch is configured for worktree-based sync (bd-e3w)
		// This allows committing to a separate branch without changing the user's working directory
		var syncBranchName string
		var repoRoot string
		var useSyncBranch bool
		var onSyncBranch bool // GH#519: track if we're on the sync branch
		if err := ensureStoreActive(); err == nil && store != nil {
			syncBranchName, _ = syncbranch.Get(ctx, store)
			if syncBranchName != "" && syncbranch.HasGitRemote(ctx) {
				repoRoot, err = syncbranch.GetRepoRoot(ctx)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: sync.branch configured but failed to get repo root: %v\n", err)
					fmt.Fprintf(os.Stderr, "Falling back to current branch commits\n")
				} else {
					// GH#519: Check if current branch is the sync branch
					// If so, commit directly instead of using worktree (which would fail)
					currentBranch, _ := getCurrentBranch(ctx)
					if currentBranch == syncBranchName {
						onSyncBranch = true
						// Don't use worktree - commit directly to current branch
						useSyncBranch = false
					} else {
						useSyncBranch = true
					}
				}
			}
		}

		// Step 2: Check if there are changes to commit (check entire .beads/ directory)
		hasChanges, err := gitHasBeadsChanges(ctx)
		if err != nil {
			FatalError("checking git status: %v", err)
		}

		// Track if we already pushed via worktree (to skip Step 5)
		pushedViaSyncBranch := false

		if hasChanges {
			if dryRun {
				if useSyncBranch {
					fmt.Printf("→ [DRY RUN] Would commit changes to sync branch '%s' via worktree\n", syncBranchName)
				} else if onSyncBranch {
					// GH#519: on sync branch, commit directly
					fmt.Printf("→ [DRY RUN] Would commit changes directly to sync branch '%s'\n", syncBranchName)
				} else {
					fmt.Println("→ [DRY RUN] Would commit changes to git")
				}
			} else if useSyncBranch {
				// Use worktree to commit to sync branch (bd-e3w)
				fmt.Printf("→ Committing changes to sync branch '%s'...\n", syncBranchName)
				result, err := syncbranch.CommitToSyncBranch(ctx, repoRoot, syncBranchName, jsonlPath, !noPush)
				if err != nil {
					FatalError("committing to sync branch: %v", err)
				}
				if result.Committed {
					fmt.Printf("✓ Committed to %s\n", syncBranchName)
					if result.Pushed {
						fmt.Printf("✓ Pushed %s to remote\n", syncBranchName)
						pushedViaSyncBranch = true
					}
				}
			} else {
				// Regular commit to current branch
				// GH#519: if on sync branch, show appropriate message
				if onSyncBranch {
					fmt.Printf("→ Committing changes directly to sync branch '%s'...\n", syncBranchName)
				} else {
					fmt.Println("→ Committing changes to git...")
				}
				if err := gitCommitBeadsDir(ctx, message); err != nil {
					FatalError("committing: %v", err)
				}
			}
		} else {
			fmt.Println("→ No changes to commit")
		}

		// Step 3: Pull from remote
		// Note: If no upstream, we already handled it above with --from-main mode
		if !noPull {
			if dryRun {
				if useSyncBranch {
					fmt.Printf("→ [DRY RUN] Would pull from sync branch '%s' via worktree\n", syncBranchName)
				} else if onSyncBranch {
					// GH#519: on sync branch, regular git pull
					fmt.Printf("→ [DRY RUN] Would pull directly on sync branch '%s'\n", syncBranchName)
				} else {
					fmt.Println("→ [DRY RUN] Would pull from remote")
				}
			} else {
				// Execute pull - either via sync branch worktree or regular git pull
				if useSyncBranch {
					// Pull from sync branch via worktree (bd-e3w)
					fmt.Printf("→ Pulling from sync branch '%s'...\n", syncBranchName)

					// bd-4u8: Check if confirmation is required for mass deletion
					requireMassDeleteConfirmation := config.GetBool("sync.require_confirmation_on_mass_delete")

					pullResult, err := syncbranch.PullFromSyncBranch(ctx, repoRoot, syncBranchName, jsonlPath, !noPush, requireMassDeleteConfirmation)
					if err != nil {
						FatalError("pulling from sync branch: %v", err)
					}
					if pullResult.Pulled {
						if pullResult.Merged {
							// bd-3s8 fix: divergent histories were merged at content level
							fmt.Printf("✓ Merged divergent histories from %s\n", syncBranchName)

							// bd-7z4: Print safety warnings from result
							for _, warning := range pullResult.SafetyWarnings {
								fmt.Fprintln(os.Stderr, warning)
							}

							// bd-4u8: Handle safety check with confirmation requirement
							if pullResult.SafetyCheckTriggered && !pullResult.Pushed {
								// bd-dmd: Don't duplicate SafetyCheckDetails - it's already in SafetyWarnings
								// Prompt for confirmation
								fmt.Fprintf(os.Stderr, "Push these changes to remote? [y/N]: ")

								var response string
								reader := bufio.NewReader(os.Stdin)
								response, _ = reader.ReadString('\n')
								response = strings.TrimSpace(strings.ToLower(response))

								if response == "y" || response == "yes" {
									fmt.Printf("→ Pushing to %s...\n", syncBranchName)
									if err := syncbranch.PushSyncBranch(ctx, repoRoot, syncBranchName); err != nil {
										FatalError("pushing to sync branch: %v", err)
									}
									fmt.Printf("✓ Pushed merged changes to %s\n", syncBranchName)
									pushedViaSyncBranch = true
								} else {
									fmt.Println("Push canceled. Run 'bd sync' again to retry.")
									fmt.Println("If this was unintended, use 'git reflog' on the sync branch to recover.")
								}
							} else if pullResult.Pushed {
								// bd-7ch: auto-push after merge
								fmt.Printf("✓ Pushed merged changes to %s\n", syncBranchName)
								pushedViaSyncBranch = true
							}
						} else if pullResult.FastForwarded {
							fmt.Printf("✓ Fast-forwarded from %s\n", syncBranchName)
						} else {
							fmt.Printf("✓ Pulled from %s\n", syncBranchName)
						}
					}
					// JSONL is already copied to main repo by PullFromSyncBranch
				} else {
					// Check merge driver configuration before pulling
					checkMergeDriverConfig()

					// GH#519: show appropriate message when on sync branch
					if onSyncBranch {
						fmt.Printf("→ Pulling from remote on sync branch '%s'...\n", syncBranchName)
					} else {
						fmt.Println("→ Pulling from remote...")
					}
					err := gitPull(ctx)
					if err != nil {
						// Check if it's a rebase conflict on beads.jsonl that we can auto-resolve
						if isInRebase() && hasJSONLConflict() {
							fmt.Println("→ Auto-resolving JSONL merge conflict...")

							// Export clean JSONL from DB (database is source of truth)
							if exportErr := exportToJSONL(ctx, jsonlPath); exportErr != nil {
								FatalErrorWithHint(fmt.Sprintf("failed to export for conflict resolution: %v", exportErr), "resolve conflicts manually and run 'bd import' then 'bd sync' again")
							}

							// Mark conflict as resolved
							addCmd := exec.CommandContext(ctx, "git", "add", jsonlPath)
							if addErr := addCmd.Run(); addErr != nil {
								FatalErrorWithHint(fmt.Sprintf("failed to mark conflict resolved: %v", addErr), "resolve conflicts manually and run 'bd import' then 'bd sync' again")
							}

							// Continue rebase
							if continueErr := runGitRebaseContinue(ctx); continueErr != nil {
								FatalErrorWithHint(fmt.Sprintf("failed to continue rebase: %v", continueErr), "resolve conflicts manually and run 'bd import' then 'bd sync' again")
							}

							fmt.Println("✓ Auto-resolved JSONL conflict")
						} else {
							// Not an auto-resolvable conflict, fail with original error
							// Check if this looks like a merge driver failure
							errStr := err.Error()
							if strings.Contains(errStr, "merge driver") ||
								strings.Contains(errStr, "no such file or directory") ||
								strings.Contains(errStr, "MERGE DRIVER INVOKED") {
								fmt.Fprintf(os.Stderr, "\nThis may be caused by an incorrect merge driver configuration.\n")
								fmt.Fprintf(os.Stderr, "Fix: bd doctor --fix\n\n")
							}

							FatalErrorWithHint(fmt.Sprintf("pulling: %v", err), "resolve conflicts manually and run 'bd import' then 'bd sync' again")
						}
					}
				}

				// Import logic - shared for both sync branch and regular pull paths
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
						FatalError("during 3-way merge: %v", err)
					}
				}

				// Step 4: Import updated JSONL after pull
				// Enable --protect-left-snapshot to prevent git-history-backfill from
				// tombstoning issues that were in our local export but got lost during merge (bd-sync-deletion fix)
				fmt.Println("→ Importing updated JSONL...")
				if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory, true); err != nil {
					FatalError("importing: %v", err)
				}

				// Validate import didn't cause data loss
				if beforeCount > 0 {
					if err := ensureStoreActive(); err == nil && store != nil {
						afterCount, err := countDBIssues(ctx, store)
						if err != nil {
							fmt.Fprintf(os.Stderr, "Warning: failed to count issues after import: %v\n", err)
						} else {
							if err := validatePostImportWithExpectedDeletions(beforeCount, afterCount, 0, jsonlPath); err != nil {
								FatalError("post-import validation failed: %v", err)
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
								FatalError("re-exporting after import: %v", err)
							}

							// Step 4.6: Commit the re-export if it created changes
							hasPostImportChanges, err := gitHasBeadsChanges(ctx)
							if err != nil {
								FatalError("checking git status after re-export: %v", err)
							}
							if hasPostImportChanges {
								fmt.Println("→ Committing DB changes from import...")
								if useSyncBranch {
									// Commit to sync branch via worktree (bd-e3w)
									result, err := syncbranch.CommitToSyncBranch(ctx, repoRoot, syncBranchName, jsonlPath, !noPush)
									if err != nil {
										FatalError("committing to sync branch: %v", err)
									}
									if result.Pushed {
										pushedViaSyncBranch = true
									}
								} else {
									if err := gitCommitBeadsDir(ctx, "bd sync: apply DB changes after import"); err != nil {
										FatalError("committing post-import changes: %v", err)
									}
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
			}
		}

		// Step 5: Push to remote (skip if using sync branch - all pushes go via worktree)
		// When sync.branch is configured, we don't push the main branch at all.
		// The sync branch worktree handles all pushes.
		if !noPush && hasChanges && !pushedViaSyncBranch && !useSyncBranch {
			if dryRun {
				fmt.Println("→ [DRY RUN] Would push to remote")
			} else {
				fmt.Println("→ Pushing to remote...")
				if err := gitPush(ctx); err != nil {
					FatalErrorWithHint(fmt.Sprintf("pushing: %v", err), "pull may have brought new changes, run 'bd sync' again")
				}
			}
		}

		if dryRun {
			fmt.Println("\n✓ Dry run complete (no changes made)")
		} else {
			// Clean up temporary snapshot files after successful sync (bd-0io)
			// This runs regardless of whether pull was performed
			sm := NewSnapshotManager(jsonlPath)
			if err := sm.Cleanup(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to clean up snapshots: %v\n", err)
			}

			// When using sync.branch, restore .beads/ from current branch to keep
			// working directory clean. The actual beads data lives on the sync branch,
			// and the main branch's .beads/ should match what's committed there.
			// This prevents "modified .beads/" showing in git status after sync.
			if useSyncBranch {
				if err := restoreBeadsDirFromBranch(ctx); err != nil {
					// Non-fatal - just means git status will show modified files
					debug.Logf("sync: failed to restore .beads/ from branch: %v", err)
				}
				// Skip final flush in PersistentPostRun - we've already exported to sync branch
				// and restored the working directory to match the current branch
				skipFinalFlush = true
			}

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
	syncCmd.Flags().Bool("squash", false, "Accumulate changes in JSONL without committing (run 'bd sync' later to commit all)")
	syncCmd.Flags().Bool("import-only", false, "Only import from JSONL (skip git operations, useful after git pull)")
	syncCmd.Flags().Bool("status", false, "Show diff between sync branch and main branch")
	syncCmd.Flags().Bool("merge", false, "Merge sync branch back to main branch")
	syncCmd.Flags().Bool("from-main", false, "One-way sync from main branch (for ephemeral branches without upstream)")
	syncCmd.Flags().Bool("no-git-history", false, "Skip git history backfill for deletions (use during JSONL filename migrations)")
	syncCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output sync statistics in JSON format")
	syncCmd.Flags().Bool("check", false, "Pre-sync integrity check: detect forced pushes, prefix mismatches, and orphaned issues")
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

// getRepoRootForWorktree returns the main repository root for running git commands
// This is always the main repository root, never the worktree root
func getRepoRootForWorktree(_ context.Context) string {
	repoRoot, err := git.GetMainRepoRoot()
	if err != nil {
		// Fallback to current directory if GetMainRepoRoot fails
		return "."
	}
	return repoRoot
}

// gitHasBeadsChanges checks if any tracked files in .beads/ have uncommitted changes
func gitHasBeadsChanges(ctx context.Context) (bool, error) {
	// Get the absolute path to .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return false, fmt.Errorf("no .beads directory found")
	}

	// Get the repository root (handles worktrees properly)
	repoRoot := getRepoRootForWorktree(ctx)
	if repoRoot == "" {
		return false, fmt.Errorf("cannot determine repository root")
	}

	// Compute relative path from repo root to .beads
	relPath, err := filepath.Rel(repoRoot, beadsDir)
	if err != nil {
		// Fall back to absolute path if relative path fails
		statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain", beadsDir)
		statusOutput, err := statusCmd.Output()
		if err != nil {
			return false, fmt.Errorf("git status failed: %w", err)
		}
		return len(strings.TrimSpace(string(statusOutput))) > 0, nil
	}

	// Run git status with relative path from repo root
	statusCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "status", "--porcelain", relPath)
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(statusOutput))) > 0, nil
}

// buildGitCommitArgs returns git commit args with config-based author and signing options (GH#600)
// This allows users to configure a separate author and disable GPG signing for beads commits.
func buildGitCommitArgs(repoRoot, message string, extraArgs ...string) []string {
	args := []string{"-C", repoRoot, "commit"}

	// Add --author if configured
	if author := config.GetString("git.author"); author != "" {
		args = append(args, "--author", author)
	}

	// Add --no-gpg-sign if configured
	if config.GetBool("git.no-gpg-sign") {
		args = append(args, "--no-gpg-sign")
	}

	// Add message
	args = append(args, "-m", message)

	// Add any extra args (like -- pathspec)
	args = append(args, extraArgs...)

	return args
}

// gitCommit commits the specified file (worktree-aware)
func gitCommit(ctx context.Context, filePath string, message string) error {
	// Get the repository root (handles worktrees properly)
	repoRoot := getRepoRootForWorktree(ctx)
	if repoRoot == "" {
		return fmt.Errorf("cannot determine repository root")
	}

	// Make file path relative to repo root for git operations
	relPath, err := filepath.Rel(repoRoot, filePath)
	if err != nil {
		relPath = filePath // Fall back to absolute path
	}

	// Stage the file from repo root context
	addCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "add", relPath)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Generate message if not provided
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// Commit from repo root context with config-based author and signing options
	commitArgs := buildGitCommitArgs(repoRoot, message)
	commitCmd := exec.CommandContext(ctx, "git", commitArgs...)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, output)
	}

	return nil
}

// gitCommitBeadsDir stages and commits only sync-related files in .beads/ (bd-red fix)
// This ensures bd sync doesn't accidentally commit other staged files.
// Only stages specific sync files (issues.jsonl, deletions.jsonl, metadata.json)
// to avoid staging gitignored snapshot files that may be tracked. (bd-guc fix)
// Worktree-aware: handles cases where .beads is in the main repo but we're running from a worktree.
func gitCommitBeadsDir(ctx context.Context, message string) error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return fmt.Errorf("no .beads directory found")
	}

	// Get the repository root (handles worktrees properly)
	repoRoot := getRepoRootForWorktree(ctx)
	if repoRoot == "" {
		return fmt.Errorf("cannot determine repository root")
	}

	// Stage only the specific sync-related files (bd-guc)
	// This avoids staging gitignored snapshot files (beads.*.jsonl, *.meta.json)
	// that may still be tracked from before they were added to .gitignore
	syncFiles := []string{
		filepath.Join(beadsDir, "issues.jsonl"),
		filepath.Join(beadsDir, "deletions.jsonl"),
		filepath.Join(beadsDir, "interactions.jsonl"),
		filepath.Join(beadsDir, "metadata.json"),
	}

	// Only add files that exist
	var filesToAdd []string
	for _, f := range syncFiles {
		if _, err := os.Stat(f); err == nil {
			// Convert to relative path from repo root for git operations
			relPath, err := filepath.Rel(repoRoot, f)
			if err != nil {
				relPath = f // Fall back to absolute path if relative fails
			}
			filesToAdd = append(filesToAdd, relPath)
		}
	}

	if len(filesToAdd) == 0 {
		return fmt.Errorf("no sync files found to commit")
	}

	// Stage only the sync files from repo root context (worktree-aware)
	args := append([]string{"-C", repoRoot, "add"}, filesToAdd...)
	addCmd := exec.CommandContext(ctx, "git", args...)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Generate message if not provided
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// Commit only .beads/ files using -- pathspec (bd-red)
	// This prevents accidentally committing other staged files that the user
	// may have staged but wasn't ready to commit yet.
	// Convert beadsDir to relative path for git commit (worktree-aware)
	relBeadsDir, err := filepath.Rel(repoRoot, beadsDir)
	if err != nil {
		relBeadsDir = beadsDir // Fall back to absolute path if relative fails
	}

	// Use config-based author and signing options with pathspec
	commitArgs := buildGitCommitArgs(repoRoot, message, "--", relBeadsDir)
	commitCmd := exec.CommandContext(ctx, "git", commitArgs...)
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
	// Get actual git directory (handles worktrees)
	gitDir, err := git.GetGitDir()
	if err != nil {
		return false
	}

	// Check for rebase-merge directory (interactive rebase)
	rebaseMergePath := filepath.Join(gitDir, "rebase-merge")
	if _, err := os.Stat(rebaseMergePath); err == nil {
		return true
	}
	// Check for rebase-apply directory (non-interactive rebase)
	rebaseApplyPath := filepath.Join(gitDir, "rebase-apply")
	if _, err := os.Stat(rebaseApplyPath); err == nil {
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

// restoreBeadsDirFromBranch restores .beads/ directory from the current branch's committed state.
// This is used after sync when sync.branch is configured to keep the working directory clean.
// The actual beads data lives on the sync branch; the main branch's .beads/ is just a snapshot.
func restoreBeadsDirFromBranch(ctx context.Context) error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return fmt.Errorf("no .beads directory found")
	}

	// Restore .beads/ from HEAD (current branch's committed state)
	// Using -- to ensure .beads/ is treated as a path, not a branch name
	cmd := exec.CommandContext(ctx, "git", "checkout", "HEAD", "--", beadsDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout failed: %w\n%s", err, output)
	}
	return nil
}

// getDefaultBranch returns the default branch name (main or master) for origin remote
// Checks remote HEAD first, then falls back to checking if main/master exist
func getDefaultBranch(ctx context.Context) string {
	return getDefaultBranchForRemote(ctx, "origin")
}

// getDefaultBranchForRemote returns the default branch name for a specific remote
// Checks remote HEAD first, then falls back to checking if main/master exist
func getDefaultBranchForRemote(ctx context.Context, remote string) string {
	// Try to get default branch from remote
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", fmt.Sprintf("refs/remotes/%s/HEAD", remote))
	output, err := cmd.Output()
	if err == nil {
		ref := strings.TrimSpace(string(output))
		// Extract branch name from refs/remotes/<remote>/main
		prefix := fmt.Sprintf("refs/remotes/%s/", remote)
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}

	// Fallback: check if <remote>/main exists
	if exec.CommandContext(ctx, "git", "rev-parse", "--verify", fmt.Sprintf("%s/main", remote)).Run() == nil {
		return "main"
	}

	// Fallback: check if <remote>/master exists
	if exec.CommandContext(ctx, "git", "rev-parse", "--verify", fmt.Sprintf("%s/master", remote)).Run() == nil {
		return "master"
	}

	// Default to main
	return "main"
}

// doSyncFromMain performs a one-way sync from the default branch (main/master)
// Used for ephemeral branches without upstream tracking (gt-ick9)
// This fetches beads from main and imports them, discarding local beads changes.
// If sync.remote is configured (e.g., "upstream" for fork workflows), uses that remote
// instead of "origin" (bd-bx9).
func doSyncFromMain(ctx context.Context, jsonlPath string, renameOnImport bool, dryRun bool, noGitHistory bool) error {
	// Determine which remote to use (default: origin, but can be configured via sync.remote)
	remote := "origin"
	if err := ensureStoreActive(); err == nil && store != nil {
		if configuredRemote, err := store.GetConfig(ctx, "sync.remote"); err == nil && configuredRemote != "" {
			remote = configuredRemote
		}
	}

	if dryRun {
		fmt.Println("→ [DRY RUN] Would sync beads from main branch")
		fmt.Printf("  1. Fetch %s main\n", remote)
		fmt.Printf("  2. Checkout .beads/ from %s/main\n", remote)
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

	// Verify the configured remote exists
	checkRemoteCmd := exec.CommandContext(ctx, "git", "remote", "get-url", remote)
	if err := checkRemoteCmd.Run(); err != nil {
		return fmt.Errorf("configured sync.remote '%s' does not exist (run 'git remote add %s <url>')", remote, remote)
	}

	defaultBranch := getDefaultBranchForRemote(ctx, remote)

	// Step 1: Fetch from main
	fmt.Printf("→ Fetching from %s/%s...\n", remote, defaultBranch)
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", remote, defaultBranch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch %s %s failed: %w\n%s", remote, defaultBranch, err, output)
	}

	// Step 2: Checkout .beads/ directory from main
	fmt.Printf("→ Checking out beads from %s/%s...\n", remote, defaultBranch)
	checkoutCmd := exec.CommandContext(ctx, "git", "checkout", fmt.Sprintf("%s/%s", remote, defaultBranch), "--", ".beads/")
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout .beads/ from %s/%s failed: %w\n%s", remote, defaultBranch, err, output)
	}

	// Step 3: Import JSONL
	fmt.Println("→ Importing JSONL...")
	if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
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

	// Get all issues including tombstones for sync propagation (bd-rp4o fix)
	// Tombstones must be exported so they propagate to other clones and prevent resurrection
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}

	// Safety check: prevent exporting empty database over non-empty JSONL
	// Note: The main bd-53c protection is the reverse ZFC check earlier in sync.go
	// which runs BEFORE export. Here we only block the most catastrophic case (empty DB)
	// to allow legitimate deletions.
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

	// Sort by ID for consistent output
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		return cmp.Compare(a.ID, b.ID)
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

	// Close temp file before rename (error checked implicitly by Rename success)
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

	// Update jsonl_content_hash metadata to enable content-based staleness detection (bd-khnb fix)
	// After export, database and JSONL are in sync, so update hash to prevent unnecessary auto-import
	// Renamed from last_import_hash (bd-39o) - more accurate since updated on both import AND export
	if currentHash, err := computeJSONLHash(jsonlPath); err == nil {
		if err := store.SetMetadata(ctx, "jsonl_content_hash", currentHash); err != nil {
			// Non-fatal warning: Metadata update failures are intentionally non-fatal to prevent blocking
			// successful exports. System degrades gracefully to mtime-based staleness detection if metadata
			// is unavailable. This ensures export operations always succeed even if metadata storage fails.
			fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_content_hash: %v\n", err)
		}
		// Use RFC3339Nano for nanosecond precision to avoid race with file mtime (fixes #399)
		exportTime := time.Now().Format(time.RFC3339Nano)
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

	// Show file diff for .beads/issues.jsonl
	fmt.Println("\nFile differences in .beads/issues.jsonl:")
	diffCmd := exec.CommandContext(ctx, "git", "diff", currentBranch+"..."+syncBranch, "--", ".beads/issues.jsonl")
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

	// Check if main branch is clean (excluding .beads/ which is expected to be dirty)
	// bd-7b7h fix: The sync.branch workflow copies JSONL to main working dir without committing,
	// so .beads/ changes are expected and should not block merge.
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "--", ":!.beads/")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		return fmt.Errorf("main branch has uncommitted changes outside .beads/, please commit or stash them first")
	}

	// bd-7b7h fix: Restore .beads/ to HEAD state before merge
	// The uncommitted .beads/ changes came from copyJSONLToMainRepo during bd sync,
	// which copied them FROM the sync branch. They're redundant with what we're merging.
	// Discarding them prevents "Your local changes would be overwritten by merge" errors.
	restoreCmd := exec.CommandContext(ctx, "git", "checkout", "HEAD", "--", ".beads/")
	if output, err := restoreCmd.CombinedOutput(); err != nil {
		// Not fatal - .beads/ might not exist in HEAD yet
		debug.Logf("note: could not restore .beads/ to HEAD: %v (%s)", err, output)
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
// Optional parameters: noGitHistory, protectLeftSnapshot (bd-sync-deletion fix)
func importFromJSONL(ctx context.Context, jsonlPath string, renameOnImport bool, opts ...bool) error {
	// Get current executable path to avoid "./bd" path issues
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve current executable: %w", err)
	}

	// Parse optional parameters
	noGitHistory := false
	protectLeftSnapshot := false
	if len(opts) > 0 {
		noGitHistory = opts[0]
	}
	if len(opts) > 1 {
		protectLeftSnapshot = opts[1]
	}

	// Build args for import command
	// Use --no-daemon to ensure subprocess uses direct mode, avoiding daemon connection issues
	args := []string{"--no-daemon", "import", "-i", jsonlPath}
	if renameOnImport {
		args = append(args, "--rename-on-import")
	}
	if noGitHistory {
		args = append(args, "--no-git-history")
	}
	// Add --protect-left-snapshot flag for post-pull imports (bd-sync-deletion fix)
	if protectLeftSnapshot {
		args = append(args, "--protect-left-snapshot")
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

// resolveNoGitHistoryForFromMain returns the resolved noGitHistory value for sync operations.
// When syncing from main (--from-main), noGitHistory is forced to true to prevent creating
// incorrect deletion records for locally-created beads that don't exist on main.
// See: https://github.com/steveyegge/beads/issues/417
func resolveNoGitHistoryForFromMain(fromMain, noGitHistory bool) bool {
	if fromMain {
		return true
	}
	return noGitHistory
}

// isExternalBeadsDir checks if the beads directory is in a different git repo than cwd.
// This is used to detect when BEADS_DIR points to a separate repository.
// Contributed by dand-oss (https://github.com/steveyegge/beads/pull/533)
func isExternalBeadsDir(ctx context.Context, beadsDir string) bool {
	// Get repo root of cwd
	cwdRepoRoot, err := syncbranch.GetRepoRoot(ctx)
	if err != nil {
		return false // Can't determine, assume local
	}

	// Get repo root of beads dir
	beadsRepoRoot, err := getRepoRootFromPath(ctx, beadsDir)
	if err != nil {
		return false // Can't determine, assume local
	}

	return cwdRepoRoot != beadsRepoRoot
}

// getRepoRootFromPath returns the git repository root for a given path.
// Unlike syncbranch.GetRepoRoot which uses cwd, this allows getting the repo root
// for any path.
// Contributed by dand-oss (https://github.com/steveyegge/beads/pull/533)
func getRepoRootFromPath(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get git root for %s: %w", path, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// commitToExternalBeadsRepo commits changes directly to an external beads repo.
// Used when BEADS_DIR points to a different git repository than cwd.
// This bypasses the worktree-based sync which fails when beads dir is external.
// Contributed by dand-oss (https://github.com/steveyegge/beads/pull/533)
func commitToExternalBeadsRepo(ctx context.Context, beadsDir, message string, push bool) (bool, error) {
	repoRoot, err := getRepoRootFromPath(ctx, beadsDir)
	if err != nil {
		return false, fmt.Errorf("failed to get repo root: %w", err)
	}

	// Stage beads files (use relative path from repo root)
	relBeadsDir, err := filepath.Rel(repoRoot, beadsDir)
	if err != nil {
		relBeadsDir = beadsDir // Fallback to absolute path
	}

	addCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "add", relBeadsDir)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git add failed: %w\n%s", err, output)
	}

	// Check if there are staged changes
	diffCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "diff", "--cached", "--quiet")
	if diffCmd.Run() == nil {
		return false, nil // No changes to commit
	}

	// Commit with config-based author and signing options
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}
	commitArgs := buildGitCommitArgs(repoRoot, message)
	commitCmd := exec.CommandContext(ctx, "git", commitArgs...)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git commit failed: %w\n%s", err, output)
	}

	// Push if requested
	if push {
		pushCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "push")
		if pushOutput, err := runGitCmdWithTimeoutMsg(ctx, pushCmd, "git push", 5*time.Second); err != nil {
			return true, fmt.Errorf("git push failed: %w\n%s", err, pushOutput)
		}
	}

	return true, nil
}

// pullFromExternalBeadsRepo pulls changes in an external beads repo.
// Used when BEADS_DIR points to a different git repository than cwd.
// Contributed by dand-oss (https://github.com/steveyegge/beads/pull/533)
func pullFromExternalBeadsRepo(ctx context.Context, beadsDir string) error {
	repoRoot, err := getRepoRootFromPath(ctx, beadsDir)
	if err != nil {
		return fmt.Errorf("failed to get repo root: %w", err)
	}

	// Check if remote exists
	remoteCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "remote")
	remoteOutput, err := remoteCmd.Output()
	if err != nil || len(strings.TrimSpace(string(remoteOutput))) == 0 {
		return nil // No remote, skip pull
	}

	pullCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "pull")
	if output, err := pullCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git pull failed: %w\n%s", err, output)
	}

	return nil
}

// SyncIntegrityResult contains the results of a pre-sync integrity check.
// bd-hlsw.1: Pre-sync integrity check
type SyncIntegrityResult struct {
	ForcedPush       *ForcedPushCheck  `json:"forced_push,omitempty"`
	PrefixMismatch   *PrefixMismatch   `json:"prefix_mismatch,omitempty"`
	OrphanedChildren *OrphanedChildren `json:"orphaned_children,omitempty"`
	HasProblems      bool              `json:"has_problems"`
}

// ForcedPushCheck detects if sync branch has diverged from remote.
type ForcedPushCheck struct {
	Detected  bool   `json:"detected"`
	LocalRef  string `json:"local_ref,omitempty"`
	RemoteRef string `json:"remote_ref,omitempty"`
	Message   string `json:"message"`
}

// PrefixMismatch detects issues with wrong prefix in JSONL.
type PrefixMismatch struct {
	ConfiguredPrefix string   `json:"configured_prefix"`
	MismatchedIDs    []string `json:"mismatched_ids,omitempty"`
	Count            int      `json:"count"`
}

// OrphanedChildren detects issues with parent that doesn't exist.
type OrphanedChildren struct {
	OrphanedIDs []string `json:"orphaned_ids,omitempty"`
	Count       int      `json:"count"`
}

// showSyncIntegrityCheck performs pre-sync integrity checks without modifying state.
// bd-hlsw.1: Detects forced pushes, prefix mismatches, and orphaned children.
// Exits with code 1 if problems are detected.
func showSyncIntegrityCheck(ctx context.Context, jsonlPath string) {
	fmt.Println("Sync Integrity Check")
	fmt.Println("====================")

	result := &SyncIntegrityResult{}

	// Check 1: Detect forced pushes on sync branch
	forcedPush := checkForcedPush(ctx)
	result.ForcedPush = forcedPush
	if forcedPush.Detected {
		result.HasProblems = true
	}
	printForcedPushResult(forcedPush)

	// Check 2: Detect prefix mismatches in JSONL
	prefixMismatch, err := checkPrefixMismatch(ctx, jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: prefix check failed: %v\n", err)
	} else {
		result.PrefixMismatch = prefixMismatch
		if prefixMismatch != nil && prefixMismatch.Count > 0 {
			result.HasProblems = true
		}
		printPrefixMismatchResult(prefixMismatch)
	}

	// Check 3: Detect orphaned children (parent issues that don't exist)
	orphaned, err := checkOrphanedChildrenInJSONL(jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: orphaned check failed: %v\n", err)
	} else {
		result.OrphanedChildren = orphaned
		if orphaned != nil && orphaned.Count > 0 {
			result.HasProblems = true
		}
		printOrphanedChildrenResult(orphaned)
	}

	// Summary
	fmt.Println("\nSummary")
	fmt.Println("-------")
	if result.HasProblems {
		fmt.Println("Problems detected! Review above and consider:")
		if result.ForcedPush != nil && result.ForcedPush.Detected {
			fmt.Println("  - Force push: Reset local sync branch or use 'bd sync --from-main'")
		}
		if result.PrefixMismatch != nil && result.PrefixMismatch.Count > 0 {
			fmt.Println("  - Prefix mismatch: Use 'bd import --rename-on-import' to fix")
		}
		if result.OrphanedChildren != nil && result.OrphanedChildren.Count > 0 {
			fmt.Println("  - Orphaned children: Remove parent references or create missing parents")
		}
		os.Exit(1)
	} else {
		fmt.Println("No problems detected. Safe to sync.")
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	}
}

// checkForcedPush detects if the sync branch has diverged from remote.
// This can happen when someone force-pushes to the sync branch.
func checkForcedPush(ctx context.Context) *ForcedPushCheck {
	result := &ForcedPushCheck{
		Detected: false,
		Message:  "No sync branch configured or no remote",
	}

	// Get sync branch name
	if err := ensureStoreActive(); err != nil {
		return result
	}

	syncBranch, _ := syncbranch.Get(ctx, store)
	if syncBranch == "" {
		return result
	}

	// Check if sync branch exists locally
	checkLocalCmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+syncBranch)
	if checkLocalCmd.Run() != nil {
		result.Message = fmt.Sprintf("Sync branch '%s' does not exist locally", syncBranch)
		return result
	}

	// Get local ref
	localRefCmd := exec.CommandContext(ctx, "git", "rev-parse", syncBranch)
	localRefOutput, err := localRefCmd.Output()
	if err != nil {
		result.Message = "Failed to get local sync branch ref"
		return result
	}
	localRef := strings.TrimSpace(string(localRefOutput))
	result.LocalRef = localRef

	// Check if remote tracking branch exists
	remote := "origin"
	if configuredRemote, err := store.GetConfig(ctx, "sync.remote"); err == nil && configuredRemote != "" {
		remote = configuredRemote
	}

	// Get remote ref
	remoteRefCmd := exec.CommandContext(ctx, "git", "rev-parse", remote+"/"+syncBranch)
	remoteRefOutput, err := remoteRefCmd.Output()
	if err != nil {
		result.Message = fmt.Sprintf("Remote tracking branch '%s/%s' does not exist", remote, syncBranch)
		return result
	}
	remoteRef := strings.TrimSpace(string(remoteRefOutput))
	result.RemoteRef = remoteRef

	// If refs match, no divergence
	if localRef == remoteRef {
		result.Message = "Sync branch is in sync with remote"
		return result
	}

	// Check if local is ahead of remote (normal case)
	aheadCmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", remoteRef, localRef)
	if aheadCmd.Run() == nil {
		result.Message = "Local sync branch is ahead of remote (normal)"
		return result
	}

	// Check if remote is ahead of local (behind, needs pull)
	behindCmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", localRef, remoteRef)
	if behindCmd.Run() == nil {
		result.Message = "Local sync branch is behind remote (needs pull)"
		return result
	}

	// If neither is ancestor, branches have diverged - likely a force push
	result.Detected = true
	result.Message = fmt.Sprintf("Sync branch has DIVERGED from remote! Local: %s, Remote: %s. This may indicate a force push on the remote.", localRef[:8], remoteRef[:8])

	return result
}

func printForcedPushResult(fp *ForcedPushCheck) {
	fmt.Println("1. Force Push Detection")
	if fp.Detected {
		fmt.Printf("   [PROBLEM] %s\n", fp.Message)
	} else {
		fmt.Printf("   [OK] %s\n", fp.Message)
	}
	fmt.Println()
}

// checkPrefixMismatch detects issues in JSONL that don't match the configured prefix.
func checkPrefixMismatch(ctx context.Context, jsonlPath string) (*PrefixMismatch, error) {
	result := &PrefixMismatch{
		MismatchedIDs: []string{},
	}

	// Get configured prefix
	if err := ensureStoreActive(); err != nil {
		return nil, err
	}

	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix == "" {
		prefix = "bd" // Default
	}
	result.ConfiguredPrefix = prefix

	// Read JSONL and check each issue's prefix
	f, err := os.Open(jsonlPath) // #nosec G304 - controlled path
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil // No JSONL, no mismatches
		}
		return nil, fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(line, &issue); err != nil {
			continue // Skip malformed lines
		}

		// Check if ID starts with configured prefix
		if !strings.HasPrefix(issue.ID, prefix+"-") {
			result.MismatchedIDs = append(result.MismatchedIDs, issue.ID)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read JSONL: %w", err)
	}

	result.Count = len(result.MismatchedIDs)
	return result, nil
}

func printPrefixMismatchResult(pm *PrefixMismatch) {
	fmt.Println("2. Prefix Mismatch Check")
	if pm == nil {
		fmt.Println("   [SKIP] Could not check prefix")
		fmt.Println()
		return
	}

	fmt.Printf("   Configured prefix: %s\n", pm.ConfiguredPrefix)
	if pm.Count > 0 {
		fmt.Printf("   [PROBLEM] Found %d issue(s) with wrong prefix:\n", pm.Count)
		// Show first 10
		limit := pm.Count
		if limit > 10 {
			limit = 10
		}
		for i := 0; i < limit; i++ {
			fmt.Printf("      - %s\n", pm.MismatchedIDs[i])
		}
		if pm.Count > 10 {
			fmt.Printf("      ... and %d more\n", pm.Count-10)
		}
	} else {
		fmt.Println("   [OK] All issues have correct prefix")
	}
	fmt.Println()
}

// checkOrphanedChildrenInJSONL detects issues with parent references to non-existent issues.
func checkOrphanedChildrenInJSONL(jsonlPath string) (*OrphanedChildren, error) {
	result := &OrphanedChildren{
		OrphanedIDs: []string{},
	}

	// Read JSONL and build maps of IDs and parent references
	f, err := os.Open(jsonlPath) // #nosec G304 - controlled path
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer f.Close()

	existingIDs := make(map[string]bool)
	parentRefs := make(map[string]string) // child ID -> parent ID

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var issue struct {
			ID     string `json:"id"`
			Parent string `json:"parent,omitempty"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(line, &issue); err != nil {
			continue
		}

		// Skip tombstones
		if issue.Status == string(types.StatusTombstone) {
			continue
		}

		existingIDs[issue.ID] = true
		if issue.Parent != "" {
			parentRefs[issue.ID] = issue.Parent
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read JSONL: %w", err)
	}

	// Find orphaned children (parent doesn't exist)
	for childID, parentID := range parentRefs {
		if !existingIDs[parentID] {
			result.OrphanedIDs = append(result.OrphanedIDs, fmt.Sprintf("%s (parent: %s)", childID, parentID))
		}
	}

	result.Count = len(result.OrphanedIDs)
	return result, nil
}

// runGitCmdWithTimeoutMsg runs a git command and prints a helpful message if it takes too long.
// This helps when git operations hang waiting for credential/browser auth.
func runGitCmdWithTimeoutMsg(ctx context.Context, cmd *exec.Cmd, cmdName string, timeoutDelay time.Duration) ([]byte, error) {
	// Use done channel to cleanly exit goroutine when command completes
	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(timeoutDelay):
			fmt.Fprintf(os.Stderr, "⏳ %s is taking longer than expected (possibly waiting for authentication). If this hangs, check for a browser auth prompt or run 'git status' in another terminal.\n", cmdName)
		case <-done:
			// Command completed, exit cleanly
		case <-ctx.Done():
			// Context canceled, don't print message
		}
	}()

	output, err := cmd.CombinedOutput()
	close(done)
	return output, err
}

func printOrphanedChildrenResult(oc *OrphanedChildren) {
	fmt.Println("3. Orphaned Children Check")
	if oc == nil {
		fmt.Println("   [SKIP] Could not check orphaned children")
		fmt.Println()
		return
	}

	if oc.Count > 0 {
		fmt.Printf("   [PROBLEM] Found %d issue(s) with missing parent:\n", oc.Count)
		limit := oc.Count
		if limit > 10 {
			limit = 10
		}
		for i := 0; i < limit; i++ {
			fmt.Printf("      - %s\n", oc.OrphanedIDs[i])
		}
		if oc.Count > 10 {
			fmt.Printf("      ... and %d more\n", oc.Count-10)
		}
	} else {
		fmt.Println("   [OK] No orphaned children found")
	}
	fmt.Println()
}
