//go:build !windows

package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPackageExecutable makes the package default fail loudly, so a test can
// prove the spawn path never fell back to the running executable.
func stubPackageExecutable(t *testing.T) *bool {
	t.Helper()
	called := false
	previous := ResolveExecutable
	ResolveExecutable = func() (string, error) {
		called = true
		return "", errors.New("package default executable reached")
	}
	t.Cleanup(func() { ResolveExecutable = previous })
	return &called
}

// TestForkExecChildUsesExecutablePathOverride pins that an
// OpenOpts.ResolveExecutable is what gets exec'd as the proxy child, with the
// db-proxy-child argv, and that the package default is not consulted.
func TestForkExecChildUsesExecutablePathOverride(t *testing.T) {
	root := t.TempDir()
	defaultCalled := stubPackageExecutable(t)

	argvFile := filepath.Join(t.TempDir(), "argv")
	script := filepath.Join(t.TempDir(), "fake-bd")
	require.NoError(t, os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\necho \"$@\" > %q\nexit 1\n", argvFile)), 0o700))

	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	opts := externalOpenOpts(root)
	opts.ResolveExecutable = func() (string, error) { return script, nil }

	_, err = spawnAndHandoff(root, opts, time.Now().Add(10*time.Second), "", lock, readAndDial(root))
	require.Error(t, err)
	require.ErrorContains(t, err, "exited before becoming ready")
	assert.False(t, *defaultCalled, "package default ResolveExecutable must not be consulted when OpenOpts.ResolveExecutable is set")

	argv, readErr := os.ReadFile(argvFile)
	require.NoError(t, readErr, "the override executable was not exec'd")
	assert.True(t, strings.HasPrefix(string(argv), "db-proxy-child --root "+root), "argv = %q", argv)
}

// TestGetCreateFailsFastWithoutProxyExecutable pins the library-host contract:
// with no proxy running and a resolver that reports ErrNoProxyExecutable, the
// open fails with that sentinel at once instead of polling out openDeadline,
// and never falls back to the running executable.
func TestGetCreateFailsFastWithoutProxyExecutable(t *testing.T) {
	root := t.TempDir()
	defaultCalled := stubPackageExecutable(t)

	opts := externalOpenOpts(root)
	opts.ResolveExecutable = func() (string, error) {
		return "", fmt.Errorf("bd not on PATH: %w", ErrNoProxyExecutable)
	}

	start := time.Now()
	_, err := GetCreateDatabaseProxyServerEndpoint(root, opts)
	require.ErrorIs(t, err, ErrNoProxyExecutable)
	assert.Less(t, time.Since(start), openDeadline/2, "a missing proxy executable must be terminal, not retried until the open deadline")
	assert.False(t, *defaultCalled)
	assert.NoFileExists(t, filepath.Join(root, PIDFileName))
}

// TestNilExecutableOverrideKeepsPackageDefault pins the CLI path: an unset
// OpenOpts.ResolveExecutable still resolves through the package variable.
func TestNilExecutableOverrideKeepsPackageDefault(t *testing.T) {
	root := t.TempDir()
	defaultCalled := stubPackageExecutable(t)
	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)

	_, err = spawnAndHandoff(root, externalOpenOpts(root), time.Now().Add(time.Second), "", lock, readAndDial(root))
	require.ErrorContains(t, err, "package default executable reached")
	assert.True(t, *defaultCalled)
}

// TestUnresolvableExecutableLeavesRecordsAlone pins that the executable is
// resolved before the spawn path mutates anything: a library host with no bd
// to spawn must not quarantine the root's proxy record (or reap its backend)
// on the way to failing.
func TestUnresolvableExecutableLeavesRecordsAlone(t *testing.T) {
	root := t.TempDir()
	stubPackageExecutable(t)
	record := pidfile.Path(root, PIDFileName)
	require.NoError(t, os.WriteFile(record, []byte(`{"pid":`), 0o600))
	discovery := readAndDial(root)
	require.Equal(t, adoptionMalformed, discovery.status)
	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)

	opts := externalOpenOpts(root)
	opts.ResolveExecutable = func() (string, error) {
		return "", fmt.Errorf("bd not on PATH: %w", ErrNoProxyExecutable)
	}
	_, err = spawnAndHandoff(root, opts, time.Now().Add(time.Second), "", lock, discovery)
	require.ErrorIs(t, err, ErrNoProxyExecutable)
	assert.FileExists(t, record, "the record must not be quarantined when no proxy can be spawned")
	assert.Empty(t, quarantines(t, root, PIDFileName))
}

