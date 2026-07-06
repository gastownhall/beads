package proxy_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	listenWait   = 2 * time.Second
	shutdownWait = 5 * time.Second
	ioTimeout    = 2 * time.Second
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func proxyAddr(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

type proxyHandle struct {
	cancel context.CancelFunc
	done   chan error    // buffered=1, holds Start's return value
	exited chan struct{} // closed when Start returns
}

func (h *proxyHandle) Cancel() { h.cancel() }

func (h *proxyHandle) waitErr(t *testing.T, timeout time.Duration) error {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-h.done:
		return err
	case <-timer.C:
		t.Fatalf("proxy.ListenAndServe did not return within %s", timeout)
		return nil
	}
}

func runProxy(t *testing.T, opts proxy.ProxyOpts) *proxyHandle {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := &proxyHandle{
		cancel: cancel,
		done:   make(chan error, 1),
		exited: make(chan struct{}),
	}
	p := proxy.NewProxyServer(opts)
	go func() {
		defer close(h.exited)
		h.done <- p.ListenAndServe(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		timer := time.NewTimer(shutdownWait)
		defer timer.Stop()
		select {
		case <-h.exited:
		case <-timer.C:
			t.Errorf("proxy did not exit within %s of cancel", shutdownWait)
		}
	})
	return h
}

func waitListening(t *testing.T, root string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pf, err := pidfile.Read(root, proxy.PIDFileName)
		if err == nil && pf != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("proxy pidfile not present within %s", timeout)
}

func assertNoPidFile(t *testing.T, root string) {
	t.Helper()
	pf, err := pidfile.Read(root, proxy.PIDFileName)
	require.NoError(t, err)
	assert.Nil(t, pf)
}

func dialProxy(t *testing.T, port int) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxyAddr(port), ioTimeout)
	require.NoError(t, err)
	require.NoError(t, c.SetDeadline(time.Now().Add(ioTimeout)))
	return c
}

func TestProxy_HappyPath_Echo(t *testing.T) {
	t.Parallel()

	ts := server.New()
	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port,
		IdleTimeout: 0,
		Server:      ts,
		Stats:       stats,
	})
	waitListening(t, root, listenWait)

	conn := dialProxy(t, port)
	_, err := conn.Write([]byte("hello"))
	require.NoError(t, err)
	buf := make([]byte, 5)
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))

	pf, err := pidfile.Read(root, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, pf)
	assert.Equal(t, os.Getpid(), pf.Pid)
	assert.Equal(t, port, pf.Port)

	require.NoError(t, conn.Close())

	h.Cancel()
	require.NoError(t, h.waitErr(t, shutdownWait))

	s := stats.Snapshot()
	assert.Equal(t, int64(1), s.ListenAndServeCalls)
	assert.Equal(t, int64(1), s.BackendStartCalls)
	assert.Equal(t, int64(1), s.BackendStopCalls)
	assert.Equal(t, int64(1), s.AcceptCalls)
	assert.Equal(t, int64(1), s.BackendDialAttempts)
	assert.Equal(t, int64(1), s.BackendDialSuccess)
	assert.Equal(t, int64(0), s.BackendDialErrors)
	assert.Equal(t, int64(1), s.HandledConns)
	assert.Equal(t, int64(5), s.BytesClientToBackend)
	assert.Equal(t, int64(5), s.BytesBackendToClient)

	bs := ts.Snapshot()
	assert.Equal(t, int64(1), bs.StartCalls)
	// readiness Dial + client Dial both go through the backend
	assert.Equal(t, int64(2), bs.DialCalls)
	assert.Equal(t, int64(2), bs.AcceptedConns)
	assert.Equal(t, int64(5), bs.BytesIn)
	assert.Equal(t, int64(5), bs.BytesOut)
	assert.Equal(t, int64(1), bs.StopCalls)

	assertNoPidFile(t, root)
}

func TestProxy_PidFile_WrittenAndRemoved(t *testing.T) {
	t.Parallel()

	ts := server.New()
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port, Server: ts,
	})
	waitListening(t, root, listenWait)

	pf, err := pidfile.Read(root, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, pf)
	assert.Equal(t, os.Getpid(), pf.Pid)
	assert.Equal(t, port, pf.Port)

	h.Cancel()
	require.NoError(t, h.waitErr(t, shutdownWait))

	assertNoPidFile(t, root)
}

func TestProxy_ListenError_PortInUse(t *testing.T) {
	t.Parallel()

	hold, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer hold.Close()
	port := hold.Addr().(*net.TCPAddr).Port

	ts := server.New()
	stats := &proxy.Stats{}
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port, Server: ts, Stats: stats,
	})
	err = h.waitErr(t, shutdownWait)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen on")

	s := stats.Snapshot()
	assert.Equal(t, int64(0), s.ListenAndServeCalls)
	assert.Equal(t, int64(0), s.BackendStartCalls)
	assert.Equal(t, int64(0), ts.Snapshot().StartCalls)

	assertNoPidFile(t, root)
}

