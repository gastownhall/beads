package conformance

import (
	"context"
	"errors"
	"math"
	"testing"

	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/journalops"
)

// This file holds the contract every implementation of journalops.Journal must
// satisfy. Each case asserts what journalops/journal.go PROMISES rather than
// what any one backend happens to do today.
//
// THE VOTE COUNT. Three wirings — the server-backed store, the embedded store
// and the unit-of-work provider — and ONE body between them: all three bottom
// out in issueops.ReadEventsPageInTx, the two stores through a five-line
// accessor around their own transaction helper and the unit of work through
// domain.EventsJournalUseCase. So this is one reading plus an engine check, the
// arrangement issueops.TreeWalker was the first to have, and the cases are
// written for it: they assert the typed error's FIELDS and the page's members
// rather than message text, because what a per-leg failure would actually BE
// here is a wrapper that loses a transaction, drops the head, or breaks
// errors.As across the alias.
//
// THAT LAST ONE IS WHY THE TRUNCATION CASE MATTERS MORE THAN IT LOOKS.
// storage.EventsJournalCursor, storage.EventsJournalRow, storage.EventsJournalPage
// and storage.EventsJournalTruncatedError are ALIASES of the journalops names,
// so every leg still returns the type it always returned. These cases hold the
// role's own spelling — journalops.TruncatedError — against three
// implementations that never name it, which is the alias identity checked at
// runtime rather than argued from the language spec.
//
// WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN, and the reason, so a later
// reader does not mistake the gap for an oversight:
//
//   - THE HEAD AND THE ROWS COMING FROM ONE INSTANT. It is the promise the Page
//     type exists for, and it is not black-box observable here. That was
//     MEASURED rather than assumed: reordering ReadEventsPageInTx to read the
//     head BEFORE the rows — the exact regression the promise forbids — leaves
//     all six cases green, because a single-threaded case has nothing to commit
//     in the gap it opens. A concurrent case would be flaky at three engines and
//     would buy a suite people re-run rather than a guarantee.
//
//     What IS observable is the OTHER way a head goes wrong, and both are
//     pinned: a head derived from the page (RunJournalLimitCapsRowsNotHead) and
//     a head derived from the surviving rows instead of the counter
//     (RunJournalHeadSurvivesAFullPrune). The one-instant promise itself is held
//     by the SHAPE of the body — both reads are inside the caller's transaction
//     and there is no two-call composition to regress into without deleting it —
//     and by review. Do not fake it with sleeps.
//   - THE COMMIT-ORDER PROPERTY OF SEQ. That seqs are gapless and ordered by
//     COMMIT rather than by insert is a claim about concurrent writers
//     (issueops.nextEventSeq), and the leg that can actually violate it is the
//     shared SQL server. It is pinned where the concurrency actually lives:
//     internal/storage/domain/db/journal_seq_test.go, whose
//     TestEventsJournal_CommitOrderedGaplessSeq drives two OVERLAPPING
//     transactions on independent connections and whose
//     TestEventsJournal_ConcurrentWritersGaplessNoDup drives a racing batch,
//     plus embeddeddolt/journal_concurrency_test.go for the embedded engine.
//     Not the dolt package's own journal tests, which exercise the same
//     server-mode plumbing but are single-threaded, and not here, where every
//     case is one writer.
//   - RETENTION POLICY. The retain-days and retain-rows floors are the
//     operator's surface (storage.EventsJournalAccessor), deliberately not on
//     this role. The fixture's Prune hook exists to CREATE the truncation the
//     read contract has to answer, and the cases pass both floors disabled; how
//     a floor resolves is pinned beside the prune bodies.
//
// SEEDING DISCIPLINE: EVERY CASE REBASELINES. The journal is append-only and
// workspace-global, the fixture is built once per role, and two of these cases
// PRUNE — one of them to nothing. So no case may assume an empty journal or a
// seq of its own choosing. Each takes the live head first (journalHead), drives
// its own mutations, and asserts about the window above that baseline. A case
// written against absolute seqs would pass in isolation and fail the moment it
// ran second, which is the shape of a contract nobody can reorder.

