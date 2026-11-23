//go:build unix

package server_test

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// backendExitNoticeTimeout bounds how long Running may keep reporting a
// backend that has left the process table. dolt's own graceful shutdown on
// SIGTERM dominates this; the detection itself is immediate.
const (
	backendExitNoticeTimeout = 30 * time.Second
	backendExitNoticePoll    = 50 * time.Millisecond
)

// TestDoltServer_RunningFalseAfterBackendExits verifies Running observes the
// dolt sql-server leaving on its own, whatever its exit status. A SIGTERM'd
// dolt shuts down gracefully and exits 0; before the fix only a non-zero exit
// flipped Running, so the proxy's backend health watcher never fired and the
// proxy stayed up (and adoptable) in front of a dead backend.
func TestDoltServer_RunningFalseAfterBackendExits(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{name: "clean exit on SIGTERM", sig: syscall.SIGTERM},
		{name: "killed", sig: syscall.SIGKILL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rootDir := newDoltServer(t)
			ctx := context.Background()
			require.NoError(t, s.Start(ctx))
			require.True(t, s.Running(ctx))

			pf, err := pidfile.Read(rootDir, server.PIDFileName)
			require.NoError(t, err)
			require.NotNil(t, pf)
			require.NoError(t, syscall.Kill(pf.Pid, tc.sig))

			deadline := time.Now().Add(backendExitNoticeTimeout)
			for s.Running(ctx) && time.Now().Before(deadline) {
				time.Sleep(backendExitNoticePoll)
			}
			require.False(t, s.Running(ctx), "Running still true %s after the dolt sql-server (pid %d) got %s", backendExitNoticeTimeout, pf.Pid, tc.sig)

			// A backend that exited on its own is not a Stop failure.
			stopCtx, cancel := context.WithTimeout(ctx, stopTimeout)
			defer cancel()
			require.NoError(t, s.Stop(stopCtx))
			left, err := pidfile.Read(rootDir, server.PIDFileName)
			require.NoError(t, err)
			assert.Nil(t, left, "Stop must remove the backend pid record")
		})
	}
}
