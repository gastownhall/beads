package proxy

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

var proxyTestGreeting = []byte("\x0a5.7.9-proxy-test\x00")

func newGreetingTestServer(t *testing.T) *server.TestDatabaseServerImpl {
	t.Helper()
	s := server.New()
	s.Handler = func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write(proxyTestGreeting)
	}
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s
}

// TestWaitForServerReady_MuteServerIsNotReady proves that a connection the
// fake server accepts but never greets is not a ready MySQL server. The fake
// is deliberately in-memory: the readiness seam only needs Dial and the
// greeting bytes, not a real database process.
func TestWaitForServerReady_MuteServerIsNotReady(t *testing.T) {
	t.Parallel()

	s := server.New()
	s.Handler = server.DiscardHandler
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	err := waitForServerReady(ctx, s, time.Second)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Positive(t, s.Snapshot().AcceptedConns, "the fake server must have accepted a readiness probe")
}

func TestWaitForServerReady_GreetedServerIsReady(t *testing.T) {
	t.Parallel()

	s := newGreetingTestServer(t)
	require.NoError(t, waitForServerReady(context.Background(), s, time.Second))
	require.EqualValues(t, 1, s.Snapshot().AcceptedConns)
}

// TestWaitForServerReady_SlowExternalGreetingIsReady covers an external
// upstream whose greeting lands after DrainAndCloseProbe's 100ms loopback
// window, as it does from a remote host where the greeting trails the dial by
// a round trip. The probe must wait for it within the dial budget instead of
// reading a healthy upstream as mute on every retry.
func TestWaitForServerReady_SlowExternalGreetingIsReady(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var accepts atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			go func() {
				defer func() { _ = conn.Close() }()
				time.Sleep(300 * time.Millisecond)
				_, _ = conn.Write(proxyTestGreeting)
			}()
		}
	}()

	upstream, err := server.NewExternalDoltServer(configfile.ExternalDoltConfig{
		Host: "127.0.0.1",
		Port: ln.Addr().(*net.TCPAddr).Port,
	})
	require.NoError(t, err)
	require.NoError(t, upstream.Start(context.Background()))

	require.NoError(t, waitForServerReady(context.Background(), upstream, time.Second))
	require.EqualValues(t, 1, accepts.Load(), "the first probe must wait out the slow greeting, not redial")
}

// TestWaitForServerReady_CancelEndsGreetingWait proves the dial-sized
// greeting wait still honors cancellation: a stop or signal aborts the ready
// wait promptly rather than riding out the dial budget against a mute
// upstream.
func TestWaitForServerReady_CancelEndsGreetingWait(t *testing.T) {
	t.Parallel()

	s := server.New()
	s.Handler = server.DiscardHandler
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)
	started := time.Now()
	err := waitForServerReady(ctx, s, serverReadyTimeout)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(started), readyDialTimeout/2,
		"cancellation must end the greeting wait, not ride out the dial budget")
	require.EqualValues(t, 1, s.Snapshot().AcceptedConns, "the cancellation must land during the first probe's greeting wait")
}

// closeHookConn invalidates a readiness condition precisely when the probe
// has finished draining and is about to report success.
type closeHookConn struct {
	net.Conn
	onClose func()
	once    sync.Once
}

func (c *closeHookConn) Close() error {
	c.once.Do(c.onClose)
	return c.Conn.Close()
}

type livenessChangesAfterProbeServer struct {
	*server.TestDatabaseServerImpl
	probeClosed sync.Once
	lost        bool
	mu          sync.Mutex
}

func (s *livenessChangesAfterProbeServer) Dial(ctx context.Context) (net.Conn, error) {
	conn, err := s.TestDatabaseServerImpl.Dial(ctx)
	if err != nil {
		return nil, err
	}
	return &closeHookConn{Conn: conn, onClose: func() {
		s.probeClosed.Do(func() {
			s.mu.Lock()
			s.lost = true
			s.mu.Unlock()
		})
	}}, nil
}

func (s *livenessChangesAfterProbeServer) Running(ctx context.Context) bool {
	s.mu.Lock()
	if s.lost {
		s.lost = false
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	return s.TestDatabaseServerImpl.Running(ctx)
}

// TestWaitForServerReady_RechecksLivenessAfterProbeDrain ensures a backend
// that dies while its greeting is drained cannot be reported ready. The fake
// becomes live again for the retry, proving the second probe, rather than the
// stale first one, establishes readiness.
func TestWaitForServerReady_RechecksLivenessAfterProbeDrain(t *testing.T) {
	t.Parallel()

	s := &livenessChangesAfterProbeServer{TestDatabaseServerImpl: newGreetingTestServer(t)}
	require.NoError(t, waitForServerReady(context.Background(), s, time.Second))
	require.GreaterOrEqual(t, s.Snapshot().AcceptedConns, int64(2), "liveness loss after draining must force a new probe")
}

func TestWaitForServerReady_RechecksContextAfterProbeDrain(t *testing.T) {
	t.Parallel()

	base := newGreetingTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &cancelAfterProbeServer{TestDatabaseServerImpl: base, cancel: cancel}

	err := waitForServerReady(ctx, s, time.Second)
	require.ErrorIs(t, err, context.Canceled)
}

type cancelAfterProbeServer struct {
	*server.TestDatabaseServerImpl
	cancel context.CancelFunc
}

func (s *cancelAfterProbeServer) Dial(ctx context.Context) (net.Conn, error) {
	conn, err := s.TestDatabaseServerImpl.Dial(ctx)
	if err != nil {
		return nil, err
	}
	return &closeHookConn{Conn: conn, onClose: s.cancel}, nil
}
