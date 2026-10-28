package conformance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract of the RETAINING retention sweep
// (issueops.SweepRequest.RetainIDs): a sweep that deletes a row and keeps a
// permanent tombstone of its id, after which NO write path commits a row with
// that id again. It is the guarantee an orchestrator needs to sweep rows whose
// ids are idempotency keys — a pinned id naming an operation that must never
// run twice — without a late retry of the operation re-creating the row.
//
// THE PROMISE HAS THREE HALVES, and the cases are grouped by them:
//
//   - the sweep RETIRES exactly what it deletes, into the tombstone table of
//     the row's own plane, in the same transaction (cases 1-2, 11-13);
//   - every create-shaped door REFUSES a retired id with
//     *issueops.IdempotencyExpiredError and writes nothing — Lifecycle.Create,
//     the batch roles, the storage-level (upsert) create, a transaction's
//     create and a rename onto the id (cases 3-8, 19);
//   - the doors that do not refuse still never write it: an import skips and
//     reports the row, and id generation never produces a retired id, top
//     level or under a parent (cases 9-10, 20).
//
// Case 14 is the property the halves add up to: a create racing the sweep of
// its id never lands. Cases 15-16 race the doors that do NOT refuse up front —
// the upsert-shaped create and the import — against the sweep, round after
// round with staggered starts, and pin that no interleaving leaves a live row
// beside a tombstone. Case 17 re-creates a retired no-history row (the shape
// an orchestrator's operation rows take) through every door, and case 18
// retires such rows with the wisps-plane request an orchestrator sends.
//
// Every case scopes its ids with the fixture's IssuePrefix and a tag, and
// every sweep names its rows with an exact pattern, so the cases share one
// workspace without reaching each other's rows.

// IDRetentionFixture supplies the roles the id-retention cases drive. Sweep is
// the backend's ordinary SweeperFixture, which carries the seed hooks, the raw
// read and the history hooks these cases reuse; the rest are the create doors
// the cases knock on. A nil door SKIPS the case that needs it, naming it.
type IDRetentionFixture struct {
	// IssuePrefix namespaces the ids each case seeds. It must equal
	// Sweep.IssuePrefix.
	IssuePrefix string
	// Sweep is the backend's Sweeper fixture: the seed hooks, the raw read
	// and the history hooks these cases reuse.
	Sweep SweeperFixture
	// UpsertCreate is the backend's UPSERT-shaped create of one row with an
	// explicit id — the storage seam: a classic store's CreateIssue, the uow
	// domain create without CreateOnly — with issue.Ephemeral choosing the
	// plane. It overwrites-or-inserts, so without the retired-id guard it
	// would resurrect a swept row. Nil skips the cases that need it, by name.
	UpsertCreate func(ctx context.Context, issue *types.Issue, actor string) error
	// Lifecycle is the guarded create/close role.
	Lifecycle publicops.Lifecycle
	// Reader reads a row back; a retired id must read as ErrNotFound.
	Reader publicops.Reader
	// BatchApplier, BatchCreator and Importer are the batch doors. Nil skips
	// the case that needs the door, by name.
	BatchApplier publicops.BatchApplier
	BatchCreator publicops.BatchCreator
	Importer     publicops.Importer
	// TxCreate creates one issue inside the backend's transaction surface
	// (storage.Transaction.CreateIssue). Nil skips the in-transaction case.
	TxCreate func(ctx context.Context, issue *types.Issue, actor string) error
	// Rename is the backend's storage-level identity change
	// (storage.Storage.UpdateIssueID): it moves a live row from oldID to
	// newID. Nil skips the rename case, by name.
	Rename func(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error
	// CommittedRetired reports whether the id_tombstones row for id is part of
	// the committed history (an `AS OF 'HEAD'` read). Nil skips that clause of
	// the history case.
	CommittedRetired func(ctx context.Context, id string) (bool, error)
}

const idRetentionActor = "id-retention-contract"

// IDRetentionUpsertImporter adapts a store that hands out no Importer role to
// one, over the bulk upsert its `bd import` writes through
// (storage.DoltStorage's CreateIssuesWithFullOptions, which the CLI's import
// calls with OnRetiredID set), so the import cases run on the store legs too.
// It carries what those cases send — the rows, the actor, AllowStale and
// SkipPrefixValidation — and reports what that seam reports: stale
// rejections, skipped edges and retired ids, with Created counting every row
// that was neither stale-rejected nor retired. The seam writes no memory
// records and no issue_prefix, so a request carrying either is refused.
func IDRetentionUpsertImporter(store ArrivalUpserter) publicops.Importer {
	return idRetentionUpsertImporter{store: store}
}

type idRetentionUpsertImporter struct {
	store ArrivalUpserter
}

func (i idRetentionUpsertImporter) ImportBatch(ctx context.Context, request publicops.ImportBatchRequest) (publicops.ImportBatchResult, error) {
	if len(request.Memories) > 0 || request.SyncIssuePrefix != "" {
		return publicops.ImportBatchResult{}, errors.New("upsert importer: the bulk upsert writes no memory records and no issue_prefix")
	}
	var result publicops.ImportBatchResult
	stale := make(map[string]struct{})
	retired := make(map[string]struct{})
	skipped := make(map[publicops.SkippedDependency]struct{})
	err := i.store.CreateIssuesWithFullOptions(ctx, request.Issues, request.Actor, storage.BatchCreateOptions{
		SkipPrefixValidation:           request.SkipPrefixValidation,
		RejectStaleUpserts:             !request.AllowStale,
		SkipDependencyValidationErrors: true,
		OnSkippedDependency: func(issueID, dependsOnID, reason string) {
			edge := publicops.SkippedDependency{IssueID: issueID, DependsOnID: dependsOnID, Reason: reason}
			if _, ok := skipped[edge]; !ok {
				skipped[edge] = struct{}{}
				result.SkippedDependencies = append(result.SkippedDependencies, edge)
			}
		},
		OnStaleRejected: func(issueID string) {
			if _, ok := stale[issueID]; !ok {
				stale[issueID] = struct{}{}
				result.StaleRejectedIDs = append(result.StaleRejectedIDs, issueID)
			}
		},
		OnRetiredID: func(issueID string) {
			if _, ok := retired[issueID]; !ok {
				retired[issueID] = struct{}{}
				result.RetiredIDs = append(result.RetiredIDs, issueID)
			}
		},
	})
	if err != nil {
		return publicops.ImportBatchResult{}, err
	}
	for _, issue := range request.Issues {
		_, isStale := stale[issue.ID]
		_, isRetired := retired[issue.ID]
		if !isStale && !isRetired {
			result.Created++
		}
	}
	return result, nil
}

// RunIDRetentionAPlainSweepLeavesTheIDReusable pins that retention is OPT-IN
// (SweepRequest.RetainIDs): a sweep that does not ask for it retires nothing,
// and the swept id can be created again.
func RunIDRetentionAPlainSweepLeavesTheIDReusable(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	id := idRetentionSeedClosed(t, ctx, fixture, "plain", false)
	result := sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{Tier: publicops.SweepDurable, IDPattern: id})
	if result.Swept != 1 {
		t.Fatalf("Swept = %d, want 1", result.Swept)
	}
	if result.RetainIDs {
		t.Errorf("result.RetainIDs = true for a sweep that did not ask for it")
	}
	idRetentionAssertTombstones(t, ctx, fixture, id, 0, 0)
	if _, err := fixture.Lifecycle.Create(ctx, idRetentionCreate(id, false)); err != nil {
		t.Fatalf("Create(%s) after a plain sweep = %v, want nil: without RetainIDs a swept id stays reusable", id, err)
	}
}

