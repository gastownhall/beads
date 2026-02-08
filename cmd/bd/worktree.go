package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// isGitWorktree detects if the current directory is in a git worktree.
// This is a wrapper around git.IsWorktree() for CLI-layer compatibility.
func isGitWorktree() bool {
	return git.IsWorktree()
}

// shouldDisableDaemonForWorktree returns true if daemon should be disabled
// due to being in a git worktree without sync-branch configured.
//
// The daemon is unsafe in worktrees because all worktrees share the same
// .beads directory, and the daemon commits to whatever branch its working
// directory has checked out - which can cause commits to go to the wrong branch.
//
// However, when sync-branch is configured, the daemon commits to a dedicated
// branch (e.g., "beads-metadata") using an internal worktree, so the user's
// current branch is never affected. This makes daemon mode safe in worktrees.
//
// Returns:
//   - true: Disable daemon (in worktree without sync-branch)
//   - false: Allow daemon (not in worktree, or sync-branch is configured)
func shouldDisableDaemonForWorktree() bool {
	// If not in a worktree, daemon is safe
	if !isGitWorktree() {
		return false
	}

	// In a worktree - check if sync-branch is configured
	// IsConfiguredWithDB checks env var, config.yaml, AND database config
	if syncbranch.IsConfiguredWithDB("") {
		// Sync-branch is configured, daemon is safe (commits go to dedicated branch)
		return false
	}

	// In worktree without sync-branch - daemon is unsafe, disable it
	return true
}

// gitRevParse runs git rev-parse with the given flag and returns the trimmed output.
// This is a helper for CLI utilities that need git command execution.
func gitRevParse(flag string) string {
	out, err := exec.Command("git", "rev-parse", flag).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// getWorktreeGitDir returns the .git directory path for a worktree
// Returns empty string if not in a git repo or not a worktree
func getWorktreeGitDir() string {
	gitDir, err := git.GetGitDir()
	if err != nil {
		return ""
	}
	return gitDir
}

// warnWorktreeDaemon prints a warning if using daemon with worktrees without sync-branch.
// Call this only when daemon mode is actually active (connected).
//
// With the new worktree safety logic, this warning should rarely appear because:
// - Daemon is auto-disabled in worktrees without sync-branch
// - When sync-branch is configured, daemon is safe (commits go to dedicated branch)
//
// This warning is kept as a safety net for edge cases where daemon might still
// be connected in a worktree (e.g., daemon started in main repo, then user cd's to worktree).
func warnWorktreeDaemon(dbPathForWarning string) {
	if !isGitWorktree() {
		return
	}

	// If sync-branch is configured, daemon is safe in worktrees - no warning needed
	if syncbranch.IsConfiguredWithDB("") {
		return
	}

	gitDir := getWorktreeGitDir()
	beadsDir := filepath.Dir(dbPathForWarning)
	if beadsDir == "." || beadsDir == "" {
		beadsDir = dbPathForWarning
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(os.Stderr, "║ WARNING: Git worktree detected with daemon mode                         ║")
	fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════════════════╣")
	fmt.Fprintln(os.Stderr, "║ Git worktrees share the same .beads directory, which can cause the      ║")
	fmt.Fprintln(os.Stderr, "║ daemon to commit/push to the wrong branch.                               ║")
	fmt.Fprintln(os.Stderr, "║                                                                          ║")
	fmt.Fprintf(os.Stderr, "║ Shared database: %-55s ║\n", truncateForBox(beadsDir, 55))
	fmt.Fprintf(os.Stderr, "║ Worktree git dir: %-54s ║\n", truncateForBox(gitDir, 54))
	fmt.Fprintln(os.Stderr, "║                                                                          ║")
	fmt.Fprintln(os.Stderr, "║ RECOMMENDED SOLUTIONS:                                                   ║")
	fmt.Fprintln(os.Stderr, "║   1. Configure sync-branch:   bd config set sync-branch beads-metadata  ║")
	fmt.Fprintln(os.Stderr, "║   2. Use --no-daemon flag:    bd --no-daemon <command>                   ║")
	fmt.Fprintln(os.Stderr, "║   3. Disable daemon mode:     export BEADS_NO_DAEMON=1                   ║")
	fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr)
}

// truncateForBox truncates a path to fit in the warning box
func truncateForBox(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	// Truncate with ellipsis
	return "..." + path[len(path)-(maxLen-3):]
}

// warnMultipleDatabases prints a warning if multiple .beads databases exist
// in the directory hierarchy, to prevent confusion and database pollution
func warnMultipleDatabases(currentDB string) {
	databases := beads.FindAllDatabases()
	if len(databases) <= 1 {
		return // Only one database found, no warning needed
	}

	// Find which database is active
	activeIdx := -1
	for i, db := range databases {
		if db.Path == currentDB {
			activeIdx = i
			break
		}
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════════════════╗")
	fmt.Fprintf(os.Stderr, "║ WARNING: %d beads databases detected in directory hierarchy             ║\n", len(databases))
	fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════════════════╣")
	fmt.Fprintln(os.Stderr, "║ Multiple databases can cause confusion and database pollution.          ║")
	fmt.Fprintln(os.Stderr, "║                                                                          ║")

	for i, db := range databases {
		isActive := (i == activeIdx)
		issueInfo := ""
		if db.IssueCount >= 0 {
			issueInfo = fmt.Sprintf(" (%d issues)", db.IssueCount)
		}

		marker := " "
		if isActive {
			marker = "▶"
		}

		line := fmt.Sprintf("%s %s%s", marker, db.BeadsDir, issueInfo)
		fmt.Fprintf(os.Stderr, "║ %-72s ║\n", truncateForBox(line, 72))
	}

	fmt.Fprintln(os.Stderr, "║                                                                          ║")
	if activeIdx == 0 {
		fmt.Fprintln(os.Stderr, "║ Currently using the closest database (▶). This is usually correct.      ║")
	} else {
		fmt.Fprintln(os.Stderr, "║ WARNING: Not using the closest database! Check your BEADS_DB setting.   ║")
	}
	fmt.Fprintln(os.Stderr, "║                                                                          ║")
	fmt.Fprintln(os.Stderr, "║ RECOMMENDED: Consolidate or remove unused databases to avoid confusion. ║")
	fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr)
}
