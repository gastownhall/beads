package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// CheckGitHooks verifies that recommended git hooks are installed.
func CheckGitHooks() DoctorCheck {
	// Check if we're in a git repository using worktree-aware detection
	gitDir, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Git Hooks",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Recommended hooks and their purposes
	recommendedHooks := map[string]string{
		"pre-commit": "Flushes pending bd changes to JSONL before commit",
		"post-merge": "Imports updated JSONL after git pull/merge",
		"pre-push":   "Exports database to JSONL before push",
	}

	hooksDir := filepath.Join(gitDir, "hooks")
	var missingHooks []string
	var installedHooks []string

	for hookName := range recommendedHooks {
		hookPath := filepath.Join(hooksDir, hookName)
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			missingHooks = append(missingHooks, hookName)
		} else {
			installedHooks = append(installedHooks, hookName)
		}
	}

	if len(missingHooks) == 0 {
		return DoctorCheck{
			Name:    "Git Hooks",
			Status:  StatusOK,
			Message: "All recommended hooks installed",
			Detail:  fmt.Sprintf("Installed: %s", strings.Join(installedHooks, ", ")),
		}
	}

	hookInstallMsg := "Install hooks with 'bd hooks install'. See https://github.com/steveyegge/beads/tree/main/examples/git-hooks for installation instructions"

	if len(installedHooks) > 0 {
		return DoctorCheck{
			Name:    "Git Hooks",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Missing %d recommended hook(s)", len(missingHooks)),
			Detail:  fmt.Sprintf("Missing: %s", strings.Join(missingHooks, ", ")),
			Fix:     hookInstallMsg,
		}
	}

	return DoctorCheck{
		Name:    "Git Hooks",
		Status:  StatusWarning,
		Message: "No recommended git hooks installed",
		Detail:  fmt.Sprintf("Recommended: %s", strings.Join([]string{"pre-commit", "post-merge", "pre-push"}, ", ")),
		Fix:     hookInstallMsg,
	}
}

// CheckSyncBranchHookCompatibility checks if pre-push hook is compatible with sync-branch mode.
// When sync-branch is configured, the pre-push hook must have the sync-branch bypass logic
// (added in version 0.29.0). Without it, users experience circular "bd sync" failures (issue #532).
func CheckSyncBranchHookCompatibility(path string) DoctorCheck {
	// Check if sync-branch is configured
	syncBranch := syncbranch.GetFromYAML()
	if syncBranch == "" {
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusOK,
			Message: "N/A (sync-branch not configured)",
		}
	}

	// sync-branch is configured - check pre-push hook version
	// Get actual git directory (handles worktrees where .git is a file)
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}
	gitDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}

	// Check for pre-push hook in standard location or shared hooks location
	var hookPath string

	// First check if core.hooksPath is configured (shared hooks)
	hooksPathCmd := exec.Command("git", "config", "--get", "core.hooksPath")
	hooksPathCmd.Dir = path
	if hooksPathOutput, err := hooksPathCmd.Output(); err == nil {
		sharedHooksDir := strings.TrimSpace(string(hooksPathOutput))
		if !filepath.IsAbs(sharedHooksDir) {
			sharedHooksDir = filepath.Join(path, sharedHooksDir)
		}
		hookPath = filepath.Join(sharedHooksDir, "pre-push")
	} else {
		// Use standard .git/hooks location
		hookPath = filepath.Join(gitDir, "hooks", "pre-push")
	}

	hookContent, err := os.ReadFile(hookPath) // #nosec G304 - path is controlled
	if err != nil {
		// No pre-push hook installed - different issue, covered by checkGitHooks
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusOK,
			Message: "N/A (no pre-push hook installed)",
		}
	}

	// Check if this is a bd hook and extract version
	hookStr := string(hookContent)
	if !strings.Contains(hookStr, "bd-hooks-version:") {
		// Not a bd hook - can't determine compatibility
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusWarning,
			Message: "Pre-push hook is not a bd hook",
			Detail:  "Cannot verify sync-branch compatibility with custom hooks",
			Fix: "Either run 'bd hooks install --force' to use bd hooks,\n" +
				"  or ensure your custom hook skips validation when pushing to sync-branch",
		}
	}

	// Extract version from hook
	var hookVersion string
	for _, line := range strings.Split(hookStr, "\n") {
		if strings.Contains(line, "bd-hooks-version:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				hookVersion = strings.TrimSpace(parts[1])
			}
			break
		}
	}

	if hookVersion == "" {
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusWarning,
			Message: "Could not determine pre-push hook version",
			Detail:  "Cannot verify sync-branch compatibility",
			Fix:     "Run 'bd hooks install --force' to update hooks",
		}
	}

	// MinSyncBranchHookVersion added sync-branch bypass logic
	// If hook version < MinSyncBranchHookVersion, it will cause circular "bd sync" failures
	if CompareVersions(hookVersion, MinSyncBranchHookVersion) < 0 {
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusError,
			Message: fmt.Sprintf("Pre-push hook incompatible with sync-branch mode (version %s)", hookVersion),
			Detail:  fmt.Sprintf("Hook version %s lacks sync-branch bypass (requires %s+). This causes circular 'bd sync' failures during push.", hookVersion, MinSyncBranchHookVersion),
			Fix:     "Run 'bd hooks install --force' to update hooks",
		}
	}

	return DoctorCheck{
		Name:    "Sync Branch Hook Compatibility",
		Status:  StatusOK,
		Message: fmt.Sprintf("Pre-push hook compatible with sync-branch (version %s)", hookVersion),
	}
}