// RunIDRetentionARetainingSweepRetiresEverySweptID pins the write half: a
// retaining durable sweep that deletes an issues-plane row and a no-history
// row of the wisps plane leaves one tombstone per row, in the tombstone table
// of the row's own plane, carrying the row's closed_at; and the result echoes
// RetainIDs.
func RunIDRetentionARetainingSweepRetiresEverySweptID(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	durable := idRetentionSeedClosed(t, ctx, fixture, "every", false)
	noHistory := sweeperIssue(fixture.Sweep, "every", "nohist", false)
	noHistory.NoHistory = true
	if err := fixture.Sweep.CreateWisp(ctx, noHistory, idRetentionActor); err != nil {
		t.Fatalf("seeding %s: %v", noHistory.ID, err)
	}
	result := sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{
		Tier:      publicops.SweepDurable,
		IDPattern: sweeperPattern(fixture.Sweep, "every"),
		RetainIDs: true,
	})
	if result.Swept != 2 {
		t.Fatalf("Swept = %d, want 2 (the durable row and the no-history row)", result.Swept)
	}
	if !result.RetainIDs {
		t.Errorf("result.RetainIDs = false, want the request echoed")
	}
	sweeperAssertIssueRows(t, ctx, fixture.Sweep, 0, durable)
	sweeperAssertWispRows(t, ctx, fixture.Sweep, 0, noHistory.ID)
	idRetentionAssertTombstones(t, ctx, fixture, durable, 1, 0)
	idRetentionAssertTombstones(t, ctx, fixture, noHistory.ID, 0, 1)
	for _, id := range []string{durable, noHistory.ID} {
		var stamped int
		if err := fixture.Sweep.QueryScalar(ctx,
			"SELECT (SELECT COUNT(*) FROM id_tombstones WHERE id = ? AND closed_at = ?) + (SELECT COUNT(*) FROM wisp_id_tombstones WHERE id = ? AND closed_at = ?)",
			[]any{id, sweeperClosedAt, id, sweeperClosedAt}, &stamped); err != nil {
			t.Fatalf("reading the tombstone closed_at of %s: %v", id, err)
		}
		if stamped != 1 {
			t.Errorf("tombstone of %s does not carry the swept row's closed_at %s", id, sweeperClosedAt.Format(time.RFC3339))
		}
	}
}

// RunIDRetentionRefusesARecreateThroughCreate pins the refusal at the guarded
// create door (Lifecycle.Create): *IdempotencyExpiredError naming the id and
// its closed_at, matching ErrIdempotencyExpired and neither ErrAlreadyExists
// nor ErrValidation, with nothing written.
func RunIDRetentionRefusesARecreateThroughCreate(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	id := idRetentionRetire(t, ctx, fixture, "create", false)
	_, err := fixture.Lifecycle.Create(ctx, idRetentionCreate(id, false))
	idRetentionAssertExpired(t, err, id)
	idRetentionAssertAbsent(t, ctx, fixture, id)
	idRetentionAssertTombstones(t, ctx, fixture, id, 1, 0)
}

// RunIDRetentionRefusesARecreateOnTheOtherPlane pins ONE ID SPACE: a durable
// row's retired id is refused to an ephemeral create, and a wisp's retired id
// to a durable one.
func RunIDRetentionRefusesARecreateOnTheOtherPlane(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	durable := idRetentionRetire(t, ctx, fixture, "cross", false)
	_, err := fixture.Lifecycle.Create(ctx, idRetentionCreate(durable, true))
	idRetentionAssertExpired(t, err, durable)
	idRetentionAssertAbsent(t, ctx, fixture, durable)

	wisp := idRetentionRetire(t, ctx, fixture, "cross", true)
	_, err = fixture.Lifecycle.Create(ctx, idRetentionCreate(wisp, false))
	idRetentionAssertExpired(t, err, wisp)
	idRetentionAssertAbsent(t, ctx, fixture, wisp)
}

// RunIDRetentionRefusesThroughABatchApply pins the refusal on the
// all-or-nothing batch door: the retired id's create item takes the WHOLE
// request down, so the fresh item before it is not written either.
func RunIDRetentionRefusesThroughABatchApply(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	if fixture.BatchApplier == nil {
		t.Skip("fixture has no BatchApplier; the batch-apply door is not served here")
	}
	id := idRetentionRetire(t, ctx, fixture, "apply", false)
	fresh := idRetentionID(fixture, "apply", "fresh")
	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         idRetentionActor,
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Issue: idRetentionOpenIssue(fresh, false)}},
			{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Issue: idRetentionOpenIssue(id, false)}},
		},
	})
	idRetentionAssertExpired(t, err, id)
	var itemErr *publicops.ItemError
	if !errors.As(err, &itemErr) || itemErr.Index != 1 {
		t.Errorf("ApplyBatch error = %v, want an *ItemError naming item 1", err)
	}
	idRetentionAssertAbsent(t, ctx, fixture, id)
	sweeperAssertIssueRows(t, ctx, fixture.Sweep, 0, fresh)
}

