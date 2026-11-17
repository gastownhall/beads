package issueops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// The emit helpers a function calls to journal a row directly.
var journalEmitHelpers = map[string]bool{
	"RecordEventInTx":    true,
	"RecordDeleteInTx":   true,
	"RecordDepEventInTx": true,
	"insertEventRow":     true,
}

// derivedReadinessEmitters journal, but only the readiness flips of AFFECTED
// NEIGHBORS — never the caller's own change. Almost every mutator ends with a
// recompute, so propagating "emits" through them would make the guard vacuous:
// a mutator could stop recording itself entirely and still pass. Emission
// therefore does not travel across a call into one of these.
var derivedReadinessEmitters = map[string]bool{
	"RecomputeIsBlockedInTx":           true,
	"RecomputeIsBlockedInTxWithResult": true,
	"RecomputeIsBlockedForIDsInTx":     true,
	"RecomputeIsBlockedForWispIDsInTx": true,
	"RecomputeIsBlockedAfterMergeInTx": true,
	"RecomputeAllIsBlockedInTx":        true,
	// The unexported members of the same family — listed so the intra-family
	// chain stays connected AND so a mutator cannot inherit emission by calling
	// one of them either.
	"recomputeIsBlockedCounting":         true,
	"recomputeIsBlockedInTxWithResult":   true,
	"recomputeIsBlockedAfterMergeScoped": true,
	"recomputeIsBlockedForAll":           true,
	"MarkIsBlockedInTx":                  true,
	"MarkIsBlockedInTxWithResult":        true,
	"recordBlockedJournalChanges":        true,
}

// journalEmitEdges is the call-graph edge set the emits fixpoint follows: every
// call except the ones that leave the readiness family. Inside the family the
// chain stays connected — its members delegate to each other and bottom out in
// recordBlockedJournalChanges — so they are still correctly seen as emitting;
// what does not happen is an ordinary mutator inheriting that emission just by
// finishing with a recompute.
func journalEmitEdges(f *journalscan.FuncInfo) []string {
	all := f.AllCallNames()
	if derivedReadinessEmitters[f.Name] {
		return all
	}
	out := make([]string, 0, len(all))
	for _, name := range all {
		if derivedReadinessEmitters[name] {
			continue
		}
		out = append(out, name)
	}
	return out
}

