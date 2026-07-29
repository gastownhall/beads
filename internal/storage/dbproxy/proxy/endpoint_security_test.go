//go:build !windows

package proxy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type helperProcess struct {
	cmd   *exec.Cmd
	token procid.Token
	done  <-chan error
}

func startHelperProcess(t *testing.T) helperProcess {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	token, err := procid.Capture(cmd.Process.Pid)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		matched, verifyErr := procid.Verify(cmd.Process.Pid, token)
		if verifyErr == nil && matched {
			handle, openErr := procid.Open(cmd.Process.Pid, token)
			if openErr == nil {
				_ = handle.Kill()
				_ = handle.Close()
			}
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("helper pid %d did not exit", cmd.Process.Pid)
		}
	})
	return helperProcess{cmd: cmd, token: token, done: done}
}

func assertHelperAlive(t *testing.T, helper helperProcess) {
	t.Helper()
	matched, err := procid.Verify(helper.cmd.Process.Pid, helper.token)
	require.NoError(t, err)
	assert.True(t, matched, "helper pid %d should still be alive", helper.cmd.Process.Pid)
}

func assertHelperExited(t *testing.T, helper helperProcess) {
	t.Helper()
	select {
	case <-helper.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("helper pid %d did not exit", helper.cmd.Process.Pid)
	}
	matched, err := procid.Verify(helper.cmd.Process.Pid, helper.token)
	require.NoError(t, err)
	assert.False(t, matched)
}

func v2Record(t *testing.T, root, kind string, helper helperProcess) pidfile.PidFile {
	t.Helper()
	rootID, err := identity.RootID(root)
	require.NoError(t, err)
	return pidfile.PidFile{
		Schema: pidfile.SchemaV2,
		Kind:   kind,
		Pid:    helper.cmd.Process.Pid,
		Birth:  string(helper.token),
		Port:   3307,
		RootID: rootID,
	}
}

func TestReadAndDialClassifiesRecordsWithoutMutation(t *testing.T) {
	t.Run("no record", func(t *testing.T) {
		root := t.TempDir()
		got := readAndDial(root)
		assert.Equal(t, adoptionNoRecord, got.status)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		root := t.TempDir()
		path := pidfile.Path(root, PIDFileName)
		require.NoError(t, os.WriteFile(path, []byte(`{"pid":`), 0o600))
		got := readAndDial(root)
		assert.Equal(t, adoptionMalformed, got.status)
		_, err := os.Stat(path)
		assert.NoError(t, err, "discovery must not mutate the pidfile")
	})

	t.Run("invalid v2 fields", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{
			Schema: pidfile.SchemaV2,
			Kind:   "foreign-kind",
			Pid:    os.Getpid(),
			Birth:  "birth",
			Port:   3307,
		}))
		got := readAndDial(root)
		assert.Equal(t, adoptionMalformed, got.status)
		assert.ErrorIs(t, got.err, pidfile.ErrKindMismatch)
	})

	t.Run("I/O error", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(pidfile.Path(root, PIDFileName), 0o700))
		got := readAndDial(root)
		assert.Equal(t, adoptionIOErr, got.status)
		assert.Error(t, got.err)
	})

	t.Run("legacy", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{Pid: os.Getpid(), Port: 3307}))
		got := readAndDial(root)
		assert.Equal(t, adoptionLegacy, got.status)
		_, err := os.Stat(pidfile.Path(root, PIDFileName))
		assert.NoError(t, err, "discovery must not mutate the pidfile")
	})

	t.Run("dead process", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		pf := v2Record(t, root, pidfile.KindProxy, helper)
		handle, err := procid.Open(pf.Pid, procid.Token(pf.Birth))
		require.NoError(t, err)
		require.NoError(t, handle.Kill())
		require.NoError(t, handle.Close())
		assertHelperExited(t, helper)
		pf.ControlPort = 3308
		require.NoError(t, pidfile.Write(root, PIDFileName, pf))
		got := readAndDial(root)
		assert.Equal(t, adoptionStaleDead, got.status)
	})

	t.Run("wrong birth", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		pf := v2Record(t, root, pidfile.KindProxy, helper)
		pf.Birth += "-wrong"
		pf.ControlPort = 3308
		require.NoError(t, pidfile.Write(root, PIDFileName, pf))
		got := readAndDial(root)
		assert.Equal(t, adoptionStaleDead, got.status)
		assertHelperAlive(t, helper)
	})
}