// RunIDRetentionRefusesThroughABatchCreate pins the refusal on the batch
// create door, with the same whole-request rollback.
func RunIDRetentionRefusesThroughABatchCreate(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	if fixture.BatchCreator == nil {
		t.Skip("fixture has no BatchCreator; the batch-create door is not served here")
	}
	id := idRetentionRetire(t, ctx, fixture, "batch", false)
	fresh := idRetentionID(fixture, "batch", "fresh")
	_, err := fixture.BatchCreator.CreateBatch(ctx, publicops.CreateBatchRequest{
		Actor:         idRetentionActor,
		ForceIDPrefix: true,
		Items: []publicops.BatchCreateItem{
			{Issue: idRetentionOpenIssue(fresh, false)},
			{Issue: idRetentionOpenIssue(id, false)},
		},
	})
	idRetentionAssertExpired(t, err, id)
	idRetentionAssertAbsent(t, ctx, fixture, id)
	sweeperAssertIssueRows(t, ctx, fixture.Sweep, 0, fresh)
}

// RunIDRetentionRefusesThroughTheStorageSeam pins the refusal on the
// UPSERT-shaped create (IDRetentionFixture.UpsertCreate), which would
// otherwise overwrite-or-insert and so resurrect the row, on both planes.
func RunIDRetentionRefusesThroughTheStorageSeam(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	if fixture.UpsertCreate == nil {
		t.Skip("fixture has no UpsertCreate; this backend serves no upsert-shaped create")
	}
	for _, ephemeral := range []bool{false, true} {
		id := idRetentionRetire(t, ctx, fixture, "seam", ephemeral)
		err := fixture.UpsertCreate(ctx, idRetentionOpenIssue(id, ephemeral), idRetentionActor)
		idRetentionAssertExpired(t, err, id)
		idRetentionAssertAbsent(t, ctx, fixture, id)
	}
}

// RunIDRetentionRefusesARenameOntoARetiredID pins that the identity change is
// a write path the promise covers: renaming a live row onto a retired id, on
// either plane, is refused with *IdempotencyExpiredError and writes nothing —
// the retired id stays absent and the source row keeps its old id.
func RunIDRetentionRefusesARenameOntoARetiredID(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	if fixture.Rename == nil {
		t.Skip("fixture has no Rename; this backend serves no identity change")
	}
	for _, ephemeral := range []bool{false, true} {
		retired := idRetentionRetire(t, ctx, fixture, "rename", ephemeral)
		source := idRetentionID(fixture, "rename", fmt.Sprintf("live-%t", ephemeral))
		live := idRetentionOpenIssue(source, ephemeral)
		if _, err := fixture.Lifecycle.Create(ctx, idRetentionCreate(source, ephemeral)); err != nil {
			t.Fatalf("seeding %s: %v", source, err)
		}
		err := fixture.Rename(ctx, source, retired, live, idRetentionActor)
		idRetentionAssertExpired(t, err, retired)
		idRetentionAssertAbsent(t, ctx, fixture, retired)
		if got := idRetentionLiveRows(t, ctx, fixture, source); got != 1 {
			t.Errorf("live rows of %s after a refused rename = %d, want 1: the refusal must roll the rename back", source, got)
		}
	}
}

// RunIDRetentionRefusesInsideATransaction pins the refusal on a transaction's
// create, the in-process door an embedder's transactional create uses.
func RunIDRetentionRefusesInsideATransaction(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	if fixture.TxCreate == nil {
		t.Skip("fixture has no TxCreate; this backend serves no transaction surface")
	}
	id := idRetentionRetire(t, ctx, fixture, "tx", false)
	err := fixture.TxCreate(ctx, idRetentionOpenIssue(id, false), idRetentionActor)
	idRetentionAssertExpired(t, err, id)
	idRetentionAssertAbsent(t, ctx, fixture, id)
}

// RunIDRetentionImportSkipsAndReportsARetiredRow pins the import door, which
// does not refuse: the retired row is skipped and named in RetiredIDs, the
// other rows still land, Created counts none of its rows — the request carries
// it twice, as an export chaining an id's rows may — and an edge into it is a
// skipped dependency.
func RunIDRetentionImportSkipsAndReportsARetiredRow(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	if fixture.Importer == nil {
		t.Skip("fixture has no Importer; the import door is not served here")
	}
	id := idRetentionRetire(t, ctx, fixture, "import", false)
	fresh := idRetentionID(fixture, "import", "fresh")
	freshRow := idRetentionOpenIssue(fresh, false)
	freshRow.Dependencies = []*types.Dependency{{IssueID: fresh, DependsOnID: id, Type: types.DepBlocks}}
	result, err := fixture.Importer.ImportBatch(ctx, publicops.ImportBatchRequest{
		Actor:                idRetentionActor,
		Source:               "id-retention-contract",
		SkipPrefixValidation: true,
		Issues:               []*types.Issue{idRetentionOpenIssue(id, false), freshRow, idRetentionOpenIssue(id, false)},
	})
	if err != nil {
		t.Fatalf("ImportBatch = %v, want nil: an import skips a retired row rather than refusing the batch", err)
	}
	if len(result.RetiredIDs) != 1 || result.RetiredIDs[0] != id {
		t.Errorf("ImportBatch.RetiredIDs = %v, want [%s]", result.RetiredIDs, id)
	}
	if result.Created != 1 {
		t.Errorf("ImportBatch.Created = %d, want 1 (the fresh row only; neither row of the retired id)", result.Created)
	}
	skippedEdge := false
	for _, skipped := range result.SkippedDependencies {
		if skipped.IssueID == fresh && skipped.DependsOnID == id {
			skippedEdge = true
		}
	}
	if !skippedEdge {
		t.Errorf("ImportBatch.SkippedDependencies = %+v, want the edge %s -> %s into the retired row", result.SkippedDependencies, fresh, id)
	}
	idRetentionAssertAbsent(t, ctx, fixture, id)
	sweeperAssertIssueRows(t, ctx, fixture.Sweep, 1, fresh)
}

