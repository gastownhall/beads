package issueops

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
// named helper (mintDependencyVersion, addLabelInTx, updateIssueInTx, ...)
// whose mint it did not switch off — see versionMintGate.
var versionMintHelpers = map[string]bool{
	"RecordVersionInTx":          true,
	"RecordVersionForCreateInTx": true,
}

// versionMintGate names the boolean parameter the constituent helpers
// (claimIssueInTx, updateIssueInTx, applyLabelPatch, applyParentPatch,
// moveIssuePersistenceInTx, addLabelInTx, removeLabelInTx, addDependencyInTx,
// removeDependencyInTx) mint under. mintDependencyVersion spells its own
// "mint", but it is only ever handed a caller's mintVersion, never a literal,
// so the one name covers every switch-off in the package.
const versionMintGate = "mintVersion"

// versionMintEdges follows the same call-graph discipline as journalEmitEdges
// — nothing in the derived-readiness family mints, and a mutator must not be
// able to inherit a mint through a recompute — with one refinement the journal
// guard has no need for: a helper that takes a mintVersion bool mints only
// when asked to, so a call passing the literal false for it is NOT a minting
// edge. That is how a composite mutation runs its constituents without minting
// and mints once itself at the end (ExecuteUpdate's claim, row write, label
// and parent patches and persistence move). Without the refinement the
// fixpoint credited the composite with the mint it told its constituents to
// skip, and deleting its own final RecordVersionInTx stayed green (reviewer
// Probe 2 on gastownhall/beads#6358). A call passing true, a variable, or the
// caller's own mintVersion parameter is still a minting edge, so the leaf
// wrappers (ClaimIssueInTx, ApplyLabelPatch, ...) and the forwarding helpers
// keep theirs.
func versionMintEdges(fns map[string]*journalscan.FuncInfo) func(*journalscan.FuncInfo) []string {
	return func(f *journalscan.FuncInfo) []string {
		var live []string
		for _, callee := range journalEmitEdges(f) {
			if journalscan.CallsOnlyWithLiteralFalse(fns, f, callee, versionMintGate) {
				continue
			}
			live = append(live, callee)
		}
		return live
	}
}

// versionCompositeMutations are the functions that run constituents with
// versionMintGate switched off and mint once themselves, so each must carry
// its own RecordVersionInTx: nothing it calls with the gate off can carry it
// on its behalf. TestCompositeMutationsCarryTheirOwnMint is what turns
// reviewer Probe 2 (delete ExecuteUpdate's final mint block) into a failure.
// ExecuteUpdate's body is executeUpdate (ExecuteUpdate is it with the template
// guard on), so the composite named here is the body that carries the mint.
var versionCompositeMutations = []string{
	"executeUpdate",
	"applyLabelPatch",
	"applyParentPatch",
}

