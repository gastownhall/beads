package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/stretchr/testify/require"
)

// The resolver tests stand in for a proxy data port with authenticated
// loopback listeners: a real listener that speaks BDPROXY1's server side
// under the workspace secret (authLoopback), or scripted misbehaviour — a
// wrong secret, silence — for the failure shapes. The resolver's discovery is
// a seam (r.discover), so no proxy process is spawned here.

// authLoopback is a loopback listener answering BDPROXY1 hellos under secret.
type authLoopback struct {
	ln      net.Listener
	secret  string
	addr    string
	port    int
	accepts atomic.Int64
	// silent, when true, accepts and closes without reading or writing: a
	// proxy that exited with its port left to something that never answers.
	silent bool
}

func startAuthLoopback(t *testing.T, secret string) *authLoopback {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return serveAuthLoopback(t, ln, secret)
}

// startAuthLoopbackAt binds a SPECIFIC port — the same-port incarnation shape,
// on a port an earlier stopped listener in the same test held. The rebind is
// retried bounded: a concurrent grab of the freed port is the only race, and
// the retry makes it vanish on any host where the release is asynchronous.
func startAuthLoopbackAt(t *testing.T, secret string, port int) *authLoopback {
	t.Helper()
	var ln net.Listener
	var err error
	require.Eventually(t, func() bool {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "rebind the freed port: %v", err)
	return serveAuthLoopback(t, ln, secret)
}

func serveAuthLoopback(t *testing.T, ln net.Listener, secret string) *authLoopback {
	t.Helper()
	a := &authLoopback{
		ln:     ln,
		secret: secret,
		addr:   ln.Addr().String(),
		port:   ln.Addr().(*net.TCPAddr).Port,
	}
	go a.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return a
}

func (a *authLoopback) endpoint() Endpoint {
	return Endpoint{Host: "127.0.0.1", Port: a.port}
}
func (a *authLoopback) serve() {
	for {
		conn, err := a.ln.Accept()
		if err != nil {
			return
		}
		a.accepts.Add(1)
		go func() {
			defer func() { _ = conn.Close() }()
			if a.silent {
				// Hold the connection without writing: drain whatever the
				// client sends and answer nothing, until it closes.
				buf := make([]byte, 4096)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}
			// A failed or unproven proof leaves the connection to drain
			// whatever the client insisted on sending (bytesSeen still
			// observes it) and drops it on return.
			_, _ = newDataAuth(a.secret, a.port).accept(conn)
		}()
	}
}

// recordingDiscover returns a discovery seam that counts calls and answers
// from script, a function of the call number (0-based).
func recordingDiscover(script func(call int) (Endpoint, error)) (func(string, OpenOpts) (Endpoint, error), *atomic.Int64) {
	var calls atomic.Int64
	return func(_ string, _ OpenOpts) (Endpoint, error) {
		n := int(calls.Add(1)) - 1
		return script(n)
	}, &calls
}

// newTestResolver pins a resolver to ep over a scratch workspace root holding
// secret, with the discovery seam installed and the fake clock unrolled to
// real time (tests that need the cooldown shorten r.minFlightInterval).
func newTestResolver(t *testing.T, secret string, ep Endpoint) *EndpointResolver {
	t.Helper()
	root := t.TempDir()
	// Write the test's own secret rather than a generated one, so the
	// resolver's dial-time read agrees with the loopback listener's.
	require.NoError(t, os.WriteFile(filepath.Join(root, identity.SecretFileName), []byte(secret), 0o600))
	r := NewEndpointResolver(root, OpenOpts{Backend: BackendExternal}, ep)
	r.minFlightInterval = 0
	return r
}

func dialCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// TestEndpointResolver_UnarmedNeverDiscovers is AC-3: a construction dial
// that fails gets its error unchanged and no rediscovery, because the
// constructor owns its own retries and errors — arming happens only after
// the open succeeds.
func TestEndpointResolver_UnarmedNeverDiscovers(t *testing.T) {
	secret := "6464646464646464646464646464646464646464646464646464646464646464"
	// A dead endpoint: the listener is closed, so dials are refused the way
	// an idle-exited proxy's port is.
	dead := Endpoint{Host: "127.0.0.1", Port: freePortOrZero(t)}
	r := newTestResolver(t, secret, dead)
	discover, calls := recordingDiscover(func(int) (Endpoint, error) {
		t.Error("unarmed dial ran discovery")
		return Endpoint{}, nil
	})
	r.discover = discover
	// Deliberately NOT armed.

	ctx, cancel := dialCtx()
	defer cancel()
	_, err := r.DialContext(ctx, "bdproxy", dead.Address())
	require.Error(t, err)
	require.False(t, IsEndpointUnavailable(err), "an unarmed failure is the caller's own error")
	require.ErrorIs(t, err, syscall.ECONNREFUSED, "the raw dial refusal must survive unchanged")
	require.EqualValues(t, 0, calls.Load())
}

// TestEndpointResolver_RefusedDialRediscoversAndRedials is the core recovery
// shape (AC-1, resolver level): a refused dial at generation g starts one
// discovery, which publishes a replacement endpoint, and the same dial
// redials it successfully — no second discovery, no second generation.
func TestEndpointResolver_RefusedDialRediscoversAndRedials(t *testing.T) {
	secret := "6464646464646464646464646464646464646464646464646464646464646464"
	dead := Endpoint{Host: "127.0.0.1", Port: freePortOrZero(t)}
	r := newTestResolver(t, secret, dead)
	replacement := startAuthLoopback(t, secret)
	discover, calls := recordingDiscover(func(int) (Endpoint, error) {
		return replacement.endpoint(), nil
	})
	r.discover = discover
	r.Arm()

	ctx, cancel := dialCtx()
	defer cancel()
	conn, err := r.DialContext(ctx, "bdproxy", dead.Address())
	require.NoError(t, err, "the dial must recover via rediscovery")
	require.NoError(t, conn.Close())
	require.EqualValues(t, 1, calls.Load(), "exactly one discovery flight")
	require.EqualValues(t, 1, replacement.accepts.Load(), "the redial is the only connection to the replacement")
}

// TestEndpointResolver_RecycledPortRefusesDataAndRecovers is the mandatory
// security edge: the old port is taken by a listener that cannot prove the
// workspace secret. The dial must send it nothing beyond the fixed hello,
// return no connection for it, and recover through a verified replacement.
func TestEndpointResolver_RecycledPortRefusesDataAndRecovers(t *testing.T) {
	secret := "6464646464646464646464646464646464646464646464646464646464646464"
	// The impostor holds the SAME port the resolver is pinned to, under a
	// different secret.
	other := startAuthLoopback(t, "6565656565656565656565656565656565656565656565656565656565656565")
	impostorPort := other.port
	other.ln.Close() // free the port…
	// …and rebind it ourselves so we can watch the bytes.
	impostor, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", impostorPort))
	require.NoError(t, err)
	t.Cleanup(func() { _ = impostor.Close() })
	bytesSeen := make(chan int64, 8)
	go func() {
		for {
			conn, err := impostor.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				buf := make([]byte, 4096)
				n, _ := conn.Read(buf)
				bytesSeen <- int64(n)
			}()
		}
	}()

	r := newTestResolver(t, secret, Endpoint{Host: "127.0.0.1", Port: impostorPort})
	replacement := startAuthLoopback(t, secret)
	discover, calls := recordingDiscover(func(int) (Endpoint, error) {
		return replacement.endpoint(), nil
	})
	r.discover = discover
	r.Arm()

	ctx, cancel := dialCtx()
	defer cancel()
	conn, err := r.DialContext(ctx, "bdproxy", fmt.Sprintf("127.0.0.1:%d", impostorPort))
	require.NoError(t, err, "the dial must recover from the unproven listener")
	require.NoError(t, conn.Close())
	require.EqualValues(t, 1, calls.Load())

	// The impostor saw only the hello — dataHelloLen bytes, never a MySQL
	// session byte.
	select {
	case n := <-bytesSeen:
		require.EqualValues(t, dataHelloLen, n,
			"the impostor received %d bytes, want exactly the %d-byte BDPROXY1 hello — a recycled port must never see session data", n, dataHelloLen)
	default:
		require.Fail(t, "the impostor saw no bytes at all; the dial never reached it")
	}
	require.EqualValues(t, 1, replacement.accepts.Load())
}

