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
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// doltTransaction implements storage.Transaction for Dolt
type doltTransaction struct {
	regularTx *sql.Tx
	ignoredTx *sql.Tx
	store     *DoltStore
	dirty     versioncontrolops.DirtyTableTracker

	// wroteRegularDep/wroteWispDep record whether a dependency row has been
	// written to each tier during this logical transaction. Regular and wisp
	// dependency tables live on separate SQL sessions, so a single-session
	// cycle check cannot see the other session's uncommitted edges; these flags
	// let AddDependencyWithOptions fall back to the merged two-session cycle
	// check once both tiers are in play. The DirtyTableTracker cannot serve this
	// role: it deliberately drops wisp_* tables because they are dolt-ignored.
	wroteRegularDep bool
	wroteWispDep    bool
	// journalPinned records that ignoredTx IS regularTx because the events
	// journal collapsed the two planes into one transaction, so the finish path
	// must not commit or roll it back a second time.
	journalPinned bool
}

func (t *doltTransaction) txFor(table string) *sql.Tx {
	if table == "wisps" || strings.HasPrefix(table, "wisp_") ||
		table == "local_metadata" || table == "repo_mtimes" {
		return t.ignoredTx
	}
	return t.regularTx
}

// isActiveWisp checks if an ID exists in the wisps table within the transaction.
// Unlike the store-level isActiveWisp, this queries within the transaction so it
// sees uncommitted wisps. Handles both -wisp- pattern and explicit-ID ephemerals (GH#2053).
func (t *doltTransaction) isActiveWisp(ctx context.Context, id string) bool {
	var exists int
	err := t.ignoredTx.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
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
			tx := &doltTransaction{regularTx: sqlTx, ignoredTx: sqlTx, store: s}
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

	var currentBranch string
	if err := conn.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		return fmt.Errorf("failed to read active branch: %w", err)
	}

	regularTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin regular tx: %w", err)
	}

	// The journal counter and rows must commit in the SAME SQL transaction as
	// every mutation they describe. bd_events_journal and bd_events_seq are
	// dolt_ignored, so on the default split-transaction shape they would land in
	// the ignored transaction while the mutation lands in the regular one: a
	// mixed durable+wisp callback would then make the two transactions contend
	// with each other on the single bd_events_seq row, and the ignored commit
	// can fail AFTER the regular side has already committed — a mutation with no
	// journal record, which is exactly the state the same-transaction guarantee
	// exists to make impossible. In journal mode both planes therefore share the
	// pinned regular transaction. The default journal-off path keeps the
	// established split transactions untouched.
	journalEnabled := s.eventsJournalEnabled.Load()
	ignoredTx := regularTx
	if !journalEnabled {
		// NOTE (GH#3140 metrics skew): the pool-wait bracket above measures only
		// the FIRST acquisition (the regular conn). A borrow inside
		// beginIgnoredTxOnBranch that has to wait increments the pool's global
		// WaitCount, which the NEXT transaction's delta then misattributes. Rare
		// given the InUse pre-check in borrowConnForIgnoredTx; not worth
		// restructuring the metrics for.
		var ignoredCleanup func()
		ignoredCleanup, ignoredTx, err = s.beginIgnoredTxOnBranch(ctx, currentBranch)
		if err != nil {
			_ = regularTx.Rollback()
			return err
		}
		defer ignoredCleanup()
	}
	clearJournalScope := issueops.ScopeEventsJournalTransaction(regularTx, journalEnabled)
	defer clearJournalScope()

	tx := &doltTransaction{regularTx: regularTx, ignoredTx: ignoredTx, store: s, journalPinned: journalEnabled}

	defer func() {
		if r := recover(); r != nil {
			_ = regularTx.Rollback()
			if !journalEnabled {
				_ = ignoredTx.Rollback()
			}
			panic(r)
		}
	}()

	if err := fn(tx); err != nil {
		_ = regularTx.Rollback()
		if !journalEnabled {
			_ = ignoredTx.Rollback()
		}
		return err
	}

	return s.finishDoltTransaction(ctx, conn, tx, commitMsg)
}