func TestReadAndDialProbeErrorsAreNonFatalDiscovery(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	pf := v2Record(t, root, pidfile.KindProxy, helper)
	pf.ControlPort = 3308
	require.NoError(t, pidfile.Write(root, PIDFileName, pf))

	originalVerify := verifyProcessIdentity
	originalRootID := resolveRootIdentity
	originalSecret := readControlSecret
	t.Cleanup(func() {
		verifyProcessIdentity = originalVerify
		resolveRootIdentity = originalRootID
		readControlSecret = originalSecret
	})

	t.Run("process verification", func(t *testing.T) {
		verifyProcessIdentity = func(int, procid.Token) (bool, error) {
			return false, errors.New("access denied")
		}
		got := readAndDial(root)
		assert.Equal(t, adoptionUnverifiable, got.status)
		assert.NotEqual(t, adoptionIOErr, got.status)
		verifyProcessIdentity = originalVerify
	})

	t.Run("root identity", func(t *testing.T) {
		resolveRootIdentity = func(string) (string, error) {
			return "", errors.New("root resolution failed")
		}
		got := readAndDial(root)
		assert.Equal(t, adoptionUnverifiable, got.status)
		assert.NotEqual(t, adoptionIOErr, got.status)
		resolveRootIdentity = originalRootID
	})

	t.Run("secret read", func(t *testing.T) {
		readControlSecret = func(string) (string, error) {
			return "", errors.New("secret read failed")
		}
		got := readAndDial(root)
		assert.Equal(t, adoptionUnverifiable, got.status)
		assert.NotEqual(t, adoptionIOErr, got.status)
		readControlSecret = originalSecret
	})
	assertHelperAlive(t, helper)
}

func TestReadAndDialForeignListenerWithUnrelatedLiveProcess(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()

	pf := v2Record(t, root, pidfile.KindProxy, helper)
	pf.Port = ln.Addr().(*net.TCPAddr).Port
	pf.ControlPort = pf.Port
	require.NoError(t, pidfile.Write(root, PIDFileName, pf))
	_, err = identity.WriteSecret(root)
	require.NoError(t, err)

	got := readAndDial(root)
	assert.Equal(t, adoptionIdentityMismatch, got.status)
	assertHelperAlive(t, helper)
}

func TestReadAndDialForeignListenerWithDeadOrWrongProcessIdentity(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	t.Run("dead pid", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		pf := v2Record(t, root, pidfile.KindProxy, helper)
		handle, openErr := procid.Open(pf.Pid, procid.Token(pf.Birth))
		require.NoError(t, openErr)
		require.NoError(t, handle.Kill())
		require.NoError(t, handle.Close())
		assertHelperExited(t, helper)
		pf.Port = port
		pf.ControlPort = port
		require.NoError(t, pidfile.Write(root, PIDFileName, pf))
		assert.Equal(t, adoptionStaleDead, readAndDial(root).status)
	})

	t.Run("wrong birth", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		pf := v2Record(t, root, pidfile.KindProxy, helper)
		pf.Birth += "-wrong"
		pf.Port = port
		pf.ControlPort = port
		require.NoError(t, pidfile.Write(root, PIDFileName, pf))
		assert.Equal(t, adoptionStaleDead, readAndDial(root).status)
		assertHelperAlive(t, helper)
	})
}