// TestEndpointResolver_ConcurrentFailuresShareOneFlight is AC-4: with
// discovery barrier-blocked, concurrent failed dials at one generation run
// exactly one discovery flight, and each waiter that stays within budget
// returns through the publish.
func TestEndpointResolver_ConcurrentFailuresShareOneFlight(t *testing.T) {
	secret := "6464646464646464646464646464646464646464646464646464646464646464"
	dead := Endpoint{Host: "127.0.0.1", Port: freePortOrZero(t)}
	r := newTestResolver(t, secret, dead)
	replacement := startAuthLoopback(t, secret)

	var mu sync.Mutex
	started := make(chan struct{}, 1)
	discover, calls := recordingDiscover(func(int) (Endpoint, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		mu.Lock()
		defer mu.Unlock()
		return replacement.endpoint(), nil
	})
	r.discover = discover
	r.Arm()

	const waiters = 5
	var wg sync.WaitGroup
	results := make([]error, waiters)
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := dialCtx()
			defer cancel()
			_, err := r.DialContext(ctx, "bdproxy", dead.Address())
			results[i] = err
		}(i)
	}
	<-started
	wg.Wait()

	require.EqualValues(t, 1, calls.Load(), "concurrent failed dials must share one flight")
	require.EqualValues(t, waiters, replacement.accepts.Load(), "each waiter redials the published endpoint")
	for i, err := range results {
		require.NoErrorf(t, err, "waiter %d", i)
	}
}

