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
	"RecordVersionInTx": true,
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
var versionCompositeMutations = []string{
	"ExecuteUpdate",
	"applyLabelPatch",
	"applyParentPatch",
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