// startPreUpgradeProxyFixture models a live proxy started by a bd older than
// 1.4: a schema-v2 proxy.pid with no ControlProto naming a live process, a
// control listener that closes the request-MAC IDENT unanswered (a 1.3.x
// proxy compares the raw secret), and proxy.lock held, as such a proxy holds
// it for its whole life. The caller releases the returned lock.
func startPreUpgradeProxyFixture(t *testing.T, root string) (helperProcess, *util.Lock) {
	t.Helper()
	helper := startHelperProcess(t)
	_, err := identity.WriteSecret(root)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(conn).ReadString('\n')
			_ = conn.Close()
		}
	}()
	pf := v2Record(t, root, pidfile.KindProxy, helper)
	pf.ControlPort = ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, pidfile.Write(root, PIDFileName, pf))
	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	require.True(t, isPreMACIdentRefusal(readAndDial(root)), "the fixture must read as a pre-upgrade proxy refusing the IDENT")
	return helper, lock
}

// TestLibraryHostLeavesPreUpgradeProxyRunning pins that a host that is not bd
// (OpenOpts.ResolveExecutable set) never stops a live proxy a bd 1.3.x
// started. The open fails at once with ErrPreUpgradeProxyRunning and leaves
// the process and its record alone, without consulting the resolver. Before
// the fix it killed the proxy every bd 1.3.x process on the workspace
// shares, and then could start no replacement.
func TestLibraryHostLeavesPreUpgradeProxyRunning(t *testing.T) {
	root := t.TempDir()
	defaultCalled := stubPackageExecutable(t)
	helper, lock := startPreUpgradeProxyFixture(t, root)
	defer lock.Unlock()

	resolverCalled := false
	opts := externalOpenOpts(root)
	opts.ResolveExecutable = func() (string, error) {
		resolverCalled = true
		return "", fmt.Errorf("bd not on PATH: %w", ErrNoProxyExecutable)
	}
	start := time.Now()
	_, err := GetCreateDatabaseProxyServerEndpoint(root, opts)
	require.ErrorIs(t, err, ErrPreUpgradeProxyRunning)
	assert.Contains(t, err.Error(), "pid "+strconv.Itoa(helper.cmd.Process.Pid))
	assert.Less(t, time.Since(start), openDeadline/2, "the refusal must be terminal, not polled until the open deadline")
	assertHelperAlive(t, helper)
	assert.FileExists(t, pidfile.Path(root, PIDFileName))
	assert.Empty(t, quarantines(t, root, PIDFileName))
	assert.False(t, resolverCalled, "a refused open must not reach the spawn path")
	assert.False(t, *defaultCalled)
}

// TestCLIStopsPreUpgradeProxy is the same fixture on the CLI's path, and the
// control for the test above: with OpenOpts.ResolveExecutable nil the open
// still stops the pre-upgrade proxy (the upgrade to 1.4), then reaches the
// spawn path once proxy.lock is free.
func TestCLIStopsPreUpgradeProxy(t *testing.T) {
	root := t.TempDir()
	previous := ResolveExecutable
	ResolveExecutable = func() (string, error) { return "", fmt.Errorf("spawn reached: %w", ErrNoProxyExecutable) }
	t.Cleanup(func() { ResolveExecutable = previous })
	helper, lock := startPreUpgradeProxyFixture(t, root)
	// A real proxy's lock is released when it dies; the fixture's is
	// released when the helper exits, or once the open returns.
	var once sync.Once
	release := func() { once.Do(lock.Unlock) }
	go func() {
		<-helper.done
		release()
	}()

	_, err := GetCreateDatabaseProxyServerEndpoint(root, externalOpenOpts(root))
	release()
	require.ErrorContains(t, err, "spawn reached")
	assertHelperExited(t, helper)
}