// CheckMergeDriver verifies that the git merge driver is correctly configured.
func CheckMergeDriver(path string) DoctorCheck {
	// Check if we're in a git repository using worktree-aware detection
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Git Merge Driver",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Get current merge driver configuration
	cmd := exec.Command("git", "config", "merge.beads.driver")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		// Merge driver not configured
		return DoctorCheck{
			Name:    "Git Merge Driver",
			Status:  StatusWarning,
			Message: "Git merge driver not configured",
			Fix:     "Run 'bd init' to configure the merge driver, or manually: git config merge.beads.driver \"bd merge %A %O %A %B\"",
		}
	}

	currentConfig := strings.TrimSpace(string(output))
	correctConfig := "bd merge %A %O %A %B"

	// Check if using old incorrect placeholders
	if strings.Contains(currentConfig, "%L") || strings.Contains(currentConfig, "%R") {
		return DoctorCheck{
			Name:    "Git Merge Driver",
			Status:  StatusError,
			Message: fmt.Sprintf("Incorrect merge driver config: %q (uses invalid %%L/%%R placeholders)", currentConfig),
			Detail:  "Git only supports %O (base), %A (current), %B (other). Using %L/%R causes merge failures.",
			Fix:     "Run 'bd doctor --fix' to update to correct config, or manually: git config merge.beads.driver \"bd merge %A %O %A %B\"",
		}
	}

	// Check if config is correct
	if currentConfig != correctConfig {
		return DoctorCheck{
			Name:    "Git Merge Driver",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Non-standard merge driver config: %q", currentConfig),
			Detail:  fmt.Sprintf("Expected: %q", correctConfig),
			Fix:     fmt.Sprintf("Run 'bd doctor --fix' to update config, or manually: git config merge.beads.driver \"%s\"", correctConfig),
		}
	}

	return DoctorCheck{
		Name:    "Git Merge Driver",
		Status:  StatusOK,
		Message: "Correctly configured",
		Detail:  currentConfig,
	}
}

// CheckSyncBranchConfig checks if sync-branch is properly configured.
func CheckSyncBranchConfig(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Skip if .beads doesn't exist
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusOK,
			Message: "N/A (no .beads directory)",
		}
	}

	// Check if we're in a git repository using worktree-aware detection
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Check sync-branch from config.yaml or environment variable
	// This is the source of truth for multi-clone setups
	syncBranch := syncbranch.GetFromYAML()

	// Get current branch
	currentBranch := ""
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = path
	if output, err := cmd.Output(); err == nil {
		currentBranch = strings.TrimSpace(string(output))
	}

	// CRITICAL: Check if we're on the sync branch - this is a misconfiguration
	// that will cause bd sync to fail trying to create a worktree for a branch
	// that's already checked out
	if syncBranch != "" && currentBranch == syncBranch {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusError,
			Message: fmt.Sprintf("On sync branch '%s'", syncBranch),
			Detail:  fmt.Sprintf("Currently on branch '%s' which is configured as the sync branch. bd sync cannot create a worktree for a branch that's already checked out.", syncBranch),
			Fix:     "Switch to your main working branch: git checkout main",
		}
	}

	if syncBranch != "" {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusOK,
			Message: fmt.Sprintf("Configured (%s)", syncBranch),
			Detail:  fmt.Sprintf("Current branch: %s, sync branch: %s", currentBranch, syncBranch),
		}
	}

	// Not configured - this is optional but recommended for multi-clone setups
	// Check if this looks like a multi-clone setup (has remote)
	hasRemote := false
	cmd = exec.Command("git", "remote")
	cmd.Dir = path
	if output, err := cmd.Output(); err == nil && len(strings.TrimSpace(string(output))) > 0 {
		hasRemote = true
	}

	if hasRemote {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusWarning,
			Message: "sync-branch not configured",
			Detail:  "Multi-clone setups should configure sync-branch in config.yaml",
			Fix:     "Add 'sync-branch: beads-sync' to .beads/config.yaml",
		}
	}

	// No remote - probably a local-only repo, sync-branch not needed
	return DoctorCheck{
		Name:    "Sync Branch Config",
		Status:  StatusOK,
		Message: "N/A (no remote configured)",
	}
}

