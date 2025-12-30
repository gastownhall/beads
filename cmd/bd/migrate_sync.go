package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/syncbranch"
)

var migrateSyncCmd = &cobra.Command{
	Use:     "sync <branch-name>",
	Short:   "Migrate to sync.branch workflow for multi-clone setups",
	Long: `Migrate to using a dedicated sync branch for beads data.

This command configures the repository to commit .beads changes to a separate
branch (e.g., "beads-sync") instead of the current working branch. This is
essential for multi-clone setups where multiple clones work independently
but need to sync beads data.

The command will:
  1. Validate the current state (not already configured, not on sync branch)
  2. Create the sync branch if it doesn't exist (from remote or locally)
  3. Set up the git worktree for the sync branch
  4. Set the sync.branch configuration

After migration, 'bd sync' will commit beads changes to the sync branch via
a git worktree, keeping your working branch clean of beads commits.

Examples:
  # Basic migration to beads-sync branch
  bd migrate-sync beads-sync

  # Preview what would happen without making changes
  bd migrate-sync beads-sync --dry-run

  # Force migration even if already configured
  bd migrate-sync beads-sync --force`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		branchName := args[0]
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		force, _ := cmd.Flags().GetBool("force")

		if err := runMigrateSync(ctx, branchName, dryRun, force); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	migrateSyncCmd.Flags().Bool("dry-run", false, "Preview migration without making changes")
	migrateSyncCmd.Flags().Bool("force", false, "Force migration even if already configured")
	migrateCmd.AddCommand(migrateSyncCmd)

	// Backwards compatibility alias at root level (hidden)
	migrateSyncAliasCmd := *migrateSyncCmd
	migrateSyncAliasCmd.Use = "migrate-sync"
	migrateSyncAliasCmd.Hidden = true
	migrateSyncAliasCmd.Deprecated = "use 'bd migrate sync' instead (will be removed in v1.0.0)"
	rootCmd.AddCommand(&migrateSyncAliasCmd)
}

