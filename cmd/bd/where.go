package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/utils"
)

// WhereResult contains information about the active beads location
type WhereResult struct {
	Path           string `json:"path"`                      // Active .beads directory path
	RedirectedFrom string `json:"redirected_from,omitempty"` // Original path if redirected
	Prefix         string `json:"prefix,omitempty"`          // Issue prefix (if detectable)
	DatabasePath   string `json:"database_path,omitempty"`   // Full path to database file
}

var whereCmd = &cobra.Command{
	Use:     "where",
	GroupID: "setup",
	Short:   "Show active beads location",
	Long: `Show the active beads database location, including redirect information.

This command is useful for debugging when using redirects, to understand
which .beads directory is actually being used.

Examples:
  bd where           # Show active beads location
  bd where --json    # Output in JSON format
`,
	Run: func(cmd *cobra.Command, args []string) {
		result := WhereResult{}

		// Find the beads directory (this follows redirects)
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			if jsonOutput {
				outputJSON(map[string]string{"error": "no beads directory found"})
			} else {
				fmt.Fprintln(os.Stderr, "Error: no beads directory found")
				fmt.Fprintln(os.Stderr, "Hint: run 'bd init' to create a database in the current directory")
			}
			os.Exit(1)
		}

		result.Path = beadsDir

		// Check if we got here via redirect by looking for the original .beads directory
		// Walk up from cwd to find any .beads with a redirect file
		originalBeadsDir := findOriginalBeadsDir()
		if originalBeadsDir != "" && originalBeadsDir != beadsDir {
			result.RedirectedFrom = originalBeadsDir
		}

		// Find the database path
		dbPath := beads.FindDatabasePath()
		if dbPath != "" {
			result.DatabasePath = dbPath

			// Try to get the prefix from the database if we have a store
			if store != nil {
				ctx := rootCtx
				if prefix, err := store.GetConfig(ctx, "issue_prefix"); err == nil && prefix != "" {
					result.Prefix = prefix
				}
			}
		}

		// If we don't have the prefix from DB, try to detect it from JSONL
		if result.Prefix == "" {
			result.Prefix = detectPrefixFromDir(beadsDir)
		}

		// Output results
		if jsonOutput {
			outputJSON(result)
		} else {
			fmt.Println(result.Path)
			if result.RedirectedFrom != "" {
				fmt.Printf("  (via redirect from %s)\n", result.RedirectedFrom)
			}
			if result.Prefix != "" {
				fmt.Printf("  prefix: %s\n", result.Prefix)
			}
			if result.DatabasePath != "" {
				fmt.Printf("  database: %s\n", result.DatabasePath)
			}
		}
	},
}

// findOriginalBeadsDir walks up from cwd looking for a .beads directory with a redirect file
// Returns the original .beads path if found, empty string otherwise
func findOriginalBeadsDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Canonicalize cwd to handle symlinks
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	// Check BEADS_DIR first
	if envDir := os.Getenv("BEADS_DIR"); envDir != "" {
		envDir = utils.CanonicalizePath(envDir)
		redirectFile := filepath.Join(envDir, beads.RedirectFileName)
		if _, err := os.Stat(redirectFile); err == nil {
			return envDir
		}
		return ""
	}

	// Walk up directory tree looking for .beads with redirect
	for dir := cwd; dir != "/" && dir != "."; {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			redirectFile := filepath.Join(beadsDir, beads.RedirectFileName)
			if _, err := os.Stat(redirectFile); err == nil {
				return beadsDir
			}
			// Found .beads without redirect - this is the actual location
			return ""
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root (works on both Unix and Windows)
			// On Unix: filepath.Dir("/") returns "/"
			// On Windows: filepath.Dir("C:\\") returns "C:\\"
			break
		}
		dir = parent
	}

	return ""
}

// detectPrefixFromDir tries to detect the issue prefix from files in the beads directory
func detectPrefixFromDir(beadsDir string) string {
	// Try to read from issues.jsonl and extract prefix from first issue ID
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	// #nosec G304 -- jsonlPath is constructed from trusted beadsDir
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		return ""
	}

	// Find first line that looks like an issue
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Quick JSON parse to get ID
		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			continue
		}

		// Extract prefix from ID (e.g., "bd-123" -> "bd")
		if idx := strings.LastIndex(issue.ID, "-"); idx > 0 {
			return issue.ID[:idx]
		}
	}

	return ""
}

func init() {
	rootCmd.AddCommand(whereCmd)
}
