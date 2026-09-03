//go:build !windows

package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql" // required by testcontainers Dolt module
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/dolt"
)

// doltServer represents a running test Dolt container instance.
type doltServer struct {
	container *dolt.DoltContainer
}

// serverStartTimeout is the max time to wait for the test Dolt server to accept connections.
const serverStartTimeout = 60 * time.Second

// Module-level singleton state.
var (
	doltServerOnce    sync.Once
	doltServerErr     error
	doltTestPort      string
	doltSingletonSrv  *doltServer
	doltTerminateOnce sync.Once
	dockerOnce        sync.Once
	dockerAvail       bool
	doltCheckOnce     sync.Once
	doltCached        doltReadiness
)

// doltReadiness describes why Dolt integration tests can or cannot run.
type doltReadiness int

// doltDockerRepo is the repository portion of DoltDockerImage (without the tag).
var doltDockerRepo, _, _ = strings.Cut(DoltDockerImage, ":")

const (
	doltNoDocker     doltReadiness = iota // Docker daemon not reachable
	doltNoImage                           // no Dolt image at all
	doltWrongVersion                      // image exists but wrong tag
	doltSkipped                           // explicit opt-out via BEADS_TEST_SKIP
	doltReady                             // ready to start containers
)

func (d doltReadiness) String() string {
	switch d {
	case doltNoDocker:
		return "Docker not available"
	case doltNoImage:
		return fmt.Sprintf("Docker image %s not cached locally (run 'docker pull %s')", DoltDockerImage, DoltDockerImage)
	case doltWrongVersion:
		return fmt.Sprintf("Docker image %s cached but wrong version (run 'docker pull %s')", doltDockerRepo, DoltDockerImage)
	case doltSkipped:
		return "Dolt tests skipped (BEADS_TEST_SKIP=dolt)"
	case doltReady:
		return "Dolt ready"
	default:
		return fmt.Sprintf("unknown dolt readiness state: %d", int(d))
	}
}

// isDockerAvailable returns true if the Docker daemon is reachable.
// The result is cached after the first call.
func isDockerAvailable() bool {
	dockerOnce.Do(func() {
		dockerAvail = exec.Command("docker", "info").Run() == nil
	})
	return dockerAvail
}

// hasTestSkip returns true if the given service appears in the BEADS_TEST_SKIP
// env var (comma-separated list). Example: BEADS_TEST_SKIP=dolt,slow
func hasTestSkip(service string) bool {
	val := os.Getenv("BEADS_TEST_SKIP")
	if val == "" {
		return false
	}
	for _, s := range strings.Split(val, ",") {
		if strings.TrimSpace(s) == service {
			return true
		}
	}
	return false
}

// checkDolt returns the readiness state for Dolt integration tests.
// It composes hasTestSkip, isDockerAvailable, isDoltImageCached, and
// isDoltRepoImageCached, caching the result.
func checkDolt() doltReadiness {
	doltCheckOnce.Do(func() {
		// Explicit skip checked first to avoid ~1s docker info cost.
		if hasTestSkip("dolt") {
			doltCached = doltSkipped
			return
		}
		if !isDockerAvailable() {
			return // doltCached zero value is doltNoDocker
		}
		if isDoltImageCached() {
			doltCached = doltReady
			return
		}
		if isDoltRepoImageCached() {
			doltCached = doltWrongVersion
			return
		}
		doltCached = doltNoImage
	})
	return doltCached
}

// isDoltImageCached returns true if the exact Dolt Docker image (repo:tag)
// is available locally, avoiding unnecessary network calls to Docker Hub.
func isDoltImageCached() bool {
	return exec.Command("docker", "image", "inspect", DoltDockerImage).Run() == nil
}

// isDoltRepoImageCached returns true if ANY version of the Dolt image repo
// exists locally (e.g. dolthub/dolt-sql-server with a different tag).
func isDoltRepoImageCached() bool {
	out, err := exec.Command("docker", "images", doltDockerRepo, "-q").Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// startDoltContainer starts the singleton Dolt container.
func startDoltContainer() error {
	ctx, cancel := context.WithTimeout(context.Background(), serverStartTimeout)
	defer cancel()

	ctr, err := dolt.Run(ctx, DoltDockerImage,
		dolt.WithDatabase("beads_test"),
		// Docker port-forwarding makes connections appear as non-localhost
		// (e.g., 172.17.0.1). The entrypoint defaults DOLT_ROOT_HOST to
		// "localhost", so root@localhost won't match external connections.
		// Set to "%" so root can connect from any host.
		testcontainers.WithEnv(map[string]string{"DOLT_ROOT_HOST": "%"}),
	)
	if err != nil {
		return fmt.Errorf("starting Dolt container: %w", err)
	}

	p, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		return fmt.Errorf("getting mapped port: %w", err)
	}

	if _, err := strconv.Atoi(p.Port()); err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		return fmt.Errorf("parsing port %q: %w", p.Port(), err)
	}

	doltTestPort = p.Port()

	if err := waitForDoltReady(doltTestPort); err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		return fmt.Errorf("waiting for Dolt server to be query-ready: %w", err)
	}

	doltSingletonSrv = &doltServer{
		container: ctr,
	}

	return nil
}

