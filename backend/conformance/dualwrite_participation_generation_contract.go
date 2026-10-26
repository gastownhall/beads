package conformance

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// This file holds the write-fence cases dualwrite_history_contract.go's own
// "WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN" section explicitly deferred:
// "PARTICIPATION-GENERATION GATING (design §16.2b)... It gets its own
// dedicated cases once 0070 lands, beside this file rather than inside it."
// Migration 0070 now carries participation_generation on both issues and
// wisps (design §16.3 steps 4-5, be-dt74u amendment, be-h89oq), so this is
// that file.
//
// Design §16.2b, verbatim substance: the flag-on writer does not increment
// current_revision or mint an issue_versions row for an UPDATE-shaped
// mutation against a record whose participation_generation is NULL — a skip,
// not a refusal (FR-2's zero-rows no-op shape). A CREATE-shaped mutation is
// different: a brand-new row has no prior legacy state to preserve, so if the
// flag is already on at creation time, the writer stamps
// participation_generation as part of the same insert that begins its
// history. A create with the flag off leaves the column NULL, indistinguishable
// from a true legacy row (FR-7's byte-identical requirement).
//
// WHY A SEPARATE FIXTURE RATHER THAN THREE MORE DualWriteFixture CLOSURES.
// DualWriteFixture's Mutate/MutateAsNoOp pair is deliberately flag-fixed-at-
// construction (see that file's SEEDING DISCIPLINE note and each leg's
// newXxxDualWriteFixture, which calls SetVersionedHistoryEnabled exactly
// once). The legacy-record case here is inherently "created while the flag
// was OFF, then mutated after the flag turns ON" — a fixture built once with
// one flag state cannot express that sequence, so SetFlag here is a toggle
// callable more than once per fixture, not a one-time constructor argument.
//
// THE FIXTURE'S CLOSURES ARE THE OBSERVABLE SURFACE, NOT THE SCHEMA — same
// discipline as DualWriteFixture: Mutate/MutateExisting drive CreateIssue and
// UpdateIssue (or their unit-of-work equivalents), never issue_versions or
// store_epoch directly.
//
// WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN:
//   - THE EXACT STAMPED VALUE. That a create-shaped mutation with the flag on
//     stamps SOME non-NULL participation_generation is pinned here; that the
//     value equals the current store_epoch.epoch is not — reaching
//     store_epoch is the same one-column, two-of-three-legs-need-white-box-
//     access problem dualwrite_history_contract.go's FR-6 note describes, and
//     the same resolution applies: pinned per leg, dolt-only, alongside
//     TestDualWriteStampsTheCurrentStoreEpochOnEachVersionRow.
//   - wisps.participation_generation ITSELF. With history on, a demotion
//     records there the participation the issues row had, and the move back
//     out restores it (issueops.carryParticipationInTx); nothing else reads
//     or writes it. The round-trip cases below pin that through the issues
//     row the wisp becomes again, the observable surface; the encoding on
//     the wisps row is pinned per leg, on dolt
//     (TestPlaneRoundTripKeepsTheGenerationItCarried).
type ParticipationGenerationFixture struct {
	// IssuePrefix namespaces the id each case mints, the same discipline
	// DualWriteFixture uses and for the same reason.
	IssuePrefix string
	// SetFlag toggles versioned history for every subsequent call through
	// this fixture, and may be called more than once per case — unlike
	// DualWriteFixture, whose flag is fixed at construction.
	SetFlag func(ctx context.Context, enabled bool) error
	// Mutate creates a new issue with the given id: a CREATE-shaped mutation
	// in design §16.2b's sense.
	Mutate func(ctx context.Context, id string) error
	// MutateExisting applies a real (non-no-op) UPDATE-shaped mutation to an
	// already-existing issue — a different value than Mutate gave it, so a
	// vacuous no-op skip can never be mistaken for the fence.
	MutateExisting func(ctx context.Context, id string) error
	// CreateWisp creates a new wisp-plane (ephemeral) issue with the given
	// id: the starting point for the two ways a record reaches the issues
	// plane without being created there. A wisp is never versioned (FR-8),
	// and one created as a wisp carries no participation, in either flag
	// state.
	CreateWisp func(ctx context.Context, id string) error
	// PromoteWisp promotes the wisp id onto the issues plane
	// (PromoteFromEphemeral, or the unit-of-work leg's PromoteWisp).
	PromoteWisp func(ctx context.Context, id string) error
	// MovePersistent moves the wisp id onto the issues plane through a
	// persistence-mode update (bd update --persistent). It is its own
	// closure rather than a second spelling of PromoteWisp because it mints at
	// a different site on every leg: the guarded update's one end-of-update
	// mint on dolt and embeddeddolt, MoveIssuePersistenceInTx's own on uow.
	// On an issues-plane row whose flags disagree with it (see
	// CreateNoHistoryIssue) the same update repairs the flags in place, a
	// same-plane normalize, rather than moving the row.
	MovePersistent func(ctx context.Context, id string) error
	// MoveEphemeral moves the issues-plane row id into the wisps table
	// through a persistence-mode update (bd update --ephemeral): the guarded
	// update on dolt and embeddeddolt, MovePersistence on uow, the client's
	// persistence-mode Lifecycle.Update on http. It is how a row that already
	// lived on the issues plane becomes a wisp, the first half of the
	// round-trip cases; PromoteWisp or MovePersistent is the second.
	MoveEphemeral func(ctx context.Context, id string) error
	// ClearWispFlag clears the wisp id's ephemeral flag IN PLACE: the untyped
	// UpdateIssue of {"wisp": false} bd mol squash makes on a squashed root
	// (UpdateWisp on the unit-of-work leg, the way its molecule writer routes
	// it). The row stays in the wisps table, now carrying neither Ephemeral
	// nor NoHistory.
	ClearWispFlag func(ctx context.Context, id string) error
	// SquashWispRoot closes the wisp id and then clears its flag as
	// ClearWispFlag does, both in ONE transaction: the shape bd mol squash's
	// root auto-close takes, where an error from either write aborts both.
	SquashWispRoot func(ctx context.Context, id string) error
	// CreateUnflaggedWisp creates id in the wisps table with neither
	// Ephemeral nor NoHistory set: import's "wisp_plane" marker, which pins
	// the plane whatever the flags say (WispPlaneOverride = &true, through
	// the batch create import runs), or on the unit-of-work leg CreateWisp,
	// which routes by the call rather than by the flags.
	CreateUnflaggedWisp func(ctx context.Context, id string) error
	// CreateNoHistoryIssue creates id in the ISSUES table carrying
	// NoHistory=true: import's promoted-no-history shape (bd-r9uce), a
	// durable row whose stray flag is preserved while WispPlaneOverride =
	// &false pins its plane, through the batch create import runs; or on the
	// unit-of-work leg CreateIssue, which routes by the call.
	CreateNoHistoryIssue func(ctx context.Context, id string) error
	// CurrentRevision reads issues.current_revision for id.
	CurrentRevision func(ctx context.Context, id string) (int64, error)
	// VersionRowCount reads how many issue_versions rows exist for id.
	VersionRowCount func(ctx context.Context, id string) (int, error)
	// ParticipationGeneration reads issues.participation_generation for id,
	// returning nil for SQL NULL.
	ParticipationGeneration func(ctx context.Context, id string) (*int64, error)
	// InWispsTable reports whether the wisps table holds a row for id: the
	// precondition the wisps-table cases check, so they cannot pass on a row
	// that a step moved to the issues table.
	InWispsTable func(ctx context.Context, id string) (bool, error)
	// ParticipatingIssueIDs is the leg's production participation read, the
	// one `bd config set versioned-history.enabled true` narrows its offenders
	// with: storage.HistoryParticipation on the store legs, the unit of work's
	// IssueUseCase on uow. Unlike ParticipationGeneration it is not white-box
	// test access; it is the read under test.
	ParticipatingIssueIDs func(ctx context.Context, ids []string) (map[string]struct{}, error)
}

