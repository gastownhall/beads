package conformance

import (
	"context"
	"testing"
	"time"
)

// This file holds the contract for R16.1 (gastownhall/beads#6133,
// be-hs42e.1 §5-§7, FR-02, FR-03): enforcement that spans more than one
// record, which ExpectedRevisionFixture's per-record CompareAndSetVersion
// structurally cannot see.
//
// GuardedWrite AND ExpectedRevisionFixture.CompareAndSetVersion SHARE THE
// EXACT SAME TYPE, PerRecordCASWrite, declared once in
// expected_revision_contract.go. R16.1-a's boundary claim is literally that
// the second is the first, called twice against records that individually
// pass but jointly violate a store invariant — sharing the type makes that
// claim structural, checked by the compiler, rather than merely asserted in
// a doc comment.
//
// TWO CONCRETE SUBJECTS ARE REQUIRED BY FR-02 RATHER THAN INVENTED HERE:
// Memory key/alias uniqueness (#5877 R2) and graph-edge
// MaximumEndpointMultiplicity. MemoryKeyAliasWrite and GraphEdgeWrite exist
// so the cases named for them construct a REAL joint violation in each
// subject's own vocabulary, rather than a synthetic one this file made up.
//
// EVERY HOOK BELOW IS INDEPENDENTLY NILABLE — see the package-level note in
// expected_revision_contract.go. Every case nil-checks every hook it uses
// and SKIPS BY NAME when one is missing.

// RecordWrite is one record's half of a multi-record write submitted to
// EnforceCrossRecordInvariant.
type RecordWrite struct {
	ID       string
	Expected *Address
	Patch    map[string]any
}

// InvariantResult is the outcome of a call to EnforceCrossRecordInvariant.
type InvariantResult struct {
	Accepted         bool
	InvariantRefusal *InvariantRefusal
}

// InvariantRefusal names the specific store invariant a multi-record write
// violated (R16.1-c) — never a bare boolean, so a caller (and a test) can
// tell which invariant fired.
type InvariantRefusal struct {
	InvariantName string
}

// LeaseHandle is an advisory lease over subject. It is observed by
// convention only (FR-03): holding one does not, by itself, change what any
// per-record or invariant-enforcing write accepts.
type LeaseHandle struct {
	Subject   string
	ExpiresAt time.Time
	// Release ends the lease early. A nil Release means the fixture expects
	// the lease to simply expire; cases treat a nil Release as optional
	// cleanup, not a required call.
	Release func(ctx context.Context) error
}

// CrossRecordInvariantFixture supplies the capabilities under test for
// R16.1: enforcement that spans more than one record, and the advisory
// lease primitive FR-03 says is insufficient on its own to provide it.
type CrossRecordInvariantFixture struct {
	IssuePrefix string

	// GuardedWrite is a per-record CAS write over the SAME type as
	// ExpectedRevisionFixture.CompareAndSetVersion (PerRecordCASWrite) — see
	// this file's package doc comment for why. A nil hook means this
	// backend exposes no per-record guard to construct the boundary case
	// against, and cases that need one skip with that reason.
	GuardedWrite PerRecordCASWrite

	// EnforceCrossRecordInvariant evaluates writes as one joint operation
	// and refuses the whole batch, with a named InvariantRefusal, if
	// accepting it would violate a store invariant spanning more than one
	// of the given records. A nil hook means this backend has not wired
	// store-invariant enforcement yet, and cases that need one skip with
	// that reason.
	EnforceCrossRecordInvariant func(ctx context.Context, writes []RecordWrite) (InvariantResult, error)

	// AcquireAdvisoryLease acquires a convention-only lease over subject for
	// up to ttl. A nil hook means this backend has no lease primitive to
	// test as an insufficient guard, and cases that need one skip with that
	// reason.
	AcquireAdvisoryLease func(ctx context.Context, subject string, ttl time.Duration) (LeaseHandle, error)

	// CountHistoryForSubject reports how many durable history entries exist
	// for subject. A nil hook means this backend cannot observe history by
	// subject, and cases that need one skip with that reason.
	CountHistoryForSubject func(ctx context.Context, subject string) (int, error)

	// MemoryKeyAliasWrite writes memoryID's key/alias pair unconditionally,
	// in the shape of a per-record write (FR-02's first named subject,
	// #5877 R2). A nil hook means this backend exposes no Memory key/alias
	// write to probe, and the case that needs it skips with that reason.
	MemoryKeyAliasWrite func(ctx context.Context, memoryID, key, alias string) (ExpectedRevisionResult, error)

	// GraphEdgeWrite writes a graph edge unconditionally, in the shape of a
	// per-record write (FR-02's second named subject,
	// MaximumEndpointMultiplicity). A nil hook means this backend exposes no
	// graph edge write to probe, and the case that needs it skips with that
	// reason.
	GraphEdgeWrite func(ctx context.Context, fromID, toID, edgeType string) (ExpectedRevisionResult, error)
}

