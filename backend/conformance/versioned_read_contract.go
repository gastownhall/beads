package conformance

import (
	"context"
	"testing"
	"time"
)

// This file holds the contract for R7.1 (gastownhall/beads#6136, design doc
// gastownhall/beads#5898 revision 9): the as-of read — the "versioned
// reads" half of Phase 3's title, authored here from scratch, since no
// fixture for it exists anywhere in this package to un-skip.
//
// R7.1, VERBATIM (design doc #5898 rev 9): "An as-of read is a named read
// whose selector is an instant. Given an instant T or a version address, a
// store returns one Bead's durable state as of that point — the latest
// version it holds that was accepted at or before T, with everything later
// excluded — under R7's record shape, marking the address it served, or
// refuses as gone or unknown with the reason (R8, R20). Like every history
// answer it is complete for the answering store's holdings and may change
// after a sync (TC3)."
//
// ADDRESS, RESTRICTION, AND CHANGEATTRIBUTION ARE DECLARED IN
// expected_revision_contract.go — see that file's package-level note on
// where this suite's shared vocabulary lives and why. This file reuses
// Restriction's five values as-is for the gone-or-unknown refusal outcome
// (R8): an as-of read never invents a sixth state, and never distinguishes
// "gone" from "unknown" any differently than R20 already does.
//
// EVERY HOOK ON THE FIXTURE BELOW IS INDEPENDENTLY NILABLE — see the
// package-level note in expected_revision_contract.go. Every case
// nil-checks every hook it uses and SKIPS BY NAME when one is missing. No
// backend implements as-of reads yet — be-x5jqd.5 is the implementation
// child this suite is written ahead of.
//
// EXPLICIT NON-GOAL: MEMORY-PLANE CONSUMER POLICY IS NOT PART OF THIS
// PRIMITIVE. R7.1's own text is explicit that a closed-before-T boundary,
// sibling exclusion, and supersession exclusion "are Memory-plane policies
// a consumer layers on this primitive (#5877), not part of it." This suite
// does not implement or assert any of the three — a future reader who finds
// them absent should read that as this file honoring R7.1's own scope, not
// as an oversight. This is the read-side analog of the R16/R16.1 boundary
// risk already recorded in expected_revision_contract.go: do not conflate
// the primitive with policy layered on top of it.
//
// THIS SUITE ASSERTS LOCAL COMPLETENESS ONLY (TC3). Nothing below requires,
// or even checks, that two stores asked about the same lineage agree — R7.1
// itself says an as-of answer "may change after a sync," so two stores at
// different sync points are expected to answer differently, and neither is
// wrong. RunAsOfReadIsCompleteOnlyForTheAnsweringStoresHoldings is the one
// case that touches more than one store, and what it asserts is exactly
// this: each store answers strictly from its own holdings, never a
// sibling's — not that the two agree.

// AsOfSelector names the point R7.1 resolves against: either a wall-clock
// instant, or a specific version Address treated as the selector itself —
// "the read that names its own answer" (R7.1: "an instant T or a version
// address"). Exactly one of At/Version is populated in every case this file
// exercises; R7.1 does not speak to the shape where both or neither are
// set, so this suite does not either.
type AsOfSelector struct {
	At      *time.Time
	Version *Address
}

// AsOfReadRefusal is R8's typed outcome for an as-of read that cannot be
// answered. Restriction is always one of the four Gone*/Unknown values,
// never RestrictionLive — a live answer is a served AsOfReadResult, not a
// refusal. Reason explains why (R8).
type AsOfReadRefusal struct {
	Restriction Restriction
	Reason      string
}

// AsOfReadResult is the outcome of an as-of read (R7.1). Address/State and
// Refusal are mutually exclusive: read Address/State only when Refusal is
// nil.
type AsOfReadResult struct {
	// Address is the version this read served, marking "the address it
	// served" (R7.1). Populated only when Refusal is nil.
	Address Address
	// State is the served version's durable state under R7's record shape.
	// Opaque to this suite: the fixture adapter translates a backend's real
	// record shape into this map, and no case here asserts its field-level
	// content — only that a served result carries an Address.
	State map[string]any
	// Refusal is populated instead of Address/State when the read cannot be
	// answered (R8). Nil means the read was served.
	Refusal *AsOfReadRefusal
}

