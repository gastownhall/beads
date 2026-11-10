package sqlbuild

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestLabelSetClauses pins the SQL and arg ordering shared by the listing
// commands that cannot take a label JOIN (bd stale, bd blocked). The clauses
// must stay interchangeable with the equivalent blocks in
// BuildIssueFilterClauses — see TestLabelSetClausesMatchesFilterClauses.
func TestLabelSetClauses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		idExpr    string
		tables    FilterTables
		labels    []string
		labelsAny []string
		exclude   []string
		wantWhere []string
		wantArgs  []any
	}{
		{
			name:   "no labels yields no clauses",
			idExpr: "id",
			tables: IssuesFilterTables,
		},
		{
			name:      "single AND label",
			idExpr:    "id",
			tables:    IssuesFilterTables,
			labels:    []string{"theme:personal"},
			wantWhere: []string{"id IN (SELECT issue_id FROM labels WHERE label = ?)"},
			wantArgs:  []any{"theme:personal"},
		},
		{
			name:   "AND labels emit one clause each, in order",
			idExpr: "id",
			tables: IssuesFilterTables,
			labels: []string{"theme:personal", "actor:robin"},
			wantWhere: []string{
				"id IN (SELECT issue_id FROM labels WHERE label = ?)",
				"id IN (SELECT issue_id FROM labels WHERE label = ?)",
			},
			wantArgs: []any{"theme:personal", "actor:robin"},
		},
		{
			name:      "OR labels collapse into one IN clause",
			idExpr:    "id",
			tables:    IssuesFilterTables,
			labelsAny: []string{"p0", "p1"},
			wantWhere: []string{"id IN (SELECT issue_id FROM labels WHERE label IN (?, ?))"},
			wantArgs:  []any{"p0", "p1"},
		},
		{
			name:      "exclude labels emit a NOT IN clause",
			idExpr:    "id",
			tables:    IssuesFilterTables,
			exclude:   []string{"monitor"},
			wantWhere: []string{"id NOT IN (SELECT issue_id FROM labels WHERE label IN (?))"},
			wantArgs:  []any{"monitor"},
		},
		{
			name:      "all three combine, AND then OR then NOT",
			idExpr:    "id",
			tables:    IssuesFilterTables,
			labels:    []string{"theme:personal"},
			labelsAny: []string{"p0", "p1"},
			exclude:   []string{"monitor"},
			wantWhere: []string{
				"id IN (SELECT issue_id FROM labels WHERE label = ?)",
				"id IN (SELECT issue_id FROM labels WHERE label IN (?, ?))",
				"id NOT IN (SELECT issue_id FROM labels WHERE label IN (?))",
			},
			wantArgs: []any{"theme:personal", "p0", "p1", "monitor"},
		},
		{
			name:      "wisps use their own label table",
			idExpr:    "id",
			tables:    WispsFilterTables,
			labels:    []string{"theme:personal"},
			wantWhere: []string{"id IN (SELECT issue_id FROM wisp_labels WHERE label = ?)"},
			wantArgs:  []any{"theme:personal"},
		},
		{
			name:      "qualified id expression is used verbatim",
			idExpr:    "issues.id",
			tables:    IssuesFilterTables,
			labels:    []string{"theme:personal"},
			wantWhere: []string{"issues.id IN (SELECT issue_id FROM labels WHERE label = ?)"},
			wantArgs:  []any{"theme:personal"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			where, args := LabelSetClauses(tc.idExpr, tc.tables, tc.labels, tc.labelsAny, tc.exclude)
			if len(where) != len(tc.wantWhere) {
				t.Fatalf("got %d clauses %v, want %d %v", len(where), where, len(tc.wantWhere), tc.wantWhere)
			}
			for i := range where {
				if where[i] != tc.wantWhere[i] {
					t.Errorf("clause %d = %q, want %q", i, where[i], tc.wantWhere[i])
				}
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("got %d args %v, want %d %v", len(args), args, len(tc.wantArgs), tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Errorf("arg %d = %v, want %v", i, args[i], tc.wantArgs[i])
				}
			}
		})
	}
}

// TestLabelSetClausesPlaceholderArgAgreement guards the failure mode that
// actually bites at runtime: a clause carrying more ? than the args beside it,
// which the driver reports far from the code that built it.
func TestLabelSetClausesPlaceholderArgAgreement(t *testing.T) {
	t.Parallel()

	where, args := LabelSetClauses("id", IssuesFilterTables,
		[]string{"a", "b"}, []string{"c", "d", "e"}, []string{"f", "g"})

	placeholders := 0
	for _, clause := range where {
		placeholders += strings.Count(clause, "?")
	}
	if placeholders != len(args) {
		t.Errorf("clauses carry %d placeholders but %d args were returned: %v / %v",
			placeholders, len(args), where, args)
	}
}

// TestLabelSetClausesMatchesFilterClauses pins the helper against the inline
// label handling in BuildIssueFilterClauses. The two must agree: bd stale and
// bd blocked filter through the helper while bd list filters through
// BuildIssueFilterClauses, and a user is entitled to the same answer from
// --label whichever command they reach for.
func TestLabelSetClausesMatchesFilterClauses(t *testing.T) {
	t.Parallel()

	labels := []string{"theme:personal", "actor:robin"}
	labelsAny := []string{"p0", "p1"}
	exclude := []string{"monitor"}

	helperWhere, helperArgs := LabelSetClauses("id", IssuesFilterTables, labels, labelsAny, exclude)

	filterWhere, filterArgs, err := BuildIssueFilterClauses("", types.IssueFilter{
		Labels:        labels,
		LabelsAny:     labelsAny,
		ExcludeLabels: exclude,
	}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses: %v", err)
	}

	if strings.Join(helperWhere, " AND ") != strings.Join(filterWhere, " AND ") {
		t.Errorf("clause mismatch:\n helper: %v\n filter: %v", helperWhere, filterWhere)
	}
	if len(helperArgs) != len(filterArgs) {
		t.Fatalf("arg count mismatch: helper %v, filter %v", helperArgs, filterArgs)
	}
	for i := range helperArgs {
		if helperArgs[i] != filterArgs[i] {
			t.Errorf("arg %d: helper %v, filter %v", i, helperArgs[i], filterArgs[i])
		}
	}
}