func TestProxy_BackendStartError(t *testing.T) {
	t.Parallel()

	ts := server.New()
	ts.StartErr = errors.New("boom")
	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port, Server: ts, Stats: stats,
	})
	err := h.waitErr(t, shutdownWait)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start database server")
	assert.Contains(t, err.Error(), "boom")

	s := stats.Snapshot()
	assert.Equal(t, int64(1), s.ListenAndServeCalls)
	assert.Equal(t, int64(1), s.BackendStartCalls)
	assert.Equal(t, int64(0), s.BackendStopCalls)

	assertNoPidFile(t, root)
}

func TestProxy_BackendNotReady_CtxCancel(t *testing.T) {
	t.Parallel()

	ts := server.New()
	ts.DialErr = errors.New("not ready")
	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port, Server: ts, Stats: stats,
	})
	// The pidfile is written only after readiness succeeds, but DialErr
	// keeps readiness failing — so waitListening would hang. Wait until
	// the proxy is in the readiness loop (>=1 Dial attempt observed).
	require.Eventually(t, func() bool {
		return ts.Snapshot().DialCalls >= 1
	}, listenWait, 10*time.Millisecond)
	h.Cancel()
	err := h.waitErr(t, shutdownWait)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database server not ready")

	s := stats.Snapshot()
	assert.Equal(t, int64(1), s.ListenAndServeCalls)
	assert.Equal(t, int64(1), s.BackendStartCalls)
	assert.Equal(t, int64(1), s.BackendStopCalls)

	bs := ts.Snapshot()
	assert.Equal(t, int64(1), bs.StartCalls)
	assert.GreaterOrEqual(t, bs.DialCalls, int64(1))
	assert.Equal(t, int64(1), bs.StopCalls)

	assertNoPidFile(t, root)
}

func TestProxy_BackendDialError(t *testing.T) {
	t.Parallel()

	ts := server.New()
	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port, Server: ts, Stats: stats,
	})
	// Wait for readiness Dial to succeed, then flip DialErr so subsequent
	// proxied connections fail.
	waitListening(t, root, listenWait)
	ts.SetDialErr(errors.New("refused"))

	for i := 0; i < 2; i++ {
		c := dialProxy(t, port)
		// Proxy accepts then closes us out after the dial error.
		_, err := c.Read(make([]byte, 1))
		assert.Error(t, err, "iteration %d: expected EOF/error from proxy", i)
		require.NoError(t, c.Close())
	}

	require.Eventually(t, func() bool {
		s := stats.Snapshot()
		return s.AcceptCalls >= 2 &&
			s.BackendDialAttempts >= 2 &&
			s.BackendDialErrors >= 2
	}, ioTimeout, 10*time.Millisecond, "stats did not reach 2 dial errors")

	s := stats.Snapshot()
	assert.Equal(t, int64(2), s.AcceptCalls)
	assert.Equal(t, int64(2), s.BackendDialAttempts)
	assert.Equal(t, int64(2), s.BackendDialErrors)
	assert.Equal(t, int64(0), s.BackendDialSuccess)
	assert.Equal(t, int64(0), s.HandledConns)

	h.Cancel()
	require.NoError(t, h.waitErr(t, shutdownWait))
	assertNoPidFile(t, root)
}

func TestProxy_IdleTimeout_Fires(t *testing.T) {
	t.Parallel()

	ts := server.New()
	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port,
		IdleTimeout: 2 * time.Second,
		Server:      ts, Stats: stats,
	})
	waitListening(t, root, listenWait)

	// No client traffic — proxy should self-terminate on idle.
	require.NoError(t, h.waitErr(t, 6*time.Second))

	s := stats.Snapshot()
	assert.Equal(t, int64(1), s.IdleTimeouts)
	assert.Equal(t, int64(1), s.BackendStopCalls)
	assert.Equal(t, int64(1), ts.Snapshot().StopCalls)

	assertNoPidFile(t, root)
}

func TestProxy_IdleTimeout_BlockedByActiveConn(t *testing.T) {
	t.Parallel()

	ts := server.New()
	ts.Handler = server.DiscardHandler
	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port,
		IdleTimeout: 2 * time.Second,
		Server:      ts, Stats: stats,
	})
	waitListening(t, root, listenWait)

	conn := dialProxy(t, port)
	defer conn.Close()

	// Wait past two full idle windows to confirm activeConns gates the watcher.
	time.Sleep(4 * time.Second)
	assert.Equal(t, int64(0), stats.Snapshot().IdleTimeouts)

	// Tear down cleanly.
	require.NoError(t, conn.Close())
	h.Cancel()
	require.NoError(t, h.waitErr(t, shutdownWait))

	assertNoPidFile(t, root)
}