// mutationEntryPoints are the issueops functions that must result in a journal
// row. Every write plumbing bottoms out in one of these. The structural
// cross-check below (TestEveryBeadMutatorJournalsOrIsExempt) guarantees this
// list stays complete: any exported function that writes a work-bead table must
// appear here or in beadDMLExemptions, so a new mutation path cannot be added
// without being accounted for.
var mutationEntryPoints = []string{
	// create
	"CreateIssueInTx",
	"CreateIssueInTxWithResult",
	"CreateIssuesInTx",
	"CreateIssuesInTxWithResult",
	"PersistDependencies",                  // creation-time dependency edges
	"PersistDependenciesWithResult",        // creation-time dependency edges
	"PersistDependenciesWithOptionsResult", // creation-time dependency edges
	// update
	"UpdateIssueInTx",
	"UpdateIssueWithoutEventInTx",
	"MergeMetadataInTx",
	"DeleteMetadataInTx",
	"ApplyLabelPatch",
	"ApplyParentPatch",
	"MoveIssuePersistenceInTx",
	// close / reopen, including the guarded CAS + savepoint path
	"CloseIssueInTx",
	"CloseIssueWithoutEventInTx",
	"CloseIssueCheckedInTx",
	"ReopenIssueInTx",
	// delete, including the role body and the resolved-set worker
	"DeleteIssueInTx",
	"DeleteIssueInTxWithResult",
	"DeleteIssuesInTx",
	"DeleteIssuesBesideWispsInTx",
	"DeleteResolvedSetInTx",
	"DeleteIssuesBySourceRepoInTx",
	"DeleteInTx",
	"RewriteDeletedReferencesInTx",
	// claim / release / lease recovery
	"ClaimIssueInTx",
	"ClaimReadyIssueInTx",
	"UnclaimIssueInTx",
	"UnclaimIssueIfAssigneeInTx",
	// The heartbeat family journals on its RE-ARM path only (a live claim
	// with no lease row the caller holds: row_lock reminted, a lease granted,
	// recorded as a mutation). Its REFRESH path writes only the clone-local
	// leases table and still records nothing; the fast-path return in
	// RenewLeaseInTx says why. The ruling that exempted the whole family
	// changed when the re-arm became a mutation (it must collide with a racing
	// release or close on row_lock), so the family is listed here.
	"RenewLeaseInTx",
	"HeartbeatIssueInTx",
	"ExecuteHeartbeatInTx",
	"ReclaimExpiredLeasesInTx",
	// plane moves, graph edges, labels, comments, renames
	"PromoteFromEphemeralInTx",
	"AddDependencyInTx",
	"RemoveDependencyInTx",
	"AddLabelInTx",
	"RemoveLabelInTx",
	"RenameLabelInTx",
	"UpdateIssueIDInTx",
	"AddIssueCommentInTx",
	"ImportIssueCommentInTx",
	// derived-state maintenance that flips a bead's exported readiness
	"RecomputeIsBlockedInTx",
	"RecomputeIsBlockedInTxWithResult",
	"RecomputeIsBlockedForIDsInTx",
	"RecomputeIsBlockedForWispIDsInTx",
	"RecomputeIsBlockedAfterMergeInTx",
	"MarkIsBlockedInTx",
	"MarkIsBlockedInTxWithResult",
	"RecomputeAllIsBlockedInTx",
	"WakeExpiredDefersInTx",
	"SweepInTx",
	// the public lifecycle surface (roles/facade wave). These delegate to the
	// leaves above; listing them pins the delegation so a role that grows its
	// own DML cannot quietly stop journaling.
	"ExecuteCreate",
	"ExecuteCreateBatch",
	"ExecuteUpdate",
	"ExecuteClose",
	"ExecuteCloseBatch",
	"ExecuteReopen",
	"ExecuteClaim",
	"ExecuteClaimNext",
	"ExecuteAddComment",
	"ExecuteAddDependencies",
	"ExecuteRemoveDependency",
}

