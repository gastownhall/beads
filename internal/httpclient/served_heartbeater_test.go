//go:build cgo

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
)

// The Heartbeater contract, run through the http client's accessor against a
// real bd serve: POST /v0/beads/issues/{id}:heartbeat.
// Nothing is parked: the id rides in the path and the actor is the body's one
// member, and every request rule the role lists is raised before the dial
// (heartbeater.go).
//
// Seeding and every precondition (the claim, the close, the release, the
// reclaim, the status move) go through the REFERENCE store's own roles, never
// the wire, per the harness's binding rule; the post-state is read raw.
//
// THE HOOK CASE runs the heartbeater off a HookFiringStore wrapped around the
// client, the chain cmd/bd composes for a connected workspace: bd serve runs
// no hooks, so it is the CLIENT's decorator that must fire none for a
// refresh — the native refresh fires none on any route (a re-arm, recorded as
// a mutation, fires on_update).
func newServedHeartbeaterFixture(t *testing.T, prefix string) conformance.HeartbeaterFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	ref := env.reference
	heartbeater, err := env.subject.Heartbeater()
	if err != nil {
		t.Fatalf("Heartbeater(): %v", err)
	}
	claimer, err := ref.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	lifecycle, err := ref.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	releaser, err := ref.Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	reclaimer, err := ref.LeaseReclaimer()
	if err != nil {
		t.Fatalf("LeaseReclaimer(): %v", err)
	}
	runner, fired := conformance.NewUpdateHookLog(t)
	hooked := heartbeater
	if runner != nil {
		if hooked, err = storage.NewHookFiringStore(env.subject, runner).Heartbeater(); err != nil {
			t.Fatalf("hooked Heartbeater(): %v", err)
		}
	}
	return conformance.HeartbeaterFixture{
		IssuePrefix:                env.prefix,
		Heartbeater:                heartbeater,
		CreateIssue:                env.createIssue,
		CreateWisp:                 env.createWisp,
		Claimer:                    claimer,
		Lifecycle:                  lifecycle,
		Releaser:                   releaser,
		LeaseReclaimer:             reclaimer,
		Exec:                       env.exec,
		QueryScalar:                env.queryScalar,
		CountHistory:               env.countHistory,
		SetJournalEnabled:          ref.SetEventsJournalEnabled,
		SetVersionedHistoryEnabled: ref.SetVersionedHistoryEnabled,
		HookedHeartbeater:          hooked,
		FiredUpdates:               fired,
	}
}

func TestServedHeartbeaterRefreshesTheHoldersLease(t *testing.T) {
	conformance.RunHeartbeaterRefreshesTheHoldersLease(t, t.Context(), newServedHeartbeaterFixture(t, "hhb00"))
}

func TestServedHeartbeaterChangesNothingOnTheRow(t *testing.T) {
	conformance.RunHeartbeaterChangesNothingOnTheRow(t, t.Context(), newServedHeartbeaterFixture(t, "hhb01"))
}

func TestServedHeartbeaterAGuardedWriteWithThePreHeartbeatRevisionStillWins(t *testing.T) {
	conformance.RunHeartbeaterAGuardedWriteWithThePreHeartbeatRevisionStillWins(t, t.Context(), newServedHeartbeaterFixture(t, "hhb02"))
}

func TestServedHeartbeaterRefusesAForeignHolder(t *testing.T) {
	conformance.RunHeartbeaterRefusesAForeignHolder(t, t.Context(), newServedHeartbeaterFixture(t, "hhb03"))
}

func TestServedHeartbeaterRefusesAfterClose(t *testing.T) {
	conformance.RunHeartbeaterRefusesAfterClose(t, t.Context(), newServedHeartbeaterFixture(t, "hhb04"))
}

func TestServedHeartbeaterRefusesAfterRelease(t *testing.T) {
	conformance.RunHeartbeaterRefusesAfterRelease(t, t.Context(), newServedHeartbeaterFixture(t, "hhb05"))
}

func TestServedHeartbeaterRefusesAfterReclaim(t *testing.T) {
	conformance.RunHeartbeaterRefusesAfterReclaim(t, t.Context(), newServedHeartbeaterFixture(t, "hhb06"))
}

func TestServedHeartbeaterRefusesAfterStatusMovedOffInProgress(t *testing.T) {
	conformance.RunHeartbeaterRefusesAfterStatusMovedOffInProgress(t, t.Context(), newServedHeartbeaterFixture(t, "hhb07"))
}

func TestServedHeartbeaterRefusesAWisp(t *testing.T) {
	conformance.RunHeartbeaterRefusesAWisp(t, t.Context(), newServedHeartbeaterFixture(t, "hhb08"))
}

func TestServedHeartbeaterRearmsAHandDoledClaim(t *testing.T) {
	conformance.RunHeartbeaterRearmsAHandDoledClaim(t, t.Context(), newServedHeartbeaterFixture(t, "hhb09"))
}

func TestServedHeartbeaterAnswersNotFoundForAnAbsentID(t *testing.T) {
	conformance.RunHeartbeaterAnswersNotFoundForAnAbsentID(t, t.Context(), newServedHeartbeaterFixture(t, "hhb10"))
}

func TestServedHeartbeaterRefusesABlankOrOverLongActor(t *testing.T) {
	conformance.RunHeartbeaterRefusesABlankOrOverLongActor(t, t.Context(), newServedHeartbeaterFixture(t, "hhb11"))
}

func TestServedHeartbeaterDoesNotMutateTheRequest(t *testing.T) {
	conformance.RunHeartbeaterDoesNotMutateTheRequest(t, t.Context(), newServedHeartbeaterFixture(t, "hhb12"))
}

func TestServedHeartbeaterASameSecondHeartbeatIsNotARearm(t *testing.T) {
	conformance.RunHeartbeaterASameSecondHeartbeatIsNotARearm(t, t.Context(), newServedHeartbeaterFixture(t, "hhb13"))
}

func TestServedHeartbeaterRefusesAnOrphanedLeaseOnAReleasedIssue(t *testing.T) {
	conformance.RunHeartbeaterRefusesAnOrphanedLeaseOnAReleasedIssue(t, t.Context(), newServedHeartbeaterFixture(t, "hhb14"))
}

func TestServedHeartbeaterRefusesAnOrphanedLeaseOnAClosedIssue(t *testing.T) {
	conformance.RunHeartbeaterRefusesAnOrphanedLeaseOnAClosedIssue(t, t.Context(), newServedHeartbeaterFixture(t, "hhb15"))
}

func TestServedHeartbeaterRefusesAnOrphanedLeaseOnAnIssueSomeoneElseHolds(t *testing.T) {
	conformance.RunHeartbeaterRefusesAnOrphanedLeaseOnAnIssueSomeoneElseHolds(t, t.Context(), newServedHeartbeaterFixture(t, "hhb16"))
}

func TestServedHeartbeaterCommitsOnlyItsOwnTables(t *testing.T) {
	conformance.RunHeartbeaterCommitsOnlyItsOwnTables(t, t.Context(), newServedHeartbeaterFixture(t, "hhb17"))
}
