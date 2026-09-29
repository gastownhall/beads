package conformance

import (
	"context"
	"testing"
)

// This file holds the contract Phase 2's dual-write history mechanism must
// satisfy: FR-1, FR-2, FR-3, FR-5 and FR-7 of be-hs42e.3 (gastownhall/beads#6135)
// directly, plus FR-6 per leg (see below). FR-4 and FR-8 are this phase's
// requirements too but are not testable through this fixture's five closures
// — see the exclusion list below for why. FR-9 is deliberately NOT this
// file's job: it is the differential test harness task 4 builds separately
// (flag-off-vs-recorded-baseline plus flag-on-perturbs-nothing-but-
// current_revision), not a property a per-mutation fixture case can express.
// This is the phase that starts populating
// issue_versions/current_revision/store_epoch behind
// storage.VersionedHistoryConfigurer while every read path still answers from
// the durable issues/wisps rows exactly as before. Nothing here asserts that a
// dual-written value is ever READ back through a query surface — that CAS
// enforcement and read-path switch is Phase 3/4 (#6136), unbuilt at this
// phase — only that the write side mints what it promises, and only when it
// promises to.
//
// WHY THIS IS A DEDICATED FIXTURE RATHER THAN AN EXTENSION OF AN EXISTING ONE.
// An earlier design iteration proposed folding these cases into the
// pre-existing RetentionFixture/CrossRecordInvariantFixture pair. That
// approach was withdrawn (be-q0w1q) after review surfaced a version-row
// identity gap those fixtures' cases would have exercised before it was
// resolved: (issue_id, revision) was the only key issue_versions had, which
// cannot express a third write racing the same revision the way a
// content-addressed or generated identity can. be-hs42e.3 design §15
// resolves the identity question with a dedicated version_id (migration
// 0068, landing after this contract exists — see the task-1 half of this
// bead), and this contract is written against §15's resolved shape, not the
// one the withdrawn approach would have extended.
//
// THE FIXTURE'S FIVE CLOSURES ARE THE OBSERVABLE SURFACE, NOT THE SCHEMA. A
// case here never reads issue_versions or store_epoch directly — that would
// make the contract a second copy of one leg's SQL rather than a promise
// every leg keeps in its own idiom (raw SQL against the dolt leg's own
// *sql.DB, an exported accessor on the embedded store, RawSQLUseCase through
// a unit of work). Mutate and MutateAsNoOp drive the same front door every
// other conformance fixture in this package drives — CreateIssue and
// UpdateIssue, or their unit-of-work equivalents — never the version table:
// emission lives at the RecordVersionInTx seam beneath all of them, so a
// closure that inserted a row directly would be asserting that this test can
// write a row, not that the engine does.
//
// WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN, and the reason, so a later
// reader does not mistake the gap for an oversight:
//
//   - THE EXACT BYTES OF durable_state. Design §15's R5.1 artifact is a
//     verbatim marshal of the mutated issue, and its shape is pinned where it
//     is produced, not here: a byte-for-byte assertion in a cross-leg
//     contract would break every time a field is added to types.Issue for
//     reasons that have nothing to do with dual-write correctness. What IS
//     pinned (RunDualWriteAttributionIsRecordedWithTheMutation) is that a row
//     exists and its attribution columns are populated the moment the
//     mutation that produced it returns — the externally observable proxy
//     for "atomic with the mutation" a black-box fixture can actually check.
//   - store_epoch AND issue_versions.epoch (FR-6, "stamped, never bumped").
//     None of this fixture's five closures can see either: CurrentRevision
//     reads issues.current_revision, and VersionRowCount and
//     LatestVersionAttribution read issue_versions but never its epoch
//     column. Adding a sixth closure just to reach one column — one two of
//     three legs would have to invent white-box access for — would make the
//     shared surface bespoke to a single requirement. FR-6 is instead pinned
//     per leg, where a leg already has (or can cheaply grow) the access: see
//     the dolt leg's TestDualWriteStampsTheCurrentStoreEpochOnEachVersionRow.
//   - PARTICIPATION-GENERATION GATING (design §16.2b). Whether an
//     update-shaped mutation on a legacy (participation_generation IS NULL)
//     row is correctly skipped is Phase 2 behavior this bead also owns, but
//     it depends on migration 0068's participation_generation column, which
//     does not exist when this contract is first written (0068 lands last in
//     this bead's build order, by the mayor's explicit sequencing). It gets
//     its own dedicated cases once 0068 lands, beside this file rather than
//     inside it.
//   - CONCURRENT WRITERS. Every case here is one writer, one issue, one
//     mutation at a time — the same restriction journal_contract.go states
//     for the same reason: a single-threaded case cannot exercise what only
//     concurrency can break, and faking it with sleeps buys a flaky suite
//     instead of a guarantee.
//   - FR-4 ("attribution is metadata: never compared when deciding whether
//     two versions' durable state differs, never included in any future
//     digest/equality check"). This is a constraint on code that does not
//     exist in Phase 2 — no digest or equality check over durable_state is
//     built by this phase — so there is nothing yet for a case to run
//     against. FR-4 has no dedicated case here for that reason, not because
//     it was missed.
//   - FR-8 (`wisps.current_revision`/`wisps.participation_generation` are
//     never read or written by this phase). None of this fixture's five
//     closures can express it: Mutate's front door is CreateIssue against
//     the issues table, so this fixture has no wisp-routed id to check in
//     the first place. FR-8 is a routing exclusion, not a property of a
//     mutated issue's own version history, and belongs beside the explicit
//     test design §9 item 5 already calls for (task 1's responsibility, not
//     this contract's).
//
// SEEDING DISCIPLINE: EVERY CASE CREATES ITS OWN ISSUE. Unlike the journal —
// append-only and workspace-global, so every case rebaselines off the live
// head — a version history is PER ISSUE, and Mutate's front door is create,
// which cannot run twice against the same id. So every case mints its own id
// from fixture.IssuePrefix plus a marker naming the case, and no case may
// assume anything about another id's history.
type DualWriteFixture struct {
	// IssuePrefix namespaces the id each case mints. Ids are global to a
	// workspace, so two cases sharing one would read each other's version
	// history.
	IssuePrefix string
	// Mutate performs one ACCEPTED mutation against a not-yet-existing id:
	// the fixture's chosen front door for "a mutation this phase must
	// version" is CreateIssue (or its unit-of-work equivalent), because
	// create is one of the call sites RecordVersionInTx wires into and the
	// one every leg can drive without first needing a row to exist.
	Mutate func(ctx context.Context, id string) error
	// MutateAsNoOp performs a mutation against an id Mutate already created
	// that existing production code (issueops.DiscardNoopIssueUpdates)
	// recognizes as a no-op before it ever reaches RecordVersionInTx — the
	// fixture's chosen shape is an update that re-sets a field to the value
	// Mutate already gave it. It is a DIFFERENT call than Mutate on purpose:
	// FR-2 is a claim about the engine's no-op skip, and a fixture that used
	// the same closure for both could not tell a real skip from Mutate
	// simply never having run.
	MutateAsNoOp func(ctx context.Context, id string) error
	// CurrentRevision reads issues.current_revision for id.
	CurrentRevision func(ctx context.Context, id string) (int64, error)
	// VersionRowCount reads how many issue_versions rows exist for id.
	VersionRowCount func(ctx context.Context, id string) (int, error)
	// LatestVersionAttribution reads change_actor, change_agent and
	// change_message off the highest-revision issue_versions row for id.
	// Columns the schema allows NULL come back as "": this fixture answers
	// "what did the mutation record", and a leg that recorded nothing for a
	// column answers that question with an empty string, not a sentinel a
	// case would have to special-case around.
	LatestVersionAttribution func(ctx context.Context, id string) (actor, agent, message string, err error)
}