// RunParticipationGenerationCreateWithFlagOnStampsAValue pins design
// §16.2(a)/(b): a create-shaped mutation with the flag already on stamps a
// non-NULL participation_generation on the new row, as part of the same
// insert that begins its history — a brand-new row has no legacy state to
// preserve, so there is nothing to skip.
func RunParticipationGenerationCreateWithFlagOnStampsAValue(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-create-stamps"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	gen, err := fixture.ParticipationGeneration(ctx, id)
	if err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	}
	if gen == nil {
		t.Errorf("participation_generation for %s is NULL immediately after a create-shaped mutation with "+
			"the flag on, want a stamped non-NULL value: design §16.2b requires the writer to declare "+
			"participation at insert time, since a brand-new row has no prior legacy state to preserve", id)
	}
}

// RunParticipationGenerationCreateWithFlagOffLeavesLegacyNull pins the other
// half of design §16.2(b): a create-shaped mutation with the flag off leaves
// participation_generation NULL, indistinguishable from a true legacy row —
// flag-off must stay byte-identical to pre-Phase-2 behavior (FR-7).
func RunParticipationGenerationCreateWithFlagOffLeavesLegacyNull(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	if err := fixture.SetFlag(ctx, false); err != nil {
		t.Fatalf("disabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-create-flag-off"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	gen, err := fixture.ParticipationGeneration(ctx, id)
	if err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	}
	if gen != nil {
		t.Errorf("participation_generation for %s = %d with the flag off at creation, want NULL: a "+
			"flag-off create must remain indistinguishable from a true legacy row (design §16.2b)", id, *gen)
	}
}

// RunParticipationGenerationSkipsUpdateOnLegacyRecord pins the write fence's
// central claim, design §16.2(b): the flag-on writer does not increment
// current_revision or mint an issue_versions row for an update-shaped
// mutation against a record whose participation_generation is NULL. The
// record must be a REAL legacy record — created while the flag was off, not
// merely one the fixture asserts is NULL — so this also re-confirms that
// precondition before trusting the rest of the case.
func RunParticipationGenerationSkipsUpdateOnLegacyRecord(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	if err := fixture.SetFlag(ctx, false); err != nil {
		t.Fatalf("disabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-legacy-skip"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	if gen, err := fixture.ParticipationGeneration(ctx, id); err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	} else if gen != nil {
		t.Fatalf("participation_generation for %s = %d before the flag was ever turned on, want NULL — "+
			"this case cannot test the legacy-record fence against a record that was never actually legacy",
			id, *gen)
	}

	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	beforeRevision, err := fixture.CurrentRevision(ctx, id)
	if err != nil {
		t.Fatalf("reading current_revision for %s: %v", id, err)
	}
	beforeCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}

	if err := fixture.MutateExisting(ctx, id); err != nil {
		t.Fatalf("mutating existing %s: %v", id, err)
	}

	afterRevision, err := fixture.CurrentRevision(ctx, id)
	if err != nil {
		t.Fatalf("reading current_revision for %s: %v", id, err)
	}
	afterCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if afterCount != beforeCount {
		t.Errorf("version row count for %s went from %d to %d across an update-shaped mutation on a "+
			"legacy (participation_generation IS NULL) record with the flag now on, want unchanged: "+
			"design §16.2b requires the writer to skip minting for a record it has not positively "+
			"promoted", id, beforeCount, afterCount)
	}
	if afterRevision != beforeRevision {
		t.Errorf("current_revision for %s went from %d to %d across the same skipped mutation, want "+
			"unchanged: a skip means neither half of RecordVersionInTx's write runs, not just the "+
			"version-row half", id, beforeRevision, afterRevision)
	}
}

// RunParticipationGenerationProceedsForPromotedRecord is the fence's
// not-vacuous check: an update-shaped mutation against a record whose
// participation_generation is ALREADY stamped (created with the flag on,
// i.e. positively declared, not legacy) must mint normally. Without this
// case, an implementation that skips every update unconditionally would
// still pass RunParticipationGenerationSkipsUpdateOnLegacyRecord.
func RunParticipationGenerationProceedsForPromotedRecord(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-promoted-proceeds"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	if gen, err := fixture.ParticipationGeneration(ctx, id); err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	} else if gen == nil {
		t.Fatalf("participation_generation for %s is NULL immediately after a create with the flag on — "+
			"this case cannot test the promoted-record path against a record that was never actually "+
			"promoted", id)
	}

	beforeCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if err := fixture.MutateExisting(ctx, id); err != nil {
		t.Fatalf("mutating existing %s: %v", id, err)
	}
	afterCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if afterCount != beforeCount+1 {
		t.Errorf("version row count for %s went from %d to %d across an update-shaped mutation on an "+
			"already-promoted record, want exactly +1: the fence must skip only a legacy (NULL) record, "+
			"not every update (design §16.2b)", id, beforeCount, afterCount)
	}
}

