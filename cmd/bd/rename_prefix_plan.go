package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// repairFallbackReason names why a family's natural (suffix-preserving)
// destination was unavailable. "exists" — a live row, or a live dotted child
// holding a child-only namespace, occupies the destination; "claimed" — an
// earlier source family in this repair took it; "retired" — a retaining sweep
// retired the destination id.
const (
	repairReasonExists  = "exists"
	repairReasonClaimed = "claimed"
	repairReasonRetired = "retired"
)

// repairRetiredSet is the batch retirement verdict the planner asks the store
// seam for: which of a batch of candidate destination ids a retaining sweep
// retired. A nil lookup means the store does not support the probe (test
// fakes), and planning proceeds without retirement checks, the way import's
// preview does.
type repairRetiredSet func(ids []string) (map[string]bool, error)

// repairCollision records one source family that could not take its wanted
// root and was re-rooted, as a whole, on a generated hash id.
type repairCollision struct {
	OldRoot      string   `json:"old_root"`
	WantedRoot   string   `json:"wanted_root"`
	AssignedRoot string   `json:"assigned_root"`
	Reason       string   `json:"reason"`
	Conflicting  string   `json:"conflicting_id,omitempty"`
	IDs          []string `json:"ids"`
}

// repairRename is one planned row rename, in execution order: families in
// prefix order, roots in natural order, each root row before its dotted
// children.
type repairRename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// prefixRepairPlan is the deterministic outcome of repair planning, built
// before any mutation: the renames to execute, the old-root -> new-root map
// text rewriting follows (dotted tails stay attached to their new root),
// every collision fallback, and the rows left untouched.
type prefixRepairPlan struct {
	TargetPrefix string
	Renames      []repairRename
	RootMap      map[string]string
	Collisions   []repairCollision
	Unchanged    int
}

// repairRow is one off-target row: its full id, the row itself, and its dotted
// path under the family root ("" for a root row).
type repairRow struct {
	id    string
	path  string
	issue *types.Issue
}

// repairFamily is every row sharing one old root (the id before its first
// dot). The root row itself may be absent — orphan dotted children still form
// a family owning that root's namespace and wanted destination.
type repairFamily struct {
	oldRoot  string
	prefix   string // prefix extracted from the root row; "" when it has none
	wanted   string // natural (suffix-preserving) destination root
	assigned string // destination root the family actually takes
	members  []*repairRow
}

// splitIssueRoot splits an id into its root (everything before the first dot)
// and the dotted path after it ("taxdb-5su1.13.2" -> "taxdb-5su1", ".13.2").
func splitIssueRoot(id string) (root, path string) {
	if idx := strings.Index(id, "."); idx > 0 {
		return id[:idx], id[idx:]
	}
	return id, ""
}

// wantedRepairRoot maps an old root to its natural destination under the
// target prefix: the root's suffix when a prefix was extracted, the whole root
// when the id belongs to no family.
func wantedRepairRoot(targetPrefix, prefix, oldRoot string) string {
	if prefix != "" && strings.HasPrefix(oldRoot, prefix+"-") {
		return targetPrefix + "-" + strings.TrimPrefix(oldRoot, prefix+"-")
	}
	return targetPrefix + "-" + oldRoot
}

