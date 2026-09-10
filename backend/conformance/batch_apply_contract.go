package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.BatchApplier
// must satisfy. Each case asserts what issueops/batchapplier.go PROMISES, cited
// by SYMBOL, rather than what any one backend happens to do; a backend that
// genuinely disagrees is parked at its own wiring site with skipKnownDivergence
// so the case still runs on the ones that agree.
//
// THREE LEGS, TWO INDEPENDENT BODIES — and unlike TreeWalker and MetadataCAS
// that is a real two-vote contract rather than one reading plus two wrapper
// checks. dolt and embeddeddolt share
// internal/storage/issueops.ApplyBatchInTx, each wrapping it in its own
// transaction. The unit-of-work leg (internal/storage/uow.batchApplier) has its
// OWN body, and the reason is mechanical rather than chosen: the shared body
// composes issueops.ExecuteCreate, ExecuteUpdate and ExecuteClose, every one of
// which takes a *sql.Tx, while a unit of work's runner is a *sql.Conn with a
// transaction open on it. Neither publishes the other, so the store bodies
// cannot be reached from there without rewriting three of the oldest write
// paths in the tree to take an interface. So a per-leg failure here can be a
// second implementation genuinely disagreeing about what a batch MEANS, not
// only a wrapper losing a field.
//
// THE CASES ARE STILL WRITTEN THE CAREFUL WAY. They read RAW ROWS through
// QueryScalar rather than asking the role what it just did — a role-answer
// assertion passes on a corrupted table — they assert SENTINELS with errors.Is
// and typed error FIELDS with errors.As rather than message text, and they take
// history DELTAS around the call, because the seeds and the kit's own writes
// inflate every absolute count. Every refusal case carries a POSITIVE HALF: a
// body that refuses correctly and never writes anything at all satisfies
// refusal-only coverage of a guarded write.
//
// The parts that decide what a REQUEST means — the ref rules, the
// ExpectedVersion-on-a-retouched-row rule, the waits-for gate normalization —
// are pure and pinned without a database beside storage.PlanApplyBatch. What is
// left here is what only a real backend can show: what LANDED, what rolled
// back, and what the gate that runs after every item saw.
//
// WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN, AND WHY.
//
// THE ONE-TRANSACTION PROMISE. ApplyBatchRequest says the request IS the
// transaction boundary, and no case here proves it. The promise is STRUCTURAL
// rather than black-box observable: a single-threaded case cannot falsify it,
// because one transaction and five produce identical answers when nothing else
// is writing, and a concurrent case would be flaky at three engines — buying a
// red suite people learn to re-run rather than a guarantee. What holds it
// instead is the SHAPE of the two bodies: ApplyBatchInTx takes a transaction it
// did not open, and the unit-of-work body runs inside one RunTxResult, so there
// is no two-call composition to regress into without deleting them. The
// ROLLBACK cases below are the closest observable proxy and they are not the
// same claim — a body that opened a transaction per item and undid the earlier
// ones by hand would pass every one of them. The probe that would upgrade this
// is a transaction-counting seam on the fixture kit. Do not fake it with
// sleeps.
//
// ItemResult.Issue's HYDRATION-INSIDE-THE-TRANSACTION clause is unpinned for
// the same reason: that the snapshot was read on the writing transaction rather
// than after it is invisible to a single-threaded reader, and the fields it
// carries are already pinned by the raw-row assertions beside them.
//
// ItemResult.RowVersion's PARTIAL COVERAGE is stated as partial by the leaf
// itself ("the token is rewritten by claim, close, unclaim and the generic
// update path, and NOT by the direct-update paths"), so there is no total
// promise to assert. The cases use the token the way the leaf tells callers to
// — equality only, read from the raw row_lock column, never ordered.
//
// EVERY CASE NAMESPACES ITS IDS with the fixture's IssuePrefix plus its own tag,
// because the legs share one database across a role's cases. That prefix is not
// the workspace's configured one, so every request carrying an explicit id also
// carries ForceIDPrefix, exactly as the batch-create contract does.

// BatchApplyFixture supplies adapter-specific storage access for the
// apply-batch assertions. Every field but the last is named and typed exactly
// like the per-backend roleFixtureKit hook it is filled from.
type BatchApplyFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// BatchApplier is the surface under test.
	BatchApplier publicops.BatchApplier
	// CreateIssue seeds a durable issue in the issues plane. It is deliberately
	// NOT the role under test: a case that established its precondition through
	// ApplyBatch would be asserting against whatever that call did.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// QueryScalar runs a single-row query and scans it. It is how these cases
	// read the issues, wisps, dependencies and events rows, which is the only
	// way to tell "the answer looks right" from "the row is right".
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the cases
	// that need it SKIP LOUDLY with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
	// CountHistoryMatching is CountHistory scoped to what the entries READ, for
	// the one case that asserts a provenance label rather than a bare count. A
	// nil hook carries the same meaning as a nil CountHistory.
	CountHistoryMatching func(context.Context, string) (int, error)
	// CommitPending puts everything written so far into the version history, so
	// a later CountHistory delta measures the call under test and not the seed
	// that led up to it. It is built at each wiring site over a seam the backend
	// already publishes — OUT OF BAND, because the frozen kit reaches the issues
	// and config planes only and publishes no commit hook.
	//
	// A nil hook means the backend cannot settle its history on demand, and the
	// cases that need it SKIP LOUDLY with that reason.
	CommitPending func(context.Context) error
}

// RunBatchApplyAppliesEveryItemInDeclarationOrder pins the shape of a landed
// plan (BatchApplier.ApplyBatch, ApplyBatchResult.Items): four heterogeneous
// items — create, create, dep_add, close — apply in the order they were
// declared, the result mirrors them index for index, and every row and edge is
// in the raw tables afterwards.
//
// It is the one case that asserts the whole verb rather than one clause of it,
// and the raw reads are what make it more than an echo test: a body that
// answered a well-shaped ApplyBatchResult and wrote nothing satisfies every
// assertion made through the result alone.
func RunBatchApplyAppliesEveryItemInDeclarationOrder(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	blocked := fixture.IssuePrefix + "-order-blocked"
	blocker := fixture.IssuePrefix + "-order-blocker"

	result := batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("blocked", batchApplyIssue(blocked, "the blocked step")),
			batchApplyCreate("blocker", batchApplyIssue(blocker, "the blocking step")),
			batchApplyDepAdd(publicops.Ref{Key: "blocked"}, publicops.Ref{Key: "blocker"}, publicops.DepBlocks, ""),
			batchApplyClose(publicops.Ref{Key: "blocker"}),
		},
	})

	if len(result.Items) != 4 {
		t.Fatalf("ApplyBatch(4 items) returned %d item results; Items has exactly one entry per requested item", len(result.Items))
	}
	for i, want := range []publicops.ItemKind{
		publicops.ItemCreate, publicops.ItemCreate, publicops.ItemDepAdd, publicops.ItemClose,
	} {
		if result.Items[i].Kind != want {
			t.Errorf("result item %d kind = %q, want %q: Items is promised in REQUEST ORDER", i, result.Items[i].Kind, want)
		}
	}
	if result.Items[2].IssueID != blocked || result.Items[2].DependsOnID != blocker {
		t.Errorf("dep_add item reported %s -> %s, want %s -> %s: IssueID is the edge's SOURCE and DependsOnID its target",
			result.Items[2].IssueID, result.Items[2].DependsOnID, blocked, blocker)
	}

	assertBatchApplyRowCount(t, ctx, fixture, "issues", blocked, 1)
	assertBatchApplyRowCount(t, ctx, fixture, "issues", blocker, 1)
	assertBatchApplyEdgeCount(t, ctx, fixture, blocked, blocker, 1)
	if got := batchApplyColumn(t, ctx, fixture, "status", blocker); got != string(types.StatusClosed) {
		t.Errorf("%s status = %q after a close item, want %q", blocker, got, types.StatusClosed)
	}
	if got := batchApplyColumn(t, ctx, fixture, "status", blocked); got == string(types.StatusClosed) {
		t.Errorf("%s was closed by a request that only asked to close %s", blocked, blocker)
	}
}

// RunBatchApplyBindsEachNamedKeyToItsMintedID pins ApplyBatchResult.Keys: it is
// "the one fact the request cannot carry and every caller needs", and it
// carries only the keys the request NAMED — CreateItem.Key is optional, so an
// unnamed create is in Items and not in Keys.
//
// The ids are MINTED rather than explicit, because a Keys map filled from the
// request's own ids would be true of nothing.
func RunBatchApplyBindsEachNamedKeyToItsMintedID(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	result := batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyCreate("named", batchApplyMintedIssue("batch apply keys named")),
			batchApplyCreate("", batchApplyMintedIssue("batch apply keys anonymous")),
		},
	})

	if len(result.Keys) != 1 {
		t.Fatalf("Keys = %v, want exactly the one key the request named: an unnamed create item is in Items and not here", result.Keys)
	}
	minted, ok := result.Keys["named"]
	if !ok || minted == "" {
		t.Fatalf("Keys[%q] = %q, want the id the create was bound to", "named", minted)
	}
	if minted != result.Items[0].IssueID {
		t.Errorf("Keys[%q] = %q but item 0 reports %q; the key names the row that item minted", "named", minted, result.Items[0].IssueID)
	}
	if result.Items[1].IssueID == "" {
		t.Error("the unnamed create reported no id; Keys carries only NAMED creates, Items carries every one")
	}
	if result.Items[1].IssueID == minted {
		t.Errorf("both creates minted %q", minted)
	}
	// Keys must name a row that exists, which is what a caller does with it.
	assertBatchApplyRowCount(t, ctx, fixture, "issues", minted, 1)
	assertBatchApplyRowCount(t, ctx, fixture, "issues", result.Items[1].IssueID, 1)
}

// RunBatchApplyResolvesABackwardKeyRef pins Ref's whole reason for existing: a
// later item addresses a row by the Key an EARLIER create item gave itself,
// without knowing the id the request has not minted yet.
//
// The create carries an EXPLICIT id, so the assertion below reads a row this
// case named rather than a row the role reported. A body that resolved the key
// to the wrong row, or to nothing, cannot pass an assertion that never asks it
// where the row went.
func RunBatchApplyResolvesABackwardKeyRef(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-backref-target"

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("target", batchApplyIssue(id, "created under a key")),
			batchApplyUpdate(publicops.Ref{Key: "target"}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "patched through the key"},
			}),
		},
	})

	if got := batchApplyColumn(t, ctx, fixture, "title", id); got != "patched through the key" {
		t.Errorf("%s title = %q, want the patch the keyed update carried; a backward Ref.Key resolves to the row the earlier create minted", id, got)
	}
}

// RunBatchApplyRefusesAKeyDeclaredLater pins the backward-only rule and the
// diagnosis that goes with it (Ref, RefError.DeclaredLater): a target key
// declared by a LATER item is an ORDERING mistake, told apart from a typo so a
// caller can fix the right thing, and it is raised before anything is written.
func RunBatchApplyRefusesAKeyDeclaredLater(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	later := fixture.IssuePrefix + "-fwdref-later"

	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyUpdate(publicops.Ref{Key: "later"}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "reaches forward"},
			}),
			batchApplyCreate("later", batchApplyIssue(later, "declared after the reference")),
		},
	})

	var refErr *publicops.RefError
	if !errors.As(err, &refErr) {
		t.Fatalf("a target ref naming a key declared later: error = %v, want *RefError", err)
	}
	if !refErr.DeclaredLater {
		t.Errorf("RefError = %#v, want DeclaredLater true: the key IS declared, by a later item, which is a different diagnosis from a typo", refErr)
	}
	if refErr.Key != "later" {
		t.Errorf("RefError.Key = %q, want %q", refErr.Key, "later")
	}
	if refErr.Index != 0 {
		t.Errorf("RefError.Index = %d, want 0: the index names the item HOLDING the bad ref", refErr.Index)
	}
	if !errors.Is(err, publicops.ErrValidation) {
		t.Errorf("error = %v, want it to match ErrValidation through RefError.Unwrap, so a front door classifies it without knowing the type", err)
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", later, 0)
}

