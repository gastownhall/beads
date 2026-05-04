package doctor

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// trackedRuntimePatterns are file patterns under .beads/ that should never be
// tracked by git. These are runtime artifacts, lock files, corrupt backups,
// and sensitive files that may have been committed before .beads/.gitignore
// covered them.
//
// Each entry is matched against the relative path within .beads/ using
// filepath.Match or prefix matching for directory patterns (trailing /).
//
// Note: interactions.jsonl is intentionally omitted — it may be versioned
// per bd audit policy (see `bd audit` help text).
var trackedRuntimePatterns = []string{
	// Lock files
	"*.lock",
	"*.pid.lock",

	// Daemon / server runtime
	"daemon.pid",
	"daemon.log",
	"daemon.lock",
	"dolt-server.pid",
	"dolt-server.log",
	"dolt-server.lock",
	"dolt-server.port",

	// Socket files
	"bd.sock",
	"bd.sock.startlock",
	".exclusive-lock",

	// Runtime state
	"push-state.json",
	"export-state.json",
	"sync-state.json",
	"last-touched",
	".local_version",
	"redirect",

	// Sync / export state
	".sync.lock",

	// Ephemeral SQLite
	"ephemeral.sqlite3",
	"ephemeral.sqlite3-journal",
	"ephemeral.sqlite3-wal",
	"ephemeral.sqlite3-shm",
}

// trackedRuntimeDirPrefixes are directory prefixes under .beads/ that should
// never be tracked. Any file whose relative path starts with one of these
// prefixes is flagged.
var trackedRuntimeDirPrefixes = []string{
	"dolt/",
	"backup/",
	"export-state/",
}

// sensitiveFileNames are filenames that indicate a security concern if
// committed anywhere under .beads/.
var sensitiveFileNames = []string{
	".beads-credential-key",
	"credential-key",
}

// corruptBackupPattern matches corrupt backup directories created by
// bd doctor --fix recovery (e.g. dolt.20260312T123507Z.corrupt.backup/).
const corruptBackupDirFragment = ".corrupt.backup/"

// CheckTrackedRuntimeFiles detects files tracked by git under .beads/ that
// should be gitignored. These are runtime artifacts, lock files, corrupt
// backups, and sensitive files that may have been committed before the
// current .beads/.gitignore patterns existed.
// repoPath is the project root directory.
func CheckTrackedRuntimeFiles(repoPath string) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(repoPath)
	repoRoot := resolvedBeadsRepoRoot(repoPath)

	// Get all files tracked by git under .beads/
	cmd := exec.Command("git", "ls-files", ".beads") // #nosec G204 - args are constructed from known parts
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:     "Tracked Runtime Files",
			Status:   StatusOK,
			Message:  "N/A (not a git repository)",
			Category: CategoryGit,
		}
	}

	trackedFiles := strings.TrimSpace(string(output))
	if trackedFiles == "" {
		return DoctorCheck{
			Name:     "Tracked Runtime Files",
			Status:   StatusOK,
			Message:  "No .beads/ files tracked by git",
			Category: CategoryGit,
		}
	}

	var flagged []string
	var hasSensitive bool

	for _, line := range strings.Split(trackedFiles, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Get the path relative to .beads/
		rel, err := filepath.Rel(beadsDir, filepath.Join(repoRoot, line))
		if err != nil {
			continue
		}

		if shouldFlagTrackedFile(rel) {
			flagged = append(flagged, line)

			// Check for sensitive files
			base := filepath.Base(rel)
			for _, sensitive := range sensitiveFileNames {
				if base == sensitive {
					hasSensitive = true
				}
			}
		}
	}

	if len(flagged) == 0 {
		return DoctorCheck{
			Name:     "Tracked Runtime Files",
			Status:   StatusOK,
			Message:  "No runtime/sensitive files tracked",
			Category: CategoryGit,
		}
	}

	status := StatusWarning
	message := fmt.Sprintf("%d runtime/sensitive file(s) tracked by git", len(flagged))
	if hasSensitive {
		status = StatusError
		message = fmt.Sprintf("%d tracked file(s) include sensitive data (credential key)", len(flagged))
	}

	detail := strings.Join(flagged, ", ")
	if len(detail) > 200 {
		detail = fmt.Sprintf("%s... (%d total)", strings.Join(flagged[:3], ", "), len(flagged))
	}

	return DoctorCheck{
		Name:     "Tracked Runtime Files",
		Status:   status,
		Message:  message,
		Detail:   detail,
		Fix:      "Run 'bd doctor --fix' to untrack, or manually: git rm --cached <files>",
		Category: CategoryGit,
	}
}

// shouldFlagTrackedFile checks if a path relative to .beads/ is a runtime
// or sensitive file that should not be tracked by git.
func shouldFlagTrackedFile(rel string) bool {
	base := filepath.Base(rel)

	// Check sensitive filenames anywhere in the tree
	for _, sensitive := range sensitiveFileNames {
		if base == sensitive {
			return true
		}
	}

	// Check corrupt backup directories
	if strings.Contains(rel, corruptBackupDirFragment) {
		return true
	}

	// Check directory prefixes
	for _, prefix := range trackedRuntimeDirPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}

	// Only match patterns against top-level .beads/ files (not files in subdirs)
	if strings.Contains(rel, "/") {
		return false
	}

	// Check filename patterns
	for _, pattern := range trackedRuntimePatterns {
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
	}

	return false
}

// FixTrackedRuntimeFiles untracks runtime/sensitive files from git.
// repoPath is the project root directory.
func FixTrackedRuntimeFiles(repoPath string) error {
	beadsDir := ResolveBeadsDirForRepo(repoPath)
	repoRoot := resolvedBeadsRepoRoot(repoPath)

	// Get all files tracked by git under .beads/
	cmd := exec.Command("git", "ls-files", ".beads") // #nosec G204 - args are constructed from known parts
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return nil // Not a git repo, nothing to do
	}

	trackedFiles := strings.TrimSpace(string(output))
	if trackedFiles == "" {
		return nil
	}

	var toUntrack []string
	for _, line := range strings.Split(trackedFiles, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		rel, err := filepath.Rel(beadsDir, filepath.Join(repoRoot, line))
		if err != nil {
			continue
		}

		if shouldFlagTrackedFile(rel) {
			toUntrack = append(toUntrack, line)
		}
	}

	if len(toUntrack) == 0 {
		return nil
	}

	// Untrack files (keeps local copies)
	args := append([]string{"rm", "--cached", "--"}, toUntrack...)
	cmd = exec.Command("git", args...) // #nosec G204 - args are constructed from known parts
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to untrack files: %w\n%s", err, string(out))
	}

	return nil
}
