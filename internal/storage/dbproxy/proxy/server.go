package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v4"
	"golang.org/x/sync/errgroup"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

const IdleTimeoutNever time.Duration = -1

type ProxyOpts struct {
	RootDir     string
	Port        int
	IdleTimeout time.Duration
	Server      server.DatabaseServer
	// StopEpoch is the proxy stop epoch the spawning parent observed under
	// proxy.lock before forking this proxy. ListenAndServe re-reads the
	// epoch immediately before publishing proxy.pid and aborts if it has
	// advanced, so a slow backend start cannot outlast a concurrent
	// `bd dolt stop` and publish a running proxy after the stop returned.
	// Empty means "no stop had ever run", which readStopEpoch also reports
	// for a missing epoch file, so the zero value stays correct for direct
	// (non-forked) callers such as tests.
	StopEpoch string
	// Stats is optional. When non-nil, the proxy records per-event counters
	// against it; tests use Snapshot() to assert. Production code should
	// leave this nil.
	Stats *Stats
}

type proxyServer struct {
	rootDir     string
	port        int
	idleTimeout time.Duration
	server      server.DatabaseServer
	stats       *Stats
	stopEpoch   string

	logger      *log.Logger
	listener    net.Listener
	activeConns atomic.Int64
	conns       errgroup.Group
}

const (
	PIDFileName  = "proxy.pid"
	LogFileName  = "proxy.log"
	LockFileName = "proxy.lock"
)

// LockHeldExitCode is the exit code a child proxy should use when
// ListenAndServe returns ErrLockHeld. The spawning parent treats this
// (EX_TEMPFAIL) as "lost the spawn race" and retries via readAndDial.
const LockHeldExitCode = 75

// ErrLockHeld is returned from ListenAndServe when another proxy already
// holds proxy.lock for the same rootDir. It is a normal "lost the race"
// outcome, not a failure: callers spawned as children should map it to
// LockHeldExitCode and exit cleanly.
var ErrLockHeld = errors.New("proxy lock held by another proxy on this rootDir")

const (
	serverReadyTimeout     = 30 * time.Second
	readyDialTimeout       = 2 * time.Second
	readyInitialBackoff    = 50 * time.Millisecond
	readyMaxBackoff        = 1 * time.Second
	idleWatcherMinInterval = 1 * time.Second
	backendStopTimeout     = 5 * time.Minute
	tcpKeepAlivePeriod     = 30 * time.Second
)

var errIdleTimeout = errors.New("idle timeout reached")

func NewProxyServer(opts ProxyOpts) *proxyServer {
	return &proxyServer{
		rootDir:     opts.RootDir,
		port:        opts.Port,
		idleTimeout: opts.IdleTimeout,
		server:      opts.Server,
		stats:       opts.Stats,
		stopEpoch:   opts.StopEpoch,
	}
}

func (p *proxyServer) tracef(format string, args ...any) {
	p.logger.Printf(format, args...)
}

