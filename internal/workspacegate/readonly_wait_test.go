package workspacegate

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

// A strict-readonly SHARED acquisition (Options.ReadOnly) and a waiting one
// (Options.Wait, with its writer-fairness intent queue) meet in exactly one
// place: a ReadOnly acquisition with Wait > 0. The ReadOnly tests in
// gate_test.go are all wait-less, so the tests below pin that seam: waiting
// must not make a ReadOnly acquisition create anything (no gate file, no
// intent file, no holder-info sidecar), and ReadOnly must not change how long
// a shared acquisition waits for an exclusive holder.
//
// None of them touches maxIntentHold or any other package variable a test
// shortens, and none runs in parallel with anything.

// gateDirEntries lists dir's entries by name (os.ReadDir sorts them), so a
// test can prove an acquisition left the gate's directory as it found it.
func gateDirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// (i) The gate file exists and no intent file does: a waiting ReadOnly shared
// acquisition takes the gate on its first attempt (OnWait never fires) and
// creates nothing beside it: no .intent, no .intent.info, no .info.
func TestReadOnlyWaitingSharedAcquireOfExistingGateCreatesNothing(t *testing.T) {
	g, dir := testGate(t)
	// Materialize the gate file the way any ordinary command does.
	_ = mustAcquire(t, g, Shared, Options{}).Release()
	before := gateDirEntries(t, dir)
	if _, err := os.Stat(g.Path()); err != nil {
		t.Fatalf("precondition: the gate file must exist: %v", err)
	}
	if _, err := os.Stat(g.intentPath()); !os.IsNotExist(err) {
		t.Fatalf("precondition: no intent file may exist, stat err = %v", err)
	}

	const wait = time.Second
	var waited []string
	start := time.Now()
	h, err := g.Acquire(context.Background(), Shared, Options{
		ReadOnly: true,
		Wait:     wait,
		OnWait:   func(holder string) { waited = append(waited, holder) },
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("waiting ReadOnly shared Acquire of a free, existing gate: %v", err)
	}
	defer func() { _ = h.Release() }()
	if len(waited) != 0 {
		t.Errorf("the first attempt did not take the free gate: OnWait fired with %q", waited)
	}
	if elapsed >= wait {
		t.Errorf("took %s, want far under its %s budget: nothing was holding the gate", elapsed, wait)
	}
	if h.f == nil {
		t.Fatal("handle holds no file: against an existing gate file a ReadOnly acquisition is a real shared hold")
	}
	if got := gateDirEntries(t, dir); !slices.Equal(got, before) {
		t.Errorf("waiting ReadOnly acquisition changed %s\n before: %v\n after:  %v", dir, before, got)
	}
	// It is a real shared hold: a maintenance operation is turned away.
	if _, err := g.Acquire(context.Background(), Exclusive, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("exclusive Acquire under the ReadOnly shared holder: err = %v, want ErrBusy", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := gateDirEntries(t, dir); !slices.Equal(got, before) {
		t.Errorf("after release %s\n before: %v\n after:  %v", dir, before, got)
	}
}

// (ii) No gate file: nothing has ever gated here. A waiting ReadOnly shared
// acquisition returns at once with an empty handle (it holds no file and has
// nothing to wait for) and leaves the directory exactly as it was: it neither
// creates the gate file nor waits for one to appear.
func TestReadOnlyWaitingSharedAcquireOfMissingGateHoldsNothing(t *testing.T) {
	g, dir := testGate(t)
	before := gateDirEntries(t, dir)
	if len(before) != 0 {
		t.Fatalf("precondition: %s must start empty, has %v", dir, before)
	}

	const wait = time.Second
	var waited []string
	start := time.Now()
	h, err := g.Acquire(context.Background(), Shared, Options{
		ReadOnly: true,
		Wait:     wait,
		OnWait:   func(holder string) { waited = append(waited, holder) },
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("waiting ReadOnly shared Acquire with no gate file: %v, want success holding nothing", err)
	}
	if h == nil || h.f != nil {
		t.Fatalf("handle = %+v, want an empty handle (no gate file was opened)", h)
	}
	if h.Mode() != Shared {
		t.Errorf("handle mode = %s, want %s", h.Mode(), Shared)
	}
	if len(waited) != 0 {
		t.Errorf("OnWait fired with %q: with no gate file there is nothing to wait for", waited)
	}
	if elapsed >= wait {
		t.Errorf("took %s, want far under its %s budget: a missing gate file is answered before any retry", elapsed, wait)
	}
	if got := gateDirEntries(t, dir); !slices.Equal(got, before) {
		t.Errorf("waiting ReadOnly acquisition changed %s\n before: %v\n after:  %v", dir, before, got)
	}
	for i := 1; i <= 2; i++ {
		if err := h.Release(); err != nil {
			t.Fatalf("Release #%d of the empty handle: %v", i, err)
		}
	}
	if got := gateDirEntries(t, dir); !slices.Equal(got, before) {
		t.Errorf("after release %s\n before: %v\n after:  %v", dir, before, got)
	}
}

// (iii) The gate file exists and is held EXCLUSIVELY: a waiting ReadOnly
// shared acquisition waits out its own budget, exactly like any shared
// acquisition, and then fails with ErrBusy. ReadOnly is not special-cased:
// it neither fails at once nor waits longer (the shared default is 30s, the
// init budget 60s). It also creates nothing while it waits, which is where
// the fairness probe (ExclusiveQueued) meets ReadOnly: the probe opens the
// intent file read-only and treats a missing one as "nothing queued".
func TestReadOnlyWaitingSharedAcquireUnderExclusiveHolderWaitsItsBudget(t *testing.T) {
	g, dir := testGate(t)
	holder := mustAcquire(t, g, Exclusive, Options{Reason: "test maintenance"})
	defer func() { _ = holder.Release() }()
	before := gateDirEntries(t, dir)

	const wait = 300 * time.Millisecond
	waited := 0
	start := time.Now()
	h, err := g.Acquire(context.Background(), Shared, Options{
		ReadOnly: true,
		Wait:     wait,
		OnWait:   func(string) { waited++ },
	})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrBusy) {
		if h != nil {
			_ = h.Release()
		}
		t.Fatalf("waiting ReadOnly shared Acquire under an exclusive holder: err = %v, want ErrBusy", err)
	}
	// Acquire's deadline is taken after start, and it reports ErrBusy only
	// once that deadline has passed, so this lower bound is exact.
	if elapsed < wait {
		t.Errorf("gave up after %s, before its %s budget ran out: ReadOnly must wait like any shared acquisition", elapsed, wait)
	}
	// The loop overshoots by at most one poll interval (100ms by default).
	// The ceiling is far more generous than that so a loaded host cannot make
	// this flaky; it still catches any special-cased budget.
	if limit := wait + 2*time.Second; elapsed > limit {
		t.Errorf("gave up after %s, want about its %s budget (at most %s)", elapsed, wait, limit)
	}
	if waited != 1 {
		t.Errorf("OnWait fired %d times, want once (after the first busy attempt)", waited)
	}
	if got := gateDirEntries(t, dir); !slices.Equal(got, before) {
		t.Errorf("waiting ReadOnly acquisition changed %s\n before: %v\n after:  %v", dir, before, got)
	}
}
