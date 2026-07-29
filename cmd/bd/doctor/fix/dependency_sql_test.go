package fix

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

func TestFixDependencyTargetExprMatchesCanonical(t *testing.T) {
	if fixDependencyTargetExpr != issueops.DepTargetExpr {
		t.Fatalf("fixDependencyTargetExpr = %q, want %q", fixDependencyTargetExpr, issueops.DepTargetExpr)
	}
}