func (p *proxyServer) ListenAndServe(parentCtx context.Context) error {
	lock, err := util.TryLock(filepath.Join(p.rootDir, LockFileName))
	if err != nil {
		if lockfile.IsLocked(err) {
			return ErrLockHeld
		}
		return fmt.Errorf("acquire %s: %w", LockFileName, err)
	}
	// proxy.lock is held for the proxy's whole lifetime, but a doomed start
	// must be able to release it early (before its backend teardown) without
	// the deferred release double-unlocking. Only the main goroutine touches
	// this.
	lockHeld := true
	releaseLock := func() {
		if lockHeld {
			lockHeld = false
			lock.Unlock()
		}
	}
	defer releaseLock()
	if err := clearSpawnMarkerAfterLock(p.rootDir); err != nil {
		return fmt.Errorf("clear proxy spawn marker: %w", err)
	}

	// Fast-abort: a concurrent `bd dolt stop` advances the stop epoch before
	// waiting (briefly) for proxy.lock, so an epoch that moved between the
	// spawning parent's read and this child taking the lock dooms this start.
	// Abort before opening any listener or booting the backend: the stopper
	// then observes a free lock within milliseconds instead of after a full
	// doomed boot-and-teardown cycle.
	if changed, err := stopEpochChanged(p.rootDir, p.stopEpoch); err != nil {
		return fmt.Errorf("check proxy stop epoch after acquiring %s: %w", LockFileName, err)
	} else if changed {
		return fmt.Errorf("%w for %s: stop epoch advanced before startup", errStartInterrupted, p.rootDir)
	}

	logPath := filepath.Join(p.rootDir, LogFileName)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- logPath is derived from operator-supplied config, not untrusted request input
	if err != nil {
		return fmt.Errorf("open proxy log %q: %w", logPath, err)
	}
	p.logger = log.New(f, "[proxy] ", log.LstdFlags|log.Lmicroseconds)
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// Install signal handlers BEFORE Listen. Without this, Go's default
	// SIGTERM action terminates the process during the startup window
	// (Listen, pidfile write, backend Start, readiness wait), bypassing all
	// deferred cleanup including RemoveDatabaseProxyPidFile.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	var sigReceived atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
		case <-sigCh:
			sigReceived.Store(true)
			p.stats.IncSignalReceived()
			cancel()
		}
	}()

	// Watch the stop epoch across the whole startup window (listen, backend
	// Start, readiness wait). A stop that begins mid-boot advances the epoch
	// first and then waits only shutdownConfirmDeadline for proxy.lock, which
	// this child holds throughout the boot; without a watcher the child would
	// notice the doomed start only at the pre-publish fence, potentially tens
	// of seconds later. Canceling ctx aborts the backend Start / ready wait
	// within about one poll interval. Transient epoch read errors are ignored
	// here; the pre-publish fence remains the authoritative check.
	epochWatchCtx, epochWatchCancel := context.WithCancel(context.Background())
	epochWatchDone := make(chan struct{})
	go func() {
		defer close(epochWatchDone)
		ticker := time.NewTicker(openPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-epochWatchCtx.Done():
				return
			case <-ticker.C:
				changed, err := stopEpochChanged(p.rootDir, p.stopEpoch)
				if err != nil {
					continue
				}
				if changed {
					cancel()
					return
				}
			}
		}
	}()
	stopEpochWatch := func() {
		epochWatchCancel()
		<-epochWatchDone
	}
	defer stopEpochWatch()

	// abortInterruptedStart tears down a doomed start whose stop epoch
	// advanced mid-boot. The interrupting stop is polling proxy.lock under a
	// budget (shutdownConfirmDeadline) far smaller than a backend stop can
	// take, and nothing has been published, so release the lock BEFORE the
	// backend teardown instead of starving the stopper into its timeout.
	abortInterruptedStart := func() error {
		p.stats.IncBackendStop()
		releaseLock()
		_ = stopBackendBounded(p.server)
		return fmt.Errorf("%w for %s: stop epoch advanced during startup", errStartInterrupted, p.rootDir)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", p.port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	p.listener = ln
	defer func() { _ = ln.Close() }()
	p.stats.IncListenAndServe()
	dataPort, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("proxy: unexpected data listener address %T", ln.Addr())
	}

	if _, err := identity.WriteSecret(p.rootDir); err != nil {
		return fmt.Errorf("write proxy secret: %w", err)
	}

	var identMu sync.RWMutex
	identReply := identity.IdentReply{
		Schema:   pidfile.SchemaV2,
		Role:     pidfile.KindProxy,
		DataPort: dataPort.Port,
	}
	control, err := startControl(p.rootDir, func() identity.IdentReply {
		identMu.RLock()
		defer identMu.RUnlock()
		return identReply
	})
	if err != nil {
		return fmt.Errorf("start control listener: %w", err)
	}
	defer func() { _ = control.Close() }()

	p.stats.IncBackendStart()
	if err := p.server.Start(ctx); err != nil {
		// Start failed with no backend left running (Start cleans up its own
		// failure), so there is no teardown to move off the lock; classifying
		// the epoch-watcher cancellation just keeps the child's exit reason
		// precise for the spawning parent.
		if changed, cerr := stopEpochChanged(p.rootDir, p.stopEpoch); cerr == nil && changed {
			return fmt.Errorf("%w for %s: stop epoch advanced during backend start (%v)", errStartInterrupted, p.rootDir, err)
		}
		return fmt.Errorf("start database server: %w", err)
	}

	if err := waitForServerReady(ctx, p.server, serverReadyTimeout); err != nil {
		if changed, cerr := stopEpochChanged(p.rootDir, p.stopEpoch); cerr == nil && changed {
			return abortInterruptedStart()
		}
		p.stats.IncBackendStop()
		_ = stopBackendBounded(p.server)
		return fmt.Errorf("database server not ready: %w", err)
	}
	birth, err := procid.Capture(os.Getpid())
	if err != nil {
		p.stats.IncBackendStop()
		_ = stopBackendBounded(p.server)
		return fmt.Errorf("capture proxy birth identity: %w", err)
	}
	rootID, err := identity.RootID(p.rootDir)
	if err != nil {
		p.stats.IncBackendStop()
		_ = stopBackendBounded(p.server)
		return fmt.Errorf("resolve proxy root identity: %w", err)
	}
	upstreamID := p.server.ID(ctx)
	identMu.Lock()
	identReply.RootID = rootID
	identReply.UpstreamID = upstreamID
	identReply.PID = os.Getpid()
	identReply.Birth = string(birth)
	identReply.ControlPort = control.Port()
	identMu.Unlock()

	// Last fence before publishing: the spawn marker was cleared when this
	// process took proxy.lock, so a `bd dolt stop` that began during a slow
	// backend start has no record of this attempt. It did advance the stop
	// epoch first, so re-check it here and abort instead of publishing a
	// running proxy after that stop returned. The startup epoch watcher is
	// stopped (synchronously) first: past this fence a stop finds the
	// published proxy.pid and stops the proxy through it, so a watcher
	// cancellation must not race the publish.
	stopEpochWatch()
	if changed, err := stopEpochChanged(p.rootDir, p.stopEpoch); err != nil {
		p.stats.IncBackendStop()
		_ = stopBackendBounded(p.server)
		return fmt.Errorf("re-check proxy stop epoch before publish: %w", err)
	} else if changed {
		return abortInterruptedStart()
	}

	if err := pidfile.Write(p.rootDir, PIDFileName, pidfile.PidFile{
		Pid:         os.Getpid(),
		Port:        dataPort.Port,
		UpstreamID:  upstreamID,
		Schema:      pidfile.SchemaV2,
		Kind:        pidfile.KindProxy,
		Birth:       string(birth),
		RootID:      rootID,
		ControlPort: control.Port(),
	}); err != nil {
		p.stats.IncBackendStop()
		_ = stopBackendBounded(p.server)
		return fmt.Errorf("write pid file: %w", err)
	}
	defer func() { _ = pidfile.Remove(p.rootDir, PIDFileName) }()

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		<-gctx.Done()
		_ = p.listener.Close()
		_ = control.Close()
		return nil
	})
	g.Go(func() error { return p.idleWatcher(gctx) })
	g.Go(func() error { return p.acceptLoop(gctx) })

	runErr := g.Wait()
	_ = p.conns.Wait()
	p.stats.IncBackendStop()
	stopErr := stopBackendBounded(p.server)
	if stopErr != nil {
		stopErr = fmt.Errorf("stop database server: %w", stopErr)
	}
	if errors.Is(runErr, errIdleTimeout) || sigReceived.Load() {
		runErr = nil
	}
	return errors.Join(runErr, stopErr)
}

