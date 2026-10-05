// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/completeness_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"errors"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
)

// legitimatelyUnsupported is the EXPLICIT denominator of every
// storage.DoltStorage method the http client deliberately does not implement —
// design D8's unsupported-allowlist v1 — each with a reason.
//
// TestInterfaceCompleteness asserts the generated shell (unsupported_gen.go)
// contains EXACTLY these methods, no more and no less, so a method cannot
// silently fall into the refusing base; TestUnsupportedContract asserts each one
// really returns the typed sentinel when called. Together they close the loop.
//
// The rule for adding an entry: a method belongs here when no v0 operation can
// answer it. A method that a wire operation CAN answer belongs in accessors.go
// or offrole.go instead, refusing until its bead lands — putting it here would
// bake a refusal into the structural gate and let the flip land silently.
var legitimatelyUnsupported = map[string]string{
	// Version control and history. The commit graph belongs to the server's own
	// storage; a client of an HTTP API has no working set to branch or diff, and
	// v0 publishes no history surface. Note the commit FAMILY is a no-op rather
	// than unsupported (D3) and is deliberately absent from this map.
	"AsOf":             "VC: point-in-time read",
	"Branch":           "VC: Dolt branch",
	"Checkout":         "VC: Dolt checkout",
	"CommitExists":     "VC: commit graph",
	"CurrentBranch":    "VC: Dolt branch",
	"DeleteBranch":     "VC: Dolt branch",
	"Diff":             "VC: Dolt diff",
	"GetConflicts":     "VC: merge conflicts",
	"GetCurrentCommit": "VC: commit graph",
	"History":          "history: issue revision log",
	"ListBranches":     "VC: Dolt branch",
	"Log":              "history: commit log",
	"Merge":            "VC: Dolt merge",
	"ResolveConflicts": "VC: merge conflicts",
	"Status":           "VC: working-set status",

	// Remotes and sync. `bd serve` is the sync boundary: the server pushes and
	// pulls its own database, and a client that did so too would be a second
	// writer nobody designed.
	"AddRemote":    "remote",
	"Fetch":        "remote",
	"ForcePush":    "remote",
	"HasRemote":    "remote",
	"ListRemotes":  "remote",
	"Pull":         "remote",
	"PullFrom":     "remote",
	"PullRemote":   "remote",
	"Push":         "remote",
	"PushRemote":   "remote",
	"PushTo":       "remote",
	"RemoveRemote": "remote",
	"Sync":         "sync: server-side concern",
	"SyncStatus":   "sync: server-side concern",

	// Federation, compaction, provenance and the merge slot: whole subsystems
	// with no v0 route table entry.
	"AddFederationPeer":     "federation",
	"GetFederationPeer":     "federation",
	"ListFederationPeers":   "federation",
	"RemoveFederationPeer":  "federation",
	"ApplyCompaction":       "compaction",
	"CheckEligibility":      "compaction",
	"GetCompactionSnapshot": "compaction",
	"GetTier1Candidates":    "compaction",
	"GetTier2Candidates":    "compaction",
	"RestoreFromSnapshot":   "compaction",
	"SnapshotIssue":         "compaction",
	"GetProvenanceByRef":    "provenance log",
	"GetProvenanceEvents":   "provenance log",
	"RecordProvenanceEvent": "provenance log",
	"MergeSlotAcquire":      "merge slot",
	"MergeSlotCheck":        "merge slot",
	"MergeSlotCreate":       "merge slot",
	"MergeSlotRelease":      "merge slot",

	// The THREE role accessors of D8's matrix with no wire operation behind
	// them. The other twenty-five are wire-backed and live in accessors.go.
	// BatchCloser left this list with the `bd close` decision: the wire has no
	// closeBatch, but the single-item no-ClaimNext shape composes onto
	// closeIssue, and the CLI's close front door routes every id through this
	// role.
	//
	// Releaser, Commenter and IssueRelations left it with client wave 2b
	// (ga-f352s), and all three left for the same reason the rule at the top
	// states: the wire publishes releaseIssue (#5507), addComment (#5594) and
	// listRelatedIssues (#5540), so "no v0 operation can answer it" stopped
	// being true of any of them. What is left here is the residue that is not
	// waiting on anything — three acts a client of an HTTP API cannot perform at
	// all, because each is about a database this process does not have.
	"Bootstrapper":      "issue role: workspace bootstrap is a server-side act",
	"InitVerifier":      "issue role: init verification is a server-side act",
	"VersionReconciler": "issue role: clone-local version markers, meaningless for a client",

	// Raw issue writes. The served writes reach the wire through their roles
	// (Lifecycle, BatchCreator, DependencyEditor, Sweeper, Deleter); these legacy
	// front doors have no operation of their own and must not be silently
	// re-routed, since their request shapes carry members the wire excludes.
	// The two raw comment appends. The wire HAS addComment (#5594) and the
	// COMMENTER ROLE now dials it (commenter.go); these stay unwired for
	// CreateIssue's rule, and the difference between them is worth keeping: one
	// of the two cannot express the operation's answer at all.
	"AddComment":                  "write: raw comment append returning NOTHING; addComment answers with the stored row — the id the insert minted and created_at at the column's precision — and a front door that discarded it would make the role's cursor promise unobservable (role: Commenter)",
	"AddIssueComment":             "write: raw comment append (role: Commenter). It has the same shape as the role's request and the same answer, which is exactly why re-routing it would be a second spelling of one write — with no actor rule and no plane story of its own",
	"AddDependency":               "write: raw edge add (role: DependencyEditor)",
	"AddDependencyWithOptions":    "write: raw edge add (role: DependencyEditor)",
	"AddLabel":                    "write: raw incremental label add (role: Lifecycle, via IssuePatch.Labels.Add). The wire CARRIES it — upstream #5510 published add_labels and client wave ga-jpywb emits it — so this stays on the allowlist for CreateIssue's rule and not for a wire gap: re-routing it would put a second spelling of the edit beside the role's, with no actor, no guard and no ordered-edit algebra",
	"ClaimIssue":                  "write: raw claim (role: Claimer)",
	"ClaimReadyIssue":             "write: raw ready-claim (role: ReadyClaimer)",
	"CloseIssueChecked":           "write: raw checked close (role: Lifecycle)",
	"CreateIssue":                 "write: raw create. The wire has createIssue (upstream #5483) and the LIFECYCLE ROLE now dials it (lifecycle.go, D8 row 16); this raw front door stays unwired deliberately, because re-routing it would put a second spelling of the create beside the role's — with a different request shape, since this one takes a bare *types.Issue and no edges",
	"CreateIssues":                "write: raw batch create; createIssue is single-item and batchCreateIssues is the batch operation, both of which the roles own (role: BatchCreator)",
	"CreateIssuesWithFullOptions": "write: raw batch create with options the wire's item body does not publish (role: BatchCreator; see W-BatchCreateItem.Issue)",
	"DeleteIssue":                 "write: raw delete (role: Deleter)",
	"DeleteIssues":                "write: raw delete (role: Deleter)",
	"DeleteIssuesBySourceRepo":    "write: no source-repo delete operation",
	"ImportIssueComment":          "write: no comment-IMPORT operation. It is not AddIssueComment with a flag — it takes the created_at the comment is to be STORED with, and addComment publishes no such member deliberately (a stored time the caller supplied makes the row disagree with the entry that records it, which is createIssue's own argument about the creation stamp)",
	"PromoteFromEphemeral":        "write: no wisp-promotion operation",
	"RemoveDependency":            "write: raw edge remove (role: DependencyEditor)",
	"RemoveDependencyWithOptions": "write: raw edge remove (role: DependencyEditor)",
	"RemoveLabel":                 "write: raw incremental label remove (role: Lifecycle, via IssuePatch.Labels.Remove); AddLabel's reason exactly, on the other half of the same published pair",
	"ReopenIssue":                 "write: raw reopen (role: Lifecycle)",
	"UpdateIssue":                 "write: raw update (role: Lifecycle)",
	"UpdateIssueChecked":          "write: raw checked update (role: Lifecycle)",
	"UpdateIssueID":               "write: no id-rewrite operation",
	"UpdateIssueType":             "write: raw type rewrite (role: Lifecycle, via IssuePatch.IssueType). It said \"no type-rewrite operation\", and the wire has one: updateIssue publishes patch.issue_type (internal/httpapi/update.go) and this client EMITS it (lifecycle.go). So this is AddLabel's reason and not a wire gap — a raw front door beside a served role, kept unwired because re-routing it would put a second spelling of the edit beside the role's, with no actor and none of the patch algebra",

	// Leases. ClaimRequest carries only an actor; there is no lease vocabulary
	// on the wire at all.
	//
	// THE TWO UNCLAIMS ARE NO LONGER PART OF THAT SENTENCE, and the split
	// matters because it is the reason `bd unclaim` still refuses. The wire
	// publishes releaseIssue (#5507) and the RELEASER ROLE dials it
	// (releaser.go), including the compare-and-set on the holder that is exactly
	// UnclaimIssueIfAssignee's question — so these are ordinary raw front doors
	// now, kept unwired for CreateIssue's rule rather than for the absence of an
	// operation. What has not moved is the LEASE row the release deletes: the
	// role owns that as part of its post-state, and neither of these front doors
	// can say anything about it, which is why the heartbeat and the reaper above
	// keep the original reason.
	"HeartbeatIssue":         "lease: no wire vocabulary",
	"ReclaimExpiredLeases":   "lease: no wire vocabulary",
	"UnclaimIssue":           "write: raw release, force flag and all (role: Releaser). Re-routing it would put a second spelling of the transition beside the role's, with a bare error where the role answers the post-state row and its reminted version",
	"UnclaimIssueIfAssignee": "write: raw conditional release (role: Releaser, via ReleaseRequest.ExpectedAssignee). Its expectation is a STRING where the role's is a POINTER, so it cannot express the one distinction the role's guard is built on — absent means 'do not check' and empty is a refusal, and this signature spells both as \"\"",

	// Config and metadata writes.
	//
	// SETTINGS ARE NO LONGER READ-ONLY over v0 — #5596 published both write
	// operations and client wave ga-jpywb dials them — so these two entries keep
	// their place for the reason UnclaimIssue and MergeMetadata keep theirs
	// rather than for the reason they were written: they are the RAW front doors
	// beside a served role. Re-routing them here would put a second spelling of
	// the edit beside issueops.WorkspaceConfig's, with a different signature and
	// none of the role's guards — SetConfig takes any key, the protected one
	// included, and DeleteConfig reports nothing about what it removed.
	//
	// The two metadata entries are about DIFFERENT PLANES, which is why they no
	// longer share a reason. SetMetadata writes the WORKSPACE's own key-value
	// table — import hashes, `_project_id` — and no v0 operation touches it at
	// all; the compare-and-set upstream added (#5473) is per-ISSUE metadata and
	// answers a different question entirely.
	//
	// MergeMetadata is a raw per-issue write, and the wire CAN express what it
	// does now that client wave ga-7i6by carries `patch.metadata`: a merge is
	// MetadataPatch.Merge through Lifecycle.Update. It stays on the allowlist
	// for the reason every other raw front door does — re-routing it here would
	// put a second spelling of the edit beside the role's, with a different
	// request shape and no actor rule — not because the wire cannot carry it.
	"DeleteConfig":  "write: raw settings remove (role: WorkspaceConfig, via UnsetSetting)",
	"SetConfig":     "write: raw settings write, protected key and all (role: WorkspaceConfig, via SetSetting). Its signature carries no guard and no per-key rule, so re-routing it would be a second door onto the plane that walks past the one issueops.SettingKeyIssuePrefix refusal exists for",
	"SetMetadata":   "write: the WORKSPACE metadata table has no wire operation at all; compareAndSetMetadata is the per-ISSUE plane (role: MetadataCAS)",
	"MergeMetadata": "write: raw per-issue metadata merge (role: Lifecycle, via IssuePatch.Metadata.Merge)",

	// Repo mtime and merge slots: clone-local bookkeeping for a local database.
	"ClearRepoMtime": "clone-local bookkeeping",
	"GetRepoMtime":   "clone-local bookkeeping",
	"SetRepoMtime":   "clone-local bookkeeping",
	"SlotClear":      "clone-local bookkeeping",
	"SlotGet":        "clone-local bookkeeping",
	"SlotSet":        "clone-local bookkeeping",

	// Counts. `getStats` answers the aggregate probes, and the two counts
	// operations the wave-2 wire published are answered by ROLES — Counter over
	// issues.count and GraphCounter over dependencies.count. These are the raw
	// front doors beside them, and the split is no longer "no operation" for
	// most of the list:
	//
	//   - the four edge counts and the two issue counts stay unwired for the
	//     rule every other raw front door here follows (CreateIssue's, above):
	//     re-routing them would put a second spelling of the question beside the
	//     role's, with a WIDER request shape. CountIssues takes a free-text
	//     query and a whole types.IssueFilter — SkipWisps, IsTemplate, Pinned,
	//     ExcludeTypes and twenty more members issueops.CountRequest deliberately
	//     does not have — so a re-route would silently drop them, which is the
	//     one thing a count must never do;
	//   - CountDependentRecords is different and genuinely inexpressible: it is
	//     a DISTINCT count of edge rows, and the role's count is a SUM over the
	//     two dependency planes. issueops.AnchorEdgeCount.Count says so and says
	//     the two differ exactly where one row id lives in both tables;
	//   - CountEvents and CountIssueComments still have no operation at all.
	"CountDependencies":       "count: raw per-issue dependency count (role: GraphCounter, direction out)",
	"CountDependentRecords":   "count: a DISTINCT count of edge rows; the wire's count is a SUM across the two dependency planes and the two differ on a cross-table id collision",
	"CountDependents":         "count: raw per-issue dependent count (role: GraphCounter, direction in)",
	"CountDependentsByStatus": "count: raw per-issue dependent count narrowed by status (role: GraphCounter, direction in plus Status)",
	"CountEvents":             "count: no counts operation",
	"CountIssueComments":      "count: no counts operation",
	"CountIssues":             "count: raw issue count taking a free-text query and a whole types.IssueFilter, whose members the count operation does not publish (role: Counter)",
	"CountIssuesByGroup":      "count: raw grouped issue count over the same wider filter (role: Counter)",

	// THERE ARE TWO EVENT PLANES AND THIS BLOCK IS ABOUT ONE OF THEM, which is
	// the disambiguation the entries below used to leave unstated. "events: no
	// wire surface" read as false the day GET /v0/beads/events landed and this
	// client started reading it (journal.go), and it was never about that plane
	// at all — the same shape SetMetadata's entry has to spell out for the two
	// metadata planes.
	//
	// The five methods here are the AUDIT plane: storage.DoltStorage's own
	// types.Event log, read by time or by issue id. No v0 operation touches it.
	// The DURABLE MUTATION JOURNAL is a different question with a different
	// record type, and this client serves it — journalops.Journal is not on
	// storage.DoltStorage, so it sits on *Store as a type assertion a caller
	// makes rather than as an accessor, and it is out of this allowlist's
	// denominator entirely.
	"EventsSince":        "events (AUDIT plane): no wire surface for the types.Event log; the mutation JOURNAL is journalops.Journal over GET /v0/beads/events and is served",
	"GetAllEventsSince":  "events (AUDIT plane): no wire surface; see EventsSince",
	"GetEvents":          "events (AUDIT plane): no wire surface; see EventsSince",
	"IterAllEventsSince": "events (AUDIT plane): no wire surface, and streaming has no wire shape either; see EventsSince",
	"IterEvents":         "events (AUDIT plane): no wire surface, and streaming has no wire shape either; see EventsSince",

	// Dependency and blocked-set raw reads. The served dependency surface is
	// anchored (listDependencies takes >=1 issue_id) and role-shaped; these are
	// the unanchored or off-role shapes. `bd blocked` and `bd ready --explain`
	// refuse on these, which is why they are named in D7's refused class.
	"DetectCycles":                 "raw read: role CycleDetector serves this shape",
	"FindWispDependentsRecursive":  "raw read: no recursive-dependents operation",
	"GetAllDependencyRecords":      "raw read: listDependencies is anchored (L8, `bd list --deps`)",
	"GetBlockedIssues":             "raw read: no blocked-set operation (`bd blocked`)",
	"GetBlockingInfoForIssues":     "raw read: role BlockingAnnotator serves this shape",
	"GetDependencies":              "raw read: role EdgeReader serves this shape",
	"GetDependencyCounts":          "raw read: the paired blocks-only batch, expressible as two GraphCounter calls with Types set to the blocking type — the role's own leaf declines to name it as a third method, and re-routing it here would invent the map-of-two-numbers shape no wire operation carries",
	"GetDependencyTree":            "raw read: role TreeWalker serves this shape",
	"GetDependentRecords":          "raw read: no dependents operation off getIssue",
	"GetDependentRecordsForIssues": "raw read: no multi-id dependents operation",
	"GetDependents":                "raw read: no dependents operation off getIssue",
	"IsBlocked":                    "raw read: role BlockingAnnotator serves this shape",
	"IsBlockedBatch":               "raw read: role BlockingAnnotator serves this shape",
	"IterAllDependencyRecords":     "raw read: streaming has no wire shape",
	"IterDependenciesWithMetadata": "raw read: streaming has no wire shape",
	"IterDependentsWithMetadata":   "raw read: streaming has no wire shape",

	// Comment reads beyond getIssue's include_comments.
	"GetCommentCounts":     "raw read: no comment-count operation",
	"GetCommentsForIssues": "raw read: comments ride include_comments, one issue at a time",
	"GetIssueCommentsPage": "raw read: no comment paging operation",
	"IterIssueComments":    "raw read: streaming has no wire shape",

	// Issue reads with no wire mapping.
	"GetEpicsEligibleForClosure": "raw read: no epic-eligibility operation",
	"GetIssueByExternalRef":      "raw read: no external-ref lookup operation",
	"GetIssuesByIDs":             "raw read: multi-id fan-out belongs to the SearchIssues IDs shape",
	"GetIssuesByLabel":           "raw read: role Reader serves the label filter (ListRequest.Labels). It said \"no label-filtered operation\", and listIssues publishes label, label_any and exclude_label — the encoder sends all three. What is true is that Reader.List does not answer THIS question: this method unions the durable and wisp planes with no status, type or plane suppression at all and its own priority/created order, where the listing answers the durable plane under workapi's default suppressions. Re-routing it would be a second spelling that returns a different set",
	"GetLabelsForIssues":         "raw read: no multi-id label operation",
	"GetNewlyUnblockedByClose":   "raw read: no unblocked-by-close operation",
	"GetNextChildID":             "raw read: id allocation is a server-side act",
	"GetStaleIssues":             "raw read: no staleness operation",
	"GetStatisticsNoBlocked":     "raw read: getStats has no blocked-suppression variant",
	"SearchIssueIDs":             "raw read: no partial-id search operation (D11)",
	"SearchIssuesWithCounts":     "raw read: no counts-bearing search operation",

	// Streaming iterators. Every wire read is a page, not a stream.
	"IterBlockedIssues": "raw read: streaming has no wire shape",
	"IterIssues":        "raw read: streaming has no wire shape",
	"IterReadyWork":     "raw read: streaming has no wire shape",
	"IterWisps":         "raw read: streaming has no wire shape",
	"ListWisps":         "raw read: no operation answers the EPHEMERAL PLANE ALONE. listIssues' include_ephemeral admits that plane into a merged listing (which is why L1 retired), and a merged page is not this method's answer",

	// Molecule progress reads.
	"GetMoleculeLastActivity": "raw read: no molecule operation",
	"GetMoleculeProgress":     "raw read: no molecule operation",

	// Transactions. A multi-statement transaction cannot span HTTP requests;
	// each wire write is its own transaction server-side.
	"RunInTransaction":               "transaction: cannot span requests",
	"RunInIssueLifecycleTransaction": "transaction: cannot span requests",
}