// RunParticipationGenerationFlagOffSkipsUpdateOnParticipatingRecord pins the
// switch's off half for a record the fence would let through: created with
// the flag on (so participation_generation is stamped), then updated after
// the flag is turned back off, it must mint nothing. ProceedsForPromotedRecord
// shows the fence passes such a record, so only the flag stands between this
// update and a mint. No other case can see a writer that ignores the flag on
// updates: every other flag-off record is legacy, and the fence stops those
// first.
func RunParticipationGenerationFlagOffSkipsUpdateOnParticipatingRecord(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-flag-off-skip"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	if gen, err := fixture.ParticipationGeneration(ctx, id); err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	} else if gen == nil {
		t.Fatalf("participation_generation for %s is NULL immediately after a create with the flag on — "+
			"this case cannot test the flag against a record the fence would have stopped anyway", id)
	}

	if err := fixture.SetFlag(ctx, false); err != nil {
		t.Fatalf("disabling versioned history: %v", err)
	}
	beforeRevision, err := fixture.CurrentRevision(ctx, id)
	if err != nil {
		t.Fatalf("reading current_revision for %s: %v", id, err)
	}
	beforeCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}

	if err := fixture.MutateExisting(ctx, id); err != nil {
		t.Fatalf("mutating existing %s: %v", id, err)
	}

	afterRevision, err := fixture.CurrentRevision(ctx, id)
	if err != nil {
		t.Fatalf("reading current_revision for %s: %v", id, err)
	}
	afterCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if afterCount != beforeCount {
		t.Errorf("version row count for %s went from %d to %d across an update-shaped mutation with the "+
			"flag off, want unchanged: turning versioned history off stops minting for every record, "+
			"including one that already participates", id, beforeCount, afterCount)
	}
	if afterRevision != beforeRevision {
		t.Errorf("current_revision for %s went from %d to %d across the same update with the flag off, "+
			"want unchanged", id, beforeRevision, afterRevision)
	}
}

// RunParticipationGenerationWispPromotionBeginsHistory pins that promoting a
// wisp onto the issues plane is CREATE-shaped in design §16.2b's sense. (This
// is wisp promotion, PromoteFromEphemeral: not R2.3's promotion of a legacy
// record, which is what ProceedsForPromotedRecord's name refers to.) The
// issues row a promotion inserts is brand new on that plane, and the wisp
// behind it, created as a wisp, was never versioned and never declared
// participation (FR-8), so there is no legacy state to preserve (a row that
// was demoted is the round-trip cases' subject): with the flag on, the promotion stamps
// participation_generation and mints the record's first version, as a create
// there would. Treated as UPDATE-shaped, the fence finds the new row NULL and
// skips, and because nothing stamps the column afterwards every later update
// skips too, so a record created with the flag on is never versioned at all.
func RunParticipationGenerationWispPromotionBeginsHistory(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	runWispArrivalBeginsHistory(t, ctx, fixture, "promote", fixture.PromoteWisp)
}

