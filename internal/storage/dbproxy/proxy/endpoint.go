package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/atomicfile"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/fdhygiene"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
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
	identityProbeTimeout  = 500 * time.Millisecond
	backendExitTimeout    = 5 * time.Second
	// quarantineRetention bounds forensic pidfile accumulation; successful
	// spawns sweep only records older than 30 days.
	quarantineRetention = 30 * 24 * time.Hour

	spawnMarkerFileName = "proxy.spawn"
	stopEpochFileName   = "proxy.stop-epoch"
)

var ResolveExecutable = os.Executable

var (
	verifyProcessIdentity = procid.Verify
	resolveRootIdentity   = identity.RootID
	readControlSecret     = identity.ReadSecret
)

// ErrUnverifiableProcess marks lifecycle operations that refuse to signal a
// process because its workspace-scoped identity cannot be verified. Callers
// may use errors.Is to offer a narrower recovery path without treating
// unrelated shutdown failures as identity failures.
var ErrUnverifiableProcess = errors.New("proxy process identity is unverifiable")

type unverifiableLifecycleError struct {
	message string
}

func (e *unverifiableLifecycleError) Error() string {
	return e.message
}

func (e *unverifiableLifecycleError) Unwrap() error {
	return ErrUnverifiableProcess
}

var stopEpochSequence atomic.Uint64

// beforeProxyChildStart is a deterministic test hook for the otherwise tiny
// release-lock-before-exec scheduling window. Production leaves it as a no-op.
var beforeProxyChildStart = func() {}

type adoptionStatus uint8

const (
	adoptionAdopted adoptionStatus = iota
	adoptionNoRecord
	adoptionStaleDead
	adoptionIdentityMismatch
	adoptionUnverifiable
	adoptionLegacy
	adoptionMalformed
	adoptionIOErr
)

func (s adoptionStatus) String() string {
	switch s {
	case adoptionAdopted:
		return "adopted"
	case adoptionNoRecord:
		return "no record"
	case adoptionStaleDead:
		return "stale dead"
	case adoptionIdentityMismatch:
		return "identity mismatch"
	case adoptionUnverifiable:
		return "identity unverifiable"
	case adoptionLegacy:
		return "legacy"
	case adoptionMalformed:
		return "malformed"
	case adoptionIOErr:
		return "I/O error"
	default:
		return fmt.Sprintf("unknown adoption status %d", s)
	}
}

type adoptionResult struct {
	status   adoptionStatus
	endpoint Endpoint
	pidfile  *pidfile.PidFile
	err      error
}

type spawnMarker struct {
	Schema      int    `json:"schema"`
	PID         int    `json:"pid"`
	Birth       string `json:"birth"`
	StopEpoch   string `json:"stop_epoch"`
	StartedUnix int64  `json:"started_unix"`
}

var errStartInterrupted = errors.New("proxy startup interrupted by concurrent shutdown")

