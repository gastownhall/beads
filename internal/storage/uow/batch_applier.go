package uow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// BatchApplierSource is the capability accessor a unit-of-work provider offers
// for the apply-many role, the sibling of BatchCreatorSource and
// DependencyEditorSource.
type BatchApplierSource interface {
	BatchApplier() (publicops.BatchApplier, error)
}

// batchApplier applies a heterogeneous batch through one unit of work.
type batchApplier struct {
	provider UnitOfWorkProvider
}

// BatchApplier returns the guarded apply-many surface for this provider.
func (p *doltSQLProvider) BatchApplier() (publicops.BatchApplier, error) {
	return NewBatchApplier(p)
}

// NewBatchApplier constructs a public batch applier backed by provider.
func NewBatchApplier(provider UnitOfWorkProvider) (publicops.BatchApplier, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new batch applier: unit-of-work provider must not be nil")
	}
	return &batchApplier{provider: provider}, nil
}

var _ publicops.BatchApplier = (*batchApplier)(nil)

// ApplyBatch applies every item in ONE unit of work and commits them together.
//
// THIS LEG IS A GENUINELY INDEPENDENT BODY, unlike the third leg of MetadataCAS
// or TreeWalker, and the reason is mechanical rather than chosen. The shared
// store body composes issueops.ExecuteCreate, ExecuteUpdate and ExecuteClose,
// every one of which takes a *sql.Tx; a unit of work's runner is a *sql.Conn
// with a transaction open on it, and no interface between the two publishes the
// other. So the store bodies could not be reached from here without rewriting
// three of the oldest write paths in the tree to take an interface.
//
// WHAT THAT MEANS FOR THE CONTRACT is stated in its header: this is a second
// vote on what a batch MEANS, not a wrapper check, and the cases are worth
// running here for exactly that reason. What it means for this file is that
// every rule the role promises has to be reached through a shared function or
// re-derived deliberately: the request VALIDATION is shared
// (storage.PlanApplyBatch), the commit message is shared
// (issueops.ApplyBatchCommitMessage), and the two halves of the end gate are
// the same repository methods the per-edge path runs.
//
// A BATCH THAT CHANGED NOTHING COMPOSES NO COMMIT MESSAGE, which is
// RunTxResult's existing signal for a unit of work with nothing to version —
// and the message is composed from what LANDED on either plane, so a wisp-only
// batch still commits. That is the trap ApplyBatchCommitMessage documents.
func (o *batchApplier) ApplyBatch(ctx context.Context, request publicops.ApplyBatchRequest) (publicops.ApplyBatchResult, error) {
	plan, err := storage.PlanApplyBatch(request)
	if err != nil {
		return publicops.ApplyBatchResult{}, err
	}
	return RunTxResult(ctx, o.provider, func(ctx context.Context, uw UnitOfWork) (publicops.ApplyBatchResult, string, error) {
		run := &uowApplyRun{
			plan:   plan,
			uw:     uw,
			keys:   make(map[string]string, len(plan.KeyIndex)),
			planes: map[string]bool{},
			result: publicops.ApplyBatchResult{
				Keys:  make(map[string]string, len(plan.KeyIndex)),
				Items: make([]publicops.ItemResult, len(plan.Items)),
			},
		}
		if err := run.apply(ctx); err != nil {
			return publicops.ApplyBatchResult{}, "", err
		}
		return run.result, storageissueops.ApplyBatchCommitMessage(plan, run.result, run.write), nil
	})
}

// uowApplyRun carries the state one request accumulates on this backend. It is
// the sibling of the store body's applyBatchRun and holds the same four facts:
// the ids the keys bound to, the plane each created row landed on, the edges
// added, and the outcomes.
type uowApplyRun struct {
	plan   storage.ApplyBatchPlan
	uw     UnitOfWork
	keys   map[string]string
	planes map[string]bool
	edges  []uowAppliedEdge
	result publicops.ApplyBatchResult
	// write records whether this unit of work has anything to commit, on the
	// same terms the store bodies record it: a create and an edge always write,
	// an update and a close write when they changed something. It carries no
	// tables — this backend stages nothing, its whole unit of work commits — so
	// the commit message is composed from Changed alone.
	write storageissueops.BatchApplyWrite
	// createContext is the workspace's prefix, statuses, types and infra-type
	// set, loaded ONCE for the whole request. Nothing an item writes changes
	// it, and a request may carry a hundred creates: loading it per item would
	// be a hundred config reads inside one transaction.
	createContext *domain.CreateContext
}

