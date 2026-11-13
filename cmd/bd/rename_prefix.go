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
	"time"

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

// prefixRepairRename is one planned ID change of a --repair run.
type prefixRepairRename struct {
	OldID string `json:"old_id"`
	NewID string `json:"new_id"`
}

// prefixRepairCollision records a source family whose suffix-preserving
// destination was taken, and the hash root the whole family moved to instead.
type prefixRepairCollision struct {
	OldRoot      string   `json:"old_root"`
	WantedRoot   string   `json:"wanted_root"`
	AssignedRoot string   `json:"assigned_root"`
	Reason       string   `json:"reason"`
	ConflictID   string   `json:"conflict_id,omitempty"`
	IDs          []string `json:"ids"`
}

// Collision reasons reported by a --repair plan.
const (
	repairCollisionExists  = "exists"  // a live target ID already holds the wanted root namespace
	repairCollisionClaimed = "claimed" // an earlier source family claimed the wanted root
	repairCollisionRetired = "retired" // a retaining sweep retired the wanted root or a member destination
)

// repairRetiredIDs reports which of ids a retaining sweep retired; storage
// refuses to rename a live row onto such an ID.
type repairRetiredIDs func(ctx context.Context, ids []string) (map[string]time.Time, error)

// repairRetiredLookup returns st's retired-ID probe, looking under the CLI's
// decorators the way the import dry run does (excludeRetiredFromImportPlan),
// or nil when the store has none (test fakes).
func repairRetiredLookup(st storage.DoltStorage) repairRetiredIDs {
	lookup, ok := st.(importRetiredLookup)
	if !ok {
		lookup, ok = storage.UnwrapStore(st).(importRetiredLookup)
	}
	if !ok {
		return nil
	}
	return lookup.RetiredIssueIDs
}

// firstRetired returns the first of ids (in order) that retired holds.
func firstRetired(ids []string, retired map[string]time.Time) string {
	for _, id := range ids {
		if _, ok := retired[id]; ok {
			return id
		}
	}
	return ""
}

// prefixRepairPlan is the complete, deterministic outcome of a --repair run,
// computed before anything is written so a dry run and the real run agree.
type prefixRepairPlan struct {
	Renames    []prefixRepairRename
	Collisions []prefixRepairCollision
	Unchanged  int
	// rootMap maps every old root (the ID before its first dot, including
	// virtual roots of orphan children) to the root it was assigned.
	rootMap map[string]string
}

// repairFamily is one old root and every source row under it: the root row
// itself (when present) and its dotted descendants.
type repairFamily struct {
	prefix     string
	oldRoot    string
	rootSuffix string
	members    []*types.Issue
}

// issueIDRoot returns id up to its first dot: the root of a dotted family.
func issueIDRoot(id string) string {
	if i := strings.Index(id, "."); i >= 0 {
		return id[:i]
	}
	return id
}

