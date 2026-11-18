//go:build linux

package proxy

import (
	"path/filepath"
	"strconv"
	"testing"

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

	require.NoError(t, cleanupOrphanBackend(root))
	assertHelperExited(t, helper)
}

// AC-5 end to end on Linux: a real dolt launched with another root's
// config.yaml stays alive.
func TestCleanupOrphanBackendSparesRealDoltOfAnotherRoot(t *testing.T) {
	root := t.TempDir()
	helper := startNamedForceStopHelper(t, t.TempDir(), "dolt", "--config", filepath.Join(t.TempDir(), "config.yaml"))
	holdProxyLock(t, root)
	writeServerInfo(t, root, strconv.Itoa(helper.cmd.Process.Pid)+":3307:secret")

	require.NoError(t, cleanupOrphanBackend(root))
	assertHelperAlive(t, helper)
}
