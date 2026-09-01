package uow

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// NewPreWriteProvider applies synchronous admission at the transaction commit
// boundary used by proxied and HTTP server storage. A rejected commit leaves
// its unit of work uncommitted; Close then rolls it back. Empty commit messages
// are read-only rollbacks and never invoke the gate.
func NewPreWriteProvider(inner UnitOfWorkProvider, gate storage.PreWriteGate) UnitOfWorkProvider {
	if gate == nil || isNilUnitOfWorkProvider(inner) {
		return inner
	}
	return &preWriteProvider{inner: inner, gate: gate}
}

type preWriteProvider struct {
	inner UnitOfWorkProvider
	gate  storage.PreWriteGate
}

func (p *preWriteProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	inner, err := p.inner.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return &preWriteUOW{UnitOfWork: inner, gate: p.gate}, nil
}

func (p *preWriteProvider) Close(ctx context.Context) error { return p.inner.Close(ctx) }

func (p *preWriteProvider) Unwrap() UnitOfWorkProvider { return p.inner }

type preWriteUOW struct {
	UnitOfWork
	gate storage.PreWriteGate
}

func (u *preWriteUOW) Commit(ctx context.Context, message string) error {
	if message != "" {
		if err := u.gate.BeforeWrite(ctx, storage.PreWriteTransaction); err != nil {
			return err
		}
	}
	return u.UnitOfWork.Commit(ctx, message)
}

func (p *preWriteProvider) IssueLifecycle() (issueops.Lifecycle, error) {
	return NewIssueOperations(p)
}

func (p *preWriteProvider) IssueReader() (issueops.Reader, error) { return NewIssueReader(p) }

func (p *preWriteProvider) IssueClaimer() (issueops.Claimer, error) { return NewIssueClaimer(p) }

func (p *preWriteProvider) IssueRelations() (issueops.Relations, error) {
	return NewIssueRelations(p)
}

func (p *preWriteProvider) EdgeReader() (issueops.EdgeReader, error) { return NewEdgeReader(p) }

func (p *preWriteProvider) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	return NewBlockingAnnotator(p)
}

func (p *preWriteProvider) TreeWalker() (issueops.TreeWalker, error) { return NewTreeWalker(p) }

func (p *preWriteProvider) Counter() (issueops.Counter, error) { return NewCounter(p) }

func (p *preWriteProvider) ReadyCounter() (issueops.ReadyCounter, error) {
	return NewReadyCounter(p)
}

func (p *preWriteProvider) ReadyClaimer() (issueops.ReadyClaimer, error) {
	return NewReadyClaimer(p)
}

func (p *preWriteProvider) Querier() (issueops.Querier, error) { return NewQuerier(p) }

func (p *preWriteProvider) StatsReporter() (issueops.StatsReporter, error) {
	return NewStatsReporter(p)
}

func (p *preWriteProvider) CycleDetector() (issueops.CycleDetector, error) {
	return NewCycleDetector(p)
}

func (p *preWriteProvider) Commenter() (issueops.Commenter, error) { return NewCommenter(p) }

func (p *preWriteProvider) BatchCloser() (issueops.BatchCloser, error) { return NewBatchCloser(p) }

func (p *preWriteProvider) BatchCreator() (issueops.BatchCreator, error) {
	return NewBatchCreator(p)
}

func (p *preWriteProvider) DependencyEditor() (issueops.DependencyEditor, error) {
	return NewDependencyEditor(p)
}

func (p *preWriteProvider) Deleter() (issueops.Deleter, error) { return NewDeleter(p) }

func (p *preWriteProvider) Sweeper() (issueops.Sweeper, error) { return NewSweeper(p) }

func (p *preWriteProvider) Importer() (issueops.Importer, error) { return NewImporter(p) }

func (p *preWriteProvider) Bootstrapper() (issueops.Bootstrapper, error) {
	return NewBootstrapper(p)
}

func (p *preWriteProvider) InitVerifier() (issueops.InitVerifier, error) {
	return NewInitVerifier(p)
}

func (p *preWriteProvider) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return NewWorkspaceConfig(p)
}

func (p *preWriteProvider) VersionReconciler() (issueops.VersionReconciler, error) {
	return NewVersionReconciler(p)
}

func (p *preWriteProvider) Memories() (memoryops.Memories, error) { return NewMemories(p) }

func (p *preWriteProvider) EventsJournalCursor() (storage.EventsJournalCursor, error) {
	return NewEventsJournalCursor(p)
}