// RunCrossRecordInvariantSurvivesTwoPassingPerRecordGuards pins R16.1-a's
// boundary claim directly: two writes, each individually valid under
// GuardedWrite (== CompareAndSetVersion), that JOINTLY violate a store
// invariant neither call's own precondition can see. Both must be Accepted
// through GuardedWrite alone — the whole point of R16.1 is that this
// per-record guard cannot catch what only spans records.
func RunCrossRecordInvariantSurvivesTwoPassingPerRecordGuards(t *testing.T, ctx context.Context, fixture CrossRecordInvariantFixture) {
	t.Helper()
	if fixture.GuardedWrite == nil {
		t.Skip("this backend exposes no per-record guard to construct the boundary case against (GuardedWrite is nil)")
	}
	shared := fixture.IssuePrefix + "-crossrec-shared-alias"

	first, err := fixture.GuardedWrite(ctx, fixture.IssuePrefix+"-crossrec-a", nil, map[string]any{"alias": shared})
	if err != nil {
		t.Fatalf("GuardedWrite(a): %v", err)
	}
	if !first.Accepted {
		t.Fatalf("GuardedWrite(a) claiming alias %q was refused (Refusal=%+v), want Accepted: a per-record guard cannot see a joint violation", shared, first.Refusal)
	}

	second, err := fixture.GuardedWrite(ctx, fixture.IssuePrefix+"-crossrec-b", nil, map[string]any{"alias": shared})
	if err != nil {
		t.Fatalf("GuardedWrite(b): %v", err)
	}
	if !second.Accepted {
		t.Fatalf("GuardedWrite(b) claiming the SAME alias %q was refused (Refusal=%+v), want Accepted (R16.1-a: per-record CAS structurally cannot catch a cross-record conflict)", shared, second.Refusal)
	}
}

