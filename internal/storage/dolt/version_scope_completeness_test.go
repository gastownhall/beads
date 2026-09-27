package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// versionScopeCalls are the ways a function binds versioned-history activation
// to the concrete transaction it just began: the store helper, or the issueops
// primitive it wraps (runDoltTransaction calls the latter directly).
var versionScopeCalls = map[string]bool{
	"scopeVersionedHistoryTransaction": true,
	"ScopeVersionedHistoryTransaction": true,
}

// rawTxVersionScopeExemptions are functions that mint a raw transaction
// reaching versioning code but legitimately do NOT scope it, each with a
// reason. The staleness check below fails if one stops being flagged, so an
// exemption cannot rot.
//
// Every entry is a WISP-PLANE mutation. They reach the shared issueops
// mutators, which is why the scan flags them, but RecordVersionInTx returns
// early on IsWisp(issue): issue_versions is the permanent-issues history and
// wisps are the ephemeral plane, whose own current_revision column is shape
// parity only (migration 0067's "SEMANTIC ASYMMETRY, DELIBERATE" note).
// Binding activation to these transactions could not mint a row, so it would
// be inert code that reads as coverage.
//
// The journal guard exempts a different set — the is_blocked recompute passes
// — which deliberately does NOT appear here: those recomputes never reach a
// versioning mutator at all, so this scan does not flag them, and listing them
// would rot immediately under the staleness check below.
var rawTxVersionScopeExemptions = map[string]string{
	"DoltStore.claimWisp":         "wisp-plane claim: RecordVersionInTx returns early on IsWisp, so no issue_versions row is reachable from this transaction",
	"DoltStore.closeWisp":         "wisp-plane close: RecordVersionInTx returns early on IsWisp, so no issue_versions row is reachable from this transaction",
	"DoltStore.closeWispChecked":  "wisp-plane checked close: RecordVersionInTx returns early on IsWisp, so no issue_versions row is reachable from this transaction",
	"DoltStore.updateWisp":        "wisp-plane update: RecordVersionInTx returns early on IsWisp, so no issue_versions row is reachable from this transaction",
	"DoltStore.updateWispChecked": "wisp-plane checked update: RecordVersionInTx returns early on IsWisp, so no issue_versions row is reachable from this transaction",
	"DoltStore.addWispDependency": "wisp-plane dependency edit: wisp edges live in wisp_dependencies and RecordVersionInTx returns early on IsWisp",
	"DoltStore.mergeMetadataWisp": "wisp slot metadata merge: mutates the wisp plane only, and RecordVersionInTx returns early on IsWisp",
	"DoltStore.clearMetadataWisp": "wisp slot metadata clear: mutates the wisp plane only, and RecordVersionInTx returns early on IsWisp",
}