// AsOfRead resolves storeID's answer to an as-of read against id's lineage,
// at selector (R7.1).
type AsOfRead func(ctx context.Context, storeID, id string, selector AsOfSelector) (AsOfReadResult, error)

// AsOfReadFixture supplies the capability under test for R7.1: a read whose
// selector is an instant or a version address, resolving to the latest
// version accepted at or before that point, or a typed refusal.
type AsOfReadFixture struct {
	IssuePrefix string

	// Resolve performs the as-of read under test (R7.1). A nil hook means
	// this backend does not implement as-of reads yet, and every case in
	// this file skips with that reason.
	Resolve AsOfRead

	// MintAt mints a fresh version for id under storeID, recorded as
	// accepted at the given instant, and returns its Address. Cases use
	// this to build a lineage with known accept-times to resolve an as-of
	// read against. A nil hook means this backend has no way to mint a
	// version at a controlled accept-time, and cases that need one skip
	// with that reason.
	MintAt func(ctx context.Context, storeID, id string, acceptedAt time.Time) (Address, error)

	// MakeUnanswerable arranges for id to be unanswerable at storeID for
	// the given Restriction (one of RestrictionGoneRetention/GoneErasure/
	// GoneReorganization/Unknown) — independently of
	// RetentionFixture/EpochFixture's own removal machinery, since R7.1's
	// refusal is a sibling contract to R17/R20, not a dependent of them. A
	// nil hook means this backend has no way to arrange this, and the case
	// that needs it skips with that reason.
	MakeUnanswerable func(ctx context.Context, storeID, id string, restriction Restriction) error
}

// RunAsOfReadSelectorAcceptsEitherAnInstantOrAVersionAddress pins R7.1's
// selector shape: an instant T and the version address that instant
// resolves to are two names for the same answer, and a store must accept
// either.
func RunAsOfReadSelectorAcceptsEitherAnInstantOrAVersionAddress(t *testing.T, ctx context.Context, fixture AsOfReadFixture) {
	t.Helper()
	if fixture.Resolve == nil {
		t.Skip("this backend does not implement as-of reads (Resolve is nil)")
	}
	if fixture.MintAt == nil {
		t.Skip("this backend has no way to mint a version at a controlled accept-time (MintAt is nil)")
	}
	store := asOfReadStore(fixture, "selector-kinds")
	id := fixture.IssuePrefix + "-asof-selector"
	t0 := time.Now().UTC().Add(-time.Hour)
	asOfReadMint(t, ctx, fixture, store, id, t0)
	t1 := t0.Add(time.Minute)
	second := asOfReadMint(t, ctx, fixture, store, id, t1)

	byInstant, err := fixture.Resolve(ctx, store, id, AsOfSelector{At: &t1})
	if err != nil {
		t.Fatalf("Resolve(selector=instant): %v", err)
	}
	if byInstant.Refusal != nil {
		t.Fatalf("Resolve(selector=instant) = %+v, want a served result", byInstant)
	}

	byVersion, err := fixture.Resolve(ctx, store, id, AsOfSelector{Version: &second})
	if err != nil {
		t.Fatalf("Resolve(selector=version address): %v", err)
	}
	if byVersion.Refusal != nil {
		t.Fatalf("Resolve(selector=version address) = %+v, want a served result", byVersion)
	}

	if byInstant.Address != second {
		t.Errorf("Resolve(selector=instant matching a version's own accept-time).Address = %s, want %s", byInstant.Address, second)
	}
	if byVersion.Address != second {
		t.Errorf("Resolve(selector=version address).Address = %s, want the same address named %s", byVersion.Address, second)
	}
	if byInstant.Address != byVersion.Address {
		t.Errorf("an instant selector (served %s) and a version-address selector naming the same version (served %s) resolved to different addresses; R7.1 accepts either as a name for the same answer", byInstant.Address, byVersion.Address)
	}
}

