package sqlbuild

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestGlobToLikePattern exercises globToLikePattern directly against the
// package that actually calls it from BuildIssueFilterClauses (be-ucslk4).
// This used to be pinned only against a byte-identical but unreachable copy
// in internal/storage/issueops, so an escaping regression in this — the
// live — implementation could ship unseen.
func TestGlobToLikePattern(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "trailing star", in: "tech-*", want: "tech-%"},
		{name: "surrounding stars", in: "*foo*", want: "%foo%"},
		{name: "question mark", in: "v?", want: "v_"},
		{name: "literal percent", in: "5%", want: "5|%"},
		{name: "literal underscore", in: "snake_case", want: "snake|_case"},
		{name: "literal pipe", in: "a|b", want: "a||b"},
		{name: "no metachars", in: "needs-pm", want: "needs-pm"},
		{name: "empty", in: "", want: ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := globToLikePattern(tc.in)
			if got != tc.want {
				t.Errorf("globToLikePattern(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestTimeBoundsBindTheUTCInstant pins that every time bound is bound as the
// UTC spelling of its instant, truncated to whole seconds. The columns hold
// UTC wall clock, and an engine whose column carries no zone (a SQL
// `timestamp` WITHOUT time zone) reads a literal's wall clock and drops its
// offset, so a bound spelled `T+02:00` would compare two hours late there.
// Binding the UTC spelling makes an offset bound select exactly what its Z
// twin does on every engine (Dolt already honors the offset; the UTC spelling
// of the same instant is the same comparison there).
func TestTimeBoundsBindTheUTCInstant(t *testing.T) {
	t.Parallel()
	plus2 := time.FixedZone("UTC+2", 2*60*60)
	at := time.Date(2026, 10, 17, 12, 0, 0, 900_000_000, plus2) // 10:00:00.9Z
	const want = "2026-10-17T10:00:00Z"
	bound := func(f *types.IssueFilter) {
		f.CreatedAfter, f.CreatedBefore = &at, &at
		f.UpdatedAfter, f.UpdatedBefore = &at, &at
		f.ClosedAfter, f.ClosedBefore = &at, &at
		f.StartedAfter, f.StartedBefore = &at, &at
		f.DeferAfter, f.DeferBefore = &at, &at
		f.DueAfter, f.DueBefore = &at, &at
	}
	var filter types.IssueFilter
	bound(&filter)
	_, args, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses: %v", err)
	}
	if len(args) != 12 {
		t.Fatalf("args = %v, want the 12 time bounds", args)
	}
	for i, arg := range args {
		if arg != want {
			t.Errorf("arg %d = %v, want %q (the bound's UTC instant at whole seconds)", i, arg, want)
		}
	}
}

// TestKeysetCursorBindsTheUTCInstant pins that the keyset cursor is bound as
// its instant in UTC, still as a time.Time. A driver can send a time.Time as
// its wall clock with no zone (embedded Dolt's does), and the columns hold UTC
// wall clock, so a cursor in another zone would page from the wrong position
// there. Both orders bind the cursor twice.
func TestKeysetCursorBindsTheUTCInstant(t *testing.T) {
	t.Parallel()
	plus2 := time.FixedZone("UTC+2", 2*60*60)
	cur := time.Date(2026, 10, 17, 12, 0, 0, 0, plus2)
	want := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	p := 2
	for _, tc := range []struct {
		name   string
		filter types.IssueFilter
		nargs  int
		at     []int // the cursor's positions in the bound args
	}{
		{"created order", types.IssueFilter{AfterCreatedAt: &cur, AfterID: "bd-42"}, 3, []int{0, 1}},
		{"priority order", types.IssueFilter{AfterPriority: &p, AfterCreatedAt: &cur, AfterID: "bd-42"}, 5, []int{2, 3}},
	} {
		_, args, err := BuildIssueFilterClauses("", tc.filter, IssuesFilterTables)
		if err != nil {
			t.Fatalf("%s: BuildIssueFilterClauses: %v", tc.name, err)
		}
		if len(args) != tc.nargs {
			t.Fatalf("%s: args = %v, want %d", tc.name, args, tc.nargs)
		}
		for _, i := range tc.at {
			got, ok := args[i].(time.Time)
			if !ok || !got.Equal(want) || got.Location() != time.UTC {
				t.Errorf("%s: arg %d = %v (%T), want the time.Time %v", tc.name, i, args[i], args[i], want)
			}
		}
	}
}
