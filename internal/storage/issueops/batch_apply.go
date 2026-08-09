package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ApplyBatchInTx is the body behind issueops.BatchApplier: apply a
// heterogeneous list of graph mutations in declaration order, splice the
// resolved ids the request asked for, and gate the graph the whole request
// produced — all on ONE transaction.
//
// It lives here rather than in an importable internal/workapi/store<role>
// package because the items must see one another's writes, and
// storage.DoltStorage publishes methods, not transactions.
//
// TWO OF THE THREE LEGS SHARE IT: the Dolt-backed stores each wrap it in their
// own transaction. THE UNIT-OF-WORK LEG DOES NOT, and cannot — it has a body of
// its own in internal/storage/uow/batch_applier.go. The reason is mechanical
// rather than chosen: this body composes ExecuteCreate, ExecuteUpdate and
// ExecuteClose, every one of which takes a *sql.Tx, while a unit of work's
// runner is a *sql.Conn with a transaction open on it, and neither publishes
// the other. Reaching this function from there would mean widening three of the
// oldest write paths in the tree to take an interface.
//
// So a three-leg contract run over this role is TWO INDEPENDENT VOTES plus an
// engine check, not one reading plus two wrapper checks — which is what the
// conformance contract's header states and what its cases are written for.
// What keeps the two bodies from meaning different things is the sharing that
// IS possible: the request validation (storage.PlanApplyBatch), the
// commit-message rule (ApplyBatchCommitMessage) and the two leaf checks the end
// gate runs are one definition each, reached from both.
//
// IT COMPOSES THE SINGLE-VERB BODIES rather than restating them. A create item
// is ExecuteCreate, an update is ExecuteUpdate, a close is ExecuteClose: the
// guards, the close policy, the blocked-state invariant and the plane routing
// are each defined once, and this role inherits every one of them instead of
// growing a second copy that can drift.
//
// WHAT IT ADDS IS THE PART A LOOP OVER THOSE VERBS CANNOT HAVE: symbolic refs
// resolved as ids are minted, a metadata splice that can reach forward because
// every id exists by the time it runs, and an END GATE over the graph all the
// items built together.
//
// It assumes a plan already produced by storage.PlanApplyBatch — refs checked,
// guards checked, waits-for gates normalized. The accessors plan BEFORE opening
// a transaction, so a refused request costs no database work.
//
// It reports what the request DID alongside the result, because only the caller
// knows what to do with that; see BatchApplyWrite for why those are two facts
// rather than one.
func ApplyBatchInTx(ctx context.Context, tx *sql.Tx, plan storage.ApplyBatchPlan) (publicops.ApplyBatchResult, BatchApplyWrite, error) {
	run := &applyBatchRun{
		plan:   plan,
		keys:   make(map[string]string, len(plan.KeyIndex)),
		planes: map[string]bool{},
		write:  BatchApplyWrite{Tables: ChangedTables{}},
		result: publicops.ApplyBatchResult{
			Keys:  make(map[string]string, len(plan.KeyIndex)),
			Items: make([]publicops.ItemResult, len(plan.Items)),
		},
	}
	if err := run.apply(ctx, tx); err != nil {
		return publicops.ApplyBatchResult{}, BatchApplyWrite{}, err
	}
	return run.result, run.write, nil
}

// BatchApplyWrite reports what one apply-batch did.
//
// THE TWO FIELDS ARE NOT THE SAME QUESTION, and conflating them is a real bug
// rather than a tidiness point: a batch made entirely of ephemeral items writes
// rows and changes no durable table, because the version-control plane ignores
// the wisp tables. A caller that read an empty table set as "nothing happened"
// would roll the write back — which is exactly what the unit-of-work leg does
// when handed an empty commit message, since that message is what commits the
// SQL transaction as well as what versions it.
type BatchApplyWrite struct {
	// Changed is true when this request WROTE something, on either plane.
	//
	// IT IS NOT ItemResult.Changed SUMMED, and the difference is deliberate.
	// ItemResult.Changed answers what a CALLER can observe — a new edge, a
	// status that moved — and is false for an idempotent re-add of an edge that
	// already existed. This one answers whether the transaction has anything to
	// commit, and that re-add still rewrote the edge row's metadata
	// (AddDependencyInTx's same-type branch). Conflating them would leave a
	// staged table with no commit message behind it.
	Changed bool
	// Tables are the DURABLE tables the request changed, for a caller that
	// stages them. It is empty when nothing was written AND when everything
	// written was ephemeral.
	Tables ChangedTables
}