// JournalFixture supplies adapter-specific access for the journal assertions:
// the role under test, the activation switch its rows depend on, the prune that
// creates a truncation, and the mutation kit that makes records exist.
type JournalFixture struct {
	// IssuePrefix namespaces the ids each case seeds. Ids are global to a
	// workspace and every case here shares one, so a case that reused another's
	// id would read another's journal rows.
	IssuePrefix string
	// Journal is the role under test, reached through whatever the backend
	// hands it out by — a type assertion on the store, or the unit-of-work
	// provider's EventsJournalCursor accessor. Never a constructor: the
	// accessor, or the assertion, is what a front door actually holds.
	Journal journalops.Journal
	// SetJournalEnabled turns durable journaling on for the workspace under
	// test (storage.EventsJournalConfigurer). It is OPERATOR surface and
	// deliberately not on the role, which is exactly why the fixture has to
	// carry it: the cases need records to exist, and a workspace that never
	// opted in records nothing while every read still succeeds emptily.
	//
	// Every case calls it with true before it seeds, rather than trusting a
	// wiring to have done so. It is idempotent, it costs nothing, and a leg
	// that forgot would otherwise fail with "no rows" in six places instead of
	// naming the one thing that was wrong.
	SetJournalEnabled func(enabled bool)
	// Prune deletes journal records below before, honoring the retain-days and
	// retain-rows floors (0 disables a floor), and reports how many it removed
	// (storage.EventsJournalAccessor.PruneEventsJournal, or the unit of work's
	// EventsJournalUseCase.Prune).
	//
	// It is here to MANUFACTURE the condition the read contract has to answer,
	// not to be tested. Both truncation cases pass both floors disabled, so the
	// bound is exactly what they ask for.
	Prune func(ctx context.Context, before int64, retainDays, retainRows int) (int64, error)
	// Mutations is one hook per journaled op — see JournalMutations, which
	// states why the kit is exhaustive rather than a sample.
	Mutations JournalMutations
}

// JournalMutations is the mutation kit: one hook per op the engine journals,
// each driving the backend's own front-door verb for it.
//
// IT IS EXHAUSTIVE BY CONSTRUCTION, and that is the point of naming the hooks
// as fields rather than accepting a list. The journaled vocabulary is closed
// and declared once (issueops.WireEventOps and issueops.EngineOnlyEventOps),
// RunJournalEveryMutationKindLandsARow checks this kit against it, and a leg
// that supplied a sample would prove the journal records the ops it happens to
// have thought of. An op added to the engine vocabulary without a hook here
// fails that case rather than quietly joining the set nothing drives.
//
// EVERY HOOK DRIVES THE BACKEND'S OWN VERB, never the journal. Emission lives
// at a seam beneath all of these (issueops.RecordEventInTx and its siblings),
// so a hook that inserted a record directly would assert that this test can
// write a row.
type JournalMutations struct {
	// Create creates a durable issue with the given id.
	Create func(ctx context.Context, id string) error
	// Update mutates a scalar field of an existing issue.
	Update func(ctx context.Context, id string) error
	// Close closes an existing open issue.
	Close func(ctx context.Context, id string) error
	// Delete removes an existing issue. Its record carries no issue snapshot —
	// there is no surviving row to take one from.
	Delete func(ctx context.Context, id string) error
	// AddDependency adds one blocking edge from an existing issue to another.
	// The record is the SOURCE's: from is the id the row names.
	AddDependency func(ctx context.Context, from, to string) error
	// RemoveDependency removes that edge again, and its record is the source's
	// for the same reason.
	RemoveDependency func(ctx context.Context, from, to string) error
	// Comment writes one structured comment on an existing issue. Its op is the
	// one the engine journals and the wire never carries; see
	// RunJournalEveryMutationKindLandsARow.
	Comment func(ctx context.Context, id, text string) error
}