func stopBackendBounded(s server.DatabaseServer) error {
	ctx, cancel := context.WithTimeout(context.Background(), backendStopTimeout)
	defer cancel()
	return s.Stop(ctx)
}

func (p *proxyServer) idleWatcher(ctx context.Context) error {
	if p.idleTimeout <= 0 {
		<-ctx.Done()
		return nil
	}
	interval := p.idleTimeout / 4
	if interval < idleWatcherMinInterval {
		interval = idleWatcherMinInterval
	}
	p.tracef("idleWatcher start (timeout=%s, tick=%s)", p.idleTimeout, interval)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var idleSince time.Time
	for {
		select {
		case <-ctx.Done():
			p.tracef("idleWatcher exit (ctx done)")
			return nil
		case <-tick.C:
			if n := p.activeConns.Load(); n > 0 {
				if !idleSince.IsZero() {
					p.tracef("idleWatcher cleared (active=%d)", n)
					idleSince = time.Time{}
				}
				continue
			}
			if idleSince.IsZero() {
				p.tracef("idleWatcher armed")
				idleSince = time.Now()
				continue
			}
			if time.Since(idleSince) >= p.idleTimeout {
				p.tracef("idleWatcher expired after %s, shutting down", p.idleTimeout)
				p.stats.IncIdleTimeout()
				return errIdleTimeout
			}
		}
	}
}

