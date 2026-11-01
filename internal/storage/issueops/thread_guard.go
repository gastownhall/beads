package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// threadGuardWalkLimit bounds one thread's membership walk. A thread that
// reaches past it is not a mail thread the guard can vouch for, so the walk
// aborts the request rather than reading the workspace's whole graph inside a
// write transaction.
const threadGuardWalkLimit = 10000

// ValidateThreadGuard refuses, before anything is read, a DeleteRequest whose
// ThreadGuard cannot be checked: one paired with Cascade, or one missing a
// field every check needs. A nil guard is valid.
//
// Both delete bodies call it — DeleteInTx at its top and the unit-of-work
// deleter before it opens a unit of work — so the refusal costs no database
// work on either route.
func ValidateThreadGuard(req publicops.DeleteRequest) error {
	guard := req.ThreadGuard
	if guard == nil {
		return nil
	}
	if req.Cascade {
		return fmt.Errorf("%w: a thread guard cannot be combined with cascade: a cascade deletes rows the guard never archived", publicops.ErrValidation)
	}
	if strings.TrimSpace(guard.MemberLabel) == "" {
		return fmt.Errorf("%w: thread guard needs a member label", publicops.ErrValidation)
	}
	if strings.TrimSpace(guard.Edge) == "" {
		return fmt.Errorf("%w: thread guard needs an edge type", publicops.ErrValidation)
	}
	if len(guard.Threads) == 0 {
		return fmt.Errorf("%w: thread guard lists no threads", publicops.ErrValidation)
	}
	for i, thread := range guard.Threads {
		if strings.TrimSpace(thread.Root) == "" {
			return fmt.Errorf("%w: thread guard thread %d has no root", publicops.ErrValidation, i)
		}
		if len(thread.Members) == 0 {
			return fmt.Errorf("%w: thread guard thread %s lists no members", publicops.ErrValidation, thread.Root)
		}
		for _, member := range thread.Members {
			if strings.TrimSpace(member.ID) == "" || strings.TrimSpace(member.Revision) == "" {
				return fmt.Errorf("%w: thread guard thread %s has a member with no id or no revision", publicops.ErrValidation, thread.Root)
			}
		}
	}
	return nil
}

// CheckThreadGuardInTx runs guard's four checks against tx for a delete of
// ids, and returns a *publicops.ThreadGuardMismatchError naming every failure,
// nil when all pass, or the read error that stopped it.
//
// It only reads. The caller runs it inside the deleting transaction, before any
// row changes, so the archive is compared against the snapshot the deletion
// acts on; a failure leaves the caller nothing to undo.
//
// It assumes a guard ValidateThreadGuard accepted.
func CheckThreadGuardInTx(ctx context.Context, tx DBTX, guard *publicops.ThreadGuard, ids []string) error {
	roots := make([]string, len(guard.Threads))
	for i, thread := range guard.Threads {
		roots[i] = thread.Root
	}
	components, err := threadComponentsInTx(ctx, tx, roots, guard.Edge, guard.ExcludeLabel)
	if err != nil {
		return err
	}

	// Every thread's members are imaged in one call; a member two threads
	// list is imaged once and read by both.
	var allMemberIDs []string
	listed := make(map[string]bool)
	for _, thread := range guard.Threads {
		for _, member := range thread.Members {
			if !listed[member.ID] {
				listed[member.ID] = true
				allMemberIDs = append(allMemberIDs, member.ID)
			}
		}
	}
	images, missing, err := threadImagesInTx(ctx, tx, allMemberIDs)
	if err != nil {
		return err
	}
	imageByID := make(map[string]publicops.Image, len(images))
	for _, img := range images {
		imageByID[img.ID] = img
	}
	gone := make(map[string]bool, len(missing))
	for _, id := range missing {
		gone[id] = true
	}

	var failed []publicops.ThreadGuardFailure
	guarded := make(map[string]bool)
	for _, thread := range guard.Threads {
		expected := make(map[string]string, len(thread.Members))
		var memberIDs []string
		for _, member := range thread.Members {
			if _, dup := expected[member.ID]; !dup {
				memberIDs = append(memberIDs, member.ID)
			}
			expected[member.ID] = member.Revision
			guarded[member.ID] = true
		}

		component := components[thread.Root]
		strays := make(map[string]bool)
		for id := range component {
			if _, ok := expected[id]; !ok {
				strays[id] = true
			}
		}
		for _, id := range memberIDs {
			if !component[id] {
				strays[id] = true
			}
		}
		if len(strays) > 0 {
			failed = append(failed, threadGuardFailure(thread.Root, publicops.ThreadGuardMembership, strays))
		}

		changed := make(map[string]bool)
		ineligible := make(map[string]bool)
		for _, id := range memberIDs {
			if gone[id] {
				changed[id] = true
				continue
			}
			img := imageByID[id]
			if img.Revision != expected[id] {
				changed[id] = true
			}
			if !threadGuardEligible(img, guard) {
				ineligible[id] = true
			}
		}
		if len(changed) > 0 {
			failed = append(failed, threadGuardFailure(thread.Root, publicops.ThreadGuardRevision, changed))
		}
		if len(ineligible) > 0 {
			failed = append(failed, threadGuardFailure(thread.Root, publicops.ThreadGuardEligibility, ineligible))
		}
	}

	labelled, err := threadGuardLabelledInTx(ctx, tx, ids, guard.MemberLabel)
	if err != nil {
		return err
	}
	uncovered := make(map[string]bool)
	for id := range labelled {
		if !guarded[id] {
			uncovered[id] = true
		}
	}
	if len(uncovered) > 0 {
		failed = append(failed, threadGuardFailure("", publicops.ThreadGuardCoverage, uncovered))
	}

	if len(failed) > 0 {
		return &publicops.ThreadGuardMismatchError{Failed: failed}
	}
	return nil
}

