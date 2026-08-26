package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// doltTransaction implements storage.Transaction for Dolt
type doltTransaction struct {
	regularTx *sql.Tx
	store     *DoltStore
	dirty     versioncontrolops.DirtyTableTracker
}

// isActiveWisp checks if an ID exists in the wisps table within the transaction.
// Unlike the store-level isActiveWisp, this queries within the transaction so it
// sees uncommitted wisps. Handles both -wisp- pattern and explicit-ID ephemerals (GH#2053).
func (t *doltTransaction) isActiveWisp(ctx context.Context, id string) bool {
	var exists int
	err := t.regularTx.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
	return err == nil
}

// planeForIssueID resolves the aggregate that owns id. Transaction methods have
// historically treated the wisp aggregate as canonical when corrupt/legacy
// data contains the ID in both planes (GetIssue, UpdateIssue, CloseIssue and
// the hook snapshots all do so); the newer lifecycle methods must preserve the
// same choice.
func (t *doltTransaction) planeForIssueID(ctx context.Context, id string) (bool, error) {
	var exists int
	err := t.regularTx.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("resolve transaction plane for %s: %w", id, err)
	}
	err = t.regularTx.QueryRowContext(ctx, "SELECT 1 FROM issues WHERE id = ? LIMIT 1", id).Scan(&exists)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("resolve transaction plane for %s: %w", id, err)
	}
	return false, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
}

// CreateIssueImport is the import-friendly issue creation hook.
// Dolt does not enforce prefix validation at the storage layer, so this delegates to CreateIssue.
func (t *doltTransaction) CreateIssueImport(ctx context.Context, issue *types.Issue, actor string, skipPrefixValidation bool) error {
	return t.CreateIssue(ctx, issue, actor)
}

// RunInTransaction executes a function within a database transaction. Its
// callback is invoked at most once per call; callers retry explicitly after a
// callback has started when their operation is safe to repeat. The commitMsg is
// used for the DOLT_COMMIT that makes regular writes visible in Dolt history.
// Wisp routing is handled by individual transaction methods based on
// ID/Ephemeral.
func (s *DoltStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	return s.runInTransaction(ctx, commitMsg, fn, s.runDoltTransaction)
}

func (s *DoltStore) runInTransaction(
	ctx context.Context,
	commitMsg string,
	fn func(storage.Transaction) error,
	run func(context.Context, string, func(storage.Transaction) error) error,
) error {
	return s.withTransactionSetupRetry(ctx, func() error {
		invoked := false
		var callbackErr error
		err := run(ctx, commitMsg, func(tx storage.Transaction) error {
			invoked = true
			callbackErr = fn(tx)
			return callbackErr
		})
		if invoked && err != nil {
			// Callback failures are caller-owned and must not affect server
			// health accounting. Infrastructure failures after a successful
			// callback keep the at-most-once boundary too, except an explicitly
			// indeterminate commit reaches withRetry so it can record the lost
			// connection before stopping without replay.
			if callbackErr == nil && errors.Is(err, ErrCommitIndeterminate) {
				return err
			}
			return backoff.Permanent(err)
		}
		return err
	})
}

// RunInIssueLifecycleTransaction runs a lifecycle transition and its durable
// side effects through one SQL transaction and one Dolt commit attempt.
func (s *DoltStore) RunInIssueLifecycleTransaction(ctx context.Context, commitMsg string, fn func(tx storage.IssueLifecycleTransaction) error) error {
	return s.runInIssueLifecycleTransaction(ctx, commitMsg, fn, s.withWriteTx)
}

// runInIssueLifecycleTransaction retries only failures that occur before the
// public callback starts. Once fn has run, its caller-owned work must never be
// replayed, even when Dolt proves that the SQL transaction rolled back.
func (s *DoltStore) runInIssueLifecycleTransaction(
	ctx context.Context,
	commitMsg string,
	fn func(tx storage.IssueLifecycleTransaction) error,
	run func(context.Context, func(*sql.Tx) error) error,
) error {
	return s.withTransactionSetupRetry(ctx, func() error {
		invoked := false
		var callbackErr error
		err := run(ctx, func(sqlTx *sql.Tx) error {
			invoked = true
			tx := &doltTransaction{regularTx: sqlTx, store: s}
			if callbackErr = fn(tx); callbackErr != nil {
				return callbackErr
			}
			tables := tx.dirtyTableNames()
			if len(tables) == 0 {
				return nil
			}
			return s.doltAddAndCommitInTx(ctx, sqlTx, tables, commitMsg)
		})
		if invoked && err != nil {
			// An ambiguous commit reaches withRetry so connection failures still
			// count toward the circuit breaker, but it is never replayed.
			if callbackErr == nil && errors.Is(err, ErrCommitIndeterminate) {
				return err
			}
			return backoff.Permanent(err)
		}
		return err
	})
}

