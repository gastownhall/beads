//go:build unix

package proxy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spawnStopFailAfter only detects a hung start or Shutdown. It is far above
// shutdownSpawnWaitDeadline's components on purpose: a loaded -race runner
// can take seconds to fork/exec the child, and that must not fail the test.
const spawnStopFailAfter = 60 * time.Second

// holdStartBeforeExec makes the next proxy start block in its
// release-lock-before-exec window. entered closes once the start is parked
// there with its spawn marker published; closing release lets it exec.
func holdStartBeforeExec(t *testing.T, root string) (entered <-chan struct{}, release chan<- struct{}) {
	t.Helper()
	childPath := filepath.Join(root, "exit-child.sh")
	require.NoError(t, os.WriteFile(childPath, []byte("#!/bin/sh\nexit 0\n"), 0o700))

	previousResolve := ResolveExecutable
	previousHook := beforeProxyChildStart
	ResolveExecutable = func() (string, error) { return childPath, nil }
	enteredCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	beforeProxyChildStart = func() {
		once.Do(func() {
			close(enteredCh)
			<-releaseCh
		})
	}
	t.Cleanup(func() {
		ResolveExecutable = previousResolve
		beforeProxyChildStart = previousHook
	})
	return enteredCh, releaseCh
}

// runShutdownDuringHeldStart parks a start before exec, runs Shutdown
// concurrently, holds the window for hold, then lets the start proceed and
// asserts the start aborts and Shutdown succeeds without leaving a marker.
func runShutdownDuringHeldStart(t *testing.T, hold time.Duration) {
	t.Helper()
	root := t.TempDir()
	entered, release := holdStartBeforeExec(t, root)

	startDone := make(chan error, 1)
	go func() {
		_, err := GetCreateDatabaseProxyServerEndpoint(root, externalOpenOpts(root))
		startDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(spawnStopFailAfter):
		t.Fatal("start did not reach injected pre-exec delay")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- Shutdown(root) }()
	select {
	case err := <-stopDone:
		t.Fatalf("Shutdown returned during delayed start: %v", err)
	case <-time.After(hold):
		// The injected hook, rather than scheduler timing, holds the exact
		// historical release-lock-before-Start window open.
	}

	close(release)
	select {
	case err := <-startDone:
		require.Error(t, err)
		assert.True(t, errors.Is(err, errStartInterrupted), "start error = %v", err)
	case <-time.After(spawnStopFailAfter):
		t.Fatal("start did not terminate after concurrent shutdown")
	}
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(spawnStopFailAfter):
		t.Fatal("Shutdown did not finish after delayed start resolved")
	}

	assert.NoFileExists(t, filepath.Join(root, spawnMarkerFileName))
}

// parkStart starts a proxy and parks it before exec with its spawn marker
// published. The returned resolve lets the start exec, abort on Shutdown's
// advanced stop epoch and clear its own marker, and returns the start's
// error once it has; later calls return the same error.
func parkStart(t *testing.T, root string) (resolve func() error) {
	t.Helper()
	entered, release := holdStartBeforeExec(t, root)
	startDone := make(chan error, 1)
	go func() {
		_, err := GetCreateDatabaseProxyServerEndpoint(root, externalOpenOpts(root))
		startDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(spawnStopFailAfter):
		t.Fatal("start did not reach injected pre-exec delay")
	}

	var once sync.Once
	var startErr error
	return func() error {
		once.Do(func() {
			close(release)
			select {
			case startErr = <-startDone:
			case <-time.After(spawnStopFailAfter):
				startErr = errors.New("start did not terminate after concurrent shutdown")
			}
		})
		return startErr
	}
}

// hookMarkerVerify runs hook before each identity verification, passing the
// 1-based call number. In these tests no proxy record exists, so every call
// is a check of the spawn marker's owner. A non-nil error from hook fails
// that verification; otherwise the real one runs.
func hookMarkerVerify(t *testing.T, hook func(call int) error) {
	t.Helper()
	previous := verifyProcessIdentity
	var calls atomic.Int32
	verifyProcessIdentity = func(pid int, tok procid.Token) (bool, error) {
		if err := hook(int(calls.Add(1))); err != nil {
			return false, err
		}
		return previous(pid, tok)
	}
	t.Cleanup(func() { verifyProcessIdentity = previous })
}