type uowAppliedEdge struct {
	index int
	dep   *types.Dependency
}

func (r *uowApplyRun) apply(ctx context.Context) error {
	for i := range r.plan.Items {
		if err := r.applyItem(ctx, i); err != nil {
			return err
		}
	}
	if err := r.spliceMetadataRefs(ctx); err != nil {
		return err
	}
	return r.runEndGate(ctx)
}

func (r *uowApplyRun) applyItem(ctx context.Context, index int) error {
	item := r.plan.Items[index]
	switch item.Kind {
	case publicops.ItemCreate:
		return r.applyCreate(ctx, index, item.Create)
	case publicops.ItemUpdate:
		return r.applyUpdate(ctx, index, item.Update)
	case publicops.ItemClose:
		return r.applyClose(ctx, index, item.Close)
	case publicops.ItemDepAdd:
		return r.applyDepAdd(ctx, index, item.DepAdd)
	}
	return fmt.Errorf("%w: apply batch item %d has unknown kind %q", publicops.ErrValidation, index, item.Kind)
}

// applyCreate mints one row through the same preparation, infra-type routing
// and error classification the single create and the create batch run, so an
// item's content rules ARE Lifecycle.Create's.
func (r *uowApplyRun) applyCreate(ctx context.Context, index int, item *publicops.CreateItem) error {
	itemErr := func(err error) error {
		return &publicops.ItemError{Index: index, Kind: publicops.ItemCreate, Key: item.Key, Err: err}
	}
	createContext, err := r.loadCreateContext(ctx)
	if err != nil {
		return err
	}
	prepared, err := storageissueops.PreparePublicCreateRequest(publicops.CreateRequest{
		Actor:         r.plan.Actor,
		Issue:         item.Issue,
		ForceIDPrefix: r.plan.ForceIDPrefix,
	}, storageissueops.PublicCreateContext{
		IssuePrefix:     createContext.IssuePrefix,
		AllowedPrefixes: createContext.AllowedPrefixes,
		CustomStatuses:  types.CustomStatusNames(createContext.CustomStatuses),
		CustomTypes:     createContext.CustomTypes,
	})
	if err != nil {
		return itemErr(err)
	}
	// Configured infra types live in the wisp tables, the same routing every
	// other create path applies. A no-history create keeps its own mode.
	if !prepared.Issue.Ephemeral && !prepared.Issue.NoHistory && createContext.InfraTypes[string(prepared.Issue.IssueType)] {
		prepared.Issue.Ephemeral = true
	}
	// A wisp_type is a claim of ephemerality, same as every other create path.
	if !prepared.Issue.Ephemeral && !prepared.Issue.NoHistory && prepared.Issue.WispType != "" {
		prepared.Issue.Ephemeral = true
	}
	params, useWisp, err := createParams(prepared)
	if err != nil {
		return itemErr(validationError(err))
	}
	var created domain.CreateIssueResult
	if useWisp {
		created, err = r.uw.IssueUseCase().CreateWisp(ctx, params, r.plan.Actor)
	} else {
		created, err = r.uw.IssueUseCase().CreateIssue(ctx, params, r.plan.Actor)
	}
	if err != nil {
		return itemErr(storageissueops.ClassifyPublicCreateError(err))
	}
	issue, err := hydrateIssueOperation(ctx, r.uw, created.Issue, false, false)
	if err != nil {
		return err
	}
	if item.Key != "" {
		r.keys[item.Key] = issue.ID
		r.result.Keys[item.Key] = issue.ID
	}
	r.planes[issue.ID] = useWisp
	r.write.Changed = true
	r.result.Items[index] = publicops.ItemResult{
		Kind:       publicops.ItemCreate,
		IssueID:    issue.ID,
		Changed:    true,
		RowVersion: issue.RowVersion,
		Issue:      issue,
	}
	return nil
}

