// Package pool implements a MySQL connection pooler that fronts a Dolt
// sql-server. Many short-lived clients (bd processes) connect to the pooler;
// the pooler multiplexes them onto a SMALL fixed set of persistent backend
// connections to Dolt, so Dolt sees ~constant connections regardless of client
// churn (gt-ye21 / #4292).
//
// Design notes (proven by the de-risking spike):
//   - Client-facing handshake is terminated by the vitess go/mysql Listener +
//     AuthServerNone (Dolt is passwordless). vitess negotiates capability flags
//     and frames result sets, so CLIENT_DEPRECATE_EOF etc. stay consistent and
//     the handler never has to match flags manually.
//   - Each client CONNECTION pins one backend for its lifetime (no mid-conn
//     multiplexing). The win is REUSE across successive clients: 300 churning
//     clients collapse to ~K backend connections on Dolt.
//   - On clean client disconnect the backend is reset (COM_RESET_CONNECTION)
//     and returned to the pool. On ANY backend error the backend is discarded
//     and reconnected, never reset-reused, so a desynced backend can never be
//     handed to the next client.
//   - Idle Dolt backends are reaped after ~40s (loopback idle reaping, NOT
//     wait_timeout). A background keepalive Ping every ~10s plus a
//     liveness-Ping-with-reconnect on borrow keeps reconnects at zero.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dolthub/vitess/go/mysql"
)

// Defaults for pool sizing and timing. Operators tune size via
// BEADS_DOLT_POOL_SIZE; the rest are conservative constants.
const (
	// DefaultSize is the number of persistent backend connections held open
	// against Dolt when no explicit size is configured.
	DefaultSize = 16
	// MinSize is the floor for the pool size; smaller values are clamped up.
	MinSize = 4

	defaultKeepalive   = 10 * time.Second
	defaultBorrowWait  = 10 * time.Second
	defaultDialTimeout = 10 * time.Second
)

// ErrBorrowTimeout is returned by borrow when no backend becomes available
// within the configured wait timeout (all K backends are checked out and the
// client gave up waiting).
var ErrBorrowTimeout = errors.New("pool: timed out waiting for an available backend")

// ErrClosed is returned by borrow once the pool has been closed.
var ErrClosed = errors.New("pool: closed")

// Config configures a Pool.
type Config struct {
	// Addr is the Dolt sql-server TCP address (host:port) the backends dial.
	Addr string
	// Socket, when non-empty, is a unix-domain socket path for the Dolt
	// sql-server; it takes precedence over Addr.
	Socket string
	// User is the MySQL user backends authenticate as (Dolt is passwordless,
	// so only the user name matters). Defaults to "root".
	User string
	// Size is the number of persistent backends. Clamped to >= MinSize.
	Size int
	// KeepaliveInterval is how often idle backends are pinged. 0 -> default.
	KeepaliveInterval time.Duration
	// BorrowWait bounds how long borrow blocks before returning
	// ErrBorrowTimeout. 0 -> default.
	BorrowWait time.Duration
	// Stats, when non-nil, receives counters for reconnects and borrow waits.
	Stats *Stats
}

func (c Config) withDefaults() Config {
	if c.User == "" {
		c.User = "root"
	}
	if c.Size < MinSize {
		c.Size = MinSize
	}
	if c.KeepaliveInterval <= 0 {
		c.KeepaliveInterval = defaultKeepalive
	}
	if c.BorrowWait <= 0 {
		c.BorrowWait = defaultBorrowWait
	}
	return c
}

// Pool is a fixed-size set of persistent backends fronting one Dolt server.
type Pool struct {
	cfg  Config
	cp   *mysql.ConnParams
	logf func(string, ...any)

	mu     sync.Mutex
	cond   *sync.Cond
	idle   []*backend
	all    []*backend
	closed bool

	keepaliveStop chan struct{}
	keepaliveDone chan struct{}
}

// New creates and warms a Pool: it dials Size persistent backends up front so
// the first client never pays a connect cost, and starts the keepalive loop.
// On any warmup failure it tears down already-dialed backends and returns the
// error.
func New(ctx context.Context, cfg Config) (*Pool, error) {
	cfg = cfg.withDefaults()
	cp, err := connParams(cfg)
	if err != nil {
		return nil, err
	}

	p := &Pool{
		cfg:           cfg,
		cp:            cp,
		logf:          func(string, ...any) {},
		keepaliveStop: make(chan struct{}),
		keepaliveDone: make(chan struct{}),
	}
	p.cond = sync.NewCond(&p.mu)

	for i := 0; i < cfg.Size; i++ {
		b, err := p.dial(ctx, i)
		if err != nil {
			for _, done := range p.all {
				done.close()
			}
			return nil, fmt.Errorf("pool: warmup backend %d: %w", i, err)
		}
		p.idle = append(p.idle, b)
		p.all = append(p.all, b)
	}

	go p.keepalive()
	return p, nil
}

