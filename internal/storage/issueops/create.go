package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

// BatchContext holds per-batch state read once and reused for every issue.
type BatchContext struct {
	CustomStatuses  []string
	CustomTypes     []string
	ConfigPrefix    string
	AllowedPrefixes string
	Opts            storage.BatchCreateOptions
	// SkipChildCounterReconcile tells CreateIssueInTxWithResult to skip its
	// per-issue ReconcileChildCounters call. CreateIssuesInTxWithResult sets
	// this because it already runs one slice-wide ReconcileChildCounters over
	// the whole accepted batch after the per-issue loop, which covers every
	// issue the per-issue call would have handled; running it again per issue
	// during a batch import was 3-4 redundant round trips per hierarchical
	// issue for a result the caller discards. Singular creates leave this
	// false so they keep reconciling immediately, per-issue.
	SkipChildCounterReconcile bool
	// DeferVersionMint tells CreateIssueInTxWithResult NOT to mint the
	// issue's version row itself but to report the deferral on its result.
	// CreateIssuesInTxWithContext sets this because it persists the batch's
	// creation-time dependency edges AFTER the per-issue loop, and the first
	// version must carry that outgoing edge set: it mints once per accepted
	// issue after the edges (and the blocked-state recompute) have landed.
	// Singular creates leave this false and mint in place — they never run
	// the dependency pass.
	DeferVersionMint bool
	// cache, when set, answers the per-issue presence/label lookups from one
	// up-front batch read and buffers the batch's audit events for one
	// bulk write (see createBatchCache). CreateIssuesInTxWithContext sets it
	// on its private copy for multi-issue batches; it is never set on a
	// caller's context.
	cache *createBatchCache
}

// NewBatchContext reads config from the database and returns a BatchContext.
func NewBatchContext(ctx context.Context, tx DBTX, opts storage.BatchCreateOptions) (*BatchContext, error) {
	customStatuses, err := GetCustomStatusesTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := ResolveCustomTypesInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get custom types: %w", err)
	}
	configPrefix, err := ReadConfigPrefix(ctx, tx)
	if err != nil {
		return nil, err
	}
	var allowedPrefixes string
	_ = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "allowed_prefixes").Scan(&allowedPrefixes)

	return &BatchContext{
		CustomStatuses:  customStatuses,
		CustomTypes:     customTypes,
		ConfigPrefix:    configPrefix,
		AllowedPrefixes: allowedPrefixes,
		Opts:            opts,
	}, nil
}

func CreateIssueInTx(ctx context.Context, tx DBTX, bc *BatchContext, issue *types.Issue, actor string) error {
	_, err := CreateIssueInTxWithResult(ctx, tx, bc, issue, actor)
	return err
}

// CreateIssueResult reports the tables actually written by CreateIssueInTx.
type CreateIssueResult struct {
	ChangedTables map[string]bool
	// StaleRejected reports that the RejectStaleUpserts guard kept the stored
	// row: nothing was written, and the issue's aux data must not be
	// persisted by later batch stages either (bd-578h9.8).
	StaleRejected         bool
	persistedDependencies []persistedDependency
	// persistedComments are the comments this create actually inserted, carried
	// up to the entry point so their journal rows land AFTER the create's. A
	// consumer must never see a comment for a bead it has not been told about.
	persistedComments []EventComment
	// versionDeferred reports that this create reached the version seam but
	// left the mint to the batch entry point (BatchContext.DeferVersionMint).
	// Set only on the path that would otherwise have minted, so the batch
	// mints for exactly the issues a singular create would have.
	versionDeferred bool
}

type persistedDependency struct {
	source     string
	target     string
	depType    types.DependencyType
	sourceWisp bool
}

func (r *CreateIssueResult) markChanged(table string) {
	if table == "" {
		return
	}
	if r.ChangedTables == nil {
		r.ChangedTables = map[string]bool{}
	}
	r.ChangedTables[table] = true
}

func mergeChangedTables(dst map[string]bool, src map[string]bool) map[string]bool {
	for table := range src {
		if dst == nil {
			dst = map[string]bool{}
		}
		dst[table] = true
	}
	return dst
}

func CreateIssueInTxWithResult(ctx context.Context, tx DBTX, bc *BatchContext, issue *types.Issue, actor string) (CreateIssueResult, error) {
	var result CreateIssueResult
	issueTable, eventTable, skip, err := prepareCreateIssueInTx(ctx, tx, bc, issue, actor)
	if err != nil || skip {
		return result, err
	}

	isNew, staleRejected, err := insertIssueIfNewCached(ctx, tx, issueTable, issue, bc.Opts, bc.cache)
	if err != nil {
		return result, err
	}
	if staleRejected {
		// The stored row is strictly newer than this snapshot: nothing was
		// written, and the snapshot's labels/comments belong to the older
		// version, so they must not merge in either (bd-578h9.8).
		result.StaleRejected = true
		if bc.Opts.OnStaleRejected != nil {
			bc.Opts.OnStaleRejected(issue.ID)
		}
		return result, nil
	}
	return finishCreateIssueInTx(ctx, tx, bc, issue, actor, issueTable, eventTable, isNew)
}

// prepareCreateIssueInTx is the part of a create before its row write:
// normalization and validation, table routing, id assignment, the create-only
// guard and the cross-plane collision check. skip reports a collision the
// options tolerate (ConflictSkip): nothing is written for the issue.
func prepareCreateIssueInTx(ctx context.Context, tx DBTX, bc *BatchContext, issue *types.Issue, actor string) (issueTable, eventTable string, skip bool, err error) {
	if err := PrepareIssueForInsert(issue, bc.CustomStatuses, bc.CustomTypes); err != nil {
		return "", "", false, err
	}

	issueTable, eventTable = TableRouting(issue)

	if err := assignCreateIssueIDInTx(ctx, tx, bc, issue, actor); err != nil {
		return "", "", false, err
	}
	if bc.Opts.CreateOnly {
		if err := EnsureIssueIDAvailableInTx(ctx, tx, issue.ID); err != nil {
			return "", "", false, err
		}
	}

	skip, err = checkCrossTableIDCollisionCached(ctx, tx, issue.ID, issueTable, bc.Opts, bc.cache)
	if err != nil {
		return "", "", false, err
	}
	return issueTable, eventTable, skip, nil
}