// beadDMLExemptions are exported functions the DML detector flags as writing a
// work-bead table but which legitimately do NOT journal, each with a reason.
// They fall into five buckets: (1) derived child-counter maintenance; (2)
// aux-table writers the templated-%s heuristic can't distinguish from a bead
// table (events, child counters); (3) constituent sub-helpers of a
// create/rename/promote/delete whose top-level entry point journals the whole
// mutation once; (4) compaction maintenance outside the
// create/update/close/delete/dep/label op vocabulary; (5) dual-write
// version-history bookkeeping that runs after the mutation is already
// journaled. The staleness check fails if any stops being flagged, so an
// exemption cannot rot.
var beadDMLExemptions = map[string]string{
	// (1) Child counters are derived CLI acceleration state. In contrast,
	// is_blocked is part of the exported bead snapshot and its recompute helpers
	// structurally journal every value that actually changes.
	"ReconcileChildCounters": "recomputes denormalized child-counter state, not a bead mutation",

	// (2) aux tables matched via templated %s, not work-bead state.
	"RecordEventInTable":            "writes the events audit table (templated %s), not work-bead state",
	"RecordFullEventInTable":        "writes the events audit table (templated %s), not work-bead state",
	"InsertDerivedEvent":            "writes the events audit table (templated %s), not work-bead state",
	"InsertDerivedEventReturningID": "writes the events audit table (templated %s), not work-bead state",
	"GetNextChildIDTx":              "writes the child_counters allocation table (templated %s), not work-bead state",
	"RetireIDsInTx":                 "writes the id tombstone tables (templated %s), not work-bead state; the retaining sweep's delete journals the rows it removes",

	// (3) constituent sub-helpers; the calling entry point journals the whole
	// mutation once (a create/rename/promote/delete emits a single row).
	"InsertIssueIntoTable":                   "raw issue insert; the calling create entry point journals the create",
	"InsertIssueIfNew":                       "raw issue insert; the calling create entry point journals the create",
	"InsertIssueStrictInTx":                  "raw issue insert; the calling create/persistence-move entry point journals it",
	"InsertDerivedComment":                   "raw comment insert; the calling comment/create entry point journals it",
	"PersistLabels":                          "constituent label write of a create; the create entry point journals it",
	"PersistComments":                        "constituent comment write of a create; the create entry point journals it",
	"UpdateWispIDInDependenciesInTx":         "rewrites dep rows during a rename; UpdateIssueIDInTx journals the rename",
	"UpdateIssueIDInDependenciesInTx":        "rewrites dep rows during a rename; UpdateIssueIDInTx journals the rename",
	"RetargetInboundDependenciesToWispInTx":  "rewrites dep rows during promote; PromoteFromEphemeralInTx journals it",
	"RetargetInboundDependenciesToIssueInTx": "rewrites dep rows during promote; PromoteFromEphemeralInTx journals it",
	"DeleteWispFromDependenciesInTx":         "cleans up dep rows during a delete that journals the delete",
	"DeleteWispsFromDependenciesInTx":        "cleans up dep rows during a delete that journals the delete",
	"DeleteWispAuxRowsInTx":                  "cleans up wisp label/event/comment/counter rows during a delete that journals the delete",

	// (4) compaction maintenance — a lossy content rewrite outside the
	// create/update/close/delete/dep/label op vocabulary. Carried from the
	// reference lineage for enterprise shape parity, and it is a KNOWN CONTRACT
	// GAP, not a harmless omission: because these rewrite issue content without
	// emitting, a journal consumer's mirror of that bead goes stale and stays
	// stale until the issue's next journaled mutation delivers a fresh snapshot.
	// Deliberate and open for review — do not read these reasons as saying the
	// omission is invisible to consumers.
	"ApplyCompactionInTx":     "compaction content rewrite outside the op vocabulary; consumer mirrors of this bead go stale until its next journaled mutation (known contract gap, carried from the reference lineage)",
	"RestoreFromSnapshotInTx": "restores a compacted issue outside the op vocabulary; consumer mirrors of this bead go stale until its next journaled mutation (known contract gap, carried from the reference lineage)",

	// (5) dual-write version-history bookkeeping. RecordVersionInTx is called
	// from inside the same mutationEntryPoints functions (the create, update,
	// close, reopen, claim, release, lease-reclaim, defer-wake, label,
	// dependency, promote and persistence-move bodies, and their domain/db
	// equivalents used by the uow leg — version_completeness_test.go pins the
	// set) immediately after those callers already invoke RecordEventInTx, so the
	// mutation itself is already journaled by the time RecordVersionInTx runs.
	// Its own UPDATE issues SET current_revision = ?, updated_at = updated_at
	// only advances a denormalized pointer to match the snapshot row it just
	// inserted into issue_versions (not a bead table, so it can't anchor the
	// exemption on its own), leaving updated_at where the mutation put it —
	// bookkeeping for an already-journaled mutation, not a second mutation
	// that needs its own emit.
	"RecordVersionInTx": "advances the denormalized current_revision pointer to match a snapshot just inserted into issue_versions (not a bead table); called from the same entry points that already journal the mutation via RecordEventInTx, so this is bookkeeping for an already-journaled mutation, not a second one",
	// RecordVersionForCreateInTx is RecordVersionInTx's create-shaped sibling
	// (design §16.2b write-fence, be-h89oq): same entry points, same
	// already-journaled-by-the-caller bookkeeping, plus stamping
	// participation_generation on the row it just inserted into issue_versions.
	"RecordVersionForCreateInTx": "advances the denormalized current_revision pointer and stamps participation_generation to match a snapshot just inserted into issue_versions (not a bead table); called from the same create entry points that already journal the mutation via RecordEventInTx, so this is bookkeeping for an already-journaled mutation, not a second one",
	// CarryParticipationToWispInTx is the demotion half of a plane round
	// trip's participation carry (carryParticipationInTx): it writes only the
	// participation_generation bookkeeping column of the wisps row the demotion
	// just inserted, and the demoting caller (DoltStore.demoteToWispInTx)
	// journals the move itself.
	"CarryParticipationToWispInTx": "writes only the participation_generation bookkeeping column of the wisps row a demotion just inserted; the demoting caller journals the move, so this is bookkeeping for an already-journaled mutation, not a second one",

	// RecordVersionAtInTx (R7.1 as-of read, gastownhall/beads#5898 rev 9,
	// gastownhall/beads#6136) is RecordVersionInTx's test-support
	// twin: it shares RecordVersionInTx's body (recordVersionAtInTx) but skips
	// the versionedHistoryEnabled gate and takes an explicit historical
	// timestamp, so an as-of-read conformance fixture can mint a version row
	// dated in the past. It has no production caller and is not part of any
	// mutationEntryPoints flow — only per-leg AsOfReadFixture.MintAt
	// implementations call it, always directly, never from inside a journaled
	// mutation. There is no separate bead mutation here for it to double-journal:
	// the same "UPDATE issues SET current_revision only advances a pointer to
	// a issue_versions snapshot, not a bead table" reasoning as RecordVersionInTx
	// applies verbatim.
	"RecordVersionAtInTx": "test-support twin of RecordVersionInTx (R7.1 as-of read); mints a historical-timestamped snapshot directly for conformance fixtures, with no production caller and no mutation of its own to journal — its UPDATE issues SET current_revision only advances a pointer to the issue_versions row it just inserted (not a bead table)",
}

