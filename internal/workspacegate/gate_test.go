package workspacegate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// TestMain doubles as the cross-process helper: when WORKSPACEGATE_HELPER
// is set, the binary re-executes into helperMain instead of the test
// runner. This keeps the cross-process tests self-contained in the normal
// `go test` binary with no fixture binaries to build.
func TestMain(m *testing.M) {
	if os.Getenv("WORKSPACEGATE_HELPER") != "" {
		helperMain()
		return
	}
	os.Exit(m.Run())
}

// helperMain: modes
//
//	hold <gate-parent-dir> <shared|exclusive> — acquire, print ACQUIRED,
//	    hold until stdin closes, release, print RELEASED.
//	intent <gate-parent-dir> — hold the gate's writer-fairness intent lock
//	    (as a queued exclusive acquirer does) until stdin closes.
//	inherited-acquire <gate-parent-dir> — waiting shared acquisition with
//	    IgnoreQueuedExclusive = InheritedSharedHold(); prints the outcome.
//	sleep — sleep long without touching any gate (handle-inheritance probe).
//	exit — exit immediately, so the caller has a real, now-dead PID (used
//	    to fabricate a stale holder-info sidecar in tests).
func helperMain() {
	switch os.Getenv("WORKSPACEGATE_HELPER") {
	case "exit":
		os.Exit(0)
	case "hold":
		dir := os.Args[1]
		mode := Shared
		if os.Args[2] == "exclusive" {
			mode = Exclusive
		}
		g, err := ForWorkspace(filepath.Join(dir, ".beads"))
		if err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		h, err := g.Acquire(context.Background(), mode, Options{Reason: "test-helper"})
		if err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		fmt.Println("ACQUIRED")
		_, _ = io.Copy(io.Discard, os.Stdin)
		if err := h.Release(); err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		fmt.Println("RELEASED")
	case "intent":
		// Hold the writer-fairness intent lock for <dir>/.beads's gate, as
		// a queued exclusive acquirer would, until stdin closes.
		g, err := ForWorkspace(filepath.Join(os.Args[1], ".beads"))
		if err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		f := g.tryTakeIntent("test-intent-helper")
		if f == nil {
			fmt.Println("ERR intent lock unavailable")
			os.Exit(1)
		}
		fmt.Println("ACQUIRED")
		_, _ = io.Copy(io.Discard, os.Stdin)
		g.releaseIntent(f)
		fmt.Println("RELEASED")
	case "inherited-acquire":
		// Waiting shared acquisition the way bd's chokepoint does it,
		// honoring an inherited shared hold; reports how long it took.
		g, err := ForWorkspace(filepath.Join(os.Args[1], ".beads"))
		if err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		inherited := InheritedSharedHold()
		start := time.Now()
		h, err := g.Acquire(context.Background(), Shared, Options{
			Wait: 3 * time.Second, PollInterval: 20 * time.Millisecond,
			IgnoreQueuedExclusive: inherited,
		})
		if err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		_ = h.Release()
		fmt.Printf("inherited=%v waited_ms=%d\n", inherited, time.Since(start).Milliseconds())
	case "sleep":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func testGate(t *testing.T) (Gate, string) {
	t.Helper()
	dir := t.TempDir()
	g, err := ForWorkspace(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatalf("ForWorkspace: %v", err)
	}
	return g, dir
}

func mustAcquire(t *testing.T, g Gate, mode Mode, opts Options) *Handle {
	t.Helper()
	h, err := g.Acquire(context.Background(), mode, opts)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", mode, err)
	}
	return h
}

func TestGateFileLocation(t *testing.T) {
	g, dir := testGate(t)
	want := filepath.Join(mustEval(t, dir), ".beads.gate.lock")
	if g.Path() != want {
		t.Fatalf("gate path = %s, want %s (sibling of .beads, never inside it)", g.Path(), want)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return out
}

func TestSharedHoldersCoexist(t *testing.T) {
	g, _ := testGate(t)
	h1 := mustAcquire(t, g, Shared, Options{})
	defer h1.Release()
	h2 := mustAcquire(t, g, Shared, Options{})
	defer h2.Release()
}

func TestExclusiveConflicts(t *testing.T) {
	g, _ := testGate(t)

	hEx := mustAcquire(t, g, Exclusive, Options{Reason: "unit test"})
	if _, err := g.Acquire(context.Background(), Shared, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("shared under exclusive: err = %v, want ErrBusy", err)
	}
	if _, err := g.Acquire(context.Background(), Exclusive, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("exclusive under exclusive: err = %v, want ErrBusy", err)
	}
	if err := hEx.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	hSh := mustAcquire(t, g, Shared, Options{})
	if _, err := g.Acquire(context.Background(), Exclusive, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("exclusive under shared: err = %v, want ErrBusy", err)
	}
	_ = hSh.Release()
}

func TestReleaseIdempotent(t *testing.T) {
	g, _ := testGate(t)
	h := mustAcquire(t, g, Exclusive, Options{})
	if err := h.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	var nilH *Handle
	if err := nilH.Release(); err != nil {
		t.Fatalf("nil Release: %v", err)
	}
}

func TestWaitAcquiresAfterRelease(t *testing.T) {
	g, _ := testGate(t)
	h := mustAcquire(t, g, Exclusive, Options{})

	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = h.Release()
		close(released)
	}()

	notified := false
	h2, err := g.Acquire(context.Background(), Shared, Options{
		Wait:         5 * time.Second,
		PollInterval: 25 * time.Millisecond,
		OnWait:       func(string) { notified = true },
	})
	if err != nil {
		t.Fatalf("waiting Acquire: %v", err)
	}
	defer h2.Release()
	<-released
	if !notified {
		t.Fatal("OnWait was not called for a contended acquisition")
	}
}

func TestContextCancelDuringWait(t *testing.T) {
	g, _ := testGate(t)
	h := mustAcquire(t, g, Exclusive, Options{})
	defer h.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := g.Acquire(ctx, Shared, Options{Wait: time.Minute, PollInterval: 20 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestExclusiveInfoSidecar(t *testing.T) {
	g, _ := testGate(t)
	h := mustAcquire(t, g, Exclusive, Options{Reason: "migration dry run"})

	_, err := g.Acquire(context.Background(), Shared, Options{})
	if err == nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("expected busy error, got %v", err)
	}
	for _, want := range []string{"migration dry run", fmt.Sprint(os.Getpid())} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("busy error %q missing holder detail %q", err, want)
		}
	}

	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(g.infoPath()); !os.IsNotExist(err) {
		t.Fatalf("info sidecar not removed on release: %v", err)
	}
	if _, err := os.Stat(g.Path()); err != nil {
		t.Fatalf("gate file must never be deleted: %v", err)
	}
}

func TestExclusiveHolder(t *testing.T) {
	g, _ := testGate(t)

	// Never-gated workspace: no gate file, not held, no error — and the
	// diagnostic must not create the gate file as a side effect.
	held, _, err := g.ExclusiveHolder()
	if err != nil || held {
		t.Fatalf("fresh gate: held=%v err=%v, want false/nil", held, err)
	}
	if _, statErr := os.Stat(g.Path()); !os.IsNotExist(statErr) {
		t.Fatalf("diagnostic probe created the gate file: %v", statErr)
	}

	hSh := mustAcquire(t, g, Shared, Options{})
	held, _, err = g.ExclusiveHolder()
	if err != nil || held {
		t.Fatalf("under shared holder: held=%v err=%v, want false/nil (shared is indistinguishable from free by design)", held, err)
	}
	_ = hSh.Release()

	hEx := mustAcquire(t, g, Exclusive, Options{Reason: "probe test"})
	held, info, err := g.ExclusiveHolder()
	if err != nil || !held {
		t.Fatalf("under exclusive holder: held=%v err=%v, want true/nil", held, err)
	}
	if info == nil || info.Reason != "probe test" {
		t.Fatalf("probe info = %+v, want reason 'probe test'", info)
	}
	_ = hEx.Release()
}

// The diagnostic must not inject spurious failures into concurrent
// fail-fast shared acquirers — the reason it probes with a shared lock,
// never an exclusive one.
func TestExclusiveHolderDoesNotDisturbSharedAcquirers(t *testing.T) {
	g, _ := testGate(t)
	// Materialize the gate file once so the probe exercises the lock path.
	h0 := mustAcquire(t, g, Shared, Options{})
	_ = h0.Release()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_, _, _ = g.ExclusiveHolder()
			}
		}
	}()
	for i := 0; i < 300; i++ {
		h, err := g.Acquire(context.Background(), Shared, Options{})
		if err != nil {
			t.Fatalf("iteration %d: fail-fast shared acquire failed under concurrent probing: %v", i, err)
		}
		_ = h.Release()
	}
	close(stop)
	<-done
}

