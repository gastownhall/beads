package dolt

import (
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// versionStagingFunnels are the ways a staging path picks up the tables the
// versioned-history seam writes: the store helper that appends them to a
// caller's fixed list, or the dirty-table tracker that marks them for
// versioncontrolops.StageAndCommit.
var versionStagingFunnels = map[string]bool{
	"withVersionedHistoryTables": true,
	"MarkVersionedHistoryDirty":  true,
}

// doltAddStagingExemptions are functions that issue a DOLT_ADD and legitimately
// do NOT route their staged set through a funnel, each with a reason. The
// staleness check below fails if one stops being flagged, so an exemption
// cannot rot.
var doltAddStagingExemptions = map[string]string{
	"DoltStore.commitWorkingSet": "stages the dolt_status-derived dirty set rather than a fixed list: a dirty issue_versions or store_epoch is already in that set by construction, so there is nothing for withVersionedHistoryTables to append",
}

// TestEveryDoltAddStagesVersionedHistoryTables is the staging twin of
// TestEveryRawTxVersionScopeIsScopedOrExempt, and exists for the same reason at
// one step further down the path: those guards prove the seam MINTS and that
// the store turns minting ON for the transaction it mints in; nothing proved
// the minted rows are STAGED into the Dolt commit the mutation makes. Until
// this guard existed, withVersionedHistoryTables' own "Every fixed-list
// DOLT_ADD path in this package routes through here" was an asserted claim, and
// it was false: the role deleter and sweeper looped their hand-listed
// sweptTables directly, so a delete that rewrote a surviving neighbor's
// citations minted version rows inside a transaction that then staged
// everything except them.
//
// Unstaged version rows are the quiet failure this whole phase exists to close.
// They read back perfectly from SQL while sitting in the working set, outside
// the commit that describes them: unreplicated, and liable to be swept into
// whatever unrelated commit stages next. For the append-only tables the loss is
// the unrecoverable kind — no later write refills a hole in issue_versions or
// store_epoch the way the next mutation of a bead rewrites its issues row.
//
// KNOWN LIMIT: the detector reads the string literals a function passes as call
// ARGUMENTS, which is the form every DOLT_ADD in this package uses. A DOLT_ADD
// assembled in a variable or through fmt.Sprintf would be invisible to it — the
// non-vacuity check below is what keeps that from silently emptying the guard,
// the way the scope guards' own "the guard is not actually running" fatals do.
func TestEveryDoltAddStagesVersionedHistoryTables(t *testing.T) {
	doltFns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}

	seenExempt := map[string]bool{}
	var checked int
	for key, f := range doltFns {
		if !callsDoltAdd(f) {
			continue
		}
		if reason, ok := doltAddStagingExemptions[key]; ok {
			if reason == "" {
				t.Errorf("%s has an empty DOLT_ADD staging exemption reason", key)
			}
			seenExempt[key] = true
			continue
		}
		checked++
		if !f.CallsAnyOf(versionStagingFunnels) {
			t.Errorf("%s stages tables with DOLT_ADD but never routes the set through withVersionedHistoryTables "+
				"(or marks it with MarkVersionedHistoryDirty) — when versioned history is active the version rows this "+
				"transaction minted stay in the working set, outside the Dolt commit that describes them: unreplicated, "+
				"and for the append-only tables unrecoverable. Route the list through s.withVersionedHistoryTables, "+
				"or add it to doltAddStagingExemptions with a reason.", key)
		}
	}

	if checked == 0 {
		t.Fatal("guard found no DOLT_ADD staging path — literal detection or parsing changed; the guard is not actually running")
	}
	for key := range doltAddStagingExemptions {
		if !seenExempt[key] {
			t.Errorf("DOLT_ADD staging exemption %q no longer matches a DOLT_ADD path — remove it", key)
		}
	}
}

// callsDoltAdd reports whether f passes a DOLT_ADD statement to anything. It
// deliberately asks nothing about the SHAPE of the call: a whole-working-set
// DOLT_ADD('-A') needs no funnel, but making that an automatic pass would let a
// future fixed-list site hide behind a shape test. Such a site takes an
// exemption line naming the reason instead.
func callsDoltAdd(f *journalscan.FuncInfo) bool {
	for _, call := range f.Calls {
		for _, arg := range call.Args {
			if strings.Contains(arg, "DOLT_ADD") {
				return true
			}
		}
	}
	return false
}