// versionedEntryPoints are the issueops functions that mutate an issue's
// durable state and must therefore mint — directly or through a helper that
// does. Every write plumbing bottoms out in one of these; the structural
// cross-check (TestEveryBeadMutatorMintsOrIsExempt) keeps the list complete.
var versionedEntryPoints = []string{
	// the heartbeat family mints on its RE-ARM path only (recorded as a
	// mutation); a refresh writes only the clone-local leases table and mints
	// nothing.
	"RenewLeaseInTx",
	"HeartbeatIssueInTx",
	"ExecuteHeartbeatInTx",
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

// versionCreateShapedMintHelper is the create-shaped entry to the seam
// (design §16.2b). It stamps participation_generation instead of checking it,
// so it is right only for a row that is brand new on the issues plane, and
// wrong anywhere else: an update-shaped mutation that reached it would stamp
// a legacy row and so promote it, which R2.3 forbids an ordinary write.
var versionCreateShapedMintHelper = map[string]bool{"RecordVersionForCreateInTx": true}

// versionUpdateShapedMintHelper is the update-shaped entry: it reads
// participation_generation and mints nothing for a legacy (NULL) row.
var versionUpdateShapedMintHelper = map[string]bool{"RecordVersionInTx": true}

// versionCreateShapedMintSites are the PURE create-shaped mint sites: every
// row each one mints for is new on the issues plane, so each must call
// RecordVersionForCreateInTx itself and must never call RecordVersionInTx.
// None is left today. PromoteFromEphemeralInTx was one until it learned that
// the issues plane can already hold the id it promotes; it is listed in
// versionDualShapeMintSites now.
var versionCreateShapedMintSites = []string{}

// versionDualShapeMintSites choose the entry per row at run time: create-shaped
// on the branch where the row is new on the issues plane, update-shaped on the
// branch where it already lived there. Each branch is a common path, so each
// site must call BOTH entries itself. Reaching either one through a delegate
// is not enough: executeUpdate runs its persistence move with
// mintVersion=false, and a batch create switches finishCreateIssueInTx's own
// mint off at run time (DeferVersionMint), so the call graph would credit each
// with mints its constituents never perform.
//
// A site that calls only one entry has had a branch switched to the other. If
// the update-shaped branch now calls the create-shaped entry, an ordinary
// write stamps a legacy row and promotes it (R2.3). If the create-shaped
// branch now calls the update-shaped entry, the fence reads the new row's NULL
// as legacy, and the record is never versioned.
//
// One production dual-shape site lives outside this package, where this
// guard's parse (journalscan.ParsePackage(".")) cannot list it: domain/db's
// issueSQLRepositoryImpl.Insert. Its CreateOnly branch is create-shaped. Its
// upsert, the create that is not CreateOnly, is create-shaped only when it
// inserted a new issues row; it is update-shaped when the issues table
// already held the id, and for every wisps-table insert. The uow leg's
// TestParticipationGenerationUpsertCreate holds those upsert branches at run
// time instead.
var versionDualShapeMintSites = map[string]string{
	"finishCreateIssueInTx":       "a singular create mints create-shaped when it inserted the row and update-shaped when it resolved to an existing one",
	"CreateIssuesInTxWithContext": "a batch create's deferred mint pass is create-shaped for a row the batch inserted and update-shaped for an existing row it resolved to",
	"moveIssuePersistenceInTx":    "a move out of the wisps table is create-shaped for a row that carried no participation; it is update-shaped for a row whose participation the move restored (a demotion carried it while history was on), and for the same-plane normalize (bd update --persistent repairing an issues-plane row's flags in place)",
	"executeUpdate":               "the guarded update's BeginsHistory branch (a move out of the wisps table of a row that carried no participation) is create-shaped; every other change it mints for, a move that restored a row's participation included, is update-shaped",
	"PromoteFromEphemeralInTx":    "a wisp promoted onto the issues plane mints create-shaped when it carried no participation; it mints update-shaped when the promotion restored the participation a demotion carried, and when the issues plane already held the id (dual presence, GH#4455) so the upsert resolved to that row",
}

// versionCreateShapedEntryPoints are the entry points whose every mint is
// create-shaped, so each must reach RecordVersionForCreateInTx.
// deferredCreates.flush (#7205's multi-row INSERT path) is one: it hands
// finishCreateIssueInTx isNew=true for every row. The call graph cannot read
// that literal, so flush still reaches the update-shaped entry through
// finishCreateIssueInTx, and it is held only to the create half.
var versionCreateShapedEntryPoints = []string{
	"deferredCreates.flush",
}

// versionDualShapeEntryPoints reach both entries by design. Each delegates to
// a dual-shape mint site, or composes create- and update-shaped mutations in
// one call, so it must reach both and is held to neither half of the split on
// its own. The dual-shape mint sites above pin which branch calls which entry.
var versionDualShapeEntryPoints = map[string]string{
	"CreateIssueInTx":             "delegates to finishCreateIssueInTx",
	"CreateIssueInTxWithResult":   "delegates to finishCreateIssueInTx",
	"CreateIssuesInTx":            "delegates to CreateIssuesInTxWithContext",
	"CreateIssuesInTxWithResult":  "delegates to CreateIssuesInTxWithContext",
	"CreateIssuesInTxWithContext": "a dual-shape mint site itself",
	"MoveIssuePersistenceInTx":    "delegates to moveIssuePersistenceInTx with its mint on",
	"PromoteFromEphemeralInTx":    "a dual-shape mint site itself",
	"ExecuteCreate":               "delegates to the create path, which ends in finishCreateIssueInTx",
	"ExecuteCreateBatch":          "delegates to CreateIssuesInTxWithContext",
	"ExecuteUpdate":               "delegates to executeUpdate",
	"ApplyBatchInTx":              "batch apply runs create items and update items in one transaction",
}

func versionShapedMints(t *testing.T) (fns map[string]*journalscan.FuncInfo, createShaped, updateShaped map[string]bool) {
	t.Helper()
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	edges := versionMintEdges(fns)
	createShaped = journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(versionCreateShapedMintHelper) }, edges)
	updateShaped = journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(versionUpdateShapedMintHelper) }, edges)
	return fns, createShaped, updateShaped
}

