package flatfile

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// GetReadyWork returns unblocked workable issues. Mirrors
// sqlbuild.BuildReadyWorkWhere + issueops.GetReadyWorkInTx: status open or
// in_progress (unless an explicit status filter is given), not pinned, not
// is_blocked (blocking edges to active targets, plus inherited parent
// blocking), infra issue types excluded, deferred issues and children of
// deferred parents excluded, and ParentID filtering follows transitive
// parent-child descendants.
func (s *FlatFileStore) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}

	return readyWorkFromSnapshot(all, filter), nil
}

// readyWorkFromSnapshot computes the ready set over one preloaded issue
// snapshot, so callers needing extra projections (counts) can reuse the same
// snapshot instead of re-reading the store.
func readyWorkFromSnapshot(all []*types.Issue, filter types.WorkFilter) []*types.Issue {
	blocked := computeBlockedSet(all)

	excludeTypes := make(map[types.IssueType]bool)
	if filter.Type == "" {
		for _, t := range sqlbuild.ReadyWorkExcludeTypes(filter.ExcludeTypes) {
			excludeTypes[t] = true
		}
	}

	var descendants map[string]bool
	if filter.ParentID != nil {
		descendants = parentDescendants(all, *filter.ParentID)
	}

	var deferredChildren map[string]bool
	if !filter.IncludeDeferred {
		deferredChildren = childrenOfDeferredParents(all)
	}

	var result []*types.Issue
	for _, issue := range all {
		if filter.Status != "" {
			if issue.Status != filter.Status {
				continue
			}
		} else if issue.Status != types.StatusOpen && issue.Status != types.StatusInProgress {
			continue
		}
		if issue.Pinned || blocked[issue.ID] {
			continue
		}
		if issue.Ephemeral {
			if !filter.IncludeEphemeral {
				continue
			}
			// MolType/WispType only constrain the wisp arm of the SQL ready
			// set (issueops.readyWorkWispIssueFilter); durable issues pass
			// regardless. Flatfile wisps are the ephemeral issues.
			if filter.MolType != nil && issue.MolType != *filter.MolType {
				continue
			}
			if filter.WispType != nil && issue.WispType != *filter.WispType {
				continue
			}
		}
		// LabelsAny is an OR-set fencing the whole ready/claim path (durable
		// and wisp arms alike, mirroring sqlbuild.BuildReadyWorkWhere): an
		// issue qualifies only if it carries at least one of the labels. It
		// AND-combines with the Labels AND-set.
		if len(filter.LabelsAny) > 0 {
			labelSet := makeStringSet(issue.Labels)
			found := false
			for _, l := range filter.LabelsAny {
				if labelSet[l] {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if filter.Type != "" {
			if string(issue.IssueType) != filter.Type {
				continue
			}
		} else if excludeTypes[issue.IssueType] {
			continue
		}
		if !filter.IncludeDeferred {
			if isDeferred(issue) || deferredChildren[issue.ID] {
				continue
			}
		}
		// Explicit transitive descendants, plus the sqlbuild implicit-child
		// fallback: a dotted-prefix ID with no parent-child dep at all.
		if filter.ParentID != nil && !descendants[issue.ID] && !isImplicitDottedChild(issue, *filter.ParentID) {
			continue
		}
		if filter.MoleculeID != "" && !isMoleculeMember(issue, filter.MoleculeID) {
			continue
		}
		if !matchesBasicWorkFilter(issue, filter) {
			continue
		}
		// Metadata predicates mirror sqlbuild.AppendMetadataClauses, which
		// BuildReadyWorkWhere applies to the ready set.
		if !matchesMetadataFilter(issue, filter.MetadataFields, filter.HasMetadataKey) {
			continue
		}
		result = append(result, issue)
	}

	sortReadyIssues(result, filter.SortPolicy)

	if filter.Offset > 0 && filter.Offset < len(result) {
		result = result[filter.Offset:]
	} else if filter.Offset > 0 && filter.Offset >= len(result) {
		return nil
	}
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}

	return result
}

// parentDescendants returns the set of transitive parent-child descendants of
// rootID (mirrors issueops.GetDescendantIDsInTx).
func parentDescendants(all []*types.Issue, rootID string) map[string]bool {
	children := make(map[string][]string)
	for _, issue := range all {
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepParentChild {
				children[dep.DependsOnID] = append(children[dep.DependsOnID], issue.ID)
			}
		}
	}
	descendants := make(map[string]bool)
	queue := []string{rootID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, child := range children[id] {
			if !descendants[child] {
				descendants[child] = true
				queue = append(queue, child)
			}
		}
	}
	return descendants
}

// isImplicitDottedChild reports whether issue is an implicit child of
// parentID: its ID has the dotted parent prefix but it carries no explicit
// parent-child dependency. Mirrors the sqlbuild ready-work parent clause
// (id LIKE 'parent.%' AND id NOT IN parent-child issue_ids), which catches
// dotted-ID children whose dependency row was never created or was lost.
func isImplicitDottedChild(issue *types.Issue, parentID string) bool {
	if !strings.HasPrefix(issue.ID, parentID+".") {
		return false
	}
	for _, dep := range issue.Dependencies {
		if dep.Type == types.DepParentChild {
			return false
		}
	}
	return true
}

// isMoleculeMember mirrors the sqlbuild MoleculeID clause: a direct
// parent-child dependency on moleculeID, or a dotted-prefix ID with no
// explicit parent-child dependency at all.
func isMoleculeMember(issue *types.Issue, moleculeID string) bool {
	for _, dep := range issue.Dependencies {
		if dep.Type == types.DepParentChild && dep.DependsOnID == moleculeID {
			return true
		}
	}
	return isImplicitDottedChild(issue, moleculeID)
}

// childrenOfDeferredParents returns IDs of issues whose parent has a future
// defer_until (mirrors issueops.getChildrenOfDeferredParentsInTx).
func childrenOfDeferredParents(all []*types.Issue) map[string]bool {
	now := time.Now().UTC()
	deferredParents := make(map[string]bool)
	for _, issue := range all {
		if issue.DeferUntil != nil && issue.DeferUntil.After(now) {
			deferredParents[issue.ID] = true
		}
	}
	if len(deferredParents) == 0 {
		return nil
	}
	children := make(map[string]bool)
	for _, issue := range all {
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepParentChild && deferredParents[dep.DependsOnID] {
				children[issue.ID] = true
				break
			}
		}
	}
	return children
}