// loadCreateContext reads the workspace's create vocabulary once per request.
func (r *uowApplyRun) loadCreateContext(ctx context.Context) (domain.CreateContext, error) {
	if r.createContext != nil {
		return *r.createContext, nil
	}
	loaded, err := r.uw.ConfigUseCase().LoadCreateContext(ctx)
	if err != nil {
		return domain.CreateContext{}, err
	}
	r.createContext = &loaded
	return loaded, nil
}

// applyUpdate patches one row through the same use case the single update runs,
// including the assignee fence that lives above ApplyUpdate rather than in it.
func (r *uowApplyRun) applyUpdate(ctx context.Context, index int, item *publicops.UpdateItem) error {
	id, err := r.resolve(item.Target, index, "target")
	if err != nil {
		return err
	}
	itemErr := func(err error) error {
		return &publicops.ItemError{Index: index, Kind: publicops.ItemUpdate, Key: item.Target.Key, IssueID: id, Err: err}
	}
	request := publicops.UpdateRequest{
		Actor:                 r.plan.Actor,
		IssueID:               id,
		Patch:                 item.Patch,
		ExpectedVersion:       item.ExpectedVersion,
		ExpectedStatus:        item.ExpectedStatus,
		ExpectedAssignee:      item.ExpectedAssignee,
		ForceClosePolicy:      item.ForceClosePolicy,
		ForceAssigneeTransfer: item.ForceAssigneeTransfer,
	}
	if err := validateUpdateRequest(request); err != nil {
		return itemErr(err)
	}
	updated, err := r.runUpdate(ctx, request)
	if err != nil {
		return itemErr(err)
	}
	r.write.Changed = r.write.Changed || updated.Changed
	r.result.Items[index] = publicops.ItemResult{
		Kind:       publicops.ItemUpdate,
		IssueID:    id,
		Changed:    updated.Changed,
		RowVersion: updated.Issue.RowVersion,
		Issue:      updated.Issue,
	}
	return nil
}

// runUpdate is the batch/plan path's own independent twin of
// issueOperations.Update's transaction body (issue_operations.go) — the two
// share no call path, so "cannot drift" was never true as a mechanism; they
// must be kept in lockstep by hand on every fence, guard and Changed-rule
// change. ga-v2k49 is a concrete instance of that drift: claimChanged's actor
// comparison here and there diverged (one verbatim, one not) until both were
// found and fixed independently.
func (r *uowApplyRun) runUpdate(ctx context.Context, request publicops.UpdateRequest) (publicops.UpdateResult, error) {
	spec, err := updateSpec(request)
	if err != nil {
		return publicops.UpdateResult{}, validationError(err)
	}
	before, _, err := operationIssue(ctx, r.uw, request.IssueID, request.IssuePlaneOnly)
	if err != nil {
		return publicops.UpdateResult{}, err
	}
	before, err = hydrateIssueOperation(ctx, r.uw, before, false, request.IssuePlaneOnly)
	if err != nil {
		return publicops.UpdateResult{}, err
	}
	if updatePreconditionsHold(request, before) {
		if err := authorizeAssigneeTransfer(ctx, r.uw, before, request); err != nil {
			return publicops.UpdateResult{}, err
		}
	}
	// ActorMatches (not a verbatim compare, ga-v2k49): see the identical
	// comment on issueOperations.Update's claimChanged in issue_operations.go
	// — this is that expression's independent twin, which must reach the same
	// answer despite having no shared code path with it.
	claimChanged := request.Claim && (before.Status != types.StatusInProgress || !storageissueops.ActorMatches(before.Assignee, request.Actor))
	updated, err := r.uw.IssueUseCase().ApplyUpdate(ctx, request.IssueID, spec, request.Actor)
	if err != nil {
		return publicops.UpdateResult{}, err
	}
	issue, err := hydrateIssueOperation(ctx, r.uw, updated, false, request.IssuePlaneOnly)
	if err != nil {
		return publicops.UpdateResult{}, err
	}
	return publicops.UpdateResult{Issue: issue, Changed: claimChanged || !semanticIssueEqual(before, issue)}, nil
}