// RunStoreInvariantTransactionScopesExactlyTheSpanningRecords pins R16.1-b:
// EnforceCrossRecordInvariant refuses a batch that jointly violates an
// invariant, and — checked against an unrelated, non-conflicting write in
// the same call shape — must not refuse more broadly than the records that
// actually span the violation.
func RunStoreInvariantTransactionScopesExactlyTheSpanningRecords(t *testing.T, ctx context.Context, fixture CrossRecordInvariantFixture) {
	t.Helper()
	if fixture.EnforceCrossRecordInvariant == nil {
		t.Skip("this backend has not wired store-invariant enforcement yet (EnforceCrossRecordInvariant is nil)")
	}
	shared := fixture.IssuePrefix + "-crossrec-scope-alias"
	spanning := []RecordWrite{
		{ID: fixture.IssuePrefix + "-crossrec-scope-a", Patch: map[string]any{"alias": shared}},
		{ID: fixture.IssuePrefix + "-crossrec-scope-b", Patch: map[string]any{"alias": shared}},
	}

	var beforeA, beforeB int
	if fixture.CountHistoryForSubject != nil {
		var err error
		beforeA, err = fixture.CountHistoryForSubject(ctx, spanning[0].ID)
		if err != nil {
			t.Fatalf("CountHistoryForSubject(%s) before the refused batch: %v", spanning[0].ID, err)
		}
		beforeB, err = fixture.CountHistoryForSubject(ctx, spanning[1].ID)
		if err != nil {
			t.Fatalf("CountHistoryForSubject(%s) before the refused batch: %v", spanning[1].ID, err)
		}
	}

	result, err := fixture.EnforceCrossRecordInvariant(ctx, spanning)
	if err != nil {
		t.Fatalf("EnforceCrossRecordInvariant(spanning): %v", err)
	}
	if result.Accepted {
		t.Fatal("EnforceCrossRecordInvariant accepted two writes that jointly violate the shared-alias invariant")
	}

	if fixture.CountHistoryForSubject != nil {
		afterA, err := fixture.CountHistoryForSubject(ctx, spanning[0].ID)
		if err != nil {
			t.Fatalf("CountHistoryForSubject(%s) after the refused batch: %v", spanning[0].ID, err)
		}
		afterB, err := fixture.CountHistoryForSubject(ctx, spanning[1].ID)
		if err != nil {
			t.Fatalf("CountHistoryForSubject(%s) after the refused batch: %v", spanning[1].ID, err)
		}
		if afterA != beforeA || afterB != beforeB {
			t.Errorf("a refused batch left a trace: history count for %s went %d->%d, for %s went %d->%d; a refusal must be atomic — the whole batch takes no effect, not just the record that individually triggered it", spanning[0].ID, beforeA, afterA, spanning[1].ID, beforeB, afterB)
		}
	}

	unrelated := []RecordWrite{
		{ID: fixture.IssuePrefix + "-crossrec-scope-unrelated", Patch: map[string]any{"alias": fixture.IssuePrefix + "-crossrec-scope-unrelated-alias"}},
	}
	independent, err := fixture.EnforceCrossRecordInvariant(ctx, unrelated)
	if err != nil {
		t.Fatalf("EnforceCrossRecordInvariant(unrelated): %v", err)
	}
	if !independent.Accepted {
		t.Fatalf("EnforceCrossRecordInvariant refused an unrelated, non-conflicting write (InvariantRefusal=%+v); the invariant transaction must scope exactly the spanning records, not more", independent.InvariantRefusal)
	}
}

// RunInvariantRefusalNamesTheViolatedInvariant pins R16.1-c: a refused batch
// carries an InvariantRefusal naming the specific invariant that fired, not
// a bare boolean.
func RunInvariantRefusalNamesTheViolatedInvariant(t *testing.T, ctx context.Context, fixture CrossRecordInvariantFixture) {
	t.Helper()
	if fixture.EnforceCrossRecordInvariant == nil {
		t.Skip("this backend has not wired store-invariant enforcement yet (EnforceCrossRecordInvariant is nil)")
	}
	shared := fixture.IssuePrefix + "-crossrec-named-alias"
	result, err := fixture.EnforceCrossRecordInvariant(ctx, []RecordWrite{
		{ID: fixture.IssuePrefix + "-crossrec-named-a", Patch: map[string]any{"alias": shared}},
		{ID: fixture.IssuePrefix + "-crossrec-named-b", Patch: map[string]any{"alias": shared}},
	})
	if err != nil {
		t.Fatalf("EnforceCrossRecordInvariant: %v", err)
	}
	if result.Accepted {
		t.Fatal("EnforceCrossRecordInvariant accepted a joint violation; cannot check the refusal's InvariantName")
	}
	if result.InvariantRefusal == nil {
		t.Fatal("InvariantRefusal = nil on a non-accepted result")
	}
	if result.InvariantRefusal.InvariantName == "" {
		t.Error("InvariantRefusal.InvariantName is empty, want a specific invariant name (R16.1-c)")
	}
}