// planPrefixRepair plans the consolidation of every ID onto targetPrefix.
//
// IDs already under targetPrefix+"-" are left alone. Every other ID keeps its
// suffix: its source prefix is the longest of knownPrefixes it starts with
// (falling back to the heuristic), so cre-db-c2a becomes kb-c2a and
// taxdb-5su1.13 becomes kb-5su1.13. Rows are grouped by old root, orphan
// children included, and a whole family moves together: when its wanted root
// is already held by a live target ID or claimed by an earlier source family,
// the family is re-rooted to a hash root with its dotted paths intact, and
// the collision is recorded. The same happens when retired (nil skips the
// probe) reports the wanted root or a member destination as retired; hash
// roots are checked the same way. A lookup error fails the plan.
func planPrefixRepair(ctx context.Context, targetPrefix string, issues []*types.Issue, knownPrefixes []string, actorName string, retired repairRetiredIDs) (*prefixRepairPlan, error) {
	plan := &prefixRepairPlan{rootMap: make(map[string]string)}

	// occupied maps a live target root namespace (child-only ones included)
	// to the smallest live ID holding it.
	occupied := make(map[string]string)
	families := make(map[string]*repairFamily)
	for _, issue := range issues {
		id := issue.ID
		if strings.HasPrefix(id, targetPrefix+"-") {
			plan.Unchanged++
			root := issueIDRoot(id)
			if cur, ok := occupied[root]; !ok || utils.NaturalCompareIDs(id, cur) < 0 {
				occupied[root] = id
			}
			continue
		}
		oldRoot := issueIDRoot(id)
		fam, ok := families[oldRoot]
		if !ok {
			prefix := utils.ExtractIssuePrefixKnown(oldRoot, knownPrefixes)
			suffix := oldRoot
			if prefix != "" && strings.HasPrefix(oldRoot, prefix+"-") && len(oldRoot) > len(prefix)+1 {
				suffix = oldRoot[len(prefix)+1:]
			}
			fam = &repairFamily{prefix: prefix, oldRoot: oldRoot, rootSuffix: suffix}
			families[oldRoot] = fam
		}
		fam.members = append(fam.members, issue)
	}

	ordered := make([]*repairFamily, 0, len(families))
	for _, fam := range families {
		slices.SortFunc(fam.members, func(a, b *types.Issue) int {
			return cmp.Or(utils.NaturalCompareIDs(a.ID, b.ID), cmp.Compare(a.ID, b.ID))
		})
		ordered = append(ordered, fam)
	}
	// Families sort by source prefix, then by root number as the repair always
	// has (so its rename order is unchanged), then naturally by root.
	slices.SortFunc(ordered, func(a, b *repairFamily) int {
		return cmp.Or(
			cmp.Compare(a.prefix, b.prefix),
			cmp.Compare(utils.ExtractIssueNumber(a.oldRoot), utils.ExtractIssueNumber(b.oldRoot)),
			utils.NaturalCompareIDs(a.oldRoot, b.oldRoot),
			cmp.Compare(a.oldRoot, b.oldRoot),
		)
	})

	// destinations lists the IDs fam would hold under root: the root itself
	// and every member's path.
	destinations := func(fam *repairFamily, root string) []string {
		ids := []string{root}
		for _, m := range fam.members {
			if id := root + strings.TrimPrefix(m.ID, fam.oldRoot); id != root {
				ids = append(ids, id)
			}
		}
		return ids
	}
	probeRetired := func(ids []string) (map[string]time.Time, error) {
		if retired == nil || len(ids) == 0 {
			return nil, nil
		}
		got, err := retired(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("read retired ids: %w", err)
		}
		return got, nil
	}
	var wantedIDs []string
	for _, fam := range ordered {
		wantedIDs = append(wantedIDs, destinations(fam, targetPrefix+"-"+fam.rootSuffix)...)
	}
	retiredWanted, err := probeRetired(wantedIDs)
	if err != nil {
		return nil, err
	}

	// Pass 1: every family that can keep its suffix claims its wanted root, so
	// natural destinations are reserved before any fallback is allocated.
	claimed := make(map[string]string) // root -> old root that claimed it
	var conflicted []*prefixRepairCollision
	conflictOf := make(map[string]*prefixRepairCollision)
	for _, fam := range ordered {
		wanted := targetPrefix + "-" + fam.rootSuffix
		var c *prefixRepairCollision
		if live, ok := occupied[wanted]; ok {
			c = &prefixRepairCollision{Reason: repairCollisionExists, ConflictID: live}
		} else if owner, ok := claimed[wanted]; ok {
			c = &prefixRepairCollision{Reason: repairCollisionClaimed, ConflictID: owner}
		} else if id := firstRetired(destinations(fam, wanted), retiredWanted); id != "" {
			c = &prefixRepairCollision{Reason: repairCollisionRetired, ConflictID: id}
		}
		if c == nil {
			claimed[wanted] = fam.oldRoot
			plan.rootMap[fam.oldRoot] = wanted
			continue
		}
		c.OldRoot, c.WantedRoot = fam.oldRoot, wanted
		conflicted = append(conflicted, c)
		conflictOf[fam.oldRoot] = c
	}

	// Pass 2: each conflicting family moves, whole, to a fresh hash root.
	rejected := make(map[string]bool)
	for _, fam := range ordered {
		c, ok := conflictOf[fam.oldRoot]
		if !ok {
			continue
		}
		seed := fam.members[0] // the root row, or the first orphan member
		assigned := ""
		for nonce := 0; nonce < repairHashAttempts; nonce++ {
			cand := repairHashRoot(targetPrefix, seed, actorName, nonce)
			if _, live := occupied[cand]; live || rejected[cand] {
				rejected[cand] = true
				continue
			}
			if _, taken := claimed[cand]; taken {
				rejected[cand] = true
				continue
			}
			got, err := probeRetired(destinations(fam, cand))
			if err != nil {
				return nil, err
			}
			if len(got) > 0 {
				rejected[cand] = true
				continue
			}
			assigned = cand
			break
		}
		if assigned == "" {
			return nil, fmt.Errorf("failed to allocate a unique root for %s after %d attempts", fam.oldRoot, repairHashAttempts)
		}
		claimed[assigned] = fam.oldRoot
		plan.rootMap[fam.oldRoot] = assigned
		c.AssignedRoot = assigned
	}

	for _, fam := range ordered {
		newRoot := plan.rootMap[fam.oldRoot]
		var ids []string
		for _, m := range fam.members {
			plan.Renames = append(plan.Renames, prefixRepairRename{OldID: m.ID, NewID: newRoot + strings.TrimPrefix(m.ID, fam.oldRoot)})
			ids = append(ids, m.ID)
		}
		if c, ok := conflictOf[fam.oldRoot]; ok {
			c.IDs = ids
		}
	}
	for _, c := range conflicted {
		plan.Collisions = append(plan.Collisions, *c)
	}
	return plan, nil
}