// RunBatchApplyRefusesAKeyNoItemDeclares pins the OTHER diagnosis
// (RefError.DeclaredLater false): nothing in the request declares the key at
// all, which is a typo or a missing item and not an ordering mistake.
//
// It is a case of its own rather than an arm of the one above because the two
// values are what a caller ACTS on differently, and a body that hard-coded
// either flag would pass one of the two.
func RunBatchApplyRefusesAKeyNoItemDeclares(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	declared := fixture.IssuePrefix + "-ghostref-declared"

	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("declared", batchApplyIssue(declared, "the only key this request declares")),
			batchApplyUpdate(publicops.Ref{Key: "never-declared"}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "unreachable"},
			}),
		},
	})

	var refErr *publicops.RefError
	if !errors.As(err, &refErr) {
		t.Fatalf("a target ref naming an undeclared key: error = %v, want *RefError", err)
	}
	if refErr.DeclaredLater {
		t.Errorf("RefError = %#v, want DeclaredLater false: no item in this request declares %q", refErr, refErr.Key)
	}
	if !errors.Is(err, publicops.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", declared, 0)
}

// RunBatchApplyRefusesARefNamingNeitherOrBoth pins Ref's exactly-one rule:
// "both set is a caller that cannot say which it meant, neither set is a
// reference to nothing, and both are ErrValidation before anything is written."
//
// The POSITIVE half is the third arm — the same item shape with exactly one
// member set lands — because a body that refused every ref would pass the two
// refusals on its own.
func RunBatchApplyRefusesARefNamingNeitherOrBoth(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	seeded := fixture.IssuePrefix + "-refshape-seed"
	batchApplySeedIssue(t, ctx, fixture, seeded, types.StatusOpen)

	patch := publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "refshape"}}
	for _, test := range []struct {
		name string
		tag  string
		ref  publicops.Ref
	}{
		{"neither member", "neither", publicops.Ref{}},
		{"both members", "both", publicops.Ref{Key: "declared", ID: seeded}},
	} {
		t.Run(test.name, func(t *testing.T) {
			unreachable := fixture.IssuePrefix + "-refshape-" + test.tag
			_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
				Actor:         "apply-writer",
				ForceIDPrefix: true,
				Items: []publicops.ApplyItem{
					batchApplyCreate("declared", batchApplyIssue(unreachable, "unreachable")),
					batchApplyUpdate(test.ref, patch),
				},
			})
			if !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("a ref naming %s: error = %v, want ErrValidation", test.name, err)
			}
			assertBatchApplyRowCount(t, ctx, fixture, "issues", unreachable, 0)
		})
	}

	// The positive half: one member set, and the identical shape lands.
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyUpdate(publicops.Ref{ID: seeded}, patch),
		},
	})
	if got := batchApplyColumn(t, ctx, fixture, "title", seeded); got != "refshape" {
		t.Errorf("%s title = %q after a well-formed id ref, want %q", seeded, got, "refshape")
	}
}

// RunBatchApplyRollsBackEverythingWhenTheLastItemRefuses is the case this role
// exists for (BatchApplier: "IT IS ALL OR NOTHING", and ApplyBatchRequest's
// transaction-boundary clause): the item that refuses is the LAST one, so
// everything before it has already been written, and the raw tables have to
// come back empty anyway.
//
// THE POSITIVE HALF IS THE OTHER HALF OF THE TEST, and it re-runs the identical
// batch with the refusing item made legal. A body that refused every request
// and wrote nothing at all passes the rollback assertions alone; only the
// positive half says the earlier items were ever capable of landing. It reuses
// the same ids deliberately: the rollback freed them, which is itself the
// claim.
func RunBatchApplyRollsBackEverythingWhenTheLastItemRefuses(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	occupied := fixture.IssuePrefix + "-rbseed-occupied"
	batchApplySeedIssue(t, ctx, fixture, occupied, types.StatusOpen)

	first := fixture.IssuePrefix + "-rb-first"
	second := fixture.IssuePrefix + "-rb-second"
	fresh := fixture.IssuePrefix + "-rb-fresh"
	idSpace := fixture.IssuePrefix + "-rb-%"

	batch := func(last string) publicops.ApplyBatchRequest {
		return publicops.ApplyBatchRequest{
			Actor:         "apply-writer",
			ForceIDPrefix: true,
			Items: []publicops.ApplyItem{
				batchApplyCreate("first", batchApplyIssue(first, "lands before the refusal")),
				batchApplyCreate("second", batchApplyIssue(second, "also lands before it")),
				batchApplyDepAdd(publicops.Ref{Key: "first"}, publicops.Ref{Key: "second"}, publicops.DepBlocks, ""),
				batchApplyCreate("last", batchApplyIssue(last, "the last item")),
			},
		}
	}

	_, err := fixture.BatchApplier.ApplyBatch(ctx, batch(occupied))
	if !errors.Is(err, publicops.ErrAlreadyExists) {
		t.Fatalf("a create over an occupied id: error = %v, want ErrAlreadyExists", err)
	}
	var itemErr *publicops.ItemError
	if !errors.As(err, &itemErr) {
		t.Fatalf("error = %v, want an *ItemError naming the item that refused", err)
	}
	if itemErr.Index != 3 || itemErr.Kind != publicops.ItemCreate {
		t.Errorf("ItemError = %#v, want Index 3 and Kind %q", itemErr, publicops.ItemCreate)
	}
	if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM issues WHERE id LIKE ?", []any{idSpace}); got != 0 {
		t.Errorf("%d issue row(s) survive in %q after a refusal at the LAST item, want none: a batch that could not apply every item applied none", got, idSpace)
	}
	if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM dependencies WHERE issue_id LIKE ?", []any{idSpace}); got != 0 {
		t.Errorf("%d dependency row(s) survive in %q after a refusal, want none", got, idSpace)
	}
	// The seeded row is untouched: an upsert would report the same error having
	// rewritten every column of it.
	if got := batchApplyColumn(t, ctx, fixture, "title", occupied); got != occupied {
		t.Errorf("the occupied row's title = %q, want %q: the refusal must not have written through it", got, occupied)
	}

	batchApplyMust(t, ctx, fixture, batch(fresh))
	if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM issues WHERE id LIKE ?", []any{idSpace}); got != 3 {
		t.Errorf("%d issue row(s) in %q after the same batch with a legal last item, want 3: "+
			"the rollback half is only a test if the batch could land", got, idSpace)
	}
	assertBatchApplyEdgeCount(t, ctx, fixture, first, second, 1)
}

// RunBatchApplyNeverReordersItsItems pins ApplyBatchRequest.Items' "ORDER IS
// NEVER CHANGED" clause against the one reordering that would be tempting:
// DependencyEditor applies every parent-child edge before any blocking one, and
// this role refuses to, because the items are not all edges.
//
// The request closes a parent and only THEN gives it a child. Under
// DependencyEditor's parent-child-first pass the close would run against a
// parent that already had an open child and refuse with
// *CloseOpenChildrenError, so a body that reordered cannot pass this — which is
// what makes a case about ordering able to fail at all. The unforced close
// landing is the assertion; the edge and the surviving closed status are what
// prove the later item ran.
func RunBatchApplyNeverReordersItsItems(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	parent := fixture.IssuePrefix + "-noreorder-parent"
	child := fixture.IssuePrefix + "-noreorder-child"

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("parent", batchApplyIssue(parent, "closed before it has a child")),
			batchApplyClose(publicops.Ref{Key: "parent"}),
			batchApplyCreate("child", batchApplyIssue(child, "attached after the close")),
			batchApplyDepAdd(publicops.Ref{Key: "child"}, publicops.Ref{Key: "parent"}, publicops.DepParentChild, ""),
		},
	})

	if got := batchApplyColumn(t, ctx, fixture, "status", parent); got != string(types.StatusClosed) {
		t.Errorf("%s status = %q, want %q: the close ran at ITS OWN position, before the edge that would have refused it", parent, got, types.StatusClosed)
	}
	if got := batchApplyColumn(t, ctx, fixture, "status", child); got == string(types.StatusClosed) {
		t.Errorf("%s was closed; only the parent was asked to close", child)
	}
	assertBatchApplyTypedEdgeCount(t, ctx, fixture, child, parent, string(publicops.DepParentChild), 1)
}

// RunBatchApplyEndGateRefusesAHierarchyTheRequestBuilt is the case the END GATE
// exists for (BatchApplier: "ORDER IS THE CONTRACT … So this role runs a REAL
// END GATE instead", and ApplyBatchRequest.SkipPerEdgeCycleCheck's "It NEVER
// drops the whole-graph gate that runs once at the end").
//
// EVERY EDGE IS LEGAL WHEN IT IS WRITTEN. The blocking edge goes in first, when
// no parent-child edge exists and the child has no ancestors; the two
// parent-child edges go in after it, and CheckBlockingHierarchyInTx returns
// early for a non-blocking type, so the per-edge probe cannot see what the pair
// built. Only a re-validation of every scheduling edge against the closure the
// WHOLE request produced catches it, and nothing else in the tree runs one:
// DependencyEditor dodges the same hole by REORDERING, which this role has
// refused to do.
//
// The graph is deliberately acyclic (child -> parent -> grandparent, plus child
// -> grandparent), so the cycle half of the gate cannot be what refuses it.
//
// POSITIVE HALF: the same three edges with the first one non-blocking all land,
// because a hierarchy is only a conflict for an edge that GATES.
func RunBatchApplyEndGateRefusesAHierarchyTheRequestBuilt(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	grand := fixture.IssuePrefix + "-endgate-grand"
	parent := fixture.IssuePrefix + "-endgate-parent"
	child := fixture.IssuePrefix + "-endgate-child"
	for _, id := range []string{grand, parent, child} {
		batchApplySeedIssue(t, ctx, fixture, id, types.StatusOpen)
	}

	gateBatch := func(first publicops.DependencyType) publicops.ApplyBatchRequest {
		return publicops.ApplyBatchRequest{
			Actor: "apply-writer",
			Items: []publicops.ApplyItem{
				batchApplyDepAdd(publicops.Ref{ID: child}, publicops.Ref{ID: grand}, first, ""),
				batchApplyDepAdd(publicops.Ref{ID: child}, publicops.Ref{ID: parent}, publicops.DepParentChild, ""),
				batchApplyDepAdd(publicops.Ref{ID: parent}, publicops.Ref{ID: grand}, publicops.DepParentChild, ""),
			},
		}
	}

	_, err := fixture.BatchApplier.ApplyBatch(ctx, gateBatch(publicops.DepBlocks))
	var conflict *publicops.DependencyHierarchyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("a blocking edge legal on its own and illegal in the graph this request built: error = %v, "+
			"want *DependencyHierarchyConflictError from the END GATE", err)
	}
	if conflict.IssueID != child || conflict.BlockerID != grand {
		t.Errorf("conflict = %#v, want IssueID %s gated on BlockerID %s", conflict, child, grand)
	}
	if !conflict.BlockerIsAncestor {
		t.Errorf("conflict = %#v, want BlockerIsAncestor true: %s reaches %s through the parent-child edges this request added",
			conflict, grand, child)
	}
	var itemErr *publicops.ItemError
	if !errors.As(err, &itemErr) {
		t.Fatalf("error = %v, want an *ItemError: the hierarchy half of the gate IS per edge, so it names the item that carried it", err)
	}
	if itemErr.Index != 0 || itemErr.Kind != publicops.ItemDepAdd {
		t.Errorf("ItemError = %#v, want Index 0 and Kind %q — the blocking edge, at the position the caller declared it",
			itemErr, publicops.ItemDepAdd)
	}
	for _, edge := range [][2]string{{child, grand}, {child, parent}, {parent, grand}} {
		assertBatchApplyEdgeCount(t, ctx, fixture, edge[0], edge[1], 0)
	}

	batchApplyMust(t, ctx, fixture, gateBatch(publicops.DepRelated))
	for _, edge := range [][2]string{{child, grand}, {child, parent}, {parent, grand}} {
		assertBatchApplyEdgeCount(t, ctx, fixture, edge[0], edge[1], 1)
	}
}