func (t *doltTransaction) dirtyTableNames() []string {
	tables := make([]string, 0, len(t.dirty.DirtyTables()))
	for table := range t.dirty.DirtyTables() {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

func (s *DoltStore) runDoltTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	// Pin a single connection for the entire operation: SQL transaction,
	// config protection, and DOLT_COMMIT must all run on the same Dolt
	// session. Each pool connection has an independent working set in Dolt
	// SQL server mode, so mixing connections causes DOLT_COMMIT to see
	// stale or unrelated changes. (GH#2455)

	// Snapshot pool stats before acquisition to detect pool-wait events (GH#3140).
	statsBefore := s.db.Stats()
	acquireStart := time.Now()

	conn, err := s.db.Conn(ctx)
	acquireMs := float64(time.Since(acquireStart).Microseconds()) / 1000.0
	doltMetrics.connAcquireMs.Record(ctx, acquireMs)

	// Detect pool-wait: if WaitCount increased, the pool was exhausted and
	// this caller had to wait for a connection to become available.
	if err == nil {
		statsAfter := s.db.Stats()
		if statsAfter.WaitCount > statsBefore.WaitCount {
			doltMetrics.poolWaitCount.Add(ctx, statsAfter.WaitCount-statsBefore.WaitCount)
			waitMs := float64(statsAfter.WaitDuration-statsBefore.WaitDuration) / float64(time.Millisecond)
			doltMetrics.poolWaitMs.Record(ctx, waitMs)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	regularTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin regular tx: %w", err)
	}

	// Every Transaction callback uses one SQL transaction for both durable and
	// dolt-ignored tables. Besides matching the public all-or-nothing/single-
	// snapshot contract, this is required for cross-plane lifecycle policy and
	// derived blocked-state maintenance: two independent snapshots cannot see
	// each other's same-callback dependency writes. Dolt ignores the wisp tables
	// during DOLT_ADD/DOLT_COMMIT, but their SQL writes commit atomically here.
	journalEnabled := s.eventsJournalEnabled.Load()
	clearJournalScope := issueops.ScopeEventsJournalTransaction(regularTx, journalEnabled)
	defer clearJournalScope()

	tx := &doltTransaction{regularTx: regularTx, store: s}

	defer func() {
		if r := recover(); r != nil {
			_ = regularTx.Rollback()
			panic(r)
		}
	}()

	if err := fn(tx); err != nil {
		_ = regularTx.Rollback()
		return err
	}

	return s.finishDoltTransaction(ctx, conn, tx, commitMsg)
}

// finishDoltTransaction commits the callback's single SQL transaction, then
// stages its durable tables into a Dolt revision. Dolt-ignored rows commit in
// the SQL phase but are excluded from DOLT_ADD/DOLT_COMMIT. Once SQL commit
// succeeds, a later staging failure has an indeterminate durable outcome.
func (s *DoltStore) finishDoltTransaction(ctx context.Context, conn *sql.Conn, tx *doltTransaction, commitMsg string) error {
	if err := tx.regularTx.Commit(); err != nil {
		return wrapSQLCommitError("sql commit", err)
	}

	if err := versioncontrolops.StageAndCommit(ctx, conn, tx.dirty.DirtyTables(), commitMsg, s.commitAuthorString()); err != nil {
		return fmt.Errorf("stage and commit after SQL commit: %w: %w", err, ErrCommitIndeterminate)
	}
	return nil
}

// isDoltNothingToCommit returns true if the error indicates there were no
// staged changes for Dolt to commit — a benign condition.
func isDoltNothingToCommit(err error) bool {
	return issueops.IsNothingToCommitError(err)
}

// CreateIssue creates an issue within the transaction.
// Routes ephemeral issues to the wisps table.
func (t *doltTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if issue == nil {
		return fmt.Errorf("issue must not be nil")
	}

	// Build the validation context on the callback transaction so config and
	// custom types registered earlier in the callback are visible to both issue
	// planes.
	bc, err := issueops.NewBatchContext(ctx, t.regularTx, storage.BatchCreateOptions{SkipPrefixValidation: true})
	if err != nil {
		return err
	}

	result, err := issueops.CreateIssueInTxWithResult(ctx, t.regularTx, bc, issue, actor)
	if err != nil {
		return err
	}
	for table := range issueops.CreateIssueDirtyTables(ctx, issue, result) {
		t.dirty.MarkDirty(table)
	}
	return nil
}

// CreateIssues creates multiple issues within the transaction
func (t *doltTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	if len(issues) == 0 {
		return nil
	}

	// This must run before splitting regular issues from wisps: the shared
	// create helper below only sees the regular subset.
	if err := issueops.ValidateCreateIssuesMixedBucketDependencies(issues); err != nil {
		return err
	}

	var regularIssues []*types.Issue
	var wispIssues []*types.Issue
	for _, issue := range issues {
		if issueops.IsWisp(issue) {
			wispIssues = append(wispIssues, issue)
		} else {
			regularIssues = append(regularIssues, issue)
		}
	}

	// See CreateIssue: one validation context on regularTx serves both
	// tiers, so in-transaction custom-type registration is visible to the
	// wisp tier too (GH#5443).
	bc, err := issueops.NewBatchContext(ctx, t.regularTx, storage.BatchCreateOptions{
		SkipPrefixValidation: true,
	})
	if err != nil {
		return err
	}

	if len(regularIssues) > 0 {
		result, err := issueops.CreateIssuesInTxWithContext(ctx, t.regularTx, bc, regularIssues, actor)
		if err != nil {
			return err
		}
		for table := range issueops.CreateIssuesDirtyTables(ctx, regularIssues, result) {
			t.dirty.MarkDirty(table)
		}
	}

	if len(wispIssues) > 0 {
		result, err := issueops.CreateIssuesInTxWithContext(ctx, t.regularTx, bc, wispIssues, actor)
		if err != nil {
			return err
		}
		for table := range issueops.CreateIssuesDirtyTables(ctx, wispIssues, result) {
			t.dirty.MarkDirty(table)
		}
	}
	return nil
}

// GetIssue retrieves an issue within the transaction.
// Checks wisps table for active wisps (including explicit-ID ephemerals).
func (t *doltTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	table := "issues"
	if t.isActiveWisp(ctx, id) {
		table = "wisps"
	}
	return scanIssueTxFromTable(ctx, t.regularTx, table, id)
}

// SearchIssueIDs returns matching IDs only, projected in Go from SearchIssues.
// It skips the issueops.SearchIssueIDsInTx fast path because this transaction
// preserves its established single-plane SearchIssues result shape. Partial-ID
// resolution calls the (fast) store path, never a transaction, so this is cold.
func (t *doltTransaction) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	// The caller wants ids only, so opt out of the bulk label read SearchIssues
	// would otherwise run and then project away. filter is a value copy, so this
	// does not touch the caller's filter. SkipLabels gates only hydration: the
	// label-driven WHERE predicates (LabelPattern, ExcludeLabels, LabelRegex,
	// Labels/LabelsAny) are built from their own filter fields in
	// BuildIssueFilterClauses and still select the same rows. Dependency
	// hydration is already gated on IncludeDependencies, so it costs nothing here.
	filter.SkipLabels = true
	issues, err := t.SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	return ids, nil
}

// SearchIssues searches for issues within the transaction.
//
// The WHERE clause, the ORDER BY, the row bound and the label/dependency
// hydration all come from the SHARED implementation the store-level searches
// use (sqlbuild.BuildIssueFilterClauses, sqlbuild.OrderBy,
// issueops.EffectiveSearchLimit/EnforceMaxRowsCap,
// issueops.GetLabelsForIssuesFromTableInTx). This used to be a second,
// hand-rolled filter builder, and that was the whole defect: a field the second
// builder did not implement was not refused, it was IGNORED, and the caller got
// plausible wrong rows with no error (ga-v1nuj — Statuses, ExcludeLabels,
// LabelPattern, LabelRegex, IsBlocked, StartedAfter/StartedBefore,
// SortBy/SortDesc, MaxRows and the AfterID/AfterCreatedAt keyset cursor were all
// accepted and dropped; labels were never hydrated at all). A new filter field
// now reaches this path for free, which is the point of not having a second
// builder.
//
// What still differs from the store-level search, deliberately and visibly:
//
//   - NO WISP MERGE. This runs ONE table — issues, or wisps when the filter
//     routes there. So a default (SkipWisps=false) search here answers what
//     a SkipWisps=true search answers at the store level. That is a structural
//     difference in the transaction, not a dropped filter field, and merging the
//     tiers is its own change with its own blast radius.
//   - filter.Lite and filter.NoIDShrink are not read. Both describe HOW to
//     fetch, not WHICH rows: ignoring Lite returns fully populated rows with
//     IsLitePartial left false, which is exactly what that flag promises a
//     caller may receive, and this path is always id-shrunk anyway. Neither can
//     produce a wrong answer, so neither is worth a refusal.
//   - filter.Offset is not read — nor is it read by issueops, so the store-level
//     search ignores it too. Refusing it HERE would invent a divergence rather
//     than remove one.
func (t *doltTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	tables := issueops.IssuesFilterTables
	if filter.Ephemeral != nil && *filter.Ephemeral {
		tables = issueops.WispsFilterTables
	}
	// If searching by IDs that are all ephemeral, use wisps table (bd-w2w)
	if len(filter.IDs) > 0 && allEphemeral(filter.IDs) {
		tables = issueops.WispsFilterTables
	}

	whereClauses, args, err := issueops.BuildIssueFilterClauses(query, filter, tables)
	if err != nil {
		return nil, err
	}
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	// A page bound is only pushed under an order the query can express (the rule
	// issueops.searchTableInTxT states): a Go-side sort key renders no ORDER BY,
	// and a LIMIT with no ORDER BY does not return the first n rows, it returns n
	// rows. Under such a key the query scans the whole matching set and the bound
	// is applied below, after the order exists.
	goSideSort := sqlbuild.IsGoSideSort(filter.SortBy)
	eff := issueops.EffectiveSearchLimit(filter.Limit, filter.MaxRows)
	limitSQL := ""
	if eff > 0 && !goSideSort {
		limitSQL = fmt.Sprintf(" LIMIT %d", eff)
	}

	//nolint:gosec // G201: table name is a fixed constant, whereSQL is parameterized
	rows, err := t.regularTx.QueryContext(ctx, fmt.Sprintf(
		`SELECT id FROM %s %s %s %s`,
		tables.Main, whereSQL, sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, ""), limitSQL), args...)
	if err != nil {
		return nil, wrapQueryError("search issues in tx", err)
	}

	var ids []string
	seen := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, wrapScanError("search issues in tx", err)
		}
		// Structural parity with issueops.searchTableInTxT, which dedups because
		// it can drive from a joined label table where a row repeats (GH#3567).
		// This tx query is JOIN-free — only id IN (<correlated subquery>)
		// predicates from sqlbuild.BuildIssueFilterClauses — so a row cannot
		// actually repeat on this path; the dedup mirrors the reference rather
		// than guarding a live duplicate source here.
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapQueryError("search issues in tx: rows iteration", err)
	}
	_ = rows.Close()

	if goSideSort {
		sort.SliceStable(ids, func(i, j int) bool { return sqlbuild.LessID(ids[i], ids[j], filter.SortDesc) })
		if eff > 0 && len(ids) > eff {
			ids = ids[:eff]
		}
	}

	var issues []*types.Issue
	for _, id := range ids {
		// Hydrate from the SAME table the filter selected. GetIssue is wisp-first
		// for a dual-resident ID; calling it here could replace a matching durable
		// row with a nonmatching wisp twin and then attach durable labels/deps.
		issue, err := scanIssueTxFromTable(ctx, t.regularTx, tables.Main, id)
		if err != nil {
			return nil, fmt.Errorf("search issues in tx: get issue %s: %w", id, err)
		}
		issues = append(issues, issue)
	}
	if err := t.hydrateSearchLabels(ctx, tables, filter, issues); err != nil {
		return nil, err
	}
	if err := t.hydrateSearchDependencies(ctx, tables.Dependencies, filter, issues); err != nil {
		return nil, err
	}

	// Trim to the caller's page before the cap check, exactly as
	// issueops.searchInTx does: the cap is a statement about the rows actually
	// handed back, and eff can exceed filter.Limit when MaxRows sized the bound.
	if filter.Limit > 0 && len(issues) > filter.Limit {
		issues = issues[:filter.Limit]
	}
	if err := issueops.EnforceMaxRowsCap(len(issues), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}
	return issues, nil
}

