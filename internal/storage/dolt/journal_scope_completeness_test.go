package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// scopeCalls are the ways a function binds journal activation to the concrete
// transaction it just began: the store helper, or the issueops primitive it
// wraps (runDoltTransactionRecording calls the latter directly).
var scopeCalls = map[string]bool{
	"scopeEventsJournalTransaction": true,
	"ScopeEventsJournalTransaction": true,
}

// rawTxScopeExemptions are functions that mint a raw transaction reaching
// journaling code but legitimately do NOT scope it, each with a reason. The
// staleness check below fails if one stops being flagged, so an exemption
// cannot rot.
var rawTxScopeExemptions = map[string]string{
	// Derived, reconstructible state. These transactions run the is_blocked
	// maintenance passes (admin repair and the post-merge recompute), which
	// recompute a projection from the dependency graph rather than mutate bead
	// state. A consumer that replays the journal recomputes the same value from
	// the same edges, so recording the repair would add rows carrying no
	// information a replay does not already have. This is the same reasoning
	// that keeps the readiness-recompute family from propagating emission in
	// the issueops completeness guard.
	"DoltStore.recomputeBlockedTxWithDB": "post-merge is_blocked recompute: derived state reconstructible from the dependency graph, so a replay consumer needs no record of it",
	"DoltStore.commitFilteredStaging":    "federation-filter is_blocked recompute over staged state: derived state reconstructible from the dependency graph, so a replay consumer needs no record of it",
}

// journalingMutatorsFromIssueops derives, from the issueops package's own
// source, every exported function that reaches a Record*InTx emit helper —
// the set of issueops mutators that can produce a journal row. Deliberately
// the PLAIN fixpoint, with no readiness-family exclusion: any reachable
// journal write matters, including the derived is_blocked updates a
// recompute emits. A new journaling mutator therefore extends the guards'
// reach automatically.
func journalingMutatorsFromIssueops(t *testing.T) map[string]bool {
	t.Helper()
	issueFns, err := journalscan.ParsePackage("../issueops")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}
	emitHelpers := map[string]bool{
		"RecordEventInTx":        true,
		"RecordDeleteInTx":       true,
		"RecordDepEventInTx":     true,
		"RecordCommentEventInTx": true,
		"insertEventRow":         true,
	}
	issueEmits := journalscan.Fixpoint(issueFns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(emitHelpers) },
		func(f *journalscan.FuncInfo) []string { return f.AllCallNames() })

	journalingMutators := map[string]bool{}
	for key, f := range issueFns {
		if f.Exported && issueEmits[key] {
			journalingMutators[f.Name] = true
		}
	}
	if len(journalingMutators) == 0 {
		t.Fatal("derived no journaling issueops mutators — the emit analysis changed and this guard is not actually running")
	}
	return journalingMutators
}

// TestEveryRawTxJournalScopeIsScopedOrExempt is the structural guard for the
// OTHER half of journal coverage. The issueops guards prove the mutation seam
// emits; this proves the store actually turns emission ON for the transaction
// the mutation runs in.
//
// It exists because that half was enumerated by hand twice and was wrong both
// times: the first sweep missed eight raw-transaction sites, and a later review
// found two more (the wisp slot/metadata writes), each silently journaling
// nothing while the permanent-issue branch of the same public method journaled
// fine. Scoping is invisible when absent — the code runs, the mutation commits,
// and the journal is simply empty — so nothing but a structural check can
// defend it.
func TestEveryRawTxJournalScopeIsScopedOrExempt(t *testing.T) {
	journalingMutators := journalingMutatorsFromIssueops(t)

	doltFns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}

	seenExempt := map[string]bool{}
	var checked int
	for key, f := range doltFns {
		// In scope: a function that mints its own transaction AND hands it to a
		// journaling mutator IN ITS OWN BODY. A function given a tx by
		// withWriteTx/runDoltTransactionRecording inherits their scoping, and those two
		// are checked here in their own right. Since #6117 the raw sites mint
		// their transaction through beginJournalWriteTx (journal-writer
		// admission), so that helper counts as minting exactly like BeginTx.
		//
		// Reachability is deliberately DIRECT rather than a call-graph fixpoint.
		// A fixpoint over selector names is far too loose in a package this size
		// — it drags in every generic helper that merely wraps a callback
		// (withReadTx, execContext) and drowns the signal. Direct calls are also
		// the exact shape of the class this guard exists for: all ten scoping
		// misses found so far were bodies that began a tx and called an issueops
		// mutator on it a few lines later. The cost is that a tx passed down to
		// an unexported sibling that mutates would slip through; no such shape
		// exists today, and the journal coverage tests cover the behavior.
		if !f.CallsAnyOf(map[string]bool{"BeginTx": true, "beginJournalWriteTx": true}) || !f.CallsAnyOf(journalingMutators) {
			continue
		}
		if reason, ok := rawTxScopeExemptions[key]; ok {
			if reason == "" {
				t.Errorf("%s has an empty exemption reason", key)
			}
			seenExempt[key] = true
			continue
		}
		checked++
		// Scoping must be DIRECT: activation binds to one concrete tx, so the
		// function that began it is the only one that can bind it.
		if !f.CallsAnyOf(scopeCalls) {
			t.Errorf("%s begins its own transaction and reaches journaling issueops code, but never calls scopeEventsJournalTransaction on it — "+
				"every mutation in that transaction journals NOTHING. Scope it (see wisps.go for the idiom), or add it to rawTxScopeExemptions with a reason.", key)
		}
	}

	if checked == 0 {
		t.Fatal("guard found no raw-transaction mutation paths — BeginTx detection or parsing changed; the guard is not actually running")
	}
	for key := range rawTxScopeExemptions {
		if !seenExempt[key] {
			t.Errorf("exemption %q no longer matches a raw-transaction mutation path — remove it", key)
		}
	}
}