// planPrefixRepair builds the complete repair plan before any mutation.
//
// Rows already beginning with targetPrefix+"-" stay untouched — hash ids,
// hand-named ids like kb-adr-0001 and target wisps alike. Every other row
// keeps its suffix under the target prefix when that destination is free; a
// conflicting family moves as a whole to one generated hash root so dotted
// ancestry stays intact. Identical store contents and actor always produce an
// identical plan: families are ordered by old prefix, then root and members
// in natural id order, natural destinations are reserved before any fallback
// is allocated, and destinations are claimed in that order.
//
// knownPrefixes are the prefixes extraction should honor before falling back
// to its heuristic (workspace config plus the prefixes detected on the rows),
// so a hyphenated family like cre-db keeps its whole suffix. Pass nil for the
// heuristic alone.
//
// retiredLookup, when non-nil, is probed in batches (natural destinations
// first, then fallback candidates) and its errors fail planning before any
// mutation, so a dry run previews exactly what execution would skip.
func planPrefixRepair(targetPrefix, actor string, issues []*types.Issue, knownPrefixes []string, retiredLookup repairRetiredSet) (*prefixRepairPlan, error) {
	plan := &prefixRepairPlan{
		TargetPrefix: targetPrefix,
		RootMap:      make(map[string]string),
	}

	// Live occupancy: every id a row holds, plus the root namespaces live
	// dotted children occupy even when the root row itself is absent.
	liveIDs := make(map[string]bool, len(issues))
	liveChildRoot := make(map[string]string, len(issues))
	for _, issue := range issues {
		liveIDs[issue.ID] = true
		if root, path := splitIssueRoot(issue.ID); path != "" {
			if _, ok := liveChildRoot[root]; !ok {
				liveChildRoot[root] = issue.ID
			}
		}
	}

	// Group the off-target rows by old root.
	families := make(map[string]*repairFamily)
	for _, issue := range issues {
		if strings.HasPrefix(issue.ID, targetPrefix+"-") {
			plan.Unchanged++
			continue
		}
		root, path := splitIssueRoot(issue.ID)
		fam := families[root]
		if fam == nil {
			fam = &repairFamily{
				oldRoot: root,
				prefix:  utils.ExtractIssuePrefixKnown(root, knownPrefixes),
				wanted:  "", // set below; wantedRepairRoot needs the prefix first
			}
			fam.wanted = wantedRepairRoot(targetPrefix, fam.prefix, root)
			families[root] = fam
		}
		fam.members = append(fam.members, &repairRow{id: issue.ID, path: path, issue: issue})
	}

	// Deterministic order: prefix-first, then roots and members in natural id
	// order (which puts every root row before its dotted children).
	ordered := make([]*repairFamily, 0, len(families))
	for _, fam := range families {
		slices.SortFunc(fam.members, func(a, b *repairRow) int {
			return utils.NaturalCompareIDs(a.id, b.id)
		})
		ordered = append(ordered, fam)
	}
	slices.SortFunc(ordered, func(a, b *repairFamily) int {
		return cmp.Or(
			cmp.Compare(a.prefix, b.prefix),
			utils.NaturalCompareIDs(a.oldRoot, b.oldRoot),
		)
	})

	// Retirement probing, batch one: every wanted root and mapped member id,
	// before any destination is handed out. Lookup errors fail planning.
	var retired map[string]bool
	if retiredLookup != nil && len(ordered) > 0 {
		probe := make([]string, 0, 2*len(ordered))
		for _, fam := range ordered {
			probe = append(probe, fam.wanted)
			for _, m := range fam.members {
				probe = append(probe, fam.wanted+m.path)
			}
		}
		var err error
		retired, err = retiredLookup(dedupeIDs(probe))
		if err != nil {
			return nil, fmt.Errorf("read retired ids: %w", err)
		}
	}

	// Destination allocation. `taken` drives the hash walk: live ids, retired
	// ids, claims, and every rejected candidate, so allocation is
	// deterministic and never revisits an id.
	claims := make(map[string]bool) // destinations earlier families claimed
	taken := make(map[string]bool, len(liveIDs)+len(families))
	for id := range liveIDs {
		taken[id] = true
	}
	for id := range retired {
		taken[id] = true
	}

	// Phase one, in family order: reserve every natural (suffix-preserving)
	// destination that is free. Families whose natural root conflicts on a
	// live row, an earlier family's claim, or a retired id wait for phase
	// two; their reason and the conflicting id are recorded as told by the
	// first conflict found.
	type pendingFallback struct {
		fam         *repairFamily
		reason      string
		conflicting string
		candidates  []string // in-memory-passing hash candidates, retirement verdict pending
	}
	var fallbacks []pendingFallback
	claimFamily := func(fam *repairFamily) {
		plan.RootMap[fam.oldRoot] = fam.assigned
		for _, m := range fam.members {
			newID := fam.assigned + m.path
			claims[newID] = true
			taken[newID] = true
			plan.Renames = append(plan.Renames, repairRename{From: m.id, To: newID})
		}
	}
	for _, fam := range ordered {
		reason, conflicting, free := repairDestinationStatus(fam.wanted, fam, liveIDs, liveChildRoot, claims, retired)
		if free {
			fam.assigned = fam.wanted
			claimFamily(fam)
		} else {
			fallbacks = append(fallbacks, pendingFallback{fam: fam, reason: reason, conflicting: conflicting})
		}
	}

	// Phase two: allocate one generated hash root per conflicting family.
	// Candidates are generated against `taken` (so live, retired and rejected
	// ids are never proposed), screened against the in-memory conflicts, then
	// probed for retirement in one batch before any is committed.
	const maxAttempts = 100
	var probeBatch []string
	for i := range fallbacks {
		fb := &fallbacks[i]
		// Seed the hash with the root row when it exists, else the first
		// ordered orphan member.
		seed := fb.fam.members[0].issue
		for attempts := 0; attempts < maxAttempts; attempts++ {
			candidate, err := generateRepairHashID(targetPrefix, seed, actor, taken)
			if err != nil {
				return nil, fmt.Errorf("failed to generate a fallback root for %s: %w", fb.fam.oldRoot, err)
			}
			taken[candidate] = true // a rejected candidate is never reconsidered
			if _, _, free := repairDestinationStatus(candidate, fb.fam, liveIDs, liveChildRoot, claims, nil); free {
				fb.candidates = append(fb.candidates, candidate)
				probeBatch = append(probeBatch, candidate)
			}
		}
	}
	var retiredCandidates map[string]bool
	if retiredLookup != nil && len(probeBatch) > 0 {
		var err error
		retiredCandidates, err = retiredLookup(dedupeIDs(probeBatch))
		if err != nil {
			return nil, fmt.Errorf("read retired ids: %w", err)
		}
	}
	for i := range fallbacks {
		fb := &fallbacks[i]
		assigned := ""
		for _, candidate := range fb.candidates {
			if retiredCandidates[candidate] {
				continue
			}
			// Re-check in memory: an earlier fallback family in this loop may
			// have claimed a candidate this family also generated.
			if _, _, free := repairDestinationStatus(candidate, fb.fam, liveIDs, liveChildRoot, claims, nil); !free {
				continue
			}
			assigned = candidate
			break
		}
		if assigned == "" {
			return nil, fmt.Errorf("failed to allocate a unique non-retired fallback root for the %s family after %d attempts", fb.fam.oldRoot, maxAttempts)
		}
		fb.fam.assigned = assigned

		collision := repairCollision{
			OldRoot:      fb.fam.oldRoot,
			WantedRoot:   fb.fam.wanted,
			AssignedRoot: assigned,
			Reason:       fb.reason,
			Conflicting:  fb.conflicting,
			IDs:          make([]string, 0, len(fb.fam.members)),
		}
		for _, m := range fb.fam.members {
			collision.IDs = append(collision.IDs, m.id)
		}
		plan.Collisions = append(plan.Collisions, collision)
		claimFamily(fb.fam)
	}
	return plan, nil
}

