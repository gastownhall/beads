package proxy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

type ErrUpstreamMismatch struct {
	RootDir string
	Want    string
	Have    string
}

func (e *ErrUpstreamMismatch) Error() string {
	return fmt.Sprintf("proxy at %s fronts upstream %s, not %s", e.RootDir, e.Have, e.Want)
}

func IsUpstreamMismatch(err error) bool {
	var m *ErrUpstreamMismatch
	return errors.As(err, &m)
}

func intendedUpstreamID(opts OpenOpts) string {
	if opts.Backend == BackendExternal {
		return server.ExternalDoltServerID(opts.External)
	}
	return ""
}

type Endpoint struct {
	Host string
	Port int
}

func (e Endpoint) Address() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

type OpenOpts struct {
	IdleTimeout    time.Duration
	Backend        Backend
	ConfigFilePath string
	LogFilePath    string
	DoltBinPath    string
	Database       string
	Port           int
	External       configfile.ExternalDoltConfig
}

const (
	openDeadline          = 15 * time.Second
	spawnReadyHardTimeout = 2 * time.Minute
	openPollInterval      = 100 * time.Millisecond
)

var ResolveExecutable = os.Executable

func PickFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

func GetCreateDatabaseProxyServerEndpoint(rootDir string, opts OpenOpts) (Endpoint, error) {
	if err := opts.Backend.Validate(); err != nil {
		return Endpoint{}, fmt.Errorf("OpenOpts.Backend: %w", err)
	}
	if opts.Port != 0 && (opts.Port < 1 || opts.Port > 65535) {
		return Endpoint{}, fmt.Errorf("OpenOpts.Port: must be 0 or 1-65535, got %d", opts.Port)
	}
	switch opts.Backend {
	case BackendLocalServer:
		if opts.ConfigFilePath == "" {
			return Endpoint{}, fmt.Errorf("OpenOpts.ConfigFilePath is required for backend %q", opts.Backend)
		}
		if opts.LogFilePath == "" {
			return Endpoint{}, fmt.Errorf("OpenOpts.LogFilePath is required for backend %q", opts.Backend)
		}
		if opts.DoltBinPath == "" {
			return Endpoint{}, fmt.Errorf("OpenOpts.DoltBinPath is required for backend %q", opts.Backend)
		}
	case BackendExternal:
		if opts.LogFilePath == "" {
			return Endpoint{}, fmt.Errorf("OpenOpts.LogFilePath is required for backend %q", opts.Backend)
		}
		if err := opts.External.Validate(); err != nil {
			return Endpoint{}, fmt.Errorf("OpenOpts.External: %w", err)
		}
	}
	deadline := time.Now().Add(openDeadline)

	timeout := time.NewTimer(openDeadline)
	defer timeout.Stop()
	poll := time.NewTicker(openPollInterval)
	defer poll.Stop()

	want := intendedUpstreamID(opts)

	var lastSpawnErr error
	for {
		if ep, pf, ok := readAndDial(rootDir); ok {
			if want != "" && pf.UpstreamID != "" && pf.UpstreamID != want {
				return Endpoint{}, &ErrUpstreamMismatch{
					RootDir: rootDir,
					Want:    want,
					Have:    pf.UpstreamID,
				}
			}
			return ep, nil
		}

		lock, err := util.TryLock(filepath.Join(rootDir, LockFileName))
		switch {
		case err == nil:
			var ep Endpoint
			if ep, lastSpawnErr = spawnAndHandoff(rootDir, opts, deadline, lock); lastSpawnErr == nil {
				return ep, nil
			}
		case !lockfile.IsLocked(err):
			return Endpoint{}, fmt.Errorf("probe proxy lock: %w", err)
		}

		select {
		case <-timeout.C:
			if lastSpawnErr != nil {
				return Endpoint{}, lastSpawnErr
			}
			return Endpoint{}, fmt.Errorf("timeout waiting for proxy on %s", rootDir)
		case <-poll.C:
		}
	}
}

