package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"
	"github.com/dolthub/dolt/go/libraries/utils/filesys"
	"golang.org/x/sync/errgroup"

	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

const defaultKeepAlivePeriod = 30 * time.Second

const (
	PIDFileName  = "proxy-child.pid"
	LockFileName = "proxy-child.lock"
)

const (
	startReadyTimeout      = 30 * time.Second
	startReadyPollInterval = 50 * time.Millisecond
	startReadyDialTimeout  = 250 * time.Millisecond
)

type DoltServer struct {
	id              string
	doltBinExec     string
	rootDir         string
	configPath      string
	config          servercfg.ServerConfig
	keepAlivePeriod time.Duration

	logFile *os.File
	eg      *errgroup.Group
	egCtx   context.Context
	cancel  context.CancelFunc
	pid     int
}

var _ DatabaseServer = (*DoltServer)(nil)

func NewDoltServer(doltBinExec, rootDir, configPath, logFilePath string, keepAlivePeriod time.Duration) (*DoltServer, error) {
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
			return nil
		}
		return fmt.Errorf("server: DoltServer.doltInit: %w\n%s", err, out)
	}

	return nil
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

func (s *DoltServer) Start(ctx context.Context) error {
	if s.eg != nil || s.egCtx != nil {
		return fmt.Errorf("server: DoltServer.Start: server already started")
	}

	lock, err := util.TryLock(filepath.Join(s.rootDir, LockFileName))
	if err != nil {
		return fmt.Errorf("server: DoltServer.Start: acquire %s: %w", LockFileName, err)
	}

	if err := s.doltConfigure(ctx); err != nil {
		lock.Unlock()
		return err
	}

	if err := s.doltInitWithRetries(ctx); err != nil {
		lock.Unlock()
		return err
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
	if s.logFile != nil {
		cmd.Stdout = s.logFile
		cmd.Stderr = s.logFile
	}

	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		s.eg, s.egCtx, s.cancel = nil, nil, nil
		cancel()
		lock.Unlock()
		return fmt.Errorf("server: DoltServer.Start: spawn dolt: %w", err)
	}

	s.pid = cmd.Process.Pid

	if err := pidfile.Write(s.rootDir, PIDFileName, pidfile.PidFile{
		Pid:  s.pid,
		Port: s.config.Port(),
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
		return cmd.Wait()
	})

	if err := s.waitReady(ctx); err != nil {
		cancel()
		_ = s.eg.Wait()
		s.eg, s.egCtx, s.cancel, s.pid = nil, nil, nil, 0
		_ = pidfile.Remove(s.rootDir, PIDFileName)
		return fmt.Errorf("server: DoltServer.Start: %w", err)
	}
	return nil
}

func (s *DoltServer) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(startReadyTimeout)
	for {
		if s.egCtx.Err() != nil {
			return errors.New("dolt sql-server exited before listener became ready")
		}

		dctx, dcancel := context.WithTimeout(ctx, startReadyDialTimeout)
		conn, err := s.Dial(dctx)
		dcancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("listener not ready after %s: %w", startReadyTimeout, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.egCtx.Done():
			return errors.New("dolt sql-server exited before listener became ready")
		case <-time.After(startReadyPollInterval):
		}
	}
}

func (s *DoltServer) Stop(_ context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	var waitErr error
	if s.eg != nil {
		waitErr = s.eg.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) || errors.Is(waitErr, context.Canceled) {
			waitErr = nil
		}
	}
	var closeErr error
	if s.logFile != nil {
		closeErr = s.logFile.Close()
		s.logFile = nil
	}
	var rmErr error
	if s.pid != 0 {
		rmErr = pidfile.Remove(s.rootDir, PIDFileName)
		s.pid = 0
	}
	if waitErr != nil {
		return fmt.Errorf("server: DoltServer.Stop: %w", waitErr)
	}
	if closeErr != nil {
		return fmt.Errorf("server: DoltServer.Stop: close log: %w", closeErr)
	}
	if rmErr != nil {
		return fmt.Errorf("server: DoltServer.Stop: remove pidfile: %w", rmErr)
	}
	return nil
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