// RunBatchApplyEndGateCycleSurvivesSkipPerEdgeCycleCheck pins the other half of
// the same clause: SkipPerEdgeCycleCheck "drops the PER-EDGE cycle probe … It
// NEVER drops the whole-graph gate that runs once at the end."
//
// With the per-edge probe off, both edges of the cycle are WRITTEN before
// anything checks them, so the gate is the only thing between the caller and a
// stored cycle — and the rollback is what proves it ran rather than shrugged.
//
// POSITIVE HALF: an acyclic pair with the same flag lands, so the case cannot
// pass on a body that refuses every skipped-probe request.
func RunBatchApplyEndGateCycleSurvivesSkipPerEdgeCycleCheck(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-skipcycle-first"
	second := fixture.IssuePrefix + "-skipcycle-second"
	third := fixture.IssuePrefix + "-skipcycle-third"
	for _, id := range []string{first, second, third} {
		batchApplySeedIssue(t, ctx, fixture, id, types.StatusOpen)
	}

	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:                 "apply-writer",
		SkipPerEdgeCycleCheck: true,
		Items: []publicops.ApplyItem{
			batchApplyDepAdd(publicops.Ref{ID: first}, publicops.Ref{ID: second}, publicops.DepBlocks, ""),
			batchApplyDepAdd(publicops.Ref{ID: second}, publicops.Ref{ID: first}, publicops.DepBlocks, ""),
		},
	})
	if !errors.Is(err, publicops.ErrDependencyCycle) {
		t.Fatalf("a cycle written with the per-edge probe skipped: error = %v, want ErrDependencyCycle from the end gate", err)
	}
	assertBatchApplyEdgeCount(t, ctx, fixture, first, second, 0)
	assertBatchApplyEdgeCount(t, ctx, fixture, second, first, 0)

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:                 "apply-writer",
		SkipPerEdgeCycleCheck: true,
		Items: []publicops.ApplyItem{
			batchApplyDepAdd(publicops.Ref{ID: first}, publicops.Ref{ID: second}, publicops.DepBlocks, ""),
			batchApplyDepAdd(publicops.Ref{ID: second}, publicops.Ref{ID: third}, publicops.DepBlocks, ""),
		},
	})
	assertBatchApplyEdgeCount(t, ctx, fixture, first, second, 1)
	assertBatchApplyEdgeCount(t, ctx, fixture, second, third, 1)
}

// RunBatchApplyExpectedVersionThatMatchesLetsTheItemThrough is the positive
// half of UpdateItem.ExpectedVersion, and it has to come first: every refusal
// case below is only a test if a matching guard is capable of passing.
//
// The token is read from the raw row_lock column rather than from a previous
// result, and compared for EQUALITY only, which is the whole of what
// ItemResult.RowVersion promises.
func RunBatchApplyExpectedVersionThatMatchesLetsTheItemThrough(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	guarded := fixture.IssuePrefix + "-versionok-guarded"
	sibling := fixture.IssuePrefix + "-versionok-sibling"
	batchApplySeedIssue(t, ctx, fixture, guarded, types.StatusOpen)

	current := batchApplyRowVersion(t, ctx, fixture, guarded)
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("sibling", batchApplyIssue(sibling, "lands beside the guarded update")),
			batchApplyGuardedUpdate(publicops.Ref{ID: guarded}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "guard held"},
			}, &current, nil, nil),
		},
	})

	if got := batchApplyColumn(t, ctx, fixture, "title", guarded); got != "guard held" {
		t.Errorf("%s title = %q, want %q: a matching ExpectedVersion lets the item through", guarded, got, "guard held")
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", sibling, 1)
}

// RunBatchApplyStaleExpectedVersionRefusesTheWholeRequest pins the clause that
// separates this role from MetadataCAS (UpdateItem.ExpectedVersion: "A MISS
// REFUSES THE WHOLE REQUEST — the opposite of MetadataCAS, and deliberately").
//
// The sibling create is the assertion. A body that refused only the guarded
// item and committed the rest would leave "a shape nobody asked for", and the
// error alone cannot tell that apart from a whole-request refusal.
func RunBatchApplyStaleExpectedVersionRefusesTheWholeRequest(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	guarded := fixture.IssuePrefix + "-versionstale-guarded"
	sibling := fixture.IssuePrefix + "-versionstale-sibling"
	batchApplySeedIssue(t, ctx, fixture, guarded, types.StatusOpen)

	stale := batchApplyRowVersion(t, ctx, fixture, guarded) + 1
	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("sibling", batchApplyIssue(sibling, "must not survive the refusal")),
			batchApplyGuardedUpdate(publicops.Ref{ID: guarded}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "clobbered"},
			}, &stale, nil, nil),
		},
	})

	if !errors.Is(err, publicops.ErrVersionMismatch) {
		t.Fatalf("a stale ExpectedVersion: error = %v, want ErrVersionMismatch", err)
	}
	var itemErr *publicops.ItemError
	if !errors.As(err, &itemErr) {
		t.Fatalf("error = %v, want the refusal wrapped in an *ItemError naming the item", err)
	}
	if itemErr.Index != 1 || itemErr.IssueID != guarded {
		t.Errorf("ItemError = %#v, want Index 1 acting on %s", itemErr, guarded)
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", sibling, 0)
	if got := batchApplyColumn(t, ctx, fixture, "title", guarded); got != guarded {
		t.Errorf("%s title = %q, want the seeded %q: a refused guard writes nothing", guarded, got, guarded)
	}
}

// RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemTouched pins
// UpdateItem.ExpectedVersion's static rule: a guard on a row an earlier item of
// the SAME request already wrote "is ErrValidation, checked before anything is
// written", because "the token is server-minted and rewritten by the write, so
// a caller cannot know what it would be mid-request".
//
// Both verbs that carry the guard are asserted, because the rule is
// CloseItem.ExpectedVersion's too ("UpdateItem.ExpectedVersion's
// already-touched rule applies here identically") and the two are separate
// construction sites.
//
// It is ErrValidation rather than ErrVersionMismatch on purpose: letting it
// through "would answer every such request with ErrVersionMismatch and leave
// the caller looking for a concurrent writer that does not exist", so this case
// asserts WHICH refusal as hard as it asserts that one happened.
func RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemTouched(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	patch := publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "first write"}}

	for _, test := range []struct {
		name  string
		tag   string
		build func(id string, version *int64) []publicops.ApplyItem
	}{
		{"after an update", "afterupdate", func(id string, version *int64) []publicops.ApplyItem {
			return []publicops.ApplyItem{
				batchApplyUpdate(publicops.Ref{ID: id}, patch),
				batchApplyGuardedUpdate(publicops.Ref{ID: id}, publicops.IssuePatch{
					Title: publicops.Field[string]{Set: true, Value: "second write"},
				}, version, nil, nil),
			}
		}},
		{"after a close", "afterclose", func(id string, version *int64) []publicops.ApplyItem {
			return []publicops.ApplyItem{
				batchApplyClose(publicops.Ref{ID: id}),
				batchApplyGuardedUpdate(publicops.Ref{ID: id}, patch, version, nil, nil),
			}
		}},
		{"on a close item", "oncloseitem", func(id string, version *int64) []publicops.ApplyItem {
			return []publicops.ApplyItem{
				batchApplyUpdate(publicops.Ref{ID: id}, patch),
				batchApplyGuardedClose(publicops.Ref{ID: id}, version),
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := fixture.IssuePrefix + "-touched-" + test.tag
			batchApplySeedIssue(t, ctx, fixture, id, types.StatusOpen)
			version := batchApplyRowVersion(t, ctx, fixture, id)

			_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
				Actor: "apply-writer",
				Items: test.build(id, &version),
			})
			if !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("guarding on a row this request already wrote (%s): error = %v, want ErrValidation", test.name, err)
			}
			if errors.Is(err, publicops.ErrVersionMismatch) {
				t.Errorf("the refusal (%s) matched ErrVersionMismatch: this is a request-SHAPE rule, not a lost race, "+
					"and answering it as a race sends the caller looking for a concurrent writer that does not exist", test.name)
			}
			// Nothing was written, which is what "checked before anything is
			// written" means: the FIRST item's patch must not have landed.
			if got := batchApplyColumn(t, ctx, fixture, "title", id); got != id {
				t.Errorf("%s title = %q, want the seeded %q: the static check runs before the transaction opens", id, got, id)
			}
			if got := batchApplyColumn(t, ctx, fixture, "status", id); got != string(types.StatusOpen) {
				t.Errorf("%s status = %q, want %q", id, got, types.StatusOpen)
			}
		})
	}
}

// RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemCreated pins the same
// rule's second sentence (UpdateItem.ExpectedVersion): "Guarding on a row an
// earlier item created is the same case: the row did not exist when the caller
// composed the request."
//
// It is separate from the case above because the two are separate branches of
// the touched set — one keyed by id, one keyed by KEY (storage.PlanApplyBatch
// keeps the two namespaces apart so a key and an id spelled the same are not
// confused for one row) — and a body that recorded only mutations would pass
// the other case and fail this one.
func RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemCreated(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	minted := fixture.IssuePrefix + "-createdguard-minted"
	var version int64 = 1

	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("minted", batchApplyIssue(minted, "created by this request")),
			batchApplyGuardedUpdate(publicops.Ref{Key: "minted"}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "guarded on a row that did not exist"},
			}, &version, nil, nil),
		},
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("guarding on a row this request created: error = %v, want ErrValidation", err)
	}
	if errors.Is(err, publicops.ErrVersionMismatch) {
		t.Error("the refusal matched ErrVersionMismatch; a row this request created never had a token the caller could read")
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", minted, 0)
}

// RunBatchApplyEvaluatesExpectedStatusAsModified pins the AS-MODIFIED clause
// (UpdateItem.ExpectedStatus): the guards are read "against the row as THIS
// REQUEST has already changed it at this item's position, not against the row
// as it was when the request began", and unlike ExpectedVersion the status
// guard carries no already-touched rule — "a caller CAN know it wants the
// status its own earlier item set".
//
// The negative arm is what makes it a test of AS-MODIFIED rather than of
// guards in general: a body evaluating against the PRE-request snapshot passes
// the negative arm and fails the positive one, and a body that ignored the
// guard entirely passes the positive arm and fails the negative one. Neither
// arm alone can tell those apart.
func RunBatchApplyEvaluatesExpectedStatusAsModified(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	inProgress := types.StatusInProgress
	open := types.StatusOpen

	held := fixture.IssuePrefix + "-statusguard-held"
	batchApplySeedIssue(t, ctx, fixture, held, types.StatusOpen)
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyUpdate(publicops.Ref{ID: held}, publicops.IssuePatch{
				Status: publicops.Field[types.Status]{Set: true, Value: inProgress},
			}),
			batchApplyGuardedUpdate(publicops.Ref{ID: held}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "guarded on what this request set"},
			}, nil, &inProgress, nil),
		},
	})
	if got := batchApplyColumn(t, ctx, fixture, "title", held); got != "guarded on what this request set" {
		t.Errorf("%s title = %q, want the second item's patch: an item guarding on what an earlier item wrote is asking a coherent question", held, got)
	}
	if got := batchApplyColumn(t, ctx, fixture, "status", held); got != string(inProgress) {
		t.Errorf("%s status = %q, want %q", held, got, inProgress)
	}

	stale := fixture.IssuePrefix + "-statusguard-stale"
	batchApplySeedIssue(t, ctx, fixture, stale, types.StatusOpen)
	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyUpdate(publicops.Ref{ID: stale}, publicops.IssuePatch{
				Status: publicops.Field[types.Status]{Set: true, Value: inProgress},
			}),
			batchApplyGuardedUpdate(publicops.Ref{ID: stale}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "never lands"},
			}, nil, &open, nil),
		},
	})
	if !errors.Is(err, publicops.ErrStatusMismatch) {
		t.Fatalf("guarding on the PRE-request status: error = %v, want ErrStatusMismatch", err)
	}
	if got := batchApplyColumn(t, ctx, fixture, "status", stale); got != string(types.StatusOpen) {
		t.Errorf("%s status = %q, want the seeded %q: a guard miss refuses the WHOLE request, so the first item rolled back too", stale, got, types.StatusOpen)
	}
	if got := batchApplyColumn(t, ctx, fixture, "title", stale); got != stale {
		t.Errorf("%s title = %q, want the seeded %q", stale, got, stale)
	}
}

