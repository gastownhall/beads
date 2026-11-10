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
