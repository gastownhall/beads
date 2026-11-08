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
// other fails here, not in production. The bd-740y4x rows below add the
// slash-qualified report pair and its negative-control spellings to those
// tables; TestAssigneeMatches/TestCheckClosable are the expected-value
// oracle for them (parity alone is not).
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
		{name: "exact double-hyphen separator decodes to the rig-qualified slash (ga-2vy9p2)", in: "gastown--mayor"},
		{name: "raw slash passes through unchanged", in: "gastown/mayor"},
		{name: "a hyphen run longer than two is not the exact slash spelling and falls back to generic collapse", in: "gastown---mayor"},
		{name: "report pair: raw slash spelling passes through (bd-740y4x)", in: "gascity/bob"},
		{name: "report pair: session-name spelling decodes to the rig-qualified slash (bd-740y4x)", in: "gascity--bob"},
		{name: "negative control: a different agent in the same rig (bd-740y4x)", in: "gascity--alice"},
		{name: "negative control: the same agent name in a different rig (bd-740y4x)", in: "python419--bob"},
		{name: "negative control: dotted-axis double-underscore spelling stays off the slash axis (bd-740y4x)", in: "gascity__bob"},
		{name: "negative control: dotted-axis dot spelling stays off the slash axis (bd-740y4x)", in: "gascity.bob"},
		{name: "negative control: dotted-axis single-underscore spelling stays off the slash axis (bd-740y4x)", in: "gascity_bob"},
		{name: "negative control: dotted-axis hyphen spelling stays off the slash axis (bd-740y4x)", in: "gascity-bob"},
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
		{name: "double-hyphen and double-underscore spellings no longer widen to the same identity (ga-2vy9p2)", assignee: "gastown--mayor", actor: "gastown__mayor"},
		{name: "double-hyphen spelling matches its raw-slash identity (ga-2vy9p2)", assignee: "gastown--mayor", actor: "gastown/mayor"},
		{name: "raw slash and raw dot stay genuinely distinct identities", assignee: "gastown/mayor", actor: "gastown.mayor"},
		{name: "report pair: session-name spelling matches its raw-slash identity (bd-740y4x)", assignee: "gascity/bob", actor: "gascity--bob"},
		{name: "report pair: raw-slash identity matches its session-name spelling, exchanged (bd-740y4x)", assignee: "gascity--bob", actor: "gascity/bob"},
		{name: "report pair negative: a different agent in the same rig does not match (bd-740y4x)", assignee: "gascity/bob", actor: "gascity--alice"},
		{name: "report pair negative: the same agent name in a different rig does not match (bd-740y4x)", assignee: "gascity/bob", actor: "python419--bob"},
		{name: "report pair negative: dotted-axis double-underscore spelling does not match the slash spelling (bd-740y4x)", assignee: "gascity/bob", actor: "gascity__bob"},
		{name: "report pair negative: dotted-axis dot spelling does not match the slash spelling (bd-740y4x)", assignee: "gascity/bob", actor: "gascity.bob"},
		{name: "report pair negative: dotted-axis single-underscore spelling does not match the slash spelling (bd-740y4x)", assignee: "gascity/bob", actor: "gascity_bob"},
		{name: "report pair negative: dotted-axis hyphen spelling does not match the slash spelling (bd-740y4x)", assignee: "gascity/bob", actor: "gascity-bob"},
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