// RunBatchApplyEvaluatesExpectedAssigneeAsModified is the assignee half of the
// same clause (UpdateItem.ExpectedAssignee), and it is its own case because it
// is a second construction site of the same rule with its own sentinel: the
// refusal is ErrAssigneeMismatch, and a body that routed only the status guard
// through the as-modified read would pass the case above and fail this one.
func RunBatchApplyEvaluatesExpectedAssigneeAsModified(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	holder := "apply-holder"
	unassigned := ""

	held := fixture.IssuePrefix + "-assigneeguard-held"
	batchApplySeedIssue(t, ctx, fixture, held, types.StatusOpen)
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyUpdate(publicops.Ref{ID: held}, publicops.IssuePatch{
				Assignee: publicops.Field[string]{Set: true, Value: holder},
			}),
			batchApplyGuardedUpdate(publicops.Ref{ID: held}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "guarded on the holder this request set"},
			}, nil, nil, &holder),
		},
	})
	if got := batchApplyColumn(t, ctx, fixture, "title", held); got != "guarded on the holder this request set" {
		t.Errorf("%s title = %q, want the second item's patch", held, got)
	}
	if got := batchApplyColumn(t, ctx, fixture, "assignee", held); got != holder {
		t.Errorf("%s assignee = %q, want %q", held, got, holder)
	}

	stale := fixture.IssuePrefix + "-assigneeguard-stale"
	batchApplySeedIssue(t, ctx, fixture, stale, types.StatusOpen)
	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyUpdate(publicops.Ref{ID: stale}, publicops.IssuePatch{
				Assignee: publicops.Field[string]{Set: true, Value: holder},
			}),
			batchApplyGuardedUpdate(publicops.Ref{ID: stale}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "never lands"},
			}, nil, nil, &unassigned),
		},
	})
	if !errors.Is(err, publicops.ErrAssigneeMismatch) {
		t.Fatalf("guarding on the PRE-request assignee: error = %v, want ErrAssigneeMismatch", err)
	}
	if got := batchApplyColumn(t, ctx, fixture, "assignee", stale); got != "" {
		t.Errorf("%s assignee = %q, want the seeded empty holder: a guard miss refuses the whole request", stale, got)
	}
}

// RunBatchApplyClosePolicyEvaluatesAtTheCloseItem pins CloseItem.Force's clause:
// "CLOSE POLICY EVALUATES AT THIS ITEM, against the row as this request has
// already changed it."
//
// Everything the policy reads is built by the request itself — the parent, the
// child and the edge between them are all items — so a body that evaluated the
// policy against the PRE-request graph would find a parent with no children and
// close it. The refusal is the assertion; the rollback of the parent it created
// is what says the refusal took the request with it.
//
// POSITIVE HALF: the identical request with Force lands, which is the whole of
// what Force does ("bypasses blocker and open-child close policy, and nothing
// else") and the proof that the refusal was the policy rather than the shape.
func RunBatchApplyClosePolicyEvaluatesAtTheCloseItem(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	parent := fixture.IssuePrefix + "-closepolicy-parent"
	child := fixture.IssuePrefix + "-closepolicy-child"

	batch := func(force bool) publicops.ApplyBatchRequest {
		closeItem := batchApplyClose(publicops.Ref{Key: "parent"})
		closeItem.Close.Force = force
		return publicops.ApplyBatchRequest{
			Actor:         "apply-writer",
			ForceIDPrefix: true,
			Items: []publicops.ApplyItem{
				batchApplyCreate("parent", batchApplyIssue(parent, "the spawning step")),
				batchApplyCreate("child", batchApplyIssue(child, "still open at the close item")),
				batchApplyDepAdd(publicops.Ref{Key: "child"}, publicops.Ref{Key: "parent"}, publicops.DepParentChild, ""),
				closeItem,
			},
		}
	}

	_, err := fixture.BatchApplier.ApplyBatch(ctx, batch(false))
	var openChildren *publicops.CloseOpenChildrenError
	if !errors.As(err, &openChildren) {
		t.Fatalf("an unforced close of a parent this request just gave a child: error = %v, want *CloseOpenChildrenError", err)
	}
	if openChildren.IssueID != parent {
		t.Errorf("CloseOpenChildrenError = %#v, want IssueID %s", openChildren, parent)
	}
	if openChildren.OpenChildren < 1 {
		t.Errorf("CloseOpenChildrenError = %#v, want at least one open child counted", openChildren)
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", parent, 0)
	assertBatchApplyRowCount(t, ctx, fixture, "issues", child, 0)

	batchApplyMust(t, ctx, fixture, batch(true))
	if got := batchApplyColumn(t, ctx, fixture, "status", parent); got != string(types.StatusClosed) {
		t.Errorf("%s status = %q under Force, want %q", parent, got, types.StatusClosed)
	}
	if got := batchApplyColumn(t, ctx, fixture, "status", child); got != string(types.StatusOpen) {
		t.Errorf("%s status = %q, want %q: Force bypasses the close policy and nothing else", child, got, types.StatusOpen)
	}
	assertBatchApplyTypedEdgeCount(t, ctx, fixture, child, parent, string(publicops.DepParentChild), 1)
}

// RunBatchApplyAllowsAClosedParentToGainAnOpenChild pins the sentence beside it
// (CloseItem.Force): "A LATER item that gives a closed parent an open child is
// NOT refused, because beads has no global invariant that a closed issue has no
// open children; the policy is a gate on the closing act, not a constraint the
// store maintains."
//
// The parent is closed OUT OF BAND rather than by an item, so this is a claim
// about the STORE's invariants rather than about ordering — the ordering case
// above already covers the same shape built inside one request, and a body that
// grew a global check would refuse this one while the ordering case still
// looked like a reordering bug.
func RunBatchApplyAllowsAClosedParentToGainAnOpenChild(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	parent := fixture.IssuePrefix + "-closedparent-parent"
	child := fixture.IssuePrefix + "-closedparent-child"
	batchApplySeedIssue(t, ctx, fixture, parent, types.StatusClosed)
	batchApplySeedIssue(t, ctx, fixture, child, types.StatusOpen)

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyDepAdd(publicops.Ref{ID: child}, publicops.Ref{ID: parent}, publicops.DepParentChild, ""),
		},
	})

	assertBatchApplyTypedEdgeCount(t, ctx, fixture, child, parent, string(publicops.DepParentChild), 1)
	if got := batchApplyColumn(t, ctx, fixture, "status", parent); got != string(types.StatusClosed) {
		t.Errorf("%s status = %q after gaining an open child, want %q: the close policy is a gate on the closing ACT", parent, got, types.StatusClosed)
	}
	if got := batchApplyColumn(t, ctx, fixture, "status", child); got != string(types.StatusOpen) {
		t.Errorf("%s status = %q, want %q", child, got, types.StatusOpen)
	}
}

// RunBatchApplyUpdateAfterCloseInOneRequest pins what the ROW holds when a
// close and an update act on it in that order inside one request. Both items
// see one another's writes (ApplyBatchInTx applies them in declaration order
// against one transaction), so the close's status and the update's patch have
// to be on the row together at the end.
//
// It reads the raw columns rather than the result snapshots, because the two
// results describe two moments and the row is the thing a later reader gets.
func RunBatchApplyUpdateAfterCloseInOneRequest(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-postclose-row"
	batchApplySeedIssue(t, ctx, fixture, id, types.StatusOpen)

	result := batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyClose(publicops.Ref{ID: id}),
			batchApplyUpdate(publicops.Ref{ID: id}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "edited after the close"},
			}),
		},
	})
	if !result.Items[0].Changed {
		t.Errorf("the close of an open row reported Changed false")
	}
	if !result.Items[1].Changed {
		t.Errorf("the update after the close reported Changed false; it moved the title")
	}

	if got := batchApplyColumn(t, ctx, fixture, "title", id); got != "edited after the close" {
		t.Errorf("%s title = %q, want the update's patch: the update saw the row the close had already written", id, got)
	}
	if got := batchApplyColumn(t, ctx, fixture, "status", id); got != string(types.StatusClosed) {
		t.Errorf("%s status = %q, want %q: an ordinary field patch does not reopen a row the same request closed", id, got, types.StatusClosed)
	}
}

// RunBatchApplyReportsChangedPerItem pins ItemResult.Changed's three clauses at
// once, because they are one promise with three spellings: "A create is always
// true. An update follows UpdateResult.Changed, a close follows
// CloseResult.Changed, and a dep_add is false for an idempotent re-add of an
// edge that already existed with the same type."
//
// The SECOND batch is byte-identical to the first, so every item that reported
// true has to report false. Asserting only the first would pass on a body that
// hard-coded true, and asserting only the second on one that hard-coded false.
// The raw counts afterwards are what say the no-op batch really wrote nothing.
func RunBatchApplyReportsChangedPerItem(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	edited := fixture.IssuePrefix + "-changed-edited"
	target := fixture.IssuePrefix + "-changed-target"
	closing := fixture.IssuePrefix + "-changed-closing"
	for _, id := range []string{edited, target, closing} {
		batchApplySeedIssue(t, ctx, fixture, id, types.StatusOpen)
	}

	request := publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyUpdate(publicops.Ref{ID: edited}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "moved once"},
			}),
			batchApplyDepAdd(publicops.Ref{ID: edited}, publicops.Ref{ID: target}, publicops.DepBlocks, ""),
			batchApplyClose(publicops.Ref{ID: closing}),
		},
	}

	first := batchApplyMust(t, ctx, fixture, request)
	for i, item := range first.Items {
		if !item.Changed {
			t.Errorf("first pass item %d (%s) reported Changed false; it persisted a semantic mutation", i, item.Kind)
		}
	}

	second := batchApplyMust(t, ctx, fixture, request)
	for i, item := range second.Items {
		if item.Changed {
			t.Errorf("second pass item %d (%s) reported Changed true; a same-value update, an idempotent re-close and "+
				"a re-add of the same edge with the same type all persist nothing", i, item.Kind)
		}
	}

	assertBatchApplyEdgeCount(t, ctx, fixture, edited, target, 1)
	assertBatchApplyRowCount(t, ctx, fixture, "issues", edited, 1)
	if got := batchApplyColumn(t, ctx, fixture, "title", edited); got != "moved once" {
		t.Errorf("%s title = %q, want %q", edited, got, "moved once")
	}
	if got := batchApplyColumn(t, ctx, fixture, "status", closing); got != string(types.StatusClosed) {
		t.Errorf("%s status = %q, want %q", closing, got, types.StatusClosed)
	}
}

// RunBatchApplyANoOpBatchRecordsNoHistory pins the Changed-to-history link
// (BatchApplier's "HISTORY IS ONE ENTRY FOR THE REQUEST … and none at all when
// nothing durable landed", read through issueops.ApplyBatchCommitMessage, which
// composes from what LANDED): a batch whose every item is a no-op records
// nothing.
//
// IT IS WRITTEN SO IT CAN FAIL. A "records no history" assertion on its own
// passes on a backend that records no history at all, so the same shape is
// measured twice: the first pass moves the log by exactly one, the identical
// second pass moves it by none.
func RunBatchApplyANoOpBatchRecordsNoHistory(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	batchApplyRequireHistory(t, fixture)
	id := fixture.IssuePrefix + "-noophistory-row"
	batchApplySeedIssue(t, ctx, fixture, id, types.StatusOpen)
	batchApplySettle(t, ctx, fixture)

	request := publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyUpdate(publicops.Ref{ID: id}, publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "one real change"},
			}),
		},
	}

	before := batchApplyHistory(t, ctx, fixture)
	batchApplyMust(t, ctx, fixture, request)
	if got := batchApplyHistory(t, ctx, fixture) - before; got != 1 {
		t.Fatalf("a batch carrying one real change recorded %d history entries, want exactly 1", got)
	}

	before = batchApplyHistory(t, ctx, fixture)
	result := batchApplyMust(t, ctx, fixture, request)
	if result.Items[0].Changed {
		t.Fatalf("the replay of a same-value patch reported Changed true; this case is only about a batch that landed nothing")
	}
	if got := batchApplyHistory(t, ctx, fixture) - before; got != 0 {
		t.Errorf("a batch whose every item was a no-op recorded %d history entries, want none", got)
	}
}