// ApplyBatchCommitMessage is the history entry an apply-batch records: the
// caller's own label when it supplied one, otherwise a default naming how many
// items of each kind landed.
//
// IT NAMES COUNTS AND NEVER IDS. A batch can carry a hundred items and mint a
// hundred ids, and an entry naming them all is the diff written twice — the
// reason CreateBatchCommitMessage gives, with more kinds to count.
//
// IT RETURNS "" ONLY WHEN NOTHING CHANGED ON EITHER PLANE. That is the
// generalization of the trap CreateBatchCommitMessage and
// CloseBatchCommitMessage each document one half of: the store-backed bodies
// stage nothing for an all-ephemeral batch and record no entry whatever this
// returns, but the unit-of-work backend reads "" as "roll this attempt back",
// so a wisp-only batch must still be handed a message or the wisps it wrote are
// discarded. Counting the DURABLE landings only would silently delete an
// ephemeral batch's work on one backend out of three.
//
// SO IT READS THE WRITE, not just the result, and that is the second half of
// the same trap. The counts below are ItemResult.Changed, which is what a
// caller can OBSERVE; a request can write a durable row without any item
// reporting a landing — an idempotent same-type re-add rewrites the edge's
// metadata, and a re-close can still settle blocked state. Composing "" for one
// of those would hand the store legs a staged table with an empty Dolt commit
// message, and hand the unit-of-work leg a rollback of a write it had made.
func ApplyBatchCommitMessage(plan storage.ApplyBatchPlan, result publicops.ApplyBatchResult, write BatchApplyWrite) string {
	counts := map[publicops.ItemKind]int{}
	for _, item := range result.Items {
		if item.Changed {
			counts[item.Kind]++
		}
	}
	var parts []string
	for _, kind := range []struct {
		kind publicops.ItemKind
		noun string
	}{
		{publicops.ItemCreate, "create"},
		{publicops.ItemUpdate, "update"},
		{publicops.ItemClose, "close"},
		{publicops.ItemDepAdd, "edge"},
	} {
		if n := counts[kind.kind]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s%s", n, kind.noun, plural(n)))
		}
	}
	if len(parts) == 0 {
		if !write.Changed && len(write.Tables) == 0 {
			return ""
		}
		// The request wrote, but nothing it wrote is a landing a caller asked
		// about. Naming the act plainly is the honest entry; naming a count of
		// zero would not be.
		return HistoryEntry(plan.Provenance, "bd: apply batch")
	}
	return HistoryEntry(plan.Provenance, "bd: apply "+strings.Join(parts, ", "))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// applyBatchRun carries the state one request accumulates: the ids its keys
// bound to, the plane each row it CREATED landed on, the edges it added, and
// what it wrote.
type applyBatchRun struct {
	plan storage.ApplyBatchPlan
	// keys maps a create item's Key to the id it was bound to. It is the
	// resolution table every backward ref reads.
	keys map[string]string
	// planes records, for each row THIS REQUEST created, whether it landed on
	// the ephemeral plane. Only rows this request created are in it: the
	// cross-plane refusal is about edges whose BOTH ends the batch creates, and
	// an edge between two rows that already existed is DependencyEditor's
	// ordinary both-planes case.
	planes map[string]bool
	// edges records every edge the request added, with the item each came from,
	// so the end gate can name the item that refused.
	edges  []appliedEdge
	write  BatchApplyWrite
	result publicops.ApplyBatchResult
}