// TestEveryRawTxVersionScopeIsScopedOrExempt is the versioned-history twin of
// TestEveryRawTxJournalScopeIsScopedOrExempt, and exists for the same reason:
// the issueops guards (version_completeness_test.go) prove the mutation seam
// MINTS; nothing proved the store turns minting ON for the transaction the
// mutation runs in. That activation half was hand-enumerated here too — three
// sites against the journal's fifteen — and the journal's own comment records
// that hand-enumerating its half "was wrong both times".
//
// An unscoped versioned-history transaction is quieter than an unscoped
// journal: the mutation commits, and the version row is simply absent. Worse,
// absence is not inert. The next mutation that DOES run scoped mints
// MAX(revision)+1 = 1, so the history asserts that the update is the creation
// — a lie that reads as well-formed history.
//
// KNOWN LIMIT, inherited from the journal guard's shape: the scan is per
// FUNCTION, not per transaction. RemoveDependencyWithOptions begins two
// transactions in one body (wisp arm, non-wisp arm) and satisfies this guard
// once EITHER arm scopes — measured, by deleting each arm's scope in turn:
// only deleting both turns it red. That is the reason the fix scoped the
// inert wisp arm too rather than the issues-plane arm alone; a half-scoped
// two-arm function is a shape this guard cannot see.
func TestEveryRawTxVersionScopeIsScopedOrExempt(t *testing.T) {
	issueFns, err := journalscan.ParsePackage("../issueops")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}
	emitHelpers := map[string]bool{
		"RecordVersionInTx": true,
	}
	// The plain fixpoint, as in the journal guard: any reachable version write
	// matters. A transaction that can produce an issue_versions row at all must
	// have activation bound to it.
	issueEmits := journalscan.Fixpoint(issueFns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(emitHelpers) },
		func(f *journalscan.FuncInfo) []string { return f.AllCallNames() })

	versioningMutators := map[string]bool{}
	for key, f := range issueFns {
		if f.Exported && issueEmits[key] {
			versioningMutators[f.Name] = true
		}
	}
	if len(versioningMutators) == 0 {
		t.Fatal("derived no versioning issueops mutators — the emit analysis changed and this guard is not actually running")
	}

	doltFns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}

	seenExempt := map[string]bool{}
	var checked int
	for key, f := range doltFns {
		// In scope: a function that mints its own transaction AND hands it to a
		// versioning mutator IN ITS OWN BODY. A function given a tx by
		// withWriteTx/runDoltTransaction inherits their scoping, and those two
		// are covered by TestTxMintingWrappersScopeVersionedHistory below.
		// Reachability is deliberately DIRECT, for the reasons the journal
		// guard documents at length.
		if !f.CallsAnyOf(map[string]bool{"BeginTx": true}) || !f.CallsAnyOf(versioningMutators) {
			continue
		}
		if reason, ok := rawTxVersionScopeExemptions[key]; ok {
			if reason == "" {
				t.Errorf("%s has an empty version-scope exemption reason", key)
			}
			seenExempt[key] = true
			continue
		}
		checked++
		// Scoping must be DIRECT: activation binds to one concrete tx, so the
		// function that began it is the only one that can bind it.
		if !f.CallsAnyOf(versionScopeCalls) {
			t.Errorf("%s begins its own transaction and reaches versioning issueops code, but never calls scopeVersionedHistoryTransaction on it — "+
				"every mutation in that transaction mints NO issue version, and the next scoped mutation is then recorded as revision 1 "+
				"(the history would claim that update is the creation). Scope it beside the journal scope (see dependencies.go for the idiom), "+
				"or add it to rawTxVersionScopeExemptions with a reason.", key)
		}
	}

	if checked == 0 {
		t.Fatal("guard found no raw-transaction mutation paths — BeginTx detection or parsing changed; the guard is not actually running")
	}
	for key := range rawTxVersionScopeExemptions {
		if !seenExempt[key] {
			t.Errorf("version-scope exemption %q no longer matches a raw-transaction mutation path — remove it", key)
		}
	}
}

// txMintingWrappers are the functions that begin a transaction and hand it to
// a CALLBACK rather than mutating in their own body. The scan above cannot see
// them: its reachability is deliberately direct, and these wrappers call no
// mutator themselves — the callback does, one frame down.
//
// They are also where the defect this guard exists for actually lived.
// runDoltTransaction is the entry point for bd create, batch create/import,
// bd graph apply, merge_slot and the CLI transact wrappers, so an unscoped
// runDoltTransaction leaves most of the issues plane unversioned while every
// raw-tx site in the scan above still looks perfectly scoped. Deleting its
// scope call does NOT fail the scan (verified by deleting it) — which is why
// this second arm exists rather than being folded into the first.
var txMintingWrappers = []string{
	"DoltStore.runDoltTransaction",
	"DoltStore.withWriteTx",
}

// TestTxMintingWrappersScopeVersionedHistory covers the half
// TestEveryRawTxVersionScopeIsScopedOrExempt structurally cannot. It fails
// loudly when a wrapper stops existing under its listed name, so a rename
// cannot quietly turn this into the same silent no-op a missing scope call
// already is.
func TestTxMintingWrappersScopeVersionedHistory(t *testing.T) {
	doltFns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}
	for _, name := range txMintingWrappers {
		f, ok := doltFns[name]
		if !ok {
			t.Errorf("tx-minting wrapper %s no longer exists under that name — update txMintingWrappers, or this guard silently stops covering it", name)
			continue
		}
		if !f.CallsAnyOf(map[string]bool{"BeginTx": true}) {
			t.Errorf("%s no longer begins its own transaction — it may no longer belong in txMintingWrappers", name)
			continue
		}
		if !f.CallsAnyOf(versionScopeCalls) {
			t.Errorf("%s begins the transaction most issues-plane mutations run in, but never binds versioned-history activation to it — "+
				"RecordVersionInTx then no-ops for every mutation routed through it, so those beads get no creation version and the first "+
				"later scoped mutation is minted as revision 1. Add ScopeVersionedHistoryTransaction beside the journal scope.", name)
		}
	}
}
