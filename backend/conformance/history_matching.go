package conformance

import (
	"strings"
	"testing"
)

// This file holds the shared half of the message-scoped history hook that three
// role contracts observe through. The hook itself is declared on each fixture
// that needs it, named and typed identically so one per-backend closure fills
// all three:
//
//	CountHistoryMatching func(ctx context.Context, pattern string) (int, error)
//
// It counts the entries in the backend's version log whose message matches
// pattern, where "" means every entry and anything else is a SQL LIKE pattern
// the backend applies with its own escaping. It exists because three cases
// assert on what an entry READS — "an entry naming this wisp", "an entry
// reading the caller's provenance label" — which plain CountHistory cannot
// express, and which those cases used to get by sending
// `SELECT COUNT(*) FROM dolt_log ...` through QueryScalar. That was the only
// Dolt-engine dependency in the role tier: a backend with no dolt_log table
// FAILED those three cases instead of skipping them.
//
// CountHistoryMatching does not supersede CountHistory. The two declare
// different capabilities — a backend can know how long its history is without
// being able to match on the text of an entry — and 25 cases run on the narrow
// hook alone. Where a backend has both, its fixture kit defines CountHistory as
// CountHistoryMatching(ctx, "") so the two cannot disagree.
//
// A nil CountHistoryMatching means "this backend cannot observe history by
// message", and every case that needs one SKIPS LOUDLY with that reason rather
// than passing quietly, exactly as a nil CountHistory does.

// historyPatternForExactMessage returns the LIKE pattern that matches message
// and nothing else.
//
// A LIKE pattern carrying no wildcards is anchored at both ends, so the cases
// that count entries by their EXACT message keep counting exactly those — but
// only while the message itself carries no % or _, which LIKE would read as a
// wildcard and quietly widen the count with. A message needing an escape fails
// here, loudly, rather than asserting on more entries than the case means.
func historyPatternForExactMessage(t *testing.T, message string) string {
	t.Helper()
	if message == "" {
		t.Fatal("an exact-message history count needs a message: the empty pattern counts every entry")
	}
	if strings.ContainsAny(message, `%_\`) {
		t.Fatalf("history message %q carries a LIKE metacharacter: CountHistoryMatching takes a pattern, so this count would reach entries the case does not mean", message)
	}
	return message
}