// RunParticipationGenerationWispPersistenceMoveBeginsHistory is
// RunParticipationGenerationWispPromotionBeginsHistory for the other way a
// wisp reaches the issues plane, a persistence-mode update (bd update
// --persistent). It needs its own case because it reaches a different mint
// site than promotion on every leg (see MovePersistent).
func RunParticipationGenerationWispPersistenceMoveBeginsHistory(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	runWispArrivalBeginsHistory(t, ctx, fixture, "move-persistent", fixture.MovePersistent)
}

// runWispArrivalBeginsHistory is the body the two wisp-arrival cases share:
// with the flag on, a wisp that arrives on the issues plane through arrive
// carries a stamped participation_generation and exactly one version row,
// and the next update-shaped mutation mints normally.
func runWispArrivalBeginsHistory(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture, how string, arrive func(context.Context, string) error) {
	t.Helper()
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-" + how
	if err := fixture.CreateWisp(ctx, id); err != nil {
		t.Fatalf("creating wisp %s: %v", id, err)
	}
	if err := arrive(ctx, id); err != nil {
		t.Fatalf("moving wisp %s onto the issues plane (%s): %v", id, how, err)
	}

	gen, err := fixture.ParticipationGeneration(ctx, id)
	if err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	}
	if gen == nil {
		t.Errorf("participation_generation for %s is NULL after the wisp reached the issues plane (%s) with "+
			"the flag on, want a stamped value: the issues row is new, so the arrival is create-shaped "+
			"(design §16.2b), and a NULL here leaves the record fenced as legacy for good", id, how)
	}
	arrivedCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if arrivedCount != 1 {
		t.Errorf("version row count for %s = %d after the wisp reached the issues plane (%s) with the flag "+
			"on, want 1: a wisp is never versioned (FR-8), so the arrival mints the record's first version",
			id, arrivedCount, how)
	}

	if err := fixture.MutateExisting(ctx, id); err != nil {
		t.Fatalf("mutating existing %s: %v", id, err)
	}
	afterCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if afterCount != arrivedCount+1 {
		t.Errorf("version row count for %s went from %d to %d across an update-shaped mutation after the "+
			"wisp reached the issues plane (%s), want exactly +1: a record that arrived with the flag on is "+
			"not legacy, so the fence must let its updates mint", id, arrivedCount, afterCount, how)
	}
}

// RunParticipationGenerationDemotedLegacyRowStaysLegacyAcrossPromotion pins
// R2.3 across a plane round trip: a legacy row (created with the flag off)
// that is made a wisp with the flag on and then promoted back onto the
// issues plane is still legacy. Its new issues row carries the participation
// it had before the demotion, so the promotion and every later update are
// update-shaped and fenced. Read off the new row alone, the arrival looks like
// a wisp's (RunParticipationGenerationWispPromotionBeginsHistory), mints
// create-shaped, and starts versioning a legacy row through two ordinary
// writes.
func RunParticipationGenerationDemotedLegacyRowStaysLegacyAcrossPromotion(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	runDemotedLegacyRowStaysLegacy(t, ctx, fixture, "legacy-promote", fixture.PromoteWisp)
}

// RunParticipationGenerationDemotedLegacyRowStaysLegacyAcrossPersistenceMove
// is RunParticipationGenerationDemotedLegacyRowStaysLegacyAcrossPromotion for
// a move back out through a persistence-mode update (bd update --persistent),
// which mints at a different site than promotion on every leg (see
// MovePersistent).
func RunParticipationGenerationDemotedLegacyRowStaysLegacyAcrossPersistenceMove(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	runDemotedLegacyRowStaysLegacy(t, ctx, fixture, "legacy-move-persistent", fixture.MovePersistent)
}

