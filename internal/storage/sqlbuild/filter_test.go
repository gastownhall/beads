package sqlbuild

import (
	"strings"
	"testing"

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

// TestBuildIssueFilterClausesAllFields pins the IssueFilter.AllFields form of
// the free-text query clause (GH#2883): every AllFieldsTextColumns arm, the
// leg-appropriate comments table, ID handling in both the text and ID-like
// branches, and one placeholder per arm so the clause/arg contract cannot
// silently skew.
func TestBuildIssueFilterClausesAllFields(t *testing.T) {
	t.Parallel()

	t.Run("text query issues leg", func(t *testing.T) {
		t.Parallel()
		clauses, args, err := BuildIssueFilterClauses("env var", types.IssueFilter{AllFields: true}, IssuesFilterTables)
		if err != nil {
			t.Fatalf("BuildIssueFilterClauses: %v", err)
		}
		if len(clauses) != 1 {
			t.Fatalf("expected 1 clause, got %d: %v", len(clauses), clauses)
		}
		clause := clauses[0]
		for _, want := range []string{
			"id LIKE ? ESCAPE '|'",
			"LOWER(title) LIKE ? ESCAPE '|'",
			"LOWER(external_ref) LIKE ? ESCAPE '|'",
			"LOWER(description) LIKE ? ESCAPE '|'",
			"LOWER(design) LIKE ? ESCAPE '|'",
			"LOWER(acceptance_criteria) LIKE ? ESCAPE '|'",
			"LOWER(notes) LIKE ? ESCAPE '|'",
			"LOWER(close_reason) LIKE ? ESCAPE '|'",
			"id IN (SELECT issue_id FROM comments WHERE LOWER(text) LIKE ? ESCAPE '|')",
		} {
			if !strings.Contains(clause, want) {
				t.Errorf("clause missing %q:\n%s", want, clause)
			}
		}
		// id substring + 7 text columns + comments subquery = 9 placeholders.
		wantArgs := 2 + len(AllFieldsTextColumns)
		if len(args) != wantArgs {
			t.Errorf("expected %d args, got %d: %v", wantArgs, len(args), args)
		}
		for i, a := range args {
			if a != "%env var%" {
				t.Errorf("arg %d = %v, want %%env var%%", i, a)
			}
		}
	})

	t.Run("literal LIKE metacharacters are escaped", func(t *testing.T) {
		t.Parallel()
		clauses, args, err := BuildIssueFilterClauses(`C:\\Temp_100%|done`, types.IssueFilter{AllFields: true}, IssuesFilterTables)
		if err != nil {
			t.Fatalf("BuildIssueFilterClauses: %v", err)
		}
		if !strings.Contains(clauses[0], "ESCAPE '|'") {
			t.Fatalf("all-fields clause must declare its LIKE escape character: %s", clauses[0])
		}
		for i, arg := range args {
			if arg == `C:\\Temp_100%|done` {
				t.Errorf("arg %d leaves LIKE metacharacters unescaped: %q", i, arg)
			}
			if arg != `%c:\\temp|_100|%||done%` {
				t.Errorf("arg %d = %q, want literal contains pattern", i, arg)
			}
		}
	})

	t.Run("id-like query keeps exact and prefix id arms", func(t *testing.T) {
		t.Parallel()
		clauses, args, err := BuildIssueFilterClauses("bd-12", types.IssueFilter{AllFields: true}, IssuesFilterTables)
		if err != nil {
			t.Fatalf("BuildIssueFilterClauses: %v", err)
		}
		if len(clauses) != 1 {
			t.Fatalf("expected 1 clause, got %d: %v", len(clauses), clauses)
		}
		clause := clauses[0]
		if !strings.Contains(clause, "id = ?") || !strings.Contains(clause, "id LIKE ?") {
			t.Errorf("id-like all-fields clause missing exact/prefix id arms:\n%s", clause)
		}
		if !strings.Contains(clause, "LOWER(description) LIKE ?") {
			t.Errorf("id-like all-fields clause missing description arm:\n%s", clause)
		}
		if args[0] != "bd-12" || args[1] != "bd-12%" {
			t.Errorf("expected exact + prefix id args first, got %v", args[:2])
		}
		wantArgs := 3 + len(AllFieldsTextColumns)
		if len(args) != wantArgs {
			t.Errorf("expected %d args, got %d: %v", wantArgs, len(args), args)
		}
	})

	t.Run("wisps leg uses wisp_comments", func(t *testing.T) {
		t.Parallel()
		clauses, _, err := BuildIssueFilterClauses("env var", types.IssueFilter{AllFields: true}, WispsFilterTables)
		if err != nil {
			t.Fatalf("BuildIssueFilterClauses: %v", err)
		}
		if !strings.Contains(clauses[0], "FROM wisp_comments") {
			t.Errorf("wisps leg should search wisp_comments:\n%s", clauses[0])
		}
	})

	t.Run("off by default", func(t *testing.T) {
		t.Parallel()
		clauses, _, err := BuildIssueFilterClauses("env var", types.IssueFilter{}, IssuesFilterTables)
		if err != nil {
			t.Fatalf("BuildIssueFilterClauses: %v", err)
		}
		if strings.Contains(clauses[0], "comments") || strings.Contains(clauses[0], "description") {
			t.Errorf("default clause must stay title/ID-scoped:\n%s", clauses[0])
		}
	})
}