// hydrateSearchLabels populates Issue.Labels from the tier the search ran
// against, using the same bulk read issueops.searchInTx uses. SkipLabels is the
// caller's opt-out; without this the transaction answered every search with
// unlabeled issues while the store-level search labeled them (ga-v1nuj).
func (t *doltTransaction) hydrateSearchLabels(ctx context.Context, tables issueops.FilterTables, filter types.IssueFilter, issues []*types.Issue) error {
	if filter.SkipLabels || len(issues) == 0 {
		return nil
	}
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	labelsByID, err := issueops.GetLabelsForIssuesFromTableInTx(ctx, t.regularTx, tables.Labels, ids)
	if err != nil {
		return fmt.Errorf("search issues in tx: hydrate labels: %w", err)
	}
	for _, issue := range issues {
		if labels, ok := labelsByID[issue.ID]; ok {
			issue.Labels = labels
		}
	}
	return nil
}

// hydrateSearchDependencies populates Issue.Dependencies when the filter asked
// for it, using the same bulk read issueops.SearchIssuesInTx uses so the two
// backends answer IncludeDependencies from one implementation. Issues with no
// edges keep a nil slice; the map simply has no entry for them.
func (t *doltTransaction) hydrateSearchDependencies(ctx context.Context, depTable string, filter types.IssueFilter, issues []*types.Issue) error {
	if !filter.IncludeDependencies || len(issues) == 0 {
		return nil
	}
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	depsByID, err := issueops.GetDependencyRecordsForIssuesFromTableInTx(ctx, t.regularTx, depTable, ids)
	if err != nil {
		return fmt.Errorf("search issues in tx: hydrate dependencies: %w", err)
	}
	for _, issue := range issues {
		if deps, ok := depsByID[issue.ID]; ok {
			issue.Dependencies = deps
		}
	}
	return nil
}