// finishCreateIssueInTx is the part of a create after its row is written:
// lease reconciliation, the created event, labels, comments, the child
// counter, the journal entry and the version seam.
func finishCreateIssueInTx(ctx context.Context, tx DBTX, bc *BatchContext, issue *types.Issue, actor, issueTable, eventTable string, isNew bool) (CreateIssueResult, error) {
	var result CreateIssueResult
	result.markChanged(issueTable)

	// Reconcile the ephemeral lease row with the accepted issue state
	// (restore an imported lease / drop an orphaned one — see
	// RestoreLeaseOnImportInTx). Wisps are never leased. The leases table is
	// dolt_ignored, so this is deliberately not marked as a changed table.
	if issueTable == "issues" {
		if err := RestoreLeaseOnImportInTx(ctx, tx, issue, isNew); err != nil {
			return result, err
		}
	}

	if isNew {
		if bc.cache != nil {
			bc.cache.bufferEvent(eventTable, createdAuxEvent(issue.ID, actor))
		} else if err := RecordEventInTable(ctx, tx, eventTable, issue.ID, types.EventCreated, actor, ""); err != nil {
			return result, fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
		}
		result.markChanged(eventTable)
	}

	labelResult, err := persistLabelsCached(ctx, tx, issue, actor, eventTable, bc.cache)
	if err != nil {
		return result, err
	}
	result.ChangedTables = mergeChangedTables(result.ChangedTables, labelResult.ChangedTables)
	commentResult, err := PersistComments(ctx, tx, issue)
	if err != nil {
		return result, err
	}
	result.ChangedTables = mergeChangedTables(result.ChangedTables, commentResult.ChangedTables)
	result.persistedComments = append(result.persistedComments, commentResult.persistedComments...)

	// Advance child_counters when a singular create materializes a hierarchical
	// ID (e.g. bd create --id P.8). The batch path already calls
	// ReconcileChildCounters after CreateIssuesInTx; without this, explicit --id
	// creates leave last_child behind the live suffix high-water mark and the
	// next bd create --parent can recycle lower suffixes (GH#4750).
	if isNew && !bc.SkipChildCounterReconcile {
		if _, childNum, ok := ParseHierarchicalID(issue.ID); ok && childNum > 0 {
			changedCounters, err := ReconcileChildCounters(ctx, tx, []*types.Issue{issue})
			if err != nil {
				return result, err
			}
			result.ChangedTables = mergeChangedTables(result.ChangedTables, changedCounters)
		}
	}
	// Journal the create once, after labels and comments are in the row's
	// transaction, so the snapshot is the complete bead. The early returns above
	// (collision skip, stale reject) wrote nothing and journal nothing.
	if err := RecordEventInTx(ctx, tx, EventCreate, issue.ID, actor); err != nil {
		return result, err
	}
	// The version row is the create's LAST durable-state write. A batch
	// create persists this issue's outgoing edges after this function returns,
	// so it defers the mint to its own end (DeferVersionMint); a singular
	// create has nothing after this point and mints here.
	if bc.DeferVersionMint {
		result.versionDeferred = true
	} else if err := RecordVersionInTx(ctx, tx, issue.ID, actor); err != nil {
		return result, err
	}
	// Creation-time comments (import/interchange carries them inline) are
	// replayable content the create snapshot does NOT contain — issue hydration
	// joins labels but not comments — so each inserted comment gets its own op,
	// emitted after the create so a consumer is never told about a comment on a
	// bead it has not seen created. Dedup hits above inserted nothing and emit
	// nothing.
	for i := range result.persistedComments {
		if err := RecordCommentEventInTx(ctx, tx, issue.ID, &result.persistedComments[i]); err != nil {
			return result, err
		}
	}
	return result, nil
}

func assignCreateIssueIDInTx(ctx context.Context, tx DBTX, bc *BatchContext, issue *types.Issue, actor string) error {
	if issue.ID == "" {
		issueTable, _ := TableRouting(issue)
		prefix := bc.ConfigPrefix
		if issue.PrefixOverride != "" {
			prefix = issue.PrefixOverride
		} else if issue.IDPrefix != "" {
			prefix = bc.ConfigPrefix + "-" + issue.IDPrefix
		} else if IsWisp(issue) {
			prefix = bc.ConfigPrefix + "-wisp"
		}
		var err error
		issue.ID, err = GenerateIssueIDInTable(ctx, tx, issueTable, prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate issue ID: %w", err)
		}
		return nil
	}
	if !bc.Opts.SkipPrefixValidation {
		if err := ValidateIssueIDPrefix(issue.ID, bc.ConfigPrefix, bc.AllowedPrefixes); err != nil {
			return fmt.Errorf("prefix validation failed for %s: %w", issue.ID, err)
		}
	}
	return nil
}

// CreateIssuesResult reports side effects that callers need for selective
// Dolt staging after CreateIssuesInTxWithResult returns.
type CreateIssuesResult struct {
	ChangedTables             map[string]bool
	ChangedChildCounterTables map[string]bool
}

func (r *CreateIssuesResult) markChanged(table string) {
	if table == "" {
		return
	}
	if r.ChangedTables == nil {
		r.ChangedTables = map[string]bool{}
	}
	r.ChangedTables[table] = true
}

func (r *CreateIssuesResult) merge(changed map[string]bool) {
	r.ChangedTables = mergeChangedTables(r.ChangedTables, changed)
}

func CreateIssuesInTx(ctx context.Context, tx DBTX, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	_, err := CreateIssuesInTxWithResult(ctx, tx, issues, actor, opts)
	return err
}

// CreateIssuesInTxWithResult creates issues and reports tables whose writes are
// only knowable after SQL reconciliation, such as child counter advances.
func CreateIssuesInTxWithResult(ctx context.Context, tx DBTX, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) (CreateIssuesResult, error) {
	bc, err := NewBatchContext(ctx, tx, opts)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	return CreateIssuesInTxWithContext(ctx, tx, bc, issues, actor)
}