func TestProxy_ConcurrentConnections(t *testing.T) {
	t.Parallel()

	ts := server.New() // default echo handler
	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port, Server: ts, Stats: stats,
	})
	waitListening(t, root, listenWait)

	const N = 5
	const payloadLen = 4

	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", proxyAddr(port), ioTimeout)
			if err != nil {
				errs <- fmt.Errorf("dial: %w", err)
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(ioTimeout))

			payload := []byte(fmt.Sprintf("p%03d", i))
			if _, err := c.Write(payload); err != nil {
				errs <- fmt.Errorf("write: %w", err)
				return
			}
			buf := make([]byte, payloadLen)
			if _, err := io.ReadFull(c, buf); err != nil {
				errs <- fmt.Errorf("read: %w", err)
				return
			}
			if !bytes.Equal(payload, buf) {
				errs <- fmt.Errorf("echo mismatch: got %q want %q", buf, payload)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	require.Eventually(t, func() bool {
		s := stats.Snapshot()
		return s.HandledConns == N &&
			s.BytesClientToBackend == int64(N*payloadLen) &&
			s.BytesBackendToClient == int64(N*payloadLen)
	}, ioTimeout, 10*time.Millisecond, "stats did not converge for N=%d clients", N)

	s := stats.Snapshot()
	assert.Equal(t, int64(N), s.AcceptCalls)
	assert.Equal(t, int64(N), s.BackendDialSuccess)
	assert.Equal(t, int64(0), s.BackendDialErrors)
	assert.Equal(t, int64(N), s.HandledConns)
	assert.Equal(t, int64(N*payloadLen), s.BytesClientToBackend)
	assert.Equal(t, int64(N*payloadLen), s.BytesBackendToClient)

	// readiness Dial counts as one extra AcceptedConn against the backend
	assert.Equal(t, int64(N+1), ts.Snapshot().AcceptedConns)

	h.Cancel()
	require.NoError(t, h.waitErr(t, shutdownWait))
}

func TestProxy_BidirectionalCopy_AsymmetricBytes(t *testing.T) {
	t.Parallel()

	ts := server.New()
	const clientMsgLen = 3
	payload := []byte("backend-says-hi") // 15 bytes
	// Read the client's send first so direction-1's Write completes before
	// the backend side closes; then write the asymmetric payload back.
	ts.Handler = func(c net.Conn) {
		defer c.Close()
		buf := make([]byte, clientMsgLen)
		_, _ = io.ReadFull(c, buf)
		_, _ = c.Write(payload)
	}

	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port, Server: ts, Stats: stats,
	})
	waitListening(t, root, listenWait)

	conn := dialProxy(t, port)
	_, err := conn.Write([]byte("abc"))
	require.NoError(t, err)
	got, err := io.ReadAll(conn)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	require.NoError(t, conn.Close())

	require.Eventually(t, func() bool {
		s := stats.Snapshot()
		return s.HandledConns == 1 &&
			s.BytesClientToBackend == clientMsgLen &&
			s.BytesBackendToClient == int64(len(payload))
	}, ioTimeout, 10*time.Millisecond, "asymmetric byte counters did not converge")

	bs := ts.Snapshot()
	assert.Equal(t, int64(clientMsgLen), bs.BytesIn)
	assert.Equal(t, int64(len(payload)), bs.BytesOut)

	h.Cancel()
	require.NoError(t, h.waitErr(t, shutdownWait))
}

func TestProxy_Cancel_DrainsInFlightConn(t *testing.T) {
	t.Parallel()

	ts := server.New()
	ts.Handler = server.DiscardHandler // backend reads, never writes
	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port, Server: ts, Stats: stats,
	})
	waitListening(t, root, listenWait)

	conn := dialProxy(t, port)
	defer conn.Close()
	_, err := conn.Write([]byte("x"))
	require.NoError(t, err)

	h.Cancel()
	require.NoError(t, h.waitErr(t, shutdownWait))

	// Proxy tore down our conn during shutdown.
	_, err = conn.Read(make([]byte, 1))
	assert.Error(t, err)

	assert.Equal(t, int64(1), stats.Snapshot().BackendStopCalls)
	assert.Equal(t, int64(1), ts.Snapshot().StopCalls)

	assertNoPidFile(t, root)
}