// finishDoltTransaction commits the regular SQL transaction, its associated
// Dolt revision, and then the ignored-table transaction. Once the regular SQL
// transaction succeeds, later failures have an indeterminate durable outcome.
// When the journal pinned both planes into the regular transaction, that single
// commit already carried the ignored tables and there is no second transaction
// to roll back or commit.
func (s *DoltStore) finishDoltTransaction(ctx context.Context, conn *sql.Conn, tx *doltTransaction, commitMsg string) error {
	rollbackIgnored := func() {
		if !tx.journalPinned {
			_ = tx.ignoredTx.Rollback()
		}
	}

	if err := tx.regularTx.Commit(); err != nil {
		rollbackIgnored()
		return wrapSQLCommitError("sql commit (regular)", err)
	}

	if err := versioncontrolops.StageAndCommit(ctx, conn, tx.dirty.DirtyTables(), commitMsg, s.commitAuthorString()); err != nil {
		rollbackIgnored()
		return fmt.Errorf("stage and commit after regular SQL commit: %w: %w", err, ErrCommitIndeterminate)
	}

	if tx.journalPinned {
		return nil
	}
	if err := tx.ignoredTx.Commit(); err != nil {
		return fmt.Errorf("sql commit (ignored, regular already committed): %w: %w", err, ErrCommitIndeterminate)
	}
	return nil
}

// ignoredTxBorrowTimeout bounds how long a borrow of a second warm connection
// from the main pool may wait before falling back to a dedicated fresh dial. It
// keeps the second acquisition from ever waiting unboundedly while the caller
// already holds the first (regular-tx) connection, which is what makes deadlock
// impossible by construction on the borrow path.
const ignoredTxBorrowTimeout = 250 * time.Millisecond

// beginIgnoredTxOnBranch starts the ignored-tables transaction, checked out to
// the regular transaction's branch. It borrows a second warm connection from the
// main pool when one is safely available — the hosted-gateway churn fix: once the
// pool is warm this costs zero new MySQL handshakes and zero Dolt session-setup
// round-trips per write. It falls back to a dedicated single-connection pool when
// borrowing could deadlock (MaxOpenConns==1, the documented case that every
// branch-isolated test exercises) or when the pool is exhausted or a borrowed
// connection turns out to be stale.
//
// The returned cleanup closure releases whichever acquisition path was taken, so
// the caller does not need to know which one ran.
func (s *DoltStore) beginIgnoredTxOnBranch(ctx context.Context, branch string) (cleanup func(), tx *sql.Tx, err error) {
	// Borrow fast path: reuse an already-open pooled connection. Unlike the
	// fallback below, this path never switches the session's branch — see
	// beginBorrowedTx for the pool invariant it preserves.
	if conn := s.borrowConnForIgnoredTx(ctx); conn != nil {
		tx, err := beginBorrowedTx(ctx, conn, branch)
		if err == nil {
			return func() { _ = conn.Close() }, tx, nil
		}
		// A stale pooled connection or a session on another branch: a fresh
		// dial always worked before, so discard this one (its session state is
		// untouched) and fall through to the fallback.
		_ = conn.Close()
	}

	// Fallback: a dedicated single-connection pool, paying the fresh dial the
	// borrow path exists to avoid.
	doltMetrics.ignoredTxFreshPool.Add(ctx, 1)
	db, err := sql.Open("mysql", s.connStr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open ignored tx connection: %w", err)
	}
	db.SetMaxOpenConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("failed to acquire ignored tx connection: %w", err)
	}

	tx, err = beginTxOnConn(ctx, conn, branch)
	if err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, nil, err
	}

	return func() { _ = conn.Close(); _ = db.Close() }, tx, nil
}