// doltReadyProbeTimeout bounds how long waitForDoltReady polls before giving up.
const doltReadyProbeTimeout = 30 * time.Second

// waitForDoltReady polls the server with a trivial query until it responds
// or doltReadyProbeTimeout elapses.
//
// The testcontainers wait strategy above only matches a log line ("Server
// ready. Accepting connections."), which confirms the TCP listener is up but
// not that Dolt's SQL engine can actually serve a query yet. In that narrow
// startup window, a query issued with an unbounded context (as several test
// helpers do) can block indefinitely instead of erroring, because nothing
// ever cancels it to unstick the read — confirmed via goroutine dump during
// be-fgd round-2 triage: the connection sat in mysqlConn.readWithTimeout /
// net.Read for 2+ minutes after the container had already logged ready. Each
// probe attempt here uses its own short bounded context, so a still-warming
// server fails an attempt fast and gets retried, rather than wedging.
func waitForDoltReady(port string) error {
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%s)/", port)
	deadline := time.Now().Add(doltReadyProbeTimeout)
	var lastErr error
	for {
		lastErr = pingDoltOnce(dsn)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dolt server at port %s did not become query-ready within %s: %w", port, doltReadyProbeTimeout, lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// pingDoltOnce opens a short-lived connection and pings it with a bounded
// context so a non-responsive server fails this attempt instead of hanging.
func pingDoltOnce(dsn string) error {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return db.PingContext(ctx)
}

// terminateSharedContainer stops and removes the shared Dolt container.
// Safe to call concurrently or multiple times (sync.Once).
func terminateSharedContainer() {
	doltTerminateOnce.Do(func() {
		if doltSingletonSrv != nil && doltSingletonSrv.container != nil {
			_ = testcontainers.TerminateContainer(doltSingletonSrv.container)
			doltSingletonSrv.container = nil
		}
	})
}

// IsolatedDoltContainer is a per-test Dolt container together with the
// accessors a test needs to inspect it from the inside.
type IsolatedDoltContainer struct {
	// Port is the mapped host port of the container's Dolt server.
	Port string

	ctr *dolt.DoltContainer
}

// Exec runs cmd inside the container and returns its exit code and combined
// output, demultiplexed (see containerExec).
func (c *IsolatedDoltContainer) Exec(ctx context.Context, cmd []string) (int, string, error) {
	if c == nil || c.ctr == nil {
		return 0, "", fmt.Errorf("no Dolt container running")
	}
	return containerExec(ctx, c.ctr, cmd)
}

// StartIsolatedDoltContainerHandle starts a per-test Dolt container and
// returns a handle to it. The container is terminated automatically when the
// test finishes.
//
// Unlike StartIsolatedDoltContainer this does NOT touch BEADS_DOLT_PORT or
// BEADS_DOLT_SERVER_PORT: those are process-wide, so a test that only wants
// its own server should not perturb sibling tests sharing the process.
func StartIsolatedDoltContainerHandle(t *testing.T) *IsolatedDoltContainer {
	t.Helper()
	if state := checkDolt(); state != doltReady {
		t.Skipf("skipping test: %s", state)
	}

	ctx, cancel := context.WithTimeout(context.Background(), serverStartTimeout)
	defer cancel()
	ctr, err := dolt.Run(ctx, DoltDockerImage,
		dolt.WithDatabase("beads_test"),
		testcontainers.WithEnv(map[string]string{"DOLT_ROOT_HOST": "%"}),
	)
	if err != nil {
		t.Fatalf("starting Dolt container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			t.Logf("terminating Dolt container: %v", err)
		}
	})

	port, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatalf("getting mapped port: %v", err)
	}

	return &IsolatedDoltContainer{Port: port.Port(), ctr: ctr}
}

