package proxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newControlServer(t *testing.T) (*controlServer, string, identity.IdentReply) {
	t.Helper()
	root := t.TempDir()
	secret, err := identity.WriteSecret(root)
	require.NoError(t, err)
	want := identity.IdentReply{
		Schema:     pidfile.SchemaV2,
		Role:       pidfile.KindProxy,
		RootID:     "workspace-root",
		UpstreamID: "dolt-server",
		PID:        123,
		Birth:      "linux-v1:boot:1",
		DataPort:   3306,
	}
	control, err := startControl(root, func() identity.IdentReply { return want })
	require.NoError(t, err)
	want.ControlPort = control.Port()
	t.Cleanup(func() { _ = control.Close() })
	return control, secret, want
}

func TestControl_Identify(t *testing.T) {
	control, secret, want := newControlServer(t)
	got, err := identity.Identify("127.0.0.1", control.Port(), secret, time.Second)
	require.NoError(t, err)
	assert.Len(t, got.MAC, 64)
	got.MAC = ""
	assert.Equal(t, &want, got)
}

func TestControl_RejectsWrongSecret(t *testing.T) {
	control, _, _ := newControlServer(t)
	_, err := identity.Identify("127.0.0.1", control.Port(), "not-the-secret", time.Second)
	require.ErrorIs(t, err, identity.ErrIdentRefused)
}

func TestControl_RejectsLegacyRawSecretRequest(t *testing.T) {
	control, secret, _ := newControlServer(t)
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(control.Port())), time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	_, err = io.WriteString(conn, "IDENT "+secret+" 0123456789abcdef0123456789abcdef\n")
	require.NoError(t, err)
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	require.Error(t, err)
}

func TestDataPortRejectsUnauthenticatedAndWrongTokenConnections(t *testing.T) {
	for _, tc := range []struct {
		name string
		send bool
	}{
		{name: "no header"},
		{name: "wrong token", send: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := &Stats{}
			p := NewProxyServer(ProxyOpts{Stats: stats})
			p.dataToken = "target"
			client, peer := net.Pipe()
			done := make(chan error, 1)
			go func() { done <- p.handleConn(context.Background(), client) }()
			if tc.send {
				_, err := io.WriteString(peer, dataAuthPrefix+"wrong!\n")
				require.NoError(t, err)
			} else {
				_ = peer.Close()
			}
			select {
			case err := <-done:
				if tc.send {
					require.NoError(t, err)
				}
			case <-time.After(time.Second):
				t.Fatal("unauthenticated connection was not closed")
			}
			require.EqualValues(t, 0, stats.Snapshot().BackendDialAttempts)
			if tc.send {
				_ = peer.SetReadDeadline(time.Now().Add(time.Second))
				_, err := peer.Read(make([]byte, 1))
				require.Error(t, err)
				_ = peer.Close()
			}
		})
	}
}

func TestDataPortDoesNotCountUnauthenticatedConnectionsAsActive(t *testing.T) {
	p := NewProxyServer(ProxyOpts{Stats: &Stats{}})
	p.dataToken = "target"
	client, peer := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- p.handleConn(context.Background(), client) }()

	// While the connection sits in the handshake read it must not count as
	// active: an unauthenticated client that simply holds the port open
	// would otherwise gate the idle watcher forever.
	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 0, p.activeConns.Load())

	_ = peer.Close()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("unauthenticated connection was not closed after the peer hung up")
	}
	assert.EqualValues(t, 0, p.activeConns.Load())
}

func TestDialDataAuthenticatesRegisteredAddress(t *testing.T) {
	root := t.TempDir()
	secret, err := identity.WriteSecret(root)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	tcpAddr := listener.Addr().(*net.TCPAddr)
	endpoint := Endpoint{Host: "127.0.0.1", Port: tcpAddr.Port}
	require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{Pid: 1, Port: endpoint.Port}))
	registerDataAddress(root, endpoint)

	gotLine := make(chan string, 1)
	serverErr := make(chan error, 1)
	acceptOne := func() {
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				serverErr <- err
				return
			}
			defer conn.Close()
			line, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				serverErr <- err
				return
			}
			gotLine <- line
			serverErr <- nil
		}()
	}

	addr := net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
	acceptOne()
	conn, err := dialData(context.Background(), addr)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	assert.Equal(t, dataAuthPrefix+identity.DataAuthTokenForPort(secret, endpoint.Port)+"\n", <-gotLine)
	assert.NoError(t, <-serverErr)

	// The pidfile is consulted at registration, not on every pool dial: the
	// proxy removes proxy.pid first at shutdown and a pooled dial must not
	// race the removal into a bogus refusal, so a moved or missing pidfile
	// leaves the verified registration — and its port-bound token — in
	// effect.
	require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{Pid: 1, Port: endpoint.Port + 1}))
	acceptOne()
	conn, err = dialData(context.Background(), addr)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	assert.Equal(t, dataAuthPrefix+identity.DataAuthTokenForPort(secret, endpoint.Port)+"\n", <-gotLine)
	assert.NoError(t, <-serverErr)

	require.NoError(t, pidfile.Remove(root, PIDFileName))
	acceptOne()
	conn, err = dialData(context.Background(), addr)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	assert.Equal(t, dataAuthPrefix+identity.DataAuthTokenForPort(secret, endpoint.Port)+"\n", <-gotLine)
	assert.NoError(t, <-serverErr)

	_, err = dialData(context.Background(), "127.0.0.1:65535")
	require.ErrorContains(t, err, "unknown data address")
}

