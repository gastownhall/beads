package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

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

// doSyncFromMain performs a one-way sync from the default branch (main/master)
// Used for ephemeral branches without upstream tracking.
// This fetches beads from main and imports them, discarding local beads changes.
// If sync.remote is configured (e.g., "upstream" for fork workflows), uses that remote
// instead of "origin".
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
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", remote, defaultBranch) //nolint:gosec // remote and defaultBranch from config
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch %s %s failed: %w\n%s", remote, defaultBranch, err, output)
	}

	// Step 2: Checkout .beads/ directory from main
	fmt.Printf("→ Checking out beads from %s/%s...\n", remote, defaultBranch)
	checkoutCmd := exec.CommandContext(ctx, "git", "checkout", fmt.Sprintf("%s/%s", remote, defaultBranch), "--", ".beads/") //nolint:gosec // remote and defaultBranch from config
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