// RunIDRetentionNeverMintsARetiredID pins the generator half: a create that
// asks for no id is never handed a retired one. The collision is forced
// deterministically — a generated id is a hash of the title, description,
// actor and created_at — and the case first proves it is real: after a PLAIN
// sweep the identical create mints the identical id again; after a RETAINING
// sweep it must mint a different one, and succeed.
func RunIDRetentionNeverMintsARetiredID(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	stamp := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	create := func() string {
		t.Helper()
		result, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
			Actor: idRetentionActor,
			Issue: &types.Issue{
				Title:       fixture.IssuePrefix + " id-retention mint collision",
				Description: "a fixed body so the generated id is a fixed hash",
				Status:      types.StatusOpen,
				Priority:    2,
				IssueType:   types.TypeTask,
				CreatedAt:   stamp,
			},
		})
		if err != nil {
			t.Fatalf("Create(generated id) = %v", err)
		}
		return result.Issue.ID
	}
	closeAndSweep := func(id string, retain bool) {
		t.Helper()
		if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: idRetentionActor, IssueID: id, Reason: "id-retention"}); err != nil {
			t.Fatalf("Close(%s) = %v", id, err)
		}
		result := sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{Tier: publicops.SweepDurable, IDPattern: id, RetainIDs: retain})
		if result.Swept != 1 {
			t.Fatalf("sweep of %s Swept = %d, want 1", id, result.Swept)
		}
	}

	first := create()
	closeAndSweep(first, false)
	if again := create(); again != first {
		t.Fatalf("after a plain sweep the identical create minted %s, want %s again: the generator is not deterministic here, so this case cannot force a collision", again, first)
	}
	closeAndSweep(first, true)
	if minted := create(); minted == first {
		t.Fatalf("the identical create minted the retired id %s", first)
	}
}

// RunIDRetentionNeverMintsARetiredChildID pins the generator half for the
// child id mint, a create under a parent: it is never handed a retired child
// id, even when the parent's child counter lags the retired child. The counter
// is only half of the mint's high-water mark, and the other half, the highest
// live child, cannot see a swept row, so the tombstone has to count as taken.
// The case drops the parent's counter row by hand (Sweep.Exec): that is the
// shape a database written before GH#4750 leaves, and the one an explicit
// child created through a door that does not advance the counter leaves. It
// creates twice afterwards, because a refused create rolls its counter advance
// back and the next one would mint the same retired id again.
func RunIDRetentionNeverMintsARetiredChildID(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	if fixture.Sweep.Exec == nil {
		t.Skip("fixture has no Sweep.Exec; this case cannot make the child counter lag the retired child")
	}
	parent := idRetentionID(fixture, "child", "parent")
	if _, err := fixture.Lifecycle.Create(ctx, idRetentionCreate(parent, false)); err != nil {
		t.Fatalf("Create(%s) = %v", parent, err)
	}
	createChild := func() (string, error) {
		result, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
			Actor:         idRetentionActor,
			ParentID:      parent,
			ForceIDPrefix: true,
			Issue:         &types.Issue{Title: "id retention child of " + parent, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		})
		if err != nil {
			return "", err
		}
		return result.Issue.ID, nil
	}
	retired, err := createChild()
	if err != nil {
		t.Fatalf("Create(child of %s) = %v", parent, err)
	}
	if want := parent + ".1"; retired != want {
		t.Fatalf("first child of %s = %s, want %s", parent, retired, want)
	}
	if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: idRetentionActor, IssueID: retired, Reason: "id-retention"}); err != nil {
		t.Fatalf("Close(%s) = %v", retired, err)
	}
	if result := sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{Tier: publicops.SweepDurable, IDPattern: retired, RetainIDs: true}); result.Swept != 1 {
		t.Fatalf("retaining sweep of %s Swept = %d, want 1", retired, result.Swept)
	}
	idRetentionAssertTombstones(t, ctx, fixture, retired, 1, 0)
	if err := fixture.Sweep.Exec(ctx, []SQLStatement{{Query: "DELETE FROM child_counters WHERE parent_id = ?", Args: []any{parent}}}); err != nil {
		t.Fatalf("dropping the child counter of %s: %v", parent, err)
	}
	for range 2 {
		minted, err := createChild()
		if err != nil {
			t.Fatalf("Create(child of %s) after its child %s was retired = %v, want a fresh child id", parent, retired, err)
		}
		if minted == retired {
			t.Fatalf("Create(child of %s) minted the retired child id %s", parent, retired)
		}
	}
	idRetentionAssertAbsent(t, ctx, fixture, retired)
}

// RunIDRetentionDryRunRetiresNothing pins that a dry run writes no tombstone
// while echoing RetainIDs and counting what it would sweep.
func RunIDRetentionDryRunRetiresNothing(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	id := idRetentionSeedClosed(t, ctx, fixture, "dry", false)
	result := sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{
		Tier: publicops.SweepDurable, IDPattern: id, RetainIDs: true, DryRun: true,
	})
	if result.Swept != 1 || !result.RetainIDs || !result.DryRun {
		t.Errorf("dry run result = {Swept:%d RetainIDs:%v DryRun:%v}, want {1 true true}", result.Swept, result.RetainIDs, result.DryRun)
	}
	sweeperAssertIssueRows(t, ctx, fixture.Sweep, 1, id)
	idRetentionAssertTombstones(t, ctx, fixture, id, 0, 0)
}