// noJournalFunnelAudit names every store site routed through the
// withRetryTxNoJournal funnel (#6117), each with the no-emit audit that
// justified the routing: the write produces no journal event, so it takes no
// journal-writer admission lock and its transaction is scoped FORBIDDEN to
// emit instead — an unexpected emit is refused before it allocates a sequence
// and the commit rejects the attempt even when the callback ignores the emit
// error. The reasons mirror each site's own doc comment; this map is the
// structural defense of those audits.
var noJournalFunnelAudit = map[string]string{
	"DoltStore.SetConfig":          "config keys are not bead state: no journal event exists to mint",
	"DoltStore.DeleteConfig":       "config keys are not bead state (see SetConfig)",
	"DoltStore.SetMetadata":        "metadata writes carry no journal op — wireEventOps has no metadata entry (see SetConfig)",
	"DoltStore.PruneEventsJournal": "the journal maintains itself: a prune minting journal rows would be self-referential, and a prune must not queue behind the writers whose rows it prunes",
	"DoltStore.ClearRepoMtime":     "derived filesystem timing cache, reconstructible by a scan (see SetConfig)",
	"DoltStore.SetRepoMtime":       "derived filesystem timing cache, reconstructible by a scan (see SetConfig)",
}

// TestNoJournalFunnelSitesStayAudited is the structural guard for the
// withRetryTxNoJournal funnel's membership (#6117), the third half of journal
// coverage after the mutation seam (issueops guards) and scoping
// (TestEveryRawTxJournalScopeIsScopedOrExempt). NoJournal routing is a
// reviewed no-emit audit, not a convenience: a site routed there takes no
// journal-writer admission lock, so a wrong audit silently removes a writer
// from the serialization the lock exists for — and scoping that writer
// journal-forbidden would refuse its emits at commit. The guard fails when a
// funnel site is unlisted (membership must be a reviewed decision), when an
// entry stops matching (an audit cannot rot), and when a listed site reaches
// a journaling issueops mutator (the no-emit claim has become false: the
// site must move back to the plain withRetryTx funnel and admit like every
// emitting writer).
func TestNoJournalFunnelSitesStayAudited(t *testing.T) {
	journalingMutators := journalingMutatorsFromIssueops(t)

	doltFns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}

	seen := map[string]bool{}
	for key, f := range doltFns {
		if !f.CallsAnyOf(map[string]bool{"withRetryTxNoJournal": true}) {
			continue
		}
		reason, ok := noJournalFunnelAudit[key]
		if !ok {
			t.Errorf("%s routes through withRetryTxNoJournal but is not in noJournalFunnelAudit — funnel membership is a reviewed no-emit audit; add the site with its reason, or route it through the plain withRetryTx funnel instead", key)
			continue
		}
		if reason == "" {
			t.Errorf("%s has an empty audit reason", key)
		}
		seen[key] = true
		if f.CallsAnyOf(journalingMutators) {
			t.Errorf("%s is audited no-journal but reaches a journaling issueops mutator — the audit is false: route it back through withRetryTx so it admits and journals like every emitting writer", key)
		}
	}
	if len(seen) == 0 {
		t.Fatal("guard found no withRetryTxNoJournal sites — funnel-call detection or parsing changed; the guard is not actually running")
	}
	for key := range noJournalFunnelAudit {
		if !seen[key] {
			t.Errorf("audit entry %q no longer routes through withRetryTxNoJournal — remove it, or restore the site's funnel routing", key)
		}
	}
}
