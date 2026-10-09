//go:build cgo

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
)

// The LeaseReclaimer contract, run through the http client against a real bd
// serve. Nothing is parked: every member of ReclaimRequest has a wire member,
// and every refusal the role lists is raised before the dial in the role's own
// order (leasereclaimer.go).
//
// The claim that writes each lease goes over the wire too, through the client's
// Claimer; only the aging of a lease and the attaching of a label reach past
// the wire, into the embedded database behind the server, because no verb can
// make a lease stale on demand.
//
// THE HOOK CASE runs the reclaimer off a HookFiringStore wrapped around the
// client, the chain cmd/bd composes for a connected workspace: bd serve runs no
// hooks, so it is the CLIENT's decorator that owes one on_update per reverted
// row, re-reading each row over the wire to hand the script.
func newServedLeaseReclaimerFixture(t *testing.T, prefix string) conformance.LeaseReclaimerFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	reclaimer, err := env.subject.LeaseReclaimer()
	if err != nil {
		t.Fatalf("LeaseReclaimer(): %v", err)
	}
	claimer, err := env.subject.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	runner, fired := conformance.NewUpdateHookLog(t)
	hooked := reclaimer
	if runner != nil {
		if hooked, err = storage.NewHookFiringStore(env.subject, runner).LeaseReclaimer(); err != nil {
			t.Fatalf("hooked LeaseReclaimer(): %v", err)
		}
	}
	return conformance.LeaseReclaimerFixture{
		IssuePrefix:          env.prefix,
		LeaseReclaimer:       reclaimer,
		CreateIssue:          env.createIssue,
		Claimer:              claimer,
		Lifecycle:            lifecycle,
		Exec:                 env.exec,
		QueryScalar:          env.queryScalar,
		HookedLeaseReclaimer: hooked,
		FiredUpdates:         fired,
	}
}

func TestServedLeaseReclaimerRevertsAStaleLease(t *testing.T) {
	conformance.RunLeaseReclaimerRevertsAStaleLease(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr00"))
}

func TestServedLeaseReclaimerMintsARevisionThatFencesTheFormerHolder(t *testing.T) {
	conformance.RunLeaseReclaimerMintsARevisionThatFencesTheFormerHolder(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr01"))
}

func TestServedLeaseReclaimerLeavesALiveLeaseAlone(t *testing.T) {
	conformance.RunLeaseReclaimerLeavesALiveLeaseAlone(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr02"))
}

func TestServedLeaseReclaimerHonorsTheGraceWindow(t *testing.T) {
	conformance.RunLeaseReclaimerHonorsTheGraceWindow(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr03"))
}

func TestServedLeaseReclaimerScopesToTheNamedIDs(t *testing.T) {
	conformance.RunLeaseReclaimerScopesToTheNamedIDs(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr04"))
}

func TestServedLeaseReclaimerAnswersAnAbsentIDWithAnEmptyResult(t *testing.T) {
	conformance.RunLeaseReclaimerAnswersAnAbsentIDWithAnEmptyResult(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr05"))
}

func TestServedLeaseReclaimerScopesToAssigneesAndLabels(t *testing.T) {
	conformance.RunLeaseReclaimerScopesToAssigneesAndLabels(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr06"))
}

func TestServedLeaseReclaimerAttributesTheRecoveryEvent(t *testing.T) {
	conformance.RunLeaseReclaimerAttributesTheRecoveryEvent(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr07"))
}

func TestServedLeaseReclaimerFiresOnUpdateOncePerReclaimedRow(t *testing.T) {
	conformance.RunLeaseReclaimerFiresOnUpdateOncePerReclaimedRow(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr08"))
}

func TestServedLeaseReclaimerRefusesAMalformedRequest(t *testing.T) {
	conformance.RunLeaseReclaimerRefusesAMalformedRequest(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr09"))
}

func TestServedLeaseReclaimerAcceptsExactlyTheCap(t *testing.T) {
	conformance.RunLeaseReclaimerAcceptsExactlyTheCap(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr10"))
}

func TestServedLeaseReclaimerDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunLeaseReclaimerDoesNotMutateTheCallerRequest(t, t.Context(), newServedLeaseReclaimerFixture(t, "hlr11"))
}