// A cancelled context must surface as the context error even in
// fail-fast mode (Wait == 0), so callers can tell shutdown from
// contention.
func TestCancelledContextBeatsBusy(t *testing.T) {
	g, _ := testGate(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Acquire(ctx, Shared, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// Degenerate guarded paths (filesystem roots) must be refused, not
// silently gated under a junk name.
func TestForDirRejectsDegeneratePaths(t *testing.T) {
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	if _, err := ForWorkspace(root); err == nil {
		t.Fatal("gating a filesystem root must be refused")
	}
}

// AcquireAll's Wait is a total budget across the gate set, not per gate.
func TestAcquireAllTotalWaitBudget(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) Gate {
		g, err := ForPhysicalRoot(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	ga, gb, gc := mk("aroot"), mk("broot"), mk("croot")
	block := mustAcquire(t, gb, Exclusive, Options{})
	defer block.Release()

	calls := 0
	start := time.Now()
	_, err := AcquireAll(context.Background(), Shared, Options{
		Wait:         300 * time.Millisecond,
		PollInterval: 50 * time.Millisecond,
		OnWait:       func(string) { calls++ },
	}, ga, gb, gc)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("AcquireAll over 3 gates took %v with a 300ms total budget (per-gate budgets?)", elapsed)
	}
	if calls != 1 {
		t.Fatalf("OnWait fired %d times across the set, want exactly 1", calls)
	}
}

func TestAcquireAllSortsDedupesAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) Gate {
		g, err := ForPhysicalRoot(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ForPhysicalRoot(%s): %v", name, err)
		}
		return g
	}
	ga, gb, gc := mk("aroot"), mk("broot"), mk("croot")

	// Dedupe: same gate twice acquires once and releases cleanly.
	m, err := AcquireAll(context.Background(), Shared, Options{}, gb, ga, gb)
	if err != nil {
		t.Fatalf("AcquireAll: %v", err)
	}
	if len(m.handles) != 2 {
		t.Fatalf("dedupe failed: %d handles, want 2", len(m.handles))
	}
	if m.handles[0].gate.path >= m.handles[1].gate.path {
		t.Fatalf("gates not acquired in sorted order: %s then %s",
			m.handles[0].gate.path, m.handles[1].gate.path)
	}
	if err := m.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Partial failure: pre-hold the middle gate exclusively; AcquireAll
	// must fail and leave the first gate free again.
	block := mustAcquire(t, gb, Exclusive, Options{})
	if _, err := AcquireAll(context.Background(), Exclusive, Options{}, ga, gb, gc); !errors.Is(err, ErrBusy) {
		t.Fatalf("AcquireAll with blocked middle gate: err = %v, want ErrBusy", err)
	}
	_ = block.Release()
	m2, err := AcquireAll(context.Background(), Exclusive, Options{}, ga, gb, gc)
	if err != nil {
		t.Fatalf("AcquireAll after cleanup: %v (first gate leaked from failed attempt?)", err)
	}
	_ = m2.Release()
}

func TestCanonicalizationAgreesAcrossSymlinkSpellings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	g1, err := ForWorkspace(filepath.Join(real, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	g2, err := ForWorkspace(filepath.Join(link, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	if g1.Path() != g2.Path() {
		t.Fatalf("same physical workspace produced two gates: %s vs %s", g1.Path(), g2.Path())
	}

	// A guarded directory that IS a symlink has no stable gate identity
	// (identity must survive the directory being absent or recreated, so
	// it derives from parent + literal base, never from resolving the
	// guarded path) — it is refused rather than gated ambiguously.
	realBeads := filepath.Join(real, ".beads")
	if err := os.Mkdir(realBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	linkBeads := filepath.Join(dir, "beads-alias")
	if err := os.Symlink(realBeads, linkBeads); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := ForWorkspace(linkBeads); err == nil {
		t.Fatal("ForWorkspace on a symlinked .beads must be refused")
	}
	if _, err := ForWorkspace(realBeads); err != nil {
		t.Fatalf("ForWorkspace on the physical .beads: %v", err)
	}
}

// A Wait shorter than the poll interval must come back within (about)
// Wait, not a full poll interval later.
func TestWaitBudgetCapsPolling(t *testing.T) {
	g, _ := testGate(t)
	h := mustAcquire(t, g, Exclusive, Options{})
	defer h.Release()

	start := time.Now()
	_, err := g.Acquire(context.Background(), Shared, Options{
		Wait:         20 * time.Millisecond,
		PollInterval: 10 * time.Second,
	})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("acquisition with 20ms budget took %v (slept a full poll interval?)", elapsed)
	}
}

// A pre-planted symlink at the sidecar path must not be followed: plain
// WriteFile would truncate the symlink's target on every exclusive
// acquisition.
func TestInfoSidecarDoesNotFollowSymlink(t *testing.T) {
	g, dir := testGate(t)

	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, g.infoPath()); err != nil {
		t.Skipf("symlink: %v", err)
	}

	h := mustAcquire(t, g, Exclusive, Options{Reason: "attack test"})
	_ = h.Release()

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("victim file unreadable after acquisition: %v", err)
	}
	if string(got) != "precious" {
		t.Fatalf("victim file was modified through the sidecar symlink: %q", got)
	}
}

// deadPID returns a real PID that is guaranteed already dead: spawn a
// trivial helper subprocess and wait for it to exit.
func deadPID(t *testing.T) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(bazeltest.ShardFreeEnv(os.Environ()), "WORKSPACEGATE_HELPER=exit")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running exit helper: %v", err)
	}
	return cmd.Process.Pid
}

