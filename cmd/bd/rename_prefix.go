package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
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

			if err := repairPrefixes(ctx, store, currentActor(), newPrefix, issues, prefixes, dryRun); err != nil {
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

// repairPrefixes consolidates multiple prefixes into a single target prefix.
// Rows already on the target prefix are left unchanged; every other row keeps
// its suffix under the target prefix when that destination is free (see
// planPrefixRepair). Families whose natural destination conflicts — with a
// live row, a live child-only namespace, a retired id, or a destination an
// earlier family claimed — move whole to a generated hash root, dotted paths
// intact.
func repairPrefixes(ctx context.Context, st storage.DoltStorage, actorName string, targetPrefix string, issues []*types.Issue, prefixes map[string]int, dryRun bool) error {
	plan, err := planPrefixRepair(targetPrefix, actorName, issues, knownIssuePrefixes(ctx, st, prefixes), repairRetiredLookup(ctx, st))
	if err != nil {
		return err
	}

	// JSON mode: exactly one object — the same object a dry run previews —
	// with no text progress on the stream. A dry run reports and stops; an
	// execution reports what it did.
	if dryRun && jsonOutput {
		return printRepairJSON(plan, prefixes, true)
	}

	if dryRun {
		fmt.Printf("DRY RUN: Would repair %d issues with incorrect prefixes\n\n", len(plan.Renames))
		fmt.Printf("Issues with correct prefix (%s): %d\n", ui.RenderAccent(targetPrefix), plan.Unchanged)
		fmt.Printf("Issues to repair: %d\n\n", len(plan.Renames))

		fmt.Printf("Planned renames (showing first 10):\n")
		for i, rename := range plan.Renames {
			if i >= 10 {
				fmt.Printf("... and %d more\n", len(plan.Renames)-10)
				break
			}
			fmt.Printf("  %s -> %s\n", ui.RenderWarn(rename.From), ui.RenderAccent(rename.To))
		}
		printRepairCollisions(plan)
		return nil
	}

	// Perform the repairs
	if !jsonOutput {
		fmt.Printf("Repairing database with multiple prefixes...\n")
		fmt.Printf("  Issues with correct prefix (%s): %d\n", ui.RenderAccent(targetPrefix), plan.Unchanged)
		fmt.Printf("  Issues to repair: %d\n\n", len(plan.Renames))
	}

	// Map of all renames for text replacement
	issueByID := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		issueByID[issue.ID] = issue
	}

	// Pattern to match any issue ID reference in text (both hash and sequential IDs)
	oldPrefixPattern := regexp.MustCompile(`\b[a-z][a-z0-9-]*-[a-z0-9]+\b`)
	gates := beadGatesByTarget(issues)

	// Rename each issue, in plan order: families by prefix, roots and members
	// in natural id order, so parents move before their dotted children.
	for _, rename := range plan.Renames {
		oldID := rename.From
		newID := rename.To

		// Apply text replacements in all issue fields
		issue := issueByID[oldID]

		// Replace root tokens through the root map: the matched token is a
		// root id, its replacement carries any dotted tail along. Roots here
		// include the virtual roots of orphan children (families whose root
		// row is absent), so references to orphan dotted ids rewrite too.
		// Unknown tokens are left alone.
		replaceFunc := func(match string) string {
			if newRoot, ok := plan.RootMap[match]; ok {
				return newRoot
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
		if err := renameIssueKeepingBeadGates(ctx, st, issue, newID, gates[oldID], actorName); err != nil {
			return fmt.Errorf("failed to update issue %s -> %s: %w", oldID, newID, err)
		}

		if !jsonOutput {
			fmt.Printf("  Renamed %s -> %s\n", ui.RenderWarn(oldID), ui.RenderAccent(newID))
		}
	}

	// Set the new prefix in config — after the renames, and even when the
	// plan turned out empty (every row was already on the target prefix).
	if err := st.SetConfig(ctx, "issue_prefix", targetPrefix); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

	if jsonOutput {
		return printRepairJSON(plan, prefixes, false)
	}

	printRepairCollisions(plan)

	fmt.Printf("\n%s Successfully consolidated %d prefixes into %s\n",
		ui.RenderPass("✓"), len(prefixes), ui.RenderAccent(targetPrefix))
	fmt.Printf("  %d issues repaired, %d issues unchanged\n", len(plan.Renames), plan.Unchanged)

	return nil
}

// repairJSONResult is the single object --json repair output: the legacy
// summary fields, plus dry_run, the complete rename list and every collision
// fallback.
type repairJSONResult struct {
	TargetPrefix    string            `json:"target_prefix"`
	PrefixesFound   int               `json:"prefixes_found"`
	IssuesRepaired  int               `json:"issues_repaired"`
	IssuesUnchanged int               `json:"issues_unchanged"`
	DryRun          bool              `json:"dry_run"`
	Renames         []repairRename    `json:"renames"`
	Collisions      []repairCollision `json:"collisions"`
}

// printRepairJSON emits the repair result as exactly one JSON object, for a
// dry run and an execution alike — a dry run previews the object an
// execution would report. Empty lists are [] rather than null.
func printRepairJSON(plan *prefixRepairPlan, prefixes map[string]int, dryRun bool) error {
	result := repairJSONResult{
		TargetPrefix:    plan.TargetPrefix,
		PrefixesFound:   len(prefixes),
		IssuesRepaired:  len(plan.Renames),
		IssuesUnchanged: plan.Unchanged,
		DryRun:          dryRun,
		Renames:         plan.Renames,
		Collisions:      plan.Collisions,
	}
	if result.Renames == nil {
		result.Renames = []repairRename{}
	}
	if result.Collisions == nil {
		result.Collisions = []repairCollision{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(&result)
}

// knownIssuePrefixes collects the prefixes repair extraction should honor
// before falling back to its heuristic: the configured issue_prefix, every
// allowed_prefixes entry, and the prefixes detectPrefixes already found on
// the rows. Config read errors count as unset — the detected list still
// applies.
func knownIssuePrefixes(ctx context.Context, st storage.DoltStorage, prefixes map[string]int) []string {
	known := make([]string, 0, len(prefixes)+2)
	if v, err := st.GetConfig(ctx, "issue_prefix"); err == nil && v != "" {
		known = append(known, v)
	}
	if v, err := st.GetConfig(ctx, "allowed_prefixes"); err == nil && v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				known = append(known, p)
			}
		}
	}
	for p := range prefixes {
		known = append(known, p)
	}
	return known
}

// repairRetiredLookup adapts the store's optional retirement seam (the same
// one import's preview reads; see importRetiredLookup) into the planner's
// batched probe. The CLI's store is decorated (hooks, telemetry); the seam
// lives on the backend underneath. A store without it — the test fakes —
// plans without retirement checks, the way import's preview does.
func repairRetiredLookup(ctx context.Context, st storage.DoltStorage) repairRetiredSet {
	lookup, ok := st.(importRetiredLookup)
	if !ok {
		lookup, ok = storage.UnwrapStore(st).(importRetiredLookup)
	}
	if !ok {
		return nil
	}
	return func(ids []string) (map[string]bool, error) {
		retired, err := lookup.RetiredIssueIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool, len(retired))
		for id := range retired {
			set[id] = true
		}
		return set, nil
	}
}

// printRepairCollisions reports every fallback family; only the rename sample
// above is capped, never the collision list.
func printRepairCollisions(plan *prefixRepairPlan) {
	if len(plan.Collisions) == 0 {
		return
	}
	fmt.Printf("\nCollisions (%d families re-rooted to generated ids):\n", len(plan.Collisions))
	for _, c := range plan.Collisions {
		conflicting := ""
		if c.Conflicting != "" {
			conflicting = fmt.Sprintf(" (held by %s)", ui.RenderWarn(c.Conflicting))
		}
		fmt.Printf("  %s -> %s unavailable [%s]%s; family of %d re-rooted to %s\n",
			ui.RenderWarn(c.OldRoot), ui.RenderAccent(c.WantedRoot), c.Reason, conflicting, len(c.IDs), ui.RenderAccent(c.AssignedRoot))
	}
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

	gates := beadGatesByTarget(issues)
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

		if err := renameIssueKeepingBeadGates(ctx, store, issue, newID, gates[oldID], currentActor()); err != nil {
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
