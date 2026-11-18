package issueops

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestFailureCloseReasonSQLRendersEveryKeyword pins the renderer's contract
// with the batched templates it is spliced into: one INSTR clause per
// FailureCloseKeywords entry, quotes doubled, and no percent sign or question
// mark (expandBatchTemplate counts both). Execution parity with IsFailureClose
// is TestFailureCloseReasonSQLMatchesIsFailureClose (internal/storage/dolt);
// comparing strings alone proves nothing about what the engine computes.
func TestFailureCloseReasonSQLRendersEveryKeyword(t *testing.T) {
	sql := FailureCloseReasonSQL("t.close_reason")
	if got, want := strings.Count(sql, "INSTR("), len(types.FailureCloseKeywords); got != want {
		t.Fatalf("INSTR clauses = %d, want one per keyword (%d):\n%s", got, want, sql)
	}
	for _, keyword := range types.FailureCloseKeywords {
		quoted := "'" + strings.ReplaceAll(keyword, "'", "''") + "'"
		if !strings.Contains(sql, quoted) {
			t.Errorf("keyword %q not rendered as %s:\n%s", keyword, quoted, sql)
		}
	}
	if !strings.Contains(sql, "'won''t fix'") {
		t.Errorf("apostrophe keyword not doubled:\n%s", sql)
	}
	for _, rendered := range []string{sql, BlockingTargetHoldsSQL("d.type", "t.status", "t.close_reason")} {
		if strings.ContainsAny(rendered, "%?") {
			t.Errorf("rendered predicate carries a template metacharacter:\n%s", rendered)
		}
	}
}

// TestBlockingTargetHoldsSQLGuardContract pins the target guard's shape on the
// ci lane, where the Dolt suites skip: the reason is NULL-guarded, a pinned
// target releases, and the closed arm splits on the conditional-blocks type.
func TestBlockingTargetHoldsSQLGuardContract(t *testing.T) {
	if pred := FailureCloseReasonSQL("t.close_reason"); !strings.Contains(pred, "COALESCE(t.close_reason, '')") {
		t.Errorf("predicate must NULL-guard the reason expression: %s", pred)
	}
	guard := BlockingTargetHoldsSQL("d.type", "t.status", "t.close_reason")
	if !strings.Contains(guard, "t.status <> 'pinned'") {
		t.Errorf("target guard must release a pinned target: %s", guard)
	}
	if !strings.Contains(guard, "d.type = 'conditional-blocks'") {
		t.Errorf("target guard must split on the conditional-blocks type: %s", guard)
	}
}

// The union's status-tested legs apply the conditional-blocks close rule
// (GH#5577): the leg guard names the target's close_reason and the
// conditional-blocks type, on the ci lane where the dolt suites skip.
func TestShouldBeBlockedUnionAppliesConditionalCloseGuard(t *testing.T) {
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		q := strings.Join(strings.Fields(shouldBeBlockedIDsUnionCoreSQL(depTable, "", "", "")), " ")
		if got := strings.Count(q, "d.type = 'conditional-blocks' AND NOT (INSTR"); got != 2 {
			t.Errorf("%s: %d conditional close guards in the status-tested legs, want 2: %s", depTable, got, q)
		}
		if !strings.Contains(q, "t.close_reason") {
			t.Errorf("%s: status-tested legs must read the target's close_reason: %s", depTable, q)
		}
	}
}
