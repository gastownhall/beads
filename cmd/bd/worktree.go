package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
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
