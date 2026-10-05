package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/workspacegate"
)

// resetGateTestEnv pins every env var the physical-root resolver consults so
// a developer machine's beads setup (shared-server mode, custom data dirs,
// central config) cannot change which gates these tests acquire.
func resetGateTestEnv(t *testing.T) {
	t.Helper()
	// Start from no held gates: an earlier in-process command test in the
	// same binary can leave workspaceGateHandle set (and its shared hold
	// advertised), which these tests assert on.
	releaseWorkspaceGates()
	for _, k := range []string{
		"BEADS_DOLT_SERVER_MODE",
		"BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_DATA_DIR",
		"BEADS_DOLT_SERVER_HOST",
		"BEADS_PROXIED_SERVER_ROOT_PATH",
		"BEADS_SHARED_SERVER_DIR",
		initGateTimeoutEnv,
		sharedGateWaitEnv,
		"BD_GIT_HOOK",
		workspacegate.InheritedHoldEnv,
	} {
		t.Setenv(k, "")
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "no-central.json"))
}

func newGateTestWorkspace(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"dolt","database":"beads.db","dolt_mode":"embedded"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

func TestCommandNeedsExclusiveGate(t *testing.T) {
	root := &cobra.Command{Use: "bd"}
	backup := &cobra.Command{Use: "backup"}
	backupRestore := &cobra.Command{Use: "restore [path]"}
	backup.AddCommand(backupRestore)
	root.AddCommand(backup)
	// Top-level `bd restore <issue-id>` is an ISSUE restore
	// (cmd/bd/restore.go), not a database restore: it must stay SHARED.
	issueRestore := &cobra.Command{Use: "restore [issue-id]"}
	root.AddCommand(issueRestore)
	list := &cobra.Command{Use: "list"}
	root.AddCommand(list)

	cases := []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{"backup restore is exclusive", backupRestore, true},
		{"top-level issue restore is not", issueRestore, false},
		{"list is not", list, false},
		{"backup parent is not", backup, false},
		{"root is not", root, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandNeedsExclusiveGate(tc.cmd); got != tc.want {
				t.Errorf("commandNeedsExclusiveGate(%s) = %v, want %v", tc.cmd.Name(), got, tc.want)
			}
		})
	}
}

func TestAcquireCommandWorkspaceGatesAbsentWorkspace(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)

	list := &cobra.Command{Use: "list"}
	missing := filepath.Join(t.TempDir(), "nope", ".beads")
	if err := acquireCommandWorkspaceGates(context.Background(), list, missing); err != nil {
		t.Fatalf("absent beadsDir must be silently ungated, got %v", err)
	}
	if workspaceGateHandle != nil {
		t.Error("absent beadsDir must leave no gate handle")
	}
}

// holdWorkspaceGateExclusive takes beadsDir's workspace gate exclusively,
// as a maintenance operation (init/restore/migrate) would.
func holdWorkspaceGateExclusive(t *testing.T, beadsDir string) *workspacegate.Handle {
	t.Helper()
	gate, err := workspacegate.ForWorkspace(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := gate.Acquire(context.Background(), workspacegate.Exclusive,
		workspacegate.Options{Reason: "test maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Release() })
	return holder
}

// countGateNotices replaces the gate-wait notice with a counter.
func countGateNotices(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := gateWaitOnWait
	gateWaitOnWait = func(string) { n.Add(1) }
	t.Cleanup(func() { gateWaitOnWait = old })
	return &n
}

// Past the bound, an ordinary command aborts with an error naming the
// holder, the budget, and the knob — and never proceeds ungated.
func TestAcquireCommandWorkspaceGatesBlockedByExclusiveHolder(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)
	t.Setenv(sharedGateWaitEnv, "300ms")
	holdWorkspaceGateExclusive(t, beadsDir)

	list := &cobra.Command{Use: "list"}
	var err error
	start := time.Now()
	stderr := captureStderr(t, func() {
		err = acquireCommandWorkspaceGates(context.Background(), list, beadsDir)
	})
	if err == nil {
		t.Fatal("SHARED acquisition under a foreign exclusive holder must abort, got nil error")
	}
	if waited := time.Since(start); waited < 250*time.Millisecond || waited > 3*time.Second {
		t.Errorf("gave up after %s, want about the 300ms bound", waited)
	}
	for _, want := range []string{"a maintenance operation is running", "waited 300ms", sharedGateWaitEnv, "test maintenance"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q missing %q", stderr, want)
		}
	}
	if workspaceGateHandle != nil {
		t.Error("failed acquisition must leave no gate handle")
	}
}

// The core behavior: an ordinary command that lands while a maintenance
// operation holds the gate waits for it (with one notice) and then runs.
func TestAcquireCommandWorkspaceGatesWaitsForExclusiveHolder(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)
	notices := countGateNotices(t)
	if got := sharedGateWait(); got != sharedGateWaitDefault {
		t.Fatalf("sharedGateWait() = %s, want default %s", got, sharedGateWaitDefault)
	}

	holder := holdWorkspaceGateExclusive(t, beadsDir)
	holdFor := gateWaitNoticeDelay + time.Second
	released := time.AfterFunc(holdFor, func() { _ = holder.Release() })
	t.Cleanup(func() { released.Stop() })

	list := &cobra.Command{Use: "list"}
	start := time.Now()
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatalf("ordinary command with a maintenance holder released after %s: %v", holdFor, err)
	}
	if waited := time.Since(start); waited < holdFor-500*time.Millisecond {
		t.Fatalf("acquired after %s while the holder held the gate for %s", waited, holdFor)
	}
	if workspaceGateHandle == nil {
		t.Fatal("expected a held gate handle")
	}
	if got := notices.Load(); got != 1 {
		t.Fatalf("wait notices = %d, want exactly 1", got)
	}
}