func (p *proxyServer) acceptLoop(ctx context.Context) error {
	p.tracef("acceptLoop start (addr=%s)", p.listener.Addr())
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				p.tracef("acceptLoop exit (ctx=%v)", ctx.Err())
				return nil
			}
			// Surface non-shutdown accept errors to the errgroup so the
			// proxy fails fast instead of busy-looping. Specific errors that
			// warrant retry (e.g. transient EMFILE under load) can be added
			// here as the need arises.
			p.tracef("acceptLoop error: %v", err)
			p.stats.IncAcceptError()
			return fmt.Errorf("accept: %w", err)
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(tcpKeepAlivePeriod)
		}
		p.tracef("acceptLoop accepted (remote=%s)", conn.RemoteAddr())
		p.stats.IncAccept()
		p.conns.Go(func() error {
			return p.handleConn(ctx, conn)
		})
	}
}

func (p *proxyServer) handleConn(ctx context.Context, client net.Conn) error {
	addr := client.RemoteAddr()
	p.tracef("handleConn(%s) start", addr)
	p.activeConns.Add(1)
	defer func() {
		p.activeConns.Add(-1)
		p.tracef("handleConn(%s) end (active=%d)", addr, p.activeConns.Load())
	}()

	p.stats.IncBackendDialAttempt()
	backend, err := p.server.Dial(ctx)
	if err != nil {
		p.tracef("handleConn(%s) backend dial error: %v", addr, err)
		p.stats.IncBackendDialError()
		_ = client.Close()
		return err
	}
	p.tracef("handleConn(%s) backend dial ok", addr)
	p.stats.IncBackendDialSuccess()
	p.stats.IncHandledConn()

	done := make(chan struct{})
	var doneOnce sync.Once
	finish := func() { doneOnce.Do(func() { close(done) }) }

	var g errgroup.Group
	g.Go(func() error {
		select {
		case <-ctx.Done():
			p.tracef("handleConn(%s) ctx canceled, force-closing", addr)
			_ = client.Close()
			_ = backend.Close()
		case <-done:
		}
		return nil
	})
	g.Go(func() error {
		defer finish()
		defer func() { _ = backend.Close() }()
		defer func() { _ = client.Close() }()
		n, err := io.Copy(backend, client)
		p.stats.AddBytesClientToBackend(n)
		p.tracef("handleConn(%s) client→backend done (n=%d, err=%v)", addr, n, err)
		return err
	})
	g.Go(func() error {
		defer finish()
		defer func() { _ = backend.Close() }()
		defer func() { _ = client.Close() }()
		n, err := io.Copy(client, backend)
		p.stats.AddBytesBackendToClient(n)
		p.tracef("handleConn(%s) backend→client done (n=%d, err=%v)", addr, n, err)
		return err
	})
	return g.Wait()
}

func waitForServerReady(ctx context.Context, s server.DatabaseServer, timeout time.Duration) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = readyInitialBackoff
	bo.MaxInterval = readyMaxBackoff
	bo.MaxElapsedTime = timeout

	return backoff.Retry(func() error {
		if !s.Running(ctx) {
			return errors.New("database server not running")
		}
		dialCtx, cancel := context.WithTimeout(ctx, readyDialTimeout)
		defer cancel()
		conn, err := s.Dial(dialCtx)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	}, backoff.WithContext(bo, ctx))
}