// UpdateIssue applies field updates and records the "updated" history event,
// which is what the store-level DoltStore.UpdateIssue records for the same
// change and what embeddedTransaction.UpdateIssue records here. Wrapping an
// update in a transaction must not change its audit trail: a consumer cannot
// see which backend or which call shape it got, so a transaction-only silence
// shows up as a user's own edits missing from the history of their own issue.
// The eventless variant exists for demotion (ephemeral_routing.go), which
// copies the historical event stream and appends one demotion event of its
// own; a generic update is not that case.
func (t *doltTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	table := "issues"
	if t.isActiveWisp(ctx, id) {
		table = "wisps"
	}

	if rawMeta, ok := updates["metadata"]; ok {
		metadataStr, err := storage.NormalizeMetadataValue(rawMeta)
		if err != nil {
			return fmt.Errorf("invalid metadata: %w", err)
		}
		if err := validateMetadataIfConfigured(json.RawMessage(metadataStr)); err != nil {
			return err
		}
	}

	result, err := issueops.UpdateIssueForPlaneInTx(ctx, t.regularTx, id, updates, actor, table == "wisps")
	if err != nil {
		return wrapExecError("update issue in tx", err)
	}
	if !result.Changed {
		return nil
	}
	t.dirty.MarkDirty(table)
	_, _, eventTable, _ := issueops.WispTableRouting(table == "wisps")
	t.dirty.MarkDirty(eventTable)
	if result.IssueRowsChanged {
		t.dirty.MarkDirty("issues")
	}
	return nil
}