// shutdownOrFail runs Shutdown, failing the test if it hangs.
func shutdownOrFail(t *testing.T, root string) error {
	t.Helper()
	stopDone := make(chan error, 1)
	go func() { stopDone <- Shutdown(root) }()
	select {
	case err := <-stopDone:
		return err
	case <-time.After(spawnStopFailAfter):
		t.Fatal("Shutdown hung")
		return nil
	}
}

// TestShutdownWaitsForStartDelayedBeforeExec parks a start before exec with
// its spawn marker published and has Shutdown check that live marker several
// times, then resolves the start (exec, abort on the advanced stop epoch,
// clear of its own marker) entirely inside one of those checks. That is the
// interleaving behind #7158: checks made under proxy.lock kept taking the lock
// the start needs to clear its marker, the clear's bounded retry gave up, and
// Shutdown waited out the marker it left behind. No check of a live marker
// may hold proxy.lock, and Shutdown must see the clear and succeed.
func TestShutdownWaitsForStartDelayedBeforeExec(t *testing.T) {
	root := t.TempDir()
	resolveStart := parkStart(t, root)

	var (
		lockErr         atomic.Pointer[error] // from the first check that could not take proxy.lock
		resolvedInCheck atomic.Bool
	)
	hookMarkerVerify(t, func(call int) error {
		if lock, err := util.TryLock(filepath.Join(root, LockFileName)); err != nil {
			lockErr.CompareAndSwap(nil, &err)
		} else {
			lock.Unlock()
		}
		if call == 4 {
			resolvedInCheck.Store(true)
			_ = resolveStart()
		}
		return nil
	})

	stopErr := shutdownOrFail(t, root)
	startErr := resolveStart() // no-op unless Shutdown returned before its 4th check

	if err := lockErr.Load(); err != nil {
		t.Fatalf("Shutdown checked the live start's spawn marker while proxy.lock was unavailable: %v", *err)
	}
	require.NoError(t, stopErr)
	require.True(t, resolvedInCheck.Load(), "Shutdown returned while the start was parked")
	require.ErrorIs(t, startErr, errStartInterrupted)
	assert.NoFileExists(t, filepath.Join(root, spawnMarkerFileName))
}

// A lock-free check that cannot verify the marker's owner reports no live
// start, so Shutdown takes proxy.lock. The re-check under the lock must still
// see the parked start and make Shutdown wait for it.
func TestShutdownLockedRecheckWaitsForLiveStart(t *testing.T) {
	root := t.TempDir()
	resolveStart := parkStart(t, root)

	var resolvedInCheck atomic.Bool
	hookMarkerVerify(t, func(call int) error {
		switch call {
		case 1: // the lock-free check
			return errors.New("transient identity verification failure")
		case 3: // the next lock-free check, reached only if the locked re-check waited
			resolvedInCheck.Store(true)
			_ = resolveStart()
		}
		return nil
	})

	stopErr := shutdownOrFail(t, root)
	startErr := resolveStart()

	require.NoError(t, stopErr)
	require.True(t, resolvedInCheck.Load(), "Shutdown returned while the start was parked")
	require.ErrorIs(t, startErr, errStartInterrupted)
	assert.NoFileExists(t, filepath.Join(root, spawnMarkerFileName))
}

// A start can publish its marker after Shutdown's lock-free check found none
// and before Shutdown takes proxy.lock. The re-check under the lock must see
// it, and Shutdown must wait until the marker is cleared.
func TestShutdownWaitsForMarkerPublishedBeforeItsLock(t *testing.T) {
	root := t.TempDir()
	markerPath := filepath.Join(root, spawnMarkerFileName)
	birth, err := procid.Capture(os.Getpid())
	require.NoError(t, err)

	var publishOnce sync.Once
	previousHook := afterSpawnMarkerLockFreeCheck
	afterSpawnMarkerLockFreeCheck = func() {
		publishOnce.Do(func() {
			// This test process is the marker's live owner.
			if err := writeSpawnMarker(root, spawnMarker{
				Schema:      1,
				PID:         os.Getpid(),
				Birth:       string(birth),
				StartedUnix: time.Now().Unix(),
			}); err != nil {
				t.Errorf("publish spawn marker: %v", err)
			}
		})
	}
	t.Cleanup(func() { afterSpawnMarkerLockFreeCheck = previousHook })

	var cleared atomic.Bool
	hookMarkerVerify(t, func(call int) error {
		// Call 1 is the locked re-check. Call 2 is the next lock-free check,
		// reached only if that re-check made Shutdown wait; the start clears
		// its marker there.
		if call == 2 {
			cleared.Store(true)
			if err := os.Remove(markerPath); err != nil {
				t.Errorf("clear spawn marker: %v", err)
			}
		}
		return nil
	})

	require.NoError(t, shutdownOrFail(t, root))
	require.True(t, cleared.Load(), "Shutdown returned while a live start's marker was present")
	assert.NoFileExists(t, markerPath)
}