func TestReadAndDialRejectsAuthenticatedRootMismatch(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	token, err := procid.Capture(os.Getpid())
	require.NoError(t, err)
	rootIDA, err := identity.RootID(rootA)
	require.NoError(t, err)

	dataListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = dataListener.Close() })
	dataPort := dataListener.Addr().(*net.TCPAddr).Port

	secret, err := identity.WriteSecret(rootA)
	require.NoError(t, err)
	var reply identity.IdentReply
	var replyMu sync.RWMutex
	control, err := startControl(rootA, func() identity.IdentReply {
		replyMu.RLock()
		defer replyMu.RUnlock()
		return reply
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = control.Close() })
	replyMu.Lock()
	reply = identity.IdentReply{
		Schema:      pidfile.SchemaV2,
		Role:        pidfile.KindProxy,
		RootID:      rootIDA,
		UpstreamID:  "upstream",
		PID:         os.Getpid(),
		Birth:       string(token),
		DataPort:    dataPort,
		ControlPort: control.Port(),
	}
	replyMu.Unlock()
	pf := pidfile.PidFile{
		Schema:      pidfile.SchemaV2,
		Kind:        pidfile.KindProxy,
		Pid:         os.Getpid(),
		Birth:       string(token),
		Port:        dataPort,
		ControlPort: control.Port(),
		RootID:      rootIDA,
		UpstreamID:  "upstream",
	}
	require.NoError(t, pidfile.Write(rootA, PIDFileName, pf))
	require.NoError(t, pidfile.Write(rootB, PIDFileName, pf))
	require.NoError(t, os.WriteFile(filepath.Join(rootB, identity.SecretFileName), []byte(secret+"\n"), 0o600))

	got := readAndDial(rootB)
	assert.Equal(t, adoptionIdentityMismatch, got.status)
}

// The handshake accepts schema v2 or newer, matching pidfile.ValidateV2's
// forward-compat policy for records: a newer proxy that still answers the v2
// handshake remains adoptable.
func TestReadAndDialAcceptsNewerSchemaHandshake(t *testing.T) {
	root := t.TempDir()
	token, err := procid.Capture(os.Getpid())
	require.NoError(t, err)
	rootID, err := identity.RootID(root)
	require.NoError(t, err)

	dataListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = dataListener.Close() })
	dataPort := dataListener.Addr().(*net.TCPAddr).Port

	_, err = identity.WriteSecret(root)
	require.NoError(t, err)
	var reply identity.IdentReply
	var replyMu sync.RWMutex
	control, err := startControl(root, func() identity.IdentReply {
		replyMu.RLock()
		defer replyMu.RUnlock()
		return reply
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = control.Close() })
	replyMu.Lock()
	reply = identity.IdentReply{
		Schema:      pidfile.SchemaV2 + 1,
		Role:        pidfile.KindProxy,
		RootID:      rootID,
		UpstreamID:  "upstream",
		PID:         os.Getpid(),
		Birth:       string(token),
		DataPort:    dataPort,
		ControlPort: control.Port(),
	}
	replyMu.Unlock()
	require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{
		Schema:      pidfile.SchemaV2 + 1,
		Kind:        pidfile.KindProxy,
		Pid:         os.Getpid(),
		Birth:       string(token),
		Port:        dataPort,
		ControlPort: control.Port(),
		RootID:      rootID,
		UpstreamID:  "upstream",
	}))

	got := readAndDial(root)
	assert.Equal(t, adoptionAdopted, got.status)
	assert.Equal(t, dataPort, got.endpoint.Port)
}

func TestLegacyRecordFailClosed(t *testing.T) {
	t.Run("held lock returns actionable error without kill", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{
			Pid:  helper.cmd.Process.Pid,
			Port: 3307,
		}))
		lock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)
		defer lock.Unlock()

		_, err = GetCreateDatabaseProxyServerEndpoint(root, externalOpenOpts(root))
		require.Error(t, err)
		assert.Contains(t, err.Error(), filepath.Join(root, LockFileName))
		assert.Contains(t, err.Error(), "old bd binary")
		assertHelperAlive(t, helper)
		_, err = os.Stat(pidfile.Path(root, PIDFileName))
		assert.NoError(t, err)
	})

	t.Run("free lock quarantines and enters spawn path without kill", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{
			Pid:  helper.cmd.Process.Pid,
			Port: 3307,
		}))
		discovery := readAndDial(root)
		require.Equal(t, adoptionLegacy, discovery.status)
		lock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)

		previous := ResolveExecutable
		ResolveExecutable = func() (string, error) { return "", errors.New("spawn reached") }
		t.Cleanup(func() { ResolveExecutable = previous })
		_, err = spawnAndHandoff(root, externalOpenOpts(root), time.Now().Add(time.Second), "", lock, discovery)
		require.ErrorContains(t, err, "spawn reached")

		assertHelperAlive(t, helper)
		assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
		assert.Len(t, quarantines(t, root, PIDFileName), 1)
	})
}