// RunBatchApplyRecordsOneEntryForAWriteThatLandedNothing is the counterpoint to
// the case above, and it is the one the "no entry when nothing landed" rule can
// be got WRONG by: a request can write a durable row without any item reporting
// a landing. A same-type re-add of an edge rewrites that edge's metadata —
// ItemResult.Changed is false, because "a dep_add is false for an idempotent
// re-add of an edge that already existed with the same type", and the row
// changes anyway.
//
// issueops.ApplyBatchCommitMessage returns "" ONLY when nothing changed on
// either plane, so this batch is handed the plain "bd: apply batch". The trap
// it closes is on the unit-of-work leg, which reads an empty message as "roll
// this attempt back": composing "" from the LANDINGS alone would discard a
// write that really happened, and the store legs would take a staged table into
// a commit with no message.
//
// THE STORED GATE IS THE ASSERTION THAT SURVIVES A BACKEND WITH NO OBSERVABLE
// HISTORY. The re-add carries a DIFFERENT gate, so the row genuinely moves and
// a rollback is visible as the old value still sitting there.
func RunBatchApplyRecordsOneEntryForAWriteThatLandedNothing(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	source := fixture.IssuePrefix + "-wrotenothing-source"
	spawner := fixture.IssuePrefix + "-wrotenothing-spawner"
	batchApplySeedIssue(t, ctx, fixture, source, types.StatusOpen)
	batchApplySeedIssue(t, ctx, fixture, spawner, types.StatusOpen)

	reAdd := func(gate string) publicops.ApplyBatchRequest {
		return publicops.ApplyBatchRequest{
			Actor: "apply-writer",
			Items: []publicops.ApplyItem{
				batchApplyDepAdd(publicops.Ref{ID: source}, publicops.Ref{ID: spawner}, publicops.DepWaitsFor,
					`{"gate":"`+gate+`"}`),
			},
		}
	}
	batchApplyMust(t, ctx, fixture, reAdd(types.WaitsForAllChildren))

	settled := fixture.CountHistory != nil && fixture.CommitPending != nil
	before := 0
	if settled {
		batchApplySettle(t, ctx, fixture)
		before = batchApplyHistory(t, ctx, fixture)
	}

	result := batchApplyMust(t, ctx, fixture, reAdd(types.WaitsForAnyChildren))
	if result.Items[0].Changed {
		t.Fatalf("a same-type re-add reported Changed true; this case is about a write NO item calls a landing")
	}

	stored := batchApplyEdgeMetadata(t, ctx, fixture, source, spawner)
	var meta types.WaitsForMeta
	if err := json.Unmarshal([]byte(stored), &meta); err != nil {
		t.Fatalf("stored waits-for metadata %q is not a gate object: %v", stored, err)
	}
	if meta.Gate != types.WaitsForAnyChildren {
		t.Errorf("stored gate = %q (metadata %q), want %q: the re-add's metadata write must survive the commit, "+
			"and an empty commit message is what rolls it back", meta.Gate, stored, types.WaitsForAnyChildren)
	}

	if !settled {
		return
	}
	if got := batchApplyHistory(t, ctx, fixture) - before; got != 1 {
		t.Errorf("a request that wrote but landed nothing recorded %d history entries, want exactly 1", got)
	}
	if fixture.CountHistoryMatching == nil {
		return
	}
	pattern := historyPatternForExactMessage(t, "bd: apply batch")
	if got := batchApplyHistoryMatching(t, ctx, fixture, pattern); got < 1 {
		t.Errorf("no history entry reads %q; naming a count of zero would not be honest, so the act is named plainly", "bd: apply batch")
	}
}

// RunBatchApplyRecordsExactlyOneHistoryEntry pins the count clause
// (ApplyBatchRequest.Actor: "It is attributed to the ONE history entry the
// request records, because a batch is one act by one caller"): however many
// items the request carried, the log moves by one.
//
// The delta is taken around the call and the seeds are settled first, because
// the fixture's own writes reach the log too and an absolute count would be
// measuring them.
func RunBatchApplyRecordsExactlyOneHistoryEntry(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	batchApplyRequireHistory(t, fixture)
	first := fixture.IssuePrefix + "-onehistory-first"
	second := fixture.IssuePrefix + "-onehistory-second"
	third := fixture.IssuePrefix + "-onehistory-third"
	batchApplySettle(t, ctx, fixture)

	before := batchApplyHistory(t, ctx, fixture)
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("first", batchApplyIssue(first, "one of four items")),
			batchApplyCreate("second", batchApplyIssue(second, "two of four items")),
			batchApplyDepAdd(publicops.Ref{Key: "first"}, publicops.Ref{Key: "second"}, publicops.DepBlocks, ""),
			batchApplyCreate("third", batchApplyIssue(third, "four of four items")),
		},
	})
	if got := batchApplyHistory(t, ctx, fixture) - before; got != 1 {
		t.Errorf("a four-item batch recorded %d history entries, want exactly 1: the request is the transaction, so it records one entry", got)
	}
}

// RunBatchApplyHistoryNamesTheActorAndReadsTheProvenance pins the two things
// the recorded act carries: ApplyBatchRequest.Actor is "the author of every
// item", and ApplyBatchRequest.Provenance "changes how the entry READS, never
// whether one is recorded".
//
// THE ACTOR IS COUNTED PER ACTOR against the raw events table rather than read
// off the newest row: created_at is second-granularity, so two writes in one
// test tie on date and an ORDER BY decides the verdict on a coin toss. A second
// distinct actor is used so the case cannot pass on a body that stamps a
// constant — the seeding actor, the store's identity, or the first batch's.
func RunBatchApplyHistoryNamesTheActorAndReadsTheProvenance(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-actor-first"
	second := fixture.IssuePrefix + "-actor-second"

	for _, test := range []struct{ actor, id string }{
		{"apply-actor-one", first},
		{"apply-actor-two", second},
	} {
		batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
			Actor:         test.actor,
			ForceIDPrefix: true,
			Items:         []publicops.ApplyItem{batchApplyCreate("", batchApplyIssue(test.id, "attributed to "+test.actor))},
		})
		if got := batchApplyEventsByActor(t, ctx, fixture, test.id, test.actor); got < 1 {
			t.Errorf("%s has %d event(s) attributed to %q, want at least one: the actor a request is asked for is the actor its trace must name",
				test.id, got, test.actor)
		}
		if got := batchApplyEventsByActor(t, ctx, fixture, test.id, "apply-writer"); got != 0 {
			t.Errorf("%s has %d event(s) attributed to %q, which never touched it", test.id, got, "apply-writer")
		}
	}

	if fixture.CountHistoryMatching == nil {
		t.Skip("this backend cannot observe history BY MESSAGE, so the Provenance clause is unpinned here")
	}
	if fixture.CommitPending == nil {
		t.Skip("this backend cannot settle its history on demand, so a labeled entry is not measurable against the log")
	}
	const label = "conformance batch apply provenance label"
	pattern := historyPatternForExactMessage(t, label)
	batchApplySettle(t, ctx, fixture)

	before := batchApplyHistoryMatching(t, ctx, fixture, pattern)
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		Provenance:    label,
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("", batchApplyIssue(fixture.IssuePrefix+"-actor-labeled", "carries a provenance label")),
		},
	})
	if got := batchApplyHistoryMatching(t, ctx, fixture, pattern) - before; got != 1 {
		t.Errorf("a labeled request left %d entries reading %q, want exactly 1", got, label)
	}
}

// RunBatchApplyARefusedRequestRecordsNoHistory pins the half a mutation is most
// likely to break: a body that opened a committing transaction before it
// validated would record an entry for a request that changed nothing.
//
// Both refusal classes are measured, because they leave by different doors —
// one before any transaction opens (storage.PlanApplyBatch) and one from inside
// it (an item that refused).
func RunBatchApplyARefusedRequestRecordsNoHistory(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	batchApplyRequireHistory(t, fixture)
	occupied := fixture.IssuePrefix + "-nohistory-occupied"
	batchApplySeedIssue(t, ctx, fixture, occupied, types.StatusOpen)
	batchApplySettle(t, ctx, fixture)

	for _, test := range []struct {
		name    string
		request publicops.ApplyBatchRequest
	}{
		{"refused before the transaction opens", publicops.ApplyBatchRequest{
			Actor: "apply-writer",
			Items: []publicops.ApplyItem{
				batchApplyUpdate(publicops.Ref{Key: "nothing declares this"}, publicops.IssuePatch{
					Title: publicops.Field[string]{Set: true, Value: "unreachable"},
				}),
			},
		}},
		{"refused by an item", publicops.ApplyBatchRequest{
			Actor:         "apply-writer",
			ForceIDPrefix: true,
			Items: []publicops.ApplyItem{
				batchApplyCreate("", batchApplyIssue(fixture.IssuePrefix+"-nohistory-lands", "would land")),
				batchApplyCreate("", batchApplyIssue(occupied, "collides")),
			},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := batchApplyHistory(t, ctx, fixture)
			if _, err := fixture.BatchApplier.ApplyBatch(ctx, test.request); err == nil {
				t.Fatalf("ApplyBatch(%s) returned no error", test.name)
			}
			if got := batchApplyHistory(t, ctx, fixture) - before; got != 0 {
				t.Errorf("a request %s recorded %d history entries, want none", test.name, got)
			}
		})
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", fixture.IssuePrefix+"-nohistory-lands", 0)
}

// RunBatchApplyAnEphemeralBatchKeepsItsWispsAndRecordsNoDurableHistory pins the
// two halves of BatchApplier's ephemeral clause together, because separating
// them is how one of them gets faked: "an all-wisp batch writes only to the
// dolt-ignored wisp tables, so an entry naming one would be the sync artifact
// ignoring them exists to prevent."
//
// THE SURVIVING WISP IS THE ASSERTION THAT MATTERS, and it is asserted against
// the raw wisps table rather than against the commit message the helper
// composed. issueops.ApplyBatchCommitMessage documents exactly this trap: the
// store bodies stage nothing for an all-ephemeral batch whatever it returns,
// but the unit-of-work backend reads an empty message as "roll this attempt
// back", so a message computed from DURABLE landings alone silently deletes the
// batch's work on one backend out of three — while a case that asserted the
// STRING would have gone green on all three.
//
// The durable-events count is unconditional, because it is a fact about a table
// every backend of this contract has; the version delta is guarded, because
// observing history is a capability a backend may not have.
func RunBatchApplyAnEphemeralBatchKeepsItsWispsAndRecordsNoDurableHistory(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	first := fixture.IssuePrefix + "-wispbatch-first"
	second := fixture.IssuePrefix + "-wispbatch-second"

	settled := fixture.CountHistory != nil && fixture.CommitPending != nil
	before := 0
	if settled {
		batchApplySettle(t, ctx, fixture)
		before = batchApplyHistory(t, ctx, fixture)
	}

	firstIssue := batchApplyIssue(first, "ephemeral one")
	firstIssue.Ephemeral = true
	secondIssue := batchApplyIssue(second, "ephemeral two")
	secondIssue.Ephemeral = true
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("first", firstIssue),
			batchApplyCreate("second", secondIssue),
			batchApplyDepAdd(publicops.Ref{Key: "second"}, publicops.Ref{Key: "first"}, publicops.DepBlocks, ""),
		},
	})

	for _, id := range []string{first, second} {
		assertBatchApplyRowCount(t, ctx, fixture, "wisps", id, 1)
		assertBatchApplyRowCount(t, ctx, fixture, "issues", id, 0)
		if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM events WHERE issue_id = ?", []any{id}); got != 0 {
			t.Errorf("the ephemeral batch wrote %d row(s) into the DURABLE events table for %s, want none", got, id)
		}
	}
	assertBatchApplyPlaneEdgeCount(t, ctx, fixture, "wisp_dependencies", second, first, 1)
	assertBatchApplyPlaneEdgeCount(t, ctx, fixture, "dependencies", second, first, 0)

	if settled {
		if got := batchApplyHistory(t, ctx, fixture) - before; got != 0 {
			t.Errorf("an all-ephemeral batch recorded %d durable history entries, want none", got)
		}
	}
}