var shellMethodRe = regexp.MustCompile(`func \(unsupportedDoltStorage\) ([A-Za-z0-9]+)\(`)

// TestInterfaceCompleteness is the STRUCTURAL half: the generated refusing shell
// must equal the allowlist exactly. A method that drifts into the shell without
// a reason is a silent capability loss; an allowlist entry no longer in the
// shell is a stale justification for something now implemented.
func TestInterfaceCompleteness(t *testing.T) {
	data, err := os.ReadFile("unsupported_gen.go")
	if err != nil {
		t.Fatalf("read unsupported_gen.go: %v", err)
	}
	shell := map[string]bool{}
	for _, m := range shellMethodRe.FindAllStringSubmatch(string(data), -1) {
		shell[m[1]] = true
	}
	if len(shell) == 0 {
		t.Fatal("parsed 0 shell methods — regex or file drift")
	}

	var names []string
	for name := range shell {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := legitimatelyUnsupported[name]; !ok {
			t.Errorf("method %q resolves to the typed-unsupported shell but is NOT in legitimatelyUnsupported: "+
				"implement it (and add it to the go:generate skip list), or add it here with a reason", name)
		}
	}

	names = names[:0]
	for name := range legitimatelyUnsupported {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !shell[name] {
			t.Errorf("method %q is in legitimatelyUnsupported but no longer in the shell (implemented?): "+
				"remove the allowlist entry", name)
		}
	}
}

