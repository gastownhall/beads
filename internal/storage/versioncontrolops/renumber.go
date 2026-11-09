package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Offline child-id collision repair (GH#4796, bd-28sb25): two clones that each
// minted children under the same parent while offline converge on colliding
// hierarchical ids (each side's P.71 is a different bead), and the pull's
// add/add conflicts wedge both clones until someone renumbers by hand. The
// repair is explicit — `bd dolt pull --renumber-children` — and this file is
// its read-only half: compare the pinned tips with their merge base, find the
// ids that genuinely collide, and allocate fresh suffixes above BOTH clones'
// high-water marks so neither side's identities are reused.
//
// The proposal deliberately over-approximates nothing: an id that both tips
// hold with identical content is not a collision (a clean add/add merge keeps
// one copy, and "do not infer separate identities from identical rows"), an
// id only one tip holds is not a collision, and no plan renames anything
// until the authorized apply path (PullWithChildRenumbers) revalidates it.

// issuesIDLen is the issues.id column's VARCHAR(255) limit: a mapped id past
// it cannot be written, so allocation rejects it before proposing anything.
const issuesIDLen = 255

// branchHashAsOf reads one branch or remote-tracking ref's commit hash.
// Local branches live in dolt_branches; remote-tracking refs
// (remotes/<remote>/<branch>) live in dolt_remote_branches, the same table
// refreshTrackingRef-style fetches maintain. sql.ErrNoRows degrades to ""
// (no such ref).
func branchHashAsOf(ctx context.Context, db DBConn, ref string) (string, error) {
	table := "dolt_branches"
	if strings.HasPrefix(ref, "remotes/") {
		table = "dolt_remote_branches"
	}
	var hash string
	//nolint:gosec // G201: table is one of two package-internal literals, not caller input.
	err := db.QueryRowContext(ctx, "SELECT hash FROM "+table+" WHERE name = ?", ref).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read ref %s: %w", ref, err)
	}
	return hash, nil
}

// readIDsAsOf reads every id of table (issues-plane only; wisps are dolt_ignore'd
// and do not support revision reads) present at rev. Missing-table errors are
// the one recognized "this database never had that table" shape and mean empty;
// any other failure propagates.
func readIDsAsOf(ctx context.Context, db DBConn, table, rev string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx,
		//nolint:gosec // G201: table is a package constant and rev is validated against doltCommitHashRE by the caller.
		fmt.Sprintf("SELECT id FROM %s AS OF '%s'", table, rev))
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("read %s as of %s: %w", table, rev, err)
	}
	defer rows.Close()
	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read %s as of %s: %w", table, rev, err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s as of %s: %w", table, rev, err)
	}
	return ids, nil
}

// readCurrentIDs reads every id of a clone-local (dolt_ignore'd) table in the
// current working set. Same missing-table contract as readIDsAsOf.
func readCurrentIDs(ctx context.Context, db DBConn, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx,
		//nolint:gosec // G201: table is a package constant.
		fmt.Sprintf("SELECT id FROM %s", table))
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", table, err)
	}
	defer rows.Close()
	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read %s: %w", table, err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", table, err)
	}
	return ids, nil
}

// issueIdentityColumns are the issues columns that decide whether the same id
// at the two tips is ONE bead or TWO: two offline clones writing the same
// task produce different content in at least one of these, while an id
// pushed and merged twice carries identical values. content_hash is not among
// them because it is a derived cache; the columns it summarizes are compared
// directly.
const issueIdentityColumns = "title, description, design, acceptance_criteria, notes, " +
	"status, priority, issue_type, created_by, source_system, external_ref, " +
	"owner, assignee, spec_id, source_repo"

