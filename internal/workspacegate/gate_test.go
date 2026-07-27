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
	"testing"
	"time"
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
	cmd.Env = append(os.Environ(), "WORKSPACEGATE_HELPER=exit")
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
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, dir, mode)
	cmd.Env = append(os.Environ(), "WORKSPACEGATE_HELPER=hold")
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
	child.Env = append(os.Environ(), "WORKSPACEGATE_HELPER=sleep")
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
