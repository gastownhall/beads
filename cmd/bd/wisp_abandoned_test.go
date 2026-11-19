package main

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestWispIsAbandoned pins the #7236 fix: --age 0s (ageThreshold <= 0) must
// treat every wisp as abandoned regardless of its updatedAt, including a
// wisp whose stored updatedAt reads back AFTER now — the shape Dolt's
// DATETIME(0) half-up rounding on write produces for a wisp created in the
// second half of a wall-clock second (#7236). Before this fix, that case
// failed the strict now.Sub(updatedAt) > ageThreshold comparison because
// the difference is negative.
func TestWispIsAbandoned(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		updatedAt time.Time
		age       time.Duration
		want      bool
	}{
		{
			name:      "age 0s: a wisp updated exactly now is abandoned",
			updatedAt: now,
			age:       0,
			want:      true,
		},
		{
			name:      "age 0s: a wisp whose stored updatedAt rounded into the future is still abandoned",
			updatedAt: now.Add(500 * time.Millisecond),
			age:       0,
			want:      true,
		},
		{
			name:      "a negative age behaves like zero",
			updatedAt: now.Add(500 * time.Millisecond),
			age:       -time.Second,
			want:      true,
		},
		{
			name:      "positive age: a fresh wisp is not abandoned",
			updatedAt: now,
			age:       time.Hour,
			want:      false,
		},
		{
			name:      "positive age: a wisp past the threshold is abandoned",
			updatedAt: now.Add(-2 * time.Hour),
			age:       time.Hour,
			want:      true,
		},
		{
			name:      "positive age: exactly at the threshold is not abandoned (strict >, unchanged)",
			updatedAt: now.Add(-time.Hour),
			age:       time.Hour,
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wispIsAbandoned(now, tc.updatedAt, tc.age); got != tc.want {
				t.Errorf("wispIsAbandoned(%v, %v, %v) = %v, want %v", now, tc.updatedAt, tc.age, got, tc.want)
			}
		})
	}
}

// fixedWispsMolReader serves findAbandonedWisps a fixed set of wisps with
// nothing blocked, no custom statuses and no dependents. It embeds the
// molReader INTERFACE, so a read the age scan does not make today panics
// rather than quietly answering zero.
type fixedWispsMolReader struct {
	molReader
	wisps []*types.Issue
}

func (r fixedWispsMolReader) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return r.wisps, nil
}
func (fixedWispsMolReader) GetBlockedIssues(context.Context, types.WorkFilter) ([]*types.BlockedIssue, error) {
	return nil, nil
}
func (fixedWispsMolReader) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}
func (fixedWispsMolReader) IsInfraTypeCtx(context.Context, types.IssueType) bool { return false }
func (fixedWispsMolReader) FindWispDependentsRecursive(context.Context, []string) (map[string]bool, error) {
	return nil, nil
}

// TestFindAbandonedWispsAgesAtStoragePrecision pins the call site that
// TestWispIsAbandoned cannot see: findAbandonedWisps must decide age through
// wispIsAbandoned. A wisp written at 12:00:00.7 is stored as 12:00:01, and a
// gc at 12:00:00.9 with --age 0s must still reclaim it; the raw
// now.Sub(updated_at) > threshold that #7236 replaced reads -100ms and misses
// it. A one-hour threshold must leave the same wisp alone at that instant and
// reclaim it two hours later, so dropping the age check fails too. --age 1s
// at 12:00:01.5 must leave it alone: only half a second has passed since the
// stored second, so a positive threshold never credits idle time that has
// not elapsed.
func TestFindAbandonedWispsAgesAtStoragePrecision(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	wisp := &types.Issue{
		ID:        "gc-idle",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Ephemeral: true,
		UpdatedAt: base.Add(700 * time.Millisecond).Round(time.Second), // 12:00:01
	}
	old := wispGCNow
	t.Cleanup(func() { wispGCNow = old })

	for _, tc := range []struct {
		now       time.Time
		threshold time.Duration
		want      int
	}{
		{base.Add(900 * time.Millisecond), 0, 1},
		{base.Add(900 * time.Millisecond), time.Hour, 0},
		{base.Add(2 * time.Hour), time.Hour, 1},
		{base.Add(1500 * time.Millisecond), time.Second, 0},
	} {
		wispGCNow = func() time.Time { return tc.now }
		got, err := findAbandonedWisps(context.Background(), fixedWispsMolReader{wisps: []*types.Issue{wisp}}, false, tc.threshold, nil)
		if err != nil {
			t.Fatalf("findAbandonedWisps(--age %s): %v", tc.threshold, err)
		}
		if len(got) != tc.want {
			t.Errorf("findAbandonedWisps(--age %s) at %s, updated_at 12:00:01: got %d wisps, want %d",
				tc.threshold, tc.now.Format("15:04:05.000"), len(got), tc.want)
		}
	}
}

