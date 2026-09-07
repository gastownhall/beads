package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// This file is the versioned-history twin of journal_completeness_test.go. The
// law it pins (gastownhall/beads#6358, Phase 2 dual-write): every ACCEPTED
// mutation of an issue's durable state — the issues/wisps columns, its labels
// and its outgoing dependencies, i.e. what GetIssueInTx hydrates plus what
// RecordVersionInTx loads — mints exactly one issue_versions row, in the same
// transaction, as the mutation's LAST durable-state write; a no-op mints none;
// a wisp is never versioned (the seam excludes it itself). Comments are not in
// durable_state, is_blocked is derived, and a deleted row has nothing to
// version, so those paths deliberately never reach the seam.
//
// Like the journal guard it detects mutators STRUCTURALLY, by the DML a
// function executes, so a new write path cannot ship without either reaching
// RecordVersionInTx or being justified in the exemption table below.

// versionMintHelpers are the helpers whose call mints a version row. There is
// exactly one seam; a function mints if it calls it directly or through a
// named helper (mintDependencyVersion, addLabelInTx, updateIssueInTx, ...).
var versionMintHelpers = map[string]bool{
	"RecordVersionInTx": true,
}

// versionMintEdges follows the same call-graph discipline as journalEmitEdges:
// nothing in the derived-readiness family mints, and a mutator must not be
// able to inherit a mint through a recompute either.
func versionMintEdges(f *journalscan.FuncInfo) []string {
	return journalEmitEdges(f)
}

// versionedEntryPoints are the issueops functions that mutate an issue's
// durable state and must therefore mint — directly or through a helper that
// does. Every write plumbing bottoms out in one of these; the structural
// cross-check (TestEveryBeadMutatorMintsOrIsExempt) keeps the list complete.
var versionedEntryPoints = []string{
	// create — the singular path mints in place; the batch path defers the
	// mint past PersistDependenciesWithOptionsResult so the first version
	// carries the creation-time edge set.
	"CreateIssueInTx",
	"CreateIssueInTxWithResult",
	"CreateIssuesInTx",
	"CreateIssuesInTxWithResult",
	"CreateIssuesInTxWithContext",
	// update, including the metadata verbs that write through it, the label
	// and parent patches, and the persistence move
	"UpdateIssueInTx",
	"UpdateIssueWithoutEventInTx",
	"MergeMetadataInTx",
	"DeleteMetadataInTx",
	"CompareAndSetMetadataKeyInTx",
	"ApplyLabelPatch",
	"ApplyParentPatch",
	"MoveIssuePersistenceInTx",
	// close / reopen, including the guarded CAS + savepoint path
	"CloseIssueInTx",
	"CloseIssueWithoutEventInTx",
	"CloseIssueCheckedInTx",
	"ReopenIssueInTx",
	// the delete role's neighbour rewrite versions the surviving neighbours
	// (the deleted rows themselves are the delete family, exempt below)
	"RewriteDeletedReferencesInTx",
	// claim / release / lease recovery
	"ClaimIssueInTx",
	"ClaimReadyIssueInTx",
	"UnclaimIssueInTx",
	"UnclaimIssueIfAssigneeInTx",
	"ReleaseIssueInTx",
	"ReclaimExpiredLeasesInTx",
	// plane moves, graph edges, labels, scheduled status flips
	"PromoteFromEphemeralInTx",
	"AddDependencyInTx",
	"RemoveDependencyInTx",
	"AddLabelInTx",
	"RemoveLabelInTx",
	"WakeExpiredDefersInTx",
	// the public lifecycle surface (roles/facade wave). These delegate to the
	// leaves above; listing them pins the delegation so a role that grows its
	// own DML cannot quietly stop versioning.
	"ExecuteCreate",
	"ExecuteCreateBatch",
	"ExecuteUpdate",
	"ExecuteClose",
	"ExecuteCloseBatch",
	"ExecuteReopen",
	"ExecuteClaim",
	"ExecuteClaimNext",
	"ExecuteAddDependencies",
	"ExecuteRemoveDependency",
	"ApplyBatchInTx",
}

