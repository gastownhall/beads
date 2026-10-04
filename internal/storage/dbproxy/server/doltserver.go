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
	"runtime"
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

// startReadyTimeout is a var so tests can shorten it.
var startReadyTimeout = 30 * time.Second

const (
	startReadyPollInterval = 50 * time.Millisecond
	startReadyDialTimeout  = 250 * time.Millisecond
)

// maxStartPortAttempts bounds how many listener ports one Start tries when
// its PortConflictPolicy lets it move off ports another process holds.
const maxStartPortAttempts = 5

// RuntimeConfigFileName is the config Start writes in the root directory when
// it runs the dolt sql-server on a different port than the operator config
// names (see SetPortConflictPolicy). It is the operator config with only
// listener.port changed, it exists only while that server runs, and the
// operator config itself is never modified. dolt sql-server has no
// command-line port override: with --config, -P/--port is silently ignored
// (verified against dolt 2.1.8 and 2.2.0), so the override has to be a file.
const RuntimeConfigFileName = "proxy-child.runtime-config.yaml"

// ErrPortInUse reports that the dolt sql-server Start launched exited before
// it was ready because another process holds its listener port. Start
// returns it, wrapped, when no PortConflictPolicy is set, when the policy
// declines, or after maxStartPortAttempts ports.
var ErrPortInUse = errors.New("dolt sql-server listener port is already in use")

// PortConflictPolicy decides whether Start may run the server on another port
// when the port configPath names (inUsePort) is held by another process. It
// returns nil to allow it, or an error saying why the port is pinned and what
// the operator can do; Start then fails with ErrPortInUse and that reason.
// Start asks once per Start, on the first conflict; ports it picked itself
// are its own to move again without asking.
type PortConflictPolicy func(configPath string, inUsePort int) error

// doltReadyLine is what dolt sql-server (go-mysql-server) logs at info level
// once its listener is bound and accepting. Seeing it on the stdout/stderr of
// the process Start launched is what proves the port belongs to that process:
// a dial alone also succeeds against another process that took the port, and
// in that case dolt logs "Port N already in use." and exits instead.
const doltReadyLine = "Server ready. Accepting connections."

// doltPortInUseTexts are the ways a dolt sql-server reports that port, its SQL
// listener port, is taken:
//   - dolt's own pre-check: "Port 3306 already in use."
//   - go-mysql-server's pre-check: "Port 127.0.0.1:3306 already in use."
//   - the bind losing the race after both checks, as Go's net error:
//     "listen tcp 127.0.0.1:3306: bind: address already in use", or on
//     Windows "...:3306: bind: Only one usage of each socket address ...".
//
// The port is part of every form, so a different listener dolt fails to bind
// (metrics, MCP) is not mistaken for the SQL port, nor is dolt's non-fatal
// unix-socket warning ("... is already in use").
func doltPortInUseTexts(port int) [][]byte {
	return [][]byte{
		[]byte(fmt.Sprintf("Port %d already in use", port)),
		[]byte(fmt.Sprintf(":%d already in use", port)),
		[]byte(fmt.Sprintf(":%d: bind: address already in use", port)),
		[]byte(fmt.Sprintf(":%d: bind: Only one usage of each socket address", port)),
	}
}

// captureBirth is procid.Capture; tests replace it to make the capture lose
// the race with a child that exits at once.
var captureBirth = procid.Capture

// exitedOnItsOwn reports whether a reaped child ended by itself rather than
// by the Kill failSpawn sends. A killed child reports exit code -1 on unix;
// Windows reports an ordinary code for a killed process, so there an exit
// cannot be told from a kill and this reports false.
func exitedOnItsOwn(ps *os.ProcessState) bool {
	return ps != nil && runtime.GOOS != "windows" && ps.ExitCode() >= 0
}

// pickFreePort returns a loopback port that was free a moment ago. Tests
// replace it. (proxy.PickFreePort is the same allocator; this package cannot
// import proxy.)
var pickFreePort = func() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
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
	// portPolicy, when set, lets Start recover from a listener port another
	// process holds. See SetPortConflictPolicy.
	portPolicy PortConflictPolicy
	// launchConfigPath is the --config the current attempt passes to dolt:
	// configPath, or the runtime config when Start moved the port.
	launchConfigPath string
	eg               *errgroup.Group
	egCtx            context.Context
	cancel           context.CancelFunc
	pid              int
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

// SetPortConflictPolicy installs fn to recover from a listener port another
// process holds. When the dolt sql-server Start launched exits because its
// port is in use and fn allows it, Start picks a free port, writes
// RuntimeConfigFileName (the config with only listener.port changed), and
// launches again from that file, up to maxStartPortAttempts ports. The
// operator config is never modified. Without a policy Start returns
// ErrPortInUse. Call it before Start.
func (s *DoltServer) SetPortConflictPolicy(fn PortConflictPolicy) {
	s.portPolicy = fn
}

