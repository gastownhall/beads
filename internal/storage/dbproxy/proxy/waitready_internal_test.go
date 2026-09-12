package proxy

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
