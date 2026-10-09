package conformance

import (
	"context"
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
//   - wisps.participation_generation. FR-8 (carried by dualwrite_history_contract.go's
//     exclusion list) applies here too: this fixture's Mutate front door is
//     CreateIssue against the issues table, the wisp-arrival cases read only
//     the issues row a wisp becomes, and no phase reads or writes the wisps
//     column regardless.
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
	// plane without being created there. A wisp is never versioned (FR-8)
	// and never carries a participation declaration, in either flag state.
	CreateWisp func(ctx context.Context, id string) error
	// PromoteWisp promotes the wisp id onto the issues plane
	// (PromoteFromEphemeral, or the unit-of-work leg's PromoteWisp).
	PromoteWisp func(ctx context.Context, id string) error
	// MovePersistent moves the wisp id onto the issues plane through a
	// persistence-mode update (bd update --persistent). It is its own
	// closure rather than a second spelling of PromoteWisp because it mints at
	// a different site on every leg: the guarded update's one end-of-update
	// mint on dolt and embeddeddolt, MoveIssuePersistenceInTx's own on uow.
	MovePersistent func(ctx context.Context, id string) error
	// CurrentRevision reads issues.current_revision for id.
	CurrentRevision func(ctx context.Context, id string) (int64, error)
	// VersionRowCount reads how many issue_versions rows exist for id.
	VersionRowCount func(ctx context.Context, id string) (int, error)
	// ParticipationGeneration reads issues.participation_generation for id,
	// returning nil for SQL NULL.
	ParticipationGeneration func(ctx context.Context, id string) (*int64, error)
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

// RunParticipationGenerationWispPromotionBeginsHistory pins that promoting a
// wisp onto the issues plane is CREATE-shaped in design §16.2b's sense. (This
// is wisp promotion, PromoteFromEphemeral: not R2.3's promotion of a legacy
// record, which is what ProceedsForPromotedRecord's name refers to.) The
// issues row a promotion inserts is brand new on that plane, and the wisp
// behind it was never versioned and never declared participation (FR-8), so
// there is no legacy state to preserve: with the flag on, the promotion stamps
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
