package proxy

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// testDataSecret is a proxy secret for tests that drive the BDPROXY1
// exchange without a proxy.secret file.
const testDataSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// dataHello renders a client hello line.
func dataHello(token, nonceHex string) string {
	return dataAuthPrefix + token + " " + nonceHex + "\n"
}

func TestDataPortRejectsUnauthenticatedAndWrongTokenConnections(t *testing.T) {
	auth := newDataAuth(testDataSecret, 4242)
	nonce := strings.Repeat("ab", dataNonceBytes)
	for _, tc := range []struct {
		name  string
		hello string // empty: hang up without sending anything
	}{
		{name: "no header"},
		{name: "wrong token", hello: dataHello(identity.DataAuthTokenForPort("not the secret", 4242), nonce)},
		{name: "token for another port", hello: dataHello(identity.DataAuthTokenForPort(testDataSecret, 4243), nonce)},
		{name: "malformed nonce", hello: dataHello(auth.token, strings.Repeat("zz", dataNonceBytes))},
		{name: "unterminated", hello: strings.TrimSuffix(dataHello(auth.token, nonce), "\n") + " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := &Stats{}
			p := NewProxyServer(ProxyOpts{Stats: stats})
			p.data = auth
			client, peer := net.Pipe()
			done := make(chan error, 1)
			go func() { done <- p.handleConn(context.Background(), client) }()
			if tc.hello == "" {
				_ = peer.Close()
			} else {
				require.Len(t, tc.hello, dataHelloLen)
				_, err := io.WriteString(peer, tc.hello)
				require.NoError(t, err)
			}
			select {
			case err := <-done:
				if tc.hello != "" {
					// (A pipe already hung up fails even SetDeadline.)
					require.NoError(t, err)
				}
			case <-time.After(time.Second):
				t.Fatal("unauthenticated connection was not closed")
			}
			require.EqualValues(t, 0, stats.Snapshot().BackendDialAttempts)
			if tc.hello != "" {
				// Closed without an answer: a caller without the token
				// learns nothing the secret derives.
				_ = peer.SetReadDeadline(time.Now().Add(time.Second))
				n, err := peer.Read(make([]byte, 1))
				require.Error(t, err)
				require.Zero(t, n)
				_ = peer.Close()
			}
		})
	}
}

// TestDataPortAnswersTokenWithProof pins the proxy half of a good exchange:
// the hello carrying the token gets the proof for the client's own nonce,
// and only then does the proxy dial its backend.
func TestDataPortAnswersTokenWithProof(t *testing.T) {
	stats := &Stats{}
	p := NewProxyServer(ProxyOpts{Server: blockingBackend{}, Stats: stats})
	p.logger = log.Default()
	p.data = newDataAuth(testDataSecret, 4242)
	client, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the backend dial fails at once; reaching it is the point
	done := make(chan error, 1)
	go func() { done <- p.handleConn(ctx, client) }()

	nonce := []byte("0123456789abcdef")
	_, err := io.WriteString(peer, dataHello(identity.DataAuthTokenForPort(testDataSecret, 4242), hex.EncodeToString(nonce)))
	require.NoError(t, err)
	answer := make([]byte, dataProofLen)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = io.ReadFull(peer, answer)
	require.NoError(t, err)
	assert.Equal(t, dataAuthPrefix+identity.DataServerProof(testDataSecret, 4242, nonce)+"\n", string(answer))
	require.ErrorIs(t, <-done, context.Canceled)
	assert.EqualValues(t, 1, stats.Snapshot().BackendDialAttempts)
}

