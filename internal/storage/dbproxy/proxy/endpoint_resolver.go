package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// ErrEndpointUnavailable marks a data dial that failed in a shape rediscovery
// is responsible for — the proxy exited, was replaced, or no longer proves the
// workspace secret — and whose recovery attempt did not produce a working
// endpoint within the dial's own budget. A caller (bd serve) treats it as
// "try again in a moment", never as a permanent verdict: the next dial may
// find the published replacement and succeed.
//
// It wraps the triggering dial error and, when discovery ran and failed, that
// discovery error too. Ineligible failures (MySQL authentication, TLS, SQL
// errors, explicit cancellation, a deadline that struck before the connect,
// an invalid address, a secret-read failure) never carry it: those are the
// caller's own errors to surface unchanged (gastownhall/beads#7295).
var ErrEndpointUnavailable = errors.New("dbproxy: proxy endpoint unavailable")

// endpointUnavailableError wraps ErrEndpointUnavailable with the dial failure
// that triggered recovery and the discovery failure, if any, that kept it
// unrecovered.
type endpointUnavailableError struct {
	trigger   error
	discovery error
}

func (e *endpointUnavailableError) Error() string {
	if e.discovery == nil {
		return fmt.Sprintf("%s: %v", ErrEndpointUnavailable, e.trigger)
	}
	return fmt.Sprintf("%s: %v (rediscovery: %v)", ErrEndpointUnavailable, e.trigger, e.discovery)
}

func (e *endpointUnavailableError) Unwrap() []error {
	if e.discovery == nil {
		return []error{ErrEndpointUnavailable, e.trigger}
	}
	return []error{ErrEndpointUnavailable, e.trigger, e.discovery}
}

// IsEndpointUnavailable reports whether err carries ErrEndpointUnavailable.
func IsEndpointUnavailable(err error) bool {
	return errors.Is(err, ErrEndpointUnavailable)
}

// discoveryMinInterval bounds how often the resolver starts a discovery
// flight: a provider whose every dial fails (the proxy is gone and spawning
// is refused) must not fork a GetCreate round per request. Failures are not
// latched permanently — once the interval elapses, the next eligible failure
// may start a new flight.
const discoveryMinInterval = time.Second

// discoveryFlight is one in-progress GetCreate round, shared by every dial
// that fails while it runs. done closes after the flight has published its
// result (or recorded its error); err is stable once done is closed.
type discoveryFlight struct {
	done chan struct{}
	err  error
}

// EndpointResolver rediscovers a workspace's proxy endpoint for a long-lived
// client (bd serve) whose pool would otherwise dial an address resolved once
// at construction — an address whose proxy exits after its finite idle
// timeout, leaving the client refused forever (gastownhall/beads#7295).
//
// It owns the MySQL dial for one provider's pool: every connection dials the
// resolver's current endpoint and proves it holds the workspace's CURRENT
// secret (authDataDial), so a recycled port can never receive a MySQL byte.
// Recovery is armed only after the provider's construction succeeded: a
// construction dial that fails gets the same retries and errors it always
// had, and no discovery runs behind the constructor's back. Construction and
// runtime both route through DialContext, so there is exactly one pool and no
// connection is ever swapped or re-opened under a consumer.
//
// Discovery delegates entirely to GetCreateDatabaseProxyServerEndpoint with
// the provider's ORIGINAL OpenOpts: identity and upstream checks, stop
// epochs, spawn and lock ownership all stay where they already live. Success
// publishes the new endpoint and advances the generation even when the port
// did not change — a same-port incarnation is still a new process to prove.
type EndpointResolver struct {
	root string
	opts OpenOpts

	mu                sync.Mutex
	endpoint          Endpoint
	generation        uint64
	armed             bool
	closed            bool
	flight            *discoveryFlight
	lastFlightStart   time.Time
	lastDiscoveryErr  error
	disabledBroadcast chan struct{}

	// discover and now are seams for tests; production leaves the defaults.
	discover func(rootDir string, opts OpenOpts) (Endpoint, error)
	now      func() time.Time
	// minFlightInterval is discoveryMinInterval, shortened by tests.
	minFlightInterval time.Duration
}

