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

// closedWispScanReader serves selectClosedWispsForPurge a fixed list of closed
// wisps and records every filter the scan sends. With honorFilter it applies
// UpdatedBefore and then Limit the way the SQL scan does (predicate first,
// limit second); without it, it returns every row, so the Go-side guard is
// all that keeps a recent row out. Infra means the "agent" type.
type closedWispScanReader struct {
	molReader
	rows        []*types.Issue
	honorFilter bool
	filters     *[]types.IssueFilter
}

func (r closedWispScanReader) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	*r.filters = append(*r.filters, filter)
	if !r.honorFilter {
		return r.rows, nil
	}
	var out []*types.Issue
	for _, issue := range r.rows {
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
