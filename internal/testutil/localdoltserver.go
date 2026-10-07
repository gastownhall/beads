//go:build !windows

package testutil

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// EnvDoltServerBackend selects where the test Dolt SQL server comes from:
//
//	container  the dolthub/dolt-sql-server image through testcontainers
//	           (needs a docker daemon and the pulled image)
//	local      `dolt sql-server` from the pinned dolt CLI, started by the test
//	           process itself (no docker; works under remote execution)
//
// Unset means container, under plain `go test` and `bazel test` alike. A
// Bazel action has no docker daemon, so there an unset target keeps the
// pre-existing "Dolt unavailable, skipping" behavior; only targets (or
// invocations, via --test_env) that set local explicitly start servers from
// the pinned CLI that tools/bazel/test_env.sh puts on PATH. Defaulting to
// local under Bazel would light up every Dolt suite that no lane runs with a
// server today in every non-ci `bazel test`.
const EnvDoltServerBackend = "BEADS_TEST_DOLT_SERVER"

// EnvDoltServerGOMAXPROCS caps the CPUs a local test server uses. The
// container jobs run on 4-vCPU GitHub runners; a remote worker runs many test
// actions side by side, and an uncapped Go server sizes itself for the whole
// machine.
const EnvDoltServerGOMAXPROCS = "BEADS_TEST_DOLT_SERVER_GOMAXPROCS"

const defaultLocalServerGOMAXPROCS = "4"

// EnvDoltServerVerbose, set to "1", makes the local backend report each
// server's port and startup time on stderr.
const EnvDoltServerVerbose = "BEADS_TEST_DOLT_SERVER_VERBOSE"

// useLocalDoltServer reports whether the local backend is selected. An
// unrecognized BEADS_TEST_DOLT_SERVER value is reported by checkDolt (see
// doltBackendErr) rather than guessed at.
func useLocalDoltServer() bool {
	return os.Getenv(EnvDoltServerBackend) == "local"
}

// doltBackendErr rejects a BEADS_TEST_DOLT_SERVER value that names neither
// backend: a typo must not silently fall back to one of them.
func doltBackendErr() error {
	switch v := os.Getenv(EnvDoltServerBackend); v {
	case "", "local", "container":
		return nil
	default:
		return fmt.Errorf("%s=%q: want \"local\" or \"container\"", EnvDoltServerBackend, v)
	}
}

// localServerBase is where local servers keep their state. It is captured at
// package init, before any TestMain runs PinSuiteTempRoot (which re-points
// TMPDIR): a server under a suite root would be reported, and killed, as a
// leaked fixture by doltserver.SweepSuiteTestServers. The container backend
// kept its data outside the host filesystem entirely; this keeps it outside
// every suite root.
var localServerBase = os.TempDir()

// pinnedDoltVersion is the version in DoltDockerImage's tag: the local
// backend must run exactly the release the container would.
var pinnedDoltVersion = func() string {
	_, tag, _ := strings.Cut(DoltDockerImage, ":")
	return tag
}()

var (
	localDoltBinOnce sync.Once
	localDoltBin     string
	localDoltBinErr  error
)

var doltVersionRe = regexp.MustCompile(`(?m)^dolt version (\S+)`)

// resolveLocalDoltBinary finds the dolt CLI (BEADS_TEST_DOLT_BINARY, then
// PATH) and checks it is the pinned release.
func resolveLocalDoltBinary() (string, error) {
	localDoltBinOnce.Do(func() {
		sweepDeadLocalServerRoots(localServerBase, processAlive)
		bin := os.Getenv("BEADS_TEST_DOLT_BINARY")
		if bin == "" {
			p, err := exec.LookPath("dolt")
			if err != nil {
				localDoltBinErr = fmt.Errorf("dolt CLI not found: %w", err)
				return
			}
			bin = p
		}
		got, err := probeDoltVersion(bin, localServerBase)
		if err != nil {
			localDoltBinErr = err
			return
		}
		if got != pinnedDoltVersion {
			localDoltBinErr = fmt.Errorf("dolt CLI %s is version %s, want %s (the %s image the container backend runs)",
				bin, got, pinnedDoltVersion, DoltDockerImage)
			return
		}
		localDoltBin = bin
	})
	return localDoltBin, localDoltBinErr
}