// runDemotedLegacyRowStaysLegacy is the body the two legacy round-trip cases
// share: a row created with the flag off, demoted with it on and brought back
// through arrive, ends with participation_generation NULL, no version rows and
// current_revision unchanged, and so does the update after it.
func runDemotedLegacyRowStaysLegacy(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture, how string, arrive func(context.Context, string) error) {
	t.Helper()
	if err := fixture.SetFlag(ctx, false); err != nil {
		t.Fatalf("disabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-demoted-" + how
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("creating legacy %s with the flag off: %v", id, err)
	}
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	if gen, err := fixture.ParticipationGeneration(ctx, id); err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	} else if gen != nil {
		t.Fatalf("participation_generation for %s = %d before the round trip, want NULL — this case "+
			"cannot test that a legacy row stays legacy against a row that was never legacy", id, *gen)
	}
	beforeRevision, err := fixture.CurrentRevision(ctx, id)
	if err != nil {
		t.Fatalf("reading current_revision for %s: %v", id, err)
	}

	if err := fixture.MoveEphemeral(ctx, id); err != nil {
		t.Fatalf("demoting %s to the wisps table: %v", id, err)
	}
	// The precondition, checked rather than assumed: a demotion that left the
	// row on the issues plane would make the arrival below a same-plane write,
	// and the legacy result would say nothing about a round trip.
	if inWisps, err := fixture.InWispsTable(ctx, id); err != nil {
		t.Fatalf("checking whether the wisps table holds %s: %v", id, err)
	} else if !inWisps {
		t.Fatalf("%s is not in the wisps table after its demotion — this case pins a round trip through "+
			"the wisps table, and the row never made it", id)
	}
	if err := arrive(ctx, id); err != nil {
		t.Fatalf("moving %s back onto the issues plane (%s): %v", id, how, err)
	}

	assertStillLegacy := func(after string) {
		t.Helper()
		if gen, err := fixture.ParticipationGeneration(ctx, id); err != nil {
			t.Fatalf("reading participation_generation for %s: %v", id, err)
		} else if gen != nil {
			t.Errorf("participation_generation for %s = %d after %s with the flag on, want NULL: the row "+
				"was legacy when it was made a wisp, and moving it back is an ordinary write, which must "+
				"not promote a legacy record (R2.3)", id, *gen, after)
		}
		if count, err := fixture.VersionRowCount(ctx, id); err != nil {
			t.Fatalf("counting version rows for %s: %v", id, err)
		} else if count != 0 {
			t.Errorf("version row count for %s = %d after %s with the flag on, want 0: a legacy row's "+
				"mints are fenced (design §16.2b), across a plane round trip too", id, count, after)
		}
		if revision, err := fixture.CurrentRevision(ctx, id); err != nil {
			t.Fatalf("reading current_revision for %s: %v", id, err)
		} else if revision != beforeRevision {
			t.Errorf("current_revision for %s went from %d to %d after %s, want unchanged: a fenced mint "+
				"runs neither half of the seam's write", id, beforeRevision, revision, after)
		}
	}
	assertStillLegacy("the round trip (" + how + ")")

	if err := fixture.MutateExisting(ctx, id); err != nil {
		t.Fatalf("mutating existing %s: %v", id, err)
	}
	assertStillLegacy("an update-shaped mutation after the round trip")
}

// RunParticipationGenerationDemotedParticipatingRowKeepsItsHistoryAcrossPromotion
// is the legacy round trip's positive twin, so that an implementation that
// fences every arrival cannot pass it: a row created with the flag on, made a
// wisp and promoted back, keeps the participation_generation it had and its
// history continues. The arrival mints exactly one version (its next, not a
// second first one) and the update after it one more.
func RunParticipationGenerationDemotedParticipatingRowKeepsItsHistoryAcrossPromotion(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	runDemotedParticipatingRowKeepsItsHistory(t, ctx, fixture, "participating-promote", fixture.PromoteWisp)
}

// RunParticipationGenerationDemotedParticipatingRowKeepsItsHistoryAcrossPersistenceMove
// is RunParticipationGenerationDemotedParticipatingRowKeepsItsHistoryAcrossPromotion
// for the move back out through a persistence-mode update.
func RunParticipationGenerationDemotedParticipatingRowKeepsItsHistoryAcrossPersistenceMove(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	runDemotedParticipatingRowKeepsItsHistory(t, ctx, fixture, "participating-move-persistent", fixture.MovePersistent)
}

// runDemotedParticipatingRowKeepsItsHistory is the body the two participating
// round-trip cases share.
func runDemotedParticipatingRowKeepsItsHistory(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture, how string, arrive func(context.Context, string) error) {
	t.Helper()
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-demoted-" + how
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("creating %s with the flag on: %v", id, err)
	}
	before, err := fixture.ParticipationGeneration(ctx, id)
	if err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	}
	if before == nil {
		t.Fatalf("participation_generation for %s is NULL right after a create with the flag on — this case "+
			"needs a participating row to start from", id)
	}
	beforeCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}

	if err := fixture.MoveEphemeral(ctx, id); err != nil {
		t.Fatalf("demoting %s to the wisps table: %v", id, err)
	}
	if inWisps, err := fixture.InWispsTable(ctx, id); err != nil {
		t.Fatalf("checking whether the wisps table holds %s: %v", id, err)
	} else if !inWisps {
		t.Fatalf("%s is not in the wisps table after its demotion — this case pins a round trip through "+
			"the wisps table, and the row never made it", id)
	}
	if count, err := fixture.VersionRowCount(ctx, id); err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	} else if count != beforeCount {
		t.Errorf("version row count for %s went from %d to %d across its demotion, want unchanged: a wisp is "+
			"never versioned (FR-8)", id, beforeCount, count)
	}
	if err := arrive(ctx, id); err != nil {
		t.Fatalf("moving %s back onto the issues plane (%s): %v", id, how, err)
	}

	after, err := fixture.ParticipationGeneration(ctx, id)
	if err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	}
	if after == nil {
		t.Errorf("participation_generation for %s is NULL after the round trip (%s), want %d: the row "+
			"participated when it was made a wisp, and a NULL here fences it as legacy for good", id, how, *before)
	} else if *after != *before {
		t.Errorf("participation_generation for %s = %d after the round trip (%s), want %d: the row keeps "+
			"the participation it carried, it does not declare a new one", id, *after, how, *before)
	}
	arrivedCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if arrivedCount != beforeCount+1 {
		t.Errorf("version row count for %s went from %d to %d across the round trip (%s), want exactly +1: "+
			"the arrival is an accepted change to a participating row, so it mints its next version",
			id, beforeCount, arrivedCount, how)
	}

	if err := fixture.MutateExisting(ctx, id); err != nil {
		t.Fatalf("mutating existing %s: %v", id, err)
	}
	if afterCount, err := fixture.VersionRowCount(ctx, id); err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	} else if afterCount != arrivedCount+1 {
		t.Errorf("version row count for %s went from %d to %d across an update-shaped mutation after the "+
			"round trip (%s), want exactly +1", id, arrivedCount, afterCount, how)
	}
}