// RunDualWriteMintsOneVersionRowPerAcceptedMutation pins FR-1: an accepted
// mutation mints EXACTLY one issue_versions row, not zero and not more than
// one.
//
// Zero would mean the phase built the flag and never wired the seam behind
// it — every other case in this file would still read a functioning store,
// because nothing about ordinary issue mutation depends on versioning
// existing. More than one is the other silent failure this phase has to
// avoid: a seam invoked twice for one caller-visible mutation (once from the
// front door's own transaction and once from a retry or a decorator wrapping
// it a second time) would double the history of every issue in the store
// without a single ordinary read ever noticing, since nothing reads
// issue_versions yet.
func RunDualWriteMintsOneVersionRowPerAcceptedMutation(t *testing.T, ctx context.Context, fixture DualWriteFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-mints-one"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	count, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if count != 1 {
		t.Errorf("version row count for %s = %d, want exactly 1: one accepted mutation mints one row, "+
			"not zero (the seam never fired) and not more than one (it fired twice for a single "+
			"caller-visible mutation)", id, count)
	}
}

// RunDualWriteNoOpMutationMintsNoRow pins FR-2: a mutation
// issueops.DiscardNoopIssueUpdates already discards before it reaches any
// write mints NO version row.
//
// This is the inheritance the task-1 design calls for explicitly:
// RecordVersionInTx sits inside the same already-short-circuited function
// bodies as RecordEventInTx, so a no-op never reaches it, and this case is
// what would catch a future refactor that moved the call ABOVE that
// short-circuit — versioning an update that changed nothing, which would
// make current_revision (and FR-5's identity between it and the row count)
// diverge from "the number of times this issue's durable state actually
// changed" for every issue anyone re-saves without editing.
func RunDualWriteNoOpMutationMintsNoRow(t *testing.T, ctx context.Context, fixture DualWriteFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-noop-mints-none"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	before, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s before the no-op: %v", id, err)
	}

	if err := fixture.MutateAsNoOp(ctx, id); err != nil {
		t.Fatalf("no-op mutating %s: %v", id, err)
	}
	after, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s after the no-op: %v", id, err)
	}
	if after != before {
		t.Errorf("version row count for %s went from %d to %d across a no-op mutation, want unchanged: "+
			"a mutation the engine already discards as a no-op must never reach RecordVersionInTx",
			id, before, after)
	}
}