// RunJournalPagesAreSeqAscendingAndSinceExclusive pins the two properties a
// resuming consumer's correctness rests on: the checkpoint it hands back is
// EXCLUSIVE, and what it gets back is in seq order.
//
// The exclusivity is asserted twice, and the second one is the load-bearing
// half. A read from the caller's own baseline could start at baseline+1 under
// either boundary if nothing sits exactly at baseline, so the case also reads
// from a seq it has just been SERVED — a row that provably exists — and
// requires that row to be absent from the answer. An inclusive boundary
// replays one record per poll forever, which is silent duplication rather than
// a visible failure.
func RunJournalPagesAreSeqAscendingAndSinceExclusive(t *testing.T, ctx context.Context, fixture JournalFixture) {
	baseline := journalBaseline(t, ctx, fixture)
	ids := journalSeedCreates(t, ctx, fixture, "asc", 3)

	page := journalRead(t, ctx, fixture, baseline, 0)
	if len(page.Rows) != len(ids) {
		t.Fatalf("read %d records above the baseline, want the %d this case created: %+v",
			len(page.Rows), len(ids), journalOpsOf(page.Rows))
	}
	if page.Rows[0].Seq != baseline+1 {
		t.Errorf("first record seq = %d, want %d: the answer begins immediately after the checkpoint",
			page.Rows[0].Seq, baseline+1)
	}
	for i, row := range page.Rows {
		if row.Seq <= baseline {
			t.Errorf("record %d has seq %d, at or below the checkpoint %d: since is EXCLUSIVE",
				i, row.Seq, baseline)
		}
		if i > 0 && row.Seq <= page.Rows[i-1].Seq {
			t.Errorf("record %d has seq %d, not above its predecessor's %d: records are seq-ASCENDING",
				i, row.Seq, page.Rows[i-1].Seq)
		}
		if row.IssueID != ids[i] {
			t.Errorf("record %d names %q, want %q: the order records arrive in is the order the "+
				"mutations committed in", i, row.IssueID, ids[i])
		}
	}

	// Resume from a record the caller has been served. Under an inclusive
	// boundary this answer repeats it.
	resumed := journalRead(t, ctx, fixture, page.Rows[0].Seq, 0)
	if len(resumed.Rows) != len(ids)-1 {
		t.Fatalf("resuming from seq %d returned %d records, want %d",
			page.Rows[0].Seq, len(resumed.Rows), len(ids)-1)
	}
	if resumed.Rows[0].Seq == page.Rows[0].Seq {
		t.Errorf("resuming from seq %d served that same record again: a consumer storing the seq it "+
			"processed and handing it back would replay one record on every poll", page.Rows[0].Seq)
	}
	if resumed.Rows[0].Seq != page.Rows[1].Seq {
		t.Errorf("resuming from seq %d began at %d, want %d: the next record and nothing before it",
			page.Rows[0].Seq, resumed.Rows[0].Seq, page.Rows[1].Seq)
	}
}

// RunJournalHeadArrivesWithItsRowsAndDetectsCaughtUp pins what a poller reads
// to decide between asking again and waiting.
//
// Three facts, and the third is what the whole role is for: the head describes
// the journal's history, it moves with the records, and a checkpoint that has
// reached it is CAUGHT UP — no records, no error, the same head. A checkpoint
// ABOVE the head is caught up too, which is not a curiosity: it is what makes
// the head safe to probe, and every other case in this file relies on it.
func RunJournalHeadArrivesWithItsRowsAndDetectsCaughtUp(t *testing.T, ctx context.Context, fixture JournalFixture) {
	baseline := journalBaseline(t, ctx, fixture)
	journalSeedCreates(t, ctx, fixture, "head", 2)

	page := journalRead(t, ctx, fixture, baseline, 0)
	if len(page.Rows) != 2 {
		t.Fatalf("read %d records, want the 2 this case created: %+v", len(page.Rows), journalOpsOf(page.Rows))
	}
	last := page.Rows[len(page.Rows)-1].Seq
	if page.Head < last {
		t.Fatalf("head = %d, below the last record served (%d): a consumer reads that as being past the "+
			"end of the journal and stops polling", page.Head, last)
	}
	if page.Head != last {
		t.Errorf("head = %d, want %d: nothing else wrote, so the head is the last record", page.Head, last)
	}

	caughtUp := journalRead(t, ctx, fixture, page.Head, 0)
	if len(caughtUp.Rows) != 0 {
		t.Errorf("reading from the head returned %d records, want none", len(caughtUp.Rows))
	}
	if caughtUp.Head != page.Head {
		t.Errorf("head moved from %d to %d with nothing written between the two reads", page.Head, caughtUp.Head)
	}

	// A checkpoint the journal has never reached is caught up, not an error.
	// This is the property journalHead is built on.
	beyond := journalRead(t, ctx, fixture, page.Head+1000, 0)
	if len(beyond.Rows) != 0 || beyond.Head != page.Head {
		t.Errorf("reading from a checkpoint above the head = %d records and head %d, want none and %d",
			len(beyond.Rows), beyond.Head, page.Head)
	}

	// One more mutation, and the head follows it.
	journalSeedCreates(t, ctx, fixture, "head-again", 1)
	advanced := journalRead(t, ctx, fixture, page.Head, 0)
	if len(advanced.Rows) != 1 {
		t.Fatalf("read %d records after one more mutation, want 1", len(advanced.Rows))
	}
	if advanced.Head != advanced.Rows[0].Seq || advanced.Head <= page.Head {
		t.Errorf("head = %d after a record at seq %d (was %d): the head has to move with what it describes",
			advanced.Head, advanced.Rows[0].Seq, page.Head)
	}
}