// CreateIssuesInTxWithContext is CreateIssuesInTxWithResult with a
// caller-supplied BatchContext. Callers that split config reads from row
// writes across SQL sessions (doltTransaction's wisp tier) build the context
// on the session that sees in-transaction config writes and pass it here.
// The caller's bc is not modified, so one context can serve several calls.
func CreateIssuesInTxWithContext(ctx context.Context, tx DBTX, bc *BatchContext, issues []*types.Issue, actor string) (CreateIssuesResult, error) {
	opts := bc.Opts
	// The plane rule is a REFUSAL, never a filter. A strict batch (BatchCreator's
	// contract) is refused whole before any row is written. A tolerant batch
	// (SkipDependencyValidationErrors — the import mode) keeps every in-batch
	// regular<->wisp edge: every row of BOTH planes is written on this one tx
	// below before PersistDependenciesWithOptionsResult runs, and that pass
	// resolves each target on its own plane, so the edge is writable — the
	// old per-batch filter skip-reported it, and since a re-import upserts the
	// rows unchanged the edge was never backfilled (wy-4276q8, wy-a648lq). A
	// caller that writes the two planes on different SQL sessions
	// (doltTransaction.CreateIssues) validates up front and passes one plane
	// per call, so a mixed batch reaching here always shares one tx.
	if !opts.SkipDependencyValidationErrors {
		if err := validateCreateIssuesMixedBucketDependencies(issues); err != nil {
			return CreateIssuesResult{}, err
		}
	}

	// This function already runs a slice-wide ReconcileChildCounters below,
	// covering every accepted issue; skip the redundant per-issue reconcile.
	// Set the flag on a shallow copy so the caller's context keeps its own
	// reconcile behavior.
	batch := *bc
	batch.SkipChildCounterReconcile = true
	// The per-issue create leaves the version mint to this function: the
	// creation-time edges below land after the per-issue loop, and the first
	// version of each issue must carry them (one version per issue at
	// creation, minted last).
	batch.DeferVersionMint = true
	if len(issues) >= createBatchCacheMinIssues && !createFastPathsDisabled.Load() {
		cache, err := newCreateBatchCache(ctx, tx, issues)
		if err != nil {
			return CreateIssuesResult{}, err
		}
		batch.cache = cache
	}

	result := CreateIssuesResult{}
	accepted := issues[:0:0]
	var toVersion []string
	record := func(issue *types.Issue, issueResult CreateIssueResult) {
		result.merge(issueResult.ChangedTables)
		if issueResult.versionDeferred {
			toVersion = append(toVersion, issue.ID)
		}
		if issueResult.StaleRejected {
			return // stale snapshot: keep its deps out of the batch too
		}
		accepted = append(accepted, issue)
	}
	// Brand-new rows are written in multi-row INSERTs (see deferredCreates):
	// a run of them is prepared in order, written together, then finished in
	// order, before the next issue that is not one of them is touched.
	var deferred deferredCreates
	flush := func() error {
		results, err := deferred.flush(ctx, tx, &batch, actor)
		if err != nil {
			return err
		}
		for i, issueResult := range results {
			record(deferred.created[i].issue, issueResult)
		}
		deferred.created = deferred.created[:0]
		return nil
	}
	for _, issue := range issues {
		if batch.cache.deferrable(issue, opts) {
			// deferrable requires the id absent from both planes, so the
			// cross-plane collision check cannot ask for a skip here.
			issueTable, eventTable, _, err := prepareCreateIssueInTx(ctx, tx, &batch, issue, actor)
			if err != nil {
				// The issues before this one are written (and can fail) first,
				// as they would have been one at a time.
				if flushErr := flush(); flushErr != nil {
					return CreateIssuesResult{}, flushErr
				}
				return CreateIssuesResult{}, err
			}
			batch.cache.markInserted(issueTable, issue.ID)
			deferred.created = append(deferred.created, deferredCreate{issue: issue, issueTable: issueTable, eventTable: eventTable})
			continue
		}
		if err := flush(); err != nil {
			return CreateIssuesResult{}, err
		}
		issueResult, err := CreateIssueInTxWithResult(ctx, tx, &batch, issue, actor)
		if err != nil {
			return CreateIssuesResult{}, err
		}
		record(issue, issueResult)
	}
	if err := flush(); err != nil {
		return CreateIssuesResult{}, err
	}
	issues = accepted
	// The buffered created/label_added events land before anything else in
	// the batch could read the events tables (nothing in it does).
	if err := batch.cache.flushEvents(ctx, tx); err != nil {
		return CreateIssuesResult{}, err
	}

	depResult, err := PersistDependenciesWithOptionsResult(ctx, tx, issues, actor, opts)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	result.merge(depResult.ChangedTables)

	changedCounters, err := ReconcileChildCounters(ctx, tx, issues)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	result.ChangedChildCounterTables = changedCounters
	for table := range changedCounters {
		result.markChanged(table)
	}
	issueIDs, wispIDs, err := createBlockedRecomputeIDs(ctx, tx, issues, depResult.persistedDependencies)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	// The ids are this batch's rows, most of them fresh and edgeless: let the
	// recompute skip the union statements for those (planRecomputeInTx).
	recomputed, err := recomputeIsBlockedInTxWithResult(ctx, tx, issueIDs, wispIDs, true)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	if recomputed.IssueRowsChanged {
		result.markChanged("issues")
	}
	if recomputed.WispRowsChanged {
		result.markChanged("wisps")
	}
	// Mint each created issue's first version LAST — after its creation-time
	// edges and the blocked-state recompute — so durable_state carries the
	// outgoing edge set. PersistDependenciesWithOptionsResult writes the edge
	// rows directly and mints nothing itself, so this is the one version per
	// issue at creation. Wisps are excluded by the seam.
	for _, id := range toVersion {
		if err := RecordVersionInTx(ctx, tx, id, actor); err != nil {
			return CreateIssuesResult{}, err
		}
	}
	return result, nil
}

// CreateIssueDirtyTables returns the regular Dolt tables CreateIssueInTx may
// dirty for the given issue. Wisp tables are intentionally omitted because they
// are Dolt-ignored and cannot be staged.
func CreateIssueDirtyTables(ctx context.Context, issue *types.Issue, result CreateIssueResult) map[string]bool {
	dirty := stageableChangedTables(result.ChangedTables)
	if issue == nil {
		return dirty
	}
	if parentID, childNum, ok := ParseHierarchicalID(issue.ID); ok &&
		storage.HasReservedChildCounter(ctx, parentID, childNum) {
		dirty["child_counters"] = true
	}
	return dirty
}

