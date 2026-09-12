package issueops

import (
	"strings"
	"testing"
)

// The batched mark/unmark templates reach the engine through
// expandBatchTemplate, which decides how many IN-lists to splice by counting
// "%s" textually. Two things are therefore load-bearing and invisible to the
// sqlmock suite next door — its expectations match an unanchored statement
// prefix and never assert bound args, so it stays green against a miscounted
// template: the occurrence count itself, and the absence of any other percent
// sign in the finished template (a future LIKE 'x%' or %d in a union leg would
// shift the count or corrupt the statement). Today a break surfaces only in the
// container-backed dolt suite; these tests pin it at the fast tier
// (gastownhall/beads#6291).

// batchTemplateOccurrences is the number of IN-lists a batched template
// carries: the outer row filter, plus one per leg of the scoped
// should-be-blocked union.
const batchTemplateOccurrences = 6

func batchedTemplates() map[string]string {
	return map[string]string{
		"markBlockedTemplateForIssues":   markBlockedTemplateForIssues(),
		"unmarkBlockedTemplateForIssues": unmarkBlockedTemplateForIssues(),
		"markBlockedTemplateForWisps":    markBlockedTemplateForWisps(),
		"unmarkBlockedTemplateForWisps":  unmarkBlockedTemplateForWisps(),
	}
}

func TestBatchedTemplatePercentBudget(t *testing.T) {
	for name, tmpl := range batchedTemplates() {
		t.Run(name, func(t *testing.T) {
			if got := strings.Count(tmpl, "%s"); got != batchTemplateOccurrences {
				t.Errorf("%%s count = %d, want %d — expandBatchTemplate binds one id group per occurrence, so a changed count must be a deliberate edit here", got, batchTemplateOccurrences)
			}
			// The stray-percent guard: every percent sign in the finished
			// template must belong to one of the %s verbs counted above.
			if got := strings.Count(tmpl, "%"); got != batchTemplateOccurrences {
				t.Errorf("total %% count = %d, want %d — a percent sign outside a %%s (LIKE 'x%%', %%d, %%%%) miscounts the template or corrupts the statement", got, batchTemplateOccurrences)
			}
		})
	}
}

func TestWaitsForGateBlockedSQLCarriesNoPercent(t *testing.T) {
	// The gate is spliced into every template as a resolved constant, so any
	// percent sign it grew would land in the counted text above.
	if got := strings.Count(waitsForGateBlockedSQL, "%"); got != 0 {
		t.Errorf("waitsForGateBlockedSQL %% count = %d, want 0", got)
	}
}

func TestExpandBatchTemplateFillsEveryOccurrence(t *testing.T) {
	placeholders, args := buildSQLInClause([]string{"issue-1", "issue-2"})
	wantArgs := batchTemplateOccurrences * len(args)

	for name, tmpl := range batchedTemplates() {
		t.Run(name, func(t *testing.T) {
			stmt, stmtArgs := expandBatchTemplate(tmpl, placeholders, args)

			if strings.ContainsAny(stmt, "%") {
				t.Errorf("expanded statement still contains %%: %s", stmt)
			}
			if got := strings.Count(stmt, "?"); got != wantArgs {
				t.Errorf("placeholder count = %d, want %d", got, wantArgs)
			}
			if got := len(stmtArgs); got != wantArgs {
				t.Fatalf("arg count = %d, want %d — placeholders and args must stay in lockstep", got, wantArgs)
			}
			// Each occurrence gets the same group, repeated in order.
			for i, arg := range stmtArgs {
				if want := args[i%len(args)]; arg != want {
					t.Errorf("arg %d = %v, want %v", i, arg, want)
				}
			}
		})
	}
}

func TestExpandBatchTemplateSingleOccurrenceDegrades(t *testing.T) {
	// A template with one %s is the plain Sprintf it always was: the args pass
	// through untouched rather than being repeated.
	placeholders, args := buildSQLInClause([]string{"issue-1", "issue-2"})
	stmt, stmtArgs := expandBatchTemplate("SELECT id FROM issues WHERE id IN (%s)", placeholders, args)

	if want := "SELECT id FROM issues WHERE id IN (?,?)"; stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
	if len(stmtArgs) != len(args) {
		t.Errorf("arg count = %d, want %d", len(stmtArgs), len(args))
	}
}