func (s *DoltServer) runtimeConfigPath() string {
	return filepath.Join(s.rootDir, RuntimeConfigFileName)
}

func (s *DoltServer) Start(ctx context.Context) error {
	if s.eg != nil || s.egCtx != nil {
		return fmt.Errorf("server: DoltServer.Start: server already started")
	}
	// A runtime config left by an earlier run is stale: every Start begins
	// from the operator config's own port.
	_ = os.Remove(s.runtimeConfigPath())
	cfg, err := servercfg.YamlConfigFromFile(filesys.LocalFS, s.configPath)
	if err != nil {
		return fmt.Errorf("server: DoltServer.Start: parse config %q: %w", s.configPath, err)
	}
	s.config = cfg
	s.launchConfigPath = s.configPath
	err = s.startWithPortRecovery(ctx)
	if err != nil {
		_ = os.Remove(s.runtimeConfigPath())
	}
	return err
}

func (s *DoltServer) startWithPortRecovery(ctx context.Context) error {
	// The policy judges the operator config, so it is asked once, about the
	// port that config names. Later conflicts are on ports Start picked
	// itself, which are Start's to move again.
	operatorPort := s.config.Port()
	for attempt := 1; ; attempt++ {
		err := s.startAttempt(ctx, attempt == 1)
		if err == nil || !errors.Is(err, ErrPortInUse) {
			return err
		}
		inUse := s.config.Port()
		if s.portPolicy == nil {
			return fmt.Errorf("%w; %s", err, portInUseRemedy(s.configPath, inUse))
		}
		if attempt >= maxStartPortAttempts {
			return fmt.Errorf("%w (gave up after %d ports, the last %d; something on this host keeps taking them)", err, attempt, inUse)
		}
		if attempt == 1 {
			if perr := s.portPolicy(s.configPath, operatorPort); perr != nil {
				return fmt.Errorf("%w; not moving off port %d: %v", err, inUse, perr)
			}
		}
		if rerr := s.useRuntimePort(inUse); rerr != nil {
			return fmt.Errorf("%w; could not move off port %d: %v", err, inUse, rerr)
		}
	}
}

// portInUseRemedy says what an operator can do about a pinned port.
func portInUseRemedy(configPath string, port int) string {
	return fmt.Sprintf("free port %d, or set listener.port in %s to a free port", port, configPath)
}

// useRuntimePort writes RuntimeConfigFileName: the operator config with
// listener.port moved to a fresh port (never inUse), and makes it the config
// the next attempt launches with and the server dials.
func (s *DoltServer) useRuntimePort(inUse int) error {
	// Parse the raw bytes, not YamlConfigFromFile's env-interpolated text:
	// dolt interpolates the runtime file when it reads it, so placeholders
	// (and "$$" escapes) must reach it unexpanded, and their values never
	// land on disk.
	raw, err := os.ReadFile(s.configPath)
	if err != nil {
		return fmt.Errorf("re-read %s: %w", s.configPath, err)
	}
	yc, err := servercfg.NewYamlConfig(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", s.configPath, err)
	}
	port := inUse
	for port == inUse {
		if port, err = pickFreePort(); err != nil {
			return fmt.Errorf("pick free port: %w", err)
		}
	}
	rcfg := *yc
	rcfg.ListenerConfig.PortNumber = &port
	body := []byte("# Written by Beads: " + s.configPath + " with listener.port moved to a free port for this run\n" +
		"# because another process held " + strconv.Itoa(inUse) + ". Removed when the server stops; edit the file above, not this one.\n" +
		rcfg.String())
	cfg, err := servercfg.NewYamlConfig(body)
	if err != nil {
		return fmt.Errorf("render runtime config: %w", err)
	}
	if cfg.Port() != port {
		return fmt.Errorf("render runtime config: port %d, want %d", cfg.Port(), port)
	}
	if err := writeFileAtomic(s.runtimeConfigPath(), body); err != nil {
		return fmt.Errorf("write %s: %w", s.runtimeConfigPath(), err)
	}
	// Dial and DSN need the values dolt will see, so read the file back the
	// way dolt does, with interpolation.
	effective, err := servercfg.YamlConfigFromFile(filesys.LocalFS, s.runtimeConfigPath())
	if err != nil {
		return fmt.Errorf("re-read %s: %w", s.runtimeConfigPath(), err)
	}
	s.config = effective
	s.launchConfigPath = s.runtimeConfigPath()
	return nil
}