// doltNoEventFlushEnv stops every dolt command from forking a detached
// `dolt send-metrics` child on exit. The *.disabled config keys below stop
// the network calls but not that child, which outlives its parent, re-creates
// <HOME>/.dolt after the owner removed it, and races terminate's RemoveAll.
const doltNoEventFlushEnv = "DOLT_DISABLE_EVENT_FLUSH=1"

// probeDoltVersion runs `bin version` with a throwaway HOME under base (so
// the user's ~/.dolt is neither read nor written) and returns the version.
func probeDoltVersion(bin, base string) (string, error) {
	home, err := os.MkdirTemp(base, "bdt-doltver-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(home) }()
	_ = writeLocalRootOwner(home)
	if err := writeLocalDoltGlobalConfig(home); err != nil {
		return "", err
	}
	cmd := exec.Command(bin, "version") // #nosec G204 G702 -- test-only; bin is the pinned dolt CLI
	cmd.Env = []string{"HOME=" + home, "DOLT_ROOT_PATH=" + home, "PATH=" + os.Getenv("PATH"), doltNoEventFlushEnv}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s version: %w: %s", bin, err, out)
	}
	m := doltVersionRe.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("%s version: unrecognized output %q", bin, out)
	}
	return string(m[1]), nil
}

// writeLocalDoltGlobalConfig writes the server's dolt global config. The
// image ships an empty one ({}); the two keys here only turn off usage
// events and the release check, which are network calls, not behavior.
func writeLocalDoltGlobalConfig(root string) error {
	dir := filepath.Join(root, ".dolt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config_global.json"),
		[]byte(`{"metrics.disabled":"true","versioncheck.disabled":"true"}`+"\n"), 0o600)
}

// localDoltServer is one `dolt sql-server` owned by this test process.
type localDoltServer struct {
	bin     string
	root    string // owned tree: data/, home/, sfp/, server.yaml, server.log, owner.pid
	dataDir string
	logPath string

	mu      sync.Mutex
	port    int
	cmd     *exec.Cmd
	done    chan struct{} // closed when cmd exits
	waitErr error
}

const (
	localServerStartAttempts = 5
	localServerStopTimeout   = 10 * time.Second
)

// startLocalDoltServer creates a fresh data directory and starts a server on
// it, provisioned like the container: root@% with no password, database
// beads_test, user test/test with USAGE and ALL ON beads_test.*.
func startLocalDoltServer() (*localDoltServer, error) {
	began := time.Now()
	s, err := newLocalDoltServer()
	if err != nil {
		return nil, err
	}
	if err := s.start(0); err != nil {
		_ = os.RemoveAll(s.root)
		return nil, err
	}
	if err := s.provision(); err != nil {
		s.terminate()
		return nil, err
	}
	if os.Getenv(EnvDoltServerVerbose) == "1" {
		fmt.Fprintf(os.Stderr, "testutil: local dolt sql-server on 127.0.0.1:%d (state %s) ready+provisioned in %s\n",
			s.Port(), s.root, time.Since(began).Round(time.Millisecond))
	}
	return s, nil
}

// newLocalDoltServer creates the state tree of a server that is not started
// yet: data/ (the server's working directory, a multi-database root like the
// image's /var/lib/dolt), home/ (its HOME and DOLT_ROOT_PATH), sfp/ (its
// secure_file_priv), server.log and the owner marker.
func newLocalDoltServer() (*localDoltServer, error) {
	bin, err := resolveLocalDoltBinary()
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(localServerBase, "bdt-dolt-")
	if err != nil {
		return nil, err
	}
	s := &localDoltServer{
		bin:     bin,
		root:    root,
		dataDir: filepath.Join(root, "data"),
		logPath: filepath.Join(root, "server.log"),
	}
	if err := writeLocalRootOwner(root); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	for _, d := range []string{s.dataDir, s.secureFilePrivDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}
	if err := writeLocalDoltGlobalConfig(filepath.Join(root, "home")); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return s, nil
}