// writeStaleInfo fabricates a holder-info sidecar naming a known-dead PID,
// bypassing the normal writeInfo path (which always records the live
// caller's own PID).
func writeStaleInfo(t *testing.T, g Gate, pid int) {
	t.Helper()
	host, _ := os.Hostname()
	data, err := json.Marshal(Info{
		PID:       pid,
		Hostname:  host,
		Reason:    "stale test holder",
		StartedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.infoPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// busyDetail must report a sidecar naming a known-dead PID as stale, not
// as fact, in BOTH Shared and Exclusive modes (2c: the Exclusive branch
// already qualified against pidAlive; the Shared branch did not).
func TestBusyDetailStaleHolderBothModes(t *testing.T) {
	g, _ := testGate(t)
	pid := deadPID(t)

	// Materialize the gate file so g.path exists (busyDetail only reads
	// the sidecar, but keep the setup realistic).
	h := mustAcquire(t, g, Shared, Options{})
	writeStaleInfo(t, g, pid)

	for _, mode := range []Mode{Shared, Exclusive} {
		got := g.busyDetail(mode)
		if !strings.Contains(got, "stale") || !strings.Contains(got, fmt.Sprint(pid)) {
			t.Errorf("busyDetail(%s) = %q, want it to call out dead pid %d as stale", mode, got, pid)
		}
	}
	_ = h.Release()
}

// The comparison-key function AcquireAll uses for dedupe/sort must lower
// on case-insensitive-capable platforms (Windows, macOS) and leave the
// path untouched elsewhere. Case-variant dedupe itself is hard to test
// portably (it depends on the actual filesystem's case sensitivity, not
// just the GOOS), so this exercises the key function directly.
func TestGateKeyCaseFolding(t *testing.T) {
	const mixed = "/Some/Mixed/Case/Path.gate.lock"
	got := gateKey(mixed)
	switch runtime.GOOS {
	case "windows", "darwin":
		if got != strings.ToLower(mixed) {
			t.Fatalf("gateKey(%q) = %q on %s, want lowercased", mixed, got, runtime.GOOS)
		}
	default:
		if got != mixed {
			t.Fatalf("gateKey(%q) = %q on %s, want unchanged", mixed, got, runtime.GOOS)
		}
	}
}

// ExclusiveHolder's error return must be distinguishable from "not held":
// an unreadable gate file (permission denied) is an error, not a false
// "held=false".
func TestExclusiveHolderErrorReturn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	g, _ := testGate(t)

	// Materialize the gate file, then strip all access so opening it
	// fails with a permission error rather than ENOENT.
	h := mustAcquire(t, g, Shared, Options{})
	_ = h.Release()
	if err := os.Chmod(g.Path(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(g.Path(), 0o600) })

	held, info, err := g.ExclusiveHolder()
	if err == nil {
		t.Fatalf("ExclusiveHolder on an unreadable gate file: held=%v info=%+v err=nil, want a non-nil error", held, info)
	}
	if held {
		t.Fatalf("ExclusiveHolder on an unreadable gate file reported held=true with an error; want held=false alongside the error")
	}
}

// --- cross-process tests ---

// holderProc is a helper process holding the gate for <dir>/.beads.
type holderProc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	drained chan struct{}
}

// stop asks the holder to release (stdin EOF), waits for its output to be
// fully drained (os/exec forbids Wait before pipe reads complete), then
// reaps it.
func (h *holderProc) stop() {
	_ = h.stdin.Close()
	<-h.drained
	_ = h.cmd.Wait()
}

// spawnHolder starts this test binary in helper mode and waits until it
// reports the lock acquired.
func spawnHolder(t *testing.T, dir, mode string) *holderProc {
	t.Helper()
	return spawnHelperProc(t, "hold", dir, mode)
}

// spawnHelperProc starts this test binary in the given helper mode and
// waits for it to print ACQUIRED.
func spawnHelperProc(t *testing.T, helperMode string, args ...string) *holderProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(bazeltest.ShardFreeEnv(os.Environ()), "WORKSPACEGATE_HELPER="+helperMode)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	sc := bufio.NewScanner(stdout)
	if !sc.Scan() || sc.Text() != "ACQUIRED" {
		t.Fatalf("helper did not acquire: %q (scan err %v)", sc.Text(), sc.Err())
	}
	drained := make(chan struct{})
	go func() { // drain remaining output so the child never blocks on write
		defer close(drained)
		for sc.Scan() {
		}
	}()
	return &holderProc{cmd: cmd, stdin: stdin, drained: drained}
}