// NewEndpointResolver returns a resolver pinned to ep, the endpoint the
// constructor just resolved, unarmed and ready to dial. The opts are the
// EXACT OpenOpts of that resolution: a rediscovery must adopt or spawn under
// the same identity, upstream and idle policy the workspace already has.
func NewEndpointResolver(rootDir string, opts OpenOpts, ep Endpoint) *EndpointResolver {
	return &EndpointResolver{
		root:              rootDir,
		opts:              opts,
		endpoint:          ep,
		disabledBroadcast: make(chan struct{}),
		discover:          GetCreateDatabaseProxyServerEndpoint,
		now:               time.Now,
		minFlightInterval: discoveryMinInterval,
	}
}

// Arm enables recovery. Call once the provider's construction succeeded; a
// resolver that is never armed keeps the historical behavior of dialing its
// construction endpoint forever.
func (r *EndpointResolver) Arm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armed = true
}

// StopDiscovery permanently disables discovery and wakes every waiter. It is
// the first half of shutdown and returns without waiting: the pool must be
// closed between this and Close so no dial is racing the flight join.
// Idempotent and safe to call on an unarmed resolver.
func (r *EndpointResolver) StopDiscovery() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.disabledBroadcast)
}

// Close stops discovery (idempotently) and joins a running flight, bounded
// by ctx. A nil return guarantees no flight remains. On ctx expiry it
// returns ctx's error with discovery still disabled; a later Close can
// finish joining, because the flight's done channel keeps working.
func (r *EndpointResolver) Close(ctx context.Context) error {
	r.StopDiscovery()
	fl := r.currentFlight()
	if fl == nil {
		return nil
	}
	select {
	case <-fl.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *EndpointResolver) currentFlight() *discoveryFlight {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flight
}

// DialContext is the MySQL driver dial for the provider's pool. The addr and
// network arguments are the DSN's construction-time pin and are ignored: the
// resolver's CURRENT endpoint is authoritative, which is the whole point —
// the pinned address is the one whose proxy can exit.
//
// Healthy dials read the secret, connect, and prove the listener: no
// discovery, no IDENT, no pidfile probe, one connection (gastownhall/beads
// #7295 AC-8). Only an eligible failure consults discovery, and it returns
// within its own context budget whether or not discovery finished — a
// discovery that outlasts one dial's budget keeps running for the next.
func (r *EndpointResolver) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, gen, err := r.dialCurrent(ctx)
	if err == nil {
		return conn, nil
	}
	if !r.recoverable(err) {
		return nil, err
	}
	trigger := err

	// A generation published between our snapshot and the failure makes
	// discovery pointless: a fresher endpoint is already known. Redial once
	// against it, without discovery, and stop there — bounded, not recursive.
	if r.currentGeneration() > gen {
		if conn, _, err := r.dialCurrent(ctx); err == nil {
			return conn, nil
		} else {
			return nil, r.unavailable(err, nil)
		}
	}

	fl := r.joinOrStartFlight()
	if fl == nil {
		// Discovery is disabled, or a flight started too recently. Report
		// unavailable with the triggering failure and whatever the last
		// discovery attempt said; do not latch — the next eligible failure
		// after the interval may try again.
		return nil, r.unavailable(trigger, r.lastDiscoveryError())
	}

	select {
	case <-fl.done:
		if fl.err != nil {
			return nil, r.unavailable(trigger, fl.err)
		}
		// Discovery succeeded and published. Redial once inside the
		// remaining budget; a failure there is terminal for this dial — no
		// recursive recovery.
		if conn, _, err := r.dialCurrent(ctx); err == nil {
			return conn, nil
		} else {
			return nil, r.unavailable(err, nil)
		}
	case <-r.disabledBroadcast:
		return nil, r.unavailable(trigger, nil)
	case <-ctx.Done():
		// The dial's budget is spent while discovery keeps running for later
		// callers. An explicit cancellation stays what it always was — the
		// caller's own error, not an unavailability claim.
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, fmt.Errorf("dbproxy: data dial %s: %w", r.currentAddress(), ctx.Err())
		}
		return nil, r.unavailable(trigger, nil)
	}
}

// dialCurrent dials the current endpoint once and returns the generation it
// dialed at, so a caller that fails can tell whether a newer endpoint has
// since been published.
func (r *EndpointResolver) dialCurrent(ctx context.Context) (net.Conn, uint64, error) {
	r.mu.Lock()
	ep, gen := r.endpoint, r.generation
	r.mu.Unlock()
	conn, err := authDataDial(ctx, r.root, ep.Address())
	if err != nil {
		return nil, gen, err
	}
	return conn, gen, nil
}

