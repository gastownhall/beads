package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// TestPostCommitRecheckPublishesOutsideItsTransaction is the structural guard
// for the recheck's publication ordering (the lost-update hazard
// doltAddAndCommitInTx documents, LatentLabsSpace/NEXUS#92).
//
// The hazard needs a writer that commits inside the recheck's OWN transaction
// window, and nothing in the store exposes that window to a test: the recheck
// body is not test-supplied, so the in-transaction ordering cannot be turned
// into a red assertion the way the runner tails can
// (close_recheck_blocked_test.go). Restoring it would therefore be invisible —
// every test in this package stays green either way — which is exactly the
// situation the journal- and version-scope completeness guards exist for, so
// the ordering gets the same kind of defense.
//
// Known limit, shared with those guards: the check is name-based over this
// package's syntax. It covers a publication call anywhere in the recheck's own
// body, including inside the transaction closure it passes to withRetryTx
// (journalscan walks nested function literals), but not one moved out into a
// named helper of its own. The liveness anchors below are what keep a rename
// from turning the whole guard into a tautology.
func TestPostCommitRecheckPublishesOutsideItsTransaction(t *testing.T) {
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}

	const (
		inTx    = "doltAddAndCommitInTx"
		postTx  = "doltAddAndCommitPostTx"
		recheck = "DoltStore.recheckBlockedAfterCommit"
		// Anchors: one function known to publish with each ordering.
		knownInTxPublisher   = "DoltStore.demoteToWispInTx"
		knownPostTxPublisher = "DoltStore.runIssueOperationTxWithMessage"
	)
	publishes := func(key, helper string) bool {
		f := fns[key]
		if f == nil {
			t.Fatalf("%s is not in the parsed package — this guard is not measuring what it names", key)
		}
		return f.CallsAnyOf(map[string]bool{helper: true})
	}

	if !publishes(knownInTxPublisher, inTx) {
		t.Fatalf("%s no longer reads as an in-transaction publisher: the %s analysis is broken, so its verdict on %s means nothing", knownInTxPublisher, inTx, recheck)
	}
	if !publishes(knownPostTxPublisher, postTx) {
		t.Fatalf("%s no longer reads as a post-transaction publisher: the %s analysis is broken, so its verdict on %s means nothing", knownPostTxPublisher, postTx, recheck)
	}

	if publishes(recheck, inTx) {
		t.Fatalf("%s publishes with %s: DOLT_ADD stages the whole table from the transaction's BEGIN-time root, so a Dolt commit minted inside the recheck's own transaction writes every concurrently committed row back to its BEGIN-time value — and a recheck runs only when concurrent unblocking writes are racing. Publish with %s after the transaction commits instead.", recheck, inTx, postTx)
	}
	if !publishes(recheck, postTx) {
		t.Fatalf("%s no longer publishes with %s: a corrected is_blocked flag that reaches no Dolt commit sits dirty in the working set until an unrelated write sweeps it in", recheck, postTx)
	}
}
