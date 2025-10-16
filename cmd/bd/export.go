package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

// validateExportPath checks if the output path is safe to write to
func validateExportPath(path string) error {
	// Get absolute path to normalize it
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %v", err)
	}

	// Convert to lowercase for case-insensitive comparison on Windows
	absPathLower := strings.ToLower(absPath)

	// List of sensitive system directories to avoid
	sensitiveDirs := []string{
		"c:\\windows",
		"c:\\program files",
		"c:\\program files (x86)",
		"c:\\programdata",
		"c:\\system volume information",
		"c:\\$recycle.bin",
		"c:\\boot",
		"c:\\recovery",
	}

	for _, dir := range sensitiveDirs {
		if strings.HasPrefix(absPathLower, strings.ToLower(dir)) {
			return fmt.Errorf("cannot write to sensitive system directory: %s", dir)
		}
	}

	return nil
}

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export issues to JSONL format",
	Long: `Export all issues to JSON Lines format (one JSON object per line).
Issues are sorted by ID for consistent diffs.

Output to stdout by default, or use -o flag for file output.`,
	Run: func(cmd *cobra.Command, args []string) {
		format, _ := cmd.Flags().GetString("format")
		output, _ := cmd.Flags().GetString("output")
		statusFilter, _ := cmd.Flags().GetString("status")

		if format != "jsonl" {
			fmt.Fprintf(os.Stderr, "Error: only 'jsonl' format is currently supported\n")
			os.Exit(1)
		}

		// Build filter
		filter := types.IssueFilter{}
		if statusFilter != "" {
			status := types.Status(statusFilter)
			filter.Status = &status
		}

		// Get all issues
		ctx := context.Background()
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Sort by ID for consistent output
		sort.Slice(issues, func(i, j int) bool {
			return issues[i].ID < issues[j].ID
		})

		// Populate dependencies for all issues in one query (avoids N+1 problem)
		allDeps, err := store.GetAllDependencyRecords(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting dependencies: %v\n", err)
			os.Exit(1)
		}
		for _, issue := range issues {
			issue.Dependencies = allDeps[issue.ID]
		}

		// Populate labels for all issues
		for _, issue := range issues {
			labels, err := store.GetLabels(ctx, issue.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting labels for %s: %v\n", issue.ID, err)
				os.Exit(1)
			}
			issue.Labels = labels
		}

		// Open output
		out := os.Stdout
		var tempFile *os.File
		var tempPath string
		var finalPath string
		if output != "" {
			// Validate output path before creating files
			if err := validateExportPath(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// Create temporary file in same directory for atomic rename
			dir := filepath.Dir(output)
			base := filepath.Base(output)
			var err error
			tempFile, err = os.CreateTemp(dir, base+".tmp.*")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error creating temporary file: %v\n", err)
				os.Exit(1)
			}
			tempPath = tempFile.Name()
			finalPath = output

			// Ensure cleanup on failure
			defer func() {
				if tempFile != nil {
					tempFile.Close()
					os.Remove(tempPath) // Clean up temp file if we haven't renamed it
				}
			}()

			out = tempFile
		}

		// Write JSONL
		encoder := json.NewEncoder(out)
		exportedIDs := make([]string, 0, len(issues))
		for _, issue := range issues {
			if err := encoder.Encode(issue); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding issue %s: %v\n", issue.ID, err)
				os.Exit(1)
			}
			exportedIDs = append(exportedIDs, issue.ID)
		}

		// Only clear dirty issues and auto-flush state if exporting to the default JSONL path
		// This prevents clearing dirty flags when exporting to custom paths (e.g., bd export -o backup.jsonl)
		if output == "" || output == findJSONLPath() {
			// Clear only the issues that were actually exported (fixes bd-52 race condition)
			if err := store.ClearDirtyIssuesByID(ctx, exportedIDs); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty issues: %v\n", err)
			}

			// Clear auto-flush state since we just manually exported
			// This cancels any pending auto-flush timer and marks DB as clean
			clearAutoFlushState()
		}

		// If writing to file, atomically replace the target file
		if tempFile != nil {
			// Close the temp file before renaming
			if err := tempFile.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close temporary file: %v\n", err)
			}
			tempFile = nil // Prevent cleanup

			// Atomically replace the target file
			if err := os.Rename(tempPath, finalPath); err != nil {
				os.Remove(tempPath) // Clean up on failure
				fmt.Fprintf(os.Stderr, "Error replacing output file: %v\n", err)
				os.Exit(1)
			}

			// Set appropriate file permissions (0644: rw-r--r--)
			if err := os.Chmod(finalPath, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to set file permissions: %v\n", err)
			}
		}
	},
}

func init() {
	exportCmd.Flags().StringP("format", "f", "jsonl", "Export format (jsonl)")
	exportCmd.Flags().StringP("output", "o", "", "Output file (default: stdout)")
	exportCmd.Flags().StringP("status", "s", "", "Filter by status")
	rootCmd.AddCommand(exportCmd)
}