// journalExemptMutations are mutation paths that deliberately do NOT journal
// and that the DML detector cannot speak to, because the table they write is
// not a work-bead table. beadDMLExemptions cannot hold them: its staleness
// check requires every entry to still be flagged as a bead mutator, and these
// never are. TestExemptMutationsDoNotJournal pins the decision from the other
// side — each must exist and must NOT emit — so re-adding an emit fails loudly
// instead of silently reversing the ruling.
var journalExemptMutations = map[string]string{}

// TestEveryMutationFunctionJournals parses this package's source, builds the
// intra-package call graph, and asserts every mutation entry point either
// records a journal row directly (calls one of the Record*InTx emit helpers) or
// calls a function that transitively does.
//
// This kills the enumeration-drift class that sank the decorator design: there,
// coverage was a hand-maintained list of overridden methods, and new mutation
// paths silently slipped through. Here, if a listed mutation function stops
// emitting — directly or through its delegates — this test fails.
func TestEveryMutationFunctionJournals(t *testing.T) {
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}

	emits := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(journalEmitHelpers) },
		journalEmitEdges)

	for _, entry := range mutationEntryPoints {
		if _, defined := fns[entry]; !defined {
			t.Errorf("mutation entry point %q not found in issueops — was it renamed? update mutationEntryPoints", entry)
			continue
		}
		if !emits[entry] {
			t.Errorf("mutation entry point %q does not journal: it neither calls a Record*InTx emit helper nor a function that transitively does", entry)
		}
	}
}

// TestExemptMutationsDoNotJournal is the inverse guard. A deliberate decision
// NOT to journal is as much a contract as a decision to journal, and nothing
// else in this file can defend it: the DML detector never flags these functions,
// so they would otherwise drift back to emitting with no test noticing. Each
// entry must still exist (a rename invalidates the ruling) and must still be
// silent (an added emit reverses it).
func TestExemptMutationsDoNotJournal(t *testing.T) {
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}

	emits := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(journalEmitHelpers) },
		journalEmitEdges)

	for name, reason := range journalExemptMutations {
		if reason == "" {
			t.Errorf("%s has an empty exemption reason", name)
		}
		if _, defined := fns[name]; !defined {
			t.Errorf("exempt mutation %q not found in issueops — was it renamed? update journalExemptMutations", name)
			continue
		}
		if emits[name] {
			t.Errorf("exempt mutation %q now journals, reversing a deliberate ruling: %s\n"+
				"If the ruling changed, move it to mutationEntryPoints; otherwise remove the emit.", name, reason)
		}
	}
}