// RunIDRetentionRetiresOnlyWhatItSwept pins "the set it deleted is the set it
// retired": a pinned row, a row a live row depends on, and a row Limit left
// for the next call are all held back and get no tombstone, while the one row
// the sweep deleted gets one.
func RunIDRetentionRetiresOnlyWhatItSwept(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	older := sweeperSeed(t, ctx, fixture.Sweep, sweeperIssue(fixture.Sweep, "only", "older", false), func(issue *types.Issue) {
		closedAt := sweeperClosedAt.Add(-time.Hour)
		issue.ClosedAt = &closedAt
	})
	newer := sweeperSeed(t, ctx, fixture.Sweep, sweeperIssue(fixture.Sweep, "only", "newer", false), nil)
	pinned := sweeperSeed(t, ctx, fixture.Sweep, sweeperIssue(fixture.Sweep, "only", "pinned", false), func(issue *types.Issue) {
		issue.Pinned = true
	})
	held := []string{newer, pinned}
	if fixture.Sweep.AddDependencies != nil {
		protected := sweeperSeed(t, ctx, fixture.Sweep, sweeperIssue(fixture.Sweep, "only", "protected", false), func(issue *types.Issue) {
			closedAt := sweeperClosedAt.Add(-2 * time.Hour)
			issue.ClosedAt = &closedAt
		})
		dependent := sweeperSeedOpen(t, ctx, fixture.Sweep, sweeperIssue(fixture.Sweep, "only", "dependent", false))
		if err := fixture.Sweep.AddDependencies(ctx, publicops.AddDependenciesRequest{
			Actor: idRetentionActor,
			Edges: []publicops.DependencyEdge{{IssueID: dependent, DependsOnID: protected, Type: publicops.DepBlocks}},
		}); err != nil {
			t.Fatalf("wiring the live dependency: %v", err)
		}
		held = append(held, protected)
	} else {
		t.Log("fixture has no AddDependencies hook; the live-dependent row is not seeded here")
	}

	result := sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{
		Tier:                  publicops.SweepDurable,
		IDPattern:             sweeperPattern(fixture.Sweep, "only"),
		ProtectLiveDependents: true,
		Limit:                 1,
		RetainIDs:             true,
	})
	if result.Swept != 1 || result.Remaining != 1 {
		t.Fatalf("Swept = %d, Remaining = %d; want 1 and 1", result.Swept, result.Remaining)
	}
	idRetentionAssertTombstones(t, ctx, fixture, older, 1, 0)
	for _, id := range held {
		idRetentionAssertTombstones(t, ctx, fixture, id, 0, 0)
		sweeperAssertIssueRows(t, ctx, fixture.Sweep, 1, id)
	}
}

// RunIDRetentionARetainingDurableSweepRecordsExactlyOneHistoryEntry pins that
// the tombstones ride the sweep's ONE history entry rather than adding a
// second, and — where the backend can read committed state — that the
// versioned tombstone is part of that entry.
func RunIDRetentionARetainingDurableSweepRecordsExactlyOneHistoryEntry(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	if fixture.Sweep.CountHistory == nil || fixture.Sweep.CommitPending == nil {
		t.Skip("this backend cannot observe or settle history, so the entry-per-call clause is unobservable here")
	}
	id := idRetentionSeedClosed(t, ctx, fixture, "hist", false)
	if err := fixture.Sweep.CommitPending(ctx); err != nil {
		t.Fatalf("CommitPending() after seeding: %v", err)
	}
	before := sweeperHistory(t, ctx, fixture.Sweep)
	sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{Tier: publicops.SweepDurable, IDPattern: id, RetainIDs: true})
	if after := sweeperHistory(t, ctx, fixture.Sweep); after != before+1 {
		t.Errorf("history went %d -> %d across one retaining sweep, want exactly one more entry", before, after)
	}
	if fixture.CommittedRetired == nil {
		return
	}
	committed, err := fixture.CommittedRetired(ctx, id)
	if err != nil {
		t.Fatalf("CommittedRetired(%s): %v", id, err)
	}
	if !committed {
		t.Errorf("the tombstone of %s is not in the committed history: it must land in the sweep's own entry", id)
	}
}

// RunIDRetentionARacingRecreateNeverLands is the property the contract adds up
// to: creates of an id racing the retaining sweep of that id never succeed —
// each meets the live row (ErrAlreadyExists) or the tombstone
// (ErrIdempotencyExpired) — and afterwards the id is absent and retired.
func RunIDRetentionARacingRecreateNeverLands(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	const racers = 8
	id := idRetentionSeedClosed(t, ctx, fixture, "race", false)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = fixture.Lifecycle.Create(ctx, idRetentionCreate(id, false))
		}()
	}
	var sweepErr error
	swept := 0
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		// A sweep that loses a write conflict to a racer is retried: the
		// property is about the creates, and the sweep must land for the case
		// to have raced anything.
		for attempt := 0; attempt < 5 && swept == 0; attempt++ {
			var result publicops.SweepResult
			result, sweepErr = fixture.Sweep.Sweeper.Sweep(ctx, publicops.SweepRequest{Tier: publicops.SweepDurable, IDPattern: id, RetainIDs: true})
			if sweepErr == nil {
				swept = result.Swept
				break
			}
		}
	}()
	close(start)
	wg.Wait()

	if sweepErr != nil || swept != 1 {
		t.Fatalf("racing sweep: Swept = %d, err = %v; want 1 and nil", swept, sweepErr)
	}
	for i, err := range errs {
		switch {
		case err == nil:
			t.Errorf("racer %d: Create(%s) = nil — a create racing the retaining sweep of its id landed", i, id)
		case errors.Is(err, publicops.ErrAlreadyExists), errors.Is(err, publicops.ErrIdempotencyExpired):
		default:
			t.Logf("racer %d: Create(%s) = %v (neither refusal; it still did not land)", i, id, err)
		}
	}
	idRetentionAssertAbsent(t, ctx, fixture, id)
	idRetentionAssertTombstones(t, ctx, fixture, id, 1, 0)
	_, err := fixture.Lifecycle.Create(ctx, idRetentionCreate(id, false))
	idRetentionAssertExpired(t, err, id)
}

// idRetentionRaceRounds is how many staggered rounds each door race runs.
// The window it hunts — a sweep committing between a create's retired-id read
// and its write — is a few statements wide, so a single round proves little;
// the stagger walks the create's start across the sweep's.
const idRetentionRaceRounds = 12

