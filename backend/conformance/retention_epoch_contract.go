package conformance

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// This file holds the contract for R20 (gastownhall/beads#6133, be-hs42e.1
// §5-§9): what a store reports about an Address once retention, erasure, or
// reorganization has stopped it resolving to live state (R20-a..R20-l), and
// the store-epoch generation counter that selectively voids addresses minted
// under an earlier one (R20-m/n).
//
// RESTRICTION AND RETAINEDWINDOW ARE DECLARED IN expected_revision_contract.go
// — see that file's package-level note on where this suite's shared
// vocabulary lives and why.
//
// GONE AND UNKNOWN ARE NEVER CONFLATED (FR-05, FR-06): Gone means the store
// once held this Version and no longer does; Unknown means the store has no
// lineage knowledge of the Address at all. Every RetentionAnswer names its
// PRODUCING STORE (R20-i) so a case can catch a backend that silently
// answers for the wrong store — this matters because R20 is explicitly a
// per-store promise, not a global one: a Reorganization can leave one store
// still serving an Address that another has moved past.
//
// THIS FILE ORIGINALLY MADE THREE §4a CHOICES THE ARCHITECTURE DOC DOES NOT
// PIN. Two have since DIED — retired by fixes made within this same PR,
// before merge, in response to review. Kept here, marked DIED, so a reader
// tracing why RetentionAnswer/RetentionFixture look the way they do does not
// have to reconstruct the history from git log or PR comments:
//
//  1. Remove/Commit's refusal under an active Hold is a typed error,
//     ErrRetentionHeld, rather than a non-accepted-shaped RetentionAnswer —
//     the doc names the promise ("must refuse ... unless forced", R20-h) but
//     not its shape. An error was chosen because Commit's success path
//     already returns a RetentionAnswer describing a Version that is now
//     GONE; reusing that same shape for a refusal would make "the hold
//     worked" and "the removal happened" look like the same kind of value.
//     (R17's Refusal reasons about the same tradeoff the other way, because
//     R17 already had a typed non-error outcome shape to reuse — Remove has
//     no such sibling shape to reuse here.) STILL STANDS.
//  2. DIED. ForceRemove's attribution used to be asserted only for shape,
//     not content, because RetentionAnswer carried no attribution field.
//     RetentionAnswer.Attribution now carries it, and
//     RunForcingAHeldRemovalRecordsWhoWhenWhy asserts real content — see
//     that function's own doc comment.
//  3. DIED. An address that no operation had touched used to resolve
//     RestrictionLive BY DEFAULT, purely by naming convention — a fabricated
//     Address, from the retired retentionAddress helper, that no store had
//     ever actually minted. Every case that needs a real, live Address now
//     mints one for real via RetentionFixture.Mint (see retentionMint).
//     RestrictionUnknown for a foreign store (FR-06) is UNCHANGED by this:
//     RunAStoreWithNoLineageKnowledgeAnswersUnknownNotGone and
//     RunGoneIsDistinguishableFromUnknown still test the Unknown case via a
//     second, foreign storeID that genuinely never saw the Address — not via
//     an untouched-but-fabricated address on the primary one, which is the
//     part that died. retentionAddress itself survives as the one helper
//     those two cases still use, precisely because Unknown requires an
//     Address no store ever minted.
//
// EVERY HOOK ON BOTH FIXTURES BELOW IS INDEPENDENTLY NILABLE — see the
// package-level note in expected_revision_contract.go. Every case
// nil-checks every hook it uses and SKIPS BY NAME when one is missing.

// RetentionAnswer is what a store reports when asked about an Address.
type RetentionAnswer struct {
	Restriction Restriction
	// RetainedWindow is populated once some Addresses within the lineage
	// have gone, naming the surviving range (R20-c) — AND, independently,
	// whenever a Hold is protecting an Address from removal, naming that
	// Address within the range it protects (R20-h). Nil means neither
	// applies (e.g. Restriction is Live with no Hold in effect, or
	// Unknown).
	RetainedWindow *RetainedWindow
	// ProducingStore names the store that produced this answer (R20-i).
	ProducingStore string
	// Attribution records who, when, and why produced this answer, for the
	// operations that force their way past an ordinary refusal — currently
	// just ForceRemove (R20-h2). Nil means this answer did not come from an
	// attributed forcing operation (e.g. an ordinary Remove/Commit, or a
	// Live/Unknown answer never touched by one).
	Attribution *ChangeAttribution
	// Epoch names the store-epoch generation this answer was evaluated
	// under, so a caller can tell WHICH epoch a GoneReorganization answer
	// belongs to, not just that reorganization is why the Address is gone
	// (R20-n). Nil means this answer did not involve epoch reasoning (e.g.
	// an ordinary Remove/Commit under R20-a..R20-l, evaluated by a store
	// with no epoch concept at all).
	Epoch *int
}

// RemovalReason is why an Address was removed: R20 requires the three
// reasons stay pairwise distinguishable in the Restriction they produce.
type RemovalReason int

