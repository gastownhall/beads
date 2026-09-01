package conformance

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// This file holds the vocabulary every one of the three new versioned-history
// contract files shares (Address, ChangeAttribution, Restriction,
// RetainedWindow, the PerRecordCASWrite hook shape, and the sentinel errors),
// plus the contract for R16/R17 (gastownhall/beads#6133, be-hs42e.1 §5-§7):
// the whole-of-state expectedRevision CAS write and its typed Refusal.
//
// THE SHARED VOCABULARY LIVES HERE, NOT IN A FOURTH FILE. All three contract
// files declare `package conformance`, so Go same-package visibility already
// gives cross-file access with no import; the architecture doc names exactly
// three contract files and does not say where their shared types live. This
// file's own §4a choice is to keep them beside R16/R17 rather than split out
// a vocabulary-only file with no tests of its own — R16/R17 is the first
// contract that needs them, so a reader asking "what is an Address" starts
// here.
//
// EVERY HOOK ON EVERY FIXTURE IN ALL THREE NEW FILES IS INDEPENDENTLY
// NILABLE — unlike metadata_cas_contract.go, where MetadataCAS itself is
// required and only two hooks are optional. Phase 0 ships zero real
// implementations of any of these capabilities in any backend's fixture kit
// (architecture §12: "every new hook is nil in every backend's fixture
// kit"), so every case below nil-checks every hook it uses and SKIPS BY NAME
// when one is missing — the same idiom history_matching.go uses for
// CountHistoryMatching.
//
// THIS SUITE DEFINES INTERFACES AND TESTS ONLY. Nothing introduced by this
// phase implements real CAS, invariant, retention, or epoch enforcement. The
// positive-path body of every Run function below is a complete, realistic
// assertion of what Phase 3's real hooks must do — because this suite IS the
// spec Phase 3 implements against (NFR-03) — but none of it runs today,
// since versioned_history_wiring_test.go wires every hook nil.
//
// RunExpectedRevisionCoversFieldsOutsideAnyWatchedSubset (R16-c) IS EXPECTED
// TO FAIL the moment a real backend wires CompareAndSetVersion, unless that
// backend has independently closed the row_lock partial-coverage gap the
// architecture doc's own §10 risk table names. That is deliberate — see the
// function's own doc comment — not a bug to relax the assertion around.

// Address opaquely identifies one Version. It names no physical column — per
// be-hs42e.1 §5, this is conceptual vocabulary that any future backend's hook
// translates to and from whatever it actually stores.
type Address string

// ChangeAttribution is who/what/why/when produced a Version, as observed by
// the transaction that produced it (be-hs42e.1 §5). Actor, Agent, and
// Message are independently nullable.
type ChangeAttribution struct {
	Actor   *string
	Agent   *string
	Message *string
	At      time.Time
}

// Restriction is one of the five states a store may report for an Address
// (be-hs42e.1 §5, FR-05). Gone and Unknown are deliberately distinct values,
// never conflated: Gone means the store once held this Version and no longer
// does; Unknown means the store has no knowledge of the lineage at all
// (FR-06) — e.g. it never synced far enough back to have an opinion.
type Restriction int

const (
	RestrictionLive Restriction = iota
	RestrictionGoneRetention
	RestrictionGoneErasure
	RestrictionGoneReorganization
	RestrictionUnknown
)

func (r Restriction) String() string {
	switch r {
	case RestrictionLive:
		return "live"
	case RestrictionGoneRetention:
		return "gone-retention"
	case RestrictionGoneErasure:
		return "gone-erasure"
	case RestrictionGoneReorganization:
		return "gone-reorganization"
	case RestrictionUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("Restriction(%d)", int(r))
	}
}

// RetainedWindow is the surviving range a store reports once some Addresses
// within a lineage have gone (be-hs42e.1 §5, R20-c/h).
type RetainedWindow struct {
	LowerBound Address
	UpperBound Address
}

