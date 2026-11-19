//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestOutgoingDependencyReadersAgree runs the cross-backend outgoing-reader
// contract (bd-uschhs) against the embedded store, whose one-connection pool
// also proves the record read closes its rows before the next query.
func TestOutgoingDependencyReadersAgree(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "odr")
	conformance.RunOutgoingDependencyReadersAgree(t, te.store, func(t *testing.T, stmts ...string) {
		t.Helper()
		te.execOnOneConn(t, t.Context(), stmts...)
	})
}
