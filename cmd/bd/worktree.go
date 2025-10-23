package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// isGitWorktree detects if the current directory is in a git worktree
// by comparing --git-dir and --git-common-dir (canonical detection method)
func isGitWorktree() bool {
	gitDir := gitRevParse("--git-dir")
	if gitDir == "" {
		return false
	}
	
	commonDir := gitRevParse("--git-common-dir")
	if commonDir == "" {
		return false
	}
	
	absGit, err1 := filepath.Abs(gitDir)
	absCommon, err2 := filepath.Abs(commonDir)
	if err1 != nil || err2 != nil {
		return false
	}
	
	return absGit != absCommon
}

// gitRevParse runs git rev-parse with the given flag and returns the trimmed output
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
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// warnWorktreeDaemon prints a warning if using daemon with worktrees
// Call this only when daemon mode is actually active (connected)
func warnWorktreeDaemon(dbPathForWarning string) {
	if !isGitWorktree() {
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
	fmt.Fprintln(os.Stderr, "║   1. Use --no-daemon flag:    bd --no-daemon <command>                   ║")
	fmt.Fprintln(os.Stderr, "║   2. Disable daemon mode:     export BEADS_NO_DAEMON=1                   ║")
	fmt.Fprintln(os.Stderr, "║                                                                          ║")
	fmt.Fprintln(os.Stderr, "║ Note: BEADS_AUTO_START_DAEMON=false only prevents auto-start;           ║")
	fmt.Fprintln(os.Stderr, "║       you can still connect to a running daemon.                         ║")
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