// TestProxy_ConcurrentInstantiation_OnlyOneWinsLock launches N proxy servers
// in parallel against the same rootdir. Exactly one wins the proxy.lock
// flock race, runs to completion, and accounts for a full ListenAndServe /
// BackendStart / BackendStop lifecycle. The losers return ErrLockHeld before
// reaching any side-effecting step.
//
// This documents the proxy package's contract: ListenAndServe serializes
// instantiation per-rootDir via proxy.lock. Same-rootDir contention always
// produces ErrLockHeld for losers — independent of whether they would have
// raced on Listen, pidfile, or backend start.
func TestProxy_ConcurrentInstantiation_OnlyOneWinsLock(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	port := freeTCPPort(t)

	const N = 5
	const settle = 500 * time.Millisecond

	type result struct {
		err   error
		stats *proxy.Stats
	}

	barrier := make(chan struct{})
	results := make(chan result, N)

	for i := 0; i < N; i++ {
		go func() {
			ts := server.New()
			stats := &proxy.Stats{}
			p := proxy.NewProxyServer(proxy.ProxyOpts{
				RootDir: root, Port: port,
				Server: ts, Stats: stats,
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			<-barrier
			go func() { done <- p.ListenAndServe(ctx) }()

			// After settle: losers have already returned (lock probe fails in
			// microseconds); the winner is blocked in the accept loop. Cancel
			// everyone uniformly and collect their final return value.
			time.Sleep(settle)
			cancel()
			results <- result{err: <-done, stats: stats}
		}()
	}

	close(barrier)

	var lockHeld, winners int
	for i := 0; i < N; i++ {
		r := <-results
		if r.err != nil {
			require.ErrorIs(t, r.err, proxy.ErrLockHeld)
			lockHeld++
			s := r.stats.Snapshot()
			// Lock acquisition fires before any IncListenAndServe / IncBackendStart.
			assert.Equal(t, int64(0), s.ListenAndServeCalls)
			assert.Equal(t, int64(0), s.BackendStartCalls)
			assert.Equal(t, int64(0), s.BackendStopCalls)
		} else {
			winners++
			s := r.stats.Snapshot()
			assert.Equal(t, int64(1), s.ListenAndServeCalls)
			assert.Equal(t, int64(1), s.BackendStartCalls)
			assert.Equal(t, int64(1), s.BackendStopCalls)
		}
	}

	assert.Equal(t, 1, winners, "expected exactly 1 lock winner")
	assert.Equal(t, N-1, lockHeld)

	// Winner's pidfile was cleaned up by its deferred Remove.
	assertNoPidFile(t, root)
}

func TestProxy_LockHeld_ReturnsSentinel(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	port := freeTCPPort(t)

	held, err := util.TryLock(filepath.Join(root, proxy.LockFileName))
	require.NoError(t, err)
	defer held.Unlock()

	ts := server.New()
	stats := &proxy.Stats{}
	p := proxy.NewProxyServer(proxy.ProxyOpts{
		RootDir: root, Port: port,
		Server: ts, Stats: stats,
	})
	err = p.ListenAndServe(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, proxy.ErrLockHeld)

	s := stats.Snapshot()
	assert.Equal(t, int64(0), s.ListenAndServeCalls)
	assert.Equal(t, int64(0), s.BackendStartCalls)
	assert.Equal(t, int64(0), s.BackendStopCalls)
	assert.Equal(t, int64(0), ts.Snapshot().StartCalls)

	assertNoPidFile(t, root)
}

func TestProxy_TraceLog_LifecycleEvents(t *testing.T) {
	t.Parallel()

	ts := server.New()
	stats := &proxy.Stats{}
	port := freeTCPPort(t)
	root := t.TempDir()

	const idle = 2 * time.Second
	const tickSlack = 1200 * time.Millisecond
	h := runProxy(t, proxy.ProxyOpts{
		RootDir: root, Port: port,
		IdleTimeout: idle,
		Server:      ts, Stats: stats,
	})
	waitListening(t, root, listenWait)

	time.Sleep(tickSlack)

	conn := dialProxy(t, port)
	_, err := conn.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	require.Equal(t, "ping", string(buf))

	time.Sleep(tickSlack)

	require.NoError(t, conn.Close())

	require.NoError(t, h.waitErr(t, 3*idle))

	body, err := os.ReadFile(filepath.Join(root, proxy.LogFileName))
	require.NoError(t, err)
	text := string(body)

	for _, want := range []string{
		"acceptLoop start",
		"acceptLoop accepted",
		"idleWatcher start",
		"idleWatcher armed",
		"idleWatcher cleared",
		"idleWatcher expired",
		"handleConn(",
		"backend dial ok",
		"client→backend done",
		"backend→client done",
		"acceptLoop exit",
	} {
		assert.Contains(t, text, want, "log missing %q", want)
	}
}
