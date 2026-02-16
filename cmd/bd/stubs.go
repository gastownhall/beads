package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
)

// findJSONLPath returns the path to the JSONL file for the current beads directory.
// Returns empty string if no beads directory is found.
func findJSONLPath() string {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return ""
	}
	for _, name := range []string{"issues.jsonl", "beads.jsonl"} {
		p := filepath.Join(beadsDir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// readFromGitRef reads a file from a specific git ref (e.g., HEAD, branch name).
// Returns the raw file contents from git.
func readFromGitRef(filePath, gitRef string) ([]byte, error) {
	// Use git show to read the file at the given ref.
	// The ref:path argument is a single token for git-show, not a shell command.
	arg := gitRef + ":" + filePath
	cmd := exec.Command("git", "show", arg) // #nosec G204 -- args are not shell-interpreted
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read %s from git ref %s: %w", filePath, gitRef, err)
	}
	return output, nil
}