// TestCreateShapedEntryPointsMintCreateShaped pins the create half of the
// shape split: each pure create-shaped mint site calls
// RecordVersionForCreateInTx itself and never RecordVersionInTx, and every
// pure create-shaped entry point reaches it. If one of them falls back to
// RecordVersionInTx, the fence finds the new row's participation_generation
// NULL, skips the mint, and the record is never versioned.
// TestEveryVersionedEntryPointMints cannot see that, because
// RecordVersionInTx satisfies it.
func TestCreateShapedEntryPointsMintCreateShaped(t *testing.T) {
	fns, createShaped, _ := versionShapedMints(t)
	for _, site := range versionCreateShapedMintSites {
		f, defined := fns[site]
		if !defined {
			t.Errorf("create-shaped mint site %q not found in issueops — was it renamed? update versionCreateShapedMintSites", site)
			continue
		}
		if !f.CallsAnyOf(versionCreateShapedMintHelper) {
			t.Errorf("create-shaped mint site %q does not call RecordVersionForCreateInTx itself; the row it mints for is new on the issues plane, so an update-shaped mint there is fenced as legacy", site)
		}
		if f.CallsAnyOf(versionUpdateShapedMintHelper) {
			t.Errorf("create-shaped mint site %q calls RecordVersionInTx; every row it mints for is new on the issues plane, so that mint is fenced as legacy — or, if it now also mints for an existing row, list it in versionDualShapeMintSites", site)
		}
	}
	for _, entry := range versionCreateShapedEntryPoints {
		if _, defined := fns[entry]; !defined {
			t.Errorf("create-shaped entry point %q not found in issueops — was it renamed? update versionCreateShapedEntryPoints", entry)
			continue
		}
		if !createShaped[entry] {
			t.Errorf("create-shaped entry point %q does not reach RecordVersionForCreateInTx, directly or transitively", entry)
		}
	}
}

// TestDualShapeMintSitesCallBothEntries pins the dual-shape sites, the ones
// the update half below has to skip. Each calls RecordVersionInTx and
// RecordVersionForCreateInTx itself, so switching either branch to the
// other entry fails here. Each dual-shape entry point reaches both, so a
// delegate that stopped minting one shape is not silently skipped by the
// update half.
func TestDualShapeMintSitesCallBothEntries(t *testing.T) {
	fns, createShaped, updateShaped := versionShapedMints(t)
	for _, site := range sortedShapeKeys(versionDualShapeMintSites) {
		if versionDualShapeMintSites[site] == "" {
			t.Errorf("dual-shape mint site %s has an empty reason", site)
		}
		f, defined := fns[site]
		if !defined {
			t.Errorf("dual-shape mint site %q not found in issueops — was it renamed? update versionDualShapeMintSites", site)
			continue
		}
		if !f.CallsAnyOf(versionCreateShapedMintHelper) {
			t.Errorf("dual-shape mint site %q no longer calls RecordVersionForCreateInTx itself; its branch for a row new on the issues plane (%s) now fences that row as legacy, and the record is never versioned", site, versionDualShapeMintSites[site])
		}
		if !f.CallsAnyOf(versionUpdateShapedMintHelper) {
			t.Errorf("dual-shape mint site %q no longer calls RecordVersionInTx itself; its branch for a row already on the issues plane (%s) now stamps participation_generation and promotes a legacy record (R2.3)", site, versionDualShapeMintSites[site])
		}
	}
	for _, entry := range sortedShapeKeys(versionDualShapeEntryPoints) {
		if versionDualShapeEntryPoints[entry] == "" {
			t.Errorf("dual-shape entry point %s has an empty reason", entry)
		}
		if _, defined := fns[entry]; !defined {
			t.Errorf("dual-shape entry point %q not found in issueops — was it renamed? update versionDualShapeEntryPoints", entry)
			continue
		}
		if !createShaped[entry] {
			t.Errorf("dual-shape entry point %q no longer reaches RecordVersionForCreateInTx — list it as update-shaped instead (remove it from versionDualShapeEntryPoints)", entry)
		}
		if !updateShaped[entry] {
			t.Errorf("dual-shape entry point %q no longer reaches RecordVersionInTx — list it in versionCreateShapedEntryPoints instead", entry)
		}
	}
}