// RunMemoryKeyAliasUniquenessIsAStoreInvariantNotACAS pins FR-02's first
// required subject: Memory key/alias uniqueness (#5877 R2) is a STORE
// invariant, not row-level CAS, so two memories independently claiming the
// same alias each succeed when checked one at a time through a
// per-record-shaped call.
func RunMemoryKeyAliasUniquenessIsAStoreInvariantNotACAS(t *testing.T, ctx context.Context, fixture CrossRecordInvariantFixture) {
	t.Helper()
	if fixture.MemoryKeyAliasWrite == nil {
		t.Skip("this backend exposes no Memory key/alias write to probe (MemoryKeyAliasWrite is nil)")
	}
	alias := fixture.IssuePrefix + "-crossrec-memalias"

	first, err := fixture.MemoryKeyAliasWrite(ctx, fixture.IssuePrefix+"-crossrec-mem-a", "key-a", alias)
	if err != nil {
		t.Fatalf("MemoryKeyAliasWrite(a): %v", err)
	}
	if !first.Accepted {
		t.Fatalf("MemoryKeyAliasWrite(a) was refused (Refusal=%+v), want Accepted", first.Refusal)
	}

	second, err := fixture.MemoryKeyAliasWrite(ctx, fixture.IssuePrefix+"-crossrec-mem-b", "key-b", alias)
	if err != nil {
		t.Fatalf("MemoryKeyAliasWrite(b): %v", err)
	}
	if !second.Accepted {
		t.Fatalf("MemoryKeyAliasWrite(b) claiming the SAME alias %q was refused (Refusal=%+v); alias uniqueness (#5877 R2) is a store invariant, so a per-record write cannot enforce it and must be Accepted here", alias, second.Refusal)
	}
}

// RunMaximumEndpointMultiplicityIsAStoreInvariantNotACAS pins FR-02's second
// required subject: MaximumEndpointMultiplicity is a STORE invariant, not
// row-level CAS, so two edges independently claiming the same endpoint each
// succeed when checked one at a time through a per-record-shaped call.
func RunMaximumEndpointMultiplicityIsAStoreInvariantNotACAS(t *testing.T, ctx context.Context, fixture CrossRecordInvariantFixture) {
	t.Helper()
	if fixture.GraphEdgeWrite == nil {
		t.Skip("this backend exposes no graph edge write to probe (GraphEdgeWrite is nil)")
	}
	from := fixture.IssuePrefix + "-crossrec-edge-from"

	first, err := fixture.GraphEdgeWrite(ctx, from, fixture.IssuePrefix+"-crossrec-edge-to-a", "depends-on")
	if err != nil {
		t.Fatalf("GraphEdgeWrite(a): %v", err)
	}
	if !first.Accepted {
		t.Fatalf("GraphEdgeWrite(a) was refused (Refusal=%+v), want Accepted", first.Refusal)
	}

	second, err := fixture.GraphEdgeWrite(ctx, from, fixture.IssuePrefix+"-crossrec-edge-to-b", "depends-on")
	if err != nil {
		t.Fatalf("GraphEdgeWrite(b): %v", err)
	}
	if !second.Accepted {
		t.Fatalf("GraphEdgeWrite(b) from the SAME endpoint %q was refused (Refusal=%+v); MaximumEndpointMultiplicity is a store invariant, so a per-record write cannot enforce it and must be Accepted here", from, second.Refusal)
	}
}