func (t *doltTransaction) TouchIssue(ctx context.Context, id, actor string) error {
	selectedWisp, err := t.planeForIssueID(ctx, id)
	if err != nil {
		return err
	}
	result, err := issueops.TouchIssueForPlaneInTx(ctx, t.regularTx, id, actor, selectedWisp)
	if err != nil {
		return wrapExecError("touch issue in tx", err)
	}
	issueTable, _, eventTable, _ := issueops.WispTableRouting(result.IsWisp)
	t.dirty.MarkDirty(issueTable)
	t.dirty.MarkDirty(eventTable)
	return nil
}

func (t *doltTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	table := "issues"
	eventTable := "events"
	if t.isActiveWisp(ctx, id) {
		table = "wisps"
		eventTable = "wisp_events"
	}

	result, err := issueops.CloseIssueInTx(ctx, t.regularTx, id, reason, actor, session)
	if err != nil {
		return wrapExecError("close issue in tx", err)
	}
	if result.AlreadyClosed {
		return nil
	}
	t.dirty.MarkDirty(table)
	t.dirty.MarkDirty(eventTable)
	if result.IssueRowsChanged {
		t.dirty.MarkDirty("issues")
	}
	return nil
}

func (t *doltTransaction) CloseIssueChecked(ctx context.Context, id, actor string, opts storage.CloseIssueOptions) (storage.CloseIssueResult, error) {
	selectedWisp, err := t.planeForIssueID(ctx, id)
	if err != nil {
		return storage.CloseIssueResult{}, err
	}
	result, err := issueops.CloseIssueCheckedForPlaneInTx(ctx, t.regularTx, id, opts.Reason, actor, opts.Session, opts.Force, opts.ExpectedVersion, selectedWisp)
	if err != nil {
		return storage.CloseIssueResult{}, wrapExecError("checked close issue in tx", err)
	}
	publicResult := storage.CloseIssueResult{Unchanged: result.AlreadyClosed, OpenChildren: result.OpenChildren}
	if result.AlreadyClosed {
		return publicResult, nil
	}
	issueTable, _, eventTable, _ := issueops.WispTableRouting(result.IsWisp)
	t.dirty.MarkDirty(issueTable)
	t.dirty.MarkDirty(eventTable)
	if result.IssueRowsChanged {
		t.dirty.MarkDirty("issues")
	}
	return publicResult, nil
}

// ReopenIssueWithResult reopens an issue within this transaction and reports
// whether the lifecycle state changed.
func (t *doltTransaction) ReopenIssueWithResult(ctx context.Context, id string, reason string, actor string) (bool, error) {
	table, eventTable := "issues", "events"
	if t.isActiveWisp(ctx, id) {
		table, eventTable = "wisps", "wisp_events"
	}
	result, err := issueops.ReopenIssueInTx(ctx, t.regularTx, id, reason, actor)
	if err != nil {
		return false, wrapExecError("reopen issue in tx", err)
	}
	if result.Changed {
		t.dirty.MarkDirty(table)
		t.dirty.MarkDirty(eventTable)
		if result.IssueRowsChanged {
			t.dirty.MarkDirty("issues")
		}
	}
	return result.Changed, nil
}

func (t *doltTransaction) DeleteIssue(ctx context.Context, id string) error {
	isWisp := t.isActiveWisp(ctx, id)
	result, err := issueops.DeleteIssueInTxWithResult(ctx, t.regularTx, id)
	if err != nil {
		return wrapExecError("delete issue in tx", err)
	}
	// Mark every table the ON DELETE CASCADE fans out to, not just the row's
	// own table: the cascaded deletions are invisible to the SQL we issue, so
	// staging only `issues` leaves them uncommitted in the working set.
	for _, cascaded := range issueops.DeleteCascadeTables(isWisp) {
		t.dirty.MarkDirty(cascaded)
	}
	if result.IssueRowsChanged {
		t.dirty.MarkDirty("issues")
	}
	return nil
}