func TestCrossProcessExclusion(t *testing.T) {
	dir := t.TempDir()
	g, err := ForWorkspace(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatal(err)
	}

	hp := spawnHolder(t, dir, "exclusive")

	if _, err := g.Acquire(context.Background(), Shared, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("shared while other process holds exclusive: %v, want ErrBusy", err)
	}
	held, info, err := g.ExclusiveHolder()
	if err != nil || !held || info == nil || info.PID != hp.cmd.Process.Pid {
		t.Fatalf("probe = %v/%+v/%v, want exclusive by pid %d", held, info, err, hp.cmd.Process.Pid)
	}

	hp.stop() // orderly release
	h, err := g.Acquire(context.Background(), Exclusive,
		Options{Wait: 10 * time.Second, PollInterval: 25 * time.Millisecond})
	if err != nil {
		t.Fatalf("acquire after helper release: %v", err)
	}
	_ = h.Release()
}

func TestCrossProcessSharedCoexist(t *testing.T) {
	dir := t.TempDir()
	g, err := ForWorkspace(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	hp := spawnHolder(t, dir, "shared")
	h, err := g.Acquire(context.Background(), Shared, Options{})
	if err != nil {
		t.Fatalf("second shared holder across processes: %v", err)
	}
	if _, err := g.Acquire(context.Background(), Exclusive, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("exclusive under cross-process shared: %v, want ErrBusy", err)
	}
	_ = h.Release()
	hp.stop()
}

func TestCrashReleasesLock(t *testing.T) {
	dir := t.TempDir()
	g, err := ForWorkspace(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	hp := spawnHolder(t, dir, "exclusive")

	if err := hp.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	<-hp.drained
	_ = hp.cmd.Wait()

	// The OS releases the flock with the process; the stale info sidecar
	// must not prevent acquisition.
	h, err := g.Acquire(context.Background(), Exclusive,
		Options{Wait: 10 * time.Second, PollInterval: 25 * time.Millisecond})
	if err != nil {
		t.Fatalf("acquire after holder crash: %v", err)
	}
	_ = h.Release()
}

func TestSpawnedChildDoesNotInheritGate(t *testing.T) {
	dir := t.TempDir()
	g, err := ForWorkspace(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	h := mustAcquire(t, g, Exclusive, Options{})

	// Spawn a child that sleeps while we hold the gate, mirroring how a
	// bd command spawns a long-lived dolt/proxy child. If the lock handle
	// leaked into the child, releasing here would not free the gate.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(exe)
	child.Env = append(bazeltest.ShardFreeEnv(os.Environ()), "WORKSPACEGATE_HELPER=sleep")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _, _ = child.Process.Wait() })

	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	h2, err := g.Acquire(context.Background(), Exclusive, Options{})
	if err != nil {
		t.Fatalf("gate still held after release with live child — handle inherited? %v", err)
	}
	_ = h2.Release()
}

// --- writer fairness (exclusive intent) ---

// A continuous stream of overlapping shared holders keeps the gate busy
// forever, so without writer fairness a waiting exclusive acquirer would
// exhaust its budget. With it, the exclusive acquirer gets the gate as soon
// as the shared holders that were already in drain, and the stream resumes
// after it releases.
func TestExclusiveWaiterNotStarvedBySharedStream(t *testing.T) {
	g, _ := testGate(t)
	first := mustAcquire(t, g, Shared, Options{})

	const (
		workers = 4
		hold    = 150 * time.Millisecond
	)
	stop := make(chan struct{})
	done := make(chan struct{})
	var acquired, failed atomic.Int64
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				// Stagger so the gate is never free between holders.
				time.Sleep(time.Duration(w) * hold / workers)
				for {
					select {
					case <-stop:
						return
					default:
					}
					h, err := g.Acquire(context.Background(), Shared,
						Options{Wait: 10 * time.Second, PollInterval: 10 * time.Millisecond})
					if err != nil {
						failed.Add(1)
						continue
					}
					acquired.Add(1)
					time.Sleep(hold)
					_ = h.Release()
				}
			}(w)
		}
		wg.Wait()
	}()
	t.Cleanup(func() { close(stop); <-done })

	// Let the stream get going, then release the original holder: from
	// here on the gate is only ever held by overlapping stream members.
	time.Sleep(2 * hold)
	_ = first.Release()

	start := time.Now()
	h, err := g.Acquire(context.Background(), Exclusive,
		Options{Wait: 5 * time.Second, PollInterval: 10 * time.Millisecond, Reason: "test exclusive waiter"})
	if err != nil {
		t.Fatalf("exclusive acquirer starved by a shared stream: %v", err)
	}
	waited := time.Since(start)
	// Bounded by the in-flight holders draining (one hold), plus slack.
	if waited > 2*time.Second {
		t.Errorf("exclusive waited %s behind a stream of %s shared holds", waited, hold)
	}
	if g.ExclusiveQueued() {
		t.Error("intent lock still held after the exclusive acquirer got the gate")
	}
	before := acquired.Load()
	time.Sleep(3 * hold)
	if got := acquired.Load(); got != before {
		t.Errorf("shared stream acquired %d times while the exclusive holder held the gate", got-before)
	}
	_ = h.Release()

	// The stream resumes once the exclusive holder is gone.
	deadline := time.Now().Add(3 * time.Second)
	for acquired.Load() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired.Load() == before {
		t.Error("shared stream did not resume after the exclusive holder released")
	}
	if n := failed.Load(); n != 0 {
		t.Errorf("%d shared acquisitions failed; with a 10s budget all should wait", n)
	}
}