// start launches the server on preferPort, or on a fresh loopback port when
// preferPort is 0 or taken. dolt sql-server rejects port 0, so the port is
// picked here and the rare bind race (another process takes it between the
// probe and dolt's bind) is retried with a new one.
func (s *localDoltServer) start(preferPort int) error {
	var lastErr error
	for attempt := 0; attempt < localServerStartAttempts; attempt++ {
		port := preferPort
		if attempt > 0 || port == 0 {
			p, err := FindFreePort()
			if err != nil {
				return err
			}
			port = p
		}
		if port == 3307 { // productionPortReasons Rule 1: never the default port
			continue
		}
		err := s.launch(port)
		if err == nil {
			return nil
		}
		lastErr = err
		if !errors.Is(err, errLocalServerBind) {
			return err
		}
	}
	return fmt.Errorf("dolt sql-server did not start after %d attempts: %w", localServerStartAttempts, lastErr)
}

var errLocalServerBind = errors.New("port already in use")

func (s *localDoltServer) launch(port int) error {
	logf, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	logStart, _ := logf.Seek(0, io.SeekEnd)
	gomaxprocs := os.Getenv(EnvDoltServerGOMAXPROCS)
	if gomaxprocs == "" {
		gomaxprocs = defaultLocalServerGOMAXPROCS
	}
	home := filepath.Join(s.root, "home")
	cfg, err := s.writeServerConfig(port)
	if err != nil {
		_ = logf.Close()
		return err
	}
	// #nosec G204 -- test-only, pinned binary, loopback listener
	cmd := exec.Command(s.bin, "sql-server", "--config", cfg)
	cmd.Dir = s.dataDir
	// A clean environment, like the container's: nothing from the test
	// (BEADS_*, DOLT_* overrides, the wrapper's dolt identity) reaches the
	// server. DOLT_ROOT_HOST=% creates root@% on first start, as
	// testcontainers asks the image to.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"DOLT_ROOT_PATH=" + home,
		"DOLT_ROOT_HOST=%",
		"TZ=UTC",
		"GOMAXPROCS=" + gomaxprocs,
		doltNoEventFlushEnv,
	}
	cmd.Stdout = logf
	cmd.Stderr = logf
	// Same process group as the test: `bazel test` (test-setup.sh) kills the
	// group when the test exits or times out, which reaps a server that every
	// cleanup missed. Pdeathsig covers plain `go test`.
	cmd.SysProcAttr = localServerSysProcAttr()
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return fmt.Errorf("start dolt sql-server: %w", err)
	}
	done := make(chan struct{})
	s.mu.Lock()
	s.cmd, s.done, s.port, s.waitErr = cmd, done, port, nil
	s.mu.Unlock()
	go func() {
		err := cmd.Wait()
		_ = logf.Close()
		s.mu.Lock()
		s.waitErr = err
		s.mu.Unlock()
		close(done)
	}()

	if err := s.waitReady(port, done, logStart); err != nil {
		s.kill()
		return err
	}
	return nil
}

func (s *localDoltServer) secureFilePrivDir() string { return filepath.Join(s.root, "sfp") }

