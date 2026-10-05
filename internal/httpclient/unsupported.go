// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/unsupported.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

// Regeneration: the http shell is the exact complement of this package's
// hand-written method set, and the skip list below is design D8's allowlist v1
// spelled as method names — the wire-backed role accessors
// (accessors.go, which counts them), the off-role raw methods with a v0 mapping (offrole.go and
// lifecycle.go's CloseIssue), the commit family and Close (store.go), and the
// metadata trio (metadata.go).
//
// Never hand-edit unsupported_gen.go. Regenerate with `go generate ./...` after
// changing the skip list; gen's strict unmatched-skip validation then doubles as
// a drift tripwire against DoltStorage interface changes, and the regen-
// idempotence gate in internal/storage/unsupportedgen byte-compares the result.
//
// -src and the tool path are two levels up because this package sits under
// internal/enterprise rather than beside internal/storage.
//
//go:generate go run ../../storage/unsupportedgen -pkg httpstore -src ../../storage -out unsupported_gen.go -type DoltStorage -skip BatchApplier,BatchCloser,BatchCreator,BlockingAnnotator,Close,CloseIssue,Commenter,Commit,CommitAll,CommitMergeResolution,CommitPending,CommitWithConfig,Counter,CycleDetector,Deleter,DependencyEditor,EdgeReader,GetAllConfig,GetConfig,GetCustomStatuses,GetCustomStatusesDetailed,GetCustomTypes,GetDependenciesWithMetadata,GetDependencyRecords,GetDependencyRecordsForIssues,GetDependentsWithMetadata,GetInfraTypes,GetIssue,GetIssueComments,GetLabels,GetLocalMetadata,GetMetadata,GetReadyWork,GetReadyWorkWithCounts,GetReadyWorkWithCountsAndTotal,GetStatistics,GraphCounter,IsInfraTypeCtx,IssueClaimer,IssueLifecycle,IssueReader,IssueRelations,Memories,MetadataCAS,Querier,ReadyClaimer,ReadyCounter,ReadyLister,Releaser,SearchIssues,SetLocalMetadata,StatsReporter,Sweeper,TreeWalker,WorkspaceConfig
