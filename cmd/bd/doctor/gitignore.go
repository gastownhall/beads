package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitignoreTemplate is the canonical .beads/.gitignore content
const GitignoreTemplate = `# Dolt database (managed by Dolt, not git)
dolt/
dolt-access.lock

# Runtime files
bd.sock
bd.sock.startlock
sync-state.json
last-touched

# Local version tracking (prevents upgrade notification spam after git ops)
.local_version

# Worktree redirect file (contains relative path to main repo's .beads/)
# Must not be committed as paths would be wrong in other clones
redirect

# Sync state (local-only, per-machine)
# These files are machine-specific and should not be shared across clones
.sync.lock
export-state/

# Ephemeral store (SQLite - wisps/molecules, intentionally not versioned)
ephemeral.sqlite3
ephemeral.sqlite3-journal
ephemeral.sqlite3-wal
ephemeral.sqlite3-shm

# Dolt server management (auto-started by bd)
dolt-server.pid
dolt-server.log
dolt-server.lock

# Legacy files (from pre-Dolt versions)
*.db
*.db?*
*.db-journal
*.db-wal
*.db-shm
db.sqlite
bd.db
daemon.lock
daemon.log
daemon-*.log.gz
daemon.pid
# NOTE: Do NOT add negation patterns here.
# They would override fork protection in .git/info/exclude.
# Config files (metadata.json, config.yaml) are tracked by git by default
# since no pattern above ignores them.
`

// ProjectGitignorePatterns are patterns that should be in the project-root .gitignore
// to prevent accidentally committing Dolt database files.
var ProjectGitignorePatterns = []string{
	".dolt/",
	"*.db",
}

// projectGitignoreComment is the section header added to the project .gitignore
const projectGitignoreComment = "# Dolt database files (added by bd init)"

// requiredPatterns are patterns that MUST be in .beads/.gitignore
var requiredPatterns = []string{
	"*.db?*",
	"redirect",
	"last-touched",
	"bd.sock.startlock",
	".sync.lock",
	"export-state/",
	"dolt/",
	"dolt-access.lock",
	"ephemeral.sqlite3",
	"dolt-server.pid",
	"dolt-server.log",
	"dolt-server.lock",
}

// CheckGitignore checks if .beads/.gitignore is up to date
func CheckGitignore() DoctorCheck {
	gitignorePath := filepath.Join(".beads", ".gitignore")

	// If a redirect exists, check the gitignore at the redirect target instead
	redirectPath := filepath.Join(".beads", "redirect")
	// #nosec G304 -- redirect path is fixed to .beads/redirect
	if data, err := os.ReadFile(redirectPath); err == nil {
		target := parseRedirectTarget(data)
		if target != "" {
			beadsDir := filepath.Dir(redirectPath)
			resolvedTarget := resolveRedirectTarget(beadsDir, target)
			gitignorePath = filepath.Join(resolvedTarget, ".gitignore")
		}
	}

	// Check if file exists
	content, err := os.ReadFile(gitignorePath) // #nosec G304 -- path is constructed from known parts
	if err != nil {
		return DoctorCheck{
			Name:    "Gitignore",
			Status:  "warning",
			Message: ".beads/.gitignore not found",
			Fix:     "Run: bd init (safe to re-run) or bd doctor --fix",
		}
	}

	// Check for required patterns
	contentStr := string(content)
	var missing []string
	for _, pattern := range requiredPatterns {
		if !strings.Contains(contentStr, pattern) {
			missing = append(missing, pattern)
		}
	}

	if len(missing) > 0 {
		return DoctorCheck{
			Name:    "Gitignore",
			Status:  "warning",
			Message: "Outdated .beads/.gitignore (missing required patterns)",
			Detail:  "Missing: " + strings.Join(missing, ", "),
			Fix:     "Run: bd doctor --fix or bd init (safe to re-run)",
		}
	}

	return DoctorCheck{
		Name:    "Gitignore",
		Status:  "ok",
		Message: "Up to date",
	}
}

// FixGitignore updates .beads/.gitignore to the current template
func FixGitignore() error {
	gitignorePath := filepath.Join(".beads", ".gitignore")

	// If file exists and is read-only, fix permissions first
	if info, err := os.Stat(gitignorePath); err == nil {
		if info.Mode().Perm()&0200 == 0 { // No write permission for owner
			if err := os.Chmod(gitignorePath, 0600); err != nil {
				return err
			}
		}
	}

	// Write canonical template with secure file permissions
	if err := os.WriteFile(gitignorePath, []byte(GitignoreTemplate), 0600); err != nil {
		return err
	}

	// Ensure permissions are set correctly (some systems respect umask)
	if err := os.Chmod(gitignorePath, 0600); err != nil {
		return err
	}

	return nil
}

