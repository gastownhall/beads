package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"
	"github.com/dolthub/dolt/go/libraries/utils/filesys"
	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/sync/errgroup"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

const defaultKeepAlivePeriod = 30 * time.Second

const (
	PIDFileName  = "proxy-child.pid"
	LockFileName = "proxy-child.lock"
)

// errBackendExited is the result of the supervising goroutine when the dolt
// sql-server exits on its own with status 0 (for example dolt's graceful
// shutdown on SIGTERM). errgroup cancels egCtx only for a non-nil result, and
// Running reads egCtx, so without it a cleanly exited backend would be
// reported as running forever.
var errBackendExited = errors.New("dolt sql-server exited")

const (
	startReadyTimeout      = 30 * time.Second
	startReadyPollInterval = 50 * time.Millisecond
	startReadyDialTimeout  = 250 * time.Millisecond
)

// maxStartPortAttempts bounds how many listener ports one Start tries when a
// PortRepicker keeps moving the server off ports another process holds.
const maxStartPortAttempts = 5

// ErrPortInUse reports that the dolt sql-server Start launched exited before
// it was ready because another process holds its configured listener port.
// Start returns it, wrapped, when no PortRepicker is set, when the repicker
// declines to move the port, or after maxStartPortAttempts ports.
var ErrPortInUse = errors.New("dolt sql-server listener port is already in use")

// PortRepicker moves the server's config file off inUsePort, which another
// process holds, so the next start attempt can bind a different port. It
// rewrites the config at the DoltServer's configPath in place; Start re-reads
// it afterwards. It returns an error when the port must not be moved (for
// example a port the operator chose), which ends Start with ErrPortInUse.
type PortRepicker func(ctx context.Context, configPath string, inUsePort int) error

// doltReadyLine is what dolt sql-server (go-mysql-server) logs at info level
// once its listener is bound and accepting. Seeing it on the stdout/stderr of
// the process Start launched is what proves the port belongs to that process:
// a dial alone also succeeds against another process that took the port, and
// in that case dolt logs "Port N already in use." and exits instead.
const doltReadyLine = "Server ready. Accepting connections."

// doltPortInUseTexts are dolt's two bind failures for port: its own
// pre-check, and the kernel's error when the port is taken after that check.
// (A bare "already in use" would also match dolt's non-fatal unix-socket
// warning.)
func doltPortInUseTexts(port int) [][]byte {
	return [][]byte{
		[]byte(fmt.Sprintf("Port %d already in use", port)),
		[]byte("address already in use"),
	}
}

type DoltServer struct {
	id              string
	doltBinExec     string
	rootDir         string
	configPath      string
	database        string
	config          servercfg.ServerConfig
	keepAlivePeriod time.Duration

	logFile *os.File
	// repickPort, when set, lets Start recover from a listener port another
	// process holds. See SetPortRepicker.
	repickPort PortRepicker
	eg         *errgroup.Group
	egCtx      context.Context
	cancel     context.CancelFunc
	pid        int
}

var _ DatabaseServer = (*DoltServer)(nil)