// BEADS_GATE_WAIT_TIMEOUT=0 and git-hook context both keep the old
// fail-fast behavior.
func TestAcquireCommandWorkspaceGatesFailFastModes(t *testing.T) {
	for _, tc := range []struct{ name, env, val string }{
		{"timeout zero", sharedGateWaitEnv, "0"},
		{"git hook", "BD_GIT_HOOK", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetGateTestEnv(t)
			t.Cleanup(releaseWorkspaceGates)
			beadsDir := newGateTestWorkspace(t)
			t.Setenv(tc.env, tc.val)
			holdWorkspaceGateExclusive(t, beadsDir)

			list := &cobra.Command{Use: "list"}
			var err error
			start := time.Now()
			stderr := captureStderr(t, func() {
				err = acquireCommandWorkspaceGates(context.Background(), list, beadsDir)
			})
			if err == nil {
				t.Fatal("fail-fast SHARED acquisition under an exclusive holder succeeded")
			}
			if waited := time.Since(start); waited > time.Second {
				t.Errorf("fail-fast acquisition took %s", waited)
			}
			if strings.Contains(stderr, "waited") {
				t.Errorf("fail-fast error claims a wait: %q", stderr)
			}
		})
	}
}

// Ctrl-C (rootCtx cancellation) aborts the wait with an error. It must not
// fall through to the fail-open "continue ungated" path.
func TestAcquireCommandWorkspaceGatesHonorsCancellation(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)
	holdWorkspaceGateExclusive(t, beadsDir)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	list := &cobra.Command{Use: "list"}
	var err error
	start := time.Now()
	stderr := captureStderr(t, func() {
		err = acquireCommandWorkspaceGates(ctx, list, beadsDir)
	})
	if err == nil {
		t.Fatal("canceled wait returned nil (proceeded ungated over a maintenance holder)")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("canceled wait kept going for %s", waited)
	}
	if !strings.Contains(stderr, "interrupted while waiting") || strings.Contains(stderr, "continuing ungated") {
		t.Errorf("stderr = %q, want an interrupted error and no fail-open warning", stderr)
	}
	if workspaceGateHandle != nil {
		t.Error("canceled acquisition must leave no gate handle")
	}
}

