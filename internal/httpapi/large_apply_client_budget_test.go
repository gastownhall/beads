// Contributed by gascity from bd-enterprise (internal/httpapi/ent_large_apply_client_budget_test.go@1ee63dff5)
// to OSS beads under the MIT license.
package httpapi

import (
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// TestTheClientLargeApplyBudgetCoversTheServer holds the client's redeclared
// large-apply vocabulary (internal/httpclient/wire, which must not import this
// package) to the server's: the client must switch to its extended ceiling at
// exactly the item count where this server switches to its extended budget,
// and that ceiling must outlast everything the server may spend on such a
// request before it answers. Otherwise the client reports a failure for an
// apply the server, running detached from it, goes on to commit.
//
// What the server may spend, as the client's clock sees it:
//   - the request read, which readTimeout bounds as a whole and which spans
//     both route()'s wait for a general slot (acquire, up to
//     semAcquireTimeout) and the body upload the handler then decodes;
//   - the wait for the one-wide large-write slot (acquireLargeApply, up to
//     semAcquireTimeout);
//   - the run itself, DefaultLargeApplyCeiling from admission;
//   - drainGrace, the server's own allowance for writing a committed answer
//     back.
func TestTheClientLargeApplyBudgetCoversTheServer(t *testing.T) {
	if wire.LargeApplyItemThreshold != largeApplyItemThreshold {
		t.Errorf("wire.LargeApplyItemThreshold = %d, server largeApplyItemThreshold = %d",
			wire.LargeApplyItemThreshold, largeApplyItemThreshold)
	}
	read := max(readTimeout, semAcquireTimeout)
	need := read + semAcquireTimeout + DefaultLargeApplyCeiling + drainGrace
	if wire.LargeApplyTimeout < need {
		t.Errorf("wire.LargeApplyTimeout = %v; the server may spend up to %v (request read %v + large-write slot wait %v + ceiling %v + drain grace %v)",
			wire.LargeApplyTimeout, need, read, semAcquireTimeout, DefaultLargeApplyCeiling, drainGrace)
	}
	if wire.DefaultTimeout >= wire.LargeApplyTimeout {
		t.Errorf("wire.DefaultTimeout = %v is not shorter than wire.LargeApplyTimeout = %v", wire.DefaultTimeout, wire.LargeApplyTimeout)
	}
}
