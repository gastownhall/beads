package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.Importer must
// satisfy: the sanctioned bulk-upsert door, the write half of `bd import`.
//
// WHY IT IS A SEPARATE FILE RATHER THAN MORE BatchCreator CASES. The batch
// creator does not merely lack these promises, it REFUSES them, twice and on
// the record, and both refusals point here:
//
//   - "AN EXPLICIT ID IS CREATE-ONLY … an id that already names a row is
//     ErrAlreadyExists and never a silent full-row upsert. THE DOCUMENTED
//     UPSERT SURFACE IS `bd import`" (issueops/batchcreator.go, on
//     BatchCreateItem.Issue). The role text names this role as the home of the
//     promise it declines.
//   - "EVERY REQUESTED EDGE IS WRITTEN OR THE BATCH REFUSES. There is no
//     per-edge report and no partial graph … a create that reported success
//     having SILENTLY dropped an edge is data loss, because the caller has no
//     way to learn the relationship is missing."
//
// The second refusal is the one worth reading closely, because it is what
// decides the shape of every skip case below. Its ground is not that dropping
// an edge is intolerable; it is that dropping one SILENTLY is, because the
// caller cannot learn about it. An import cannot refuse — a snapshot naming
// work that was filtered out upstream would be unimportable, and the row is
// wanted whether or not its edge resolves — so it takes the other branch of
// the same reasoning and REPORTS, in ImportBatchResult.SkippedDependencies.
//
// So this contract does not restate a rejected promise. It states the
// obligation the rejection creates: the report is the whole justification for
// the drop, and an importer whose report is empty has committed exactly the
// data loss BatchCreator refuses. Every case here therefore asserts the report
// as hard as it asserts the store, and none of them accepts "a skip was
// mentioned somewhere" for an answer.
//
// THERE IS ONE WIRING, NOT THREE, AND THAT IS A FINDING RATHER THAN AN
// OVERSIGHT. A capability in this repo is a role plus an accessor
// (engdocs/ADDING_AN_ISSUEOPS_ROLE.md), and Importer has exactly one accessor:
// uow.ImporterSource, served by the unit-of-work provider and reached in
// production by cmd/bd/import_proxied_server.go. storage.DoltStorage has no
// Importer() — it is the only uow capability source with no counterpart there
// — so on the two store backends `bd import` still runs the raw seam
// (store.CreateIssuesWithFullOptions, cmd/bd/import_shared.go) and there is no
// role for a contract to hold. Giving them one means a new method on the
// storage interface and a new body behind it at two backends, which is API
// work rather than test work.
//
// The consequence is that this file covers exactly the gap nothing else can.
// The ENGINE below it (issueops.CreateIssuesInTxWithResult) is the same one the
// store legs run and the audit tier already observes through that raw seam
// (backend/conformance/audit_molecule_wisp_batch_iter.go). What no test
// anywhere reaches is the mapping this role puts on top of it — the
// stale-rejection list, its deduplication, the Created arithmetic, and the
// skip list — which is unit-of-work-own code on the one leg RunAll's Factory
// (func(*testing.T) storage.DoltStorage) structurally cannot reach.

// ImporterFixture supplies adapter-specific storage access for the import
// assertions. Every field is named and typed exactly like the per-backend
// roleFixtureKit hook it is filled from, so a wiring is kit plus accessor plus
// prefix with no adapter in between.
type ImporterFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// Importer is the surface under test.
	Importer publicops.Importer
	// QueryScalar runs a single-row query and scans it, RETURNING the error
	// rather than failing the test.
	QueryScalar func(context.Context, string, []any, ...any) error
}

