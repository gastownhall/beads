package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
			// ZFC safety check: if DB significantly diverges from JSONL,
			// force import first to sync with JSONL source of truth.
			// After import, skip export to prevent overwriting JSONL (JSONL is source of truth).
			//
			// Added REVERSE ZFC check - if JSONL has MORE issues than DB,
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

						// Case 2: JSONL has significantly more issues than DB
						// This is the DANGEROUS case - exporting would lose issues!
						// A stale/empty DB exporting over a populated JSONL causes data loss.
						if jsonlCount > dbCount && !skipExport {
							divergence := float64(jsonlCount-dbCount) / float64(jsonlCount)
							// Use stricter threshold for this dangerous case:
							// - Any loss > 20% is suspicious
							// - Complete loss (DB empty) is always blocked
							if dbCount == 0 || divergence > 0.2 {
								fmt.Printf("→ JSONL has %d issues but DB has only %d (stale DB detected)\n", jsonlCount, dbCount)
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

				// Case 3: JSONL content differs from DB (hash mismatch)
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
						fmt.Println("→ JSONL content differs from last sync")
						fmt.Println("→ Importing JSONL first to prevent stale DB from overwriting changes...")
						if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
							FatalError("importing (hash mismatch): %v", err)
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

			// Clear sync state on successful sync (daemon backoff/hints)
			_ = ClearSyncState(beadsDir)

			fmt.Println("\n✓ Sync complete")
			return
		}

		// Check if sync.branch is configured for worktree-based sync
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

		// Force-push detection for sync branch (bd-hlsw.4)
		// Check if the remote sync branch was force-pushed since last sync
		if useSyncBranch && !noPull && !dryRun {
			forcePushStatus, err := syncbranch.CheckForcePush(ctx, store, repoRoot, syncBranchName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not check for force-push: %v\n", err)
			} else if forcePushStatus.Detected {
				fmt.Fprintf(os.Stderr, "\n⚠️  %s\n\n", forcePushStatus.Message)

				if acceptRebase {
					// User explicitly accepted the rebase via --accept-rebase flag
					fmt.Println("→ --accept-rebase specified, resetting to remote state...")
					if err := syncbranch.ResetToRemote(ctx, repoRoot, syncBranchName, jsonlPath); err != nil {
						FatalError("failed to reset to remote: %v", err)
					}
					// Clear the stored SHA since we're resetting
					if err := syncbranch.ClearStoredRemoteSHA(ctx, store); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to clear stored remote SHA: %v\n", err)
					}
					fmt.Println("✓ Reset to remote sync branch state")
					fmt.Println("→ Re-importing JSONL after reset...")
					if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
						FatalError("importing after reset: %v", err)
					}
					fmt.Println("✓ Import complete after reset")

					// Update stored SHA to current remote
					if err := syncbranch.UpdateStoredRemoteSHA(ctx, store, repoRoot, syncBranchName); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to update stored remote SHA: %v\n", err)
					}

					fmt.Println("\n✓ Sync complete (reset to remote after force-push)")
					return
				}

				// Prompt for confirmation
				fmt.Fprintln(os.Stderr, "Options:")
				fmt.Fprintln(os.Stderr, "  1. Reset to remote (discard local sync branch changes)")
				fmt.Fprintln(os.Stderr, "  2. Abort sync (investigate manually)")
				fmt.Fprintln(os.Stderr, "")
				fmt.Fprintln(os.Stderr, "To reset automatically, run: bd sync --accept-rebase")
				fmt.Fprintln(os.Stderr, "")
				fmt.Fprint(os.Stderr, "Reset to remote state? [y/N]: ")

				var response string
				reader := bufio.NewReader(os.Stdin)
				response, _ = reader.ReadString('\n')
				response = strings.TrimSpace(strings.ToLower(response))

				if response == "y" || response == "yes" {
					fmt.Println("→ Resetting to remote state...")
					if err := syncbranch.ResetToRemote(ctx, repoRoot, syncBranchName, jsonlPath); err != nil {
						FatalError("failed to reset to remote: %v", err)
					}
					// Clear the stored SHA since we're resetting
					if err := syncbranch.ClearStoredRemoteSHA(ctx, store); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to clear stored remote SHA: %v\n", err)
					}
					fmt.Println("✓ Reset to remote sync branch state")
					fmt.Println("→ Re-importing JSONL after reset...")
					if err := importFromJSONL(ctx, jsonlPath, renameOnImport, noGitHistory); err != nil {
						FatalError("importing after reset: %v", err)
					}
					fmt.Println("✓ Import complete after reset")

					// Update stored SHA to current remote
					if err := syncbranch.UpdateStoredRemoteSHA(ctx, store, repoRoot, syncBranchName); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to update stored remote SHA: %v\n", err)
					}

					fmt.Println("\n✓ Sync complete (reset to remote after force-push)")
					return
				}

				// User chose to abort
				FatalErrorWithHint("sync aborted due to force-push detection",
					"investigate the sync branch history, then run 'bd sync --accept-rebase' to reset to remote")
			}
		}

		// Step 2: Check if there are changes to commit (check entire .beads/ directory)
		// GH#812: When using sync-branch, skip this check - CommitToSyncBranch handles it internally.
		// The main repo's .beads may be gitignored on code branches (valid per #797/#801).
		// In that case, gitHasBeadsChanges returns false even when JSONL has changes,
		// because git doesn't track the files. CommitToSyncBranch copies files to the
		// worktree where they ARE tracked (different gitignore) and checks there.
		var hasChanges bool
		var err error
		if !useSyncBranch {
			hasChanges, err = gitHasBeadsChanges(ctx)
			if err != nil {
				FatalError("checking git status: %v", err)
			}
		} else {
			// Let CommitToSyncBranch determine if there are actual changes in the worktree
			hasChanges = true
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
				// Use worktree to commit to sync branch
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
				} else {
					// GH#812: When useSyncBranch is true, we always attempt commit
					// (bypassing gitHasBeadsChanges). Report when worktree has no changes.
					fmt.Println("→ No changes to commit")
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
					// Pull from sync branch via worktree
					fmt.Printf("→ Pulling from sync branch '%s'...\n", syncBranchName)

					// Check if confirmation is required for mass deletion
					requireMassDeleteConfirmation := config.GetBool("sync.require_confirmation_on_mass_delete")

					pullResult, err := syncbranch.PullFromSyncBranch(ctx, repoRoot, syncBranchName, jsonlPath, !noPush, requireMassDeleteConfirmation)
					if err != nil {
						FatalError("pulling from sync branch: %v", err)
					}
					if pullResult.Pulled {
						if pullResult.Merged {
							// Divergent histories were merged at content level
							fmt.Printf("✓ Merged divergent histories from %s\n", syncBranchName)

							// Print safety warnings from result
							for _, warning := range pullResult.SafetyWarnings {
								fmt.Fprintln(os.Stderr, warning)
							}

							// Handle safety check with confirmation requirement
							if pullResult.SafetyCheckTriggered && !pullResult.Pushed {
								// Don't duplicate SafetyCheckDetails - it's already in SafetyWarnings
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
								// Auto-push after merge
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
				// tombstoning issues that were in our local export but got lost during merge
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
									// Commit to sync branch via worktree
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
			// Clean up temporary snapshot files after successful sync
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

			// Clear sync state on successful sync (daemon backoff/hints)
			if bd := beads.FindBeadsDir(); bd != "" {
				_ = ClearSyncState(bd)
			}

			// Update stored remote SHA after successful sync (bd-hlsw.4)
			// This enables force-push detection on subsequent syncs
			if useSyncBranch && !noPush {
				if err := syncbranch.UpdateStoredRemoteSHA(ctx, store, repoRoot, syncBranchName); err != nil {
					debug.Logf("sync: failed to update stored remote SHA: %v", err)
				}
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
