package issueops

import (
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
	"recomputeIsBlockedAfterMergeScoped": true,
	"recomputeIsBlockedForAll":           true,
	"MarkIsBlockedInTx":                  true,
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
	"DeleteIssuesInTx",
	"DeleteResolvedSetInTx",
	"DeleteIssuesBySourceRepoInTx",
	"DeleteInTx",
	"RewriteDeletedReferencesInTx",
	// claim / release / lease recovery
	"ClaimIssueInTx",
	"ClaimReadyIssueInTx",
	"UnclaimIssueInTx",
	"UnclaimIssueIfAssigneeInTx",
	// HeartbeatIssueInTx is deliberately absent — see journalExemptMutations.
	"ReclaimExpiredLeasesInTx",
	// plane moves, graph edges, labels, comments, renames
	"PromoteFromEphemeralInTx",
	"AddDependencyInTx",
	"RemoveDependencyInTx",
	"AddLabelInTx",
	"RemoveLabelInTx",
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
// They fall into four buckets: (1) derived child-counter maintenance; (2)
// aux-table writers the templated-%s heuristic can't distinguish from a bead
// table (events, child counters); (3) constituent sub-helpers of a
// create/rename/promote/delete whose top-level entry point journals the whole
// mutation once; (4) compaction maintenance outside the
// create/update/close/delete/dep/label op vocabulary. The staleness check fails
// if any stops being flagged, so an exemption cannot rot.
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
}

// journalExemptMutations are mutation paths that deliberately do NOT journal
// and that the DML detector cannot speak to, because the table they write is
// not a work-bead table. beadDMLExemptions cannot hold them: its staleness
// check requires every entry to still be flagged as a bead mutator, and these
// never are. TestExemptMutationsDoNotJournal pins the decision from the other
// side — each must exist and must NOT emit — so re-adding an emit fails loudly
// instead of silently reversing the ruling.
var journalExemptMutations = map[string]string{
	"HeartbeatIssueInTx": "high-frequency lease keepalive: writes only the clone-local `leases` table " +
		"(lease-liveness state), never a durable bead field. Journaling it would put a full-snapshot " +
		"write plus the shared seq-counter serialization on the hottest write in the fleet, and a replay " +
		"consumer gains nothing — lease state is working-set-plane and expires on its own. Lease RECLAIM " +
		"is the opposite case and does journal: it clears assignee and reverts status, which is durable.",
}

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