// RunDualWriteAttributionIsRecordedWithTheMutation pins FR-3 and FR-4: the
// version row minted for an accepted mutation carries that mutation's
// attribution, visible the instant the mutation's own call returns.
//
// "Visible the instant the call returns" is the load-bearing half: this
// contract cannot see two writes land in the same database transaction, but
// it CAN see whether a caller has to do anything extra — flush, commit
// separately, poll — before the attribution it just supplied shows up beside
// the row it mutated. A dual-write seam that recorded the version
// asynchronously, or in a transaction of its own committed after the
// caller's, would still pass every other case in this file and fail this one.
func RunDualWriteAttributionIsRecordedWithTheMutation(t *testing.T, ctx context.Context, fixture DualWriteFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-attribution"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	actor, _, _, err := fixture.LatestVersionAttribution(ctx, id)
	if err != nil {
		t.Fatalf("reading attribution for %s immediately after the mutation that must have minted it: %v", id, err)
	}
	if actor == "" {
		t.Errorf("change_actor for %s is empty immediately after an accepted mutation: FR-3/FR-4 require "+
			"the version row to carry the mutation's attribution, and an actor is the one attribution "+
			"input every leg's Mutate closure supplies", id)
	}
}

// RunDualWriteCurrentRevisionMatchesTheNewVersionRow pins FR-5: after any
// accepted mutation, issues.current_revision agrees with how many version
// rows the issue actually has.
//
// This restates the bead's task-1 description — bump current_revision by a
// plain +1 on the row already carrying the mutation, in the same transaction
// as the version row it accompanies — as something a black-box fixture can
// check without seeing either write: current_revision IS a count of the
// version rows RecordVersionInTx has minted for this issue, never ahead of
// it and never behind. Divergence in either direction is a real bug this
// phase must not ship: current_revision racing ahead of the row count would
// tell Phase 3/4's future CAS check to expect a revision that was never
// durably recorded, and falling behind would let a second writer believe it
// observed the latest version when a row past it already exists.
func RunDualWriteCurrentRevisionMatchesTheNewVersionRow(t *testing.T, ctx context.Context, fixture DualWriteFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-revision-matches"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	revision, err := fixture.CurrentRevision(ctx, id)
	if err != nil {
		t.Fatalf("reading current_revision for %s: %v", id, err)
	}
	count, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if revision != int64(count) {
		t.Errorf("current_revision for %s = %d but it has %d version row(s): the two must always agree — "+
			"current_revision is nothing but a count of the rows RecordVersionInTx has minted for this "+
			"issue", id, revision, count)
	}
}