// SetLogger installs a logging function (e.g. (*log.Logger).Printf). Optional;
// the pool is silent by default.
func (p *Pool) SetLogger(logf func(string, ...any)) {
	if logf != nil {
		p.logf = logf
	}
}

// Size returns the configured number of backends.
func (p *Pool) Size() int { return p.cfg.Size }

func (p *Pool) dial(ctx context.Context, id int) (*backend, error) {
	dctx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()
	c, err := mysql.Connect(dctx, p.cp)
	if err != nil {
		return nil, err
	}
	return &backend{id: id, conn: c}, nil
}

// Borrow blocks until a live backend is available or the wait timeout elapses.
// The returned backend is guaranteed live (it is Pinged, and lazily
// reconnected if the ping fails). Callers MUST return it via Release (clean
// session end) or Discard (after any error).
func (p *Pool) Borrow(ctx context.Context) (*backend, error) {
	deadline := time.Now().Add(p.cfg.BorrowWait)

	p.mu.Lock()
	if len(p.idle) == 0 && !p.closed {
		p.cfg.Stats.IncBorrowWait()
	}
	for len(p.idle) == 0 && !p.closed {
		if !p.waitUntil(deadline) {
			p.mu.Unlock()
			if p.isClosed() {
				return nil, ErrClosed
			}
			p.cfg.Stats.IncBorrowTimeout()
			return nil, ErrBorrowTimeout
		}
	}
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	b := p.idle[len(p.idle)-1]
	p.idle = p.idle[:len(p.idle)-1]
	p.mu.Unlock()

	// Liveness check with lazy reconnect: an idle backend may have been reaped
	// by Dolt. Pinging here keeps a possibly-dead conn off the hot path.
	if err := b.ping(); err != nil {
		p.logf("backend %d dead on borrow (%v); reconnecting", b.id, err)
		p.cfg.Stats.IncReconnect()
		nb, derr := p.dial(ctx, b.id)
		if derr != nil {
			// Reconnect failed: put a placeholder back so pool size is
			// preserved and signal waiters, then surface the error.
			b.close()
			p.returnDead(b.id)
			return nil, fmt.Errorf("pool: reconnect backend %d on borrow: %w", b.id, derr)
		}
		b.close()
		b = nb
		p.swapInAll(b)
	}
	return b, nil
}

// waitUntil blocks on the cond until either it is signalled or the deadline
// passes. It returns true if it may have been signalled (caller should
// re-check the predicate) and false if the deadline elapsed. p.mu must be held.
func (p *Pool) waitUntil(deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	// sync.Cond has no timed wait; use a timer that broadcasts on expiry.
	timer := time.AfterFunc(remaining, func() {
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	})
	p.cond.Wait()
	timer.Stop()
	return time.Now().Before(deadline)
}

// Release resets the backend's session state (COM_RESET_CONNECTION) and returns
// it to the idle set. This is the ONLY path that reuses a backend, and it is
// only safe at a clean session boundary with no query in flight. If the reset
// fails, the backend is discarded and replaced so a desynced backend never
// re-enters the pool.
func (p *Pool) Release(b *backend) {
	if b == nil {
		return
	}
	if err := b.reset(); err != nil {
		p.logf("backend %d reset failed (%v); discarding", b.id, err)
		p.Discard(b)
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		b.close()
		return
	}
	p.idle = append(p.idle, b)
	p.cond.Signal()
	p.mu.Unlock()
}

// Discard throws away a (possibly desynced) backend and asynchronously
// reconnects a replacement so the pool keeps its size. Use after ANY backend
// error: a backend that errored mid-query may have unread bytes or partial
// state and must never be reset-reused.
func (p *Pool) Discard(b *backend) {
	if b == nil {
		return
	}
	id := b.id
	b.close()
	p.cfg.Stats.IncDiscard()

	// Reconnect in the background so the erroring client path isn't blocked.
	go func() {
		nb, err := p.dial(context.Background(), id)
		if err != nil {
			p.logf("backend %d reconnect after discard failed: %v", id, err)
			p.returnDead(id)
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			nb.close()
			return
		}
		p.swapInAllLocked(nb)
		p.idle = append(p.idle, nb)
		p.cond.Signal()
		p.mu.Unlock()
	}()
}

