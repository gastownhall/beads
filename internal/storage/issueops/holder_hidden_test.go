package issueops

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// TestHolderTokenNotInSelectColumns enforces the design invariant (D12): the
// holder token is enforcement-only state and must NEVER appear in the
// canonical issue read surface. If it did, a fenced-out worker could recover
// the current token via `bd show --json` and present it, collapsing the token
// into the same defeated class as a caller-quoted fence. It is written only in
// the WHERE/SET of enforced mutations and read only inside the advisory
// classification.
func TestHolderTokenNotInSelectColumns(t *testing.T) {
	if strings.Contains(sqlbuild.IssueSelectColumns, "holder_token") {
		t.Error("holder_token is present in sqlbuild.IssueSelectColumns — it must stay out of the read surface (design D12)")
	}
	if strings.Contains(IssueSelectColumns, "holder_token") {
		t.Error("holder_token is present in issueops.IssueSelectColumns — it must stay out of the read surface (design D12)")
	}
}