func PickFreePort() (int, error) {
	// The managed proxy no longer uses this bind-close allocator: its child
	// binds port 0 and publishes the kernel-assigned port. The remaining
	// production caller allocates the Dolt config port; that race requires
	// the managed-config ownership/retry contract deferred to the PR-C RFC.
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
	stopEpoch, err := readStopEpoch(rootDir)
	if err != nil {
		return Endpoint{}, fmt.Errorf("read proxy stop epoch: %w", err)
	}
	deadline := time.Now().Add(openDeadline)

	timeout := time.NewTimer(openDeadline)
	defer timeout.Stop()
	poll := time.NewTicker(openPollInterval)
	defer poll.Stop()

	want := intendedUpstreamID(opts)

	var lastSpawnErr error
	for {
		discovery := readAndDial(rootDir)
		switch discovery.status {
		case adoptionAdopted:
			return adoptedEndpoint(rootDir, want, discovery)
		case adoptionIOErr:
			return Endpoint{}, fmt.Errorf("discover proxy from %s: %w", pidfile.Path(rootDir, PIDFileName), discovery.err)
		}

		lock, err := util.TryLock(filepath.Join(rootDir, LockFileName))
		switch {
		case err == nil:
			// Re-read under proxy.lock. Another opener may have replaced the
			// record after our lock-free discovery.
			discovery = readAndDial(rootDir)
			if discovery.status == adoptionAdopted {
				lock.Unlock()
				return adoptedEndpoint(rootDir, want, discovery)
			}
			if discovery.status == adoptionIOErr {
				lock.Unlock()
				return Endpoint{}, fmt.Errorf("discover proxy from %s under lock: %w", pidfile.Path(rootDir, PIDFileName), discovery.err)
			}

			markerActive, markerErr := inspectSpawnMarkerLocked(rootDir)
			if markerErr != nil {
				lock.Unlock()
				return Endpoint{}, markerErr
			}
			if markerActive {
				lock.Unlock()
				lastSpawnErr = nil
				// This break exits the switch only; the outer discovery loop
				// continues with its normal bounded poll.
				break
			}

			var ep Endpoint
			ep, lastSpawnErr = spawnAndHandoff(rootDir, opts, deadline, stopEpoch, lock, discovery)
			if lastSpawnErr == nil {
				return ep, nil
			}
			if errors.Is(lastSpawnErr, errStartInterrupted) {
				return Endpoint{}, lastSpawnErr
			}
		case !lockfile.IsLocked(err):
			return Endpoint{}, fmt.Errorf("probe proxy lock: %w", err)
		case discovery.status == adoptionLegacy:
			recordPath := pidfile.Path(rootDir, PIDFileName)
			return Endpoint{}, fmt.Errorf(
				"legacy proxy record %s is protected by held lock %s; stop the pre-upgrade proxy with the old bd binary or wait for its idle exit, then quarantine the record manually by renaming %s to %s.stale-<unix-timestamp> before retrying",
				recordPath,
				filepath.Join(rootDir, LockFileName),
				recordPath,
				recordPath,
			)
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

func adoptedEndpoint(rootDir, want string, discovery adoptionResult) (Endpoint, error) {
	if want != "" && discovery.pidfile.UpstreamID != "" && discovery.pidfile.UpstreamID != want {
		return Endpoint{}, &ErrUpstreamMismatch{
			RootDir: rootDir,
			Want:    want,
			Have:    discovery.pidfile.UpstreamID,
		}
	}
	return discovery.endpoint, nil
}

func spawnAndHandoff(
	rootDir string,
	opts OpenOpts,
	deadline time.Time,
	stopEpoch string,
	lock *util.Lock,
	discovery adoptionResult,
) (Endpoint, error) {
	handedOff := false
	defer func() {
		if !handedOff {
			lock.Unlock()
		}
	}()

	currentEpoch, err := readStopEpoch(rootDir)
	if err != nil {
		return Endpoint{}, fmt.Errorf("read proxy stop epoch under lock: %w", err)
	}
	if currentEpoch != stopEpoch {
		return Endpoint{}, fmt.Errorf("%w for %s", errStartInterrupted, rootDir)
	}

	if err := quarantineForSpawn(rootDir, discovery); err != nil {
		return Endpoint{}, err
	}
	if err := cleanupOrphanBackend(rootDir); err != nil {
		return Endpoint{}, err
	}

	handedOff = true
	child, err := forkExecChild(rootDir, opts, opts.Port, stopEpoch, lock)
	if err != nil {
		return Endpoint{}, fmt.Errorf("fork child: %w", err)
	}
	defer func() { _ = clearOwnSpawnMarker(rootDir, child.marker) }()
	defer func() {
		if child.handle != nil {
			_ = child.handle.Close()
		}
	}()

	hard := time.NewTimer(spawnReadyHardTimeout)
	defer hard.Stop()
	poll := time.NewTicker(openPollInterval)
	defer poll.Stop()

	for {
		discovered := readAndDial(rootDir)
		if discovered.status == adoptionAdopted {
			if err := sweepOldQuarantines(rootDir, time.Now()); err != nil {
				log.Printf("dbproxy: could not sweep old quarantined records in %s: %v", rootDir, err)
			}
			return discovered.endpoint, nil
		}
		if discovered.status == adoptionIOErr {
			return Endpoint{}, fmt.Errorf("discover spawned proxy: %w", discovered.err)
		}
		select {
		case childErr := <-child.done:
			if interrupted, ierr := stopEpochChanged(rootDir, stopEpoch); ierr != nil {
				return Endpoint{}, ierr
			} else if interrupted {
				return Endpoint{}, fmt.Errorf("%w for %s", errStartInterrupted, rootDir)
			}
			if childErr == nil {
				childErr = errors.New("child exited without reporting an error")
			}
			// A LockHeldExitCode exit is a lost spawn race, not a listen
			// failure; any other exit gets the child's log path so the real
			// error (listen, backend start, ...) is findable.
			var exitErr *exec.ExitError
			if errors.As(childErr, &exitErr) && exitErr.ExitCode() == LockHeldExitCode {
				return Endpoint{}, fmt.Errorf(
					"proxy child lost the proxy.lock spawn race for %s: %w",
					rootDir, childErr,
				)
			}
			if opts.Port != 0 {
				return Endpoint{}, fmt.Errorf(
					"proxy child exited before becoming ready on explicitly configured port %d (see %s): %w",
					opts.Port, opts.LogFilePath, childErr,
				)
			}
			return Endpoint{}, fmt.Errorf(
				"proxy child exited before publishing its OS-assigned port (see %s): %w",
				opts.LogFilePath, childErr,
			)
		case <-hard.C:
			if err := killSpawnedChild(child); err != nil {
				return Endpoint{}, fmt.Errorf("hard timeout waiting for proxy on %s; safe child kill failed: %w", describeSpawnPort(opts.Port), err)
			}
			return Endpoint{}, fmt.Errorf("hard timeout (%s) waiting for proxy on %s", spawnReadyHardTimeout, describeSpawnPort(opts.Port))
		case <-poll.C:
		}
		if time.Now().After(deadline) {
			if err := killSpawnedChild(child); err != nil {
				return Endpoint{}, fmt.Errorf("timeout waiting for proxy on %s; safe child kill failed: %w", describeSpawnPort(opts.Port), err)
			}
			return Endpoint{}, fmt.Errorf("timeout waiting for proxy to become ready on %s", describeSpawnPort(opts.Port))
		}
	}
}

// describeSpawnPort renders a requested spawn port for wait/timeout
// messages: 0 is the default OS-assigned path, not a literal "port 0".
func describeSpawnPort(port int) string {
	if port == 0 {
		return "its OS-assigned port"
	}
	return fmt.Sprintf("port %d", port)
}

type spawnedProxyChild struct {
	cmd    *exec.Cmd
	done   <-chan error
	handle *procid.Handle
	marker spawnMarker
}

func forkExecChild(rootDir string, opts OpenOpts, port int, stopEpoch string, lock *util.Lock) (*spawnedProxyChild, error) {
	released := false
	defer func() {
		if !released {
			lock.Unlock()
		}
	}()

	self, err := ResolveExecutable()
	if err != nil {
		return nil, fmt.Errorf("locate bd executable: %w", err)
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
		"--stop-epoch", stopEpoch,
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
		if ext.KeepAlivePeriod != 0 {
			args = append(args, "--external-keep-alive", ext.KeepAlivePeriod.String())
		}
	}

	logFile, err := os.OpenFile(opts.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: logFilePath is caller-derived (workspace path), not user-request input
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", opts.LogFilePath, err)
	}

	cmd := exec.Command(self, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = procAttrDetached()

	birth, err := procid.Capture(os.Getpid())
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("capture spawning process identity: %w", err)
	}
	marker := spawnMarker{
		Schema:      1,
		PID:         os.Getpid(),
		Birth:       string(birth),
		StopEpoch:   stopEpoch,
		StartedUnix: time.Now().Unix(),
	}
	if err := writeSpawnMarker(rootDir, marker); err != nil {
		_ = logFile.Close()
		return nil, err
	}

	// The marker is durable before proxy.lock is released. Shutdown treats a
	// matching live owner as an in-progress start and waits; the child removes
	// it only after acquiring proxy.lock. This closes the release-before-Start
	// window without making the child deadlock on the parent's flock.
	released = true
	lock.Unlock()
	beforeProxyChildStart()

	// GH#4634: same hazard as the direct sql-server spawn, one hop further
	// out. The proxy child is detached and long-lived, and it starts the
	// sql-server itself, so a caller's non-CLOEXEC descriptor would otherwise
	// be inherited twice over and pinned for the proxy's whole lifetime.
	if leaked := fdhygiene.MarkInheritedCloexec(); len(leaked) > 0 {
		// debug.Logf, not log.Printf: this fires in normal operation whenever
		// the caller's environment leaves any fd open, and the parent's stderr
		// may be parsed script output.
		debug.Logf("dbproxy: marked %d inherited fd(s) close-on-exec before starting proxy child: %v", len(leaked), leaked)
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = clearOwnSpawnMarker(rootDir, marker)
		return nil, fmt.Errorf("start proxy child: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		done <- waitErr
		close(done)
	}()

	// Theoretical race: the birth token is captured after Start, so the child
	// could exit and its PID be recycled before Capture runs, making the
	// handle describe the unrelated replacement. The window is a few
	// milliseconds against OS PID-reuse latency, and the OS has no primitive
	// to atomically capture identity at spawn, so this is accepted and
	// documented rather than defended.
	var handle *procid.Handle
	childBirth, captureErr := procid.Capture(cmd.Process.Pid)
	if captureErr == nil {
		handle, captureErr = procid.Open(cmd.Process.Pid, childBirth)
	}
	if captureErr != nil && !procid.IsProcessGone(captureErr) {
		log.Printf("dbproxy: could not open verified handle for newly spawned proxy pid %d: %v", cmd.Process.Pid, captureErr)
	}

	return &spawnedProxyChild{
		cmd:    cmd,
		done:   done,
		handle: handle,
		marker: marker,
	}, nil
}

func IsRunning(rootDir string) (bool, int) {
	discovery := readAndDial(rootDir)
	if discovery.status != adoptionAdopted {
		return false, 0
	}
	return true, discovery.pidfile.Pid
}

func readAndDial(rootDir string) adoptionResult {
	pf, err := pidfile.Read(rootDir, PIDFileName)
	if err != nil {
		if isMalformedPIDFileError(err) {
			return adoptionResult{status: adoptionMalformed, err: err}
		}
		return adoptionResult{status: adoptionIOErr, err: err}
	}
	if pf == nil {
		return adoptionResult{status: adoptionNoRecord}
	}
	if err := pf.ValidateV2(pidfile.KindProxy); err != nil {
		if errors.Is(err, pidfile.ErrLegacySchema) {
			return adoptionResult{status: adoptionLegacy, pidfile: pf, err: err}
		}
		return adoptionResult{status: adoptionMalformed, pidfile: pf, err: err}
	}

	matched, err := verifyProcessIdentity(pf.Pid, procid.Token(pf.Birth))
	if err != nil {
		// Discovery must not turn an identity-probe failure into a fatal
		// pidfile I/O error. In particular, Windows ERROR_ACCESS_DENIED on a
		// recycled PID belongs in this non-adopting path; proxy.lock still
		// gates quarantine and replacement.
		return adoptionResult{
			status:  adoptionUnverifiable,
			pidfile: pf,
			err:     fmt.Errorf("verify proxy pid %d: %w", pf.Pid, err),
		}
	}
	if !matched {
		return adoptionResult{status: adoptionStaleDead, pidfile: pf}
	}

	expectedRootID, err := resolveRootIdentity(rootDir)
	if err != nil {
		return adoptionResult{status: adoptionUnverifiable, pidfile: pf, err: err}
	}
	secret, err := readControlSecret(rootDir)
	if err != nil {
		return adoptionResult{status: adoptionUnverifiable, pidfile: pf, err: err}
	}
	reply, err := identity.Identify("127.0.0.1", pf.ControlPort, secret, identityProbeTimeout)
	if err != nil {
		return adoptionResult{status: adoptionIdentityMismatch, pidfile: pf, err: err}
	}
	// Accept schema v2 or newer, matching pidfile.ValidateV2's forward-compat
	// policy for records.
	if reply.Schema < pidfile.SchemaV2 ||
		reply.Role != pidfile.KindProxy ||
		reply.RootID != expectedRootID ||
		reply.RootID != pf.RootID ||
		reply.PID != pf.Pid ||
		reply.Birth != pf.Birth ||
		reply.DataPort != pf.Port ||
		reply.ControlPort != pf.ControlPort ||
		reply.UpstreamID != pf.UpstreamID {
		return adoptionResult{
			status:  adoptionIdentityMismatch,
			pidfile: pf,
			err:     errors.New("authenticated proxy identity does not match its pidfile or workspace"),
		}
	}

	ep := Endpoint{Host: "127.0.0.1", Port: pf.Port}
	if !probePort(ep, identityProbeTimeout) {
		return adoptionResult{
			status:  adoptionIdentityMismatch,
			pidfile: pf,
			err:     fmt.Errorf("authenticated proxy data port %d is not accepting connections", pf.Port),
		}
	}
	return adoptionResult{status: adoptionAdopted, endpoint: ep, pidfile: pf}
}

func probePort(ep Endpoint, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", ep.Address(), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func isMalformedPIDFileError(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr)
}

func quarantineForSpawn(rootDir string, discovery adoptionResult) error {
	switch discovery.status {
	case adoptionNoRecord:
		return nil
	case adoptionStaleDead:
		log.Printf("dbproxy: quarantining stale dead proxy record %s before restart", pidfile.Path(rootDir, PIDFileName))
	case adoptionIdentityMismatch:
		log.Printf(
			"dbproxy: refusing proxy identity at %s (%v); quarantining only its record and starting a fresh proxy",
			pidfile.Path(rootDir, PIDFileName), discovery.err,
		)
	case adoptionUnverifiable:
		log.Printf(
			"dbproxy: proxy identity at %s could not be verified (%v); quarantining only its record under proxy.lock and starting a fresh proxy",
			pidfile.Path(rootDir, PIDFileName), discovery.err,
		)
	case adoptionLegacy:
		log.Printf(
			"dbproxy: quarantining legacy proxy record %s; a pre-upgrade proxy may still be running and must be stopped with the old bd binary if it does not idle-exit",
			pidfile.Path(rootDir, PIDFileName),
		)
	case adoptionMalformed:
		log.Printf("dbproxy: quarantining malformed proxy record %s (%v) before restart", pidfile.Path(rootDir, PIDFileName), discovery.err)
	default:
		return fmt.Errorf("cannot spawn from proxy discovery status %s", discovery.status)
	}
	target, err := quarantineRecord(rootDir, PIDFileName, time.Now())
	if err != nil {
		return fmt.Errorf("quarantine proxy record: %w", err)
	}
	log.Printf("dbproxy: preserved proxy record as %s", target)
	return nil
}

func quarantineRecord(rootDir, name string, now time.Time) (string, error) {
	source := pidfile.Path(rootDir, name)
	for stamp := now.Unix(); ; stamp++ {
		target := filepath.Join(rootDir, name+".stale-"+strconv.FormatInt(stamp, 10))
		if _, err := os.Lstat(target); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect quarantine target %s: %w", target, err)
		}
		if err := os.Rename(source, target); err != nil {
			return "", fmt.Errorf("rename %s to %s: %w", source, target, err)
		}
		return target, nil
	}
}

func sweepOldQuarantines(rootDir string, now time.Time) error {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return err
	}
	cutoff := now.Add(-quarantineRetention).Unix()
	prefixes := []string{
		PIDFileName + ".stale-",
		server.PIDFileName + ".stale-",
	}
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		var stampText string
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry.Name(), prefix) {
				stampText = strings.TrimPrefix(entry.Name(), prefix)
				break
			}
		}
		if stampText == "" {
			continue
		}
		stamp, err := strconv.ParseInt(stampText, 10, 64)
		if err != nil || stamp >= cutoff {
			continue
		}
		if err := os.Remove(filepath.Join(rootDir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove expired quarantine %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(errs...)
}

func cleanupOrphanBackend(rootDir string) error {
	recordPath := pidfile.Path(rootDir, server.PIDFileName)
	pf, readErr := pidfile.Read(rootDir, server.PIDFileName)

	childLockPath := filepath.Join(rootDir, server.LockFileName)
	childLock, lockErr := util.TryLock(childLockPath)
	switch {
	case lockErr == nil:
		childLock.Unlock()
	case lockfile.IsLocked(lockErr):
		pid := 0
		if pf != nil {
			pid = pf.Pid
		}
		return fmt.Errorf(
			"refusing backend cleanup: %s is held while proxy lock is free (recorded pid %d at %s); stop the process holding the child lock or remove the stale lock owner before retrying",
			childLockPath, pid, recordPath,
		)
	default:
		return fmt.Errorf("probe backend lock %s: %w", childLockPath, lockErr)
	}

	if readErr != nil {
		if isMalformedPIDFileError(readErr) {
			return unverifiableProcessError(
				"backend cleanup",
				recordPath,
				0,
				readErr,
				unverifiableProcessChecks{},
			)
		}
		return fmt.Errorf("read backend record %s: %w", recordPath, readErr)
	}
	if pf == nil {
		return nil
	}
	if err := pf.ValidateV2(pidfile.KindDoltBackend); err != nil {
		dead, live, probeErr := probeUnverifiablePID(pf.Pid)
		if dead {
			if _, quarantineErr := quarantineRecord(rootDir, server.PIDFileName, time.Now()); quarantineErr != nil {
				return fmt.Errorf("quarantine dead unverifiable backend record: %w", quarantineErr)
			}
			return nil
		}
		if probeErr != nil {
			err = errors.Join(err, fmt.Errorf("probe recorded pid: %w", probeErr))
		}
		return unverifiableProcessError(
			"backend cleanup",
			recordPath,
			pf.Pid,
			err,
			unverifiableProcessChecks{
				LiveEstablished: live,
			},
		)
	}
	expectedRootID, err := identity.RootID(rootDir)
	if err != nil {
		return fmt.Errorf("resolve backend root identity: %w", err)
	}
	if pf.RootID != expectedRootID {
		return unverifiableProcessError(
			"backend cleanup",
			recordPath,
			pf.Pid,
			fmt.Errorf("root identity mismatch (record has %q, workspace has %q)", pf.RootID, expectedRootID),
			unverifiableProcessChecks{},
		)
	}

	handle, dead, err := openRecordedProcess(pf)
	if err != nil {
		return unverifiableProcessError(
			"backend cleanup",
			recordPath,
			pf.Pid,
			err,
			unverifiableProcessChecks{},
		)
	}
	if dead {
		if _, err := quarantineRecord(rootDir, server.PIDFileName, time.Now()); err != nil {
			return fmt.Errorf("quarantine dead backend record: %w", err)
		}
		return nil
	}
	if err := handle.Kill(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("kill verified orphan backend pid %d from %s: %w", pf.Pid, recordPath, err)
	}
	// Close before waiting: an open handle on Windows keeps the dead PID
	// allocated, so procid.Verify would keep matching it and the exit wait
	// below would always time out.
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close verified backend process handle for pid %d: %w", pf.Pid, err)
	}
	if err := waitForRecordedProcessExit(pf, backendExitTimeout); err != nil {
		return fmt.Errorf("wait for verified orphan backend pid %d: %w", pf.Pid, err)
	}
	if _, err := quarantineRecord(rootDir, server.PIDFileName, time.Now()); err != nil {
		return fmt.Errorf("quarantine orphan backend record: %w", err)
	}
	return nil
}

type unverifiableProcessChecks struct {
	LiveEstablished bool
	LegacyProxy     bool
}

func unverifiableProcessError(
	operation string,
	recordPath string,
	pid int,
	cause error,
	checks unverifiableProcessChecks,
) error {
	liveness := ""
	if checks.LiveEstablished {
		liveness = " live"
	}
	stopGuidance := "stop the recorded process with the binary that started it"
	if checks.LegacyProxy && checks.LiveEstablished {
		stopGuidance = "stop the pre-upgrade proxy with the old bd binary"
	}
	return &unverifiableLifecycleError{message: fmt.Sprintf(
		"%s refused for unverifiable%s process pid %d recorded at %s: %v; %s, then quarantine the record manually by renaming %s to %s.stale-<unix-timestamp> before retrying",
		operation,
		liveness,
		pid,
		recordPath,
		cause,
		stopGuidance,
		recordPath,
		recordPath,
	)}
}

func probeUnverifiablePID(pid int) (dead bool, live bool, err error) {
	if pid <= 0 {
		return false, false, errors.New("record has no valid pid")
	}
	_, err = procid.Capture(pid)
	if err == nil {
		return false, true, nil
	}
	if procid.IsProcessGone(err) {
		return true, false, nil
	}
	return false, false, err
}

func openRecordedProcess(pf *pidfile.PidFile) (*procid.Handle, bool, error) {
	handle, err := procid.Open(pf.Pid, procid.Token(pf.Birth))
	if err == nil {
		return handle, false, nil
	}
	if procid.IsProcessGone(err) {
		return nil, true, nil
	}

	current, captureErr := procid.Capture(pf.Pid)
	if captureErr != nil {
		if procid.IsProcessGone(captureErr) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("open process identity: %w (recheck: %v)", err, captureErr)
	}
	if current != procid.Token(pf.Birth) {
		// A different birth token proves the recorded process exited, even
		// when the numeric PID has since been recycled.
		return nil, true, nil
	}
	return nil, false, fmt.Errorf("open verified process handle: %w", err)
}

func waitForRecordedProcessExit(pf *pidfile.PidFile, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		matched, err := procid.Verify(pf.Pid, procid.Token(pf.Birth))
		if err != nil {
			return err
		}
		if !matched {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout (%s) waiting for process exit", timeout)
		}
		time.Sleep(shutdownConfirmPoll)
	}
}