// CheckRedirectNotTracked verifies that .beads/redirect is NOT tracked by git.
// Redirect files contain relative paths that only work in the original worktree.
// If committed, they cause warnings in other clones where the path is invalid.
func CheckRedirectNotTracked() DoctorCheck {
	redirectPath := filepath.Join(".beads", "redirect")

	// First check if the file exists
	if _, err := os.Stat(redirectPath); os.IsNotExist(err) {
		// File doesn't exist - nothing to check
		return DoctorCheck{
			Name:    "Redirect Tracking",
			Status:  StatusOK,
			Message: "No redirect file present",
		}
	}

	// Check if git considers this file tracked
	// git ls-files exits 0 and outputs the filename if tracked, empty if untracked
	cmd := exec.Command("git", "ls-files", redirectPath) // #nosec G204 - args are hardcoded paths
	output, err := cmd.Output()
	if err != nil {
		// Not in a git repo or git error - skip check
		return DoctorCheck{
			Name:    "Redirect Tracking",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	trackedPath := strings.TrimSpace(string(output))
	if trackedPath == "" {
		// File exists but is not tracked - this is correct
		return DoctorCheck{
			Name:    "Redirect Tracking",
			Status:  StatusOK,
			Message: "redirect file not tracked (correct)",
		}
	}

	// File is tracked - this is a problem
	return DoctorCheck{
		Name:    "Redirect Tracking",
		Status:  StatusWarning,
		Message: "redirect file is tracked by git",
		Detail:  "The .beads/redirect file contains a relative path that only works in this worktree. When committed, it causes warnings in other clones.",
		Fix:     "Run 'bd doctor --fix' to untrack, or manually: git rm --cached .beads/redirect",
	}
}

// FixRedirectTracking untracks the .beads/redirect file from git
func FixRedirectTracking() error {
	redirectPath := filepath.Join(".beads", "redirect")

	// Check if file is actually tracked first
	cmd := exec.Command("git", "ls-files", redirectPath) // #nosec G204 - args are hardcoded paths
	output, err := cmd.Output()
	if err != nil {
		return nil // Not a git repo, nothing to do
	}

	trackedPath := strings.TrimSpace(string(output))
	if trackedPath == "" {
		return nil // Not tracked, nothing to do
	}

	// Untrack the file (keeps the local copy)
	cmd = exec.Command("git", "rm", "--cached", redirectPath) // #nosec G204 - args are hardcoded paths
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to untrack redirect file: %w", err)
	}

	return nil
}

// parseRedirectTarget extracts the first non-comment, non-empty redirect target.
// It also strips a UTF-8 BOM if present.
func parseRedirectTarget(data []byte) string {
	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}

	return ""
}

// resolveRedirectTarget resolves a redirect target relative to the .beads parent.
// Absolute targets are cleaned and returned as-is.
func resolveRedirectTarget(beadsDir string, target string) string {
	if target == "" {
		return ""
	}

	resolvedTarget := target
	if !filepath.IsAbs(target) {
		projectRoot := filepath.Dir(beadsDir)
		resolvedTarget = filepath.Join(projectRoot, target)
	}
	resolvedTarget = filepath.Clean(resolvedTarget)
	if absPath, err := filepath.Abs(resolvedTarget); err == nil {
		resolvedTarget = absPath
	}

	return resolvedTarget
}

