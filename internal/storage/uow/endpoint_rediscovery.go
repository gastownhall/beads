package uow

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// proxyEndpointResolver refreshes the local dbproxy address a provider pool
// dials, for every NEW physical connection, after bootstrap (#7295).
//
// A managed provider resolves its proxy once, at construction, and bakes the
// address into the pool's DSN. The proxy retires after an idle interval
// (internal/storage/dbproxy/proxy/server.go idleWatcher) and a replacement
// binds a fresh OS-assigned port, so the long-lived pool's next physical
// connection dials a dead address forever. The resolver re-runs the same
// authenticated discovery the constructor used — adoption of a live proxy
// before the spawn lock, spawn markers, stop epochs and cleanup unchanged —
// and swaps only the address the driver dials; database, credentials, TLS
// and timeouts stay as the DSN set them.
//
// It is armed only once bootstrap has proven a pool (openAndInitSchema).
// Before that every bootstrap connection — probe, version check, schema
// work, session-database assertion — uses the fixed construction address, so
// the open keeps the single-session steady state it has always had, and the
// pool handed to the provider is the pool that was proven. Pooled connection
// reuse never consults it: the MySQL driver's BeforeConnect hook fires only
// when database/sql decides a NEW physical connection is needed.
type proxyEndpointResolver struct {
	// resolve performs one authenticated discovery. In production it is
	// proxy.GetCreateDatabaseProxyServerEndpoint over the exact root and
	// proxy.OpenOpts of the constructing provider, captured immutably here;
	// tests inject their own closure through this field instead of mutating
	// package globals.
	//
	// Discovery owns its own deadlines and lifecycle. Callers pass
	// context.Background(): their cancellation ends THEIR wait, never the
	// discovery itself — a spawned proxy child is shared, a peer may already
	// have adopted it, and a canceled opener is not a shutdown request.
	resolve func(ctx context.Context) (proxy.Endpoint, error)

	mu sync.Mutex
	// armed gates discovery: disarmed until the provider's open completes,
	// armed for the provider's lifetime. Guarded by mu so the arm() write
	// synchronizes with any hook already racing a first connection.
	armed bool
	// flight is the single in-flight discovery, nil when none is running.
	// New connections needing the endpoint while one is running join that
	// flight instead of starting their own — overlap coalesces into ONE
	// authenticated discovery. Nothing is cached: the slot is cleared when the
	// flight publishes, so the next connection after the overlap discovers
	// again.
	flight *resolveFlight
}

// resolveFlight is one shared discovery: the endpoint or error it produced,
// published once, and done, closed at publication, which joiners wait on.
type resolveFlight struct {
	done chan struct{}
	ep   proxy.Endpoint
	err  error
}

// newProxyEndpointResolver builds the production resolver for the provider
// rooted at rootDir (absolute path) with the exact proxy.OpenOpts its
// construction used.
func newProxyEndpointResolver(rootDir string, openOpts proxy.OpenOpts) *proxyEndpointResolver {
	return &proxyEndpointResolver{
		resolve: func(ctx context.Context) (proxy.Endpoint, error) {
			return proxy.GetCreateDatabaseProxyServerEndpoint(rootDir, openOpts)
		},
	}
}

// arm enables discovery for connections opened from now on. Called once, at
// the end of a successful open, immediately before the provider is handed
// out; never before (bootstrap connections must not rediscover).
func (r *proxyEndpointResolver) arm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armed = true
}

// beforeConnect is the mysql.BeforeConnect hook. While disarmed it leaves the
// driver's connection-local configuration untouched, so the pool dials the
// construction address. Once armed it resolves the endpoint and replaces only
// cfg.Addr — everything else the DSN set (database, credentials, TLS,
// timeouts, ClientFoundRows) is the driver's own clone of that
// configuration. A discovery failure is returned, never swallowed and never
// answered by dialing the stale construction address.
//
// Overlapping connections share one discovery: the caller that admits the
// flight becomes its worker and publishes the result under r.mu; the others
// wait on the flight outside the mutex and read what it published.
//
// Cancellation is the caller's own: an already-canceled caller is refused
// before it can admit or start work, and a waiter whose context ends is
// released with its context error while the shared discovery keeps running —
// its result stays available to every other waiter, and the spawned proxy
// work it may include is never run down by one caller hanging up.
func (r *proxyEndpointResolver) beforeConnect(ctx context.Context, cfg *mysql.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	armed := r.armed
	r.mu.Unlock()
	if !armed {
		return nil
	}

	flight, worker := r.admit()
	if worker {
		ep, err := r.resolve(context.Background())
		r.mu.Lock()
		flight.ep, flight.err = ep, err
		r.flight = nil
		close(flight.done)
		r.mu.Unlock()
		if err != nil {
			return fmt.Errorf("uow: rediscover proxy endpoint: %w", err)
		}
		cfg.Addr = ep.Address()
		return nil
	}

	select {
	case <-flight.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if flight.err != nil {
		return fmt.Errorf("uow: rediscover proxy endpoint: %w", flight.err)
	}
	cfg.Addr = flight.ep.Address()
	return nil
}

// admit returns the flight this hook joins, and whether the caller must run
// it. Joiners never hold r.mu while waiting.
func (r *proxyEndpointResolver) admit() (flight *resolveFlight, worker bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.flight != nil {
		return r.flight, false
	}
	flight = &resolveFlight{done: make(chan struct{})}
	r.flight = flight
	return flight, true
}