func threadGuardFailure(root, check string, ids map[string]bool) publicops.ThreadGuardFailure {
	return publicops.ThreadGuardFailure{Root: root, Check: check, IDs: workapi.SortedDeleteIDs(ids)}
}

// threadGuardEligible reports whether img is still archivable thread mail:
// labelled MemberLabel, a wisp, closed, and closed before the cutoff. It reads
// the image rather than the row so the revision and the eligibility are
// answered from one read.
func threadGuardEligible(img publicops.Image, guard *publicops.ThreadGuard) bool {
	labelled := false
	for _, row := range img.Labels {
		if row["label"] == guard.MemberLabel {
			labelled = true
			break
		}
	}
	if !labelled || img.Table != "wisps" || img.Row["status"] != string(types.StatusClosed) {
		return false
	}
	closedAt, ok := img.Row["closed_at"].(string)
	if !ok {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, closedAt)
	return err == nil && at.Before(guard.Cutoff)
}

// threadImagesInTx images ids and reports separately the ids that name no row
// in either plane, which ImageInTx refuses as a whole.
func threadImagesInTx(ctx context.Context, tx DBTX, ids []string) ([]publicops.Image, []string, error) {
	images, err := ImageInTx(ctx, tx, ids)
	var notFound *publicops.NotFoundError
	if !errors.As(err, &notFound) {
		if err != nil {
			return nil, nil, fmt.Errorf("thread guard: image members: %w", err)
		}
		return images, nil, nil
	}
	gone := make(map[string]bool, len(notFound.IDs))
	for _, id := range notFound.IDs {
		gone[id] = true
	}
	var present []string
	for _, id := range ids {
		if !gone[id] {
			present = append(present, id)
		}
	}
	if len(present) == 0 {
		return nil, notFound.IDs, nil
	}
	images, err = ImageInTx(ctx, tx, present)
	if err != nil {
		return nil, nil, fmt.Errorf("thread guard: image members: %w", err)
	}
	return images, notFound.IDs, nil
}

