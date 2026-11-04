package proxy

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log"
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