// RunDualWriteFlagOffProducesNoVersionRows pins FR-7: with
// storage.VersionedHistoryConfigurer left off, an otherwise-identical
// mutation mints no version row and leaves current_revision at its untouched
// schema default.
//
// This is the phase's core safety property, restated from the write side:
// every store this bead ships to already runs with the flag off, so a
// dual-write engine that fired regardless of the switch would start writing
// issue_versions rows nobody asked for, in every workspace, the moment this
// PR merges. The fixture this case is called against must be constructed
// with the flag OFF from the start — see each leg's flag-off constructor —
// so the case cannot pass by accident against a fixture that happens to
// share state with a flag-on one.
func RunDualWriteFlagOffProducesNoVersionRows(t *testing.T, ctx context.Context, fixture DualWriteFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-flag-off"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s with the flag off: %v", id, err)
	}
	count, err := fixture.VersionRowCount(ctx, id)
	if err != nil {
		t.Fatalf("counting version rows for %s: %v", id, err)
	}
	if count != 0 {
		t.Errorf("version row count for %s = %d with storage.VersionedHistoryConfigurer OFF, want 0: a "+
			"disabled flag must produce byte-identical writes to the pre-Phase-2 engine, and a row minted "+
			"anyway means every existing workspace starts accumulating history nobody enabled", id, count)
	}
	revision, err := fixture.CurrentRevision(ctx, id)
	if err != nil {
		t.Fatalf("reading current_revision for %s: %v", id, err)
	}
	if revision != 1 {
		t.Errorf("current_revision for %s = %d with the flag off, want 1 (the column's own schema "+
			"default, untouched): a flag-off store must never execute the bump half of RecordVersionInTx "+
			"either", id, revision)
	}
}

// RunDualWriteNoOpMutationLeavesThePriorVersionRowUnperturbed strengthens
// FR-2 from the one angle RunDualWriteNoOpMutationMintsNoRow does not reach:
// that case pins that a no-op mints no NEW row; this pins that a no-op does
// not silently rewrite the row already there.
//
// A dual-write seam that mistakenly ran an UPDATE instead of an
// insert-or-skip — reusing the prior row's primary key because the no-op
// path recomputed "the current version" instead of being skipped before it
// got there — would pass RunDualWriteNoOpMutationMintsNoRow outright: the row
// count never changes when an UPDATE overwrites in place. Comparing the
// attribution byte-for-byte across the no-op is what catches that failure
// mode instead.
func RunDualWriteNoOpMutationLeavesThePriorVersionRowUnperturbed(t *testing.T, ctx context.Context, fixture DualWriteFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-noop-unperturbed"
	if err := fixture.Mutate(ctx, id); err != nil {
		t.Fatalf("mutating %s: %v", id, err)
	}
	beforeActor, beforeAgent, beforeMessage, err := fixture.LatestVersionAttribution(ctx, id)
	if err != nil {
		t.Fatalf("reading attribution for %s before the no-op: %v", id, err)
	}

	if err := fixture.MutateAsNoOp(ctx, id); err != nil {
		t.Fatalf("no-op mutating %s: %v", id, err)
	}
	afterActor, afterAgent, afterMessage, err := fixture.LatestVersionAttribution(ctx, id)
	if err != nil {
		t.Fatalf("reading attribution for %s after the no-op: %v", id, err)
	}
	if beforeActor != afterActor || beforeAgent != afterAgent || beforeMessage != afterMessage {
		t.Errorf("attribution for %s changed across a no-op mutation, (%q,%q,%q) -> (%q,%q,%q): a mutation "+
			"that mints no new row must also leave the existing one exactly as the accepted mutation "+
			"before it left it, not silently rewritten in place",
			id, beforeActor, beforeAgent, beforeMessage, afterActor, afterAgent, afterMessage)
	}
}