// CheckSyncBranchHealth detects when the sync branch has diverged from main
// or from the remote sync branch (after a force-push reset).
// bd-6rf: Detect and fix stale beads-sync branch
func CheckSyncBranchHealth(path string) DoctorCheck {
	// Skip if not in a git repo using worktree-aware detection
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Get configured sync branch
	syncBranch := syncbranch.GetFromYAML()
	if syncBranch == "" {
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: "N/A (no sync branch configured)",
		}
	}

	// Check if local sync branch exists
	cmd := exec.Command("git", "rev-parse", "--verify", syncBranch) // #nosec G204 - syncBranch from config file
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		// Local branch doesn't exist - that's fine, bd sync will create it
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: fmt.Sprintf("N/A (local %s branch not created yet)", syncBranch),
		}
	}

	// Check if remote sync branch exists
	remote := "origin"
	remoteBranch := fmt.Sprintf("%s/%s", remote, syncBranch)
	cmd = exec.Command("git", "rev-parse", "--verify", remoteBranch) // #nosec G204 - remoteBranch from config
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		// Remote branch doesn't exist - that's fine
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: fmt.Sprintf("N/A (remote %s not found)", remoteBranch),
		}
	}

	// Check 1: Is local sync branch diverged from remote? (after force-push)
	// If they have no common ancestor in recent history, the remote was likely force-pushed
	cmd = exec.Command("git", "merge-base", syncBranch, remoteBranch) // #nosec G204 - branches from config
	cmd.Dir = path
	mergeBaseOutput, err := cmd.Output()
	if err != nil {
		// No common ancestor - branches have completely diverged
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Local %s diverged from remote", syncBranch),
			Detail:  "The remote sync branch was likely reset/force-pushed. Your local branch has orphaned history.",
			Fix:     "Run 'bd doctor --fix' to reset sync branch",
		}
	}

	// Check if local is behind remote (needs to fast-forward)
	mergeBase := strings.TrimSpace(string(mergeBaseOutput))
	cmd = exec.Command("git", "rev-parse", syncBranch) // #nosec G204 - syncBranch from config
	cmd.Dir = path
	localHead, _ := cmd.Output()
	localHeadStr := strings.TrimSpace(string(localHead))

	cmd = exec.Command("git", "rev-parse", remoteBranch) // #nosec G204 - remoteBranch from config
	cmd.Dir = path
	remoteHead, _ := cmd.Output()
	remoteHeadStr := strings.TrimSpace(string(remoteHead))

	// If merge base equals local but not remote, local is behind
	if mergeBase == localHeadStr && mergeBase != remoteHeadStr {
		// Count how far behind
		cmd = exec.Command("git", "rev-list", "--count", fmt.Sprintf("%s..%s", syncBranch, remoteBranch)) // #nosec G204 - branches from config
		cmd.Dir = path
		countOutput, _ := cmd.Output()
		behindCount := strings.TrimSpace(string(countOutput))

		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: fmt.Sprintf("Local %s is %s commits behind remote (will sync)", syncBranch, behindCount),
		}
	}

	// Check 2: Is sync branch far behind main on source files?
	// Get the main branch name
	mainBranch := "main"
	cmd = exec.Command("git", "rev-parse", "--verify", "main")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		// Try "master" as fallback
		cmd = exec.Command("git", "rev-parse", "--verify", "master")
		cmd.Dir = path
		if err := cmd.Run(); err != nil {
			// Can't determine main branch
			return DoctorCheck{
				Name:    "Sync Branch Health",
				Status:  StatusOK,
				Message: "OK",
			}
		}
		mainBranch = "master"
	}

	// Count commits main is ahead of sync branch
	cmd = exec.Command("git", "rev-list", "--count", fmt.Sprintf("%s..%s", syncBranch, mainBranch)) // #nosec G204 - branches from config/hardcoded
	cmd.Dir = path
	aheadOutput, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: "OK",
		}
	}
	aheadCount := strings.TrimSpace(string(aheadOutput))

	// Check if there are non-.beads/ file differences (stale source code)
	cmd = exec.Command("git", "diff", "--name-only", fmt.Sprintf("%s..%s", syncBranch, mainBranch), "--", ":(exclude).beads/") // #nosec G204 - branches from config/hardcoded
	cmd.Dir = path
	diffOutput, _ := cmd.Output()
	diffFiles := strings.TrimSpace(string(diffOutput))

	if diffFiles != "" && aheadCount != "0" {
		// Count the number of different files
		fileCount := len(strings.Split(diffFiles, "\n"))
		// Parse ahead count as int for comparison
		aheadCountInt := 0
		_, _ = fmt.Sscanf(aheadCount, "%d", &aheadCountInt)

		// Only warn if significantly behind (20+ commits AND 50+ source files)
		// Small drift is normal between bd sync operations
		if fileCount > 50 && aheadCountInt > 20 {
			return DoctorCheck{
				Name:    "Sync Branch Health",
				Status:  StatusWarning,
				Message: fmt.Sprintf("Sync branch %s commits behind %s on source files", aheadCount, mainBranch),
				Detail:  fmt.Sprintf("%d source files differ between %s and %s. The sync branch has stale code.", fileCount, syncBranch, mainBranch),
				Fix:     "Run 'bd doctor --fix' to reset sync branch to main",
			}
		}
	}

	return DoctorCheck{
		Name:    "Sync Branch Health",
		Status:  StatusOK,
		Message: "OK",
	}
}