// threadGuardWalkColumns are the edge columns a walk matches its frontier
// against, one UNION ALL arm each. Every column leads an index with type
// (the target columns) or a unique key (issue_id); an OR of them, or the
// COALESCE of the targets, is served by none, because go-mysql-server does not
// index-merge.
var threadGuardWalkColumns = []string{"issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}

// threadComponentsInTx walks, from every root at once, the connected
// components of the roots over edge-typed dependencies in either direction, in
// both edge tables, and returns each distinct root's component: every id it
// reaches, root included. A bead labelled exclude is neither reached nor
// walked through; an excluded root reaches nothing and maps to an empty set.
//
// Roots whose walks meet share one component, so each sees the other's
// members, exactly as walking each root alone would. The statements issued
// depend on the walk's depth and on how many IN batches each level fills, not
// on how many roots there are.
//
// The walk does not ask whether a reached id names a row: a reply edge onto a
// bead deleted since is still part of what the caller would be deleting
// around, and the revision check reports the gone member.
func threadComponentsInTx(ctx context.Context, tx DBTX, roots []string, edge, exclude string) (map[string]map[string]bool, error) {
	seeds := make(map[string]bool, len(roots))
	for _, root := range roots {
		seeds[root] = true
	}
	excluded := make(map[string]bool)
	if exclude != "" {
		var err error
		if excluded, err = threadGuardLabelledInTx(ctx, tx, workapi.SortedDeleteIDs(seeds), exclude); err != nil {
			return nil, err
		}
	}

	sets := newThreadGuardSets()
	reached := make(map[string]bool)
	for root := range seeds {
		if !excluded[root] {
			reached[root] = true
			sets.add(root, true)
		}
	}
	frontier := workapi.SortedDeleteIDs(reached)
	for len(frontier) > 0 {
		found := make(map[string]bool)
		edges := make(map[[2]string]bool)
		for i := 0; i < len(frontier); i += deleteBatchSize {
			end := min(i+deleteBatchSize, len(frontier))
			inClause, args := buildSQLInClause(frontier[i:end])
			queryArgs := make([]any, 0, len(threadGuardWalkColumns)*(1+len(args)))
			for range threadGuardWalkColumns {
				queryArgs = append(append(queryArgs, edge), args...)
			}
			from := sets.rootOf(frontier[i])
			for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
				rows, err := tx.QueryContext(ctx, threadGuardWalkSQL(depTable, inClause), queryArgs...)
				if err != nil {
					if optionalBlockedTable(depTable) && isTableNotExistError(err) {
						continue
					}
					return nil, fmt.Errorf("thread guard: walk %s from %s: %w", depTable, from, err)
				}
				for rows.Next() {
					var source string
					var target sql.NullString
					if err := rows.Scan(&source, &target); err != nil {
						_ = rows.Close()
						return nil, fmt.Errorf("thread guard: scan %s edge: %w", depTable, err)
					}
					for _, id := range [2]string{source, target.String} {
						if id != "" && !reached[id] && !excluded[id] {
							found[id] = true
						}
					}
					if source != "" && target.String != "" {
						edges[[2]string{source, target.String}] = true
					}
				}
				_ = rows.Close()
				if err := rows.Err(); err != nil {
					return nil, fmt.Errorf("thread guard: walk %s from %s: %w", depTable, from, err)
				}
			}
		}
		if exclude != "" && len(found) > 0 {
			labelled, err := threadGuardLabelledInTx(ctx, tx, workapi.SortedDeleteIDs(found), exclude)
			if err != nil {
				return nil, err
			}
			for id := range labelled {
				delete(found, id)
				excluded[id] = true
			}
		}
		for id := range found {
			reached[id] = true
			sets.add(id, false)
		}
		// Every edge row had a frontier end, so after this level each found id
		// shares a set with the root that reached it.
		for e := range edges {
			if reached[e[0]] && reached[e[1]] {
				sets.union(e[0], e[1])
			}
		}
		for _, root := range roots {
			if reached[root] && sets.size(root) > threadGuardWalkLimit {
				return nil, fmt.Errorf("thread guard: thread %s reaches more than %d beads over %q edges", sets.rootOf(root), threadGuardWalkLimit, edge)
			}
		}
		frontier = workapi.SortedDeleteIDs(found)
	}

	byRep := make(map[string]map[string]bool)
	for id := range reached {
		rep := sets.find(id)
		if byRep[rep] == nil {
			byRep[rep] = make(map[string]bool)
		}
		byRep[rep][id] = true
	}
	components := make(map[string]map[string]bool, len(seeds))
	for root := range seeds {
		if reached[root] {
			components[root] = byRep[sets.find(root)]
		} else {
			components[root] = map[string]bool{}
		}
	}
	return components, nil
}

