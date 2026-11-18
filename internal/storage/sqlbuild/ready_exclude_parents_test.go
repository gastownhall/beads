package sqlbuild

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// GH#6817: a flagged ready read (WorkFilter.ExcludeParentsWithOpenChildren)
// excludes the parents the close gate counts open children for. The WHERE
// must fold the FLAG and the executor-resolved parent SET together, so the
// unflagged — or setless — read never changes, a set without the flag never
// excludes anything, and a populated set batches its NOT IN exactly like the
// deferred-children set does.
func TestBuildReadyWorkWhereExcludeParentsFoldsTheFlagAndSetTogether(t *testing.T) {
	t.Parallel()

	base := types.WorkFilter{Status: types.StatusOpen}
	off, offArgs, err := BuildReadyWorkWhere(base, IssuesFilterTables, ReadyWorkWhereInputs{})
	if err != nil {
		t.Fatalf("BuildReadyWorkWhere (flag off): %v", err)
	}
	if strings.Contains(off, "id NOT IN (") {
		t.Fatalf("the unflagged baseline already carries an id NOT IN clause: %s", off)
	}

	flagged := base
	flagged.ExcludeParentsWithOpenChildren = true

	for _, tables := range []FilterTables{IssuesFilterTables, WispsFilterTables} {
		t.Run(tables.Main+"/empty_set_adds_no_clause", func(t *testing.T) {
			where, args, err := BuildReadyWorkWhere(flagged, tables, ReadyWorkWhereInputs{})
			if err != nil {
				t.Fatalf("BuildReadyWorkWhere: %v", err)
			}
			if where != off {
				t.Errorf("an empty parent set must leave the WHERE identical to the unflagged read\n got: %s\nwant: %s", where, off)
			}
			if len(args) != len(offArgs) {
				t.Errorf("args = %d, want %d: an empty set contributes no placeholders", len(args), len(offArgs))
			}
		})

		t.Run(tables.Main+"/other_familys_set_adds_no_clause", func(t *testing.T) {
			other := WispsFilterTables.Main
			if tables.Main == WispsFilterTables.Main {
				other = IssuesFilterTables.Main
			}
			where, _, err := BuildReadyWorkWhere(flagged, tables,
				ReadyWorkWhereInputs{OpenChildParentIDs: map[string][]string{other: {"p-1"}}})
			if err != nil {
				t.Fatalf("BuildReadyWorkWhere: %v", err)
			}
			if where != off {
				t.Errorf("a set keyed for the other family must not exclude this one\n got: %s\nwant: %s", where, off)
			}
		})

		t.Run(tables.Main+"/flag_off_ignores_a_populated_set", func(t *testing.T) {
			where, _, err := BuildReadyWorkWhere(base, tables,
				ReadyWorkWhereInputs{OpenChildParentIDs: map[string][]string{tables.Main: {"p-1"}}})
			if err != nil {
				t.Fatalf("BuildReadyWorkWhere: %v", err)
			}
			if where != off {
				t.Errorf("without the flag a populated set must add no clause\n got: %s\nwant: %s", where, off)
			}
		})

		t.Run(tables.Main+"/populated_set_batches_its_exclusion", func(t *testing.T) {
			ids := make([]string, QueryBatchSize+1)
			wantArgs := make([]string, len(ids))
			for i := range ids {
				ids[i] = fmt.Sprintf("p-%d", i)
				wantArgs[i] = ids[i]
			}
			where, args, err := BuildReadyWorkWhere(flagged, tables,
				ReadyWorkWhereInputs{OpenChildParentIDs: map[string][]string{tables.Main: ids}})
			if err != nil {
				t.Fatalf("BuildReadyWorkWhere: %v", err)
			}
			if got := strings.Count(where, "id NOT IN ("); got != 2 {
				t.Errorf("expected 2 batched NOT IN clauses for %d parents, got %d", len(ids), got)
			}
			gotArgs := make([]string, 0, len(args))
			for _, a := range args {
				gotArgs = append(gotArgs, fmt.Sprint(a))
			}
			tail := gotArgs[len(gotArgs)-len(wantArgs):]
			for i := range wantArgs {
				if tail[i] != wantArgs[i] {
					t.Errorf("parent-set placeholders out of order at %d: got %q, want %q", i, tail[i], wantArgs[i])
				}
			}
		})
	}
}

// The exclusion set's SQL is the close gate's open-children count
// (issueops countOpenChildrenForTargetInTx) turned into the id projection:
// both child families, the literal status and type tests, the wisp leg's
// mirror dedupe — and the TYPED target column, never DepTargetExpr's
// COALESCE, which would merge the issue and wisp parent spaces and exclude a
// parent for children recorded against the other column.
func TestOpenChildParentIDsSQLMatchesTheCloseGatesLegs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ parentTable, column string }{
		{"issues", "dependency.depends_on_issue_id"},
		{"wisps", "dependency.depends_on_wisp_id"},
	} {
		combined := OpenChildParentIDsSQL(test.parentTable)
		other := "dependency.depends_on_wisp_id"
		if test.column == other {
			other = "dependency.depends_on_issue_id"
		}
		if !strings.Contains(combined, test.column) {
			t.Errorf("OpenChildParentIDsSQL(%q) must select the typed column %s", test.parentTable, test.column)
		}
		if strings.Contains(combined, other) {
			t.Errorf("OpenChildParentIDsSQL(%q) must not mix in the other family's column", test.parentTable)
		}
		if strings.Contains(combined, DepTargetExpr) || strings.Contains(combined, "COALESCE") {
			t.Errorf("OpenChildParentIDsSQL(%q) must never merge the typed target columns", test.parentTable)
		}
		for _, needle := range []string{
			"FROM dependencies AS dependency",
			"JOIN issues AS child ON child.id = dependency.issue_id",
			"FROM wisp_dependencies AS dependency",
			"JOIN wisps AS child ON child.id = dependency.issue_id",
			"type = 'parent-child'",
			"child.status != 'closed'",
		} {
			if !strings.Contains(combined, needle) {
				t.Errorf("OpenChildParentIDsSQL(%q) is missing the close gate leg %q", test.parentTable, needle)
			}
		}
		if got := strings.Count(combined, "NOT EXISTS"); got != 1 {
			t.Errorf("OpenChildParentIDsSQL(%q) must dedupe the wisp leg's mirrored rows exactly once, got %d", test.parentTable, got)
		}
		if !strings.Contains(combined, "SELECT 1 FROM dependencies AS durable WHERE durable.id = dependency.id") {
			t.Errorf("OpenChildParentIDsSQL(%q) is missing the close gate's mirror dedupe", test.parentTable)
		}

		durable := DurableOpenChildParentIDsSQL(test.parentTable)
		// The durable leg reads the durable tables only. The wisps family's
		// leg still selects its own typed COLUMN here — children recorded in
		// dependencies against a wisp parent — so the check names the wisp
		// TABLE, not the column.
		if strings.Contains(durable, "wisp_dependencies") {
			t.Errorf("DurableOpenChildParentIDsSQL(%q) must read the durable tables only: %s", test.parentTable, durable)
		}
		if strings.Contains(durable, "NOT EXISTS") {
			t.Errorf("the durable leg has no mirror to dedupe: %s", durable)
		}
		if !strings.Contains(durable, test.column) {
			t.Errorf("DurableOpenChildParentIDsSQL(%q) must select the typed column %s", test.parentTable, test.column)
		}
	}
}