// versionExemptions are exported functions the DML detector flags as writing a
// work-bead table but which legitimately do NOT mint a version for the row
// they write, each with a reason. The staleness check fails if any stops being
// flagged, so an exemption cannot rot. Note that some of these DO reach the
// seam transitively for a NEIGHBOUR (DeleteInTx rewrites references on the
// surviving issues through UpdateIssueInTx); the exemption is about the row
// the function itself writes, and versionNeverMints below pins the ones that
// must not reach the seam at all.
var versionExemptions = map[string]string{
	// comments — a separate table, not part of durable_state (GetIssueInTx
	// hydrates labels, not comments), so a comment write versions nothing.
	"AddIssueCommentInTx":    "comments are not in durable_state",
	"ImportIssueCommentInTx": "comments are not in durable_state",
	"ExecuteAddComment":      "comments are not in durable_state",
	"AddCommentEventInTx":    "comments are not in durable_state",
	"PersistComments":        "constituent comment write of a create; comments are not in durable_state",
	"InsertDerivedComment":   "raw comment insert; comments are not in durable_state",

	// is_blocked — derived readiness state, recomputed from the graph, never
	// a mutation of the bead in its own right.
	"RecomputeIsBlockedInTx":           "is_blocked is derived state",
	"RecomputeIsBlockedInTxWithResult": "is_blocked is derived state",
	"RecomputeIsBlockedForIDsInTx":     "is_blocked is derived state",
	"RecomputeIsBlockedForWispIDsInTx": "is_blocked is derived state",
	"RecomputeIsBlockedAfterMergeInTx": "is_blocked is derived state",
	"RecomputeAllIsBlockedInTx":        "is_blocked is derived state",
	"MarkIsBlockedInTx":                "is_blocked is derived state",

	// the delete family — no surviving row to version; the deleted-Versioned-
	// Bead guarantee is Phase 3 (#6358). DeleteInTx versions its NEIGHBOURS
	// through RewriteDeletedReferencesInTx, never the deleted rows.
	"DeleteIssueInTx":                 "delete family: no surviving row (Phase 3)",
	"DeleteIssuesInTx":                "delete family: no surviving row (Phase 3)",
	"DeleteResolvedSetInTx":           "delete family: no surviving row (Phase 3)",
	"DeleteInTx":                      "delete family: no surviving row (Phase 3); neighbours version via RewriteDeletedReferencesInTx",
	"SweepInTx":                       "delete family: no surviving row (Phase 3)",
	"DeleteIssuesBySourceRepoInTx":    "bulk delete: no surviving row (Phase 3)",
	"DeleteWispFromDependenciesInTx":  "delete-family cleanup of edges whose target is gone",
	"DeleteWispsFromDependenciesInTx": "delete-family cleanup of edges whose target is gone",

	// rename — history stays keyed to the old id; out of contract by design.
	"UpdateIssueIDInTx":               "rename: out of contract by design (history stays keyed to the old id)",
	"UpdateWispIDInDependenciesInTx":  "rename: dep-row rekey, out of contract by design",
	"UpdateIssueIDInDependenciesInTx": "rename: dep-row rekey, out of contract by design",

	// compaction bookkeeping and restore — outside the op vocabulary; the
	// content rewrite that precedes compaction versions through UpdateIssue.
	"ApplyCompactionInTx":     "compaction bookkeeping columns, out of contract by design",
	"RestoreFromSnapshotInTx": "restore: CAS composition is Phase 3 (#6358)",

	// constituent sub-helpers whose entry point mints the whole mutation once
	"InsertIssueIntoTable":                   "raw issue insert; the calling create entry point mints",
	"InsertIssueIfNew":                       "raw issue insert; the calling create entry point mints",
	"InsertIssueStrictInTx":                  "raw issue insert; the calling create/persistence-move entry point mints",
	"PersistLabels":                          "constituent label write of a create; the create entry point mints",
	"PersistDependencies":                    "creation-time edges; CreateIssuesInTxWithContext mints once per issue after they land",
	"PersistDependenciesWithResult":          "creation-time edges; CreateIssuesInTxWithContext mints once per issue after they land",
	"PersistDependenciesWithOptionsResult":   "creation-time edges; CreateIssuesInTxWithContext mints once per issue after they land",
	"RetargetInboundDependenciesToWispInTx":  "rewrites INBOUND edges during a plane move; the moved issue's own entry point mints",
	"RetargetInboundDependenciesToIssueInTx": "rewrites INBOUND edges during a plane move; the moved issue's own entry point mints",

	// aux tables matched via templated %s, not work-bead state
	"ReconcileChildCounters":        "child_counters are derived CLI acceleration state",
	"GetNextChildIDTx":              "child_counters allocation, not work-bead state",
	"RecordEventInTable":            "writes the events audit table (templated %s), not work-bead state",
	"RecordFullEventInTable":        "writes the events audit table (templated %s), not work-bead state",
	"InsertDerivedEvent":            "writes the events audit table (templated %s), not work-bead state",
	"InsertDerivedEventReturningID": "writes the events audit table (templated %s), not work-bead state",

	// the seam itself: its UPDATE issues SET current_revision is the
	// bookkeeping half of the mint, not a mutation that needs its own.
	"RecordVersionInTx": "the seam itself; advances current_revision to match the row it just inserted",
}