func runMigrateSync(ctx context.Context, branchName string, dryRun, force bool) error {
	// Validate branch name
	if err := syncbranch.ValidateBranchName(branchName); err != nil {
		return fmt.Errorf("invalid branch name: %w", err)
	}

	// Check if we're in a git repository
	if !isGitRepo() {
		return fmt.Errorf("not in a git repository")
	}

	// Ensure store is initialized for config operations
	if err := ensureDirectMode("migrate-sync requires direct database access"); err != nil {
		return err
	}

	// Get current branch
	currentBranch, err := getCurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}

	// Check if already on the sync branch
	if currentBranch == branchName {
		return fmt.Errorf("currently on branch '%s' - switch to your main working branch first (e.g., 'git checkout main')", branchName)
	}

	// Check if sync.branch is already configured
	existingSyncBranch, err := syncbranch.Get(ctx, store)
	if err != nil {
		return fmt.Errorf("failed to check existing config: %w", err)
	}

	if existingSyncBranch != "" && !force {
		if existingSyncBranch == branchName {
			fmt.Printf("✓ Already configured to use sync branch '%s'\n", branchName)
			fmt.Println("  Use --force to reconfigure anyway")
			return nil
		}
		return fmt.Errorf("sync.branch already configured as '%s' (use --force to change to '%s')", existingSyncBranch, branchName)
	}

	// Check if we have a remote
	hasRemote := hasGitRemote(ctx)
	if !hasRemote {
		fmt.Println("⚠ Warning: No git remote configured. Sync branch will only exist locally.")
	}

	// Get repo root
	repoRoot, err := syncbranch.GetRepoRoot(ctx)
	if err != nil {
		return fmt.Errorf("failed to get repository root: %w", err)
	}

	// Find JSONL path
	jsonlPath := findJSONLPath()
	if jsonlPath == "" {
		return fmt.Errorf("not in a bd workspace (no .beads directory found)")
	}

	// Check if sync branch exists (locally or remotely)
	branchExistsLocally := branchExistsLocal(ctx, branchName)
	branchExistsRemotely := branchExistsRemote(ctx, branchName)

	if dryRun {
		fmt.Println("=== DRY RUN - No changes will be made ===")
		fmt.Println()
		fmt.Printf("Current branch: %s\n", currentBranch)
		fmt.Printf("Sync branch: %s\n", branchName)
		fmt.Printf("Repository root: %s\n", repoRoot)
		fmt.Printf("JSONL path: %s\n", jsonlPath)
		fmt.Println()

		if existingSyncBranch != "" {
			fmt.Printf("→ Would change sync.branch from '%s' to '%s'\n", existingSyncBranch, branchName)
		} else {
			fmt.Printf("→ Would set sync.branch to '%s'\n", branchName)
		}

		if branchExistsLocally {
			fmt.Printf("→ Branch '%s' exists locally\n", branchName)
		} else if branchExistsRemotely {
			fmt.Printf("→ Would create local branch '%s' from remote\n", branchName)
		} else {
			fmt.Printf("→ Would create new branch '%s'\n", branchName)
		}

		// Use worktree-aware git directory detection
		gitDir, err := git.GetGitDir()
		if err != nil {
			return fmt.Errorf("not a git repository: %w", err)
		}
		worktreePath := filepath.Join(gitDir, "beads-worktrees", branchName)
		fmt.Printf("→ Would create worktree at: %s\n", worktreePath)

		fmt.Println("\n=== END DRY RUN ===")
		return nil
	}

	// Step 1: Create the sync branch if it doesn't exist
	fmt.Printf("→ Setting up sync branch '%s'...\n", branchName)

	if !branchExistsLocally && !branchExistsRemotely {
		// Create new branch from current HEAD
		fmt.Printf("  Creating new branch '%s'...\n", branchName)
		createCmd := exec.CommandContext(ctx, "git", "branch", branchName)
		if output, err := createCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create branch: %w\n%s", err, output)
		}
	} else if !branchExistsLocally && branchExistsRemotely {
		// Fetch and create local tracking branch
		fmt.Printf("  Fetching remote branch '%s'...\n", branchName)
		fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", branchName)
		if output, err := fetchCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to fetch remote branch: %w\n%s", err, output)
		}

		// Create local branch tracking remote
		createCmd := exec.CommandContext(ctx, "git", "branch", branchName, "origin/"+branchName)
		if output, err := createCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create local tracking branch: %w\n%s", err, output)
		}
	} else {
		fmt.Printf("  Branch '%s' already exists locally\n", branchName)
	}

	// Step 2: Create the worktree
	// Use worktree-aware git directory detection
	gitDir, err := git.GetGitDir()
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}
	worktreePath := filepath.Join(gitDir, "beads-worktrees", branchName)
	fmt.Printf("→ Creating worktree at %s...\n", worktreePath)

	wtMgr := git.NewWorktreeManager(repoRoot)
	if err := wtMgr.CreateBeadsWorktree(branchName, worktreePath); err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}

	// Step 3: Sync current JSONL to worktree
	fmt.Println("→ Syncing current beads data to worktree...")

	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to get relative JSONL path: %w", err)
	}

	if err := wtMgr.SyncJSONLToWorktree(worktreePath, jsonlRelPath); err != nil {
		return fmt.Errorf("failed to sync JSONL to worktree: %w", err)
	}

	// Also sync other beads files
	beadsDir := filepath.Dir(jsonlPath)
	for _, filename := range []string{"deletions.jsonl", "metadata.json"} {
		srcPath := filepath.Join(beadsDir, filename)
		if _, err := os.Stat(srcPath); err == nil {
			relPath, err := filepath.Rel(repoRoot, srcPath)
			if err == nil {
				_ = wtMgr.SyncJSONLToWorktree(worktreePath, relPath)
			}
		}
	}

	// Step 4: Commit initial state to sync branch if there are changes
	fmt.Println("→ Committing initial state to sync branch...")

	worktreeJSONLPath := filepath.Join(worktreePath, jsonlRelPath)
	hasChanges, err := hasChangesInWorktreeDir(ctx, worktreePath)
	if err != nil {
		fmt.Printf("  Warning: failed to check for changes: %v\n", err)
	}

	if hasChanges {
		if err := commitInitialSyncState(ctx, worktreePath, jsonlRelPath); err != nil {
			fmt.Printf("  Warning: failed to commit initial state: %v\n", err)
		} else {
			fmt.Println("  Initial state committed to sync branch")
		}
	} else {
		// Check if .beads directory exists in worktree but no changes
		worktreeBeadsDir := filepath.Join(worktreePath, ".beads")
		if _, err := os.Stat(worktreeBeadsDir); os.IsNotExist(err) {
			// .beads doesn't exist in worktree - this is a fresh setup
			fmt.Println("  No existing beads data in sync branch")
		} else {
			fmt.Println("  Sync branch already has current beads data")
		}
	}
	_ = worktreeJSONLPath // silence unused warning

	// Step 5: Set sync.branch config
	fmt.Printf("→ Setting sync.branch to '%s'...\n", branchName)

	if err := syncbranch.Set(ctx, store, branchName); err != nil {
		return fmt.Errorf("failed to set sync.branch config: %w", err)
	}

	// Step 6: Push sync branch to remote if we have one
	if hasRemote {
		fmt.Printf("→ Pushing sync branch '%s' to remote...\n", branchName)
		pushCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "push", "--set-upstream", "origin", branchName)
		output, err := pushCmd.CombinedOutput()
		if err != nil {
			// Non-fatal - branch might already be up to date or push might fail for other reasons
			if !strings.Contains(string(output), "Everything up-to-date") {
				fmt.Printf("  Warning: failed to push sync branch: %v\n", err)
				fmt.Printf("  You may need to push manually: git push -u origin %s\n", branchName)
			}
		} else {
			fmt.Printf("  Pushed '%s' to origin\n", branchName)
		}
	}

	fmt.Println()
	fmt.Println("✓ Migration complete!")
	fmt.Println()
	fmt.Printf("  sync.branch: %s\n", branchName)
	fmt.Printf("  worktree: %s\n", worktreePath)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  • 'bd sync' will now commit beads changes to the sync branch")
	fmt.Println("  • Your working branch stays clean of beads commits")
	fmt.Println("  • Other clones should also run 'bd migrate-sync " + branchName + "'")

	return nil
}

// branchExistsLocal checks if a branch exists locally
func branchExistsLocal(ctx context.Context, branch string) bool {
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

// branchExistsRemote checks if a branch exists on origin remote
func branchExistsRemote(ctx context.Context, branch string) bool {
	// First fetch to ensure we have latest remote refs
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", "--prune")
	_ = fetchCmd.Run() // Best effort

	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return cmd.Run() == nil
}

// hasChangesInWorktreeDir checks if there are any uncommitted changes in the worktree
func hasChangesInWorktreeDir(ctx context.Context, worktreePath string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// commitInitialSyncState commits the initial beads state to the sync branch
func commitInitialSyncState(ctx context.Context, worktreePath, jsonlRelPath string) error {
	beadsRelDir := filepath.Dir(jsonlRelPath)

	// Stage all beads files
	addCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "add", beadsRelDir)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Commit
	commitCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "commit", "--no-verify", "-m", "bd migrate-sync: initial sync branch setup")
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		// Check if there's nothing to commit
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %w\n%s", err, output)
	}

	return nil
}