// applyClose closes one row through the same use case the single close runs, so
// close policy is evaluated at THIS ITEM against the row as this request has
// already changed it.
func (r *uowApplyRun) applyClose(ctx context.Context, index int, item *publicops.CloseItem) error {
	id, err := r.resolve(item.Target, index, "target")
	if err != nil {
		return err
	}
	itemErr := func(err error) error {
		return &publicops.ItemError{Index: index, Kind: publicops.ItemClose, Key: item.Target.Key, IssueID: id, Err: err}
	}
	issue, useWisp, err := operationIssue(ctx, r.uw, id, false)
	if err != nil {
		return itemErr(err)
	}
	if item.ExpectedVersion != nil {
		if _, err := r.uw.IssueUseCase().ApplyUpdate(ctx, id, domain.UpdateSpec{ExpectedVersion: item.ExpectedVersion}, r.plan.Actor); err != nil {
			return itemErr(err)
		}
	}
	before, err := hydrateIssueOperation(ctx, r.uw, issue, false, false)
	if err != nil {
		return err
	}
	params := domain.CloseIssueParams{Reason: item.Reason, Session: item.Session}
	var closed domain.CloseIssueResult
	// Marked after the ExpectedVersion update above, so a rewind drops this
	// item's close and not that update's notification. See closeBatchItem: the
	// shared close verbs announce every success, and a batch item must announce
	// only what it actually persisted (ga-2yaqp.1).
	mark := markBatchNotifications(r.uw)
	if useWisp {
		closed, err = r.uw.IssueUseCase().CloseWispChecked(ctx, id, params, r.plan.Actor, item.Force)
	} else {
		closed, err = r.uw.IssueUseCase().CloseIssueChecked(ctx, id, params, r.plan.Actor, item.Force)
	}
	if err != nil {
		return itemErr(err)
	}
	if closed.Issue != nil {
		issue = closed.Issue
	}
	hydrated, err := hydrateIssueOperation(ctx, r.uw, issue, false, false)
	if err != nil {
		return err
	}
	closeChanged := !semanticIssueEqual(before, hydrated)
	if !closeChanged {
		rewindBatchNotifications(r.uw, mark)
	}
	r.write.Changed = r.write.Changed || closeChanged
	r.result.Items[index] = publicops.ItemResult{
		Kind:       publicops.ItemClose,
		IssueID:    id,
		Changed:    closeChanged,
		RowVersion: hydrated.RowVersion,
		Issue:      hydrated,
	}
	return nil
}

// applyDepAdd writes one edge through the SOURCE-ROUTED bulk verb, which is
// what makes an edge land in the same plane the store bodies land it in.
//
// WHETHER THE EDGE WAS NEW IS READ BEFORE THE WRITE, because this backend's
// insert does not report it: BulkAddDepsResult echoes the request. A pre-read
// per edge is bounded by the request's hundred-item cap and is the only way
// ItemResult.Changed can mean the same thing on all three legs — an idempotent
// re-add of the same pair with the same type changed nothing.
func (r *uowApplyRun) applyDepAdd(ctx context.Context, index int, item *publicops.DepAddItem) error {
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
		return itemErr(fmt.Errorf("%w: %s", publicops.ErrSelfDependency, source))
	}
	sourceWisp, sourceMine := r.planes[source]
	targetWisp, targetMine := r.planes[target]
	if sourceMine && targetMine && sourceWisp != targetWisp {
		return itemErr(storageissueops.CrossPlaneBatchEdgeError(source, target))
	}
	existed, err := r.edgeExists(ctx, source, target, item.Type)
	if err != nil {
		return itemErr(err)
	}
	dep := &types.Dependency{
		IssueID:     source,
		DependsOnID: target,
		Type:        item.Type,
		Metadata:    item.Metadata,
	}
	if _, err := r.uw.DependencyUseCase().AddDependencies(ctx, []*types.Dependency{dep}, r.plan.Actor, domain.BulkAddDepsOpts{
		SkipPerEdgeCycleCheck: r.plan.SkipPerEdgeCycleCheck,
	}); err != nil {
		return itemErr(err)
	}
	// The edge row was written EITHER WAY — a new edge is an insert, a same-type
	// re-add rewrites that row's metadata — so the unit of work has something to
	// commit even when the caller sees no change. The store bodies say the same
	// thing by staging the dependency table unconditionally.
	r.write.Changed = true
	r.edges = append(r.edges, uowAppliedEdge{index: index, dep: dep})
	r.result.Items[index] = publicops.ItemResult{
		Kind:        publicops.ItemDepAdd,
		IssueID:     source,
		DependsOnID: target,
		Changed:     !existed,
	}
	return nil
}

