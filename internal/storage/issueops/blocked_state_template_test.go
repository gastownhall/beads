package issueops

import (
	"strings"
	"testing"
)

// The batched mark/unmark templates reach the engine through
// expandBatchTemplate, which fills the batch IN-lists by walking the template
// for "%s" and interleaves the exogeneity ids the template already carries as
// "?" placeholders. Three things are therefore load-bearing and invisible to
// the sqlmock suite next door — its expectations match an unanchored statement
// prefix and never assert bound args, so it stays green against a miscounted
// template: the %s occurrence count, the absence of any other percent sign in
// the finished template (a future LIKE 'x%' or %d in a union leg would shift
// the count or corrupt the statement), and the absence of any "?" the caller
// did not bind. Today a break surfaces only in the container-backed dolt
// suite; these tests pin it at the fast tier (gastownhall/beads#6291).

// batchTemplateOccurrences is the number of batch IN-lists a batched template
// carries: the outer row filter, and one per leg of the scoped
// should-be-blocked union.
//
// It was briefly 8: the parent-child legs' exogeneity set was spliced as the
// query that derives it, and that query carried the batch scope twice more.
// The set is now read once per batch and bound as ids
// (scopedExplainedParentsInTx), which is both 6.9x faster on a 200-id batch
// and why the ? group below exists.
const batchTemplateOccurrences = 6

// explainedFixture is a non-empty exogeneity set of each kind, so the bound-?
// plumbing is exercised rather than degenerating to the empty case.
func explainedFixture() subtreeExplainedParents {
	return subtreeExplainedParents{
		issueParents: []string{"parent-1", "parent-2"},
		wispParents:  []string{"wisp-parent-1"},
	}
}

func batchedTemplates(explained subtreeExplainedParents) map[string]string {
	return map[string]string{
		"markBlockedTemplateForIssues":   markBlockedTemplateForIssues(explained),
		"unmarkBlockedTemplateForIssues": unmarkBlockedTemplateForIssues(explained),
		"markBlockedTemplateForWisps":    markBlockedTemplateForWisps(explained),
		"unmarkBlockedTemplateForWisps":  unmarkBlockedTemplateForWisps(explained),
	}
}

func TestBatchedTemplatePercentBudget(t *testing.T) {
	for name, tmpl := range batchedTemplates(explainedFixture()) {
		t.Run(name, func(t *testing.T) {
			if got := strings.Count(tmpl, "%s"); got != batchTemplateOccurrences {
				t.Errorf("%%s count = %d, want %d — expandBatchTemplate binds one batch id group per occurrence, so a changed count must be a deliberate edit here", got, batchTemplateOccurrences)
			}
			// The stray-percent guard: every percent sign in the finished
			// template must belong to one of the %s verbs counted above.
			if got := strings.Count(tmpl, "%"); got != batchTemplateOccurrences {
				t.Errorf("total %% count = %d, want %d — a percent sign outside a %%s (LIKE 'x%%', %%d, %%%%) miscounts the template or corrupts the statement", got, batchTemplateOccurrences)
			}
		})
	}
}

// TestBatchedTemplateQuestionMarksAreTheExogeneitySet pins the other half of
// expandBatchTemplate's contract: the ONLY ? a template carries are the two
// parent-child legs' bound exogeneity ids, so an empty set leaves none.
func TestBatchedTemplateQuestionMarksAreTheExogeneitySet(t *testing.T) {
	explained := explainedFixture()
	want := len(explained.issueParents) + len(explained.wispParents)
	for name, tmpl := range batchedTemplates(explained) {
		t.Run(name, func(t *testing.T) {
			if got := strings.Count(tmpl, "?"); got != want {
				t.Errorf("? count = %d, want %d (the bound exogeneity ids)", got, want)
			}
		})
	}
	for name, tmpl := range batchedTemplates(subtreeExplainedParents{}) {
		t.Run(name+"/empty", func(t *testing.T) {
			if got := strings.Count(tmpl, "?"); got != 0 {
				t.Errorf("? count = %d over an empty exogeneity set, want 0", got)
			}
		})
	}
}