// RunImporterRejectsAStaleRowAndNamesIt pins the in-transaction half of the
// stale guard (ImportBatchRequest.AllowStale, ImportBatchResult.
// StaleRejectedIDs): with AllowStale off, a row whose updated_at is older than
// the stored one keeps every stored column AND IS NAMED IN THE RESULT, and the
// Created count is what LANDED rather than what was asked for.
//
// WHAT THE AUDIT FIXTURE MAKES UNOBSERVABLE, and what this case exists for.
// The audit twin (testAuditCreateRejectStaleUpserts) runs the same three arms
// and reads back one thing: GetIssue(...).Title. Through that single column,
// arm (a) and arm (c) are INDISTINGUISHABLE — the title fails to change in
// both — while underneath they are different clauses of different code:
//
//   - (a) OLDER is a REJECTION. The engine's explicit staleness read fires,
//     nothing is written at all, aux data is deliberately not merged, and the
//     row is reported.
//   - (c) EQUAL is an ACCEPTANCE whose columns happen not to move. The row is
//     NOT rejected — updated_at has second granularity, so a tie may be two
//     distinct same-second edits and the local row wins on the conditional
//     upsert — nothing is reported, and it counts as created.
//
// Reading the title alone, an implementation that rejected every tie would
// pass the audit case forever, and a caller would be told it imported N rows
// that it did not. So the arms below assert the REPORT and the COUNT, which is
// the only place the two clauses differ.
//
// EACH REJECTING BATCH CARRIES A SIBLING THAT LANDS. Created is promised as
// "the rows the batch wrote … excluding stale-rejected rows", and a
// single-row batch cannot tell an implementation that computed it from the
// request from one that computed it from what landed: both answer 0 for a
// rejected row. Two rows, one rejected, separates them — 1 against 2.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: nothing. The anchor is seeded by
// this case's own first import, under this case's own id, and the seed's
// stored updated_at is read back RAW before any arm leans on being older or
// newer than it — a seeding path that re-stamped the row with the clock would
// leave every subsequent arm comparing against the wrong instant. The stamps
// are fixed calendar dates rather than offsets from now, so no arm's meaning
// depends on when the suite runs.
func RunImporterRejectsAStaleRowAndNamesIt(t *testing.T, ctx context.Context, fixture ImporterFixture) {
	t.Helper()
	anchor := fixture.IssuePrefix + "-impstale"
	sibling := fixture.IssuePrefix + "-impstale-sibling"
	var (
		y2020 = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		y2021 = time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
		y2022 = time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	)

	// The seed is an import too: this role IS the upsert door, so the state an
	// upsert is tested against is the state an import leaves.
	seed := runImporterBatch(t, ctx, fixture, "seed", importerIssueAt(anchor, "original", y2021))
	if seed.Created != 1 {
		t.Fatalf("the seeding import reported Created = %d, want 1", seed.Created)
	}
	if len(seed.StaleRejectedIDs) != 0 {
		t.Fatalf("the seeding import rejected %v as stale; there was nothing to be stale against", seed.StaleRejectedIDs)
	}
	assertImporterScalar(t, ctx, fixture, "seeded title", anchor, "original")
	if stored := readImporterUpdatedAt(t, ctx, fixture, anchor); !stored.Equal(y2021) {
		t.Fatalf("the seeded row is stored at updated_at %s, want %s verbatim: an anchor the import re-stamped with the clock makes every arm below compare against the wrong instant",
			stored, y2021)
	}

	t.Run("OlderIsRejectedAndNamed", func(t *testing.T) {
		result := runImporterBatch(t, ctx, fixture, "older",
			importerIssueAt(anchor, "stale", y2020),
			importerIssueAt(sibling, "lands beside the rejection", y2022))

		assertImporterScalar(t, ctx, fixture, "title after an older row", anchor, "original")
		assertImporterRowCount(t, ctx, fixture, "issues", sibling, 1)
		if got := result.StaleRejectedIDs; len(got) != 1 || got[0] != anchor {
			t.Errorf("StaleRejectedIDs = %v, want exactly [%s]: a row the guard kept out of the store is one the caller is owed the id of, or the import reports having written work it discarded",
				got, anchor)
		}
		if result.Created != 1 {
			t.Errorf("Created = %d for a 2-row batch with 1 rejection, want 1: Created counts what landed, not what was asked for", result.Created)
		}
	})

	t.Run("NewerOverwrites", func(t *testing.T) {
		result := runImporterBatch(t, ctx, fixture, "newer", importerIssueAt(anchor, "fresh", y2022))

		assertImporterScalar(t, ctx, fixture, "title after a newer row", anchor, "fresh")
		if len(result.StaleRejectedIDs) != 0 {
			t.Errorf("StaleRejectedIDs = %v for a strictly newer row, want none", result.StaleRejectedIDs)
		}
		if result.Created != 1 {
			t.Errorf("Created = %d for one accepted row, want 1", result.Created)
		}
	})

	t.Run("EqualIsAcceptedNotRejected", func(t *testing.T) {
		result := runImporterBatch(t, ctx, fixture, "equal", importerIssueAt(anchor, "tie", y2022))

		// Same observable as the older arm through the title alone, which is
		// the whole point: only the two assertions below tell them apart.
		assertImporterScalar(t, ctx, fixture, "title after an equal-stamp row", anchor, "fresh")
		if len(result.StaleRejectedIDs) != 0 {
			t.Errorf("StaleRejectedIDs = %v for an equal-timestamp row, want none: a tie is the local row winning a conditional upsert, not the stale guard firing, and reporting it would tell a caller its row was discarded when it was accepted",
				result.StaleRejectedIDs)
		}
		if result.Created != 1 {
			t.Errorf("Created = %d for an equal-timestamp row, want 1: the row was written, and only its columns declined to move", result.Created)
		}
	})
}

