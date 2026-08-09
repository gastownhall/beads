package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The notifying wrapper over a REAL provider.
//
// The fakes elsewhere in this package pin the firing rules; they cannot pin
// what a hook script is actually HANDED, because the payload is assembled from
// reads the fakes answer themselves. These run against the real use cases and
// the real transaction, which is the only place a claim about the payload is
// worth anything — and the only place the import path's raw-statement seam is
// exercised at all.
//
// ONE PROVIDER FOR THE WHOLE SUITE (it boots a real Dolt sql-server) and no
// t.Parallel, matching TestImporterUOW next door.
func TestNotifyingProviderOverARealProvider(t *testing.T) {
	ctx := context.Background()
	inner := newUOWRoleFixtureProvider(t, ctx, "nfy")
	runner := &notifyRunner{}
	provider := NewNotifyingProvider(inner, Sinks{Hook: runner})

	create := func(t *testing.T, params domain.CreateIssueParams) {
		t.Helper()
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			_, err := uw.IssueUseCase().CreateIssue(ctx, params, "notify-test")
			return "bd: create issue", err
		}); err != nil {
			t.Fatalf("create %s: %v", params.Issue.ID, err)
		}
	}

	t.Run("PayloadCarriesLabels", func(t *testing.T) {
		create(t, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: "nfy-labels", Title: "Labelled", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "nfy-labels",
			CreateOnly: true,
		})

		runner.reset()
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			return "bd: label issue", uw.LabelUseCase().AddLabel(ctx, "nfy-labels", "lane:hooks", "notify-test")
		}); err != nil {
			t.Fatalf("add label: %v", err)
		}

		fired := runner.snapshots()
		if len(fired) != 1 {
			t.Fatalf("fired %d hooks, want 1: %v", len(fired), runner.events())
		}
		// The label the write just added has to be ON the payload. A hook
		// script routes on labels, and an unlabeled issue reads to it as an
		// unrouted one — the DoltStorage plumbing re-reads the issue before it
		// fires, so a script has always been handed a hydrated row.
		if got := fired[0].Labels; len(got) != 1 || got[0] != "lane:hooks" {
			t.Fatalf("payload labels = %v, want [lane:hooks]", got)
		}
	})

	t.Run("CreatePayloadCarriesItsInitialLabels", func(t *testing.T) {
		runner.reset()
		create(t, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: "nfy-initial", Title: "Born labelled", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "nfy-initial",
			Labels:     []string{"lane:initial"},
			CreateOnly: true,
		})

		fired := runner.snapshots()
		if len(fired) != 1 || runner.events()[0].event != hooks.EventCreate {
			t.Fatalf("fired %v, want one create", runner.events())
		}
		// The DoltStorage plumbing strips the labels off its on_create and
		// replays them as synthetic on_updates (divergence 1 in the file
		// header). This carries them on the create, which is the information
		// that mattered.
		if got := fired[0].Labels; len(got) != 1 || got[0] != "lane:initial" {
			t.Fatalf("create payload labels = %v, want [lane:initial]", got)
		}
	})

	t.Run("ReverseEdgeTellsTheFarEndWithItsGraph", func(t *testing.T) {
		create(t, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: "nfy-target", Title: "Existing", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "nfy-target",
			CreateOnly: true,
		})

		runner.reset()
		create(t, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: "nfy-source", Title: "New", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "nfy-source",
			CreateOnly: true,
			// `bd create --blocks nfy-target`: the edge LEAVES the existing
			// issue, so the create changed a row it did not create.
			Dependencies: []domain.DependencySpec{
				{Type: types.DepBlocks, TargetID: "nfy-target", SwapDirection: true},
			},
		})

		want := []firedHook{{hooks.EventCreate, "nfy-source"}, {hooks.EventUpdate, "nfy-target"}}
		assertFired(t, runner.events(), want)

		// And the far end's payload carries the edge the create wrote, which is
		// the whole reason its watchers are being told.
		assertCarriesEdgeTo(t, runner.snapshots()[1], "nfy-source")
	})

	t.Run("ForwardEdgeUpdatesTheCreatedRowWithItsGraph", func(t *testing.T) {
		create(t, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: "nfy-parent", Title: "Parent", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "nfy-parent",
			CreateOnly: true,
		})

		runner.reset()
		create(t, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: "nfy-child", Title: "Child", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "nfy-child",
			CreateOnly: true,
			// `bd create --parent nfy-parent`: the edge leaves the NEW row.
			ParentID: "nfy-parent",
		})

		// The create event carries the row; the update that follows carries the
		// row's GRAPH, which is the only event that does. The DoltStorage
		// plumbing fires the same pair (CompleteIssueOperationCreate then
		// dependencyHookEvents).
		want := []firedHook{{hooks.EventCreate, "nfy-child"}, {hooks.EventUpdate, "nfy-child"}}
		assertFired(t, runner.events(), want)
		assertCarriesEdgeTo(t, runner.snapshots()[1], "nfy-parent")
	})

	t.Run("ReverseEdgeAcrossPlanesStillTellsTheFarEnd", func(t *testing.T) {
		// The far end lives on the OTHER plane. A plane-pinned read of it would
		// miss and drop the notification silently, which is the failure this
		// pins: the snapshot resolves both planes.
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			_, err := uw.IssueUseCase().CreateWisp(ctx, domain.CreateIssueParams{
				Issue: &types.Issue{
					ID: "nfy-wisp-target", Title: "Ephemeral target", Status: types.StatusOpen,
					IssueType: types.TypeTask, Priority: 2, Ephemeral: true,
				},
				ExplicitID: "nfy-wisp-target",
				CreateOnly: true,
			}, "notify-test")
			return "bd: create wisp", err
		}); err != nil {
			t.Fatalf("create wisp: %v", err)
		}

		runner.reset()
		create(t, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: "nfy-cross", Title: "Durable source", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "nfy-cross",
			CreateOnly: true,
			Dependencies: []domain.DependencySpec{
				{Type: types.DepBlocks, TargetID: "nfy-wisp-target", SwapDirection: true},
			},
		})

		want := []firedHook{{hooks.EventCreate, "nfy-cross"}, {hooks.EventUpdate, "nfy-wisp-target"}}
		assertFired(t, runner.events(), want)
		assertCarriesEdgeTo(t, runner.snapshots()[1], "nfy-cross")
	})

	t.Run("GraphApplyReportsItsNodesAndEveryEdgeSource", func(t *testing.T) {
		create(t, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: "nfy-graph-live", Title: "Lives outside the plan", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			ExplicitID: "nfy-graph-live",
			CreateOnly: true,
		})

		runner.reset()
		plan := domain.GraphPlan{
			Nodes: []domain.GraphNode{
				{Key: "root", Issue: &types.Issue{ID: "nfy-graph-root", Title: "Plan root", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2}},
				{Key: "child", Issue: &types.Issue{ID: "nfy-graph-child", Title: "Plan child", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2}, ParentKey: "root"},
			},
			// The from-side is a row the plan did NOT create, so no create
			// event names it — only the edge update tells its watchers.
			Edges: []domain.GraphEdge{
				{FromID: "nfy-graph-live", ToKey: "root", Type: types.DepBlocks},
			},
		}
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			_, err := uw.IssueUseCase().ApplyIssueGraph(ctx, plan, "notify-test")
			return "bd: apply graph", err
		}); err != nil {
			t.Fatalf("ApplyIssueGraph: %v", err)
		}

		// Creates in node order, then one edge-carrying update per distinct
		// source: the child (its parent link) and the live row (the explicit
		// edge). The root is a create only — nothing leaves it.
		assertFired(t, runner.events(), []firedHook{
			{hooks.EventCreate, "nfy-graph-root"},
			{hooks.EventCreate, "nfy-graph-child"},
			{hooks.EventUpdate, "nfy-graph-child"},
			{hooks.EventUpdate, "nfy-graph-live"},
		})
		assertCarriesEdgeTo(t, runner.snapshots()[2], "nfy-graph-root")
		assertCarriesEdgeTo(t, runner.snapshots()[3], "nfy-graph-root")
	})

	t.Run("ImportRunsUnderTheWrapperAndFiresNothing", func(t *testing.T) {
		// The import role reaches the transaction's statement runner directly
		// (importer.go), which used to mean a type assertion on the concrete
		// unit of work — and that assertion FAILED through this wrapper, so
		// every proxied import in a workspace with hooks errored out. It peels
		// the decorator now.
		source, ok := provider.(ImporterSource)
		if !ok {
			t.Fatalf("wrapped provider %T does not offer the Importer accessor", provider)
		}
		importer, err := source.Importer()
		if err != nil {
			t.Fatalf("Importer(): %v", err)
		}

		runner.reset()
		result, err := importer.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor:  "notify-test",
			Source: "notifying_integration_test",
			Issues: []*types.Issue{
				{ID: "nfy-import-1", Title: "Imported one", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
				{ID: "nfy-import-2", Title: "Imported two", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
			},
		})
		if err != nil {
			t.Fatalf("ImportBatch through the wrapper: %v", err)
		}
		if result.Created != 2 {
			t.Fatalf("Created = %d, want 2", result.Created)
		}
		// Divergence 3 in the file header: the batch engine writes statements,
		// not use-case calls, so there is nothing for the recorder to see —
		// the same silence the DoltStorage plumbing's import keeps.
		if got := runner.events(); len(got) != 0 {
			t.Fatalf("import fired %v, want nothing", got)
		}

		// The rows really landed: an import that quietly wrote nothing would
		// satisfy every assertion above.
		if _, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
			issue, err := uw.IssueUseCase().GetIssue(ctx, "nfy-import-1")
			if err != nil {
				return struct{}{}, err
			}
			if issue == nil || issue.Title != "Imported one" {
				t.Fatalf("imported issue = %+v, want the row the batch named", issue)
			}
			return struct{}{}, nil
		}); err != nil {
			t.Fatalf("read back the imported issue: %v", err)
		}
	})
}

// assertCarriesEdgeTo fails unless the payload's dependency records name the
// far end of the edge the mutation wrote — the reason its watchers are being
// told at all.
func assertCarriesEdgeTo(t *testing.T, issue *types.Issue, dependsOn string) {
	t.Helper()
	for _, edge := range issue.Dependencies {
		if edge != nil && edge.DependsOnID == dependsOn {
			return
		}
	}
	t.Fatalf("payload for %s carried dependencies %+v, want the edge to %s", issue.ID, issue.Dependencies, dependsOn)
}