// threadGuardWalkSQL is one walk statement over depTable: every edge row with
// an end in inClause's ids, one UNION ALL arm per threadGuardWalkColumns
// column, each binding the edge type and then the ids.
//
//nolint:gosec // G201: depTable is a fixed name; inClause contains only ? placeholders
func threadGuardWalkSQL(depTable, inClause string) string {
	arms := make([]string, len(threadGuardWalkColumns))
	for i, col := range threadGuardWalkColumns {
		arms[i] = fmt.Sprintf(`SELECT issue_id, %s AS depends_on_id FROM %s WHERE type = ? AND %s IN (%s)`,
			DepTargetExpr, depTable, col, inClause)
	}
	return strings.Join(arms, " UNION ALL ")
}

// threadGuardSets is a union-find over the ids a thread-guard walk reaches.
// Each set records its size and the lowest sorted root in it, which names the
// set in the walk's errors.
type threadGuardSets struct {
	parent map[string]string
	count  map[string]int
	root   map[string]string
}

func newThreadGuardSets() *threadGuardSets {
	return &threadGuardSets{parent: map[string]string{}, count: map[string]int{}, root: map[string]string{}}
}

// add makes id a set of its own; isRoot says whether it names a thread.
func (s *threadGuardSets) add(id string, isRoot bool) {
	if _, ok := s.parent[id]; ok {
		return
	}
	s.parent[id] = id
	s.count[id] = 1
	if isRoot {
		s.root[id] = id
	}
}

func (s *threadGuardSets) find(id string) string {
	rep := id
	for s.parent[rep] != rep {
		rep = s.parent[rep]
	}
	for id != rep {
		next := s.parent[id]
		s.parent[id] = rep
		id = next
	}
	return rep
}

func (s *threadGuardSets) union(a, b string) {
	ra, rb := s.find(a), s.find(b)
	if ra == rb {
		return
	}
	if s.count[ra] < s.count[rb] {
		ra, rb = rb, ra
	}
	s.parent[rb] = ra
	s.count[ra] += s.count[rb]
	if r := s.root[rb]; r != "" && (s.root[ra] == "" || r < s.root[ra]) {
		s.root[ra] = r
	}
	delete(s.count, rb)
	delete(s.root, rb)
}

func (s *threadGuardSets) size(id string) int { return s.count[s.find(id)] }

// rootOf names id's set by the lowest sorted root in it.
func (s *threadGuardSets) rootOf(id string) string { return s.root[s.find(id)] }

// threadGuardLabelledInTx reports which of ids carry label, in either plane.
//
//nolint:gosec // G201: inClause contains only ? placeholders
func threadGuardLabelledInTx(ctx context.Context, tx DBTX, ids []string, label string) (map[string]bool, error) {
	labelled := make(map[string]bool)
	for i := 0; i < len(ids); i += deleteBatchSize {
		end := min(i+deleteBatchSize, len(ids))
		inClause, args := buildSQLInClause(ids[i:end])
		queryArgs := append([]any{label}, args...)
		for _, labelTable := range []string{"labels", "wisp_labels"} {
			rows, err := tx.QueryContext(ctx,
				fmt.Sprintf(`SELECT issue_id FROM %s WHERE label = ? AND issue_id IN (%s)`, labelTable, inClause),
				queryArgs...)
			if err != nil {
				return nil, fmt.Errorf("thread guard: read %s: %w", labelTable, err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("thread guard: scan %s: %w", labelTable, err)
				}
				labelled[id] = true
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("thread guard: read %s: %w", labelTable, err)
			}
		}
	}
	return labelled, nil
}