// writeFileAtomic writes body to path (mode 0600) through a temp file and a
// rename, so dolt never reads a partial config.
func writeFileAtomic(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
		"--config", s.launchConfigPath,
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
	//
	// The streams are a pipe into this process rather than the log file
	// itself, so a dolt sql-server whose proxy died takes SIGPIPE on its next
	// write; the proxy's shutdown and force-stop paths reap such a backend
	// anyway. WaitDelay keeps a grandchild that inherited the pipe from
	// holding cmd.Wait (and so Stop) open.
	watch := newStartupWatch(s.logFile, s.config.Port())
	cmd.Stdout = watch
	cmd.Stderr = watch
	cmd.WaitDelay = 5 * time.Second

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
	// failSpawn tears down a child that Start gave up on before handing it
	// to the supervising goroutine. A dolt sql-server that finds its port
	// taken can exit within milliseconds, before the identity capture or
	// pid record below gets to it, and then those steps fail only because
	// the child is already gone. So once the child is reaped (cmd.Wait also
	// drains its output into the watcher), its own exit decides the error:
	// ErrPortInUse when it reported its port taken, so Start's normal
	// recovery runs; "exited before ready" when it exited by itself; the
	// step's error only when the child was still alive and we killed it.
	failSpawn := func(step string, stepErr error) error {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		s.eg, s.egCtx, s.cancel = nil, nil, nil
		cancel()
		lock.Unlock()
		if watch.sawPortInUse() {
			s.pid = 0
			return fmt.Errorf("server: DoltServer.Start: %w", s.exitedBeforeReady(watch))
		}
		if exitedOnItsOwn(cmd.ProcessState) {
			code := cmd.ProcessState.ExitCode()
			s.pid = 0
			return fmt.Errorf("server: DoltServer.Start: dolt sql-server exited (status %d) before startup completed (%s: %v)", code, step, stepErr)
		}
		s.pid = 0
		return fmt.Errorf("server: DoltServer.Start: %s: %w", step, stepErr)
	}
	birth, err := captureBirth(s.pid)
	if err != nil {
		return failSpawn("capture child birth identity", err)
	}
	rootID, err := identity.RootID(s.rootDir)
	if err != nil {
		return failSpawn("resolve proxy root identity", err)
	}

	if err := pidfile.Write(s.rootDir, PIDFileName, pidfile.PidFile{
		Pid:    s.pid,
		Port:   s.config.Port(),
		Schema: pidfile.SchemaV2,
		Kind:   pidfile.KindDoltBackend,
		Birth:  string(birth),
		RootID: rootID,
	}); err != nil {
		return failSpawn("write pidfile", err)
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
// such signal and the dial alone decides, as before; that is also the escape
// hatch for a dolt whose output never carries the line (a wrapper that
// filters it, a reworded release), which otherwise fails after
// startReadyTimeout with an error naming the missing line. Either way, a
// child that exits first is reported, as ErrPortInUse when it said its port
// was taken.
//
// There is deliberately no "answered for a while, accept anyway" fallback:
// dolt only reaches its port check after its own startup, which under load
// can take longer than any grace period, and until then a foreign listener
// answers exactly like a ready server.
func (s *DoltServer) waitReady(ctx context.Context, watch *startupWatch) error {
	needReadyLine := logLevelEmitsReadyLine(s.config.LogLevel())
	deadline := time.Now().Add(startReadyTimeout)
	var lastErr error
	answered := false
	for {
		if s.egCtx.Err() != nil {
			return s.exitedBeforeReady(watch)
		}

		dctx, dcancel := context.WithTimeout(ctx, startReadyDialTimeout)
		conn, err := s.Dial(dctx)
		dcancel()
		if err == nil {
			_ = conn.Close()
			answered = true
		}
		switch {
		case err == nil && (!needReadyLine || watch.isReady()):
			if s.egCtx.Err() != nil {
				return s.exitedBeforeReady(watch)
			}
			return nil
		case err != nil:
			lastErr = err
		default:
			lastErr = nil
		}

		if time.Now().After(deadline) {
			if needReadyLine && !watch.isReady() {
				what := "and nothing answered on " + s.listenAddr()
				if answered {
					what = "although something answered on " + s.listenAddr()
				}
				return fmt.Errorf("dolt sql-server (pid %d) never logged %q within %s %s; readiness needs that line on dolt's own output: "+
					"if a wrapper filters dolt's stdout/stderr or this dolt words it differently, set log_level to warning in %s to fall back to dial-only readiness",
					s.pid, doltReadyLine, startReadyTimeout, what, s.configPath)
			}
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

func (s *DoltServer) listenAddr() string {
	if sock := s.config.Socket(); sock != "" {
		return sock
	}
	return net.JoinHostPort(s.config.Host(), strconv.Itoa(s.config.Port()))
}

// exitedBeforeReady is waitReady's error for a child that exited first. The
// supervising goroutine's cmd.Wait returns only after the output copy has
// finished, so by the time egCtx is done the watcher has seen everything the
// child wrote.
func (s *DoltServer) exitedBeforeReady(watch *startupWatch) error {
	if watch.sawPortInUse() {
		return fmt.Errorf("%w: dolt sql-server could not bind %s and exited", ErrPortInUse, s.listenAddr())
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
	if err := os.Remove(s.runtimeConfigPath()); err != nil && !os.IsNotExist(err) {
		rmErr = errors.Join(rmErr, err)
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