// AddDependency adds a dependency within the transaction.
// Checks for existing pairs to prevent silent type overwrites.
func (t *doltTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return t.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{})
}

func (t *doltTransaction) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, addOpts storage.DependencyAddOptions) error {
	table := "dependencies"
	sourceTable := "issues"
	eventTable := "events"
	if t.isActiveWisp(ctx, dep.IssueID) {
		table = "wisp_dependencies"
		sourceTable = "wisps"
		eventTable = "wisp_events"
	}

	isCrossPrefix := isCrossPrefixDep(dep.IssueID, dep.DependsOnID)
	targetTable := "issues"
	kind := issueops.DepTargetIssue
	switch {
	case isCrossPrefix, strings.HasPrefix(dep.DependsOnID, "external:"):
		kind = issueops.DepTargetExternal
	default:
		if t.isActiveWisp(ctx, dep.DependsOnID) {
			targetTable = "wisps"
			kind = issueops.DepTargetWisp
		}
	}

	opts := issueops.AddDependencyOpts{
		SourceTable:    sourceTable,
		TargetTable:    targetTable,
		WriteTable:     table,
		IsCrossPrefix:  isCrossPrefix,
		SkipCycleCheck: addOpts.SkipCycleCheck,
		TargetKind:     &kind,
		EmitEvent:      addOpts.EmitEvent,
	}

	result, err := issueops.AddDependencyInTxWithResult(ctx, t.regularTx, dep, actor, opts)
	if err != nil {
		return err
	}
	t.dirty.MarkDirty(table)
	// AddDependencyInTx records a dependency_added audit row only for a genuine
	// emit (explicit verb + new edge). MarkDirty intentionally filters the
	// clone-local event tables, while still documenting the mutation at this
	// facade boundary.
	if result.EventWritten {
		t.dirty.MarkDirty(eventTable)
	}
	if result.IssueRowsChanged {
		t.dirty.MarkDirty("issues")
	}
	return nil
}

// CycleThroughEdges reports a scheduling cycle through one of the new edges.
// Both dependency planes share this SQL transaction, so uncommitted writes on
// either side are visible to the graph gate.
func (t *doltTransaction) CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error) {
	graph := make(map[string][]string)
	if err := issueops.AppendSchedulingGraphInTx(ctx, t.regularTx, []string{"dependencies", "wisp_dependencies"}, graph); err != nil {
		return "", err
	}
	return issueops.CycleThroughEdgesInGraph(graph, edges), nil
}

func (t *doltTransaction) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	table := "dependencies"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_dependencies"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.regularTx.QueryContext(ctx, fmt.Sprintf(`
		SELECT issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM %s
		WHERE issue_id = ?
	`, issueops.DepTargetExpr, table), issueID)
	if err != nil {
		return nil, wrapQueryError("get dependency records in tx", err)
	}
	defer rows.Close()

	var deps []*types.Dependency
	for rows.Next() {
		var d types.Dependency
		var metadata sql.NullString
		var threadID sql.NullString
		if err := rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type, &d.CreatedAt, &d.CreatedBy, &metadata, &threadID); err != nil {
			return nil, wrapScanError("get dependency records in tx", err)
		}
		if metadata.Valid {
			d.Metadata = metadata.String
		}
		if threadID.Valid {
			d.ThreadID = threadID.String
		}
		deps = append(deps, &d)
	}
	return deps, rows.Err()
}

func (t *doltTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return t.RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, storage.DependencyRemoveOptions{})
}

func (t *doltTransaction) RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, rmOpts storage.DependencyRemoveOptions) error {
	table := "dependencies"
	eventTable := "events"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_dependencies"
		eventTable = "wisp_events"
	}
	result, err := issueops.RemoveDependencyInTxWithResult(ctx, t.regularTx, issueID, dependsOnID, actor, rmOpts.EmitEvent)
	if err != nil {
		return wrapExecError("remove dependency in tx", err)
	}
	t.dirty.MarkDirty(table)
	// RemoveDependencyInTx records a dependency_removed audit row only for a
	// genuine emit (explicit verb + edge removal). MarkDirty filters the
	// clone-local event tables from Dolt staging.
	if result.EventWritten {
		t.dirty.MarkDirty(eventTable)
	}
	if result.IssueRowsChanged {
		t.dirty.MarkDirty("issues")
	}
	return nil
}

// AddLabel adds a label within the transaction
func (t *doltTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	table := "labels"
	eventTable := "events"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_labels"
		eventTable = "wisp_events"
	}

	if err := issueops.AddLabelInTx(ctx, t.regularTx, table, eventTable, issueID, label, actor); err != nil {
		return wrapExecError("add label in tx", err)
	}
	t.dirty.MarkDirty(table)
	t.dirty.MarkDirty(eventTable)
	return nil
}

func (t *doltTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	table := "labels"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_labels"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.regularTx.QueryContext(ctx, fmt.Sprintf(`SELECT label FROM %s WHERE issue_id = ? ORDER BY label`, table), issueID)
	if err != nil {
		return nil, wrapQueryError("get labels in tx", err)
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, wrapScanError("get labels in tx", err)
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}

// RemoveLabel removes a label within the transaction
func (t *doltTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	table := "labels"
	eventTable := "events"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_labels"
		eventTable = "wisp_events"
	}

	if err := issueops.RemoveLabelInTx(ctx, t.regularTx, table, eventTable, issueID, label, actor); err != nil {
		return wrapExecError("remove label in tx", err)
	}
	t.dirty.MarkDirty(table)
	t.dirty.MarkDirty(eventTable)
	return nil
}

// SetConfig sets a config value within the transaction
func (t *doltTransaction) SetConfig(ctx context.Context, key, value string) error {
	_, err := t.regularTx.ExecContext(ctx, `
		INSERT INTO config (`+"`key`"+`, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, key, value)
	if err != nil {
		return wrapExecError("set config in tx", err)
	}
	t.dirty.MarkDirty("config")

	// ResolveCustomTypesInTx reads the normalized tables first, so without
	// this sync a type registered in-transaction stays invisible to
	// validation whenever the table already has rows.
	table, err := issueops.SyncConfigTables(ctx, t.regularTx, key, value)
	if err != nil {
		return err
	}
	if table != "" {
		t.dirty.MarkDirty(table)
	}

	// Keep store-level caches (GetCustomTypes and friends) coherent with
	// in-transaction config writes; see invalidateConfigCaches.
	if t.store != nil {
		t.store.invalidateConfigCaches(key)
	}
	return nil
}