// RunBatchApplyRefusesACrossPlaneEdgeBetweenRowsItCreated pins
// CreateItem.Issue's plane clause: "The two planes hold their edges in
// different tables, so a dep_add item BETWEEN two rows this request creates on
// opposite planes is refused with everything else the request asked for."
//
// POSITIVE HALF, AND IT IS THE HALF THAT SAYS WHAT THE RULE IS ABOUT: an edge
// between a durable row and a wisp that ALREADY EXISTED is allowed, because
// that is DependencyEditor's ordinary both-planes case. A body that refused
// every cross-plane edge would satisfy the refusal alone and break `bd dep add`
// between a wisp and an issue.
func RunBatchApplyRefusesACrossPlaneEdgeBetweenRowsItCreated(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	durable := fixture.IssuePrefix + "-xplane-durable"
	wisp := fixture.IssuePrefix + "-xplane-wisp"

	ephemeral := batchApplyIssue(wisp, "the ephemeral end")
	ephemeral.Ephemeral = true
	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("durable", batchApplyIssue(durable, "the durable end")),
			batchApplyCreate("wisp", ephemeral),
			batchApplyDepAdd(publicops.Ref{Key: "durable"}, publicops.Ref{Key: "wisp"}, publicops.DepBlocks, ""),
		},
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("an edge between two rows this request creates on opposite planes: error = %v, want ErrValidation", err)
	}
	var itemErr *publicops.ItemError
	if !errors.As(err, &itemErr) {
		t.Fatalf("error = %v, want an *ItemError naming the edge item", err)
	}
	if itemErr.Index != 2 || itemErr.Kind != publicops.ItemDepAdd {
		t.Errorf("ItemError = %#v, want Index 2 and Kind %q", itemErr, publicops.ItemDepAdd)
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", durable, 0)
	assertBatchApplyRowCount(t, ctx, fixture, "wisps", wisp, 0)

	standingDurable := fixture.IssuePrefix + "-xplane-standing-durable"
	standingWisp := fixture.IssuePrefix + "-xplane-standing-wisp"
	batchApplySeedIssue(t, ctx, fixture, standingDurable, types.StatusOpen)
	batchApplySeedWisp(t, ctx, fixture, standingWisp)
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyDepAdd(publicops.Ref{ID: standingDurable}, publicops.Ref{ID: standingWisp}, publicops.DepBlocks, ""),
		},
	})
	assertBatchApplyPlaneEdgeCount(t, ctx, fixture, "dependencies", standingDurable, standingWisp, 1)
}

// RunBatchApplyAcceptsAnExternalEdgeTarget pins DepAddItem.Source's clause: "A
// TARGET MAY BE A ROW THIS DATABASE DOES NOT HOLD: an 'external:' reference, or
// an id whose prefix names another repository. Both are stored as external
// references exactly as DependencyEditor stores them."
//
// Both spellings are asserted because they are one rule with two shapes, and a
// role that refused either would make a plan naming work in a sibling rig
// unappliable.
func RunBatchApplyAcceptsAnExternalEdgeTarget(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	external := fixture.IssuePrefix + "-extedge-external"
	foreign := fixture.IssuePrefix + "-extedge-foreign"
	const externalTarget = "external:JIRA-4471"
	const foreignTarget = "otherrig-9910"
	batchApplySeedIssue(t, ctx, fixture, external, types.StatusOpen)
	batchApplySeedIssue(t, ctx, fixture, foreign, types.StatusOpen)

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyDepAdd(publicops.Ref{ID: external}, publicops.Ref{ID: externalTarget}, publicops.DepBlocks, ""),
			batchApplyDepAdd(publicops.Ref{ID: foreign}, publicops.Ref{ID: foreignTarget}, publicops.DepBlocks, ""),
		},
	})

	assertBatchApplyEdgeCount(t, ctx, fixture, external, externalTarget, 1)
	assertBatchApplyEdgeCount(t, ctx, fixture, foreign, foreignTarget, 1)
	// The typed column is what makes it an EXTERNAL reference rather than a
	// dangling local one: a row naming a local id that does not exist would
	// satisfy the count above.
	for _, edge := range [][2]string{{external, externalTarget}, {foreign, foreignTarget}} {
		if got := batchApplyCount(t, ctx, fixture,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_external = ?",
			[]any{edge[0], edge[1]}); got != 1 {
			t.Errorf("edge %s -> %s is not in depends_on_external (%d row(s)); a target this database does not hold is stored as an external reference",
				edge[0], edge[1], got)
		}
	}
}

// RunBatchApplyNormalizesTheWaitsForGate pins DepAddItem.Metadata's
// normalization: "An absent, blank or `{}` Metadata on a DepWaitsFor edge is
// written as {'gate':'all-children'}, because a stored waits-for row must be
// self-describing: readers predating the gate column's introduction do not
// default a missing gate, so an empty one is a row those readers get wrong."
//
// It reads the STORED metadata column rather than the result, because the whole
// point of the clause is what a later reader finds on the row — and a body that
// normalized on the way out and stored the caller's blank would satisfy every
// assertion made through the role.
func RunBatchApplyNormalizesTheWaitsForGate(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	spawner := fixture.IssuePrefix + "-gate-spawner"
	batchApplySeedIssue(t, ctx, fixture, spawner, types.StatusOpen)

	for _, test := range []struct {
		name     string
		metadata string
		want     string
	}{
		{"absent", "", types.WaitsForAllChildren},
		{"blank", "   ", types.WaitsForAllChildren},
		{"empty object", "{}", types.WaitsForAllChildren},
		{"named gate", `{"gate":"any-children"}`, types.WaitsForAnyChildren},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := fmt.Sprintf("%s-gate-%s", fixture.IssuePrefix, test.name)
			batchApplySeedIssue(t, ctx, fixture, source, types.StatusOpen)
			batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
				Actor: "apply-writer",
				Items: []publicops.ApplyItem{
					batchApplyDepAdd(publicops.Ref{ID: source}, publicops.Ref{ID: spawner}, publicops.DepWaitsFor, test.metadata),
				},
			})
			stored := batchApplyEdgeMetadata(t, ctx, fixture, source, spawner)
			var meta types.WaitsForMeta
			if err := json.Unmarshal([]byte(stored), &meta); err != nil {
				t.Fatalf("stored waits-for metadata %q is not a gate object: %v", stored, err)
			}
			if meta.Gate != test.want {
				t.Errorf("stored gate = %q (metadata %q), want %q: a stored waits-for row must be self-describing",
					meta.Gate, stored, test.want)
			}
		})
	}

	t.Run("unknown gate", func(t *testing.T) {
		source := fixture.IssuePrefix + "-gate-unknown"
		batchApplySeedIssue(t, ctx, fixture, source, types.StatusOpen)
		_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
			Actor: "apply-writer",
			Items: []publicops.ApplyItem{
				batchApplyDepAdd(publicops.Ref{ID: source}, publicops.Ref{ID: spawner}, publicops.DepWaitsFor, `{"gate":"some-children"}`),
			},
		})
		if !errors.Is(err, publicops.ErrValidation) {
			t.Fatalf("a waits-for gate that is neither known value: error = %v, want ErrValidation", err)
		}
		assertBatchApplyEdgeCount(t, ctx, fixture, source, spawner, 0)
	})
}

// RunBatchApplySplicesAForwardMetadataRef pins the one exception to the
// backward-only rule (CreateItem.MetadataRefs): "IT IS THE ONE PLACE A KEY MAY
// REACH FORWARD … Every id is minted before any splice is applied, so the
// direction cannot matter here."
//
// The shape is the MEASURED one the clause names — "a retry that re-mints a
// bead and stamps the original's id onto it" — with the stamped row declared
// first and the row it names declared second. It asserts the raw metadata
// column of the row that reached forward, against an id this case chose, so
// neither half of the comparison comes from the role.
func RunBatchApplySplicesAForwardMetadataRef(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	stamped := fixture.IssuePrefix + "-fwdmeta-stamped"
	later := fixture.IssuePrefix + "-fwdmeta-later"

	first := batchApplyIssue(stamped, "records the id of the item after it")
	item := batchApplyCreate("first", first)
	item.Create.MetadataRefs = map[string]publicops.Ref{"gc.retry_of": {Key: "later"}}

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			item,
			batchApplyCreate("later", batchApplyIssue(later, "declared after the reference to it")),
		},
	})

	stored, ok := batchApplyMetadataKey(t, ctx, fixture, stamped, "gc.retry_of")
	if !ok {
		t.Fatalf("%s carries no gc.retry_of key; the forward metadata_ref was never spliced", stamped)
	}
	var spliced string
	if err := json.Unmarshal(stored, &spliced); err != nil {
		t.Fatalf("gc.retry_of on %s is not a JSON string (%s): a ref resolves to an id written as the WHOLE value of the key", stamped, stored)
	}
	if spliced != later {
		t.Errorf("gc.retry_of on %s = %q, want %q", stamped, spliced, later)
	}
}

// RunBatchApplySplicesASelfMetadataRef pins the other direction the same clause
// allows: a metadata_ref may "name this item's own Key", which is the shape a
// row recording its own identity under a caller's key takes.
//
// It is a case of its own because self-reference is where a resolution table
// keyed by declaration index goes wrong: the id exists only after the item that
// declares it has run, and the splice is what makes that a non-question.
func RunBatchApplySplicesASelfMetadataRef(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-selfmeta-row"

	item := batchApplyCreate("self", batchApplyIssue(id, "names its own key"))
	item.Create.MetadataRefs = map[string]publicops.Ref{"gc.self": {Key: "self"}}

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items:         []publicops.ApplyItem{item},
	})

	stored, ok := batchApplyMetadataKey(t, ctx, fixture, id, "gc.self")
	if !ok {
		t.Fatalf("%s carries no gc.self key; a metadata_ref naming this item's own key was not spliced", id)
	}
	var spliced string
	if err := json.Unmarshal(stored, &spliced); err != nil {
		t.Fatalf("gc.self on %s is not a JSON string (%s)", id, stored)
	}
	if spliced != id {
		t.Errorf("gc.self on %s = %q, want the row's own id %q", id, spliced, id)
	}
}

// RunBatchApplyRefusesAMetadataRefNoItemDeclares pins the one thing a metadata
// ref may NOT do (storage.PlanApplyBatch's planApplyBatchCreate: "What it may
// not do is name a key no item declares"), and that the refusal is a *RefError
// naming the metadata key rather than a bare validation message — the member is
// what tells a caller WHICH of an item's refs failed.
func RunBatchApplyRefusesAMetadataRefNoItemDeclares(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-ghostmeta-row"

	item := batchApplyCreate("only", batchApplyIssue(id, "names a key nothing declares"))
	item.Create.MetadataRefs = map[string]publicops.Ref{"gc.retry_of": {Key: "never-declared"}}

	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items:         []publicops.ApplyItem{item},
	})
	var refErr *publicops.RefError
	if !errors.As(err, &refErr) {
		t.Fatalf("a metadata_ref naming an undeclared key: error = %v, want *RefError", err)
	}
	if refErr.DeclaredLater {
		t.Errorf("RefError = %#v, want DeclaredLater false", refErr)
	}
	if refErr.Member != "metadata_ref gc.retry_of" {
		t.Errorf("RefError.Member = %q, want the metadata key whose ref failed: an item may hold two refs", refErr.Member)
	}
	if !errors.Is(err, publicops.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", id, 0)
}

// RunBatchApplyTheSpliceRecordsAnUpdateEvent pins the honesty clause on the
// splice (CreateItem.MetadataRefs: "THE SPLICE IS A SECOND WRITE and it says
// so … which records an update event on the spliced row. A caller reading the
// event stream sees a create and then an update, not one create carrying values
// nothing could have known yet").
//
// THE CONTROL IS THE OTHER HALF. A sibling create in the SAME request carries
// no metadata_ref, so a body that recorded an update event on every create
// would pass an assertion made only about the spliced row.
func RunBatchApplyTheSpliceRecordsAnUpdateEvent(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	spliced := fixture.IssuePrefix + "-spliceevent-spliced"
	control := fixture.IssuePrefix + "-spliceevent-control"

	item := batchApplyCreate("spliced", batchApplyIssue(spliced, "carries a metadata_ref"))
	item.Create.MetadataRefs = map[string]publicops.Ref{"gc.peer": {Key: "control"}}

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			item,
			batchApplyCreate("control", batchApplyIssue(control, "carries none")),
		},
	})

	if got := batchApplyEventCount(t, ctx, fixture, spliced, string(types.EventUpdated)); got != 1 {
		t.Errorf("%s has %d %q event(s), want exactly 1: the splice is a second write and records one", spliced, got, types.EventUpdated)
	}
	if got := batchApplyEventCount(t, ctx, fixture, control, string(types.EventUpdated)); got != 0 {
		t.Errorf("%s has %d %q event(s), want none: it carried no metadata_ref, so nothing spliced it", control, got, types.EventUpdated)
	}
	if got := batchApplyEventCount(t, ctx, fixture, spliced, string(types.EventCreated)); got != 1 {
		t.Errorf("%s has %d %q event(s), want exactly 1: a caller reading the stream sees a create AND THEN an update", spliced, got, types.EventCreated)
	}
}