// StartIsolatedDoltContainer starts a per-test Dolt container and returns the
// mapped host port, additionally pointing BEADS_DOLT_PORT and
// BEADS_DOLT_SERVER_PORT at it for the duration of the test.
func StartIsolatedDoltContainer(t *testing.T) string {
	t.Helper()
	c := StartIsolatedDoltContainerHandle(t)
	t.Setenv("BEADS_DOLT_PORT", c.Port)
	t.Setenv("BEADS_DOLT_SERVER_PORT", c.Port)
	return c.Port
}

// ensureSharedContainer starts the singleton container and sets
// BEADS_DOLT_PORT and BEADS_DOLT_SERVER_PORT.
func ensureSharedContainer() {
	doltServerOnce.Do(func() {
		doltServerErr = startDoltContainer()
		if doltServerErr == nil && doltTestPort != "" {
			if err := os.Setenv("BEADS_DOLT_PORT", doltTestPort); err != nil {
				doltServerErr = fmt.Errorf("set BEADS_DOLT_PORT: %w", err)
			} else if err := os.Setenv("BEADS_DOLT_SERVER_PORT", doltTestPort); err != nil {
				doltServerErr = fmt.Errorf("set BEADS_DOLT_SERVER_PORT: %w", err)
			}
		}
	})
}

// EnsureDoltContainerForTestMain starts a shared Dolt container for use in
// TestMain functions. Call TerminateDoltContainer() after m.Run() to clean up.
// Sets BEADS_DOLT_PORT and BEADS_DOLT_SERVER_PORT process-wide.
func EnsureDoltContainerForTestMain() error {
	if state := checkDolt(); state != doltReady {
		return fmt.Errorf("%s", state)
	}

	ensureSharedContainer()
	return doltServerErr
}

// RequireDoltContainer ensures a shared Dolt container is running. Skips the
// test if Docker is not available.
func RequireDoltContainer(t *testing.T) {
	t.Helper()
	if state := checkDolt(); state != doltReady {
		t.Skipf("skipping test: %s", state)
	}

	ensureSharedContainer()
	if doltServerErr != nil {
		t.Fatalf("Dolt container setup failed: %v", doltServerErr)
	}
}

// DoltContainerAddr returns the address (host:port) of the Dolt container.
func DoltContainerAddr() string {
	return "127.0.0.1:" + doltTestPort
}

// DoltContainerPort returns the mapped host port of the Dolt container.
func DoltContainerPort() string {
	return doltTestPort
}

// DoltContainerPortInt returns the mapped host port as an int.
func DoltContainerPortInt() int {
	p, _ := strconv.Atoi(doltTestPort)
	return p
}

// TerminateDoltContainer stops and removes the shared Dolt container.
// Called from TestMain after m.Run().
func TerminateDoltContainer() {
	terminateSharedContainer()
}

// DoltContainerCrashed returns true if the shared container has exited unexpectedly.
// Returns false if no container was started.
func DoltContainerCrashed() bool {
	if doltSingletonSrv == nil || doltSingletonSrv.container == nil {
		return false
	}
	state, err := doltSingletonSrv.container.State(context.Background())
	if err != nil {
		return true // can't check state — assume crashed
	}
	return !state.Running
}

// DoltContainerCrashError returns an error if the shared container has exited
// unexpectedly, nil otherwise.
func DoltContainerCrashError() error {
	if doltSingletonSrv == nil || doltSingletonSrv.container == nil {
		return nil
	}
	state, err := doltSingletonSrv.container.State(context.Background())
	if err != nil {
		return fmt.Errorf("failed to check container state: %w", err)
	}
	if !state.Running {
		return fmt.Errorf("Dolt container exited (status=%s, exit=%d)", state.Status, state.ExitCode)
	}
	return nil
}

// containerExec runs cmd inside ctr and returns its exit code and combined
// output. Used by tests that need to inspect a container's filesystem (e.g.
// the .dolt_dropped_databases/ directory), which has no host-visible path
// since the containers are started without a bind-mounted data dir.
//
// tcexec.Multiplexed() is load-bearing, not decorative. Without it Exec hands
// back the raw hijacked Docker stream and io.ReadAll glues the 8-byte stdcopy
// frame header onto the payload: `echo hello` returns
// "\x01\x00\x00\x00\x00\x00\x00\x06hello\n". A caller that feeds such
// a string back into the container — a path read out of find(1), say — then
// fails with "invalid argument" on a prefix it cannot see.
func containerExec(ctx context.Context, ctr *dolt.DoltContainer, cmd []string) (int, string, error) {
	code, reader, err := ctr.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return 0, "", fmt.Errorf("exec in Dolt container: %w", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		return code, "", fmt.Errorf("reading exec output: %w", err)
	}
	return code, string(out), nil
}
