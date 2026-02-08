package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// legacyDeletionRecordCmd represents a single deletion entry from the legacy deletions.jsonl manifest.
// This is inlined here for migration purposes only - new code uses inline tombstones.
type legacyDeletionRecordCmd struct {
	ID        string    `json:"id"`               // Issue ID that was deleted
	Timestamp time.Time `json:"ts"`               // When the deletion occurred
	Actor     string    `json:"by"`               // Who performed the deletion
	Reason    string    `json:"reason,omitempty"` // Optional reason for deletion
}

// loadLegacyDeletionsCmd reads the legacy deletions.jsonl manifest.
// Returns a map of deletion records keyed by issue ID and any warnings.
// This is inlined here for migration purposes only.
func loadLegacyDeletionsCmd(path string) (map[string]legacyDeletionRecordCmd, []string, error) {
	records := make(map[string]legacyDeletionRecordCmd)
	var warnings []string

	f, err := os.Open(path) // #nosec G304 - controlled path from caller
	if err != nil {
		if os.IsNotExist(err) {
			return records, nil, nil
		}
		return nil, nil, fmt.Errorf("failed to open deletions file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var record legacyDeletionRecordCmd
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			warnings = append(warnings, fmt.Sprintf("line %d: invalid JSON", lineNum))
			continue
		}
		if record.ID == "" {
			warnings = append(warnings, fmt.Sprintf("line %d: missing ID", lineNum))
			continue
		}
		records[record.ID] = record
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("error reading deletions file: %w", err)
	}

	return records, warnings, nil
}

var migrateTombstonesCmd = &cobra.Command{
	Use:   "tombstones",
	Short: "Convert deletions.jsonl entries to inline tombstones",
	Long: `Migrate legacy deletions.jsonl entries to inline tombstones in issues.jsonl.

This command converts existing deletion records from the legacy deletions.jsonl
manifest to inline tombstone entries in issues.jsonl. This is part of the
transition from separate deletion tracking to unified tombstone-based deletion.

The migration:
1. Reads existing deletions from deletions.jsonl
2. Checks issues.jsonl for already-existing tombstones
3. Creates tombstone entries for unmigrated deletions
4. Appends new tombstones to issues.jsonl
5. Archives deletions.jsonl with .migrated suffix

Use --dry-run to preview changes without modifying files.

Examples:
  bd migrate-tombstones           # Migrate deletions to tombstones
  bd migrate-tombstones --dry-run # Preview what would be migrated
  bd migrate-tombstones --verbose # Show detailed progress`,
	Run: func(cmd *cobra.Command, _ []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		verbose, _ := cmd.Flags().GetBool("verbose")

		// Block writes in readonly mode
		if !dryRun {
			CheckReadonly("migrate-tombstones")
		}

		// Find .beads directory
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "no_beads_directory",
					"message": "No .beads directory found. Run 'bd init' first.",
				})
			} else {
				fmt.Fprintf(os.Stderr, "Error: no .beads directory found\n")
				fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to initialize bd\n")
			}
			os.Exit(1)
		}

		// Check paths
		deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")
		issuesPath := filepath.Join(beadsDir, "issues.jsonl")

		// Load existing deletions
		records, warnings, err := loadLegacyDeletionsCmd(deletionsPath)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "load_deletions_failed",
					"message": err.Error(),
				})
			} else {
				fmt.Fprintf(os.Stderr, "Error loading deletions.jsonl: %v\n", err)
			}
			os.Exit(1)
		}

		if len(records) == 0 {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"status":   "noop",
					"message":  "No deletions to migrate",
					"migrated": 0,
					"skipped":  0,
				})
			} else {
				fmt.Println("No deletions.jsonl entries to migrate")
			}
			return
		}

		// Print warnings from loading
		for _, warning := range warnings {
			if !jsonOutput {
				fmt.Printf("%s\n", ui.RenderWarn(fmt.Sprintf("Warning: %s", warning)))
			}
		}

		// Load existing issues.jsonl to find existing tombstones
		existingTombstones := make(map[string]bool)
		if _, err := os.Stat(issuesPath); err == nil {
			// nolint:gosec // G304: issuesPath is controlled from beadsDir
			file, err := os.Open(issuesPath)
			if err != nil {
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"error":   "load_issues_failed",
						"message": err.Error(),
					})
				} else {
					fmt.Fprintf(os.Stderr, "Error opening issues.jsonl: %v\n", err)
				}
				os.Exit(1)
			}

			decoder := json.NewDecoder(file)
			for {
				var issue types.Issue
				if err := decoder.Decode(&issue); err != nil {
					if err.Error() == "EOF" {
						break
					}
					// Skip corrupt lines, continue reading
					continue
				}
				if issue.IsTombstone() {
					existingTombstones[issue.ID] = true
				}
			}
			_ = file.Close()
		}

		// Determine which deletions need migration
		var toMigrate []legacyDeletionRecordCmd
		var skippedIDs []string
		for id, record := range records {
			if existingTombstones[id] {
				skippedIDs = append(skippedIDs, id)
				if verbose && !jsonOutput {
					fmt.Printf("  Skipping %s (tombstone already exists)\n", id)
				}
			} else {
				toMigrate = append(toMigrate, record)
			}
		}

		if len(toMigrate) == 0 {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"status":   "noop",
					"message":  "All deletions already migrated to tombstones",
					"migrated": 0,
					"skipped":  len(skippedIDs),
				})
			} else {
				fmt.Printf("All %d deletion(s) already have tombstones in issues.jsonl\n", len(skippedIDs))
			}
			return
		}

		// Dry run - just report what would happen
		if dryRun {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"dry_run":       true,
					"would_migrate": len(toMigrate),
					"skipped":       len(skippedIDs),
					"total":         len(records),
				})
			} else {
				fmt.Println("Dry run mode - no changes will be made")
				fmt.Printf("\nWould migrate %d deletion(s) to tombstones:\n", len(toMigrate))
				for _, record := range toMigrate {
					fmt.Printf("  - %s (deleted %s by %s)\n",
						record.ID,
						record.Timestamp.Format("2006-01-02"),
						record.Actor)
				}
				if len(skippedIDs) > 0 {
					fmt.Printf("\nWould skip %d already-migrated deletion(s)\n", len(skippedIDs))
				}
			}
			return
		}

		// Perform migration - append tombstones to issues.jsonl
		if verbose && !jsonOutput {
			fmt.Printf("Creating %d tombstone(s)...\n", len(toMigrate))
		}

		// Open issues.jsonl for appending
		// nolint:gosec // G304: issuesPath is controlled from beadsDir
		file, err := os.OpenFile(issuesPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "open_issues_failed",
					"message": err.Error(),
				})
			} else {
				fmt.Fprintf(os.Stderr, "Error opening issues.jsonl for append: %v\n", err)
			}
			os.Exit(1)
		}
		defer file.Close()

		encoder := json.NewEncoder(file)
		var migratedIDs []string
		for _, record := range toMigrate {
			tombstone := convertLegacyDeletionRecordToTombstone(record)
			if err := encoder.Encode(tombstone); err != nil {
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"error":    "write_tombstone_failed",
						"message":  err.Error(),
						"issue_id": record.ID,
					})
				} else {
					fmt.Fprintf(os.Stderr, "Error writing tombstone for %s: %v\n", record.ID, err)
				}
				os.Exit(1)
			}
			migratedIDs = append(migratedIDs, record.ID)
			if verbose && !jsonOutput {
				fmt.Printf("  ✓ Created tombstone for %s\n", record.ID)
			}
		}

		// Archive deletions.jsonl
		archivePath := deletionsPath + ".migrated"
		if err := os.Rename(deletionsPath, archivePath); err != nil {
			// Warn but don't fail - tombstones were already created
			if !jsonOutput {
				fmt.Printf("%s\n", ui.RenderWarn(fmt.Sprintf("Warning: could not archive deletions.jsonl: %v", err)))
			}
		} else if verbose && !jsonOutput {
			fmt.Printf("  ✓ Archived deletions.jsonl to %s\n", filepath.Base(archivePath))
		}

		// Success output
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":       "success",
				"migrated":     len(migratedIDs),
				"skipped":      len(skippedIDs),
				"total":        len(records),
				"archive":      archivePath,
				"migrated_ids": migratedIDs,
			})
		} else {
			fmt.Printf("\n%s\n\n", ui.RenderPass("✓ Migration complete"))
			fmt.Printf("  Migrated: %d tombstone(s)\n", len(migratedIDs))
			if len(skippedIDs) > 0 {
				fmt.Printf("  Skipped:  %d (already had tombstones)\n", len(skippedIDs))
			}
			fmt.Printf("  Archived: %s\n", filepath.Base(archivePath))
			fmt.Println("\nNext steps:")
			fmt.Println("  1. Run 'bd sync' to propagate tombstones to remote")
			fmt.Println("  2. Other clones will receive tombstones on next sync")
		}
	},
}