// TestEveryBeadMutatorJournalsOrIsExempt is the STRUCTURAL completeness
// cross-check. It detects, by DML rather than by name, every EXPORTED function
// that writes a work-bead table (INSERT / UPDATE / DELETE against issues, wisps,
// dependencies, labels, comments, and their wisp variants — literal or templated
// table name), and asserts each one journals (calls an emit helper directly or
// transitively) OR is an explicitly-exempted derived-state / aux / sub-helper.
// A new exported mutator that writes a bead table therefore cannot ship without
// either journaling or being justified in beadDMLExemptions, closing the "named
// outside the pattern, silently un-journaled" hole the hand list alone left open.
func TestEveryBeadMutatorJournalsOrIsExempt(t *testing.T) {
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}

	// A function writes a bead table if its own body does, or a free function it
	// calls transitively does.
	beadDML := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.OwnBeadDML },
		func(f *journalscan.FuncInfo) []string { return f.IdentCalls })

	// A function emits if it calls an emit helper directly or transitively.
	emits := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(journalEmitHelpers) },
		journalEmitEdges)

	seenExempt := map[string]bool{}
	var checked int
	for key, f := range fns {
		if f.Recv != "" || !f.Exported || !beadDML[key] {
			continue
		}
		if reason, ok := beadDMLExemptions[f.Name]; ok {
			if reason == "" {
				t.Errorf("%s has an empty exemption reason", f.Name)
			}
			seenExempt[f.Name] = true
			continue
		}
		checked++
		if !emits[key] {
			t.Errorf("exported function %q writes a work-bead table but does not journal (no Record*InTx emit helper directly or transitively) and is not exempted — make it journal, or add it to beadDMLExemptions with a reason", f.Name)
		}
	}

	if checked == 0 {
		t.Fatal("cross-check found no exported bead mutators — DML detection or parsing changed; the guard is not actually running")
	}
	for m := range beadDMLExemptions {
		if !seenExempt[m] {
			t.Errorf("exemption %q no longer matches an exported bead-writing function — remove it", m)
		}
	}
}

// derivedActorParam is, for each member of the readiness family and the
// recheck recorders, the position of its actor argument: the identity every
// derived is_blocked record the call journals (or a post-commit recheck later
// journals) carries.
var derivedActorParam = map[string]int{
	"RecomputeIsBlockedInTx":              4,
	"RecomputeIsBlockedInTxWithResult":    4,
	"MarkIsBlockedInTx":                   4,
	"MarkIsBlockedInTxWithResult":         4,
	"RecomputeIsBlockedForIDsInTx":        3,
	"RecomputeIsBlockedForWispIDsInTx":    3,
	"RecomputeIsBlockedAfterDelete":       4,
	"recomputeIsBlockedInTxWithResult":    4,
	"recordBlockedJournalChanges":         5,
	"NoteStatusChangeBlockedRecheck":      3,
	"NoteDependencyRemovalBlockedRecheck": 3,
	"NoteDeleteBlockedRecheck":            3,
	"noteBlockedRecheck":                  2,
}

