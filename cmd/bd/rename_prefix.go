package main

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var renamePrefixCmd = &cobra.Command{
	Use:     "rename-prefix <new-prefix>",
	GroupID: GroupMaintenance,
	Short:   "Rename the issue prefix for all issues in the database",
	Long: `Rename the issue prefix for all issues in the database.
This will update all issue IDs and all text references across all fields.

USE CASES:
- Shortening long prefixes (e.g., 'knowledge-work-' → 'kw-')
- Rebranding project naming conventions
- Consolidating multiple prefixes after database corruption
- Migrating to team naming standards

Prefix validation rules:
- Max length: 8 characters
- Allowed characters: lowercase letters, numbers, hyphens
- Must start with a letter
- Must end with a hyphen (e.g., 'kw-', 'work-')
- Cannot be empty or just a hyphen

Multiple prefix detection and repair:
If issues have multiple prefixes (corrupted database), use --repair to consolidate them.
The --repair flag will rename all issues with incorrect prefixes to the new prefix,
preserving issues that already have the correct prefix.

EXAMPLES:
  bd rename-prefix kw-                # Rename from 'knowledge-work-' to 'kw-'
  bd rename-prefix mtg- --repair      # Consolidate multiple prefixes into 'mtg-'
  bd rename-prefix team- --dry-run    # Preview changes without applying

NOTE: This is a rare operation. Most users never need this command.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		newPrefix := args[0]
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		repair, _ := cmd.Flags().GetBool("repair")

		// Block writes in readonly mode
		if !dryRun {
			CheckReadonly("rename-prefix")
		}

		ctx := rootCtx

		// rename-prefix requires direct mode (not supported by daemon)
		if daemonClient != nil {
			if err := ensureDirectMode("daemon does not support rename-prefix command"); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else if store == nil {
			if err := ensureStoreActive(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		if err := validatePrefix(newPrefix); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		oldPrefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil || oldPrefix == "" {
			fmt.Fprintf(os.Stderr, "Error: failed to get current prefix: %v\n", err)
			os.Exit(1)
		}

		newPrefix = strings.TrimRight(newPrefix, "-")

		// Check for multiple prefixes first
		issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to list issues: %v\n", err)
			os.Exit(1)
		}

		prefixes := detectPrefixes(issues)

		if len(prefixes) > 1 {
			// Multiple prefixes detected - requires repair mode

			fmt.Fprintf(os.Stderr, "%s Multiple prefixes detected in database:\n", ui.RenderFail("✗"))
			for prefix, count := range prefixes {
				fmt.Fprintf(os.Stderr, "  - %s: %d issues\n", ui.RenderWarn(prefix), count)
			}
			fmt.Fprintf(os.Stderr, "\n")

			if !repair {
				fmt.Fprintf(os.Stderr, "Error: cannot rename with multiple prefixes. Use --repair to consolidate.\n")
				fmt.Fprintf(os.Stderr, "Example: bd rename-prefix %s --repair\n", newPrefix)
				os.Exit(1)
			}

			// Repair mode: consolidate all prefixes to newPrefix
			if err := repairPrefixes(ctx, store, actor, newPrefix, issues, prefixes, dryRun); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to repair prefixes: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Single prefix case - check if trying to rename to same prefix
		if len(prefixes) == 1 && oldPrefix == newPrefix {
			fmt.Fprintf(os.Stderr, "Error: new prefix is the same as current prefix: %s\n", oldPrefix)
			os.Exit(1)
		}

		// issues already fetched above
		if len(issues) == 0 {
			fmt.Printf("No issues to rename. Updating prefix to %s\n", newPrefix)
			if !dryRun {
				if err := store.SetConfig(ctx, "issue_prefix", newPrefix); err != nil {
					fmt.Fprintf(os.Stderr, "Error: failed to update prefix: %v\n", err)
					os.Exit(1)
				}
			}
			return
		}

		if dryRun {
				fmt.Printf("DRY RUN: Would rename %d issues from prefix '%s' to '%s'\n\n", len(issues), oldPrefix, newPrefix)
			fmt.Printf("Sample changes:\n")
			for i, issue := range issues {
				if i >= 5 {
					fmt.Printf("... and %d more issues\n", len(issues)-5)
					break
				}
				oldID := fmt.Sprintf("%s-%s", oldPrefix, strings.TrimPrefix(issue.ID, oldPrefix+"-"))
				newID := fmt.Sprintf("%s-%s", newPrefix, strings.TrimPrefix(issue.ID, oldPrefix+"-"))
				fmt.Printf("  %s -> %s\n", ui.RenderAccent(oldID), ui.RenderAccent(newID))
			}
			return
		}


		fmt.Printf("Renaming %d issues from prefix '%s' to '%s'...\n", len(issues), oldPrefix, newPrefix)

		if err := renamePrefixInDB(ctx, oldPrefix, newPrefix, issues); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to rename prefix: %v\n", err)
			os.Exit(1)
		}

		// Schedule full export (IDs changed, incremental won't work)
		markDirtyAndScheduleFullExport()

		fmt.Printf("%s Successfully renamed prefix from %s to %s\n", ui.RenderPass("✓"), ui.RenderAccent(oldPrefix), ui.RenderAccent(newPrefix))

		if jsonOutput {
			result := map[string]interface{}{
				"old_prefix":   oldPrefix,
				"new_prefix":   newPrefix,
				"issues_count": len(issues),
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(result)
		}
	},
}

func validatePrefix(prefix string) error {
	prefix = strings.TrimRight(prefix, "-")

	if prefix == "" {
		return fmt.Errorf("prefix cannot be empty")
	}

	matched, _ := regexp.MatchString(`^[a-z][a-z0-9-]*$`, prefix)
	if !matched {
		return fmt.Errorf("prefix must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens: %s", prefix)
	}

	if strings.HasPrefix(prefix, "-") || strings.HasSuffix(prefix, "--") {
		return fmt.Errorf("prefix has invalid hyphen placement: %s", prefix)
	}

	return nil
}

// detectPrefixes analyzes all issues and returns a map of prefix -> count
func detectPrefixes(issues []*types.Issue) map[string]int {
	prefixes := make(map[string]int)
	for _, issue := range issues {
		prefix := utils.ExtractIssuePrefix(issue.ID)
		if prefix != "" {
			prefixes[prefix]++
		}
	}
	return prefixes
}

// issueSort is used for sorting issues by prefix and number
type issueSort struct {
	issue  *types.Issue
	prefix string
	number int
}

// repairPrefixes consolidates multiple prefixes into a single target prefix
// Issues with the correct prefix are left unchanged.
// Issues with incorrect prefixes get new hash-based IDs.
func repairPrefixes(ctx context.Context, st storage.Storage, actorName string, targetPrefix string, issues []*types.Issue, prefixes map[string]int, dryRun bool) error {

	// Separate issues into correct and incorrect prefix groups
	var correctIssues []*types.Issue
	var incorrectIssues []issueSort

	for _, issue := range issues {
		prefix := utils.ExtractIssuePrefix(issue.ID)
		number := utils.ExtractIssueNumber(issue.ID)

		if prefix == targetPrefix {
			correctIssues = append(correctIssues, issue)
		} else {
			incorrectIssues = append(incorrectIssues, issueSort{
				issue:  issue,
				prefix: prefix,
				number: number,
			})
		}
	}

	// Sort incorrect issues: first by prefix lexicographically, then by number
	slices.SortFunc(incorrectIssues, func(a, b issueSort) int {
		return cmp.Or(
			cmp.Compare(a.prefix, b.prefix),
			cmp.Compare(a.number, b.number),
		)
	})

	// Get a database connection for ID generation
	conn, err := st.UnderlyingConn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get database connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Build a map of all renames for text replacement using hash IDs
	// Track used IDs to avoid collisions within the batch
	renameMap := make(map[string]string)
	usedIDs := make(map[string]bool)

	// Mark existing correct IDs as used
	for _, issue := range correctIssues {
		usedIDs[issue.ID] = true
	}

	// Generate hash IDs for all incorrect issues
	for _, is := range incorrectIssues {
		newID, err := generateRepairHashID(ctx, conn, targetPrefix, is.issue, actorName, usedIDs)
		if err != nil {
			return fmt.Errorf("failed to generate hash ID for %s: %w", is.issue.ID, err)
		}
		renameMap[is.issue.ID] = newID
		usedIDs[newID] = true
	}

	if dryRun {
		fmt.Printf("DRY RUN: Would repair %d issues with incorrect prefixes\n\n", len(incorrectIssues))
		fmt.Printf("Issues with correct prefix (%s): %d\n", ui.RenderAccent(targetPrefix), len(correctIssues))
		fmt.Printf("Issues to repair: %d\n\n", len(incorrectIssues))

		fmt.Printf("Planned renames (showing first 10):\n")
		for i, is := range incorrectIssues {
			if i >= 10 {
				fmt.Printf("... and %d more\n", len(incorrectIssues)-10)
				break
			}
			oldID := is.issue.ID
			newID := renameMap[oldID]
			fmt.Printf("  %s -> %s\n", ui.RenderWarn(oldID), ui.RenderAccent(newID))
		}
		return nil
	}

	// Perform the repairs
	fmt.Printf("Repairing database with multiple prefixes...\n")
	fmt.Printf("  Issues with correct prefix (%s): %d\n", ui.RenderAccent(targetPrefix), len(correctIssues))
	fmt.Printf("  Issues to repair: %d\n\n", len(incorrectIssues))

	// Pattern to match any issue ID reference in text (both hash and sequential IDs)
	oldPrefixPattern := regexp.MustCompile(`\b[a-z][a-z0-9-]*-[a-z0-9]+\b`)

	// Rename each issue
	for _, is := range incorrectIssues {
		oldID := is.issue.ID
		newID := renameMap[oldID]

		// Apply text replacements in all issue fields
		issue := is.issue
		issue.ID = newID

		// Replace all issue IDs in text fields using the rename map
		replaceFunc := func(match string) string {
			if newID, ok := renameMap[match]; ok {
				return newID
			}
			return match
		}

		issue.Title = oldPrefixPattern.ReplaceAllStringFunc(issue.Title, replaceFunc)
		issue.Description = oldPrefixPattern.ReplaceAllStringFunc(issue.Description, replaceFunc)
		if issue.Design != "" {
			issue.Design = oldPrefixPattern.ReplaceAllStringFunc(issue.Design, replaceFunc)
		}
		if issue.AcceptanceCriteria != "" {
			issue.AcceptanceCriteria = oldPrefixPattern.ReplaceAllStringFunc(issue.AcceptanceCriteria, replaceFunc)
		}
		if issue.Notes != "" {
			issue.Notes = oldPrefixPattern.ReplaceAllStringFunc(issue.Notes, replaceFunc)
		}

		// Update the issue in the database
		if err := st.UpdateIssueID(ctx, oldID, newID, issue, actorName); err != nil {
			return fmt.Errorf("failed to update issue %s -> %s: %w", oldID, newID, err)
		}

		fmt.Printf("  Renamed %s -> %s\n", ui.RenderWarn(oldID), ui.RenderAccent(newID))
	}

	// Update all dependencies to use new prefix
	for oldPrefix := range prefixes {
		if oldPrefix != targetPrefix {
			if err := st.RenameDependencyPrefix(ctx, oldPrefix, targetPrefix); err != nil {
				return fmt.Errorf("failed to update dependencies for prefix %s: %w", oldPrefix, err)
			}
		}
	}

	// Update counters for all old prefixes
	for oldPrefix := range prefixes {
		if oldPrefix != targetPrefix {
			if err := st.RenameCounterPrefix(ctx, oldPrefix, targetPrefix); err != nil {
				return fmt.Errorf("failed to update counter for prefix %s: %w", oldPrefix, err)
			}
		}
	}

	// Set the new prefix in config
	if err := st.SetConfig(ctx, "issue_prefix", targetPrefix); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

	// Schedule full export (IDs changed, incremental won't work)
	markDirtyAndScheduleFullExport()

	fmt.Printf("\n%s Successfully consolidated %d prefixes into %s\n",
		ui.RenderPass("✓"), len(prefixes), ui.RenderAccent(targetPrefix))
	fmt.Printf("  %d issues repaired, %d issues unchanged\n", len(incorrectIssues), len(correctIssues))

	if jsonOutput {
		result := map[string]interface{}{
			"target_prefix":    targetPrefix,
			"prefixes_found":   len(prefixes),
			"issues_repaired":  len(incorrectIssues),
			"issues_unchanged": len(correctIssues),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
	}

	return nil
}

func renamePrefixInDB(ctx context.Context, oldPrefix, newPrefix string, issues []*types.Issue) error {
	// NOTE: Each issue is updated in its own transaction. A failure mid-way could leave
	// the database in a mixed state with some issues renamed and others not.
	// For production use, consider implementing a single atomic RenamePrefix() method
	// in the storage layer that wraps all updates in one transaction.

	oldPrefixPattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(oldPrefix) + `-(\d+)\b`)

	replaceFunc := func(match string) string {
		return strings.Replace(match, oldPrefix+"-", newPrefix+"-", 1)
	}

	for _, issue := range issues {
		oldID := issue.ID
		numPart := strings.TrimPrefix(oldID, oldPrefix+"-")
		newID := fmt.Sprintf("%s-%s", newPrefix, numPart)

		issue.ID = newID

		issue.Title = oldPrefixPattern.ReplaceAllStringFunc(issue.Title, replaceFunc)
		issue.Description = oldPrefixPattern.ReplaceAllStringFunc(issue.Description, replaceFunc)
		if issue.Design != "" {
			issue.Design = oldPrefixPattern.ReplaceAllStringFunc(issue.Design, replaceFunc)
		}
		if issue.AcceptanceCriteria != "" {
			issue.AcceptanceCriteria = oldPrefixPattern.ReplaceAllStringFunc(issue.AcceptanceCriteria, replaceFunc)
		}
		if issue.Notes != "" {
			issue.Notes = oldPrefixPattern.ReplaceAllStringFunc(issue.Notes, replaceFunc)
		}

		if err := store.UpdateIssueID(ctx, oldID, newID, issue, actor); err != nil {
			return fmt.Errorf("failed to update issue %s: %w", oldID, err)
		}
	}

	if err := store.RenameDependencyPrefix(ctx, oldPrefix, newPrefix); err != nil {
		return fmt.Errorf("failed to update dependencies: %w", err)
	}

	if err := store.RenameCounterPrefix(ctx, oldPrefix, newPrefix); err != nil {
		return fmt.Errorf("failed to update counter: %w", err)
	}

	if err := store.SetConfig(ctx, "issue_prefix", newPrefix); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

	return nil
}

// generateRepairHashID generates a hash-based ID for an issue during repair
// Uses the sqlite.GenerateIssueID function but also checks usedIDs for batch collision avoidance
func generateRepairHashID(ctx context.Context, conn *sql.Conn, prefix string, issue *types.Issue, actor string, usedIDs map[string]bool) (string, error) {
	// Try to generate a unique ID using the standard generation function
	// This handles collision detection against existing database IDs
	newID, err := sqlite.GenerateIssueID(ctx, conn, prefix, issue, actor)
	if err != nil {
		return "", err
	}

	// Check if this ID was already used in this batch
	// If so, we need to generate a new one with a different timestamp
	attempts := 0
	for usedIDs[newID] && attempts < 100 {
		// Slightly modify the creation time to get a different hash
		modifiedIssue := *issue
		modifiedIssue.CreatedAt = issue.CreatedAt.Add(time.Duration(attempts+1) * time.Nanosecond)
		newID, err = sqlite.GenerateIssueID(ctx, conn, prefix, &modifiedIssue, actor)
		if err != nil {
			return "", err
		}
		attempts++
	}

	if usedIDs[newID] {
		return "", fmt.Errorf("failed to generate unique ID after %d attempts", attempts)
	}

	return newID, nil
}

func init() {
	renamePrefixCmd.Flags().Bool("dry-run", false, "Preview changes without applying them")
	renamePrefixCmd.Flags().Bool("repair", false, "Repair database with multiple prefixes by consolidating them")
	rootCmd.AddCommand(renamePrefixCmd)
}
