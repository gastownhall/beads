//go:build !windows

// Package execgracetest provides a controlled fake dolt for tests of graceful
// command cancellation. The fake is a bash script written to a temporary
// directory: it answers `dolt version`, prints "ready" on stdout once its
// signal disposition is in place, and then behaves as the chosen Mode.
package execgracetest

import (
	"bytes"
	"errors"
	"fmt"
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

// Mode selects how the fake dolt reacts to cancellation.
type Mode int

const (
	// Cooperative starts a helper in its own process group (standing in for
	// dolt's detached git transport) and, on SIGTERM, terminates and reaps
	// that helper before writing a cleanup marker and exiting. A SIGKILL
	// skips all of that and leaves the helper running.
	Cooperative Mode = iota
	// IgnoresTerm ignores SIGTERM, so only a forced kill stops it.
	IgnoresTerm
	// PipeHolder starts a descendant that inherits and holds the output
	// pipes, then stops on SIGTERM itself.
	PipeHolder
)

// Child is one fake dolt and the state it reports through its directory.
type Child struct {
	// BinDir holds the executable named dolt.
	BinDir string
	state  string
	out    *readyWriter
}

// WriteFakeDolt writes a fake dolt for mode that reports version to
// `dolt version`, and registers cleanup that kills any helper or descendant
// it started, so a failing test leaves no process behind. Prepend BinDir to
// PATH (before building the command) to have exec resolve "dolt" to it.
func WriteFakeDolt(t testing.TB, mode Mode, version string) *Child {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("the fake dolt is a bash script; bash is not on PATH")
	}
	c := &Child{BinDir: t.TempDir(), state: t.TempDir(), out: newReadyWriter()}
	t.Cleanup(c.killLeftovers)

	var body string
	switch mode {
	case Cooperative:
		body = fmt.Sprintf(`set -m
sleep 300 </dev/null >/dev/null 2>&1 &
helper=$!
set +m
echo "$helper" > %[1]s
trap 'kill -TERM "$helper" 2>/dev/null; wait "$helper"; echo cleaned > %[2]s; exit 143' TERM
echo ready
wait "$helper"
`, shQuote(c.path("helper.pid")), shQuote(c.path("cleanup")))
	case IgnoresTerm:
		body = "trap '' TERM\necho ready\nexec sleep 300\n"
	case PipeHolder:
		body = fmt.Sprintf("sleep 300 &\necho $! > %s\necho ready\nexec sleep 300\n",
			shQuote(c.path("descendant.pid")))
	default:
		t.Fatalf("unknown mode %d", mode)
	}
	script := "#!/usr/bin/env bash\n" +
		"if [ \"$1\" = version ]; then echo " + shQuote("dolt version "+version) + "; exit 0; fi\n" +
		body
	if err := os.WriteFile(filepath.Join(c.BinDir, "dolt"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	return c
}

// Attach sends cmd's stdout and stderr to the child's ready detector. Call it
// before cmd.Start.
func (c *Child) Attach(cmd *exec.Cmd) {
	cmd.Stdout = c.out
	cmd.Stderr = c.out
}

// WaitReady blocks until the child has printed "ready", failing the test
// after timeout.
func (c *Child) WaitReady(t testing.TB, timeout time.Duration) {
	t.Helper()
	select {
	case <-c.out.ready:
	case <-time.After(timeout):
		t.Fatalf("fake dolt never became ready; output: %q", c.out.String())
	}
}

// CleanedUp reports whether the cooperative child ran its SIGTERM cleanup.
func (c *Child) CleanedUp() bool {
	_, err := os.Stat(c.path("cleanup"))
	return err == nil
}

// HelperAlive reports whether the cooperative child's detached helper is
// still running (or not yet reaped).
func (c *Child) HelperAlive(t testing.TB) bool {
	t.Helper()
	pid, err := c.pid("helper.pid")
	if err != nil {
		t.Fatalf("helper pid: %v", err)
	}
	return syscall.Kill(pid, 0) == nil
}

func (c *Child) killLeftovers() {
	for _, name := range []string{"helper.pid", "descendant.pid"} {
		if pid, err := c.pid(name); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func (c *Child) pid(name string) (int, error) {
	data, err := os.ReadFile(c.path(name))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, errors.New("malformed pid file " + name)
	}
	return pid, nil
}

func (c *Child) path(name string) string { return filepath.Join(c.state, name) }

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// readyWriter collects output and closes ready once a "ready" line arrives.
type readyWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	once  sync.Once
	ready chan struct{}
}

func newReadyWriter() *readyWriter { return &readyWriter{ready: make(chan struct{})} }

func (w *readyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), "ready\n") {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *readyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// WaitBounded runs cmd.Wait and fails the test, after killing the child, if
// it outlasts limit.
func WaitBounded(t testing.TB, cmd *exec.Cmd, limit time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("Wait did not return within %s of cancellation", limit)
	}
}