// RunAsOfReadReturnsTheLatestVersionAtOrBeforeTStrictlyExcludingLater pins
// R7.1's ordering promise: resolving at T returns the latest version
// accepted at or before T, and a version accepted after T is excluded even
// when its own instant is close by.
func RunAsOfReadReturnsTheLatestVersionAtOrBeforeTStrictlyExcludingLater(t *testing.T, ctx context.Context, fixture AsOfReadFixture) {
	t.Helper()
	if fixture.Resolve == nil {
		t.Skip("this backend does not implement as-of reads (Resolve is nil)")
	}
	if fixture.MintAt == nil {
		t.Skip("this backend has no way to mint a version at a controlled accept-time (MintAt is nil)")
	}
	store := asOfReadStore(fixture, "ordering")
	id := fixture.IssuePrefix + "-asof-ordering"
	t0 := time.Now().UTC().Add(-time.Hour)
	asOfReadMint(t, ctx, fixture, store, id, t0)
	t1 := t0.Add(time.Minute)
	middle := asOfReadMint(t, ctx, fixture, store, id, t1)
	t2 := t0.Add(2 * time.Minute)
	asOfReadMint(t, ctx, fixture, store, id, t2)

	atExactly, err := fixture.Resolve(ctx, store, id, AsOfSelector{At: &t1})
	if err != nil {
		t.Fatalf("Resolve(T=middle's own instant): %v", err)
	}
	if atExactly.Refusal != nil || atExactly.Address != middle {
		t.Errorf("Resolve(T=middle's own instant) = %+v, want the version accepted exactly at T (%s), not a later one", atExactly, middle)
	}

	between := t1.Add(30 * time.Second)
	betweenResult, err := fixture.Resolve(ctx, store, id, AsOfSelector{At: &between})
	if err != nil {
		t.Fatalf("Resolve(T strictly between two versions): %v", err)
	}
	if betweenResult.Refusal != nil || betweenResult.Address != middle {
		t.Errorf("Resolve(T strictly between two versions) = %+v, want the latest version at-or-before T (%s); the version accepted after T must be strictly excluded", betweenResult, middle)
	}
}

// RunAsOfReadReportsTheAddressItServed pins R7.1's "marking the address it
// served": a served result names the exact Address of the version it
// returned, not merely a truthy success.
func RunAsOfReadReportsTheAddressItServed(t *testing.T, ctx context.Context, fixture AsOfReadFixture) {
	t.Helper()
	if fixture.Resolve == nil {
		t.Skip("this backend does not implement as-of reads (Resolve is nil)")
	}
	if fixture.MintAt == nil {
		t.Skip("this backend has no way to mint a version at a controlled accept-time (MintAt is nil)")
	}
	store := asOfReadStore(fixture, "reports-address")
	id := fixture.IssuePrefix + "-asof-reports-address"
	at := time.Now().UTC()
	address := asOfReadMint(t, ctx, fixture, store, id, at)

	result, err := fixture.Resolve(ctx, store, id, AsOfSelector{At: &at})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Refusal != nil {
		t.Fatalf("Resolve = %+v, want a served result", result)
	}
	if result.Address == "" {
		t.Fatal("Resolve returned an empty Address on a served result, want the address of the version it served (R7.1)")
	}
	if result.Address != address {
		t.Errorf("Resolve(...).Address = %s, want the served version's own address %s", result.Address, address)
	}
}

// RunAsOfReadRefusesAsGoneOrUnknownNeverSubstitutingAnotherVersion pins R8:
// a read that cannot be answered refuses as a typed Gone or Unknown outcome
// with a reason, and never silently returns a neighboring version, the
// nearest surviving version, or current state as a substitute — R8's own
// text names all three as non-answers.
func RunAsOfReadRefusesAsGoneOrUnknownNeverSubstitutingAnotherVersion(t *testing.T, ctx context.Context, fixture AsOfReadFixture) {
	t.Helper()
	if fixture.Resolve == nil {
		t.Skip("this backend does not implement as-of reads (Resolve is nil)")
	}
	if fixture.MintAt == nil {
		t.Skip("this backend has no way to mint a version at a controlled accept-time (MintAt is nil)")
	}
	if fixture.MakeUnanswerable == nil {
		t.Skip("this backend has no way to make a lineage unanswerable (MakeUnanswerable is nil)")
	}

	for _, restriction := range []Restriction{
		RestrictionGoneRetention,
		RestrictionGoneErasure,
		RestrictionGoneReorganization,
		RestrictionUnknown,
	} {
		t.Run(restriction.String(), func(t *testing.T) {
			store := asOfReadStore(fixture, "unanswerable-"+restriction.String())
			id := fixture.IssuePrefix + "-asof-unanswerable-" + restriction.String()
			at := time.Now().UTC()
			address := asOfReadMint(t, ctx, fixture, store, id, at)

			if err := fixture.MakeUnanswerable(ctx, store, id, restriction); err != nil {
				t.Fatalf("MakeUnanswerable(%s, %s): %v", id, restriction, err)
			}

			result, err := fixture.Resolve(ctx, store, id, AsOfSelector{At: &at})
			if err != nil {
				t.Fatalf("Resolve after MakeUnanswerable(%s): %v", restriction, err)
			}
			if result.Refusal == nil {
				t.Fatalf("Resolve after MakeUnanswerable(%s) = %+v, want a Refusal, not a served result (R8)", restriction, result)
			}
			if result.Refusal.Restriction != restriction {
				t.Errorf("Refusal.Restriction = %s, want %s", result.Refusal.Restriction, restriction)
			}
			if result.Refusal.Reason == "" {
				t.Error("Refusal.Reason is empty, want a reason (R8)")
			}
			if result.Address != "" {
				t.Errorf("Resolve on a refusal returned Address = %s, want empty: R8 forbids silently substituting a neighboring version, the nearest surviving version, or current state (the pre-refusal address was %s)", result.Address, address)
			}
		})
	}
}