// PerRecordCASWrite is the shape every per-record whole-of-state CAS write
// takes. ExpectedRevisionFixture.CompareAndSetVersion and
// CrossRecordInvariantFixture.GuardedWrite share this EXACT type on purpose:
// R16.1-a's boundary claim is literally that the second is the first, called
// twice — sharing the type makes that claim structural, not just asserted.
type PerRecordCASWrite func(ctx context.Context, id string, expected *Address, patch map[string]any) (ExpectedRevisionResult, error)

// ExpectedRevisionResult is the outcome of a PerRecordCASWrite call. Accepted
// and Refusal are mutually exclusive: read Refusal only when Accepted is
// false, read NewVersion only when it is true.
type ExpectedRevisionResult struct {
	Accepted   bool
	NewVersion Address
	Refusal    *Refusal
}

// Refusal is R17's typed, machine-readable outcome for a write that named a
// version no longer current — never a generic error, never indistinguishable
// from an accepted write (R17-c/d1).
type Refusal struct {
	// RefusingVersion is the Address that was actually current when the
	// write was evaluated (R17-a).
	RefusingVersion Address
	// Attribution is RefusingVersion's ChangeAttribution, as observed by the
	// refusing transaction (R17-b).
	Attribution ChangeAttribution
}

// ErrRevisionNotFound means the write named an id with no current version at
// all — distinct from a Refusal, which requires a current version to refuse
// against (R17-d2).
var ErrRevisionNotFound = errors.New("expected-revision: id has no current version")

// ErrRevisionValidation means the write itself was malformed, independent of
// whether its expectation held (R17-d3).
var ErrRevisionValidation = errors.New("expected-revision: request is invalid")

// ExpectedRevisionFixture supplies the whole-of-state CAS capability under
// test (R16, R17): a write that names the Version it expects to be current,
// accepted only while that expectation holds, refused with a typed Refusal
// otherwise.
type ExpectedRevisionFixture struct {
	IssuePrefix string

	// CompareAndSetVersion performs a whole-of-state CAS write: it is
	// accepted only while expected (nil meaning "no version named", i.e. an
	// unconditional write, R16-b) names the currently-current Address. A nil
	// hook means this backend does not yet implement expectedRevision CAS,
	// and every case in this file SKIPS with that reason.
	CompareAndSetVersion PerRecordCASWrite

	// CurrentVersion reports the Address currently current for id. A nil
	// hook means this backend cannot independently confirm the current
	// Address, and cases that need one skip with that reason.
	CurrentVersion func(ctx context.Context, id string) (Address, error)

	// MutateOutsideExpectedRevision writes to id through a path R16-c
	// specifically targets: one NOT gated by the whole-of-state precondition
	// (the architecture doc names row_lock's documented partial-coverage gap
	// as the concrete probe — see §7 risk, §10 risk table). A nil hook means
	// this backend exposes no such out-of-band path, and the one case that
	// needs it skips with that reason.
	MutateOutsideExpectedRevision func(ctx context.Context, id string, field, value string) error
}

// RunExpectedRevisionAcceptsAWriteNamingTheCurrentVersion pins R16's
// baseline: a write naming the Address that is actually current must be
// accepted.
func RunExpectedRevisionAcceptsAWriteNamingTheCurrentVersion(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}
	if fixture.CurrentVersion == nil {
		t.Skip("this backend cannot independently confirm the current Address (CurrentVersion is nil)")
	}
	id, seeded := expectedRevisionSeed(t, ctx, fixture, "current", map[string]any{"phase": "start"})

	current, err := fixture.CurrentVersion(ctx, id)
	if err != nil {
		t.Fatalf("CurrentVersion(%s): %v", id, err)
	}
	if current != seeded.NewVersion {
		t.Fatalf("CurrentVersion(%s) = %s, want the seed's NewVersion %s", id, current, seeded.NewVersion)
	}

	result, err := fixture.CompareAndSetVersion(ctx, id, &current, map[string]any{"phase": "advanced"})
	if err != nil {
		t.Fatalf("CompareAndSetVersion(%s, expected=current): %v", id, err)
	}
	if !result.Accepted {
		t.Fatalf("CompareAndSetVersion naming the true current Address was refused (Refusal=%+v), want Accepted (R16: a write naming the current Version must be accepted)", result.Refusal)
	}
}