// derivedActorSystemCallers are the only functions under internal/storage that
// may hand the readiness family, or a function that forwards its actor to it
// (DeleteIssueInTx and the bulk deletes), NoRequestActor or "": the paths with
// no request behind them and the storage surfaces that take no actor
// parameter, so have none to pass, each with why. Every other caller passes
// the actor of the request whose mutation caused the maintenance.
var derivedActorSystemCallers = map[string]string{
	"issueops/blocked_consistency.go:recomputeIsBlockedCounting":   "the full repair behind `bd recompute-blocked` and `bd doctor --fix`",
	"issueops/blocked_merge.go:recomputeIsBlockedAfterMergeScoped": "a pull's or merge's recompute: the rows came from another clone",
	"issueops/blocked_merge.go:recomputeIsBlockedForAll":           "a pull's or merge's recompute: the rows came from another clone",
	"issueops/bulk_ops.go:DeleteIssuesBySourceRepoInTx":            "`bd repo remove`: storage.DeleteIssuesBySourceRepo carries no actor",
	"dolt/wisps.go:deleteWisp":                                     "the wisp delete surface carries no actor (its delete row names none)",
	"dolt/wisps.go:deleteWispBatchTx":                              "the wisp delete surface carries no actor (its delete rows name none)",
	"createbatchequiv/scenario_waitsfor.go:runWaitsFor":            "an equivalence scenario's raw-SQL repair step, not a request",
	// storage.DeleteIssue, storage.DeleteIssues and storage.Transaction.DeleteIssue
	// take no actor. Their callers are mostly system cleanup (the Decay phase of a
	// local `bd gc`, `bd doctor --fix`), but not only: a local `bd cook` proto
	// delete and the local `bd mol burn`/`bd mol squash` writer reach
	// Transaction.DeleteIssue holding an actor these surfaces cannot carry, so
	// their records name no one until the surfaces take one.
	"dolt/issues.go:deleteIssue":              "storage.DeleteIssue carries no actor (see above)",
	"dolt/issues.go:deleteIssues":             "storage.DeleteIssues carries no actor (see above)",
	"dolt/transaction.go:DeleteIssue":         "storage.Transaction.DeleteIssue carries no actor (see above)",
	"embeddeddolt/store.go:DeleteIssue":       "storage.DeleteIssue carries no actor (see above)",
	"embeddeddolt/store.go:DeleteIssues":      "storage.DeleteIssues carries no actor (see above)",
	"embeddeddolt/transaction.go:DeleteIssue": "storage.Transaction.DeleteIssue carries no actor (see above)",
}

// TestNoRequestActorReachesTheReadinessFamilyOnlyFromSystemPaths is the census
// behind the explicit actor parameter: the parameter makes every caller of
// the readiness family decide attribution at compile time, and this holds
// which callers may decide "no one". A derived record a user's request caused
// but that names no one reads as system/unknown, so a consumer holding every
// record of a user's change to that user fails the change. A new caller that passes
// NoRequestActor or "" fails here until it is listed in
// derivedActorSystemCallers with its reason.
//
// Two ways around a purely syntactic check are closed. A function that hands
// one of its own parameters to an actor position (DeleteIssueInTx passing its
// actor to the recompute it runs) joins the family at that parameter, to a
// fixpoint, so a caller that passes it "" is a caller of the family. And an
// argument that is a local alias of "" or NoRequestActor (a := ""; f(..., a),
// or var a string) counts as passing no actor.
func TestNoRequestActorReachesTheReadinessFamilyOnlyFromSystemPaths(t *testing.T) {
	root := ".."
	type parsedFile struct {
		rel  string
		fset *token.FileSet
		file *ast.File
	}
	var files []parsedFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		files = append(files, parsedFile{rel: filepath.ToSlash(rel), fset: fset, file: file})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	family := derivedActorFamily(func(yield func(*ast.FuncDecl)) {
		for _, f := range files {
			for _, decl := range f.file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
					yield(fn)
				}
			}
		}
	})
	for _, forwarder := range []string{"DeleteIssueInTx", "DeleteIssueInTxWithResult"} {
		if _, ok := family[forwarder]; !ok {
			t.Errorf("%s hands its actor to the readiness family but the forwarding analysis did not find it: "+
				"the census no longer sees callers that reach the family through a forwarder", forwarder)
		}
	}

	seen := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := f.rel + ":" + fn.Name.Name
			empty := emptyActorLocals(fn.Body)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := calleeName(call)
				idx, inFamily := family[name]
				if !inFamily || idx >= len(call.Args) {
					return true
				}
				if !namesNoRequestActor(call.Args[idx], empty) {
					return true
				}
				seen[key] = true
				if _, ok := derivedActorSystemCallers[key]; !ok {
					t.Errorf("%s (%s) passes no request actor to %s: a path a request drives must pass that "+
						"request's actor, so the derived is_blocked records it causes name the user who caused "+
						"them; a genuinely system path passes NoRequestActor and is listed in "+
						"derivedActorSystemCallers with its reason",
						key, f.fset.Position(call.Pos()), name)
				}
				return true
			})
		}
	}
	for key := range derivedActorSystemCallers {
		if !seen[key] {
			t.Errorf("derivedActorSystemCallers lists %s, which no longer passes NoRequestActor: delete the entry", key)
		}
	}
}