func TestWaitsForGateBlockedSQLCarriesNoPercentOrPlaceholder(t *testing.T) {
	// The gate is spliced into every template as a resolved constant, so any
	// percent sign or placeholder it grew would land in the counted text above.
	if got := strings.Count(waitsForGateBlockedSQL, "%"); got != 0 {
		t.Errorf("waitsForGateBlockedSQL %% count = %d, want 0", got)
	}
	if got := strings.Count(waitsForGateBlockedSQL, "?"); got != 0 {
		t.Errorf("waitsForGateBlockedSQL ? count = %d, want 0", got)
	}
}

func TestExpandBatchTemplateFillsEveryOccurrence(t *testing.T) {
	placeholders, args := buildSQLInClause([]string{"issue-1", "issue-2"})
	explained := explainedFixture()
	bound := explained.args()
	wantArgs := batchTemplateOccurrences*len(args) + len(bound)

	for name, tmpl := range batchedTemplates(explained) {
		t.Run(name, func(t *testing.T) {
			stmt, stmtArgs, err := expandBatchTemplate(tmpl, placeholders, args, bound)
			if err != nil {
				t.Fatalf("expandBatchTemplate: %v", err)
			}

			if strings.ContainsAny(stmt, "%") {
				t.Errorf("expanded statement still contains %%: %s", stmt)
			}
			if got := strings.Count(stmt, "?"); got != wantArgs {
				t.Errorf("placeholder count = %d, want %d", got, wantArgs)
			}
			if got := len(stmtArgs); got != wantArgs {
				t.Fatalf("arg count = %d, want %d — placeholders and args must stay in lockstep", got, wantArgs)
			}
			// TEXT ORDER: the batch group for the outer filter and legs 1-2,
			// then leg 3's batch group followed by the issue parents, then leg
			// 4's batch group followed by the wisp parents, then leg 5's.
			var want []interface{}
			for k := 0; k < batchTemplateOccurrences; k++ {
				want = append(want, args...)
				if k == 3 {
					want = append(want, "parent-1", "parent-2")
				}
				if k == 4 {
					want = append(want, "wisp-parent-1")
				}
			}
			for i, arg := range stmtArgs {
				if arg != want[i] {
					t.Errorf("arg %d = %v, want %v", i, arg, want[i])
				}
			}
		})
	}
}

// TestExpandBatchTemplateRefusesABoundMismatch is the guard that keeps a
// miscounted bound group from reaching the engine as an opaque argument-count
// error: the template's ? placeholders and the bound ids must agree exactly.
func TestExpandBatchTemplateRefusesABoundMismatch(t *testing.T) {
	placeholders, args := buildSQLInClause([]string{"issue-1"})
	tmpl := markBlockedTemplateForIssues(subtreeExplainedParents{issueParents: []string{"parent-1"}})

	if _, _, err := expandBatchTemplate(tmpl, placeholders, args, nil); err == nil {
		t.Error("want an error when the template carries a ? the caller did not bind")
	}
	if _, _, err := expandBatchTemplate(
		tmpl, placeholders, args, []interface{}{"parent-1", "parent-2"}); err == nil {
		t.Error("want an error when the caller binds more ids than the template carries")
	}
}

func TestExpandBatchTemplateSingleOccurrenceDegrades(t *testing.T) {
	// A template with one %s and no bound ids is the plain Sprintf it always
	// was: the args pass through untouched rather than being repeated.
	placeholders, args := buildSQLInClause([]string{"issue-1", "issue-2"})
	stmt, stmtArgs, err := expandBatchTemplate(
		"SELECT id FROM issues WHERE id IN (%s)", placeholders, args, nil)
	if err != nil {
		t.Fatalf("expandBatchTemplate: %v", err)
	}

	if want := "SELECT id FROM issues WHERE id IN (?,?)"; stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
	if len(stmtArgs) != len(args) {
		t.Errorf("arg count = %d, want %d", len(stmtArgs), len(args))
	}
}