// GetReadyWorkWithCounts is GetReadyWork plus dependency/comment counts.
// Counts are blocks-only, mirroring the ready-with-counts SQL projection.
func (s *FlatFileStore) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	// One snapshot feeds both the ready set and the counts, so a concurrent
	// write cannot skew DependentCount/Parent against the issues returned.
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	issues := readyWorkFromSnapshot(all, filter)

	dependentCounts := make(map[string]int)
	parentMap := make(map[string]string)
	for _, a := range all {
		for _, dep := range a.Dependencies {
			if dep.Type == types.DepParentChild {
				// With multiple parent-child deps, sqlbuild.SearchCountsSQL
				// projects MIN(target) AS parent_id — keep the
				// lexicographically smallest, not the last seen.
				if p, ok := parentMap[a.ID]; !ok || dep.DependsOnID < p {
					parentMap[a.ID] = dep.DependsOnID
				}
			} else if dep.Type == types.DepBlocks {
				dependentCounts[dep.DependsOnID]++
			}
		}
	}

	var result []*types.IssueWithCounts
	for _, issue := range issues {
		dc := 0
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepBlocks {
				dc++
			}
		}
		// I/O failures must surface, not report CommentCount=0: the SQL
		// reference propagates errors from the comment-count subquery, and
		// loadAllIssues treats read errors as fatal for the same reason.
		cc, err := s.countCommentFiles(issue.ID)
		if err != nil {
			return nil, err
		}
		wc := &types.IssueWithCounts{
			Issue:           issue,
			DependencyCount: dc,
			DependentCount:  dependentCounts[issue.ID],
			CommentCount:    cc,
		}
		if p, ok := parentMap[issue.ID]; ok {
			wc.Parent = &p
		}
		result = append(result, wc)
	}
	return result, nil
}