func TestIdentityMismatchQuarantinesRecordWithoutKillingProcess(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	pf := v2Record(t, root, pidfile.KindProxy, helper)
	pf.ControlPort = 3308
	require.NoError(t, pidfile.Write(root, PIDFileName, pf))
	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)

	previous := ResolveExecutable
	ResolveExecutable = func() (string, error) { return "", errors.New("spawn reached") }
	t.Cleanup(func() { ResolveExecutable = previous })
	_, err = spawnAndHandoff(
		root,
		externalOpenOpts(root),
		time.Now().Add(time.Second),
		"",
		lock,
		adoptionResult{
			status:  adoptionIdentityMismatch,
			pidfile: &pf,
			err:     errors.New("foreign control listener"),
		},
	)
	require.ErrorContains(t, err, "spawn reached")
	assertHelperAlive(t, helper)
	assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
	assert.Len(t, quarantines(t, root, PIDFileName), 1)
}

func TestUnverifiableDiscoveryQuarantinesUnderLockWithoutKillingProcess(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	pf := v2Record(t, root, pidfile.KindProxy, helper)
	pf.ControlPort = 3308
	require.NoError(t, pidfile.Write(root, PIDFileName, pf))
	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)

	previous := ResolveExecutable
	ResolveExecutable = func() (string, error) { return "", errors.New("spawn reached") }
	t.Cleanup(func() { ResolveExecutable = previous })
	_, err = spawnAndHandoff(
		root,
		externalOpenOpts(root),
		time.Now().Add(time.Second),
		"",
		lock,
		adoptionResult{
			status:  adoptionUnverifiable,
			pidfile: &pf,
			err:     errors.New("identity probe denied"),
		},
	)
	require.ErrorContains(t, err, "spawn reached")
	assertHelperAlive(t, helper)
	assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
	assert.Len(t, quarantines(t, root, PIDFileName), 1)
}

func TestMalformedRecordQuarantinesBeforeSpawn(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(pidfile.Path(root, PIDFileName), []byte(`{"pid":`), 0o600))
	discovery := readAndDial(root)
	require.Equal(t, adoptionMalformed, discovery.status)
	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)

	previous := ResolveExecutable
	ResolveExecutable = func() (string, error) { return "", errors.New("spawn reached") }
	t.Cleanup(func() { ResolveExecutable = previous })
	_, err = spawnAndHandoff(root, externalOpenOpts(root), time.Now().Add(time.Second), "", lock, discovery)
	require.ErrorContains(t, err, "spawn reached")
	assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
	assert.Len(t, quarantines(t, root, PIDFileName), 1)
}

func TestGetCreatePropagatesPIDFileIOErrorWithoutSpawning(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(pidfile.Path(root, PIDFileName), 0o700))
	_, err := GetCreateDatabaseProxyServerEndpoint(root, externalOpenOpts(root))
	require.ErrorContains(t, err, "discover proxy")
	assert.NoFileExists(t, filepath.Join(root, spawnMarkerFileName))
}

func TestCleanupOrphanBackendRefusesHeldChildLock(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	require.NoError(t, pidfile.Write(root, server.PIDFileName, v2Record(t, root, pidfile.KindDoltBackend, helper)))

	proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	defer proxyLock.Unlock()
	childLock, err := util.TryLock(filepath.Join(root, server.LockFileName))
	require.NoError(t, err)
	defer childLock.Unlock()

	err = cleanupOrphanBackend(root)
	require.ErrorContains(t, err, "refusing backend cleanup")
	assertHelperAlive(t, helper)
	assert.FileExists(t, pidfile.Path(root, server.PIDFileName))
	assert.Empty(t, quarantines(t, root, server.PIDFileName))
}