func NewDoltServer(doltBinExec, rootDir, configPath, logFilePath string, keepAlivePeriod time.Duration, database string) (*DoltServer, error) {
	if doltBinExec == "" {
		return nil, errors.New("server: NewDoltServer: doltBinExec is required")
	}
	if rootDir == "" {
		return nil, errors.New("server: NewDoltServer: rootDir is required")
	}
	if configPath == "" {
		return nil, errors.New("server: NewDoltServer: configPath is required")
	}
	absDoltBinExec, err := filepath.Abs(doltBinExec)
	if err != nil {
		return nil, errors.New("server: NewDoltServer: failed to determine absolute path of doltBinExec")
	}
	absRootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, errors.New("server: NewDoltServer: failed to determine absolute path of rootDir")
	}
	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, errors.New("server: NewDoltServer: failed to determine absolute path of configPath")
	}
	cfg, err := servercfg.YamlConfigFromFile(filesys.LocalFS, configPath)
	if err != nil {
		return nil, fmt.Errorf("server: NewDoltServer: parse config %q: %w", configPath, err)
	}
	var logFile *os.File
	if logFilePath != "" {
		absLogFilePath, err := filepath.Abs(logFilePath)
		if err != nil {
			return nil, errors.New("server: NewDoltServer: failed to determine absolute path of logFilePath")
		}
		logFile, err = os.OpenFile(absLogFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // logFilePath is caller-derived, not user-request input
		if err != nil {
			return nil, fmt.Errorf("server: NewDoltServer: open log %q: %w", logFilePath, err)
		}
	}
	if keepAlivePeriod == 0 {
		keepAlivePeriod = defaultKeepAlivePeriod
	}
	sum := sha256.Sum256([]byte(absRootDir))
	return &DoltServer{
		id:              hex.EncodeToString(sum[:]),
		doltBinExec:     absDoltBinExec,
		rootDir:         absRootDir,
		configPath:      absConfigPath,
		database:        database,
		config:          cfg,
		keepAlivePeriod: keepAlivePeriod,
		logFile:         logFile,
	}, nil
}

func (s *DoltServer) ID(_ context.Context) string {
	return s.id
}

func (s *DoltServer) DSN(_ context.Context, database, user, password string) string {
	dsn := util.DoltServerDSN{
		User:        user,
		Password:    password,
		Database:    database,
		TLSRequired: s.config.RequireSecureTransport(),
		TLSCert:     s.config.TLSCert(),
		TLSKey:      s.config.TLSKey(),
	}
	if sock := s.config.Socket(); sock != "" {
		dsn.Socket = sock
	} else {
		dsn.Host = s.config.Host()
		dsn.Port = s.config.Port()
	}
	return dsn.String()
}

func (s *DoltServer) doltConfigure(ctx context.Context) error {
	probe := exec.CommandContext(ctx, s.doltBinExec, "config", "--global", "--get", "user.name")
	if out, err := probe.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return nil
	}
	name, email := "beads", "beads@localhost"
	if out, err := exec.CommandContext(ctx, "git", "config", "user.name").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			name = v
		}
	}
	if out, err := exec.CommandContext(ctx, "git", "config", "user.email").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			email = v
		}
	}
	if out, err := exec.CommandContext(ctx, s.doltBinExec, "config", "--global", "--add", "user.name", name).CombinedOutput(); err != nil {
		return fmt.Errorf("server: DoltServer.doltConfigure: set user.name: %w\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, s.doltBinExec, "config", "--global", "--add", "user.email", email).CombinedOutput(); err != nil {
		return fmt.Errorf("server: DoltServer.doltConfigure: set user.email: %w\n%s", err, out)
	}
	return nil
}

func (s *DoltServer) doltInit(ctx context.Context) error {
	if err := os.MkdirAll(s.rootDir, 0o755); err != nil {
		return fmt.Errorf("server: DoltServer.doltInit: mkdir %q: %w", s.rootDir, err)
	}

	cmd := exec.CommandContext(ctx, s.doltBinExec, "init")
	cmd.Dir = s.rootDir
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "already been initialized") {
			return doltserver.MarkDoltDirCompatible(s.rootDir)
		}
		return fmt.Errorf("server: DoltServer.doltInit: %w\n%s", err, out)
	}

	return doltserver.MarkDoltDirCompatible(s.rootDir)
}

var retryableDoltInitErrSubstrings = []string{
	"repository state is invalid",
}

func isRetryableDoltInitErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range retryableDoltInitErrSubstrings {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func (s *DoltServer) doltInitWithRetries(ctx context.Context) error {
	const maxRetries = 4
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 100 * time.Millisecond
	bo.MaxInterval = 1 * time.Second
	bo.MaxElapsedTime = 0

	op := func() error {
		err := s.doltInit(ctx)
		if err == nil {
			return nil
		}
		if !isRetryableDoltInitErr(err) {
			return backoff.Permanent(err)
		}
		return err
	}

	return backoff.Retry(op, backoff.WithMaxRetries(backoff.WithContext(bo, ctx), maxRetries))
}

// SetPortRepicker installs fn to recover from a listener port another process
// holds: when the dolt sql-server Start launched exits because its port is in
// use, Start calls fn, re-reads the config, and launches again, up to
// maxStartPortAttempts ports. Without one, Start returns ErrPortInUse. Call it
// before Start.
func (s *DoltServer) SetPortRepicker(fn PortRepicker) {
	s.repickPort = fn
}

func (s *DoltServer) Start(ctx context.Context) error {
	if s.eg != nil || s.egCtx != nil {
		return fmt.Errorf("server: DoltServer.Start: server already started")
	}
	for attempt := 1; ; attempt++ {
		err := s.startAttempt(ctx, attempt == 1)
		if err == nil || !errors.Is(err, ErrPortInUse) || s.repickPort == nil {
			return err
		}
		if attempt >= maxStartPortAttempts {
			return fmt.Errorf("%w (gave up after %d ports)", err, attempt)
		}
		inUse := s.config.Port()
		if rerr := s.repickPort(ctx, s.configPath, inUse); rerr != nil {
			return fmt.Errorf("%w; not moving off port %d: %v", err, inUse, rerr)
		}
		cfg, rerr := servercfg.YamlConfigFromFile(filesys.LocalFS, s.configPath)
		if rerr != nil {
			return fmt.Errorf("server: DoltServer.Start: re-read config %q after moving off port %d: %w", s.configPath, inUse, rerr)
		}
		s.config = cfg
	}
}

// startAttempt launches dolt sql-server once and waits until it is ready.
// Only the first attempt runs the dolt config/init preparation; a retry after
// a port conflict reuses it.
func (s *DoltServer) startAttempt(ctx context.Context, prepare bool) error {
	lock, err := util.TryLock(filepath.Join(s.rootDir, LockFileName))
	if err != nil {
		return fmt.Errorf("server: DoltServer.Start: acquire %s: %w", LockFileName, err)
	}

	if prepare {
		if err := s.doltConfigure(ctx); err != nil {
			lock.Unlock()
			return err
		}

		if err := s.doltInitWithRetries(ctx); err != nil {
			lock.Unlock()
			return err
		}
	}

	args := []string{
		"sql-server",
		"--config", s.configPath,
	}

	managedCtx, cancel := context.WithCancel(context.Background())
	eg, egCtx := errgroup.WithContext(managedCtx)
	s.eg = eg
	s.egCtx = egCtx
	s.cancel = cancel

	cmd := exec.CommandContext(managedCtx, s.doltBinExec, args...)
	cmd.Dir = s.rootDir
	cmd.Stdin = nil
	// Both streams go through one watcher, which forwards to the log file
	// and spots the ready line and a port conflict for waitReady.
	watch := newStartupWatch(s.logFile, s.config.Port())
	cmd.Stdout = watch
	cmd.Stderr = watch

	// The proxied server runs CALL DOLT_PUSH/FETCH in-process; see
	// doltserver.ServerSpawnEnv for the guards it needs (GH#4272).
	cmd.Env = doltserver.ServerSpawnEnv()

	if err := cmd.Start(); err != nil {
		s.eg, s.egCtx, s.cancel = nil, nil, nil
		cancel()
		lock.Unlock()
		return fmt.Errorf("server: DoltServer.Start: spawn dolt: %w", err)
	}

	s.pid = cmd.Process.Pid
	birth, err := procid.Capture(s.pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		s.eg, s.egCtx, s.cancel, s.pid = nil, nil, nil, 0
		cancel()
		lock.Unlock()
		return fmt.Errorf("server: DoltServer.Start: capture child birth identity: %w", err)
	}
	rootID, err := identity.RootID(s.rootDir)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		s.eg, s.egCtx, s.cancel, s.pid = nil, nil, nil, 0
		cancel()
		lock.Unlock()
		return fmt.Errorf("server: DoltServer.Start: resolve proxy root identity: %w", err)
	}

	if err := pidfile.Write(s.rootDir, PIDFileName, pidfile.PidFile{
		Pid:    s.pid,
		Port:   s.config.Port(),
		Schema: pidfile.SchemaV2,
		Kind:   pidfile.KindDoltBackend,
		Birth:  string(birth),
		RootID: rootID,
	}); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		s.eg, s.egCtx, s.cancel, s.pid = nil, nil, nil, 0
		cancel()
		lock.Unlock()
		return fmt.Errorf("server: DoltServer.Start: write pidfile: %w", err)
	}

	eg.Go(func() error {
		defer lock.Unlock()
		if err := cmd.Wait(); err != nil {
			return err
		}
		return errBackendExited
	})

	if err := s.waitReady(ctx, watch); err != nil {
		cancel()
		_ = s.eg.Wait()
		s.eg, s.egCtx, s.cancel, s.pid = nil, nil, nil, 0
		_ = pidfile.Remove(s.rootDir, PIDFileName)
		return fmt.Errorf("server: DoltServer.Start: %w", err)
	}
	return nil
}

