package conformance

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.BlockedLister must satisfy: what issueops/blockedlister.go
// promises, asserted on every leg (dolt, embedded dolt, unit of work, and the
// served HTTP client) so `bd blocked` answers one listing whichever route it
// takes.
//
// EVERY CASE IS SCOPED BY A LABEL: the blocked set is a property of the whole
// database, not of an id prefix, so a row seeded by one case is inside the
// next one's answer unless each asks through Labels.

// BlockedListerFixture supplies adapter-specific storage access for the
// blocked-listing assertions.
type BlockedListerFixture struct {
	// IssuePrefix namespaces the ids each case seeds, so several cases can
	// share one database.
	IssuePrefix string
	// BlockedLister is the surface under test.
	BlockedLister publicops.BlockedLister
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge, recomputing the stored blocked flag the
	// way every dependency write does.
	AddDependency func(context.Context, *types.Dependency, string) error
	// MarkBlocked sets the stored is_blocked flag on one durable issue
	// directly, without the edge that would justify it: the stale flag `bd
	// recompute-blocked` exists to repair. It must fail unless exactly one row
	// changed. A nil hook means this leg cannot write the column, and the case
	// that needs it skips with that reason.
	MarkBlocked func(context.Context, string) error
}

// RunBlockedLister runs every blocked-listing case as a subtest of t.
func RunBlockedLister(t *testing.T, ctx context.Context, fixture BlockedListerFixture) {
	t.Helper()
	t.Run("NamesADirectBlocker", func(t *testing.T) { runBlockedNamesADirectBlocker(t, ctx, fixture) })
	t.Run("AttributesAChildToItsBlockedParent", func(t *testing.T) { runBlockedAttributesAChildToItsParent(t, ctx, fixture) })
	t.Run("ClosedBlockerDoesNotBlock", func(t *testing.T) { runBlockedClosedBlockerDoesNotBlock(t, ctx, fixture) })
	t.Run("IncludesTheWispPlane", func(t *testing.T) { runBlockedIncludesTheWispPlane(t, ctx, fixture) })
	t.Run("ExcludesThePinnedStatus", func(t *testing.T) { runBlockedExcludesThePinnedStatus(t, ctx, fixture) })
	t.Run("ParentFilter", func(t *testing.T) { runBlockedParentFilter(t, ctx, fixture) })
	t.Run("LabelFilters", func(t *testing.T) { runBlockedLabelFilters(t, ctx, fixture) })
	t.Run("Order", func(t *testing.T) { runBlockedOrder(t, ctx, fixture) })
	t.Run("NamesWaitsForAndConditionalBlockers", func(t *testing.T) { runBlockedNamesWaitsForAndConditionalBlockers(t, ctx, fixture) })
	t.Run("OmitsAStaleFlagWithNoNameableBlocker", func(t *testing.T) { runBlockedOmitsAStaleFlag(t, ctx, fixture) })
	t.Run("HasNoTypeExclusions", func(t *testing.T) { runBlockedHasNoTypeExclusions(t, ctx, fixture) })
	t.Run("EmptyIsAnEmptySlice", func(t *testing.T) { runBlockedEmptyIsAnEmptySlice(t, ctx, fixture) })
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) { runBlockedDoesNotMutateTheRequest(t, ctx, fixture) })
}

// runBlockedNamesADirectBlocker: an issue with an open `blocks` target is
// listed with that target as its one blocker; the target itself is not.
func runBlockedNamesADirectBlocker(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	label := f.IssuePrefix + "-bl-direct"
	blocked, blocker := blListerID(f, "direct-src"), blListerID(f, "direct-dst")
	blListerSeedIssue(t, ctx, f, blListerIssue(blocked, 1, label))
	blListerSeedIssue(t, ctx, f, blListerIssue(blocker, 1, label))
	blListerSeedEdge(t, ctx, f, blocked, blocker, types.DepBlocks)

	items := blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}})
	blListerAssertRows(t, items, map[string][]string{blocked: {blocker}})
}