// RunIDRetentionARacingUpsertNeverResurrects races the UPSERT-shaped create of
// an id against the retaining sweep of that id, round after round with
// staggered starts. Whatever the interleaving, the round ends in exactly one
// of two states: the sweep won (Swept 1, the upsert refused, the id absent and
// retired) or the upsert won (Swept 0, the row live and open, no tombstone).
// Never a live row beside a tombstone, and never a nil create with the row
// gone. This is the race a READ COMMITTED upsert loses unless its retired-id
// read and the sweep contend on a lock.
//
// The race cases need a fixture whose roles can hold several connections at
// once; a leg whose case fixture serializes them (one connection) must run
// these three cases on a fixture that does not, or they race nothing.
//
// THEY DISCRIMINATE ONLY ON A READ COMMITTED BACKEND. The embedded store
// serializes its transactions, and so does the http leg served over it, so
// there every round is one whole transaction after the other and these cases
// pass with the coordination lock removed entirely. On a Dolt sql-server (the
// dolt and uow legs) transactions overlap, but each reads one snapshot and a
// sweep's delete and an upsert's write of the same row conflict at commit
// anyway; the uow leg also passes with the lock removed. Green on those legs
// is no evidence for the race fix. A leg can fail these cases only where the
// upsert and import doors run READ COMMITTED and only the coordination cell
// orders them against a sweep, such as a PostgreSQL store behind the shared
// SQL body.
func RunIDRetentionARacingUpsertNeverResurrects(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	upsert := fixture.UpsertCreate
	if upsert == nil {
		t.Skip("fixture has no UpsertCreate; this backend serves no upsert-shaped create")
	}
	idRetentionRaceDoor(t, ctx, fixture, "upsertrace", func(ctx context.Context, id string) (bool, error) {
		err := upsert(ctx, idRetentionOpenIssue(id, false), idRetentionActor)
		return err == nil, err
	})
}

// RunIDRetentionARacingImportNeverResurrects is the same race through the
// import door, which skips and reports a retired row rather than refusing it:
// a round the sweep wins must report the id in RetiredIDs and land nothing.
func RunIDRetentionARacingImportNeverResurrects(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	importer := fixture.Importer
	if importer == nil {
		t.Skip("fixture has no Importer; the import door is not served here")
	}
	idRetentionRaceDoor(t, ctx, fixture, "importrace", func(ctx context.Context, id string) (bool, error) {
		row := idRetentionOpenIssue(id, false)
		// Newer than the seeded row, so the import's stale guard never keeps
		// the stored row: a round the import wins really rewrites it.
		row.UpdatedAt = time.Now().UTC().Add(time.Minute)
		result, err := importer.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor:                idRetentionActor,
			Source:               "id-retention-contract",
			SkipPrefixValidation: true,
			Issues:               []*types.Issue{row},
		})
		if err != nil {
			return false, err
		}
		landed := result.Created == 1
		if landed && slices.Contains(result.RetiredIDs, id) {
			return true, fmt.Errorf("ImportBatch counted %s as created and reported it retired: %+v", id, result)
		}
		return landed, nil
	})
}

// RunIDRetentionRefusesANoHistoryRecreateThroughEveryDoor retires a
// no-history row — stored on the wisps plane, swept by the durable tier, the
// shape an orchestrator's operation rows take — and re-creates it, still
// no-history, through every door the fixture serves: each refuses it (the
// import skips and reports it) and nothing is written.
func RunIDRetentionRefusesANoHistoryRecreateThroughEveryDoor(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	retire := func(door string) string {
		t.Helper()
		issue := sweeperIssue(fixture.Sweep, "nohistdoor", door, false)
		issue.NoHistory = true
		if err := fixture.Sweep.CreateWisp(ctx, issue, idRetentionActor); err != nil {
			t.Fatalf("seeding %s: %v", issue.ID, err)
		}
		result := sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{Tier: publicops.SweepDurable, IDPattern: issue.ID, RetainIDs: true})
		if result.Swept != 1 {
			t.Fatalf("retaining sweep of %s Swept = %d, want 1", issue.ID, result.Swept)
		}
		idRetentionAssertTombstones(t, ctx, fixture, issue.ID, 0, 1)
		return issue.ID
	}
	noHistory := func(id string) *types.Issue {
		issue := idRetentionOpenIssue(id, false)
		issue.NoHistory = true
		return issue
	}
	doors := []struct {
		name   string
		served bool
		create func(id string) error
	}{
		{"lifecycle", true, func(id string) error {
			_, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{Actor: idRetentionActor, ForceIDPrefix: true, Issue: noHistory(id)})
			return err
		}},
		{"upsert", fixture.UpsertCreate != nil, func(id string) error {
			return fixture.UpsertCreate(ctx, noHistory(id), idRetentionActor)
		}},
		{"tx", fixture.TxCreate != nil, func(id string) error {
			return fixture.TxCreate(ctx, noHistory(id), idRetentionActor)
		}},
		{"apply", fixture.BatchApplier != nil, func(id string) error {
			_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
				Actor:         idRetentionActor,
				ForceIDPrefix: true,
				Items:         []publicops.ApplyItem{{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Issue: noHistory(id)}}},
			})
			return err
		}},
	}
	for _, door := range doors {
		if !door.served {
			t.Logf("fixture does not serve the %s door; not re-created through it", door.name)
			continue
		}
		id := retire(door.name)
		idRetentionAssertExpired(t, door.create(id), id)
		idRetentionAssertAbsent(t, ctx, fixture, id)
	}
	if fixture.Importer == nil {
		t.Log("fixture does not serve the import door; not re-imported through it")
		return
	}
	id := retire("import")
	result, err := fixture.Importer.ImportBatch(ctx, publicops.ImportBatchRequest{
		Actor:                idRetentionActor,
		Source:               "id-retention-contract",
		SkipPrefixValidation: true,
		Issues:               []*types.Issue{noHistory(id)},
	})
	if err != nil {
		t.Fatalf("ImportBatch(retired no-history row) = %v, want nil", err)
	}
	if len(result.RetiredIDs) != 1 || result.RetiredIDs[0] != id || result.Created != 0 {
		t.Errorf("ImportBatch(retired no-history row) = {Created:%d RetiredIDs:%v}, want {0 [%s]}", result.Created, result.RetiredIDs, id)
	}
	idRetentionAssertAbsent(t, ctx, fixture, id)
}