// RunAsOfReadIsCompleteOnlyForTheAnsweringStoresHoldings pins R7.1's closing
// clause and TC3: an as-of answer "is complete for the answering store's
// holdings and may change after a sync." This case asserts NOTHING about
// agreement between two stores asked about the same lineage — proving that
// absence is the point, not a gap. It mints a lineage into exactly one of
// two stores and confirms each store answers strictly from its own
// holdings: the store that holds the lineage serves it, and the store that
// never received it answers Unknown (FR-06, reused from R20) rather than
// reaching across to a sibling store's knowledge, erroring, or answering
// Gone — which would claim knowledge of a lineage it never held.
func RunAsOfReadIsCompleteOnlyForTheAnsweringStoresHoldings(t *testing.T, ctx context.Context, fixture AsOfReadFixture) {
	t.Helper()
	if fixture.Resolve == nil {
		t.Skip("this backend does not implement as-of reads (Resolve is nil)")
	}
	if fixture.MintAt == nil {
		t.Skip("this backend has no way to mint a version at a controlled accept-time (MintAt is nil)")
	}
	holds := asOfReadStore(fixture, "tc3-holds-the-lineage")
	neverSynced := asOfReadStore(fixture, "tc3-never-synced")
	id := fixture.IssuePrefix + "-asof-tc3"
	at := time.Now().UTC()
	address := asOfReadMint(t, ctx, fixture, holds, id, at)

	local, err := fixture.Resolve(ctx, holds, id, AsOfSelector{At: &at})
	if err != nil {
		t.Fatalf("Resolve on the store that holds the lineage: %v", err)
	}
	if local.Refusal != nil || local.Address != address {
		t.Fatalf("Resolve on the store that holds the lineage = %+v, want it served from that store's own holdings (%s)", local, address)
	}

	remote, err := fixture.Resolve(ctx, neverSynced, id, AsOfSelector{At: &at})
	if err != nil {
		t.Fatalf("Resolve on a store that never received this lineage: %v", err)
	}
	if remote.Refusal == nil {
		t.Fatalf("Resolve on a store that never received this lineage = %+v, want a Refusal: an as-of answer is complete for the answering store's own holdings, not a sibling's (TC3)", remote)
	}
	if remote.Refusal.Restriction != RestrictionUnknown {
		t.Errorf("Refusal.Restriction on the never-synced store = %s, want RestrictionUnknown: no lineage knowledge is Unknown, not Gone (FR-06)", remote.Refusal.Restriction)
	}
}

// --- fixture helpers -------------------------------------------------------

// asOfReadStore names a storeID namespaced by the fixture's IssuePrefix and
// tag, so cases that need more than one store (e.g. the TC3 local-
// completeness case) get independent ones.
func asOfReadStore(fixture AsOfReadFixture, tag string) string {
	return fixture.IssuePrefix + "-asof-store-" + tag
}

// asOfReadMint mints a version for id on store at the given instant via
// fixture.MintAt, fataling the case if minting fails.
func asOfReadMint(t *testing.T, ctx context.Context, fixture AsOfReadFixture, store, id string, at time.Time) Address {
	t.Helper()
	address, err := fixture.MintAt(ctx, store, id, at)
	if err != nil {
		t.Fatalf("MintAt(%s, %s, %s): %v", store, id, at, err)
	}
	return address
}