// RunExpectedRevisionAcceptsAWriteNamingNoVersion pins R16-b's paraphrase: a
// nil expectation means an unconditional write, not "the key must be
// absent" — the unconditional path must still exist over a record that
// already has a current Version.
func RunExpectedRevisionAcceptsAWriteNamingNoVersion(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}
	id, _ := expectedRevisionSeed(t, ctx, fixture, "unconditional", map[string]any{"phase": "start"})

	result, err := fixture.CompareAndSetVersion(ctx, id, nil, map[string]any{"phase": "overwritten"})
	if err != nil {
		t.Fatalf("CompareAndSetVersion(%s, expected=nil) over an existing record: %v", id, err)
	}
	if !result.Accepted {
		t.Fatal("CompareAndSetVersion(expected=nil) over an existing record was refused, want Accepted (R16-b: nil means an unconditional write, not \"must be absent\")")
	}
}

// RunExpectedRevisionCoversFieldsOutsideAnyWatchedSubset pins R16's
// whole-of-state promise against the concrete gap the architecture doc names
// (§7 risk, §10 risk table): row_lock's documented partial-coverage leaves
// some fields writable without reminting the Version those fields belong to.
// A write through MutateOutsideExpectedRevision must still count as a change
// against the expectation — otherwise a caller's CAS can silently observe a
// stale precondition as current.
//
// THIS CASE IS EXPECTED TO FAIL the moment a real backend wires
// CompareAndSetVersion, UNLESS that backend has independently closed the
// row_lock gap first (architecture §10 risk table: "this suite existing and
// failing loudly against that gap IS THE POINT"). It is not a bug in this
// test if it starts failing against a real hook — it is the suite doing its
// job. Do not weaken this assertion to make a real backend pass; close the
// gap instead.
func RunExpectedRevisionCoversFieldsOutsideAnyWatchedSubset(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}
	if fixture.MutateOutsideExpectedRevision == nil {
		t.Skip("this backend exposes no out-of-band mutation path to probe the whole-of-state boundary with (MutateOutsideExpectedRevision is nil)")
	}
	id, seeded := expectedRevisionSeed(t, ctx, fixture, "outofband", map[string]any{"phase": "start"})
	staleExpected := seeded.NewVersion

	if err := fixture.MutateOutsideExpectedRevision(ctx, id, "side_channel", "changed"); err != nil {
		t.Fatalf("MutateOutsideExpectedRevision(%s): %v", id, err)
	}

	result, err := fixture.CompareAndSetVersion(ctx, id, &staleExpected, map[string]any{"phase": "advanced"})
	if err != nil {
		t.Fatalf("CompareAndSetVersion(%s, expected=pre-mutation version): %v", id, err)
	}
	if result.Accepted {
		t.Fatal("CompareAndSetVersion naming the pre-mutation Address was accepted after an out-of-band field write; " +
			"R16's whole-of-state precondition must cover fields outside any single watched subset")
	}
}

