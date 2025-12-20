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

	beadsDir := filepath.Join(path, ".beads")

	// Find untracked JSONL files
	// Use --untracked-files=all to show individual files, not just the directory
	cmd := exec.Command("git", "status", "--porcelain", "--untracked-files=all", ".beads/")
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
		fullPath := filepath.Join(path, file)
		// Verify file exists in .beads directory (security check)
		if !strings.HasPrefix(fullPath, beadsDir) {
			continue
		}
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}

		// #nosec G204 -- file is validated against a whitelist of JSONL files
		addCmd := exec.Command("git", "add", file)
		addCmd.Dir = path
		if err := addCmd.Run(); err != nil {
			return fmt.Errorf("failed to stage %s: %w", file, err)
		}
		fmt.Printf("  Staged %s\n", filepath.Base(file))
	}

	// Commit only the JSONL files we staged (using --only to preserve other staged changes)
	commitMsg := "chore(beads): commit untracked JSONL files\n\nAuto-committed by bd doctor --fix (bd-pbj)"
	commitArgs := []string{"commit", "--only", "-m", commitMsg}
	commitArgs = append(commitArgs, untrackedFiles...)
	commitCmd := exec.Command("git", commitArgs...) // #nosec G204 -- untrackedFiles validated above
	commitCmd.Dir = path
	commitCmd.Stdout = os.Stdout
	commitCmd.Stderr = os.Stderr

	if err := commitCmd.Run(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	return nil
}