// TestUpdateShapedEntryPointsNeverMintCreateShaped pins the update half:
// every versioned entry point that is neither create-shaped nor dual-shape
// must not reach RecordVersionForCreateInTx, so no ordinary write of an
// existing row can stamp it out of legacy. The update-shaped branch inside a
// dual-shape site is TestDualShapeMintSitesCallBothEntries's to pin.
func TestUpdateShapedEntryPointsNeverMintCreateShaped(t *testing.T) {
	_, createShaped, _ := versionShapedMints(t)
	create := map[string]bool{}
	for _, entry := range versionCreateShapedEntryPoints {
		create[entry] = true
	}
	var checked int
	for _, entry := range versionedEntryPoints {
		if create[entry] {
			continue
		}
		if _, dual := versionDualShapeEntryPoints[entry]; dual {
			continue
		}
		checked++
		if createShaped[entry] {
			t.Errorf("update-shaped entry point %q reaches RecordVersionForCreateInTx, so it can stamp participation_generation on an existing row and promote a legacy record (R2.3) — mint it through RecordVersionInTx, or list it in versionCreateShapedEntryPoints or versionDualShapeEntryPoints if a row it mints for is new on the issues plane", entry)
		}
	}
	if checked == 0 {
		t.Fatal("no update-shaped entry point was checked — versionedEntryPoints or the shape lists changed; the guard is not actually running")
	}
}

// sortedShapeKeys returns m's keys in order, so the shape guards report in a
// stable order.
func sortedShapeKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// versionTestSupportMints sit outside the shape split above, explicitly: they
// are neither create-shaped, dual-shape nor update-shaped. They are not
// mutation entry points (versionedEntryPoints) but the seam's test-support
// twin, exempt in versionExemptions, and they reach the shared body directly
// rather than through either entry. Each must reach neither
// RecordVersionForCreateInTx nor RecordVersionInTx: the unfenced mint
// (mintUnfenced) never stamps participation_generation, so a conformance
// fixture's controlled-timestamp mint cannot promote a legacy row, and it is
// not gated or fenced, so routing it through the update-shaped entry would
// silently stop the as-of fixtures from minting history-off rows.
var versionTestSupportMints = map[string]string{
	"RecordVersionAtInTx": "R7.1 as-of read fixtures' controlled-timestamp mint (mintUnfenced): no gate, no fence, no stamp",
}