func spawnAndHandoff(rootDir string, opts OpenOpts, deadline time.Time, lock *util.Lock) (Endpoint, error) {
	handedOff := false
	defer func() {
		if !handedOff {
			lock.Unlock()
		}
	}()

	// Stale pidfile from a previous (now-dead) proxy must not mislead racing
	// readers into dialing a port that nobody is listening on.
	_ = pidfile.Remove(rootDir, PIDFileName)

	// Probe the proxy-child flock: if held, a previous proxy-child is still
	// alive and has an orphaned dolt sql-server we must kill before
	// respawning. If we can acquire it, no proxy-child is running — release
	// immediately so the child we are about to spawn can take it.
	if l, err := util.TryLock(filepath.Join(rootDir, server.LockFileName)); err == nil {
		l.Unlock()
	} else if lockfile.IsLocked(err) {
		if pf, perr := pidfile.Read(rootDir, server.PIDFileName); perr == nil && pf != nil {
			if proc, ferr := os.FindProcess(pf.Pid); ferr == nil {
				_ = proc.Kill()
			}
			_ = pidfile.Remove(rootDir, server.PIDFileName)
		}
	}

	port := opts.Port
	if port == 0 {
		var err error
		if port, err = PickFreePort(); err != nil {
			return Endpoint{}, fmt.Errorf("pick port: %w", err)
		}
	}

	handedOff = true
	cmd, done, err := forkExecChild(rootDir, opts, port, lock)
	if err != nil {
		return Endpoint{}, fmt.Errorf("fork child: %w", err)
	}

	hard := time.NewTimer(spawnReadyHardTimeout)
	defer hard.Stop()
	poll := time.NewTicker(openPollInterval)
	defer poll.Stop()

	for {
		if ep, _, ok := readAndDial(rootDir); ok {
			return ep, nil
		}
		select {
		case <-done:
			return Endpoint{}, fmt.Errorf("proxy child on port %d exited before becoming ready (likely lost lock race)", port)
		case <-hard.C:
			_ = cmd.Process.Kill()
			return Endpoint{}, fmt.Errorf("hard timeout (%s) waiting for proxy on port %d", spawnReadyHardTimeout, port)
		case <-poll.C:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return Endpoint{}, fmt.Errorf("timeout waiting for proxy to become ready on port %d", port)
		}
	}
}

func forkExecChild(rootDir string, opts OpenOpts, port int, lock *util.Lock) (*exec.Cmd, <-chan struct{}, error) {
	released := false
	defer func() {
		if !released {
			lock.Unlock()
		}
	}()

	self, err := ResolveExecutable()
	if err != nil {
		return nil, nil, fmt.Errorf("locate bd executable: %w", err)
	}

	idleTimeout := opts.IdleTimeout
	if idleTimeout < 0 {
		idleTimeout = IdleTimeoutNever
	}

	args := []string{
		"db-proxy-child",
		"--root", rootDir,
		"--port", strconv.Itoa(port),
		"--idle-timeout", idleTimeout.String(),
		"--backend", string(opts.Backend),
	}
	if opts.ConfigFilePath != "" {
		args = append(args, "--config", opts.ConfigFilePath)
	}
	if opts.LogFilePath != "" {
		args = append(args, "--logpath", opts.LogFilePath)
	}
	if opts.DoltBinPath != "" {
		args = append(args, "--dolt-bin", opts.DoltBinPath)
	}
	if opts.Database != "" {
		args = append(args, "--database", opts.Database)
	}
	if opts.Backend == BackendExternal {
		ext := opts.External
		if ext.Host != "" {
			args = append(args, "--external-host", ext.Host)
		}
		if ext.Port != 0 {
			args = append(args, "--external-port", strconv.Itoa(ext.Port))
		}
		if ext.Socket != "" {
			args = append(args, "--external-socket-path", ext.Socket)
		}
		if ext.TLSRequired {
			args = append(args, "--external-tls")
		}
		if ext.TLSCert != "" {
			args = append(args, "--external-tls-cert-path", ext.TLSCert)
		}
		if ext.TLSKey != "" {
			args = append(args, "--external-tls-key-path", ext.TLSKey)
		}
		if ext.KeepAlivePeriod != 0 {
			args = append(args, "--external-keep-alive", ext.KeepAlivePeriod.String())
		}
	}

	logFile, err := os.OpenFile(opts.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: logFilePath is caller-derived (workspace path), not user-request input
	if err != nil {
		return nil, nil, fmt.Errorf("open log file %q: %w", opts.LogFilePath, err)
	}

	cmd := exec.Command(self, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = procAttrDetached()

	released = true
	lock.Unlock()

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, nil, fmt.Errorf("start proxy child: %w", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cmd.Wait()
		_ = logFile.Close()
	}()

	return cmd, done, nil
}

func IsRunning(rootDir string) (bool, int) {
	_, pf, ok := readAndDial(rootDir)
	if !ok {
		return false, 0
	}
	return true, pf.Pid
}

func readAndDial(rootDir string) (Endpoint, *pidfile.PidFile, bool) {
	pf, err := pidfile.Read(rootDir, PIDFileName)
	if err != nil || pf == nil {
		return Endpoint{}, nil, false
	}
	ep := Endpoint{Host: "127.0.0.1", Port: pf.Port}
	if !probePort(ep, 500*time.Millisecond) {
		return Endpoint{}, nil, false
	}
	return ep, pf, true
}

func probePort(ep Endpoint, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", ep.Address(), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