// RunImporterReportsTheAbsentTargetItDroppedOnce is the case this contract
// exists for, stated against the exact request BatchCreator refuses:
// RunBatchCreatorRefusesAnAbsentEdgeTarget sends an item whose edge names a
// missing id of its OWN prefix and requires ErrValidation wrapping ErrNotFound
// with nothing created. The importer takes the other branch — the row lands,
// the edge does not — and the report is the entire reason that is allowed
// rather than the data loss the batch creator calls it.
//
// THE REPORT IS THEREFORE ASSERTED AS AN EXACT LIST, not searched. The audit
// twin (testAuditCreateAllWispsInlineDependencies) scans its skip slice for a
// matching pair and stops at the first hit, so it cannot see a report that
// names the same edge fifty times, and it never reads the reason at all. This
// case names the whole list and its length.
//
// THE ITEM ASKS FOR THE SAME MISSING EDGE TWICE, which is what makes the
// deduplication clause observable ("SkippedDependencies lists the edges
// dropped by the batch, DEDUPLICATED"). The engine reports per requested edge,
// so both copies are reported to the role and collapsing them is the role's
// OWN work — one of the few pieces of this leg's mapping that no engine test
// can cover. Without the repeat, a mapping that appended blindly is
// indistinguishable from one that deduplicates.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: nothing. The absent target is an id
// under this case's own prefixed name that no case creates, and its absence is
// asserted rather than assumed — a target another case had created would make
// the edge resolvable and turn every assertion below into a statement about a
// batch that skipped nothing.
func RunImporterReportsTheAbsentTargetItDroppedOnce(t *testing.T, ctx context.Context, fixture ImporterFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-impmiss-source"
	absent := fixture.IssuePrefix + "-impmiss-absent"
	assertImporterRowCount(t, ctx, fixture, "issues", absent, 0)
	assertImporterRowCount(t, ctx, fixture, "wisps", absent, 0)

	issue := importerIssue(source, "names something absent, twice")
	issue.Dependencies = []*types.Dependency{
		{IssueID: source, DependsOnID: absent, Type: types.DepBlocks},
		{IssueID: source, DependsOnID: absent, Type: types.DepBlocks},
	}
	result := runImporterBatch(t, ctx, fixture, "miss", issue)

	// The row is the point: an import that refused here would make a snapshot
	// naming filtered-out work unimportable.
	assertImporterRowCount(t, ctx, fixture, "issues", source, 1)
	if result.Created != 1 {
		t.Errorf("Created = %d, want 1: the row landed", result.Created)
	}
	assertImporterEdgeCount(t, ctx, fixture, source, absent, 0)
	assertImporterSkipped(t, result, []publicops.SkippedDependency{{IssueID: source, DependsOnID: absent}})
}

// RunImporterReportsTheCrossPlaneEdgeItDropped pins the same obligation for the
// plane rule. The request is the one RunBatchCreatorRefusesACrossPlaneInBatchEdge
// sends and refuses whole — a durable item and an ephemeral item created
// together with an edge between them — where the import keeps BOTH ROWS and
// drops only the edge.
//
// BOTH ROWS ARE ASSERTED, on their own tables. The dropped edge is the visible
// part, and an implementation that dropped the ephemeral item along with it
// would satisfy every statement about the edge: there is no edge because there
// is no wisp. The audit twin's arm (b) reads both rows back through GetIssue,
// which resolves across planes and so cannot say WHICH table either landed in.
//
// The report is again an exact list. Arm (b) of the audit case asserts no
// report at all — it passes a nil skip callback — so a silent cross-plane drop
// is invisible to it, which is precisely the drop BatchCreator calls data
// loss.
func RunImporterReportsTheCrossPlaneEdgeItDropped(t *testing.T, ctx context.Context, fixture ImporterFixture) {
	t.Helper()
	durable := fixture.IssuePrefix + "-impplane-durable"
	wisp := fixture.IssuePrefix + "-impplane-wisp"

	ephemeral := importerIssue(wisp, "the ephemeral end")
	ephemeral.Ephemeral = true
	ephemeral.Dependencies = []*types.Dependency{
		{IssueID: wisp, DependsOnID: durable, Type: types.DepBlocks},
	}
	result := runImporterBatch(t, ctx, fixture, "cross-plane",
		importerIssue(durable, "the durable end"), ephemeral)

	assertImporterRowCount(t, ctx, fixture, "issues", durable, 1)
	assertImporterRowCount(t, ctx, fixture, "wisps", wisp, 1)
	if result.Created != 2 {
		t.Errorf("Created = %d, want 2: dropping an edge costs the batch neither row", result.Created)
	}
	assertImporterEdgeCount(t, ctx, fixture, wisp, durable, 0)
	assertImporterSkipped(t, result, []publicops.SkippedDependency{{IssueID: wisp, DependsOnID: durable}})
}