// approvedDoltPublishers lists every function in this package allowed to pass
// a DOLT_ADD or DOLT_COMMIT statement — a write to Dolt's version history —
// each with the written reason it owns one. GH#5752 deleted the in-tx staging
// helper doltAddAndCommitInTx: minting history inside the still-open SQL
// transaction stages from the session's BEGIN-time root, so under concurrent
// writers the minted commit writes every row changed in the meantime back to
// its BEGIN-time value (the lost-update hazard), and on an ambiguous SQL
// commit it mints a Dolt revision for a transaction the server may have rolled
// back. Every SQL-committed mutation now publishes through the post-tx
// publisher (publishCommittedOperation / publishCommittedTables ->
// doltAddAndCommitPostTx), strictly after its transaction committed; the six
// entries below are the only intentional direct publishers left, and none of
// them runs inside a caller's open SQL transaction.
var approvedDoltPublishers = map[string]string{
	"DoltStore.stageAndCommit": "the single staging funnel (doltAddAndCommit -> stageAndCommit) every post-tx publication routes through; owns the version-commit deferral check and the empty-staged-set guard, and pins its own connection",
	"DoltStore.commitWorkingSet": "the explicit working-set commit point (CommitPending / bd dolt commit): by design stages the dolt_status-derived dirty set rather than a mutation's fixed table list",
	"DoltStore.concludeOpenMerge": "merge-settlement conclusion: commits the merge a resolution left open, on the caller's pinned connection because dolt's merge state is session state",
	"DoltStore.CommitWithConfig": "explicit commit that intentionally includes the config table (after bd config set / bd init / bd rename-prefix)",
	"DoltStore.CommitAll": "the explicit commit-everything entry point (bd vc commit / bd dolt commit), which an operator asks for by name",
	"DoltStore.commitFilteredStaging": "federation peer-sync: commits the staged filtered state after the private-type exclusion rebuild",
}

// inTxStagerNames is the deleted in-tx staging helper, by the names a
// resurrection would use: its own name (a definition) or a call to it.
var inTxStagerNames = map[string]bool{"doltAddAndCommitInTx": true}

// inTxDoltStagingSurvivors returns the functions that would resurrect the
// deleted in-tx staging helper: a definition of it, or any function calling it.
func inTxDoltStagingSurvivors(fns map[string]*journalscan.FuncInfo) []string {
	var bad []string
	for key, f := range fns {
		if inTxStagerNames[f.Name] {
			bad = append(bad, key+" defines the deleted in-tx staging helper")
			continue
		}
		if f.CallsAnyOf(inTxStagerNames) {
			bad = append(bad, key+" calls the deleted in-tx staging helper")
		}
	}
	sort.Strings(bad)
	return bad
}

// unapprovedDoltPublishers returns every function that passes a DOLT_ADD or
// DOLT_COMMIT statement without an entry in the approved map.
func unapprovedDoltPublishers(fns map[string]*journalscan.FuncInfo, approved map[string]string) []string {
	var bad []string
	for key, f := range fns {
		if _, ok := approved[key]; f.OwnDoltVersionCall && !ok {
			bad = append(bad, key)
		}
	}
	sort.Strings(bad)
	return bad
}

// staleDoltPublisherExemptions returns approved entries that have rotted: the
// named function no longer exists, or no longer passes a Dolt version
// statement — an exemption that matches nothing hides real coverage while
// suggesting the guard is policed.
func staleDoltPublisherExemptions(fns map[string]*journalscan.FuncInfo, approved map[string]string) []string {
	var bad []string
	for key := range approved {
		f, ok := fns[key]
		if !ok {
			bad = append(bad, key+" (function not found)")
			continue
		}
		if !f.OwnDoltVersionCall {
			bad = append(bad, key+" (passes no DOLT_ADD/DOLT_COMMIT statement)")
		}
	}
	sort.Strings(bad)
	return bad
}