// RunBatchApplyKeepsAStoredNullApartFromAnEmptyString pins the value rule an
// item's metadata answers to (CreateItem.Issue reads under CreateRequest.Issue's
// rules "WITHOUT EXCEPTION", and UpdateItem.Patch is read exactly as
// UpdateRequest.Patch is): a key holding JSON null and a key holding the empty
// string are two different stored values, and nothing converts one into the
// other on the way in.
//
// It reads the raw metadata column, because the two collapse into one another
// at every layer that decodes JSON into concrete Go values — which is exactly
// how the distinction gets lost — and it drives BOTH doors into an item's
// metadata, the create's document and the update's per-key Set, because they
// are separate funnels.
func RunBatchApplyKeepsAStoredNullApartFromAnEmptyString(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-nullvsempty-row"

	issue := batchApplyIssue(id, "carries a null and an empty string")
	issue.Metadata = json.RawMessage(`{"created.null":null,"created.empty":""}`)

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("row", issue),
			batchApplyUpdate(publicops.Ref{Key: "row"}, publicops.IssuePatch{
				Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{
					"patched.null":  json.RawMessage(`null`),
					"patched.empty": json.RawMessage(`""`),
				}},
			}),
		},
	})

	for _, test := range []struct{ key, want string }{
		{"created.null", `null`},
		{"created.empty", `""`},
		{"patched.null", `null`},
		{"patched.empty", `""`},
	} {
		stored, ok := batchApplyMetadataKey(t, ctx, fixture, id, test.key)
		if !ok {
			t.Errorf("%s carries no metadata key %q; a key stored holding null is PRESENT", id, test.key)
			continue
		}
		if got := string(stored); got != test.want {
			t.Errorf("metadata %q on %s = %s, want %s: null and the empty string are different values and neither becomes the other",
				test.key, id, got, test.want)
		}
	}
}

// RunBatchApplyLandsAnIdempotencyRecordWithItsWork pins the composition
// BatchApplier says replaces an idempotency key on the request: "an idempotency
// RECORD is itself a write, so a caller that needs one makes it an ITEM of the
// batch, and the record then lands or rolls back with the work it describes."
//
// The claim is BOTH OR NEITHER, so it is measured both ways. The refusing half
// is what a request-level key would have made impossible to get wrong and is
// therefore the half worth asserting: a record that survived the rollback of
// the work it describes would make the caller's next replay a no-op over
// nothing.
func RunBatchApplyLandsAnIdempotencyRecordWithItsWork(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	occupied := fixture.IssuePrefix + "-rigidem-occupied"
	batchApplySeedIssue(t, ctx, fixture, occupied, types.StatusOpen)

	record := fixture.IssuePrefix + "-rigidem-record"
	work := fixture.IssuePrefix + "-rigidem-work"
	recordIssue := batchApplyIssue(record, "the idempotency record")
	recordIssue.Metadata = json.RawMessage(`{"gc.idempotency_key":"apply-once"}`)

	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("record", recordIssue),
			batchApplyCreate("work", batchApplyIssue(work, "the work the record describes")),
			batchApplyDepAdd(publicops.Ref{Key: "work"}, publicops.Ref{Key: "record"}, publicops.DepRelated, ""),
		},
	})
	assertBatchApplyRowCount(t, ctx, fixture, "issues", record, 1)
	assertBatchApplyRowCount(t, ctx, fixture, "issues", work, 1)
	stored, ok := batchApplyMetadataKey(t, ctx, fixture, record, "gc.idempotency_key")
	if !ok || string(stored) != `"apply-once"` {
		t.Errorf("the record's gc.idempotency_key = %s (present %v), want %q", stored, ok, `"apply-once"`)
	}

	refusedRecord := fixture.IssuePrefix + "-rigidem-refused-record"
	refusedWork := fixture.IssuePrefix + "-rigidem-refused-work"
	refusedIssue := batchApplyIssue(refusedRecord, "a record for work that never lands")
	refusedIssue.Metadata = json.RawMessage(`{"gc.idempotency_key":"apply-never"}`)
	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("record", refusedIssue),
			batchApplyCreate("work", batchApplyIssue(refusedWork, "would land")),
			batchApplyCreate("collides", batchApplyIssue(occupied, "takes the request down")),
		},
	})
	if err == nil {
		t.Fatal("the refusing composition returned no error")
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", refusedRecord, 0)
	assertBatchApplyRowCount(t, ctx, fixture, "issues", refusedWork, 0)
}

// RunBatchApplyBoundsTheItemCount pins ApplyBatchRequest.Items' size rule and
// the constant that spells it (issueops.MaxApplyBatchItems): a request of
// exactly the bound is accepted, one over it is ErrValidation, and the refusal
// writes nothing.
//
// The boundary is asserted from BOTH sides because an off-by-one in either
// direction is the only interesting way this can be wrong, and either half
// alone is satisfied by a bound one step away.
func RunBatchApplyBoundsTheItemCount(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	const accepted = "batch apply at the item bound"
	const refused = "batch apply over the item bound"

	atBound := make([]publicops.ApplyItem, publicops.MaxApplyBatchItems)
	for i := range atBound {
		atBound[i] = batchApplyCreate("", batchApplyMintedIssue(accepted))
	}
	result := batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{Actor: "apply-writer", Items: atBound})
	if len(result.Items) != publicops.MaxApplyBatchItems {
		t.Fatalf("a request of exactly MaxApplyBatchItems returned %d item results, want %d", len(result.Items), publicops.MaxApplyBatchItems)
	}
	if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM issues WHERE title = ?", []any{accepted}); got != publicops.MaxApplyBatchItems {
		t.Errorf("%d row(s) landed for a request of exactly MaxApplyBatchItems, want %d", got, publicops.MaxApplyBatchItems)
	}

	overBound := make([]publicops.ApplyItem, publicops.MaxApplyBatchItems+1)
	for i := range overBound {
		overBound[i] = batchApplyCreate("", batchApplyMintedIssue(refused))
	}
	if _, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor: "apply-writer", Items: overBound,
	}); !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("a request of MaxApplyBatchItems+1 items: error = %v, want ErrValidation", err)
	}
	if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM issues WHERE title = ?", []any{refused}); got != 0 {
		t.Errorf("%d row(s) landed for a refused over-length request, want none", got)
	}
}

// RunBatchApplyReplayMintsANewSetOfRows pins the property BatchApplier states
// outright and no other case would notice: "IT IS NOT IDEMPOTENT AND CARRIES NO
// IDEMPOTENCY KEY. Replaying a request applies it again — the creates mint new
// ids."
//
// It is deliberate rather than accidental, so it is pinned rather than left to
// be discovered: a body that grew request-level deduplication would be a
// SILENT behavior change, invisible to every case above, and the composition
// case beside this one is the mechanism this property is the reason for.
func RunBatchApplyReplayMintsANewSetOfRows(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	const title = "batch apply replayed row"
	request := publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyCreate("first", batchApplyMintedIssue(title)),
			batchApplyCreate("second", batchApplyMintedIssue(title)),
		},
	}

	firstPass := batchApplyMust(t, ctx, fixture, request)
	if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM issues WHERE title = ?", []any{title}); got != 2 {
		t.Fatalf("%d row(s) after one pass, want 2", got)
	}

	secondPass := batchApplyMust(t, ctx, fixture, request)
	if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM issues WHERE title = ?", []any{title}); got != 4 {
		t.Errorf("%d row(s) after replaying the same request, want 4: a replay applies the request again", got)
	}
	if firstPass.Keys["first"] == secondPass.Keys["first"] {
		t.Errorf("both passes bound key %q to %q; a replay MINTS NEW IDS", "first", firstPass.Keys["first"])
	}
}

// RunBatchApplyDoesNotMutateTheCallerRequest pins the snapshot clause
// (BatchApplier: "Implementations never mutate caller-owned request values,
// snapshot the request at method entry, and apply validation and normalization
// only to attempt-local clones").
//
// It is written around the two members that would break it. The create item's
// *Issue is SHARED with storage.ApplyBatchPlan rather than deep-copied — the
// plan says so — so only the body cloning it before the write keeps the
// assigned ID off the caller's struct, and a caller whose next request carried
// that ID would be refused as an occupied id. The waits-for edge's Metadata is
// REWRITTEN by normalization, into the plan's fresh item slice; a body that
// normalized in place would hand the caller back a request it did not write.
func RunBatchApplyDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	spawner := fixture.IssuePrefix + "-snapshot-spawner"
	batchApplySeedIssue(t, ctx, fixture, spawner, types.StatusOpen)

	build := func() publicops.ApplyBatchRequest {
		created := &types.Issue{
			Title: "caller owned", Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask, Labels: []string{"kept"},
			Metadata: json.RawMessage(`{"caller":"owned"}`),
		}
		return publicops.ApplyBatchRequest{
			Actor:      "apply-writer",
			Provenance: "conformance batch apply snapshot",
			Items: []publicops.ApplyItem{
				batchApplyCreate("created", created),
				batchApplyDepAdd(publicops.Ref{ID: spawner}, publicops.Ref{Key: "created"}, publicops.DepWaitsFor, ""),
			},
		}
	}
	request := build()
	snapshot := build()

	if _, err := fixture.BatchApplier.ApplyBatch(ctx, request); err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}
	if !reflect.DeepEqual(request, snapshot) {
		t.Errorf("ApplyBatch rewrote the caller's request:\n got %+v\nwant %+v", request, snapshot)
	}
	if got := request.Items[0].Create.Issue.ID; got != "" {
		t.Errorf("the caller's issue came back carrying ID %q; the next request built from that struct would be refused as an occupied id", got)
	}
	if got := request.Items[1].DepAdd.Metadata; got != "" {
		t.Errorf("the caller's waits-for Metadata was rewritten to %q; normalization applies to attempt-local clones", got)
	}
}

// RunBatchApplyRefusesAnUnusableRequest pins the request-level refusals that
// carry no item wrapper (ApplyBatch: "REFUSALS RAISED BEFORE ANY ITEM RUN are
// the request's own"): an empty Actor and an empty Items.
//
// The empty-items clause is the one worth stating. Answering a WRITE batch that
// wrote nothing with a cheerful empty success is how a front door with a
// filtered-to-nothing plan silently stops applying anything, which is why every
// write batch in this family refuses it while the read batches answer empty.
func RunBatchApplyRefusesAnUnusableRequest(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	const probe = "batch apply unusable request"
	item := batchApplyCreate("", batchApplyMintedIssue(probe))

	for _, test := range []struct {
		name    string
		request publicops.ApplyBatchRequest
	}{
		{"no actor", publicops.ApplyBatchRequest{Items: []publicops.ApplyItem{item}}},
		{"no items", publicops.ApplyBatchRequest{Actor: "apply-writer"}},
		{"kind with no payload", publicops.ApplyBatchRequest{
			Actor: "apply-writer", Items: []publicops.ApplyItem{{Kind: publicops.ItemCreate}},
		}},
		{"kind carrying another kind's payload", publicops.ApplyBatchRequest{
			Actor: "apply-writer",
			Items: []publicops.ApplyItem{{Kind: publicops.ItemUpdate, Create: &publicops.CreateItem{
				Issue: batchApplyMintedIssue("mismatched"),
			}}},
		}},
		{"unknown kind", publicops.ApplyBatchRequest{
			Actor: "apply-writer",
			Items: []publicops.ApplyItem{{Kind: "reopen", Create: &publicops.CreateItem{
				Issue: batchApplyMintedIssue("not a verb"),
			}}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := fixture.BatchApplier.ApplyBatch(ctx, test.request)
			if !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("ApplyBatch(%s) error = %v, want ErrValidation", test.name, err)
			}
			if len(result.Items) != 0 || len(result.Keys) != 0 {
				t.Errorf("ApplyBatch(%s) returned %d items and %d keys with an error; a refusal applies nothing",
					test.name, len(result.Items), len(result.Keys))
			}
		})
	}

	// The positive half: the same ITEM SHAPE, in a request that names an actor,
	// lands. Without it every arm above is satisfied by a role that refuses
	// everything. The item is rebuilt rather than reused so a body that wrote an
	// id through the caller's struct cannot make this half pass or fail for a
	// reason this case is not about.
	if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM issues WHERE title = ?", []any{probe}); got != 0 {
		t.Fatalf("%d row(s) already carry the probe title before the positive half runs; a refused request applied one of them", got)
	}
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor: "apply-writer", Items: []publicops.ApplyItem{batchApplyCreate("", batchApplyMintedIssue(probe))},
	})
	if got := batchApplyCount(t, ctx, fixture, "SELECT COUNT(*) FROM issues WHERE title = ?", []any{probe}); got != 1 {
		t.Errorf("%d row(s) landed for the same item under a valid request, want 1", got)
	}
}