// TestTestSupportMintsStayOutsideTheShapeSplit pins versionTestSupportMints.
func TestTestSupportMintsStayOutsideTheShapeSplit(t *testing.T) {
	fns, createShaped, updateShaped := versionShapedMints(t)
	shapeListed := map[string]string{}
	for _, entry := range versionedEntryPoints {
		shapeListed[entry] = "versionedEntryPoints"
	}
	for _, entry := range versionCreateShapedEntryPoints {
		shapeListed[entry] = "versionCreateShapedEntryPoints"
	}
	for _, site := range versionCreateShapedMintSites {
		shapeListed[site] = "versionCreateShapedMintSites"
	}
	for entry := range versionDualShapeEntryPoints {
		shapeListed[entry] = "versionDualShapeEntryPoints"
	}
	for site := range versionDualShapeMintSites {
		shapeListed[site] = "versionDualShapeMintSites"
	}
	for _, name := range sortedShapeKeys(versionTestSupportMints) {
		if versionTestSupportMints[name] == "" {
			t.Errorf("%s has an empty test-support reason", name)
		}
		if _, defined := fns[name]; !defined {
			t.Errorf("test-support mint %q not found in issueops — was it renamed? update versionTestSupportMints", name)
			continue
		}
		if list, listed := shapeListed[name]; listed {
			t.Errorf("test-support mint %q is listed in %s; it is the seam's twin, not a mutation entry point or mint site, and stays outside the shape split", name, list)
		}
		if createShaped[name] {
			t.Errorf("test-support mint %q reaches RecordVersionForCreateInTx, so a fixture's mint can stamp participation_generation and promote a legacy record (R2.3)", name)
		}
		if updateShaped[name] {
			t.Errorf("test-support mint %q reaches RecordVersionInTx, whose gate and fence would stop an as-of fixture from minting a history-off row", name)
		}
		if _, exempt := versionExemptions[name]; !exempt {
			t.Errorf("test-support mint %q is not in versionExemptions; it is the seam's twin, not a mutation entry point", name)
		}
	}
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
	"AddIssueCommentInTx":     "comments are not in durable_state",
	"ImportIssueCommentInTx":  "comments are not in durable_state",
	"ExecuteAddComment":       "comments are not in durable_state",
	"AddCommentEventInTx":     "comments are not in durable_state",
	"PersistComments":         "constituent comment write of a create; comments are not in durable_state",
	"InsertDerivedComment":    "raw comment insert; comments are not in durable_state",
	"TouchIssueUpdatedAtInTx": "constituent snapshot touch; the label mutation caller mints the complete post-mutation version",

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
	"DeleteIssuesBesideWispsInTx":     "delete family: no surviving row (Phase 3)",
	"DeleteResolvedSetInTx":           "delete family: no surviving row (Phase 3)",
	"DeleteInTx":                      "delete family: no surviving row (Phase 3); neighbours version via RewriteDeletedReferencesInTx",
	"SweepInTx":                       "delete family: no surviving row (Phase 3)",
	"DeleteIssuesBySourceRepoInTx":    "bulk delete: no surviving row (Phase 3)",
	"DeleteWispFromDependenciesInTx":  "delete-family cleanup of edges whose target is gone",
	"DeleteWispsFromDependenciesInTx": "delete-family cleanup of edges whose target is gone",
	"DeleteWispAuxRowsInTx":           "delete-family cleanup of aux rows whose owning wisp is gone",

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
	"RetireIDsInTx":                 "writes the id tombstone tables (templated %s), not work-bead state; a tombstone is not part of any bead's durable_state",

	// the seam itself: its UPDATE issues SET current_revision is the
	// bookkeeping half of the mint, not a mutation that needs its own.
	"RecordVersionInTx":            "the seam itself; advances current_revision to match the row it just inserted",
	"RecordVersionForCreateInTx":   "the seam's create-shaped sibling; stamps participation_generation and advances current_revision to match the row it just inserted",
	"CarryParticipationToWispInTx": "demotion bookkeeping: records on the wisps row the participation the issues row it replaces had; a wisp is never versioned (FR-8)",

	// RecordVersionAtInTx (R7.1 as-of read, gastownhall/beads#5898 rev 9,
	// gastownhall/beads#6136) shares RecordVersionInTx's body
	// (recordVersionAtInTx) but deliberately does not call RecordVersionInTx
	// itself — it exists precisely to bypass RecordVersionInTx's
	// versionedHistoryEnabled gate, so an as-of-read conformance fixture can
	// mint a version dated in the past. It is the seam too, by the same
	// reasoning as RecordVersionInTx's own entry above.
	"RecordVersionAtInTx": "test-support twin of RecordVersionInTx, sharing its body; the seam itself, not a caller of it — advances current_revision to match the row it just inserted",
}

// versionSinkBookkeeping are the helpers whose own work-bead DML is the
// bookkeeping half of a SINK rather than a mutation of the bead: they write a
// column no durable_state reads and no caller can set, inside the sink's own
// statement sequence. Their DML does not make every caller of the sink a bead
// mutator — without this, every journal emitter (RecordEventInTx and its
// siblings) would read as one, because the journal's create seam stamps
// wisps.arrival_seq. Each entry must still exist and still write a bead table,
// so the carve-out cannot outlive the code it names.
var versionSinkBookkeeping = map[string]string{
	"stampArrivalSeqInTx": "the journal's create seam stamping wisps.arrival_seq: a store-assigned, clone-local column outside durable_state",
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
	"DeleteIssuesBesideWispsInTx":      "delete family (Phase 3)",
	"DeleteResolvedSetInTx":            "delete family (Phase 3)",
	"DeleteIssuesBySourceRepoInTx":     "bulk delete (Phase 3)",
	"UpdateIssueIDInTx":                "rename: out of contract by design",
	"ApplyCompactionInTx":              "compaction bookkeeping: out of contract by design",
	"RestoreFromSnapshotInTx":          "restore: Phase 3",
	"BootstrapInTx":                    "bootstrap: config and metadata tables only, no issue-plane row",
	"CarryParticipationToWispInTx":     "demotion bookkeeping for a row that becomes a wisp; a wisp is never versioned (FR-8)",
	"carryParticipationInTx":           "plane-move bookkeeping: carries participation_generation across the move; the move's own entry point mints, once",
}