// TestDoltVersionStatementsLiveOnlyInApprovedPublishers is the GH#5752
// structural guard: the in-tx staging helper stays deleted, and DOLT_ADD /
// DOLT_COMMIT statements — writes to Dolt's version history — appear in
// exactly the approved publisher functions. A new SQL-committed mutation must
// publish through the post-tx publisher, never mint history inside its own
// open transaction; an explicit commit entry point gets its own line here,
// with a reason.
func TestDoltVersionStatementsLiveOnlyInApprovedPublishers(t *testing.T) {
	doltFns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}

	if bad := inTxDoltStagingSurvivors(doltFns); len(bad) > 0 {
		t.Errorf("the in-tx staging helper survived: %v — publish through the post-tx publisher instead; a Dolt commit minted inside an open SQL transaction stages from the BEGIN-time root and reverts concurrent writers' rows", bad)
	}

	publishers := unapprovedDoltPublishers(doltFns, approvedDoltPublishers)
	if len(publishers) > 0 {
		t.Errorf("DOLT_ADD/DOLT_COMMIT outside the approved publishers: %v — route the mutation through publishCommittedOperation/publishCommittedTables, or add the function to approvedDoltPublishers with a written reason", publishers)
	}

	if bad := staleDoltPublisherExemptions(doltFns, approvedDoltPublishers); len(bad) > 0 {
		t.Errorf("stale approvedDoltPublishers entries: %v — delete or update the entry so the map keeps describing live publishers", bad)
	}

	// Non-vacuity: the package must still contain approved publishers holding
	// Dolt version statements, or the checks above pass by seeing nothing.
	live := map[string]bool{}
	for key, f := range doltFns {
		if f.OwnDoltVersionCall {
			live[key] = true
		}
	}
	for key := range approvedDoltPublishers {
		if !live[key] {
			t.Errorf("approved publisher %s passes no DOLT_ADD/DOLT_COMMIT statement — the guard is not seeing the package it polices", key)
		}
	}
	if len(live) == 0 {
		t.Fatal("guard found no DOLT_ADD/DOLT_COMMIT statement in the package — literal detection or parsing changed; the guard is not actually running")
	}
}

// TestDoltPublisherGuardRejectsSyntheticViolations keeps the guard above from
// being vacuous: each checker must reject a hand-built violation the real
// package no longer contains (the deleted helper, an unapproved publisher, a
// rotted exemption).
func TestDoltPublisherGuardRejectsSyntheticViolations(t *testing.T) {
	fns := map[string]*journalscan.FuncInfo{
		"DoltStore.legit":   {Recv: "DoltStore", Name: "legit", OwnDoltVersionCall: true},
		"DoltStore.smuggle": {Recv: "DoltStore", Name: "smuggle", OwnDoltVersionCall: true},
		"DoltStore.retired": {Recv: "DoltStore", Name: "retired"},
	}
	approved := map[string]string{
		"DoltStore.legit":   "still publishing",
		"DoltStore.retired": "exemption that outlived its function's version statements",
		"DoltStore.gone":    "exemption for a function that no longer exists",
	}

	t.Run("unapproved publisher is flagged", func(t *testing.T) {
		if bad := unapprovedDoltPublishers(fns, approved); len(bad) != 1 || bad[0] != "DoltStore.smuggle" {
			t.Fatalf("unapprovedDoltPublishers = %v, want [DoltStore.smuggle]", bad)
		}
	})

	t.Run("stale exemptions are flagged", func(t *testing.T) {
		bad := staleDoltPublisherExemptions(fns, approved)
		want := []string{
			"DoltStore.gone (function not found)",
			"DoltStore.retired (passes no DOLT_ADD/DOLT_COMMIT statement)",
		}
		if len(bad) != 2 || bad[0] != want[0] || bad[1] != want[1] {
			t.Fatalf("staleDoltPublisherExemptions = %v, want %v", bad, want)
		}
	})

	t.Run("in-tx helper definition or call is flagged", func(t *testing.T) {
		inTxFns := map[string]*journalscan.FuncInfo{
			"DoltStore.doltAddAndCommitInTx": {Recv: "DoltStore", Name: "doltAddAndCommitInTx"},
			"DoltStore.migrationLeftover":    {Recv: "DoltStore", Name: "migrationLeftover", IdentCalls: []string{"doltAddAndCommitInTx"}},
			"DoltStore.innocent":             {Recv: "DoltStore", Name: "innocent"},
		}
		want := []string{
			"DoltStore.doltAddAndCommitInTx defines the deleted in-tx staging helper",
			"DoltStore.migrationLeftover calls the deleted in-tx staging helper",
		}
		bad := inTxDoltStagingSurvivors(inTxFns)
		if len(bad) != 2 || bad[0] != want[0] || bad[1] != want[1] {
			t.Fatalf("inTxDoltStagingSurvivors = %v, want %v", bad, want)
		}
	})
}