// derivedActorFamily is derivedActorParam closed over forwarding: a function
// that passes one of its own parameters, unchanged, to a member's actor
// position is a member at that parameter. Names are matched bare, like the
// census's callee names, so a method and a function sharing a name share an
// entry.
func derivedActorFamily(eachFunc func(yield func(*ast.FuncDecl))) map[string]int {
	family := make(map[string]int, len(derivedActorParam))
	for name, idx := range derivedActorParam {
		family[name] = idx
	}
	for changed := true; changed; {
		changed = false
		eachFunc(func(fn *ast.FuncDecl) {
			if _, member := family[fn.Name.Name]; member {
				return
			}
			params := map[string]int{}
			pos := 0
			for _, field := range fn.Type.Params.List {
				if len(field.Names) == 0 {
					pos++
					continue
				}
				for _, n := range field.Names {
					params[n.Name] = pos
					pos++
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				idx, inFamily := family[calleeName(call)]
				if !inFamily || idx >= len(call.Args) {
					return true
				}
				if id, ok := call.Args[idx].(*ast.Ident); ok {
					if p, isParam := params[id.Name]; isParam {
						if _, member := family[fn.Name.Name]; !member {
							family[fn.Name.Name] = p
							changed = true
						}
					}
				}
				return true
			})
		})
	}
	return family
}

func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	}
	return ""
}

// emptyActorLocals names the locals of body that are ever given no actor: set
// to "" or NoRequestActor, to another such local, or declared `var x string`
// with no value. Any such assignment counts, even one a later branch
// overwrites, so a conditionally empty actor is caught too.
func emptyActorLocals(body *ast.BlockStmt) map[string]bool {
	empty := map[string]bool{}
	for grew := true; grew; {
		grew = false
		mark := func(name string) {
			if name != "_" && !empty[name] {
				empty[name] = true
				grew = true
			}
		}
		ast.Inspect(body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				if len(s.Lhs) != len(s.Rhs) {
					return true
				}
				for i, lhs := range s.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && namesNoRequestActor(s.Rhs[i], empty) {
						mark(id.Name)
					}
				}
			case *ast.ValueSpec:
				if len(s.Values) == 0 {
					if typ, ok := s.Type.(*ast.Ident); ok && typ.Name == "string" {
						for _, id := range s.Names {
							mark(id.Name)
						}
					}
					return true
				}
				for i, id := range s.Names {
					if i < len(s.Values) && namesNoRequestActor(s.Values[i], empty) {
						mark(id.Name)
					}
				}
			}
			return true
		})
	}
	return empty
}

// namesNoRequestActor reports whether expr is NoRequestActor (bare or
// package-qualified), the empty string literal, or a local in empty.
func namesNoRequestActor(expr ast.Expr, empty map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return namesNoRequestActor(e.X, empty)
	case *ast.Ident:
		return e.Name == "NoRequestActor" || empty[e.Name]
	case *ast.SelectorExpr:
		return e.Sel.Name == "NoRequestActor"
	case *ast.BasicLit:
		return e.Kind == token.STRING && (e.Value == `""` || e.Value == "``")
	}
	return false
}