// versionNeverMints pins the deliberate NOT-versioned rulings from the other
// side: each must exist and must not reach RecordVersionInTx, directly or
// transitively, so a well-meaning edit cannot silently start versioning
// comments, derived state, deletes, renames, compaction bookkeeping, lease
// keepalives or bootstrap. (Migrations live in the schema package and are out
// of this scan's reach; they are out of contract by design.)
var versionNeverMints = map[string]string{
	"AddIssueCommentInTx":              "comments are not in durable_state",
	"ImportIssueCommentInTx":           "comments are not in durable_state",
	"ExecuteAddComment":                "comments are not in durable_state",
	"RecomputeIsBlockedInTx":           "is_blocked is derived state",
	"RecomputeIsBlockedInTxWithResult": "is_blocked is derived state",
	"RecomputeIsBlockedForIDsInTx":     "is_blocked is derived state",
	"RecomputeIsBlockedForWispIDsInTx": "is_blocked is derived state",
	"RecomputeIsBlockedAfterMergeInTx": "is_blocked is derived state",
	"RecomputeAllIsBlockedInTx":        "is_blocked is derived state",
	"MarkIsBlockedInTx":                "is_blocked is derived state",
	"DeleteIssueInTx":                  "delete family (Phase 3)",
	"DeleteIssuesInTx":                 "delete family (Phase 3)",
	"DeleteResolvedSetInTx":            "delete family (Phase 3)",
	"DeleteIssuesBySourceRepoInTx":     "bulk delete (Phase 3)",
	"UpdateIssueIDInTx":                "rename: out of contract by design",
	"ApplyCompactionInTx":              "compaction bookkeeping: out of contract by design",
	"RestoreFromSnapshotInTx":          "restore: Phase 3",
	"HeartbeatIssueInTx":               "lease keepalive: writes only the clone-local leases table, never a durable bead field",
	"BootstrapInTx":                    "bootstrap: config and metadata tables only, no issue-plane row",
}

func versionMints(t *testing.T) (map[string]*journalscan.FuncInfo, map[string]bool) {
	t.Helper()
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}
	mints := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(versionMintHelpers) },
		versionMintEdges)
	return fns, mints
}

