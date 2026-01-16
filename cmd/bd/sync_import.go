package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/types"
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

// importFromJSONLInline imports the JSONL file directly without spawning a subprocess.
// This avoids path resolution issues when running from directories with .beads/redirect.
// The parent process's store and dbPath are used, ensuring consistent path resolution.
// (bd-ysal fix)
func importFromJSONLInline(ctx context.Context, jsonlPath string, renameOnImport bool, _ /* noGitHistory */ bool) error {
	// Verify we have an active store
	if store == nil {
		return fmt.Errorf("no database store available for inline import")
	}

	// Read and parse the JSONL file
	// #nosec G304 - jsonlPath is from findJSONLPath() which uses trusted paths
	f, err := os.Open(jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to open JSONL file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var allIssues []*types.Issue
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return fmt.Errorf("error parsing line %d: %w", lineNum, err)
		}
		issue.SetDefaults()
		allIssues = append(allIssues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading JSONL: %w", err)
	}

	// Import using shared logic
	opts := ImportOptions{
		RenameOnImport: renameOnImport,
	}
	result, err := importIssuesCore(ctx, dbPath, store, allIssues, opts)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}

	// Update staleness metadata (same as import.go lines 386-411)
	// This is critical: without this, CheckStaleness will still report stale
	if currentHash, hashErr := computeJSONLHash(jsonlPath); hashErr == nil {
		if err := store.SetMetadata(ctx, "jsonl_content_hash", currentHash); err != nil {
			debug.Logf("Warning: failed to update jsonl_content_hash: %v", err)
		}
		if err := store.SetJSONLFileHash(ctx, currentHash); err != nil {
			debug.Logf("Warning: failed to update jsonl_file_hash: %v", err)
		}
		importTime := time.Now().Format(time.RFC3339Nano)
		if err := store.SetMetadata(ctx, "last_import_time", importTime); err != nil {
			debug.Logf("Warning: failed to update last_import_time: %v", err)
		}
	} else {
		debug.Logf("Warning: failed to compute JSONL hash: %v", hashErr)
	}

	// Update database mtime
	if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
		debug.Logf("Warning: failed to update database mtime: %v", err)
	}

	// Print summary
	fmt.Fprintf(os.Stderr, "Import complete: %d created, %d updated", result.Created, result.Updated)
	if result.Unchanged > 0 {
		fmt.Fprintf(os.Stderr, ", %d unchanged", result.Unchanged)
	}
	if result.Skipped > 0 {
		fmt.Fprintf(os.Stderr, ", %d skipped", result.Skipped)
	}
	fmt.Fprintf(os.Stderr, "\n")

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
// GH#1110: Now uses RepoContext to ensure git commands run in beads repo.
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

	// Get RepoContext for beads repo
	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("failed to get repo context: %w", err)
	}

	// Verify the configured remote exists
	checkRemoteCmd := rc.GitCmd(ctx, "remote", "get-url", remote)
	if err := checkRemoteCmd.Run(); err != nil {
		return fmt.Errorf("configured sync.remote '%s' does not exist (run 'git remote add %s <url>')", remote, remote)
	}

	defaultBranch := getDefaultBranchForRemote(ctx, remote)

	// Step 1: Fetch from main
	fmt.Printf("→ Fetching from %s/%s...\n", remote, defaultBranch)
	fetchCmd := rc.GitCmd(ctx, "fetch", remote, defaultBranch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch %s %s failed: %w\n%s", remote, defaultBranch, err, output)
	}

	// Step 2: Checkout .beads/ directory from main
	fmt.Printf("→ Checking out beads from %s/%s...\n", remote, defaultBranch)
	checkoutCmd := rc.GitCmd(ctx, "checkout", fmt.Sprintf("%s/%s", remote, defaultBranch), "--", ".beads/")
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
