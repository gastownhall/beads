package sqlbuild

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestBuildReclaimScopeClausesEmpty(t *testing.T) {
	clauses, args := BuildReclaimScopeClauses(types.ReclaimFilter{}, IssuesFilterTables, "i")
	if len(clauses) != 0 || len(args) != 0 {
		t.Fatalf("empty filter yielded clauses=%v args=%v, want none", clauses, args)
	}
	suffix, sargs := ReclaimScopeSQL(types.ReclaimFilter{}, IssuesFilterTables, "i")
	if suffix != "" || sargs != nil {
		t.Fatalf("empty filter suffix = %q args=%v, want empty", suffix, sargs)
	}
}

func TestBuildReclaimScopeClausesShapeAndAlias(t *testing.T) {
	filter := types.ReclaimFilter{
		IDs:           []string{"wy-a", "wy-b"},
		Assignees:     []string{"zelda"},
		Labels:        []string{"lane-a", "tier:opus"},
		LabelsAny:     []string{"x", "y"},
		ExcludeLabels: []string{"pinned"},
	}
	clauses, args := BuildReclaimScopeClauses(filter, IssuesFilterTables, "i")
	joined := strings.Join(clauses, " AND ")

	wantContains := []string{
		"i.id IN (?,?)",     // IDs
		"i.assignee IN (?)", // assignees
		"i.id IN (SELECT issue_id FROM labels WHERE label = ?)",        // per-label AND (x2)
		"i.id IN (SELECT issue_id FROM labels WHERE label IN (?,?))",   // label-any OR
		"i.id NOT IN (SELECT issue_id FROM labels WHERE label IN (?))", // exclude
	}
	for _, w := range wantContains {
		if !strings.Contains(joined, w) {
			t.Errorf("clauses missing %q\n got: %s", w, joined)
		}
	}
	// Two AND-labels => two per-label subqueries.
	if got := strings.Count(joined, "WHERE label = ?"); got != 2 {
		t.Errorf("per-label AND subquery count = %d, want 2", got)
	}
	// Args order: ids, assignee, each AND-label, label-any set, exclude set.
	want := []any{"wy-a", "wy-b", "zelda", "lane-a", "tier:opus", "x", "y", "pinned"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg[%d] = %v, want %v (full %v)", i, args[i], want[i], args)
		}
	}
}

func TestBuildReclaimScopeClausesNoAlias(t *testing.T) {
	clauses, _ := BuildReclaimScopeClauses(types.ReclaimFilter{IDs: []string{"wy-a"}}, IssuesFilterTables, "")
	if len(clauses) != 1 || clauses[0] != "id IN (?)" {
		t.Fatalf("no-alias id clause = %v, want [\"id IN (?)\"]", clauses)
	}
}

// A scope field supplied with only-empty values must FAIL CLOSED to "1=0"
// (match nothing), never drop out into a global sweep. This is the belt to the
// CLI's suspenders, protecting programmatic callers.
func TestBuildReclaimScopeClausesFailsClosedOnDegenerate(t *testing.T) {
	cases := map[string]types.ReclaimFilter{
		"ids":       {IDs: []string{"", ""}},
		"assignees": {Assignees: []string{""}},
		"labels":    {Labels: []string{""}},
		"labelsAny": {LabelsAny: []string{""}},
		"exclude":   {ExcludeLabels: []string{""}},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			clauses, args := BuildReclaimScopeClauses(f, IssuesFilterTables, "i")
			if len(args) != 0 {
				t.Fatalf("degenerate %s produced args %v, want none", name, args)
			}
			joined := strings.Join(clauses, " AND ")
			if !strings.Contains(joined, "1=0") {
				t.Fatalf("degenerate %s = %q, want a 1=0 fail-closed clause", name, joined)
			}
		})
	}
}