func killSpawnedChild(child *spawnedProxyChild) error {
	select {
	case <-child.done:
		return nil
	default:
	}
	if child.handle == nil {
		// No verified handle could be opened at spawn time. The child is our
		// own un-reaped process, so its PID cannot have been recycled and a
		// direct kill is safe; the alternative is leaking a live proxy child.
		if err := child.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("kill spawned proxy child pid %d: %w", child.cmd.Process.Pid, err)
		}
	} else if err := child.handle.Kill(); err != nil {
		return err
	}
	timer := time.NewTimer(shutdownConfirmDeadline)
	defer timer.Stop()
	select {
	case <-child.done:
		return nil
	case <-timer.C:
		return fmt.Errorf("timeout (%s) waiting for pid %d", shutdownConfirmDeadline, child.cmd.Process.Pid)
	}
}

func readStopEpoch(rootDir string) (string, error) {
	path := filepath.Join(rootDir, stopEpochFileName)
	data, err := os.ReadFile(path) // #nosec G304 - path is a fixed control filename under the workspace proxy root
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func advanceStopEpoch(rootDir string) error {
	epoch := strconv.FormatInt(time.Now().UnixNano(), 10) +
		"-" + strconv.Itoa(os.Getpid()) +
		"-" + strconv.FormatUint(stopEpochSequence.Add(1), 10)
	if err := atomicfile.WriteFile(filepath.Join(rootDir, stopEpochFileName), []byte(epoch+"\n"), 0o600); err != nil {
		return err
	}
	return nil
}

func stopEpochChanged(rootDir, expected string) (bool, error) {
	current, err := readStopEpoch(rootDir)
	if err != nil {
		return false, fmt.Errorf("read proxy stop epoch: %w", err)
	}
	return current != expected, nil
}

func writeSpawnMarker(rootDir string, marker spawnMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(rootDir, spawnMarkerFileName), data, 0o600); err != nil {
		return fmt.Errorf("write spawn marker: %w", err)
	}
	return nil
}

