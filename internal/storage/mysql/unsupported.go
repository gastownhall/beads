package mysql

import (
	"github.com/steveyegge/beads/internal/storage"
)

// Regeneration mirrors postgres: the mysql shell is the exact complement of
// *sqlkit.Store's method set (identical base + identical Commit/CommitGraph overrides,
// so the complement is identical to postgres's shell modulo package name).
//
//go:generate go run ../unsupportedgen -pkg mysql -src .. -out unsupported_gen.go -type DoltStorage -skip AddDependency,AddIssueComment,AddLabel,ClaimIssue,ClaimReadyIssue,Close,CloseIssue,Commit,CommitMergeResolution,CommitPending,CommitWithConfig,CountDependencies,CountDependents,CountEvents,CountIssueComments,CountIssues,CountIssuesByGroup,CreateIssue,CreateIssues,DeleteConfig,DeleteIssue,DeleteIssues,DetectCycles,GetAllConfig,GetBlockedIssues,GetBlockingInfoForIssues,GetCommentCounts,GetCommentsForIssues,GetConfig,GetCustomStatuses,GetCustomStatusesDetailed,GetCustomTypes,GetDependencies,GetDependenciesWithMetadata,GetDependencyCounts,GetDependencyRecords,GetDependencyRecordsForIssues,GetDependencyTree,GetDependents,GetDependentsWithMetadata,GetEpicsEligibleForClosure,GetEvents,GetInfraTypes,GetIssue,GetIssueByExternalRef,GetIssueComments,GetIssuesByIDs,GetIssuesByLabel,GetLabels,GetLabelsForIssues,GetLocalMetadata,GetMetadata,GetNewlyUnblockedByClose,GetNextChildID,GetReadyWork,GetReadyWorkWithCounts,GetStaleIssues,GetStatistics,IsBlocked,IsInfraTypeCtx,IterBlockedIssues,IterDependenciesWithMetadata,IterDependentsWithMetadata,IterEvents,IterIssueComments,IterIssues,IterReadyWork,IterWisps,ListWisps,RemoveDependency,RemoveLabel,ReopenIssue,RunInTransaction,SearchIssues,SearchIssuesWithCounts,SetConfig,SetLocalMetadata,SetMetadata,UpdateIssue,UpdateIssueType,AddComment,ClearRepoMtime,CountDependentsByStatus,DeleteIssuesBySourceRepo,FindWispDependentsRecursive,GetAllDependencyRecords,GetAllEventsSince,GetMoleculeLastActivity,GetMoleculeProgress,GetRepoMtime,ImportIssueComment,IterAllDependencyRecords,IterAllEventsSince,PromoteFromEphemeral,SetRepoMtime,UpdateIssueID,CreateIssuesWithFullOptions,SlotClear,SlotGet,SlotSet,SearchIssueIDs,HeartbeatIssue,ReclaimExpiredLeases,UnclaimIssue

// errUnsupported is the constructor every generated stub in unsupported_gen.go calls.
// It returns the backend-agnostic *storage.ErrUnsupported sentinel; Backend is "mysql".
func errUnsupported(op string) error {
	return &storage.ErrUnsupported{Op: op, Backend: Backend}
}