// appliedEdge is one edge the request wrote, paired with its item's index.
type appliedEdge struct {
	index int
	edge  publicops.DependencyEdge
}

// apply runs the three phases in order: the items, the metadata splice, the end
// gate.
func (r *applyBatchRun) apply(ctx context.Context, tx *sql.Tx) error {
	for i := range r.plan.Items {
		if err := r.applyItem(ctx, tx, i); err != nil {
			return err
		}
	}
	if err := r.spliceMetadataRefs(ctx, tx); err != nil {
		return err
	}
	return r.runEndGate(ctx, tx)
}

// applyItem applies one item and records its outcome at its own index.
func (r *applyBatchRun) applyItem(ctx context.Context, tx *sql.Tx, index int) error {
	item := r.plan.Items[index]
	switch item.Kind {
	case publicops.ItemCreate:
		return r.applyCreate(ctx, tx, index, item.Create)
	case publicops.ItemUpdate:
		return r.applyUpdate(ctx, tx, index, item.Update)
	case publicops.ItemClose:
		return r.applyClose(ctx, tx, index, item.Close)
	case publicops.ItemDepAdd:
		return r.applyDepAdd(ctx, tx, index, item.DepAdd)
	}
	// Unreachable: PlanApplyBatch refuses an unknown kind before any
	// transaction opens. Answering rather than falling through keeps a future
	// kind from landing silently as a no-op item.
	return fmt.Errorf("%w: apply batch item %d has unknown kind %q", storage.ErrValidation, index, item.Kind)
}

// applyCreate mints one row through the same body a single create runs.
//
// A CREATE IS ALWAYS Changed. There is no idempotent create here — an occupied
// explicit id is ErrAlreadyExists and takes the whole request with it — so the
// flag is not a question this verb can answer no to.
//
// IT PAYS ExecuteCreate's PER-CALL SETUP PER ITEM, where ExecuteCreateBatch
// hoists it once for a homogeneous batch. That is deliberate: hoisting here
// would mean re-deriving the preparation, the infra-type routing, the id
// assignment and the error classification beside them, which is the drift the
// role exists to avoid, and the cost is bounded by the request's hundred-item
// cap. Revisit it with a measurement, not with a copy of ExecuteCreateBatch.
func (r *applyBatchRun) applyCreate(ctx context.Context, tx *sql.Tx, index int, item *publicops.CreateItem) error {
	created, tables, err := ExecuteCreate(ctx, tx, publicops.CreateRequest{
		Actor:         r.plan.Actor,
		Issue:         item.Issue,
		ForceIDPrefix: r.plan.ForceIDPrefix,
	})
	if err != nil {
		return &publicops.ItemError{Index: index, Kind: publicops.ItemCreate, Key: item.Key, Err: err}
	}
	r.write.Tables.Merge(tables)
	r.write.Changed = true
	if item.Key != "" {
		r.keys[item.Key] = created.Issue.ID
		r.result.Keys[item.Key] = created.Issue.ID
	}
	r.planes[created.Issue.ID] = IsWisp(created.Issue)
	r.result.Items[index] = publicops.ItemResult{
		Kind:       publicops.ItemCreate,
		IssueID:    created.Issue.ID,
		Changed:    true,
		RowVersion: created.Issue.RowVersion,
		Issue:      created.Issue,
	}
	return nil
}