const (
	RemovalReasonRetention RemovalReason = iota
	RemovalReasonErasure
	RemovalReasonReorganization
)

func (r RemovalReason) String() string {
	switch r {
	case RemovalReasonRetention:
		return "retention"
	case RemovalReasonErasure:
		return "erasure"
	case RemovalReasonReorganization:
		return "reorganization"
	default:
		return fmt.Sprintf("RemovalReason(%d)", int(r))
	}
}

// RemovalPreview is Remove's enumerate-before-you-change half (R20-g): the
// Addresses about to be affected are reported before Commit makes the
// change real.
type RemovalPreview struct {
	AffectedAddresses []Address
	Commit            func(ctx context.Context) (RetentionAnswer, error)
}

// ErrRetentionHeld means a RemovalPreview's Commit was refused because an
// active Hold covers the Address (R20-h). See this file's package doc
// comment, §4a choice 1, for why this is a typed error rather than a
// non-accepted-shaped RetentionAnswer.
var ErrRetentionHeld = errors.New("retention: address is held")

// RetentionResolve answers what a store currently reports for address. It is
// shared verbatim between RetentionFixture and EpochFixture: an epoch bump's
// effect on an Address (R20-n) is observed through the exact same read path
// as an ordinary removal's effect (R20-a..R20-l).
type RetentionResolve func(ctx context.Context, storeID string, address Address) (RetentionAnswer, error)

// RetentionFixture supplies the capabilities under test for R20's removal,
// hold, forced-removal, and erasure semantics.
type RetentionFixture struct {
	IssuePrefix string

	// Resolve reports storeID's current answer for address. A nil hook
	// means this backend does not yet report retention state, and cases
	// that need one skip with that reason.
	Resolve RetentionResolve

	// Remove reports the Addresses a removal is about to affect
	// (RemovalPreview.AffectedAddresses) before Commit makes the change
	// real (R20-g). A nil hook means this backend has no removal primitive,
	// and cases that need one skip with that reason.
	Remove func(ctx context.Context, storeID string, address Address, reason RemovalReason) (RemovalPreview, error)

	// Hold prevents Remove/Commit from taking effect against address until
	// released (R20-h). A nil hook means this backend has no hold
	// primitive, and cases that need one skip with that reason.
	Hold func(ctx context.Context, storeID string, address Address) error

	// ForceRemove removes address despite an active Hold, recording who,
	// when, and why in the resulting Gone record (R20-h2). A nil hook means
	// this backend has no forced-removal primitive, and the cases that need
	// one skip with that reason.
	ForceRemove func(ctx context.Context, storeID string, address Address, attribution ChangeAttribution, reason string) (RetentionAnswer, error)

	// Erase mints a corrected replacement Version for address rather than
	// editing it in place (R20-l), returning the corrected Address. A nil
	// hook means this backend has no erasure primitive, and the case that
	// needs it skips with that reason.
	Erase func(ctx context.Context, storeID string, address Address, correctedState string, attribution ChangeAttribution) (Address, error)

	// Mint mints a fresh Version for id under storeID, returning its
	// Address — analogous to EpochFixture.MintUnderEpoch, without an epoch
	// to peg it to. A nil hook means this backend has no way to mint a
	// real, resolvable Address to test retention against, and cases that
	// need one skip with that reason. Replaces the retired convention that
	// a fabricated, never-minted Address defaults to RestrictionLive (see
	// this file's package doc comment, formerly §4a choice 3).
	Mint func(ctx context.Context, storeID, id string) (Address, error)
}

// RunRemovalLeavesTheAddressAbleToAnswer pins R20-a/b: after a committed
// removal, the Address can still be resolved — it must answer, just not
// live.
func RunRemovalLeavesTheAddressAbleToAnswer(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "answerable")
	address := retentionMint(t, ctx, fixture, store, "answerable")

	preview, err := fixture.Remove(ctx, store, address, RemovalReasonRetention)
	if err != nil {
		t.Fatalf("Remove(%s, %s): %v", store, address, err)
	}
	if _, err := preview.Commit(ctx); err != nil {
		t.Fatalf("Commit removing %s: %v", address, err)
	}

	answer, err := fixture.Resolve(ctx, store, address)
	if err != nil {
		t.Fatalf("Resolve(%s, %s) after removal: %v", store, address, err)
	}
	if answer.Restriction == RestrictionLive {
		t.Fatalf("Resolve(%s) after Remove+Commit reports RestrictionLive, want a non-live Restriction: removal must leave the address able to answer, just not live", address)
	}
}