// RunRefusalReportsTheRefusingVersionAddress pins R17-a: a Refusal names the
// Address that was actually current when the write was evaluated, read here
// independently via CurrentVersion rather than trusted from the caller's own
// stale value.
func RunRefusalReportsTheRefusingVersionAddress(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}
	if fixture.CurrentVersion == nil {
		t.Skip("this backend cannot independently confirm the current Address (CurrentVersion is nil)")
	}
	id, stale := expectedRevisionSeedStale(t, ctx, fixture, "refaddr")

	result, err := fixture.CompareAndSetVersion(ctx, id, &stale, map[string]any{"phase": "clobber"})
	if err != nil {
		t.Fatalf("CompareAndSetVersion(%s, stale expected): %v", id, err)
	}
	if result.Accepted || result.Refusal == nil {
		t.Fatalf("CompareAndSetVersion over a stale expectation = %+v, want a Refusal", result)
	}

	real, err := fixture.CurrentVersion(ctx, id)
	if err != nil {
		t.Fatalf("CurrentVersion(%s): %v", id, err)
	}
	if result.Refusal.RefusingVersion != real {
		t.Errorf("Refusal.RefusingVersion = %s, want the real current Address %s (R17-a)", result.Refusal.RefusingVersion, real)
	}
}

// RunRefusalReportsTheRefusingVersionsChangeAttribution pins R17-b: a
// Refusal carries the ChangeAttribution the refusing transaction observed
// for the version that beat the caller.
//
// §4a: PerRecordCASWrite's patch is a generic map[string]any with no
// dedicated attribution field, so this case threads a ChangeAttribution
// through the documented patch["_attribution"] convention
// (expectedRevisionAttributionPatch) — this file's own choice, since the
// architecture doc does not pin how a caller supplies attribution on a
// per-record write, only that the backend must observe and later report it.
func RunRefusalReportsTheRefusingVersionsChangeAttribution(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}
	actor := "cas-winner"
	wantAttribution := ChangeAttribution{Actor: &actor, At: time.Now().UTC()}

	id, seeded := expectedRevisionSeed(t, ctx, fixture, "refattr", map[string]any{"phase": "start"})
	staleAddr := seeded.NewVersion
	advanced, err := fixture.CompareAndSetVersion(ctx, id, &staleAddr,
		expectedRevisionAttributionPatch(map[string]any{"phase": "moved-on"}, wantAttribution))
	if err != nil {
		t.Fatalf("advancing %s with a known attribution: %v", id, err)
	}
	if !advanced.Accepted {
		t.Fatalf("advancing %s with a known attribution was refused", id)
	}

	result, err := fixture.CompareAndSetVersion(ctx, id, &staleAddr, map[string]any{"phase": "clobber"})
	if err != nil {
		t.Fatalf("CompareAndSetVersion(%s, stale expected): %v", id, err)
	}
	if result.Accepted || result.Refusal == nil {
		t.Fatalf("CompareAndSetVersion over a stale expectation = %+v, want a Refusal", result)
	}
	if !sameAttribution(result.Refusal.Attribution, wantAttribution) {
		t.Errorf("Refusal.Attribution = %+v, want %+v (R17-b: the attribution the refusing transaction observed)", result.Refusal.Attribution, wantAttribution)
	}
}

// RunRefusalIsATypedOutcomeNotAGenericError pins R17-c/d1: a stale
// expectation comes back as a populated Refusal with a nil error, never as a
// generic Go error.
func RunRefusalIsATypedOutcomeNotAGenericError(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}
	id, stale := expectedRevisionSeedStale(t, ctx, fixture, "typedoutcome")

	result, err := fixture.CompareAndSetVersion(ctx, id, &stale, map[string]any{"phase": "clobber"})
	if err != nil {
		t.Fatalf("a stale expectation returned error = %v, want nil: R17 reports a refusal, not an error (R17-c)", err)
	}
	if result.Accepted {
		t.Fatal("a stale expectation was accepted")
	}
	if result.Refusal == nil {
		t.Fatal("Refusal = nil on a non-accepted result, want a populated Refusal (R17-d1)")
	}
}

