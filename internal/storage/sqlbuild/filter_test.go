package sqlbuild

import (
	"slices"
	"strings"
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

// stringPtr is a test-local pointer helper for the filter literals below.
func stringPtr(s string) *string { return &s }

// TestAssigneeFilterClauseComparesLowercased pins the assignee clauses of
// both listing builders (GH#8628): the filter compares LOWER(assignee) with
// a lowered bound, so an --assignee spelling matches the stored spelling
// whatever its letter case, while the separator fold the ownership gates
// apply has no SQL form and stays exact. The NoAssignee/Unassigned arms are
// the emptiness check and keep their byte-identical shape.
func TestAssigneeFilterClauseComparesLowercased(t *testing.T) {
	clauses, args, err := BuildIssueFilterClauses("", types.IssueFilter{Assignee: stringPtr("HieroPhant")}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses: %v", err)
	}
	found := false
	for _, c := range clauses {
		if c == "LOWER(assignee) = ?" {
			found = true
		}
	}
	if !found {
		t.Errorf("clauses = %v, want a LOWER(assignee) = ? clause", clauses)
	}
	if len(args) != 1 || args[0] != "hierophant" {
		t.Errorf("args = %v, want [hierophant]", args)
	}

	where, rargs, err := BuildReadyWorkWhere(types.WorkFilter{Assignee: stringPtr("HieroPhant")}, IssuesFilterTables, ReadyWorkWhereInputs{})
	if err != nil {
		t.Fatalf("BuildReadyWorkWhere: %v", err)
	}
	if !strings.Contains(where, "LOWER(assignee) = ?") {
		t.Errorf("ready WHERE = %q, want a LOWER(assignee) = ? term", where)
	}
	bound := false
	for _, a := range rargs {
		if s, ok := a.(string); ok && s == "hierophant" {
			bound = true
		}
	}
	if !bound {
		t.Errorf("ready args = %v, want the lowered assignee bound", rargs)
	}

	noAssignee, _, err := BuildIssueFilterClauses("", types.IssueFilter{NoAssignee: true}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses(NoAssignee): %v", err)
	}
	if !slices.Contains(noAssignee, "(assignee IS NULL OR assignee = '')") {
		t.Errorf("NoAssignee clauses = %v, want the byte-identical emptiness check", noAssignee)
	}
	unassignedWhere, _, err := BuildReadyWorkWhere(types.WorkFilter{Unassigned: true}, IssuesFilterTables, ReadyWorkWhereInputs{})
	if err != nil {
		t.Fatalf("BuildReadyWorkWhere(Unassigned): %v", err)
	}
	if strings.Contains(unassignedWhere, "LOWER(assignee)") {
		t.Errorf("unassigned WHERE = %q, unexpectedly cased", unassignedWhere)
	}
}

// TestAssigneeFilterMatches pins the Go form of the assignee predicate so
// the in-memory listings and verdicts admit exactly what the SQL admits
// (GH#8628): case folds, separators and padding do not.
func TestAssigneeFilterMatches(t *testing.T) {
	for _, tc := range []struct {
		stored, asked string
		want          bool
	}{
		{"hierophant", "HIEROPHANT", true},
		{"Hierophant", "hierophant", true},
		{"hierophant", "hierophant", true},
		{"hierophant", "hierophant2", false},
		{"hierophant", " hierophant", false},
		{"a.b", "a__b", false}, // separators stay exact in the listings
		{"ſ", "s", false},      // long s does not lowercase to s
		{"K", "k", true},       // the Kelvin sign lowercases to k
	} {
		if got := AssigneeFilterMatches(tc.stored, tc.asked); got != tc.want {
			t.Errorf("AssigneeFilterMatches(%q, %q) = %v, want %v", tc.stored, tc.asked, got, tc.want)
		}
	}
}
