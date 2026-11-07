package proxy

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

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
	// modeReset reads the hello and resets the connection.
	modeReset
	// modeSilent reads the hello and never answers.
	modeSilent
	// modeProveThenDenyAccess proves the secret, then answers as a MySQL
	// server refusing the login (ERR 1045) and closes.
	modeProveThenDenyAccess
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
	case modeProve, modeProveThenDenyAccess:
		secret, err := identity.ReadSecret(l.rootDir)
		if err != nil {
			_ = conn.Close()
			return
		}
		if ok, _ := newDataAuth(secret, port).accept(conn); !ok {
			_ = conn.Close()
			return
		}
		if l.mode == modeProveThenDenyAccess {
			payload := append([]byte{0xff, 0x15, 0x04, '#'}, "28000Access denied for user 'root'"...)
			packet := append([]byte{byte(len(payload)), 0, 0, 0}, payload...)
			_, _ = conn.Write(packet)
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
		case modeReset:
			// Linger 0 makes the close a reset instead of an orderly FIN.
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0)
			}
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
	conn, err := r.recoverDial(context.Background(), staleGeneration, connRefused)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got := r.generation; got != staleGeneration+1 {
		t.Fatalf("generation = %d, want %d after a same-port discovery", got, staleGeneration+1)
	}
	// A late failure from the old incarnation redials the published endpoint
	// without discovering again.
	conn, err = r.recoverDial(context.Background(), staleGeneration, connRefused)
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
	if !IsEndpointUnavailable(err) || !errors.Is(err, errDiscoveryCooldown) || !errors.Is(err, discoveryErr) || !errors.Is(err, connRefused) {
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
	if !errors.Is(err, connRefused) || IsEndpointUnavailable(err) {
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

// TestEndpointResolver_SharedFlightWaitersReturnByDeadline: concurrent dials
// that fail at one generation share one discovery flight, and while it is
// blocked each returns by its own deadline with its refusal as the cause. The
// flight carries on, and once it publishes the next dial uses its endpoint
// without discovering again.
func TestEndpointResolver_SharedFlightWaitersReturnByDeadline(t *testing.T) {
	d := newFakeDiscovery()
	d.gate = make(chan struct{})
	r, rootDir, _ := newTestResolver(t, refusedEndpoint(t), d)
	released := false
	release := func() {
		if !released {
			released = true
			close(d.gate)
		}
	}
	t.Cleanup(release)
	replacement := newResolverTestListener(t, rootDir, modeProve)
	d.set(replacement.endpoint(), nil)
	r.Arm()

	const waiters = 8
	deadlines := []time.Duration{100 * time.Millisecond, 250 * time.Millisecond, 400 * time.Millisecond}
	var wg sync.WaitGroup
	errs := make([]error, waiters)
	took := make([]time.Duration, waiters)
	for i := range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			conn, err := dialWithin(r, deadlines[i%len(deadlines)])
			took[i] = time.Since(start)
			if conn != nil {
				_ = conn.Close()
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		deadline := deadlines[i%len(deadlines)]
		if !IsEndpointUnavailable(err) || !errors.Is(err, errDiscoveryPending) || !errors.Is(err, connRefused) {
			t.Errorf("waiter %d: %v, want unavailable with discovery pending and the refusal as its cause", i, err)
		}
		if took[i] > deadline+200*time.Millisecond {
			t.Errorf("waiter %d took %v, past its %v deadline", i, took[i], deadline)
		}
	}
	if got := d.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want one shared flight", got)
	}

	release()
	deadline := time.Now().Add(5 * time.Second)
	for r.Endpoint() != replacement.endpoint() {
		if time.Now().After(deadline) {
			t.Fatal("the flight never published its endpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := dialWithin(r, time.Second)
	if err != nil {
		t.Fatalf("dial after the flight published: %v", err)
	}
	_ = conn.Close()
	if got := d.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d after publication, want 1", got)
	}
}

// TestEndpointResolver_CloseJoinsFlightAndPreventsAnother: Close waits for a
// running flight within its context, reports the flight when the context ends
// first, and once it returns nil the flight is over, published nothing, and
// no later dial can start another.
func TestEndpointResolver_CloseJoinsFlightAndPreventsAnother(t *testing.T) {
	d := newFakeDiscovery()
	d.gate = make(chan struct{})
	original := refusedEndpoint(t)
	r, rootDir, clock := newTestResolver(t, original, d)
	released := false
	release := func() {
		if !released {
			released = true
			close(d.gate)
		}
	}
	t.Cleanup(release)
	replacement := newResolverTestListener(t, rootDir, modeProve)
	d.set(replacement.endpoint(), nil)
	r.Arm()

	go func() { _, _ = dialWithin(r, 50*time.Millisecond) }()
	waitEntered(t, d)

	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close with the flight blocked: %v, want its context's deadline", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- r.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned %v while the flight was still running", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close after the flight finished: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return once the flight finished")
	}
	if got := r.Endpoint(); got != original {
		t.Fatalf("endpoint = %v after Close, want the flight to publish nothing", got)
	}

	clock.advance(discoveryCooldown)
	_, err := dialWithin(r, time.Second)
	if !IsEndpointUnavailable(err) || !errors.Is(err, errResolverClosed) {
		t.Fatalf("dial after Close: %v, want unavailable because the resolver is closed", err)
	}
	if got := d.calls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want no flight after Close", got)
	}
}

// TestEndpointResolver_CloseWithoutFlightReturnsAtOnce: with no discovery
// flight running, Close has nothing to wait for and succeeds even with a
// context that has already ended, both on a resolver that never discovered
// and on one whose flight has finished. A done context reports a flight only
// while one is running.
func TestEndpointResolver_CloseWithoutFlightReturnsAtOnce(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	fresh, _, _ := newTestResolver(t, refusedEndpoint(t), newFakeDiscovery())
	if err := fresh.Close(canceled); err != nil {
		t.Fatalf("Close of a resolver that never discovered, with a canceled context: %v, want nil", err)
	}

	d := newFakeDiscovery()
	discoveryErr := errors.New("no proxy here")
	d.set(Endpoint{}, discoveryErr)
	r, _, _ := newTestResolver(t, refusedEndpoint(t), d)
	r.Arm()
	// The dial waits for its flight, so the flight is over once it returns.
	if _, err := dialWithin(r, 5*time.Second); !errors.Is(err, discoveryErr) {
		t.Fatalf("dial: %v, want the discovery failure", err)
	}
	if err := r.Close(canceled); err != nil {
		t.Fatalf("Close after the flight finished, with a canceled context: %v, want nil", err)
	}
}

// TestEndpointResolver_CanceledDialStaysUnmarked: a caller that cancels as
// recovery gives up gets its own context.Canceled, never the
// endpoint-unavailable marker that HTTP answers as a 503 db_unavailable. The
// fake dial refuses whatever its context, standing in for a cancellation that
// lands just after the connect was refused.
func TestEndpointResolver_CanceledDialStaysUnmarked(t *testing.T) {
	refused := func(context.Context, string, string, int) (net.Conn, dataDialStage, error) {
		return nil, dataDialConnect, connRefused
	}
	unmarked := func(err error) bool {
		return errors.Is(err, context.Canceled) && !IsEndpointUnavailable(err)
	}

	// The waiter sees the flight's end and the cancellation together, and
	// which it picks first is up to the scheduler, so repeat the dial: before
	// the fix nearly every one lost the cancellation.
	t.Run("as a failed flight completes", func(t *testing.T) {
		for i := range 100 {
			ctx, cancel := context.WithCancel(context.Background())
			r := NewEndpointResolver("unused", OpenOpts{}, Endpoint{})
			r.dial = refused
			r.discover = func(string, OpenOpts) (Endpoint, error) {
				cancel()
				return Endpoint{}, errors.New("proxy startup failed")
			}
			r.Arm()
			_, err := r.DialContext(ctx, DataNetwork, "")
			cancel()
			_ = r.Close(context.Background())
			if !unmarked(err) {
				t.Fatalf("dial %d: %v, want the bare cancellation", i, err)
			}
		}
	})

	t.Run("within the cooldown", func(t *testing.T) {
		d := newFakeDiscovery()
		discoveryErr := errors.New("proxy startup failed")
		d.set(Endpoint{}, discoveryErr)
		r, _, _ := newTestResolver(t, Endpoint{}, d)
		r.dial = refused
		r.Arm()
		if _, err := dialWithin(r, 5*time.Second); !IsEndpointUnavailable(err) || !errors.Is(err, discoveryErr) {
			t.Fatalf("first dial: %v, want unavailable with the discovery failure", err)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := r.DialContext(canceled, DataNetwork, ""); !unmarked(err) {
			t.Fatalf("canceled dial within the cooldown: %v, want the bare cancellation", err)
		}
		if got := d.calls.Load(); got != 1 {
			t.Fatalf("discovery calls = %d, want 1 within the cooldown", got)
		}
	})

	t.Run("on a closed resolver", func(t *testing.T) {
		d := newFakeDiscovery()
		r, _, _ := newTestResolver(t, Endpoint{}, d)
		r.dial = refused
		r.Arm()
		if err := r.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := r.DialContext(canceled, DataNetwork, ""); !unmarked(err) {
			t.Fatalf("canceled dial after Close: %v, want the bare cancellation", err)
		}
		if got := d.calls.Load(); got != 0 {
			t.Fatalf("discovery calls = %d, want 0 after Close", got)
		}
	})
}

// TestEndpointResolver_SocketFailuresAreRecoverable holds the classifier to
// the errors this platform's sockets really return, which differ by OS:
// Windows reports WSA errnos, which syscall.ECONNREFUSED and its siblings do
// not match. A connect to a port nothing listens on, and a listener that
// resets the connection instead of answering the hello, are failures
// rediscovery can fix.
func TestEndpointResolver_SocketFailuresAreRecoverable(t *testing.T) {
	rootDir := t.TempDir()
	if _, err := identity.WriteSecret(rootDir); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		endpoint func(t *testing.T) Endpoint
		stage    dataDialStage
		cause    error
	}{
		{"refused connect", refusedEndpoint, dataDialConnect, connRefused},
		{"reset during the proof", func(t *testing.T) Endpoint {
			return newResolverTestListener(t, rootDir, modeReset).endpoint()
		}, dataDialProof, connReset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := tc.endpoint(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, stage, err := dialAuthenticated(ctx, rootDir, ep.Address(), ep.Port)
			if conn != nil {
				_ = conn.Close()
				t.Fatal("dial succeeded")
			}
			if stage != tc.stage || !errors.Is(err, tc.cause) {
				t.Fatalf("stage %d, err %v: want stage %d failing with %v", stage, err, tc.stage, tc.cause)
			}
			if !recoverableDialFailure(stage, err) {
				t.Fatalf("%v at stage %d is not recoverable", err, stage)
			}
		})
	}
}

// TestEndpointResolver_IneligibleFailuresPassThrough: a failure that does not
// say the proxy has gone away reaches the caller as it is — no
// endpoint-unavailable marker — and runs no discovery, even on an armed
// resolver.
func TestEndpointResolver_IneligibleFailuresPassThrough(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, r *EndpointResolver, rootDir string) (context.Context, func(error) bool)
	}{
		{"secret cannot be read", func(t *testing.T, r *EndpointResolver, rootDir string) (context.Context, func(error) bool) {
			r.endpoint = newResolverTestListener(t, rootDir, modeProve).endpoint()
			if err := os.Remove(filepath.Join(rootDir, identity.SecretFileName)); err != nil {
				t.Fatal(err)
			}
			return context.Background(), func(err error) bool {
				return errors.Is(err, os.ErrNotExist) && strings.Contains(err.Error(), "read data authentication secret")
			}
		}},
		{"canceled before connect", func(t *testing.T, r *EndpointResolver, _ string) (context.Context, func(error) bool) {
			r.endpoint = refusedEndpoint(t)
			return canceled, func(err error) bool { return errors.Is(err, context.Canceled) }
		}},
		{"deadline before connect", func(t *testing.T, r *EndpointResolver, _ string) (context.Context, func(error) bool) {
			r.endpoint = refusedEndpoint(t)
			return expired, func(err error) bool { return errors.Is(err, context.DeadlineExceeded) }
		}},
		{"invalid address", func(t *testing.T, r *EndpointResolver, _ string) (context.Context, func(error) bool) {
			r.endpoint = Endpoint{Host: "127.0.0.1", Port: -1}
			return context.Background(), func(err error) bool {
				var addrErr *net.AddrError
				var opErr *net.OpError
				return errors.As(err, &addrErr) || errors.As(err, &opErr)
			}
		}},
		{"canceled during the handshake", func(t *testing.T, r *EndpointResolver, rootDir string) (context.Context, func(error) bool) {
			r.endpoint = newResolverTestListener(t, rootDir, modeSilent).endpoint()
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)
			t.Cleanup(cancel)
			return ctx, func(err error) bool { return errors.Is(err, context.Canceled) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeDiscovery()
			d.set(Endpoint{}, errors.New("discovery must not run"))
			r, rootDir, _ := newTestResolver(t, Endpoint{}, d)
			ctx, isOriginal := tc.setup(t, r, rootDir)
			r.Arm()

			conn, err := r.DialContext(ctx, DataNetwork, "")
			if conn != nil {
				_ = conn.Close()
				t.Fatal("dial succeeded")
			}
			if !isOriginal(err) || IsEndpointUnavailable(err) {
				t.Fatalf("err = %v, want the original failure without the endpoint-unavailable marker", err)
			}
			if got := d.calls.Load(); got != 0 {
				t.Fatalf("discovery calls = %d, want 0", got)
			}
		})
	}

	// A MySQL failure after a proven dial (here the login refused) is the
	// driver's to report: the resolver handed over a good connection.
	t.Run("mysql access denied", func(t *testing.T) {
		d := newFakeDiscovery()
		d.set(Endpoint{}, errors.New("discovery must not run"))
		r, rootDir, _ := newTestResolver(t, Endpoint{}, d)
		r.endpoint = newResolverTestListener(t, rootDir, modeProveThenDenyAccess).endpoint()
		r.Arm()

		cfg := mysql.NewConfig()
		cfg.User, cfg.Net, cfg.Addr = "root", DataNetwork, r.endpoint.Address()
		cfg.DialFunc = r.DialContext
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			t.Fatal(err)
		}
		db := sql.OpenDB(connector)
		defer func() { _ = db.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = db.PingContext(ctx)
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1045 || IsEndpointUnavailable(err) {
			t.Fatalf("ping: %v, want MySQL error 1045 without the endpoint-unavailable marker", err)
		}
		if got := d.calls.Load(); got != 0 {
			t.Fatalf("discovery calls = %d, want 0", got)
		}
	})
}

