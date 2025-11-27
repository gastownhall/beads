package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Clean up temporary git merge artifacts from .beads directory",
	Long: `Delete temporary git merge artifacts from the .beads directory.

This command removes temporary files created during git merges and conflicts.
It does NOT delete issues from the database - use 'bd cleanup' for that.

Files removed:
- 3-way merge snapshots (beads.base.jsonl, beads.left.jsonl, beads.right.jsonl)
- Merge metadata (*.meta.json)
- Git merge driver temp files (*.json[0-9], *.jsonl[0-9])

Files preserved:
- issues.jsonl (source of truth)
- beads.db (SQLite database)
- metadata.json
- config.yaml
- All daemon files

EXAMPLES:
Clean up temporary files:
  bd clean

Preview what would be deleted:
  bd clean --dry-run

SEE ALSO:
  bd cleanup    Delete closed issues from database`,
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Find beads directory
		beadsDir := findBeadsDir()
		if beadsDir == "" {
			fmt.Fprintf(os.Stderr, "Error: .beads directory not found\n")
			os.Exit(1)
		}

		// Read patterns from .beads/.gitignore (only merge artifacts section)
		cleanPatterns, err := readMergeArtifactPatterns(beadsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading .gitignore: %v\n", err)
			os.Exit(1)
		}

		// Collect files to delete
		var filesToDelete []string
		for _, pattern := range cleanPatterns {
			matches, err := filepath.Glob(filepath.Join(beadsDir, pattern))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: error matching pattern %s: %v\n", pattern, err)
				continue
			}
			filesToDelete = append(filesToDelete, matches...)
		}

		if len(filesToDelete) == 0 {
			fmt.Println("Nothing to clean - all artifacts already removed")
			return
		}

		// Just run by default, no --force needed

		if dryRun {
			fmt.Println(color.YellowString("DRY RUN - no changes will be made"))
		}
		fmt.Printf("Found %d file(s) to clean:\n", len(filesToDelete))
		for _, file := range filesToDelete {
			relPath, err := filepath.Rel(beadsDir, file)
			if err != nil {
				relPath = file
			}
			fmt.Printf("  %s\n", relPath)
		}

		if dryRun {
			return
		}

		// Actually delete the files
		deletedCount := 0
		errorCount := 0
		for _, file := range filesToDelete {
			if err := os.Remove(file); err != nil {
				if !os.IsNotExist(err) {
					relPath, _ := filepath.Rel(beadsDir, file)
					fmt.Fprintf(os.Stderr, "Warning: failed to delete %s: %v\n", relPath, err)
					errorCount++
				}
			} else {
				deletedCount++
			}
		}

		fmt.Printf("\nDeleted %d file(s)", deletedCount)
		if errorCount > 0 {
			fmt.Printf(" (%d error(s))", errorCount)
		}
		fmt.Println()
	},
}

// readMergeArtifactPatterns reads the .beads/.gitignore file and extracts
// patterns from the "Merge artifacts" section
func readMergeArtifactPatterns(beadsDir string) ([]string, error) {
	gitignorePath := filepath.Join(beadsDir, ".gitignore")
	// #nosec G304 -- gitignorePath is safely constructed via filepath.Join from beadsDir
	// (which comes from findBeadsDir searching upward for .beads). This can only open
	// .gitignore within the project's .beads directory. See TestReadMergeArtifactPatterns_PathTraversal
	file, err := os.Open(gitignorePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open .gitignore: %w", err)
	}
	defer file.Close()

	var patterns []string
	inMergeSection := false
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Look for the merge artifacts section
		if strings.Contains(line, "Merge artifacts") {
			inMergeSection = true
			continue
		}

		// Stop at the next section (starts with #)
		if inMergeSection && strings.HasPrefix(line, "#") {
			break
		}

		// Collect patterns from merge section
		if inMergeSection && line != "" && !strings.HasPrefix(line, "#") {
			// Skip negation patterns (starting with !)
			if !strings.HasPrefix(line, "!") {
				patterns = append(patterns, line)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading .gitignore: %w", err)
	}

	return patterns, nil
}

func init() {
	cleanCmd.Flags().Bool("dry-run", false, "Preview what would be deleted without making changes")
	rootCmd.AddCommand(cleanCmd)
}
