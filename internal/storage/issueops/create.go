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
	if err := PrepareIssueForInsert(issue, bc.CustomStatuses, bc.CustomTypes); err != nil {
		return result, err
	}

	issueTable, eventTable := TableRouting(issue)

	if err := assignCreateIssueIDInTx(ctx, tx, bc, issue, actor); err != nil {
		return result, err
	}
	if bc.Opts.CreateOnly {
		if err := EnsureIssueIDAvailableInTx(ctx, tx, issue.ID); err != nil {
			return result, err
		}
	}

	if skip, err := checkCrossTableIDCollision(ctx, tx, issue.ID, issueTable, bc.Opts); err != nil {
		return result, err
	} else if skip {
		return result, nil
	}

	isNew, staleRejected, err := InsertIssueIfNew(ctx, tx, issueTable, issue, bc.Opts)
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
		if err := RecordEventInTable(ctx, tx, eventTable, issue.ID, types.EventCreated, actor, ""); err != nil {
			return result, fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
		}
		result.markChanged(eventTable)
	}

	labelResult, err := PersistLabels(ctx, tx, issue, actor, eventTable)
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
	filteredIssues, err := filterCreateIssuesMixedBucketDependencies(issues, opts)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	issues = filteredIssues

	// This function already runs a slice-wide ReconcileChildCounters below,
	// covering every accepted issue; skip the redundant per-issue reconcile.
	// Set the flag on a shallow copy so the caller's context keeps its own
	// reconcile behavior.
	batch := *bc
	batch.SkipChildCounterReconcile = true

	result := CreateIssuesResult{}
	accepted := issues[:0:0]
	for _, issue := range issues {
		issueResult, err := CreateIssueInTxWithResult(ctx, tx, &batch, issue, actor)
		if err != nil {
			return CreateIssuesResult{}, err
		}
		result.merge(issueResult.ChangedTables)
		if issueResult.StaleRejected {
			continue // stale snapshot: keep its deps out of the batch too
		}
		accepted = append(accepted, issue)
	}
	issues = accepted

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
	recomputed, err := RecomputeIsBlockedInTxWithResult(ctx, tx, issueIDs, wispIDs)
	if err != nil {
		return CreateIssuesResult{}, err
	}
	if recomputed.IssueRowsChanged {
		result.markChanged("issues")
	}
	if recomputed.WispRowsChanged {
		result.markChanged("wisps")
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

// ValidateCreateIssuesMixedBucketDependencies rejects same-batch dependency
// edges between regular issues and wisps. Dependencies are stored in separate
// backing tables per bucket, so a batch cannot create both ends atomically when
// the edge crosses buckets.
func ValidateCreateIssuesMixedBucketDependencies(issues []*types.Issue) error {
	_, err := filterCreateIssuesMixedBucketDependencies(issues, storage.BatchCreateOptions{})
	return err
}

// FilterCreateIssuesMixedBucketDependencies applies the same cross-bucket
// dependency policy as CreateIssuesInTx, but over the full issue set. Callers
// that split one logical batch into bounded sub-batches (chunked import) must
// run this once up front: the per-batch filter inside the engine only sees one
// sub-batch, so it could no longer detect an edge whose endpoints land in
// different chunks. Filtered edges are reported via opts.OnSkippedDependency;
// issues whose dependency list changes are copied, never mutated.
func FilterCreateIssuesMixedBucketDependencies(issues []*types.Issue, opts storage.BatchCreateOptions) ([]*types.Issue, error) {
	return filterCreateIssuesMixedBucketDependencies(issues, opts)
}

func filterCreateIssuesMixedBucketDependencies(issues []*types.Issue, opts storage.BatchCreateOptions) ([]*types.Issue, error) {
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
		return issues, nil
	}

	var filteredIssues []*types.Issue
	for issueIndex, issue := range issues {
		if issue == nil {
			continue
		}
		var keptDeps []*types.Dependency
		filteredDeps := false
		for depIndex, dep := range issue.Dependencies {
			if dep == nil {
				if filteredDeps {
					keptDeps = append(keptDeps, dep)
				}
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
				if !opts.SkipDependencyValidationErrors {
					// Through the shared constructor, so the two bodies raise
					// one message AND one sentinel: the role promises this
					// refusal is the caller's fault, and an untyped error left
					// callers classifying it by prose.
					return nil, CrossPlaneBatchEdgeError(sourceID, dep.DependsOnID)
				}
				if !filteredDeps {
					keptDeps = append([]*types.Dependency(nil), issue.Dependencies[:depIndex]...)
					filteredDeps = true
				}
				recordSkippedDependencyEdge(opts, sourceID, dep.DependsOnID, "cross-bucket dependency between regular issue and wisp in the same batch")
				continue
			}
			if filteredDeps {
				keptDeps = append(keptDeps, dep)
			}
		}
		if filteredDeps {
			if filteredIssues == nil {
				filteredIssues = append([]*types.Issue(nil), issues...)
			}
			issueCopy := *issue
			issueCopy.Dependencies = keptDeps
			filteredIssues[issueIndex] = &issueCopy
		}
	}
	if filteredIssues != nil {
		return filteredIssues, nil
	}
	return issues, nil
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
	for _, dependency := range dependencies {
		var affectedIssues, affectedWisps []string
		var err error
		if dependency.sourceWisp {
			affectedIssues, affectedWisps, err = AffectedByDepChangeForWispInTx(ctx, tx, dependency.source, dependency.target, dependency.depType)
		} else {
			affectedIssues, affectedWisps, err = AffectedByDepChangeInTx(ctx, tx, dependency.source, dependency.target, dependency.depType)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("affected by created dependency %s -> %s: %w", dependency.source, dependency.target, err)
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
	if id == "" {
		return false, nil
	}
	siblingTable := "wisps"
	if issueTable == "wisps" {
		siblingTable = "issues"
	}
	var siblingCount int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, siblingTable), id).Scan(&siblingCount); err != nil {
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
	var existingCount int
	if issue.ID != "" {
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, issueTable), issue.ID).Scan(&existingCount); err != nil {
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
		comment := "Added label: " + label
		if err := InsertDerivedEvent(ctx, tx, eventTable, AuxEvent{
			IssueID:   issue.ID,
			EventType: types.EventLabelAdded,
			Actor:     actor,
			Comment:   str(comment),
		}); err != nil {
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
	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}
		for _, dep := range issue.Dependencies {
			// Default IssueID to the owning issue when not pre-set (e.g.,
			// markdown bulk create where the ID is auto-generated).
			if dep.IssueID == "" {
				dep.IssueID = issue.ID
			}
			depTable := "dependencies"
			if IsActiveWispInTx(ctx, tx, dep.IssueID) {
				depTable = "wisp_dependencies"
			}
			pending = append(pending, pendingDependency{dep: dep, depTable: depTable})
		}
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
			kind := ClassifyDepTarget(ctx, tx, dep, isCrossPrefix)

			if kind != DepTargetExternal {
				lookupTable := "issues"
				if kind == DepTargetWisp {
					lookupTable = "wisps"
				}
				var exists int
				//nolint:gosec // G201: lookupTable is one of two hardcoded constants
				if err := tx.QueryRowContext(ctx,
					fmt.Sprintf("SELECT 1 FROM %s WHERE id = ?", lookupTable),
					dep.DependsOnID).Scan(&exists); err != nil {
					if err == sql.ErrNoRows {
						recordSkippedDependency(opts, dep, "target not found")
						continue
					}
					return result, fmt.Errorf("failed to check dependency target %s for %s: %w", dep.DependsOnID, dep.IssueID, err)
				}
			}

			if kind != DepTargetExternal && types.ExtractPrefix(dep.IssueID) == types.ExtractPrefix(dep.DependsOnID) {
				if err := CheckBlockingHierarchyInTx(ctx, tx, dep, nil); err != nil {
					if opts.SkipDependencyValidationErrors {
						recordSkippedDependency(opts, dep, err.Error())
						continue
					}
					return result, fmt.Errorf("invalid dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
			}

			if err := CheckDependencyCycleInTx(ctx, tx, dep, nil); err != nil {
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