// TestEndpointResolver_HealthyDialAuthenticatesWithoutDiscovery: a dial to a
// healthy proxy is one connection proving the secret as it is on disk now —
// a rotated secret, with no cached copy — and nothing else: no discovery and
// no extra connection.
func TestEndpointResolver_HealthyDialAuthenticatesWithoutDiscovery(t *testing.T) {
	d := newFakeDiscovery()
	d.set(Endpoint{}, errors.New("discovery must not run"))
	r, rootDir, _ := newTestResolver(t, Endpoint{}, d)
	l := newResolverTestListener(t, rootDir, modeProve)
	r.endpoint = l.endpoint()
	r.Arm()

	for i := 1; i <= 2; i++ {
		if _, err := identity.WriteSecret(rootDir); err != nil {
			t.Fatal(err)
		}
		conn, err := dialWithin(r, 5*time.Second)
		if err != nil {
			t.Fatalf("dial %d after rotating the secret: %v", i, err)
		}
		_ = conn.Close()
		if got := l.accepted.Load(); got != int32(i) {
			t.Fatalf("after dial %d the proxy accepted %d connections, want %d", i, got, i)
		}
	}
	if got := d.calls.Load(); got != 0 {
		t.Fatalf("discovery calls = %d, want 0", got)
	}
	if got := r.Endpoint(); got != l.endpoint() {
		t.Fatalf("endpoint = %v, want %v", got, l.endpoint())
	}
}