// RunRemovalReasonIsRetentionErasureOrReorganizationDistinctly pins R20-d:
// the three RemovalReasons produce pairwise-distinct Restrictions, so a
// caller can tell which kind of removal it is looking at from the answer
// alone.
func RunRemovalReasonIsRetentionErasureOrReorganizationDistinctly(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "reasons")
	cases := []struct {
		reason RemovalReason
		want   Restriction
	}{
		{RemovalReasonRetention, RestrictionGoneRetention},
		{RemovalReasonErasure, RestrictionGoneErasure},
		{RemovalReasonReorganization, RestrictionGoneReorganization},
	}
	seen := map[Restriction]bool{}
	for _, c := range cases {
		address := retentionMint(t, ctx, fixture, store, "reason-"+c.reason.String())
		preview, err := fixture.Remove(ctx, store, address, c.reason)
		if err != nil {
			t.Fatalf("Remove(%s, reason=%s): %v", address, c.reason, err)
		}
		if _, err := preview.Commit(ctx); err != nil {
			t.Fatalf("Commit(%s, reason=%s): %v", address, c.reason, err)
		}
		answer, err := fixture.Resolve(ctx, store, address)
		if err != nil {
			t.Fatalf("Resolve(%s) after Remove(reason=%s): %v", address, c.reason, err)
		}
		if answer.Restriction != c.want {
			t.Errorf("Remove(reason=%s) then Resolve = %s, want %s", c.reason, answer.Restriction, c.want)
		}
		if seen[answer.Restriction] {
			t.Errorf("Restriction %s was already reported for a different RemovalReason; the three reasons must map to pairwise-distinct Restrictions", answer.Restriction)
		}
		seen[answer.Restriction] = true
	}
}

// RunRemovalReportsTheSurvivingRetainedWindow pins R20-c: a removal's answer
// carries a populated RetainedWindow naming the surviving range.
func RunRemovalReportsTheSurvivingRetainedWindow(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "window")
	address := retentionMint(t, ctx, fixture, store, "window")

	preview, err := fixture.Remove(ctx, store, address, RemovalReasonRetention)
	if err != nil {
		t.Fatalf("Remove(%s): %v", address, err)
	}
	answer, err := preview.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit(%s): %v", address, err)
	}
	if answer.RetainedWindow == nil {
		t.Fatal("RetainedWindow = nil after a removal, want a populated window (R20-c)")
	}
	if answer.RetainedWindow.LowerBound == "" || answer.RetainedWindow.UpperBound == "" {
		t.Errorf("RetainedWindow = %+v, want both bounds non-empty", answer.RetainedWindow)
	}
}

// RunRemovalNeverReassignsASurvivingAddress pins R20-f: removing one Address
// must never alter or reassign a different, surviving Address's own answer.
func RunRemovalNeverReassignsASurvivingAddress(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "survivor")
	removed := retentionMint(t, ctx, fixture, store, "survivor-removed")
	survivor := retentionMint(t, ctx, fixture, store, "survivor-kept")

	before, err := fixture.Resolve(ctx, store, survivor)
	if err != nil {
		t.Fatalf("Resolve(survivor) before removing its neighbor: %v", err)
	}

	preview, err := fixture.Remove(ctx, store, removed, RemovalReasonRetention)
	if err != nil {
		t.Fatalf("Remove(%s): %v", removed, err)
	}
	if _, err := preview.Commit(ctx); err != nil {
		t.Fatalf("Commit(%s): %v", removed, err)
	}

	after, err := fixture.Resolve(ctx, store, survivor)
	if err != nil {
		t.Fatalf("Resolve(survivor) after removing its neighbor: %v", err)
	}
	if !sameRetentionAnswer(before, after) {
		t.Errorf("removing a neighbor changed the survivor's own answer from %+v to %+v; removing one Address must never reassign or alter another", before, after)
	}
}

// RunGoneIsDistinguishableFromUnknown pins FR-05's core claim: the SAME
// Address, asked of the store that removed it versus a store that never
// held it, must not resolve identically — Gone (once held, now removed) and
// Unknown (no lineage knowledge at all) are never conflated.
func RunGoneIsDistinguishableFromUnknown(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "gonevunknown")
	strangerStore := retentionStore(fixture, "gonevunknown-stranger")
	gone := retentionMint(t, ctx, fixture, store, "gonevunknown-gone")

	preview, err := fixture.Remove(ctx, store, gone, RemovalReasonRetention)
	if err != nil {
		t.Fatalf("Remove(%s): %v", gone, err)
	}
	if _, err := preview.Commit(ctx); err != nil {
		t.Fatalf("Commit(%s): %v", gone, err)
	}

	goneAnswer, err := fixture.Resolve(ctx, store, gone)
	if err != nil {
		t.Fatalf("Resolve(gone, its own store): %v", err)
	}
	unknownAnswer, err := fixture.Resolve(ctx, strangerStore, gone)
	if err != nil {
		t.Fatalf("Resolve(gone's address, a store that never held it): %v", err)
	}
	if goneAnswer.Restriction == RestrictionLive || unknownAnswer.Restriction == RestrictionLive {
		t.Fatalf("Live leaked into a Gone/Unknown comparison: gone=%s unknown=%s", goneAnswer.Restriction, unknownAnswer.Restriction)
	}
	if goneAnswer.Restriction == unknownAnswer.Restriction {
		t.Errorf("the SAME Address resolved identically (%s) whether asked of the store that removed it or a store that never held it; FR-05 requires Gone and Unknown stay distinct", goneAnswer.Restriction)
	}
}

