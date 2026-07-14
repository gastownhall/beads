package postgres

import (
	"github.com/steveyegge/beads/internal/storage"
)

// Regeneration: the postgres shell is the exact complement of *sqlkit.Store's
// method set. Regenerate unsupported_gen.go with the directive below after
// finalizing the skip list (the sqlkit-implemented ops — the integrator fills
// in <ops>). gen's strict unmatched-skip validation then doubles as a drift
// tripwire against DoltStorage interface changes.
//
//go:generate go run ../unsupportedgen -pkg postgres -src .. -out unsupported_gen.go -type DoltStorage -skip AddDependency,AddIssueComment,AddLabel,ClaimIssue,ClaimReadyIssue,Close,CloseIssue,Commit,CommitMergeResolution,CommitPending,CommitWithConfig,CountDependencies,CountDependents,CountEvents,CountIssueComments,CountIssues,CountIssuesByGroup,CreateIssue,CreateIssues,DeleteConfig,DeleteIssue,DeleteIssues,DetectCycles,GetAllConfig,GetBlockedIssues,GetBlockingInfoForIssues,GetCommentCounts,GetCommentsForIssues,GetConfig,GetCustomStatuses,GetCustomStatusesDetailed,GetCustomTypes,GetDependencies,GetDependenciesWithMetadata,GetDependencyCounts,GetDependencyRecords,GetDependencyRecordsForIssues,GetDependencyTree,GetDependents,GetDependentsWithMetadata,GetEpicsEligibleForClosure,GetEvents,GetInfraTypes,GetIssue,GetIssueByExternalRef,GetIssueComments,GetIssuesByIDs,GetIssuesByLabel,GetLabels,GetLabelsForIssues,GetLocalMetadata,GetMetadata,GetNewlyUnblockedByClose,GetNextChildID,GetReadyWork,GetReadyWorkWithCounts,GetStaleIssues,GetStatistics,IsBlocked,IsInfraTypeCtx,IterBlockedIssues,IterDependenciesWithMetadata,IterDependentsWithMetadata,IterEvents,IterIssueComments,IterIssues,IterReadyWork,IterWisps,ListWisps,RemoveDependency,RemoveLabel,ReopenIssue,RunInTransaction,SearchIssues,SearchIssuesWithCounts,SetConfig,SetLocalMetadata,SetMetadata,UpdateIssue,UpdateIssueType,AddComment,ClearRepoMtime,CountDependentsByStatus,DeleteIssuesBySourceRepo,FindWispDependentsRecursive,GetAllDependencyRecords,GetAllEventsSince,GetMoleculeLastActivity,GetMoleculeProgress,GetRepoMtime,ImportIssueComment,IterAllDependencyRecords,IterAllEventsSince,PromoteFromEphemeral,SetRepoMtime,UpdateIssueID,CreateIssuesWithFullOptions,SlotClear,SlotGet,SlotSet,SearchIssueIDs,HeartbeatIssue,ReclaimExpiredLeases,UnclaimIssue

// errUnsupported is the constructor every generated stub in unsupported_gen.go
// calls. It returns the backend-agnostic *storage.ErrUnsupported sentinel, whose
// Error() renders `operation %q not supported by the %s backend`. That message is
// correct for every stubbed op — the shell also stubs non-VC core methods
// (GetStatistics, DeleteIssues, ...) that a Dolt backend is not the remedy for —
// so it carries no history/version-control hint. Backend is the package const
// "postgres"; callers can errors.As/Is on the returned sentinel.
func errUnsupported(op string) error {
	return &storage.ErrUnsupported{Op: op, Backend: Backend}
}