// runBlockedAttributesAChildToItsParent: a child of a blocked parent, with no
// blocker of its own, is listed with its parent named as the blocker.
func runBlockedAttributesAChildToItsParent(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	label := f.IssuePrefix + "-bl-child"
	parent, child, blocker := blListerID(f, "child-parent"), blListerID(f, "child-child"), blListerID(f, "child-blocker")
	blListerSeedIssue(t, ctx, f, blListerIssue(parent, 1, label))
	blListerSeedIssue(t, ctx, f, blListerIssue(blocker, 1, label+"-other"))
	blListerSeedIssue(t, ctx, f, blListerIssue(child, 1, label))
	blListerSeedEdge(t, ctx, f, child, parent, types.DepParentChild)
	blListerSeedEdge(t, ctx, f, parent, blocker, types.DepBlocks)

	items := blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}})
	blListerAssertRows(t, items, map[string][]string{parent: {blocker}, child: {parent}})
}

// runBlockedClosedBlockerDoesNotBlock: a `blocks` edge onto a closed target
// names no blocker, so the source is not listed.
func runBlockedClosedBlockerDoesNotBlock(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	label := f.IssuePrefix + "-bl-closed"
	src, dst := blListerID(f, "closed-src"), blListerID(f, "closed-dst")
	blListerSeedIssue(t, ctx, f, blListerIssue(src, 1, label))
	closed := blListerIssue(dst, 1, label)
	closed.Status = types.StatusClosed
	blListerSeedIssue(t, ctx, f, closed)
	blListerSeedEdge(t, ctx, f, src, dst, types.DepBlocks)

	items := blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}})
	blListerAssertRows(t, items, map[string][]string{})
}

// runBlockedIncludesTheWispPlane: a blocked wisp is listed beside the issues.
func runBlockedIncludesTheWispPlane(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	label := f.IssuePrefix + "-bl-wisp"
	wisp, blocker := blListerID(f, "wisp-src"), blListerID(f, "wisp-dst")
	w := blListerIssue(wisp, 1, label)
	w.Ephemeral = true
	if err := f.CreateWisp(ctx, w, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", wisp, err)
	}
	b := blListerIssue(blocker, 1, label)
	b.Ephemeral = true
	if err := f.CreateWisp(ctx, b, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", blocker, err)
	}
	blListerSeedEdge(t, ctx, f, wisp, blocker, types.DepBlocks)

	items := blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}})
	blListerAssertRows(t, items, map[string][]string{wisp: {blocker}})
}

// runBlockedExcludesThePinnedStatus: a row whose status is pinned is never
// listed, whatever blocks it.
func runBlockedExcludesThePinnedStatus(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	label := f.IssuePrefix + "-bl-pinned"
	pinned, open, blocker := blListerID(f, "pinned-src"), blListerID(f, "pinned-open"), blListerID(f, "pinned-dst")
	p := blListerIssue(pinned, 1, label)
	p.Status = types.StatusPinned
	blListerSeedIssue(t, ctx, f, p)
	blListerSeedIssue(t, ctx, f, blListerIssue(open, 1, label))
	blListerSeedIssue(t, ctx, f, blListerIssue(blocker, 1, label+"-other"))
	blListerSeedEdge(t, ctx, f, pinned, blocker, types.DepBlocks)
	blListerSeedEdge(t, ctx, f, open, blocker, types.DepBlocks)

	items := blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}})
	blListerAssertRows(t, items, map[string][]string{open: {blocker}})
}

// runBlockedParentFilter: ParentID narrows to the parent's direct children.
func runBlockedParentFilter(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	label := f.IssuePrefix + "-bl-parent"
	parent, kid, stranger, blocker := blListerID(f, "pf-parent"), blListerID(f, "pf-kid"), blListerID(f, "pf-stranger"), blListerID(f, "pf-blocker")
	blListerSeedIssue(t, ctx, f, blListerIssue(parent, 1, label+"-other"))
	blListerSeedIssue(t, ctx, f, blListerIssue(blocker, 1, label+"-other"))
	blListerSeedIssue(t, ctx, f, blListerIssue(kid, 1, label))
	blListerSeedIssue(t, ctx, f, blListerIssue(stranger, 1, label))
	blListerSeedEdge(t, ctx, f, kid, parent, types.DepParentChild)
	blListerSeedEdge(t, ctx, f, kid, blocker, types.DepBlocks)
	blListerSeedEdge(t, ctx, f, stranger, blocker, types.DepBlocks)

	all := blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}})
	blListerAssertRows(t, all, map[string][]string{kid: {blocker}, stranger: {blocker}})
	narrowed := blListerList(t, ctx, f, publicops.BlockedRequest{ParentID: parent, Labels: []string{label}})
	blListerAssertRows(t, narrowed, map[string][]string{kid: {blocker}})
}