// RunIDRetentionAWispsPlaneRetainingSweepRetiresItsRows drives the exact
// request shape an orchestrator sweeping its operation rows sends: tier
// wisps-plane, RetainIDs, ProtectReferenced off and a Limit. The wisps-plane
// tier has its own candidate path on every backend, so the durable and
// ephemeral cases above do not cover it. A no-history row and an ephemeral
// row, both closed, are retired into wisp_id_tombstones with their closed_at,
// and a re-create of either is refused through Lifecycle.Create and, where
// the backend serves one, through the upsert-shaped create.
func RunIDRetentionAWispsPlaneRetainingSweepRetiresItsRows(t *testing.T, ctx context.Context, fixture IDRetentionFixture) {
	t.Helper()
	noHistory := sweeperIssue(fixture.Sweep, "wplane", "nohist", false)
	noHistory.NoHistory = true
	if err := fixture.Sweep.CreateWisp(ctx, noHistory, idRetentionActor); err != nil {
		t.Fatalf("seeding %s: %v", noHistory.ID, err)
	}
	ephemeral := sweeperIssue(fixture.Sweep, "wplane", "eph", true)
	if err := fixture.Sweep.CreateWisp(ctx, ephemeral, idRetentionActor); err != nil {
		t.Fatalf("seeding %s: %v", ephemeral.ID, err)
	}
	result := sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{
		Tier:              publicops.SweepWispsPlane,
		IDPattern:         sweeperPattern(fixture.Sweep, "wplane"),
		RetainIDs:         true,
		ProtectReferenced: false,
		Limit:             10,
	})
	if result.Swept != 2 || result.Remaining != 0 {
		t.Fatalf("wisps-plane retaining sweep: Swept = %d, Remaining = %d; want 2 and 0", result.Swept, result.Remaining)
	}
	if !result.RetainIDs {
		t.Errorf("result.RetainIDs = false, want the request echoed")
	}
	for _, id := range []string{noHistory.ID, ephemeral.ID} {
		idRetentionAssertTombstones(t, ctx, fixture, id, 0, 1)
		var stamped int
		if err := fixture.Sweep.QueryScalar(ctx, "SELECT COUNT(*) FROM wisp_id_tombstones WHERE id = ? AND closed_at = ?",
			[]any{id, sweeperClosedAt}, &stamped); err != nil {
			t.Fatalf("reading the tombstone closed_at of %s: %v", id, err)
		}
		if stamped != 1 {
			t.Errorf("wisp tombstone of %s does not carry the swept row's closed_at %s", id, sweeperClosedAt.Format(time.RFC3339))
		}
	}
	reissue := func(id string) *types.Issue {
		issue := idRetentionOpenIssue(id, id == ephemeral.ID)
		issue.NoHistory = id == noHistory.ID
		return issue
	}
	for _, id := range []string{noHistory.ID, ephemeral.ID} {
		_, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{Actor: idRetentionActor, ForceIDPrefix: true, Issue: reissue(id)})
		idRetentionAssertExpired(t, err, id)
		idRetentionAssertAbsent(t, ctx, fixture, id)
		if fixture.UpsertCreate == nil {
			t.Logf("fixture serves no upsert-shaped create; %s not re-created through it", id)
			continue
		}
		idRetentionAssertExpired(t, fixture.UpsertCreate(ctx, reissue(id), idRetentionActor), id)
		idRetentionAssertAbsent(t, ctx, fixture, id)
	}
}

// idRetentionRaceDoor runs idRetentionRaceRounds rounds of one door against
// the retaining sweep. Each round seeds its own closed row, starts the create
// and the sweep together, and delays one of them by a stagger that walks
// across the rounds (the create first in even rounds, the sweep in odd ones).
// create reports whether its row landed.
func idRetentionRaceDoor(t *testing.T, ctx context.Context, fixture IDRetentionFixture, tag string, create func(ctx context.Context, id string) (bool, error)) {
	t.Helper()
	sweepWon := 0
	defer func() {
		t.Logf("%s: the sweep won %d of %d rounds, the create the rest", tag, sweepWon, idRetentionRaceRounds)
	}()
	for round := range idRetentionRaceRounds {
		id := idRetentionSeedClosed(t, ctx, fixture, fmt.Sprintf("%s%d", tag, round), false)
		stagger := time.Duration(round/2) * 8 * time.Millisecond
		var createDelay, sweepDelay time.Duration
		if round%2 == 0 {
			sweepDelay = stagger
		} else {
			createDelay = stagger
		}

		var landed bool
		var createErr, sweepErr error
		swept := 0
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			time.Sleep(createDelay)
			landed, createErr = create(ctx, id)
		}()
		go func() {
			defer wg.Done()
			<-start
			time.Sleep(sweepDelay)
			// A sweep that loses a serialization or write conflict to the
			// create is retried: the property is about the create, and the
			// round needs the sweep to have run.
			for attempt := 0; attempt < 5; attempt++ {
				var result publicops.SweepResult
				result, sweepErr = fixture.Sweep.Sweeper.Sweep(ctx, publicops.SweepRequest{Tier: publicops.SweepDurable, IDPattern: id, RetainIDs: true})
				if sweepErr == nil {
					swept = result.Swept
					return
				}
			}
		}()
		close(start)
		wg.Wait()

		if sweepErr != nil {
			t.Fatalf("round %d: racing sweep of %s = %v", round, id, sweepErr)
		}
		live := idRetentionLiveRows(t, ctx, fixture, id)
		tombstones := idRetentionTombstoneRows(t, ctx, fixture, id)
		if live > 0 && tombstones > 0 {
			t.Fatalf("round %d: %s has a live row AND a tombstone (create landed=%v err=%v, Swept=%d): the create resurrected a retired id", round, id, landed, createErr, swept)
		}
		switch swept {
		case 1:
			sweepWon++
			if landed {
				t.Fatalf("round %d: the sweep retired %s and the racing create reported success (err=%v); a create racing the retaining sweep of its id must never land", round, id, createErr)
			}
			if live != 0 || tombstones != 1 {
				t.Fatalf("round %d: after the sweep won, %s has %d live rows and %d tombstones; want 0 and 1", round, id, live, tombstones)
			}
			// The only acceptable loss is the retired-id answer: the typed
			// refusal, or the import's skip-and-report (a nil error). Any
			// other failure means this round raced nothing.
			if createErr != nil && !errors.Is(createErr, publicops.ErrIdempotencyExpired) {
				t.Fatalf("round %d: create of %s = %v, want the typed refusal or a reported skip", round, id, createErr)
			}
		case 0:
			if !landed || live != 1 || tombstones != 0 {
				t.Fatalf("round %d: the sweep swept nothing, yet create landed=%v (err=%v) and %s has %d live rows and %d tombstones; want the create landed, 1 and 0", round, landed, createErr, id, live, tombstones)
			}
		default:
			t.Fatalf("round %d: sweep of exactly %s Swept = %d", round, id, swept)
		}
	}
}

