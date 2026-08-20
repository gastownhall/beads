//go:build !windows

package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql" // required by testcontainers Dolt module
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
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
	doltServerOnce        sync.Once
	doltServerErr         error
	doltTestPort          string
	doltSingletonSrv      *doltServer
	doltTerminateOnce     sync.Once
	dockerOnce            sync.Once
	dockerAvail           bool
	doltCheckOnce         sync.Once
	doltCached            doltReadiness
	doltGitRemoteMountDir string
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

	// Bind-mount a dedicated host directory into the container at the
	// identical path. dolt-sql-server (in the container) and the test
	// process (on the host) can then both reach files under this path by
	// the same string — e.g. a git remote one side provisions and the
	// other side clones/pushes to via CLI. Mounting a distinctly-named
	// leaf dir, rather than the tmp root itself, means this can never
	// shadow whatever the container's own /tmp is used for internally.
	mountDir := filepath.Join(os.TempDir(), "beads-dolt-git-remote-mount")
	if err := os.MkdirAll(mountDir, 0o755); err != nil {
		return fmt.Errorf("creating git-remote mount dir: %w", err)
	}
	doltGitRemoteMountDir = mountDir

	ctr, err := dolt.Run(ctx, DoltDockerImage,
		dolt.WithDatabase("beads_test"),
		// Docker port-forwarding makes connections appear as non-localhost
		// (e.g., 172.17.0.1). The entrypoint defaults DOLT_ROOT_HOST to
		// "localhost", so root@localhost won't match external connections.
		// Set to "%" so root can connect from any host.
		testcontainers.WithEnv(map[string]string{"DOLT_ROOT_HOST": "%"}),
		// testcontainers.WithMounts+BindMount is not used here: this
		// version's mapToDockerMounts() only logs a warning and drops
		// BindOptions for bind-type sources, so there is no way to carry
		// an SELinux relabel through that typed path. Under rootless
		// Podman with SELinux enforcing (as on this rig), the container
		// gets "permission denied" on an otherwise-0755, correctly-owned
		// bind-mounted host dir unless the mount is relabeled — hence the
		// raw HostConfig.Binds entry with the classic ":Z" (private
		// relabel) suffix, verified by manual `podman run -v ...:Z` repro.
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, mountDir+":"+mountDir+":Z")
		}),
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
	doltSingletonSrv = &doltServer{
		container: ctr,
	}

	return nil
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

// StartIsolatedDoltContainer starts a per-test Dolt container and returns the
// mapped host port. The container is terminated automatically when the test finishes.
func StartIsolatedDoltContainer(t *testing.T) string {
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

	portStr := port.Port()
	t.Setenv("BEADS_DOLT_PORT", portStr)
	t.Setenv("BEADS_DOLT_SERVER_PORT", portStr)
	return portStr
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

// DoltContainerHandle returns the shared Dolt container so tests can Exec
// commands inside it — e.g. to provision a bare git remote that the
// dolt-sql-server process (not the test process) must be able to reach.
// Callers must call RequireDoltContainer(t) first, same as the other
// DoltContainer* accessors.
func DoltContainerHandle() testcontainers.Container {
	return doltSingletonSrv.container
}

// DoltContainerGitRemoteMountDir returns a host directory that is
// bind-mounted into the shared Dolt container at the identical path, so
// files created under it are visible to both the test process (host) and
// the dolt-sql-server process (container) by the same path string. Use
// this — not an arbitrary container-only or host-only tmp dir — for any
// fixture (like a git remote) that both sides must independently reach.
// Callers must call RequireDoltContainer(t) first, same as the other
// DoltContainer* accessors.
func DoltContainerGitRemoteMountDir() string {
	return doltGitRemoteMountDir
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