// convertLegacyDeletionRecordToTombstone creates a tombstone issue from a legacy deletion record.
func convertLegacyDeletionRecordToTombstone(del legacyDeletionRecordCmd) *types.Issue {
	deletedAt := del.Timestamp
	return &types.Issue{
		ID:           del.ID,
		Title:        "(deleted)",
		Description:  "",
		Status:       types.StatusTombstone,
		Priority:     0,              // Unknown priority (0 = unset)
		IssueType:    types.TypeTask, // Default type (must be valid)
		CreatedAt:    del.Timestamp,
		UpdatedAt:    del.Timestamp,
		DeletedAt:    &deletedAt,
		DeletedBy:    del.Actor,
		DeleteReason: del.Reason,
		OriginalType: "", // Not available in legacy deletions.jsonl
	}
}

func init() {
	migrateTombstonesCmd.Flags().Bool("dry-run", false, "Preview changes without modifying files")
	migrateTombstonesCmd.Flags().Bool("verbose", false, "Show detailed progress")
	migrateTombstonesCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	migrateCmd.AddCommand(migrateTombstonesCmd)

	// Backwards compatibility alias at root level (hidden)
	migrateTombstonesAliasCmd := *migrateTombstonesCmd
	migrateTombstonesAliasCmd.Use = "migrate-tombstones"
	migrateTombstonesAliasCmd.Hidden = true
	migrateTombstonesAliasCmd.Deprecated = "use 'bd migrate tombstones' instead (will be removed in v1.0.0)"
	rootCmd.AddCommand(&migrateTombstonesAliasCmd)
}
