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

// doltTransaction implements storage.Transaction for Dolt. Durable tables and
// the dolt-ignored wisp plane (wisps, wisp_*, local_metadata, repo_mtimes) all
// run on tx, the one SQL transaction on the operation's pinned connection, so
// every read sees every write made earlier in the transaction and the planes
// commit or roll back together. dirty collects the versioned tables the writes
// changed for the operation's Dolt commit; it drops dolt-ignored tables, which
// never enter a Dolt commit.
type doltTransaction struct {
	tx    *sql.Tx
	store *DoltStore
	dirty versioncontrolops.DirtyTableTracker
}

// isActiveWisp checks if an ID exists in the wisps table within the transaction.
// Unlike the store-level isActiveWisp, this queries within the transaction so it
// sees uncommitted wisps. Handles both -wisp- pattern and explicit-ID ephemerals (GH#2053).
func (t *doltTransaction) isActiveWisp(ctx context.Context, id string) bool {
	var exists int
	err := t.tx.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
	return err == nil
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
			tx := &doltTransaction{tx: sqlTx, store: s}
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
	pending, err := s.runDoltTransactionRecording(ctx, commitMsg, fn)
	// Settled here, after the pinned connection has gone back to the pool: the
	// recheck needs one of its own.
	// pending is non-empty only when the SQL commit landed or may have
	// landed, so this also runs when the Dolt commit after it failed
	// (ErrCommitIndeterminate): those rows are committed all the same.
	s.settleBlockedRecheck(ctx, pending)
	return err
}

// runDoltTransactionRecording is runDoltTransaction's transaction. Once it has
// committed it hands back the dependents the callback's unblocking writes
// recorded, for the post-commit blocked-state recheck (gastownhall/beads#6716).
func (s *DoltStore) runDoltTransactionRecording(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) (issueops.BlockedRecheck, error) {
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
		return issueops.BlockedRecheck{}, fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	sqlTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return issueops.BlockedRecheck{}, fmt.Errorf("failed to begin tx: %w", err)
	}

	// Both planes run in this one pinned SQL transaction, whether or not the
	// events journal is on. The dolt-ignored tables (wisps, wisp_*,
	// local_metadata, repo_mtimes, and the journal's bd_events_journal and
	// bd_events_seq) never enter a Dolt commit anyway: DirtyTableTracker drops
	// them and StageAndCommit stages only the tables it is given. Sharing the
	// session is what makes a mixed durable+wisp callback read its own wisp
	// writes, commit or roll back as a whole (a commit-time conflict on a wisp
	// row refuses the durable half too, determinately), and stage the durable
	// rows a wisp write changes (a dependent's is_blocked) into its own Dolt
	// commit. The journal switch decides only whether journal rows are written.
	journalEnabled := s.eventsJournalEnabled.Load()
	clearJournalScope := issueops.ScopeEventsJournalTransaction(sqlTx, journalEnabled)
	defer clearJournalScope()
	// Shape counterpart of the activation switch just above (PR A1):
	// insertEventRow/readEventsRowsInTx reach this store's probed shape only
	// through a tx-scoped value, never through the store itself, so without
	// this call every write on this path — CreateIssue, UpdateIssue, comments,
	// the whole doltTransaction mutator surface reached from
	// runDoltTransactionRecording — would silently fall back to
	// canonicalJournalShape and defeat adaptive I/O for this store's main
	// write path. The switch does not depend on the shape: a nil shape (no
	// probe result) writes canonically, as scopeEventsJournalTransaction
	// explains.
	clearJournalShape := issueops.ScopeEventsJournalShape(sqlTx, s.journalShape.Load())
	defer clearJournalShape()
	// Versioned history binds to the SAME transaction the mutation runs
	// in, for the same reason the journal does: RecordVersionInTx no-ops unless
	// this scope is set. Without it every issues-plane mutation routed through
	// runDoltTransaction — the whole doltTransaction mutator surface, reached
	// from bd create, batch create/import, bd graph apply, merge_slot and the
	// CLI transact wrappers — commits durably while minting no version row, so
	// the bead has no creation version and the first later mutation that does
	// run scoped is minted as revision 1: the history then asserts that the
	// update IS the creation. A switch read that fails here fails the operation
	// before fn runs, for the same reason (VersionedHistorySwitch.ResolveIn).
	versioned, err := s.versionedHistory.ResolveIn(ctx, sqlTx)
	if err != nil {
		_ = sqlTx.Rollback()
		return issueops.BlockedRecheck{}, err
	}
	clearVersionScope := issueops.ScopeVersionedHistoryTransaction(sqlTx, versioned)
	defer clearVersionScope()
	// Unblocking writes on either plane record their dependents on the
	// transaction.
	clearRecheckScope := issueops.ScopeBlockedRecheckTransaction(sqlTx)
	defer clearRecheckScope()

	tx := &doltTransaction{tx: sqlTx, store: s}

	defer func() {
		if r := recover(); r != nil {
			_ = sqlTx.Rollback()
			panic(r)
		}
	}()

	if err := fn(tx); err != nil {
		_ = sqlTx.Rollback()
		return issueops.BlockedRecheck{}, err
	}

	return s.finishDoltTransactionRecording(ctx, conn, tx, commitMsg)
}

