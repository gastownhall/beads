package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// ErrEndpointUnavailable marks a dial EndpointResolver could not complete
// because the workspace proxy it was dialing has gone away and rediscovering
// it did not produce a usable endpoint within the dial's budget. Match it with
// errors.Is; the error also wraps the dial failure that triggered recovery
// and, when there was one, the discovery failure.
var ErrEndpointUnavailable = errors.New("dbproxy: proxy endpoint unavailable")

// EndpointUnavailableError is the error EndpointResolver returns for an
// eligible dial failure it could not recover from.
type EndpointUnavailableError struct {
	// Dial is the dial failure that triggered (or would have triggered)
	// rediscovery.
	Dial error
	// Discovery is why rediscovery did not help: its own failure, its still
	// running past the dial's deadline, the cooldown, or a closed resolver.
	// Nil when rediscovery succeeded and the redial failed.
	Discovery error
}

func (e *EndpointUnavailableError) Error() string {
	if e.Discovery == nil {
		return fmt.Sprintf("%v: %v", ErrEndpointUnavailable, e.Dial)
	}
	return fmt.Sprintf("%v: %v (rediscovery: %v)", ErrEndpointUnavailable, e.Dial, e.Discovery)
}

func (e *EndpointUnavailableError) Unwrap() []error {
	if e.Discovery == nil {
		return []error{ErrEndpointUnavailable, e.Dial}
	}
	return []error{ErrEndpointUnavailable, e.Dial, e.Discovery}
}

// IsEndpointUnavailable reports whether err carries ErrEndpointUnavailable.
func IsEndpointUnavailable(err error) bool {
	return errors.Is(err, ErrEndpointUnavailable)
}

var (
	errDiscoveryPending  = errors.New("proxy rediscovery is still running")
	errDiscoveryCooldown = errors.New("proxy rediscovery ran less than a second ago")
	errResolverClosed    = errors.New("the endpoint resolver is closed")
)

// discoveryCooldown is the least time between two rediscovery flights.
const discoveryCooldown = time.Second

// EndpointResolver is the authenticated data-port dialer of one long-lived
// opener of a workspace proxy (a unit-of-work provider), for
// mysql.Config.DialFunc.
//
// The endpoint it starts from was resolved once, when the opener was built.
// The proxy there can exit afterwards — a finite idle timeout once the
// opener's pool has let its last connection go (GH#7295), a crash, bd dolt
// stop — and every later dial would then go to a dead port. Once armed, the
// resolver answers a dial that fails because nothing proved itself the
// workspace's proxy at that address by running
// GetCreateDatabaseProxyServerEndpoint again, with the opener's original
// options: it adopts a proxy another opener has started meanwhile, or starts
// one under the workspace's own idle policy, and the dial is retried once on
// what it returns.
//
// A healthy dial does nothing more than the bdproxy dialer does: it reads the
// current secret, connects, and runs the BDPROXY1 exchange. Discovery runs
// only for a dial that failed, one flight at a time, at most once per
// discoveryCooldown, and never with the resolver's mutex held. A dial waits
// for a flight only until its own context ends; the flight carries on for the
// dials after it. Nothing is ever replayed: a dial that cannot be recovered
// in its budget fails with EndpointUnavailableError, and the next one tries
// again.
//
// Every connection it returns has proved rootDir's current proxy secret. It
// dials from its own rootDir and endpoint, never from the bdproxy address
// registry, and it never stops a proxy.
type EndpointResolver struct {
	rootDir string
	opts    OpenOpts

	discover func(rootDir string, opts OpenOpts) (Endpoint, error)
	dial     func(ctx context.Context, rootDir, addr string, port int) (net.Conn, dataDialStage, error)
	now      func() time.Time

	mu         sync.Mutex
	endpoint   Endpoint
	generation uint64
	armed      bool
	closed     bool
	closedCh   chan struct{}
	flight     *discoveryFlight
	lastStart  time.Time
	lastErr    error
}

// discoveryFlight is one run of discover; done closes once endpoint and err
// are final.
type discoveryFlight struct {
	done     chan struct{}
	endpoint Endpoint
	err      error
}

// NewEndpointResolver returns an unarmed resolver for the proxy of rootDir
// that dials endpoint, the address GetCreateDatabaseProxyServerEndpoint(rootDir,
// opts) returned. Until Arm it only dials endpoint and returns its errors as
// they are, so an opener's construction keeps its own retries and errors.
func NewEndpointResolver(rootDir string, opts OpenOpts, endpoint Endpoint) *EndpointResolver {
	return &EndpointResolver{
		rootDir:  rootDir,
		opts:     opts,
		discover: GetCreateDatabaseProxyServerEndpoint,
		dial:     dialAuthenticated,
		now:      time.Now,
		endpoint: endpoint,
		closedCh: make(chan struct{}),
	}
}

// Endpoint is the endpoint the resolver currently dials.
func (r *EndpointResolver) Endpoint() Endpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.endpoint
}

// Arm enables rediscovery. Call it once the opener is fully built.
func (r *EndpointResolver) Arm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armed = true
}

// Disable stops any further rediscovery and wakes the dials waiting for a
// flight. It does not wait for a running flight; Close does.
func (r *EndpointResolver) Disable() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		close(r.closedCh)
	}
}

// Close disables rediscovery and waits, within ctx, for a running flight to
// finish. A nil return guarantees no flight remains and none can start; on
// ctx's end it reports the flight still running, and a later Close can finish
// the wait. With no flight running it returns nil at once, whatever ctx. A
// flight that finishes after Close publishes nothing.
func (r *EndpointResolver) Close(ctx context.Context) error {
	r.Disable()
	// Disable stops any flight from starting, and only one runs at a time,
	// so the flight running now is the last one.
	r.mu.Lock()
	f := r.flight
	r.mu.Unlock()
	if f == nil {
		return nil
	}
	select {
	case <-f.done:
		return nil
	default:
	}
	select {
	case <-f.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("dbproxy: close endpoint resolver: proxy rediscovery is still running: %w", ctx.Err())
	}
}