func TestCleanupOrphanBackendRefusesRootMismatch(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	pf := v2Record(t, root, pidfile.KindDoltBackend, helper)
	pf.RootID = "different-workspace"
	require.NoError(t, pidfile.Write(root, server.PIDFileName, pf))

	proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	defer proxyLock.Unlock()
	err = cleanupOrphanBackend(root)
	require.ErrorContains(t, err, "root identity mismatch")
	assertHelperAlive(t, helper)
	assert.FileExists(t, pidfile.Path(root, server.PIDFileName))
	assert.Empty(t, quarantines(t, root, server.PIDFileName))
}

func TestCleanupOrphanBackendLegacyRecords(t *testing.T) {
	t.Run("dead pid quarantines", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		handle, err := procid.Open(helper.cmd.Process.Pid, helper.token)
		require.NoError(t, err)
		require.NoError(t, handle.Kill())
		require.NoError(t, handle.Close())
		assertHelperExited(t, helper)
		require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{
			Pid:  helper.cmd.Process.Pid,
			Port: 3306,
		}))

		proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)
		defer proxyLock.Unlock()
		require.NoError(t, cleanupOrphanBackend(root))
		assert.NoFileExists(t, pidfile.Path(root, server.PIDFileName))
		assert.Len(t, quarantines(t, root, server.PIDFileName), 1)
	})

	t.Run("live pid refuses", func(t *testing.T) {
		root := t.TempDir()
		helper := startHelperProcess(t)
		require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{
			Pid:  helper.cmd.Process.Pid,
			Port: 3306,
		}))

		proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
		require.NoError(t, err)
		defer proxyLock.Unlock()
		err = cleanupOrphanBackend(root)
		require.ErrorContains(t, err, "unverifiable live process")
		require.ErrorContains(t, err, pidfile.Path(root, server.PIDFileName)+".stale-<unix-timestamp>")
		assertHelperAlive(t, helper)
		assert.FileExists(t, pidfile.Path(root, server.PIDFileName))
		assert.Empty(t, quarantines(t, root, server.PIDFileName))
	})
}

func TestCleanupOrphanBackendBirthMismatchIsDeadRecord(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	pf := v2Record(t, root, pidfile.KindDoltBackend, helper)
	pf.Birth += "-recycled"
	require.NoError(t, pidfile.Write(root, server.PIDFileName, pf))

	proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	defer proxyLock.Unlock()
	require.NoError(t, cleanupOrphanBackend(root))
	assertHelperAlive(t, helper)
	assert.NoFileExists(t, pidfile.Path(root, server.PIDFileName))
	assert.Len(t, quarantines(t, root, server.PIDFileName), 1)
}

func TestCleanupOrphanBackendKillsVerifiedOrphanAndQuarantines(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	require.NoError(t, pidfile.Write(root, server.PIDFileName, v2Record(t, root, pidfile.KindDoltBackend, helper)))

	proxyLock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)
	previous := ResolveExecutable
	ResolveExecutable = func() (string, error) { return "", errors.New("spawn reached after orphan cleanup") }
	t.Cleanup(func() { ResolveExecutable = previous })
	_, err = spawnAndHandoff(
		root,
		externalOpenOpts(root),
		time.Now().Add(time.Second),
		"",
		proxyLock,
		adoptionResult{status: adoptionNoRecord},
	)
	require.ErrorContains(t, err, "spawn reached after orphan cleanup")

	assertHelperExited(t, helper)
	assert.NoFileExists(t, pidfile.Path(root, server.PIDFileName))
	assert.Len(t, quarantines(t, root, server.PIDFileName), 1)
}

