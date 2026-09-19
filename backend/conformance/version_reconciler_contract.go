package conformance

import (
	"context"
	"errors"
	"testing"

	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of
// publicops.VersionReconciler must satisfy. Each case asserts what
// issueops/versionreconciler.go PROMISES, cited by line, rather than what any
// one backend happens to do today; a backend that disagrees is parked at its
// own wiring site with skipKnownDivergence so the case still runs on the ones
// that agree.
//
// WHAT THIS CONTRACT IS FOR, given that the decision is elsewhere. Every
// backend plans through workapi.PlanVersionReconcile, and that planner's whole
// table — upgrade, no-op, both refusals, the catch-up to the mark, the dotted
// comparison — is pinned without a database in
// internal/workapi/versionreconcile_test.go. Repeating it here would be testing
// the same pure function three times over three sql-servers. What only a real
// backend can show is the SUBSTRATE half, and that is what these cases assert:
//
//   - that a recorded marker is still there for the NEXT caller, which on the
//     unit-of-work backend means a different transaction and on the store
//     backends a different call against a plane that is deliberately not
//     committed to history;
//   - that a refusal and a validation failure write NOTHING, which is a
//     statement about the two markers on disk rather than about the result
//     struct the planner already returned;
//   - that reconciliation adds no history entry on a plane that is
//     dolt-ignored — the fact that lets this run on every startup at all;
//   - that a reconciliation which cannot complete reports an error and leaves
//     the markers standing, which is the case that matters most for a role
//     nothing waits on. Its front doors log and walk past every error this
//     role returns (versionreconciler.go:112-118), so a body that half-wrote
//     and reported success would be invisible from the command line forever.
//
// THE MARKERS ARE GLOBAL TO A WORKSPACE and cannot be namespaced the way the
// issue contracts namespace their seeded ids: the two keys are the point, and a
// case that wrote them under a per-test name would assert nothing about the
// pair the role actually reads. Every case therefore SEEDS both markers
// explicitly through the fixture's out-of-band hook before it asserts, so the
// cases are order-independent over one shared plane rather than a sequence
// that has to run in order.
//
// There are three wirings and only TWO independent bodies between them: dolt
// and embeddeddolt both hand back internal/workapi/storeversionreconciler and
// write through their own SetLocalMetadata, so they are one vote plus an engine
// check; the unit-of-work provider is the second, and it does the whole read,
// plan and write inside one transaction.

// VersionReconcilerFixture supplies adapter-specific storage access for the
// version-marker assertions.
type VersionReconcilerFixture struct {
	// Reconciler is the surface under test.
	Reconciler publicops.VersionReconciler
	// RecordMarkers writes the two markers OUT OF BAND, past the role, and is
	// how every case establishes the state it asserts about. It is NOT a
	// roleFixtureKit hook — the kit is frozen and reaches the issues plane and
	// the config plane, not the clone-local metadata one — so each wiring
	// supplies its own short closure over the metadata seam that backend
	// already publishes, the way the cycle contract supplies its own Exec.
	//
	// It exists at all because two of the states this role must handle are
	// states the role CANNOT produce: a recorded version BELOW the high-water
	// mark is what something outside this role leaves behind, and reconciling
	// is the one thing that can never create it.
	RecordMarkers func(ctx context.Context, recorded, highWaterMark string) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// RunVersionReconcilerRecordsAWorkspaceWithNoMarkers pins
// versionreconciler.go:21-29 and :159-174 on the state every workspace starts
// in: nothing recorded, "" rather than a missing-row error, and a first
// reconciliation that sets BOTH markers and is visible to the next caller.
func RunVersionReconcilerRecordsAWorkspaceWithNoMarkers(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) {
	t.Helper()
	seedVersionMarkers(t, ctx, fixture, "", "")

	before := readVersionMarkers(t, ctx, fixture)
	if before.Recorded != "" || before.HighWaterMark != "" {
		t.Fatalf("RecordedVersion() = %+v on a workspace with no markers, want both empty", before)
	}

	result := reconcileVersion(t, ctx, fixture, "1.2.0")
	if result.Previous != "" || result.Current != "1.2.0" || !result.Migrated || result.Downgrade {
		t.Fatalf("ReconcileVersion() = %+v, want {Previous:\"\" Current:1.2.0 Migrated:true}", result)
	}
	assertVersionMarkers(t, ctx, fixture, "1.2.0", "1.2.0")
}

// RunVersionReconcilerAdvancesBothMarkersOnAnUpgrade pins the ordinary case —
// the one that fires once per upgrade per workspace and never again
// (versionreconciler.go:159-174).
func RunVersionReconcilerAdvancesBothMarkersOnAnUpgrade(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) {
	t.Helper()
	seedVersionMarkers(t, ctx, fixture, "1.2.0", "1.2.0")

	result := reconcileVersion(t, ctx, fixture, "1.3.0")
	if result.Previous != "1.2.0" || result.Current != "1.3.0" || !result.Migrated || result.Downgrade {
		t.Fatalf("ReconcileVersion() = %+v, want {Previous:1.2.0 Current:1.3.0 Migrated:true}", result)
	}
	assertVersionMarkers(t, ctx, fixture, "1.3.0", "1.3.0")
}

// RunVersionReconcilerTreatsTheSameVersionAsANoOp pins versionreconciler.go:75-78
// — "reconciling twice is not two migrations" — on the path that runs before
// EVERY command, which is why it is asserted as a distinguishable outcome and
// not merely as "no error".
func RunVersionReconcilerTreatsTheSameVersionAsANoOp(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) {
	t.Helper()
	seedVersionMarkers(t, ctx, fixture, "1.3.0", "1.3.0")

	result := reconcileVersion(t, ctx, fixture, "1.3.0")
	if result.Migrated || result.Downgrade {
		t.Fatalf("ReconcileVersion() = %+v on an unchanged version, want neither flag set", result)
	}
	if result.Previous != "1.3.0" || result.Current != "1.3.0" {
		t.Fatalf("ReconcileVersion() = %+v, want Previous and Current both 1.3.0", result)
	}
	assertVersionMarkers(t, ctx, fixture, "1.3.0", "1.3.0")
}

// RunVersionReconcilerRefusesADowngradeWithoutAnError pins
// versionreconciler.go:79-89: a refusal is an OUTCOME, the newer number stays,
// and the caller gets a nil error because an older binary is allowed to use a
// workspace a newer one has opened.
func RunVersionReconcilerRefusesADowngradeWithoutAnError(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) {
	t.Helper()
	seedVersionMarkers(t, ctx, fixture, "1.3.0", "1.3.0")

	result, err := fixture.Reconciler.ReconcileVersion(ctx, publicops.VersionReconcileRequest{CLIVersion: "1.2.0"})
	if err != nil {
		t.Fatalf("ReconcileVersion(1.2.0) error = %v, want a refusal reported as an outcome", err)
	}
	if !result.Downgrade || result.Migrated {
		t.Fatalf("ReconcileVersion() = %+v, want Downgrade with nothing migrated", result)
	}
	if result.Previous != "1.3.0" || result.Current != "1.3.0" {
		t.Fatalf("ReconcileVersion() = %+v, want the value that STAYED reported as Current", result)
	}
	assertVersionMarkers(t, ctx, fixture, "1.3.0", "1.3.0")
}

// RunVersionReconcilerRefusesAVersionBelowTheHighWaterMark pins
// versionreconciler.go:30-38, and it is the case the second marker exists for:
// the recorded version alone would ACCEPT this binary, and the mark is what
// remembers that a newer one has already prepared this workspace.
//
// The state it asserts about is one this role cannot produce, which is why the
// fixture writes it out of band.
func RunVersionReconcilerRefusesAVersionBelowTheHighWaterMark(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) {
	t.Helper()
	seedVersionMarkers(t, ctx, fixture, "1.2.0", "1.4.0")

	result := reconcileVersion(t, ctx, fixture, "1.3.0")
	if !result.Downgrade || result.Migrated {
		t.Fatalf("ReconcileVersion(1.3.0) = %+v below a 1.4.0 mark, want Downgrade", result)
	}
	assertVersionMarkers(t, ctx, fixture, "1.2.0", "1.4.0")
}

// RunVersionReconcilerCatchesUpToTheHighWaterMark pins the other half of that
// guard: the mark is a CEILING rather than a value to exceed, so the binary
// that left it may record itself again, and doing so moves the marker without
// moving the mark.
func RunVersionReconcilerCatchesUpToTheHighWaterMark(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) {
	t.Helper()
	seedVersionMarkers(t, ctx, fixture, "1.2.0", "1.4.0")

	result := reconcileVersion(t, ctx, fixture, "1.4.0")
	if !result.Migrated || result.Downgrade {
		t.Fatalf("ReconcileVersion(1.4.0) = %+v at a 1.4.0 mark, want a migration", result)
	}
	assertVersionMarkers(t, ctx, fixture, "1.4.0", "1.4.0")
}

// RunVersionReconcilerRefusesAnEmptyVersion pins versionreconciler.go:44-47 and
// :176 — ErrValidation, and nothing written.
//
// Both facts matter and the second one is the reason this case reads the
// markers back: recording "" over a real marker would take the downgrade guard
// down with it, and the workspace it left behind would be indistinguishable
// from one no binary had ever opened.
func RunVersionReconcilerRefusesAnEmptyVersion(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) {
	t.Helper()
	seedVersionMarkers(t, ctx, fixture, "1.3.0", "1.3.0")

	_, err := fixture.Reconciler.ReconcileVersion(ctx, publicops.VersionReconcileRequest{})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("ReconcileVersion(\"\") error = %v, want ErrValidation", err)
	}
	assertVersionMarkers(t, ctx, fixture, "1.3.0", "1.3.0")
}

// RunVersionReconcilerLeavesTheMarkersStandingWhenItCannotComplete is the case
// that matters most for a role no user waits on.
//
// versionreconciler.go:112-118 says a genuine failure is an error the CALLER
// logs and walks past, and both front doors do exactly that at debug level.
// That makes a body which half-wrote and reported success invisible from the
// command line forever, so the promise is asserted from the other side: when
// the substrate cannot serve the call, the answer is an ERROR — not a
// zero-valued outcome that reads as a no-op — and the markers are exactly what
// they were.
//
// A canceled context is the one failure every backend can be made to have. It
// is not the only one they will meet in the field, but a body that respects
// cancellation on the way in is a body that has not written before it looked.
func RunVersionReconcilerLeavesTheMarkersStandingWhenItCannotComplete(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) {
	t.Helper()
	seedVersionMarkers(t, ctx, fixture, "1.3.0", "1.3.0")

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	if _, err := fixture.Reconciler.ReconcileVersion(canceled, publicops.VersionReconcileRequest{CLIVersion: "1.4.0"}); err == nil {
		t.Fatal("ReconcileVersion() on a canceled context = nil error, want the failure reported rather than a silent no-op")
	}
	if _, err := fixture.Reconciler.RecordedVersion(canceled, publicops.RecordedVersionRequest{}); err == nil {
		t.Fatal("RecordedVersion() on a canceled context = nil error, want the failure reported")
	}
	assertVersionMarkers(t, ctx, fixture, "1.3.0", "1.3.0")
}

// RunVersionReconcilerRecordsNoHistory pins versionreconciler.go:178-182. The
// markers are clone-local and dolt-ignored, so recording one adds nothing to
// the workspace's log — the fact that lets this role run before every command
// without filling a workspace's history with the version of the binary that
// read it.
//
// The delta is taken around a reconciliation that DOES write, because a no-op
// leaving no history behind would prove nothing.
func RunVersionReconcilerRecordsNoHistory(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture cannot observe history on this backend")
	}
	seedVersionMarkers(t, ctx, fixture, "1.2.0", "1.2.0")

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory() before: %v", err)
	}
	if result := reconcileVersion(t, ctx, fixture, "1.3.0"); !result.Migrated {
		t.Fatalf("ReconcileVersion() = %+v, want the migration this case measures", result)
	}
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory() after: %v", err)
	}
	if after != before {
		t.Fatalf("history entries %d -> %d, want a clone-local write to record none", before, after)
	}
}