// TestUnsupportedContract is the BEHAVIORAL half: every allowlisted method must
// really return a typed *storage.ErrUnsupported naming itself. DB-free and
// network-free — the generated stubs ignore their receiver, so a zero-value
// store answers them.
func TestUnsupportedContract(t *testing.T) {
	conformance.RunUnsupportedContract(t, &Store{}, legitimatelyUnsupported)
}

// wireBackedAccessors is design D8's matrix, spelled as code: the role accessors
// with a v0 operation behind them.
//
// THE THREE QUALIFIERS THIS LINE CARRIED ARE DOWN TO ONE. It read "one partial —
// WorkspaceConfig, whose write verbs have no operation — and two composed,
// ReadyClaimer and BatchCloser", and client wave ga-jpywb falsified two of the
// three clauses. WorkspaceConfig is no longer partial: #5596 published both
// write verbs and the accessor dials them, so the partial column is EMPTY.
// ReadyClaimer is no longer composed in the sense this line meant it: it dials
// claimNext (#5510) and keeps the composition only as a capability-gated
// down-level leg, which is a fallback rather than the way the role is served.
// BatchCloser is the last one, and it has the same shape for the same reason —
// issues.batchClose where advertised, a single closeIssue where not.
//
// So the qualifier that survives is "one accessor whose down-level leg is a
// composition, and one more beside it", which is a statement about SERVER AGE
// rather than about the wire's surface. The distinction is worth keeping
// separate from the count below: every name here has a v0 operation behind it
// today, and that was not true when the list was written.
//
// IssueLifecycle left the partial column with client wave ga-jbuyf: createIssue
// was the operation its Create half had nothing to dial, and upstream #5483
// published it.
//
// The design page records seventeen because its row 17 was PENDING-DECISION when
// it was written. The decision recorded on ga-141ra moved BatchCloser across:
// `bd close` routes every id, single-id included, through CloseBatch, so leaving
// it refused left an http workspace with no close path at all while the server
// advertised issues.close to nobody.
//
// MetadataCAS and BatchApplier are the two the upstream sync added
// (compareAndSetMetadata #5473, applyBatch #5480), and they were the whole
// population of pendingWireBackedAccessors below, which is what that list was
// written for. MetadataCAS left it with client wave ga-7i6by.
// Counter and GraphCounter are the twenty-first and twenty-second, from client
// wave ga-icks1: countIssues (#5508) and dependencies.count (#5536) were both
// published by the wave-2 wire sync and both accessors landed with the same
// wave, so neither spent a PR in pendingWireBackedAccessors below.
//
// Releaser, Commenter and IssueRelations are the twenty-third, twenty-fourth
// and twenty-fifth, from client wave ga-f352s. All three had been published
// upstream ahead of this client — releaseIssue #5507, addComment #5594,
// listRelatedIssues #5540 — and all three landed in ONE wave, so none of them
// spent a PR in pendingWireBackedAccessors either. What they have in common is
// the shape of the port rather than the shape of the operation: each maps member
// for member, and what each one cost was a decision about the ANSWER — an
// anonymous post-state, a stored row that must not be discarded, and a
// single-anchor miss that is a 404 rather than a sentinel.
//
// ReadyLister is the twenty-sixth, from upstream's ready-listing role: it dials
// listReadyWork and, when the page does not settle the total, countReadyWork —
// the two operations IssueReader and ReadyCounter already serve.
var wireBackedAccessors = []string{
	"IssueReader", "ReadyCounter", "IssueClaimer", "ReadyClaimer", "Querier",
	"StatsReporter", "CycleDetector", "TreeWalker", "EdgeReader",
	"BlockingAnnotator", "WorkspaceConfig", "Sweeper", "Deleter", "BatchCreator",
	"Memories", "IssueLifecycle", "DependencyEditor", "BatchCloser",
	"MetadataCAS", "BatchApplier", "Counter", "GraphCounter",
	"Releaser", "Commenter", "IssueRelations", "ReadyLister",
}