// RunJournalLimitCapsRowsNotHead pins the difference between the page and the
// journal, which is the whole reason Page carries two members.
//
// A head derived from the records in hand is the plausible implementation and
// the broken one: every bounded read would then report a head equal to its last
// record, a consumer would read "caught up", and it would stall however far
// behind it happened to be. So the case takes a page NARROWER than the backlog
// and requires the head to be ahead of it. It also pins limit 0 as uncapped,
// because a role that quietly imposed a ceiling of its own would make the same
// consumer stall at a boundary nothing documents.
func RunJournalLimitCapsRowsNotHead(t *testing.T, ctx context.Context, fixture JournalFixture) {
	baseline := journalBaseline(t, ctx, fixture)
	ids := journalSeedCreates(t, ctx, fixture, "limit", 4)

	bounded := journalRead(t, ctx, fixture, baseline, 2)
	if len(bounded.Rows) != 2 {
		t.Fatalf("a limit of 2 returned %d records, want 2", len(bounded.Rows))
	}
	lastServed := bounded.Rows[len(bounded.Rows)-1].Seq
	if bounded.Head <= lastServed {
		t.Errorf("a bounded page reported head %d with its last record at %d: the head describes the "+
			"JOURNAL, not the page, and a consumer reads this one as caught up while %d records wait",
			bounded.Head, lastServed, len(ids)-2)
	}
	if bounded.Head != baseline+int64(len(ids)) {
		t.Errorf("bounded head = %d, want %d: the head of everything this case wrote",
			bounded.Head, baseline+int64(len(ids)))
	}

	uncapped := journalRead(t, ctx, fixture, baseline, 0)
	if len(uncapped.Rows) != len(ids) {
		t.Fatalf("a limit of 0 returned %d records, want all %d: 0 means uncapped, and any ceiling on "+
			"this read belongs to a front door rather than to the role", len(uncapped.Rows), len(ids))
	}
	if uncapped.Head != bounded.Head {
		t.Errorf("head = %d uncapped and %d bounded; the limit must not reach it", uncapped.Head, bounded.Head)
	}

	// The bounded page is a PREFIX of the uncapped answer, not a sample of it.
	for i, row := range bounded.Rows {
		if row.Seq != uncapped.Rows[i].Seq {
			t.Errorf("bounded record %d is seq %d, want %d: a limit takes the first n, so paging with "+
				"one loses nothing", i, row.Seq, uncapped.Rows[i].Seq)
		}
	}
}