// A waiting shared acquirer defers to queued intent (and says so), but a
// fail-fast one and one whose ancestor holds the gate do not.
func TestSharedWaitDefersToQueuedExclusive(t *testing.T) {
	g, _ := testGate(t)
	intent := g.tryTakeIntent("test queued maintenance")
	if intent == nil {
		t.Fatal("tryTakeIntent on a fresh gate returned nil")
	}
	t.Cleanup(func() { g.releaseIntent(intent) })

	// Deferred for its whole budget, then the final attempt ignores the
	// queue: delayed, never failed, because nothing holds the gate.
	var holder string
	start := time.Now()
	h, err := g.Acquire(context.Background(), Shared, Options{
		Wait: 300 * time.Millisecond, PollInterval: 20 * time.Millisecond,
		OnWait: func(h string) { holder = h },
	})
	if err != nil {
		t.Fatalf("waiting shared under queued intent with a free gate: %v, want success on the final attempt", err)
	}
	_ = h.Release()
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Errorf("shared acquirer did not defer to queued intent (acquired after %s)", waited)
	}
	if !strings.Contains(holder, "queued for exclusive access") || !strings.Contains(holder, "test queued maintenance") {
		t.Errorf("wait notice %q does not name the queued exclusive acquirer", holder)
	}

	h = mustAcquire(t, g, Shared, Options{}) // fail-fast: legacy behavior
	_ = h.Release()
	h = mustAcquire(t, g, Shared, Options{Wait: time.Second, IgnoreQueuedExclusive: true})
	_ = h.Release()

	// Once the intent is withdrawn, a waiting shared acquirer proceeds.
	g.releaseIntent(intent)
	intent = nil
	h = mustAcquire(t, g, Shared, Options{Wait: time.Second})
	_ = h.Release()
}

// Ordinary shared traffic never creates intent files, and an uncontended
// exclusive acquisition does not either; only a waiting exclusive does, and
// it withdraws its intent when it gives up.
func TestIntentLifecycle(t *testing.T) {
	g, _ := testGate(t)
	h := mustAcquire(t, g, Shared, Options{Wait: time.Second})
	_ = h.Release()
	h = mustAcquire(t, g, Exclusive, Options{Wait: time.Second})
	_ = h.Release()
	if _, err := os.Stat(g.intentPath()); !os.IsNotExist(err) {
		t.Fatalf("intent file exists without any contended exclusive wait (stat err %v)", err)
	}

	sh := mustAcquire(t, g, Shared, Options{})
	_, err := g.Acquire(context.Background(), Exclusive,
		Options{Wait: 300 * time.Millisecond, PollInterval: 20 * time.Millisecond, Reason: "test giving up"})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("exclusive under shared holder: %v, want ErrBusy", err)
	}
	if _, err := os.Stat(g.intentPath()); err != nil {
		t.Fatalf("contended exclusive wait did not publish intent: %v", err)
	}
	if g.ExclusiveQueued() {
		t.Error("intent still held after the exclusive acquirer gave up")
	}
	if _, err := os.Stat(g.intentInfoPath()); !os.IsNotExist(err) {
		t.Errorf("intent sidecar left behind by an orderly give-up (stat err %v)", err)
	}
	_ = sh.Release()
}