// RunRefusalIsDistinguishableFromAnAcceptedWrite runs an accepted and a
// refused case side by side and pins that Accepted is the only signal a
// caller needs: Refusal and NewVersion are never both populated, and never
// both empty.
func RunRefusalIsDistinguishableFromAnAcceptedWrite(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}

	for _, test := range []struct {
		name    string
		prepare func(t *testing.T) (string, Address)
	}{
		{"accepted", func(t *testing.T) (string, Address) {
			id, seeded := expectedRevisionSeed(t, ctx, fixture, "distinguish-accept", map[string]any{"phase": "start"})
			return id, seeded.NewVersion
		}},
		{"refused", func(t *testing.T) (string, Address) {
			return expectedRevisionSeedStale(t, ctx, fixture, "distinguish-refuse")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			id, expected := test.prepare(t)
			result, err := fixture.CompareAndSetVersion(ctx, id, &expected, map[string]any{"phase": "attempt"})
			if err != nil {
				t.Fatalf("CompareAndSetVersion(%s): %v", id, err)
			}
			if result.Accepted && result.Refusal != nil {
				t.Fatalf("result = %+v, want Refusal populated only when Accepted is false", result)
			}
			if !result.Accepted && result.Refusal == nil {
				t.Fatalf("result = %+v, want Refusal populated when Accepted is false", result)
			}
			if result.Accepted && result.NewVersion == "" {
				t.Error("Accepted = true but NewVersion is empty")
			}
		})
	}
}

// RunRefusalIsDistinguishableFromNotFound pins R17-d2: an id with no current
// version at all is ErrRevisionNotFound, never a Refusal — a Refusal
// requires a current version to refuse against.
func RunRefusalIsDistinguishableFromNotFound(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}
	ghost := Address("ghost-version")
	id := fixture.IssuePrefix + "-exprev-neverseeded"

	result, err := fixture.CompareAndSetVersion(ctx, id, &ghost, map[string]any{"phase": "attempt"})
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("CompareAndSetVersion on a never-seeded id error = %v, want ErrRevisionNotFound (R17-d2)", err)
	}
	if result.Accepted || result.Refusal != nil {
		t.Errorf("result = %+v, want neither Accepted nor a Refusal for a not-found id", result)
	}
}

// RunRefusalIsDistinguishableFromValidationFailure pins R17-d3: a malformed
// request is ErrRevisionValidation, independent of whether its expectation
// would have held.
func RunRefusalIsDistinguishableFromValidationFailure(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}
	result, err := fixture.CompareAndSetVersion(ctx, "", nil, map[string]any{"phase": "attempt"})
	if !errors.Is(err, ErrRevisionValidation) {
		t.Fatalf("CompareAndSetVersion with an empty id error = %v, want ErrRevisionValidation (R17-d3)", err)
	}
	if result.Accepted || result.Refusal != nil {
		t.Errorf("result = %+v, want neither Accepted nor a Refusal for a malformed request", result)
	}
}

// RunRefusalNeverSilentlyPicksAWinner pins the race shape underneath R17:
// two writes competing over the same stale expectation must produce exactly
// one Accepted and one real Refusal — never both accepted, never a merged or
// silently overwritten result — and the Address left current afterward must
// be the winner's, read independently.
func RunRefusalNeverSilentlyPicksAWinner(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture) {
	t.Helper()
	if fixture.CompareAndSetVersion == nil {
		t.Skip("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)")
	}
	if fixture.CurrentVersion == nil {
		t.Skip("this backend cannot independently confirm the current Address (CurrentVersion is nil)")
	}
	id, seeded := expectedRevisionSeed(t, ctx, fixture, "competing", map[string]any{"phase": "start"})
	shared := seeded.NewVersion

	first, err := fixture.CompareAndSetVersion(ctx, id, &shared, map[string]any{"phase": "writer-a"})
	if err != nil {
		t.Fatalf("writer A: CompareAndSetVersion(%s): %v", id, err)
	}
	second, err := fixture.CompareAndSetVersion(ctx, id, &shared, map[string]any{"phase": "writer-b"})
	if err != nil {
		t.Fatalf("writer B: CompareAndSetVersion(%s): %v", id, err)
	}

	if first.Accepted == second.Accepted {
		t.Fatalf("two competing writes against the same expectation both reported Accepted=%v, want exactly one to win", first.Accepted)
	}
	winner, loser := first, second
	if second.Accepted {
		winner, loser = second, first
	}
	if loser.Refusal == nil {
		t.Fatal("the losing write carries no Refusal")
	}

	real, err := fixture.CurrentVersion(ctx, id)
	if err != nil {
		t.Fatalf("CurrentVersion(%s): %v", id, err)
	}
	if real != winner.NewVersion {
		t.Errorf("CurrentVersion(%s) = %s after the race, want the winner's NewVersion %s: a third value means the race was decided somewhere other than the two calls made here", id, real, winner.NewVersion)
	}
}