// borrowConnForIgnoredTx returns a second connection borrowed from the main pool
// for the ignored-tables transaction, or nil if borrowing is unsafe or would
// block. The caller falls back to a dedicated single-connection pool on nil.
func (s *DoltStore) borrowConnForIgnoredTx(ctx context.Context) *sql.Conn {
	st := s.db.Stats()
	// MaxOpenConns==1: the caller already pinned the pool's only connection for
	// the regular tx, so a borrow would deadlock. Preserve today's behavior.
	if st.MaxOpenConnections == 1 {
		return nil
	}
	// Exhausted pool: skip the wait and go straight to the fallback.
	// MaxOpenConnections==0 means unlimited — always safe to grow.
	if st.MaxOpenConnections > 0 && st.InUse >= st.MaxOpenConnections {
		return nil
	}

	bctx, cancel := context.WithTimeout(ctx, ignoredTxBorrowTimeout)
	defer cancel()
	conn, err := s.db.Conn(bctx)
	if err != nil {
		// Lost a stats/Conn race, parent ctx canceled, or a slow dial exceeded
		// the borrow timeout — fall back to a fresh dial.
		return nil
	}
	return conn
}

// beginBorrowedTx begins the ignored-tables transaction on a connection
// borrowed from the main pool, without ever changing that session's branch.
//
// Pool invariant: DOLT_CHECKOUT is session-level, and the borrow cleanup
// returns the connection to the pool as-is — so switching its branch here
// would leak a foreign branch into the pool for an unrelated later caller.
// Every other production checkout site (federation staging, compact, flatten)
// restores the branch before releasing the connection; the borrow path
// preserves the same invariant by refusing instead of switching. Today no
// shipped flow diverges a pool session's branch from the regular tx's branch
// (DoltStore.Checkout has no non-test callers), so this is defense in depth
// for future Checkout callers and for multi-connection tests.
//
// Instead of an unconditional checkout it verifies the session is already on
// the requested branch — the overwhelmingly common case — and sends the
// caller to the fresh-dial fallback otherwise. Same round-trip count as the
// checkout it replaces (one statement), so the borrow fast path stays free.
func beginBorrowedTx(ctx context.Context, conn *sql.Conn, branch string) (*sql.Tx, error) {
	var active string
	if err := conn.QueryRowContext(ctx, "SELECT active_branch()").Scan(&active); err != nil {
		return nil, fmt.Errorf("failed to read borrowed conn's active branch: %w", err)
	}
	if active != branch {
		return nil, fmt.Errorf("borrowed conn is on branch %q, want %q: refusing to switch a pooled session's branch", active, branch)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin ignored tx: %w", err)
	}
	return tx, nil
}