// returnDead handles a failed reconnect: it removes the slot's contribution so
// borrowers don't block forever waiting on a backend that will never return,
// then signals waiters (who will see the reduced idle set / timeout).
func (p *Pool) returnDead(id int) {
	p.mu.Lock()
	// Drop the dead backend from `all` so close() doesn't double-free; a future
	// keepalive or borrow on a surviving backend keeps the pool serving.
	p.removeFromAllLocked(id)
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *Pool) swapInAll(nb *backend) {
	p.mu.Lock()
	p.swapInAllLocked(nb)
	p.mu.Unlock()
}

func (p *Pool) swapInAllLocked(nb *backend) {
	for i, b := range p.all {
		if b.id == nb.id {
			p.all[i] = nb
			return
		}
	}
	p.all = append(p.all, nb)
}

func (p *Pool) removeFromAllLocked(id int) {
	for i, b := range p.all {
		if b.id == id {
			p.all = append(p.all[:i], p.all[i+1:]...)
			return
		}
	}
}

func (p *Pool) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// keepalive pings idle backends so Dolt does not reap them. To avoid racing a
// concurrent Borrow on the same conn, it pops each backend out of idle, pings
// it without the lock, then returns it.
func (p *Pool) keepalive() {
	defer close(p.keepaliveDone)
	t := time.NewTicker(p.cfg.KeepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-p.keepaliveStop:
			return
		case <-t.C:
			p.pingAll()
		}
	}
}

// pingAll pings EVERY backend — idle and borrowed alike. A borrowed backend is
// pinned to a long-lived client connection that may sit idle (go-sql-driver
// keeps conns open in its own pool); without pinging it too, Dolt's ~40s idle
// reaper would kill it and the next client query would EOF. The per-backend
// mutex serializes the ping against any in-flight client query, so pinging a
// borrowed backend is race-free and never disturbs its session state.
//
// For an IDLE backend whose ping fails, we reconnect and swap in a replacement.
// For a BORROWED backend whose ping fails, we must not swap it (a client holds
// the pointer); we leave it, and the failure surfaces on that client's next
// query, after which ConnectionClosed will Discard+replace it.
func (p *Pool) pingAll() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	// Snapshot the backend set and the idle membership so we can ping without
	// holding the pool lock (pings do wire I/O).
	snapshot := make([]*backend, len(p.all))
	copy(snapshot, p.all)
	idleSet := make(map[int]bool, len(p.idle))
	for _, b := range p.idle {
		idleSet[b.id] = true
	}
	p.mu.Unlock()

	for _, b := range snapshot {
		if err := b.ping(); err != nil {
			p.logf("backend %d failed keepalive ping: %v", b.id, err)
			if idleSet[b.id] {
				p.reconnectIdle(b)
			}
			// Borrowed backend: leave it; the client query path / Discard heals it.
		}
	}
}

// reconnectIdle replaces a dead idle backend with a fresh connection, but only
// if it is still in the idle set (it may have been borrowed since the snapshot,
// in which case we leave it for the borrow/query path to heal).
func (p *Pool) reconnectIdle(dead *backend) {
	p.mu.Lock()
	idx := -1
	for i, b := range p.idle {
		if b == dead {
			idx = i
			break
		}
	}
	if idx < 0 || p.closed {
		p.mu.Unlock()
		return // borrowed since snapshot, or pool closing
	}
	// Remove it from idle while we reconnect (without the lock).
	p.idle = append(p.idle[:idx], p.idle[idx+1:]...)
	p.mu.Unlock()

	p.cfg.Stats.IncReconnect()
	nb, derr := p.dial(context.Background(), dead.id)
	if derr != nil {
		p.logf("backend %d keepalive reconnect failed: %v", dead.id, derr)
		dead.close()
		p.returnDead(dead.id)
		return
	}
	dead.close()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		nb.close()
		return
	}
	p.swapInAllLocked(nb)
	p.idle = append(p.idle, nb)
	p.cond.Signal()
	p.mu.Unlock()
}

// Close stops the keepalive loop and closes every backend. In-flight Borrow
// callers are woken and receive ErrClosed. Idempotent.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	all := p.all
	p.all = nil
	p.idle = nil
	p.cond.Broadcast()
	p.mu.Unlock()

	close(p.keepaliveStop)
	<-p.keepaliveDone

	for _, b := range all {
		b.close()
	}
}