// clearOwnSpawnMarker retries a delete that fails, as one does on Windows
// while a waiter's lock-free read holds the marker open, instead of leaving a
// live-owner marker behind.
func TestClearOwnSpawnMarkerRetriesFailedDelete(t *testing.T) {
	root := t.TempDir()
	own := spawnMarker{Schema: 1, PID: os.Getpid(), Birth: "this-start", StartedUnix: time.Now().Unix()}
	require.NoError(t, writeSpawnMarker(root, own))

	attempts := 0
	previousRemove := removeSpawnMarkerFile
	removeSpawnMarkerFile = func(name string) error {
		attempts++
		if attempts <= 3 {
			return &os.PathError{Op: "remove", Path: name, Err: errors.New("simulated sharing violation")}
		}
		return previousRemove(name)
	}
	t.Cleanup(func() { removeSpawnMarkerFile = previousRemove })

	require.NoError(t, clearOwnSpawnMarker(root, own))
	assert.Equal(t, 4, attempts)
	assert.NoFileExists(t, filepath.Join(root, spawnMarkerFileName))
}

// A marker whose owner is gone (a crashed starter, or a PID since reused) is
// not an in-progress start: Shutdown removes it under proxy.lock and succeeds.
func TestShutdownRemovesDeadOwnerSpawnMarker(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, writeSpawnMarker(root, spawnMarker{
		Schema:      1,
		PID:         os.Getpid(),
		Birth:       "birth-token-of-an-exited-process",
		StartedUnix: time.Now().Unix(),
	}))

	require.NoError(t, Shutdown(root))
	assert.NoFileExists(t, filepath.Join(root, spawnMarkerFileName))
}

// A live starter that is slow to exec (loaded host, -race) must be waited
// out, not reported as "proxy left running" once shutdownConfirmDeadline
// elapses: the start is still making progress and will abort on the advanced
// stop epoch.
func TestShutdownWaitsForLiveStarterPastConfirmDeadline(t *testing.T) {
	runShutdownDuringHeldStart(t, shutdownConfirmDeadline+time.Second)
}