// closedWispScanReader serves selectClosedWispsForPurge a fixed list of rows
// and records every filter the scan sends. With honorFilter it applies Status,
// Ephemeral and UpdatedBefore and then Limit the way the SQL scan does
// (predicates first, limit second); without it, it returns every row, so the
// Go-side guard is all that keeps a recent row out. Infra means the "agent"
// type. deps are the dependency edges between rows, and dependentReads, when
// set, records the targets of each read of them; a read past
// maxDependentReads of them, when that is set, fails, so a walk that does not
// end fails instead of hanging.
type closedWispScanReader struct {
	molReader
	rows              []*types.Issue
	honorFilter       bool
	filters           *[]types.IssueFilter
	deps              []*types.Dependency
	dependentReads    *[][]string
	maxDependentReads int
}

func (r closedWispScanReader) GetDependentRecordsForIssues(_ context.Context, targetIDs []string) (map[string][]*types.Dependency, error) {
	if r.dependentReads != nil {
		*r.dependentReads = append(*r.dependentReads, slices.Clone(targetIDs))
		if r.maxDependentReads > 0 && len(*r.dependentReads) > r.maxDependentReads {
			return nil, fmt.Errorf("the walk did not end after %d dependents reads", r.maxDependentReads)
		}
	}
	out := map[string][]*types.Dependency{}
	for _, dep := range r.deps {
		if slices.Contains(targetIDs, dep.DependsOnID) {
			out[dep.DependsOnID] = append(out[dep.DependsOnID], dep)
		}
	}
	return out, nil
}

