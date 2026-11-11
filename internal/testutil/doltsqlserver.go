package testutil

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// DoltSQLServerReadyTimeout is how long StartDoltSQLServer waits for one
// launch of `dolt sql-server` to accept connections.
const DoltSQLServerReadyTimeout = 15 * time.Second

// doltSQLServerStartAttempts bounds the fresh-port retries after a lost bind
// race.
const doltSQLServerStartAttempts = 5

// errDoltSQLServerBind marks a launch whose port was taken between
// FindFreePort and dolt's own bind.
var errDoltSQLServerBind = errors.New("port already in use")

// StartDoltSQLServer starts `dolt sql-server` on a loopback port with dataDir
// as its --data-dir, waits until it accepts connections, and returns the port.
// The server is killed when t ends.
//
// FindFreePort releases the port it probes, so another process can take it
// before dolt binds; dolt then exits at once. That early exit is detected and
// retried on a fresh port a bounded number of times. Any other failure (an
// exit for another reason, or no readiness within DoltSQLServerReadyTimeout)
// fails t with dolt's exit status and output instead of a bare timeout.
func StartDoltSQLServer(t testing.TB, dataDir string) int {
	t.Helper()
	srv, err := startDoltSQLServer(dataDir, FindFreePort, DoltSQLServerReadyTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.stop)
	return srv.port
}

// adHocDoltSQLServer is one running `dolt sql-server` child.
type adHocDoltSQLServer struct {
	port int
	cmd  *exec.Cmd
	done chan struct{}
}

func (s *adHocDoltSQLServer) stop() {
	_ = s.cmd.Process.Kill()
	<-s.done
}

func startDoltSQLServer(dataDir string, pickPort func() (int, error), readyTimeout time.Duration) (*adHocDoltSQLServer, error) {
	var errs []error
	for attempt := 1; attempt <= doltSQLServerStartAttempts; attempt++ {
		port, err := pickPort()
		if err != nil {
			return nil, fmt.Errorf("pick a port for dolt sql-server: %w", err)
		}
		if port == 3307 { // never the production default port
			continue
		}
		srv, err := launchDoltSQLServer(dataDir, port, readyTimeout)
		if err == nil {
			return srv, nil
		}
		errs = append(errs, fmt.Errorf("attempt %d: %w", attempt, err))
		if !errors.Is(err, errDoltSQLServerBind) {
			return nil, errors.Join(errs...)
		}
	}
	return nil, fmt.Errorf("dolt sql-server did not start after %d attempts: %w",
		doltSQLServerStartAttempts, errors.Join(errs...))
}

func launchDoltSQLServer(dataDir string, port int, readyTimeout time.Duration) (*adHocDoltSQLServer, error) {
	out := &lockedBuffer{}
	// #nosec G204 -- fixed args, loopback listener, test-only
	cmd := exec.Command("dolt", "sql-server", "-H", "127.0.0.1", "-P", strconv.Itoa(port), "--data-dir", dataDir)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start dolt sql-server: %w", err)
	}
	srv := &adHocDoltSQLServer{port: port, cmd: cmd, done: make(chan struct{})}
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(srv.done)
	}()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-srv.done:
			output := strings.TrimSpace(out.String())
			// dolt checks the port itself ("Port N already in use."); a bind
			// that loses the race after that check reports the kernel's
			// "address already in use".
			if strings.Contains(output, "already in use") {
				return nil, fmt.Errorf("%w: port %d: dolt sql-server exited (%v): %s",
					errDoltSQLServerBind, port, waitErr, output)
			}
			return nil, fmt.Errorf("dolt sql-server on port %d exited during startup (%v); output:\n%s",
				port, waitErr, output)
		default:
		}
		// Only dial once this process says it is listening: if another
		// process owns the port, a dial could reach that listener instead.
		if strings.Contains(out.String(), "Server ready. Accepting connections.") {
			// #nosec G704 -- addr is always loopback with a test-selected port.
			if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
				_ = conn.Close()
				return srv, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	srv.stop()
	return nil, fmt.Errorf("dolt sql-server on port %d did not become ready within %s; output:\n%s",
		port, readyTimeout, strings.TrimSpace(out.String()))
}

// lockedBuffer is a bytes.Buffer safe for exec's output copier and a reader.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