// CreateIssuesDirtyTables returns the regular Dolt tables CreateIssuesInTx may
// dirty, including child counters that reconciliation actually advanced.
func CreateIssuesDirtyTables(ctx context.Context, issues []*types.Issue, result CreateIssuesResult) map[string]bool {
	dirty := stageableChangedTables(result.ChangedTables)
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if parentID, childNum, ok := ParseHierarchicalID(issue.ID); ok &&
			storage.HasReservedChildCounter(ctx, parentID, childNum) {
			dirty["child_counters"] = true
		}
	}
	return dirty
}

func stageableChangedTables(changed map[string]bool) map[string]bool {
	dirty := map[string]bool{}
	for table := range changed {
		if table == "wisps" || strings.HasPrefix(table, "wisp_") {
			continue
		}
		dirty[table] = true
	}
	return dirty
}

// ValidateCreateIssuesMixedBucketDependencies refuses same-batch dependency
// edges between regular issues and wisps. It is the plane rule of the STRICT
// batch (BatchCreator's contract, CrossPlaneBatchEdgeError): a caller whose
// regular and wisp rows are written on different SQL sessions
// (doltTransaction.CreateIssues) cannot create both ends of such an edge
// atomically, so the batch is refused whole before any row is written. It is
// deliberately NOT applied to a tolerant batch (SkipDependencyValidationErrors):
// on one tx the dependency pass runs after every row of both planes exists and
// writes the edge (see CreateIssuesInTxWithContext).
func ValidateCreateIssuesMixedBucketDependencies(issues []*types.Issue) error {
	return validateCreateIssuesMixedBucketDependencies(issues)
}

func validateCreateIssuesMixedBucketDependencies(issues []*types.Issue) error {
	batchWispByID := make(map[string]bool, len(issues))
	hasRegular := false
	hasWisp := false
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		isWisp := IsWisp(issue)
		if isWisp {
			hasWisp = true
		} else {
			hasRegular = true
		}
		if issue.ID != "" {
			batchWispByID[issue.ID] = isWisp
		}
	}
	if !hasRegular || !hasWisp {
		return nil
	}

	for _, issue := range issues {
		if issue == nil {
			continue
		}
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			sourceID := issue.ID
			sourceIsWisp := IsWisp(issue)
			if dep.IssueID != "" {
				sourceID = dep.IssueID
				if isWisp, ok := batchWispByID[sourceID]; ok {
					sourceIsWisp = isWisp
				}
			}
			targetIsWisp, targetInBatch := batchWispByID[dep.DependsOnID]
			if targetInBatch && sourceIsWisp != targetIsWisp {
				// Through the shared constructor, so the two bodies raise
				// one message AND one sentinel: the role promises this
				// refusal is the caller's fault, and an untyped error left
				// callers classifying it by prose.
				return CrossPlaneBatchEdgeError(sourceID, dep.DependsOnID)
			}
		}
	}
	return nil
}

func createBlockedRecomputeIDs(ctx context.Context, tx DBTX, issues []*types.Issue, dependencies []persistedDependency) ([]string, []string, error) {
	issueSeen := make(map[string]bool, len(issues))
	wispSeen := make(map[string]bool, len(issues))
	issueIDs := make([]string, 0, len(issues))
	wispIDs := make([]string, 0, len(issues))
	add := func(id string, isWisp bool) {
		if id == "" {
			return
		}
		if isWisp {
			if !wispSeen[id] {
				wispSeen[id] = true
				wispIDs = append(wispIDs, id)
			}
			return
		}
		if !issueSeen[id] {
			issueSeen[id] = true
			issueIDs = append(issueIDs, id)
		}
	}
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		isWisp := IsWisp(issue)
		add(issue.ID, isWisp)
	}
	// The rows a created edge affects are AffectedByDepChange(ForWisp)InTx's:
	// its source, plus the waiters on a parent-child edge's target, closed
	// over parent-child descendants. That closure distributes over union, so
	// the batch seeds every edge at once and expands once, rather than
	// re-walking the descendants of each edge's source separately.
	var depIssueSeed, depWispSeed, spawnerIDs []string
	depIssueSeen, depWispSeen, spawnerSeen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, dependency := range dependencies {
		switch dependency.depType {
		case types.DepBlocks, types.DepConditionalBlocks, types.DepWaitsFor, types.DepParentChild:
		default:
			continue
		}
		if dependency.sourceWisp {
			if !depWispSeen[dependency.source] {
				depWispSeen[dependency.source] = true
				depWispSeed = append(depWispSeed, dependency.source)
			}
		} else if !depIssueSeen[dependency.source] {
			depIssueSeen[dependency.source] = true
			depIssueSeed = append(depIssueSeed, dependency.source)
		}
		if dependency.depType == types.DepParentChild && dependency.target != "" && !spawnerSeen[dependency.target] {
			spawnerSeen[dependency.target] = true
			spawnerIDs = append(spawnerIDs, dependency.target)
		}
	}
	if len(depIssueSeed) > 0 || len(depWispSeed) > 0 {
		if len(spawnerIDs) > 0 {
			if err := loadWaitersOnSpawnerIDsInTx(ctx, tx, spawnerIDs, &depIssueSeed, depIssueSeen, &depWispSeed, depWispSeen); err != nil {
				return nil, nil, fmt.Errorf("affected by created dependencies: %w", err)
			}
		}
		affectedIssues, affectedWisps, err := expandByParentChildDescendantsInTx(ctx, tx, depIssueSeed, depWispSeed, depIssueSeen, depWispSeen)
		if err != nil {
			return nil, nil, fmt.Errorf("affected by created dependencies: %w", err)
		}
		for _, id := range affectedIssues {
			add(id, false)
		}
		for _, id := range affectedWisps {
			add(id, true)
		}
	}
	return issueIDs, wispIDs, nil
}

// PrepareIssueForInsert normalizes timestamps, validates, and computes the content hash.
func PrepareIssueForInsert(issue *types.Issue, customStatuses, customTypes []string) error {
	if err := ValidateMetadataIfConfigured(issue.Metadata); err != nil {
		return fmt.Errorf("metadata validation failed for issue %s: %w", issue.ID, err)
	}

	// Normalize timestamps to UTC, defaulting to now.
	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	} else {
		issue.CreatedAt = issue.CreatedAt.UTC()
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	} else {
		issue.UpdatedAt = issue.UpdatedAt.UTC()
	}
	// Optional timestamps (closed_at, started_at, due_at, …) may arrive from a
	// JSONL import carrying a non-UTC offset; normalize them to UTC so the stored
	// instant matches created_at/updated_at instead of keeping local wall-clock.
	issue.NormalizeOptionalTimestampsToUTC()

	// Ensure closed issues have a closed_at timestamp.
	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		closedAt := maxTime.Add(time.Second)
		issue.ClosedAt = &closedAt
	}

	if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
		return fmt.Errorf("validation failed for issue %s: %w", issue.ID, err)
	}
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}
	return nil
}

