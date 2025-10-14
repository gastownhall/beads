package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/sqlite"
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
  - Collisions (same ID, different content) are detected
  - Use --resolve-collisions to automatically remap colliding issues
  - Use --dry-run to preview changes without applying them`,
	Run: func(cmd *cobra.Command, args []string) {
		input, _ := cmd.Flags().GetString("input")
		skipUpdate, _ := cmd.Flags().GetBool("skip-existing")
		strict, _ := cmd.Flags().GetBool("strict")
		resolveCollisions, _ := cmd.Flags().GetBool("resolve-collisions")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Open input
		in := os.Stdin
		if input != "" {
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

		// Phase 2: Detect collisions
		sqliteStore, ok := store.(*sqlite.SQLiteStorage)
		if !ok {
			fmt.Fprintf(os.Stderr, "Error: collision detection requires SQLite storage backend\n")
			os.Exit(1)
		}

		collisionResult, err := sqlite.DetectCollisions(ctx, sqliteStore, allIssues)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error detecting collisions: %v\n", err)
			os.Exit(1)
		}

		var idMapping map[string]string
		var created, updated, skipped int

		// Phase 3: Handle collisions
		if len(collisionResult.Collisions) > 0 {
			// Print collision report
			printCollisionReport(collisionResult)

			if dryRun {
				// In dry-run mode, just print report and exit
				fmt.Fprintf(os.Stderr, "\nDry-run mode: no changes made\n")
				os.Exit(0)
			}

			if !resolveCollisions {
				// Default behavior: fail on collision (safe mode)
				fmt.Fprintf(os.Stderr, "\nCollision detected! Use --resolve-collisions to automatically remap colliding issues.\n")
				fmt.Fprintf(os.Stderr, "Or use --dry-run to preview without making changes.\n")
				os.Exit(1)
			}

			// Resolve collisions by scoring and remapping
			fmt.Fprintf(os.Stderr, "\nResolving collisions...\n")

			// Get all existing issues for scoring
			allExistingIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting existing issues: %v\n", err)
				os.Exit(1)
			}

			// Score collisions
			if err := sqlite.ScoreCollisions(ctx, sqliteStore, collisionResult.Collisions, allExistingIssues); err != nil {
				fmt.Fprintf(os.Stderr, "Error scoring collisions: %v\n", err)
				os.Exit(1)
			}

			// Remap collisions
			idMapping, err = sqlite.RemapCollisions(ctx, sqliteStore, collisionResult.Collisions, allExistingIssues)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error remapping collisions: %v\n", err)
				os.Exit(1)
			}

			// Print remapping report
			printRemappingReport(idMapping, collisionResult.Collisions)

			// Colliding issues were already created with new IDs
			created = len(collisionResult.Collisions)

			// Remove colliding issues from allIssues (they're already processed)
			filteredIssues := make([]*types.Issue, 0)
			collidingIDs := make(map[string]bool)
			for _, collision := range collisionResult.Collisions {
				collidingIDs[collision.ID] = true
			}
			for _, issue := range allIssues {
				if !collidingIDs[issue.ID] {
					filteredIssues = append(filteredIssues, issue)
				}
			}
			allIssues = filteredIssues
		} else if dryRun {
			// No collisions in dry-run mode
			fmt.Fprintf(os.Stderr, "No collisions detected.\n")
			fmt.Fprintf(os.Stderr, "Would create %d new issues, update %d existing issues\n",
				len(collisionResult.NewIssues), len(collisionResult.ExactMatches))
			os.Exit(0)
		}

		// Phase 4: Process remaining issues (exact matches and new issues)
		for _, issue := range allIssues {
			// Check if issue exists
			existing, err := store.GetIssue(ctx, issue.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error checking issue %s: %v\n", issue.ID, err)
				os.Exit(1)
			}

			if existing != nil {
				if skipUpdate {
					skipped++
					continue
				}

				// Update existing issue
				// Parse raw JSON to detect which fields are present
				var rawData map[string]interface{}
				jsonBytes, _ := json.Marshal(issue)
				if err := json.Unmarshal(jsonBytes, &rawData); err != nil {
					// If unmarshaling fails, treat all fields as present
					rawData = make(map[string]interface{})
				}

				updates := make(map[string]interface{})
				if _, ok := rawData["title"]; ok {
					updates["title"] = issue.Title
				}
				if _, ok := rawData["description"]; ok {
					updates["description"] = issue.Description
				}
				if _, ok := rawData["design"]; ok {
					updates["design"] = issue.Design
				}
				if _, ok := rawData["acceptance_criteria"]; ok {
					updates["acceptance_criteria"] = issue.AcceptanceCriteria
				}
				if _, ok := rawData["notes"]; ok {
					updates["notes"] = issue.Notes
				}
				if _, ok := rawData["status"]; ok {
					updates["status"] = issue.Status
				}
				if _, ok := rawData["priority"]; ok {
					updates["priority"] = issue.Priority
				}
				if _, ok := rawData["issue_type"]; ok {
					updates["issue_type"] = issue.IssueType
				}
				if _, ok := rawData["assignee"]; ok {
					updates["assignee"] = issue.Assignee
				}
				if _, ok := rawData["estimated_minutes"]; ok {
					if issue.EstimatedMinutes != nil {
						updates["estimated_minutes"] = *issue.EstimatedMinutes
					} else {
						updates["estimated_minutes"] = nil
					}
				}
				if _, ok := rawData["external_ref"]; ok {
					if issue.ExternalRef != nil {
						updates["external_ref"] = *issue.ExternalRef
					} else {
						updates["external_ref"] = nil
					}
				}

				if err := store.UpdateIssue(ctx, issue.ID, updates, "import"); err != nil {
					fmt.Fprintf(os.Stderr, "Error updating issue %s: %v\n", issue.ID, err)
					os.Exit(1)
				}
				updated++
			} else {
				// Create new issue
				if err := store.CreateIssue(ctx, issue, "import"); err != nil {
					fmt.Fprintf(os.Stderr, "Error creating issue %s: %v\n", issue.ID, err)
					os.Exit(1)
				}
				created++
			}
		}

		// Phase 5: Sync ID counters after importing issues with explicit IDs
		// This prevents ID collisions with subsequently auto-generated issues
		// CRITICAL: If this fails, subsequent auto-generated IDs WILL collide with imported issues
		if err := sqliteStore.SyncAllCounters(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to sync ID counters: %v\n", err)
			fmt.Fprintf(os.Stderr, "Cannot proceed - auto-generated IDs would collide with imported issues.\n")
			os.Exit(1)
		}

		// Phase 6: Process dependencies
		// Do this after all issues are created to handle forward references
		var depsCreated, depsSkipped int
		for _, issue := range allIssues {
			if len(issue.Dependencies) == 0 {
				continue
			}

			for _, dep := range issue.Dependencies {
				// Check if dependency already exists
				existingDeps, err := store.GetDependencyRecords(ctx, dep.IssueID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error checking dependencies for %s: %v\n", dep.IssueID, err)
					os.Exit(1)
				}

				// Skip if this exact dependency already exists
				exists := false
				for _, existing := range existingDeps {
					if existing.DependsOnID == dep.DependsOnID && existing.Type == dep.Type {
						exists = true
						break
					}
				}

				if exists {
					depsSkipped++
					continue
				}

				// Add dependency
				if err := store.AddDependency(ctx, dep, "import"); err != nil {
					if strict {
						// In strict mode, fail on any dependency error
						fmt.Fprintf(os.Stderr, "Error: could not add dependency %s → %s: %v\n",
							dep.IssueID, dep.DependsOnID, err)
						fmt.Fprintf(os.Stderr, "Use --strict=false to treat dependency errors as warnings\n")
						os.Exit(1)
					}
					// In non-strict mode, ignore errors for missing target issues or cycles
					// This can happen if dependencies reference issues not in the import
					fmt.Fprintf(os.Stderr, "Warning: could not add dependency %s → %s: %v\n",
						dep.IssueID, dep.DependsOnID, err)
					continue
				}
				depsCreated++
			}
		}

		// Schedule auto-flush after import completes
		markDirtyAndScheduleFlush()

		// Print summary
		fmt.Fprintf(os.Stderr, "Import complete: %d created, %d updated", created, updated)
		if skipped > 0 {
			fmt.Fprintf(os.Stderr, ", %d skipped", skipped)
		}
		if depsCreated > 0 || depsSkipped > 0 {
			fmt.Fprintf(os.Stderr, ", %d dependencies added", depsCreated)
			if depsSkipped > 0 {
				fmt.Fprintf(os.Stderr, " (%d already existed)", depsSkipped)
			}
		}
		if len(idMapping) > 0 {
			fmt.Fprintf(os.Stderr, ", %d issues remapped", len(idMapping))
		}
		fmt.Fprintf(os.Stderr, "\n")
	},
}

// printCollisionReport prints a detailed report of detected collisions
func printCollisionReport(result *sqlite.CollisionResult) {
	fmt.Fprintf(os.Stderr, "\n=== Collision Detection Report ===\n")
	fmt.Fprintf(os.Stderr, "Exact matches (idempotent): %d\n", len(result.ExactMatches))
	fmt.Fprintf(os.Stderr, "New issues: %d\n", len(result.NewIssues))
	fmt.Fprintf(os.Stderr, "COLLISIONS DETECTED: %d\n\n", len(result.Collisions))

	if len(result.Collisions) > 0 {
		fmt.Fprintf(os.Stderr, "Colliding issues:\n")
		for _, collision := range result.Collisions {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", collision.ID, collision.IncomingIssue.Title)
			fmt.Fprintf(os.Stderr, "    Conflicting fields: %v\n", collision.ConflictingFields)
		}
	}
}

// printRemappingReport prints a report of ID remappings with reference scores
func printRemappingReport(idMapping map[string]string, collisions []*sqlite.CollisionDetail) {
	fmt.Fprintf(os.Stderr, "\n=== Remapping Report ===\n")
	fmt.Fprintf(os.Stderr, "Issues remapped: %d\n\n", len(idMapping))

	// Sort by old ID for consistent output
	type mapping struct {
		oldID string
		newID string
		score int
	}
	mappings := make([]mapping, 0, len(idMapping))

	scoreMap := make(map[string]int)
	for _, collision := range collisions {
		scoreMap[collision.ID] = collision.ReferenceScore
	}

	for oldID, newID := range idMapping {
		mappings = append(mappings, mapping{
			oldID: oldID,
			newID: newID,
			score: scoreMap[oldID],
		})
	}

	sort.Slice(mappings, func(i, j int) bool {
		return mappings[i].score < mappings[j].score
	})

	fmt.Fprintf(os.Stderr, "Remappings (sorted by reference count):\n")
	for _, m := range mappings {
		fmt.Fprintf(os.Stderr, "  %s → %s (refs: %d)\n", m.oldID, m.newID, m.score)
	}
	fmt.Fprintf(os.Stderr, "\nAll text and dependency references have been updated.\n")
}

func init() {
	importCmd.Flags().StringP("input", "i", "", "Input file (default: stdin)")
	importCmd.Flags().BoolP("skip-existing", "s", false, "Skip existing issues instead of updating them")
	importCmd.Flags().Bool("strict", false, "Fail on dependency errors instead of treating them as warnings")
	importCmd.Flags().Bool("resolve-collisions", false, "Automatically resolve ID collisions by remapping")
	importCmd.Flags().Bool("dry-run", false, "Preview collision detection without making changes")
	rootCmd.AddCommand(importCmd)
}
