package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var renamePrefixCmd = &cobra.Command{
	Use:     "rename-prefix <new-prefix>",
	GroupID: "maint",
	Short:   "Rename the issue prefix for all issues in the database",
	Long: `Rename the issue prefix for all issues in the database.
This will update all issue IDs and all text references across all fields.

USE CASES:
- Shortening long prefixes (e.g., 'knowledge-work-' → 'kw-')
- Rebranding project naming conventions
- Consolidating multiple prefixes after database corruption
- Migrating to team naming standards

Prefix validation rules:
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
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("rename-prefix is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("rename-prefix")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		newPrefix := args[0]
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		repair, _ := cmd.Flags().GetBool("repair")

		if !dryRun {
			CheckReadonly("rename-prefix")
		}

		ctx := rootCtx

		if store == nil {
			if err := ensureStoreActive(); err != nil {
				return HandleError("%v", err)
			}
		}

		if err := validatePrefix(newPrefix); err != nil {
			return HandleError("%v", err)
		}

		oldPrefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil || oldPrefix == "" {
			return HandleError("failed to get current prefix: %v", err)
		}

		newPrefix = strings.TrimRight(newPrefix, "-")

		issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			return HandleError("failed to list issues: %v", err)
		}

		prefixes := detectPrefixes(issues)

		if len(prefixes) > 1 {
			fmt.Fprintf(os.Stderr, "%s Multiple prefixes detected in database:\n", ui.RenderFail("✗"))
			for prefix, count := range prefixes {
				fmt.Fprintf(os.Stderr, "  - %s: %d issues\n", ui.RenderWarn(prefix), count)
			}
			fmt.Fprintf(os.Stderr, "\n")

			if !repair {
				return HandleErrorWithHint(
					"cannot rename with multiple prefixes. Use --repair to consolidate.",
					fmt.Sprintf("Example: bd rename-prefix %s --repair", newPrefix),
				)
			}

			if err := repairPrefixes(ctx, store, actor, newPrefix, issues, prefixes, dryRun); err != nil {
				return HandleError("failed to repair prefixes: %v", err)
			}
			if !dryRun {
				commandDidWrite.Store(true)
			}
			return nil
		}

		if len(prefixes) == 1 && oldPrefix == newPrefix {
			return HandleError("new prefix is the same as current prefix: %s", oldPrefix)
		}

		if len(issues) == 0 {
			fmt.Printf("No issues to rename. Updating prefix to %s\n", newPrefix)
			if !dryRun {
				if err := store.SetConfig(ctx, "issue_prefix", newPrefix); err != nil {
					return HandleError("failed to update prefix: %v", err)
				}
				commandDidWrite.Store(true)
			}
			return nil
		}

		// The prefix actually on the rows is the ground truth for ID rewriting,
		// not the (possibly stale) issue_prefix config cell — see #5135 review.
		// detectPrefixes already established this is a single, consistent
		// prefix (the len(prefixes) > 1 case returned above).
		detected := oldPrefix
		if len(prefixes) == 1 {
			for p := range prefixes {
				detected = p
			}
		}

		// detected == newPrefix means every row is already correctly prefixed
		// and only the config cell is stale (GH#4827 half-migrated database).
		// Repair the config without touching any IDs.
		if detected == newPrefix {
			if dryRun {
				fmt.Printf("DRY RUN: config prefix disagrees with issue IDs — would repair config only: '%s' -> '%s' (%d issue IDs already correct)\n", oldPrefix, newPrefix, len(issues))
				return nil
			}
			if err := store.SetConfig(ctx, "issue_prefix", newPrefix); err != nil {
				return HandleError("failed to update prefix: %v", err)
			}
			commandDidWrite.Store(true)
			fmt.Printf("%s Repaired config prefix: '%s' -> '%s' (%d issue IDs already correct, none rewritten)\n", ui.RenderPass("✓"), ui.RenderAccent(oldPrefix), ui.RenderAccent(newPrefix), len(issues))
			if jsonOutput {
				result := map[string]interface{}{
					"old_prefix":      oldPrefix,
					"new_prefix":      newPrefix,
					"issues_count":    0,
					"config_repaired": true,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if eerr := enc.Encode(result); eerr != nil {
					return eerr
				}
			}
			return nil
		}

		if dryRun {
			fmt.Printf("DRY RUN: Would rename issues from prefix '%s' to '%s'\n\n", detected, newPrefix)
			fmt.Printf("Sample changes:\n")
			shown := 0
			for _, issue := range issues {
				newID := rewriteIssueID(detected, newPrefix, issue.ID)
				if shown < 5 {
					fmt.Printf("  %s -> %s\n", ui.RenderAccent(issue.ID), ui.RenderAccent(newID))
					shown++
				}
			}
			if remaining := len(issues) - shown; remaining > 0 {
				fmt.Printf("... and %d more issues\n", remaining)
			}
			return nil
		}

		fmt.Printf("Renaming issues from prefix '%s' to '%s'...\n", detected, newPrefix)

		if err := renamePrefixInDB(ctx, detected, newPrefix, issues); err != nil {
			return HandleError("failed to rename prefix: %v", err)
		}

		commandDidWrite.Store(true)

		fmt.Printf("%s Successfully renamed prefix from %s to %s\n", ui.RenderPass("✓"), ui.RenderAccent(detected), ui.RenderAccent(newPrefix))

		if jsonOutput {
			result := map[string]interface{}{
				"old_prefix":   detected,
				"new_prefix":   newPrefix,
				"issues_count": len(issues),
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if eerr := enc.Encode(result); eerr != nil {
				return eerr
			}
		}

		return nil
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
func repairPrefixes(ctx context.Context, st storage.DoltStorage, actorName string, targetPrefix string, issues []*types.Issue, prefixes map[string]int, dryRun bool) error {

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
		newID, err := generateRepairHashID(targetPrefix, is.issue, actorName, usedIDs)
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

	// Set the new prefix in config
	if err := st.SetConfig(ctx, "issue_prefix", targetPrefix); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

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

// rewriteIssueID maps oldID from oldPrefix to newPrefix. oldPrefix must be
// the prefix actually detected on the issue rows (see detectPrefixes), not
// the possibly-stale issue_prefix config cell — see PR #5135 review (maphew,
// 2026-07-29): using the config cell here made the "already on target
// prefix" check ambiguous with genuine prefix-shortening renames (e.g.
// "beads-vscode-" -> "beads-" would leave "beads-vscode-1" unchanged,
// because it also starts with "beads-"). The GH#4827 half-migrated-config
// case is now handled by the caller comparing the detected prefix to
// newPrefix directly and skipping ID rewrites entirely (config-only
// repair), so this function no longer needs — or has — a newPrefix guard.
func rewriteIssueID(oldPrefix, newPrefix, oldID string) string {
	oldP := strings.TrimRight(oldPrefix, "-")
	newP := strings.TrimRight(newPrefix, "-")
	if oldP == "" || newP == "" {
		return oldID
	}
	if strings.HasPrefix(oldID, oldP+"-") {
		return newP + "-" + strings.TrimPrefix(oldID, oldP+"-")
	}
	return oldID
}

func renamePrefixInDB(ctx context.Context, oldPrefix, newPrefix string, issues []*types.Issue) error {
	// NOTE: Each issue is updated in its own transaction. A failure mid-way could leave
	// the database in a mixed state with some issues renamed and others not.
	// For production use, consider implementing a single atomic RenamePrefix() method
	// in the storage layer that wraps all updates in one transaction.

	oldP := strings.TrimRight(oldPrefix, "-")
	newP := strings.TrimRight(newPrefix, "-")
	oldPrefixPattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(oldP) + `-(\d+)\b`)

	replaceFunc := func(match string) string {
		return strings.Replace(match, oldP+"-", newP+"-", 1)
	}

	for _, issue := range issues {
		oldID := issue.ID
		newID := rewriteIssueID(oldP, newP, oldID)

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

		// ID already on the target prefix (stale config cell only): skip UpdateIssueID
		// so we never produce atlas-atlas-* (GH#4827).
		if newID == oldID {
			continue
		}

		issue.ID = newID
		if err := store.UpdateIssueID(ctx, oldID, newID, issue, actor); err != nil {
			return fmt.Errorf("failed to update issue %s: %w", oldID, err)
		}
	}

	if err := store.SetConfig(ctx, "issue_prefix", newP); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

	return nil
}

// generateRepairHashID generates a hash-based ID for an issue during repair.
// Uses content hashing and checks usedIDs for batch collision avoidance.
func generateRepairHashID(prefix string, issue *types.Issue, actor string, usedIDs map[string]bool) (string, error) {
	// Generate a hash ID from issue content (same approach as generateHashIDForIssue)
	content := fmt.Sprintf("%s|%s|%s|%d|%d",
		issue.Title,
		issue.Description,
		actor,
		issue.CreatedAt.UnixNano(),
		0, // nonce
	)
	h := sha256.Sum256([]byte(content))
	shortHash := hex.EncodeToString(h[:4]) // 4 bytes = 8 hex chars
	newID := fmt.Sprintf("%s-%s", prefix, shortHash)

	// Check if this ID was already used in this batch
	// If so, we need to generate a new one with a different nonce
	attempts := 0
	for usedIDs[newID] && attempts < 100 {
		attempts++
		content = fmt.Sprintf("%s|%s|%s|%d|%d",
			issue.Title,
			issue.Description,
			actor,
			issue.CreatedAt.UnixNano(),
			attempts,
		)
		h = sha256.Sum256([]byte(content))
		shortHash = hex.EncodeToString(h[:4])
		newID = fmt.Sprintf("%s-%s", prefix, shortHash)
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