// Writer fairness end to end at the chokepoint: while bd init is queued for
// the gate, a new ordinary command waits behind it rather than slipping in —
// and an ordinary command spawned by a bd process that already holds the
// gate shared (InheritedHoldEnv) does slip in, since it cannot deadlock the
// queue it would otherwise join.
func TestAcquireCommandWorkspaceGatesQueuesBehindWaitingInit(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)
	physicalRoot := filepath.Join(filepath.Dir(beadsDir), "dolt-data")
	countGateNotices(t)

	// An in-flight ordinary command holds the gate shared.
	inflight, err := workspacegate.ForWorkspace(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := inflight.Acquire(context.Background(), workspacegate.Shared, workspacegate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sh.Release() })

	// bd init queues behind it.
	initDone := make(chan error, 1)
	var initHandle atomic.Pointer[workspacegate.MultiHandle]
	go func() {
		h, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, nil)
		initHandle.Store(h)
		initDone <- err
	}()
	t.Cleanup(func() {
		if h := initHandle.Load(); h != nil {
			_ = h.Release()
		}
	})
	queued := inflight.ExclusiveQueued
	deadline := time.Now().Add(5 * time.Second)
	for !queued() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !queued() {
		t.Fatal("bd init never queued for the gate")
	}

	// A new ordinary command must not jump the queue.
	t.Setenv(sharedGateWaitEnv, "300ms")
	list := &cobra.Command{Use: "list"}
	var lerr error
	stderr := captureStderr(t, func() { lerr = acquireCommandWorkspaceGates(context.Background(), list, beadsDir) })
	if lerr == nil {
		t.Fatal("ordinary command jumped ahead of a queued bd init")
	}
	if !strings.Contains(stderr, "queued for exclusive access") || !strings.Contains(stderr, "bd init") {
		t.Errorf("stderr %q does not name the queued bd init", stderr)
	}

	// ...unless its parent bd already holds the gate shared.
	t.Setenv(workspacegate.InheritedHoldEnv, strconv.Itoa(os.Getppid()))
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatalf("child of a shared holder queued behind init: %v", err)
	}
	releaseWorkspaceGates()

	// The in-flight command finishes; init gets the gate.
	_ = sh.Release()
	select {
	case err := <-initDone:
		if err != nil {
			t.Fatalf("bd init after the in-flight command drained: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bd init did not get the gate after the in-flight command released")
	}
}

// A successful shared hold advertises this PID to child processes, and
// release restores whatever value was inherited.
func TestSharedHoldAdvertisedToChildren(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)
	t.Setenv(workspacegate.InheritedHoldEnv, "inherited")

	list := &cobra.Command{Use: "list"}
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatal(err)
	}
	if got, want := os.Getenv(workspacegate.InheritedHoldEnv), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("%s while holding = %q, want %q", workspacegate.InheritedHoldEnv, got, want)
	}
	releaseWorkspaceGates()
	if got := os.Getenv(workspacegate.InheritedHoldEnv); got != "inherited" {
		t.Fatalf("%s after release = %q, want the inherited value restored", workspacegate.InheritedHoldEnv, got)
	}
}

