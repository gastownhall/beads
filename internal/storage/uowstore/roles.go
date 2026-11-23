package uowstore

import (
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// The 33 storage.Storage role accessors. Each builds the uow package's role
// over guarded{s}, never over the bare provider:
//
//   - The role's units of work are the provider's own: guarded.NewUOW is the
//     provider's NewUOW, so a notifying or external-dependency provider still
//     decorates every unit of work the role opens, and a guarded delete still
//     finds the provider's fence (guarded answers uow.FenceProvider). That is
//     all a provider's own XSource accessor adds over its constructor: every
//     one in the tree returns uow.NewX over itself.
//   - A role an embedder holds past Close refuses with ErrStoreClosed instead
//     of reaching a provider whose pool is gone.
//
// Roles are never generated stubs: a role the uow package cannot build is a
// compile error here, not a runtime ErrUnsupported. A role runs every call in
// its own unit of work, so it is unaffected by the Store's RunInTransaction:
// roles are not transactional with each other.

// role builds one role over the guard, refusing after Close.
func role[T any](s *Store, build func(uow.UnitOfWorkProvider) (T, error)) (T, error) {
	if _, err := s.provider(); err != nil {
		var zero T
		return zero, err
	}
	return build(guarded{s})
}

func (s *Store) IssueLifecycle() (issueops.Lifecycle, error) { return role(s, uow.NewIssueOperations) }

func (s *Store) IssueReader() (issueops.Reader, error) { return role(s, uow.NewIssueReader) }

func (s *Store) IssueClaimer() (issueops.Claimer, error) { return role(s, uow.NewIssueClaimer) }

func (s *Store) ReadyClaimer() (issueops.ReadyClaimer, error) { return role(s, uow.NewReadyClaimer) }

func (s *Store) BatchCloser() (issueops.BatchCloser, error) { return role(s, uow.NewBatchCloser) }

func (s *Store) BatchCreator() (issueops.BatchCreator, error) { return role(s, uow.NewBatchCreator) }

func (s *Store) DependencyEditor() (issueops.DependencyEditor, error) {
	return role(s, uow.NewDependencyEditor)
}

func (s *Store) Commenter() (issueops.Commenter, error) { return role(s, uow.NewCommenter) }

func (s *Store) Counter() (issueops.Counter, error) { return role(s, uow.NewCounter) }

func (s *Store) StatsReporter() (issueops.StatsReporter, error) { return role(s, uow.NewStatsReporter) }

func (s *Store) IssueRelations() (issueops.Relations, error) { return role(s, uow.NewIssueRelations) }

func (s *Store) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return role(s, uow.NewWorkspaceConfig)
}

func (s *Store) Memories() (memoryops.Memories, error) { return role(s, uow.NewMemories) }

func (s *Store) VersionReconciler() (issueops.VersionReconciler, error) {
	return role(s, uow.NewVersionReconciler)
}

func (s *Store) CycleDetector() (issueops.CycleDetector, error) { return role(s, uow.NewCycleDetector) }

func (s *Store) EdgeReader() (issueops.EdgeReader, error) { return role(s, uow.NewEdgeReader) }

func (s *Store) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	return role(s, uow.NewBlockingAnnotator)
}

func (s *Store) TreeWalker() (issueops.TreeWalker, error) { return role(s, uow.NewTreeWalker) }

func (s *Store) GraphCounter() (issueops.GraphCounter, error) { return role(s, uow.NewGraphCounter) }

func (s *Store) BatchGetter() (issueops.BatchGetter, error) { return role(s, uow.NewBatchGetter) }

func (s *Store) ReadyCounter() (issueops.ReadyCounter, error) { return role(s, uow.NewReadyCounter) }

func (s *Store) Querier() (issueops.Querier, error) { return role(s, uow.NewQuerier) }

func (s *Store) Sweeper() (issueops.Sweeper, error) { return role(s, uow.NewSweeper) }

func (s *Store) Deleter() (issueops.Deleter, error) { return role(s, uow.NewDeleter) }

func (s *Store) Bootstrapper() (issueops.Bootstrapper, error) { return role(s, uow.NewBootstrapper) }

func (s *Store) InitVerifier() (issueops.InitVerifier, error) { return role(s, uow.NewInitVerifier) }

func (s *Store) MetadataCAS() (issueops.MetadataCAS, error) { return role(s, uow.NewMetadataCAS) }

func (s *Store) BatchApplier() (issueops.BatchApplier, error) { return role(s, uow.NewBatchApplier) }

func (s *Store) Releaser() (issueops.Releaser, error) { return role(s, uow.NewReleaser) }

func (s *Store) LeaseReclaimer() (issueops.LeaseReclaimer, error) {
	return role(s, uow.NewLeaseReclaimer)
}

func (s *Store) Heartbeater() (issueops.Heartbeater, error) { return role(s, uow.NewHeartbeater) }

func (s *Store) BlockedLister() (issueops.BlockedLister, error) { return role(s, uow.NewBlockedLister) }

func (s *Store) MoleculeStepper() (issueops.MoleculeStepper, error) {
	return role(s, uow.NewMoleculeStepper)
}
