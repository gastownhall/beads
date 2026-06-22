package pool

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/dolthub/vitess/go/mysql"
)

// drainTimeout bounds how long Shutdown waits for in-flight client connections
// to finish before closing backends out from under them.
const drainTimeout = 5 * time.Second

// Server warms a Pool and serves the pooler's MySQL protocol on a unix socket
// via a vitess Listener. It owns the pool lifecycle; callers drive readiness
// and shutdown via Serve/Shutdown.
type Server struct {
	cfg      Config
	socket   string
	logf     func(string, ...any)
	pool     *Pool
	handler  *Handler
	listener *mysql.Listener
}

// ServerConfig configures a pooler Server.
type ServerConfig struct {
	// Socket is the unix-domain socket path the pooler listens on. Clients
	// (bd) connect here; it is what BEADS_DOLT_SERVER_SOCKET should point at.
	Socket string
	// Pool is the backend pool configuration.
	Pool Config
	// Logf, when non-nil, receives diagnostic log lines.
	Logf func(string, ...any)
}

// NewServer warms the backend pool and creates (but does not yet Accept) the
// listener. The socket is removed first if it already exists (stale from a dead
// pooler). On any failure the pool is torn down.
func NewServer(ctx context.Context, sc ServerConfig) (*Server, error) {
	if sc.Socket == "" {
		return nil, fmt.Errorf("pool: server socket path is required")
	}
	logf := sc.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	p, err := New(ctx, sc.Pool)
	if err != nil {
		return nil, err
	}
	p.SetLogger(logf)

	if err := os.Remove(sc.Socket); err != nil && !os.IsNotExist(err) {
		p.Close()
		return nil, fmt.Errorf("pool: remove stale socket %q: %w", sc.Socket, err)
	}

	h := NewHandler(p)
	l, err := mysql.NewListener("unix", sc.Socket, mysql.NewAuthServerNone(), h, 0, 0)
	if err != nil {
		p.Close()
		return nil, fmt.Errorf("pool: listen on unix://%s: %w", sc.Socket, err)
	}

	return &Server{
		cfg:      sc.Pool,
		socket:   sc.Socket,
		logf:     logf,
		pool:     p,
		handler:  h,
		listener: l,
	}, nil
}

// Pool returns the underlying pool (for stats/inspection).
func (s *Server) Pool() *Pool { return s.pool }

// Socket returns the listening socket path.
func (s *Server) Socket() string { return s.socket }

// Serve blocks accepting and handling client connections until Shutdown (or a
// listener error) stops it. vitess's Listener.Accept owns per-connection
// goroutines and graceful close.
func (s *Server) Serve() {
	s.logf("pooler serving on unix://%s (pool size %d)", s.socket, s.pool.Size())
	s.listener.Accept()
}

// Shutdown gracefully stops the pooler:
//  1. Close the listener so Accept returns and no new clients are admitted.
//  2. Wait (bounded) for in-flight client connections to finish their current
//     work and return their backends.
//  3. Close all backends and remove the socket file.
//
// vitess's Listener.Close stops accepting but does not itself wait for handler
// goroutines, so step 2 polls the handler's active-connection count.
func (s *Server) Shutdown() {
	s.listener.Close()

	deadline := time.Now().Add(drainTimeout)
	for s.handler.ActiveConns() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := s.handler.ActiveConns(); n > 0 {
		s.logf("pooler shutdown: %d client conn(s) still active after %s; closing backends", n, drainTimeout)
	}

	s.pool.Close()
	_ = os.Remove(s.socket)
}