// repairPrefixes consolidates multiple prefixes into a single target prefix.
// Issues already under the target prefix are left unchanged. Every other
// issue keeps its suffix (see planPrefixRepair); a family whose destination
// is taken moves to a hash root and is reported.
func repairPrefixes(ctx context.Context, st storage.DoltStorage, actorName string, targetPrefix string, issues []*types.Issue, prefixes map[string]int, dryRun bool) error {
	known := make([]string, 0, len(prefixes))
	for p := range prefixes {
		known = append(known, p)
	}
	plan, err := planPrefixRepair(ctx, targetPrefix, issues, known, actorName, repairRetiredLookup(st))
	if err != nil {
		return err
	}
	byID := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}

	if dryRun {
		if jsonOutput {
			return writeRepairJSON(targetPrefix, len(prefixes), plan, true)
		}
		fmt.Printf("DRY RUN: Would repair %d issues with incorrect prefixes\n\n", len(plan.Renames))
		fmt.Printf("Issues with correct prefix (%s): %d\n", ui.RenderAccent(targetPrefix), plan.Unchanged)
		fmt.Printf("Issues to repair: %d\n\n", len(plan.Renames))

		fmt.Printf("Planned renames (showing first 10):\n")
		for i, r := range plan.Renames {
			if i >= 10 {
				fmt.Printf("... and %d more\n", len(plan.Renames)-10)
				break
			}
			fmt.Printf("  %s -> %s\n", ui.RenderWarn(r.OldID), ui.RenderAccent(r.NewID))
		}
		printRepairCollisions(plan.Collisions)
		return nil
	}

	// Perform the repairs. JSON output is the single result object at the
	// end, so text progress is suppressed under --json.
	if !jsonOutput {
		fmt.Printf("Repairing database with multiple prefixes...\n")
		fmt.Printf("  Issues with correct prefix (%s): %d\n", ui.RenderAccent(targetPrefix), plan.Unchanged)
		fmt.Printf("  Issues to repair: %d\n\n", len(plan.Renames))
	}

	// Pattern to match any issue ID reference in text (both hash and
	// sequential IDs). It stops at a dot, so a reference to a dotted child
	// matches its root and keeps its path when the root is mapped.
	idPattern := regexp.MustCompile(`\b[a-z][a-z0-9-]*-[a-z0-9]+\b`)
	replaceFunc := func(match string) string {
		if newRoot, ok := plan.rootMap[match]; ok {
			return newRoot
		}
		return match
	}
	gates := beadGatesByTarget(issues)

	for _, r := range plan.Renames {
		issue := byID[r.OldID]

		issue.Title = idPattern.ReplaceAllStringFunc(issue.Title, replaceFunc)
		issue.Description = idPattern.ReplaceAllStringFunc(issue.Description, replaceFunc)
		if issue.Design != "" {
			issue.Design = idPattern.ReplaceAllStringFunc(issue.Design, replaceFunc)
		}
		if issue.AcceptanceCriteria != "" {
			issue.AcceptanceCriteria = idPattern.ReplaceAllStringFunc(issue.AcceptanceCriteria, replaceFunc)
		}
		if issue.Notes != "" {
			issue.Notes = idPattern.ReplaceAllStringFunc(issue.Notes, replaceFunc)
		}

		if err := renameIssueKeepingBeadGates(ctx, st, issue, r.NewID, gates[r.OldID], actorName); err != nil {
			return fmt.Errorf("failed to update issue %s -> %s: %w", r.OldID, r.NewID, err)
		}

		if !jsonOutput {
			fmt.Printf("  Renamed %s -> %s\n", ui.RenderWarn(r.OldID), ui.RenderAccent(r.NewID))
		}
	}

	if err := repointRepairedDependencies(ctx, st, plan, issues, actorName); err != nil {
		return err
	}

	// Set the new prefix in config
	if err := st.SetConfig(ctx, "issue_prefix", targetPrefix); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

	if jsonOutput {
		return writeRepairJSON(targetPrefix, len(prefixes), plan, false)
	}
	printRepairCollisions(plan.Collisions)
	fmt.Printf("\n%s Successfully consolidated %d prefixes into %s\n",
		ui.RenderPass("✓"), len(prefixes), ui.RenderAccent(targetPrefix))
	fmt.Printf("  %d issues repaired, %d issues unchanged\n", len(plan.Renames), plan.Unchanged)
	return nil
}