// TestEveryVersionedEntryPointMints parses this package's source, builds the
// intra-package call graph, and asserts every versioned entry point reaches
// RecordVersionInTx directly or through a function that transitively does.
// If a listed mutation stops minting — directly or through its delegates —
// this test fails.
func TestEveryVersionedEntryPointMints(t *testing.T) {
	fns, mints := versionMints(t)
	for _, entry := range versionedEntryPoints {
		if _, defined := fns[entry]; !defined {
			t.Errorf("versioned entry point %q not found in issueops — was it renamed? update versionedEntryPoints", entry)
			continue
		}
		if !mints[entry] {
			t.Errorf("versioned entry point %q does not mint a version: it neither calls RecordVersionInTx nor a function that transitively does", entry)
		}
	}
}

// TestEveryBeadMutatorMintsOrIsExempt is the STRUCTURAL completeness
// cross-check. It detects, by DML rather than by name, every EXPORTED function
// that writes a work-bead table (INSERT / UPDATE / DELETE against issues,
// wisps, dependencies, labels, comments, and their wisp variants — literal or
// templated table name), and asserts each one mints (reaches RecordVersionInTx
// directly or transitively) OR is explicitly exempted with a reason. A new
// exported mutator that writes a bead table therefore cannot ship without
// either versioning or being justified in versionExemptions.
func TestEveryBeadMutatorMintsOrIsExempt(t *testing.T) {
	fns, mints := versionMints(t)

	beadDML := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.OwnBeadDML },
		func(f *journalscan.FuncInfo) []string { return f.IdentCalls })

	seenExempt := map[string]bool{}
	var checked int
	for key, f := range fns {
		if f.Recv != "" || !f.Exported || !beadDML[key] {
			continue
		}
		if reason, ok := versionExemptions[f.Name]; ok {
			if reason == "" {
				t.Errorf("%s has an empty exemption reason", f.Name)
			}
			seenExempt[f.Name] = true
			continue
		}
		checked++
		if !mints[key] {
			t.Errorf("exported function %q writes a work-bead table but mints no version (no RecordVersionInTx directly or transitively) and is not exempted — make it mint, or add it to versionExemptions with a reason", f.Name)
		}
	}

	if checked == 0 {
		t.Fatal("cross-check found no exported bead mutators — DML detection or parsing changed; the guard is not actually running")
	}
	for m := range versionExemptions {
		if !seenExempt[m] {
			t.Errorf("exemption %q no longer matches an exported bead-writing function — remove it", m)
		}
	}
}

// TestVersionNeverMintsPathsStaySilent is the inverse guard. A deliberate
// decision NOT to version is as much a contract as a decision to version, and
// the structural check above cannot defend it. Each entry must still exist (a
// rename invalidates the ruling) and must still be silent (an added mint
// reverses it).
func TestVersionNeverMintsPathsStaySilent(t *testing.T) {
	fns, mints := versionMints(t)
	for name, reason := range versionNeverMints {
		if reason == "" {
			t.Errorf("%s has an empty exemption reason", name)
		}
		if _, defined := fns[name]; !defined {
			t.Errorf("never-mints path %q not found in issueops — was it renamed? update versionNeverMints", name)
			continue
		}
		if mints[name] {
			t.Errorf("never-mints path %q now reaches RecordVersionInTx, reversing a deliberate ruling: %s\n"+
				"If the ruling changed, move it to versionedEntryPoints; otherwise remove the mint.", name, reason)
		}
	}
}

// TestVersionedEntryPointsAreNotExempt keeps the two tables disjoint: a
// function cannot both be required to mint and be excused from it.
func TestVersionedEntryPointsAreNotExempt(t *testing.T) {
	for _, entry := range versionedEntryPoints {
		if _, ok := versionExemptions[entry]; ok {
			t.Errorf("%q is both a versioned entry point and exempt — pick one", entry)
		}
		if _, ok := versionNeverMints[entry]; ok {
			t.Errorf("%q is both a versioned entry point and a never-mints path — pick one", entry)
		}
	}
}