// RunJournalTruncationIsTypedAndNamesTheWindow pins the failure the journal
// exists to make loud.
//
// A checkpoint below the retained window cannot be answered with records, and
// the two silent alternatives are both data loss: an empty success strands the
// consumer forever, and skipping to the current floor drops every record in
// between without saying so. So the read FAILS, with a typed error naming a
// window the caller can act on — and the case proves the window is actually
// resumable rather than merely printed, by resuming from it.
//
// The type is asserted through journalops.TruncatedError, which no leg names:
// they all return the storage alias. errors.As matching across that is the
// alias identity, checked rather than assumed.
func RunJournalTruncationIsTypedAndNamesTheWindow(t *testing.T, ctx context.Context, fixture JournalFixture) {
	baseline := journalBaseline(t, ctx, fixture)
	journalSeedCreates(t, ctx, fixture, "trunc", 4)
	head := baseline + 4
	// Prune the first two of the four, floors disabled so the bound is exactly
	// what this case asks for. The delete is a prefix, so everything earlier
	// cases wrote goes with them.
	firstRetained := baseline + 3
	journalPrune(t, ctx, fixture, firstRetained)

	trunc := journalRequireTruncated(t, "reading from a pruned checkpoint",
		journalReadErr(ctx, fixture, baseline, 0))
	if trunc.Since != baseline {
		t.Errorf("Since = %d, want the caller's own checkpoint %d: the nearest hole is the one a "+
			"consumer has to decide about first", trunc.Since, baseline)
	}
	if trunc.Floor != firstRetained {
		t.Errorf("Floor = %d, want %d: the lowest seq still retained", trunc.Floor, firstRetained)
	}
	if trunc.Head != head {
		t.Errorf("Head = %d, want %d: the highest seq ever assigned, which a prune does not move",
			trunc.Head, head)
	}

	// The window is RESUMABLE: Floor-1 is a checkpoint the implementation can
	// serve, and it serves everything retained.
	resumed := journalRead(t, ctx, fixture, trunc.Floor-1, 0)
	if len(resumed.Rows) != 2 || resumed.Rows[0].Seq != trunc.Floor {
		t.Fatalf("resuming from Floor-1 (%d) returned %d records starting at %d, want 2 starting at %d: "+
			"a window a caller cannot resume from is a window that told it nothing",
			trunc.Floor-1, len(resumed.Rows), journalFirstSeq(resumed), trunc.Floor)
	}

	// A full export from the beginning of history must not present the
	// surviving suffix as a complete one.
	exported := journalRequireTruncated(t, "exporting from the beginning of history",
		journalReadErr(ctx, fixture, 0, 0))
	if exported.Floor != firstRetained {
		t.Errorf("export Floor = %d, want %d", exported.Floor, firstRetained)
	}

	// And a BOUNDED read from the same checkpoint takes the same decision, with
	// the same window. A limit that returned the first retained records as a
	// success would be the silent skip, reintroduced by the paging path.
	bounded := journalRequireTruncated(t, "reading a bounded page from a pruned checkpoint",
		journalReadErr(ctx, fixture, baseline, 2))
	if bounded.Since != trunc.Since || bounded.Floor != trunc.Floor || bounded.Head != trunc.Head {
		t.Errorf("a bounded read reported the window [%d..%d] after %d and an unbounded one [%d..%d] "+
			"after %d; the limit bounds the ANSWER, never the verdict",
			bounded.Floor, bounded.Head, bounded.Since, trunc.Floor, trunc.Head, trunc.Since)
	}
}