// applyUpdate patches one row through the same body a single update runs, which
// is what makes the guards evaluate AS-MODIFIED for free: the row it reads is
// the row this request has already written.
func (r *applyBatchRun) applyUpdate(ctx context.Context, tx *sql.Tx, index int, item *publicops.UpdateItem) error {
	id, err := r.resolve(item.Target, index, "target")
	if err != nil {
		return err
	}
	updated, tables, err := ExecuteUpdate(ctx, tx, publicops.UpdateRequest{
		Actor:                 r.plan.Actor,
		IssueID:               id,
		Patch:                 item.Patch,
		ExpectedVersion:       item.ExpectedVersion,
		ExpectedStatus:        item.ExpectedStatus,
		ExpectedAssignee:      item.ExpectedAssignee,
		ForceClosePolicy:      item.ForceClosePolicy,
		ForceAssigneeTransfer: item.ForceAssigneeTransfer,
	})
	if err != nil {
		return &publicops.ItemError{Index: index, Kind: publicops.ItemUpdate, Key: item.Target.Key, IssueID: id, Err: err}
	}
	r.write.Tables.Merge(tables)
	r.write.Changed = r.write.Changed || updated.Changed || len(tables) > 0
	r.result.Items[index] = publicops.ItemResult{
		Kind:       publicops.ItemUpdate,
		IssueID:    id,
		Changed:    updated.Changed,
		RowVersion: updated.Issue.RowVersion,
		Issue:      updated.Issue,
	}
	return nil
}

// applyClose closes one row through the same body a single close runs, so close
// policy is evaluated at THIS ITEM against the row as this request has already
// changed it.
func (r *applyBatchRun) applyClose(ctx context.Context, tx *sql.Tx, index int, item *publicops.CloseItem) error {
	id, err := r.resolve(item.Target, index, "target")
	if err != nil {
		return err
	}
	closed, tables, err := ExecuteClose(ctx, tx, publicops.CloseRequest{
		Actor:           r.plan.Actor,
		IssueID:         id,
		Reason:          item.Reason,
		Session:         item.Session,
		Force:           item.Force,
		ExpectedVersion: item.ExpectedVersion,
	})
	if err != nil {
		return &publicops.ItemError{Index: index, Kind: publicops.ItemClose, Key: item.Target.Key, IssueID: id, Err: err}
	}
	r.write.Tables.Merge(tables)
	r.write.Changed = r.write.Changed || closed.Changed || len(tables) > 0
	r.result.Items[index] = publicops.ItemResult{
		Kind:       publicops.ItemClose,
		IssueID:    id,
		Changed:    closed.Changed,
		RowVersion: closed.Issue.RowVersion,
		Issue:      closed.Issue,
	}
	return nil
}

// applyDepAdd writes one edge, source-routed exactly as DependencyEditor routes
// one.
//
// THE EDGE'S Changed IS WHETHER A ROW WAS GENUINELY WRITTEN. AddDependencyInTx
// records a dependency_added event only for a new edge, so an idempotent
// re-add of the same pair with the same type reports Changed false and stages
// nothing — the rule RemoveDependencyResult.Removed states for the mirror
// operation.
func (r *applyBatchRun) applyDepAdd(ctx context.Context, tx *sql.Tx, index int, item *publicops.DepAddItem) error {
	source, err := r.resolve(item.Source, index, "source")
	if err != nil {
		return err
	}
	target, err := r.resolve(item.Target, index, "target")
	if err != nil {
		return err
	}
	itemErr := func(err error) error {
		return &publicops.ItemError{Index: index, Kind: publicops.ItemDepAdd, Key: item.Source.Key, IssueID: source, Err: err}
	}
	if source == target {
		// Two different refs can still name one row — a key and the id it was
		// bound to. The planner catches only the syntactically identical pair.
		return itemErr(fmt.Errorf("%w: %s", publicops.ErrSelfDependency, source))
	}
	// THE CROSS-PLANE REFUSAL IS ABOUT ROWS THIS REQUEST CREATED, and only
	// those. The two planes hold their edges in different tables, so an edge
	// between a durable row and a wisp the same batch minted is one the batch
	// that created both ends cannot write — BatchCreator's rule, which the
	// store engine enforces there because it sees the batch as a set and cannot
	// here, because these creates ran one at a time. An edge between rows that
	// already existed is untouched by this: either class may depend on the
	// other (DependencyEditor).
	sourceWisp, sourceMine := r.planes[source]
	targetWisp, targetMine := r.planes[target]
	if sourceMine && targetMine && sourceWisp != targetWisp {
		return itemErr(CrossPlaneBatchEdgeError(source, target))
	}
	if !sourceMine {
		sourceWisp = IsActiveWispInTx(ctx, tx, source)
	}
	sourceTable, _, eventTable, depTable := WispTableRouting(sourceWisp)

	dep := &types.Dependency{
		IssueID:     source,
		DependsOnID: target,
		Type:        item.Type,
		Metadata:    item.Metadata,
	}
	eventWritten, err := AddDependencyInTx(ctx, tx, dep, r.plan.Actor, AddDependencyOpts{
		SourceTable:    sourceTable,
		WriteTable:     depTable,
		IsCrossPrefix:  types.ExtractPrefix(source) != types.ExtractPrefix(target),
		SkipCycleCheck: r.plan.SkipPerEdgeCycleCheck,
		EmitEvent:      true,
	})
	if err != nil {
		return itemErr(err)
	}
	// Stage the source's dependency table always and its events table only when
	// a row was recorded — the selective staging addDependencyEdgeInTx
	// documents, so an idempotent re-add cannot sweep unrelated pending event
	// rows into this commit (GH#2455).
	r.write.Tables.Add(depTable)
	// The edge row was written EITHER WAY: a new edge is an insert, and a
	// same-type re-add rewrites that row's metadata. So the transaction has
	// something to commit even when the caller sees no change.
	r.write.Changed = true
	if eventWritten {
		r.write.Tables.Add(eventTable)
	}
	r.edges = append(r.edges, appliedEdge{
		index: index,
		edge:  publicops.DependencyEdge{IssueID: source, DependsOnID: target, Type: item.Type},
	})
	r.result.Items[index] = publicops.ItemResult{
		Kind:        publicops.ItemDepAdd,
		IssueID:     source,
		DependsOnID: target,
		Changed:     eventWritten,
	}
	return nil
}

