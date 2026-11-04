package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
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
	before := newGateTestWorkspace(t)
	if err := acquireCommandWorkspaceGates(context.Background(), list, before); err != nil || workspaceGateHandle == nil {
		t.Fatalf("initial command must hold a gate: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "nope", ".beads")
	if err := acquireCommandWorkspaceGates(context.Background(), list, missing); err != nil {
		t.Fatalf("absent beadsDir must be silently ungated, got %v", err)
	}
	if workspaceGateHandle != nil {
		t.Error("absent beadsDir must leave no gate handle")
	}
}

// A process that runs more than one command (tests, embedders) must not keep
// the previous command's SHARED hold once the next command takes its own
// gates: otherwise the first workspace stays held, and a maintenance
// operation there is turned away by a command that has already finished.
func TestAcquireCommandWorkspaceGatesReleasesThePreviousCommandsHold(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	list := &cobra.Command{Use: "list"}
	ctx := context.Background()

	first := newGateTestWorkspace(t)
	if err := acquireCommandWorkspaceGates(ctx, list, first); err != nil || workspaceGateHandle == nil {
		t.Fatalf("first command must hold a gate: %v", err)
	}
	second := newGateTestWorkspace(t)
	if err := acquireCommandWorkspaceGates(ctx, list, second); err != nil || workspaceGateHandle == nil {
		t.Fatalf("second command must hold a gate: %v", err)
	}

	gate, err := workspacegate.ForWorkspace(first)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := gate.Acquire(ctx, workspacegate.Exclusive, workspacegate.Options{Reason: "test maintenance"})
	if err != nil {
		t.Fatalf("exclusive acquire on the first command's workspace once the second command holds its own gates: %v, want success", err)
	}
	_ = holder.Release()
}

// gateCloseProbeStore and gateCloseProbeProvider stand in for the store and
// provider a previous command left open; Close runs the test's probe.
type gateCloseProbeStore struct {
	storage.DoltStorage
	onClose func()
}

func (s *gateCloseProbeStore) Close() error {
	s.onClose()
	return nil
}

type gateCloseProbeProvider struct {
	uow.UnitOfWorkProvider
	onClose func()
}

func (p *gateCloseProbeProvider) Close(context.Context) error {
	p.onClose()
	return nil
}

// When a command's RunE fails, Cobra skips PersistentPostRunE, so the store it
// opened is still open when the same process runs its next command. Before the
// next chokepoint drops the previous command's gates it must close that store
// and provider: gates outlive the store, or an exclusive maintenance operation
// could start against storage that has not quiesced.
func TestAcquireCommandWorkspaceGatesClosesThePreviousCommandsStoreFirst(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	originalStore, originalProvider := store, uowProvider
	t.Cleanup(func() { store, uowProvider = originalStore, originalProvider })
	list := &cobra.Command{Use: "list"}
	ctx := context.Background()

	first := newGateTestWorkspace(t)
	if err := acquireCommandWorkspaceGates(ctx, list, first); err != nil || workspaceGateHandle == nil {
		t.Fatalf("first command must hold a gate: %v", err)
	}
	gate, err := workspacegate.ForWorkspace(first)
	if err != nil {
		t.Fatal(err)
	}
	// Each close records whether the first command's gate was still held.
	var closes []string
	probe := func(what string) func() {
		return func() {
			h, err := gate.Acquire(ctx, workspacegate.Exclusive, workspacegate.Options{})
			switch {
			case errors.Is(err, workspacegate.ErrBusy):
				closes = append(closes, what+" closed under the gate")
			case err == nil:
				_ = h.Release()
				closes = append(closes, what+" closed after the gate dropped")
			default:
				t.Errorf("probing the first command's gate when its %s closed: %v", what, err)
			}
		}
	}
	store = &gateCloseProbeStore{onClose: probe("store")}
	uowProvider = &gateCloseProbeProvider{onClose: probe("provider")}

	if err := acquireCommandWorkspaceGates(ctx, list, newGateTestWorkspace(t)); err != nil {
		t.Fatalf("second command: %v", err)
	}
	if want := []string{"provider closed under the gate", "store closed under the gate"}; !slices.Equal(closes, want) {
		t.Fatalf("the previous command's storage: %v, want %v", closes, want)
	}
	if store != nil || uowProvider != nil {
		t.Fatal("the previous command's store and provider must not survive into the next command")
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
	list := &cobra.Command{Use: "list"}
	prior := newGateTestWorkspace(t)
	if err := acquireCommandWorkspaceGates(context.Background(), list, prior); err != nil || workspaceGateHandle == nil {
		t.Fatalf("initial command must hold a gate: %v", err)
	}
	beadsDir := newGateTestWorkspace(t)
	t.Setenv(sharedGateWaitEnv, "300ms")
	holdWorkspaceGateExclusive(t, beadsDir)

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

	oldDelay := gateWaitNoticeDelay
	gateWaitNoticeDelay = 200 * time.Millisecond
	t.Cleanup(func() { gateWaitNoticeDelay = oldDelay })

	holder := holdWorkspaceGateExclusive(t, beadsDir)
	holdFor := time.Second
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
	// Record every notice immediately. Installed before the init goroutine
	// starts: its notice timer reads these globals.
	var (
		noticeMu sync.Mutex
		notices  []string
	)
	oldDelay, oldOnWait := gateWaitNoticeDelay, gateWaitOnWait
	gateWaitNoticeDelay = 0
	gateWaitOnWait = func(h string) { noticeMu.Lock(); notices = append(notices, h); noticeMu.Unlock() }
	t.Cleanup(func() { gateWaitNoticeDelay, gateWaitOnWait = oldDelay, oldOnWait })
	queuedNotice := func() bool {
		noticeMu.Lock()
		defer noticeMu.Unlock()
		for _, h := range notices {
			if strings.Contains(h, "queued for exclusive access") && strings.Contains(h, "bd init") {
				return true
			}
		}
		return false
	}

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

	// A new ordinary command must not jump the queue: it defers for its
	// budget (the notice names the queued init), then its final attempt
	// gets in anyway because nothing holds the gate exclusively — queued
	// maintenance delays ordinary commands, it never fails them.
	t.Setenv(sharedGateWaitEnv, "300ms")
	list := &cobra.Command{Use: "list"}
	start := time.Now()
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatalf("ordinary command behind a queued (not running) init failed: %v", err)
	}
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Errorf("ordinary command jumped ahead of a queued bd init (acquired after %s)", waited)
	}
	deadlineN := time.Now().Add(time.Second)
	for !queuedNotice() && time.Now().Before(deadlineN) {
		time.Sleep(5 * time.Millisecond)
	}
	if !queuedNotice() {
		noticeMu.Lock()
		t.Errorf("no wait notice named the queued bd init; notices: %q", notices)
		noticeMu.Unlock()
	}
	releaseWorkspaceGates()

	// ...unless its parent bd already holds the gate shared.
	t.Setenv(workspacegate.InheritedHoldEnv, strconv.Itoa(os.Getppid()))
	start = time.Now()
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatalf("child of a shared holder queued behind init: %v", err)
	}
	if waited := time.Since(start); waited > 200*time.Millisecond {
		t.Errorf("child of a shared holder still deferred to the queue for %s", waited)
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
	advertiseSharedHoldEnabled = true
	t.Cleanup(func() { advertiseSharedHoldEnabled = false })
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

// A strict readonly command on a never-gated workspace holds nothing, so it
// must not advertise a shared hold: a child trusting the marker would skip the
// writer-fairness queue for a maintenance operation this process does not
// block. Once the gate files exist the strict readonly hold is real, and it is
// advertised like any other.
func TestSharedHoldAdvertisedOnlyWhenAGateIsHeld(t *testing.T) {
	resetGateTestEnv(t)
	advertiseSharedHoldEnabled = true
	t.Cleanup(func() { advertiseSharedHoldEnabled = false })
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)
	list := &cobra.Command{Use: "list"}
	ctx := context.Background()

	setReadonlyMode(t, true)
	if err := acquireCommandWorkspaceGates(ctx, list, beadsDir); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(workspacegate.InheritedHoldEnv); got != "" {
		t.Fatalf("%s while a strict readonly command holds nothing = %q, want it left empty", workspacegate.InheritedHoldEnv, got)
	}
	releaseWorkspaceGates()

	// A normal command materializes the gate files.
	setReadonlyMode(t, false)
	if err := acquireCommandWorkspaceGates(ctx, list, beadsDir); err != nil {
		t.Fatal(err)
	}
	releaseWorkspaceGates()

	setReadonlyMode(t, true)
	if err := acquireCommandWorkspaceGates(ctx, list, beadsDir); err != nil {
		t.Fatal(err)
	}
	if got, want := os.Getenv(workspacegate.InheritedHoldEnv), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("%s while a strict readonly command holds existing gates = %q, want %q", workspacegate.InheritedHoldEnv, got, want)
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

// Hooks must never stall the user's git command behind a maintenance
// operation: the bd subprocesses a hook spawns get BD_GIT_HOOK=1 from
// hookSubprocessEnv, and that alone makes their gate acquisition fail fast,
// whatever BEADS_GATE_WAIT_TIMEOUT says.
func TestHookSubprocessEnvKeepsGateFailFast(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	t.Setenv(sharedGateWaitEnv, "2m")
	var hookVal string
	for _, kv := range hookSubprocessEnv([]string{"PATH=/bin"}) {
		if v, ok := strings.CutPrefix(kv, "BD_GIT_HOOK="); ok {
			hookVal = v
		}
	}
	if hookVal == "" {
		t.Fatal("hookSubprocessEnv does not set BD_GIT_HOOK; hook subprocesses would wait on the workspace gate")
	}
	t.Setenv("BD_GIT_HOOK", hookVal)
	if got := sharedGateWait(); got != 0 {
		t.Fatalf("sharedGateWait() in a hook subprocess env = %s, want 0 (fail fast)", got)
	}

	beadsDir := newGateTestWorkspace(t)
	holdWorkspaceGateExclusive(t, beadsDir)
	start := time.Now()
	var err error
	captureStderr(t, func() {
		err = acquireCommandWorkspaceGates(context.Background(), &cobra.Command{Use: "export"}, beadsDir)
	})
	if err == nil {
		t.Fatal("hook subprocess acquired the gate under an exclusive holder")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("hook subprocess waited %s on the gate", waited)
	}
}

// In shared-server mode the exclusive holder may be another project's
// operation, so the error must not claim it is on "this workspace".
func TestAcquireCommandWorkspaceGatesSharedServerErrorText(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	t.Setenv(sharedGateWaitEnv, "200ms")
	holdWorkspaceGateExclusive(t, beadsDir)

	var err error
	stderr := captureStderr(t, func() {
		err = acquireCommandWorkspaceGates(context.Background(), &cobra.Command{Use: "list"}, beadsDir)
	})
	if err == nil {
		t.Fatal("acquired under an exclusive holder")
	}
	for _, want := range []string{"running on the shared server (possibly from another project)", "test maintenance"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q missing %q", stderr, want)
		}
	}
}

