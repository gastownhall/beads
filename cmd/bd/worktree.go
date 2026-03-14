package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/gastownhall/beads/internal/beads"
	"github.com/gastownhall/beads/internal/git"
)

// isGitWorktree detects if the current directory is in a git worktree.
// This is a wrapper around git.IsWorktree() for CLI-layer compatibility.
func isGitWorktree() bool {
	return git.IsWorktree()
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