// RunImporterReportsTheCycleEdgeItDropped pins the last of the three drops: an
// in-batch dependency cycle. Both rows land, the graph stays acyclic, and the
// ONE edge given up to keep it acyclic is named.
//
// THE ASSERTION IS "EXACTLY ONE OF THE PAIR SURVIVED, AND THE REPORT NAMES THE
// OTHER ONE", which is stated without pinning WHICH. The order the engine
// walks a batch's pending edges in is not a promise of this role, so a case
// that demanded the first edge survive would fail an implementation that is
// equally correct. What IS a promise is that the report and the store agree:
// the edge the caller was told about is the edge that is missing, and a report
// naming the surviving edge is worse than no report — it sends a caller
// re-adding a relationship that is already there while the one that is gone
// stays gone.
//
// The audit twin's arm (b) asserts that both ISSUES exist and stops. It reads
// no edge and no report, so it cannot distinguish "one edge dropped" from
// "both dropped" from "the cycle was written" — the whole subject of the arm
// is unobservable to it.
func RunImporterReportsTheCycleEdgeItDropped(t *testing.T, ctx context.Context, fixture ImporterFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-impcycle-first"
	second := fixture.IssuePrefix + "-impcycle-second"

	head := importerIssue(first, "blocks the second")
	head.Dependencies = []*types.Dependency{
		{IssueID: first, DependsOnID: second, Type: types.DepBlocks},
	}
	tail := importerIssue(second, "blocks the first, closing the cycle")
	tail.Dependencies = []*types.Dependency{
		{IssueID: second, DependsOnID: first, Type: types.DepBlocks},
	}
	result := runImporterBatch(t, ctx, fixture, "cycle", head, tail)

	assertImporterRowCount(t, ctx, fixture, "issues", first, 1)
	assertImporterRowCount(t, ctx, fixture, "issues", second, 1)
	if result.Created != 2 {
		t.Errorf("Created = %d, want 2: a cyclic edge costs the batch neither row", result.Created)
	}

	forward := importerEdgeCount(t, ctx, fixture, first, second)
	backward := importerEdgeCount(t, ctx, fixture, second, first)
	if forward+backward != 1 {
		t.Fatalf("edges %s->%s = %d and %s->%s = %d, want exactly one of the two: both is the cycle the batch is supposed to refuse to write, neither throws away an edge nothing was wrong with",
			first, second, forward, second, first, backward)
	}
	dropped := publicops.SkippedDependency{IssueID: first, DependsOnID: second}
	if forward == 1 {
		dropped = publicops.SkippedDependency{IssueID: second, DependsOnID: first}
	}
	assertImporterSkipped(t, result, []publicops.SkippedDependency{dropped})
}

// runImporterBatch imports issues under the option set `bd import` actually
// sends (cmd/bd/import_proxied_server.go): stale rows rejected in the
// transaction, and an explicit import admitting ids outside the workspace's
// configured prefix. It fails the test on an error, because every case here is
// about a batch that LANDS — the refusals belong to BatchCreator.
func runImporterBatch(t *testing.T, ctx context.Context, fixture ImporterFixture, source string, issues ...*types.Issue) publicops.ImportBatchResult {
	t.Helper()
	result, err := fixture.Importer.ImportBatch(ctx, publicops.ImportBatchRequest{
		Actor:                "importer",
		Issues:               issues,
		SkipPrefixValidation: true,
		Source:               source,
	})
	if err != nil {
		t.Fatalf("ImportBatch(%s, %d issues): %v", source, len(issues), err)
	}
	return result
}

