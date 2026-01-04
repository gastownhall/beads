package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// GitignoreTemplate is the canonical .beads/.gitignore content
const GitignoreTemplate = `# SQLite databases
*.db
*.db?*
*.db-journal
*.db-wal
*.db-shm

# Daemon runtime files
daemon.lock
daemon.log
daemon.pid
bd.sock
sync-state.json
last-touched

# Local version tracking (prevents upgrade notification spam after git ops)
.local_version

# Legacy database files
db.sqlite
bd.db

# Worktree redirect file (contains relative path to main repo's .beads/)
# Must not be committed as paths would be wrong in other clones
redirect

# Merge artifacts (temporary files from 3-way merge)
beads.base.jsonl
beads.base.meta.json
beads.left.jsonl
beads.left.meta.json
beads.right.jsonl
beads.right.meta.json

# NOTE: Do NOT add negation patterns (e.g., !issues.jsonl) here.
# They would override fork protection in .git/info/exclude, allowing
# contributors to accidentally commit upstream issue databases.
# The JSONL files (issues.jsonl, interactions.jsonl) and config files
# are tracked by git by default since no pattern above ignores them.
`

// requiredPatterns are patterns that MUST be in .beads/.gitignore
var requiredPatterns = []string{
	"beads.base.jsonl",
	"beads.left.jsonl",
	"beads.right.jsonl",
	"beads.base.meta.json",
	"beads.left.meta.json",
	"beads.right.meta.json",
	"*.db?*",
	"redirect",
	"last-touched",
}

// CheckGitignore checks if .beads/.gitignore is up to date
func CheckGitignore() DoctorCheck {
	gitignorePath := filepath.Join(".beads", ".gitignore")
	
	// Check if file exists
	content, err := os.ReadFile(gitignorePath) // #nosec G304 -- path is hardcoded
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
			Message: "Outdated .beads/.gitignore (missing merge artifact patterns)",
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

// CheckIssuesTracking verifies that issues.jsonl is tracked by git.
// This catches cases where global gitignore patterns (e.g., *.jsonl) would
// cause issues.jsonl to be ignored, breaking bd sync.
// In sync-branch mode, the file may be intentionally ignored in working branches (GH#858).
func CheckIssuesTracking() DoctorCheck {
	issuesPath := filepath.Join(".beads", "issues.jsonl")

	// First check if the file exists
	if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
		// File doesn't exist yet - not an error, bd init may not have been run
		return DoctorCheck{
			Name:   "Issues Tracking",
			Status: "ok",
			Message: "No issues.jsonl yet (will be created on first issue)",
		}
	}

	// In sync-branch mode, JSONL files may be intentionally ignored in working branches.
	// They are tracked only in the dedicated sync branch.
	if branch := syncbranch.GetFromYAML(); branch != "" {
		return DoctorCheck{
			Name:    "Issues Tracking",
			Status:  StatusOK,
			Message: "N/A (sync-branch mode)",
			Detail:  fmt.Sprintf("JSONL files tracked in '%s' branch only", branch),
		}
	}

	// Check if git considers this file ignored
	// git check-ignore exits 0 if ignored, 1 if not ignored, 128 if error
	cmd := exec.Command("git", "check-ignore", "-q", issuesPath) // #nosec G204 - args are hardcoded paths
	err := cmd.Run()

	if err == nil {
		// Exit code 0 means the file IS ignored - this is bad
		// Get details about what's ignoring it
		detailCmd := exec.Command("git", "check-ignore", "-v", issuesPath) // #nosec G204 - args are hardcoded paths
		output, _ := detailCmd.Output()
		detail := strings.TrimSpace(string(output))

		return DoctorCheck{
			Name:    "Issues Tracking",
			Status:  "warning",
			Message: "issues.jsonl is ignored by git (bd sync will fail)",
			Detail:  detail,
			Fix:     "Check global gitignore: git config --global core.excludesfile",
		}
	}

	// Exit code 1 means not ignored (good), any other error we ignore
	return DoctorCheck{
		Name:    "Issues Tracking",
		Status:  "ok",
		Message: "issues.jsonl is tracked by git",
	}
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

// CheckSyncBranchGitignore checks if git index flags are set on issues.jsonl when sync.branch is configured.
// Without these flags, the file appears modified in git status even though changes go to the sync branch.
// GH#797, GH#801, GH#870.
func CheckSyncBranchGitignore() DoctorCheck {
	// Only relevant when sync.branch is configured
	branch := syncbranch.GetFromYAML()
	if branch == "" {
		return DoctorCheck{
			Name:    "Sync Branch Gitignore",
			Status:  StatusOK,
			Message: "N/A (sync.branch not configured)",
		}
	}

	issuesPath := filepath.Join(".beads", "issues.jsonl")

	// Check if file exists
	if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Sync Branch Gitignore",
			Status:  StatusOK,
			Message: "No issues.jsonl yet",
		}
	}

	// Check if file is tracked by git
	cmd := exec.Command("git", "ls-files", "--error-unmatch", issuesPath) // #nosec G204 - args are hardcoded paths
	if err := cmd.Run(); err != nil {
		// File is not tracked - check if it's excluded
		return DoctorCheck{
			Name:    "Sync Branch Gitignore",
			Status:  StatusOK,
			Message: "issues.jsonl is not tracked (via .gitignore or exclude)",
		}
	}

	// File is tracked - check for git index flags
	cwd, err := os.Getwd()
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Gitignore",
			Status:  StatusWarning,
			Message: "Cannot determine current directory",
		}
	}

	hasAssumeUnchanged, hasSkipWorktree, err := fix.HasSyncBranchGitignoreFlags(cwd)
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Gitignore",
			Status:  StatusWarning,
			Message: "Cannot check git index flags",
			Detail:  err.Error(),
		}
	}

	if hasAssumeUnchanged || hasSkipWorktree {
		return DoctorCheck{
			Name:    "Sync Branch Gitignore",
			Status:  StatusOK,
			Message: "Git index flags set (issues.jsonl hidden from git status)",
		}
	}

	// No flags set - this is the problem case
	return DoctorCheck{
		Name:    "Sync Branch Gitignore",
		Status:  StatusWarning,
		Message: "issues.jsonl shows as modified (missing git index flags)",
		Detail:  fmt.Sprintf("sync.branch='%s' configured but issues.jsonl appears in git status", branch),
		Fix:     "Run 'bd doctor --fix' or 'bd sync' to set git index flags",
	}
}

// FixSyncBranchGitignore sets git index flags on issues.jsonl when sync.branch is configured.
func FixSyncBranchGitignore() error {
	// Only relevant when sync.branch is configured
	branch := syncbranch.GetFromYAML()
	if branch == "" {
		return nil // Not in sync-branch mode, nothing to do
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine current directory: %w", err)
	}

	return fix.SyncBranchGitignore(cwd)
}