// readIssueIdentityAsOf reads one issue's identity columns at rev, as strings
// (NULL included), for the both-tips comparison.
func readIssueIdentityAsOf(ctx context.Context, db DBConn, id, rev string) ([]sql.NullString, error) {
	rows, err := db.QueryContext(ctx,
		//nolint:gosec // G201: rev is validated against doltCommitHashRE by the caller.
		fmt.Sprintf("SELECT %s FROM issues AS OF '%s' WHERE id = ?", issueIdentityColumns, rev), id)
	if err != nil {
		return nil, fmt.Errorf("read issue %s as of %s: %w", id, rev, err)
	}
	defer rows.Close()
	vals := make([]sql.NullString, 0, 15)
	if rows.Next() {
		cols, scanErr := rows.Columns()
		if scanErr != nil {
			return nil, fmt.Errorf("read issue %s as of %s: %w", id, rev, scanErr)
		}
		vals = make([]sql.NullString, len(cols))
		scans := make([]any, len(cols))
		for i := range vals {
			scans[i] = &vals[i]
		}
		if err := rows.Scan(scans...); err != nil {
			return nil, fmt.Errorf("read issue %s as of %s: %w", id, rev, err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read issue %s as of %s: %w", id, rev, err)
	}
	return vals, nil
}

// sameIssueIdentity reports whether the two reads agree on every identity
// column. Either side reading zero rows (the id vanished at that rev between
// the set read and this read — a concurrent move) is NOT the same identity.
func sameIssueIdentity(a, b []sql.NullString) bool {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Valid != b[i].Valid || a[i].String != b[i].String {
			return false
		}
	}
	return true
}

// childCounterAsOf reads one parent's child counter at rev; 0 when the parent
// has no counter row there (the row's absence is not a negative high-water).
func childCounterAsOf(ctx context.Context, db DBConn, parent, rev string) (int64, error) {
	var last sql.NullInt64
	//nolint:gosec // G201: rev is validated against doltCommitHashRE by the caller.
	if err := db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT last_child FROM child_counters AS OF '%s' WHERE parent_id = ?", rev), parent).
		Scan(&last); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read child counter for %s as of %s: %w", parent, rev, err)
	}
	if !last.Valid || last.Int64 < 0 {
		return 0, nil
	}
	return last.Int64, nil
}

// maxUsedSuffix scans id sets for the highest direct-child suffix of parent:
// every id of the form parent.N (never parent.N.M, whose suffix belongs to
// its own parent). Tombstones count — once a suffix was handed out it must
// not be reissued, which is exactly issueops.UsedChildIDsInTx's contract.
func maxUsedSuffix(parent string, idSets ...map[string]bool) int64 {
	prefix := parent + "."
	var max int64
	for _, ids := range idSets {
		for id := range ids {
			if !strings.HasPrefix(id, prefix) {
				continue
			}
			// Only parent.N counts, never parent.N.M: a deeper id's suffix
			// belongs to its own parent.
			rest := id[len(prefix):]
			if !isAllDigits(rest) {
				continue
			}
			if n, err := strconv.ParseInt(rest, 10, 64); err == nil && n > max {
				max = n
			}
		}
	}
	return max
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// renumberAllocInput is everything the pure allocator needs, so every rule is
// unit-testable without a database.
type renumberAllocInput struct {
	// candidates are the colliding ids: hierarchical ids present at BOTH
	// tips, absent at the merge base, with differing content.
	candidates []string
	// localIDs are every hierarchical live issue id at the LOCAL tip — the
	// pool subtree mapping draws descendants from.
	localIDs map[string]bool
	// floors are the allocation parents' starting high-water marks: the max
	// of both tips' counters and every used suffix both sides ever handed
	// out (live, tombstoned, wisp-plane).
	floors map[string]int64
	// taken are the ids no destination may take: live and retired ids on
	// both planes, both tips, plus tombstones.
	taken map[string]bool
}

// assignChildRenumbers is the pure allocation core: reduce the candidates to
// minimal roots, sort them by parent and numeric suffix, allocate each root a
// fresh suffix above its parent's floor, and map every local descendant under
// its root by exact boundary (P.71 -> P.74 carries P.71.2 -> P.74.2 and never
// touches P.710). counterFloors is the floor each allocation parent must be
// raised to (never lowered) once the repair runs. Any destination that is
// taken, or too long for issues.id, rejects the whole plan: a proposal that
// cannot be applied is not proposed.
func assignChildRenumbers(in renumberAllocInput) (mappings []storage.ChildRenumberMapping, counterFloors map[string]int64, err error) {
	candSet := make(map[string]bool, len(in.candidates))
	type parsedID struct {
		id     string
		parent string
		suffix int
	}
	roots := make([]parsedID, 0, len(in.candidates))
	for _, id := range in.candidates {
		candSet[id] = true
	}
	for _, id := range in.candidates {
		parent, suffix, ok := issueops.ParseHierarchicalID(id)
		if !ok {
			return nil, nil, fmt.Errorf("renumber: candidate %q is not hierarchical", id)
		}
		// A candidate with ANY candidate ancestor is a descendant of another
		// collision: it maps by that root's prefix, never by fresh
		// allocation. Walk the ancestor chain until it leaves the candidate
		// set.
		descendant := false
		for anc := parent; candSet[anc]; {
			descendant = true
			var pok bool
			anc, _, pok = issueops.ParseHierarchicalID(anc)
			if !pok {
				break
			}
		}
		if descendant {
			continue
		}
		roots = append(roots, parsedID{id: id, parent: parent, suffix: suffix})
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].parent != roots[j].parent {
			return roots[i].parent < roots[j].parent
		}
		return roots[i].suffix < roots[j].suffix
	})

	counterFloors = make(map[string]int64, len(in.floors))
	for p, f := range in.floors {
		counterFloors[p] = f
	}
	assigned := make(map[string]bool)
	mappings = make([]storage.ChildRenumberMapping, 0, len(roots)*2)

	flushSubtree := func(root parsedID, newRoot string) error {
		// Every local live id under the old root — root included — carries
		// over with the same tail: the exact boundary P.71.* never matches
		// P.710.
		prefix := root.id + "."
		var subtree []string
		for id := range in.localIDs {
			if strings.HasPrefix(id, prefix) {
				subtree = append(subtree, id)
			}
		}
		sort.Slice(subtree, func(i, j int) bool {
			return len(subtree[i]) < len(subtree[j]) ||
				(len(subtree[i]) == len(subtree[j]) && subtree[i] < subtree[j])
		})
		for _, id := range subtree {
			newID := newRoot + id[len(root.id):]
			if err := validateRenumDestination(newID, in.taken, assigned); err != nil {
				return err
			}
			assigned[newID] = true
			mappings = append(mappings, storage.ChildRenumberMapping{OldID: id, NewID: newID})
		}
		return nil
	}

	for _, root := range roots {
		floor := in.floors[root.parent]
		var newID string
		var next int64
		for next = floor + 1; ; next++ {
			newID = fmt.Sprintf("%s.%d", root.parent, next)
			if !in.taken[newID] && !assigned[newID] {
				break
			}
		}
		if err := validateRenumDestination(newID, in.taken, assigned); err != nil {
			return nil, nil, err
		}
		assigned[newID] = true
		mappings = append(mappings, storage.ChildRenumberMapping{OldID: root.id, NewID: newID, Root: true})
		if next > counterFloors[root.parent] {
			counterFloors[root.parent] = next
		}
		if err := flushSubtree(root, newID); err != nil {
			return nil, nil, err
		}
	}
	return mappings, counterFloors, nil
}