// writeServerConfig writes the server's --config file for port. dolt ignores
// every other flag when --config is given, so it carries the listener too.
// Its only other setting is secure_file_priv: the image runs with "" (any
// path), which inside a container reaches only the container's filesystem,
// but a local server runs as the test user on the host, where passwordless
// root@% plus LOAD_FILE / INTO OUTFILE would read and write the user's
// files from any process that can reach the port. Confining it to sfp/ is the
// one deliberate difference from the container (TestDoltServerFingerprint
// normalizes it).
func (s *localDoltServer) writeServerConfig(port int) (string, error) {
	path := filepath.Join(s.root, "server.yaml")
	cfg := fmt.Sprintf("listener:\n  host: 127.0.0.1\n  port: %d\nsystem_variables:\n  secure_file_priv: %s\n",
		port, strconv.Quote(s.secureFilePrivDir()))
	return path, os.WriteFile(path, []byte(cfg), 0o600)
}

// localServerReadyLine is what dolt sql-server logs once its listener is
// bound (the line testcontainers' dolt module waits for, too).
const localServerReadyLine = "Server ready. Accepting connections."

// waitReady waits until this process has logged that it is listening and
// answers a bounded ping (see waitForDoltReady), and notices an early exit,
// classifying a bind failure so start can retry on another port. The log line
// is what makes the ping trustworthy: if another process owns the port, dolt
// fails to bind and exits, but a ping could still reach the other listener.
func (s *localDoltServer) waitReady(port int, done chan struct{}, logStart int64) error {
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port)
	deadline := time.Now().Add(serverStartTimeout)
	lastErr := errors.New("server has not logged " + strconv.Quote(localServerReadyLine))
	for time.Now().Before(deadline) {
		select {
		case <-done:
			tail := s.logSince(logStart)
			// dolt 2.2.0 checks the port itself ("Port N already in use.");
			// a bind that loses the race after that check reports the
			// kernel's "address already in use".
			if strings.Contains(tail, "already in use") {
				return fmt.Errorf("%w: port %d: %s", errLocalServerBind, port, tail)
			}
			return fmt.Errorf("dolt sql-server exited during startup on port %d: %s", port, tail)
		default:
		}
		if bytes.Contains(s.readLogSince(logStart), []byte(localServerReadyLine)) {
			if lastErr = pingDoltOnce(dsn); lastErr == nil {
				return nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("dolt sql-server on port %d not query-ready within %s: %v; log: %s",
		port, serverStartTimeout, lastErr, s.logSince(logStart))
}

// readLogSince returns the server log from byte offset off on.
func (s *localDoltServer) readLogSince(off int64) []byte {
	b, err := os.ReadFile(s.logPath)
	if err != nil || int64(len(b)) < off {
		return nil
	}
	return b[off:]
}

// logSince returns the last 4 KiB of the server log from byte offset off on,
// for error messages.
func (s *localDoltServer) logSince(off int64) string {
	b := bytes.TrimSpace(s.readLogSince(off))
	if len(b) > 4096 {
		b = b[len(b)-4096:]
	}
	return string(b)
}

// provision replays what testcontainers' dolt module and the image
// entrypoint do after the server is up.
func (s *localDoltServer) provision() error {
	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/", s.Port()))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, stmt := range []string{
		"CREATE DATABASE IF NOT EXISTS `beads_test`",
		"CREATE USER IF NOT EXISTS 'test'@'%' IDENTIFIED BY 'test'",
		"GRANT USAGE ON *.* TO 'test'@'%'",
		"GRANT ALL ON `beads_test`.* TO 'test'@'%'",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("provision local dolt server: %s: %w", stmt, err)
		}
	}
	return nil
}

// Port returns the current listening port.
func (s *localDoltServer) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// exited reports whether the server process has exited.
func (s *localDoltServer) exited() (bool, error) {
	s.mu.Lock()
	done, werr := s.done, s.waitErr
	s.mu.Unlock()
	if done == nil {
		return true, nil
	}
	select {
	case <-done:
		s.mu.Lock()
		werr = s.waitErr
		s.mu.Unlock()
		return true, werr
	default:
		return false, nil
	}
}