// versionParticipationCarrySites are the issueops plane moves, each of which
// must carry the moved row's participation across the planes itself
// (carryParticipationInTx): a move into the wisps table records it on the
// wisps row, and a move back out restores it on the new issues row. A site
// that stops carrying it makes every arrival look brand new, so a legacy row
// made a wisp and then persistent again would start being versioned through
// ordinary writes (R2.3). The dolt leg's DoltStore.demoteToWispInTx is the one
// plane move outside this package; it calls CarryParticipationToWispInTx, and
// the dolt leg's TestDemoteToWispCarriesParticipation holds it at run time.
var versionParticipationCarrySites = map[string]string{
	"moveIssuePersistenceInTx": "carries participation into the wisps row on a demotion and restores it on the move back out",
	"PromoteFromEphemeralInTx": "restores the participation a demotion carried, before the wisps row is deleted",
}

// TestPlaneMovesCarryParticipation pins versionParticipationCarrySites: each
// calls carryParticipationInTx itself.
func TestPlaneMovesCarryParticipation(t *testing.T) {
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	carry := map[string]bool{"carryParticipationInTx": true}
	for _, site := range sortedShapeKeys(versionParticipationCarrySites) {
		if versionParticipationCarrySites[site] == "" {
			t.Errorf("participation-carry site %s has an empty reason", site)
		}
		f, defined := fns[site]
		if !defined {
			t.Errorf("participation-carry site %q not found in issueops — was it renamed? update versionParticipationCarrySites", site)
			continue
		}
		if !f.CallsAnyOf(carry) {
			t.Errorf("plane move %q no longer calls carryParticipationInTx (%s): a row's participation is lost across the move, and a legacy row made a wisp and then persistent again begins history through ordinary writes (R2.3)", site, versionParticipationCarrySites[site])
		}
	}
}

func versionMints(t *testing.T) (map[string]*journalscan.FuncInfo, map[string]bool) {
	t.Helper()
	return versionMintsIn(t, ".")
}

// versionMintsIn is versionMints over the package at dir, so the guard's own
// machinery can be proven against a synthetic package.
func versionMintsIn(t *testing.T, dir string) (map[string]*journalscan.FuncInfo, map[string]bool) {
	t.Helper()
	fns, err := journalscan.ParsePackage(dir)
	if err != nil {
		t.Fatalf("parse package %s: %v", dir, err)
	}
	mints := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(versionMintHelpers) },
		versionMintEdges(fns))
	return fns, mints
}

// compositeMintLeaks returns, for a composite mutation f, every function key
// that one of its live edges — the calls left after the literal-false
// constituents are dropped — resolves to and that reaches the seam: each is a
// mint f would perform besides its own. The called bare name is resolved to
// every key it can denote, the free function and every method of that name,
// exactly as the fixpoint resolves it. mints is keyed "Recv.Name" for a
// method, so a bare-name lookup would quietly stop seeing a constituent the
// moment it became one (TestCompositeCheckSeesMethodConstituents).
func compositeMintLeaks(fns map[string]*journalscan.FuncInfo, mints map[string]bool, f *journalscan.FuncInfo) []string {
	seen := map[string]bool{}
	var leaks []string
	for _, callee := range versionMintEdges(fns)(f) {
		for _, key := range journalscan.Resolve(fns, callee) {
			if mints[key] && !seen[key] {
				seen[key] = true
				leaks = append(leaks, key)
			}
		}
	}
	sort.Strings(leaks)
	return leaks
}

