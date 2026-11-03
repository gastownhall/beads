package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
)

// listenerMode is how a resolverTestListener answers a connection.
type listenerMode int

const (
	// modeProve answers the hello with the proof under the root's secret as
	// it is on disk when the connection arrives, then keeps the connection.
	modeProve listenerMode = iota
	// modeWrongProof answers with a well-formed line that proves nothing.
	modeWrongProof
	// modeClose reads the hello and closes.
	modeClose
	// modeSilent reads the hello and never answers.
	modeSilent
)

// resolverTestListener is a loopback data port. It counts the connections it
// accepted and every byte a client sent after its hello, which must stay zero
// for a listener that did not prove the secret.
type resolverTestListener struct {
	ln        net.Listener
	rootDir   string
	mode      listenerMode
	accepted  atomic.Int32
	afterLine atomic.Int64
	mu        sync.Mutex
	conns     []net.Conn
}

func newResolverTestListener(t *testing.T, rootDir string, mode listenerMode) *resolverTestListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &resolverTestListener{ln: ln, rootDir: rootDir, mode: mode}
	t.Cleanup(l.close)
	go l.serve()
	return l
}

func (l *resolverTestListener) endpoint() Endpoint {
	return Endpoint{Host: "127.0.0.1", Port: l.ln.Addr().(*net.TCPAddr).Port}
}

func (l *resolverTestListener) close() {
	_ = l.ln.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		_ = c.Close()
	}
}

func (l *resolverTestListener) serve() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.accepted.Add(1)
		l.mu.Lock()
		l.conns = append(l.conns, conn)
		l.mu.Unlock()
		go l.handle(conn)
	}
}

func (l *resolverTestListener) handle(conn net.Conn) {
	port := l.endpoint().Port
	switch l.mode {
	case modeProve:
		secret, err := identity.ReadSecret(l.rootDir)
		if err != nil {
			_ = conn.Close()
			return
		}
		if ok, _ := newDataAuth(secret, port).accept(conn); !ok {
			_ = conn.Close()
			return
		}
	default:
		hello := make([]byte, dataHelloLen)
		if _, err := io.ReadFull(conn, hello); err != nil {
			_ = conn.Close()
			return
		}
		switch l.mode {
		case modeWrongProof:
			_, _ = io.WriteString(conn, dataAuthPrefix+strings.Repeat("0", dataTokenHexLen)+"\n")
		case modeClose:
			_ = conn.Close()
			return
		}
	}
	n, _ := io.Copy(io.Discard, conn)
	l.afterLine.Add(n)
}

// refusedEndpoint is a loopback port nothing listens on.
func refusedEndpoint(t *testing.T) Endpoint {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ep := Endpoint{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
	_ = ln.Close()
	return ep
}

// fakeDiscovery stands in for GetCreateDatabaseProxyServerEndpoint. Each call
// is counted; when gate is set the call blocks until gate closes, and then
// returns result() (or err).
type fakeDiscovery struct {
	calls   atomic.Int32
	entered chan struct{}
	gate    chan struct{}
	mu      sync.Mutex
	ep      Endpoint
	err     error
}

func newFakeDiscovery() *fakeDiscovery {
	return &fakeDiscovery{entered: make(chan struct{}, 16)}
}

func (d *fakeDiscovery) set(ep Endpoint, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ep, d.err = ep, err
}

func (d *fakeDiscovery) discover(string, OpenOpts) (Endpoint, error) {
	d.calls.Add(1)
	d.entered <- struct{}{}
	if d.gate != nil {
		<-d.gate
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ep, d.err
}

// fakeClock is the resolver's clock, advanced by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestResolver returns a resolver for a fresh root with a secret, dialing
// ep, whose discovery is d and whose clock is the returned fakeClock.
func newTestResolver(t *testing.T, ep Endpoint, d *fakeDiscovery) (*EndpointResolver, string, *fakeClock) {
	t.Helper()
	rootDir := t.TempDir()
	if _, err := identity.WriteSecret(rootDir); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	r := NewEndpointResolver(rootDir, OpenOpts{}, ep)
	r.discover = d.discover
	r.now = clock.Now
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r, rootDir, clock
}

// waitEntered waits for a discovery call to begin.
func waitEntered(t *testing.T, d *fakeDiscovery) {
	t.Helper()
	select {
	case <-d.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}
}

func dialWithin(r *EndpointResolver, d time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return r.DialContext(ctx, DataNetwork, "ignored:0")
}

// TestEndpointResolver_RefusedDialRediscoversAndRedials is the recovery the
// GH#7295 fix rests on: an armed resolver whose proxy has gone away runs
// discovery once and redials what it returns, and later dials use the new
// endpoint without discovering again.
func TestEndpointResolver_RefusedDialRediscoversAndRedials(t *testing.T) {
	d := newFakeDiscovery()
	r, rootDir, _ := newTestResolver(t, refusedEndpoint(t), d)
	replacement := newResolverTestListener(t, rootDir, modeProve)
	d.set(replacement.endpoint(), nil)
	r.Arm()

	conn, err := dialWithin(r, 5*time.Second)
	if err != nil {
		t.Fatalf("dial after the proxy went away: %v, want a connection to its replacement", err)
	}
	_ = conn.Close()
	conn, err = dialWithin(r, 5*time.Second)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	_ = conn.Close()
	if got := d.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want 1", got)
	}
	if got := r.Endpoint(); got != replacement.endpoint() {
		t.Fatalf("endpoint = %v, want the replacement %v", got, replacement.endpoint())
	}
}

// TestEndpointResolver_UnprovenListenerIsNeverHandedToTheDriver is the
// recycled-port edge: another process now listening on the proxy's old port
// gets no session data, whatever it answers, and the failure is the one that
// sends the resolver looking for the real proxy.
func TestEndpointResolver_UnprovenListenerIsNeverHandedToTheDriver(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode listenerMode
		want error
	}{
		{"wrong proof", modeWrongProof, errDataAuth},
		{"closes after the hello", modeClose, io.EOF},
		{"silent", modeSilent, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeDiscovery()
			discoveryErr := errors.New("no proxy here")
			d.set(Endpoint{}, discoveryErr)
			r, rootDir, _ := newTestResolver(t, Endpoint{}, d)
			impostor := newResolverTestListener(t, rootDir, tc.mode)
			r.endpoint = impostor.endpoint()
			r.Arm()

			conn, err := dialWithin(r, 300*time.Millisecond)
			if conn != nil {
				_ = conn.Close()
				t.Fatal("dial returned a connection to a listener that did not prove the secret")
			}
			if !IsEndpointUnavailable(err) {
				t.Fatalf("err = %v, want ErrEndpointUnavailable", err)
			}
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want it to wrap %v", err, tc.want)
				}
				// The dial failed with budget to spare, so it waited for the
				// flight.
				if !errors.Is(err, discoveryErr) {
					t.Fatalf("err = %v, want it to carry the discovery failure", err)
				}
			}
			waitEntered(t, d)
			if got := d.calls.Load(); got != 1 {
				t.Fatalf("discovery calls = %d, want 1", got)
			}
			impostor.close()
			time.Sleep(20 * time.Millisecond)
			if got := impostor.afterLine.Load(); got != 0 {
				t.Fatalf("impostor received %d bytes after the hello, want none", got)
			}
		})
	}
}