// RunParticipationGenerationWispFlagClearMintsNothing pins that the fence
// decides a row's plane by the TABLE that holds it, not by the row's flags.
// bd mol squash clears a wisp root's ephemeral flag in place, so the row
// stays in the wisps table carrying neither Ephemeral nor NoHistory, and a
// plane test read off those flags takes it for an issues-plane row. It is
// not one: a wisps-table row is never versioned (FR-8) and has no issues row
// whose participation_generation the fence could read. So the update must
// succeed and mint nothing. Read off the flags, the fence either aborts the
// whole update looking for an issues row that is not there, or, without the
// fence, mints a stray version for a wisp.
func RunParticipationGenerationWispFlagClearMintsNothing(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	runWispTableRowMintsNothing(t, ctx, fixture, "flag-clear", fixture.CreateWisp, fixture.ClearWispFlag)
}

// RunParticipationGenerationWispRootSquashMintsNothing is
// RunParticipationGenerationWispFlagClearMintsNothing in the shape bd mol
// squash actually runs it: the root's close and its flag clear in one
// transaction, so a mint that fails on the cleared row takes the close down
// with it and the squash cannot complete.
func RunParticipationGenerationWispRootSquashMintsNothing(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	runWispTableRowMintsNothing(t, ctx, fixture, "squash", fixture.CreateWisp, fixture.SquashWispRoot)
}

// RunParticipationGenerationUnflaggedWispCreateMintsNothing pins the
// create-shaped half of the same rule: a row CREATED in the wisps table
// without either flag (import's "wisp_plane" marker) reaches the
// create-shaped mint, and must come out of it with no error and no version,
// exactly as a flagged wisp does.
func RunParticipationGenerationUnflaggedWispCreateMintsNothing(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	runWispTableRowMintsNothing(t, ctx, fixture, "unflagged-create", fixture.CreateUnflaggedWisp)
}

// runWispTableRowMintsNothing is the body the three wisps-table cases share:
// with the flag on, every step succeeds on id, the wisps table still holds id
// afterwards, and id ends with no issue_versions row.
func runWispTableRowMintsNothing(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture, how string, steps ...func(context.Context, string) error) {
	t.Helper()
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-" + how
	for i, step := range steps {
		if err := step(ctx, id); err != nil {
			t.Fatalf("step %d of %s on wisps-table row %s with the flag on: %v — a row the wisps table "+
				"holds is never versioned (FR-8), so its mint must be a no-op, not an error that aborts "+
				"the mutation", i+1, how, id, err)
		}
	}
	// The precondition, checked after the steps rather than assumed: a step
	// that moved the row to the issues table would leave a NULL
	// participation_generation there, the update-shaped mint would be fenced
	// as legacy, and the zero count below would hold without saying anything
	// about a wisps-table row.
	if inWisps, err := fixture.InWispsTable(ctx, id); err != nil {
		t.Fatalf("checking whether the wisps table holds %s: %v", id, err)
	} else if !inWisps {
		t.Fatalf("%s is not in the wisps table after %s — this case pins that a wisps-table row mints "+
			"nothing, and cannot say so about a row a step moved out of that table", id, how)
	}
	count, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if count != 0 {
		t.Errorf("version row count for wisps-table row %s = %d after %s with the flag on, want 0: a "+
			"wisp is never versioned (FR-8), whatever its flags say", id, count, how)
	}
}