// bd edit's $EDITOR (which can outlive bd by hours) must not inherit the
// shared-hold marker.
func TestEnvWithoutSharedHoldMarker(t *testing.T) {
	t.Setenv(workspacegate.InheritedHoldEnv, "12345")
	t.Setenv("BEADS_GATE_TEST_KEEP", "1")
	env := envWithoutSharedHoldMarker()
	var kept bool
	for _, kv := range env {
		if strings.HasPrefix(kv, workspacegate.InheritedHoldEnv+"=") {
			t.Fatalf("marker leaked into editor env: %q", kv)
		}
		kept = kept || kv == "BEADS_GATE_TEST_KEEP=1"
	}
	if !kept {
		t.Fatal("envWithoutSharedHoldMarker dropped unrelated variables")
	}
}

// setReadonlyMode pins strict --readonly for one test, as PersistentPreRunE
// leaves readonlyMode by the time it acquires the gates.
func setReadonlyMode(t *testing.T, on bool) {
	t.Helper()
	original := readonlyMode
	readonlyMode = on
	t.Cleanup(func() { readonlyMode = original })
}

// newGateTestServerWorkspace is newGateTestWorkspace for a local-server
// workspace: its physical root is .beads/dolt, not .beads/embeddeddolt.
func newGateTestServerWorkspace(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"dolt","database":"beads.db","dolt_mode":"server"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// dirEntryNames lists dir's immediate entries by name, so a test can prove a
// command left the directory exactly as it found it.
func dirEntryNames(t *testing.T, dir string) []string {
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

// Strict --readonly is mutation-free (effectiveRootStorePolicy), so taking the
// command's gates must not create any gate file: not the workspace gate beside
// .beads, and not the physical-root gate inside it (.beads/embeddeddolt for an
// embedded workspace, .beads/dolt for a local server one).
func TestAcquireCommandWorkspaceGatesStrictReadonlyCreatesNoGateFiles(t *testing.T) {
	layouts := []struct {
		name  string
		build func(*testing.T) string
	}{
		{"embedded", newGateTestWorkspace},
		{"local server", newGateTestServerWorkspace},
	}
	for _, tc := range layouts {
		t.Run(tc.name, func(t *testing.T) {
			resetGateTestEnv(t)
			t.Cleanup(releaseWorkspaceGates)
			setReadonlyMode(t, true)
			beadsDir := tc.build(t)
			projectDir := filepath.Dir(beadsDir)
			projectBefore, beadsBefore := dirEntryNames(t, projectDir), dirEntryNames(t, beadsDir)

			list := &cobra.Command{Use: "list"}
			if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
				t.Fatalf("strict readonly acquisition on a never-gated workspace: %v", err)
			}
			// SHARED fail-open also returns nil and creates nothing; it is
			// told apart by running with no handle at all.
			if workspaceGateHandle == nil {
				t.Fatal("strict readonly acquisition on a never-gated workspace ran ungated, want a handle that holds nothing")
			}
			if !workspaceGateHandle.Empty() {
				t.Fatal("strict readonly acquisition on a never-gated workspace locked a gate file")
			}
			if got := dirEntryNames(t, projectDir); !slices.Equal(got, projectBefore) {
				t.Errorf("strict readonly changed %s\n before: %v\n after:  %v", projectDir, projectBefore, got)
			}
			if got := dirEntryNames(t, beadsDir); !slices.Equal(got, beadsBefore) {
				t.Errorf("strict readonly changed %s\n before: %v\n after:  %v", beadsDir, beadsBefore, got)
			}
		})
	}
}

// Not creating a gate file must not mean not honoring one. Once a workspace's
// gate files exist (any normal bd command creates them), a strict readonly
// command still holds them SHARED — so maintenance sees it and refuses to run
// over it — and is still refused while maintenance holds the gate.
func TestAcquireCommandWorkspaceGatesStrictReadonlyStillHonorsExistingGates(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)
	list := &cobra.Command{Use: "list"}
	ctx := context.Background()

	// A normal command materializes the gate files.
	setReadonlyMode(t, false)
	if err := acquireCommandWorkspaceGates(ctx, list, beadsDir); err != nil {
		t.Fatal(err)
	}
	releaseWorkspaceGates()

	setReadonlyMode(t, true)
	if err := acquireCommandWorkspaceGates(ctx, list, beadsDir); err != nil {
		t.Fatalf("strict readonly acquisition against existing gate files: %v", err)
	}
	gate, err := workspacegate.ForWorkspace(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Acquire(ctx, workspacegate.Exclusive, workspacegate.Options{}); !errors.Is(err, workspacegate.ErrBusy) {
		t.Fatalf("exclusive acquire while a strict readonly command is running: err = %v, want ErrBusy", err)
	}
	releaseWorkspaceGates()

	holder, err := gate.Acquire(ctx, workspacegate.Exclusive, workspacegate.Options{Reason: "test maintenance"})
	if err != nil {
		t.Fatalf("exclusive acquire after the strict readonly command released: %v", err)
	}
	defer func() { _ = holder.Release() }()
	// Bound the shared wait: resetGateTestEnv clears it, which means the 30s default.
	t.Setenv(sharedGateWaitEnv, "300ms")
	if err := acquireCommandWorkspaceGates(ctx, list, beadsDir); err == nil {
		t.Fatal("strict readonly acquisition under a foreign exclusive holder must abort, got nil error")
	}
}

// Only STRICT readonly skips creating the gate files. A normal command keeps
// creating them: that is what makes it visible to a maintenance operation that
// starts later (the exclusive acquirer locks the same file), so the first gate
// holder on a workspace cannot go unseen.
func TestAcquireCommandWorkspaceGatesNormalCommandStillCreatesGateFiles(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	setReadonlyMode(t, false)
	beadsDir := newGateTestWorkspace(t)

	list := &cobra.Command{Use: "list"}
	if err := acquireCommandWorkspaceGates(context.Background(), list, beadsDir); err != nil {
		t.Fatal(err)
	}
	for _, gateFile := range []string{
		filepath.Join(filepath.Dir(beadsDir), ".beads.gate.lock"),
		filepath.Join(beadsDir, "embeddeddolt.gate.lock"),
	} {
		if _, err := os.Stat(gateFile); err != nil {
			t.Errorf("a normal command must create its gate file: %v", err)
		}
	}
}

// End to end: the real `bd --readonly` leaves no gate file behind. The
// in-process tests above pin acquireCommandWorkspaceGates; this one pins that
// the command line reaches it with strict readonly already resolved.
func TestStrictReadonlyCommandCreatesNoGateFiles(t *testing.T) {
	bdBin := buildBDForInitTests(t)
	beadsDir := newGateTestServerWorkspace(t)
	projectDir := filepath.Dir(beadsDir)

	cmd := exec.Command(bdBin, "--readonly", "list")
	cmd.Dir = projectDir
	cmd.Env = hermeticInitEnv(t.TempDir(), "BEADS_DIR="+beadsDir, "BEADS_DOLT_AUTO_START=0")
	// No Dolt server sits behind this workspace, so the command is expected to
	// fail when it opens the store; only what it left on disk matters here.
	out, _ := cmd.CombinedOutput()

	var gateFiles []string
	for _, dir := range []string{projectDir, beadsDir} {
		for _, name := range dirEntryNames(t, dir) {
			if strings.Contains(name, ".gate.lock") {
				gateFiles = append(gateFiles, filepath.Join(dir, name))
			}
		}
	}
	if len(gateFiles) != 0 {
		t.Fatalf("bd --readonly created gate files %v\noutput: %s", gateFiles, out)
	}
}