// currentAddress snapshots the current endpoint's address under the mutex;
// the dial path must not read r.endpoint unlocked while a flight may publish.
func (r *EndpointResolver) currentAddress() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.endpoint.Address()
}

func (r *EndpointResolver) currentGeneration() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

func (r *EndpointResolver) lastDiscoveryError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastDiscoveryErr
}

// recoverable reports whether err is a failure whose endpoint rediscovery
// can plausibly fix: armed, and a dial stage whose shape says the proxy is
// gone or unproven. Everything else — a secret that cannot be read (a retired
// topology must not be recreated), an invalid address, a context that was
// already dead before the connect, a MySQL/TLS/SQL error (which never reaches
// a dial) — is surfaced unchanged.
func (r *EndpointResolver) recoverable(err error) bool {
	r.mu.Lock()
	armed := r.armed
	r.mu.Unlock()
	if !armed {
		return false
	}
	stage, ok := dialStageOf(err)
	if !ok {
		return false
	}
	switch stage {
	case stageConnect:
		// A refusal or reset is the idle-exited proxy's signature. Anything
		// else (a name that does not resolve, a mid-connect deadline) is
		// not something rediscovery can change.
		return errors.Is(err, syscall.ECONNREFUSED) ||
			errors.Is(err, syscall.ECONNRESET) ||
			errors.Is(err, syscall.ECONNABORTED) ||
			errors.Is(err, syscall.EPIPE)
	case stageHandshake:
		// The listener answered without proving it holds the workspace
		// secret: wrong proof, EOF, silence, reset, broken pipe, or a
		// deadline that struck while the proof was still owed. An explicit
		// cancellation is the caller unwinding, never an unavailability.
		if errors.Is(err, context.Canceled) {
			return false
		}
		return errors.Is(err, errDataAuth) ||
			errors.Is(err, io.EOF) ||
			errors.Is(err, io.ErrUnexpectedEOF) ||
			errors.Is(err, syscall.ECONNRESET) ||
			errors.Is(err, syscall.EPIPE) ||
			errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, os.ErrDeadlineExceeded)
	default:
		return false
	}
}

// joinOrStartFlight returns the flight a failed dial should wait on: the one
// already running, or a new one just started. It returns nil when discovery
// is disabled or a flight started within the minimum interval (the cooldown:
// the caller reports unavailable with what it already knows) — and for a
// waiter joining an existing flight, the same flight every concurrent dial
// at this generation joins (gastownhall/beads#7295 AC-4).
func (r *EndpointResolver) joinOrStartFlight() *discoveryFlight {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.flight != nil && flightFinished(r.flight) {
		// A finished flight is kept only for its error; a new failure after
		// the cooldown replaces it.
		r.flight = nil
	}
	if r.flight != nil {
		return r.flight
	}
	if r.closed || !r.armed {
		return nil
	}
	now := r.now()
	if r.lastFlightStart.Add(r.minFlightInterval).After(now) {
		return nil
	}
	fl := &discoveryFlight{done: make(chan struct{})}
	r.flight = fl
	r.lastFlightStart = now
	go r.runFlight(fl)
	return fl
}

func flightFinished(fl *discoveryFlight) bool {
	select {
	case <-fl.done:
		return true
	default:
		return false
	}
}

// runFlight performs the discovery call and publishes its result. The call
// runs OUTSIDE r.mu with the caller's original opts and GetCreate's own
// deadlines (it takes no context), so a slow spawn cannot block a healthy
// dial or a shutdown. A late flight that finishes after StopDiscovery finds
// the resolver closed and publishes nothing.
func (r *EndpointResolver) runFlight(fl *discoveryFlight) {
	ep, err := r.discover(r.root, r.opts)

	r.mu.Lock()
	defer r.mu.Unlock()
	fl.err = err
	if err == nil && !r.closed {
		// Publish and advance the generation even at the same port: a
		// recycled-port listener is precisely what the next dial's proof
		// exists to catch, and a same-port incarnation is still new.
		r.endpoint = ep
		r.generation++
		registerDataAddress(r.root, ep)
	}
	if err != nil {
		r.lastDiscoveryErr = err
	}
	close(fl.done)
}

func (r *EndpointResolver) unavailable(trigger, discovery error) error {
	return &endpointUnavailableError{trigger: trigger, discovery: discovery}
}
