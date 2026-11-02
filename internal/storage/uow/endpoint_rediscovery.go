package uow

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// errProviderClosed refuses a new connection of a provider that has been closed.
var errProviderClosed = errors.New("uow: provider closed")

// proxyEndpointResolver re-resolves the workspace proxy's endpoint for every
// new physical connection of a provider pool (#7295). A proxy with a finite
// idle timeout exits once its last client disconnects, and the replacement the
// next opener spawns may publish a different OS-assigned port. A pool whose DSN
// pinned the construction-time address would keep dialing the retired port,
// so the driver's BeforeConnect hook replaces the address on its per-connection
// copy of the configuration before it dials.
//
// discover is the same proxy discovery the constructor ran: it authenticates
// the published proxy, adopts a live one without taking the spawn lock, and
// otherwise spawns a replacement under the existing proxy lifecycle rules.
//
// A resolver starts disarmed: bootstrap (the existence probe, version and
// schema work, the session-database assertion) runs on the endpoint the
// constructor resolved, which fresh-bootstrap heal authority is bound to.
// openAndInitSchema arms it only once the provider it hands out is proven.
//
// Hooks that overlap share one discovery, run by a worker of its own; nothing
// is cached for later connections, because a stale port can be recycled by an
// unrelated listener that still accepts TCP. Each new connection
// authenticates the proxy afresh, and a pooled connection is reused without
// discovery.
//
// close stops admission and waits for the discovery in flight, so no worker
// outlives the provider.
type proxyEndpointResolver struct {
	discover func() (proxy.Endpoint, error)

	mu       sync.Mutex
	armed    bool
	closed   bool
	inflight *endpointDiscovery
}

// endpointDiscovery is one discovery run, shared by the hooks that overlap it.
// ep and err are set before done is closed.
type endpointDiscovery struct {
	done chan struct{}
	ep   proxy.Endpoint
	err  error
}

func newProxyEndpointResolver(discover func() (proxy.Endpoint, error)) *proxyEndpointResolver {
	return &proxyEndpointResolver{discover: discover}
}

// workspaceProxyDiscovery binds discovery to the absolute workspace root and the
// exact OpenOpts a constructor used, so every rediscovery asks for the same
// proxy, backend and upstream the provider was opened against.
func workspaceProxyDiscovery(rootDir string, opts proxy.OpenOpts) func() (proxy.Endpoint, error) {
	return func() (proxy.Endpoint, error) {
		return proxy.GetCreateDatabaseProxyServerEndpoint(rootDir, opts)
	}
}

// arm starts rediscovery for every later new physical connection.
func (r *proxyEndpointResolver) arm() {
	r.mu.Lock()
	r.armed = true
	r.mu.Unlock()
}

// beforeConnect is the mysql.BeforeConnect hook. cfg is the driver's clone for
// this one connection: only its address changes, so database, credentials,
// TLS, timeouts and ClientFoundRows stay as the provider was opened with.
func (r *proxyEndpointResolver) beforeConnect(ctx context.Context, cfg *mysql.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errProviderClosed
	}
	if !r.armed {
		r.mu.Unlock()
		return nil
	}
	d := r.inflight
	if d == nil {
		d = &endpointDiscovery{done: make(chan struct{})}
		r.inflight = d
		go r.run(d)
	}
	r.mu.Unlock()

	// Cancellation ends only this caller's wait: the discovery keeps running
	// for the other waiters, since a proxy it may be spawning is not this
	// caller's to stop. When both are ready, select picks either, so the
	// caller's cancellation is checked again and always wins.
	select {
	case <-d.done:
	case <-ctx.Done():
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return errProviderClosed
	}
	if d.err != nil {
		// Never fall back to the construction address, which may now belong to
		// an unrelated listener, and never ErrBadConn: the pool would retry on
		// the same failing discovery instead of reporting it.
		return fmt.Errorf("uow: rediscover proxy endpoint: %w", d.err)
	}
	cfg.Addr = d.ep.Address()
	return nil
}

// close refuses every later hook and waits for the discovery in flight, if
// any, to finish. It is safe to call more than once.
func (r *proxyEndpointResolver) close() {
	r.mu.Lock()
	r.closed = true
	d := r.inflight
	r.mu.Unlock()
	if d != nil {
		<-d.done
	}
}

// run performs discovery d and publishes its result, clearing the in-flight
// slot so the next connection starts a discovery of its own.
func (r *proxyEndpointResolver) run(d *endpointDiscovery) {
	ep, err := r.discover()
	r.mu.Lock()
	defer r.mu.Unlock()
	d.ep, d.err = ep, err
	r.inflight = nil
	close(d.done)
}