// bd-pdwxc6 / #7527: forkExecChild rotates an oversized server.log and
// proxy.log while proxy.lock is held and before it opens the child's stdout
// fd, so the new child writes to a fresh primary. A file at or under the
// ceiling is left in place, and so is a server log bd does not own
// (RotateLogFile unset): an operator's log outside rootDir keeps growing in
// place and their own .1 survives.
func TestForkExecChild_RotatesOversizedLogs(t *testing.T) {
	t.Setenv(doltserver.EnvMaxLogBytes, "1024")
	const operatorGeneration = "operator generation\n"
	for _, tc := range []struct {
		name                        string
		customLog                   bool
		serverSize, proxySize       int
		serverRotated, proxyRotated bool
	}{
		{name: "both oversized", serverSize: 4096, proxySize: 4096, serverRotated: true, proxyRotated: true},
		{name: "only server oversized", serverSize: 4096, proxySize: 1024, serverRotated: true},
		{name: "only proxy oversized", serverSize: 1024, proxySize: 4096, proxyRotated: true},
		{name: "oversized custom server log", customLog: true, serverSize: 4096, proxySize: 4096, proxyRotated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			childPath := filepath.Join(root, "echo-child.sh")
			require.NoError(t, os.WriteFile(childPath, []byte("#!/bin/sh\necho child-stdout\nexit 0\n"), 0o700))
			previous := ResolveExecutable
			ResolveExecutable = func() (string, error) { return childPath, nil }
			t.Cleanup(func() { ResolveExecutable = previous })

			serverLog := filepath.Join(root, "server.log")
			if tc.customLog {
				serverLog = filepath.Join(t.TempDir(), "operator.log")
				require.NoError(t, os.WriteFile(serverLog+".1", []byte(operatorGeneration), 0o600))
			}
			proxyLog := filepath.Join(root, LogFileName)
			oldServer := strings.Repeat("S", tc.serverSize-len("old-server\n")) + "old-server\n"
			oldProxy := strings.Repeat("P", tc.proxySize-len("old-proxy\n")) + "old-proxy\n"
			require.NoError(t, os.WriteFile(serverLog, []byte(oldServer), 0o600))
			require.NoError(t, os.WriteFile(proxyLog, []byte(oldProxy), 0o600))

			lock, err := util.TryLock(filepath.Join(root, LockFileName))
			require.NoError(t, err)
			opts := externalOpenOpts(root)
			opts.LogFilePath = serverLog
			opts.RotateLogFile = !tc.customLog
			child, err := forkExecChild(root, opts, opts.Port, "", lock)
			require.NoError(t, err)
			select {
			case err := <-child.done:
				require.NoError(t, err)
			case <-time.After(spawnStopFailAfter):
				t.Fatal("stub proxy child did not exit")
			}

			serverBody, err := os.ReadFile(serverLog)
			require.NoError(t, err)
			if tc.serverRotated {
				rotated, err := os.ReadFile(serverLog + ".1")
				require.NoError(t, err)
				assert.Equal(t, oldServer, string(rotated))
				assert.Equal(t, "child-stdout\n", string(serverBody), "child stdout must go to a fresh server.log")
			} else {
				assert.Equal(t, oldServer+"child-stdout\n", string(serverBody))
				if tc.customLog {
					generation, err := os.ReadFile(serverLog + ".1")
					require.NoError(t, err)
					assert.Equal(t, operatorGeneration, string(generation), "the operator's own .1 must survive")
				} else {
					assert.NoFileExists(t, serverLog+".1")
				}
			}

			if tc.proxyRotated {
				rotated, err := os.ReadFile(proxyLog + ".1")
				require.NoError(t, err)
				assert.Equal(t, oldProxy, string(rotated))
				assert.NoFileExists(t, proxyLog)
			} else {
				assert.NoFileExists(t, proxyLog+".1")
				body, err := os.ReadFile(proxyLog)
				require.NoError(t, err)
				assert.Equal(t, oldProxy, string(body))
			}
		})
	}
}

// forkExecChild hands the child the server log to bound while it runs
// (--rotate-logpath) only for a log bd owns (RotateLogFile), the same
// ownership rule as the startup rotation.
func TestForkExecChild_PassesRotateLogPathOnlyForOwnedLog(t *testing.T) {
	for _, owned := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned", false: "operator log"}[owned], func(t *testing.T) {
			root := t.TempDir()
			childPath := filepath.Join(root, "args-child.sh")
			require.NoError(t, os.WriteFile(childPath, []byte("#!/bin/sh\necho \"$@\"\nexit 0\n"), 0o700))
			previous := ResolveExecutable
			ResolveExecutable = func() (string, error) { return childPath, nil }
			t.Cleanup(func() { ResolveExecutable = previous })

			lock, err := util.TryLock(filepath.Join(root, LockFileName))
			require.NoError(t, err)
			opts := externalOpenOpts(root)
			opts.LogFilePath = filepath.Join(root, "server.log")
			opts.RotateLogFile = owned
			child, err := forkExecChild(root, opts, opts.Port, "", lock)
			require.NoError(t, err)
			select {
			case err := <-child.done:
				require.NoError(t, err)
			case <-time.After(spawnStopFailAfter):
				t.Fatal("stub proxy child did not exit")
			}

			argv, err := os.ReadFile(opts.LogFilePath)
			require.NoError(t, err)
			if owned {
				assert.Contains(t, string(argv), "--rotate-logpath")
			} else {
				assert.NotContains(t, string(argv), "--rotate-logpath")
			}
		})
	}
}