// ValidateIssueIDPrefix validates that the issue ID matches the configured prefix
// or any of the allowed_prefixes.
func ValidateIssueIDPrefix(id, prefix, allowedPrefixes string) error {
	if strings.HasPrefix(id, prefix+"-") {
		return nil
	}
	if allowedPrefixes != "" {
		for _, allowed := range strings.Split(allowedPrefixes, ",") {
			allowed = strings.TrimSpace(allowed)
			if allowed != "" && strings.HasPrefix(id, allowed+"-") {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: issue ID %s does not match configured prefix %s", storage.ErrPrefixMismatch, id, prefix)
}

// ParseHierarchicalID checks if an ID is hierarchical (e.g., "bd-abc.1")
// and returns the parent ID and child number.
func ParseHierarchicalID(id string) (parentID string, childNum int, ok bool) {
	lastDot := strings.LastIndex(id, ".")
	if lastDot == -1 {
		return "", 0, false
	}
	parentID = id[:lastDot]
	var num int
	if _, err := fmt.Sscanf(id[lastDot+1:], "%d", &num); err != nil {
		return "", 0, false
	}
	return parentID, num, true
}

// AllWisps returns true if every issue in the slice should be routed to the
// wisps table (i.e., is ephemeral or no-history). Used to gate the fast path
// that skips Dolt versioning in batch creates.
func AllWisps(issues []*types.Issue) bool {
	for _, issue := range issues {
		if !issue.Ephemeral && !issue.NoHistory {
			return false
		}
	}
	return true
}

// checkCrossTableIDCollision rejects a create whose ID already lives in the
// sibling table (GH#4455). Issues and wisps share one ID space but live in
// separate tables; an ID present in both makes the merge-based lookups
// (bd ready/search) hard-error for the whole store. The target-table
// existence check in InsertIssueIfNew only sees one table, so nothing else in
// the create path closes this hole.
//
// Promotion (PromoteFromEphemeralInTx) deliberately inserts into issues while
// the wisp row still exists, then deletes the wisp — but it calls
// InsertIssueIfNew directly and never routes through here, so its transient
// dual-presence window is unaffected.
//
// ConflictSkip is the auto-import upgrade-recovery path (GH#3955), which must
// never hard-fail; there we skip the colliding row instead (lookups stay
// tolerant via GH#4163).
//
//nolint:gosec // G201: siblingTable is one of two hardcoded constants
func checkCrossTableIDCollision(ctx context.Context, tx DBTX, id, issueTable string, opts storage.BatchCreateOptions) (skip bool, err error) {
	return checkCrossTableIDCollisionCached(ctx, tx, id, issueTable, opts, nil)
}

// checkCrossTableIDCollisionCached is checkCrossTableIDCollision reading the
// sibling plane's presence from cache when it covers id.
func checkCrossTableIDCollisionCached(ctx context.Context, tx DBTX, id, issueTable string, opts storage.BatchCreateOptions, cache *createBatchCache) (skip bool, err error) {
	if id == "" {
		return false, nil
	}
	siblingTable := "wisps"
	if issueTable == "wisps" {
		siblingTable = "issues"
	}
	siblingCount, err := cache.rowCount(ctx, tx, siblingTable, id)
	if err != nil {
		return false, fmt.Errorf("failed to check cross-table ID collision for %s: %w", id, err)
	}
	if siblingCount == 0 {
		return false, nil
	}
	if opts.ConflictSkip {
		return true, nil
	}
	return false, fmt.Errorf("cannot create %q: ID already exists in the %s table (issues and wisps share one ID space)", id, siblingTable)
}

// InsertIssueIfNew inserts the issue and returns whether it was genuinely new,
// and whether the RejectStaleUpserts guard rejected it.
//
// When opts.ConflictSkip is true and an issue with the same ID already exists,
// the row is left untouched (no UPSERT) and isNew is false. This is the
// auto-import upgrade-recovery guarantee (GH#3955): even if the emptiness
// guard in maybeAutoImportJSONL regresses, a stale issues.jsonl can never
// overwrite live rows — worst case is a no-op. Otherwise the INSERT … ON
// DUPLICATE KEY UPDATE runs, so explicit `bd import` keeps UPSERT semantics;
// with opts.RejectStaleUpserts the update half is conditional on the incoming
// row being strictly newer than the stored one (bd-pkim8, bd-hj85c).
// Staleness is decided by an explicit in-transaction read (stored updated_at
// strictly newer ⇒ rejected) so callers can skip aux persistence and count
// the row as skipped instead of created (bd-578h9.8). Equal-timestamp rows
// are deliberately NOT rejected here, even though the ODKU's
// VALUES(updated_at) > updated_at condition keeps every stored column for
// them: updated_at has second granularity, so a tie may be two distinct
// same-second updates — the local row must win the tie (an incoming row with
// an empty notes field must not wipe local notes), but its aux data
// (labels/comments/deps, which never bump updated_at) still merges
// additively (bd-hj85c).
//
//nolint:gosec // G201: table is a hardcoded constant
func InsertIssueIfNew(ctx context.Context, tx DBTX, issueTable string, issue *types.Issue, opts storage.BatchCreateOptions) (isNew bool, staleRejected bool, err error) {
	return insertIssueIfNewCached(ctx, tx, issueTable, issue, opts, nil)
}

// insertIssueIfNewCached is InsertIssueIfNew reading the row's presence from
// cache when it covers the id, and recording the write in cache.
func insertIssueIfNewCached(ctx context.Context, tx DBTX, issueTable string, issue *types.Issue, opts storage.BatchCreateOptions, cache *createBatchCache) (isNew bool, staleRejected bool, err error) {
	var existingCount int
	if issue.ID != "" {
		existingCount, err = cache.rowCount(ctx, tx, issueTable, issue.ID)
		if err != nil {
			return false, false, fmt.Errorf("failed to check issue existence for %s: %w", issue.ID, err)
		}
	}
	if opts.ConflictSkip && existingCount > 0 {
		return false, false, nil // issue already exists — skip, never overwrite
	}
	if opts.CreateOnly {
		if err := insertIssueCreateOnly(ctx, tx, issueTable, issue); err != nil {
			if isCreateOnlyDuplicateError(err) {
				return false, false, fmt.Errorf("%w: %s", storage.ErrAlreadyExists, issue.ID)
			}
			return false, false, err
		}
		cache.markInserted(issueTable, issue.ID)
		return true, false, nil
	}
	if opts.RejectStaleUpserts && existingCount > 0 {
		var storedNewer int
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ? AND updated_at > ?`, issueTable), issue.ID, issue.UpdatedAt).Scan(&storedNewer); err != nil {
			return false, false, fmt.Errorf("failed to check issue staleness for %s: %w", issue.ID, err)
		}
		if storedNewer > 0 {
			// The conditional ODKU would keep every stored column anyway;
			// skipping the no-op insert makes the rejection observable.
			return false, true, nil
		}
	}
	if err := insertIssueIntoTable(ctx, tx, issueTable, issue, opts.RejectStaleUpserts); err != nil {
		return false, false, fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
	}
	cache.markInserted(issueTable, issue.ID)
	return existingCount == 0, false, nil
}

func isCreateOnlyDuplicateError(err error) bool {
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return true
	}
	return gmssql.ErrPrimaryKeyViolation.Is(err) || gmssql.ErrUniqueKeyViolation.Is(err)
}

// InsertIssueStrictInTx inserts one issue without probing either storage plane.
// Callers that move an aggregate use it while the source row necessarily still
// occupies the shared ID, so cross-plane create guards would reject a valid move.
func InsertIssueStrictInTx(ctx context.Context, tx DBTX, table string, issue *types.Issue) error {
	if err := insertIssueCreateOnly(ctx, tx, table, issue); err != nil {
		if isCreateOnlyDuplicateError(err) {
			return fmt.Errorf("%w: %s", storage.ErrAlreadyExists, issue.ID)
		}
		return err
	}
	return nil
}

func PersistLabels(ctx context.Context, tx DBTX, issue *types.Issue, actor, eventTable string) (CreateIssueResult, error) {
	return persistLabelsCached(ctx, tx, issue, actor, eventTable, nil)
}

// createdAuxEvent is the created event RecordEventInTable mints for a new
// issue, as an AuxEvent a batch can buffer.
func createdAuxEvent(issueID, actor string) AuxEvent {
	return AuxEvent{
		IssueID:   issueID,
		EventType: types.EventCreated,
		Actor:     actor,
		OldValue:  str(""),
		NewValue:  str(""),
	}
}

// labelAddedAuxEvent is the label_added event PersistLabels records for a
// label its insert actually added.
func labelAddedAuxEvent(issueID, actor, label string) AuxEvent {
	return AuxEvent{
		IssueID:   issueID,
		EventType: types.EventLabelAdded,
		Actor:     actor,
		Comment:   str("Added label: " + label),
	}
}

// persistLabelsCached is PersistLabels for a batch: with a cache it knows the
// issue's stored labels before inserting, so the labels that are new land in
// one multi-row INSERT and their label_added events are buffered, instead of
// one INSERT IGNORE (plus a RowsAffected probe) and one event write per label.
// The labels added, their order, and the events recorded are the same.
func persistLabelsCached(ctx context.Context, tx DBTX, issue *types.Issue, actor, eventTable string, cache *createBatchCache) (CreateIssueResult, error) {
	if cache == nil {
		return persistLabelsPerRow(ctx, tx, issue, actor, eventTable)
	}
	var result CreateIssueResult
	if len(issue.Labels) == 0 {
		return result, nil
	}
	labelTable := "labels"
	if IsWisp(issue) {
		labelTable = "wisp_labels"
	}
	stored, err := cache.storedLabels(ctx, tx, labelTable, issue.ID)
	if err != nil {
		return result, err
	}
	seen := make(map[string]struct{}, len(issue.Labels))
	var added []string
	for _, label := range issue.Labels {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		// Same over-length refusal, in the same label order, as the per-row
		// path; the enclosing transaction rolls everything back.
		if err := types.CheckFieldLen("label", label); err != nil {
			return result, err
		}
		if stored[label] {
			continue
		}
		added = append(added, label)
	}
	if len(added) == 0 {
		return result, nil
	}
	values := make([]string, len(added))
	args := make([]any, 0, 2*len(added))
	for i, label := range added {
		values[i] = "(?, ?)"
		args = append(args, issue.ID, label)
	}
	//nolint:gosec // G201: table is determined by ephemeral flag; only placeholders are formatted in.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT IGNORE INTO %s (issue_id, label)
		VALUES %s
	`, labelTable, strings.Join(values, ", ")), args...); err != nil {
		return result, fmt.Errorf("failed to insert labels for %s: %w", issue.ID, err)
	}
	result.markChanged(labelTable)
	result.markChanged(eventTable)
	for _, label := range added {
		cache.addLabel(labelTable, issue.ID, label)
		cache.bufferEvent(eventTable, labelAddedAuxEvent(issue.ID, actor, label))
	}
	return result, nil
}

func persistLabelsPerRow(ctx context.Context, tx DBTX, issue *types.Issue, actor, eventTable string) (CreateIssueResult, error) {
	var result CreateIssueResult
	if len(issue.Labels) == 0 {
		return result, nil
	}
	labelTable := "labels"
	if IsWisp(issue) {
		labelTable = "wisp_labels"
	}
	seen := make(map[string]struct{}, len(issue.Labels))
	for _, label := range issue.Labels {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		// Reject an over-length label before the INSERT IGNORE, which would
		// otherwise silently truncate it to VARCHAR(255). This is the create and
		// import chokepoint (AddLabelInTx guards the bd label-add path). The whole
		// create runs in one transaction, so returning here rolls it back — the
		// issue and its labels are not persisted.
		if err := types.CheckFieldLen("label", label); err != nil {
			return result, err
		}
		//nolint:gosec // G201: table is determined by ephemeral flag
		sqlResult, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT IGNORE INTO %s (issue_id, label)
			VALUES (?, ?)
		`, labelTable), issue.ID, label)
		if err != nil {
			return result, fmt.Errorf("failed to insert label %q for %s: %w", label, issue.ID, err)
		}
		rowsAffected, err := sqlResult.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("failed to check label insert result for %q on %s: %w", label, issue.ID, err)
		}
		if rowsAffected == 0 {
			continue
		}
		result.markChanged(labelTable)
		if err := InsertDerivedEvent(ctx, tx, eventTable, labelAddedAuxEvent(issue.ID, actor, label)); err != nil {
			return result, fmt.Errorf("failed to record label event %q for %s: %w", label, issue.ID, err)
		}
		result.markChanged(eventTable)
	}
	return result, nil
}

func PersistComments(ctx context.Context, tx DBTX, issue *types.Issue) (CreateIssueResult, error) {
	var result CreateIssueResult
	if len(issue.Comments) == 0 {
		return result, nil
	}
	commentTable := "comments"
	if IsWisp(issue) {
		commentTable = "wisp_comments"
	}
	for _, comment := range issue.Comments {
		createdAt := comment.CreatedAt
		if createdAt.IsZero() {
			// No supplied timestamp: this is a live comment, so stamp it the
			// same way AddIssueComment does — one second past the issue's
			// newest comment when the clock second would collide. Otherwise
			// several such comments in one create share a second and read back
			// in content-digest order rather than the order they were listed.
			stamped, err := NextLiveCommentTime(ctx, tx, commentTable, issue.ID, time.Now())
			if err != nil {
				return result, fmt.Errorf("failed to insert comment for %s: %w", issue.ID, err)
			}
			createdAt = stamped
		}
		createdAtText := FormatAuxTime(createdAt)
		if comment.ID == "" {
			// No incoming id (fresh comment): content-derived id, collapsing
			// onto an identical existing row exactly like the import dedup.
			id, existed, err := InsertDerivedComment(ctx, tx, commentTable, issue.ID, comment.Author, comment.Text, createdAtText)
			if err != nil {
				return result, fmt.Errorf("failed to insert comment for %s: %w", issue.ID, err)
			}
			comment.ID = id
			if !existed {
				result.markChanged(commentTable)
				result.persistedComments = append(result.persistedComments, EventComment{
					ID: id, Author: comment.Author, Text: comment.Text, CreatedAt: createdAt, Source: CommentSourceStructured,
				})
			}
			continue
		}
		// Incoming id (import/interchange): preserve it, with the historical
		// existence check preventing duplicates on re-import.
		var exists int
		//nolint:gosec // G201: table is determined by ephemeral flag
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
				SELECT COUNT(*) FROM %s
				WHERE issue_id = ? AND author = ? AND created_at = ? AND text = ?
			`, commentTable), issue.ID, comment.Author, createdAtText, comment.Text).Scan(&exists); err != nil {
			return result, fmt.Errorf("failed to check comment existence for %s: %w", issue.ID, err)
		}
		if exists > 0 {
			continue
		}
		//nolint:gosec // G201: table is determined by ephemeral flag
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (id, issue_id, author, text, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, commentTable), comment.ID, issue.ID, comment.Author, comment.Text, createdAtText)
		if err != nil {
			return result, fmt.Errorf("failed to insert comment for %s: %w", issue.ID, err)
		}
		result.markChanged(commentTable)
		result.persistedComments = append(result.persistedComments, EventComment{
			ID: comment.ID, Author: comment.Author, Text: comment.Text, CreatedAt: createdAt, Source: CommentSourceStructured,
		})
	}
	return result, nil
}

