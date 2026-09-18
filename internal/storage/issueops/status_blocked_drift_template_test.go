package issueops

import (
	"strings"
	"testing"
)

// driftTemplates is every template in status_blocked_drift.go that decides
// which status='blocked' rows count as drift. All three must agree, because
// the fix's select-then-recheck shape relies on the UPDATE repeating the
// SELECT's membership test exactly.
func driftTemplates(table, depTable string) map[string]string {
	return map[string]string{
		"countStatusBlockedDriftSQL":  countStatusBlockedDriftSQL(table, depTable),
		"selectStatusBlockedDriftSQL": selectStatusBlockedDriftSQL(table, depTable),
		"fixOneStatusBlockedDriftSQL": fixOneStatusBlockedDriftSQL(table, depTable),
	}
}

func driftTablePairs() []struct{ table, depTable string } {
	return []struct{ table, depTable string }{
		{"issues", "dependencies"},
		{"wisps", "wisp_dependencies"},
	}
}

// TestStatusBlockedDriftSharesTheBlockedDefinition pins the structural half of
// the gastownhall/beads#6565 fix: the status-drift templates must embed
// shouldBeBlockedIDsUnionSQL VERBATIM, not a predicate that happens to agree
// with it today.
//
// The original detector carried its own 'blocks'-only union. Two predicates
// for one concept is the whole bug — every row blocked for some other reason
// fell outside the narrow set, was reported as drift, and was force-opened by
// --fix while is_blocked stayed 1. A container-backed test catches that per
// edge type, one test at a time; this catches the shape itself, in
// milliseconds, the moment someone writes a second definition.
func TestStatusBlockedDriftSharesTheBlockedDefinition(t *testing.T) {
	for _, pair := range driftTablePairs() {
		want := shouldBeBlockedIDsUnionSQL(pair.depTable)
		for name, tmpl := range driftTemplates(pair.table, pair.depTable) {
			t.Run(pair.table+"/"+name, func(t *testing.T) {
				if !strings.Contains(tmpl, want) {
					t.Errorf("%s does not embed shouldBeBlockedIDsUnionSQL(%q) verbatim — "+
						"the drift predicate has drifted from the definition is_blocked uses, "+
						"which force-opens rows the graph still holds blocked.\ngot:\n%s",
						name, pair.depTable, tmpl)
				}
			})
		}
	}
}

// TestStatusBlockedDriftPredicateCoversEveryBlockingReason names the reasons
// out loud, so a narrowing of the shared builder itself still fails here with
// a message that says which reason went missing rather than an opaque
// substring mismatch.
func TestStatusBlockedDriftPredicateCoversEveryBlockingReason(t *testing.T) {
	reasons := map[string]string{
		"blocks / conditional-blocks": "d.type = 'blocks' OR d.type = 'conditional-blocks'",
		"inherited parent-child":      "d.type = 'parent-child'",
		"held waits-for gate":         "d.type = 'waits-for'",
		"wisp-target edge":            "d.depends_on_wisp_id",
	}
	for _, pair := range driftTablePairs() {
		tmpl := countStatusBlockedDriftSQL(pair.table, pair.depTable)
		for reason, frag := range reasons {
			t.Run(pair.table+"/"+reason, func(t *testing.T) {
				if !strings.Contains(tmpl, frag) {
					t.Errorf("status-drift predicate no longer accounts for %s (missing %q) — "+
						"rows blocked only for that reason will be reported as drift and force-opened",
						reason, frag)
				}
			})
		}
	}
}

// TestStatusBlockedDriftTemplatesAreFullyResolved guards the splice itself.
// Each drift template Sprintf's the union's finished text into a %[2]s verb, so
// a percent sign appearing anywhere in the union would be consumed by that
// outer Sprintf and corrupt the statement — the same failure mode the batched
// templates next door budget for, arriving by a different route. These
// statements take no bound arguments beyond fixOneStatusBlockedDriftSQL's
// single id, so a leftover verb is pure corruption, never a placeholder.
func TestStatusBlockedDriftTemplatesAreFullyResolved(t *testing.T) {
	for _, pair := range driftTablePairs() {
		for name, tmpl := range driftTemplates(pair.table, pair.depTable) {
			t.Run(pair.table+"/"+name, func(t *testing.T) {
				if strings.ContainsAny(tmpl, "%") {
					t.Errorf("%s still contains a %% verb after expansion:\n%s", name, tmpl)
				}
				wantArgs := 0
				if name == "fixOneStatusBlockedDriftSQL" {
					wantArgs = 1 // the per-row id
				}
				if got := strings.Count(tmpl, "?"); got != wantArgs {
					t.Errorf("%s placeholder count = %d, want %d", name, got, wantArgs)
				}
			})
		}
	}
}