// TestKillSpawnedChildLetsChildRunItsSIGTERMHandler pins bd-ig2lqc (#8489):
// on a spawn timeout the proxy child must get SIGTERM first, so its handler
// can stop the dolt backend it already started; a bare SIGKILL skips the
// handler and leaves an unrecorded dolt sql-server behind.
func TestKillSpawnedChildLetsChildRunItsSIGTERMHandler(t *testing.T) {
	for _, withHandle := range []bool{true, false} {
		name := "no handle"
		if withHandle {
			name = "verified handle"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(dir, "ready")
			cleaned := filepath.Join(dir, "cleaned-up")
			script := `trap 'touch "$2"; exit 0' TERM; touch "$1"; while :; do sleep 0.05; done`
			cmd := exec.Command("/bin/sh", "-c", script, "sh", ready, cleaned)
			require.NoError(t, cmd.Start())
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
			})

			require.Eventually(t, func() bool {
				_, err := os.Stat(ready)
				return err == nil
			}, spawnStopFailAfter, 10*time.Millisecond, "child never installed its SIGTERM trap")

			child := &spawnedProxyChild{cmd: cmd, done: done}
			if withHandle {
				tok, err := procid.Capture(cmd.Process.Pid)
				require.NoError(t, err)
				handle, err := procid.Open(cmd.Process.Pid, tok)
				require.NoError(t, err)
				t.Cleanup(func() { _ = handle.Close() })
				child.handle = handle
			}

			require.NoError(t, killSpawnedChild(child))
			_, err := os.Stat(cleaned)
			assert.NoError(t, err, "killSpawnedChild sent SIGKILL without SIGTERM first: the child's shutdown handler never ran (an already-started dolt backend would be orphaned)")
		})
	}
}

// scriptProxyChild makes the next spawn exec a shell script in place of
// `bd db-proxy-child`. The script touches ready once its body (and any trap
// it sets first) is installed, and writes its pid to pidPath.
func scriptProxyChild(t *testing.T, root, body string) (ready, pidPath string) {
	t.Helper()
	ready = filepath.Join(root, "child-ready")
	pidPath = filepath.Join(root, "child-pid")
	script := "#!/bin/sh\n" + body + "\necho $$ > '" + pidPath + "'\ntouch '" + ready + "'\nwhile :; do sleep 0.05; done\n"
	childPath := filepath.Join(root, "script-child.sh")
	require.NoError(t, os.WriteFile(childPath, []byte(script), 0o700))
	previous := ResolveExecutable
	ResolveExecutable = func() (string, error) { return childPath, nil }
	t.Cleanup(func() { ResolveExecutable = previous })
	return ready, pidPath
}

// spawnOpenWait is the open deadline spawnUntilTimeout gives a script child:
// long enough for a loaded runner to exec it and install its trap.
const spawnOpenWait = 3 * time.Second

// spawnUntilTimeout runs spawnAndHandoff, under proxy.lock, with an open
// deadline spawnOpenWait out, so a script child that never publishes a proxy
// record is timed out. It returns the error and how long the call took.
func spawnUntilTimeout(t *testing.T, root, ready string) (time.Duration, error) {
	t.Helper()
	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	start := time.Now()
	_, err = spawnAndHandoff(root, externalOpenOpts(root), start.Add(spawnOpenWait), "", lock, adoptionResult{status: adoptionNoRecord})
	elapsed := time.Since(start)
	require.FileExists(t, ready, "script child never became ready inside the open deadline; the run proves nothing")
	return elapsed, err
}

// AC-1 (#8489): a spawn that times out sends its child SIGTERM, and a child
// that exits inside spawnStopGrace is never killed; the caller gets today's
// timeout error.
func TestSpawnTimeoutStopsChildWithSIGTERMFirst(t *testing.T) {
	root := t.TempDir()
	termed := filepath.Join(root, "termed")
	ready, _ := scriptProxyChild(t, root, `trap 'touch "`+termed+`"; exit 0' TERM`)

	elapsed, err := spawnUntilTimeout(t, root, ready)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout waiting for proxy to become ready on port 3307")
	assert.NotContains(t, err.Error(), "safe child kill failed")
	assert.FileExists(t, termed, "the timed-out child never received SIGTERM: it was killed outright")
	// Well under the production spawnStopGrace (5s): no grace was waited out.
	assert.Less(t, elapsed, spawnOpenWait+4*time.Second, "a child that exits on SIGTERM must not wait out the grace")
}

// shortenSpawnStopGrace sets spawnStopGrace for one test.
func shortenSpawnStopGrace(t *testing.T, grace time.Duration) {
	t.Helper()
	previous := spawnStopGrace
	spawnStopGrace = grace
	t.Cleanup(func() { spawnStopGrace = previous })
}

// assertScriptChildGone reads the pid a script child recorded and asserts
// the process no longer exists (killSpawnedChild reaps it before returning).
func assertScriptChildGone(t *testing.T, pidPath string) {
	t.Helper()
	data, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	assert.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "timed-out child pid %d is still alive", pid)
}

