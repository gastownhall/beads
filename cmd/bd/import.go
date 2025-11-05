package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Import issues from JSONL format",
	Long: `Import issues from JSON Lines format (one JSON object per line).

Reads from stdin by default, or use -i flag for file input.

Behavior:
  - Existing issues (same ID) are updated
  - New issues are created
  - Collisions (same ID, different content) are detected and reported
  - Use --dedupe-after to find and merge content duplicates after import
  - Use --dry-run to preview changes without applying them`,
	Run: func(cmd *cobra.Command, args []string) {
		input, _ := cmd.Flags().GetString("input")
		skipUpdate, _ := cmd.Flags().GetBool("skip-existing")
		strict, _ := cmd.Flags().GetBool("strict")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		renameOnImport, _ := cmd.Flags().GetBool("rename-on-import")
		dedupeAfter, _ := cmd.Flags().GetBool("dedupe-after")
		orphanHandling, _ := cmd.Flags().GetString("orphan-handling")

		// Open input
		in := os.Stdin
		if input != "" {
			// #nosec G304 - user-provided file path is intentional
			f, err := os.Open(input)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error opening input file: %v\n", err)
				os.Exit(1)
			}
			defer func() {
				if err := f.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to close input file: %v\n", err)
				}
			}()
			in = f
		}

		// Phase 1: Read and parse all JSONL
		ctx := context.Background()
		scanner := bufio.NewScanner(in)

		var allIssues []*types.Issue
		lineNum := 0

		for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
		continue
		}

		// Detect git conflict markers
		if strings.Contains(line, "<<<<<<<") || strings.Contains(line, "=======") || strings.Contains(line, ">>>>>>>") {
		 fmt.Fprintf(os.Stderr, "Error: Git conflict markers detected in JSONL file (line %d)\n\n", lineNum)
		fmt.Fprintf(os.Stderr, "To resolve:\n")
		fmt.Fprintf(os.Stderr, "  git checkout --ours .beads/issues.jsonl && bd import -i .beads/issues.jsonl\n")
		 fmt.Fprintf(os.Stderr, "  git checkout --theirs .beads/issues.jsonl && bd import -i .beads/issues.jsonl\n\n")
			fmt.Fprintf(os.Stderr, "For advanced field-level merging, see: https://github.com/neongreen/mono/tree/main/beads-merge\n")
		 os.Exit(1)
		 }

		// Parse JSON
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing line %d: %v\n", lineNum, err)
			os.Exit(1)
		}

		allIssues = append(allIssues, &issue)
	}

		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			os.Exit(1)
		}

		// Phase 2: Use shared import logic
		opts := ImportOptions{
			DryRun:         dryRun,
			SkipUpdate:     skipUpdate,
			Strict:         strict,
			RenameOnImport: renameOnImport,
			OrphanHandling: orphanHandling,
		}

		result, err := importIssuesCore(ctx, dbPath, store, allIssues, opts)

		// Handle errors and special cases
		if err != nil {
			// Check if it's a prefix mismatch error
			if result != nil && result.PrefixMismatch {
				fmt.Fprintf(os.Stderr, "\n=== Prefix Mismatch Detected ===\n")
				fmt.Fprintf(os.Stderr, "Database configured prefix: %s-\n", result.ExpectedPrefix)
				fmt.Fprintf(os.Stderr, "Found issues with different prefixes:\n")
				for prefix, count := range result.MismatchPrefixes {
					fmt.Fprintf(os.Stderr, "  %s- (%d issues)\n", prefix, count)
				}
				fmt.Fprintf(os.Stderr, "\nOptions:\n")
				fmt.Fprintf(os.Stderr, "  --rename-on-import    Auto-rename imported issues to match configured prefix\n")
				fmt.Fprintf(os.Stderr, "  --dry-run             Preview what would be imported\n")
				fmt.Fprintf(os.Stderr, "\nOr use 'bd rename-prefix' after import to fix the database.\n")
				os.Exit(1)
			}
			
			// Check if it's a collision error
			if result != nil && len(result.CollisionIDs) > 0 {
				// Print collision report before exiting
				fmt.Fprintf(os.Stderr, "\n=== Collision Detection Report ===\n")
				fmt.Fprintf(os.Stderr, "COLLISIONS DETECTED: %d\n\n", result.Collisions)
				fmt.Fprintf(os.Stderr, "Colliding issue IDs: %v\n", result.CollisionIDs)
				fmt.Fprintf(os.Stderr, "\nWith hash-based IDs, collisions should not occur.\n")
				fmt.Fprintf(os.Stderr, "This may indicate manual ID manipulation or a bug.\n")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Import failed: %v\n", err)
			os.Exit(1)
		}

		// Handle dry-run mode
		if dryRun {
			if result.PrefixMismatch {
				fmt.Fprintf(os.Stderr, "\n=== Prefix Mismatch Detected ===\n")
				fmt.Fprintf(os.Stderr, "Database configured prefix: %s-\n", result.ExpectedPrefix)
				fmt.Fprintf(os.Stderr, "Found issues with different prefixes:\n")
				for prefix, count := range result.MismatchPrefixes {
					fmt.Fprintf(os.Stderr, "  %s- (%d issues)\n", prefix, count)
				}
				fmt.Fprintf(os.Stderr, "\nUse --rename-on-import to automatically fix prefixes during import.\n")
			}
			
			if result.Collisions > 0 {
				fmt.Fprintf(os.Stderr, "\n=== Collision Detection Report ===\n")
				fmt.Fprintf(os.Stderr, "COLLISIONS DETECTED: %d\n", result.Collisions)
				fmt.Fprintf(os.Stderr, "Colliding issue IDs: %v\n", result.CollisionIDs)
			} else if !result.PrefixMismatch {
				fmt.Fprintf(os.Stderr, "No collisions detected.\n")
			}
			msg := fmt.Sprintf("Would create %d new issues, update %d existing issues", result.Created, result.Updated)
			if result.Unchanged > 0 {
				msg += fmt.Sprintf(", %d unchanged", result.Unchanged)
			}
			fmt.Fprintf(os.Stderr, "%s\n", msg)
			fmt.Fprintf(os.Stderr, "\nDry-run mode: no changes made\n")
			os.Exit(0)
		}

		// Print remapping report if collisions were resolved
		if len(result.IDMapping) > 0 {
			fmt.Fprintf(os.Stderr, "\n=== Remapping Report ===\n")
			fmt.Fprintf(os.Stderr, "Issues remapped: %d\n\n", len(result.IDMapping))

			// Sort by old ID for consistent output
			type mapping struct {
				oldID string
				newID string
			}
			mappings := make([]mapping, 0, len(result.IDMapping))
			for oldID, newID := range result.IDMapping {
				mappings = append(mappings, mapping{oldID, newID})
			}
			sort.Slice(mappings, func(i, j int) bool {
				return mappings[i].oldID < mappings[j].oldID
			})

			fmt.Fprintf(os.Stderr, "Remappings:\n")
			for _, m := range mappings {
				fmt.Fprintf(os.Stderr, "  %s → %s\n", m.oldID, m.newID)
			}
			fmt.Fprintf(os.Stderr, "\nAll text and dependency references have been updated.\n")
		}

		// Flush immediately after import (no debounce) to ensure daemon sees changes
		// Without this, daemon FileWatcher won't detect the import for up to 30s
		// Only flush if there were actual changes to avoid unnecessary I/O
		if result.Created > 0 || result.Updated > 0 || len(result.IDMapping) > 0 {
			flushToJSONL()
		}

		// Print summary
		fmt.Fprintf(os.Stderr, "Import complete: %d created, %d updated", result.Created, result.Updated)
		if result.Unchanged > 0 {
			fmt.Fprintf(os.Stderr, ", %d unchanged", result.Unchanged)
		}
		if result.Skipped > 0 {
			fmt.Fprintf(os.Stderr, ", %d skipped", result.Skipped)
		}
		if len(result.IDMapping) > 0 {
			fmt.Fprintf(os.Stderr, ", %d issues remapped", len(result.IDMapping))
		}
		fmt.Fprintf(os.Stderr, "\n")

		// Run duplicate detection if requested
		if dedupeAfter {
			fmt.Fprintf(os.Stderr, "\n=== Post-Import Duplicate Detection ===\n")

			// Get all issues (fresh after import)
			allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issues for deduplication: %v\n", err)
				os.Exit(1)
			}

			duplicateGroups := findDuplicateGroups(allIssues)
			if len(duplicateGroups) == 0 {
				fmt.Fprintf(os.Stderr, "No duplicates found.\n")
				return
			}

			refCounts := countReferences(allIssues)

			fmt.Fprintf(os.Stderr, "Found %d duplicate group(s)\n\n", len(duplicateGroups))

			for i, group := range duplicateGroups {
				target := chooseMergeTarget(group, refCounts)
				fmt.Fprintf(os.Stderr, "Group %d: %s\n", i+1, group[0].Title)

				for _, issue := range group {
					refs := refCounts[issue.ID]
					marker := "  "
					if issue.ID == target.ID {
						marker = "→ "
					}
					fmt.Fprintf(os.Stderr, "  %s%s (%s, P%d, %d refs)\n",
						marker, issue.ID, issue.Status, issue.Priority, refs)
				}

				sources := make([]string, 0, len(group)-1)
				for _, issue := range group {
					if issue.ID != target.ID {
						sources = append(sources, issue.ID)
					}
				}
				fmt.Fprintf(os.Stderr, "  Suggested: bd merge %s --into %s\n\n",
					strings.Join(sources, " "), target.ID)
			}

			fmt.Fprintf(os.Stderr, "Run 'bd duplicates --auto-merge' to merge all duplicates.\n")
		}
	},
}

func init() {
	importCmd.Flags().StringP("input", "i", "", "Input file (default: stdin)")
	importCmd.Flags().BoolP("skip-existing", "s", false, "Skip existing issues instead of updating them")
	importCmd.Flags().Bool("strict", false, "Fail on dependency errors instead of treating them as warnings")
	importCmd.Flags().Bool("dedupe-after", false, "Detect and report content duplicates after import")
	importCmd.Flags().Bool("dry-run", false, "Preview collision detection without making changes")
	importCmd.Flags().Bool("rename-on-import", false, "Rename imported issues to match database prefix (updates all references)")
	importCmd.Flags().String("orphan-handling", "", "How to handle missing parent issues: strict/resurrect/skip/allow (default: use config or 'allow')")
	importCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output import statistics in JSON format")
	rootCmd.AddCommand(importCmd)
}
