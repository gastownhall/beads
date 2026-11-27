package uowstore

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

const (
	// setupRetryInitial and setupRetryMaxElapsed bound the retry of beginning
	// the unit of work, the only step RunInTransaction ever repeats.
	setupRetryInitial    = 25 * time.Millisecond
	setupRetryMaxElapsed = 10 * time.Second
	// rollbackTimeout bounds the detached rollback after a failed callback, so
	// a canceled caller context still releases a clean pinned session.
	rollbackTimeout = 5 * time.Second
)

// RunInTransaction runs fn in one unit of work and commits it with commitMsg.
//
// The contract is DoltStore's, which SDK callers were written against, and
// deliberately not uow.RunTx's:
//
//   - Only beginning the unit of work is retried. fn runs EXACTLY ONCE; a
//     serialization failure at commit is returned, never replayed.
//   - fn returning an error, or panicking, rolls back on a detached context
//     and returns fn's error unchanged (or re-panics).
//   - A non-empty commitMsg mints one Dolt commit; an empty one is a plain SQL
//     COMMIT with no Dolt commit. A deferred-version-commit marker on ctx is
//     honored by the unit of work.
//   - A commit failure without a decoded server response (the connection went
//     away mid-commit) wraps storage.ErrCommitIndeterminate: the write may
//     have landed, so the caller must not replay it blindly.
func (s *Store) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	uw, err := s.beginWithRetry(ctx)
	if err != nil {
		return err
	}
	// Closing the unit of work releases its pinned session; closing one that
	// never committed rolls it back. Detached, so a canceled caller still
	// rolls back, and deferred, so a panicking fn rolls back and re-panics.
	defer closeDetached(ctx, uw)

	if err := fn(newTxAdapter(uw)); err != nil {
		return err
	}

	if err := uw.Commit(ctx, commitMsg); err != nil {
		if issueops.IsNothingToCommitError(err) {
			return nil
		}
		return classifyCommitError(err)
	}
	return nil
}

// beginWithRetry begins a unit of work, retrying transient setup failures
// with a bounded backoff. Nothing has run yet, so a retry replays nothing.
func (s *Store) beginWithRetry(ctx context.Context) (uow.UnitOfWork, error) {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = setupRetryInitial
	bo.MaxElapsedTime = setupRetryMaxElapsed
	var uw uow.UnitOfWork
	err := backoff.Retry(func() error {
		var err error
		uw, err = s.newUOW(ctx)
		if err == nil {
			return nil
		}
		if isRetryableSetupError(err) {
			return err
		}
		return backoff.Permanent(err)
	}, backoff.WithContext(bo, ctx))
	if err != nil {
		var perm *backoff.PermanentError
		if errors.As(err, &perm) {
			return nil, perm.Err
		}
		return nil, err
	}
	return uw, nil
}

// isRetryableSetupError reports whether beginning a unit of work failed for a
// reason a fresh attempt can clear: a lost or stale connection, or a
// serialization failure at START TRANSACTION.
func isRetryableSetupError(err error) bool {
	if errors.Is(err, ErrStoreClosed) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, mysql.ErrInvalidConn) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
	}
	return false
}

// isSerialization reports a serialization failure (MySQL 1213 deadlock, 1205
// lock wait timeout): a definite rollback, which uow.RunTx replays.
func isSerialization(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

// classifyCommitError wraps a commit failure whose outcome is unknown. Only a
// decoded server error proves a definite rejection; every transport or
// protocol error is conservatively indeterminate, the same rule DoltStore
// applies (internal/storage/dolt isIndeterminateCommitResponse).
func classifyCommitError(err error) error {
	if errors.Is(err, storage.ErrCommitIndeterminate) {
		return err
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return err
	}
	return fmt.Errorf("commit: %w: %w", err, storage.ErrCommitIndeterminate)
}

// closeDetached closes uw on a context that survives the caller's
// cancellation, so a rollback after a canceled caller still runs.
func closeDetached(ctx context.Context, uw uow.UnitOfWork) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	uw.Close(closeCtx)
}

// txAdapter is storage.Transaction over one unit of work's use cases, modeled
// on the proxied CLI's batch (cmd/bd/batch_proxied_server.go runBatchOpUOW).
// Issue-versus-wisp routing resolves each id once per transaction.
type txAdapter struct {
	uw     uow.UnitOfWork
	planes map[string]bool // id -> isWisp
}

var _ storage.Transaction = (*txAdapter)(nil)

func newTxAdapter(uw uow.UnitOfWork) *txAdapter {
	return &txAdapter{uw: uw, planes: map[string]bool{}}
}

