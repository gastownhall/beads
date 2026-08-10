package db

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// TestEveryRepositoryMutatorJournals is the completeness guard for the
// unit-of-work write plumbing. Unlike the DoltStorage plumbing, several of this
// package's repository mutators reimplement their own SQL instead of routing
// through issueops, so the issueops seam alone does NOT cover them.
//
// The guard is STRUCTURAL, not name-based: it does not trust a verb regex on the
// method name or a broad "Insert" coverage token (either of which could let a
// mutator named off-pattern, or a false delegation, ship un-journaled). Instead
// it detects a mutator by BEHAVIOR — any method on a work-bead repository whose
// body (directly, or through a free function it calls) executes an INSERT /
// UPDATE / DELETE against a work-bead table — and asserts each one journals.
//
// A new reimplemented mutator that writes a bead table and forgets to journal
// fails this test even if it is named nothing like a mutator.
func TestEveryRepositoryMutatorJournals(t *testing.T) {
	// Repository receiver types that own work-bead state. issue/dependency/label
	// reimplement issueops SQL; comment writes the comments bead table.
	beadReceivers := map[string]bool{
		"issueSQLRepositoryImpl":      true,
		"dependencySQLRepositoryImpl": true,
		"labelSQLRepositoryImpl":      true,
		"commentSQLRepositoryImpl":    true,
	}

	// Bead-mutating methods that legitimately do NOT journal, each with a reason.
	// The staleness check below fails if any of these stops being a bead mutator,
	// so a rename or refactor cannot silently strand an exemption.
	exempt := map[string]string{
		"labelSQLRepositoryImpl.DeleteAllForIDs":              "bulk label cleanup runs under a parent issue delete; the surviving journal record is the node delete",
		"dependencySQLRepositoryImpl.markDirectBlockedSource": "maintains the derived is_blocked column as a side effect of a journaled dependency insert",
	}

	// Direct emit helpers and the issueops functions that journal the mutation
	// they perform.
	//
	// The is_blocked recompute helpers are deliberately NOT listed even though
	// they do emit: they journal only the derived readiness flips of AFFECTED
	// neighbors, so counting them would let any mutator that ends with a
	// recompute — which is most of them — pass while never recording its own
	// change.
	emitCalls := map[string]bool{
		"RecordEventInTx":                       true,
		"RecordDeleteInTx":                      true,
		"RecordDepEventInTx":                    true,
		"RecordCommentEventInTx":                true,
		"RecordDependencyRemovalsForTableInTx":  true,
		"RecordDependencyRemovalsForIssuesInTx": true,
		"CloseIssueInTx":                        true,
		"CloseIssueCheckedInTx":                 true,
		"ReopenIssueInTx":                       true,
		"ClaimIssueInTx":                        true,
		"ClaimReadyIssueInTx":                   true,
		"UnclaimIssueInTx":                      true,
		"UnclaimIssueIfAssigneeInTx":            true,
		"ReclaimExpiredLeasesInTx":              true,
		"PromoteFromEphemeralInTx":              true,
		"MoveIssuePersistenceInTx":              true,
		"UpdateIssueInTx":                       true,
		"AddIssueCommentInTx":                   true,
		"ImportIssueCommentInTx":                true,
		"AddLabelInTx":                          true,
		"RemoveLabelInTx":                       true,
		"WakeExpiredDefersInTx":                 true,
	}

	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse domain/db package: %v", err)
	}

	// beadDML: fn writes a bead table directly, or calls a free function that
	// transitively does (e.g. Insert -> insertIssueRow). Only free-function
	// (bare-identifier) calls propagate DML, so an events/leases write reached
	// through a selector (r.events.Record) does not count as a bead mutation.
	beadDML := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.OwnBeadDML },
		func(f *journalscan.FuncInfo) []string { return f.IdentCalls })

	// emits: fn calls an emit helper directly, or calls any package function
	// (free or method-by-name) that emits.
	emits := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(emitCalls) },
		func(f *journalscan.FuncInfo) []string { return f.AllCallNames() })

	seenExempt := map[string]bool{}
	var checked int
	for name, f := range fns {
		if !beadReceivers[f.Recv] || !beadDML[name] {
			continue
		}
		exemptKey := f.Recv + "." + f.Name
		if reason, ok := exempt[exemptKey]; ok {
			if reason == "" {
				t.Errorf("%s has an empty exemption reason", exemptKey)
			}
			seenExempt[exemptKey] = true
			continue
		}
		checked++
		if !emits[name] {
			t.Errorf("%s.%s writes a work-bead table but does not journal: it neither calls a Record*InTx emit helper, nor delegates to an emitting issueops function, nor to a sibling mutator that journals", f.Recv, f.Name)
		}
	}

	if checked == 0 {
		t.Fatal("guard found no bead-mutating repository methods — receiver names, DML detection, or parsing changed; the completeness guard is not actually running")
	}
	// Staleness: every exemption must still name a real bead mutator.
	for m := range exempt {
		if !seenExempt[m] {
			t.Errorf("exemption %q no longer matches a bead-mutating repository method — remove it", m)
		}
	}
}