// repointRepairedDependencies points each edge that still names a renamed
// issue's old ID at its new one. Storage keeps an edge between two different
// prefixes as a reference into another repository (issueops.
// IsExternalDepTarget), so renaming its target never re-points it; once the
// families share the target prefix the edge is re-added as an ordinary one.
// issues must hold the post-rename IDs.
func repointRepairedDependencies(ctx context.Context, st storage.DoltStorage, plan *prefixRepairPlan, issues []*types.Issue, actorName string) error {
	if len(plan.Renames) == 0 {
		return nil
	}
	newID := make(map[string]string, len(plan.Renames))
	for _, r := range plan.Renames {
		newID[r.OldID] = r.NewID
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	slices.Sort(ids)
	records, err := st.GetDependencyRecordsForIssues(ctx, ids)
	if err != nil {
		return fmt.Errorf("failed to read dependencies: %w", err)
	}
	for _, source := range ids {
		for _, dep := range records[source] {
			target, ok := newID[dep.DependsOnID]
			if !ok {
				continue
			}
			if err := st.RemoveDependency(ctx, source, dep.DependsOnID, actorName); err != nil {
				return fmt.Errorf("failed to re-point dependency %s -> %s: %w", source, dep.DependsOnID, err)
			}
			moved := *dep
			moved.IssueID, moved.DependsOnID = source, target
			if err := st.AddDependency(ctx, &moved, actorName); err != nil {
				return fmt.Errorf("failed to re-point dependency %s -> %s at %s: %w", source, dep.DependsOnID, target, err)
			}
		}
	}
	return nil
}

// writeRepairJSON writes a --repair result as one JSON object: the complete
// rename mapping and every collision, for a dry run and a real run alike.
func writeRepairJSON(targetPrefix string, prefixesFound int, plan *prefixRepairPlan, dryRun bool) error {
	renames := plan.Renames
	if renames == nil {
		renames = []prefixRepairRename{}
	}
	collisions := plan.Collisions
	if collisions == nil {
		collisions = []prefixRepairCollision{}
	}
	result := map[string]interface{}{
		"target_prefix":    targetPrefix,
		"prefixes_found":   prefixesFound,
		"issues_repaired":  len(plan.Renames),
		"issues_unchanged": plan.Unchanged,
		"dry_run":          dryRun,
		"renames":          renames,
		"collisions":       collisions,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// printRepairCollisions lists every family that could not keep its suffix.
func printRepairCollisions(collisions []prefixRepairCollision) {
	if len(collisions) == 0 {
		return
	}
	fmt.Printf("\n%s %d families could not keep their suffix and moved to a hash root:\n", ui.RenderWarn("⚠"), len(collisions))
	for _, c := range collisions {
		conflict := c.Reason
		if c.ConflictID != "" {
			conflict += ": " + c.ConflictID
		}
		fmt.Printf("  %s (wanted %s, %s) -> %s  [%d ids]\n",
			ui.RenderWarn(c.OldRoot), c.WantedRoot, conflict, ui.RenderAccent(c.AssignedRoot), len(c.IDs))
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

// repairHashAttempts bounds the hash roots tried for one conflicting family.
const repairHashAttempts = 100

// repairHashRoot derives the nonce-th candidate hash root for a family from
// its seed row's content (same approach as generateHashIDForIssue).
func repairHashRoot(prefix string, seed *types.Issue, actor string, nonce int) string {
	content := fmt.Sprintf("%s|%s|%s|%d|%d",
		seed.Title,
		seed.Description,
		actor,
		seed.CreatedAt.UnixNano(),
		nonce,
	)
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(h[:4])) // 4 bytes = 8 hex chars
}

func init() {
	renamePrefixCmd.Flags().Bool("dry-run", false, "Preview changes without applying them")
	renamePrefixCmd.Flags().Bool("repair", false, "Repair database with multiple prefixes by consolidating them")
	rootCmd.AddCommand(renamePrefixCmd)
}