// RunJournalHeadSurvivesAFullPrune pins the one state where records and history
// disagree, and it is the state that decides whether a consumer can ever stop
// polling.
//
// Prune deletes records; it never touches the counter. So a journal with
// nothing left in it still knows how far it got, which is what lets it answer
// "you are at the end of my history" instead of "I have nothing" — the two are
// the same empty result set at the SQL level, and a head derived from the
// surviving records cannot tell them apart. The case also pins the consequence
// on the write side: seq does not reset, so the next mutation continues the
// history rather than colliding with a consumer's dedupe window.
func RunJournalHeadSurvivesAFullPrune(t *testing.T, ctx context.Context, fixture JournalFixture) {
	baseline := journalBaseline(t, ctx, fixture)
	journalSeedCreates(t, ctx, fixture, "prune", 2)
	head := baseline + 2

	journalPrune(t, ctx, fixture, head+1)

	emptied := journalRead(t, ctx, fixture, head, 0)
	if len(emptied.Rows) != 0 {
		t.Fatalf("a fully pruned journal returned %d records", len(emptied.Rows))
	}
	if emptied.Head != head {
		t.Errorf("head = %d after every record was pruned, want %d: the head is the journal's HISTORY, "+
			"and a head derived from the surviving records would drop to nothing here", emptied.Head, head)
	}

	// A consumer that had not reached the head is told, and the window it is
	// told about is the empty one: Floor above Head means "fully pruned".
	trunc := journalRequireTruncated(t, "reading a fully pruned journal from below its head",
		journalReadErr(ctx, fixture, baseline, 0))
	if trunc.Floor != head+1 || trunc.Head != head {
		t.Errorf("fully pruned window = [%d..%d], want [%d..%d]: Floor above Head is how a caller reads "+
			"'nothing retained, caught up to Head'", trunc.Floor, trunc.Head, head+1, head)
	}

	// Seq continues from the counter, not from the (now empty) table.
	journalSeedCreates(t, ctx, fixture, "prune-after", 1)
	next := journalRead(t, ctx, fixture, head, 0)
	if len(next.Rows) != 1 {
		t.Fatalf("read %d records after a post-prune mutation, want 1", len(next.Rows))
	}
	if next.Rows[0].Seq != head+1 {
		t.Errorf("the first record after a full prune is seq %d, want %d: a seq that restarted would "+
			"hand a consumer numbers it has already processed", next.Rows[0].Seq, head+1)
	}
}

// RunJournalEveryMutationKindLandsARow pins the journal's completeness: every
// op the engine journals is one a reader actually receives.
//
// The vocabulary is CLOSED and declared once — issueops.WireEventOps plus
// issueops.EngineOnlyEventOps — and this case checks the fixture's kit against
// it, so an op added to the engine without a hook here fails rather than
// joining a set nothing drives. A backend that recorded five of the seven would
// otherwise pass every other case in this file: they all read whatever records
// exist, and a missing KIND is invisible to a reader that only counts.
//
// THE ENGINE-ONLY OPS ARE DEMANDED, NOT SKIPPED, and that distinction is the
// whole reason issueops.IsWireEventOp exists. The journal records seven ops and
// the public event vocabulary is six; a projector SKIPS the seventh rather than
// faulting on it, and that skip is only sound while the record is there to be
// skipped. A contract that dropped the engine-only ops because "no wire event
// carries them" would be asserting the projector's view of the journal instead
// of the journal's own, and the first backend to stop recording a comment would
// pass it.
//
// It runs LAST, after both pruning cases, which is deliberate: a journal that
// was emptied and then written to again is the state a long-lived workspace
// actually spends its life in, and the mutations here have to land in it.
//
// MOVING IT EARLIER IS A SILENT WEAKENING, so this is stated here as well as in
// the dispatch table and the three leg wirings — at the site somebody editing a
// leg file would actually be looking. Every case rebaselines off the live head,
// so a reorder still COMPILES AND STILL PASSES; what it quietly gives up is the
// only coverage anywhere that a journal keeps recording after a full prune,
// with nothing going red to say so. If you reorder these, say why.
func RunJournalEveryMutationKindLandsARow(t *testing.T, ctx context.Context, fixture JournalFixture) {
	journalBaseline(t, ctx, fixture)
	subject := fixture.IssuePrefix + "-kind-subject"
	other := fixture.IssuePrefix + "-kind-other"

	kit := journalMutationKit(fixture, subject, other)
	journalRequireKitCoversTheVocabulary(t, kit)

	sawEngineOnly := false
	for _, mutation := range kit {
		before := journalHead(t, ctx, fixture)
		if err := mutation.run(ctx); err != nil {
			t.Fatalf("%s: %v", mutation.what, err)
		}
		page := journalRead(t, ctx, fixture, before, 0)
		if !journalRecords(page.Rows, string(mutation.op), mutation.subject) {
			t.Errorf("%s recorded no %q for %s; the journal above seq %d holds %v. A mutation kind the "+
				"journal drops is a replay that silently diverges from the workspace it claims to mirror",
				mutation.what, mutation.op, mutation.subject, before, journalOpsOf(page.Rows))
			continue
		}
		if !storeops.IsWireEventOp(mutation.op) {
			sawEngineOnly = true
		}
	}

	if !sawEngineOnly {
		t.Errorf("no engine-only op was journaled; the journal is supposed to be WIDER than the public " +
			"event vocabulary, and a projector's skip of one is only sound while the record exists")
	}
}