// RunParticipationGenerationNoHistoryIssueCreateDeclaresParticipation pins
// that a create declares participation for every ISSUES-table row it makes
// with the flag on, a row carrying NoHistory included. Import keeps a
// promoted no-history wisp's durable row on the issues plane with its stray
// no_history=true (bd-r9uce). While the row says no-history it is not
// versioned, so the create mints nothing. But it was created with history
// on, so it is not legacy: once bd update --persistent repairs the flag in
// place (a same-plane normalize, update-shaped by design), the repair and
// every update after it must mint. Left NULL by the create, the column reads
// as legacy to the fence, and the row is never versioned at all.
func RunParticipationGenerationNoHistoryIssueCreateDeclaresParticipation(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-no-history-create"
	if err := fixture.CreateNoHistoryIssue(ctx, id); err != nil {
		t.Fatalf("creating no-history issues-plane row %s: %v", id, err)
	}
	gen, err := fixture.ParticipationGeneration(ctx, id)
	if err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	}
	if gen == nil {
		t.Errorf("participation_generation for %s is NULL after a create with the flag on, want a stamped "+
			"value: the row is new on the issues plane, so the create declares participation (design "+
			"§16.2b) even though its NoHistory flag keeps it unversioned for now", id)
	}
	if count, err := fixture.VersionRowCount(ctx, id); err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	} else if count != 0 {
		t.Errorf("version row count for %s = %d after a create carrying NoHistory, want 0: a no-history "+
			"row is not versioned while it says so", id, count)
	}

	if err := fixture.MovePersistent(ctx, id); err != nil {
		t.Fatalf("repairing %s to persistent: %v", id, err)
	}
	repairedCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if repairedCount != 1 {
		t.Errorf("version row count for %s = %d after the persistent repair, want 1: the row was created "+
			"with the flag on, so the fence must let the repair's update-shaped mint through", id, repairedCount)
	}

	if err := fixture.MutateExisting(ctx, id); err != nil {
		t.Fatalf("mutating existing %s: %v", id, err)
	}
	afterCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if afterCount != repairedCount+1 {
		t.Errorf("version row count for %s went from %d to %d across an update-shaped mutation after the "+
			"repair, want exactly +1", id, repairedCount, afterCount)
	}
}

