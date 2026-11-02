package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// scopeCalls are the ways a function binds journal activation to the concrete
// transaction it just began: the raw-transaction helper that begins it scoped,
// or the issueops primitive it wraps (commitWriteTxWith and
// runDoltTransactionRecording call the latter directly).
var scopeCalls = map[string]bool{
	"beginJournalScopedTx":          true,
	"ScopeEventsJournalTransaction": true,
}

// txBegins are the ways a function mints its own write transaction.
var txBegins = map[string]bool{
	"BeginTx":              true,
	"beginJournalScopedTx": true,
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
// source, every exported function that reaches a Record*InTx emit helper: the
// issueops mutators that can produce a journal row. It is deliberately the
// PLAIN fixpoint, with no readiness-family exclusion: any reachable journal
// write matters, including the derived is_blocked updates a recompute emits.
// A new journaling mutator therefore extends the guards' reach automatically.
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
//
// The mutator set is not hand-listed either: see
// journalingMutatorsFromIssueops.
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
		// are checked here in their own right.
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
		if !f.CallsAnyOf(txBegins) || !f.CallsAnyOf(journalingMutators) {
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
			t.Errorf("%s begins its own transaction and reaches journaling issueops code, but never scopes the journal on it (beginJournalScopedTx) — "+
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

// journalWriterAdmittingScopes are the only functions allowed to call
// issueops.ScopeEventsJournalTransaction directly: each takes the
// journal-writer lock on the session before it begins the transaction it scopes
// (gastownhall/beads#6117).
var journalWriterAdmittingScopes = map[string]bool{
	"DoltStore.commitWriteTxWith":           true,
	"DoltStore.runDoltTransactionRecording": true,
	"DoltStore.beginJournalScopedTx":        true,
}

// TestEveryJournalScopeIsAdmittedAsAJournalWriter is the converse of
// TestEveryRawTxJournalScopeIsScopedOrExempt: a transaction scoped for the
// journal must also queue on the journal-writer lock, or it overlaps another
// journaled writer and one of them loses the bd_events_seq conflict (1213) at
// commit. A raw transaction scopes through beginJournalScopedTx, which admits it;
// a direct ScopeEventsJournalTransaction call anywhere else scopes without
// admission.
func TestEveryJournalScopeIsAdmittedAsAJournalWriter(t *testing.T) {
	doltFns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}
	direct := map[string]bool{"ScopeEventsJournalTransaction": true}
	seen := map[string]bool{}
	for key, f := range doltFns {
		if !f.CallsAnyOf(direct) {
			continue
		}
		if !journalWriterAdmittingScopes[key] {
			t.Errorf("%s scopes the events journal on a transaction directly, so that transaction never takes the journal-writer lock — "+
				"begin it with beginJournalScopedTx instead", key)
			continue
		}
		seen[key] = true
	}
	for key := range journalWriterAdmittingScopes {
		if !seen[key] {
			t.Errorf("%s no longer scopes the journal directly — remove it from journalWriterAdmittingScopes", key)
		}
	}
}

// noJournalFunnels are the store's journal-free write funnels: no
// journal-writer lock, and an emit inside is refused (gastownhall/beads#6117).
var noJournalFunnels = map[string]bool{
	"withRetryTxNoJournal": true,
	"withWriteTxNoJournal": true,
}

// noJournalFunnelAudit names every store site routed through a NoJournal
// funnel, each with the no-emit audit that justified the routing. The reasons
// mirror each site's own doc comment; this map is their structural defense.
var noJournalFunnelAudit = map[string]string{
	"DoltStore.SetConfig":              "config keys are not bead state: no journal event exists to mint",
	"DoltStore.DeleteConfig":           "config keys are not bead state (see SetConfig)",
	"DoltStore.SetMetadata":            "metadata writes carry no journal op (see SetConfig)",
	"DoltStore.SetLocalMetadata":       "clone-local, dolt-ignored state: no journal op (see SetConfig)",
	"DoltStore.ClearRepoMtime":         "derived filesystem timing cache, reconstructible by a scan",
	"DoltStore.SetRepoMtime":           "derived filesystem timing cache, reconstructible by a scan",
	"DoltStore.ApplyCompaction":        "compaction is a documented journal gap: it records no journal event",
	"DoltStore.SnapshotIssue":          "compaction snapshot archive: a documented journal gap (see ApplyCompaction)",
	"DoltStore.RestoreFromSnapshot":    "compaction restore: a documented journal gap (see ApplyCompaction)",
	"DoltStore.RecordProvenanceEvent":  "a provenance event is not a journal event",
	"DoltStore.PruneEventsJournal":     "the journal maintains itself: a prune records no event, and must not queue behind the writers whose rows it prunes",
	"DoltStore.RunEventsMaintenanceTx": "auto-prune maintenance (single attempt): its callbacks read the prune state and delete journal rows, recording no event",
	"bootstrapper.Bootstrap":           "bootstrap writes config and metadata rows, which record no journal event (see SetConfig)",
	"memories.Remember":                "a memory is a config row and records no journal event",
	"memories.Forget":                  "a memory is a config row and records no journal event",
}

// TestNoJournalFunnelSitesStayAudited is the structural guard for the
// NoJournal funnels' membership (gastownhall/beads#6117). NoJournal routing is
// a reviewed no-emit audit: a site routed there takes no journal-writer lock,
// so a wrong audit removes a writer from the serialization the lock exists
// for, and its emits are refused at runtime. The guard fails when a funnel
// site is unlisted (membership must be a reviewed decision), when an entry
// stops matching (an audit cannot rot), and when a listed site reaches a
// journaling mutator, in issueops or through memoryops (the no-emit claim
// is false: route the site back through the admitted funnel).
func TestNoJournalFunnelSitesStayAudited(t *testing.T) {
	journalingMutators := journalingMutatorsFromIssueops(t)

	// The memory routes run their bodies in memoryops: a memoryops function
	// that reaches a journaling issueops mutator journals too.
	memoryFns, err := journalscan.ParsePackage("../memoryops")
	if err != nil {
		t.Fatalf("parse memoryops package: %v", err)
	}
	memoryEmits := journalscan.Fixpoint(memoryFns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(journalingMutators) },
		func(f *journalscan.FuncInfo) []string { return f.AllCallNames() })
	journaling := map[string]bool{}
	for name := range journalingMutators {
		journaling[name] = true
	}
	for key, f := range memoryFns {
		if f.Exported && memoryEmits[key] {
			journaling[f.Name] = true
		}
	}

	doltFns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}
	seen := map[string]bool{}
	for key, f := range doltFns {
		if noJournalFunnels[f.Name] || !f.CallsAnyOf(noJournalFunnels) {
			continue
		}
		reason, ok := noJournalFunnelAudit[key]
		if !ok {
			t.Errorf("%s routes through a NoJournal funnel but is not in noJournalFunnelAudit: membership is a reviewed no-emit audit; add the site with its reason, or route it through the admitted funnel", key)
			continue
		}
		if reason == "" {
			t.Errorf("%s has an empty audit reason", key)
		}
		seen[key] = true
		if f.CallsAnyOf(journaling) {
			t.Errorf("%s is audited journal-free but reaches a journaling mutator: route it back through the admitted funnel so it is admitted and journals like every emitting writer", key)
		}
	}
	if len(seen) == 0 {
		t.Fatal("guard found no NoJournal funnel sites: funnel-call detection or parsing changed; the guard is not actually running")
	}
	for key := range noJournalFunnelAudit {
		if !seen[key] {
			t.Errorf("audit entry %q no longer routes through a NoJournal funnel: remove it, or restore the site's routing", key)
		}
	}
}
