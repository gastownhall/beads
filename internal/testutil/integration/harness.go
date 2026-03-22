//go:build integration && !windows

// Package integration provides shared test infrastructure for multi-process
// integration tests (GH#2763). It offers process lifecycle management,
// subprocess orchestration, and environment filtering for tests that validate
// real dolt server behavior under production-like concurrency.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ProcessRegistry tracks spawned processes and ensures cleanup via t.Cleanup.
// Each registered process gets SIGTERM → 5s grace → SIGKILL escalation.
type ProcessRegistry struct {
	mu    sync.Mutex
	procs map[int]*os.Process
	t     *testing.T
}

// NewProcessRegistry creates a registry that automatically kills all tracked
// processes when the test completes.
func NewProcessRegistry(t *testing.T) *ProcessRegistry {
	t.Helper()
	r := &ProcessRegistry{
		procs: make(map[int]*os.Process),
		t:     t,
	}
	t.Cleanup(r.killAll)
	return r
}

// Register adds a process to the registry for lifecycle tracking.
func (r *ProcessRegistry) Register(p *os.Process) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.procs[p.Pid] = p
}

// Deregister removes a process from the registry (e.g., after clean Stop).
func (r *ProcessRegistry) Deregister(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.procs, pid)
}

// killAll sends SIGTERM, waits 5s, then SIGKILL to all tracked processes.
func (r *ProcessRegistry) killAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for pid, p := range r.procs {
		r.t.Logf("ProcessRegistry: cleaning up PID %d", pid)
		if err := p.Signal(syscall.SIGTERM); err != nil {
			// Process may already be dead.
			continue
		}
		done := make(chan struct{})
		go func() {
			// Wait() only works for child processes. If it fails (ECHILD),
			// fall back to polling IsProcessAlive.
			if _, err := p.Wait(); err != nil {
				for i := 0; i < 50; i++ {
					if !IsProcessAlive(pid) {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			close(done)
		}()
		select {
		case <-done:
			// Clean exit after SIGTERM.
		case <-time.After(5 * time.Second):
			r.t.Logf("ProcessRegistry: PID %d did not exit after SIGTERM, sending SIGKILL", pid)
			_ = p.Signal(syscall.SIGKILL)
			<-done
		}
	}
	r.procs = make(map[int]*os.Process)
}

// SubprocessRunner builds a test binary once and spawns subprocesses from it.
// The binary is compiled with the integration build tag.
type SubprocessRunner struct {
	once     sync.Once
	testBin  string
	buildErr error
	modRoot  string
	pkg      string
	tags     string
}

// NewSubprocessRunner creates a runner that will compile the given package.
// modRoot is the Go module root, pkg is the package path (e.g., "./internal/storage/dolt/").
func NewSubprocessRunner(modRoot, pkg string) *SubprocessRunner {
	return &SubprocessRunner{
		modRoot: modRoot,
		pkg:     pkg,
		tags:    "integration",
	}
}

// Build compiles the test binary (once). Returns the path to the binary.
func (r *SubprocessRunner) Build(t *testing.T) string {
	t.Helper()
	r.once.Do(func() {
		r.testBin = filepath.Join(t.TempDir(), "integration.test")
		t.Logf("SubprocessRunner: building test binary: go test -tags %s -c -o %s %s", r.tags, r.testBin, r.pkg)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		build := exec.CommandContext(ctx, "go", "test",
			"-tags", r.tags,
			"-c",
			"-o", r.testBin,
			r.pkg,
		)
		build.Dir = r.modRoot
		build.Env = append(os.Environ(), "CGO_ENABLED=1")
		var stderr bytes.Buffer
		build.Stderr = &stderr
		if err := build.Run(); err != nil {
			r.buildErr = fmt.Errorf("failed to build test binary: %w\nstderr: %s", err, stderr.String())
		}
		if err := os.Chmod(r.testBin, 0700); err != nil {
			r.buildErr = fmt.Errorf("failed to chmod test binary: %w", err)
		}
	})
	if r.buildErr != nil {
		t.Fatalf("SubprocessRunner: %v", r.buildErr)
	}
	return r.testBin
}

// Spawn starts a subprocess running the given test helper function.
// The sentinel env var (e.g., BEADS_SCHEMA_INIT_HELPER=1) triggers the helper.
// Extra env vars are merged with the filtered base environment.
// Uses process groups for reliable cleanup.
func (r *SubprocessRunner) Spawn(ctx context.Context, t *testing.T, helperTest string, env map[string]string) *exec.Cmd {
	t.Helper()
	bin := r.Build(t)

	// Per-subprocess timeout (30s default, or context deadline).
	subCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(subCtx, bin, "-test.run=^"+helperTest+"$", "-test.v")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = FilterEnv(os.Environ())
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

// safeEnvKeys is an allowlist of environment variable prefixes that are safe
// to pass to test subprocesses.
var safeEnvKeys = []string{
	"PATH=",
	"HOME=",
	"TMPDIR=",
	"LANG=",
	"USER=",
	"SHELL=",
	"GOPATH=",
	"GOROOT=",
	"GOMODCACHE=",
	"GOCACHE=",
	"GOFLAGS=",
	"CGO_ENABLED=",
	"CGO_CFLAGS=",
	"CGO_LDFLAGS=",
	"CGO_CPPFLAGS=",
	"CC=",
	"CXX=",
	"PKG_CONFIG_PATH=",
	"LD_LIBRARY_PATH=",
	"DYLD_LIBRARY_PATH=",
	"BEADS_", // All BEADS_ test control vars.
	"DOLT_",  // Dolt configuration.
	"TZ=",
	"LC_",
	"XDG_",
	"TERM=",
}

// FilterEnv returns a filtered copy of the environment, keeping only safe
// variables. This prevents leaking credentials to test subprocesses.
func FilterEnv(environ []string) []string {
	var filtered []string
	for _, e := range environ {
		for _, prefix := range safeEnvKeys {
			if strings.HasPrefix(e, prefix) {
				filtered = append(filtered, e)
				break
			}
		}
	}
	return filtered
}

// RequireDolt skips the test if the dolt binary is not available on PATH.
func RequireDolt(t *testing.T) string {
	t.Helper()
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		t.Skip("dolt binary not found on PATH, skipping integration test")
	}
	return doltPath
}

// InitDoltDir initializes a fresh dolt data directory in dir.
// Returns the path to the dolt database directory.
func InitDoltDir(t *testing.T, dir string) string {
	t.Helper()
	RequireDolt(t)

	doltDir := filepath.Join(dir, "dolt")
	if err := os.MkdirAll(doltDir, 0700); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	cmd := exec.Command("dolt", "init")
	cmd.Dir = doltDir
	cmd.Env = append(os.Environ(),
		"DOLT_ROOT_PATH="+dir,
		"HOME="+dir,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("dolt init failed: %v\nstderr: %s", err, stderr.String())
	}
	return doltDir
}

// ModuleRoot finds the Go module root by searching for go.mod.
func ModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod in any parent directory")
		}
		dir = parent
	}
}

// IsProcessAlive checks if a process with the given PID exists and is alive.
func IsProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// WaitForPort polls until a TCP connection to host:port succeeds or timeout expires.
func WaitForPort(t *testing.T, host string, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("WaitForPort: %s not reachable after %v", addr, timeout)
}

// ReadPIDFile reads and parses a PID file, returning 0 if missing or corrupt.
func ReadPIDFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// ReadPortFile reads and parses a port file, returning 0 if missing or corrupt.
func ReadPortFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return port
}