// TestCompositeMutationsCarryTheirOwnMint pins that each composite mutation
// calls RecordVersionInTx itself AND that none of the edges it leaves live
// (after the literal-false constituents are dropped) reaches the seam, each
// called name resolved to every function it can denote (compositeMintLeaks)
// — so the composite's own call is load-bearing, and deleting it fails
// TestEveryVersionedEntryPointMints rather than being papered over by a
// constituent it explicitly told not to mint.
func TestCompositeMutationsCarryTheirOwnMint(t *testing.T) {
	fns, mints := versionMints(t)
	for _, name := range versionCompositeMutations {
		f, defined := fns[name]
		if !defined {
			t.Errorf("composite mutation %q not found in issueops — was it renamed? update versionCompositeMutations", name)
			continue
		}
		if !f.CallsAnyOf(versionMintHelpers) {
			t.Errorf("composite mutation %q does not call RecordVersionInTx itself; it runs its constituents with %s=false, so nothing else mints for it", name, versionMintGate)
		}
		for _, key := range compositeMintLeaks(fns, mints, f) {
			t.Errorf("composite mutation %q would also mint through %q, so its own RecordVersionInTx is no longer the one mint the write-path doc promises — either that constituent is called with %s left on, or it mints unconditionally", name, key, versionMintGate)
		}
	}
}

// TestCompositeCheckSeesMethodConstituents proves the composite check against
// a synthetic package whose constituent is a METHOD: the mints fixpoint keys
// it "svc.constituent", the composite calls it by the bare name, and the
// check must still see the mint a call with the gate left on lets through.
// This is what keeps TestCompositeMutationsCarryTheirOwnMint from quietly
// ceasing to bite should a constituent in issueops become a method.
func TestCompositeCheckSeesMethodConstituents(t *testing.T) {
	dir := t.TempDir()
	src := `package probe

func RecordVersionInTx() {}

type svc struct{}

func (s *svc) constituent(mintVersion bool) {
	if mintVersion {
		RecordVersionInTx()
	}
}

// Composite runs its constituent with the gate off and mints once itself.
func Composite(s *svc) { s.constituent(false); RecordVersionInTx() }

// Leaky leaves the gate on, so its constituent mints as well as it does.
func Leaky(s *svc) { s.constituent(true); RecordVersionInTx() }
`
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	fns, mints := versionMintsIn(t, dir)
	if !mints["svc.constituent"] {
		t.Fatal("svc.constituent does not mint; the synthetic package no longer exercises a method constituent")
	}
	if mints["constituent"] {
		t.Fatal("mints is keyed by the bare name of a method; the check must resolve to the receiver-qualified key, and this probe no longer shows that it has to")
	}
	if got := compositeMintLeaks(fns, mints, fns["Composite"]); len(got) != 0 {
		t.Errorf("Composite leaks through %q, want nothing: its constituent is switched off", got)
	}
	if got := compositeMintLeaks(fns, mints, fns["Leaky"]); !reflect.DeepEqual(got, []string{"svc.constituent"}) {
		t.Errorf("Leaky leaks through %q, want [svc.constituent]: a method constituent left with the gate on must be seen through its receiver-qualified key", got)
	}
}

