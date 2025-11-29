package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// UntrackedJSONL stages and commits untracked .beads/*.jsonl files.
// This fixes the issue where bd cleanup -f creates deletions.jsonl but
// leaves it untracked. (bd-pbj)
func UntrackedJSONL(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid workspace path: %w", err)
	}
	path = absPath

	beadsDir, err := safeWorkspacePath(path, ".beads")
	if err != nil {
		return err
	}

	// Find untracked JSONL files
	cmd := exec.Command("git", "status", "--porcelain", ".beads/")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	// Parse output for untracked JSONL files
	var untrackedFiles []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Untracked files start with "?? "
		if strings.HasPrefix(line, "?? ") {
			file := strings.TrimPrefix(line, "?? ")
			if strings.HasSuffix(file, ".jsonl") {
				untrackedFiles = append(untrackedFiles, file)
			}
		}
	}

	if len(untrackedFiles) == 0 {
		fmt.Println("  No untracked JSONL files found")
		return nil
	}

	// Stage the untracked files
	for _, file := range untrackedFiles {
		cleanFile := filepath.Clean(file)
		if filepath.IsAbs(cleanFile) || cleanFile == ".." || strings.HasPrefix(cleanFile, ".."+string(os.PathSeparator)) {
			continue
		}

		// Only allow files inside .beads/
		slashFile := filepath.ToSlash(cleanFile)
		if !strings.HasPrefix(slashFile, ".beads/") {
			continue
		}

		fullPath, err := safeWorkspacePath(path, cleanFile)
		if err != nil || !isWithinWorkspace(beadsDir, fullPath) {
			continue
		}
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}

		addCmd := exec.Command("git", "add", cleanFile) // #nosec G204 -- cleanFile constrained to .beads/*.jsonl within the validated workspace
		addCmd.Dir = path
		if err := addCmd.Run(); err != nil {
			return fmt.Errorf("failed to stage %s: %w", cleanFile, err)
		}
		fmt.Printf("  Staged %s\n", filepath.Base(cleanFile))
	}

	// Commit the staged files
	commitMsg := "chore(beads): commit untracked JSONL files\n\nAuto-committed by bd doctor --fix (bd-pbj)"
	commitCmd := exec.Command("git", "commit", "-m", commitMsg)
	commitCmd.Dir = path
	commitCmd.Stdout = os.Stdout
	commitCmd.Stderr = os.Stderr

	if err := commitCmd.Run(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	return nil
}
