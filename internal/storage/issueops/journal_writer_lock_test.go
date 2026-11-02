package issueops

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// TestJournalWriterLockName pins the lock name every journaled writer on one
// database queues on (gastownhall/beads#6117): readable under the server's
// 64-byte GET_LOCK limit, and the fnv64a-hashed form past it — the same
// convention the delete fence and schema.MigrationLockName use, so two beads
// databases sharing one dolt sql-server never queue on each other's writers.
func TestJournalWriterLockName(t *testing.T) {
	if got, want := JournalWriterLockName("beads"), "bd_events_seq:beads"; got != want {
		t.Fatalf("JournalWriterLockName(beads) = %q, want %q", got, want)
	}
	if got, want := JournalWriterLockName(""), "bd_events_seq:"; got != want {
		t.Fatalf("JournalWriterLockName(\"\") = %q, want %q", got, want)
	}
	long := strings.Repeat("d", 200)
	hashed := JournalWriterLockName(long)
	if len(hashed) > 64 {
		t.Fatalf("hashed name %q is %d bytes, want <= 64", hashed, len(hashed))
	}
	if !strings.HasPrefix(hashed, journalWriterLockPrefix) {
		t.Fatalf("hashed name %q lost the %q prefix", hashed, journalWriterLockPrefix)
	}
	if hashed == JournalWriterLockName(strings.Repeat("d", 201)) {
		t.Fatalf("two different long database names hashed to one lock name")
	}
}

// TestJournalAdmissionDeadline pins the admission-deadline resolution: a
// route-fixed deadline wins, and an unmarked context gets the earlier of its
// own deadline and the default budget.
func TestJournalAdmissionDeadline(t *testing.T) {
	fixed := time.Now().Add(3 * time.Second)
	ctx := WithJournalAdmissionDeadline(context.Background(), fixed)
	if !JournalAdmissionDeadline(ctx).Equal(fixed) {
		t.Fatalf("route-fixed deadline not honored")
	}
	defaultDeadline := JournalAdmissionDeadline(context.Background())
	if d := time.Until(defaultDeadline); d > JournalWriterAdmissionBudget || d <= 0 {
		t.Fatalf("default budget resolution = %v, want within %v", d, JournalWriterAdmissionBudget)
	}
	soon := time.Now().Add(2 * time.Second)
	ctxSoon, cancel := context.WithDeadline(context.Background(), soon)
	defer cancel()
	if got := JournalAdmissionDeadline(ctxSoon); !got.Equal(soon) {
		t.Fatalf("caller deadline not honored: got %v, want %v", got, soon)
	}
	// WithRouteJournalAdmission takes the earlier of the budget and the
	// caller's own deadline.
	routed := WithRouteJournalAdmission(ctxSoon, JournalWriterAdmissionBudget)
	if got := JournalAdmissionDeadline(routed); !got.Equal(soon) {
		t.Fatalf("route admission did not clamp to the caller deadline: got %v, want %v", got, soon)
	}
}

// TestUnlockedJournalRouteMarker pins the read/ephemeral route marker: marked
// contexts report unlocked, everything else does not.
func TestUnlockedJournalRouteMarker(t *testing.T) {
	if UnlockedJournalRoute(context.Background()) {
		t.Fatalf("plain context reports an unlocked journal route")
	}
	ctx := WithUnlockedJournalRoute(context.Background())
	if !UnlockedJournalRoute(ctx) {
		t.Fatalf("marked context does not report an unlocked journal route")
	}
	// The marker survives a child context (deadlines added downstream).
	child, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if !UnlockedJournalRoute(child) {
		t.Fatalf("child of a marked context lost the unlocked journal route")
	}
}

// TestJournalWriterBusyErrorIsNamed pins the busy sentinel: admission-budget
// exhaustion is distinguishable from backend failures by errors.Is.
func TestJournalWriterBusyErrorIsNamed(t *testing.T) {
	err := journalWriterBusyError("bd_events_seq:beads", "beads", time.Now())
	if !errors.Is(err, ErrJournalWriterBusy) {
		t.Fatalf("busy error does not wrap ErrJournalWriterBusy: %v", err)
	}
	if strings.Contains(err.Error(), "unknown") {
		t.Fatalf("busy error lost its lock identity: %v", err)
	}
	// Fuzzing guard: a random backend error must not match the sentinel.
	if errors.Is(errors.New(randomBackendError()), ErrJournalWriterBusy) {
		t.Fatalf("unrelated error matched the busy sentinel")
	}
}

func randomBackendError() string {
	return "sql: connection refused at " + strings.Repeat("x", rand.Intn(10))
}
// TestUnlockedJournalScopeTriState pins AC-4 of gastownhall/beads#6117 at the
// issueops seam: a journal-unlocked scope is activated-but-forbidden with the
// journal on — an emit is refused with ErrJournalUnlockedTx before any SQL and
// the violation latches for the commit sites — and with the journal off it is
// the same plain no-op as an ordinary journal-off scope. The tx is a typed-nil
// *sql.Tx: the forbidden and no-op paths never touch it, which is the point.
func TestUnlockedJournalScopeTriState(t *testing.T) {
	// Forbidden scope, journal on: the emit is refused before any SQL and the
	// violation latches until the scope's cleanup runs.
	var forbidden *sql.Tx
	clear := ScopeUnlockedJournalTransaction(forbidden, true)
	if err := RecordDeleteInTx(context.Background(), forbidden, "bd-1", "actor"); !errors.Is(err, ErrJournalUnlockedTx) {
		t.Fatalf("forbidden emit error = %v, want ErrJournalUnlockedTx", err)
	}
	if err := JournalUnlockedEmitViolation(forbidden); !errors.Is(err, ErrJournalUnlockedTx) {
		t.Fatalf("violated scope does not report ErrJournalUnlockedTx: %v", err)
	}
	clear()
	if err := JournalUnlockedEmitViolation(forbidden); err != nil {
		t.Fatalf("cleared scope still reports a violation: %v", err)
	}

	// Unlocked scope, journal off: the no-op is exactly an ordinary
	// journal-off scope's — the emit entry returns nil and nothing latches.
	var off *sql.Tx
	clearOff := ScopeUnlockedJournalTransaction(off, false)
	if err := RecordDeleteInTx(context.Background(), off, "bd-1", "actor"); err != nil {
		t.Fatalf("journal-off unlocked emit is not a no-op: %v", err)
	}
	if err := JournalUnlockedEmitViolation(off); err != nil {
		t.Fatalf("journal-off unlocked scope latched a violation: %v", err)
	}
	clearOff()

	// An ordinary activated scope emits without latching a violation (the
	// emit itself is not exercised here; it would need a real transaction).
	var plain *sql.Tx
	clearPlain := ScopeEventsJournalTransaction(plain, true)
	if err := JournalUnlockedEmitViolation(plain); err != nil {
		t.Fatalf("ordinary activated scope reports a violation: %v", err)
	}
	clearPlain()
}