func TestQuarantineNamingCollisionAndRetentionSweep(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(2_000_000_000, 0)
	currentPath := pidfile.Path(root, PIDFileName)
	require.NoError(t, os.WriteFile(currentPath, []byte("first"), 0o600))
	first, err := quarantineRecord(root, PIDFileName, now)
	require.NoError(t, err)
	assert.Equal(t, currentPath+".stale-2000000000", first)

	require.NoError(t, os.WriteFile(currentPath, []byte("second"), 0o600))
	second, err := quarantineRecord(root, PIDFileName, now)
	require.NoError(t, err)
	assert.Equal(t, currentPath+".stale-2000000001", second)
	require.NoError(t, os.WriteFile(currentPath, []byte("current"), 0o600))
	foreign := filepath.Join(root, "foreign.stale-1")
	require.NoError(t, os.WriteFile(foreign, []byte("foreign"), 0o600))
	recent := currentPath + ".stale-" + strconv.FormatInt(now.Add(2*24*time.Hour).Unix(), 10)
	require.NoError(t, os.WriteFile(recent, []byte("recent"), 0o600))

	require.NoError(t, sweepOldQuarantines(root, now.Add(31*24*time.Hour)))
	assert.NoFileExists(t, first)
	assert.NoFileExists(t, second)
	assert.FileExists(t, currentPath, "current record must never be swept")
	assert.FileExists(t, foreign, "foreign files must never be swept")
	assert.FileExists(t, recent, "quarantines younger than 30 days must be retained")
}

func TestShutdownVerifiedRecordStopsProcess(t *testing.T) {
	root := t.TempDir()
	proxyHelper := startHelperProcess(t)
	backendHelper := startHelperProcess(t)
	require.NoError(t, pidfile.Write(root, PIDFileName, v2Record(t, root, pidfile.KindProxy, proxyHelper)))
	require.NoError(t, pidfile.Write(root, server.PIDFileName, v2Record(t, root, pidfile.KindDoltBackend, backendHelper)))

	require.NoError(t, Shutdown(root))
	assertHelperExited(t, proxyHelper)
	assertHelperExited(t, backendHelper)
	assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
	assert.NoFileExists(t, pidfile.Path(root, server.PIDFileName))
}

func TestShutdownReportsPartialOutcomeAndRefusesLegacyProcess(t *testing.T) {
	root := t.TempDir()
	proxyHelper := startHelperProcess(t)
	backendHelper := startHelperProcess(t)
	require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{
		Pid:  proxyHelper.cmd.Process.Pid,
		Port: 3307,
	}))
	require.NoError(t, pidfile.Write(root, server.PIDFileName, v2Record(t, root, pidfile.KindDoltBackend, backendHelper)))

	err := Shutdown(root)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnverifiableProcess)
	assert.True(t, CanForceStopUnverified(err))
	assert.Contains(t, err.Error(), "backend stopped; proxy left running")
	assert.Contains(t, err.Error(), pidfile.Path(root, PIDFileName))
	assert.Contains(t, err.Error(), "old bd binary")
	assert.Contains(t, err.Error(), pidfile.Path(root, PIDFileName)+".stale-<unix-timestamp>")
	assertHelperAlive(t, proxyHelper)
	assertHelperExited(t, backendHelper)
	assert.FileExists(t, pidfile.Path(root, PIDFileName))
	assert.Empty(t, quarantines(t, root, PIDFileName))
}

func TestCanForceStopUnverifiedRequiresUnverifiableRefusals(t *testing.T) {
	unverifiable := fmt.Errorf("%w: legacy record", ErrUnverifiableProcess)
	other := errors.New("unrelated shutdown failure")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "proxy only", err: &shutdownError{proxyErr: unverifiable}, want: true},
		{name: "backend only", err: &shutdownError{backendErr: unverifiable}, want: true},
		{name: "paired legacy", err: &shutdownError{proxyErr: unverifiable, backendErr: unverifiable}, want: true},
		{name: "unverifiable proxy with unrelated backend error", err: &shutdownError{proxyErr: unverifiable, backendErr: other}},
		{name: "unrelated proxy error", err: &shutdownError{proxyErr: other}},
		{name: "bare sentinel", err: unverifiable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, CanForceStopUnverified(tc.err))
		})
	}
}

func TestShutdownQuarantinesBirthMismatchWithoutKillingProcess(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	pf := v2Record(t, root, pidfile.KindProxy, helper)
	pf.Birth += "-wrong"
	require.NoError(t, pidfile.Write(root, PIDFileName, pf))

	require.NoError(t, Shutdown(root))
	assertHelperAlive(t, helper)
	assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
	assert.Len(t, quarantines(t, root, PIDFileName), 1)
}