// spliceMetadataRefs writes the resolved ids into the metadata of the create
// items that asked for them, AFTER every id in the request exists.
//
// THAT ORDERING IS THE WHOLE REASON THIS IS A SECOND PASS, and it is what lets
// a metadata ref reach FORWARD while a target ref may not: a target has to be a
// row an item can act on, a metadata ref is a value, and by the time this runs
// there is no direction left to be wrong about.
//
// IT GOES THROUGH ExecuteUpdate, so the splice records an update event on the
// spliced row and answers to the same metadata validation any other write does.
// A caller reading the event stream sees a create and then an update rather
// than one create carrying values nothing could have known yet — which is
// honest, and is what the leaf documents.
func (r *applyBatchRun) spliceMetadataRefs(ctx context.Context, tx *sql.Tx) error {
	for index := range r.plan.Items {
		item := r.plan.Items[index]
		if item.Kind != publicops.ItemCreate || len(item.Create.MetadataRefs) == 0 {
			continue
		}
		set, err := r.resolveMetadataRefs(index, item.Create.MetadataRefs)
		if err != nil {
			return err
		}
		id := r.result.Items[index].IssueID
		updated, tables, err := ExecuteUpdate(ctx, tx, publicops.UpdateRequest{
			Actor:   r.plan.Actor,
			IssueID: id,
			Patch:   publicops.IssuePatch{Metadata: publicops.MetadataPatch{Set: set}},
		})
		if err != nil {
			return &publicops.ItemError{
				Index: index, Kind: publicops.ItemCreate, Key: item.Create.Key, IssueID: id, Err: err,
			}
		}
		r.write.Tables.Merge(tables)
		// The create already reported Changed; the splice re-reads the row so
		// the snapshot the caller is handed carries the spliced metadata and the
		// version token the splice produced, rather than the ones from before it.
		r.result.Items[index].Issue = updated.Issue
		r.result.Items[index].RowVersion = updated.Issue.RowVersion
	}
	return nil
}