func TestSharedGateWaitEnv(t *testing.T) {
	for _, tc := range []struct {
		raw, hook string
		want      time.Duration
	}{
		{"", "", sharedGateWaitDefault},
		{"45", "", 45 * time.Second},
		{"2m", "", 2 * time.Minute},
		{"0", "", 0},
		{"0s", "", 0},
		{"-5s", "", sharedGateWaitDefault},
		{"soon", "", sharedGateWaitDefault},
		{"2m", "1", 0},
		{"", "1", 0},
	} {
		t.Run(tc.raw+"/hook="+tc.hook, func(t *testing.T) {
			t.Setenv(sharedGateWaitEnv, tc.raw)
			t.Setenv("BD_GIT_HOOK", tc.hook)
			oldQuiet := quietFlag
			quietFlag = true
			t.Cleanup(func() { quietFlag = oldQuiet })
			if got := sharedGateWait(); got != tc.want {
				t.Fatalf("sharedGateWait() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAcquireInitMutationGateKeepsReplacementExclusiveDuringPreflight(t *testing.T) {
	resetGateTestEnv(t)
	beadsDir := newGateTestWorkspace(t)
	physicalRoot := filepath.Join(filepath.Dir(beadsDir), "dolt-data")

	oldOnWait, oldDelay := gateWaitOnWait, gateWaitNoticeDelay
	secondWaited := make(chan struct{}, 1)
	gateWaitNoticeDelay = 0
	gateWaitOnWait = func(string) {
		select {
		case secondWaited <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { gateWaitOnWait, gateWaitNoticeDelay = oldOnWait, oldDelay })

	firstPreflightEntered := make(chan struct{})
	allowFirstPreflight := make(chan struct{})
	type result struct {
		h   *workspacegate.MultiHandle
		err error
	}
	firstResult := make(chan result, 1)
	go func() {
		h, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, func() error {
			close(firstPreflightEntered)
			<-allowFirstPreflight
			return nil
		})
		firstResult <- result{h: h, err: err}
	}()
	<-firstPreflightEntered

	secondPreflightEntered := make(chan struct{})
	secondResult := make(chan result, 1)
	go func() {
		h, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, func() error {
			close(secondPreflightEntered)
			return nil
		})
		secondResult <- result{h: h, err: err}
	}()

	select {
	case <-secondWaited:
	case <-secondPreflightEntered:
		t.Fatal("second replacement entered preflight while first held the mutation gates")
	}

	close(allowFirstPreflight)
	first := <-firstResult
	if first.err != nil {
		t.Fatalf("first init mutation gate: %v", first.err)
	}
	if err := first.h.Release(); err != nil {
		t.Fatalf("release first init mutation gate: %v", err)
	}

	<-secondPreflightEntered
	second := <-secondResult
	if second.err != nil {
		t.Fatalf("second init mutation gate: %v", second.err)
	}
	if err := second.h.Release(); err != nil {
		t.Fatalf("release second init mutation gate: %v", err)
	}
}

// holdInitGatesFor takes init's exclusive gate set as a foreign holder would
// (another project's bd init on the same shared dolt dir) and returns its
// handle.
func holdInitGatesFor(t *testing.T, beadsDir, physicalRoot string) *workspacegate.MultiHandle {
	t.Helper()
	h, err := acquireExclusiveWorkspaceGates(context.Background(), beadsDir, "test holder init", physicalRoot)
	if err != nil {
		t.Fatalf("test holder acquisition: %v", err)
	}
	return h
}

// A holder that outlasts the generic 5s exclusive budget but releases within
// init's bound (a concurrent init takes ~8s on a shared server): init waits,
// prints exactly one notice, then proceeds.
func TestAcquireInitMutationGateWaitsPastGenericBudget(t *testing.T) {
	resetGateTestEnv(t)
	beadsDir := newGateTestWorkspace(t)
	physicalRoot := filepath.Join(filepath.Dir(beadsDir), "dolt-data")
	notices := countGateNotices(t)

	if got := initGateWait(); got != initGateWaitDefault || got <= exclusiveGateWait+time.Second {
		t.Fatalf("initGateWait() = %s, want default %s comfortably above exclusiveGateWait %s", got, initGateWaitDefault, exclusiveGateWait)
	}

	holder := holdInitGatesFor(t, beadsDir, physicalRoot)
	const holdFor = 6 * time.Second
	released := time.AfterFunc(holdFor, func() { _ = holder.Release() })
	t.Cleanup(func() { released.Stop(); _ = holder.Release() })

	start := time.Now()
	h, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, nil)
	if err != nil {
		t.Fatalf("init gate with holder released after %s: %v", holdFor, err)
	}
	defer func() { _ = h.Release() }()
	if waited := time.Since(start); waited < holdFor-500*time.Millisecond {
		t.Fatalf("init acquired after %s while the holder held the gate for %s", waited, holdFor)
	}
	if got := notices.Load(); got != 1 {
		t.Fatalf("wait notices = %d, want exactly 1", got)
	}
}

// A holder that outlasts the bound: init still refuses, with an error that
// names the budget and the knob, and keeps ErrBusy in the chain.
func TestAcquireInitMutationGateFailsClearlyPastBound(t *testing.T) {
	resetGateTestEnv(t)
	beadsDir := newGateTestWorkspace(t)
	physicalRoot := filepath.Join(filepath.Dir(beadsDir), "dolt-data")
	notices := countGateNotices(t)
	oldDelay := gateWaitNoticeDelay
	gateWaitNoticeDelay = 5 * time.Second // longer than the bound below
	t.Cleanup(func() { gateWaitNoticeDelay = oldDelay })
	t.Setenv(initGateTimeoutEnv, "300ms")

	holder := holdInitGatesFor(t, beadsDir, physicalRoot)
	t.Cleanup(func() { _ = holder.Release() })

	preflightRan := false
	start := time.Now()
	_, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, func() error {
		preflightRan = true
		return nil
	})
	if err == nil {
		t.Fatal("init gate acquired while a foreign holder kept it past the bound")
	}
	if waited := time.Since(start); waited < 250*time.Millisecond || waited > 3*time.Second {
		t.Fatalf("init gave up after %s, want about the 300ms bound", waited)
	}
	if !errors.Is(err, workspacegate.ErrBusy) {
		t.Fatalf("error %v does not wrap workspacegate.ErrBusy", err)
	}
	for _, want := range []string{"bd init refuses to run over live bd activity", "waited 300ms", initGateTimeoutEnv, "test holder init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if preflightRan {
		t.Error("preflight ran without the gates")
	}
	time.Sleep(50 * time.Millisecond)
	if got := notices.Load(); got != 0 {
		t.Errorf("notice fired %d times for a wait shorter than the notice delay", got)
	}
}

func TestAcquireInitMutationGateHonorsCancellation(t *testing.T) {
	resetGateTestEnv(t)
	beadsDir := newGateTestWorkspace(t)
	physicalRoot := filepath.Join(filepath.Dir(beadsDir), "dolt-data")
	countGateNotices(t)

	holder := holdInitGatesFor(t, beadsDir, physicalRoot)
	t.Cleanup(func() { _ = holder.Release() })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := acquireInitMutationGate(ctx, beadsDir, physicalRoot, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("canceled init kept waiting for %s", waited)
	}
}

func TestInitGateWaitEnv(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"", initGateWaitDefault},
		{"90", 90 * time.Second},
		{"2m", 2 * time.Minute},
		{"1500ms", 1500 * time.Millisecond},
		{"0", initGateWaitDefault},
		{"-5s", initGateWaitDefault},
		{"soon", initGateWaitDefault},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv(initGateTimeoutEnv, tc.raw)
			oldQuiet := quietFlag
			quietFlag = true
			t.Cleanup(func() { quietFlag = oldQuiet })
			if got := initGateWait(); got != tc.want {
				t.Fatalf("initGateWait() with %q = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

func TestAcquireInitMutationGateReleasesOnPreflightError(t *testing.T) {
	resetGateTestEnv(t)
	beadsDir := newGateTestWorkspace(t)
	physicalRoot := filepath.Join(filepath.Dir(beadsDir), "dolt-data")
	refusal := errors.New("destroy token rejected")

	_, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, func() error {
		return refusal
	})
	if !errors.Is(err, refusal) {
		t.Fatalf("init mutation gate error = %v, want preflight refusal", err)
	}

	h, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, nil)
	if err != nil {
		t.Fatalf("init mutation gate remained held after refusal: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("release init mutation gate: %v", err)
	}
}

func TestReleaseWorkspaceGatesIdempotent(t *testing.T) {
	resetGateTestEnv(t)
	beadsDir := newGateTestWorkspace(t)

	list := &cobra.Command{Use: "list"}
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatal(err)
	}
	if workspaceGateHandle == nil {
		t.Fatal("expected a held gate handle")
	}
	releaseWorkspaceGates()
	if workspaceGateHandle != nil {
		t.Error("handle must be cleared on release")
	}
	// Second release must be a no-op, not a panic or double-unlock.
	releaseWorkspaceGates()

	// And the gate must actually be free again: an exclusive acquisition
	// succeeds after release.
	gate, err := workspacegate.ForWorkspace(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := gate.Acquire(context.Background(), workspacegate.Exclusive, workspacegate.Options{})
	if err != nil {
		t.Fatalf("gate still held after releaseWorkspaceGates: %v", err)
	}
	_ = h.Release()
}

// The cross-wiring guarantee: a chokepoint SHARED hold (a normal command
// mid-flight) excludes acquireMigrateGates' EXCLUSIVE acquisition on the
// same workspace. Also exercises the nil-rootCtx path inside
// acquireMigrateGates (tests have no process signal context), which used to
// panic before the nil-context normalization.
func TestChokepointSharedExcludesMigrateExclusive(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)

	// rootCtx is a package global that production sets via
	// setupGracefulShutdown() in PersistentPreRunE and cancels via
	// rootCancel() in PersistentPostRunE WITHOUT resetting the var to nil —
	// harmless in production (the process exits), but any earlier in-process
	// test that exercises the full command path (Execute()) leaves rootCtx
	// pointing at an already-canceled context for whatever test runs next in
	// the same binary. acquireMigrateGates now threads rootCtx through to
	// acquireExclusiveWorkspaceGates, so this test is sensitive to that
	// leak: pin it to nil (the documented "no process signal context yet"
	// case this test exercises) regardless of what ran before it.
	oldRootCtx := rootCtx
	rootCtx = nil
	t.Cleanup(func() { rootCtx = oldRootCtx })

	oldWait := exclusiveGateWait
	exclusiveGateWait = 10 * time.Millisecond
	t.Cleanup(func() { exclusiveGateWait = oldWait })

	list := &cobra.Command{Use: "list"}
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatal(err)
	}
	if workspaceGateHandle == nil {
		t.Fatal("expected a held shared gate handle")
	}

	release, err := acquireMigrateGates(beadsDir, false, "test migrate")
	if err == nil {
		release()
		t.Fatal("migrate EXCLUSIVE acquisition must fail while the chokepoint holds SHARED")
	}

	// After the shared holder releases, the migration proceeds.
	releaseWorkspaceGates()
	release, err = acquireMigrateGates(beadsDir, false, "test migrate")
	if err != nil {
		t.Fatalf("migrate acquisition after shared release: %v", err)
	}
	release()
}