// finishDoltTransactionRecording is finishDoltTransaction that also hands back
// the dependents the transaction's unblocking writes recorded, for the
// post-commit blocked-state recheck.
//
// They are handed back with the error when the commit is indeterminate
// (ErrCommitIndeterminate): the SQL commit may have landed, or did land and
// only the Dolt commit after it failed. The rows are then committed without a
// caller being able to replay the write, so the recheck is the only thing that
// can settle their dependents; being a full recompute of committed state, it is
// harmless if the commit did not land. Any other failure means the SQL commit
// was refused and rolled back, and nothing is handed back.
func (s *DoltStore) finishDoltTransactionRecording(ctx context.Context, conn *sql.Conn, tx *doltTransaction, commitMsg string) (issueops.BlockedRecheck, error) {
	err := s.finishDoltTransaction(ctx, conn, tx, commitMsg)
	if err != nil && !errors.Is(err, ErrCommitIndeterminate) {
		return issueops.BlockedRecheck{}, err
	}
	return issueops.TakeBlockedRecheck(tx.tx), err
}

// finishDoltTransaction commits the SQL transaction, which carries both
// planes, and then its Dolt revision. Once the SQL commit succeeds, a later
// failure has an indeterminate durable outcome.
func (s *DoltStore) finishDoltTransaction(ctx context.Context, conn *sql.Conn, tx *doltTransaction, commitMsg string) error {
	if err := tx.tx.Commit(); err != nil {
		return wrapSQLCommitError("sql commit", err)
	}

	// The mutations in this transaction ran with versioned history scoped on
	// tx (see runDoltTransactionRecording), so anything they minted has to be
	// staged with them or the version rows never reach this operation's Dolt
	// commit. Marked here, once, rather than at each MarkDirty call site.
	tx.dirty.MarkVersionedHistoryDirty(s.versionedHistory.Enabled())

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

	// One validation context serves both tiers: it lives in regular
	// dolt-tracked tables (config, custom_types), and reading it on the
	// transaction keeps types registered earlier in it
	// (tx.SetConfig("types.custom", ...)) visible to a wisp create (GH#5443).
	bc, err := issueops.NewBatchContext(ctx, t.tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
	if err != nil {
		return err
	}

	if issueops.IsWisp(issue) {
		_, err = issueops.CreateIssueInTxWithResult(ctx, t.tx, bc, issue, actor)
		return err
	}

	result, err := issueops.CreateIssueInTxWithResult(ctx, t.tx, bc, issue, actor)
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

	// See CreateIssue: one validation context serves both
	// tiers, so in-transaction custom-type registration is visible to the
	// wisp tier too (GH#5443).
	bc, err := issueops.NewBatchContext(ctx, t.tx, storage.BatchCreateOptions{
		SkipPrefixValidation: true,
	})
	if err != nil {
		return err
	}

	if len(regularIssues) > 0 {
		result, err := issueops.CreateIssuesInTxWithContext(ctx, t.tx, bc, regularIssues, actor)
		if err != nil {
			return err
		}
		for table := range issueops.CreateIssuesDirtyTables(ctx, regularIssues, result) {
			t.dirty.MarkDirty(table)
		}
	}

	if len(wispIssues) > 0 {
		result, err := issueops.CreateIssuesInTxWithContext(ctx, t.tx, bc, wispIssues, actor)
		if err != nil {
			return err
		}
		// A wisp's create-time edge can flip a durable row's is_blocked: a
		// waiter on the wisp's durable spawner.
		if result.ChangedTables["issues"] {
			t.dirty.MarkDirty("issues")
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
	return scanIssueTxFromTable(ctx, t.tx, table, id)
}

// SearchIssueIDs returns matching IDs only, projected in Go from SearchIssues.
// It skips the issueops.SearchIssueIDsInTx fast path because that merges
// issues+wisps, while doltTransaction.SearchIssues reads one table (see its
// NO WISP MERGE note); the projection keeps the two consistent. Not worth
// re-implementing: partial-ID resolution calls the (fast) store path, never a
// transaction, so this is cold.
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
//     routes there — while the shared searchInTx merges them. So a default
//     (SkipWisps=false) search here answers what a SkipWisps=true search
//     answers at the store level. That is a caller-visible parity gap kept on
//     purpose, not a dropped filter field: merging the tiers here changes what
//     transaction callers see and is its own change with its own blast radius.
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
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(
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
		issue, err := t.GetIssue(ctx, id)
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
	labelsByID, err := issueops.GetLabelsForIssuesFromTableInTx(ctx, t.tx, tables.Labels, ids)
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
	depsByID, err := issueops.GetDependencyRecordsForIssuesFromTableInTx(ctx, t.tx, depTable, ids)
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

	result, err := issueops.UpdateIssueInTx(ctx, t.tx, id, updates, actor)
	if err != nil {
		return wrapExecError("update issue in tx", err)
	}
	if !result.Changed {
		return nil
	}
	t.dirty.MarkDirty(table)
	_, _, eventTable, _ := issueops.WispTableRouting(table == "wisps")
	t.dirty.MarkDirty(eventTable)
	// A wisp's status change can recompute a durable dependent.
	if result.IssueRowsChanged {
		t.dirty.MarkDirty("issues")
	}
	return nil
}

func (t *doltTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	table := "issues"
	eventTable := "events"
	if t.isActiveWisp(ctx, id) {
		table = "wisps"
		eventTable = "wisp_events"
	}

	result, err := issueops.CloseIssueInTx(ctx, t.tx, id, reason, actor, session)
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

// ReopenIssueWithResult reopens an issue within this transaction and reports
// whether the lifecycle state changed.
func (t *doltTransaction) ReopenIssueWithResult(ctx context.Context, id string, reason string, actor string) (bool, error) {
	table, eventTable := "issues", "events"
	if t.isActiveWisp(ctx, id) {
		table, eventTable = "wisps", "wisp_events"
	}
	result, err := issueops.ReopenIssueInTx(ctx, t.tx, id, reason, actor)
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
	// storage.Tx.DeleteIssue carries no actor (it is the actorless system
	// surface), so the journal rows record none — 0066's "system/unknown".
	result, err := issueops.DeleteIssueInTxWithResult(ctx, t.tx, id, "")
	if err != nil {
		return wrapExecError("delete issue in tx", err)
	}
	// Mark every table the ON DELETE CASCADE fans out to, not just the row's
	// own table: the cascaded deletions are invisible to the SQL we issue, so
	// staging only `issues` leaves them uncommitted in the working set. A
	// wisp's delete also removes the durable edges that pointed at it.
	for _, cascaded := range issueops.DeleteCascadeTables(isWisp) {
		t.dirty.MarkDirty(cascaded)
	}
	// Deleting a wisp can unblock a durable dependent.
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

	// One session sees both planes, so the in-tx target check and cycle check
	// see a target or an edge written earlier in this transaction on either
	// plane, and the wisp_dependencies -> issues FK sees an uncommitted durable
	// target.
	written, addErr := issueops.AddDependencyInTxWithResult(ctx, t.tx, dep, actor, opts)
	if addErr != nil {
		return addErr
	}
	t.dirty.MarkDirty(table)
	// AddDependencyInTx records a dependency_added event on the source's event
	// table only for a genuine emit (explicit verb + new edge); stage that table
	// so StageAndCommit commits the event with the edge (a torn write otherwise
	// leaves the event in the working set, dropped on reset). A structural or
	// idempotent add writes no event, so leave eventTable unstaged.
	if written.EventWritten {
		t.dirty.MarkDirty(eventTable)
	}
	// A blocking edge flips the source's or a descendant's is_blocked; a
	// durable row it flipped is committed with the edge (GH#6647), and an edge
	// that flipped none stages no `issues` (GH#2455).
	if written.Blocked.IssueRowsChanged {
		t.dirty.MarkDirty("issues")
	}
	return nil
}

// CycleThroughEdges reports a scheduling cycle through one of the new edges.
// The graph merges dependencies with wisp_dependencies, so uncommitted writes
// on both planes are gated — the previous DetectCycles read only the durable
// table and let bulk wisp edges commit scheduling cycles (bd-578h9.9).
func (t *doltTransaction) CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error) {
	graph := make(map[string][]string)
	if err := issueops.AppendSchedulingGraphInTx(ctx, t.tx, []string{"dependencies"}, graph); err != nil {
		return "", err
	}
	if err := issueops.AppendSchedulingGraphInTx(ctx, t.tx, []string{"wisp_dependencies"}, graph); err != nil {
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
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
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
	written, err := issueops.RemoveDependencyInTxWithResult(ctx, t.tx, issueID, dependsOnID, actor, rmOpts.EmitEvent)
	if err != nil {
		return wrapExecError("remove dependency in tx", err)
	}
	t.dirty.MarkDirty(table)
	// RemoveDependencyInTx records a dependency_removed event on the source's
	// event table only for a genuine emit (explicit verb + edge removal); stage
	// that table so it commits with the edge. A structural or missing-edge remove
	// writes no event, so leave eventTable unstaged.
	if written.EventWritten {
		t.dirty.MarkDirty(eventTable)
	}
	// The removal's recompute can clear the source's or a descendant's
	// is_blocked; that unblock is committed with the removal (GH#6647).
	if written.Blocked.IssueRowsChanged {
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

	if err := issueops.AddLabelInTx(ctx, t.tx, table, eventTable, issueID, label, actor); err != nil {
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
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`SELECT label FROM %s WHERE issue_id = ? ORDER BY label`, table), issueID)
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

	if err := issueops.RemoveLabelInTx(ctx, t.tx, table, eventTable, issueID, label, actor); err != nil {
		return wrapExecError("remove label in tx", err)
	}
	t.dirty.MarkDirty(table)
	t.dirty.MarkDirty(eventTable)
	return nil
}

// SetConfig sets a config value within the transaction
func (t *doltTransaction) SetConfig(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx, `
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
	table, err := issueops.SyncConfigTables(ctx, t.tx, key, value)
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
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, wrapQueryError("get config in tx", err)
}

// SetMetadata sets a metadata value within the transaction
func (t *doltTransaction) SetMetadata(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx, `
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
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, wrapQueryError("get metadata in tx", err)
}

// SetLocalMetadata sets a value in the dolt-ignored local_metadata table within the transaction.
func (t *doltTransaction) SetLocalMetadata(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx, "REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", key, value)
	return wrapExecError("set local metadata in tx", err)
}

// GetLocalMetadata gets a value from the dolt-ignored local_metadata table within the transaction.
func (t *doltTransaction) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM local_metadata WHERE `key` = ?", key).Scan(&value)
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
	id, _, err := issueops.InsertDerivedComment(ctx, t.tx, table, issueID, author, text, createdAtText)
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
	if err := issueops.RecordCommentEventInTx(ctx, t.tx, issueID, &issueops.EventComment{
		ID: id, Author: author, Text: text, CreatedAt: stored, Source: issueops.CommentSourceStructured,
	}); err != nil {
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
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
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
	id, err := issueops.InsertDerivedEventReturningID(ctx, t.tx, table, issueops.AuxEvent{
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
	if err := issueops.RecordCommentEventInTx(ctx, t.tx, issueID, &issueops.EventComment{
		ID: id, Author: actor, Text: comment, CreatedAt: stored, Source: issueops.CommentSourceAudit,
	}); err != nil {
		return wrapExecError("journal comment in tx", err)
	}
	return nil
}

// GetIssueCommentsPage returns one keyset page of an issue's comments within the
// transaction. The InTx read routes issueID to its tier itself, so a comment
// written on either tier earlier in THIS uncommitted transaction is visible.
func (t *doltTransaction) GetIssueCommentsPage(ctx context.Context, issueID string, after storage.CommentPageCursor, limit int) ([]*types.Comment, error) {
	return issueops.GetIssueCommentsPageInTx(ctx, t.tx, issueID, after, limit)
}

// CountIssuesByGroup returns per-group issue counts within the transaction,
// merging wisps, including any created earlier in this uncommitted
// transaction. Note the pre-existing count-vs-search asymmetry:
// CountIssuesByGroupInTx merges wisps into the buckets while SearchIssues reads
// the issues table only, so the two need not agree when wisps exist.
func (t *doltTransaction) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	return issueops.CountIssuesByGroupInTx(ctx, t.tx, filter, groupBy)
}

// GetDependentRecords returns the raw inbound dependency rows of targetID within
// the transaction. A target's inbound edges span BOTH dependency tables (a wisp
// source points at a durable target); the InTx read unions them on the one
// handle, so it sees edges written earlier in this uncommitted transaction on
// either plane.
func (t *doltTransaction) GetDependentRecords(ctx context.Context, targetID string, depType string, limit int, afterID string) ([]*types.Dependency, error) {
	return issueops.GetDependentRecordsInTx(ctx, t.tx, targetID, depType, limit, afterID)
}

// GetDependentRecordsForIssues returns the raw inbound dependency rows for a set
// of target ids within the transaction, keyed by target id, with the same
// both-plane visibility as GetDependentRecords.
func (t *doltTransaction) GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error) {
	return issueops.GetDependentRecordsForIssuesInTx(ctx, t.tx, targetIDs)
}

// CountDependentRecords returns the total inbound-edge count of targetID within
// the transaction, with the same both-plane visibility as GetDependentRecords.
func (t *doltTransaction) CountDependentRecords(ctx context.Context, targetID string, depType string) (int, error) {
	return issueops.CountDependentRecordsInTx(ctx, t.tx, targetID, depType)
}

// IsBlocked reports the denormalized transitive is_blocked flag and direct
// blockers of issueID within the transaction. The InTx read routes issueID to
// its tier itself, so the flag and blocker edges written for issueID earlier in
// THIS uncommitted transaction are visible on either tier.
func (t *doltTransaction) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	return issueops.IsBlockedInTx(ctx, t.tx, issueID)
}

// IsBlockedBatch reports the denormalized transitive is_blocked flag for a page
// of ids within the transaction. A batch can mix durable and wisp ids; the InTx
// read partitions them by wispness on the one handle, so every id reflects the
// flag written earlier in THIS uncommitted transaction, on either tier.
func (t *doltTransaction) IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error) {
	return issueops.IsBlockedBatchInTx(ctx, t.tx, ids)
}

// EventsSince returns durable events strictly after the keyset cursor within the
// transaction. Mirrors DoltStore.EventsSince's issueops delegation. The feed is
// durable-only by contract (wisp events are excluded), and an event recorded
// earlier in THIS uncommitted transaction is visible.
func (t *doltTransaction) EventsSince(ctx context.Context, cursor storage.EventCursor, issueID string, limit int) ([]*types.Event, error) {
	return issueops.EventsSinceInTx(ctx, t.tx, cursor.CreatedAt, cursor.ID, issueID, limit)
}
