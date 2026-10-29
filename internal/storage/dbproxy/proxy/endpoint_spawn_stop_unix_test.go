//go:build unix

package proxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
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

func TestShutdownWaitsForStartDelayedBeforeExec(t *testing.T) {
	runShutdownDuringHeldStart(t, 150*time.Millisecond)
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

func TestCanceledDiscoveryDoesNotExecAfterStartWindow(t *testing.T) {
	root := t.TempDir()
	entered, release := holdStartBeforeExec(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, "exit-child.sh"), []byte("#!/bin/sh\ntouch \"$3/started\"\nexit 0\n"), 0o700))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := GetCreateDatabaseProxyServerEndpointContext(ctx, root, externalOpenOpts(root))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(spawnStopFailAfter):
		close(release)
		t.Fatal("start did not reach pre-exec window")
	}
	cancel()
	close(release)
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(spawnStopFailAfter):
		t.Fatal("canceled start did not finish")
	}
	assert.NoFileExists(t, filepath.Join(root, "started"))
	assert.NoFileExists(t, filepath.Join(root, spawnMarkerFileName))
}

// Force a peer to authenticate and adopt this opener's actual child before
// canceling the original wait. Killing "our child" would kill the peer's proxy.
func TestCanceledDiscoveryPreservesChildAdoptedByPeer(t *testing.T) {
	root := t.TempDir()
	stub := filepath.Join(root, "shared-child.sh")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700))
	previousResolve, previousHook := ResolveExecutable, afterProxyChildStart
	ResolveExecutable = func() (string, error) { return stub, nil }
	t.Cleanup(func() { ResolveExecutable, afterProxyChildStart = previousResolve, previousHook })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var childPID int
	var birth procid.Token
	adopted := false
	afterProxyChildStart = func(child *spawnedProxyChild) {
		childPID = child.cmd.Process.Pid
		var err error
		birth, err = procid.Capture(childPID)
		require.NoError(t, err)
		t.Cleanup(func() {
			handle, err := procid.Open(childPID, birth)
			if err == nil {
				_ = handle.Kill()
				_ = handle.Close()
			}
			select {
			case <-child.done:
			case <-time.After(5 * time.Second):
				t.Error("fixture child did not exit")
			}
		})
		rootID, err := identity.RootID(root)
		require.NoError(t, err)
		_, err = identity.WriteSecret(root)
		require.NoError(t, err)
		// No MySQL data connection is needed for authenticated adoption.
		var reply identity.IdentReply
		var mu sync.RWMutex
		control, err := startControl(root, func() identity.IdentReply {
			mu.RLock()
			defer mu.RUnlock()
			return reply
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = control.Close() })
		pf := pidfile.PidFile{Schema: pidfile.SchemaV2, Kind: pidfile.KindProxy,
			Pid: childPID, Birth: string(birth), RootID: rootID, Port: 3307, ControlPort: control.Port()}
		mu.Lock()
		reply = identity.IdentReply{Schema: pf.Schema, Role: pf.Kind, RootID: pf.RootID,
			PID: pf.Pid, Birth: pf.Birth, DataPort: pf.Port, ControlPort: pf.ControlPort}
		mu.Unlock()
		require.NoError(t, pidfile.Write(root, PIDFileName, pf))
		peer, err := GetCreateDatabaseProxyServerEndpointContext(context.Background(), root, externalOpenOpts(root))
		require.NoError(t, err)
		require.Equal(t, pf.Port, peer.Port)
		adopted = true
		cancel()
	}
	_, err := GetCreateDatabaseProxyServerEndpointContext(ctx, root, externalOpenOpts(root))
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, adopted, "peer must adopt before cancellation")
	alive, err := procid.Verify(childPID, birth)
	require.NoError(t, err)
	assert.True(t, alive, "canceling the original opener must preserve a shared child")
	assert.Equal(t, adoptionAdopted, readAndDial(root).status)
}

// These lifecycle tests use actual child processes because a returned timeout
// alone cannot prove that startup cleanup ran or a peer's process survived.
func installStartupChild(t *testing.T, root, script string, started func(*spawnedProxyChild)) *helperProcess {
	t.Helper()
	stub := filepath.Join(root, "startup-child.sh")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\n"+script+"\n"), 0o700))
	previousResolve, previousHook := ResolveExecutable, afterProxyChildStart
	ResolveExecutable = func() (string, error) { return stub, nil }
	child := &helperProcess{}
	afterProxyChildStart = func(spawned *spawnedProxyChild) {
		child.cmd, child.done = spawned.cmd, spawned.done
		var err error
		child.token, err = procid.Capture(child.cmd.Process.Pid)
		// A deliberately failing child may already be gone. Its cmd/done
		// still belong to this unreaped fixture and must be retained.
		if err != nil && !procid.IsProcessGone(err) {
			require.NoError(t, err)
		}
		if started != nil {
			started(spawned)
		}
	}
	t.Cleanup(func() {
		ResolveExecutable, afterProxyChildStart = previousResolve, previousHook
		if child.cmd == nil {
			return
		}
		select {
		case <-child.done:
			return
		default:
		}
		// This fixture owns the unreaped child. Do not use a handle closed
		// by the resolver's defer, or signal a pid obtained from a pidfile.
		_ = child.cmd.Process.Kill()
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			t.Error("fixture child did not exit")
		}
	})
	return child
}

