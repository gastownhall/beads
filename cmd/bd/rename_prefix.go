package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	publicops "github.com/steveyegge/beads/issueops"
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
preserving issues that already have the correct prefix. Each renamed issue keeps
its suffix (taxdb-u6ch -> mtg-u6ch, cre-db-c2a.3 -> mtg-c2a.3); source IDs are
split by issue_prefix, allowed_prefixes and the prefixes found on the rows. When
a family's root is already live, claimed by another family or retired, the whole
family moves to one new hash root (mtg-<8 hex>) and the move is reported.
Text references are rewritten only inside the renamed issues. Dependencies
follow the renames. An edge between two prefixes is re-pointed by removing
and re-adding it, so it records the repair's actor and time; one that storage
refuses to re-add is left as it was and reported.

Changing the prefix for new issues only:
--mint-only makes new issues use the new prefix and leaves every existing ID
and text reference as it is. The old prefix is added to allowed_prefixes, so
explicit --id values and children of existing issues keep working. A
dependency between an old-prefix and a new-prefix issue does not block: it is
stored as a reference to another repository, which bd ready ignores. A later
--repair consolidates the prefixes and re-points those dependencies.

EXAMPLES:
  bd rename-prefix kw-                # Rename from 'knowledge-work-' to 'kw-'
  bd rename-prefix mtg- --repair      # Consolidate multiple prefixes into 'mtg-'
  bd rename-prefix team- --dry-run    # Preview changes without applying
  bd rename-prefix kb- --mint-only    # New issues get 'kb-'; existing IDs stay

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
		mintOnly, _ := cmd.Flags().GetBool("mint-only")
		if mintOnly && repair {
			return HandleErrorRespectJSON("--mint-only and --repair cannot be used together: --mint-only renames no issues")
		}

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

		if mintOnly {
			return runMintOnlyPrefix(ctx, store, strings.TrimRight(oldPrefix, "-"), newPrefix, dryRun)
		}

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

		// Only rows already on newPrefix make this a no-op. After --mint-only
		// the config names newPrefix while every row may still carry the old
		// prefix, and renaming those rows is what the switch has left to do.
		if len(prefixes) == 1 && oldPrefix == newPrefix && detected == newPrefix {
			return HandleError("new prefix is the same as current prefix: %s", oldPrefix)
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

// mintOnlyResult is the single object bd rename-prefix --mint-only --json
// writes.
type mintOnlyResult struct {
	OldPrefix            string   `json:"old_prefix"`
	NewPrefix            string   `json:"new_prefix"`
	MintOnly             bool     `json:"mint_only"`
	DryRun               bool     `json:"dry_run"`
	AllowedPrefixesAdded []string `json:"allowed_prefixes_added"`
}

// mintOnlyPrefix points new issues at newPrefix without touching any ID or
// text. The old prefix stays valid through allowed_prefixes, so explicit
// --id values and children of existing issues keep working. A dry run
// reports the same changes and writes nothing.
func mintOnlyPrefix(ctx context.Context, st storage.DoltStorage, oldPrefix, newPrefix string, dryRun bool) (*mintOnlyResult, error) {
	result := &mintOnlyResult{OldPrefix: oldPrefix, NewPrefix: newPrefix, MintOnly: true, DryRun: dryRun, AllowedPrefixesAdded: []string{}}
	allowed, err := st.GetConfig(ctx, "allowed_prefixes")
	if err != nil {
		return nil, fmt.Errorf("failed to read allowed_prefixes: %w", err)
	}
	present := false
	for _, p := range strings.Split(allowed, ",") {
		if strings.TrimRight(strings.TrimSpace(p), "-") == oldPrefix {
			present = true
		}
	}
	if !present {
		result.AllowedPrefixesAdded = append(result.AllowedPrefixesAdded, oldPrefix)
	}
	if dryRun {
		return result, nil
	}
	if !present {
		updated := oldPrefix
		if trimmed := strings.TrimSpace(allowed); trimmed != "" {
			updated = trimmed + "," + oldPrefix
		}
		if err := st.SetConfig(ctx, "allowed_prefixes", updated); err != nil {
			return nil, fmt.Errorf("failed to update allowed_prefixes: %w", err)
		}
	}
	if err := st.SetConfig(ctx, "issue_prefix", newPrefix); err != nil {
		return nil, fmt.Errorf("failed to update prefix: %w", err)
	}
	return result, nil
}

// runMintOnlyPrefix is bd rename-prefix --mint-only.
func runMintOnlyPrefix(ctx context.Context, st storage.DoltStorage, oldPrefix, newPrefix string, dryRun bool) error {
	if oldPrefix == newPrefix {
		return HandleErrorRespectJSON("new prefix is the same as current prefix: %s", oldPrefix)
	}
	result, err := mintOnlyPrefix(ctx, st, oldPrefix, newPrefix, dryRun)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if !dryRun {
		commandDidWrite.Store(true)
	}
	// config.yaml's issue-prefix wins over the database for bd create.
	if yamlPrefix := strings.TrimRight(overlayYAMLPrefix(""), "-"); yamlPrefix != "" && yamlPrefix != newPrefix {
		fmt.Fprintf(os.Stderr, "%s config.yaml sets issue-prefix: %s, which bd create uses before the database; set it to %s so new issues mint %s-*\n",
			ui.RenderWarn("⚠"), yamlPrefix, newPrefix, newPrefix)
	}
	// Storage classifies an edge by the part of each ID before its first
	// hyphen and ignores allowed_prefixes, so an edge between the two
	// prefixes lands in the external column, which blocked state never reads.
	if issueops.IsExternalDepTarget(oldPrefix+"-1", newPrefix+"-1") {
		fmt.Fprintf(os.Stderr, "%s A dependency between a %s-* and a %s-* issue does not block: it is stored as a reference to another repository, which bd ready ignores. bd rename-prefix %s --repair consolidates the prefixes and re-points such dependencies.\n",
			ui.RenderWarn("⚠"), oldPrefix, newPrefix, newPrefix)
	}
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	if dryRun {
		fmt.Printf("DRY RUN: would set issue_prefix '%s' -> '%s'; no issue IDs or text would change\n", oldPrefix, newPrefix)
		if len(result.AllowedPrefixesAdded) > 0 {
			fmt.Printf("  Would add %s to allowed_prefixes\n", ui.RenderAccent(oldPrefix))
		}
		return nil
	}
	fmt.Printf("%s New issues now mint %s-*; existing IDs and text are unchanged\n", ui.RenderPass("✓"), ui.RenderAccent(newPrefix))
	if len(result.AllowedPrefixesAdded) > 0 {
		fmt.Printf("  Added %s to allowed_prefixes so existing %s-* IDs stay valid\n", ui.RenderAccent(oldPrefix), oldPrefix)
	}
	return nil
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
	occupied, ordered, unchanged := groupRepairFamilies(targetPrefix, issues, knownPrefixes)
	plan.Unchanged = unchanged
	alloc := &repairRootAllocator{
		targetPrefix: targetPrefix,
		actorName:    actorName,
		retired:      retired,
		occupied:     occupied,
		claimed:      make(map[string]string),
		rejected:     make(map[string]bool),
	}

	// Pass 1: every family that can keep its suffix claims its wanted root, so
	// natural destinations are reserved before any fallback is allocated.
	conflicted, conflictOf, err := alloc.claimWantedRoots(ctx, plan, ordered)
	if err != nil {
		return nil, err
	}

	// Pass 2: each conflicting family moves, whole, to a fresh hash root.
	for _, fam := range ordered {
		c, ok := conflictOf[fam.oldRoot]
		if !ok {
			continue
		}
		assigned, err := alloc.hashRoot(ctx, fam)
		if err != nil {
			return nil, err
		}
		alloc.claimed[assigned] = fam.oldRoot
		plan.rootMap[fam.oldRoot] = assigned
		c.AssignedRoot = assigned
	}

	for _, fam := range ordered {
		ids := appendFamilyRenames(plan, fam)
		if c, ok := conflictOf[fam.oldRoot]; ok {
			c.IDs = ids
		}
	}
	for _, c := range conflicted {
		plan.Collisions = append(plan.Collisions, *c)
	}
	return plan, nil
}

// groupRepairFamilies splits issues into the live target namespace and the
// source families. occupied maps each live target root namespace (child-only
// ones included) to the smallest live ID holding it, and unchanged counts the
// IDs already under targetPrefix. ordered holds every source family, grouped
// by old root with orphan children included, its members sorted by ID, in the
// order the repair renames them.
func groupRepairFamilies(targetPrefix string, issues []*types.Issue, knownPrefixes []string) (occupied map[string]string, ordered []*repairFamily, unchanged int) {
	occupied = make(map[string]string)
	families := make(map[string]*repairFamily)
	for _, issue := range issues {
		id := issue.ID
		root := issueIDRoot(id)
		if strings.HasPrefix(id, targetPrefix+"-") {
			unchanged++
			if cur, ok := occupied[root]; !ok || utils.NaturalCompareIDs(id, cur) < 0 {
				occupied[root] = id
			}
			continue
		}
		fam, ok := families[root]
		if !ok {
			fam = newRepairFamily(root, knownPrefixes)
			families[root] = fam
		}
		fam.members = append(fam.members, issue)
	}

	ordered = make([]*repairFamily, 0, len(families))
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
	return occupied, ordered, unchanged
}

// newRepairFamily starts the family of oldRoot. Its source prefix is the
// longest of knownPrefixes the root starts with (falling back to the
// heuristic), and the rest of the root is the suffix the family keeps.
func newRepairFamily(oldRoot string, knownPrefixes []string) *repairFamily {
	prefix := utils.ExtractIssuePrefixKnown(oldRoot, knownPrefixes)
	suffix := oldRoot
	if prefix != "" && strings.HasPrefix(oldRoot, prefix+"-") && len(oldRoot) > len(prefix)+1 {
		suffix = oldRoot[len(prefix)+1:]
	}
	return &repairFamily{prefix: prefix, oldRoot: oldRoot, rootSuffix: suffix}
}

// destinations lists the IDs fam would hold under root: the root itself and
// every member's path.
func (fam *repairFamily) destinations(root string) []string {
	ids := []string{root}
	for _, m := range fam.members {
		if id := root + strings.TrimPrefix(m.ID, fam.oldRoot); id != root {
			ids = append(ids, id)
		}
	}
	return ids
}

// appendFamilyRenames records the rename of every member of fam onto the
// root plan assigned it, and returns the members' old IDs.
func appendFamilyRenames(plan *prefixRepairPlan, fam *repairFamily) []string {
	newRoot := plan.rootMap[fam.oldRoot]
	var ids []string
	for _, m := range fam.members {
		plan.Renames = append(plan.Renames, prefixRepairRename{OldID: m.ID, NewID: newRoot + strings.TrimPrefix(m.ID, fam.oldRoot)})
		ids = append(ids, m.ID)
	}
	return ids
}

// repairRootAllocator assigns the source families their new roots.
type repairRootAllocator struct {
	targetPrefix string
	actorName    string
	retired      repairRetiredIDs  // nil skips the retired probe
	occupied     map[string]string // live target root namespace -> smallest live ID
	claimed      map[string]string // root -> old root that claimed it
	rejected     map[string]bool   // hash roots found live, claimed or retired
}

// probeRetired reports which of ids a retaining sweep retired.
func (a *repairRootAllocator) probeRetired(ctx context.Context, ids []string) (map[string]time.Time, error) {
	if a.retired == nil || len(ids) == 0 {
		return nil, nil
	}
	got, err := a.retired(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read retired ids: %w", err)
	}
	return got, nil
}

// claimWantedRoots gives every family whose wanted root is free that root,
// in family order. It returns the collision of each family that cannot keep
// its suffix, in family order and by old root.
func (a *repairRootAllocator) claimWantedRoots(ctx context.Context, plan *prefixRepairPlan, ordered []*repairFamily) ([]*prefixRepairCollision, map[string]*prefixRepairCollision, error) {
	var wantedIDs []string
	for _, fam := range ordered {
		wantedIDs = append(wantedIDs, fam.destinations(a.targetPrefix+"-"+fam.rootSuffix)...)
	}
	retiredWanted, err := a.probeRetired(ctx, wantedIDs)
	if err != nil {
		return nil, nil, err
	}

	var conflicted []*prefixRepairCollision
	conflictOf := make(map[string]*prefixRepairCollision)
	for _, fam := range ordered {
		wanted := a.targetPrefix + "-" + fam.rootSuffix
		c := a.wantedRootConflict(fam, wanted, retiredWanted)
		if c == nil {
			a.claimed[wanted] = fam.oldRoot
			plan.rootMap[fam.oldRoot] = wanted
			continue
		}
		c.OldRoot, c.WantedRoot = fam.oldRoot, wanted
		conflicted = append(conflicted, c)
		conflictOf[fam.oldRoot] = c
	}
	return conflicted, conflictOf, nil
}

// wantedRootConflict reports why fam cannot take wanted: a live target ID
// holds it, an earlier family claimed it, or it or a member destination
// under it is retired. It returns nil when wanted is free.
func (a *repairRootAllocator) wantedRootConflict(fam *repairFamily, wanted string, retired map[string]time.Time) *prefixRepairCollision {
	if live, ok := a.occupied[wanted]; ok {
		return &prefixRepairCollision{Reason: repairCollisionExists, ConflictID: live}
	}
	if owner, ok := a.claimed[wanted]; ok {
		return &prefixRepairCollision{Reason: repairCollisionClaimed, ConflictID: owner}
	}
	if id := firstRetired(fam.destinations(wanted), retired); id != "" {
		return &prefixRepairCollision{Reason: repairCollisionRetired, ConflictID: id}
	}
	return nil
}

// hashRoot finds fam a fresh hash root: the first candidate that no live ID
// holds, no family claimed and under which no member destination is retired.
func (a *repairRootAllocator) hashRoot(ctx context.Context, fam *repairFamily) (string, error) {
	seed := fam.members[0] // the root row, or the first orphan member
	for nonce := 0; nonce < repairHashAttempts; nonce++ {
		cand := repairHashRoot(a.targetPrefix, seed, a.actorName, nonce)
		_, live := a.occupied[cand]
		_, taken := a.claimed[cand]
		if live || taken || a.rejected[cand] {
			a.rejected[cand] = true
			continue
		}
		got, err := a.probeRetired(ctx, fam.destinations(cand))
		if err != nil {
			return "", err
		}
		if len(got) > 0 {
			a.rejected[cand] = true
			continue
		}
		return cand, nil
	}
	return "", fmt.Errorf("failed to allocate a unique root for %s after %d attempts", fam.oldRoot, repairHashAttempts)
}

// repairKnownPrefixes lists the prefixes a repair splits source IDs by: the
// database's issue_prefix, every allowed_prefixes entry, and every prefix
// detected on the rows. Declaring a family's prefix in allowed_prefixes is how
// a user settles an ambiguous split (cre-db-wxyz is cre-db + wxyz).
func repairKnownPrefixes(ctx context.Context, st storage.DoltStorage, detected map[string]int) ([]string, error) {
	var known []string
	for _, key := range []string{"issue_prefix", "allowed_prefixes"} {
		value, err := st.GetConfig(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", key, err)
		}
		for _, p := range strings.Split(value, ",") {
			if p = strings.TrimRight(strings.TrimSpace(p), "-"); p != "" {
				known = append(known, p)
			}
		}
	}
	for p := range detected {
		known = append(known, p)
	}
	slices.Sort(known)
	return slices.Compact(known), nil
}

// repairPrefixes consolidates multiple prefixes into a single target prefix.
// Issues already under the target prefix are left unchanged. Every other
// issue keeps its suffix (see planPrefixRepair); a family whose destination
// is taken moves to a hash root and is reported.
func repairPrefixes(ctx context.Context, st storage.DoltStorage, actorName string, targetPrefix string, issues []*types.Issue, prefixes map[string]int, dryRun bool) error {
	known, err := repairKnownPrefixes(ctx, st, prefixes)
	if err != nil {
		return err
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
		return reportRepairDryRun(targetPrefix, len(prefixes), plan)
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
	rewrite := func(text string) string { return idPattern.ReplaceAllStringFunc(text, replaceFunc) }
	gates := beadGatesByTarget(issues)

	for _, r := range plan.Renames {
		issue := byID[r.OldID]
		rewriteRepairText(issue, rewrite)

		if err := renameIssueKeepingBeadGates(ctx, st, issue, r.NewID, gates[r.OldID], actorName); err != nil {
			return fmt.Errorf("failed to update issue %s -> %s: %w", r.OldID, r.NewID, err)
		}

		if !jsonOutput {
			fmt.Printf("  Renamed %s -> %s\n", ui.RenderWarn(r.OldID), ui.RenderAccent(r.NewID))
		}
	}

	refused, err := repointRepairedDependencies(ctx, st, plan, issues, actorName)
	if err != nil {
		return err
	}

	// Set the new prefix in config
	if err := st.SetConfig(ctx, "issue_prefix", targetPrefix); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

	if jsonOutput {
		return writeRepairJSON(targetPrefix, len(prefixes), plan, refused, false)
	}
	printRepairCollisions(plan.Collisions)
	printUnrepointedEdges(refused)
	fmt.Printf("\n%s Successfully consolidated %d prefixes into %s\n",
		ui.RenderPass("✓"), len(prefixes), ui.RenderAccent(targetPrefix))
	fmt.Printf("  %d issues repaired, %d issues unchanged\n", len(plan.Renames), plan.Unchanged)
	return nil
}

// reportRepairDryRun reports what a --repair run would do; it writes nothing.
func reportRepairDryRun(targetPrefix string, prefixesFound int, plan *prefixRepairPlan) error {
	if jsonOutput {
		return writeRepairJSON(targetPrefix, prefixesFound, plan, nil, true)
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

// rewriteRepairText applies rewrite to every text field of issue a repair
// rewrites references in.
func rewriteRepairText(issue *types.Issue, rewrite func(string) string) {
	issue.Title = rewrite(issue.Title)
	issue.Description = rewrite(issue.Description)
	if issue.Design != "" {
		issue.Design = rewrite(issue.Design)
	}
	if issue.AcceptanceCriteria != "" {
		issue.AcceptanceCriteria = rewrite(issue.AcceptanceCriteria)
	}
	if issue.Notes != "" {
		issue.Notes = rewrite(issue.Notes)
	}
}

// unrepointedEdge is a dependency edge a --repair run left as it was because
// storage refused to re-point it, or because it closes the cycle storage
// refused a parent-child re-point for (refuseIfItClosesARefusedCycle).
// Re-pointing re-adds the edge, and storage validates the re-add as it does a
// new edge, with hierarchy and cycle checks that an edge stored across
// prefixes, or one older than the check, never went through. The refused
// edge keeps its stored ID and column. That is not always the external
// column: a database older than the dependency-target split, or a bd rename,
// keeps edges between two prefixes in the issue column, where a refused
// blocks edge still blocks.
type unrepointedEdge struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	WantedID    string `json:"wanted_depends_on_id"`
	Type        string `json:"type"`
	Error       string `json:"error"`
}

// repointRepairedDependencies points each edge that still names a renamed
// issue's old ID at its new one. Storage keeps an edge between two different
// prefixes as a reference into another repository (issueops.
// IsExternalDepTarget), so renaming its target never re-points it; once the
// families share the target prefix the edge is re-added as an ordinary one.
// The same holds for an edge whose SOURCE was renamed onto the target prefix
// and whose target is an unrenamed target-prefix row (taxdb-x -> kb-hand
// becomes kb-x -> kb-hand): it stays in the external column, which blocked
// state never reads, until it is re-added. An edge that storage already
// re-pointed (both ends under one prefix before the repair, as in a renamed
// family) is left alone. issues must hold the post-rename IDs.
//
// The column an edge is stored in is not visible here, so an edge between
// two prefixes that already sits in the issue column (a database older than
// the dependency-target split, or a bd rename) is re-added too and takes the
// repair's actor and time.
//
// Parent-child edges move first, then every other edge, so storage checks
// each blocking re-add against the re-pointed hierarchy. Moved in source-ID
// order alone, a re-add was checked against a partly re-pointed graph whose
// ancestry and cycle walks stop at old IDs that no longer exist, and whether
// an edge was refused depended on how its IDs sorted. A parent-child edge can
// itself be refused for a cycle through a blocking edge that blocked state
// never reads, such as an epic's close gate on a child already on the target
// prefix, stored across prefixes. Such a blocking edge is left as it was
// (refuseIfItClosesARefusedCycle): re-pointed, it would start to block while
// the child stays detached.
//
// Each edge moves in its own transaction, so a re-add storage refuses rolls
// its removal back. The refused edge is returned instead of ending the run,
// and the caller still sets issue_prefix. Any other failure ends the run and
// names the edges refused before it.
func repointRepairedDependencies(ctx context.Context, st storage.DoltStorage, plan *prefixRepairPlan, issues []*types.Issue, actorName string) ([]unrepointedEdge, error) {
	moves, err := repairEdgeMoves(ctx, st, plan, issues)
	if err != nil {
		return nil, err
	}
	var refused []unrepointedEdge
	// The parent-child moves storage refused for a cycle.
	var cycleRefused []repairEdgeMove
	for _, m := range moves {
		err := refuseIfItClosesARefusedCycle(ctx, st, m, cycleRefused, actorName)
		if err == nil {
			err = repointDependency(ctx, st, m.source, m.dep, m.target, actorName)
		}
		if err == nil {
			continue
		}
		edge := unrepointedEdge{
			IssueID: m.source, DependsOnID: m.dep.DependsOnID, WantedID: m.target, Type: string(m.dep.Type), Error: err.Error(),
		}
		var closes *closesRefusedCycleError
		if !errors.As(err, &closes) && !isDependencyRefusal(err) {
			return nil, repointFailure(edge, err, refused)
		}
		refused = append(refused, edge)
		if m.dep.Type == types.DepParentChild && errors.Is(err, publicops.ErrDependencyCycle) {
			cycleRefused = append(cycleRefused, m)
		}
	}
	return refused, nil
}

// errRepairProbe ends every probe transaction refuseIfItClosesARefusedCycle
// runs, so the transaction rolls back.
var errRepairProbe = errors.New("rename-prefix --repair probe")

// closesRefusedCycleError is why a repair left a blocking edge as it was:
// without it, storage accepts the parent-child move it refused for a cycle.
type closesRefusedCycleError struct{ hierarchy repairEdgeMove }

func (e *closesRefusedCycleError) Error() string {
	h := e.hierarchy
	return fmt.Sprintf("it closes the cycle that kept %s -> %s (%s) from being re-pointed", h.source, h.target, h.dep.Type)
}

// refuseIfItClosesARefusedCycle returns a *closesRefusedCycleError when
// storage accepts one of the parent-child moves it refused for a cycle
// (cycleRefused) once m's edge is gone; the store is left as it was either
// way. Storage's cycle walk follows an edge in any column, while blocked
// state never reads one stored across prefixes, so such an edge can keep a
// parent-child edge out while blocking nothing; re-pointed, it would start to
// block while the child stays detached. Each probe removes m's edge and
// retries one refused move in a transaction that always rolls back. Only a
// blocking edge is probed: the cycle walk follows no other type, and
// parent-child edges move first.
func refuseIfItClosesARefusedCycle(ctx context.Context, st storage.DoltStorage, m repairEdgeMove, cycleRefused []repairEdgeMove, actorName string) error {
	if m.dep.Type == types.DepParentChild || !types.IsSchedulingEdge(m.dep.Type) {
		return nil
	}
	for _, h := range cycleRefused {
		err := transactHonoringAutoCommit(ctx, st, "", func(tx storage.Transaction) error {
			if err := tx.RemoveDependency(ctx, m.source, m.dep.DependsOnID, actorName); err != nil {
				return err
			}
			if err := moveDependencyInTx(ctx, tx, h.source, h.dep, h.target, actorName); err != nil {
				return err
			}
			return errRepairProbe
		})
		if errors.Is(err, errRepairProbe) {
			return &closesRefusedCycleError{hierarchy: h}
		}
		if !isDependencyRefusal(err) {
			return err
		}
	}
	return nil
}

// repairEdgeMove is one stored edge a repair re-points: the edge on source
// and the target it must name.
type repairEdgeMove struct {
	source string
	dep    *types.Dependency
	target string
}

// repairEdgeMoves lists the edges a repair re-points: parent-child edges
// first, then every other edge, each group in source-ID order.
func repairEdgeMoves(ctx context.Context, st storage.DoltStorage, plan *prefixRepairPlan, issues []*types.Issue) ([]repairEdgeMove, error) {
	if len(plan.Renames) == 0 {
		return nil, nil
	}
	newID := make(map[string]string, len(plan.Renames))
	oldID := make(map[string]string, len(plan.Renames))
	for _, r := range plan.Renames {
		newID[r.OldID] = r.NewID
		oldID[r.NewID] = r.OldID
	}
	ids := make([]string, 0, len(issues))
	stored := make(map[string]bool, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
		stored[issue.ID] = true
	}
	slices.Sort(ids)
	records, err := st.GetDependencyRecordsForIssues(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to read dependencies: %w", err)
	}
	var hierarchy, rest []repairEdgeMove
	for _, source := range ids {
		for _, dep := range records[source] {
			target, ok := repairedDependencyTarget(source, dep.DependsOnID, newID, oldID, stored)
			switch {
			case !ok:
			case dep.Type == types.DepParentChild:
				hierarchy = append(hierarchy, repairEdgeMove{source: source, dep: dep, target: target})
			default:
				rest = append(rest, repairEdgeMove{source: source, dep: dep, target: target})
			}
		}
	}
	return append(hierarchy, rest...), nil
}

// isDependencyRefusal reports whether err is storage refusing the edge
// itself, as it refuses a new edge: a cycle, a block on the issue's own
// ancestor or descendant, an edge onto itself, a second type for one pair or
// a missing endpoint. Anything else (a lost connection, a commit whose result
// is unknown, a canceled run) is a failure of the write.
func isDependencyRefusal(err error) bool {
	var hierarchy *publicops.DependencyHierarchyConflictError
	var conflict *publicops.DependencyTypeConflictError
	var missing *publicops.DependencyEndpointNotFoundError
	return errors.Is(err, publicops.ErrDependencyCycle) || errors.Is(err, publicops.ErrSelfDependency) ||
		errors.As(err, &hierarchy) || errors.As(err, &conflict) || errors.As(err, &missing)
}

// repointFailure is the error a failed re-point ends the repair with. The
// run never reaches its report, so the error names the edges storage
// refused before it, which were left as they were.
func repointFailure(failed unrepointedEdge, err error, refused []unrepointedEdge) error {
	err = fmt.Errorf("failed to re-point dependency %s -> %s at %s: %w", failed.IssueID, failed.DependsOnID, failed.WantedID, err)
	if len(refused) == 0 {
		return err
	}
	edges := make([]string, len(refused))
	for i, e := range refused {
		edges[i] = fmt.Sprintf("%s -> %s (%s, wanted %s): %s", e.IssueID, e.DependsOnID, e.Type, e.WantedID, e.Error)
	}
	return fmt.Errorf("%w; before it, storage refused to re-point %d dependencies, which were left as they were: %s",
		err, len(refused), strings.Join(edges, "; "))
}

// repairedDependencyTarget reports the target an edge stored on source must
// name after the repair, and false when storage already holds it right.
func repairedDependencyTarget(source, dependsOn string, newID, oldID map[string]string, stored map[string]bool) (string, bool) {
	if target, ok := newID[dependsOn]; ok {
		// The edge still names its target's old ID.
		return target, true
	}
	// The target kept its ID, or storage already re-pointed the edge at its
	// new one. Re-add only an edge the source's rename turned from
	// cross-prefix into same-prefix, judged on both ends' IDs before the
	// repair, so it lands in the issue column instead of the external one.
	was, renamed := oldID[source]
	if !renamed || !stored[dependsOn] {
		return "", false
	}
	wasTarget := dependsOn
	if o, ok := oldID[dependsOn]; ok {
		wasTarget = o
	}
	if !issueops.IsExternalDepTarget(was, wasTarget) || issueops.IsExternalDepTarget(source, dependsOn) {
		return "", false
	}
	return dependsOn, true
}

// repointDependency moves one edge stored on source onto target: the removal
// and the re-add share a transaction, so a re-add storage refuses leaves the
// edge exactly as it was.
func repointDependency(ctx context.Context, st storage.DoltStorage, source string, dep *types.Dependency, target, actorName string) error {
	// Commit as the store-level edge writes would: a Dolt commit per edge on
	// a SQL server (dropped under dolt.auto-commit batch or off), none on an
	// embedded store, whose post-run auto-commit takes the whole repair,
	// issue_prefix included. A transaction commit there would mark the
	// command committed and skip that auto-commit.
	msg := ""
	if !isEmbeddedStore(st) {
		msg = fmt.Sprintf("bd: rename-prefix --repair re-points %s -> %s at %s", source, dep.DependsOnID, target)
	}
	return transactHonoringAutoCommit(ctx, st, msg, func(tx storage.Transaction) error {
		return moveDependencyInTx(ctx, tx, source, dep, target, actorName)
	})
}

// moveDependencyInTx removes the edge dep stored on source and re-adds it
// naming target.
func moveDependencyInTx(ctx context.Context, tx storage.Transaction, source string, dep *types.Dependency, target, actorName string) error {
	if err := tx.RemoveDependency(ctx, source, dep.DependsOnID, actorName); err != nil {
		return err
	}
	moved := *dep
	moved.IssueID, moved.DependsOnID = source, target
	return tx.AddDependency(ctx, &moved, actorName)
}

// writeRepairJSON writes a --repair result as one JSON object: the complete
// rename mapping and every collision, for a dry run and a real run alike,
// plus the edges a real run could not re-point (a dry run re-points none).
func writeRepairJSON(targetPrefix string, prefixesFound int, plan *prefixRepairPlan, refused []unrepointedEdge, dryRun bool) error {
	renames := plan.Renames
	if renames == nil {
		renames = []prefixRepairRename{}
	}
	collisions := plan.Collisions
	if collisions == nil {
		collisions = []prefixRepairCollision{}
	}
	if refused == nil {
		refused = []unrepointedEdge{}
	}
	result := map[string]interface{}{
		"target_prefix":     targetPrefix,
		"prefixes_found":    prefixesFound,
		"issues_repaired":   len(plan.Renames),
		"issues_unchanged":  plan.Unchanged,
		"dry_run":           dryRun,
		"renames":           renames,
		"collisions":        collisions,
		"unrepointed_edges": refused,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// printUnrepointedEdges lists every edge the repair left as it was.
func printUnrepointedEdges(edges []unrepointedEdge) {
	if len(edges) == 0 {
		return
	}
	fmt.Printf("\n%s %d dependencies could not be re-pointed and were left exactly as they were:\n",
		ui.RenderWarn("⚠"), len(edges))
	for _, e := range edges {
		fmt.Printf("  %s -> %s (%s, wanted %s): %s\n", ui.RenderWarn(e.IssueID), e.DependsOnID, e.Type, ui.RenderAccent(e.WantedID), e.Error)
	}
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
	renamePrefixCmd.Flags().Bool("mint-only", false, "Mint new issues under the new prefix; keep existing IDs and accept the old prefix")
	rootCmd.AddCommand(renamePrefixCmd)
}
