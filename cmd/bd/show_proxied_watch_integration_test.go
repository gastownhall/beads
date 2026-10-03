//go:build cgo

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// watchBannerWriter buffers a stream and signals every time the watch banner
// is printed, so a test can wait for the Nth render instead of sleeping.
type watchBannerWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	count   int
	renders chan int
}

const showWatchBanner = "Watching for changes..."

func (w *watchBannerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if now := strings.Count(w.buf.String(), showWatchBanner); now > w.count {
		w.count = now
		select {
		case w.renders <- now:
		default:
		}
	}
	return n, err
}

func (w *watchBannerWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitForRender blocks until the banner has been printed at least want times,
// giving up when the process exits or the timeout elapses.
func (w *watchBannerWriter) waitForRender(want int, timeout time.Duration, exited <-chan struct{}) bool {
	deadline := time.After(timeout)
	for {
		w.mu.Lock()
		got := w.count
		w.mu.Unlock()
		if got >= want {
			return true
		}
		select {
		case <-w.renders:
		case <-exited:
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.count >= want
		case <-deadline:
			return false
		}
	}
}

// pipeCapture hands a child one end of a pipe as its stdout and lets the test
// read, on demand, exactly what the child has written so far. A plain buffer
// behind exec's copy goroutine cannot give that: stdout and stderr are copied
// by two independent goroutines, so a write the child made to stdout before
// the banner can reach the test after the banner does.
type pipeCapture struct {
	r   *os.File
	buf bytes.Buffer
}

// newPipeCapture returns the capture and the write end to set as cmd.Stdout.
// The caller closes the write end once the child has started.
func newPipeCapture(t *testing.T) (*pipeCapture, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &pipeCapture{r: r}, w
}

// drain moves every byte already in the pipe into the buffer and returns the
// whole capture. A read attempts the syscall before it consults the deadline,
// so bytes the child has written are never left behind; the deadline only ends
// the wait once the pipe is empty.
func (c *pipeCapture) drain(t *testing.T) string {
	t.Helper()
	chunk := make([]byte, 4096)
	for {
		if err := c.r.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			t.Fatalf("stdout pipe deadline: %v", err)
		}
		n, err := c.r.Read(chunk)
		c.buf.Write(chunk[:n])
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) {
				return c.buf.String()
			}
			t.Fatalf("read stdout pipe: %v", err)
		}
	}
}