// RunAnAddressNeverResolvesToADifferentState guards against a mixed-up
// lookup key: two distinct Addresses on the same store, deliberately left
// in different Restrictions, must never have their answers cross-
// contaminate.
func RunAnAddressNeverResolvesToADifferentState(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "nomixup")
	removedAddr := retentionMint(t, ctx, fixture, store, "nomixup-removed")
	liveAddr := retentionMint(t, ctx, fixture, store, "nomixup-live")

	preview, err := fixture.Remove(ctx, store, removedAddr, RemovalReasonRetention)
	if err != nil {
		t.Fatalf("Remove(%s): %v", removedAddr, err)
	}
	if _, err := preview.Commit(ctx); err != nil {
		t.Fatalf("Commit(%s): %v", removedAddr, err)
	}

	removedAnswer, err := fixture.Resolve(ctx, store, removedAddr)
	if err != nil {
		t.Fatalf("Resolve(removed): %v", err)
	}
	liveAnswer, err := fixture.Resolve(ctx, store, liveAddr)
	if err != nil {
		t.Fatalf("Resolve(live): %v", err)
	}

	if liveAnswer.Restriction != RestrictionLive {
		t.Fatalf("Resolve(an address nothing ever removed) = %s, want RestrictionLive", liveAnswer.Restriction)
	}
	if removedAnswer.Restriction == RestrictionLive {
		t.Fatal("Resolve(a removed address) = RestrictionLive, want a Gone restriction")
	}
	if sameRetentionAnswer(removedAnswer, liveAnswer) {
		t.Errorf("Resolve(%s) and Resolve(%s) returned identical answers %+v; a mixed-up lookup key would look exactly like this", removedAddr, liveAddr, removedAnswer)
	}
}

// RunADestructiveOperationEnumeratesAffectedAddressesFirst pins R20-g:
// Remove reports the Addresses it is about to affect BEFORE anything
// changes. Checked by confirming that obtaining the preview alone — without
// calling Commit — leaves the address's own answer untouched, regardless of
// what that answer is.
func RunADestructiveOperationEnumeratesAffectedAddressesFirst(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "enumeratefirst")
	address := retentionMint(t, ctx, fixture, store, "enumeratefirst")

	before, err := fixture.Resolve(ctx, store, address)
	if err != nil {
		t.Fatalf("Resolve before Remove: %v", err)
	}

	preview, err := fixture.Remove(ctx, store, address, RemovalReasonRetention)
	if err != nil {
		t.Fatalf("Remove(%s): %v", address, err)
	}
	if len(preview.AffectedAddresses) == 0 {
		t.Fatal("RemovalPreview.AffectedAddresses is empty, want the addresses this removal is about to affect (R20-g)")
	}

	// Commit is deliberately NOT called yet.
	after, err := fixture.Resolve(ctx, store, address)
	if err != nil {
		t.Fatalf("Resolve after Remove but before Commit: %v", err)
	}
	if !sameRetentionAnswer(before, after) {
		t.Errorf("Resolve changed from %+v to %+v after Remove but before Commit; enumeration must precede the change, not cause it", before, after)
	}
}

// RunAHoldPreventsRemovalAndReportsInRetainedBounds pins R20-h: an active
// Hold makes Remove/Commit refuse, AND the held Address must show up
// positively in the reported RetainedWindow bounds — not merely be inferred
// from the absence of a Gone restriction.
//
// §4a choice 1 (see this file's package doc comment): Commit's refusal is
// the typed ErrRetentionHeld rather than a non-accepted-shaped
// RetentionAnswer.
func RunAHoldPreventsRemovalAndReportsInRetainedBounds(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Hold == nil {
		t.Skip("this backend has no hold primitive (Hold is nil)")
	}
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "held")
	address := retentionMint(t, ctx, fixture, store, "held")

	if err := fixture.Hold(ctx, store, address); err != nil {
		t.Fatalf("Hold(%s): %v", address, err)
	}

	preview, err := fixture.Remove(ctx, store, address, RemovalReasonRetention)
	if err != nil {
		t.Fatalf("Remove(%s) under a hold: %v", address, err)
	}
	if _, err := preview.Commit(ctx); !errors.Is(err, ErrRetentionHeld) {
		t.Fatalf("Commit(%s) under a hold error = %v, want ErrRetentionHeld", address, err)
	}

	answer, err := fixture.Resolve(ctx, store, address)
	if err != nil {
		t.Fatalf("Resolve(%s) after a refused, held removal: %v", address, err)
	}
	if answer.Restriction == RestrictionGoneRetention || answer.Restriction == RestrictionGoneErasure || answer.Restriction == RestrictionGoneReorganization {
		t.Errorf("Resolve(%s) reports %s after Commit was refused for being held, want it to remain out of any Gone restriction", address, answer.Restriction)
	}
	if answer.RetainedWindow == nil {
		t.Fatalf("Resolve(%s) after a refused, held removal reports RetainedWindow = nil, want a populated window: R20-h's promise is that a Hold shows up IN the retained bounds, not merely as an absence of a Gone restriction", address)
	}
	if answer.RetainedWindow.LowerBound != address || answer.RetainedWindow.UpperBound != address {
		t.Errorf("Resolve(%s).RetainedWindow = %+v, want both bounds naming the held Address itself: nothing else was ever minted in this store, so the range the Hold protects is exactly this one Address", address, answer.RetainedWindow)
	}
}