// edgeExists reports whether the source already carries this exact edge with
// this exact type. A pair carrying a DIFFERENT type is not "existing" here: the
// insert refuses it with *DependencyTypeConflictError, so reporting it as
// unchanged would be describing a write that never happens.
func (r *uowApplyRun) edgeExists(ctx context.Context, source, target string, depType types.DependencyType) (bool, error) {
	records, err := r.uw.DependencyUseCase().GetForIssueIDs(ctx, []string{source})
	if err != nil {
		return false, err
	}
	for _, dep := range records[source] {
		if dep.DependsOnID == target && dep.Type == depType {
			return true, nil
		}
	}
	return false, nil
}

// spliceMetadataRefs writes the resolved ids into the metadata of the create
// items that asked for them, AFTER every id in the request exists — the second
// pass that lets a metadata ref reach forward while a target ref may not.
func (r *uowApplyRun) spliceMetadataRefs(ctx context.Context) error {
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
		updated, err := r.runUpdate(ctx, publicops.UpdateRequest{
			Actor:   r.plan.Actor,
			IssueID: id,
			Patch:   publicops.IssuePatch{Metadata: publicops.MetadataPatch{Set: set}},
		})
		if err != nil {
			return &publicops.ItemError{Index: index, Kind: publicops.ItemCreate, Key: item.Create.Key, IssueID: id, Err: err}
		}
		r.result.Items[index].Issue = updated.Issue
		r.result.Items[index].RowVersion = updated.Issue.RowVersion
	}
	return nil
}

func (r *uowApplyRun) resolveMetadataRefs(index int, refs map[string]publicops.Ref) (map[string]json.RawMessage, error) {
	set := make(map[string]json.RawMessage, len(refs))
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
				return nil, &publicops.RefError{Index: index, Member: "metadata_ref " + metaKey, Key: ref.Key}
			}
			id = resolved
		}
		encoded, err := json.Marshal(id)
		if err != nil {
			return nil, fmt.Errorf("%w: apply batch item %d: encoding metadata_ref %q: %v",
				publicops.ErrValidation, index, metaKey, err)
		}
		set[metaKey] = encoded
	}
	return set, nil
}

// runEndGate re-validates the graph the WHOLE request produced, through the
// same two repository checks the per-edge path runs, so this leg and the store
// legs answer the same refusal to the same request. It is never skippable.
func (r *uowApplyRun) runEndGate(ctx context.Context) error {
	if len(r.edges) == 0 {
		return nil
	}
	var pairs [][2]string
	for _, applied := range r.edges {
		if !types.IsSchedulingEdge(applied.dep.Type) {
			continue
		}
		pairs = append(pairs, [2]string{applied.dep.IssueID, applied.dep.DependsOnID})
	}
	cyclePath, err := r.uw.DependencyUseCase().CycleThroughEdges(ctx, pairs)
	if err != nil {
		return fmt.Errorf("final cycle check failed (no edges added): %w", err)
	}
	if cyclePath != "" {
		return domain.NewCycleError("dependency cycle would be created: %s (no edges added; run 'bd dep cycles' for analysis)", cyclePath)
	}
	for _, applied := range r.edges {
		if storageissueops.IsExternalDepTarget(applied.dep.IssueID, applied.dep.DependsOnID) {
			continue
		}
		if err := r.uw.DependencyUseCase().ValidateBlockingHierarchy(ctx, applied.dep); err != nil {
			return &publicops.ItemError{
				Index:   applied.index,
				Kind:    publicops.ItemDepAdd,
				IssueID: applied.dep.IssueID,
				Err:     err,
			}
		}
	}
	return nil
}

// resolve turns one ref into the id it names. A key is always already bound
// here: PlanApplyBatch proved every target key is declared by an EARLIER item,
// and an earlier create that failed returned before this one ran.
func (r *uowApplyRun) resolve(ref publicops.Ref, index int, member string) (string, error) {
	if ref.Key == "" {
		return ref.ID, nil
	}
	id, ok := r.keys[ref.Key]
	if !ok {
		return "", &publicops.RefError{Index: index, Member: member, Key: ref.Key}
	}
	return id, nil
}