// TestProxiedServerShowWatch pins `bd show --watch` under --proxied-server to
// the direct route's contract: render once, redraw when the issue's
// status/updated_at snapshot changes, and stop cleanly on SIGINT. Proxied mode
// used to refuse it outright (proxy.watch.unsupported), so every consumer on
// the default proxied transport had to reimplement the poll itself.
func TestProxiedServerShowWatch(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("redraws_on_change_and_stops_on_interrupt", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swr")
		issue := bdProxiedCreate(t, bd, p.dir, "Watch me", "--type", "task")

		var stdout bytes.Buffer
		stderr := &watchBannerWriter{renders: make(chan int, 1)}
		cmd := exec.Command(bd, "show", issue.ID, "--watch")
		cmd.Dir = p.dir
		cmd.Env = bdProxiedEnv(p.dir)
		cmd.Stdout = &stdout
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start bd show --watch: %v", err)
		}
		var waitErr error
		exited := make(chan struct{})
		go func() {
			waitErr = cmd.Wait()
			close(exited)
		}()
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			<-exited
		})

		if !stderr.waitForRender(1, 60*time.Second, exited) {
			t.Fatalf("bd show --watch never started watching\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), "not supported") {
			t.Fatalf("bd show --watch refused under --proxied-server:\n%s", stderr.String())
		}

		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--status", "in_progress")

		if !stderr.waitForRender(2, 60*time.Second, exited) {
			t.Fatalf("bd show --watch did not redraw after the status change\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}

		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatalf("signal bd show --watch: %v", err)
		}
		select {
		case <-exited:
			if waitErr != nil {
				t.Fatalf("bd show --watch exited with %v after SIGINT, want 0\nstderr:\n%s", waitErr, stderr.String())
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("bd show --watch ignored SIGINT\nstderr:\n%s", stderr.String())
		}
		if !strings.Contains(stderr.String(), "Stopped watching.") {
			t.Errorf("stderr lacks the stop line:\n%s", stderr.String())
		}

		out := stdout.String()
		if got := strings.Count(out, "Watch me"); got < 2 {
			t.Errorf("stdout rendered the issue %d time(s), want an initial render and a redraw:\n%s", got, out)
		}
		if !strings.Contains(strings.ToUpper(out), "IN_PROGRESS") {
			t.Errorf("redraw does not show the new status:\n%s", out)
		}
	})

	// A poll that fails keeps the last render and prints nothing: the route
	// used to print "Error refreshing <id>" every 2s once the issue was
	// deleted, and under --json an error object on stdout each time.
	t.Run("deleted_issue_polls_stay_quiet", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swd")
		issue := bdProxiedCreate(t, bd, p.dir, "Watch then delete", "--type", "task")

		// The child writes stdout straight into a pipe the test drains itself.
		// The initial render is written before the banner, so once the banner
		// is seen a drain returns all of it, and "before" cannot be taken
		// mid-render.
		stdout, stdoutW := newPipeCapture(t)
		stderr := &watchBannerWriter{renders: make(chan int, 1)}
		cmd := exec.Command(bd, "--json", "show", issue.ID, "--watch")
		cmd.Dir = p.dir
		cmd.Env = bdProxiedEnv(p.dir)
		cmd.Stdout = stdoutW
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			_ = stdoutW.Close()
			t.Fatalf("start bd show --watch: %v", err)
		}
		_ = stdoutW.Close()
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			<-exited
		})

		if !stderr.waitForRender(1, 60*time.Second, exited) {
			t.Fatalf("bd show --watch never started watching\nstdout:\n%s\nstderr:\n%s", stdout.drain(t), stderr.String())
		}
		before := len(stdout.drain(t))
		bdProxiedDelete(t, bd, p.dir, issue.ID, "--force")

		// Several poll intervals, so a per-tick report would have fired.
		select {
		case <-exited:
			t.Fatalf("bd show --watch exited after the issue was deleted\nstderr:\n%s", stderr.String())
		case <-time.After(3 * showWatchPollInterval):
		}
		if after := stdout.drain(t)[before:]; after != "" {
			t.Errorf("stdout grew after the delete:\n%s", after)
		}
		if strings.Contains(stderr.String(), "Error") || strings.Contains(stderr.String(), "not found") {
			t.Errorf("stderr reported the failed polls:\n%s", stderr.String())
		}
	})

	t.Run("missing_id_fails_instead_of_watching", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swm")
		stdout, stderr, err, timedOut := bdProxiedRunDeadline(t, bd, p.dir, 60*time.Second, "show", "swm-nonexistent999", "--watch")
		if timedOut {
			t.Fatalf("bd show --watch on a missing id kept watching\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if err == nil {
			t.Fatalf("bd show --watch on a missing id exited 0\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if want := "Issue swm-nonexistent999 not found"; !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	})

	t.Run("requires_exactly_one_id", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swo")
		a := bdProxiedCreate(t, bd, p.dir, "Watch A", "--type", "task")
		b := bdProxiedCreate(t, bd, p.dir, "Watch B", "--type", "task")
		stdout, stderr := bdProxiedShowFail(t, bd, p.dir, a.ID, b.ID, "--watch")
		if combined := stdout + stderr; !strings.Contains(combined, "watch mode requires exactly one issue ID") {
			t.Errorf("want the direct route's one-id error, got:\n%s", combined)
		}
	})
}