func PersistDependencies(ctx context.Context, tx DBTX, issues []*types.Issue, actor string) error {
	_, err := PersistDependenciesWithResult(ctx, tx, issues, actor)
	return err
}

func PersistDependenciesWithResult(ctx context.Context, tx DBTX, issues []*types.Issue, actor string) (CreateIssueResult, error) {
	return PersistDependenciesWithOptionsResult(ctx, tx, issues, actor, storage.BatchCreateOptions{})
}

func PersistDependenciesWithOptionsResult(ctx context.Context, tx DBTX, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) (CreateIssueResult, error) {
	var result CreateIssueResult
	type pendingDependency struct {
		dep      *types.Dependency
		depTable string
	}
	var pending []pendingDependency
	var deps []*types.Dependency
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			// Default IssueID to the owning issue when not pre-set (e.g.,
			// markdown bulk create where the ID is auto-generated).
			if dep.IssueID == "" {
				dep.IssueID = issue.ID
			}
			deps = append(deps, dep)
		}
	}
	// A multi-edge batch answers its per-edge routing, presence and graph
	// reads from batch reads (depBatchLookups); nil keeps the per-edge reads.
	var lookups *depBatchLookups
	if len(deps) >= depBatchLookupsMinDeps && !createFastPathsDisabled.Load() {
		var err error
		if lookups, err = newDepBatchLookups(ctx, tx, deps); err != nil {
			return result, err
		}
	}
	for _, dep := range deps {
		depTable := "dependencies"
		if lookups.isWisp(ctx, tx, dep.IssueID) {
			depTable = "wisp_dependencies"
		}
		pending = append(pending, pendingDependency{dep: dep, depTable: depTable})
	}

	// Persist hierarchy first so blocking edges in the same import see the full
	// planned ancestry. The enclosing create transaction rolls this phase back
	// if a later dependency is invalid.
	for phase := 0; phase < 2; phase++ {
		parentPhase := phase == 0
		for _, item := range pending {
			dep := item.dep
			if (dep.Type == types.DepParentChild) != parentPhase {
				continue
			}
			isCrossPrefix := types.ExtractPrefix(dep.IssueID) != types.ExtractPrefix(dep.DependsOnID)
			kind := lookups.classify(ctx, tx, dep, isCrossPrefix)

			if kind != DepTargetExternal {
				exists, err := lookups.targetExists(ctx, tx, kind, dep.DependsOnID)
				if err != nil {
					return result, fmt.Errorf("failed to check dependency target %s for %s: %w", dep.DependsOnID, dep.IssueID, err)
				}
				if !exists {
					recordSkippedDependency(opts, dep, "target not found")
					continue
				}
			}

			if kind != DepTargetExternal && types.ExtractPrefix(dep.IssueID) == types.ExtractPrefix(dep.DependsOnID) {
				if err := lookups.checkHierarchy(ctx, tx, dep); err != nil {
					if opts.SkipDependencyValidationErrors {
						recordSkippedDependency(opts, dep, err.Error())
						continue
					}
					return result, fmt.Errorf("invalid dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
			}

			if err := lookups.checkCycle(ctx, tx, dep); err != nil {
				if opts.SkipDependencyValidationErrors {
					recordSkippedDependency(opts, dep, err.Error())
					continue
				}
				return result, fmt.Errorf("invalid dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
			}

			createdAt := dep.CreatedAt
			if createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			// Deterministic id from (issue_id, target) keeps bulk-imported edges
			// merge-safe across clones — two clones importing the same JSONL get the
			// same primary key, not two random UUIDs that collide on uk_dep_* (#4259).
			createdBy := dependencyCreatedBy(dep, actor)
			metadata := dep.Metadata
			if metadata == "" {
				metadata = "{}"
			}
			//nolint:gosec // G201: item.depTable is one of two hardcoded constants; target column from DepTargetKind.Column()
			sqlResult, err := tx.ExecContext(ctx, fmt.Sprintf(`
					INSERT INTO %s (id, issue_id, %s, type, created_by, created_at, metadata, thread_id)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)
					ON DUPLICATE KEY UPDATE type = type
				`, item.depTable, kind.Column()), depid.New(dep.IssueID, dep.DependsOnID), dep.IssueID, dep.DependsOnID, dep.Type, createdBy, createdAt, metadata, dep.ThreadID)
			if err != nil {
				return result, fmt.Errorf("failed to insert dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
			}
			rowsAffected, err := sqlResult.RowsAffected()
			if err != nil {
				return result, fmt.Errorf("failed to check dependency insert result for %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
			}
			if err := lookups.recordInsert(ctx, tx, item.depTable, dep, rowsAffected); err != nil {
				return result, err
			}
			if rowsAffected > 0 {
				result.markChanged(item.depTable)
				result.persistedDependencies = append(result.persistedDependencies, persistedDependency{
					source:     dep.IssueID,
					target:     dep.DependsOnID,
					depType:    dep.Type,
					sourceWisp: item.depTable == "wisp_dependencies",
				})
				if dep.Type == types.DepParentChild {
					if err := TouchDependencyCoordinationTableInTx(ctx, tx, dep.DependsOnID, item.depTable); err != nil {
						return result, err
					}
				}
				// Creation-time edges are independently replayable operations; do
				// not rely on the issue create payload's inline dependencies.
				if err := RecordDepEventInTx(ctx, tx, EventDepAdd, dep.IssueID, string(dep.Type), dep.DependsOnID, metadata, actor); err != nil {
					return result, err
				}
			}
		}
	}
	return result, nil
}

// dependencyCreatedBy returns the author stamped on a dependency edge.
// Import/restore paths populate dep.CreatedBy from JSONL; interactive
// creation leaves it empty and falls back to the current actor.
func dependencyCreatedBy(dep *types.Dependency, actor string) string {
	if dep != nil && dep.CreatedBy != "" {
		return dep.CreatedBy
	}
	return actor
}

func recordSkippedDependency(opts storage.BatchCreateOptions, dep *types.Dependency, reason string) {
	if dep == nil {
		return
	}
	recordSkippedDependencyEdge(opts, dep.IssueID, dep.DependsOnID, reason)
}

func recordSkippedDependencyEdge(opts storage.BatchCreateOptions, issueID, dependsOnID, reason string) {
	if opts.OnSkippedDependency == nil {
		return
	}
	opts.OnSkippedDependency(issueID, dependsOnID, reason)
}

func ReconcileChildCounters(ctx context.Context, tx DBTX, issues []*types.Issue) (map[string]bool, error) {
	type bucket struct {
		maxChild int
		isWisp   bool
		known    bool
	}
	parents := make(map[string]*bucket)
	var changed map[string]bool

	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if IsWisp(issue) {
			if b, ok := parents[issue.ID]; ok {
				b.isWisp, b.known = true, true
			} else {
				parents[issue.ID] = &bucket{isWisp: true, known: true}
			}
		}
	}

	for _, issue := range issues {
		if issue == nil {
			continue
		}
		parentID, childNum, ok := ParseHierarchicalID(issue.ID)
		if !ok {
			continue
		}
		b, exists := parents[parentID]
		if !exists {
			b = &bucket{}
			parents[parentID] = b
		}
		if childNum > b.maxChild {
			b.maxChild = childNum
		}
	}

	unknownParentIDs := make([]string, 0, len(parents))
	for parentID, b := range parents {
		if b.maxChild > 0 && !b.known {
			unknownParentIDs = append(unknownParentIDs, parentID)
		}
	}
	wispParents, err := WispIDSetInTx(ctx, tx, unknownParentIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to route child counter parents: %w", err)
	}
	for _, parentID := range unknownParentIDs {
		_, parents[parentID].isWisp = wispParents[parentID]
	}

	for parentID, b := range parents {
		if b.maxChild == 0 {
			continue
		}
		table := "child_counters"
		parentTable := "issues"
		if b.isWisp {
			table = "wisp_child_counters"
			parentTable = "wisps"
		}
		var parentExists int
		// Orphaned hierarchical IDs are valid import input when the parent was
		// deleted before export. Their auxiliary counter has no owner and must
		// not be inserted: both counter tables enforce a parent foreign key.
		//nolint:gosec // G201: parentTable is one of two hardcoded constants.
		err := tx.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT 1 FROM %s WHERE id = ?
		`, parentTable), parentID).Scan(&parentExists)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to check child counter parent %s: %w", parentID, err)
		}
		var current int
		//nolint:gosec // G201: table is one of two hardcoded constants.
		err = tx.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT last_child FROM %s WHERE parent_id = ?
		`, table), parentID).Scan(&current)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("failed to read child counter for %s: %w", parentID, err)
		}
		if err == nil && current >= b.maxChild {
			continue
		}
		// Qualify the existing-row column with the table name so the canonical
		// MySQL form and SQLite's translated ON CONFLICT form both unambiguously
		// refer to the target row rather than the incoming value.
		//nolint:gosec // G201: table is one of two hardcoded constants.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %[1]s (parent_id, last_child) VALUES (?, ?)
			ON DUPLICATE KEY UPDATE last_child = GREATEST(%[1]s.last_child, ?)
		`, table), parentID, b.maxChild, b.maxChild); err != nil {
			return nil, fmt.Errorf("failed to reconcile child counter for %s: %w", parentID, err)
		}
		if changed == nil {
			changed = map[string]bool{}
		}
		changed[table] = true
	}
	return changed, nil
}
