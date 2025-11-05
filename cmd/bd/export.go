package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// countIssuesInJSONL counts the number of issues in a JSONL file
func countIssuesInJSONL(path string) (int, error) {
	// #nosec G304 - controlled path from config
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close file: %v\n", err)
		}
	}()

	count := 0
	decoder := json.NewDecoder(file)
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			if err.Error() == "EOF" {
				break
			}
			// Return error for corrupt/invalid JSON
			return count, fmt.Errorf("invalid JSON at issue %d: %w", count+1, err)
		}
		count++
	}
	return count, nil
}

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
		force, _ := cmd.Flags().GetBool("force")

		if format != "jsonl" {
			fmt.Fprintf(os.Stderr, "Error: only 'jsonl' format is currently supported\n")
			os.Exit(1)
		}

		// Export command doesn't work with daemon - need direct access
		// Ensure we have a direct store connection
		if store == nil {
			// Initialize store directly even if daemon is running
			var err error
			if dbPath == "" {
				fmt.Fprintf(os.Stderr, "Error: no database path found\n")
				os.Exit(1)
			}
			store, err = sqlite.New(dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
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

		// Safety check: prevent exporting empty database over non-empty JSONL
		if len(issues) == 0 && output != "" && !force {
			existingCount, err := countIssuesInJSONL(output)
			if err != nil {
				// If we can't read the file, it might not exist yet, which is fine
				if !os.IsNotExist(err) {
					fmt.Fprintf(os.Stderr, "Warning: failed to read existing JSONL: %v\n", err)
				}
			} else if existingCount > 0 {
				fmt.Fprintf(os.Stderr, "Error: refusing to export empty database over non-empty JSONL file\n")
				fmt.Fprintf(os.Stderr, "  Database has 0 issues, JSONL has %d issues\n", existingCount)
				fmt.Fprintf(os.Stderr, "  This would result in data loss!\n")
				fmt.Fprintf(os.Stderr, "Hint: Use --force to override this safety check, or delete the JSONL file first:\n")
				fmt.Fprintf(os.Stderr, "  bd export -o %s --force\n", output)
				fmt.Fprintf(os.Stderr, "  rm %s\n", output)
				os.Exit(1)
			}
		}

		// Warning: check if export would lose >50% of issues
		if output != "" {
			existingCount, err := countIssuesInJSONL(output)
			if err == nil && existingCount > 0 {
				lossPercent := float64(existingCount-len(issues)) / float64(existingCount) * 100
				if lossPercent > 50 {
					fmt.Fprintf(os.Stderr, "WARNING: Export would lose %.1f%% of issues!\n", lossPercent)
					fmt.Fprintf(os.Stderr, "  Existing JSONL: %d issues\n", existingCount)
					fmt.Fprintf(os.Stderr, "  Database: %d issues\n", len(issues))
					fmt.Fprintf(os.Stderr, "  This suggests database staleness or corruption.\n")
					fmt.Fprintf(os.Stderr, "Press Ctrl+C to abort, or Enter to continue: ")
					// Read a line from stdin to wait for user confirmation
					var response string
					_, _ = fmt.Scanln(&response) // ignore EOF on empty input
				}
			}
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
					_ = tempFile.Close()
					_ = os.Remove(tempPath) // Clean up temp file if we haven't renamed it
				}
			}()

			out = tempFile
		}

		// Write JSONL (timestamp-only deduplication DISABLED due to bd-160)
		encoder := json.NewEncoder(out)
		exportedIDs := make([]string, 0, len(issues))
		skippedCount := 0
		for _, issue := range issues {
			// DISABLED: timestamp-only deduplication causes data loss (bd-160)
			// The export_hashes table gets out of sync with JSONL after git operations,
			// causing exports to skip issues that aren't actually in the file.
			//
			// skip, err := shouldSkipExport(ctx, issue)
			// if err != nil {
			// 	fmt.Fprintf(os.Stderr, "Warning: failed to check if %s should skip: %v\n", issue.ID, err)
			// 	skip = false
			// }
			// if skip {
			// 	skippedCount++
			// 	continue
			// }

			if err := encoder.Encode(issue); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding issue %s: %v\n", issue.ID, err)
				os.Exit(1)
			}

			// DISABLED: export hash tracking (bd-160)
			// contentHash, err := computeIssueContentHash(issue)
			// if err != nil {
			// 	fmt.Fprintf(os.Stderr, "Warning: failed to compute hash for %s: %v\n", issue.ID, err)
			// } else if err := store.SetExportHash(ctx, issue.ID, contentHash); err != nil {
			// 	fmt.Fprintf(os.Stderr, "Warning: failed to save export hash for %s: %v\n", issue.ID, err)
			// }

			exportedIDs = append(exportedIDs, issue.ID)
		}

		// Report skipped issues if any (helps debugging bd-159)
		if skippedCount > 0 && (output == "" || output == findJSONLPath()) {
			fmt.Fprintf(os.Stderr, "Skipped %d issue(s) with timestamp-only changes\n", skippedCount)
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

			// Store JSONL file hash for integrity validation (bd-160)
			// nolint:gosec // G304: finalPath is validated JSONL export path
			jsonlData, err := os.ReadFile(finalPath)
			if err == nil {
				hasher := sha256.New()
				hasher.Write(jsonlData)
				fileHash := hex.EncodeToString(hasher.Sum(nil))
				if err := store.SetJSONLFileHash(ctx, fileHash); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_file_hash: %v\n", err)
				}
			}
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
				_ = os.Remove(tempPath) // Clean up on failure
				fmt.Fprintf(os.Stderr, "Error replacing output file: %v\n", err)
				os.Exit(1)
			}

			// Set appropriate file permissions (0600: rw-------)
			if err := os.Chmod(finalPath, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to set file permissions: %v\n", err)
			}

		// Verify JSONL file integrity after export
			 actualCount, err := countIssuesInJSONL(finalPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Export verification failed: %v\n", err)
			os.Exit(1)
		}
		if actualCount != len(exportedIDs) {
			fmt.Fprintf(os.Stderr, "Error: Export verification failed\n")
			fmt.Fprintf(os.Stderr, "  Expected: %d issues\n", len(exportedIDs))
			fmt.Fprintf(os.Stderr, "  JSONL file: %d lines\n", actualCount)
			fmt.Fprintf(os.Stderr, "  Mismatch indicates export failed to write all issues\n")
			os.Exit(1)
		}
	}

	// Output statistics if JSON format requested
		if jsonOutput {
			stats := map[string]interface{}{
				"success":      true,
				"exported":     len(exportedIDs),
				"skipped":      skippedCount,
				"total_issues": len(issues),
			}
			if output != "" {
				stats["output_file"] = output
			}
			data, _ := json.MarshalIndent(stats, "", "  ")
			fmt.Fprintln(os.Stderr, string(data))
		}
	},
}

func init() {
	exportCmd.Flags().StringP("format", "f", "jsonl", "Export format (jsonl)")
	exportCmd.Flags().StringP("output", "o", "", "Output file (default: stdout)")
	exportCmd.Flags().StringP("status", "s", "", "Filter by status")
	exportCmd.Flags().Bool("force", false, "Force export even if database is empty")
	exportCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output export statistics in JSON format")
	rootCmd.AddCommand(exportCmd)
}