// RunAdvisoryLeaseAloneDoesNotPreventTheInvariantViolation pins FR-03's
// negative case: an advisory lease is observed BY CONVENTION ONLY, so
// holding one over the shared subject does not, by itself, stop the same
// per-record violation RunCrossRecordInvariantSurvivesTwoPassingPerRecordGuards
// constructs.
func RunAdvisoryLeaseAloneDoesNotPreventTheInvariantViolation(t *testing.T, ctx context.Context, fixture CrossRecordInvariantFixture) {
	t.Helper()
	if fixture.AcquireAdvisoryLease == nil {
		t.Skip("this backend has no lease primitive to test as an insufficient guard (AcquireAdvisoryLease is nil)")
	}
	if fixture.GuardedWrite == nil {
		t.Skip("this backend exposes no per-record guard to construct the boundary case against (GuardedWrite is nil)")
	}
	shared := fixture.IssuePrefix + "-crossrec-leased-alias"
	crossRecordInvariantAcquireLease(t, ctx, fixture, shared, time.Minute)

	first, err := fixture.GuardedWrite(ctx, fixture.IssuePrefix+"-crossrec-leased-a", nil, map[string]any{"alias": shared})
	if err != nil {
		t.Fatalf("GuardedWrite(a) under a held lease: %v", err)
	}
	second, err := fixture.GuardedWrite(ctx, fixture.IssuePrefix+"-crossrec-leased-b", nil, map[string]any{"alias": shared})
	if err != nil {
		t.Fatalf("GuardedWrite(b) under a held lease: %v", err)
	}
	if !first.Accepted || !second.Accepted {
		t.Fatalf("holding an advisory lease changed a per-record GuardedWrite's own verdict (first.Accepted=%v second.Accepted=%v); the lease is observed by convention only and GuardedWrite must not consult it (FR-03)", first.Accepted, second.Accepted)
	}
}

// RunLeaseAcquisitionRecordsNoHistoryEntry mirrors
// RunMetadataCASAWispSwapRecordsNoDurableHistory's delta-around-the-call
// shape (metadata_cas_contract.go, history_matching.go): acquiring an
// advisory lease is bookkeeping, not a durable state change, so it must
// record no history entry for its subject.
func RunLeaseAcquisitionRecordsNoHistoryEntry(t *testing.T, ctx context.Context, fixture CrossRecordInvariantFixture) {
	t.Helper()
	if fixture.AcquireAdvisoryLease == nil {
		t.Skip("this backend has no lease primitive to test (AcquireAdvisoryLease is nil)")
	}
	if fixture.CountHistoryForSubject == nil {
		t.Skip("this backend cannot observe history by subject (CountHistoryForSubject is nil)")
	}
	subject := fixture.IssuePrefix + "-crossrec-lease-history"

	before, err := fixture.CountHistoryForSubject(ctx, subject)
	if err != nil {
		t.Fatalf("CountHistoryForSubject(before): %v", err)
	}

	crossRecordInvariantAcquireLease(t, ctx, fixture, subject, time.Minute)

	after, err := fixture.CountHistoryForSubject(ctx, subject)
	if err != nil {
		t.Fatalf("CountHistoryForSubject(after): %v", err)
	}
	if got := after - before; got != 0 {
		t.Errorf("acquiring an advisory lease recorded %d history entries for %q, want none", got, subject)
	}
}

// --- fixture helpers -------------------------------------------------------

// crossRecordInvariantAcquireLease acquires an advisory lease and registers
// its Release (when the fixture provides one) as a test cleanup, so a case
// does not have to thread a defer through its own body. No caller needs the
// handle itself — both cases only need a lease held over the subject — so
// this does not return one.
func crossRecordInvariantAcquireLease(t *testing.T, ctx context.Context, fixture CrossRecordInvariantFixture, subject string, ttl time.Duration) {
	t.Helper()
	lease, err := fixture.AcquireAdvisoryLease(ctx, subject, ttl)
	if err != nil {
		t.Fatalf("AcquireAdvisoryLease(%s): %v", subject, err)
	}
	if lease.Release != nil {
		t.Cleanup(func() {
			if err := lease.Release(ctx); err != nil {
				t.Errorf("releasing the advisory lease on %q: %v", subject, err)
			}
		})
	}
}