func (r closedWispScanReader) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, issue := range r.rows {
		if slices.Contains(ids, issue.ID) {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (r closedWispScanReader) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	*r.filters = append(*r.filters, filter)
	if !r.honorFilter {
		return r.rows, nil
	}
	var out []*types.Issue
	for _, issue := range r.rows {
		if filter.Status != nil && issue.Status != *filter.Status {
			continue
		}
		if filter.Ephemeral != nil && issue.Ephemeral != *filter.Ephemeral {
			continue
		}
		if filter.UpdatedBefore != nil && !issue.UpdatedAt.Before(*filter.UpdatedBefore) {
			continue
		}
		if filter.Limit > 0 && len(out) == filter.Limit {
			break
		}
		out = append(out, issue)
	}
	return out, nil
}

func (closedWispScanReader) IsInfraTypeCtx(_ context.Context, t types.IssueType) bool {
	return t == "agent"
}

func closedWispAt(id string, updated time.Time) *types.Issue {
	return &types.Issue{ID: id, Status: types.StatusClosed, Ephemeral: true, IssueType: types.TypeTask, UpdatedAt: updated}
}

func closedWispIDs(issues []*types.Issue) []string {
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	return ids
}

// TestWispGCClosedSelectorHonorsAge pins GH#8202's closed-GC age floor: the
// scan sends now-age as UpdatedBefore with the 5000-row limit, so a full page
// of recently closed wisps cannot crowd out an old one, and the Go-side guard
// keeps out a recent row from a reader that ignores the bound. A row exactly
// at the cutoff is kept, matching the strict comparison of wispIsAbandoned.
func TestWispGCClosedSelectorHonorsAge(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := wispGCNow
	t.Cleanup(func() { wispGCNow = old })
	wispGCNow = func() time.Time { return now }

	t.Run("bound applies before the scan limit", func(t *testing.T) {
		var rows []*types.Issue
		for i := 0; i < wispPurgeScanLimit; i++ {
			rows = append(rows, closedWispAt(fmt.Sprintf("gc-recent-%d", i), now.Add(-time.Minute)))
		}
		rows = append(rows, closedWispAt("gc-old", now.Add(-2*time.Hour)))
		var filters []types.IssueFilter
		sel, err := selectClosedWispsForPurge(context.Background(), closedWispScanReader{rows: rows, honorFilter: true, filters: &filters}, time.Hour, nil)
		if err != nil {
			t.Fatalf("selectClosedWispsForPurge: %v", err)
		}
		if got := closedWispIDs(sel.candidates); !slices.Equal(got, []string{"gc-old"}) {
			t.Errorf("candidates = %d rows (first %v), want only gc-old: recent rows filled the 5000-row scan", len(got), got[:min(len(got), 3)])
		}
		if len(filters) != 1 {
			t.Fatalf("scan sent %d queries, want 1", len(filters))
		}
		f := filters[0]
		if f.UpdatedBefore == nil || !f.UpdatedBefore.Equal(now.Add(-time.Hour)) {
			t.Errorf("UpdatedBefore = %v, want %v", f.UpdatedBefore, now.Add(-time.Hour))
		}
		if f.Limit != wispPurgeScanLimit || f.Status == nil || *f.Status != types.StatusClosed || f.Ephemeral == nil || !*f.Ephemeral {
			t.Errorf("scan filter = Limit %d Status %v Ephemeral %v, want 5000 closed ephemeral", f.Limit, f.Status, f.Ephemeral)
		}
	})

	t.Run("go-side guard", func(t *testing.T) {
		rows := []*types.Issue{
			closedWispAt("gc-old", now.Add(-2*time.Hour)),
			closedWispAt("gc-recent", now.Add(-10*time.Minute)),
			closedWispAt("gc-at-cutoff", now.Add(-time.Hour)),
			closedWispAt("gc-just-past", now.Add(-time.Hour-time.Second)),
		}
		var filters []types.IssueFilter
		sel, err := selectClosedWispsForPurge(context.Background(), closedWispScanReader{rows: rows, filters: &filters}, time.Hour, nil)
		if err != nil {
			t.Fatalf("selectClosedWispsForPurge: %v", err)
		}
		if got, want := closedWispIDs(sel.candidates), []string{"gc-old", "gc-just-past"}; !slices.Equal(got, want) {
			t.Errorf("candidates = %v, want %v", got, want)
		}
	})
}

// TestWispGCClosedSelectorZeroAgeSelectsAll pins closed GC's escape hatch
// (GH#8202): --age 0s, or any negative age, sends no UpdatedBefore bound and
// selects every closed wisp whatever its age, including one whose stored
// updated_at reads back ahead of now (#7236). Pinned wisps, configured infra
// types and --exclude-type stay protected.
func TestWispGCClosedSelectorZeroAgeSelectsAll(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := wispGCNow
	t.Cleanup(func() { wispGCNow = old })
	wispGCNow = func() time.Time { return now }

	pinned := closedWispAt("gc-pinned", now.Add(-2*time.Hour))
	pinned.Pinned = true
	infra := closedWispAt("gc-infra", now.Add(-2*time.Hour))
	infra.IssueType = "agent"
	rows := []*types.Issue{
		closedWispAt("gc-old", now.Add(-2*time.Hour)),
		closedWispAt("gc-recent", now.Add(-time.Second)),
		closedWispAt("gc-ahead", now.Add(500*time.Millisecond)),
		pinned,
		infra,
	}
	exclude := []types.IssueType{"rig"}

	for _, age := range []time.Duration{0, -time.Hour} {
		var filters []types.IssueFilter
		sel, err := selectClosedWispsForPurge(context.Background(), closedWispScanReader{rows: rows, filters: &filters}, age, exclude)
		if err != nil {
			t.Fatalf("--age %s: %v", age, err)
		}
		if got, want := closedWispIDs(sel.candidates), []string{"gc-old", "gc-recent", "gc-ahead"}; !slices.Equal(got, want) {
			t.Errorf("--age %s: candidates = %v, want %v", age, got, want)
		}
		if sel.pinned != 1 || sel.infra != 1 {
			t.Errorf("--age %s: pinned %d infra %d, want 1 and 1", age, sel.pinned, sel.infra)
		}
		if len(filters) != 1 {
			t.Fatalf("--age %s: scan sent %d queries, want 1", age, len(filters))
		}
		if filters[0].UpdatedBefore != nil {
			t.Errorf("--age %s: UpdatedBefore = %v, want no age bound", age, *filters[0].UpdatedBefore)
		}
		if !slices.Equal(filters[0].ExcludeTypes, exclude) {
			t.Errorf("--age %s: ExcludeTypes = %v, want %v", age, filters[0].ExcludeTypes, exclude)
		}
	}
}

// TestWispGCClosedSelectorHoldsBackWhatNewerWispsDepend pins the closed
// selection's hold-back (GH#8202): an age-eligible wisp that a closed wisp too
// new for --age depends on stays, over any edge type and through another held
// wisp, so a chain ages out with its newest step instead of straddling the
// cutoff. A chain whose every member is old enough goes as before, and so does
// a wisp that only live work, an old pinned wisp or a durable row depends on
// (GH#5753). The first read of dependents asks about the age-eligible wisps, a
// later one only about older closed wisps outside them, and --age 0s holds
// nothing back and reads no dependents.
func TestWispGCClosedSelectorHoldsBackWhatNewerWispsDepend(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := wispGCNow
	t.Cleanup(func() { wispGCNow = old })
	wispGCNow = func() time.Time { return now }

	aged, fresh := now.Add(-2*time.Hour), now.Add(-time.Minute)
	rows := []*types.Issue{
		closedWispAt("gc-step1", aged),
		closedWispAt("gc-step2", aged),
		closedWispAt("gc-step3", fresh),
		closedWispAt("gc-root", aged),
		closedWispAt("gc-child", fresh),
		closedWispAt("gc-done1", aged),
		closedWispAt("gc-done2", aged),
		closedWispAt("gc-lone", aged),
	}
	edge := func(from, to string, typ types.DependencyType) *types.Dependency {
		return &types.Dependency{IssueID: from, DependsOnID: to, Type: typ}
	}
	deps := []*types.Dependency{
		// A sequential molecule whose last step just closed.
		edge("gc-step2", "gc-step1", types.DepBlocks),
		edge("gc-step3", "gc-step2", types.DepBlocks),
		// A root whose child step just closed.
		edge("gc-child", "gc-root", types.DepParentChild),
		// A chain that finished long ago.
		edge("gc-done2", "gc-done1", types.DepBlocks),
	}

	t.Run("positive age", func(t *testing.T) {
		// Only a closed wisp too new for --age holds a wisp back: aged wisps
		// that a fresh open wisp, an aged pinned wisp and a fresh durable row
		// depend on stay selected.
		live := closedWispAt("gc-live", fresh)
		live.Status = types.StatusOpen
		pinned := closedWispAt("gc-pinned", aged)
		pinned.Pinned = true
		durable := closedWispAt("gc-durable", fresh)
		durable.Ephemeral = false
		allRows := append(slices.Clone(rows),
			closedWispAt("gc-under-live", aged), live,
			closedWispAt("gc-under-pinned", aged), pinned,
			closedWispAt("gc-under-durable", aged), durable)
		allDeps := append(slices.Clone(deps),
			edge("gc-live", "gc-under-live", types.DepBlocks),
			edge("gc-pinned", "gc-under-pinned", types.DepParentChild),
			edge("gc-durable", "gc-under-durable", types.DepBlocks))
		var filters []types.IssueFilter
		var reads [][]string
		r := closedWispScanReader{rows: allRows, honorFilter: true, filters: &filters, deps: allDeps, dependentReads: &reads}
		sel, err := selectClosedWispsForPurge(context.Background(), r, time.Hour, nil)
		if err != nil {
			t.Fatalf("selectClosedWispsForPurge: %v", err)
		}
		want := []string{"gc-done1", "gc-done2", "gc-lone", "gc-under-live", "gc-under-pinned", "gc-under-durable"}
		if got := closedWispIDs(sel.candidates); !slices.Equal(got, want) {
			t.Errorf("candidates = %v, want %v: either an older wisp a just-closed one depends on was selected, or live work, a pinned wisp or a durable row held one back", got, want)
		}
		if sel.heldBack != 3 || sel.pinned != 1 {
			t.Errorf("heldBack = %d, pinned = %d, want 3 (gc-step1, gc-step2, gc-root) and 1", sel.heldBack, sel.pinned)
		}
		// The first read asks about the age-eligible wisps; the walk reads on
		// only above an older closed wisp outside them, here the pinned one.
		wantReads := [][]string{
			{"gc-step1", "gc-step2", "gc-root", "gc-done1", "gc-done2", "gc-lone", "gc-under-live", "gc-under-pinned", "gc-under-durable"},
			{"gc-pinned"},
		}
		if !slices.EqualFunc(reads, wantReads, slices.Equal[[]string]) {
			t.Errorf("dependents reads = %v, want %v", reads, wantReads)
		}
	})

	t.Run("zero age", func(t *testing.T) {
		var filters []types.IssueFilter
		var reads [][]string
		r := closedWispScanReader{rows: rows, honorFilter: true, filters: &filters, deps: deps, dependentReads: &reads}
		sel, err := selectClosedWispsForPurge(context.Background(), r, 0, nil)
		if err != nil {
			t.Fatalf("selectClosedWispsForPurge: %v", err)
		}
		if got, want := closedWispIDs(sel.candidates), closedWispIDs(rows); !slices.Equal(got, want) {
			t.Errorf("--age 0s candidates = %v, want every closed wisp %v", got, want)
		}
		if sel.heldBack != 0 || len(reads) != 0 {
			t.Errorf("--age 0s heldBack = %d after %d dependents reads, want 0 and none", sel.heldBack, len(reads))
		}
	})
}

// TestWispGCClosedSelectorHoldBackCrossesTheScanLimit pins the hold-back
// across the closed scan's 5000-row limit (GH#8202). The limit selects the
// first step of a chain but leaves out the older steps that depend on it,
// and the chain's newest step is too new for --age: the walk reads on above
// the steps the scan left out, so the first step stays instead of going to an
// irreversible --force purge. A chain the limit cuts with nothing newer above
// it still goes, and nothing above the newest step is read.
func TestWispGCClosedSelectorHoldBackCrossesTheScanLimit(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := wispGCNow
	t.Cleanup(func() { wispGCNow = old })
	wispGCNow = func() time.Time { return now }

	aged, fresh := now.Add(-2*time.Hour), now.Add(-time.Minute)
	// The scan's page is the first 5000 aged rows: gc-first, gc-old-first and
	// the fillers. gc-middle-1, gc-middle-2 and gc-old-next are aged too but
	// come after it, and gc-newest is too new for the scan.
	rows := []*types.Issue{closedWispAt("gc-first", aged), closedWispAt("gc-old-first", aged)}
	for i := len(rows); i < wispPurgeScanLimit; i++ {
		rows = append(rows, closedWispAt(fmt.Sprintf("gc-filler-%d", i), aged))
	}
	page := closedWispIDs(rows)
	rows = append(rows,
		closedWispAt("gc-middle-1", aged),
		closedWispAt("gc-middle-2", aged),
		closedWispAt("gc-old-next", aged),
		closedWispAt("gc-newest", fresh),
	)
	deps := []*types.Dependency{
		// gc-newest -> gc-middle-2 -> gc-middle-1 -> gc-first.
		{IssueID: "gc-newest", DependsOnID: "gc-middle-2", Type: types.DepBlocks},
		{IssueID: "gc-middle-2", DependsOnID: "gc-middle-1", Type: types.DepBlocks},
		{IssueID: "gc-middle-1", DependsOnID: "gc-first", Type: types.DepBlocks},
		// A chain that finished long ago: gc-old-next -> gc-old-first.
		{IssueID: "gc-old-next", DependsOnID: "gc-old-first", Type: types.DepBlocks},
	}
	var filters []types.IssueFilter
	var reads [][]string
	r := closedWispScanReader{rows: rows, honorFilter: true, filters: &filters, deps: deps, dependentReads: &reads}
	sel, err := selectClosedWispsForPurge(context.Background(), r, time.Hour, nil)
	if err != nil {
		t.Fatalf("selectClosedWispsForPurge: %v", err)
	}
	if !sel.scanCapped {
		t.Fatal("the scan did not reach its limit: the fixture must fill the 5000-row page")
	}
	got := closedWispIDs(sel.candidates)
	if slices.Contains(got, "gc-first") {
		t.Errorf("gc-first selected although gc-newest, too new for --age, depends on it through gc-middle-2 and gc-middle-1, which the scan limit left out (heldBack %d)", sel.heldBack)
	}
	if !slices.Contains(got, "gc-old-first") {
		t.Error("gc-old-first held back although nothing newer depends on it")
	}
	if len(got) != wispPurgeScanLimit-1 || sel.heldBack != 1 {
		t.Errorf("candidates = %d, heldBack = %d, want %d and 1", len(got), sel.heldBack, wispPurgeScanLimit-1)
	}
	// One read per level: the page, the aged steps found above it, then the
	// one above those.
	wantReads := [][]string{page, {"gc-middle-1", "gc-old-next"}, {"gc-middle-2"}}
	if len(reads) != len(wantReads) {
		t.Fatalf("dependents reads = %d, want %d: %v", len(reads), len(wantReads), reads[min(len(reads), 1):])
	}
	for i, want := range wantReads {
		if asked := slices.Sorted(slices.Values(reads[i])); !slices.Equal(asked, slices.Sorted(slices.Values(want))) {
			t.Errorf("dependents read %d asked about %d ids (first %v), want %d (first %v)", i, len(asked), asked[:min(len(asked), 3)], len(want), want[:min(len(want), 3)])
		}
	}
}

// TestWispGCClosedSelectorHoldPassesOnlyThroughClosedWisps pins what carries a
// newer wisp's hold down to an older one (GH#8202): a closed wisp too new for
// --age keeps what it depends on through an older closed wisp the scan did not
// select (a pinned one here), but not through live work or a durable row,
// whose dependents the selection leaves to the deletion guard (GH#5753). It
// also passes through two such wisps that `bd relate` linked both ways, and
// the walk reads each of them once, so it ends on that cycle.
func TestWispGCClosedSelectorHoldPassesOnlyThroughClosedWisps(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := wispGCNow
	t.Cleanup(func() { wispGCNow = old })
	wispGCNow = func() time.Time { return now }

	aged, fresh := now.Add(-2*time.Hour), now.Add(-time.Minute)
	pinned := closedWispAt("gc-pinned", aged)
	pinned.Pinned = true
	live := closedWispAt("gc-live", aged)
	live.Status = types.StatusOpen
	durable := closedWispAt("gc-durable", aged)
	durable.Ephemeral = false
	cycleX := closedWispAt("gc-cycle-x", aged)
	cycleX.Pinned = true
	cycleY := closedWispAt("gc-cycle-y", aged)
	cycleY.Pinned = true
	rows := []*types.Issue{
		closedWispAt("gc-under-pinned", aged), pinned, closedWispAt("gc-over-pinned", fresh),
		closedWispAt("gc-under-live", aged), live, closedWispAt("gc-over-live", fresh),
		closedWispAt("gc-under-durable", aged), durable, closedWispAt("gc-over-durable", fresh),
		closedWispAt("gc-under-cycle", aged), cycleX, cycleY, closedWispAt("gc-over-cycle", fresh),
	}
	// gc-over-X -> gc-X -> gc-under-X for each middle row X.
	var deps []*types.Dependency
	for _, middle := range []string{"pinned", "live", "durable"} {
		deps = append(deps,
			&types.Dependency{IssueID: "gc-" + middle, DependsOnID: "gc-under-" + middle, Type: types.DepBlocks},
			&types.Dependency{IssueID: "gc-over-" + middle, DependsOnID: "gc-" + middle, Type: types.DepBlocks},
		)
	}
	// gc-over-cycle -> gc-cycle-y <-> gc-cycle-x -> gc-under-cycle.
	deps = append(deps,
		&types.Dependency{IssueID: "gc-cycle-x", DependsOnID: "gc-under-cycle", Type: types.DepBlocks},
		&types.Dependency{IssueID: "gc-cycle-x", DependsOnID: "gc-cycle-y", Type: types.DepRelatesTo},
		&types.Dependency{IssueID: "gc-cycle-y", DependsOnID: "gc-cycle-x", Type: types.DepRelatesTo},
		&types.Dependency{IssueID: "gc-over-cycle", DependsOnID: "gc-cycle-y", Type: types.DepBlocks},
	)
	var filters []types.IssueFilter
	var reads [][]string
	r := closedWispScanReader{rows: rows, honorFilter: true, filters: &filters, deps: deps, dependentReads: &reads, maxDependentReads: 50}
	sel, err := selectClosedWispsForPurge(context.Background(), r, time.Hour, nil)
	if err != nil {
		t.Fatalf("selectClosedWispsForPurge: %v", err)
	}
	if got, want := closedWispIDs(sel.candidates), []string{"gc-under-live", "gc-under-durable"}; !slices.Equal(got, want) {
		t.Errorf("candidates = %v, want %v: the hold passes through the older pinned wisps, the related pair too, and stops at live work and durable rows", got, want)
	}
	if sel.heldBack != 2 || sel.pinned != 3 {
		t.Errorf("heldBack = %d, pinned = %d, want 2 (gc-under-pinned, gc-under-cycle) and 3", sel.heldBack, sel.pinned)
	}
	// The candidates, then gc-pinned and gc-cycle-x, then gc-cycle-y.
	if len(reads) != 3 {
		t.Errorf("dependents reads = %d, want 3, one per level: %v", len(reads), reads)
	}
}

// TestClosedWispTooNewMatchesTheScanBound pins the hold-back's age test to
// the closed scan's: the SQL bound compares updated_at with now-age at whole
// seconds, so a row in the cutoff's own second is too new even when the strict
// Go check alone would call it old enough. --age 0s finds nothing too new.
func TestClosedWispTooNewMatchesTheScanBound(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 700_000_000, time.UTC)
	for _, tc := range []struct {
		updated time.Time
		age     time.Duration
		want    bool
	}{
		{time.Date(2026, 11, 19, 10, 59, 59, 0, time.UTC), time.Hour, false},
		{time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC), time.Hour, true},
		{time.Date(2026, 11, 19, 11, 30, 0, 0, time.UTC), time.Hour, true},
		{now.Add(time.Second), time.Hour, true},
		{now, 0, false},
		{now.Add(time.Second), -time.Hour, false},
	} {
		if got := closedWispTooNew(now, tc.updated, tc.age); got != tc.want {
			t.Errorf("closedWispTooNew(now, %s, %s) = %v, want %v", tc.updated.Format(time.RFC3339Nano), tc.age, got, tc.want)
		}
	}
}