// TestFalseIsNotShadowedInIssueops pins the reading CallsOnlyWithLiteralFalse
// rests on as EXACT for this package rather than merely conservative. The
// scanner counts a false argument as the predeclared identifier only when
// nothing it can see declares that name; this walk proves nothing in the
// package does, in any declaration position — so every mintVersion=false in
// issueops is the switch-off the guard takes it for, and no true is a false
// in disguise. Test files are walked too: a package-level shadow in one would
// change what every false in the package means under test.
func TestFalseIsNotShadowedInIssueops(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}
	var positions int
	var shadows []string
	declared := func(what string, ident *ast.Ident) {
		if ident == nil {
			return
		}
		positions++
		if ident.Name == "false" || ident.Name == "true" {
			shadows = append(shadows, fmt.Sprintf("%s: %s named %s", fset.Position(ident.Pos()), what, ident.Name))
		}
	}
	declaredFields := func(what string, list *ast.FieldList) {
		if list == nil {
			return
		}
		for _, field := range list.List {
			for _, name := range field.Names {
				declared(what, name)
			}
		}
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.ImportSpec:
					declared("import name", node.Name)
				case *ast.ValueSpec:
					for _, name := range node.Names {
						declared("var/const", name)
					}
				case *ast.TypeSpec:
					declared("type", node.Name)
				case *ast.FuncDecl:
					declared("function", node.Name)
					declaredFields("receiver", node.Recv)
				case *ast.FuncType: // the signature of a FuncDecl or a FuncLit
					declaredFields("parameter", node.Params)
					declaredFields("result", node.Results)
				case *ast.AssignStmt:
					if node.Tok == token.DEFINE {
						for _, lhs := range node.Lhs {
							if ident, ok := lhs.(*ast.Ident); ok {
								declared("short variable declaration", ident)
							}
						}
					}
				case *ast.RangeStmt:
					if node.Tok == token.DEFINE {
						for _, expr := range []ast.Expr{node.Key, node.Value} {
							if ident, ok := expr.(*ast.Ident); ok {
								declared("range variable", ident)
							}
						}
					}
				case *ast.LabeledStmt:
					declared("label", node.Label)
				}
				return true
			})
		}
	}
	if positions == 0 {
		t.Fatal("the walk visited no declaration position in issueops — parsing changed; the guard is not actually running")
	}
	sort.Strings(shadows)
	for _, shadow := range shadows {
		t.Errorf("%s shadows a predeclared identifier, so a %s=false in this package may not be the switch-off the guard reads it as — rename it", shadow, versionMintGate)
	}
}

// TestLiteralFalseCalleesResolveUnambiguously pins the other half of the
// switch-off's exactness. Resolution is name-based: a called bare name
// denotes the free function of that name AND every method of that name, and
// when a caller switches the name off with mintVersion=false the fixpoint
// drops its edge to all of them — including a same-named function that
// declares no mintVersion and mints unconditionally, whose mint the switch-off
// cannot have switched off. So every callee reached by a literal-false
// mintVersion call must resolve to exactly one function, or every function it
// resolves to must declare the gate; otherwise the name is ambiguous and one
// of them must be renamed. (Resolve's semantics are deliberately left alone:
// name-based resolution is what keeps the guard free of type information.)
func TestLiteralFalseCalleesResolveUnambiguously(t *testing.T) {
	fns, _ := versionMints(t)
	callers := make([]string, 0, len(fns))
	for key := range fns {
		callers = append(callers, key)
	}
	sort.Strings(callers)
	var switchedOff int
	checked := map[string]bool{}
	for _, caller := range callers {
		f := fns[caller]
		for _, callee := range f.AllCallNames() {
			if !journalscan.CallsOnlyWithLiteralFalse(fns, f, callee, versionMintGate) {
				continue
			}
			switchedOff++
			if checked[callee] {
				continue
			}
			checked[callee] = true
			keys := journalscan.Resolve(fns, callee)
			if len(keys) == 1 {
				continue
			}
			var ungated []string
			for _, key := range keys {
				if fns[key].ParamIndex(versionMintGate) < 0 {
					ungated = append(ungated, key)
				}
			}
			if len(ungated) > 0 {
				t.Errorf("%q is called with %s=false (by %s) and resolves to %q, of which %q declare no %s: the switch-off would drop the fixpoint's edge to a function it cannot have switched off — rename one of them, or give it a %s parameter", callee, versionMintGate, caller, keys, ungated, versionMintGate, versionMintGate)
			}
		}
	}
	if switchedOff == 0 {
		t.Fatalf("no %s=false call found in issueops — the composite mutations no longer switch their constituents off, or the predicate stopped seeing the literal; the guard is not exercising the switch-off at all", versionMintGate)
	}
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

	for name, reason := range versionSinkBookkeeping {
		found := false
		for _, f := range fns {
			if f.Recv == "" && f.Name == name {
				found = true
				if !f.OwnBeadDML {
					t.Errorf("sink bookkeeping helper %q no longer writes a work-bead table — remove it from versionSinkBookkeeping (%s)", name, reason)
				}
			}
		}
		if !found {
			t.Errorf("sink bookkeeping helper %q not found in issueops — was it renamed? update versionSinkBookkeeping", name)
		}
	}
	beadDML := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.OwnBeadDML && versionSinkBookkeeping[f.Name] == "" },
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