// runBlockedLabelFilters: Labels is AND, LabelsAny is OR, ExcludeLabels
// removes, and every value is trimmed with blanks dropped.
func runBlockedLabelFilters(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	base := f.IssuePrefix + "-bl-lab"
	red, blue := base+"-red", base+"-blue"
	both, onlyRed, onlyBlue, blocker := blListerID(f, "lab-both"), blListerID(f, "lab-red"), blListerID(f, "lab-blue"), blListerID(f, "lab-blocker")
	blListerSeedIssue(t, ctx, f, blListerIssue(both, 1, base, red, blue))
	blListerSeedIssue(t, ctx, f, blListerIssue(onlyRed, 1, base, red))
	blListerSeedIssue(t, ctx, f, blListerIssue(onlyBlue, 1, base, blue))
	blListerSeedIssue(t, ctx, f, blListerIssue(blocker, 1, base+"-other"))
	for _, id := range []string{both, onlyRed, onlyBlue} {
		blListerSeedEdge(t, ctx, f, id, blocker, types.DepBlocks)
	}
	want := func(ids ...string) map[string][]string {
		m := map[string][]string{}
		for _, id := range ids {
			m[id] = []string{blocker}
		}
		return m
	}

	blListerAssertRows(t, blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{" " + red + " ", "", blue}}), want(both))
	blListerAssertRows(t, blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{base}, LabelsAny: []string{red, blue}}), want(both, onlyRed, onlyBlue))
	blListerAssertRows(t, blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{base}, ExcludeLabels: []string{" " + blue}}), want(onlyRed))
}

// runBlockedOrder: priority ascending, then created_at descending, then id.
//
// The created_at values are seeded explicitly, so every key decides at least
// one pair: two rows share a priority and differ in created_at (newest first),
// and five share both priority and created_at (the id breaks the tie). The
// expected order is written out rather than recomputed from the answer.
func runBlockedOrder(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	label := f.IssuePrefix + "-bl-order"
	blocker := blListerID(f, "order-blocker")
	blListerSeedIssue(t, ctx, f, blListerIssue(blocker, 1, label+"-other"))
	older := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	// Two of the five tied rows are wisps. The store body assembles rows
	// issues-plane first, each plane in id order, so the ids alone would put
	// the tied run in the wrong order (b, d, e, then a, c); only the id
	// tiebreak puts it right.
	rows := []struct {
		name     string
		priority int
		created  time.Time
		wisp     bool
	}{
		{"order-p3", 3, newer, false},
		{"order-p0-older", 0, older, false},
		{"order-p0-newer-e", 0, newer, false},
		{"order-p0-newer-c", 0, newer, true},
		{"order-p0-newer-a", 0, newer, true},
		{"order-p0-newer-d", 0, newer, false},
		{"order-p0-newer-b", 0, newer, false},
		{"order-p1", 1, older, false},
	}
	for _, row := range rows {
		issue := blListerIssue(blListerID(f, row.name), row.priority, label)
		issue.CreatedAt = row.created
		issue.UpdatedAt = row.created
		if row.wisp {
			issue.Ephemeral = true
			if err := f.CreateWisp(ctx, issue, "seed"); err != nil {
				t.Fatalf("seed wisp %s: %v", issue.ID, err)
			}
		} else {
			blListerSeedIssue(t, ctx, f, issue)
		}
		blListerSeedEdge(t, ctx, f, issue.ID, blocker, types.DepBlocks)
	}
	want := []string{
		blListerID(f, "order-p0-newer-a"),
		blListerID(f, "order-p0-newer-b"),
		blListerID(f, "order-p0-newer-c"),
		blListerID(f, "order-p0-newer-d"),
		blListerID(f, "order-p0-newer-e"),
		blListerID(f, "order-p0-older"),
		blListerID(f, "order-p1"),
		blListerID(f, "order-p3"),
	}
	got := blListerIDs(blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}}))
	if !slices.Equal(got, want) {
		t.Errorf("listing order = %v, want %v (priority ascending, created_at descending, id)", got, want)
	}
}