// FixGitHooks fixes missing or broken git hooks by calling bd hooks install.
func FixGitHooks(path string) error {
	return fix.GitHooks(path)
}

// FixMergeDriver fixes the git merge driver configuration to use correct placeholders.
func FixMergeDriver(path string) error {
	return fix.MergeDriver(path)
}

// FixSyncBranchHealth fixes database-JSONL sync issues.
func FixSyncBranchHealth(path string) error {
	return fix.DBJSONLSync(path)
}

// CheckOrphanedIssues detects issues referenced in git commits but still open.
// This catches cases where someone implemented a fix with "(bd-xxx)" in the commit
// message but forgot to run "bd close".
func CheckOrphanedIssues(path string) DoctorCheck {
	// Skip if not in a git repo (check from path directory)
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (not a git repository)",
			Category: CategoryGit,
		}
	}

	beadsDir := filepath.Join(path, ".beads")

	// Skip if no .beads directory
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (no .beads directory)",
			Category: CategoryGit,
		}
	}

	// Get database path from config or use canonical name
	dbPath := filepath.Join(beadsDir, "beads.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (no database)",
			Category: CategoryGit,
		}
	}

	// Open database read-only
	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (unable to open database)",
			Category: CategoryGit,
		}
	}
	defer db.Close()

	// Get issue prefix from config
	var issuePrefix string
	err = db.QueryRow("SELECT value FROM config WHERE key = 'issue_prefix'").Scan(&issuePrefix)
	if err != nil || issuePrefix == "" {
		issuePrefix = "bd" // default
	}

	// Get all open issue IDs
	rows, err := db.Query("SELECT id FROM issues WHERE status IN ('open', 'in_progress')")
	if err != nil {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (unable to query issues)",
			Category: CategoryGit,
		}
	}
	defer rows.Close()

	openSet := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			openSet[id] = true
		}
	}

	if len(openSet) == 0 {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "No open issues to check",
			Category: CategoryGit,
		}
	}

	// Get issue IDs referenced in git commits
	cmd = exec.Command("git", "log", "--oneline", "--all")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (unable to read git log)",
			Category: CategoryGit,
		}
	}

	// Parse commit messages for issue references
	// Match pattern like (bd-xxx) or (bd-xxx.1) including hierarchical IDs
	pattern := fmt.Sprintf(`\(%s-[a-z0-9.]+\)`, regexp.QuoteMeta(issuePrefix))
	re := regexp.MustCompile(pattern)

	// Track which open issues appear in commits (with first commit hash)
	orphanedIssues := make(map[string]string) // issue ID -> commit hash
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		matches := re.FindAllString(line, -1)
		for _, match := range matches {
			// Extract issue ID (remove parentheses)
			issueID := strings.Trim(match, "()")
			if openSet[issueID] {
				// Only record the first (most recent) commit
				if _, exists := orphanedIssues[issueID]; !exists {
					// Extract commit hash (first word of line)
					parts := strings.SplitN(line, " ", 2)
					if len(parts) > 0 {
						orphanedIssues[issueID] = parts[0]
					}
				}
			}
		}
	}

	if len(orphanedIssues) == 0 {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "No issues referenced in commits but still open",
			Category: CategoryGit,
		}
	}

	// Build detail message
	var details []string
	for id, commit := range orphanedIssues {
		details = append(details, fmt.Sprintf("%s (commit %s)", id, commit))
	}

	return DoctorCheck{
		Name:     "Orphaned Issues",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d issue(s) referenced in commits but still open", len(orphanedIssues)),
		Detail:   strings.Join(details, ", "),
		Fix:      "Run 'bd show <id>' to check if implemented, then 'bd close <id>' if done",
		Category: CategoryGit,
	}
}

// openDBReadOnly opens a SQLite database in read-only mode
func openDBReadOnly(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
}