// RunParticipationGenerationLegacyNoHistoryIssueStaysLegacy is
// RunParticipationGenerationNoHistoryIssueCreateDeclaresParticipation's
// negative twin, and pins R2.3: an ordinary write never promotes a legacy
// record. The same no-history issues-table row, created while the flag was
// OFF, is legacy. Only a create-shaped call stamps a flagged row, so once the
// flag is on, an update-shaped mutation must leave participation_generation
// NULL and mint nothing. So must the bd update --persistent repair that then
// clears the flag in place: that repair is update-shaped too (a same-plane
// normalize), so the row it leaves is an ordinary legacy row, still fenced.
func RunParticipationGenerationLegacyNoHistoryIssueStaysLegacy(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	if err := fixture.SetFlag(ctx, false); err != nil {
		t.Fatalf("disabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-no-history-legacy"
	if err := fixture.CreateNoHistoryIssue(ctx, id); err != nil {
		t.Fatalf("creating no-history issues-plane row %s with the flag off: %v", id, err)
	}
	if gen, err := fixture.ParticipationGeneration(ctx, id); err != nil {
		t.Fatalf("reading participation_generation for %s: %v", id, err)
	} else if gen != nil {
		t.Fatalf("participation_generation for %s = %d before the flag was ever turned on, want NULL — "+
			"this case cannot test that a legacy no-history row stays legacy against a row that was never "+
			"legacy", id, *gen)
	}

	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	beforeRevision, err := fixture.CurrentRevision(ctx, id)
	if err != nil {
		t.Fatalf("reading current_revision for %s: %v", id, err)
	}

	assertStillLegacy := func(after string) {
		t.Helper()
		if gen, err := fixture.ParticipationGeneration(ctx, id); err != nil {
			t.Fatalf("reading participation_generation for %s: %v", id, err)
		} else if gen != nil {
			t.Errorf("participation_generation for %s = %d after %s with the flag on, want NULL: the row "+
				"was created with the flag off, and an update-shaped write must not promote a legacy "+
				"record (R2.3), whatever its no-history flag says", id, *gen, after)
		}
		if count, err := fixture.VersionRowCount(ctx, id); err != nil {
			t.Fatalf("counting version rows for %s: %v", id, err)
		} else if count != 0 {
			t.Errorf("version row count for %s = %d after %s with the flag on, want 0: a legacy row's "+
				"update-shaped mint is fenced (design §16.2b)", id, count, after)
		}
		if revision, err := fixture.CurrentRevision(ctx, id); err != nil {
			t.Fatalf("reading current_revision for %s: %v", id, err)
		} else if revision != beforeRevision {
			t.Errorf("current_revision for %s went from %d to %d after %s, want unchanged: a fenced "+
				"mint runs neither half of the seam's write", id, beforeRevision, revision, after)
		}
	}

	if err := fixture.MutateExisting(ctx, id); err != nil {
		t.Fatalf("mutating existing %s: %v", id, err)
	}
	assertStillLegacy("an update-shaped mutation")

	if err := fixture.MovePersistent(ctx, id); err != nil {
		t.Fatalf("repairing %s to persistent: %v", id, err)
	}
	assertStillLegacy("the persistent repair")
}

// RunParticipationGenerationFenceIgnoresSchemaSkewHatch pins design §16.2(c):
// BD_IGNORE_SCHEMA_SKEW=1 downgrades CheckForwardDrift's refusal to a warning
// (internal/storage/schema/schema.go), but it must have no effect on this
// fence — the fence keys on the participation_generation column's real data,
// not on the migration cursor the hatch is about, so a write admitted through
// that hatch is still, correctly, skipped when the record is legacy.
func RunParticipationGenerationFenceIgnoresSchemaSkewHatch(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")

	if err := fixture.SetFlag(ctx, false); err != nil {
		t.Fatalf("disabling versioned history: %v", err)
	}
	id := fixture.IssuePrefix + "-schema-skew-hatch"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	beforeCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}

	if err := fixture.MutateExisting(ctx, id); err != nil {
		t.Fatalf("mutating existing %s: %v", id, err)
	}

	afterCount, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if afterCount != beforeCount {
		t.Errorf("version row count for %s went from %d to %d with BD_IGNORE_SCHEMA_SKEW=1 set, want "+
			"unchanged: the schema-skew hatch must not bypass the participation_generation fence — they "+
			"key on different planes (migration cursor vs. real column data) — so a write admitted "+
			"through the hatch against a legacy record must still be skipped", id, beforeCount, afterCount)
	}
}

// RunParticipationGenerationReadReportsExactlyTheParticipatingRows pins the
// read the pre-enable scan narrows its offenders with
// (issueops.ParticipatingIssueIDsInTx): asked about a mix of rows, it reports
// exactly the ones whose issues-table participation_generation is set (the
// column the write fence reads; with FindUnversionable's IsWisp filter, the rows
// an update-shaped mint versions), and it agrees with the column on
// every issues-table row it is asked about:
//   - a row created with the flag on, and one that kept its participation
//     across a round trip through the wisps table, are reported;
//   - a legacy row, one that stayed legacy across the same round trip, a wisp,
//     a row demoted to the wisps table (it has no issues row now), an id that
//     names no row and an empty id are not.
//
// It answers whatever the flag is, because the switch asks before it turns
// history on, and it reads nothing for no ids. A read that reported a legacy
// row would let a value the write fence never looks at refuse the switch; one
// that missed a participating row would clear the switch over a row the mint
// then refuses every write to.
func RunParticipationGenerationReadReportsExactlyTheParticipatingRows(t *testing.T, ctx context.Context, fixture ParticipationGenerationFixture) {
	t.Helper()
	prefix := fixture.IssuePrefix + "-pread"
	legacy, legacyRoundTrip := prefix+"-legacy", prefix+"-legacy-rt"
	participating, participatingRoundTrip := prefix+"-part", prefix+"-part-rt"
	wisp, demoted, missing := prefix+"-wisp", prefix+"-demoted", prefix+"-missing"

	if err := fixture.SetFlag(ctx, false); err != nil {
		t.Fatalf("disabling versioned history: %v", err)
	}
	for _, id := range []string{legacy, legacyRoundTrip} {
		if err := fixture.Mutate(ctx, id); err != nil {
			t.Fatalf("creating legacy %s with the flag off: %v", id, err)
		}
	}
	if err := fixture.SetFlag(ctx, true); err != nil {
		t.Fatalf("enabling versioned history: %v", err)
	}
	for _, id := range []string{participating, participatingRoundTrip, demoted} {
		if err := fixture.Mutate(ctx, id); err != nil {
			t.Fatalf("creating %s with the flag on: %v", id, err)
		}
	}
	if err := fixture.CreateWisp(ctx, wisp); err != nil {
		t.Fatalf("creating wisp %s: %v", wisp, err)
	}
	for _, id := range []string{legacyRoundTrip, participatingRoundTrip} {
		if err := fixture.MoveEphemeral(ctx, id); err != nil {
			t.Fatalf("demoting %s to the wisps table: %v", id, err)
		}
		if err := fixture.PromoteWisp(ctx, id); err != nil {
			t.Fatalf("promoting %s back onto the issues plane: %v", id, err)
		}
	}
	if err := fixture.MoveEphemeral(ctx, demoted); err != nil {
		t.Fatalf("demoting %s to the wisps table: %v", demoted, err)
	}
	if inWisps, err := fixture.InWispsTable(ctx, demoted); err != nil {
		t.Fatalf("checking whether the wisps table holds %s: %v", demoted, err)
	} else if !inWisps {
		t.Fatalf("%s is not in the wisps table after its demotion, so it cannot stand for a demoted row", demoted)
	}

	asked := []string{legacy, legacyRoundTrip, participating, participatingRoundTrip, wisp, demoted, missing, "", participating}
	want := []string{participating, participatingRoundTrip}
	for _, flag := range []bool{true, false} {
		if err := fixture.SetFlag(ctx, flag); err != nil {
			t.Fatalf("setting versioned history to %v: %v", flag, err)
		}
		got, err := fixture.ParticipatingIssueIDs(ctx, asked)
		if err != nil {
			t.Fatalf("reading participation with the flag %v: %v", flag, err)
		}
		if ids := sortedSetIDs(got); !reflect.DeepEqual(ids, want) {
			t.Errorf("with the flag %v the participation read reported %v, want exactly the participating rows %v", flag, ids, want)
		}
		for _, id := range []string{legacy, legacyRoundTrip, participating, participatingRoundTrip} {
			gen, err := fixture.ParticipationGeneration(ctx, id)
			if err != nil {
				t.Fatalf("reading participation_generation for %s: %v", id, err)
			}
			if _, reported := got[id]; reported != (gen != nil) {
				t.Errorf("the participation read says %s participates = %v, but its participation_generation is %v", id, reported, gen)
			}
		}
	}
	if got, err := fixture.ParticipatingIssueIDs(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("the participation read for no ids = %v, %v; want an empty set and no error", got, err)
	}
}

func sortedSetIDs(set map[string]struct{}) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