// runBlockedNamesWaitsForAndConditionalBlockers: a waits-for onto a spawner
// whose child is still open, and a conditional-blocks onto an open target,
// each name their target, as a blocks edge does.
func runBlockedNamesWaitsForAndConditionalBlockers(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	label := f.IssuePrefix + "-bl-wf"
	spawner, spawned := blListerID(f, "wf-spawner"), blListerID(f, "wf-spawned")
	waiter := blListerID(f, "wf-waiter")
	target, conditional := blListerID(f, "cb-target"), blListerID(f, "cb-src")
	blListerSeedIssue(t, ctx, f, blListerIssue(spawner, 1, label+"-other"))
	blListerSeedIssue(t, ctx, f, blListerIssue(spawned, 1, label+"-other"))
	blListerSeedIssue(t, ctx, f, blListerIssue(target, 1, label+"-other"))
	blListerSeedIssue(t, ctx, f, blListerIssue(waiter, 1, label))
	blListerSeedIssue(t, ctx, f, blListerIssue(conditional, 1, label))
	blListerSeedEdge(t, ctx, f, spawned, spawner, types.DepParentChild)
	blListerSeedEdge(t, ctx, f, waiter, spawner, types.DepWaitsFor)
	blListerSeedEdge(t, ctx, f, conditional, target, types.DepConditionalBlocks)

	items := blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}})
	blListerAssertRows(t, items, map[string][]string{waiter: {spawner}, conditional: {target}})
}

// runBlockedOmitsAStaleFlag: a row whose stored flag says blocked but which
// has no active blocker and no parent names no blocker, so it is not listed.
// This is the narrowing gc's readiness projection documents relying on.
func runBlockedOmitsAStaleFlag(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	if f.MarkBlocked == nil {
		t.Skip("this leg cannot write the stored is_blocked flag directly")
	}
	label := f.IssuePrefix + "-bl-stale"
	stale, real, blocker := blListerID(f, "stale-src"), blListerID(f, "stale-real"), blListerID(f, "stale-blocker")
	blListerSeedIssue(t, ctx, f, blListerIssue(stale, 1, label))
	blListerSeedIssue(t, ctx, f, blListerIssue(real, 1, label))
	blListerSeedIssue(t, ctx, f, blListerIssue(blocker, 1, label+"-other"))
	blListerSeedEdge(t, ctx, f, real, blocker, types.DepBlocks)
	if err := f.MarkBlocked(ctx, stale); err != nil {
		t.Fatalf("mark %s blocked: %v", stale, err)
	}

	items := blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}})
	blListerAssertRows(t, items, map[string][]string{real: {blocker}})
}

// runBlockedHasNoTypeExclusions: a gate is listed like any other row; the
// blocked door excludes no issue type (unlike the ready front).
func runBlockedHasNoTypeExclusions(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	label := f.IssuePrefix + "-bl-type"
	gate, epic, blocker := blListerID(f, "type-gate"), blListerID(f, "type-epic"), blListerID(f, "type-blocker")
	g := blListerIssue(gate, 1, label)
	g.IssueType = types.TypeGate
	g.AwaitType = "human"
	blListerSeedIssue(t, ctx, f, g)
	e := blListerIssue(epic, 1, label)
	e.IssueType = types.TypeEpic
	blListerSeedIssue(t, ctx, f, e)
	blListerSeedIssue(t, ctx, f, blListerIssue(blocker, 1, label+"-other"))
	blListerSeedEdge(t, ctx, f, gate, blocker, types.DepBlocks)
	blListerSeedEdge(t, ctx, f, epic, blocker, types.DepBlocks)

	items := blListerList(t, ctx, f, publicops.BlockedRequest{Labels: []string{label}})
	blListerAssertRows(t, items, map[string][]string{gate: {blocker}, epic: {blocker}})
}

