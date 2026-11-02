package issueops

import (
	"context"
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