func TestShutdownRefusesForeignRootProxyRecord(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	pf := v2Record(t, root, pidfile.KindProxy, helper)
	pf.RootID = "foreign-workspace-root"
	require.NoError(t, pidfile.Write(root, PIDFileName, pf))

	err := Shutdown(root)
	require.ErrorContains(t, err, "root identity mismatch")
	require.ErrorContains(t, err, pidfile.Path(root, PIDFileName))
	assertHelperAlive(t, helper)
	assert.FileExists(t, pidfile.Path(root, PIDFileName))
	assert.Empty(t, quarantines(t, root, PIDFileName))
}

func TestShutdownQuarantinesVerifiedDeadRecord(t *testing.T) {
	root := t.TempDir()
	helper := startHelperProcess(t)
	pf := v2Record(t, root, pidfile.KindProxy, helper)
	handle, err := procid.Open(pf.Pid, procid.Token(pf.Birth))
	require.NoError(t, err)
	require.NoError(t, handle.Kill())
	require.NoError(t, handle.Close())
	assertHelperExited(t, helper)
	require.NoError(t, pidfile.Write(root, PIDFileName, pf))

	require.NoError(t, Shutdown(root))
	assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
	assert.Len(t, quarantines(t, root, PIDFileName), 1)
}

func TestShutdownQuarantinesDeadLegacyAndMalformedRecords(t *testing.T) {
	tests := []struct {
		name   string
		record func(helper helperProcess) pidfile.PidFile
	}{
		{
			name: "legacy",
			record: func(helper helperProcess) pidfile.PidFile {
				return pidfile.PidFile{Pid: helper.cmd.Process.Pid, Port: 3307}
			},
		},
		{
			name: "malformed v2",
			record: func(helper helperProcess) pidfile.PidFile {
				return pidfile.PidFile{
					Schema: pidfile.SchemaV2,
					Kind:   pidfile.KindProxy,
					Pid:    helper.cmd.Process.Pid,
					Port:   3307,
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			helper := startHelperProcess(t)
			record := tc.record(helper)
			handle, err := procid.Open(helper.cmd.Process.Pid, helper.token)
			require.NoError(t, err)
			require.NoError(t, handle.Kill())
			require.NoError(t, handle.Close())
			assertHelperExited(t, helper)
			require.NoError(t, pidfile.Write(root, PIDFileName, record))

			require.NoError(t, Shutdown(root))
			assert.NoFileExists(t, pidfile.Path(root, PIDFileName))
			assert.Len(t, quarantines(t, root, PIDFileName), 1)
		})
	}
}

func TestControlFilePathsIncludesSecretAndQuarantines(t *testing.T) {
	root := t.TempDir()
	proxyStale := pidfile.Path(root, PIDFileName) + ".stale-100"
	backendStale := pidfile.Path(root, server.PIDFileName) + ".stale-200"
	require.NoError(t, os.WriteFile(proxyStale, []byte("proxy"), 0o600))
	require.NoError(t, os.WriteFile(backendStale, []byte("backend"), 0o600))
	_, err := identity.WriteSecret(root)
	require.NoError(t, err)

	paths := ControlFilePaths(root)
	assert.Contains(t, paths, filepath.Join(root, identity.SecretFileName))
	assert.Contains(t, paths, proxyStale)
	assert.Contains(t, paths, backendStale)

	assert.Empty(t, PurgeControlFiles(root))
	assert.NoFileExists(t, filepath.Join(root, identity.SecretFileName))
	assert.NoFileExists(t, proxyStale)
	assert.NoFileExists(t, backendStale)
}

// N-process cold-start convergence remains intentionally deferred to the
// lifecycle-lane stage; this security suite covers single-spawner invariants.

func externalOpenOpts(root string) OpenOpts {
	return OpenOpts{
		Backend:     BackendExternal,
		External:    configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 3306},
		LogFilePath: filepath.Join(root, LogFileName),
		Port:        3307,
	}
}

func quarantines(t *testing.T, root, name string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, name+".stale-*"))
	require.NoError(t, err)
	return matches
}