// --- fixture helpers -------------------------------------------------------

// expectedRevisionSeed performs an unconditional (expected=nil) write to
// mint id's first Version, and fatals unless the fixture accepts it — every
// case in this file needs a live record to test a whole-of-state CAS
// against, and CompareAndSetVersion(expected=nil) is the only write path
// this fixture exposes to make one.
func expectedRevisionSeed(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture, tag string, patch map[string]any) (string, ExpectedRevisionResult) {
	t.Helper()
	id := fixture.IssuePrefix + "-exprev-" + tag
	if patch == nil {
		patch = map[string]any{}
	}
	result, err := fixture.CompareAndSetVersion(ctx, id, nil, patch)
	if err != nil {
		t.Fatalf("seeding %s: CompareAndSetVersion(expected=nil) error = %v", id, err)
	}
	if !result.Accepted {
		t.Fatalf("seeding %s: CompareAndSetVersion(expected=nil) Accepted = false, want true (R16-b: an unconditional write to a fresh id must be accepted)", id)
	}
	return id, result
}

// expectedRevisionSeedStale seeds id, advances it once more, and returns the
// Address from BEFORE that second write — a caller naming this Address as
// Expected has a stale expectation, because the second write has since
// moved the current Version on.
func expectedRevisionSeedStale(t *testing.T, ctx context.Context, fixture ExpectedRevisionFixture, tag string) (string, Address) {
	t.Helper()
	id, seeded := expectedRevisionSeed(t, ctx, fixture, tag, map[string]any{"phase": "start"})
	staleAddr := seeded.NewVersion
	advanced, err := fixture.CompareAndSetVersion(ctx, id, &staleAddr, map[string]any{"phase": "moved-on"})
	if err != nil {
		t.Fatalf("advancing %s past the address a later case will treat as stale: %v", id, err)
	}
	if !advanced.Accepted {
		t.Fatalf("advancing %s past the address a later case will treat as stale was refused", id)
	}
	return id, staleAddr
}

// expectedRevisionAttributionPatch adds attribution to patch under the
// "_attribution" key — the convention this file uses to thread a
// ChangeAttribution through PerRecordCASWrite's generic patch parameter. See
// RunRefusalReportsTheRefusingVersionsChangeAttribution's doc comment for
// why this convention exists.
func expectedRevisionAttributionPatch(patch map[string]any, attribution ChangeAttribution) map[string]any {
	if patch == nil {
		patch = map[string]any{}
	}
	patch["_attribution"] = attribution
	return patch
}

// sameAttribution compares two ChangeAttribution values by content. It does
// not use reflect.DeepEqual: comparing time.Time by its internal
// representation is fragile (monotonic readings can differ for an otherwise
// equal instant), so At is compared with time.Time.Equal and the nullable
// string fields are compared by dereferenced value.
func sameAttribution(a, b ChangeAttribution) bool {
	return samePtrString(a.Actor, b.Actor) &&
		samePtrString(a.Agent, b.Agent) &&
		samePtrString(a.Message, b.Message) &&
		a.At.Equal(b.At)
}

func samePtrString(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