// Intent from a crashed exclusive waiter is released by the OS with the
// process; the leftover intent sidecar must not hold anyone back.
func TestStaleIntentFromCrashedProcess(t *testing.T) {
	dir := t.TempDir()
	g, err := ForWorkspace(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	hp := spawnHelperProc(t, "intent", dir)

	var holder string
	h0, err := g.Acquire(context.Background(), Shared, Options{
		Wait: 200 * time.Millisecond, PollInterval: 20 * time.Millisecond,
		OnWait: func(h string) { holder = h },
	})
	if err != nil {
		t.Fatalf("shared wait under another process's intent: %v", err)
	}
	_ = h0.Release()
	if !strings.Contains(holder, fmt.Sprintf("pid %d", hp.cmd.Process.Pid)) {
		t.Fatalf("wait notice %q does not name the intent holder pid %d", holder, hp.cmd.Process.Pid)
	}

	if err := hp.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	<-hp.drained
	_ = hp.cmd.Wait()
	if _, err := os.Stat(g.intentInfoPath()); err != nil {
		t.Fatalf("expected the crashed helper's intent sidecar to linger: %v", err)
	}

	start := time.Now()
	h, err := g.Acquire(context.Background(), Shared,
		Options{Wait: 10 * time.Second, PollInterval: 25 * time.Millisecond})
	if err != nil {
		t.Fatalf("shared acquisition after intent holder crashed: %v", err)
	}
	_ = h.Release()
	if waited := time.Since(start); waited > 3*time.Second {
		t.Errorf("shared acquisition took %s after the intent holder died", waited)
	}
	// A new exclusive waiter can take the intent over the stale sidecar.
	if f := g.tryTakeIntent("test successor"); f == nil {
		t.Error("intent lock not reacquirable after its holder crashed")
	} else {
		g.releaseIntent(f)
	}
}

func TestContextCancelWhileDeferringToIntent(t *testing.T) {
	g, _ := testGate(t)
	intent := g.tryTakeIntent("test queued maintenance")
	if intent == nil {
		t.Fatal("tryTakeIntent returned nil")
	}
	t.Cleanup(func() { g.releaseIntent(intent) })

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := g.Acquire(ctx, Shared, Options{Wait: 30 * time.Second})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("cancellation took %s to stop the wait", waited)
	}
}

func TestInheritedSharedHold(t *testing.T) {
	for _, tc := range []struct {
		name, val string
		want      bool
	}{
		{"unset", "", false},
		{"garbage", "not-a-pid", false},
		{"self", fmt.Sprint(os.Getpid()), false},
		{"dead", fmt.Sprint(deadPID(t)), false},
		{"live parent", fmt.Sprint(os.Getppid()), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(InheritedHoldEnv, tc.val)
			if got := InheritedSharedHold(); got != tc.want {
				t.Fatalf("InheritedSharedHold() with %s=%q = %v, want %v", InheritedHoldEnv, tc.val, got, tc.want)
			}
		})
	}
}

// setMaxIntentHold shortens maxIntentHold for one test.
func setMaxIntentHold(t *testing.T, d time.Duration) {
	t.Helper()
	old := maxIntentHold
	maxIntentHold = d
	t.Cleanup(func() { maxIntentHold = old })
}

// acquireAsync runs AcquireAll in a goroutine; the result arrives on the
// returned channel and any acquired handle is released at test end.
type acqResult struct {
	h   *MultiHandle
	err error
	at  time.Time
}

func acquireAsync(t *testing.T, mode Mode, opts Options, gates ...Gate) <-chan acqResult {
	t.Helper()
	ch := make(chan acqResult, 1)
	go func() {
		h, err := AcquireAll(context.Background(), mode, opts, gates...)
		ch <- acqResult{h: h, err: err, at: time.Now()}
	}()
	return ch
}

func waitQueued(t *testing.T, g Gate) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !g.ExclusiveQueued() {
		if time.Now().After(deadline) {
			t.Fatal("exclusive waiter never published intent")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func recv(t *testing.T, ch <-chan acqResult, what string) acqResult {
	t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("%s: %v", what, r.err)
		}
		t.Cleanup(func() { _ = r.h.Release() })
		return r
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: no result within 10s (deadlock?)", what)
	}
	return acqResult{}
}

// pending fails the test if the acquisition has already completed.
func pending(t *testing.T, ch <-chan acqResult, what string) {
	t.Helper()
	select {
	case r := <-ch:
		if r.h != nil {
			_ = r.h.Release()
		}
		t.Fatalf("%s finished early (err %v)", what, r.err)
	default:
	}
}