// dedupeIDs returns ids without duplicates, order preserved.
func dedupeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// repairDestinationStatus reports whether destinationRoot is free for fam's
// members, and when it is not, the fallback reason and the id that holds the
// conflicting slot. A nil retired set skips retirement checks (the in-memory
// screen during candidate generation; the verdict arrives with the batch).
func repairDestinationStatus(destinationRoot string, fam *repairFamily, liveIDs map[string]bool, liveChildRoot map[string]string, claims, retired map[string]bool) (reason, conflicting string, free bool) {
	if liveIDs[destinationRoot] {
		return repairReasonExists, destinationRoot, false
	}
	if child, ok := liveChildRoot[destinationRoot]; ok {
		// No live root row, but a live dotted child already occupies this
		// namespace; adopting the root would graft the source family onto
		// that child's ancestry.
		return repairReasonExists, child, false
	}
	if claims[destinationRoot] {
		return repairReasonClaimed, destinationRoot, false
	}
	if retired[destinationRoot] {
		return repairReasonRetired, destinationRoot, false
	}
	for _, m := range fam.members {
		destination := destinationRoot + m.path
		if liveIDs[destination] {
			return repairReasonExists, destination, false
		}
		if claims[destination] {
			return repairReasonClaimed, destination, false
		}
		if retired[destination] {
			return repairReasonRetired, destination, false
		}
	}
	return "", "", true
}
