package dolt

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestOutgoingDependencyReadersAgree runs the cross-backend outgoing-reader
// contract (bd-uschhs) against the server store; the embedded package runs the
// same contract, so the two backends agree on one answer.
func TestOutgoingDependencyReadersAgree(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	conformance.RunOutgoingDependencyReadersAgree(t, store, func(t *testing.T, stmts ...string) {
		t.Helper()
		execOnOneConn(t, t.Context(), store, stmts...)
	})
}
