//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_readycounter_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/issueops"
)

// The ReadyCounter contract, run through the composition (design D8 row 2).
//
// Its central promise is an IDENTITY with the listing — CountReady(r).Total ==
// len(Reader.Ready(r with Limit=0).Items) — so the fixture binds BOTH roles off
// the http client and the identity is proved end to end over two separate
// operations, countReadyWork and listReadyWork. On this backend that is a
// stronger statement than it is locally: the two answers are assembled by two
// different handlers on the server AND by two different encodings on the way
// there, and the encoder's shared filter list is what keeps them one question.

func servedReadyCounterFixture(t *testing.T, name string) conformance.ReadyCounterFixture {
	t.Helper()
	c := composition(t)
	f := c.fixture(t)
	counter := bindRole(t, f, func(s *Store) (issueops.ReadyCounter, error) { return s.ReadyCounter() })
	reader := bindRole(t, f, func(s *Store) (issueops.Reader, error) { return s.IssueReader() })
	return conformance.ReadyCounterFixture{
		IssuePrefix:   servedIssuePrefix + "-" + name,
		ReadyCounter:  counter,
		Reader:        reader,
		CreateIssue:   c.seedIssue,
		CreateWisp:    c.seedIssue,
		AddDependency: c.seedDependency,
		// The raw-row read the status case needs. Both surfaces it compares hide
		// a row this role must not count, so neither can tell "excluded" from
		// "never seeded" — and a nil hook here is a SKIP, which this tier treats
		// as a failure.
		QueryScalar:  c.queryScalar,
		CountHistory: c.countHistory,
	}
}

func TestServedReadyCounterEqualsTheUnboundedPage(t *testing.T) {
	conformance.RunReadyCounterEqualsTheUnboundedPage(t, t.Context(), servedReadyCounterFixture(t, "rc"))
}

func TestServedReadyCounterRejectsLimitAndOffset(t *testing.T) {
	conformance.RunReadyCounterRejectsLimitAndOffset(t, t.Context(), servedReadyCounterFixture(t, "rc"))
}

func TestServedReadyCounterCountsTheBlockerAwareSet(t *testing.T) {
	conformance.RunReadyCounterCountsTheBlockerAwareSet(t, t.Context(), servedReadyCounterFixture(t, "rc"))
}

func TestServedReadyCounterEphemeralGateMatchesTheListing(t *testing.T) {
	conformance.RunReadyCounterEphemeralGateMatchesTheListing(t, t.Context(), servedReadyCounterFixture(t, "rc"))
}

// TestServedReadyCounterCountsOnlyTheOpenRowsItsListingLists is the status half
// of the identity: an in-progress row and a closed one are outside the ready
// front, and the count has to exclude them for the same reason the listing does.
// It reads both seeds back through QueryScalar first, because a count that
// excluded them and a seed that never landed are the same number.
func TestServedReadyCounterCountsOnlyTheOpenRowsItsListingLists(t *testing.T) {
	conformance.RunReadyCounterCountsOnlyTheOpenRowsItsListingLists(t, t.Context(), servedReadyCounterFixture(t, "rc"))
}

func TestServedReadyCounterEmptyFrontIsZeroAndNil(t *testing.T) {
	conformance.RunReadyCounterEmptyFrontIsZeroAndNil(t, t.Context(), servedReadyCounterFixture(t, "rc"))
}

func TestServedReadyCounterWritesNothing(t *testing.T) {
	conformance.RunReadyCounterWritesNothing(t, t.Context(), servedReadyCounterFixture(t, "rc"))
}

func TestServedReadyCounterDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunReadyCounterDoesNotMutateTheCallerRequest(t, t.Context(), servedReadyCounterFixture(t, "rc"))
}