// waitReady waits until the dolt sql-server this Start launched is accepting
// connections on its configured listener.
//
// A successful dial is not enough on its own: the port is chosen before dolt
// binds it, and if another process takes it in between, the dial reaches that
// process while dolt fails to bind and exits. So when the config's log level
// lets dolt log its ready line (info or more verbose, which every
// Beads-generated config uses), the dial only counts after that line has
// appeared on this child's own output. Under a quieter log level there is no
// such signal and the dial alone decides, as before. Either way, a child that
// exits first is reported, as ErrPortInUse when it said its port was taken.
func (s *DoltServer) waitReady(ctx context.Context, watch *startupWatch) error {
	needReadyLine := logLevelEmitsReadyLine(s.config.LogLevel())
	deadline := time.Now().Add(startReadyTimeout)
	var lastErr error
	for {
		if s.egCtx.Err() != nil {
			return s.exitedBeforeReady(watch)
		}

		if !needReadyLine || watch.isReady() {
			dctx, dcancel := context.WithTimeout(ctx, startReadyDialTimeout)
			conn, err := s.Dial(dctx)
			dcancel()
			if err == nil {
				_ = conn.Close()
				if s.egCtx.Err() != nil {
					return s.exitedBeforeReady(watch)
				}
				return nil
			}
			lastErr = err
		} else {
			lastErr = fmt.Errorf("dolt sql-server has not logged %q", doltReadyLine)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("listener not ready after %s: %w", startReadyTimeout, lastErr)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.egCtx.Done():
			return s.exitedBeforeReady(watch)
		case <-watch.readyCh():
		case <-time.After(startReadyPollInterval):
		}
	}
}

// exitedBeforeReady is waitReady's error for a child that exited first. The
// supervising goroutine's cmd.Wait returns only after the output copy has
// finished, so by the time egCtx is done the watcher has seen everything the
// child wrote.
func (s *DoltServer) exitedBeforeReady(watch *startupWatch) error {
	if watch.sawPortInUse() {
		return fmt.Errorf("%w: dolt sql-server could not bind %s:%d and exited", ErrPortInUse, s.config.Host(), s.config.Port())
	}
	return errors.New("dolt sql-server exited before listener became ready")
}

// logLevelEmitsReadyLine reports whether dolt logs doltReadyLine (an info
// message) at level.
func logLevelEmitsReadyLine(level servercfg.LogLevel) bool {
	switch level {
	case servercfg.LogLevel_Trace, servercfg.LogLevel_Debug, servercfg.LogLevel_Info:
		return true
	}
	return false
}

// startupWatch receives the dolt sql-server's stdout and stderr. It forwards
// everything to the log file (if any) and, until the ready line shows up,
// scans the output for that line and for a port conflict.
type startupWatch struct {
	dst io.Writer

	inUse [][]byte

	mu        sync.Mutex
	tail      []byte
	scanning  bool
	portInUse bool
	ready     chan struct{}
}