// CountReadyWork returns the total ready-work count for the filter,
// ignoring pagination, satisfying storage.ReadyWorkCounter. It equals
// len(GetReadyWork(filter with Limit=0)); with an in-memory scan there is
// no cheaper count than materializing the ready set.
func (s *FlatFileStore) CountReadyWork(ctx context.Context, filter types.WorkFilter) (int, error) {
	filter.Limit = 0
	filter.Offset = 0
	issues, err := s.GetReadyWork(ctx, filter)
	if err != nil {
		return 0, err
	}
	return len(issues), nil
}

// GetBlockedIssues mirrors issueops.GetBlockedIssuesInTx: the blocked set is
// the is_blocked projection; each blocked issue lists its direct active
// blockers (blocks/waits-for/conditional-blocks edges), and an issue blocked
// only by inheritance substitutes its parent as the sole blocker. Of the
// WorkFilter fields the reference applies ONLY ParentID — Status, Type,
// Priority, Assignee, Unassigned, label fields, ExcludeTypes, and Limit are
// all ignored there, so flatfile ignores them too.
func (s *FlatFileStore) GetBlockedIssues(_ context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}

	blocked := computeBlockedSet(all)
	statusMap := make(map[string]types.Status, len(all))
	for _, issue := range all {
		statusMap[issue.ID] = issue.Status
	}

	var result []*types.BlockedIssue
	for _, issue := range all {
		if !blocked[issue.ID] {
			continue
		}
		if filter.ParentID != nil && !isBlockedScopeChild(issue, *filter.ParentID) {
			continue
		}

		var blockers []string
		var parentID string
		for _, dep := range issue.Dependencies {
			switch dep.Type {
			case types.DepParentChild:
				parentID = dep.DependsOnID
			case types.DepBlocks, types.DepWaitsFor, types.DepConditionalBlocks:
				if st, ok := statusMap[dep.DependsOnID]; ok && activeStatus(st) {
					blockers = append(blockers, dep.DependsOnID)
				}
			default:
			}
		}
		if len(blockers) == 0 {
			// Inherited blocking: substitute the parent as the blocker.
			if parentID == "" {
				continue
			}
			blockers = []string{parentID}
		}

		// SQL backends project blocked rows without hydrating Dependencies
		// (blocked_by carries the blocking edges); the flat-file issue struct
		// embeds them, so clear the copy to keep `bd blocked --json` shape
		// identical across backends.
		projected := *issue
		projected.Dependencies = nil
		result = append(result, &types.BlockedIssue{
			Issue:          projected,
			BlockedByCount: len(blockers),
			BlockedBy:      blockers,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Issue.Priority != result[j].Issue.Priority {
			return result[i].Issue.Priority < result[j].Issue.Priority
		}
		return result[i].Issue.CreatedAt.After(result[j].Issue.CreatedAt)
	})

	return result, nil
}

// isBlockedScopeChild mirrors the GetBlockedIssuesInTx ParentID scope: direct
// parent-child children of parentID plus any dotted-prefix ID
// (strings.HasPrefix(id, parentID+".")).
func isBlockedScopeChild(issue *types.Issue, parentID string) bool {
	if strings.HasPrefix(issue.ID, parentID+".") {
		return true
	}
	for _, dep := range issue.Dependencies {
		if dep.Type == types.DepParentChild && dep.DependsOnID == parentID {
			return true
		}
	}
	return false
}