// beginTxOnConn checks a connection out to branch and begins a transaction on
// it. Only the fallback path uses it: the fallback owns a dedicated
// single-connection pool, so checking its session out is safe. Every Dolt SQL
// session has its own active branch, so the explicit checkout is required on
// a fresh dial.
func beginTxOnConn(ctx context.Context, conn *sql.Conn, branch string) (*sql.Tx, error) {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", branch); err != nil {
		return nil, fmt.Errorf("failed to checkout ignored tx branch %s: %w", branch, err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin ignored tx: %w", err)
	}
	return tx, nil
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

	// Build the validation context on regularTx for both tiers: wisp rows
	// live on the ignored session, but the validation context (config,
	// custom_types) lives in regular dolt-tracked tables — reading it
	// through regularTx keeps types registered earlier in this transaction
	// (tx.SetConfig("types.custom", ...)) visible. Both sessions are
	// pinned to the same branch (GH#5443).
	bc, err := issueops.NewBatchContext(ctx, t.regularTx, storage.BatchCreateOptions{SkipPrefixValidation: true})
	if err != nil {
		return err
	}

	if issueops.IsWisp(issue) {
		_, err = issueops.CreateIssueInTxWithResult(ctx, t.ignoredTx, bc, issue, actor)
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
		if _, err := issueops.CreateIssuesInTxWithContext(ctx, t.ignoredTx, bc, wispIssues, actor); err != nil {
			return err
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
	return scanIssueTxFromTable(ctx, t.txFor(table), table, id)
}

// SearchIssueIDs returns matching IDs only, projected in Go from SearchIssues.
// It skips the issueops.SearchIssueIDsInTx fast path because that merges
// issues+wisps over one *sql.Tx, while doltTransaction splits them across
// regularTx/ignoredTx (see txFor). Not worth re-implementing: partial-ID
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
//     routes there — because doltTransaction splits the two tiers across
//     regularTx/ignoredTx (see txFor) and the shared searchInTx merges them over
//     a single *sql.Tx. So a default (SkipWisps=false) search here answers what
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
	rows, err := t.txFor(tables.Main).QueryContext(ctx, fmt.Sprintf(
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
	labelsByID, err := issueops.GetLabelsForIssuesFromTableInTx(ctx, t.txFor(tables.Labels), tables.Labels, ids)
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
	depsByID, err := issueops.GetDependencyRecordsForIssuesFromTableInTx(ctx, t.txFor(depTable), depTable, ids)
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

	result, err := issueops.UpdateIssueInTx(ctx, t.txFor(table), id, updates, actor)
	if err != nil {
		return wrapExecError("update issue in tx", err)
	}
	if !result.Changed {
		return nil
	}
	t.dirty.MarkDirty(table)
	_, _, eventTable, _ := issueops.WispTableRouting(table == "wisps")
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

	result, err := issueops.CloseIssueInTx(ctx, t.txFor(table), id, reason, actor, session)
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
	result, err := issueops.ReopenIssueInTx(ctx, t.txFor(table), id, reason, actor)
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
	table := "issues"
	if isWisp {
		table = "wisps"
	}
	if err := issueops.DeleteIssueInTx(ctx, t.txFor(table), id); err != nil {
		return wrapExecError("delete issue in tx", err)
	}
	// Mark every table the ON DELETE CASCADE fans out to, not just the row's
	// own table: the cascaded deletions are invisible to the SQL we issue, so
	// staging only `issues` leaves them uncommitted in the working set.
	for _, cascaded := range issueops.DeleteCascadeTables(isWisp) {
		t.dirty.MarkDirty(cascaded)
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

	// Regular and dolt-ignored tables run on separate SQL sessions, so when
	// the edge's write table and its target issue live in different tiers,
	// target reads on the write tx cannot see a target created earlier in
	// this same logical transaction (e.g. `bd create --deps blocks:<id>`
	// swapping the new issue into the target slot). Read the target on its
	// own tx and hand the row to AddDependencyInTx.
	crossTierTarget := kind != issueops.DepTargetExternal && t.txFor(targetTable) != t.txFor(table)
	if crossTierTarget {
		precheck, err := t.readDepTargetForPrecheck(ctx, dep.IssueID, targetTable, dep.DependsOnID)
		if err != nil {
			return err
		}
		opts.PrecheckedTarget = precheck
	}

	// The single-session in-tx cycle check only sees its own session's
	// uncommitted rows. Fall back to the merged two-session check whenever a
	// scheduling cycle could hide on the other session: either this edge itself
	// crosses tiers, or a dependency row was already written to the other tier
	// earlier in this logical transaction. The latter covers a create-time
	// batch like `blocks:<wisp>,depends-on:<regular>`, where the cross-tier
	// `blocks` edge is pending on the ignored session and the same-tier
	// `depends-on` edge would otherwise close the cycle unseen.
	if !opts.SkipCycleCheck && (crossTierTarget || t.otherDepTierPending(table)) {
		if err := t.checkCrossTierSchedulingCycle(ctx, dep); err != nil {
			return err
		}
		opts.SkipCycleCheck = true
	}

	var addErr error
	var eventWritten bool
	if opts.PrecheckedTarget != nil && table == "wisp_dependencies" && kind == issueops.DepTargetIssue {
		eventWritten, addErr = t.addWispDepSuspendingIssueTargetFK(ctx, dep, actor, opts)
	} else {
		eventWritten, addErr = issueops.AddDependencyInTx(ctx, t.txFor(table), dep, actor, opts)
	}
	if addErr != nil {
		return addErr
	}
	t.dirty.MarkDirty(table)
	// AddDependencyInTx records a dependency_added event on the source's event
	// table only for a genuine emit (explicit verb + new edge); stage that table
	// so StageAndCommit commits the event with the edge (a torn write otherwise
	// leaves the event in the working set, dropped on reset). A structural or
	// idempotent add writes no event, so leave eventTable unstaged.
	if eventWritten {
		t.dirty.MarkDirty(eventTable)
	}
	t.recordDepTierWrite(table)
	return nil
}

// otherDepTierPending reports whether a dependency row was written to the tier
// opposite writeTable earlier in this logical transaction. Because the regular
// and wisp dependency tables run on separate SQL sessions, an in-tx cycle check
// on writeTable's session cannot see the other session's uncommitted scheduling
// edges; when the other tier has pending writes the caller must use the merged
// two-session cycle check instead.
func (t *doltTransaction) otherDepTierPending(writeTable string) bool {
	if writeTable == "wisp_dependencies" {
		return t.wroteRegularDep
	}
	return t.wroteWispDep
}

// recordDepTierWrite notes that a dependency row was written to writeTable's
// tier so a later same-tier edge on the opposite session can detect that the
// merged cycle check is required. See otherDepTierPending.
func (t *doltTransaction) recordDepTierWrite(writeTable string) {
	if writeTable == "wisp_dependencies" {
		t.wroteWispDep = true
		return
	}
	t.wroteRegularDep = true
}

// addWispDepSuspendingIssueTargetFK inserts a wisp-source dependency whose
// target is a regular issue created earlier in this logical transaction.
// wisp_dependencies carries a real FK (depends_on_issue_id -> issues), and
// the ignored session cannot see an issues row still uncommitted on the
// regular session, so the insert would fail FK validation even though the
// target's existence was just validated on the regular tx. The regular tx
// commits before the ignored tx, so the committed end-state always satisfies
// the FK; suspend the session's FK checks around this one statement scope.
func (t *doltTransaction) addWispDepSuspendingIssueTargetFK(ctx context.Context, dep *types.Dependency, actor string, opts issueops.AddDependencyOpts) (bool, error) {
	if _, err := t.ignoredTx.ExecContext(ctx, "SET foreign_key_checks = 0"); err != nil {
		return false, fmt.Errorf("suspend foreign key checks for cross-tier dependency: %w", err)
	}
	eventWritten, addErr := issueops.AddDependencyInTx(ctx, t.ignoredTx, dep, actor, opts)
	if _, err := t.ignoredTx.ExecContext(ctx, "SET foreign_key_checks = 1"); err != nil && addErr == nil {
		addErr = fmt.Errorf("restore foreign key checks after cross-tier dependency: %w", err)
	}
	return eventWritten, addErr
}

// readDepTargetForPrecheck validates a dependency target on the transaction
// that owns its table and returns the row fields AddDependencyInTx needs when
// it cannot read the target itself (cross-tier edges).
func (t *doltTransaction) readDepTargetForPrecheck(ctx context.Context, sourceID, targetTable, id string) (*issueops.DepTargetPrecheck, error) {
	var p issueops.DepTargetPrecheck
	//nolint:gosec // G201: targetTable is "issues" or "wisps"
	err := t.txFor(targetTable).QueryRowContext(ctx,
		fmt.Sprintf(`SELECT issue_type, status FROM %s WHERE id = ?`, targetTable), id,
	).Scan(&p.IssueType, &p.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, issueops.MissingDependencyTarget(sourceID, id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to check target issue existence: %w", err)
	}
	return &p, nil
}

// checkCrossTierSchedulingCycle rejects a scheduling edge that would close a
// cycle, using the merged view of both sessions' dependency tables. The in-tx
// cycle check scans both tables on the write tx and so misses edges added on
// the other session earlier in this logical transaction.
//
// The set is types.IsSchedulingEdge's, by call and not by restatement: an
// inline copy that missed a fifth scheduling type would fall through to
// "not a scheduling edge" and skip this gate entirely, which is silence rather
// than a failure — the whole reason that predicate was consolidated (ga-2ltro.10).
func (t *doltTransaction) checkCrossTierSchedulingCycle(ctx context.Context, dep *types.Dependency) error {
	if !types.IsSchedulingEdge(dep.Type) {
		return nil
	}
	cycle, err := t.CycleThroughEdges(ctx, [][2]string{{dep.IssueID, dep.DependsOnID}})
	if err != nil {
		return err
	}
	if cycle != "" {
		return domain.ErrDependencyCycle
	}
	return nil
}

// CycleThroughEdges reports a scheduling cycle through one of the new edges.
// The graph merges the regular tx's dependencies with the ignored tx's
// wisp_dependencies, so uncommitted writes on both sides are gated — the
// previous DetectCycles ran only on the regular tx and let bulk wisp edges
// commit scheduling cycles (bd-578h9.9).
func (t *doltTransaction) CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error) {
	graph := make(map[string][]string)
	if err := issueops.AppendSchedulingGraphInTx(ctx, t.txFor("dependencies"), []string{"dependencies"}, graph); err != nil {
		return "", err
	}
	if err := issueops.AppendSchedulingGraphInTx(ctx, t.txFor("wisp_dependencies"), []string{"wisp_dependencies"}, graph); err != nil {
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
	rows, err := t.txFor(table).QueryContext(ctx, fmt.Sprintf(`
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
	eventWritten, err := issueops.RemoveDependencyInTx(ctx, t.txFor(table), issueID, dependsOnID, actor, rmOpts.EmitEvent)
	if err != nil {
		return wrapExecError("remove dependency in tx", err)
	}
	t.dirty.MarkDirty(table)
	// RemoveDependencyInTx records a dependency_removed event on the source's
	// event table only for a genuine emit (explicit verb + edge removal); stage
	// that table so it commits with the edge. A structural or missing-edge remove
	// writes no event, so leave eventTable unstaged.
	if eventWritten {
		t.dirty.MarkDirty(eventTable)
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

	if err := issueops.AddLabelInTx(ctx, t.txFor(table), table, eventTable, issueID, label, actor); err != nil {
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
	rows, err := t.txFor(table).QueryContext(ctx, fmt.Sprintf(`SELECT label FROM %s WHERE issue_id = ? ORDER BY label`, table), issueID)
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

	if err := issueops.RemoveLabelInTx(ctx, t.txFor(table), table, eventTable, issueID, label, actor); err != nil {
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
	_, err := t.ignoredTx.ExecContext(ctx, "REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", key, value)
	return wrapExecError("set local metadata in tx", err)
}

// GetLocalMetadata gets a value from the dolt-ignored local_metadata table within the transaction.
func (t *doltTransaction) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := t.ignoredTx.QueryRowContext(ctx, "SELECT value FROM local_metadata WHERE `key` = ?", key).Scan(&value)
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
	id, _, err := issueops.InsertDerivedComment(ctx, t.txFor(table), table, issueID, author, text, createdAtText)
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
	if err := issueops.RecordCommentEventInTx(ctx, t.txFor(table), issueID, &issueops.EventComment{
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
	rows, err := t.txFor(table).QueryContext(ctx, fmt.Sprintf(`
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
	id, err := issueops.InsertDerivedEventReturningID(ctx, t.txFor(table), table, issueops.AuxEvent{
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
	if err := issueops.RecordCommentEventInTx(ctx, t.txFor(table), issueID, &issueops.EventComment{
		ID: id, Author: actor, Text: comment, CreatedAt: stored, Source: issueops.CommentSourceAudit,
	}); err != nil {
		return wrapExecError("journal comment in tx", err)
	}
	return nil
}

// GetIssueCommentsPage returns one keyset page of an issue's comments within the
// transaction. Like the OLD GetIssueComments/GetDependencyRecords tx methods, it
// pre-resolves wispness on the ignored session and hands the InTx read the
// handle that owns issueID's tier, so a comment written on either tier earlier in
// THIS uncommitted transaction is visible (durable rows live on regularTx, wisp
// rows on ignoredTx — see the struct comment on the two-session split).
func (t *doltTransaction) GetIssueCommentsPage(ctx context.Context, issueID string, after storage.CommentPageCursor, limit int) ([]*types.Comment, error) {
	tx := t.regularTx
	if t.isActiveWisp(ctx, issueID) {
		tx = t.ignoredTx
	}
	return issueops.GetIssueCommentsPageInTx(ctx, tx, issueID, after, limit)
}

// CountIssuesByGroup returns per-group issue counts within the transaction.
//
// TWO-SESSION SCOPING: the count runs on regularTx, so it reflects this tx's own
// uncommitted DURABLE issues plus all COMMITTED issues and wisps, but NOT wisps
// created in this same uncommitted transaction (those live on the separate
// ignored session). This matches doltTransaction.SearchIssues, which is likewise
// durable-tier for the tx's own writes. Note the pre-existing count-vs-search
// asymmetry: CountIssuesByGroupInTx merges committed wisps into the buckets while
// SearchIssues reads the issues table only, so the two need not agree when
// committed wisps exist. The embedded backend has no session split and sees
// in-tx wisps here.
func (t *doltTransaction) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	return issueops.CountIssuesByGroupInTx(ctx, t.regularTx, filter, groupBy)
}

// GetDependentRecords returns the raw inbound dependency rows of targetID within
// the transaction.
//
// TWO-SESSION SCOPING: a target's inbound edges genuinely span BOTH dependency
// tables (a wisp source points at a durable target), and the InTx read unions
// them with an in-query, cross-table de-dup that must run on a single handle.
// Run on regularTx, it sees this tx's own uncommitted DURABLE edges plus all
// COMMITTED edges, but NOT wisp edges written in this same uncommitted
// transaction (those live on the ignored session and become visible after
// commit). The embedded backend has no session split and sees in-tx wisp edges.
func (t *doltTransaction) GetDependentRecords(ctx context.Context, targetID string, depType string, limit int, afterID string) ([]*types.Dependency, error) {
	return issueops.GetDependentRecordsInTx(ctx, t.regularTx, targetID, depType, limit, afterID)
}

// GetDependentRecordsForIssues returns the raw inbound dependency rows for a set
// of target ids within the transaction, keyed by target id. Same TWO-SESSION
// SCOPING as GetDependentRecords: uncommitted-durable plus committed edges on the
// server backend; wisp edges written in this same transaction are visible after
// commit. The embedded backend sees in-tx wisp edges.
func (t *doltTransaction) GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error) {
	return issueops.GetDependentRecordsForIssuesInTx(ctx, t.regularTx, targetIDs)
}

// CountDependentRecords returns the total inbound-edge count of targetID within
// the transaction. Same TWO-SESSION SCOPING as GetDependentRecords — the count
// uses a cross-table NOT-IN subquery that must run on one handle, so on the
// server backend it excludes wisp edges written in this same uncommitted
// transaction (visible after commit). The embedded backend sees them.
func (t *doltTransaction) CountDependentRecords(ctx context.Context, targetID string, depType string) (int, error) {
	return issueops.CountDependentRecordsInTx(ctx, t.regularTx, targetID, depType)
}

// IsBlocked reports the denormalized transitive is_blocked flag and direct
// blockers of issueID within the transaction. Like GetIssueCommentsPage, it
// pre-resolves wispness and reads on the session that owns issueID's tier, so the
// is_blocked flag and blocker edges written for issueID earlier in THIS
// uncommitted transaction are visible on either tier.
func (t *doltTransaction) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	tx := t.regularTx
	if t.isActiveWisp(ctx, issueID) {
		tx = t.ignoredTx
	}
	return issueops.IsBlockedInTx(ctx, tx, issueID)
}

// IsBlockedBatch reports the denormalized transitive is_blocked flag for a page
// of ids within the transaction. A batch can mix durable and wisp ids whose
// is_blocked columns live on different sessions, so — unlike a single-handle
// delegation — it partitions the ids by wispness (resolved on the ignored
// session so this tx's own uncommitted wisps count) and reads each tier's
// is_blocked on its owning session, then merges. Every id therefore reflects the
// flag written earlier in THIS uncommitted transaction, on either tier.
func (t *doltTransaction) IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error) {
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	wispIDs, permIDs, err := issueops.PartitionWispIDsInTx(ctx, t.ignoredTx, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(ids))
	if len(permIDs) > 0 {
		durable, err := issueops.IsBlockedBatchInTx(ctx, t.regularTx, permIDs)
		if err != nil {
			return nil, err
		}
		for id, blocked := range durable {
			result[id] = blocked
		}
	}
	if len(wispIDs) > 0 {
		wisp, err := issueops.IsBlockedBatchInTx(ctx, t.ignoredTx, wispIDs)
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