// --- request builders ------------------------------------------------------

// batchApplyIssue is an issue with an EXPLICIT id, for the cases that have to
// name their rows. Every request carrying one also carries ForceIDPrefix, since
// the fixture prefix is not the workspace's configured one.
func batchApplyIssue(id, title string) *types.Issue {
	return &types.Issue{ID: id, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

// batchApplyMintedIssue is an issue with NO id, for the cases about the ids the
// role mints — Keys, the item bound and the replay property.
func batchApplyMintedIssue(title string) *types.Issue {
	return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

func batchApplyCreate(key string, issue *types.Issue) publicops.ApplyItem {
	return publicops.ApplyItem{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Key: key, Issue: issue}}
}

func batchApplyUpdate(target publicops.Ref, patch publicops.IssuePatch) publicops.ApplyItem {
	return publicops.ApplyItem{Kind: publicops.ItemUpdate, Update: &publicops.UpdateItem{Target: target, Patch: patch}}
}

func batchApplyGuardedUpdate(target publicops.Ref, patch publicops.IssuePatch, version *int64, status *publicops.Status, assignee *string) publicops.ApplyItem {
	return publicops.ApplyItem{Kind: publicops.ItemUpdate, Update: &publicops.UpdateItem{
		Target:           target,
		Patch:            patch,
		ExpectedVersion:  version,
		ExpectedStatus:   status,
		ExpectedAssignee: assignee,
	}}
}

func batchApplyClose(target publicops.Ref) publicops.ApplyItem {
	return publicops.ApplyItem{Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: target, Reason: "applied"}}
}

func batchApplyGuardedClose(target publicops.Ref, version *int64) publicops.ApplyItem {
	return publicops.ApplyItem{Kind: publicops.ItemClose, Close: &publicops.CloseItem{
		Target: target, Reason: "applied", ExpectedVersion: version,
	}}
}

func batchApplyDepAdd(source, target publicops.Ref, depType publicops.DependencyType, metadata string) publicops.ApplyItem {
	return publicops.ApplyItem{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{
		Source: source, Target: target, Type: depType, Metadata: metadata,
	}}
}

// --- fixture helpers -------------------------------------------------------

// batchApplyMust applies a request the case expects to land, and fatals on a
// refusal. It never asserts the CONTENT of the result: the cases read rows.
func batchApplyMust(t *testing.T, ctx context.Context, fixture BatchApplyFixture, request publicops.ApplyBatchRequest) publicops.ApplyBatchResult {
	t.Helper()
	result, err := fixture.BatchApplier.ApplyBatch(ctx, request)
	if err != nil {
		t.Fatalf("ApplyBatch(%d items): %v", len(request.Items), err)
	}
	return result
}

// batchApplySeedIssue writes one durable issue through the fixture's own
// create, which is deliberately NOT the role under test.
func batchApplySeedIssue(t *testing.T, ctx context.Context, fixture BatchApplyFixture, id string, status types.Status) {
	t.Helper()
	issue := batchApplyIssue(id, id)
	issue.Status = status
	if err := fixture.CreateIssue(ctx, issue, "apply-seed"); err != nil {
		t.Fatalf("seeding %s: %v", id, err)
	}
}

// batchApplySeedWisp writes one ephemeral issue through the fixture's own
// ephemeral create.
func batchApplySeedWisp(t *testing.T, ctx context.Context, fixture BatchApplyFixture, id string) {
	t.Helper()
	issue := batchApplyIssue(id, id)
	issue.Ephemeral = true
	if err := fixture.CreateWisp(ctx, issue, "apply-seed"); err != nil {
		t.Fatalf("seeding wisp %s: %v", id, err)
	}
}

// batchApplyCount runs a scalar COUNT query.
func batchApplyCount(t *testing.T, ctx context.Context, fixture BatchApplyFixture, query string, args []any) int {
	t.Helper()
	var count int
	if err := fixture.QueryScalar(ctx, query, args, &count); err != nil {
		t.Fatalf("counting with %q %v: %v", query, args, err)
	}
	return count
}

// batchApplyColumn reads one text column off one row. It is how every case that
// says "the row holds X" says it: reading the value back through the role is
// exactly the check that passes on a body that never wrote.
func batchApplyColumn(t *testing.T, ctx context.Context, fixture BatchApplyFixture, column, id string) string {
	t.Helper()
	var value string
	// The durable plane only: every case that reads a column reads a durable
	// row, and the two that care about a wisp read its presence rather than its
	// fields.
	//nolint:gosec // G201: column is one of this contract's own hardcoded names.
	query := fmt.Sprintf("SELECT COALESCE(%s, '') FROM issues WHERE id = ?", column)
	if err := fixture.QueryScalar(ctx, query, []any{id}, &value); err != nil {
		t.Fatalf("reading issues.%s for %s: %v", column, id, err)
	}
	return value
}

// batchApplyRowVersion reads the row's optimistic-concurrency token from the
// raw row_lock column. Every ExpectedVersion case takes it from here rather
// than from a previous ItemResult, so the guard is compared against the row
// instead of against something the role said about itself.
func batchApplyRowVersion(t *testing.T, ctx context.Context, fixture BatchApplyFixture, id string) int64 {
	t.Helper()
	var version int64
	if err := fixture.QueryScalar(ctx, "SELECT row_lock FROM issues WHERE id = ?", []any{id}, &version); err != nil {
		t.Fatalf("reading row_lock for %s: %v", id, err)
	}
	return version
}

// assertBatchApplyRowCount asserts how many rows of a plane carry an id.
func assertBatchApplyRowCount(t *testing.T, ctx context.Context, fixture BatchApplyFixture, table, id string, want int) {
	t.Helper()
	//nolint:gosec // G201: table is one of this contract's hardcoded plane names.
	query := "SELECT COUNT(*) FROM " + table + " WHERE id = ?"
	if got := batchApplyCount(t, ctx, fixture, query, []any{id}); got != want {
		t.Errorf("%s rows for %s = %d, want %d", table, id, got, want)
	}
}

// assertBatchApplyEdgeCount asserts how many stored edges run from source to
// target across BOTH dependency tables and all three target columns: which of
// each a row landed in is a placement detail the cases that care about it
// assert through assertBatchApplyPlaneEdgeCount instead.
func assertBatchApplyEdgeCount(t *testing.T, ctx context.Context, fixture BatchApplyFixture, source, target string, want int) {
	t.Helper()
	const query = `SELECT
		(SELECT COUNT(*) FROM dependencies WHERE issue_id = ?
			AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?) +
		(SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?
			AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?)`
	if got := batchApplyCount(t, ctx, fixture, query, []any{source, target, source, target}); got != want {
		t.Errorf("edges %s -> %s = %d, want %d", source, target, got, want)
	}
}

// assertBatchApplyPlaneEdgeCount is assertBatchApplyEdgeCount narrowed to ONE
// dependency table. Where the placement of a durable edge is a detail, WHICH
// PLANE an edge landed on is the promise itself, and a sum across both tables
// cannot state it.
func assertBatchApplyPlaneEdgeCount(t *testing.T, ctx context.Context, fixture BatchApplyFixture, table, source, target string, want int) {
	t.Helper()
	//nolint:gosec // G201: table is one of this contract's two hardcoded names.
	query := "SELECT COUNT(*) FROM " + table + " WHERE issue_id = ?" +
		" AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?"
	if got := batchApplyCount(t, ctx, fixture, query, []any{source, target}); got != want {
		t.Errorf("%s rows %s -> %s = %d, want %d", table, source, target, got, want)
	}
}

// assertBatchApplyTypedEdgeCount narrows the edge count by TYPE, for the cases
// whose claim is about which kind of edge landed rather than that one did.
func assertBatchApplyTypedEdgeCount(t *testing.T, ctx context.Context, fixture BatchApplyFixture, source, target, depType string, want int) {
	t.Helper()
	const query = `SELECT
		(SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND type = ?
			AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?) +
		(SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND type = ?
			AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?)`
	args := []any{source, depType, target, source, depType, target}
	if got := batchApplyCount(t, ctx, fixture, query, args); got != want {
		t.Errorf("%s edges %s -> %s = %d, want %d", depType, source, target, got, want)
	}
}

// batchApplyEdgeMetadata reads the stored metadata blob of one edge, which is
// where the waits-for gate normalization has to be visible.
func batchApplyEdgeMetadata(t *testing.T, ctx context.Context, fixture BatchApplyFixture, source, target string) string {
	t.Helper()
	var blob string
	const query = "SELECT COALESCE(metadata, '') FROM dependencies WHERE issue_id = ?" +
		" AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?"
	if err := fixture.QueryScalar(ctx, query, []any{source, target}, &blob); err != nil {
		t.Fatalf("reading edge metadata for %s -> %s: %v", source, target, err)
	}
	return blob
}

// batchApplyMetadataKey reads the WHOLE metadata blob off a row and reports one
// key's raw bytes. The whole blob is read rather than a JSON path extract,
// because that is what lets a case see an absent key and a null one as the
// different things they are — a path extract answers NULL for both.
func batchApplyMetadataKey(t *testing.T, ctx context.Context, fixture BatchApplyFixture, id, key string) (json.RawMessage, bool) {
	t.Helper()
	// Scanned as a STRING, which is the destination the three kits' scalar
	// readers agree on; a []byte destination is one of them only.
	var blob string
	const query = "SELECT COALESCE(metadata, '{}') FROM issues WHERE id = ?"
	if err := fixture.QueryScalar(ctx, query, []any{id}, &blob); err != nil {
		t.Fatalf("reading issues.metadata for %s: %v", id, err)
	}
	if blob == "" || blob == "null" {
		return nil, false
	}
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(blob), &object); err != nil {
		t.Fatalf("parsing issues.metadata for %s (%s): %v", id, blob, err)
	}
	stored, ok := object[key]
	return stored, ok
}

// batchApplyEventCount counts one issue's durable events of one type.
func batchApplyEventCount(t *testing.T, ctx context.Context, fixture BatchApplyFixture, id, eventType string) int {
	t.Helper()
	return batchApplyCount(t, ctx, fixture,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", []any{id, eventType})
}

// batchApplyEventsByActor counts the durable events on an issue attributed to
// one actor. It goes through the frozen kit's scalar seam rather than a new
// fixture hook, because the events table is one every backend of this contract
// already has.
func batchApplyEventsByActor(t *testing.T, ctx context.Context, fixture BatchApplyFixture, id, actor string) int {
	t.Helper()
	return batchApplyCount(t, ctx, fixture,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND actor = ?", []any{id, actor})
}

// batchApplyRequireHistory skips LOUDLY when the backend cannot observe or
// settle its history, naming the clause that goes unpinned there rather than
// letting the case pass quietly.
func batchApplyRequireHistory(t *testing.T, fixture BatchApplyFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the one-entry-per-request clause is unobservable here")
	}
	if fixture.CommitPending == nil {
		t.Skip("this backend cannot settle its history on demand, so a request over these seeds is not a change against the history")
	}
}

// batchApplySettle puts the seeds into the version history, so the delta a case
// takes afterwards measures the call under test rather than the setup that led
// up to it.
func batchApplySettle(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()
	if fixture.CommitPending == nil {
		return
	}
	if err := fixture.CommitPending(ctx); err != nil {
		t.Fatalf("settling the seeds: %v", err)
	}
}

func batchApplyHistory(t *testing.T, ctx context.Context, fixture BatchApplyFixture) int {
	t.Helper()
	count, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory: %v", err)
	}
	return count
}

func batchApplyHistoryMatching(t *testing.T, ctx context.Context, fixture BatchApplyFixture, pattern string) int {
	t.Helper()
	count, err := fixture.CountHistoryMatching(ctx, pattern)
	if err != nil {
		t.Fatalf("CountHistoryMatching(%q): %v", pattern, err)
	}
	return count
}
