package proxy

// backendPool keeps a set of already-authenticated, live MySQL connections to
// the dolt backend and lends them to clients at session granularity (one
// backend per client TCP connection). This is the core of the connection-churn
// fix: without it, every bd CLI invocation forks a process that opens a fresh
// connection (TCP + auth handshake + session setup) and tears it down at exit,
// which the dolt sql-server pays for as raw connection-setup CPU. Reusing warm
// backends collapses that to ~zero new connections per second in steady state.
//
// Connections are keyed by (capabilities, database): byte-transparent
// command-phase forwarding requires the client↔proxy and proxy↔backend
// capability sets to match, and the initial database must be correct since the
// proxy does not parse the command stream. All bd clients share one driver and
// DSN, so they collapse onto a single key and reuse the same warm connections.

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// backendKey identifies an interchangeable class of pooled connections. Two
// borrowers may share a connection only if their keys are equal.
type backendKey struct {
	caps uint32
	db   string
}

// pooledConn is a live, authenticated backend connection plus the metadata the
// pool needs to decide reuse.
type pooledConn struct {
	conn      net.Conn
	key       backendKey
	createdAt time.Time
	transient bool // dialed past the idle cap; closed (not returned) on put
}

// poolDialer establishes a raw TCP connection to the backend. It is satisfied
// by server.DatabaseServer.Dial.
type poolDialer func(ctx context.Context) (net.Conn, error)

type backendPool struct {
	dial     poolDialer
	user     string
	password string
	maxIdle  int           // max idle connections retained per key
	lifetime time.Duration // 0 = unlimited; retire+reopen on exceed
	stats    *Stats

	mu     sync.Mutex
	idle   map[backendKey][]*pooledConn
	closed bool
}

func newBackendPool(dial poolDialer, user, password string, maxIdle int, lifetime time.Duration, stats *Stats) *backendPool {
	if maxIdle < 1 {
		maxIdle = 1
	}
	return &backendPool{
		dial:     dial,
		user:     user,
		password: password,
		maxIdle:  maxIdle,
		lifetime: lifetime,
		stats:    stats,
		idle:     make(map[backendKey][]*pooledConn),
	}
}

var errPoolClosed = errors.New("proxy: backend pool closed")

// get returns a connection for key, reusing a live idle one when available or
// dialing+authenticating a fresh one otherwise. The handshake against the
// backend is performed with exactly key.caps so the session is wire-compatible
// with the client the caller will forward.
func (p *backendPool) get(ctx context.Context, key backendKey) (*pooledConn, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errPoolClosed
		}
		stack := p.idle[key]
		if n := len(stack); n > 0 {
			pc := stack[n-1]
			p.idle[key] = stack[:n-1]
			p.mu.Unlock()
			if p.expired(pc) {
				_ = pc.conn.Close()
				p.stats.IncPoolRetire()
				continue
			}
			// Liveness check: a backend may have dropped the conn while idle.
			pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := pingBackend(pingCtx, pc.conn)
			cancel()
			if err != nil {
				_ = pc.conn.Close()
				p.stats.IncPoolDead()
				continue
			}
			p.stats.IncPoolHit()
			return pc, nil
		}
		p.mu.Unlock()
		return p.dialNew(ctx, key, false)
	}
}

// dialNew opens and authenticates a fresh backend connection for key.
func (p *backendPool) dialNew(ctx context.Context, key backendKey, transient bool) (*pooledConn, error) {
	conn, err := p.dial(ctx)
	if err != nil {
		p.stats.IncPoolDialError()
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	if err := backendHandshake(conn, key.caps, key.db, p.user, p.password, deadline); err != nil {
		_ = conn.Close()
		p.stats.IncPoolDialError()
		return nil, err
	}
	p.stats.IncPoolMiss()
	return &pooledConn{
		conn:      conn,
		key:       key,
		createdAt: time.Now(),
		transient: transient,
	}, nil
}

// put returns pc to the pool after resetting its session state. If the reset
// fails, the connection is destroyed rather than risk leaking state to the
// next borrower. Transient connections and connections returned after the pool
// is closed are always destroyed.
func (p *backendPool) put(pc *pooledConn) {
	if pc == nil {
		return
	}
	resetCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := resetBackend(resetCtx, pc.conn)
	cancel()
	if err != nil {
		p.stats.IncPoolResetError()
		_ = pc.conn.Close()
		return
	}
	p.stats.IncPoolReset()

	if pc.transient || p.expired(pc) {
		_ = pc.conn.Close()
		p.stats.IncPoolRetire()
		return
	}

	p.mu.Lock()
	if p.closed || len(p.idle[pc.key]) >= p.maxIdle {
		p.mu.Unlock()
		_ = pc.conn.Close()
		return
	}
	p.idle[pc.key] = append(p.idle[pc.key], pc)
	p.mu.Unlock()
}

func (p *backendPool) expired(pc *pooledConn) bool {
	if p.lifetime <= 0 || pc.createdAt.IsZero() {
		return false
	}
	return time.Since(pc.createdAt) >= p.lifetime
}

// drain closes every idle connection and marks the pool closed. In-flight
// borrowed connections are closed by their handlers on put (which sees closed
// and destroys them).
func (p *backendPool) drain() {
	p.mu.Lock()
	idle := p.idle
	p.idle = make(map[backendKey][]*pooledConn)
	p.closed = true
	p.mu.Unlock()
	for _, stack := range idle {
		for _, pc := range stack {
			_ = pc.conn.Close()
		}
	}
}

// idleCount reports the total number of idle connections held (test helper).
func (p *backendPool) idleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, stack := range p.idle {
		n += len(stack)
	}
	return n
}