// seedVersionMarkers puts the two markers in a known state past the role.
func seedVersionMarkers(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture, recorded, highWaterMark string) {
	t.Helper()
	if fixture.RecordMarkers == nil {
		t.Fatal("fixture.RecordMarkers is nil: every case seeds the markers it asserts about")
	}
	if err := fixture.RecordMarkers(ctx, recorded, highWaterMark); err != nil {
		t.Fatalf("seed version markers (%q, %q): %v", recorded, highWaterMark, err)
	}
}

// reconcileVersion runs a reconciliation that is expected to succeed, refusal
// included: a refusal is an outcome on this role, not an error.
func reconcileVersion(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture, cliVersion string) publicops.VersionReconcileResult {
	t.Helper()
	result, err := fixture.Reconciler.ReconcileVersion(ctx, publicops.VersionReconcileRequest{CLIVersion: cliVersion})
	if err != nil {
		t.Fatalf("ReconcileVersion(%q): %v", cliVersion, err)
	}
	return result
}

func readVersionMarkers(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture) publicops.RecordedVersionResult {
	t.Helper()
	result, err := fixture.Reconciler.RecordedVersion(ctx, publicops.RecordedVersionRequest{})
	if err != nil {
		t.Fatalf("RecordedVersion(): %v", err)
	}
	return result
}

// assertVersionMarkers reads the pair back THROUGH THE ROLE, which is the
// promise being checked: versionreconciler.go:170-174 says a later
// RecordedVersion is what "reconciled" means, and on the unit-of-work backend
// that read is a new transaction — the only place a write that never committed
// shows up.
func assertVersionMarkers(t *testing.T, ctx context.Context, fixture VersionReconcilerFixture, recorded, highWaterMark string) {
	t.Helper()
	got := readVersionMarkers(t, ctx, fixture)
	if got.Recorded != recorded || got.HighWaterMark != highWaterMark {
		t.Fatalf("RecordedVersion() = %+v, want {Recorded:%q HighWaterMark:%q}", got, recorded, highWaterMark)
	}
}
