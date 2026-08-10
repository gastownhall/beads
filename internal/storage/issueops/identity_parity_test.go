package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/validation"
)

// TestIdentityCanonicalizationParityWithValidation guards the two
// intentionally-duplicated copies of the identity-canonicalization rule
// (this package's canonicalActor/actorMatches, internal/validation's
// exported CanonicalActor/ActorMatches) against silent drift. issueops
// cannot import internal/storage/domain — internal/storage/issueops already
// imports internal/storage/domain in several files (config_helpers.go,
// dependencies.go, etc.), so the reverse import would cycle — but it CAN
// safely import internal/validation (validation depends only on
// internal/types), and does so here for test purposes only. Per the ga-3ipxu
// gate review on #5439: "I won't hold the PR for extraction to a shared leaf
// package, but two keep-in-sync copies need at least a cross-package parity
// test so the tables can't drift silently." Run the identical case tables
// TestCanonicalActor/TestActorMatches already pin in
// internal/validation/issue_test.go through BOTH implementations; a future
// edit to either separator set or matching rule that isn't mirrored in the
// other fails here, not in production.
func TestIdentityCanonicalizationParityWithValidation(t *testing.T) {
	canonicalCases := []struct {
		name string
		in   string
	}{
		{name: "empty stays empty", in: ""},
		{name: "no separators is unchanged", in: "alice"},
		{name: "dot separator canonicalizes", in: "gastown.mayor"},
		{name: "double-underscore separator canonicalizes to the same form", in: "gastown__mayor"},
		{name: "hyphen separator canonicalizes to the same form", in: "gastown-mayor"},
		{name: "single underscore is already canonical", in: "gastown_mayor"},
		{name: "mixed separators each collapse to one canonical separator", in: "gastown.dog-3"},
		{name: "leading separator is preserved as a separator, not dropped", in: ".mayor"},
		{name: "trailing separator is preserved as a separator, not dropped", in: "mayor."},
	}
	for _, tc := range canonicalCases {
		t.Run("CanonicalActor/"+tc.name, func(t *testing.T) {
			local := canonicalActor(tc.in)
			shared := validation.CanonicalActor(tc.in)
			if local != shared {
				t.Errorf("issueops.canonicalActor(%q) = %q, internal/validation.CanonicalActor(%q) = %q — the two copies have drifted apart",
					tc.in, local, tc.in, shared)
			}
		})
	}

	matchCases := []struct {
		name            string
		assignee, actor string
	}{
		{name: "byte-identical matches", assignee: "alice", actor: "alice"},
		{name: "different spellings of the same identity match (ga-wzl83)", assignee: "gastown.mayor", actor: "gastown__mayor"},
		{name: "genuinely different identities do not match", assignee: "gastown.mayor", actor: "gastown.dog-3"},
		{name: "both empty match", assignee: "", actor: ""},
		{name: "empty assignee never matches a non-empty actor", assignee: "", actor: "mayor"},
	}
	for _, tc := range matchCases {
		t.Run("ActorMatches/"+tc.name, func(t *testing.T) {
			local := actorMatches(tc.assignee, tc.actor)
			shared := validation.ActorMatches(tc.assignee, tc.actor)
			if local != shared {
				t.Errorf("issueops.actorMatches(%q, %q) = %v, internal/validation.ActorMatches(%q, %q) = %v — the two copies have drifted apart",
					tc.assignee, tc.actor, local, tc.assignee, tc.actor, shared)
			}
		})
	}
}