// journalMutation is one row of the mutation kit under test: the op it must
// record, the id that record names, and how to drive it.
type journalMutation struct {
	op      storeops.EventOp
	what    string
	subject string
	run     func(ctx context.Context) error
}

// journalMutationKit binds each hook the fixture supplies to the op it must
// produce, in an order that leaves each mutation something to act on: the two
// issues exist before anything edits them, the edge exists before it is
// removed, and the delete comes last.
func journalMutationKit(fixture JournalFixture, subject, other string) []journalMutation {
	m := fixture.Mutations
	return []journalMutation{
		{op: storeops.EventCreate, what: "creating an issue", subject: subject,
			run: func(ctx context.Context) error {
				if err := m.Create(ctx, other); err != nil {
					return err
				}
				return m.Create(ctx, subject)
			}},
		{op: storeops.EventUpdate, what: "updating an issue", subject: subject,
			run: func(ctx context.Context) error { return m.Update(ctx, subject) }},
		{op: storeops.EventCommentWrite, what: "commenting on an issue", subject: subject,
			run: func(ctx context.Context) error {
				return m.Comment(ctx, subject, "a comment the journal records and no wire event carries")
			}},
		{op: storeops.EventDepAdd, what: "adding a dependency", subject: subject,
			run: func(ctx context.Context) error { return m.AddDependency(ctx, subject, other) }},
		{op: storeops.EventDepRemove, what: "removing a dependency", subject: subject,
			run: func(ctx context.Context) error { return m.RemoveDependency(ctx, subject, other) }},
		{op: storeops.EventClose, what: "closing an issue", subject: subject,
			run: func(ctx context.Context) error { return m.Close(ctx, subject) }},
		{op: storeops.EventDelete, what: "deleting an issue", subject: subject,
			run: func(ctx context.Context) error { return m.Delete(ctx, subject) }},
	}
}

// journalRequireKitCoversTheVocabulary fails when the kit and the engine's
// closed op vocabulary disagree in either direction.
//
// Both directions matter. An op the engine journals and the kit does not drive
// is a kind nothing here proves is recorded — the gap this case was written
// for. An op the kit drives that the engine does not declare is a case
// asserting a vocabulary nobody publishes, which is how a contract outlives the
// thing it describes.
func journalRequireKitCoversTheVocabulary(t *testing.T, kit []journalMutation) {
	t.Helper()
	driven := map[storeops.EventOp]bool{}
	for _, mutation := range kit {
		driven[mutation.op] = true
	}
	declared := map[storeops.EventOp]bool{}
	for _, op := range append(storeops.WireEventOps(), storeops.EngineOnlyEventOps()...) {
		declared[op] = true
		if !driven[op] {
			t.Fatalf("the engine journals %q and this contract drives no mutation that produces it: add a "+
				"hook to JournalMutations and a row to journalMutationKit, or the op joins the set nothing "+
				"proves is recorded", op)
		}
	}
	for op := range driven {
		if !declared[op] {
			t.Fatalf("journalMutationKit drives %q, which is in neither issueops.WireEventOps nor "+
				"issueops.EngineOnlyEventOps: this contract is asserting a vocabulary nobody declares", op)
		}
	}
}

// journalBaseline activates the journal and returns the head every assertion in
// the calling case is written relative to. See the seeding-discipline note at
// the top of this file for why no case may assume an empty journal.
func journalBaseline(t *testing.T, ctx context.Context, fixture JournalFixture) int64 {
	t.Helper()
	fixture.SetJournalEnabled(true)
	return journalHead(t, ctx, fixture)
}