// importerIssue is one import row with an EXPLICIT id: an import always names
// the rows it carries, because it is replaying a snapshot rather than minting
// work.
func importerIssue(id, title string) *types.Issue {
	return &types.Issue{ID: id, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

// importerIssueAt is importerIssue carrying the timestamps a snapshot was
// exported with, which is what the stale guard compares.
func importerIssueAt(id, title string, stamp time.Time) *types.Issue {
	issue := importerIssue(id, title)
	issue.CreatedAt = stamp
	issue.UpdatedAt = stamp
	return issue
}

// assertImporterSkipped asserts the WHOLE skip report, in order, by the two
// fields that identify an edge. It is an equality rather than a search: a
// report is what justifies a silent drop, so extra entries (a report that
// names an edge the batch actually wrote) and repeated ones (a report that
// never deduplicated) are both failures, and neither is visible to a scan that
// stops at its first match.
//
// The Reason is checked for PRESENCE only. It is prose the role documents as
// "missing, invalid, or would form a disallowed edge" without fixing the
// wording, so pinning a string would freeze a message rather than a promise —
// but an empty one leaves a caller with a drop it cannot act on.
func assertImporterSkipped(t *testing.T, result publicops.ImportBatchResult, want []publicops.SkippedDependency) {
	t.Helper()
	if len(result.SkippedDependencies) != len(want) {
		t.Fatalf("SkippedDependencies = %+v, want exactly %d entry/entries %+v: the report is the whole reason a dropped edge is not data loss, so a missing entry hides one and a repeated entry is the deduplication clause failing",
			result.SkippedDependencies, len(want), want)
	}
	for i, got := range result.SkippedDependencies {
		if got.IssueID != want[i].IssueID || got.DependsOnID != want[i].DependsOnID {
			t.Errorf("SkippedDependencies[%d] names %s -> %s, want %s -> %s",
				i, got.IssueID, got.DependsOnID, want[i].IssueID, want[i].DependsOnID)
		}
		if got.Reason == "" {
			t.Errorf("SkippedDependencies[%d] (%s -> %s) carries no reason; a caller told only that an edge went missing cannot act on it",
				i, got.IssueID, got.DependsOnID)
		}
	}
}

// assertImporterRowCount asserts how many rows of a plane carry an id.
func assertImporterRowCount(t *testing.T, ctx context.Context, fixture ImporterFixture, table, id string, want int) {
	t.Helper()
	var got int
	//nolint:gosec // G201: table is one of the contract's two hardcoded names.
	query := "SELECT COUNT(*) FROM " + table + " WHERE id = ?"
	if err := fixture.QueryScalar(ctx, query, []any{id}, &got); err != nil {
		t.Fatalf("count %s rows for %s: %v", table, id, err)
	}
	if got != want {
		t.Errorf("%s rows for %s = %d, want %d", table, id, got, want)
	}
}

// importerEdgeCount counts the stored edges from source to target across BOTH
// dependency tables and all three target columns. A dropped edge is dropped on
// every plane, so the cases that expect zero want zero everywhere, and a
// per-table count could report one while the row sat in the other.
func importerEdgeCount(t *testing.T, ctx context.Context, fixture ImporterFixture, source, target string) int {
	t.Helper()
	var got int
	const query = `SELECT
		(SELECT COUNT(*) FROM dependencies WHERE issue_id = ?
			AND (depends_on_issue_id = ? OR depends_on_wisp_id = ? OR depends_on_external = ?)) +
		(SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?
			AND (depends_on_issue_id = ? OR depends_on_wisp_id = ? OR depends_on_external = ?))`
	args := []any{source, target, target, target, source, target, target, target}
	if err := fixture.QueryScalar(ctx, query, args, &got); err != nil {
		t.Fatalf("count edges %s -> %s: %v", source, target, err)
	}
	return got
}

func assertImporterEdgeCount(t *testing.T, ctx context.Context, fixture ImporterFixture, source, target string, want int) {
	t.Helper()
	if got := importerEdgeCount(t, ctx, fixture, source, target); got != want {
		t.Errorf("edges %s -> %s = %d, want %d", source, target, got, want)
	}
}

// assertImporterScalar asserts one stored column of one durable row.
func assertImporterScalar(t *testing.T, ctx context.Context, fixture ImporterFixture, what, id, want string) {
	t.Helper()
	var got string
	if err := fixture.QueryScalar(ctx, "SELECT title FROM issues WHERE id = ?", []any{id}, &got); err != nil {
		t.Fatalf("read %s of %s: %v", what, id, err)
	}
	if got != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

// readImporterUpdatedAt reads the column the stale guard compares, so a case
// can prove its anchor really is older or newer than the row it is about to
// send rather than assume it.
func readImporterUpdatedAt(t *testing.T, ctx context.Context, fixture ImporterFixture, id string) time.Time {
	t.Helper()
	var stamp time.Time
	if err := fixture.QueryScalar(ctx, "SELECT updated_at FROM issues WHERE id = ?", []any{id}, &stamp); err != nil {
		t.Fatalf("read updated_at of %s: %v", id, err)
	}
	return stamp.UTC()
}