// resolve reports whether id names a wisp, reading through the unit of work
// so a row created earlier in this transaction is found.
func (t *txAdapter) resolve(ctx context.Context, id string) (bool, error) {
	if isWisp, ok := t.planes[id]; ok {
		return isWisp, nil
	}
	_, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(t.uw), id)
	if err != nil {
		return false, err
	}
	t.planes[id] = isWisp
	return isWisp, nil
}

// CreateIssue is the transaction's create: plane by issueops.IsWisp, no
// prefix validation, as DoltStore's transaction create. See create.
func (t *txAdapter) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if issue == nil {
		return errors.New("CreateIssue: issue must not be nil")
	}
	return t.create(ctx, []*types.Issue{issue}, actor, createOpts{})
}

// CreateIssues is the transaction's batch create, dependencies included.
func (t *txAdapter) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	return t.create(ctx, issues, actor, createOpts{withDeps: true})
}

func (t *txAdapter) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	isWisp, err := t.resolve(ctx, id)
	if err != nil {
		return err
	}
	if isWisp {
		return t.uw.IssueUseCase().UpdateWisp(ctx, id, updates, actor)
	}
	return t.uw.IssueUseCase().UpdateIssue(ctx, id, updates, actor)
}

// CloseIssue is the unchecked close the interface specifies.
func (t *txAdapter) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	isWisp, err := t.resolve(ctx, id)
	if err != nil {
		return err
	}
	params := domain.CloseIssueParams{Reason: reason, Session: session}
	if isWisp {
		_, err = t.uw.IssueUseCase().CloseWisp(ctx, id, params, actor)
	} else {
		_, err = t.uw.IssueUseCase().CloseIssue(ctx, id, params, actor)
	}
	return err
}

func (t *txAdapter) DeleteIssue(ctx context.Context, id string) error {
	isWisp, err := t.resolve(ctx, id)
	if err != nil {
		return err
	}
	// storage.Transaction.DeleteIssue (and the flat DeleteIssue, which runs
	// this) carries no actor, so the journal rows record none, as on
	// DoltStore.
	if isWisp {
		_, err = t.uw.IssueUseCase().DeleteWisp(ctx, id, issueops.NoRequestActor)
	} else {
		_, err = t.uw.IssueUseCase().DeleteIssue(ctx, id, issueops.NoRequestActor)
	}
	if err == nil {
		delete(t.planes, id)
	}
	return err
}

func (t *txAdapter) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	issue, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(t.uw), id)
	if err != nil {
		return nil, err
	}
	t.planes[id] = isWisp
	return issue, nil
}

func (t *txAdapter) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	page, err := t.uw.IssueUseCase().SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (t *txAdapter) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	return t.uw.IssueUseCase().SearchIssueIDs(ctx, query, filter)
}

// AddDependency lands the edge in its source's plane through the bulk verb,
// as the proxied CLI's batch does. It is quiet by default, as on DoltStore:
// the journal entry is written, the dependency_added events-table row only
// when opts.EmitEvent asks for it.
func (t *txAdapter) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return t.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{})
}

func (t *txAdapter) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts storage.DependencyAddOptions) error {
	if dep == nil {
		return errors.New("AddDependency: dependency must not be nil")
	}
	_, err := t.uw.DependencyUseCase().AddDependencies(ctx, []*types.Dependency{dep}, actor,
		domain.BulkAddDepsOpts{SkipPerEdgeCycleCheck: opts.SkipCycleCheck, SuppressEvents: !opts.EmitEvent})
	return err
}

// RemoveDependency removes the edge from its source's plane, quietly as
// AddDependency adds one. Removing an edge that is not there is not an error,
// as on every backend.
func (t *txAdapter) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return t.RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, storage.DependencyRemoveOptions{})
}

func (t *txAdapter) RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, opts storage.DependencyRemoveOptions) error {
	_, err := t.uw.DependencyUseCase().RemoveDependencyBySourceWithOptions(ctx, issueID, dependsOnID, actor,
		domain.RemoveDepBySourceOpts{SuppressEvent: !opts.EmitEvent})
	return err
}

func (t *txAdapter) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	isWisp, err := t.resolve(ctx, issueID)
	if err != nil {
		return nil, err
	}
	var recs map[string][]*types.Dependency
	if isWisp {
		recs, err = t.uw.DependencyUseCase().GetWispDependencyRecords(ctx, []string{issueID})
	} else {
		recs, err = t.uw.DependencyUseCase().GetIssueDependencyRecords(ctx, []string{issueID})
	}
	if err != nil {
		return nil, err
	}
	return recs[issueID], nil
}

