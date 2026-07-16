package flatfile

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// flatFileTx implements storage.Transaction. Each file write is atomic
// (write-then-rename) and journaled (journal.go), so a failed transaction
// rolls every touched file back to its pre-image. The embedded
// *FlatFileStore promotes every store method, so the transaction reuses them
// directly; only the methods below diverge from their store namesakes.
type flatFileTx struct {
	*FlatFileStore
}

var _ storage.Transaction = (*flatFileTx)(nil)

// RunInTransaction executes fn as one all-or-nothing mutation: file writes
// inside fn journal their pre-images, and an error (or panic, matching
// sqlkit) restores them all. Known gap: UpdateIssueID's best-effort
// comment-dir/event-file renames are directory moves outside the file
// journal and are not undone.
func (s *FlatFileStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	// Hold txMu's write half for the whole span: concurrent transactions and
	// every non-transactional mutation (txShared read-lockers) are excluded,
	// so nothing foreign is ever journaled — a rollback can only revert this
	// transaction's own writes. txOwner lets the callback's own mutations,
	// which run on this goroutine, skip the read half (see txShared); it is
	// cleared before txMu is released (deferred LIFO).
	s.txMu.Lock()
	defer s.txMu.Unlock()
	s.txOwner.Store(goroutineID())
	defer s.txOwner.Store(0)

	s.journalMu.Lock()
	s.journal = make(map[string]journalEntry)
	s.journalMu.Unlock()
	defer func() {
		s.journalMu.Lock()
		s.journal = nil
		s.journalMu.Unlock()
	}()

	defer func() {
		if r := recover(); r != nil {
			_ = s.rollbackJournal()
			panic(r)
		}
	}()

	if err := fn(&flatFileTx{s}); err != nil {
		if rbErr := s.rollbackJournal(); rbErr != nil {
			return fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return err
	}
	return s.commitTransaction(ctx, commitMsg)
}

// runAtomic runs fn as one all-or-nothing, fully serialized mutation: outside
// a transaction it wraps fn in RunInTransaction (no commit message, so no git
// side effects) — txMu's write half excludes every concurrent writer and the
// pre-image journal rolls back a partial multi-file failure, mirroring the
// SQL backends' withWriteTx. Inside a transaction callback it runs fn
// directly: nested RunInTransaction self-deadlocks, and the outer journal
// already covers fn's writes.
func (s *FlatFileStore) runAtomic(ctx context.Context, fn func() error) error {
	if s.txOwner.Load() == goroutineID() {
		return fn()
	}
	return s.RunInTransaction(ctx, "", func(storage.Transaction) error { return fn() })
}

// commitTransaction is the flat-file analog of embeddeddolt's post-commit
// StageAndCommit: a successful transaction with a commit message creates one
// git version commit. Without it, cmd/bd's transact() marks the command as
// explicitly committed and PersistentPostRun skips maybeAutoCommit, leaving
// transactional writes uncommitted in the working tree. Mirroring
// embeddeddolt's dirty-table guard, an unchanged tree is a no-op; a store
// outside any git work tree (conformance temp dirs) has no version layer to
// commit to, matching sqlkit's unversioned handling of commitMsg.
func (s *FlatFileStore) commitTransaction(ctx context.Context, commitMsg string) error {
	if commitMsg == "" {
		return nil
	}
	if _, err := s.git(ctx, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil
	}
	out, err := s.git(ctx, "status", "--porcelain", s.beadsPathspec())
	if err != nil {
		return err
	}
	if out == "" {
		return nil
	}
	return s.Commit(ctx, commitMsg)
}

// AddDependencyWithOptions ignores the transaction-scoped options: flat-file
// runs the same cycle check on every insert, so there is no cheaper bulk path
// to opt into. The store has no AddDependencyWithOptions of its own.
func (tx *flatFileTx) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, _ storage.DependencyAddOptions) error {
	return tx.AddDependency(ctx, dep, actor)
}

// GetDependencyRecords reads the issue's dependencies without the store
// method's checkClosed guard — the transaction is only handed out while the
// store is open.
func (tx *flatFileTx) GetDependencyRecords(_ context.Context, issueID string) ([]*types.Dependency, error) {
	issue, err := tx.readIssue(issueID)
	if err != nil {
		return nil, err
	}
	return issue.Dependencies, nil
}

// CycleThroughEdges reports a rendered scheduling cycle through one of the
// new edges, mirroring the SQL backends: the graph is the combined scheduling
// set (blocks/conditional-blocks/parent-child, per
// issueops.AppendSchedulingGraphInTx — 'related' and 'discovered-from' edges
// cannot form scheduling cycles), and rendering is delegated to the shared
// issueops.CycleThroughEdgesInGraph. Flat-file writes land on disk inside the
// transaction, so loadAllIssues already sees the new edges, as the shared
// renderer requires.
func (tx *flatFileTx) CycleThroughEdges(_ context.Context, edges [][2]string) (string, error) {
	if len(edges) == 0 {
		return "", nil
	}
	all, err := tx.loadAllIssues()
	if err != nil {
		return "", err
	}
	return issueops.CycleThroughEdgesInGraph(schedulingAdjacency(all), edges), nil
}

// SetMetadata / GetMetadata mirror the store methods but without the
// checkClosed guard, matching the transaction's other read-your-writes paths.
func (tx *flatFileTx) SetMetadata(_ context.Context, key, value string) error {
	return tx.setKV(tx.configKVPath, metadataKeyPrefix+key, value)
}

func (tx *flatFileTx) GetMetadata(_ context.Context, key string) (string, error) {
	return tx.getKV(tx.configKVPath, metadataKeyPrefix+key)
}

// AddComment writes a structured comment, unlike the store's AddComment which
// records a `commented` audit event.
func (tx *flatFileTx) AddComment(ctx context.Context, issueID, actor, comment string) error {
	_, err := tx.AddIssueComment(ctx, issueID, actor, comment)
	return err
}
