//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_journal_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
)

// The Journal contract against the served surface.
//
// It is the FOURTH leg of a role that has three in-tree bodies and one shared
// one: both Dolt stores and the unit-of-work provider bottom out in
// issueops.ReadEventsPageInTx, so those three are one reading plus an engine
// check. This leg is genuinely independent — a JSON envelope over HTTP, a typed
// error rebuilt from a problem document, and a page bound that is the handler's
// rather than the role's — which is why the contract's own header calls the
// truncation case the one that matters more than it looks: the alias identity it
// checks at runtime is checked here across a WIRE.
//
// THE SPLIT BETWEEN THE FIXTURE'S HOOKS IS THE HARNESS RULE, and on this role it
// is also the role's own doctrine. Journal is bound to the CLIENT; activation
// and retention are bound to the REFERENCE store, because they are operator
// surface that journalops deliberately keeps off the role — a publishing surface
// must not be one line away from a delete — and this client implements neither.
// So the fixture's shape is not a testing convenience here, it is the same split
// the production types make.
//
// ORDER AND SEQUENTIALITY ARE LOAD-BEARING, exactly as they are on the three
// legs upstream: two cases PRUNE, one of them to nothing, every case rebaselines
// off the live head, and EveryMutationKindLandsARow runs last on purpose.
func TestServedJournalContract(t *testing.T) {
	env := newServedEnv(t, "hjrn")
	ctx := t.Context()
	fixture := newServedJournalFixture(t, env)
	t.Cleanup(func() {
		// Journaling is INSTANCE-scoped, and the reference store outlives this
		// fixture inside one test binary. Leaving it on would journal every
		// mutation a later case in this package makes.
		env.reference.SetEventsJournalEnabled(false)
	})

	t.Run("PagesAreSeqAscendingAndSinceExclusive", func(t *testing.T) {
		conformance.RunJournalPagesAreSeqAscendingAndSinceExclusive(t, ctx, fixture)
	})
	t.Run("HeadArrivesWithItsRowsAndDetectsCaughtUp", func(t *testing.T) {
		conformance.RunJournalHeadArrivesWithItsRowsAndDetectsCaughtUp(t, ctx, fixture)
	})
	t.Run("LimitCapsRowsNotHead", func(t *testing.T) {
		conformance.RunJournalLimitCapsRowsNotHead(t, ctx, fixture)
	})
	t.Run("TruncationIsTypedAndNamesTheWindow", func(t *testing.T) {
		conformance.RunJournalTruncationIsTypedAndNamesTheWindow(t, ctx, fixture)
	})
	t.Run("HeadSurvivesAFullPrune", func(t *testing.T) {
		conformance.RunJournalHeadSurvivesAFullPrune(t, ctx, fixture)
	})
	t.Run("EveryMutationKindLandsARow", func(t *testing.T) {
		conformance.RunJournalEveryMutationKindLandsARow(t, ctx, fixture)
	})
}

// newServedJournalFixture binds the role to the client through the TYPE
// ASSERTION `bd serve` makes, never through the concrete method set.
//
// The journal is not on storage.DoltStorage — journalops states why a role with
// no accessor is the right shape for it — so publishing it IS implementing this
// interface, and a client that stopped would fail here rather than keep
// compiling against a struct.
func newServedJournalFixture(t *testing.T, env *servedEnv) conformance.JournalFixture {
	t.Helper()
	cursor, ok := any(env.subject).(storage.EventsJournalCursor)
	if !ok {
		t.Fatalf("%T does not implement storage.EventsJournalCursor", env.subject)
	}
	reference := env.reference
	return conformance.JournalFixture{
		IssuePrefix:       env.prefix,
		Journal:           cursor,
		SetJournalEnabled: reference.SetEventsJournalEnabled,
		Prune:             reference.PruneEventsJournal,
		// EVERY MUTATION DRIVES THE REFERENCE STORE, which is the harness rule
		// and not a shortcut around a refusing client. Two of the seven have no
		// client-side spelling at all — a raw delete and a raw dependency remove
		// are on the unsupported allowlist — and the other five would prove the
		// client agrees with itself: the question this contract asks is whether
		// the client can READ the journal a server wrote.
		Mutations: servedJournalMutations(reference),
	}
}
