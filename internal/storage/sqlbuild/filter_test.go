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

// TestAssigneeFilterFoldsLetterCase pins GH#8628 on the listing plane:
// letter case is not part of an identity, so every --assignee filter renders
// LOWER(assignee) = ? bound to the lowered value — LOWER() on Dolt's utf8mb4
// is strings.ToLower, the same fold AssigneeFilterMatches runs in Go — and the
// unassigned predicates stay byte for byte what they were.
func TestAssigneeFilterFoldsLetterCase(t *testing.T) {
	t.Parallel()
	asked := "HieroPhant"
	const wantClause = "LOWER(assignee) = ?"

	clauses, args, err := BuildIssueFilterClauses("", types.IssueFilter{Assignee: &asked}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses: %v", err)
	}
	if !slices.Contains(clauses, wantClause) || slices.Contains(clauses, "assignee = ?") {
		t.Errorf("issue filter clauses = %q, want %q and no verbatim assignee = ?", clauses, wantClause)
	}
	if !slices.Contains(args, any("hierophant")) || slices.Contains(args, any(asked)) {
		t.Errorf("issue filter args = %v, want the lowered %q bound, not %q", args, "hierophant", asked)
	}

	for _, tables := range []FilterTables{IssuesFilterTables, WispsFilterTables} {
		where, args, err := BuildReadyWorkWhere(types.WorkFilter{Assignee: &asked}, tables, ReadyWorkWhereInputs{})
		if err != nil {
			t.Fatalf("BuildReadyWorkWhere: %v", err)
		}
		if !strings.Contains(where, wantClause) {
			t.Errorf("ready where = %s, want %q", where, wantClause)
		}
		if !slices.Contains(args, any("hierophant")) || slices.Contains(args, any(asked)) {
			t.Errorf("ready args = %v, want the lowered %q bound, not %q", args, "hierophant", asked)
		}
	}

	const unassigned = "(assignee IS NULL OR assignee = '')"
	clauses, _, err = BuildIssueFilterClauses("", types.IssueFilter{NoAssignee: true}, IssuesFilterTables)
	if err != nil || !slices.Contains(clauses, unassigned) {
		t.Errorf("NoAssignee clauses = %q (err %v), want %q unchanged", clauses, err, unassigned)
	}
	where, _, err := BuildReadyWorkWhere(types.WorkFilter{Unassigned: true, Assignee: &asked}, IssuesFilterTables, ReadyWorkWhereInputs{})
	if err != nil || !strings.Contains(where, unassigned) || strings.Contains(where, wantClause) {
		t.Errorf("Unassigned ready where = %s (err %v), want %q alone", where, err, unassigned)
	}
}

// TestAssigneeFilterMatches pins the Go copy of the listing predicate: it is
// strings.ToLower on both sides, so it admits exactly what LOWER(assignee) = ?
// admits — case variants, including the Unicode ones ToLower folds, and
// nothing ToLower keeps apart (U+017F long s is not s, which EqualFold would
// merge).
func TestAssigneeFilterMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stored, asked string
		want          bool
	}{
		{"hierophant", "hierophant", true},
		{"hierophant", "HIEROPHANT", true},
		{"Hierophant", "hierophant", true},
		{"k", "K", true},
		{"josé", "JOSÉ", true},
		{"josé", "jose", false},
		{"s", "ſ", false},
		{"hierophant", "hierophant2", false},
		{"hierophant", " hierophant", false},
		{"gastown.mayor", "gastown_mayor", false}, // the listing folds case only
		{"bob", "hierophant", false},
	}
	for _, tc := range cases {
		if got := AssigneeFilterMatches(tc.stored, tc.asked); got != tc.want {
			t.Errorf("AssigneeFilterMatches(%q, %q) = %v, want %v", tc.stored, tc.asked, got, tc.want)
		}
	}
	if got := AssigneeFilterArg("HieroPhant"); got != "hierophant" {
		t.Errorf("AssigneeFilterArg(%q) = %q, want %q", "HieroPhant", got, "hierophant")
	}
}