// The availability regression this design must not have: a long-lived
// shared holder (a --watch, an open editor, an embedder) plus an exclusive
// waiter that therefore cannot get in. NEW shared commands must still run —
// before the intent cap they failed for the exclusive waiter's whole budget.
func TestDoomedExclusiveWaiterDoesNotFailNewSharedCommands(t *testing.T) {
	t.Run("final attempt ignores queue", func(t *testing.T) {
		g, _ := testGate(t)
		long := mustAcquire(t, g, Shared, Options{})
		t.Cleanup(func() { _ = long.Release() })
		x := acquireAsync(t, Exclusive, Options{Wait: 3 * time.Second, PollInterval: 20 * time.Millisecond, Reason: "test doomed init"}, g)
		waitQueued(t, g)

		h, err := g.Acquire(context.Background(), Shared, Options{Wait: time.Second, PollInterval: 20 * time.Millisecond})
		if err != nil {
			t.Fatalf("new shared command failed behind a doomed exclusive waiter: %v", err)
		}
		_ = h.Release()
		if r := <-x; !errors.Is(r.err, ErrBusy) {
			t.Fatalf("doomed exclusive waiter: %v, want ErrBusy", r.err)
		}
	})
	t.Run("intent dropped after maxIntentHold", func(t *testing.T) {
		setMaxIntentHold(t, 300*time.Millisecond)
		g, _ := testGate(t)
		long := mustAcquire(t, g, Shared, Options{})
		t.Cleanup(func() { _ = long.Release() })
		x := acquireAsync(t, Exclusive, Options{Wait: 3 * time.Second, PollInterval: 20 * time.Millisecond}, g)
		waitQueued(t, g)
		time.Sleep(500 * time.Millisecond)
		if g.ExclusiveQueued() {
			t.Fatal("exclusive waiter still holds intent past maxIntentHold")
		}
		start := time.Now()
		h, err := g.Acquire(context.Background(), Shared, Options{Wait: time.Second, PollInterval: 20 * time.Millisecond})
		if err != nil {
			t.Fatalf("shared after the intent cap: %v", err)
		}
		_ = h.Release()
		if waited := time.Since(start); waited > 200*time.Millisecond {
			t.Errorf("shared acquirer still deferred %s after the intent was dropped", waited)
		}
		pending(t, x, "exclusive waiter") // still waiting, just no longer queueing others
		_ = long.Release()
		recv(t, x, "exclusive waiter after the long holder left")
	})
}

// Lock order with intent across a gate set (a < b): a shared AcquireAll that
// holds a and defers on b's intent, while an exclusive waiter on b alone
// drains b. Nobody deadlocks, and the exclusive waiter goes first.
func TestIntentLockOrderSharedHoldsEarlierGate(t *testing.T) {
	dir := t.TempDir()
	a, _ := ForWorkspace(filepath.Join(dir, "a"))
	b, _ := ForPhysicalRoot(filepath.Join(dir, "b"))
	if gateKey(a.Path()) >= gateKey(b.Path()) {
		t.Fatalf("test assumes %s sorts before %s", a.Path(), b.Path())
	}
	hb := mustAcquire(t, b, Shared, Options{})
	x := acquireAsync(t, Exclusive, Options{Wait: 5 * time.Second, PollInterval: 10 * time.Millisecond}, b)
	waitQueued(t, b)
	s := acquireAsync(t, Shared, Options{Wait: 5 * time.Second, PollInterval: 10 * time.Millisecond}, a, b)
	time.Sleep(150 * time.Millisecond)
	if held, _, _ := a.ExclusiveHolder(); held {
		t.Fatal("unexpected exclusive holder on a")
	}
	pending(t, s, "shared {a,b}")
	pending(t, x, "exclusive {b}")

	_ = hb.Release()
	xr := recv(t, x, "exclusive waiter on b")
	time.Sleep(100 * time.Millisecond)
	pending(t, s, "shared {a,b} while the exclusive holder has b")
	xRelease := time.Now()
	_ = xr.h.Release()
	sr := recv(t, s, "shared {a,b} behind the queued exclusive")
	if sr.at.Before(xRelease) {
		t.Error("shared acquirer got b while the exclusive holder had it")
	}
}

// ...and the other direction: an exclusive AcquireAll holding a and the
// intent on b, a shared waiter on b alone, and a shared waiter on {a,b}
// blocked on a. All three complete once b's shared holder leaves.
func TestIntentLockOrderExclusiveHoldsEarlierGate(t *testing.T) {
	dir := t.TempDir()
	a, _ := ForWorkspace(filepath.Join(dir, "a"))
	b, _ := ForPhysicalRoot(filepath.Join(dir, "b"))
	hb := mustAcquire(t, b, Shared, Options{})
	x := acquireAsync(t, Exclusive, Options{Wait: 5 * time.Second, PollInterval: 10 * time.Millisecond}, a, b)
	waitQueued(t, b)
	if held, _, _ := a.ExclusiveHolder(); !held {
		t.Fatal("exclusive waiter should hold a while queued on b")
	}
	s1 := acquireAsync(t, Shared, Options{Wait: 5 * time.Second, PollInterval: 10 * time.Millisecond}, b)
	s2 := acquireAsync(t, Shared, Options{Wait: 5 * time.Second, PollInterval: 10 * time.Millisecond}, a, b)
	time.Sleep(150 * time.Millisecond)
	pending(t, s1, "shared {b}")
	pending(t, s2, "shared {a,b}")

	_ = hb.Release()
	xr := recv(t, x, "exclusive {a,b}")
	_ = xr.h.Release()
	recv(t, s1, "shared {b}")
	recv(t, s2, "shared {a,b}")
}

// An exhausted AcquireAll budget leaves later gates exactly one attempt,
// and queued intent on such a gate cannot fail the set (the gate is free).
func TestAcquireAllExhaustedBudgetFinalAttemptIgnoresIntent(t *testing.T) {
	dir := t.TempDir()
	a, _ := ForWorkspace(filepath.Join(dir, "a"))
	b, _ := ForPhysicalRoot(filepath.Join(dir, "b"))
	intent := b.tryTakeIntent("test queued on b")
	if intent == nil {
		t.Fatal("tryTakeIntent on b failed")
	}
	t.Cleanup(func() { b.releaseIntent(intent) })

	// a held for the whole call (released only after it returns): the set
	// fails on a, the gate that is really held, never reaching b.
	ha := mustAcquire(t, a, Exclusive, Options{})
	_, err := AcquireAll(context.Background(), Shared,
		Options{Wait: 100 * time.Millisecond, PollInterval: 10 * time.Millisecond}, a, b)
	_ = ha.Release()
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), a.Path()) {
		t.Fatalf("a held past the budget: %v, want ErrBusy on %s", err, a.Path())
	}

	// Both gates free, intent queued on b: b defers for the rest of the
	// budget, then its final attempt gets in.
	start := time.Now()
	m, err := AcquireAll(context.Background(), Shared,
		Options{Wait: 200 * time.Millisecond, PollInterval: 10 * time.Millisecond}, a, b)
	if err != nil {
		t.Fatalf("shared set with free gates and queued intent on b: %v", err)
	}
	_ = m.Release()
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("b's queued intent was not honored before the final attempt (took %s)", waited)
	}
}