func publishStartupChild(t *testing.T, root string, child *spawnedProxyChild) Endpoint {
	t.Helper()
	birth, err := procid.Capture(child.cmd.Process.Pid)
	require.NoError(t, err)
	rootID, err := identity.RootID(root)
	require.NoError(t, err)
	_, err = identity.WriteSecret(root)
	require.NoError(t, err)
	var reply identity.IdentReply
	var mu sync.RWMutex
	control, err := startControl(root, func() identity.IdentReply {
		mu.RLock()
		defer mu.RUnlock()
		return reply
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = control.Close() })
	pf := pidfile.PidFile{Schema: pidfile.SchemaV2, Kind: pidfile.KindProxy,
		Pid: child.cmd.Process.Pid, Birth: string(birth), RootID: rootID,
		Port: 3307, ControlPort: control.Port()}
	mu.Lock()
	reply = identity.IdentReply{Schema: pf.Schema, Role: pf.Kind, RootID: pf.RootID,
		PID: pf.Pid, Birth: pf.Birth, DataPort: pf.Port, ControlPort: pf.ControlPort}
	mu.Unlock()
	require.NoError(t, pidfile.Write(root, PIDFileName, pf))
	return Endpoint{Host: "127.0.0.1", Port: pf.Port}
}

func TestProxyStartupTimeoutKillsOwnedChild(t *testing.T) {
	for _, entry := range []struct {
		name string
		open func(string, OpenOpts) (Endpoint, error)
	}{
		{"legacy", GetCreateDatabaseProxyServerEndpoint},
		{"context", func(root string, opts OpenOpts) (Endpoint, error) {
			return GetCreateDatabaseProxyServerEndpointContext(context.Background(), root, opts)
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			root := t.TempDir()
			child := installStartupChild(t, root, "exec sleep 120", nil)
			_, err := entry.open(root, externalOpenOpts(root))
			require.Error(t, err)
			require.NotNil(t, child.cmd, "the actual child must start before the startup bound expires")
			assert.Contains(t, err.Error(), "timeout waiting for proxy to become ready on port 3307")
			assert.NotErrorIs(t, err, context.DeadlineExceeded, "prime must not classify our own timeout as its store timeout")
			assertHelperExited(t, *child)
			assert.NoFileExists(t, filepath.Join(root, spawnMarkerFileName))
		})
	}
}

func TestProxyOpenTimeoutPreservesLastSpawnError(t *testing.T) {
	root := t.TempDir()
	installStartupChild(t, root, "exit 7", func(_ *spawnedProxyChild) {
		// Keep the outer opener waiting after its child's exit, so the
		// operation bound must return the informative lastSpawnErr.
		lock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)
		t.Cleanup(lock.Unlock)
	})
	_, err := getCreateDatabaseProxyServerEndpointContext(context.Background(), root, externalOpenOpts(root), time.Now().Add(5*time.Second))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy child exited before becoming ready on explicitly configured port 3307")
	assert.Contains(t, err.Error(), externalOpenOpts(root).LogFilePath)
	assert.Contains(t, err.Error(), "exit status 7")
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
}

func TestProxyCallerDeadlinePreservesChildAdoptedByPeer(t *testing.T) {
	root := t.TempDir()
	caller, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	adopted := false
	child := installStartupChild(t, root, "exec sleep 120", func(spawned *spawnedProxyChild) {
		want := publishStartupChild(t, root, spawned)
		peer, err := GetCreateDatabaseProxyServerEndpoint(root, externalOpenOpts(root))
		require.NoError(t, err)
		require.Equal(t, want, peer)
		adopted = true
		<-caller.Done()
	})
	_, err := GetCreateDatabaseProxyServerEndpointContext(caller, root, externalOpenOpts(root))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, adopted, "peer must authenticate before the caller deadline")
	assertHelperAlive(t, *child)
	assert.Equal(t, adoptionAdopted, readAndDial(root).status)
}

func TestProxyReadyReplyAtStartupDeadline(t *testing.T) {
	for _, callerCancels := range []bool{false, true} {
		name := "internal deadline accepts completed authentication"
		if callerCancels {
			name = "caller cancellation wins over completed authentication"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			caller, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			wait, cancelWait := context.WithTimeout(caller, 5*time.Second)
			defer cancelWait()
			previous := afterProxyIdentityRead
			t.Cleanup(func() { afterProxyIdentityRead = previous })
			var want Endpoint
			child := installStartupChild(t, root, "exec sleep 120", func(spawned *spawnedProxyChild) {
				want = publishStartupChild(t, root, spawned)
			})
			authenticated := false
			afterProxyIdentityRead = func() {
				authenticated = true
				if callerCancels {
					cancelCaller()
				}
				// Park only after the authenticated reply was validated, so
				// the read-complete and deadline cases are both ready.
				<-wait.Done()
			}
			lock, err := util.TryLock(filepath.Join(root, LockFileName))
			require.NoError(t, err)
			ep, err := spawnAndHandoffWait(caller, wait, root, externalOpenOpts(root), "", lock, adoptionResult{status: adoptionNoRecord})
			require.True(t, authenticated)
			if callerCancels {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
				assert.Equal(t, want, ep)
			}
			assertHelperAlive(t, *child)
		})
	}
}

func TestProxyStartupTimeoutHonorsStopEpoch(t *testing.T) {
	root := t.TempDir()
	child := installStartupChild(t, root, "exec sleep 120", func(_ *spawnedProxyChild) {
		require.NoError(t, advanceStopEpoch(root))
	})
	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	_, err = spawnAndHandoffContext(context.Background(), root, externalOpenOpts(root), time.Now().Add(5*time.Second), "", lock, adoptionResult{status: adoptionNoRecord})
	require.ErrorIs(t, err, errStartInterrupted)
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
	assertHelperAlive(t, *child)
}