// TestEndpointResolver_WaiterReturnsByDeadlineWhileDiscoveryBlocked is the
// other half of AC-4: a discovery that outlasts a dial's budget does not
// hold the dial — it returns unavailable within budget, and the flight keeps
// running for later callers.
func TestEndpointResolver_WaiterReturnsByDeadlineWhileDiscoveryBlocked(t *testing.T) {
	secret := "6464646464646464646464646464646464646464646464646464646464646464"
	dead := Endpoint{Host: "127.0.0.1", Port: freePortOrZero(t)}
	r := newTestResolver(t, secret, dead)
	replacement := startAuthLoopback(t, secret)
	release := make(chan struct{})
	discover, calls := recordingDiscover(func(int) (Endpoint, error) {
		<-release
		return replacement.endpoint(), nil
	})
	r.discover = discover
	r.Arm()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := r.DialContext(ctx, "bdproxy", dead.Address())
	elapsed := time.Since(start)
	require.Error(t, err)
	require.True(t, IsEndpointUnavailable(err), "a dial that outlived its budget reports unavailability, got: %v", err)
	require.Less(t, elapsed, 2*time.Second, "the waiter must return by its dial deadline, took %s", elapsed)
	require.ErrorIs(t, err, syscall.ECONNREFUSED, "the triggering refusal is preserved as the cause")

	// The flight is still running; releasing it publishes for later callers.
	close(release)
	require.Eventually(t, func() bool {
		ctx, cancel := dialCtx()
		defer cancel()
		_, err := r.DialContext(ctx, "bdproxy", dead.Address())
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "a later dial must find the published replacement")
	require.EqualValues(t, 1, calls.Load(), "the later dial reuses the published flight, not a new one")
}

// TestEndpointResolver_CloseJoinsFlightAndStopsDiscovery is AC-5: provider
// Close succeeds while a flight is running — the flight finishes, and no
// later dial can start another.
func TestEndpointResolver_CloseJoinsFlightAndStopsDiscovery(t *testing.T) {
	secret := "6464646464646464646464646464646464646464646464646464646464646464"
	dead := Endpoint{Host: "127.0.0.1", Port: freePortOrZero(t)}
	r := newTestResolver(t, secret, dead)
	flightStarted := make(chan struct{}, 1)
	released := make(chan struct{})
	discover, calls := recordingDiscover(func(int) (Endpoint, error) {
		select {
		case flightStarted <- struct{}{}:
		default:
		}
		<-released
		return Endpoint{}, errors.New("discovery failed")
	})
	r.discover = discover
	r.Arm()

	// A dial whose flight is blocked starts the flight and waits.
	dialDone := make(chan error, 1)
	go func() {
		ctx, cancel := dialCtx()
		defer cancel()
		_, err := r.DialContext(ctx, "bdproxy", dead.Address())
		dialDone <- err
	}()
	<-flightStarted

	// Close joins the flight: while the flight is still running, Close
	// must NOT return — its success means "no flight remains".
	closeDone := make(chan error, 1)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	go func() { closeDone <- r.Close(closeCtx) }()
	select {
	case err := <-closeDone:
		require.Fail(t, "Close returned %v while the flight was still running", err)
	case <-time.After(500 * time.Millisecond):
	}

	// Release the flight; only NOW Close may return — proof it joined.
	close(released)
	select {
	case err := <-closeDone:
		require.NoError(t, err, "Close must join the running flight and succeed")
	case <-time.After(5 * time.Second):
		require.Fail(t, "Close did not return after the flight finished")
	}

	// The in-flight dial returned (woken by the disable broadcast or the
	// flight's own completion), and no later dial starts another flight.
	select {
	case err := <-dialDone:
		require.Error(t, err)
	case <-time.After(6 * time.Second):
		require.Fail(t, "the waiter did not return after Close")
	}
	ctx, cancel := dialCtx()
	defer cancel()
	_, err := r.DialContext(ctx, "bdproxy", dead.Address())
	require.Error(t, err)
	require.True(t, IsEndpointUnavailable(err), "a closed resolver reports unavailability without discovery, got: %v", err)
	require.EqualValues(t, 1, calls.Load(), "no dial after Close may start a discovery flight")

	// Close is idempotent and, with no flight, immediate.
	require.NoError(t, r.Close(closeCtx))
}

// TestEndpointResolver_SamePortRespawnAdvancesGeneration pins the same-port
// incarnation: publishing an endpoint whose ADDRESS did not change is still
// a new generation. The resolver cannot compare ports to deduplicate — a
// recycled port is precisely what the next dial's BDPROXY1 proof exists to
// catch — so it must advance the generation even for an identical endpoint.
// Rebinding one port twice is not arrangeable reliably, so the test pins the
// generation contract itself: the published endpoint equals the stale one,
// the redial still fails (the new incarnation is not up yet), and the
// generation has advanced anyway.
func TestEndpointResolver_SamePortRespawnAdvancesGeneration(t *testing.T) {
	secret := "6464646464646464646464646464646464646464646464646464646464646464"
	dead := Endpoint{Host: "127.0.0.1", Port: freePortOrZero(t)}
	r := newTestResolver(t, secret, dead)
	discover, calls := recordingDiscover(func(int) (Endpoint, error) {
		// Discovery reports the SAME address: a respawn that landed on the
		// same port, before it finished binding.
		return dead, nil
	})
	r.discover = discover
	r.Arm()

	require.Equal(t, uint64(0), r.currentGeneration())

	ctx, cancel := dialCtx()
	defer cancel()
	_, err := r.DialContext(ctx, "bdproxy", dead.Address())
	require.Error(t, err, "the redial reaches a port nobody has bound yet")
	require.True(t, IsEndpointUnavailable(err))
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, uint64(1), r.currentGeneration(),
		"a published endpoint advances the generation even at the same port")
	r.mu.Lock()
	published := r.endpoint
	r.mu.Unlock()
	require.Equal(t, dead, published, "the published endpoint is the one discovery returned")

	// The next dial learns from the generation: elapse the cooldown against
	// the fake clock, then the respawn — same port, now listening — answers.
	respawn := startAuthLoopbackAt(t, secret, dead.Port)
	r.now = func() time.Time { return time.Now().Add(time.Hour) }
	conn, err := r.DialContext(ctx, "bdproxy", dead.Address())
	require.NoError(t, err, "a same-port incarnation is recoverable")
	require.NoError(t, conn.Close())
	require.EqualValues(t, 1, calls.Load(), "the redial at the new generation does not rediscover")
	require.EqualValues(t, 1, respawn.accepts.Load())
}

