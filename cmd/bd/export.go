package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/util"
	"github.com/steveyegge/beads/internal/validation"
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

// getIssueIDsFromJSONL reads a JSONL file and returns a set of issue IDs
func getIssueIDsFromJSONL(path string) (map[string]bool, error) {
	// #nosec G304 - controlled path from config
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close file: %v\n", err)
		}
	}()

	ids := make(map[string]bool)
	decoder := json.NewDecoder(file)
	lineNum := 0
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			if err.Error() == "EOF" {
				break
			}
			// Return error for corrupt/invalid JSON
			return ids, fmt.Errorf("invalid JSON at line %d: %w", lineNum+1, err)
		}
		ids[issue.ID] = true
		lineNum++
	}
	return ids, nil
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
	Use:     "export",
	GroupID: "sync",
	Short:   "Export issues to JSONL or Obsidian format",
	Long: `Export all issues to JSON Lines or Obsidian Tasks markdown format.
Issues are sorted by ID for consistent diffs.

Output to stdout by default, or use -o flag for file output.
For obsidian format, defaults to ai_docs/changes-log.md

Formats:
  jsonl     - JSON Lines format (one JSON object per line) [default]
  obsidian  - Obsidian Tasks markdown format with checkboxes, priorities, dates

Examples:
  bd export --status open -o open-issues.jsonl
  bd export --format obsidian                    # outputs to ai_docs/changes-log.md
  bd export --format obsidian -o custom.md       # outputs to custom.md
  bd export --type bug --priority-max 1
  bd export --created-after 2025-01-01 --assignee alice`,
	Run: func(cmd *cobra.Command, args []string) {
		format, _ := cmd.Flags().GetString("format")
		output, _ := cmd.Flags().GetString("output")
		statusFilter, _ := cmd.Flags().GetString("status")
		force, _ := cmd.Flags().GetBool("force")
		eventsFlag, _ := cmd.Flags().GetBool("events")
		eventsReset, _ := cmd.Flags().GetBool("events-reset")

		// Additional filter flags
		assignee, _ := cmd.Flags().GetString("assignee")
		issueType, _ := cmd.Flags().GetString("type")
		issueType = util.NormalizeIssueType(issueType) // Expand aliases (mr→merge-request, etc.)
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		priorityMinStr, _ := cmd.Flags().GetString("priority-min")
		priorityMaxStr, _ := cmd.Flags().GetString("priority-max")
		createdAfter, _ := cmd.Flags().GetString("created-after")
		createdBefore, _ := cmd.Flags().GetString("created-before")
		updatedAfter, _ := cmd.Flags().GetString("updated-after")
		updatedBefore, _ := cmd.Flags().GetString("updated-before")
		idFilter, _ := cmd.Flags().GetString("id")
		parentID, _ := cmd.Flags().GetString("parent")

		debug.Logf("Debug: export flags - output=%q, force=%v\n", output, force)

		if format != "jsonl" && format != "obsidian" {
			fmt.Fprintf(os.Stderr, "Error: format must be 'jsonl' or 'obsidian'\n")
			os.Exit(1)
		}

		// Default output path for obsidian format
		if format == "obsidian" && output == "" {
			output = "ai_docs/changes-log.md"
		}

		// Export command requires direct database access for consistent snapshot
		// If daemon is connected, close it and open direct connection
		if daemonClient != nil {
			debug.Logf("Debug: export command forcing direct mode (closes daemon connection)\n")
			_ = daemonClient.Close()
			daemonClient = nil
		}

		// Note: We used to check database file timestamps here, but WAL files
		// get created when opening the DB, making timestamp checks unreliable.
		// Instead, we check issue counts after loading (see below).

		// Ensure we have a direct store connection
		if store == nil {
			var err error
			if dbPath == "" {
				fmt.Fprintf(os.Stderr, "Error: no database path found\n")
				os.Exit(1)
			}
			beadsDir := filepath.Dir(dbPath)
			store, err = factory.NewFromConfigWithOptions(rootCtx, beadsDir, factory.Options{
				LockTimeout: lockTimeout,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
		}

		// Handle --events and --events-reset flags
		if eventsFlag || eventsReset {
			eventsPath := filepath.Join(filepath.Dir(dbPath), "events.jsonl")
			ctx := rootCtx

			if eventsReset {
				if err := resetEventsExport(ctx, store, eventsPath); err != nil {
					fmt.Fprintf(os.Stderr, "Error resetting events export: %v\n", err)
					os.Exit(1)
				}
				fmt.Fprintf(os.Stderr, "Events export state reset\n")
			}

			if eventsFlag {
				if err := exportEventsToJSONL(ctx, store, eventsPath); err != nil {
					fmt.Fprintf(os.Stderr, "Error exporting events: %v\n", err)
					os.Exit(1)
				}
				fmt.Fprintf(os.Stderr, "Events exported to %s\n", eventsPath)
			}

			return
		}

		// Normalize labels: trim, dedupe, remove empty
		labels = util.NormalizeLabels(labels)
		labelsAny = util.NormalizeLabels(labelsAny)

		// Build filter
		// Tombstone export logic:
		// - No status filter → include tombstones for sync propagation
		// - --status=tombstone → include only tombstones (filter handles this)
		// - --status=<other> → exclude tombstones (user wants specific status)
		filter := types.IssueFilter{}
		if statusFilter != "" {
			status := types.Status(statusFilter)
			filter.Status = &status
			// Only include tombstones if explicitly filtering for them
			filter.IncludeTombstones = (status == types.StatusTombstone)
		} else {
			// No status filter: include tombstones for sync propagation
			filter.IncludeTombstones = true
		}
		if assignee != "" {
			filter.Assignee = &assignee
		}
		if issueType != "" {
			t := types.IssueType(issueType)
			filter.IssueType = &t
		}
		if len(labels) > 0 {
			filter.Labels = labels
		}
		if len(labelsAny) > 0 {
			filter.LabelsAny = labelsAny
		}
		if idFilter != "" {
			ids := util.NormalizeLabels(strings.Split(idFilter, ","))
			if len(ids) > 0 {
				filter.IDs = ids
			}
		}
		if parentID != "" {
			filter.ParentID = &parentID
		}

		// Priority exact match (use Changed() to properly handle P0)
		if cmd.Flags().Changed("priority") {
			priorityStr, _ := cmd.Flags().GetString("priority")
			priority, err := validation.ValidatePriority(priorityStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --priority: %v\n", err)
				os.Exit(1)
			}
			filter.Priority = &priority
		}

		// Priority ranges
		if cmd.Flags().Changed("priority-min") {
			priorityMin, err := validation.ValidatePriority(priorityMinStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --priority-min: %v\n", err)
				os.Exit(1)
			}
			filter.PriorityMin = &priorityMin
		}
		if cmd.Flags().Changed("priority-max") {
			priorityMax, err := validation.ValidatePriority(priorityMaxStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --priority-max: %v\n", err)
				os.Exit(1)
			}
			filter.PriorityMax = &priorityMax
		}

		// Date ranges
		if createdAfter != "" {
			t, err := parseTimeFlag(createdAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --created-after: %v\n", err)
				os.Exit(1)
			}
			filter.CreatedAfter = &t
		}
		if createdBefore != "" {
			t, err := parseTimeFlag(createdBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --created-before: %v\n", err)
				os.Exit(1)
			}
			filter.CreatedBefore = &t
		}
		if updatedAfter != "" {
			t, err := parseTimeFlag(updatedAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --updated-after: %v\n", err)
				os.Exit(1)
			}
			filter.UpdatedAfter = &t
		}
		if updatedBefore != "" {
			t, err := parseTimeFlag(updatedBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --updated-before: %v\n", err)
				os.Exit(1)
			}
			filter.UpdatedBefore = &t
		}

		// Get all issues
		ctx := rootCtx
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Detect if any substantive filters are active (partial export)
		// Safety checks should be skipped for filtered exports since the user
		// intentionally wants a subset of issues, not the full database
		isFilteredExport := filter.Status != nil ||
			filter.Assignee != nil ||
			filter.IssueType != nil ||
			len(filter.Labels) > 0 ||
			len(filter.LabelsAny) > 0 ||
			len(filter.IDs) > 0 ||
			filter.ParentID != nil ||
			filter.Priority != nil ||
			filter.PriorityMin != nil ||
			filter.PriorityMax != nil ||
			filter.CreatedAfter != nil ||
			filter.CreatedBefore != nil ||
			filter.UpdatedAfter != nil ||
			filter.UpdatedBefore != nil

		// Safety check: prevent exporting empty database over non-empty JSONL
		// Skip this check for filtered exports - the user intentionally wants a subset
		if len(issues) == 0 && output != "" && !force && !isFilteredExport {
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

		// Safety check: prevent exporting stale database that would lose issues
		// Skip this check for filtered exports - the user intentionally wants a subset
		if output != "" && !force && !isFilteredExport {
			debug.Logf("Debug: checking staleness - output=%s, force=%v\n", output, force)

			// Read existing JSONL to get issue IDs
			jsonlIDs, err := getIssueIDsFromJSONL(output)
			if err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "Warning: failed to read existing JSONL for staleness check: %v\n", err)
			}

			if err == nil && len(jsonlIDs) > 0 {
				// Build set of DB issue IDs
				dbIDs := make(map[string]bool)
				for _, issue := range issues {
					dbIDs[issue.ID] = true
				}

				// Check if JSONL has any issues that DB doesn't have
				var missingIDs []string
				for id := range jsonlIDs {
					if !dbIDs[id] {
						missingIDs = append(missingIDs, id)
					}
				}

				debug.Logf("Debug: JSONL has %d issues, DB has %d issues, missing %d\n",
					len(jsonlIDs), len(issues), len(missingIDs))

				if len(missingIDs) > 0 {
					slices.Sort(missingIDs)
					fmt.Fprintf(os.Stderr, "Error: refusing to export stale database that would lose issues\n")
					fmt.Fprintf(os.Stderr, "  Database has %d issues\n", len(issues))
					fmt.Fprintf(os.Stderr, "  JSONL has %d issues\n", len(jsonlIDs))
					fmt.Fprintf(os.Stderr, "  Export would lose %d issue(s):\n", len(missingIDs))

					// Show first 10 missing issues
					showCount := len(missingIDs)
					if showCount > 10 {
						showCount = 10
					}
					for i := 0; i < showCount; i++ {
						fmt.Fprintf(os.Stderr, "    - %s\n", missingIDs[i])
					}
					if len(missingIDs) > 10 {
						fmt.Fprintf(os.Stderr, "    ... and %d more\n", len(missingIDs)-10)
					}

					fmt.Fprintf(os.Stderr, "\n")
					fmt.Fprintf(os.Stderr, "This usually means:\n")
					fmt.Fprintf(os.Stderr, "  1. You need to run 'bd import -i %s' to sync the latest changes\n", output)
					fmt.Fprintf(os.Stderr, "  2. Or another workspace added issues that weren't synced to this database\n")
					fmt.Fprintf(os.Stderr, "\n")
					fmt.Fprintf(os.Stderr, "To force export anyway (will lose these issues):\n")
					fmt.Fprintf(os.Stderr, "  bd export -o %s --force\n", output)
					os.Exit(1)
				}
			}
		}

		// Filter out wisps - they should never be exported to JSONL
		// Wisps exist only in SQLite and are shared via .beads/redirect, not JSONL.
		filtered := make([]*types.Issue, 0, len(issues))
		for _, issue := range issues {
			if !issue.Ephemeral {
				filtered = append(filtered, issue)
			}
		}
		issues = filtered

		// Sort by ID for consistent output
		slices.SortFunc(issues, func(a, b *types.Issue) int {
			return cmp.Compare(a.ID, b.ID)
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

		// Populate labels and comments for all issues (batch APIs)
		ids := make([]string, 0, len(issues))
		for _, issue := range issues {
			ids = append(ids, issue.ID)
		}

		labelsMap, err := store.GetLabelsForIssues(ctx, ids)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting labels: %v\n", err)
			os.Exit(1)
		}
		commentsMap, err := store.GetCommentsForIssues(ctx, ids)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting comments: %v\n", err)
			os.Exit(1)
		}

		for _, issue := range issues {
			issue.Labels = labelsMap[issue.ID]
			issue.Comments = commentsMap[issue.ID]
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

			// Ensure output directory exists
			if err := os.MkdirAll(dir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
				os.Exit(1)
			}

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

		// Write output based on format
		exportedIDs := make([]string, 0, len(issues))
		skippedCount := 0

		if format == "obsidian" {
			// Write Obsidian Tasks markdown format
			if err := writeObsidianExport(out, issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing Obsidian export: %v\n", err)
				os.Exit(1)
			}
			for _, issue := range issues {
				exportedIDs = append(exportedIDs, issue.ID)
			}
		} else {
			// Write JSONL (timestamp-only deduplication DISABLED due to bd-160)
			encoder := json.NewEncoder(out)
			for _, issue := range issues {
				if err := encoder.Encode(issue); err != nil {
					fmt.Fprintf(os.Stderr, "Error encoding issue %s: %v\n", issue.ID, err)
					os.Exit(1)
				}
				exportedIDs = append(exportedIDs, issue.ID)
			}
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

			// Store JSONL file hash for integrity validation
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
			// Skip chmod for symlinks - os.Chmod follows symlinks and would change the target's
			// permissions, which may be in a read-only location (e.g., /nix/store on NixOS).
			if info, err := os.Lstat(finalPath); err == nil && info.Mode()&os.ModeSymlink == 0 {
				if err := os.Chmod(finalPath, 0600); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to set file permissions: %v\n", err)
				}
			}

			// Verify JSONL file integrity after export (skip for other formats)
			if format == "jsonl" {
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

			// Update database mtime to be >= JSONL mtime (fixes #278, #301, #321)
			// Only do this when exporting to default JSONL path (not arbitrary outputs)
			// This prevents validatePreExport from incorrectly blocking on next export
			if output == "" || output == findJSONLPath() {
				// Dolt backend does not have a SQLite DB file, so only touch mtime for SQLite.
				// Use store.Path() to get the actual database location, not the JSONL directory,
				// since sync-branch exports write JSONL to a worktree but the DB stays in the main repo.
				if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
					dbPath := sqliteStore.Path()
					if err := TouchDatabaseFile(dbPath, finalPath); err != nil {
						// Log warning but don't fail export
						fmt.Fprintf(os.Stderr, "Warning: failed to update database mtime: %v\n", err)
					}
				}
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
	exportCmd.Flags().StringP("format", "f", "jsonl", "Export format: jsonl, obsidian")
	exportCmd.Flags().StringP("output", "o", "", "Output file (default: stdout)")
	exportCmd.Flags().StringP("status", "s", "", "Filter by status")
	exportCmd.Flags().Bool("force", false, "Force export even if database is empty")
	exportCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output export statistics in JSON format")
	exportCmd.Flags().Bool("events", false, "Export events to .beads/events.jsonl (append-only)")
	exportCmd.Flags().Bool("events-reset", false, "Reset events export state and truncate events.jsonl")

	// Filter flags
	registerPriorityFlag(exportCmd, "")
	exportCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	exportCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore, merge-request, molecule, gate). Aliases: mr→merge-request, feat→feature, mol→molecule")
	exportCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL)")
	exportCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE)")

	// Priority filters
	exportCmd.Flags().String("priority-min", "", "Filter by minimum priority (inclusive, 0-4 or P0-P4)")
	exportCmd.Flags().String("priority-max", "", "Filter by maximum priority (inclusive, 0-4 or P0-P4)")

	// Date filters
	exportCmd.Flags().String("created-after", "", "Filter issues created after date (YYYY-MM-DD or RFC3339)")
	exportCmd.Flags().String("created-before", "", "Filter issues created before date (YYYY-MM-DD or RFC3339)")
	exportCmd.Flags().String("updated-after", "", "Filter issues updated after date (YYYY-MM-DD or RFC3339)")
	exportCmd.Flags().String("updated-before", "", "Filter issues updated before date (YYYY-MM-DD or RFC3339)")

	// ID filter
	exportCmd.Flags().String("id", "", "Filter by specific issue IDs (comma-separated, e.g., bd-1,bd-5,bd-10)")

	// Parent filter
	exportCmd.Flags().String("parent", "", "Filter by parent issue ID (shows children of specified issue)")

	rootCmd.AddCommand(exportCmd)
}