// refusingAccessors is the complement: the ones with no wire operation at all.
//
// IT NO LONGER HAS A SECOND CLAUSE. The list used to read "or with one this
// client has no wave behind yet", and three of its six names were in that second
// state — Releaser, Commenter and IssueRelations, each with a complete operation
// upstream and no accessor here. Client wave ga-f352s wired all three, so what
// is left is the first clause alone: three acts that are about a database this
// process does not have, which no wire operation will ever answer for a client.
var refusingAccessors = []string{
	"VersionReconciler", "Bootstrapper", "InitVerifier",
}

// pendingWireBackedAccessors are the wire-backed rows of D8's matrix whose role
// is not wired YET: the server advertises the operation, but the accessor still
// refuses through (*Store).unsupported until its role bead lands. They are a
// subset of wireBackedAccessors, split out so the behavioral probe can demand
// that every OTHER wire-backed accessor already returns a live role while these
// still refuse — which is what forces a flip to move the name across in the same
// PR as the wiring.
//
// IT WAS EMPTY, and the case it was kept for arrived: "a v0 operation landing
// upstream tomorrow gets an accessor that refuses for exactly one PR, and this
// is the list that says so out loud." Two did — compareAndSetMetadata (#5473)
// and applyBatch (#5480) — and their accessors refused in accessors.go until
// the ports landed. MetadataCAS's did, with client wave ga-7i6by, and moving
// its name out of this list was not optional: the probe below fails the day a
// pending role starts answering, which is the flip rule enforced behaviorally.
//
// Leaving these OFF the matrix entirely was the alternative, and it is the one
// that hides them: an accessor named nowhere is an accessor no gate reads, so
// nothing would have failed on the day one was wired without review.
// IT IS EMPTY AGAIN, and that is the list working rather than the list being
// pointless: both names that entered it have left it through the flip rule the
// probe below enforces — MetadataCAS with client wave ga-7i6by, BatchApplier
// with ga-mijra — and the next operation the wire publishes ahead of its client
// wave lands here for exactly one PR.
var pendingWireBackedAccessors = []string{}