// stop asks the server to exit (SIGTERM, then SIGKILL after
// localServerStopTimeout) and keeps its data directory.
func (s *localDoltServer) stop() error {
	s.mu.Lock()
	cmd, done := s.cmd, s.done
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		return nil
	case <-time.After(localServerStopTimeout):
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("dolt sql-server (pid %d) ignored SIGTERM for %s; killed", cmd.Process.Pid, localServerStopTimeout)
	}
}

func (s *localDoltServer) kill() {
	s.mu.Lock()
	cmd, done := s.cmd, s.done
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	<-done
}

// restart starts the server again on its existing data directory, on the
// same port when it is still free.
func (s *localDoltServer) restart() error {
	if gone, _ := s.exited(); !gone {
		return fmt.Errorf("dolt sql-server is still running")
	}
	return s.start(s.Port())
}

// terminate stops the server and removes everything it owned.
func (s *localDoltServer) terminate() {
	_ = s.stop()
	_ = os.RemoveAll(s.root)
}

// execInDataDir runs cmd on the host with the server's data directory as its
// working directory: the local counterpart of exec'ing into the container,
// whose working directory is its data directory (/var/lib/dolt).
func (s *localDoltServer) execInDataDir(ctx context.Context, argv []string) (int, string, error) {
	if len(argv) == 0 {
		return 0, "", fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- test-only
	cmd.Dir = s.dataDir
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), string(out), nil
	}
	if err != nil {
		return 0, "", err
	}
	return 0, string(out), nil
}

// Local state roots are removed by terminate, but nothing runs it when the
// test binary dies (panic, -timeout, SIGKILL) or when a package's TestMain
// never calls TerminateDoltContainer: the container backend has Ryuk for
// that, the local backend has these. Every root records its owning PID, and
// the first use of the backend in a later process removes the roots whose
// owner is gone (the doltserver.SweepDeadSuiteRoots rule, copied because
// doltserver's own tests import this package).
const (
	localRootOwnerFile   = "owner.pid"
	localServerRootGlob  = "bdt-dolt-*"
	localVersionRootGlob = "bdt-doltver-*"
)

func writeLocalRootOwner(root string) error {
	return os.WriteFile(filepath.Join(root, localRootOwnerFile), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

// readLocalRootOwner returns the PID in root's owner marker; ok is false
// when there is none or it does not hold a PID, which means "owner unknown",
// never "owner dead".
func readLocalRootOwner(root string) (pid int, ok bool) {
	b, err := os.ReadFile(filepath.Join(root, localRootOwnerFile)) // #nosec G304 -- fixed name under a root this user owns
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive reports whether pid names a running process (EPERM: it
// exists but belongs to someone else, so it is alive too).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// sweepDeadLocalServerRoots removes base's local-backend state roots whose
// owner marker names a dead process and returns them. A root without a
// marker (a pre-marker leftover, or one whose owner is still between
// MkdirTemp and writing it), with a live owner, not a real directory, or not
// owned by this user is left alone. Only directories are reclaimed: on
// Linux Pdeathsig already killed the dead owner's servers; elsewhere a
// surviving server keeps serving its deleted directory until it is killed.
func sweepDeadLocalServerRoots(base string, alive func(int) bool) []string {
	var swept []string
	for _, glob := range []string{localServerRootGlob, localVersionRootGlob} {
		matches, _ := filepath.Glob(filepath.Join(base, glob))
		for _, root := range matches {
			info, err := os.Lstat(root)
			if err != nil || !info.IsDir() {
				continue
			}
			if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
				continue
			}
			pid, ok := readLocalRootOwner(root)
			if !ok || alive(pid) {
				continue
			}
			if os.RemoveAll(root) == nil {
				swept = append(swept, root)
			}
		}
	}
	if len(swept) > 0 && os.Getenv(EnvDoltServerVerbose) == "1" {
		fmt.Fprintf(os.Stderr, "testutil: removed %d local dolt server state dir(s) of dead test processes: %v\n", len(swept), swept)
	}
	return swept
}