// runBlockedEmptyIsAnEmptySlice: nothing blocked is an empty, non-nil slice
// and a nil error.
func runBlockedEmptyIsAnEmptySlice(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	result, err := f.BlockedLister.ListBlocked(ctx, publicops.BlockedRequest{Labels: []string{f.IssuePrefix + "-bl-nothing"}})
	if err != nil {
		t.Fatalf("ListBlocked: %v", err)
	}
	if result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("Items = %#v, want an empty non-nil slice", result.Items)
	}
}

// runBlockedDoesNotMutateTheRequest: the caller's label slices come back as
// they were sent, normalization notwithstanding.
func runBlockedDoesNotMutateTheRequest(t *testing.T, ctx context.Context, f BlockedListerFixture) {
	req := publicops.BlockedRequest{
		ParentID:      " ",
		Labels:        []string{" a ", "", "a"},
		LabelsAny:     []string{" b"},
		ExcludeLabels: []string{"c "},
	}
	before := publicops.BlockedRequest{
		ParentID:      req.ParentID,
		Labels:        slices.Clone(req.Labels),
		LabelsAny:     slices.Clone(req.LabelsAny),
		ExcludeLabels: slices.Clone(req.ExcludeLabels),
	}
	if _, err := f.BlockedLister.ListBlocked(ctx, req); err != nil {
		t.Fatalf("ListBlocked: %v", err)
	}
	if !reflect.DeepEqual(req, before) {
		t.Errorf("request after = %#v, before = %#v", req, before)
	}
}

func blListerID(f BlockedListerFixture, name string) string {
	return fmt.Sprintf("%s-%s", f.IssuePrefix, name)
}

func blListerIssue(id string, priority int, labels ...string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  priority,
		IssueType: types.TypeTask,
		Labels:    labels,
	}
}

func blListerSeedIssue(t *testing.T, ctx context.Context, f BlockedListerFixture, issue *types.Issue) {
	t.Helper()
	if err := f.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

func blListerSeedEdge(t *testing.T, ctx context.Context, f BlockedListerFixture, from, to string, kind types.DependencyType) {
	t.Helper()
	if err := f.AddDependency(ctx, &types.Dependency{IssueID: from, DependsOnID: to, Type: kind}, "seed"); err != nil {
		t.Fatalf("seed %s edge %s -> %s: %v", kind, from, to, err)
	}
}

func blListerList(t *testing.T, ctx context.Context, f BlockedListerFixture, req publicops.BlockedRequest) []*types.BlockedIssue {
	t.Helper()
	result, err := f.BlockedLister.ListBlocked(ctx, req)
	if err != nil {
		t.Fatalf("ListBlocked(%+v): %v", req, err)
	}
	if result.Items == nil {
		t.Fatalf("ListBlocked(%+v) Items is nil; the role promises a non-nil slice", req)
	}
	return result.Items
}

func blListerIDs(items []*types.BlockedIssue) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

// assertBlockedRows requires the listing to be exactly want: the same ids,
// each naming exactly the blockers want gives it (in any order), with
// BlockedByCount equal to their number.
func blListerAssertRows(t *testing.T, items []*types.BlockedIssue, want map[string][]string) {
	t.Helper()
	got := map[string][]string{}
	for _, item := range items {
		if item == nil {
			t.Fatalf("listing carries a nil row")
		}
		if _, dup := got[item.ID]; dup {
			t.Errorf("row %s listed twice", item.ID)
		}
		blockers := slices.Clone(item.BlockedBy)
		slices.Sort(blockers)
		got[item.ID] = blockers
		if item.BlockedByCount != len(item.BlockedBy) {
			t.Errorf("row %s: BlockedByCount = %d, len(BlockedBy) = %d", item.ID, item.BlockedByCount, len(item.BlockedBy))
		}
	}
	normalized := map[string][]string{}
	for id, blockers := range want {
		b := slices.Clone(blockers)
		slices.Sort(b)
		normalized[id] = b
	}
	if !reflect.DeepEqual(got, normalized) {
		t.Errorf("listing = %v, want %v", got, normalized)
	}
}