// TestAccessorMatrixMatchesDesign pins the 26/3 split against the allowlist so a
// role accessor cannot change sides without the design row changing with it.
//
// The numbers are the LIST's, not a literal: the design page recorded 20/8 when
// it was written and five accessors have crossed since (Counter and GraphCounter
// with ga-icks1; Releaser, Commenter and IssueRelations with ga-f352s). The
// three that remain refusing are permanently unservable, so this ratio only
// moves again if the tier itself grows.
// This is the capability-matrix flip rule applied to the store: a refuse→served
// flip lands in the same PR as the method that serves it.
func TestAccessorMatrixMatchesDesign(t *testing.T) {
	if got, want := len(wireBackedAccessors)+len(refusingAccessors), 29; got != want {
		t.Fatalf("the matrix covers %d accessors, want %d", got, want)
	}
	for _, name := range wireBackedAccessors {
		if reason, ok := legitimatelyUnsupported[name]; ok {
			t.Errorf("%q is wire-backed in D8 but sits on the unsupported allowlist (%q): "+
				"it belongs in accessors.go, refusing until its role bead lands", name, reason)
		}
	}
	for _, name := range refusingAccessors {
		if _, ok := legitimatelyUnsupported[name]; !ok {
			t.Errorf("%q refuses in D8 but is not on the unsupported allowlist", name)
		}
	}
}

