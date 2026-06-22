package proxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pool"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

// PooledOpts configures a pooledServer.
type PooledOpts struct {
	// RootDir holds proxy.lock / proxy.pid / proxy.log (shared with the
	// passthrough proxy so only one server per rootDir runs at a time).
	RootDir string
	// Socket is the unix-domain socket the pooler listens on for clients (bd).
	Socket string
	// DoltAddr is the external Dolt sql-server TCP address (host:port). Ignored
	// if DoltSocket is set.
	DoltAddr string
	// DoltSocket is an optional unix socket path for the external Dolt server.
	DoltSocket string
	// DoltUser is the MySQL user backends authenticate as (default "root").
	DoltUser string
	// PoolSize is the number of persistent backends. Clamped to >= pool.MinSize.
	PoolSize int
	// IdleTimeout shuts the pooler down after this long with no client
	// connections (0 disables, matching the passthrough proxy).
	IdleTimeout time.Duration
}

// pooledServer is the QUERY-LEVEL serving mode of the dbproxy. It reuses the
// passthrough proxy's lifecycle scaffolding — the proxy.lock flock, proxy.pid
// pidfile, proxy.log, SIGTERM/SIGINT handling, the LockHeldExitCode contract,
// and the idle-shutdown watcher — but swaps the serve core for a pool.Server
// (vitess Listener + connection pool) listening on a unix socket.
type pooledServer struct {
	opts   PooledOpts
	logger *log.Logger
}

// NewPooledServer constructs a pooledServer.
func NewPooledServer(opts PooledOpts) *pooledServer {
	return &pooledServer{opts: opts}
}

// ListenAndServe acquires the rootDir lock, warms the pool, writes the pidfile
// (recording the socket path), serves clients, and shuts down on idle timeout
// or signal. It returns ErrLockHeld if another server already owns the rootDir,
// so a spawned child can map it to LockHeldExitCode exactly like the
// passthrough proxy.
func (s *pooledServer) ListenAndServe(parentCtx context.Context) error {
	lock, err := util.TryLock(filepath.Join(s.opts.RootDir, LockFileName))
	if err != nil {
		if lockfile.IsLocked(err) {
			return ErrLockHeld
		}
		return fmt.Errorf("acquire %s: %w", LockFileName, err)
	}
	defer lock.Unlock()

	logPath := filepath.Join(s.opts.RootDir, LogFileName)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- logPath derived from operator config, not request input
	if err != nil {
		return fmt.Errorf("open proxy log %q: %w", logPath, err)
	}
	s.logger = log.New(f, "[pool-proxy] ", log.LstdFlags|log.Lmicroseconds)
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// Install signal handlers BEFORE warmup so SIGTERM during startup still
	// runs deferred cleanup (pidfile + socket removal).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	var sigReceived atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
		case <-sigCh:
			sigReceived.Store(true)
			cancel()
		}
	}()

	srv, err := pool.NewServer(ctx, pool.ServerConfig{
		Socket: s.opts.Socket,
		Pool: pool.Config{
			Addr:   s.opts.DoltAddr,
			Socket: s.opts.DoltSocket,
			User:   s.opts.DoltUser,
			Size:   s.opts.PoolSize,
		},
		Logf: s.logger.Printf,
	})
	if err != nil {
		return fmt.Errorf("warm pooler: %w", err)
	}

	if err := pidfile.Write(s.opts.RootDir, PIDFileName, pidfile.PidFile{
		Pid:    os.Getpid(),
		Socket: s.opts.Socket,
	}); err != nil {
		srv.Shutdown()
		return fmt.Errorf("write pid file: %w", err)
	}
	defer func() { _ = pidfile.Remove(s.opts.RootDir, PIDFileName) }()

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		srv.Serve() // blocks until srv.Shutdown closes the listener
	}()

	idleErr := s.idleWatch(ctx, srv)

	srv.Shutdown()
	<-serveDone

	if errors.Is(idleErr, errIdleTimeout) || sigReceived.Load() {
		return nil
	}
	return idleErr
}

// idleWatch shuts the pooler down after IdleTimeout with no active client
// connections. It mirrors proxyServer.idleWatcher but reads the live
// connection count from the pool handler. Returns errIdleTimeout on expiry, or
// nil when the context is canceled.
func (s *pooledServer) idleWatch(ctx context.Context, srv *pool.Server) error {
	if s.opts.IdleTimeout <= 0 {
		<-ctx.Done()
		return nil
	}
	interval := s.opts.IdleTimeout / 4
	if interval < idleWatcherMinInterval {
		interval = idleWatcherMinInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var idleSince time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if srv.ActiveConns() > 0 {
				idleSince = time.Time{}
				continue
			}
			if idleSince.IsZero() {
				idleSince = time.Now()
				continue
			}
			if time.Since(idleSince) >= s.opts.IdleTimeout {
				return errIdleTimeout
			}
		}
	}
}