// GetConfig gets a config value within the transaction
func (t *doltTransaction) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := t.regularTx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, wrapQueryError("get config in tx", err)
}

// SetMetadata sets a metadata value within the transaction
func (t *doltTransaction) SetMetadata(ctx context.Context, key, value string) error {
	_, err := t.regularTx.ExecContext(ctx, `
		INSERT INTO metadata (`+"`key`"+`, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, key, value)
	if err == nil {
		t.dirty.MarkDirty("metadata")
	}
	return wrapExecError("set metadata in tx", err)
}

// GetMetadata gets a metadata value within the transaction
func (t *doltTransaction) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := t.regularTx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, wrapQueryError("get metadata in tx", err)
}

// SetLocalMetadata sets a value in the dolt-ignored local_metadata table within the transaction.
func (t *doltTransaction) SetLocalMetadata(ctx context.Context, key, value string) error {
	_, err := t.regularTx.ExecContext(ctx, "REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", key, value)
	return wrapExecError("set local metadata in tx", err)
}

// GetLocalMetadata gets a value from the dolt-ignored local_metadata table within the transaction.
func (t *doltTransaction) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := t.regularTx.QueryRowContext(ctx, "SELECT value FROM local_metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, wrapQueryError("get local metadata in tx", err)
}

func (t *doltTransaction) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	_, err := t.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}

	table := "comments"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_comments"
	}

	createdAtText := issueops.FormatAuxTime(createdAt)
	id, _, err := issueops.InsertDerivedComment(ctx, t.regularTx, table, issueID, author, text, createdAtText)
	if err != nil {
		return nil, fmt.Errorf("failed to add comment: %w", err)
	}
	t.dirty.MarkDirty(table)

	stored, err := issueops.ParseAuxTime(createdAtText)
	if err != nil {
		return nil, fmt.Errorf("failed to add comment: %w", err)
	}
	// This path writes the comment row directly rather than through
	// issueops.ImportIssueCommentInTx, so it must journal the comment op itself
	// — the create/comment entry points cover their own writes, not this one.
	if err := issueops.RecordCommentEventForPlaneInTx(ctx, t.regularTx, issueID, &issueops.EventComment{
		ID: id, Author: author, Text: text, CreatedAt: stored, Source: issueops.CommentSourceStructured,
	}, table == "wisp_comments"); err != nil {
		return nil, wrapExecError("journal import comment in tx", err)
	}
	return &types.Comment{ID: id, IssueID: issueID, Author: author, Text: text, CreatedAt: stored}, nil
}

func (t *doltTransaction) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	table := "comments"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_comments"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.regularTx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM %s
		WHERE issue_id = ?
		ORDER BY created_at ASC, id ASC
	`, table), issueID)
	if err != nil {
		return nil, wrapQueryError("get comments in tx", err)
	}
	defer rows.Close()
	var comments []*types.Comment
	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return nil, wrapScanError("get comments in tx", err)
		}
		comments = append(comments, &c)
	}
	return comments, rows.Err()
}

