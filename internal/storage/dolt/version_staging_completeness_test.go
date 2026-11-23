package dolt

import (
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