// resolveMetadataRefs turns one item's refs into the metadata keys to write.
//
// A REF RESOLVES TO AN ID, and the id is written as a JSON STRING — the whole
// value of that key, one level deep. Nothing merges into a nested object,
// because the metadata object's nesting is value structure and this splice
// replaces values.
func (r *applyBatchRun) resolveMetadataRefs(index int, refs map[string]publicops.Ref) (map[string]json.RawMessage, error) {
	set := make(map[string]json.RawMessage, len(refs))
	// Sorted so the update this composes is byte-identical run to run, which is
	// what keeps the metadata write's own deterministic key order deterministic
	// from here as well.
	metaKeys := make([]string, 0, len(refs))
	for metaKey := range refs {
		metaKeys = append(metaKeys, metaKey)
	}
	sort.Strings(metaKeys)
	for _, metaKey := range metaKeys {
		ref := refs[metaKey]
		id := ref.ID
		if ref.Key != "" {
			resolved, ok := r.keys[ref.Key]
			if !ok {
				// PlanApplyBatch proved the key is declared, so the only way to
				// be here is a declaring create item that minted no id, which
				// cannot happen without an error having already returned.
				return nil, &publicops.RefError{Index: index, Member: "metadata_ref " + metaKey, Key: ref.Key}
			}
			id = resolved
		}
		encoded, err := json.Marshal(id)
		if err != nil {
			return nil, fmt.Errorf("%w: apply batch item %d: encoding metadata_ref %q: %v",
				storage.ErrValidation, index, metaKey, err)
		}
		set[metaKey] = encoded
	}
	return set, nil
}

// runEndGate re-validates the graph the WHOLE request produced, and it is never
// skippable.
//
// IT IS WHY THIS ROLE CAN REFUSE TO REORDER. DependencyEditor applies every
// parent-child edge before any blocking one so the planned hierarchy is
// complete before a blocking edge is checked against it; this role keeps the
// caller's order, so a blocking edge can be written before the parent-child
// edge that makes it a hierarchy conflict — and the per-edge probe that ran at
// the time saw a graph that had not been built yet.
//
// The two halves are different questions and are checked separately. The CYCLE
// half is a property of the edge SET: no single edge owns a cycle several of
// them close together, so it is raised as the request's own refusal with no
// item wrapper. The HIERARCHY half IS per edge — one blocking edge gating one
// issue on one of its own ancestors — so it names the item that carried it.
func (r *applyBatchRun) runEndGate(ctx context.Context, tx *sql.Tx) error {
	if len(r.edges) == 0 {
		return nil
	}
	edges := make([]publicops.DependencyEdge, 0, len(r.edges))
	for _, applied := range r.edges {
		edges = append(edges, applied.edge)
	}
	if err := checkAddedEdgesForCycles(ctx, tx, edges); err != nil {
		return err
	}
	for _, applied := range r.edges {
		dep := &types.Dependency{
			IssueID:     applied.edge.IssueID,
			DependsOnID: applied.edge.DependsOnID,
			Type:        applied.edge.Type,
		}
		// An external or cross-repository target has no local hierarchy to
		// conflict with, and the per-edge path filters it for the same reason.
		if IsExternalDepTarget(dep.IssueID, dep.DependsOnID) {
			continue
		}
		if err := CheckBlockingHierarchyInTx(ctx, tx, dep, cycleDetectionTables()); err != nil {
			return &publicops.ItemError{
				Index:   applied.index,
				Kind:    publicops.ItemDepAdd,
				IssueID: dep.IssueID,
				Err:     err,
			}
		}
	}
	return nil
}

// resolve turns one ref into the id it names.
//
// A KEY IS ALWAYS ALREADY BOUND HERE: PlanApplyBatch proved every target key is
// declared by an EARLIER item, and an earlier create that failed returned
// before this one ran. The refusal below is the one that would be raised if
// that invariant were ever broken, rather than a nil id handed to a verb.
func (r *applyBatchRun) resolve(ref publicops.Ref, index int, member string) (string, error) {
	if ref.Key == "" {
		return ref.ID, nil
	}
	id, ok := r.keys[ref.Key]
	if !ok {
		return "", &publicops.RefError{Index: index, Member: member, Key: ref.Key}
	}
	return id, nil
}