func (t *txAdapter) CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error) {
	return t.uw.DependencyUseCase().CycleThroughEdges(ctx, edges)
}

func (t *txAdapter) AddLabel(ctx context.Context, issueID, label, actor string) error {
	isWisp, err := t.resolve(ctx, issueID)
	if err != nil {
		return err
	}
	if isWisp {
		return t.uw.LabelUseCase().AddWispLabel(ctx, issueID, label, actor)
	}
	return t.uw.LabelUseCase().AddLabel(ctx, issueID, label, actor)
}

func (t *txAdapter) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	isWisp, err := t.resolve(ctx, issueID)
	if err != nil {
		return err
	}
	if isWisp {
		return t.uw.LabelUseCase().RemoveWispLabel(ctx, issueID, label, actor)
	}
	return t.uw.LabelUseCase().RemoveLabel(ctx, issueID, label, actor)
}

func (t *txAdapter) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	isWisp, err := t.resolve(ctx, issueID)
	if err != nil {
		return nil, err
	}
	if isWisp {
		return t.uw.LabelUseCase().GetWispLabels(ctx, issueID)
	}
	return t.uw.LabelUseCase().GetLabels(ctx, issueID)
}

func (t *txAdapter) SetConfig(ctx context.Context, key, value string) error {
	return t.uw.ConfigUseCase().SetConfig(ctx, key, value)
}

func (t *txAdapter) GetConfig(ctx context.Context, key string) (string, error) {
	return t.uw.ConfigUseCase().GetConfig(ctx, key)
}

func (t *txAdapter) SetMetadata(ctx context.Context, key, value string) error {
	return t.uw.ConfigUseCase().SetMetadata(ctx, key, value)
}

func (t *txAdapter) GetMetadata(ctx context.Context, key string) (string, error) {
	return t.uw.ConfigUseCase().GetMetadata(ctx, key)
}

func (t *txAdapter) SetLocalMetadata(ctx context.Context, key, value string) error {
	return t.uw.ConfigUseCase().SetLocalMetadata(ctx, key, value)
}

func (t *txAdapter) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	return t.uw.ConfigUseCase().GetLocalMetadata(ctx, key)
}

func (t *txAdapter) AddComment(ctx context.Context, issueID, actor, comment string) error {
	isWisp, err := t.resolve(ctx, issueID)
	if err != nil {
		return err
	}
	if isWisp {
		_, err = t.uw.CommentUseCase().AddCommentToWisp(ctx, issueID, actor, comment)
	} else {
		_, err = t.uw.CommentUseCase().AddCommentToIssue(ctx, issueID, actor, comment)
	}
	return err
}

func (t *txAdapter) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	isWisp, err := t.resolve(ctx, issueID)
	if err != nil {
		return nil, err
	}
	if isWisp {
		return t.uw.CommentUseCase().GetCommentsForWisp(ctx, issueID)
	}
	return t.uw.CommentUseCase().GetCommentsForIssue(ctx, issueID)
}

func (t *txAdapter) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	return t.uw.IssueUseCase().CountIssuesByGroup(ctx, filter, groupBy)
}

func (t *txAdapter) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	return t.uw.DependencyUseCase().IsBlocked(ctx, issueID)
}

func (t *txAdapter) IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error) {
	return t.uw.DependencyUseCase().IsBlockedBatch(ctx, ids)
}

// The Transaction methods below have no one-call use case, so they refuse
// rather than grow new SQL here.

func (t *txAdapter) ImportIssueComment(context.Context, string, string, string, time.Time) (*types.Comment, error) {
	return nil, errUnsupported("Transaction.ImportIssueComment")
}

func (t *txAdapter) GetIssueCommentsPage(context.Context, string, storage.CommentPageCursor, int) ([]*types.Comment, error) {
	return nil, errUnsupported("Transaction.GetIssueCommentsPage")
}

func (t *txAdapter) GetDependentRecords(context.Context, string, string, int, string) ([]*types.Dependency, error) {
	return nil, errUnsupported("Transaction.GetDependentRecords")
}

func (t *txAdapter) GetDependentRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, errUnsupported("Transaction.GetDependentRecordsForIssues")
}

func (t *txAdapter) CountDependentRecords(context.Context, string, string) (int, error) {
	return 0, errUnsupported("Transaction.CountDependentRecords")
}

func (t *txAdapter) EventsSince(context.Context, storage.EventCursor, string, int) ([]*types.Event, error) {
	return nil, errUnsupported("Transaction.EventsSince")
}