// validateRenumDestination rejects a destination that already holds a live or
// retired id on either plane, that another mapping in this plan already took,
// or that no longer fits issues.id.
func validateRenumDestination(newID string, taken, assigned map[string]bool) error {
	if len(newID) > issuesIDLen {
		return fmt.Errorf("renumber: mapped id %q exceeds the %d-byte issue id limit", newID, issuesIDLen)
	}
	if taken[newID] {
		return fmt.Errorf("renumber: mapped id %q is already a live or retired id", newID)
	}
	if assigned[newID] {
		return fmt.Errorf("renumber: mapped id %q collides with another id in the same plan", newID)
	}
	return nil
}

// PlanChildRenumbers computes the offline-collision repair proposal for
// pulling branch from remote — read-only. The caller validates revs against
// doltCommitHashRE's shape indirectly: every AS OF read below inlines a hash
// read from dolt_branches itself, never an operator-supplied string.
//
// The proposal: the hierarchical ids present at BOTH tips and absent at the
// merge base, excluding ids whose rows are identical at both tips (one bead,
// not a collision). Those are renumbered — the LOCAL copies, above both
// clones' high-water marks. When remote has no tracking ref, or the histories
// share no ancestor, planning fails without touching anything.
func PlanChildRenumbers(ctx context.Context, db DBConn, branch, remote string) (*storage.ChildRenumberPlan, error) {
	trackingRef := "remotes/" + remote + "/" + branch
	localHead, err := branchHashAsOf(ctx, db, branch)
	if err != nil {
		return nil, err
	}
	if localHead == "" {
		return nil, fmt.Errorf("renumber: branch %s not found", branch)
	}
	remoteHead, err := branchHashAsOf(ctx, db, trackingRef)
	if err != nil {
		return nil, err
	}
	if remoteHead == "" {
		return nil, fmt.Errorf("renumber: no remote-tracking ref %s; fetch from %s first", trackingRef, remote)
	}
	if !doltCommitHashRE.MatchString(localHead) || !doltCommitHashRE.MatchString(remoteHead) {
		return nil, fmt.Errorf("renumber: unreadable head hash for %s or %s", branch, trackingRef)
	}

	var baseHash sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT dolt_merge_base(?, ?)", branch, trackingRef).Scan(&baseHash); err != nil {
		return nil, fmt.Errorf("renumber: merge base of %s and %s: %w", branch, trackingRef, err)
	}
	if !baseHash.Valid || !doltCommitHashRE.MatchString(baseHash.String) {
		return nil, fmt.Errorf("renumber: %s and %s share no common ancestor; renumbering cannot repair divergent histories",
			branch, trackingRef)
	}
	base := baseHash.String

	plan := &storage.ChildRenumberPlan{
		Remote:     remote,
		RemoteRef:  trackingRef,
		RemoteHead: remoteHead,
		LocalHead:  localHead,
		Base:       base,
	}

	localIDs, err := readIDsAsOf(ctx, db, "issues", localHead)
	if err != nil {
		return nil, err
	}
	remoteIDs, err := readIDsAsOf(ctx, db, "issues", remoteHead)
	if err != nil {
		return nil, err
	}
	baseIDs, err := readIDsAsOf(ctx, db, "issues", base)
	if err != nil {
		return nil, err
	}

	// Candidates: hierarchical, at both tips, absent at the base, and not
	// identical rows — the last check keeps a twice-pushed bead out of the
	// repair entirely.
	var candidates []string
	for id := range localIDs {
		if !remoteIDs[id] || baseIDs[id] || !strings.Contains(id, ".") {
			continue
		}
		ours, err := readIssueIdentityAsOf(ctx, db, id, localHead)
		if err != nil {
			return nil, err
		}
		theirs, err := readIssueIdentityAsOf(ctx, db, id, remoteHead)
		if err != nil {
			return nil, err
		}
		if sameIssueIdentity(ours, theirs) {
			continue
		}
		candidates = append(candidates, id)
	}
	if len(candidates) == 0 {
		return plan, nil
	}
	sort.Strings(candidates)

	// Taken: live and retired ids both planes could still be holding. Base
	// ids are not taken — a suffix used only before the divergence is free
	// again on both tips, matching GetNextChildIDTx's live-plus-tombstone
	// high-water.
	localTomb, err := readIDsAsOf(ctx, db, issueops.IDTombstoneTable, localHead)
	if err != nil {
		return nil, err
	}
	remoteTomb, err := readIDsAsOf(ctx, db, issueops.IDTombstoneTable, remoteHead)
	if err != nil {
		return nil, err
	}
	localWispIDs, err := readCurrentIDs(ctx, db, "wisps")
	if err != nil {
		return nil, err
	}
	localWispTomb, err := readCurrentIDs(ctx, db, issueops.WispIDTombstoneTable)
	if err != nil {
		return nil, err
	}
	taken := make(map[string]bool, len(localIDs)+len(remoteIDs)+len(localTomb)+len(remoteTomb))
	for _, set := range []map[string]bool{localIDs, remoteIDs, localTomb, remoteTomb, localWispIDs, localWispTomb} {
		for id := range set {
			if strings.Contains(id, ".") {
				taken[id] = true
			}
		}
	}

	// Floors: both tips' counters plus every suffix both sides ever handed
	// out — live children, versioned tombstones at both tips, and the
	// clone-local wisp plane.
	parents := make(map[string]bool)
	for _, id := range candidates {
		if parent, _, ok := issueops.ParseHierarchicalID(id); ok {
			parents[parent] = true
		}
	}
	floors := make(map[string]int64, len(parents))
	for parent := range parents {
		floor, err := childCounterAsOf(ctx, db, parent, localHead)
		if err != nil {
			return nil, err
		}
		if remoteFloor, err := childCounterAsOf(ctx, db, parent, remoteHead); err != nil {
			return nil, err
		} else if remoteFloor > floor {
			floor = remoteFloor
		}
		if used := maxUsedSuffix(parent, localIDs, remoteIDs, localTomb, remoteTomb, localWispIDs, localWispTomb); used > floor {
			floor = used
		}
		floors[parent] = floor
	}

	localHier := make(map[string]bool, len(localIDs))
	for id := range localIDs {
		if strings.Contains(id, ".") {
			localHier[id] = true
		}
	}
	mappings, counterFloors, err := assignChildRenumbers(renumberAllocInput{
		candidates: candidates,
		localIDs:   localHier,
		floors:     floors,
		taken:      taken,
	})
	if err != nil {
		return nil, err
	}
	plan.Mappings = mappings
	plan.CounterFloors = counterFloors
	return plan, nil
}