// journalHead reads the journal's head through the role itself, from a
// checkpoint no journal will ever reach.
//
// A read at or above the head is CAUGHT UP — no records, no error, the head —
// which RunJournalHeadArrivesWithItsRowsAndDetectsCaughtUp pins directly. That
// makes this the one probe that works in every state these cases put the
// journal in, including after a prune has taken the floor above every seq an
// earlier case knew about.
func journalHead(t *testing.T, ctx context.Context, fixture JournalFixture) int64 {
	t.Helper()
	page, err := fixture.Journal.ReadEventsJournalPage(ctx, math.MaxInt64, 0)
	if err != nil {
		t.Fatalf("reading the journal head: %v", err)
	}
	if len(page.Rows) != 0 {
		t.Fatalf("a checkpoint above every possible seq returned %d records", len(page.Rows))
	}
	return page.Head
}

// journalRead is one page, with the error failing the case. Cases that expect a
// failure use journalReadErr instead.
func journalRead(t *testing.T, ctx context.Context, fixture JournalFixture, since int64, limit int) journalops.Page {
	t.Helper()
	page, err := fixture.Journal.ReadEventsJournalPage(ctx, since, limit)
	if err != nil {
		t.Fatalf("reading the journal from %d (limit %d): %v", since, limit, err)
	}
	return page
}

// journalReadErr is one page read for its error alone.
func journalReadErr(ctx context.Context, fixture JournalFixture, since int64, limit int) error {
	_, err := fixture.Journal.ReadEventsJournalPage(ctx, since, limit)
	return err
}

// journalRequireTruncated asserts err is the role's typed truncation and
// returns it.
//
// It matches journalops.TruncatedError, which no in-tree leg names — they all
// return the storage alias — so the match is the alias identity holding at
// runtime. A nil error is reported as the silent case it actually is, because
// "no error" here means the read presented a gap as a success.
func journalRequireTruncated(t *testing.T, what string, err error) *journalops.TruncatedError {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded; a checkpoint below the retained window has to fail loudly, or the "+
			"consumer either stalls forever or skips the pruned records without knowing", what)
	}
	var trunc *journalops.TruncatedError
	if !errors.As(err, &trunc) {
		t.Fatalf("%s failed with %T (%v), want *journalops.TruncatedError: a caller dispatches on the "+
			"TYPE and reads the window off its fields", what, err, err)
	}
	return trunc
}

// journalPrune removes every record below before, both retention floors
// disabled.
func journalPrune(t *testing.T, ctx context.Context, fixture JournalFixture, before int64) {
	t.Helper()
	if _, err := fixture.Prune(ctx, before, 0, 0); err != nil {
		t.Fatalf("pruning the journal below %d: %v", before, err)
	}
}

// journalSeedCreates creates n issues under the case's own marker and returns
// their ids in creation order.
func journalSeedCreates(t *testing.T, ctx context.Context, fixture JournalFixture, marker string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := range n {
		id := fixture.IssuePrefix + "-" + marker + "-" + string(rune('a'+i))
		if err := fixture.Mutations.Create(ctx, id); err != nil {
			t.Fatalf("seeding %s: %v", id, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// journalRecords reports whether any record names op for issue.
func journalRecords(rows []journalops.Row, op, issue string) bool {
	for _, row := range rows {
		if row.Op == op && row.IssueID == issue {
			return true
		}
	}
	return false
}

// journalOpsOf renders a window's ops for a failure message. The payloads are
// deliberately left out: what a reader needs from a failure here is which kinds
// arrived, and issue snapshots are kilobytes each.
func journalOpsOf(rows []journalops.Row) []string {
	ops := make([]string, 0, len(rows))
	for _, row := range rows {
		ops = append(ops, row.Op+"("+row.IssueID+")")
	}
	return ops
}

// journalFirstSeq is the first record's seq, or 0 for an empty page, so a
// failure message can name what it got without an index panic.
func journalFirstSeq(page journalops.Page) int64 {
	if len(page.Rows) == 0 {
		return 0
	}
	return page.Rows[0].Seq
}