func TestControl_RejectsOversizedAndGarbageRequests(t *testing.T) {
	control, _, _ := newControlServer(t)
	cases := []struct {
		name    string
		request string
	}{
		{name: "oversized", request: "IDENT " + strings.Repeat("x", maxIdentRequestBytes) + "\n"},
		{name: "garbage", request: "WHOAMI\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(control.Port())), time.Second)
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
			_, err = io.WriteString(conn, tc.request)
			require.NoError(t, err)
			buf := make([]byte, 1)
			_, err = conn.Read(buf)
			assert.Error(t, err)
		})
	}
}

func TestControl_ConcurrentIdentify(t *testing.T) {
	control, secret, want := newControlServer(t)
	const calls = maxConcurrentIdentRequests
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			got, err := identity.Identify("127.0.0.1", control.Port(), secret, time.Second)
			if err == nil {
				got.MAC = ""
				if *got != want {
					err = errors.New("identity reply mismatch")
				}
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}

func TestControl_CapsConcurrentHandshakes(t *testing.T) {
	control, _, _ := newControlServer(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(control.Port()))
	conns := make([]net.Conn, 0, maxConcurrentIdentRequests)
	t.Cleanup(func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	for range maxConcurrentIdentRequests {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		require.NoError(t, err)
		conns = append(conns, conn)
	}
	require.Eventually(t, func() bool {
		return len(control.slots) == maxConcurrentIdentRequests
	}, time.Second, 10*time.Millisecond)

	extra, err := net.DialTimeout("tcp", addr, time.Second)
	require.NoError(t, err)
	defer extra.Close()
	require.NoError(t, extra.SetDeadline(time.Now().Add(time.Second)))
	_, err = io.WriteString(extra, "IDENT blocked blocked\n")
	require.NoError(t, err)
	buf := make([]byte, 1)
	_, err = extra.Read(buf)
	require.Error(t, err)
}

// Persistent transient Accept failures (e.g. EMFILE) must not tear down the
// control server: the data path may still be healthy, and losing control only
// degrades adoption to the caller's poll/retry path.
func TestControl_AcceptLoopSurvivesPersistentErrors(t *testing.T) {
	listener := &failingListener{err: errors.New("transient accept failure"), failures: 11}
	control := &controlServer{
		listener: listener,
		done:     make(chan struct{}),
		closing:  make(chan struct{}),
		slots:    make(chan struct{}, maxConcurrentIdentRequests),
	}
	immediately := make(chan time.Time)
	close(immediately)
	recorder := &acceptRetryRecorder{afterResult: immediately}
	go control.acceptLoopWithAfter(recorder.after)

	select {
	case <-control.done:
	case <-time.After(time.Second):
		t.Fatal("control accept loop did not exit after the listener closed")
	}
	assert.Equal(t, listener.failures+1, listener.accepts,
		"loop must retry through every transient failure until the listener reports closed")
	assert.ErrorIs(t, listener.finalErr, net.ErrClosed)
	assert.False(t, listener.closed, "accept failures must not close the control listener")
	assert.Equal(t, []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		80 * time.Millisecond,
		160 * time.Millisecond,
		320 * time.Millisecond,
		640 * time.Millisecond,
		1280 * time.Millisecond,
		2560 * time.Millisecond,
		5 * time.Second,
		5 * time.Second,
	}, recorder.delays)
}

type acceptRetryRecorder struct {
	afterResult <-chan time.Time
	delays      []time.Duration
}

func (r *acceptRetryRecorder) after(delay time.Duration) <-chan time.Time {
	r.delays = append(r.delays, delay)
	return r.afterResult
}

type failingListener struct {
	err      error
	failures int
	accepts  int
	finalErr error
	closed   bool
}

func (l *failingListener) Accept() (net.Conn, error) {
	l.accepts++
	if l.accepts <= l.failures {
		return nil, l.err
	}
	l.finalErr = net.ErrClosed
	return nil, l.finalErr
}

func (l *failingListener) Close() error {
	l.closed = true
	return nil
}

func (l *failingListener) Addr() net.Addr {
	return &net.TCPAddr{}
}