// startupWatchTail is how much recent output the watcher keeps for matching,
// so a marker split across two writes is still found.
const startupWatchTail = 4096

func newStartupWatch(logFile *os.File, port int) *startupWatch {
	w := &startupWatch{inUse: doltPortInUseTexts(port), scanning: true, ready: make(chan struct{})}
	if logFile != nil {
		w.dst = logFile
	}
	return w
}

// Write never fails: a log file that cannot be written must not break the
// pipe the dolt sql-server writes to.
func (w *startupWatch) Write(p []byte) (int, error) {
	if w.dst != nil {
		_, _ = w.dst.Write(p)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.scanning {
		return len(p), nil
	}
	w.tail = append(w.tail, p...)
	for _, text := range w.inUse {
		if bytes.Contains(w.tail, text) {
			w.portInUse = true
		}
	}
	if bytes.Contains(w.tail, []byte(doltReadyLine)) {
		w.scanning = false
		w.tail = nil
		close(w.ready)
		return len(p), nil
	}
	if over := len(w.tail) - startupWatchTail; over > 0 {
		w.tail = append(w.tail[:0], w.tail[over:]...)
	}
	return len(p), nil
}

func (w *startupWatch) readyCh() <-chan struct{} { return w.ready }

func (w *startupWatch) isReady() bool {
	select {
	case <-w.ready:
		return true
	default:
		return false
	}
}

func (w *startupWatch) sawPortInUse() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.portInUse
}

func (s *DoltServer) Stop(ctx context.Context) error {
	gcErr := s.runShutdownGC(ctx)
	if gcErr != nil {
		gcErr = fmt.Errorf("server: DoltServer.Stop: %w", gcErr)
	}

	if s.cancel != nil {
		s.cancel()
	}
	var waitErr error
	if s.eg != nil {
		waitErr = s.eg.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) || errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, errBackendExited) {
			waitErr = nil
		}
	}
	if waitErr != nil {
		waitErr = fmt.Errorf("server: DoltServer.Stop: %w", waitErr)
	}
	var closeErr error
	if s.logFile != nil {
		closeErr = s.logFile.Close()
		s.logFile = nil
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("server: DoltServer.Stop: close log: %w", closeErr)
	}
	var rmErr error
	if s.pid != 0 {
		rmErr = pidfile.Remove(s.rootDir, PIDFileName)
		s.pid = 0
	}
	if rmErr != nil {
		rmErr = fmt.Errorf("server: DoltServer.Stop: remove pidfile: %w", rmErr)
	}
	return errors.Join(gcErr, waitErr, closeErr, rmErr)
}

func (s *DoltServer) runShutdownGC(ctx context.Context) (retErr error) {
	if s.database == "" || !s.Running(ctx) {
		return nil
	}
	db, err := sql.Open("mysql", s.DSN(ctx, s.database, "root", ""))
	if err != nil {
		return fmt.Errorf("open gc connection: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, db.Close()) }()

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire gc connection: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, conn.Close()) }()

	if err := versioncontrolops.DoltGC(ctx, conn); err != nil {
		retErr = errors.Join(retErr, err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_STATS_GC()"); err != nil {
		retErr = errors.Join(retErr, fmt.Errorf("dolt_stats_gc: %w", err))
	}
	return retErr
}

func (s *DoltServer) Running(_ context.Context) bool {
	if s.egCtx == nil {
		return false
	}
	return s.egCtx.Err() == nil
}

func (s *DoltServer) Dial(ctx context.Context) (net.Conn, error) {
	network, addr := "tcp", net.JoinHostPort(s.config.Host(), strconv.Itoa(s.config.Port()))
	if sock := s.config.Socket(); sock != "" {
		network, addr = "unix", sock
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("server: DoltServer.Dial: %w", err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(s.keepAlivePeriod)
	}
	return conn, nil
}