// CheckRedirectTargetValid verifies that the redirect target exists and has a valid beads database.
// This catches cases where the redirect points to a non-existent directory or one without a database.
func CheckRedirectTargetValid() DoctorCheck {
	redirectPath := filepath.Join(".beads", "redirect")

	// Check if redirect file exists
	data, err := os.ReadFile(redirectPath) // #nosec G304 - path is hardcoded
	if os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Redirect Target Valid",
			Status:  StatusOK,
			Message: "No redirect configured",
		}
	}
	if err != nil {
		return DoctorCheck{
			Name:    "Redirect Target Valid",
			Status:  StatusWarning,
			Message: "Cannot read redirect file",
			Detail:  err.Error(),
		}
	}

	// Parse redirect target
	target := parseRedirectTarget(data)
	if target == "" {
		return DoctorCheck{
			Name:    "Redirect Target Valid",
			Status:  StatusWarning,
			Message: "Redirect file is empty",
			Fix:     "Remove the empty redirect file or add a valid path",
		}
	}

	beadsDir := filepath.Dir(redirectPath)
	resolvedTarget := resolveRedirectTarget(beadsDir, target)

	// Check if target directory exists
	info, err := os.Stat(resolvedTarget)
	if os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Redirect Target Valid",
			Status:  StatusError,
			Message: "Redirect target does not exist",
			Detail:  fmt.Sprintf("Target: %s", resolvedTarget),
			Fix:     "Fix the redirect path or create the target directory",
		}
	}
	if err != nil {
		return DoctorCheck{
			Name:    "Redirect Target Valid",
			Status:  StatusWarning,
			Message: "Cannot access redirect target",
			Detail:  err.Error(),
		}
	}
	if !info.IsDir() {
		return DoctorCheck{
			Name:    "Redirect Target Valid",
			Status:  StatusError,
			Message: "Redirect target is not a directory",
			Detail:  fmt.Sprintf("Target: %s", resolvedTarget),
		}
	}

	// Check for valid beads database in target
	// First check for Dolt backend via metadata.json — Dolt server mode has no local .db file
	metadataPath := filepath.Join(resolvedTarget, "metadata.json")
	metadataData, metaErr := os.ReadFile(metadataPath) // #nosec G304 -- constructed from known path
	if metaErr == nil && strings.Contains(string(metadataData), `"backend"`) &&
		strings.Contains(string(metadataData), `"dolt"`) {
		return DoctorCheck{
			Name:    "Redirect Target Valid",
			Status:  StatusOK,
			Message: fmt.Sprintf("Redirect target valid (dolt backend): %s", resolvedTarget),
		}
	}

	dbPath := filepath.Join(resolvedTarget, "beads.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Also check for any .db file
		matches, _ := filepath.Glob(filepath.Join(resolvedTarget, "*.db"))
		if len(matches) == 0 {
			return DoctorCheck{
				Name:    "Redirect Target Valid",
				Status:  StatusWarning,
				Message: "Redirect target has no beads database",
				Detail:  fmt.Sprintf("Target: %s", resolvedTarget),
				Fix:     "Run 'bd init' in the target directory or check redirect path",
			}
		}
	}

	return DoctorCheck{
		Name:    "Redirect Target Valid",
		Status:  StatusOK,
		Message: fmt.Sprintf("Redirect target valid: %s", resolvedTarget),
	}
}

// CheckRedirectTargetSyncWorktree verifies that the redirect target has a working beads-sync worktree.
// This is important for repos using sync-branch mode with redirects.
func CheckRedirectTargetSyncWorktree() DoctorCheck {
	redirectPath := filepath.Join(".beads", "redirect")

	// Check if redirect file exists
	data, err := os.ReadFile(redirectPath) // #nosec G304 - path is hardcoded
	if os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Redirect Target Sync",
			Status:  StatusOK,
			Message: "No redirect configured",
		}
	}
	if err != nil {
		return DoctorCheck{
			Name:    "Redirect Target Sync",
			Status:  StatusOK, // Don't warn if we can't read - other check handles that
			Message: "N/A (cannot read redirect)",
		}
	}

	target := parseRedirectTarget(data)
	if target == "" {
		return DoctorCheck{
			Name:    "Redirect Target Sync",
			Status:  StatusOK,
			Message: "N/A (empty redirect)",
		}
	}

	// Resolve the target path
	beadsDir := filepath.Dir(redirectPath)
	resolvedTarget := resolveRedirectTarget(beadsDir, target)

	// Check if the target has a sync-branch configured in config.yaml
	configPath := filepath.Join(resolvedTarget, "config.yaml")
	configData, err := os.ReadFile(configPath) // #nosec G304 - constructed from known path
	if err != nil {
		// No config.yaml means no sync-branch, which is fine
		return DoctorCheck{
			Name:    "Redirect Target Sync",
			Status:  StatusOK,
			Message: "N/A (target not using sync-branch mode)",
		}
	}

	// Simple check for sync-branch in config
	if !strings.Contains(string(configData), "sync-branch:") {
		return DoctorCheck{
			Name:    "Redirect Target Sync",
			Status:  StatusOK,
			Message: "N/A (target not using sync-branch mode)",
		}
	}

	// Target uses sync-branch - check for beads-sync worktree in the repo containing the target
	// The target is inside a .beads dir, so the repo is the parent of .beads
	targetRepoRoot := filepath.Dir(resolvedTarget)

	// Check for beads-sync worktree
	worktreePath := filepath.Join(targetRepoRoot, ".beads-sync")
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Redirect Target Sync",
			Status:  StatusWarning,
			Message: "Redirect target missing beads-sync worktree",
			Detail:  fmt.Sprintf("Expected worktree at: %s", worktreePath),
			Fix:     fmt.Sprintf("Run 'bd init' in %s to set up beads", targetRepoRoot),
		}
	}

	return DoctorCheck{
		Name:    "Redirect Target Sync",
		Status:  StatusOK,
		Message: "Redirect target has beads-sync worktree",
	}
}