// GetEpicsEligibleForClosure returns epics where all children are closed.
func (s *FlatFileStore) GetEpicsEligibleForClosure(_ context.Context) ([]*types.EpicStatus, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}

	// Build parent → children map.
	children := make(map[string][]*types.Issue)
	for _, issue := range all {
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepParentChild {
				children[dep.DependsOnID] = append(children[dep.DependsOnID], issue)
			}
		}
	}

	var result []*types.EpicStatus
	for _, issue := range all {
		if issue.IssueType != types.TypeEpic || issue.Status == types.StatusClosed {
			continue
		}
		// issueops.GetEpicsEligibleForClosureInTx selects epics FROM issues
		// only, so an ephemeral epic is never a candidate. Children stay
		// unfiltered: the SQL side reads child deps and statuses from both
		// the durable and wisp tables.
		if issue.Ephemeral {
			continue
		}
		kids := children[issue.ID]
		if len(kids) == 0 {
			continue
		}
		closed := 0
		for _, kid := range kids {
			if kid.Status == types.StatusClosed {
				closed++
			}
		}
		result = append(result, &types.EpicStatus{
			Epic:             issue,
			TotalChildren:    len(kids),
			ClosedChildren:   closed,
			EligibleForClose: closed == len(kids),
		})
	}
	return result, nil
}

// isDeferred mirrors the SQL deferred clause (defer_until IS NULL OR
// defer_until <= now): only a future defer_until hides an issue. Status alone
// must not: an explicit Status=deferred filter still returns issues whose
// defer_until is unset or past, matching sqlbuild.BuildReadyWorkWhere.
func isDeferred(issue *types.Issue) bool {
	return issue.DeferUntil != nil && issue.DeferUntil.After(time.Now())
}

// matchesBasicWorkFilter applies the WorkFilter fields that are common to the
// ready and blocked paths: priority, assignee, and label constraints.
func matchesBasicWorkFilter(issue *types.Issue, f types.WorkFilter) bool {
	if f.Priority != nil && issue.Priority != *f.Priority {
		return false
	}
	// Unassigned takes precedence over Assignee, mirroring the else-if in
	// sqlbuild.BuildReadyWorkWhere and issueops.readyWorkWispIssueFilter:
	// when both are set, SQL ignores Assignee entirely.
	if f.Unassigned {
		if issue.Assignee != "" {
			return false
		}
	} else if f.Assignee != nil && issue.Assignee != *f.Assignee {
		return false
	}
	if len(f.Labels) > 0 {
		labelSet := makeStringSet(issue.Labels)
		for _, l := range f.Labels {
			if !labelSet[l] {
				return false
			}
		}
	}
	if len(f.ExcludeLabels) > 0 {
		labelSet := makeStringSet(issue.Labels)
		for _, l := range f.ExcludeLabels {
			if labelSet[l] {
				return false
			}
		}
	}
	return true
}

// sortReadyIssues mirrors issueops.sortReadyIssues: hybrid puts recent
// (<48h) issues first ordered by priority, older issues by created_at;
// oldest is created_at ASC; priority is priority ASC then created_at DESC.
// ID is the final tie-break everywhere.
func sortReadyIssues(issues []*types.Issue, policy types.SortPolicy) {
	recentCutoff := time.Now().UTC().Add(-48 * time.Hour)
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		switch policy {
		case types.SortPolicyOldest:
			return issueCreatedBefore(a, b)
		case types.SortPolicyPriority:
			return issuePriorityBefore(a, b)
		case types.SortPolicyHybrid, "":
			aRecent := !a.CreatedAt.Before(recentCutoff)
			bRecent := !b.CreatedAt.Before(recentCutoff)
			if aRecent != bRecent {
				return aRecent
			}
			if aRecent && a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			return issueCreatedBefore(a, b)
		default:
			return issuePriorityBefore(a, b)
		}
	})
}

func issuePriorityBefore(a, b *types.Issue) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID < b.ID
}

func issueCreatedBefore(a, b *types.Issue) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}