// TestEndpointResolver_SamePortRestartAdvancesGeneration: a proxy restarted
// on the same (fixed) port is still a new incarnation. Discovery publishes it
// and advances the generation, so a dial that failed against the old
// incarnation redials instead of starting another flight.
func TestEndpointResolver_SamePortRestartAdvancesGeneration(t *testing.T) {
	d := newFakeDiscovery()
	r, rootDir, _ := newTestResolver(t, Endpoint{}, d)
	l := newResolverTestListener(t, rootDir, modeProve)
	r.endpoint = l.endpoint()
	d.set(l.endpoint(), nil)
	r.Arm()

	r.mu.Lock()
	staleGeneration := r.generation
	r.mu.Unlock()
	conn, err := r.recoverDial(context.Background(), staleGeneration, syscall.ECONNREFUSED)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got := r.generation; got != staleGeneration+1 {
		t.Fatalf("generation = %d, want %d after a same-port discovery", got, staleGeneration+1)
	}
	// A late failure from the old incarnation redials the published endpoint
	// without discovering again.
	conn, err = r.recoverDial(context.Background(), staleGeneration, syscall.ECONNREFUSED)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got := d.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want 1: a late old-generation failure started another flight", got)
	}
}

// TestEndpointResolver_DiscoveryCooldown: a failed discovery is not latched,
// but the next one waits a second; a dial in between fails with this dial's
// error and the previous discovery's.
func TestEndpointResolver_DiscoveryCooldown(t *testing.T) {
	d := newFakeDiscovery()
	r, rootDir, clock := newTestResolver(t, refusedEndpoint(t), d)
	discoveryErr := errors.New("proxy startup failed")
	d.set(Endpoint{}, discoveryErr)
	r.Arm()

	if _, err := dialWithin(r, time.Second); !errors.Is(err, discoveryErr) {
		t.Fatalf("first dial: %v, want the discovery failure", err)
	}
	_, err := dialWithin(r, time.Second)
	if !IsEndpointUnavailable(err) || !errors.Is(err, errDiscoveryCooldown) || !errors.Is(err, discoveryErr) || !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("dial within the cooldown: %v, want unavailable carrying the refusal, the cooldown and the prior discovery error", err)
	}
	if got := d.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want 1 within the cooldown", got)
	}

	clock.advance(discoveryCooldown)
	l := newResolverTestListener(t, rootDir, modeProve)
	d.set(l.endpoint(), nil)
	conn, err := dialWithin(r, time.Second)
	if err != nil {
		t.Fatalf("dial after the cooldown: %v", err)
	}
	_ = conn.Close()
	if got := d.calls.Load(); got != 2 {
		t.Fatalf("discovery calls = %d, want 2", got)
	}
}

// TestEndpointResolver_UnarmedDialNeverRediscovers: until Arm, a failed dial
// is returned as it is — the opener's construction keeps its own retries and
// errors — and discovery never runs. Arm is what enables recovery.
func TestEndpointResolver_UnarmedDialNeverRediscovers(t *testing.T) {
	d := newFakeDiscovery()
	r, rootDir, _ := newTestResolver(t, refusedEndpoint(t), d)
	replacement := newResolverTestListener(t, rootDir, modeProve)
	d.set(replacement.endpoint(), nil)

	_, err := dialWithin(r, time.Second)
	if !errors.Is(err, syscall.ECONNREFUSED) || IsEndpointUnavailable(err) {
		t.Fatalf("unarmed dial: %v, want the bare refusal", err)
	}
	if got := d.calls.Load(); got != 0 {
		t.Fatalf("discovery calls = %d before Arm, want 0", got)
	}

	r.Arm()
	conn, err := dialWithin(r, 5*time.Second)
	if err != nil {
		t.Fatalf("armed dial: %v, want the replacement", err)
	}
	_ = conn.Close()
	if got := d.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d after Arm, want 1", got)
	}
}