// idRetentionLiveRows counts the rows holding id in both planes.
func idRetentionLiveRows(t *testing.T, ctx context.Context, fixture IDRetentionFixture, id string) int {
	t.Helper()
	var live int
	if err := fixture.Sweep.QueryScalar(ctx,
		"SELECT (SELECT COUNT(*) FROM issues WHERE id = ?) + (SELECT COUNT(*) FROM wisps WHERE id = ?)",
		[]any{id, id}, &live); err != nil {
		t.Fatalf("counting live rows of %s: %v", id, err)
	}
	return live
}

// idRetentionTombstoneRows counts the tombstones of id in both tables.
func idRetentionTombstoneRows(t *testing.T, ctx context.Context, fixture IDRetentionFixture, id string) int {
	t.Helper()
	var tombstones int
	if err := fixture.Sweep.QueryScalar(ctx,
		"SELECT (SELECT COUNT(*) FROM id_tombstones WHERE id = ?) + (SELECT COUNT(*) FROM wisp_id_tombstones WHERE id = ?)",
		[]any{id, id}, &tombstones); err != nil {
		t.Fatalf("counting tombstones of %s: %v", id, err)
	}
	return tombstones
}

// --- fixture helpers -------------------------------------------------------

func idRetentionID(fixture IDRetentionFixture, tag, name string) string {
	return fmt.Sprintf("%s-%s-%s", fixture.IssuePrefix, tag, name)
}

func idRetentionOpenIssue(id string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     "id retention " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

func idRetentionCreate(id string, ephemeral bool) publicops.CreateRequest {
	return publicops.CreateRequest{Actor: idRetentionActor, ForceIDPrefix: true, Issue: idRetentionOpenIssue(id, ephemeral)}
}

// idRetentionSeedClosed seeds one closed row, closed at sweeperClosedAt, on
// the plane ephemeral names.
func idRetentionSeedClosed(t *testing.T, ctx context.Context, fixture IDRetentionFixture, tag string, ephemeral bool) string {
	t.Helper()
	return sweeperSeedClosedIssue(t, ctx, fixture.Sweep, tag, ephemeral)
}

// idRetentionRetire seeds one closed row and retires it with a retaining sweep
// of exactly that id, on the tier that holds it.
func idRetentionRetire(t *testing.T, ctx context.Context, fixture IDRetentionFixture, tag string, ephemeral bool) string {
	t.Helper()
	id := idRetentionSeedClosed(t, ctx, fixture, tag, ephemeral)
	tier := publicops.SweepDurable
	if ephemeral {
		tier = publicops.SweepEphemeral
	}
	result := sweeperSweep(t, ctx, fixture.Sweep, publicops.SweepRequest{Tier: tier, IDPattern: id, RetainIDs: true})
	if result.Swept != 1 {
		t.Fatalf("retaining sweep of %s Swept = %d, want 1", id, result.Swept)
	}
	return id
}

// idRetentionAssertExpired asserts the typed refusal of a retired id.
func idRetentionAssertExpired(t *testing.T, err error, id string) {
	t.Helper()
	if err == nil {
		t.Fatalf("create of retired id %s = nil, want *IdempotencyExpiredError", id)
	}
	if !errors.Is(err, publicops.ErrIdempotencyExpired) {
		t.Fatalf("create of retired id %s = %v, want it to match ErrIdempotencyExpired", id, err)
	}
	var expired *publicops.IdempotencyExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("create of retired id %s = %v (%T), want an *IdempotencyExpiredError", id, err, err)
	}
	if expired.ID != id {
		t.Errorf("IdempotencyExpiredError.ID = %q, want %q", expired.ID, id)
	}
	if !expired.ClosedAt.Equal(sweeperClosedAt) {
		t.Errorf("IdempotencyExpiredError.ClosedAt = %s, want the swept row's closed_at %s", expired.ClosedAt, sweeperClosedAt)
	}
	if errors.Is(err, publicops.ErrAlreadyExists) || errors.Is(err, publicops.ErrValidation) {
		t.Errorf("create of retired id %s = %v also matches ErrAlreadyExists or ErrValidation; it must be its own refusal", id, err)
	}
}

// idRetentionAssertAbsent asserts no row holds id in either plane and the
// reader answers ErrNotFound.
func idRetentionAssertAbsent(t *testing.T, ctx context.Context, fixture IDRetentionFixture, id string) {
	t.Helper()
	sweeperAssertIssueRows(t, ctx, fixture.Sweep, 0, id)
	sweeperAssertWispRows(t, ctx, fixture.Sweep, 0, id)
	if fixture.Reader == nil {
		return
	}
	if _, err := fixture.Reader.Get(ctx, publicops.GetRequest{ID: id}); !errors.Is(err, publicops.ErrNotFound) {
		t.Errorf("Reader.Get(%s) = %v, want ErrNotFound for a retired id", id, err)
	}
}

// idRetentionAssertTombstones counts the tombstones of id in each table.
func idRetentionAssertTombstones(t *testing.T, ctx context.Context, fixture IDRetentionFixture, id string, durable, wisp int) {
	t.Helper()
	for _, want := range []struct {
		table string
		count int
	}{{"id_tombstones", durable}, {"wisp_id_tombstones", wisp}} {
		var got int
		if err := fixture.Sweep.QueryScalar(ctx, "SELECT COUNT(*) FROM "+want.table+" WHERE id = ?", []any{id}, &got); err != nil {
			t.Fatalf("counting %s rows for %s: %v", want.table, id, err)
		}
		if got != want.count {
			t.Errorf("%s rows for %s = %d, want %d", want.table, id, got, want.count)
		}
	}
}