// TestEndpointResolver_CooldownBoundsFlightRestarts pins the "at most once
// per second" rate: a failure right after a flight does not start another,
// reports unavailability retaining the prior discovery error, and is not
// latched — once the interval elapses, the next eligible failure may fly
// again and succeed.
func TestEndpointResolver_CooldownBoundsFlightRestarts(t *testing.T) {
	secret := "6464646464646464646464646464646464646464646464646464646464646464"
	dead := Endpoint{Host: "127.0.0.1", Port: freePortOrZero(t)}
	r := newTestResolver(t, secret, dead)
	replacement := startAuthLoopback(t, secret)
	r.minFlightInterval = time.Hour // every flight after the first is inside the cooldown

	fly := false
	discover, calls := recordingDiscover(func(int) (Endpoint, error) {
		if !fly {
			fly = true
			return Endpoint{}, errors.New("spawn refused")
		}
		return replacement.endpoint(), nil
	})
	r.discover = discover
	r.Arm()

	ctx, cancel := dialCtx()
	defer cancel()
	_, err := r.DialContext(ctx, "bdproxy", dead.Address())
	require.Error(t, err)
	require.True(t, IsEndpointUnavailable(err))
	require.EqualValues(t, 1, calls.Load())

	// Inside the cooldown: unavailable, retaining the prior discovery error,
	// and no second flight.
	_, err = r.DialContext(ctx, "bdproxy", dead.Address())
	require.Error(t, err)
	require.True(t, IsEndpointUnavailable(err))
	require.ErrorContains(t, err, "spawn refused", "the cooldown response retains the prior discovery error")
	require.EqualValues(t, 1, calls.Load(), "the cooldown must not start a second flight")

	// Not latched: the interval is measured from the flight's start, and a
	// fake clock that has moved past it allows another flight.
	r.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	conn, err := r.DialContext(ctx, "bdproxy", dead.Address())
	require.NoError(t, err, "after the cooldown a failed discovery is not latched")
	require.NoError(t, conn.Close())
	require.EqualValues(t, 2, calls.Load())
}