// DialContext is a mysql.Config.DialFunc. network and addr are ignored: the
// resolver dials its own current endpoint. A canceled dial is never reported
// as ErrEndpointUnavailable.
func (r *EndpointResolver) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	r.mu.Lock()
	ep, generation, armed := r.endpoint, r.generation, r.armed
	r.mu.Unlock()

	conn, stage, err := r.dialEndpoint(ctx, ep)
	if err == nil {
		return conn, nil
	}
	if !armed || !recoverableDialFailure(stage, err) {
		return nil, err
	}
	conn, err = r.recoverDial(ctx, generation, err)
	// A cancellation that lands as recovery gives up (as a failed flight
	// completes, within the cooldown, at Close) is still the caller's: a
	// client that left, not a proxy that is unavailable.
	if IsEndpointUnavailable(err) && errors.Is(ctx.Err(), context.Canceled) {
		return nil, ctx.Err()
	}
	return conn, err
}

func (r *EndpointResolver) dialEndpoint(ctx context.Context, ep Endpoint) (net.Conn, dataDialStage, error) {
	return r.dial(ctx, r.rootDir, net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)), ep.Port)
}

// recoverDial handles an eligible failure of a dial made at generation: it
// redials a newer endpoint when one has been published since, and otherwise
// joins or starts a discovery flight and redials what it finds, once.
func (r *EndpointResolver) recoverDial(ctx context.Context, generation uint64, dialErr error) (net.Conn, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, &EndpointUnavailableError{Dial: dialErr, Discovery: errResolverClosed}
	}
	if r.generation != generation {
		ep := r.endpoint
		r.mu.Unlock()
		return r.redial(ctx, ep, dialErr)
	}
	f := r.flight
	if f == nil {
		if !r.lastStart.IsZero() && r.now().Sub(r.lastStart) < discoveryCooldown {
			prior := r.lastErr
			r.mu.Unlock()
			return nil, &EndpointUnavailableError{Dial: dialErr, Discovery: errors.Join(errDiscoveryCooldown, prior)}
		}
		f = &discoveryFlight{done: make(chan struct{})}
		r.flight = f
		r.lastStart = r.now()
		go r.run(f)
	}
	r.mu.Unlock()

	select {
	case <-f.done:
	case <-r.closedCh:
		return nil, &EndpointUnavailableError{Dial: dialErr, Discovery: errResolverClosed}
	case <-ctx.Done():
		select {
		case <-f.done:
			// The flight finished as the dial's budget ran out: report
			// what it found.
		default:
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, ctx.Err()
			}
			return nil, &EndpointUnavailableError{Dial: dialErr, Discovery: errDiscoveryPending}
		}
	}
	if f.err != nil {
		return nil, &EndpointUnavailableError{Dial: dialErr, Discovery: f.err}
	}
	return r.redial(ctx, f.endpoint, dialErr)
}

// redial dials ep once more after recovery, with what is left of ctx. Its
// failure is final: there is no second round of recovery.
func (r *EndpointResolver) redial(ctx context.Context, ep Endpoint, dialErr error) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, &EndpointUnavailableError{Dial: dialErr}
	}
	conn, stage, err := r.dialEndpoint(ctx, ep)
	if err == nil {
		return conn, nil
	}
	if recoverableDialFailure(stage, err) || (stage == dataDialConnect && errors.Is(err, context.DeadlineExceeded)) {
		return nil, &EndpointUnavailableError{Dial: err}
	}
	return nil, err
}

// run is one discovery flight. It publishes what it finds, even an unchanged
// address — the generation still advances, so dials that failed against the
// old incarnation redial rather than starting another flight — unless the
// resolver was closed meanwhile.
func (r *EndpointResolver) run(f *discoveryFlight) {
	ep, err := r.discover(r.rootDir, r.opts)

	r.mu.Lock()
	switch {
	case r.closed:
		err = errResolverClosed
	case err == nil:
		r.endpoint = ep
		r.generation++
		r.lastErr = nil
	default:
		r.lastErr = err
	}
	f.endpoint, f.err = ep, err
	r.flight = nil
	r.mu.Unlock()
	close(f.done)
}

// recoverableDialFailure reports whether a dial failure at stage says the
// workspace proxy is no longer at the address dialed, so rediscovering it can
// help: the connect was refused, reset or aborted, or the listener there
// failed to prove the secret — wrong proof, a close, a reset, or silence
// until the deadline, which counts even when it surfaces as the context's
// DeadlineExceeded because the connect had already succeeded. The refusals
// and resets are matched by their errnos on this platform (isConnectGone,
// isConnLost).
//
// Everything else is the caller's: explicit cancellation, a deadline that
// expired before the connect, an address that does not parse or resolve, and
// a secret that cannot be read (a missing proxy.secret can mean the topology
// was retired, which rediscovery must not undo). MySQL, TLS and SQL failures
// happen after the dial and never reach here.
func recoverableDialFailure(stage dataDialStage, err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	switch stage {
	case dataDialConnect:
		return isConnectGone(err)
	case dataDialProof:
		if errors.Is(err, errDataAuth) ||
			errors.Is(err, io.EOF) ||
			errors.Is(err, io.ErrUnexpectedEOF) ||
			isConnLost(err) ||
			errors.Is(err, context.DeadlineExceeded) {
			return true
		}
		var netErr net.Error
		return errors.As(err, &netErr) && netErr.Timeout()
	default:
		return false
	}
}
