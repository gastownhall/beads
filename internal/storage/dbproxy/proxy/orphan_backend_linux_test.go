//go:build linux

package proxy

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AC-4 end to end on Linux: a real process named dolt, launched with this
// root's config.yaml as bd 1.3.x did and named by sql-server.info, is killed
// through the pidfd-held identity checks.
func TestCleanupOrphanBackendKillsRealUnrecordedDolt(t *testing.T) {
	root := t.TempDir()
	helper := startNamedForceStopHelper(t, t.TempDir(), "dolt", "--config", filepath.Join(root, "config.yaml"))
	holdProxyLock(t, root)
	writeServerInfo(t, root, strconv.Itoa(helper.cmd.Process.Pid)+":3307:secret")

	require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
	assertHelperExited(t, helper)
}

// AC-5 end to end on Linux: a real dolt launched with another root's
// config.yaml stays alive.
func TestCleanupOrphanBackendSparesRealDoltOfAnotherRoot(t *testing.T) {
	root := t.TempDir()
	helper := startNamedForceStopHelper(t, t.TempDir(), "dolt", "--config", filepath.Join(t.TempDir(), "config.yaml"))
	holdProxyLock(t, root)
	writeServerInfo(t, root, strconv.Itoa(helper.cmd.Process.Pid)+":3307:secret")

	require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
	assertHelperAlive(t, helper)
}

// The --config match reads the real argv split on its NUL separators and
// compares whole arguments, so a real dolt whose command line merely
// contains "--config <root>/config.yaml" stays alive: a longer file name,
// or the pair inside one argument (which joined with spaces reads exactly
// like the bd launch).
func TestCleanupOrphanBackendSparesRealDoltWithoutExactConfigArg(t *testing.T) {
	for name, args := range map[string]func(root string) []string{
		"config.yaml.other": func(root string) []string {
			return []string{"--config", filepath.Join(root, "config.yaml.other")}
		},
		"pair inside one argument": func(root string) []string {
			return []string{"--config " + filepath.Join(root, "config.yaml")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			helper := startNamedForceStopHelper(t, t.TempDir(), "dolt", args(root)...)
			holdProxyLock(t, root)
			writeServerInfo(t, root, strconv.Itoa(helper.cmd.Process.Pid)+":3307:secret")

			require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
			assertHelperAlive(t, helper)
		})
	}
}

// On a kernel without pidfd the handle is the bare PID, which the orphan
// could leave between the checks and the kill. A real, fully matching dolt
// is then left alive, with one log line naming the manual remedy.
func TestCleanupOrphanBackendSparesRealDoltWithoutPidfd(t *testing.T) {
	root := t.TempDir()
	helper := startNamedForceStopHelper(t, t.TempDir(), "dolt", "--config", filepath.Join(root, "config.yaml"))
	holdProxyLock(t, root)
	infoPath := writeServerInfo(t, root, strconv.Itoa(helper.cmd.Process.Pid)+":3307:secret")
	previous := openOrphanCandidate
	openOrphanCandidate = func(pid int) (orphanCandidate, bool, error) {
		return &unverifiedProcess{pid: pid, pidfd: -1}, false, nil
	}
	t.Cleanup(func() { openOrphanCandidate = previous })
	logs := captureLog(t)

	require.NoError(t, cleanupOrphanBackend(root, BackendLocalServer))
	assertHelperAlive(t, helper)
	pid := strconv.Itoa(helper.cmd.Process.Pid)
	assert.Contains(t, logs.String(), "dolt pid "+pid+" named by "+infoPath)
	assert.Contains(t, logs.String(), "stop pid "+pid+" and retry")
}