// AddComment adds a comment within the transaction
func (t *doltTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	table := "events"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_events"
	}

	createdAt := issueops.NowAuxTime()
	id, err := issueops.InsertDerivedEventReturningID(ctx, t.regularTx, table, issueops.AuxEvent{
		IssueID:   issueID,
		EventType: types.EventCommented,
		Actor:     actor,
		Comment:   sql.NullString{String: comment, Valid: true},
		CreatedAt: createdAt,
	})
	if err != nil {
		return wrapExecError("add comment in tx", err)
	}
	t.dirty.MarkDirty(table)
	stored, err := issueops.ParseAuxTime(createdAt)
	if err != nil {
		return wrapExecError("add comment in tx", err)
	}
	// This path writes the audit comment row directly rather than through
	// issueops.AddCommentEventInTx, so it must journal the comment op itself.
	// The text is replayable content, so it carries the same payload as a
	// structured comment, distinguished by Source.
	if err := issueops.RecordCommentEventForPlaneInTx(ctx, t.regularTx, issueID, &issueops.EventComment{
		ID: id, Author: actor, Text: comment, CreatedAt: stored, Source: issueops.CommentSourceAudit,
	}, table == "wisp_events"); err != nil {
		return wrapExecError("journal comment in tx", err)
	}
	return nil
}

// GetIssueCommentsPage returns one keyset page of an issue's comments within the
// transaction. Both issue planes share the callback's SQL transaction, so a
// comment written on either tier earlier in the callback is visible.
func (t *doltTransaction) GetIssueCommentsPage(ctx context.Context, issueID string, after storage.CommentPageCursor, limit int) ([]*types.Comment, error) {
	return issueops.GetIssueCommentsPageInTx(ctx, t.regularTx, issueID, after, limit)
}

// CountIssuesByGroup returns per-group issue counts within the transaction.
// It sees durable and wisp writes made earlier in this callback. The established
// count-vs-search asymmetry remains: the count merges both planes while
// SearchIssues selects one plane.
func (t *doltTransaction) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	return issueops.CountIssuesByGroupInTx(ctx, t.regularTx, filter, groupBy)
}

// GetDependentRecords returns the raw inbound dependency rows of targetID within
// the transaction.
//
// A target's inbound edges span both dependency tables; the shared transaction
// lets the cross-table de-dup observe same-callback writes from both planes.
func (t *doltTransaction) GetDependentRecords(ctx context.Context, targetID string, depType string, limit int, afterID string) ([]*types.Dependency, error) {
	return issueops.GetDependentRecordsInTx(ctx, t.regularTx, targetID, depType, limit, afterID)
}

// GetDependentRecordsForIssues returns the raw inbound dependency rows for a set
// of target ids within the transaction, keyed by target id.
func (t *doltTransaction) GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error) {
	return issueops.GetDependentRecordsForIssuesInTx(ctx, t.regularTx, targetIDs)
}

// CountDependentRecords returns the total inbound-edge count of targetID within
// the transaction.
func (t *doltTransaction) CountDependentRecords(ctx context.Context, targetID string, depType string) (int, error) {
	return issueops.CountDependentRecordsInTx(ctx, t.regularTx, targetID, depType)
}

// IsBlocked reports the denormalized transitive is_blocked flag and direct
// blockers of issueID within the transaction. Plane selection is wisp-first,
// matching GetIssue and the lifecycle methods for dual-resident IDs.
func (t *doltTransaction) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	return issueops.IsBlockedForPlaneInTx(ctx, t.regularTx, issueID, t.isActiveWisp(ctx, issueID))
}

// IsBlockedBatch reports the denormalized transitive is_blocked flag for a page
// of ids within the transaction. It partitions IDs wisp-first, then pins each
// batch read to the selected plane so a dual-resident ID cannot fall back to its
// durable twin.
func (t *doltTransaction) IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error) {
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	wispIDs, permIDs, err := issueops.PartitionWispIDsInTx(ctx, t.regularTx, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(ids))
	if len(permIDs) > 0 {
		durable, err := issueops.IsBlockedBatchForPlaneInTx(ctx, t.regularTx, permIDs, false)
		if err != nil {
			return nil, err
		}
		for id, blocked := range durable {
			result[id] = blocked
		}
	}
	if len(wispIDs) > 0 {
		wisp, err := issueops.IsBlockedBatchForPlaneInTx(ctx, t.regularTx, wispIDs, true)
		if err != nil {
			return nil, err
		}
		for id, blocked := range wisp {
			result[id] = blocked
		}
	}
	return result, nil
}

// EventsSince returns durable events strictly after the keyset cursor within the
// transaction. Mirrors DoltStore.EventsSince's issueops delegation. The feed is
// durable-only by contract (wisp events are excluded), and durable event writes
// land on regularTx, so an event recorded earlier in THIS uncommitted
// transaction is visible.
func (t *doltTransaction) EventsSince(ctx context.Context, cursor storage.EventCursor, issueID string, limit int) ([]*types.Event, error) {
	return issueops.EventsSinceInTx(ctx, t.regularTx, cursor.CreatedAt, cursor.ID, issueID, limit)
}