// The race behind a farm flake of the test above: the budget can run out
// DURING the intent probe (a stalled process), after the iteration already
// decided the attempt was not final. The acquisition must still make its
// final, queue-ignoring attempt instead of failing on the queue alone. The
// hook stalls the acquirer past its deadline at exactly that point, so this
// is deterministic rather than load-dependent.
func TestSharedFinalAttemptSurvivesStallDuringIntentProbe(t *testing.T) {
	g, _ := testGate(t)
	intent := g.tryTakeIntent("test queued maintenance")
	if intent == nil {
		t.Fatal("tryTakeIntent failed")
	}
	t.Cleanup(func() { g.releaseIntent(intent) })

	// Generous budget: the first probe must happen well inside it even on a
	// starved runner, or there is no deferral to stall (asserted below).
	const wait = time.Second
	var stalls int
	testHookAfterIntentDefer = func() {
		stalls++
		time.Sleep(wait + 50*time.Millisecond) // budget gone before the deadline check
	}
	t.Cleanup(func() { testHookAfterIntentDefer = nil })

	h, err := g.Acquire(context.Background(), Shared, Options{Wait: wait, PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("queued intent failed a shared acquisition whose budget ran out mid-probe: %v", err)
	}
	_ = h.Release()
	if stalls != 1 {
		t.Fatalf("intent deferrals = %d, want exactly 1 (then the final attempt)", stalls)
	}

	// Same through AcquireAll, where later gates get the leftover budget.
	stalls = 0
	other, _ := ForPhysicalRoot(filepath.Join(filepath.Dir(g.Path()), "zz"))
	m, err := AcquireAll(context.Background(), Shared, Options{Wait: wait, PollInterval: 10 * time.Millisecond}, g, other)
	if err != nil {
		t.Fatalf("AcquireAll: queued intent failed the set after a mid-probe stall: %v", err)
	}
	_ = m.Release()
	if stalls != 1 {
		t.Fatalf("AcquireAll intent deferrals = %d, want exactly 1 (then the final attempt)", stalls)
	}
}

// Two exclusive waiters behind one shared holder: no deadlock, both get the
// gate in turn, and the intent is handed off and finally released.
func TestTwoExclusiveWaiters(t *testing.T) {
	g, _ := testGate(t)
	sh := mustAcquire(t, g, Shared, Options{})
	opts := Options{Wait: 5 * time.Second, PollInterval: 10 * time.Millisecond}
	x1 := acquireAsync(t, Exclusive, opts, g)
	x2 := acquireAsync(t, Exclusive, opts, g)
	waitQueued(t, g)
	time.AfterFunc(200*time.Millisecond, func() { _ = sh.Release() })

	var second <-chan acqResult
	var r acqResult
	select {
	case r = <-x1:
		second = x2
	case r = <-x2:
		second = x1
	case <-time.After(5 * time.Second):
		t.Fatal("neither exclusive waiter got the gate")
	}
	if r.err != nil {
		t.Fatalf("first exclusive waiter: %v", r.err)
	}
	time.Sleep(100 * time.Millisecond)
	_ = r.h.Release()
	r = recv(t, second, "second exclusive waiter")
	_ = r.h.Release()
	if g.ExclusiveQueued() {
		t.Error("intent still held after both exclusive waiters finished")
	}
}

// The nested-bd escape hatch works through a real exec: a child whose
// environment names a live ancestor skips the queue; without it, it defers.
func TestInheritedHoldThroughSubprocess(t *testing.T) {
	dir := t.TempDir()
	g, err := ForWorkspace(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	intent := g.tryTakeIntent("test queued maintenance")
	if intent == nil {
		t.Fatal("tryTakeIntent failed")
	}
	t.Cleanup(func() { g.releaseIntent(intent) })

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) string {
		t.Helper()
		cmd := exec.Command(exe, dir)
		env := []string{"WORKSPACEGATE_HELPER=inherited-acquire"}
		for _, kv := range bazeltest.ShardFreeEnv(os.Environ()) {
			if !strings.HasPrefix(kv, InheritedHoldEnv+"=") {
				env = append(env, kv)
			}
		}
		cmd.Env = append(env, extra...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("helper: %v (%s)", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	parse := func(out string) (bool, time.Duration) {
		var inherited bool
		var ms int64
		if _, err := fmt.Sscanf(out, "inherited=%t waited_ms=%d", &inherited, &ms); err != nil {
			t.Fatalf("helper output %q: %v", out, err)
		}
		return inherited, time.Duration(ms) * time.Millisecond
	}

	inh, waited := parse(run(fmt.Sprintf("%s=%d", InheritedHoldEnv, os.Getpid())))
	if !inh || waited > time.Second {
		t.Errorf("child of a live shared holder: inherited=%v waited=%s, want true and no queueing", inh, waited)
	}
	inh, waited = parse(run())
	if inh || waited < 2*time.Second {
		t.Errorf("unrelated child: inherited=%v waited=%s, want false and a full deferral", inh, waited)
	}
}
