//go:build unix

package proxy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
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