// CheckNoVestigialSyncWorktrees detects beads-sync worktrees in redirected repos that are unused.
// When a repo uses .beads/redirect, it doesn't need its own beads-sync worktree since
// sync operations happen in the redirect target. These vestigial worktrees waste space.
func CheckNoVestigialSyncWorktrees() DoctorCheck {
	redirectPath := filepath.Join(".beads", "redirect")

	// Check if redirect file exists
	if _, err := os.Stat(redirectPath); os.IsNotExist(err) {
		// No redirect - this check doesn't apply
		return DoctorCheck{
			Name:    "Vestigial Sync Worktrees",
			Status:  StatusOK,
			Message: "N/A (no redirect configured)",
		}
	}

	// Check for local .beads-sync worktree
	cwd, err := os.Getwd()
	if err != nil {
		return DoctorCheck{
			Name:    "Vestigial Sync Worktrees",
			Status:  StatusOK,
			Message: "N/A (cannot determine cwd)",
		}
	}

	// Walk up to find git root
	gitRoot := cwd
	for {
		if _, err := os.Stat(filepath.Join(gitRoot, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(gitRoot)
		if parent == gitRoot {
			// Reached filesystem root, not in a git repo
			return DoctorCheck{
				Name:    "Vestigial Sync Worktrees",
				Status:  StatusOK,
				Message: "N/A (not in git repository)",
			}
		}
		gitRoot = parent
	}

	// Check for .beads-sync worktree
	syncWorktreePath := filepath.Join(gitRoot, ".beads-sync")
	if _, err := os.Stat(syncWorktreePath); os.IsNotExist(err) {
		// No local worktree - good
		return DoctorCheck{
			Name:    "Vestigial Sync Worktrees",
			Status:  StatusOK,
			Message: "No vestigial sync worktrees found",
		}
	}

	// Found a local .beads-sync but we have a redirect - this is vestigial
	return DoctorCheck{
		Name:    "Vestigial Sync Worktrees",
		Status:  StatusWarning,
		Message: "Vestigial .beads-sync worktree found",
		Detail:  fmt.Sprintf("This repo uses redirect but has unused worktree at: %s", syncWorktreePath),
		Fix:     fmt.Sprintf("Remove with: rm -rf %s", syncWorktreePath),
	}
}

// CheckLastTouchedNotTracked verifies that .beads/last-touched is NOT tracked by git.
// The last-touched file is local runtime state that should never be committed.
// If committed, it causes spurious diffs in other clones.
func CheckLastTouchedNotTracked() DoctorCheck {
	lastTouchedPath := filepath.Join(".beads", "last-touched")

	// First check if the file exists
	if _, err := os.Stat(lastTouchedPath); os.IsNotExist(err) {
		// File doesn't exist - nothing to check
		return DoctorCheck{
			Name:    "Last-Touched Tracking",
			Status:  StatusOK,
			Message: "No last-touched file present",
		}
	}

	// Check if git considers this file tracked
	// git ls-files exits 0 and outputs the filename if tracked, empty if untracked
	cmd := exec.Command("git", "ls-files", lastTouchedPath) // #nosec G204 - args are hardcoded paths
	output, err := cmd.Output()
	if err != nil {
		// Not in a git repo or git error - skip check
		return DoctorCheck{
			Name:    "Last-Touched Tracking",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	trackedPath := strings.TrimSpace(string(output))
	if trackedPath == "" {
		// File exists but is not tracked - this is correct
		return DoctorCheck{
			Name:    "Last-Touched Tracking",
			Status:  StatusOK,
			Message: "last-touched file not tracked (correct)",
		}
	}

	// File is tracked - this is a problem
	return DoctorCheck{
		Name:    "Last-Touched Tracking",
		Status:  StatusWarning,
		Message: "last-touched file is tracked by git",
		Detail:  "The .beads/last-touched file is local runtime state that should never be committed.",
		Fix:     "Run 'bd doctor --fix' to untrack, or manually: git rm --cached .beads/last-touched",
	}
}

// FixLastTouchedTracking untracks the .beads/last-touched file from git
func FixLastTouchedTracking() error {
	lastTouchedPath := filepath.Join(".beads", "last-touched")

	// Check if file is actually tracked first
	cmd := exec.Command("git", "ls-files", lastTouchedPath) // #nosec G204 - args are hardcoded paths
	output, err := cmd.Output()
	if err != nil {
		return nil // Not a git repo, nothing to do
	}

	trackedPath := strings.TrimSpace(string(output))
	if trackedPath == "" {
		return nil // Not tracked, nothing to do
	}

	// Untrack the file (keeps the local copy)
	cmd = exec.Command("git", "rm", "--cached", lastTouchedPath) // #nosec G204 - args are hardcoded paths
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to untrack last-touched file: %w", err)
	}

	return nil
}

// CheckProjectGitignore checks if the project-root .gitignore contains patterns
// to prevent accidentally committing Dolt database files (.dolt/ and *.db).
func CheckProjectGitignore() DoctorCheck {
	gitignorePath := ".gitignore"

	content, err := os.ReadFile(gitignorePath) // #nosec G304 -- path is hardcoded
	if err != nil {
		if os.IsNotExist(err) {
			return DoctorCheck{
				Name:    "Project Gitignore",
				Status:  StatusWarning,
				Message: "No project .gitignore found — Dolt files may be committed accidentally",
				Fix:     "Run: bd init (safe to re-run) or bd doctor --fix",
			}
		}
		return DoctorCheck{
			Name:    "Project Gitignore",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Cannot read project .gitignore: %v", err),
		}
	}

	contentStr := string(content)
	var missing []string
	for _, pattern := range ProjectGitignorePatterns {
		if !containsGitignorePattern(contentStr, pattern) {
			missing = append(missing, pattern)
		}
	}

	if len(missing) > 0 {
		return DoctorCheck{
			Name:    "Project Gitignore",
			Status:  StatusWarning,
			Message: "Project .gitignore missing Dolt exclusion patterns",
			Detail:  "Missing: " + strings.Join(missing, ", "),
			Fix:     "Run: bd doctor --fix or bd init (safe to re-run)",
		}
	}

	return DoctorCheck{
		Name:    "Project Gitignore",
		Status:  StatusOK,
		Message: "Dolt files excluded",
	}
}

// EnsureProjectGitignore adds .dolt/ and *.db patterns to the project-root
// .gitignore if they are not already present. Creates the file if it doesn't exist.
// This prevents users from accidentally committing Dolt database files.
func EnsureProjectGitignore() error {
	gitignorePath := ".gitignore"

	var existingContent string
	// #nosec G304 -- path is hardcoded
	if content, err := os.ReadFile(gitignorePath); err == nil {
		existingContent = string(content)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read .gitignore: %w", err)
	}

	var toAdd []string
	for _, pattern := range ProjectGitignorePatterns {
		if !containsGitignorePattern(existingContent, pattern) {
			toAdd = append(toAdd, pattern)
		}
	}

	if len(toAdd) == 0 {
		return nil // All patterns already present
	}

	newContent := existingContent
	if len(newContent) > 0 && !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}

	newContent += "\n" + projectGitignoreComment + "\n"
	for _, pattern := range toAdd {
		newContent += pattern + "\n"
	}

	// #nosec G306 -- gitignore needs to be readable by git and collaborators
	if err := os.WriteFile(gitignorePath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to write .gitignore: %w", err)
	}

	return nil
}

// FixProjectGitignore is an alias for EnsureProjectGitignore, used by bd doctor --fix.
func FixProjectGitignore() error {
	return EnsureProjectGitignore()
}

// containsGitignorePattern checks if a gitignore file content contains the given pattern.
// It checks for the pattern as a standalone line (ignoring leading/trailing whitespace).
func containsGitignorePattern(content, pattern string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == pattern {
			return true
		}
	}
	return false
}