// TestWireBackedAccessorsAreBehaviorallyWired is the behavioral half of the
// matrix. TestAccessorMatrixMatchesDesign pins the split STRUCTURALLY — which
// names sit on the unsupported allowlist — but cannot tell a wired accessor from
// one still handing back (*Store).unsupported, because both compile and both
// satisfy the interface. This one CALLS each accessor: a wired one must not
// return the typed sentinel for a valid call, and a pending one must. So 'wired'
// comes to MEAN 'does not refuse', and the day a pending role is wired this test
// fails until its name moves out of pendingWireBackedAccessors — the flip rule,
// enforced behaviorally.
func TestWireBackedAccessorsAreBehaviorallyWired(t *testing.T) {
	pending := map[string]bool{}
	for _, name := range pendingWireBackedAccessors {
		if !slices.Contains(wireBackedAccessors, name) {
			t.Errorf("%q is listed pending but is not a wire-backed accessor", name)
		}
		pending[name] = true
	}

	// A store with a transport, so the accessors that narrow it (roleWire) build
	// their role rather than reporting the no-transport build fault. Constructing a
	// role dials nothing, and neither does (*Store).unsupported, so this stays a
	// server-free smoke.
	s := New(testTarget(t), &fakeWire{res: &apigen.ContextResponse{}}, nil)

	for _, name := range wireBackedAccessors {
		method := reflect.ValueOf(s).MethodByName(name)
		if !method.IsValid() {
			t.Errorf("%q is not a method on *Store", name)
			continue
		}
		out := method.Call(nil)
		if len(out) != 2 {
			t.Errorf("%s returns %d values, want (role, error)", name, len(out))
			continue
		}
		err, _ := out[1].Interface().(error)

		var unsup *storage.ErrUnsupported
		refused := err != nil && errors.As(err, &unsup)
		switch {
		case pending[name]:
			if !refused {
				t.Errorf("%s is listed pending but does NOT return the typed sentinel (err=%v): "+
					"its role is wired — move it out of pendingWireBackedAccessors", name, err)
			}
		case refused:
			t.Errorf("%s is wire-backed but still returns the typed sentinel (%v): "+
				"wire its role, or list it in pendingWireBackedAccessors", name, err)
		case err != nil:
			t.Errorf("%s returned a non-sentinel error on a valid call: %v", name, err)
		}
	}
}