// startTermTrapChild starts a shell child that records SIGTERM in termed but
// keeps running, and waits until its trap is installed.
func startTermTrapChild(t *testing.T, termed string) (*exec.Cmd, chan error) {
	t.Helper()
	ready := termed + ".ready"
	script := `trap 'touch "$2"' TERM; touch "$1"; while :; do sleep 0.05; done`
	cmd := exec.Command("/bin/sh", "-c", script, "sh", ready, termed)
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	require.Eventually(t, func() bool {
		_, err := os.Stat(ready)
		return err == nil
	}, spawnStopFailAfter, 10*time.Millisecond, "child never installed its SIGTERM trap")
	return cmd, done
}

// AC-2 (#8489): a timed-out child still alive spawnStopGrace after SIGTERM is
// killed, and the spawn returns inside open deadline + grace + confirm.
func TestSpawnTimeoutKillsChildThatIgnoresSIGTERMAfterGrace(t *testing.T) {
	grace := 500 * time.Millisecond
	shortenSpawnStopGrace(t, grace)
	root := t.TempDir()
	termed := filepath.Join(root, "termed")
	ready, pidPath := scriptProxyChild(t, root, `trap 'touch "`+termed+`"' TERM`)

	elapsed, err := spawnUntilTimeout(t, root, ready)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout waiting for proxy to become ready on port 3307")
	assert.NotContains(t, err.Error(), "safe child kill failed")
	assert.FileExists(t, termed, "the child was killed without SIGTERM first")
	assert.GreaterOrEqual(t, elapsed, spawnOpenWait+grace, "the child was killed before its SIGTERM grace ended")
	assert.Less(t, elapsed, spawnOpenWait+grace+shutdownConfirmDeadline)
	assertScriptChildGone(t, pidPath)
}

// AC-2: where a stop cannot be requested (Windows), the timed-out child is
// killed at once rather than after the grace.
func TestKillSpawnedChildKillsAtOnceWhenStopUnsupported(t *testing.T) {
	shortenSpawnStopGrace(t, spawnStopFailAfter)
	previous := requestSpawnedChildStopFn
	requestSpawnedChildStopFn = func(*exec.Cmd) error { return errGracefulStopUnsupported }
	t.Cleanup(func() { requestSpawnedChildStopFn = previous })

	termed := filepath.Join(t.TempDir(), "termed")
	cmd, done := startTermTrapChild(t, termed)
	start := time.Now()
	require.NoError(t, killSpawnedChild(&spawnedProxyChild{cmd: cmd, done: done}))
	assert.Less(t, time.Since(start), shutdownConfirmDeadline, "an unsupported stop request must not wait out the grace")
	assert.NoFileExists(t, termed)
	assert.ErrorIs(t, syscall.Kill(cmd.Process.Pid, 0), syscall.ESRCH)
}

// AC-2: a kill whose exit is never confirmed still returns today's timeout
// error, which spawnAndHandoff reports as "safe child kill failed".
func TestKillSpawnedChildUnconfirmedKillStillErrors(t *testing.T) {
	shortenSpawnStopGrace(t, 100*time.Millisecond)
	termed := filepath.Join(t.TempDir(), "termed")
	cmd, _ := startTermTrapChild(t, termed)
	never := make(chan error)

	err := killSpawnedChild(&spawnedProxyChild{cmd: cmd, done: never})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout (5s) waiting for pid "+strconv.Itoa(cmd.Process.Pid))
	assert.FileExists(t, termed, "the child was killed without SIGTERM first")
}

// AC-3 (#8489): Shutdown waits on a live starter's spawn marker for at least
// the starter's own worst case, which now includes the SIGTERM grace a
// timed-out child gets before it is killed; otherwise a stop racing that
// starter reports "proxy left running" while the start is within budget.
func TestShutdownSpawnWaitCoversSpawnStopGrace(t *testing.T) {
	assert.GreaterOrEqual(t, shutdownSpawnWaitDeadline(), openDeadline+spawnStopGrace+2*shutdownConfirmDeadline)

	shortenSpawnStopGrace(t, spawnStopGrace+time.Minute)
	assert.GreaterOrEqual(t, shutdownSpawnWaitDeadline(), openDeadline+spawnStopGrace+2*shutdownConfirmDeadline,
		"the budget must track spawnStopGrace, not a value frozen at init")
}