// RunForcingAHeldRemovalRecordsWhoWhenWhy pins R20-h2: ForceRemove crosses an
// active Hold and must record who, when, and why — read back via
// RetentionAnswer.Attribution, not just accepted as parameters (formerly
// §4a choice 2, now DIED; see this file's package doc comment).
func RunForcingAHeldRemovalRecordsWhoWhenWhy(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Hold == nil {
		t.Skip("this backend has no hold primitive (Hold is nil)")
	}
	if fixture.ForceRemove == nil {
		t.Skip("this backend has no forced-removal primitive (ForceRemove is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "forced")
	address := retentionMint(t, ctx, fixture, store, "forced")

	if err := fixture.Hold(ctx, store, address); err != nil {
		t.Fatalf("Hold(%s): %v", address, err)
	}

	actor := "retention-operator"
	message := "legal hold expired"
	attribution := ChangeAttribution{Actor: &actor, Message: &message, At: time.Now().UTC()}

	answer, err := fixture.ForceRemove(ctx, store, address, attribution, "hold-expired")
	if err != nil {
		t.Fatalf("ForceRemove(%s) despite an active hold: %v", address, err)
	}
	if answer.Restriction == RestrictionLive {
		t.Errorf("ForceRemove(%s) reports RestrictionLive, want a Gone restriction", address)
	}
	if answer.ProducingStore != store {
		t.Errorf("ForceRemove(%s).ProducingStore = %q, want %q", address, answer.ProducingStore, store)
	}
	if answer.Attribution == nil {
		t.Fatalf("ForceRemove(%s).Attribution = nil, want the who/when/why recorded for this forced removal (R20-h2)", address)
	}
	if !sameAttribution(*answer.Attribution, attribution) {
		t.Errorf("ForceRemove(%s).Attribution = %+v, want %+v (R20-h2: who/when/why must be recorded, not just accepted as parameters)", address, *answer.Attribution, attribution)
	}
}

// RunEveryRetentionAnswerNamesItsProducingStore pins R20-i for both a live
// and a non-live answer: ProducingStore always names the store that
// actually produced the answer.
func RunEveryRetentionAnswerNamesItsProducingStore(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	storeA := retentionStore(fixture, "namesA")
	storeB := retentionStore(fixture, "namesB")
	liveAddr := retentionMint(t, ctx, fixture, storeA, "names-live")
	goneAddr := retentionMint(t, ctx, fixture, storeB, "names-gone")

	preview, err := fixture.Remove(ctx, storeB, goneAddr, RemovalReasonRetention)
	if err != nil {
		t.Fatalf("Remove(%s): %v", goneAddr, err)
	}
	if _, err := preview.Commit(ctx); err != nil {
		t.Fatalf("Commit(%s): %v", goneAddr, err)
	}

	liveAnswer, err := fixture.Resolve(ctx, storeA, liveAddr)
	if err != nil {
		t.Fatalf("Resolve(%s, %s): %v", storeA, liveAddr, err)
	}
	if liveAnswer.ProducingStore != storeA {
		t.Errorf("Resolve(%s, ...).ProducingStore = %q, want %q (R20-i: live answer)", storeA, liveAnswer.ProducingStore, storeA)
	}

	goneAnswer, err := fixture.Resolve(ctx, storeB, goneAddr)
	if err != nil {
		t.Fatalf("Resolve(%s, %s): %v", storeB, goneAddr, err)
	}
	if goneAnswer.ProducingStore != storeB {
		t.Errorf("Resolve(%s, ...).ProducingStore = %q, want %q (R20-i: non-live answer)", storeB, goneAnswer.ProducingStore, storeB)
	}
}

// RunAStoreWithNoLineageKnowledgeAnswersUnknownNotGone pins FR-06: a store
// asked about a store/Address pair it has no lineage relationship with at
// all — never received it, never removed it — must answer Unknown, not one
// of the Gone restrictions.
func RunAStoreWithNoLineageKnowledgeAnswersUnknownNotGone(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	store := retentionStore(fixture, "nolineage")
	address := retentionAddress(fixture, "nolineage-nevertracked")

	answer, err := fixture.Resolve(ctx, store, address)
	if err != nil {
		t.Fatalf("Resolve(%s, %s): %v", store, address, err)
	}
	if answer.Restriction != RestrictionUnknown {
		t.Errorf("Resolve on a store/address pair with no lineage relationship = %s, want RestrictionUnknown (FR-06)", answer.Restriction)
	}
}

// RunAStoreThatRemovesStateStillAnswersGoneDurably pins R20-j: a removed
// Address's Gone answer is durable — two successive reads must agree, not
// merely happen to agree once.
func RunAStoreThatRemovesStateStillAnswersGoneDurably(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Remove == nil {
		t.Skip("this backend has no removal primitive (Remove is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "durable")
	address := retentionMint(t, ctx, fixture, store, "durable")

	preview, err := fixture.Remove(ctx, store, address, RemovalReasonErasure)
	if err != nil {
		t.Fatalf("Remove(%s): %v", address, err)
	}
	if _, err := preview.Commit(ctx); err != nil {
		t.Fatalf("Commit(%s): %v", address, err)
	}

	first, err := fixture.Resolve(ctx, store, address)
	if err != nil {
		t.Fatalf("Resolve(%s), first read: %v", address, err)
	}
	second, err := fixture.Resolve(ctx, store, address)
	if err != nil {
		t.Fatalf("Resolve(%s), second read: %v", address, err)
	}
	if first.Restriction == RestrictionLive {
		t.Fatalf("Resolve(%s) after removal = RestrictionLive, want a Gone restriction", address)
	}
	if !sameRetentionAnswer(first, second) {
		t.Errorf("two successive Resolve(%s) calls disagreed: %+v vs %+v; a removal must answer Gone DURABLY, not from a cache that can revert", address, first, second)
	}
}

// RunErasureMintsACorrectedVersionRatherThanEditingInPlace pins R20-l: Erase
// returns a NEW Address for the corrected state rather than editing the
// original in place — the original resolves GoneErasure, the corrected
// replacement resolves Live.
func RunErasureMintsACorrectedVersionRatherThanEditingInPlace(t *testing.T, ctx context.Context, fixture RetentionFixture) {
	t.Helper()
	if fixture.Erase == nil {
		t.Skip("this backend has no erasure primitive (Erase is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.Mint == nil {
		t.Skip("this backend has no way to mint a real Address to test retention against (Mint is nil)")
	}
	store := retentionStore(fixture, "erasure")
	original := retentionMint(t, ctx, fixture, store, "erasure-original")
	actor := "retention-operator"
	attribution := ChangeAttribution{Actor: &actor, At: time.Now().UTC()}

	corrected, err := fixture.Erase(ctx, store, original, `{"corrected":true}`, attribution)
	if err != nil {
		t.Fatalf("Erase(%s): %v", original, err)
	}
	if corrected == original {
		t.Fatalf("Erase(%s) returned the SAME Address, want a NEW one (R20-l: erasure mints a corrected version rather than editing in place)", original)
	}

	originalAnswer, err := fixture.Resolve(ctx, store, original)
	if err != nil {
		t.Fatalf("Resolve(original): %v", err)
	}
	if originalAnswer.Restriction != RestrictionGoneErasure {
		t.Errorf("Resolve(the erased original) = %s, want RestrictionGoneErasure", originalAnswer.Restriction)
	}

	correctedAnswer, err := fixture.Resolve(ctx, store, corrected)
	if err != nil {
		t.Fatalf("Resolve(corrected): %v", err)
	}
	if correctedAnswer.Restriction != RestrictionLive {
		t.Errorf("Resolve(the corrected replacement) = %s, want RestrictionLive", correctedAnswer.Restriction)
	}
}

// EpochBumpTrigger enumerates the only three events R20-m allows to advance
// a store's epoch.
type EpochBumpTrigger int

const (
	EpochBumpTriggerRestore EpochBumpTrigger = iota
	EpochBumpTriggerDestructiveReinit
	EpochBumpTriggerTokenSchemeChange
)

func (tr EpochBumpTrigger) String() string {
	switch tr {
	case EpochBumpTriggerRestore:
		return "restore"
	case EpochBumpTriggerDestructiveReinit:
		return "destructive-reinit"
	case EpochBumpTriggerTokenSchemeChange:
		return "token-scheme-change"
	default:
		return fmt.Sprintf("EpochBumpTrigger(%d)", int(tr))
	}
}

// EpochFixture supplies the capabilities under test for R20-m/n: the
// store-epoch generation counter and its selective effect on addresses
// minted under an earlier epoch.
type EpochFixture struct {
	IssuePrefix string

	// CurrentEpoch reports storeID's current epoch generation. A nil hook
	// means this backend has no epoch concept yet, and cases that need one
	// skip with that reason.
	CurrentEpoch func(ctx context.Context, storeID string) (int, error)

	// BumpEpoch advances storeID's epoch for the given trigger, returning
	// the new epoch. A nil hook means this backend has no epoch concept
	// yet, and cases that need one skip with that reason.
	BumpEpoch func(ctx context.Context, storeID string, trigger EpochBumpTrigger) (int, error)

	// MintUnderEpoch mints a fresh Version for id under storeID's current
	// epoch, returning its Address. A nil hook means this backend has no
	// way to mint a Version under a known epoch, and cases that need one
	// skip with that reason.
	MintUnderEpoch func(ctx context.Context, storeID, id string) (Address, error)

	// StillServes reports whether storeID still serves the Version named by
	// a prior-epoch address, despite the epoch having moved on. A nil hook
	// means this backend cannot report that, and the case that needs it
	// skips with that reason.
	StillServes func(ctx context.Context, storeID string, address Address) (bool, error)

	// Resolve is the SAME RetentionResolve type RetentionFixture uses — an
	// epoch bump's effect on an Address is observed through the identical
	// read path as an ordinary removal. A nil hook means this backend does
	// not yet report retention state, and cases that need one skip with
	// that reason.
	Resolve RetentionResolve

	// CurrentAddressFor reports the Address a still-served, prior-epoch
	// Version now resolves to under the new epoch. A nil hook means this
	// backend cannot report that, and the case that needs it skips with
	// that reason.
	CurrentAddressFor func(ctx context.Context, storeID string, oldAddress Address) (Address, error)
}

// RunAnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange pins R20-m: a
// store's epoch advances for exactly the three named triggers.
//
// THIS CASE CANNOT PROVE A FOURTH, UNINVITED TRIGGER NEVER BUMPS THE EPOCH
// in some real backend — enumerating "never" over an open set of possible
// call sites is not something an interface-level test can do. That is
// Phase 3's implementation discipline to hold, not something this suite can
// enforce (NFR-03's blind spot, mirroring canonicalizeForContract's stated
// blind spot in metadata_cas_contract.go).
func RunAnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange(t *testing.T, ctx context.Context, fixture EpochFixture) {
	t.Helper()
	if fixture.BumpEpoch == nil {
		t.Skip("this backend has no epoch concept yet (BumpEpoch is nil)")
	}
	if fixture.CurrentEpoch == nil {
		t.Skip("this backend cannot report its current epoch (CurrentEpoch is nil)")
	}
	store := epochStore(fixture, "triggers")

	for _, trigger := range []EpochBumpTrigger{
		EpochBumpTriggerRestore,
		EpochBumpTriggerDestructiveReinit,
		EpochBumpTriggerTokenSchemeChange,
	} {
		before, err := fixture.CurrentEpoch(ctx, store)
		if err != nil {
			t.Fatalf("CurrentEpoch before %s: %v", trigger, err)
		}
		after, err := fixture.BumpEpoch(ctx, store, trigger)
		if err != nil {
			t.Fatalf("BumpEpoch(%s): %v", trigger, err)
		}
		if after <= before {
			t.Errorf("BumpEpoch(%s) = %d, want an epoch greater than the pre-bump value %d", trigger, after, before)
		}
		observed, err := fixture.CurrentEpoch(ctx, store)
		if err != nil {
			t.Fatalf("CurrentEpoch after %s: %v", trigger, err)
		}
		if observed != after {
			t.Errorf("CurrentEpoch after BumpEpoch(%s) = %d, want the bumped value %d", trigger, observed, after)
		}
	}
}

// RunEpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed pins R20-n — "Rev7
// tightening... the clause most likely to be gotten wrong" in the
// architecture doc's own words. A prior-epoch Address goes
// GoneReorganization UNLESS the backend still serves that Version, in which
// case it must stay Live AND additionally report the Address it now
// resolves to under the new epoch.
//
// StillServes is an ORACLE here, not a control: this case cannot make a real
// backend keep serving one address and drop another, since Phase 0 wires no
// real backend. It asks StillServes what the fixture (once real) decided,
// and checks the rest of the contract's promise against that decision for
// both outcomes.
func RunEpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed(t *testing.T, ctx context.Context, fixture EpochFixture) {
	t.Helper()
	if fixture.MintUnderEpoch == nil {
		t.Skip("this backend has no way to mint a Version under a known epoch (MintUnderEpoch is nil)")
	}
	if fixture.BumpEpoch == nil {
		t.Skip("this backend has no epoch concept yet (BumpEpoch is nil)")
	}
	if fixture.StillServes == nil {
		t.Skip("this backend cannot report whether it still serves a prior-epoch Version (StillServes is nil)")
	}
	if fixture.Resolve == nil {
		t.Skip("this backend does not yet report retention state (Resolve is nil)")
	}
	if fixture.CurrentAddressFor == nil {
		t.Skip("this backend cannot report a still-served Version's new Address (CurrentAddressFor is nil)")
	}
	store := epochStore(fixture, "voids")

	addrA, err := fixture.MintUnderEpoch(ctx, store, "record-a")
	if err != nil {
		t.Fatalf("MintUnderEpoch(record-a): %v", err)
	}
	addrB, err := fixture.MintUnderEpoch(ctx, store, "record-b")
	if err != nil {
		t.Fatalf("MintUnderEpoch(record-b): %v", err)
	}

	newEpoch, err := fixture.BumpEpoch(ctx, store, EpochBumpTriggerRestore)
	if err != nil {
		t.Fatalf("BumpEpoch: %v", err)
	}

	servesA, err := fixture.StillServes(ctx, store, addrA)
	if err != nil {
		t.Fatalf("StillServes(%s): %v", addrA, err)
	}
	servesB, err := fixture.StillServes(ctx, store, addrB)
	if err != nil {
		t.Fatalf("StillServes(%s): %v", addrB, err)
	}
	if servesA == servesB {
		t.Skip("this scenario needs one still-served and one no-longer-served address to exercise both halves of R20-n; StillServes reported the same answer for both, so this fixture gives this case nothing to contrast")
	}

	stillServed, noLongerServed := addrA, addrB
	if servesB && !servesA {
		stillServed, noLongerServed = addrB, addrA
	}

	voidedAnswer, err := fixture.Resolve(ctx, store, noLongerServed)
	if err != nil {
		t.Fatalf("Resolve(no-longer-served): %v", err)
	}
	if voidedAnswer.Restriction != RestrictionGoneReorganization {
		t.Errorf("Resolve(a prior-epoch Address no longer served) = %s, want RestrictionGoneReorganization (R20-n)", voidedAnswer.Restriction)
	}
	if voidedAnswer.Epoch == nil {
		t.Fatal("Resolve(a prior-epoch Address no longer served).Epoch = nil, want the current epoch populated (R20-n: the answer must name the current epoch, not just Restriction alone)")
	} else if *voidedAnswer.Epoch != newEpoch {
		t.Errorf("Resolve(a prior-epoch Address no longer served).Epoch = %d, want the current epoch %d", *voidedAnswer.Epoch, newEpoch)
	}

	keptAnswer, err := fixture.Resolve(ctx, store, stillServed)
	if err != nil {
		t.Fatalf("Resolve(still-served): %v", err)
	}
	if keptAnswer.Restriction != RestrictionLive {
		t.Errorf("Resolve(a prior-epoch Address the backend still serves) = %s, want RestrictionLive (R20-n's exception)", keptAnswer.Restriction)
	}

	newAddr, err := fixture.CurrentAddressFor(ctx, store, stillServed)
	if err != nil {
		t.Fatalf("CurrentAddressFor(%s): %v", stillServed, err)
	}
	if newAddr == "" {
		t.Error("CurrentAddressFor(a still-served prior-epoch Address) returned an empty Address, want a real one under the new epoch")
	}
}

// --- fixture helpers -------------------------------------------------------

// retentionStore names a storeID namespaced by the fixture's IssuePrefix and
// tag, so cases that need more than one store (e.g. to probe FR-06's
// no-lineage-knowledge case) get independent ones.
func retentionStore(fixture RetentionFixture, tag string) string {
	return fixture.IssuePrefix + "-retention-store-" + tag
}

// retentionAddress builds a fresh, syntactically valid Address namespaced by
// the fixture's IssuePrefix and tag, WITHOUT ever giving it to any store.
// RunAStoreWithNoLineageKnowledgeAnswersUnknownNotGone is the only remaining
// caller: Unknown specifically means a store with no lineage knowledge of
// the Address at all (FR-06), so fabricating one that no store has ever
// seen is the correct construction here, not a shortcut — unlike every
// other case in this file, which now mints a real Address via
// fixture.Mint/retentionMint instead of relying on the retired
// Live-baseline convention (see this file's package doc comment, formerly
// §4a choice 3).
func retentionAddress(fixture RetentionFixture, tag string) Address {
	return Address(fixture.IssuePrefix + "-retention-address-" + tag)
}

// retentionMint mints a fresh Address for tag on store via fixture.Mint,
// fataling the case if minting fails — analogous to how
// EpochFixture.MintUnderEpoch is called directly in this file's Epoch
// cases, without its own wrapper. Cases use this in place of the retired
// retentionAddress convention (formerly §4a choice 3, now DIED — see this
// file's package doc comment) for any Address that must resolve as a real,
// store-established Version.
func retentionMint(t *testing.T, ctx context.Context, fixture RetentionFixture, store, tag string) Address {
	t.Helper()
	address, err := fixture.Mint(ctx, store, tag)
	if err != nil {
		t.Fatalf("Mint(%s, %s): %v", store, tag, err)
	}
	return address
}

// epochStore names a storeID namespaced by the fixture's IssuePrefix and tag.
func epochStore(fixture EpochFixture, tag string) string {
	return fixture.IssuePrefix + "-epoch-store-" + tag
}

// sameRetentionAnswer compares two RetentionAnswer values by content rather
// than identity, so a case can assert "this did not change" without
// depending on the concrete Restriction the backend happened to pick.
func sameRetentionAnswer(a, b RetentionAnswer) bool {
	return a.Restriction == b.Restriction &&
		a.ProducingStore == b.ProducingStore &&
		sameRetainedWindow(a.RetainedWindow, b.RetainedWindow) &&
		sameAttributionPtr(a.Attribution, b.Attribution) &&
		sameIntPtr(a.Epoch, b.Epoch)
}

func sameAttributionPtr(a, b *ChangeAttribution) bool {
	if a == nil || b == nil {
		return a == b
	}
	return sameAttribution(*a, *b)
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameRetainedWindow(a, b *RetainedWindow) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