func TestDataPortDoesNotCountUnauthenticatedConnectionsAsActive(t *testing.T) {
	p := NewProxyServer(ProxyOpts{Stats: &Stats{}})
	p.data = newDataAuth(testDataSecret, 4242)
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

// TestDialDataRequiresTheProxysProof pins the client half: dialData sends
// its hello to the registered address and hands the connection to the
// driver only when the answer proves the proxy secret. The registration was
// verified, but the process listening there now may not be the proxy (it
// can exit, and another local user can bind the freed port): such a
// listener gets the hello and nothing more, whether it stays silent, hangs
// up, answers garbage or a MySQL greeting, or replays an answer from an
// earlier connection. proxy.pid is never consulted (the test writes none):
// the proxy removes it first at shutdown, and a pooled dial must not race
// that into a refusal.
func TestDialDataRequiresTheProxysProof(t *testing.T) {
	root := t.TempDir()
	secret, err := identity.WriteSecret(root)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	endpoint := Endpoint{Host: "127.0.0.1", Port: port}
	registerDataAddress(root, endpoint)
	addr := endpoint.Address()

	// serveOne accepts one connection, lets answer run the listener's side,
	// and reports everything the client sent until it hung up.
	serveOne := func(answer func(net.Conn) error) <-chan []byte {
		received := make(chan []byte, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				received <- nil
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			rec := &recordingConn{Conn: conn}
			if err := answer(rec); err != nil {
				received <- nil
				return
			}
			_, _ = io.Copy(io.Discard, rec)
			received <- rec.read
		}()
		return received
	}
	readHello := func(conn net.Conn) error {
		_, err := io.ReadFull(conn, make([]byte, dataHelloLen))
		return err
	}
	answerWith := func(line string) func(net.Conn) error {
		return func(conn net.Conn) error {
			if err := readHello(conn); err != nil {
				return err
			}
			_, err := io.WriteString(conn, line)
			return err
		}
	}

	t.Run("proxy", func(t *testing.T) {
		received := serveOne(func(conn net.Conn) error {
			ok, err := newDataAuth(secret, port).accept(conn)
			if !ok {
				return errors.New("proxy refused the hello")
			}
			return err
		})
		conn, err := dialData(context.Background(), addr)
		require.NoError(t, err)
		_, err = io.WriteString(conn, "SELECT 1")
		require.NoError(t, err)
		require.NoError(t, conn.Close())
		got := <-received
		require.Len(t, got, dataHelloLen+len("SELECT 1"))
		assert.Equal(t, dataAuthPrefix+identity.DataAuthTokenForPort(secret, port)+" ", string(got[:dataHelloLen-2*dataNonceBytes-1]))
		assert.Equal(t, "SELECT 1", string(got[dataHelloLen:]))
	})

	greeting := "J\x00\x00\x00\x0a8.0.33\x00" + strings.Repeat("\x01", dataProofLen)
	for _, tc := range []struct {
		name   string
		answer func(net.Conn) error
	}{
		{name: "hangs up", answer: func(conn net.Conn) error {
			if err := readHello(conn); err != nil {
				return err
			}
			return conn.Close()
		}},
		{name: "answers garbage", answer: answerWith(dataAuthPrefix + strings.Repeat("a", dataTokenHexLen) + "\n")},
		{name: "replays an answer for another nonce", answer: answerWith(dataAuthPrefix + identity.DataServerProof(secret, port, []byte("0123456789abcdef")) + "\n")},
		{name: "is a proxy under another secret", answer: func(conn net.Conn) error {
			ok, err := newDataAuth(testDataSecret, port).accept(conn)
			if ok {
				return errors.New("a proxy without this secret accepted the hello")
			}
			if err != nil {
				return err
			}
			return conn.Close() // as handleConn does on a refused hello
		}},
		// (Reading the hello first keeps the record whole: the client
		// resets the connection when it hangs up on unread greeting bytes.)
		{name: "speaks MySQL", answer: answerWith(greeting)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := serveOne(tc.answer)
			conn, err := dialData(context.Background(), addr)
			require.Error(t, err)
			assert.Nil(t, conn)
			got := <-received
			assert.Len(t, got, dataHelloLen, "the listener received more than the hello: %q", got)
		})
	}

	t.Run("cancel unblocks a silent listener", func(t *testing.T) {
		release := make(chan struct{})
		received := serveOne(func(conn net.Conn) error {
			<-release
			return nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		start := time.Now()
		_, err := dialData(ctx, addr)
		require.ErrorIs(t, err, context.Canceled)
		assert.Less(t, time.Since(start), dataAuthTimeout)
		close(release)
		<-received
	})

	_, err = dialData(context.Background(), "127.0.0.1:65535")
	require.ErrorContains(t, err, "unknown data address")
}

// recordingConn records what is read through it.
type recordingConn struct {
	net.Conn
	read []byte
}

func (c *recordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read = append(c.read, p[:n]...)
	return n, err
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

// bd-g4xtvt (GH#5697): the control listener is loopback TCP, reachable by any
// local uid. A full pool of idle connections that never send a request must
// not lock a legitimate secret holder out of the identity handshake.
func TestControl_IdleConnectionsDoNotStarveIdentify(t *testing.T) {
	control, secret, want := newControlServer(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(control.Port()))
	conns := make([]net.Conn, 0, maxConcurrentIdentRequests)
	t.Cleanup(func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	firstDial := time.Now()
	for range maxConcurrentIdentRequests {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		require.NoError(t, err)
		conns = append(conns, conn)
	}
	require.Eventually(t, func() bool {
		return activeControlConns(control) == maxConcurrentIdentRequests
	}, time.Second, time.Millisecond)

	// The idle holders keep their connections for identDeadline; the
	// caller's budget is shorter, so it must be served without waiting
	// them out.
	got, err := identity.Identify("127.0.0.1", control.Port(), secret, identityProbeTimeout)
	require.NoError(t, err, "idle unauthenticated connections exhausted the control listener")
	require.Less(t, time.Since(firstDial), identDeadline,
		"an idle holder may have timed out before the probe, so the pool was not full")
	got.MAC = ""
	assert.Equal(t, &want, got)
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

// activeControlConns reports how many connections control holds in its pool.
func activeControlConns(control *controlServer) int {
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.active.Len()
}

// AC-2 (bd-g4xtvt): a full pool admits the newcomer by closing its oldest
// connection; the pool never holds more than maxConcurrentIdentRequests.
func TestControl_FullPoolEvictsOldest(t *testing.T) {
	control, listener, _ := startSyntheticControl(t, time.After)
	firstAdmitted := time.Now()
	peers := make([]*testControlConn, 0, maxConcurrentIdentRequests+3)
	for range maxConcurrentIdentRequests {
		conn, _ := newTestControlConn(t)
		feedControlConn(t, listener, conn)
		peers = append(peers, conn)
		require.LessOrEqual(t, activeControlConns(control), maxConcurrentIdentRequests)
	}
	require.Eventually(t, func() bool {
		return activeControlConns(control) == maxConcurrentIdentRequests
	}, time.Second, time.Millisecond)

	for evicted := range 3 {
		newcomer, _ := newTestControlConn(t)
		feedControlConn(t, listener, newcomer)
		peers = append(peers, newcomer)
		requireClosed(t, peers[evicted], "the oldest connection was not evicted")
		require.Less(t, time.Since(firstAdmitted), identDeadline,
			"the oldest connection may have timed out rather than been evicted")
		assert.Equal(t, maxConcurrentIdentRequests, activeControlConns(control))
		for _, kept := range []*testControlConn{peers[evicted+1], peers[len(peers)-2], newcomer} {
			assert.False(t, kept.isClosed(), "a connection newer than the oldest was closed")
		}
	}
}

// chanListener hands the control accept loop the connections and errors a
// test feeds it, in order.
type chanListener struct {
	conns  chan net.Conn
	errs   chan error
	closed chan struct{}
	once   sync.Once
}

func newChanListener() *chanListener {
	return &chanListener{conns: make(chan net.Conn), errs: make(chan error), closed: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case err := <-l.errs:
		return nil, err
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
}

// startSyntheticControl runs a control server whose accept loop serves the
// connections fed to the returned listener. It returns the control secret.
func startSyntheticControl(t *testing.T, after func(time.Duration) <-chan time.Time) (*controlServer, *chanListener, string) {
	t.Helper()
	listener := newChanListener()
	secret := testDataSecret
	control := &controlServer{
		listener: listener,
		secret:   secret,
		done:     make(chan struct{}),
		closing:  make(chan struct{}),
		reply: func() identity.IdentReply {
			return identity.IdentReply{Schema: pidfile.SchemaV2, Role: pidfile.KindProxy, RootID: "workspace-root"}
		},
	}
	go control.acceptLoopWithAfter(after)
	t.Cleanup(func() { _ = control.Close() })
	return control, listener, secret
}

// feedControlConn hands conn to the accept loop.
func feedControlConn(t *testing.T, listener *chanListener, conn net.Conn) {
	t.Helper()
	select {
	case listener.conns <- conn:
	case <-time.After(time.Second):
		t.Fatal("control accept loop did not accept the connection")
	}
}

// testControlConn is the proxy side of a net.Pipe. It records closes, can
// fail SetDeadline or shorten the deadline the handler sets, and reports
// when a reply write starts and returns.
type testControlConn struct {
	net.Conn
	deadlineErr error
	deadline    time.Duration // nonzero: replaces the handler's deadline

	closes     atomic.Int32
	closed     chan struct{}
	closeOnce  sync.Once
	writing    chan struct{}
	written    chan struct{}
	writeStart sync.Once
	writeEnd   sync.Once
}

// newTestControlConn returns the proxy side of a pipe and the client side,
// which the test closes at cleanup.
func newTestControlConn(t *testing.T) (*testControlConn, net.Conn) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	return &testControlConn{
		Conn:    server,
		closed:  make(chan struct{}),
		writing: make(chan struct{}),
		written: make(chan struct{}),
	}, client
}

func (c *testControlConn) SetDeadline(deadline time.Time) error {
	if c.deadlineErr != nil {
		return c.deadlineErr
	}
	if c.deadline != 0 {
		deadline = time.Now().Add(c.deadline)
	}
	return c.Conn.SetDeadline(deadline)
}

func (c *testControlConn) Write(p []byte) (int, error) {
	c.writeStart.Do(func() { close(c.writing) })
	defer c.writeEnd.Do(func() { close(c.written) })
	return c.Conn.Write(p)
}

func (c *testControlConn) Close() error {
	c.closes.Add(1)
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *testControlConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func requireClosed(t *testing.T, conn *testControlConn, msg string) {
	t.Helper()
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal(msg)
	}
}

// AC-3 (bd-g4xtvt): however a handler ends, its connection is closed and
// leaves the pool exactly once, without taking another connection's place.
func TestControl_ConnectionCleanup(t *testing.T) {
	nonce := strings.Repeat("ab", 16)
	writeAsync := func(client net.Conn, request string) {
		go func() { _, _ = io.WriteString(client, request) }()
	}
	for _, tc := range []struct {
		name   string
		setup  func(conn *testControlConn)
		client func(t *testing.T, client net.Conn, secret string)
	}{
		{name: "success", client: func(t *testing.T, client net.Conn, secret string) {
			writeAsync(client, identRequest(t, secret, nonce))
			_, err := bufio.NewReader(client).ReadString('\n')
			require.NoError(t, err)
		}},
		{name: "missing newline", client: func(t *testing.T, client net.Conn, secret string) {
			_, err := io.WriteString(client, "IDENT "+nonce)
			require.NoError(t, err)
			_ = client.Close()
		}},
		{name: "oversize", client: func(t *testing.T, client net.Conn, secret string) {
			writeAsync(client, "IDENT "+strings.Repeat("x", maxIdentRequestBytes)+"\n")
		}},
		{name: "wrong verb", client: func(t *testing.T, client net.Conn, secret string) {
			writeAsync(client, "WHOAMI "+strings.Repeat("0", 64)+" "+nonce+"\n")
		}},
		{name: "invalid MAC", client: func(t *testing.T, client net.Conn, secret string) {
			writeAsync(client, identRequest(t, "not-the-secret", nonce))
		}},
		{name: "peer close", client: func(t *testing.T, client net.Conn, secret string) {
			_ = client.Close()
		}},
		{name: "deadline setup fails", setup: func(conn *testControlConn) {
			conn.deadlineErr = errors.New("set deadline failed")
		}},
		{name: "deadline expiry", setup: func(conn *testControlConn) {
			conn.deadline = 20 * time.Millisecond
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			control, listener, secret := startSyntheticControl(t, time.After)
			conn, client := newTestControlConn(t)
			if tc.setup != nil {
				tc.setup(conn)
			}
			feedControlConn(t, listener, conn)
			if tc.client != nil {
				tc.client(t, client, secret)
			}
			requireClosed(t, conn, "the handler did not close its connection")
			require.Eventually(t, func() bool {
				return activeControlConns(control) == 0
			}, time.Second, time.Millisecond)
		})
	}

	t.Run("tracked through a blocked reply write", func(t *testing.T) {
		control, listener, secret := startSyntheticControl(t, time.After)
		conn, client := newTestControlConn(t)
		feedControlConn(t, listener, conn)
		writeAsync(client, identRequest(t, secret, nonce))
		select {
		case <-conn.writing:
		case <-time.After(time.Second):
			t.Fatal("the handler did not start its reply")
		}
		// The client has not read the reply, so the write is still blocked.
		assert.Equal(t, 1, activeControlConns(control), "a handler writing its reply left the pool early")
		assert.False(t, conn.isClosed())

		_, err := bufio.NewReader(client).ReadString('\n')
		require.NoError(t, err)
		requireClosed(t, conn, "the handler did not close its connection")
		require.Eventually(t, func() bool {
			return activeControlConns(control) == 0
		}, time.Second, time.Millisecond)
	})

	t.Run("an evicted handler leaves its replacement in place", func(t *testing.T) {
		control, listener, secret := startSyntheticControl(t, time.After)
		victim, victimClient := newTestControlConn(t)
		feedControlConn(t, listener, victim)
		writeAsync(victimClient, identRequest(t, secret, nonce))
		select {
		case <-victim.writing:
		case <-time.After(time.Second):
			t.Fatal("the handler did not start its reply")
		}
		for range maxConcurrentIdentRequests - 1 {
			conn, _ := newTestControlConn(t)
			feedControlConn(t, listener, conn)
		}
		newcomer, _ := newTestControlConn(t)
		feedControlConn(t, listener, newcomer)

		requireClosed(t, victim, "the oldest connection was not evicted")
		select {
		case <-victim.written:
		case <-time.After(time.Second):
			t.Fatal("eviction did not unblock the victim's reply write")
		}
		// The second close is the victim handler's own cleanup.
		require.Eventually(t, func() bool {
			return victim.closes.Load() == 2
		}, time.Second, time.Millisecond)
		assert.Equal(t, maxConcurrentIdentRequests, activeControlConns(control))
		assert.False(t, newcomer.isClosed())
	})
}

// identRequest renders an IDENT request for nonce, authenticated by secret.
func identRequest(t *testing.T, secret, nonce string) string {
	t.Helper()
	mac, err := identity.IdentRequestMAC(secret, nonce)
	require.NoError(t, err)
	return "IDENT " + mac + " " + nonce + "\n"
}

// AC-5 (bd-g4xtvt): below capacity the listener answers as before: a valid
// client gets the signed reply without the secret crossing the wire, and a
// malformed or unauthenticated request gets no reply bytes.
func TestControl_AnswersOnlyAuthenticatedRequests(t *testing.T) {
	control, secret, want := newControlServer(t)
	relayPort, sent := recordingRelay(t, control.Port())
	got, err := identity.Identify("127.0.0.1", relayPort, secret, identityProbeTimeout)
	require.NoError(t, err)
	assert.Len(t, got.MAC, 64)
	got.MAC = ""
	assert.Equal(t, &want, got)
	wire := sent()
	require.True(t, strings.HasPrefix(wire, "IDENT "), "relay recorded %q", wire)
	assert.NotContains(t, wire, secret, "the request disclosed the secret")

	nonce := strings.Repeat("ab", 16)
	mac := strings.Fields(identRequest(t, secret, nonce))[1]
	for _, tc := range []struct {
		name    string
		request string
	}{
		{name: "wrong secret", request: identRequest(t, "not-the-secret", nonce)},
		{name: "raw secret", request: "IDENT " + secret + " " + nonce + "\n"},
		{name: "malformed nonce", request: "IDENT " + mac + " zz\n"},
		{name: "missing newline", request: "IDENT " + mac + " " + nonce},
		{name: "wrong verb", request: "WHOAMI " + mac + " " + nonce + "\n"},
		{name: "oversized", request: "IDENT " + strings.Repeat("x", maxIdentRequestBytes) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(control.Port())), time.Second)
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
			_, err = io.WriteString(conn, tc.request)
			require.NoError(t, err)
			require.NoError(t, conn.(*net.TCPConn).CloseWrite())
			reply, err := io.ReadAll(conn)
			var netErr net.Error
			require.False(t, errors.As(err, &netErr) && netErr.Timeout(), "the listener kept the connection open")
			assert.Empty(t, reply)
		})
	}
}

// recordingRelay forwards one connection to the control port and returns
// its own port and a func reporting what the client sent once it is done.
func recordingRelay(t *testing.T, controlPort int) (int, func() string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var sent strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		client, err := ln.Accept()
		if err != nil {
			return
		}
		defer client.Close()
		upstream, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)))
		if err != nil {
			return
		}
		defer upstream.Close()
		go func() { _, _ = io.Copy(client, upstream) }()
		_, _ = io.Copy(upstream, io.TeeReader(client, &sent))
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() string {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("relay did not finish")
		}
		return sent.String()
	}
}