func readSpawnMarker(rootDir string) (*spawnMarker, error) {
	path := filepath.Join(rootDir, spawnMarkerFileName)
	data, err := os.ReadFile(path) // #nosec G304 - path is a fixed control filename under the workspace proxy root
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var marker spawnMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if marker.Schema != 1 || marker.PID <= 0 || marker.Birth == "" {
		return nil, fmt.Errorf("invalid spawn marker %s", path)
	}
	return &marker, nil
}

func inspectSpawnMarkerLocked(rootDir string) (bool, error) {
	marker, err := readSpawnMarker(rootDir)
	if err != nil {
		return false, fmt.Errorf("inspect proxy spawn marker: %w", err)
	}
	if marker == nil {
		return false, nil
	}
	matched, err := procid.Verify(marker.PID, procid.Token(marker.Birth))
	if err != nil {
		return false, fmt.Errorf("verify proxy spawn owner pid %d: %w", marker.PID, err)
	}
	if matched {
		return true, nil
	}
	if err := os.Remove(filepath.Join(rootDir, spawnMarkerFileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("remove dead spawn marker: %w", err)
	}
	return false, nil
}

func clearSpawnMarkerAfterLock(rootDir string) error {
	err := os.Remove(filepath.Join(rootDir, spawnMarkerFileName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func clearOwnSpawnMarker(rootDir string, own spawnMarker) error {
	deadline := time.Now().Add(shutdownConfirmDeadline)
	for {
		marker, err := readSpawnMarker(rootDir)
		if err != nil || marker == nil {
			return err
		}
		if *marker != own {
			return nil
		}

		lock, err := util.TryLock(filepath.Join(rootDir, LockFileName))
		if err == nil {
			current, readErr := readSpawnMarker(rootDir)
			if readErr == nil && current != nil && *current == own {
				readErr = clearSpawnMarkerAfterLock(rootDir)
			}
			lock.Unlock()
			return readErr
		}
		if !lockfile.IsLocked(err) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout clearing spawn marker %s", filepath.Join(rootDir, spawnMarkerFileName))
		}
		time.Sleep(shutdownConfirmPoll)
	}
}