// freePortOrZero grabs an OS-assigned port and closes its listener again:
// almost always refused when dialed, which is the shape the tests want (an
// idle-exited proxy's port). The TOCTOU window is irrelevant here — any
// failure the dial hits in it is still connect-stage and the tests that use
// it assert on discovery counts, not on the refusal itself.
func freePortOrZero(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// TestEndpointResolverDiscoveryFailsReportsBothCauses pins the error shape of
// an exhausted recovery: the marker, the triggering dial error and the
// discovery error all survive wrapping, so an operator's log names both the
// dead endpoint's failure and why rediscovery did not rescue it.
func TestEndpointResolverDiscoveryFailsReportsBothCauses(t *testing.T) {
	secret := "6464646464646464646464646464646464646464646464646464646464646464"
	dead := Endpoint{Host: "127.0.0.1", Port: freePortOrZero(t)}
	r := newTestResolver(t, secret, dead)
	discover, _ := recordingDiscover(func(int) (Endpoint, error) {
		return Endpoint{}, errors.New("proxy.lock held by another start")
	})
	r.discover = discover
	r.Arm()

	ctx, cancel := dialCtx()
	defer cancel()
	_, err := r.DialContext(ctx, "bdproxy", dead.Address())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEndpointUnavailable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED, "the triggering dial error is preserved")
	require.ErrorContains(t, err, "proxy.lock held by another start", "the discovery error is preserved")
	// The wrapped error still classifies at the uow seam.
	require.True(t, errors.Is(fmt.Errorf("uow: pin connection: %w", err), ErrEndpointUnavailable))
}
